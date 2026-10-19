package app

import (
	"encoding/json/v2"
	"errors"
	"fmt"
	"net/http"
	"net/mail"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"uuid"

	"github.com/jackc/pgx/v5"

	"github.com/thekiharani/monitor/internal/cloud/config"
	"github.com/thekiharani/monitor/internal/cloud/httpx"
	"github.com/thekiharani/monitor/internal/cloud/notify"
	"github.com/thekiharani/monitor/internal/cloud/usage"
)

const maxChannelEmails = 10

var telegramChatID = regexp.MustCompile(`^(-?[0-9]{1,20}|@[A-Za-z0-9_]{5,32})$`)

type channelInput struct {
	Kind        *string   `json:"kind"`
	Name        *string   `json:"name"`
	Emails      *[]string `json:"emails"`
	ChatID      *string   `json:"chat_id"`
	TopicID     *int64    `json:"topic_id"`
	WebhookURL  *string   `json:"webhook_url"`
	URL         *string   `json:"url"`
	Enabled     *bool     `json:"enabled"`
	AllMonitors *bool     `json:"all_monitors"`
	MonitorIDs  *[]string `json:"monitor_ids"`
}

func invalidChannel(message string) httpx.Error {
	return httpx.Error{Code: "invalid_channel", Message: message, Status: http.StatusUnprocessableEntity}
}

func (a *App) applyChannelInput(kind string, cfg *channelConfig, in channelInput) error {
	switch kind {
	case "email":
		if in.Emails != nil {
			var emails []string
			for _, raw := range *in.Emails {
				address, err := mail.ParseAddress(strings.TrimSpace(raw))
				if err != nil || address.Name != "" || len(address.Address) > 254 {
					return invalidChannel(fmt.Sprintf("%q is not an email address.", raw))
				}
				if lower := strings.ToLower(address.Address); !slices.Contains(emails, lower) {
					emails = append(emails, lower)
				}
			}
			cfg.Emails = emails
		}
		if len(cfg.Emails) == 0 || len(cfg.Emails) > maxChannelEmails {
			return invalidChannel(fmt.Sprintf("An email channel sends to 1 to %d addresses.", maxChannelEmails))
		}
	case "telegram":
		if a.cfg.TelegramBotToken == "" {
			return invalidChannel("Telegram alerts are not available on this server yet.")
		}
		if in.ChatID != nil {
			cfg.ChatID = strings.TrimSpace(*in.ChatID)
		}
		if !telegramChatID.MatchString(cfg.ChatID) {
			return invalidChannel("chat_id must be a numeric Telegram chat id or an @channel username.")
		}
		if in.TopicID != nil {
			cfg.TopicID = *in.TopicID
		}
		if cfg.TopicID < 0 {
			return invalidChannel("topic_id must be a forum topic id.")
		}
	case "slack":
		if in.WebhookURL != nil {
			cfg.WebhookURL = strings.TrimSpace(*in.WebhookURL)
		}
		u, err := url.Parse(cfg.WebhookURL)
		if err != nil || u.Scheme != "https" || u.Host != "hooks.slack.com" || !strings.HasPrefix(u.Path, "/services/") {
			return invalidChannel("webhook_url must be a Slack incoming webhook (https://hooks.slack.com/services/…).")
		}
	case "webhook":
		if in.URL != nil {
			cfg.URL = strings.TrimSpace(*in.URL)
		}
		u, err := url.Parse(cfg.URL)
		if err != nil || u.Host == "" || u.User != nil || len(cfg.URL) > 2048 || (u.Scheme != "https" && !(u.Scheme == "http" && a.cfg.ProbeAllowPrivate)) {
			return invalidChannel("url must be an https:// address.")
		}
		if cfg.Secret == "" {
			secret, err := randomToken(32)
			if err != nil {
				return err
			}
			cfg.Secret = "whsec_" + secret
		}
	default:
		return invalidChannel("kind must be email, telegram, slack or webhook.")
	}
	return nil
}

