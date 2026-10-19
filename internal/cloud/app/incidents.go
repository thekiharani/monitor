package app

import (
	"context"
	"encoding/json/v2"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/thekiharani/monitor/internal/cloud/config"
	"github.com/thekiharani/monitor/internal/cloud/dbx"
	"github.com/thekiharani/monitor/internal/cloud/notify"
)

const (
	confirmFailures        = 2
	confirmRetryDelay      = 30 * time.Second
	notificationBatch      = 20
	notificationAttempts   = 8
	notificationTimeout    = 20 * time.Second
	heartbeatSweepInterval = 15 * time.Second
)

type monitorSnapshot struct {
	ID, AccountID, Name, Kind, State string
	Workspace                        string
	Target                           *string
	IntervalSeconds, GraceSeconds    int
	CertWarnDays                     int
	CertExpiresAt, CertWarnedFor     *time.Time
}

func lockMonitor(ctx context.Context, tx pgx.Tx, id string) (monitorSnapshot, error) {
	var m monitorSnapshot
	err := tx.QueryRow(ctx, `
		SELECT m.id::text,m.account_id::text,m.name,m.kind,m.state,m.target,m.interval_seconds,m.grace_seconds,m.cert_warn_days,m.cert_expires_at,m.cert_warned_for,
			COALESCE(acc.name,'')
		FROM monitors m JOIN accounts acc ON acc.id=m.account_id
		WHERE m.id=$1 AND m.deleted_at IS NULL AND m.paused_at IS NULL FOR UPDATE OF m`, id).
		Scan(&m.ID, &m.AccountID, &m.Name, &m.Kind, &m.State, &m.Target, &m.IntervalSeconds, &m.GraceSeconds, &m.CertWarnDays, &m.CertExpiresAt, &m.CertWarnedFor, &m.Workspace)
	return m, err
}

func regionalVerdict(ctx context.Context, tx pgx.Tx, monitorID string) (failing bool, anyChecked bool, regionsDown []string, err error) {
	var total, failingCount, checked int
	err = tx.QueryRow(ctx, `
		SELECT count(*), count(*) FILTER (WHERE consecutive_failures >= $2), count(*) FILTER (WHERE last_checked_at IS NOT NULL),
			COALESCE(array_agg(region ORDER BY region) FILTER (WHERE consecutive_failures >= $2), '{}')
		FROM monitor_schedules WHERE monitor_id=$1`, monitorID, confirmFailures).Scan(&total, &failingCount, &checked, &regionsDown)
	if err != nil || total == 0 {
		return false, false, nil, err
	}
	threshold := max(1, (total+1)/2)
	return failingCount >= threshold, checked > 0, regionsDown, nil
}

func (a *App) transition(ctx context.Context, tx pgx.Tx, m monitorSnapshot, next, cause string, regionsDown []string, at time.Time) error {
	if next == m.State {
		return nil
	}
	if regionsDown == nil {
		regionsDown = []string{}
	}
	if _, err := tx.Exec(ctx, `UPDATE monitors SET state=$2,state_changed_at=$3 WHERE id=$1`, m.ID, next, at); err != nil {
		return err
	}
	switch {
	case next == "down":
		var incidentID string
		if err := tx.QueryRow(ctx, `
			INSERT INTO incidents(monitor_id,account_id,started_at,cause,regions_down) VALUES($1,$2,$3,$4,$5)
			ON CONFLICT (monitor_id) WHERE resolved_at IS NULL DO UPDATE SET cause=EXCLUDED.cause
			RETURNING id::text`, m.ID, m.AccountID, at, truncate(cause, 512), regionsDown).Scan(&incidentID); err != nil {
			return err
		}
		silenced, err := inMaintenance(ctx, tx, m)
		if err != nil || silenced {
			return err
		}
		return a.enqueueForMonitor(ctx, tx, m, notify.Event{
			Kind: notify.EventDown, IncidentID: incidentID, Cause: cause, RegionsDown: regionsDown, StartedAt: &at,
		})
	case m.State == "down":
		var incidentID string
		var started time.Time
		err := tx.QueryRow(ctx, `
			UPDATE incidents SET resolved_at=$2 WHERE monitor_id=$1 AND resolved_at IS NULL
			RETURNING id::text,started_at`, m.ID, at).Scan(&incidentID, &started)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		event := a.eventFor(m, notify.Event{Kind: notify.EventUp, IncidentID: incidentID, StartedAt: &started, ResolvedAt: &at})
		payload, err := json.Marshal(event)
		if err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `
			INSERT INTO notifications(account_id,channel_id,monitor_id,incident_id,event,payload)
			SELECT DISTINCT n.account_id,n.channel_id,n.monitor_id,n.incident_id,'up',$2::jsonb
			FROM notifications n JOIN alert_channels c ON c.id=n.channel_id AND c.deleted_at IS NULL AND c.enabled
			WHERE n.incident_id=$1 AND n.event='down'`, incidentID, payload)
		if err == nil {
			a.notifyAlerts()
		}
		return err
	}
	return nil
}