func channelSummary(kind string, cfg channelConfig) map[string]any {
	switch kind {
	case "email":
		return map[string]any{"emails": cfg.Emails}
	case "telegram":
		return map[string]any{"chat_id": cfg.ChatID, "topic_id": cfg.TopicID}
	case "slack":
		masked := cfg.WebhookURL
		if base, secret, found := strings.CutLast(masked, "/"); found && base != "" && len(secret) > 4 {
			masked = base + "/…" + secret[len(secret)-4:]
		}
		return map[string]any{"webhook_url": masked}
	case "webhook":
		return map[string]any{"url": cfg.URL, "signing_secret_set": cfg.Secret != ""}
	}
	return map[string]any{}
}

type channelRow struct {
	ID, Kind, Name, Sealed string
	AllMonitors, Enabled   bool
	MonitorIDs             []string
	LastDeliveredAt        *time.Time
	LastError              *string
	CreatedAt              time.Time
}

const channelColumns = `c.id::text,c.kind,c.name,c.config_encrypted,c.all_monitors,c.enabled,
	COALESCE((SELECT array_agg(mc.monitor_id::text ORDER BY mc.monitor_id) FROM monitor_channels mc WHERE mc.channel_id=c.id),'{}'),
	c.last_delivered_at,c.last_error,c.created_at`

func scanChannel(row pgx.Row) (channelRow, error) {
	var c channelRow
	err := row.Scan(&c.ID, &c.Kind, &c.Name, &c.Sealed, &c.AllMonitors, &c.Enabled, &c.MonitorIDs, &c.LastDeliveredAt, &c.LastError, &c.CreatedAt)
	return c, err
}

func (a *App) channelView(c channelRow) map[string]any {
	cfg, err := a.openChannelConfig(c.Sealed)
	summary := map[string]any{}
	if err == nil {
		summary = channelSummary(c.Kind, cfg)
	}
	return map[string]any{
		"id": c.ID, "kind": c.Kind, "name": c.Name, "config": summary, "enabled": c.Enabled,
		"all_monitors": c.AllMonitors, "monitor_ids": c.MonitorIDs,
		"last_delivered_at": httpx.RFC3339(c.LastDeliveredAt), "last_error": c.LastError, "created_at": httpx.RFC3339(&c.CreatedAt),
	}
}

func (a *App) sealChannel(cfg channelConfig) (string, error) {
	raw, err := json.Marshal(cfg)
	if err != nil {
		return "", err
	}
	return config.Seal(a.cfg.AppEncryptionKey, string(raw))
}

func setChannelMonitors(r *http.Request, tx pgx.Tx, channelID, accountID string, ids []string) error {
	for _, id := range ids {
		if _, err := uuid.Parse(id); err != nil {
			return invalidChannel("monitor_ids must be monitor ids.")
		}
	}
	if _, err := tx.Exec(r.Context(), `DELETE FROM monitor_channels WHERE channel_id=$1`, channelID); err != nil {
		return err
	}
	tag, err := tx.Exec(r.Context(), `
		INSERT INTO monitor_channels(monitor_id,channel_id,account_id)
		SELECT m.id,$1,$2 FROM monitors m WHERE m.account_id=$2 AND m.deleted_at IS NULL AND m.id::text = ANY($3)`,
		channelID, accountID, ids)
	if err == nil && int(tag.RowsAffected()) != len(slices.Compact(slices.Sorted(slices.Values(ids)))) {
		return invalidChannel("monitor_ids names a monitor that does not exist.")
	}
	return err
}

func (a *App) listChannels(w http.ResponseWriter, r *http.Request) {
	p, ok := a.auth(w, r)
	if !ok {
		return
	}
	items := make([]map[string]any, 0)
	if err := a.readTenant(r.Context(), p.AccountID.String(), func(tx pgx.Tx) error {
		rows, err := tx.Query(r.Context(), `SELECT `+channelColumns+` FROM alert_channels c WHERE c.account_id=$1 AND c.deleted_at IS NULL ORDER BY c.created_at`, p.AccountID.String())
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			c, err := scanChannel(rows)
			if err != nil {
				return err
			}
			items = append(items, a.channelView(c))
		}
		return rows.Err()
	}); err != nil {
		writeMonitorError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{
		"data": items, "telegram_available": a.cfg.TelegramBotToken != "", "telegram_bot": a.cfg.TelegramBotName,
	})
}