func inMaintenance(ctx context.Context, tx pgx.Tx, m monitorSnapshot) (bool, error) {
	var active bool
	err := tx.QueryRow(ctx, `
		SELECT EXISTS (SELECT 1 FROM maintenance_windows
			WHERE account_id=$1 AND deleted_at IS NULL AND now() >= starts_at AND now() < ends_at
			AND (cardinality(monitor_ids)=0 OR $2::uuid = ANY(monitor_ids)))`, m.AccountID, m.ID).Scan(&active)
	return active, err
}

func (a *App) eventFor(m monitorSnapshot, e notify.Event) notify.Event {
	e.MonitorID = m.ID
	e.MonitorName = m.Name
	e.Workspace = m.Workspace
	if m.Target != nil {
		e.Target = redactTarget(*m.Target)
	}
	e.DashboardURL = strings.TrimRight(a.cfg.DashboardOrigin, "/") + "/app/monitors/" + m.ID
	return e
}

func redactTarget(target string) string {
	if before, _, ok := strings.Cut(target, "?"); ok {
		return before
	}
	return target
}

func (a *App) enqueueForMonitor(ctx context.Context, tx pgx.Tx, m monitorSnapshot, e notify.Event) error {
	payload, err := json.Marshal(a.eventFor(m, e))
	if err != nil {
		return err
	}
	var incident *string
	if e.IncidentID != "" {
		incident = &e.IncidentID
	}
	tag, err := tx.Exec(ctx, `
		INSERT INTO notifications(account_id,channel_id,monitor_id,incident_id,event,payload)
		SELECT c.account_id,c.id,$2,$3,$4,$5::jsonb FROM alert_channels c
		WHERE c.account_id=$1 AND c.enabled AND c.deleted_at IS NULL
		  AND (c.all_monitors OR EXISTS (SELECT 1 FROM monitor_channels mc WHERE mc.channel_id=c.id AND mc.monitor_id=$2))`,
		m.AccountID, m.ID, incident, e.Kind, payload)
	if err == nil && tag.RowsAffected() > 0 {
		a.notifyAlerts()
	}
	return err
}

func (a *App) warnCertificate(ctx context.Context, tx pgx.Tx, m monitorSnapshot, expires *time.Time) error {
	if expires == nil || m.CertWarnDays == 0 || time.Until(*expires) > time.Duration(m.CertWarnDays)*24*time.Hour {
		return nil
	}
	if m.CertWarnedFor != nil && m.CertWarnedFor.Equal(*expires) {
		return nil
	}
	if _, err := tx.Exec(ctx, `UPDATE monitors SET cert_warned_for=$2 WHERE id=$1`, m.ID, *expires); err != nil {
		return err
	}
	return a.enqueueForMonitor(ctx, tx, m, notify.Event{Kind: notify.EventCertificate, CertExpiresAt: expires})
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

func (a *App) notifyAlerts() {
	select {
	case a.alertWake <- struct{}{}:
	default:
	}
}

func (a *App) alertWorker(ctx context.Context) {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
		case <-a.alertWake:
		case <-a.stop:
			return
		case <-ctx.Done():
			return
		}
		for a.deliverAlerts(ctx) == notificationBatch {
			if ctx.Err() != nil {
				return
			}
		}
	}
}

type pendingNotification struct {
	ID, AccountID, ChannelID, Kind, Event, ConfigSealed string
	IncidentID                                          *string
	Attempts                                            int
	Payload                                             notify.Event
}