func (a *App) createChannel(w http.ResponseWriter, r *http.Request) {
	p, ok := a.authWorkspaceManager(w, r)
	if !ok {
		return
	}
	var in channelInput
	body, err := readBody(w, r, 32<<10)
	if err != nil || decode(body, &in) != nil {
		httpx.WriteError(w, r, eToHTTP("invalid_json"))
		return
	}
	if in.Kind == nil {
		httpx.WriteError(w, r, invalidChannel("kind is required."))
		return
	}
	name := ""
	if in.Name != nil {
		name = strings.TrimSpace(*in.Name)
	}
	if n := len([]rune(name)); n < 1 || n > 128 {
		httpx.WriteError(w, r, invalidChannel("name must be 1 to 128 characters."))
		return
	}
	var cfg channelConfig
	if err = a.applyChannelInput(*in.Kind, &cfg, in); err != nil {
		writeMonitorError(w, r, err)
		return
	}
	sealed, err := a.sealChannel(cfg)
	if err != nil {
		httpx.WriteError(w, r, eToHTTP("internal_error"))
		return
	}
	limits := a.planLimits(p.Plan)
	var created channelRow
	err = a.withTenant(r.Context(), p.AccountID.String(), func(tx pgx.Tx) error {
		var count int
		if err := tx.QueryRow(r.Context(), `SELECT count(*) FROM alert_channels WHERE account_id=$1 AND deleted_at IS NULL`, p.AccountID.String()).Scan(&count); err != nil {
			return err
		}
		if count >= limits.MaxAlertChannels {
			return errPlanLimit
		}
		allMonitors := in.AllMonitors == nil || *in.AllMonitors
		enabled := in.Enabled == nil || *in.Enabled
		var id string
		if err := tx.QueryRow(r.Context(), `
			INSERT INTO alert_channels(account_id,kind,name,config_encrypted,all_monitors,enabled) VALUES($1,$2,$3,$4,$5,$6) RETURNING id::text`,
			p.AccountID.String(), *in.Kind, name, sealed, allMonitors, enabled).Scan(&id); err != nil {
			return err
		}
		if !allMonitors && in.MonitorIDs != nil {
			if err := setChannelMonitors(r, tx, id, p.AccountID.String(), *in.MonitorIDs); err != nil {
				return err
			}
		}
		var err error
		created, err = scanChannel(tx.QueryRow(r.Context(), `SELECT `+channelColumns+` FROM alert_channels c WHERE c.id=$1`, id))
		return err
	})
	if err != nil {
		writeMonitorError(w, r, err)
		return
	}
	view := a.channelView(created)
	if *in.Kind == "webhook" {
		view["signing_secret"] = cfg.Secret
	}
	httpx.WriteJSON(w, http.StatusCreated, map[string]any{"data": view})
}

func channelID(r *http.Request) (string, bool) {
	id := r.PathValue("id")
	_, err := uuid.Parse(id)
	return id, err == nil
}

func (a *App) updateChannel(w http.ResponseWriter, r *http.Request) {
	p, ok := a.authWorkspaceManager(w, r)
	if !ok {
		return
	}
	id, valid := channelID(r)
	if !valid {
		httpx.WriteError(w, r, eToHTTP("channel_not_found"))
		return
	}
	var in channelInput
	body, err := readBody(w, r, 32<<10)
	if err != nil || decode(body, &in) != nil {
		httpx.WriteError(w, r, eToHTTP("invalid_json"))
		return
	}
	var updated channelRow
	err = a.withTenant(r.Context(), p.AccountID.String(), func(tx pgx.Tx) error {
		current, err := scanChannel(tx.QueryRow(r.Context(), `SELECT `+channelColumns+` FROM alert_channels c WHERE c.id=$1 AND c.account_id=$2 AND c.deleted_at IS NULL FOR UPDATE`, id, p.AccountID.String()))
		if err != nil {
			return err
		}
		if in.Kind != nil && *in.Kind != current.Kind {
			return invalidChannel("A channel's kind cannot change; add a new channel instead.")
		}
		cfg, err := a.openChannelConfig(current.Sealed)
		if err != nil {
			return err
		}
		if err = a.applyChannelInput(current.Kind, &cfg, in); err != nil {
			return err
		}
		sealed, err := a.sealChannel(cfg)
		if err != nil {
			return err
		}
		name := current.Name
		if in.Name != nil {
			name = strings.TrimSpace(*in.Name)
		}
		if n := len([]rune(name)); n < 1 || n > 128 {
			return invalidChannel("name must be 1 to 128 characters.")
		}
		allMonitors, enabled := current.AllMonitors, current.Enabled
		if in.AllMonitors != nil {
			allMonitors = *in.AllMonitors
		}
		if in.Enabled != nil {
			enabled = *in.Enabled
		}
		if _, err = tx.Exec(r.Context(), `UPDATE alert_channels SET name=$3,config_encrypted=$4,all_monitors=$5,enabled=$6,updated_at=now() WHERE id=$1 AND account_id=$2`,
			id, p.AccountID.String(), name, sealed, allMonitors, enabled); err != nil {
			return err
		}
		if in.MonitorIDs != nil {
			if err = setChannelMonitors(r, tx, id, p.AccountID.String(), *in.MonitorIDs); err != nil {
				return err
			}
		}
		updated, err = scanChannel(tx.QueryRow(r.Context(), `SELECT `+channelColumns+` FROM alert_channels c WHERE c.id=$1`, id))
		return err
	})
	if errors.Is(err, pgx.ErrNoRows) {
		httpx.WriteError(w, r, eToHTTP("channel_not_found"))
		return
	}
	if err != nil {
		writeMonitorError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"data": a.channelView(updated)})
}