func (a *App) deliverAlerts(parent context.Context) int {
	ctx, cancel := context.WithTimeout(parent, time.Minute)
	defer cancel()
	if _, err := a.db.Exec(ctx, `
		UPDATE notifications n SET attempts=$1, last_error=$2
		FROM alert_channels c
		WHERE c.id=n.channel_id AND n.sent_at IS NULL AND n.attempts<$1
		  AND (c.deleted_at IS NOT NULL OR (NOT c.enabled AND n.event<>'test'))`,
		notificationAttempts, "The channel was deleted or turned off before this could be delivered."); err != nil && parent.Err() == nil {
		a.logError("retiring alerts for removed channels failed", "alert_worker", "retire_notifications", err)
	}
	rows, err := a.db.Query(ctx, `
		WITH due AS (
			SELECT n.id FROM notifications n JOIN alert_channels c ON c.id=n.channel_id
			WHERE n.sent_at IS NULL AND n.next_attempt_at<=now() AND n.attempts<$1
			  AND c.deleted_at IS NULL AND (c.enabled OR n.event='test')
			ORDER BY n.next_attempt_at LIMIT $2 FOR UPDATE OF n SKIP LOCKED
		)
		UPDATE notifications n SET attempts=n.attempts+1,
			next_attempt_at=now()+make_interval(secs => least(3600, 15*power(2, n.attempts)))
		FROM due, alert_channels c
		WHERE n.id=due.id AND c.id=n.channel_id
		RETURNING n.id::text,n.account_id::text,n.channel_id::text,c.kind,n.event,c.config_encrypted,n.incident_id::text,n.attempts,n.payload`,
		notificationAttempts, notificationBatch)
	if err != nil {
		if parent.Err() == nil {
			a.logError("alert claim failed", "alert_worker", "claim_notifications", err)
		}
		return 0
	}
	var batch []pendingNotification
	for rows.Next() {
		var n pendingNotification
		if err = rows.Scan(&n.ID, &n.AccountID, &n.ChannelID, &n.Kind, &n.Event, &n.ConfigSealed, &n.IncidentID, &n.Attempts, &n.Payload); err != nil {
			rows.Close()
			a.logError("alert scan failed", "alert_worker", "scan_notification", err)
			return 0
		}
		batch = append(batch, n)
	}
	rows.Close()
	for _, n := range batch {
		a.deliverAlert(ctx, n)
	}
	return len(batch)
}

func (a *App) deliverAlert(ctx context.Context, n pendingNotification) {
	sendCtx, cancel := context.WithTimeout(ctx, notificationTimeout)
	defer cancel()
	delivery, err := a.sendAlert(sendCtx, n)
	if err != nil {
		message := truncate(err.Error(), 512)
		if _, dbErr := a.db.Exec(ctx, `UPDATE notifications SET last_error=$2 WHERE id=$1`, n.ID, message); dbErr != nil {
			a.logError("alert failure record failed", "alert_worker", "record_failure", dbErr, "notification_id", n.ID)
		}
		if _, dbErr := a.db.Exec(ctx, `UPDATE alert_channels SET last_error=$2,updated_at=now() WHERE id=$1`, n.ChannelID, message); dbErr != nil {
			a.logError("channel failure record failed", "alert_worker", "record_channel_failure", dbErr, "channel_id", n.ChannelID)
		}
		a.logWarn("alert delivery failed", "alert_worker", "send_notification", err, "notification_id", n.ID, "channel_kind", n.Kind, "attempts", n.Attempts)
		return
	}
	var messageID *string
	if delivery.MessageID != "" {
		messageID = &delivery.MessageID
	}
	if _, err = a.db.Exec(ctx, `UPDATE notifications SET sent_at=now(),last_error=NULL,provider_message_id=$2 WHERE id=$1`, n.ID, messageID); err != nil {
		a.logError("alert sent record failed", "alert_worker", "record_sent", err, "notification_id", n.ID)
	}
	if _, err = a.db.Exec(ctx, `UPDATE alert_channels SET last_delivered_at=now(),last_error=NULL WHERE id=$1`, n.ChannelID); err != nil {
		a.logError("channel delivery record failed", "alert_worker", "record_channel_delivery", err, "channel_id", n.ChannelID)
	}
}

type channelConfig struct {
	Emails     []string `json:"emails,omitempty"`
	ChatID     string   `json:"chat_id,omitempty"`
	TopicID    int64    `json:"topic_id,omitempty"`
	WebhookURL string   `json:"webhook_url,omitempty"`
	URL        string   `json:"url,omitempty"`
	Secret     string   `json:"secret,omitempty"`
}

func (a *App) openChannelConfig(sealed string) (channelConfig, error) {
	var cfg channelConfig
	plain, err := config.Open(a.cfg.AppEncryptionKey, sealed)
	if err != nil {
		return cfg, err
	}
	err = json.Unmarshal([]byte(plain), &cfg)
	return cfg, err
}