func (a *App) deleteChannel(w http.ResponseWriter, r *http.Request) {
	p, ok := a.authWorkspaceManager(w, r)
	if !ok {
		return
	}
	id, valid := channelID(r)
	if !valid {
		httpx.WriteError(w, r, eToHTTP("channel_not_found"))
		return
	}
	affected, err := a.tenantExec(r.Context(), p.AccountID.String(),
		`UPDATE alert_channels SET deleted_at=now(),enabled=false,updated_at=now() WHERE id=$1 AND account_id=$2 AND deleted_at IS NULL`, id, p.AccountID.String())
	if err != nil {
		writeMonitorError(w, r, err)
		return
	}
	if affected == 0 {
		httpx.WriteError(w, r, eToHTTP("channel_not_found"))
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *App) testChannel(w http.ResponseWriter, r *http.Request) {
	p, ok := a.auth(w, r)
	if !ok {
		return
	}
	id, valid := channelID(r)
	if !valid {
		httpx.WriteError(w, r, eToHTTP("channel_not_found"))
		return
	}
	if !a.checkLimit("channel-test:"+id, usage.Limits{RequestsPerSecond: 1.0 / 10, Burst: 3}) {
		httpx.WriteError(w, r, eToHTTP("rate_limited"))
		return
	}
	var workspace string
	event := notify.Event{Kind: notify.EventTest, MonitorName: "Test", DashboardURL: strings.TrimRight(a.cfg.DashboardOrigin, "/") + "/app/alerts"}
	err := a.withTenant(r.Context(), p.AccountID.String(), func(tx pgx.Tx) error {
		if err := tx.QueryRow(r.Context(), `SELECT COALESCE(name,'') FROM accounts WHERE id=$1`, p.AccountID.String()).Scan(&workspace); err != nil {
			return err
		}
		event.Workspace = workspace
		payload, err := json.Marshal(event)
		if err != nil {
			return err
		}
		tag, err := tx.Exec(r.Context(), `
			INSERT INTO notifications(account_id,channel_id,event,payload)
			SELECT account_id,id,'test',$3 FROM alert_channels WHERE id=$1 AND account_id=$2 AND deleted_at IS NULL`,
			id, p.AccountID.String(), payload)
		if err == nil && tag.RowsAffected() == 0 {
			return pgx.ErrNoRows
		}
		return err
	})
	if errors.Is(err, pgx.ErrNoRows) {
		httpx.WriteError(w, r, eToHTTP("channel_not_found"))
		return
	}
	if err != nil {
		writeMonitorError(w, r, err)
		return
	}
	a.notifyAlerts()
	httpx.WriteJSON(w, http.StatusAccepted, map[string]any{"data": map[string]string{"status": "queued"}})
}

func (a *App) channelDeliveries(w http.ResponseWriter, r *http.Request) {
	p, ok := a.auth(w, r)
	if !ok {
		return
	}
	id, valid := channelID(r)
	if !valid {
		httpx.WriteError(w, r, eToHTTP("channel_not_found"))
		return
	}
	items := make([]map[string]any, 0)
	if err := a.readTenant(r.Context(), p.AccountID.String(), func(tx pgx.Tx) error {
		var exists bool
		if err := tx.QueryRow(r.Context(), `SELECT true FROM alert_channels WHERE id=$1 AND account_id=$2 AND deleted_at IS NULL`, id, p.AccountID.String()).Scan(&exists); err != nil {
			return err
		}
		rows, err := tx.Query(r.Context(), `
			SELECT n.id::text,n.event,n.monitor_id::text,m.name,n.attempts,n.sent_at,n.last_error,n.created_at
			FROM notifications n LEFT JOIN monitors m ON m.id=n.monitor_id
			WHERE n.channel_id=$1 AND n.account_id=$2 ORDER BY n.created_at DESC LIMIT 50`, id, p.AccountID.String())
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var nid, event string
			var monitorIDValue, monitorName, lastError *string
			var attempts int
			var sentAt *time.Time
			var createdAt time.Time
			if err = rows.Scan(&nid, &event, &monitorIDValue, &monitorName, &attempts, &sentAt, &lastError, &createdAt); err != nil {
				return err
			}
			status := "pending"
			switch {
			case sentAt != nil:
				status = "delivered"
			case attempts >= notificationAttempts:
				status = "failed"
			}
			items = append(items, map[string]any{
				"id": nid, "event": event, "monitor_id": monitorIDValue, "monitor_name": monitorName, "status": status,
				"attempts": attempts, "sent_at": httpx.RFC3339(sentAt), "last_error": lastError, "created_at": httpx.RFC3339(&createdAt),
			})
		}
		return rows.Err()
	}); errors.Is(err, pgx.ErrNoRows) {
		httpx.WriteError(w, r, eToHTTP("channel_not_found"))
		return
	} else if err != nil {
		writeMonitorError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"data": items})
}