func (a *App) sendAlert(ctx context.Context, n pendingNotification) (notify.Delivery, error) {
	cfg, err := a.openChannelConfig(n.ConfigSealed)
	if err != nil {
		return notify.Delivery{}, fmt.Errorf("channel configuration unreadable: %w", err)
	}
	event := n.Payload
	switch n.Kind {
	case "email":
		return notify.Delivery{}, a.queueAlertEmails(ctx, n.AccountID, cfg.Emails, event)
	case "telegram":
		if a.cfg.TelegramBotToken == "" {
			return notify.Delivery{}, errors.New("telegram alerts are not configured on this server")
		}
		if n.Event == notify.EventUp && n.IncidentID != nil {
			var original *string
			if err := a.db.QueryRow(ctx, `
				SELECT provider_message_id FROM notifications
				WHERE incident_id=$1 AND channel_id=$2 AND event='down' AND provider_message_id IS NOT NULL
				ORDER BY created_at LIMIT 1`, *n.IncidentID, n.ChannelID).Scan(&original); err == nil && original != nil {
				event.ReplyTo = *original
			}
		}
		return notify.Telegram{Token: a.cfg.TelegramBotToken, ChatID: cfg.ChatID, TopicID: cfg.TopicID, APIBase: a.cfg.TelegramAPIBase, Client: a.alertClient()}.Send(ctx, event)
	case "slack":
		return notify.Slack{WebhookURL: cfg.WebhookURL, Client: a.alertClient()}.Send(ctx, event)
	case "webhook":
		return notify.Webhook{URL: cfg.URL, Secret: cfg.Secret, Client: a.alertClient()}.Send(ctx, event)
	}
	return notify.Delivery{}, fmt.Errorf("unknown channel kind %q", n.Kind)
}

func (a *App) alertClient() *http.Client {
	return notify.Client(notificationTimeout, a.cfg.ProbeAllowPrivate)
}

var alertPresentation = map[string][2]string{
	notify.EventDown:        {"crit", "Down"},
	notify.EventUp:          {"ok", "Recovered"},
	notify.EventCertificate: {"warn", "Certificate"},
	notify.EventTest:        {"info", "Test alert"},
}

const alertTimeLayout = "15:04 UTC, 2 January 2006"

func alertEmailPayload(e notify.Event, workspace string) map[string]any {
	look := alertPresentation[e.Kind]
	payload := map[string]any{
		"event": e.Kind, "tone": look[0], "label": look[1], "headline": e.Headline(), "detail": e.Detail(),
		"monitor_name": e.MonitorName, "target": e.Target, "dashboard_url": e.DashboardURL, "downtime": e.Downtime(),
		"regions": strings.Join(e.RegionsDown, ", "), "workspace": workspace,
	}
	if e.Kind == notify.EventDown {
		payload["cause"] = e.Cause
	}
	for key, at := range map[string]*time.Time{"started_at": e.StartedAt, "resolved_at": e.ResolvedAt, "cert_expires_at": e.CertExpiresAt} {
		if at != nil {
			payload[key] = at.UTC().Format(alertTimeLayout)
		}
	}
	if e.Kind != notify.EventDown {
		delete(payload, "started_at")
	}
	return payload
}

func (a *App) queueAlertEmails(ctx context.Context, accountID string, recipients []string, e notify.Event) error {
	if len(recipients) == 0 {
		return errors.New("no recipients configured")
	}
	var workspace string
	if err := a.db.QueryRow(ctx, `SELECT COALESCE(name,'') FROM accounts WHERE id=$1`, accountID).Scan(&workspace); err != nil {
		return err
	}
	raw, err := json.Marshal(alertEmailPayload(e, workspace))
	if err != nil {
		return err
	}
	tx, err := a.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer dbx.Rollback(ctx, tx)
	for _, to := range recipients {
		if _, err = tx.Exec(ctx, `INSERT INTO email_jobs(account_id,to_email,template,payload,next_attempt_at) VALUES($1,$2,'monitor_alert',$3,now())`, accountID, to, raw); err != nil {
			return err
		}
	}
	if err = tx.Commit(ctx); err != nil {
		return err
	}
	a.notifyEmail()
	return nil
}

func (a *App) heartbeatWorker(ctx context.Context) {
	ticker := time.NewTicker(heartbeatSweepInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			a.withJobLock(ctx, "monitor:heartbeat-sweep", "heartbeat_worker", "acquire_sweep_lock", a.sweepHeartbeats)
		case <-a.stop:
			return
		case <-ctx.Done():
			return
		}
	}
}