func (a *App) listIncidents(w http.ResponseWriter, r *http.Request) {
	p, ok := a.auth(w, r)
	if !ok {
		return
	}
	status := r.URL.Query().Get("status")
	monitorFilter := r.URL.Query().Get("monitor_id")
	if status != "" && status != "open" && status != "resolved" {
		httpx.WriteError(w, r, eToHTTP("invalid_request"))
		return
	}
	if monitorFilter != "" {
		if _, err := uuid.Parse(monitorFilter); err != nil {
			httpx.WriteError(w, r, eToHTTP("invalid_request"))
			return
		}
	}
	limit := 50
	if raw := r.URL.Query().Get("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 || n > 200 {
			httpx.WriteError(w, r, eToHTTP("invalid_request"))
			return
		}
		limit = n
	}
	items := make([]map[string]any, 0)
	if err := a.readTenant(r.Context(), p.AccountID.String(), func(tx pgx.Tx) error {
		rows, err := tx.Query(r.Context(), `
			SELECT i.id::text,i.monitor_id::text,m.name,i.started_at,i.resolved_at,i.cause,i.regions_down,i.acknowledged_at
			FROM incidents i JOIN monitors m ON m.id=i.monitor_id
			WHERE i.account_id=$1 AND ($2='' OR i.monitor_id::text=$2)
			  AND ($3='' OR ($3='open' AND i.resolved_at IS NULL) OR ($3='resolved' AND i.resolved_at IS NOT NULL))
			ORDER BY i.started_at DESC LIMIT $4`, p.AccountID.String(), monitorFilter, status, limit)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var id, monitorIDValue, name, cause string
			var started time.Time
			var resolved, acknowledged *time.Time
			var regions []string
			if err = rows.Scan(&id, &monitorIDValue, &name, &started, &resolved, &cause, &regions, &acknowledged); err != nil {
				return err
			}
			end := time.Now()
			if resolved != nil {
				end = *resolved
			}
			items = append(items, map[string]any{
				"id": id, "monitor_id": monitorIDValue, "monitor_name": name, "started_at": httpx.RFC3339(&started),
				"resolved_at": httpx.RFC3339(resolved), "cause": cause, "regions_down": regions,
				"acknowledged_at": httpx.RFC3339(acknowledged), "duration_seconds": int(end.Sub(started).Seconds()),
			})
		}
		return rows.Err()
	}); err != nil {
		writeMonitorError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"data": items})
}

func (a *App) acknowledgeIncident(w http.ResponseWriter, r *http.Request) {
	p, ok := a.auth(w, r)
	if !ok {
		return
	}
	id, valid := channelID(r)
	if !valid {
		httpx.WriteError(w, r, eToHTTP("not_found"))
		return
	}
	affected, err := a.tenantExec(r.Context(), p.AccountID.String(),
		`UPDATE incidents SET acknowledged_at=COALESCE(acknowledged_at,now()),acknowledger_id=COALESCE(acknowledger_id,$3) WHERE id=$1 AND account_id=$2`,
		id, p.AccountID.String(), p.PrincipalID.String())
	if err != nil {
		writeMonitorError(w, r, err)
		return
	}
	if affected == 0 {
		httpx.WriteError(w, r, eToHTTP("not_found"))
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

type maintenanceInput struct {
	Title      string    `json:"title"`
	StartsAt   time.Time `json:"starts_at"`
	EndsAt     time.Time `json:"ends_at"`
	MonitorIDs []string  `json:"monitor_ids"`
}

func (a *App) listMaintenance(w http.ResponseWriter, r *http.Request) {
	p, ok := a.auth(w, r)
	if !ok {
		return
	}
	items := make([]map[string]any, 0)
	if err := a.readTenant(r.Context(), p.AccountID.String(), func(tx pgx.Tx) error {
		rows, err := tx.Query(r.Context(), `
			SELECT id::text,title,starts_at,ends_at,COALESCE(array(SELECT unnest(monitor_ids)::text),'{}')
			FROM maintenance_windows WHERE account_id=$1 AND deleted_at IS NULL AND ends_at>now()-interval '30 days'
			ORDER BY starts_at DESC LIMIT 100`, p.AccountID.String())
		if err != nil {
			return err
		}
		defer rows.Close()
		now := time.Now()
		for rows.Next() {
			var id, title string
			var starts, ends time.Time
			var monitors []string
			if err = rows.Scan(&id, &title, &starts, &ends, &monitors); err != nil {
				return err
			}
			state := "scheduled"
			switch {
			case now.After(ends):
				state = "completed"
			case now.After(starts):
				state = "active"
			}
			items = append(items, map[string]any{
				"id": id, "title": title, "starts_at": httpx.RFC3339(&starts), "ends_at": httpx.RFC3339(&ends),
				"monitor_ids": monitors, "all_monitors": len(monitors) == 0, "state": state,
			})
		}
		return rows.Err()
	}); err != nil {
		writeMonitorError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"data": items})
}

func (a *App) createMaintenance(w http.ResponseWriter, r *http.Request) {
	p, ok := a.authWorkspaceManager(w, r)
	if !ok {
		return
	}
	var in maintenanceInput
	body, err := readBody(w, r, 32<<10)
	if err != nil || decode(body, &in) != nil {
		httpx.WriteError(w, r, eToHTTP("invalid_json"))
		return
	}
	in.Title = strings.TrimSpace(in.Title)
	invalid := func(message string) {
		httpx.WriteError(w, r, httpx.Error{Code: "invalid_maintenance", Message: message, Status: http.StatusUnprocessableEntity})
	}
	switch {
	case len([]rune(in.Title)) < 1 || len([]rune(in.Title)) > 256:
		invalid("title must be 1 to 256 characters.")
		return
	case !in.EndsAt.After(in.StartsAt):
		invalid("ends_at must be after starts_at.")
		return
	case in.EndsAt.Before(time.Now()):
		invalid("The window has already ended.")
		return
	case in.EndsAt.Sub(in.StartsAt) > 7*24*time.Hour:
		invalid("A maintenance window lasts at most 7 days.")
		return
	}
	for _, id := range in.MonitorIDs {
		if _, err := uuid.Parse(id); err != nil {
			invalid("monitor_ids must be monitor ids.")
			return
		}
	}
	if in.MonitorIDs == nil {
		in.MonitorIDs = []string{}
	}
	var id string
	err = a.withTenant(r.Context(), p.AccountID.String(), func(tx pgx.Tx) error {
		var known int
		if err := tx.QueryRow(r.Context(), `SELECT count(*) FROM monitors WHERE account_id=$1 AND deleted_at IS NULL AND id::text = ANY($2)`,
			p.AccountID.String(), in.MonitorIDs).Scan(&known); err != nil {
			return err
		}
		if known != len(slices.Compact(slices.Sorted(slices.Values(in.MonitorIDs)))) {
			return httpx.Error{Code: "invalid_maintenance", Message: "monitor_ids names a monitor that does not exist.", Status: http.StatusUnprocessableEntity}
		}
		return tx.QueryRow(r.Context(), `
			INSERT INTO maintenance_windows(account_id,title,starts_at,ends_at,monitor_ids,creator_id)
			VALUES($1,$2,$3,$4,$5::uuid[],$6) RETURNING id::text`,
			p.AccountID.String(), in.Title, in.StartsAt, in.EndsAt, in.MonitorIDs, p.PrincipalID.String()).Scan(&id)
	})
	if err != nil {
		writeMonitorError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusCreated, map[string]any{"data": map[string]any{"id": id}})
}

func (a *App) deleteMaintenance(w http.ResponseWriter, r *http.Request) {
	p, ok := a.authWorkspaceManager(w, r)
	if !ok {
		return
	}
	id, valid := channelID(r)
	if !valid {
		httpx.WriteError(w, r, eToHTTP("not_found"))
		return
	}
	affected, err := a.tenantExec(r.Context(), p.AccountID.String(),
		`UPDATE maintenance_windows SET deleted_at=now() WHERE id=$1 AND account_id=$2 AND deleted_at IS NULL`, id, p.AccountID.String())
	if err != nil {
		writeMonitorError(w, r, err)
		return
	}
	if affected == 0 {
		httpx.WriteError(w, r, eToHTTP("not_found"))
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *App) ping(w http.ResponseWriter, r *http.Request) {
	token := r.PathValue("token")
	w.Header().Set("Cache-Control", "no-store")
	if len(token) < 16 || len(token) > 64 {
		http.Error(w, "unknown heartbeat", http.StatusNotFound)
		return
	}
	if !a.checkLimit("ping:"+token, usage.Limits{RequestsPerSecond: 1, Burst: 10}) {
		http.Error(w, "too many pings", http.StatusTooManyRequests)
		return
	}
	found, err := a.recordPing(r.Context(), token)
	if err != nil {
		a.logError("heartbeat ping failed", "heartbeat_api", "record_ping", err)
		http.Error(w, "unavailable", http.StatusServiceUnavailable)
		return
	}
	if !found {
		http.Error(w, "unknown heartbeat", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = w.Write([]byte("OK\n"))
}