func (a *App) sweepHeartbeats(parent context.Context) {
	ctx, cancel := context.WithTimeout(parent, time.Minute)
	defer cancel()
	rows, err := a.db.Query(ctx, `
		SELECT m.id::text FROM monitors m JOIN accounts acc ON acc.id=m.account_id AND acc.status='active'
		WHERE m.kind='heartbeat' AND m.deleted_at IS NULL AND m.paused_at IS NULL AND m.state<>'down'
		  AND GREATEST(m.last_checked_at, m.state_changed_at) < now()-make_interval(secs => m.interval_seconds+m.grace_seconds)
		LIMIT 500`)
	if err != nil {
		a.logError("heartbeat sweep failed", "heartbeat_worker", "select_overdue", err)
		return
	}
	var overdue []string
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err == nil {
			overdue = append(overdue, id)
		}
	}
	rows.Close()
	for _, id := range overdue {
		if err := a.markHeartbeatMissed(ctx, id); err != nil {
			a.logError("heartbeat transition failed", "heartbeat_worker", "mark_missed", err, "monitor_id", id)
		}
	}
}

func (a *App) markHeartbeatMissed(ctx context.Context, id string) error {
	tx, err := a.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer dbx.Rollback(ctx, tx)
	m, err := lockMonitor(ctx, tx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	cause := "not pinging within " + notify.HumanDuration(time.Duration(m.IntervalSeconds+m.GraceSeconds)*time.Second)
	if err = recordRaw(ctx, tx, m.ID, m.AccountID, observation{Region: "heartbeat", At: now, Error: &cause}); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `UPDATE monitors SET last_error=$2 WHERE id=$1`, m.ID, cause); err != nil {
		return err
	}
	if err = a.transition(ctx, tx, m, "down", cause, nil, now); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (a *App) recordPing(ctx context.Context, token string) (bool, error) {
	tx, err := a.db.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer dbx.Rollback(ctx, tx)
	var id string
	err = tx.QueryRow(ctx, `
		SELECT m.id::text FROM monitors m JOIN accounts acc ON acc.id=m.account_id AND acc.status='active'
		WHERE m.heartbeat_token=$1 AND m.deleted_at IS NULL`, token).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	m, err := lockMonitor(ctx, tx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	now := time.Now().UTC()
	if err = recordRaw(ctx, tx, m.ID, m.AccountID, observation{Region: "heartbeat", At: now, Up: true}); err != nil {
		return false, err
	}
	if _, err = tx.Exec(ctx, `UPDATE monitors SET last_checked_at=$2,last_error=NULL WHERE id=$1`, m.ID, now); err != nil {
		return false, err
	}
	if err = a.transition(ctx, tx, m, "up", "", nil, now); err != nil {
		return false, err
	}
	return true, tx.Commit(ctx)
}

type observation struct {
	Region                         string
	At                             time.Time
	Up                             bool
	Status                         *int
	Total, DNS, Connect, TLS, TTFB *float64
	Error                          *string
	CertExpires                    *time.Time
}

func recordRaw(ctx context.Context, tx pgx.Tx, monitorID, accountID string, o observation) error {
	if _, err := tx.Exec(ctx, `
		INSERT INTO check_results(monitor_id,account_id,region,checked_at,up,status_code,total_ms,dns_ms,connect_ms,tls_ms,ttfb_ms,error,cert_expires_at)
		VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13) ON CONFLICT DO NOTHING`,
		monitorID, accountID, o.Region, o.At, o.Up, o.Status, o.Total, o.DNS, o.Connect, o.TLS, o.TTFB, o.Error, o.CertExpires); err != nil {
		return err
	}
	failed, responded, responseMs := 0, 0, 0.0
	if !o.Up {
		failed = 1
	}
	if o.Up && o.Total != nil {
		responded, responseMs = 1, *o.Total
	}
	_, err := tx.Exec(ctx, `
		INSERT INTO monitor_daily(monitor_id,account_id,day,checks,failures,response_ms_sum,response_count)
		VALUES($1,$2,($3::timestamptz AT TIME ZONE 'UTC')::date,1,$4,$5,$6)
		ON CONFLICT (monitor_id,day) DO UPDATE SET
			checks=monitor_daily.checks+1, failures=monitor_daily.failures+EXCLUDED.failures,
			response_ms_sum=monitor_daily.response_ms_sum+EXCLUDED.response_ms_sum,
			response_count=monitor_daily.response_count+EXCLUDED.response_count`,
		monitorID, accountID, o.At, failed, responseMs, responded)
	return err
}
