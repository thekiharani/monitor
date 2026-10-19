package app

import (
	"context"
	"encoding/json/v2"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"
	"uuid"

	"github.com/jackc/pgx/v5"

	"github.com/thekiharani/monitor/internal/checks"
	"github.com/thekiharani/monitor/internal/cloud/access"
	"github.com/thekiharani/monitor/internal/cloud/config"
	"github.com/thekiharani/monitor/internal/cloud/dbx"
	"github.com/thekiharani/monitor/internal/cloud/httpx"
	"github.com/thekiharani/monitor/internal/cloud/probe"
	"github.com/thekiharani/monitor/internal/cloud/usage"
)

const (
	probePollInterval  = time.Second
	rawResultRetention = 31
	partitionsAhead    = 7
)

type dueCheck struct {
	MonitorID string
	AccountID string
	Region    string
	Eligible  bool
	Spec      checks.Spec
}

func (a *App) claimDueChecks(ctx context.Context, region string, limit int, claimant string) ([]dueCheck, error) {
	rows, err := a.db.Query(ctx, `
		WITH due AS (
			SELECT monitor_id, region FROM monitor_schedules
			WHERE region=$1 AND next_run_at<=now()
			ORDER BY next_run_at LIMIT $2
			FOR UPDATE SKIP LOCKED
		)
		UPDATE monitor_schedules s
		SET next_run_at=now()+make_interval(secs => m.interval_seconds),
			claimant_id=NULLIF($3,'')::uuid,
			claim_expires_at=CASE WHEN $3='' THEN NULL ELSE now()+make_interval(secs => m.timeout_ms/1000.0)+$4::interval END
		FROM due, monitors m, accounts acc
		WHERE s.monitor_id=due.monitor_id AND s.region=due.region AND m.id=s.monitor_id AND acc.id=m.account_id
		RETURNING m.id::text, m.account_id::text, s.region,
			m.deleted_at IS NULL AND m.paused_at IS NULL AND acc.status='active',
			m.kind, m.target, m.method, m.request_encrypted, m.expected_statuses,
			COALESCE(m.keyword,''), m.keyword_absent, m.follow_redirects, m.timeout_ms`, region, limit, claimant, probeClaimGrace.String())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []dueCheck
	for rows.Next() {
		var d dueCheck
		var statuses []int16
		var timeoutMs int
		var sealed *string
		if err = rows.Scan(&d.MonitorID, &d.AccountID, &d.Region, &d.Eligible, &d.Spec.Kind, &d.Spec.URL, &d.Spec.Method,
			&sealed, &statuses, &d.Spec.Keyword, &d.Spec.KeywordAbsent, &d.Spec.FollowRedirects, &timeoutMs); err != nil {
			return nil, err
		}
		request, openErr := openMonitorRequest(a.cfg.AppEncryptionKey, sealed)
		if openErr != nil {
			d.Eligible = false
			a.logError("monitor request unreadable", "probe_worker", "open_request", openErr, "monitor_id", d.MonitorID)
		}
		d.Spec.Headers = request.Headers
		if request.Body != nil {
			d.Spec.Body = *request.Body
		}
		for _, s := range statuses {
			d.Spec.ExpectedStatuses = append(d.Spec.ExpectedStatuses, int(s))
		}
		d.Spec.Timeout = time.Duration(timeoutMs) * time.Millisecond
		d.Spec.PublicOnly = !a.cfg.ProbeAllowPrivate
		out = append(out, d)
	}
	return out, rows.Err()
}

func (a *App) recordCheck(ctx context.Context, d dueCheck, checkedAt time.Time, res checks.Result) error {
	tx, err := a.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer dbx.Rollback(ctx, tx)
	m, err := lockMonitor(ctx, tx, d.MonitorID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	o := observation{
		Region: d.Region, At: checkedAt, Up: res.Up, CertExpires: res.CertExpiresAt,
		Total: nullableMs(res.TotalMs), DNS: nullableMs(res.DNSMs), Connect: nullableMs(res.ConnectMs), TLS: nullableMs(res.TLSMs), TTFB: nullableMs(res.TTFBMs),
	}
	if res.StatusCode > 0 {
		o.Status = &res.StatusCode
	}
	if res.Error != "" {
		message := truncate(res.Error, 512)
		o.Error = &message
	}
	if err = recordRaw(ctx, tx, d.MonitorID, d.AccountID, o); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `
		UPDATE monitor_schedules SET
			consecutive_failures=CASE WHEN $3 THEN 0 ELSE consecutive_failures+1 END,
			last_checked_at=$4,
			next_run_at=CASE WHEN NOT $3 AND consecutive_failures+1 < $5 THEN least(next_run_at, $4::timestamptz+$6::interval) ELSE next_run_at END
		WHERE monitor_id=$1 AND region=$2`,
		d.MonitorID, d.Region, res.Up, checkedAt, confirmFailures, confirmRetryDelay.String()); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `
		UPDATE monitors SET last_checked_at=$2, last_status_code=$3, last_response_ms=$4, last_error=$5,
			cert_expires_at=COALESCE($6,cert_expires_at)
		WHERE id=$1 AND (last_checked_at IS NULL OR last_checked_at<=$2)`,
		d.MonitorID, checkedAt, o.Status, o.Total, o.Error, res.CertExpiresAt); err != nil {
		return err
	}
	failing, _, regionsDown, err := regionalVerdict(ctx, tx, d.MonitorID)
	if err != nil {
		return err
	}
	next := m.State
	switch {
	case failing:
		next = "down"
	case m.State == "down" || res.Up:
		next = "up"
	}
	if err = a.transition(ctx, tx, m, next, res.Error, regionsDown, checkedAt); err != nil {
		return err
	}
	if err = a.warnCertificate(ctx, tx, m, res.CertExpiresAt); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func nullableMs(v float64) *float64 {
	if v <= 0 {
		return nil
	}
	return &v
}

func (a *App) probeWorker(ctx context.Context) {
	region := a.cfg.LocalProbeRegion
	if region == "" {
		return
	}
	a.ensurePartitions(ctx)
	slots := make(chan struct{}, a.cfg.ProbeConcurrency)
	var running sync.WaitGroup
	defer running.Wait()
	ticker := time.NewTicker(probePollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			free := cap(slots) - len(slots)
			if free == 0 {
				continue
			}
			due, err := a.claimDueChecks(ctx, region, free, "")
			if err != nil {
				if ctx.Err() == nil {
					a.logError("probe claim failed", "probe_worker", "claim_due_checks", err, "region", region)
				}
				continue
			}
			for _, d := range due {
				if !d.Eligible {
					continue
				}
				slots <- struct{}{}
				running.Go(func() {
					defer func() { <-slots }()
					a.runAndRecord(ctx, d)
				})
			}
		case <-a.stop:
			return
		case <-ctx.Done():
			return
		}
	}
}

func (a *App) runAndRecord(ctx context.Context, d dueCheck) {
	checkedAt := time.Now().UTC()
	res := checks.Run(ctx, d.Spec)
	if ctx.Err() != nil {
		return
	}
	recordCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	if err := a.recordCheck(recordCtx, d, checkedAt, res); err != nil {
		a.logError("probe record failed", "probe_worker", "record_check", err, "monitor_id", d.MonitorID, "region", d.Region)
	}
}

func partitionName(day time.Time) string {
	return "check_results_p" + day.Format("20060102")
}

func (a *App) ensurePartitions(parent context.Context) {
	ctx, cancel := context.WithTimeout(parent, cleanupTimeout)
	defer cancel()
	today := time.Now().UTC().Truncate(24 * time.Hour)
	for offset := -1; offset <= partitionsAhead; offset++ {
		day := today.AddDate(0, 0, offset)
		statement := fmt.Sprintf(`CREATE TABLE IF NOT EXISTS %s PARTITION OF check_results FOR VALUES FROM ('%s') TO ('%s')`,
			partitionName(day), day.Format(time.RFC3339), day.AddDate(0, 0, 1).Format(time.RFC3339))
		if _, err := a.db.Exec(ctx, statement); err != nil {
			a.logError("partition create failed", "partition_worker", "create_partition", err, "partition", partitionName(day))
		}
	}
}

func (a *App) dropExpiredPartitions(parent context.Context) {
	ctx, cancel := context.WithTimeout(parent, cleanupTimeout)
	defer cancel()
	cutoff := partitionName(time.Now().UTC().AddDate(0, 0, -rawResultRetention))
	var names []string
	if err := a.db.QueryRow(ctx, `
		SELECT COALESCE(array_agg(c.relname::text),'{}') FROM pg_inherits i
		JOIN pg_class c ON c.oid=i.inhrelid JOIN pg_class p ON p.oid=i.inhparent
		WHERE p.relname='check_results'`).Scan(&names); err != nil {
		a.logError("partition list failed", "partition_worker", "list_partitions", err)
		return
	}
	for _, name := range names {
		if !strings.HasPrefix(name, "check_results_p") || name >= cutoff {
			continue
		}
		if _, err := a.db.Exec(ctx, `DROP TABLE IF EXISTS `+pgx.Identifier{name}.Sanitize()); err != nil {
			a.logError("partition drop failed", "partition_worker", "drop_partition", err, "partition", name)
			continue
		}
		a.logEvent("expired check results removed", "partition_worker", "drop_partition", "partition", name)
	}
}

func (a *App) pruneDailyHistory(parent context.Context) {
	ctx, cancel := context.WithTimeout(parent, cleanupTimeout)
	defer cancel()
	for _, plan := range usage.CustomerPlans {
		if _, err := a.db.Exec(ctx, `
			DELETE FROM monitor_daily d USING accounts acc
			WHERE acc.id=d.account_id AND acc.plan=$1 AND d.day<current_date-$2::int`,
			plan, a.planLimits(plan).RetentionDays); err != nil {
			a.logError("daily history prune failed", "partition_worker", "prune_monitor_daily", err, "plan", plan)
		}
	}
}

func (a *App) maintainResults(ctx context.Context) {
	a.withJobLock(ctx, "monitor:results-maintenance", "partition_worker", "acquire_maintenance_lock", func(ctx context.Context) {
		a.ensurePartitions(ctx)
		a.dropExpiredPartitions(ctx)
		a.pruneDailyHistory(ctx)
	})
}

const (
	probeReportWindow = 10 * time.Minute
	probeClaimGrace   = 2 * time.Minute
	probeClockSkew    = time.Minute
	probeOnlineWithin = 2 * time.Minute
)

type probeIdentity struct {
	ID     string
	Region string
}

func (a *App) authenticateProbe(w http.ResponseWriter, r *http.Request) (probeIdentity, bool) {
	if !a.requireHost(w, r, a.cfg.APIOrigin) {
		return probeIdentity{}, false
	}
	raw := access.Bearer(r.Header.Get("Authorization"))
	var id probeIdentity
	if !strings.HasPrefix(raw, probe.TokenPrefix) {
		httpx.WriteError(w, r, eToHTTP("unauthorized"))
		return id, false
	}
	err := a.db.QueryRow(r.Context(), `
		UPDATE probes SET last_seen_at=now(), last_ip=$2
		WHERE secret_hash=$1 AND revoked_at IS NULL
		RETURNING id::text, region`, config.HashSecret(raw), a.clientIPValue(r)).Scan(&id.ID, &id.Region)
	if errors.Is(err, pgx.ErrNoRows) {
		httpx.WriteError(w, r, eToHTTP("unauthorized"))
		return id, false
	}
	if err != nil {
		a.logError("probe authentication failed", "probe_api", "authenticate", err)
		httpx.WriteError(w, r, eToHTTP("internal_error"))
		return id, false
	}
	if !slices.Contains(a.cfg.ProbeRegions, id.Region) {
		httpx.WriteError(w, r, eToHTTP("probe_region_unavailable"))
		return id, false
	}
	return id, true
}

func (a *App) probeClaim(w http.ResponseWriter, r *http.Request) {
	identity, ok := a.authenticateProbe(w, r)
	if !ok {
		return
	}
	var in probe.Claim
	if body, err := readBody(w, r, 4<<10); err != nil || decode(body, &in) != nil {
		httpx.WriteError(w, r, eToHTTP("invalid_json"))
		return
	}
	if in.Version != "" {
		if _, err := a.db.Exec(r.Context(), `UPDATE probes SET version=$2 WHERE id=$1 AND version IS DISTINCT FROM $2`, identity.ID, truncate(in.Version, 64)); err != nil {
			a.logError("probe version update failed", "probe_api", "record_version", err, "probe_id", identity.ID)
		}
	}
	due, err := a.claimDueChecks(r.Context(), identity.Region, min(max(in.Limit, 1), probe.MaxClaim), identity.ID)
	if err != nil {
		a.logError("probe claim failed", "probe_api", "claim_due_checks", err, "probe_id", identity.ID, "region", identity.Region)
		httpx.WriteError(w, r, eToHTTP("internal_error"))
		return
	}
	work := probe.Work{Region: identity.Region, Checks: make([]probe.Check, 0, len(due))}
	for _, d := range due {
		if d.Eligible {
			work.Checks = append(work.Checks, probe.CheckFromSpec(d.MonitorID, d.Spec))
		}
	}
	httpx.WriteJSON(w, http.StatusOK, work)
}

func (a *App) probeResults(w http.ResponseWriter, r *http.Request) {
	identity, ok := a.authenticateProbe(w, r)
	if !ok {
		return
	}
	var in probe.Batch
	if body, err := readBody(w, r, 2<<20); err != nil || decode(body, &in) != nil {
		httpx.WriteError(w, r, eToHTTP("invalid_json"))
		return
	}
	if len(in.Results) > probe.MaxReport {
		httpx.WriteError(w, r, eToHTTP("invalid_request"))
		return
	}
	now := time.Now().UTC()
	accepted := 0
	for _, report := range in.Results {
		if _, err := uuid.Parse(report.MonitorID); err != nil || report.CheckedAt.Before(now.Add(-probeReportWindow)) || report.CheckedAt.After(now.Add(probeClockSkew)) {
			continue
		}
		var accountID string
		err := a.db.QueryRow(r.Context(), `
			UPDATE monitor_schedules SET claimant_id=NULL, claim_expires_at=NULL
			WHERE monitor_id=$1 AND region=$2 AND claimant_id=$3 AND claim_expires_at > now()
			RETURNING account_id::text`, report.MonitorID, identity.Region, identity.ID).Scan(&accountID)
		if errors.Is(err, pgx.ErrNoRows) {
			continue
		}
		if err == nil {
			d := dueCheck{MonitorID: report.MonitorID, AccountID: accountID, Region: identity.Region, Eligible: true}
			err = a.recordCheck(r.Context(), d, report.CheckedAt.UTC(), report.Result())
		}
		if err != nil {
			a.logError("probe result rejected", "probe_api", "record_check", err, "probe_id", identity.ID, "monitor_id", report.MonitorID)
			continue
		}
		accepted++
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]int{"accepted": accepted, "received": len(in.Results)})
}

func (a *App) onlineRegions(ctx context.Context) (map[string]bool, error) {
	online := map[string]bool{}
	if a.cfg.LocalProbeRegion != "" {
		online[a.cfg.LocalProbeRegion] = true
	}
	rows, err := a.db.Query(ctx, `SELECT DISTINCT region FROM probes WHERE revoked_at IS NULL AND last_seen_at > now()-$1::interval`, probeOnlineWithin.String())
	if err != nil {
		return online, err
	}
	regions, err := pgx.CollectRows(rows, pgx.RowTo[string])
	for _, region := range regions {
		online[region] = true
	}
	return online, err
}

const (
	probeOfflineAfter  = 5 * time.Minute
	probeWatchInterval = time.Minute
	probeAlertTime     = "15:04 UTC, 2 January 2006"
)

type probeTransition struct {
	ID, Name, Region string
	LastSeen         *time.Time
	Offline          bool
}

func (a *App) probeWatchWorker(ctx context.Context) {
	ticker := time.NewTicker(probeWatchInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			a.withJobLock(ctx, "monitor:probe-watch", "probe_watch", "acquire_watch_lock", a.watchProbes)
		case <-a.stop:
			return
		case <-ctx.Done():
			return
		}
	}
}

func (a *App) watchProbes(parent context.Context) {
	ctx, cancel := context.WithTimeout(parent, 30*time.Second)
	defer cancel()
	if err := a.alertProbeTransitions(ctx); err != nil {
		a.logError("probe watch failed", "probe_watch", "alert_transitions", err)
	}
}

func (a *App) alertProbeTransitions(ctx context.Context) error {
	tx, err := a.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer dbx.Rollback(ctx, tx)
	offline, err := collectTransitions(ctx, tx, true, `
		UPDATE probes SET offline_alerted_at=now()
		WHERE revoked_at IS NULL AND offline_alerted_at IS NULL AND COALESCE(last_seen_at, created_at) < now()-$1::interval
		RETURNING id::text, name, region, last_seen_at`, probeOfflineAfter.String())
	if err != nil {
		return err
	}
	online, err := collectTransitions(ctx, tx, false, `
		UPDATE probes SET offline_alerted_at=NULL
		WHERE revoked_at IS NULL AND offline_alerted_at IS NOT NULL AND last_seen_at > offline_alerted_at
		RETURNING id::text, name, region, last_seen_at`)
	if err != nil {
		return err
	}
	transitions := append(offline, online...)
	if len(transitions) == 0 {
		return nil
	}
	recipients, err := probeOperators(ctx, tx)
	if err != nil {
		return err
	}
	for _, t := range transitions {
		var peers int
		if err = tx.QueryRow(ctx, `SELECT count(*) FROM probes WHERE region=$1 AND revoked_at IS NULL AND last_seen_at > now()-$2::interval`,
			t.Region, probeOnlineWithin.String()).Scan(&peers); err != nil {
			return err
		}
		payload, err := json.Marshal(a.probeAlert(t, peers))
		if err != nil {
			return err
		}
		for _, to := range recipients {
			if _, err = tx.Exec(ctx, `INSERT INTO email_jobs(to_email,template,payload,next_attempt_at) VALUES($1,'probe_status',$2,now())`, to, payload); err != nil {
				return err
			}
		}
		a.logEvent("probe availability changed", "probe_watch", "alert_transition",
			"probe_id", t.ID, "region", t.Region, "offline", t.Offline, "recipients", len(recipients))
	}
	if err = tx.Commit(ctx); err != nil {
		return err
	}
	a.notifyEmail()
	return nil
}

func collectTransitions(ctx context.Context, tx pgx.Tx, offline bool, query string, args ...any) ([]probeTransition, error) {
	rows, err := tx.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (probeTransition, error) {
		t := probeTransition{Offline: offline}
		err := row.Scan(&t.ID, &t.Name, &t.Region, &t.LastSeen)
		return t, err
	})
}

func probeOperators(ctx context.Context, tx pgx.Tx) ([]string, error) {
	rows, err := tx.Query(ctx, `
		SELECT DISTINCT lower(i.verified_email)
		FROM access_grants g
		JOIN principals p ON p.id=g.principal_id AND p.status='active' AND p.deleted_at IS NULL
		JOIN principal_identities i ON i.principal_id=g.principal_id AND i.deleted_at IS NULL AND i.verified_email IS NOT NULL
		WHERE g.scope_type='platform' AND g.revoked_at IS NULL AND (g.expires_at IS NULL OR g.expires_at > now())
		  AND g.capability IN ($1, $2)
		ORDER BY 1`, access.ProbesManage, access.PlatformEverything)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowTo[string])
}

func (a *App) probeAlert(t probeTransition, peers int) map[string]string {
	lastSeen := "Never"
	if t.LastSeen != nil {
		lastSeen = t.LastSeen.UTC().Format(probeAlertTime)
	}
	regionState := fmt.Sprintf("%d %s online", peers, probesWord(peers))
	alert := map[string]string{
		"probe_name": t.Name, "region": t.Region, "last_seen": lastSeen, "region_state": regionState,
		"control_url": strings.TrimRight(a.cfg.DashboardOrigin, "/") + "/control/probes",
	}
	switch {
	case !t.Offline:
		alert["tone"], alert["label"] = "ok", "Probe back"
		alert["headline"] = fmt.Sprintf("The %s probe is reporting again", t.Name)
		alert["detail"] = fmt.Sprintf("Checks from %s are running again.", t.Region)
	case t.LastSeen == nil:
		alert["tone"], alert["label"] = "warn", "Probe never connected"
		alert["headline"] = fmt.Sprintf("The %s probe has not connected", t.Name)
		alert["detail"] = fmt.Sprintf("It was created more than %d minutes ago and has never claimed work. Check that it is running with its token.", int(probeOfflineAfter.Minutes()))
	case peers == 0:
		alert["tone"], alert["label"] = "crit", "Region offline"
		alert["headline"] = fmt.Sprintf("The %s probe stopped reporting", t.Name)
		alert["detail"] = fmt.Sprintf("No other probe is online in %s, so checks from that region have stopped and it cannot confirm outages.", t.Region)
	default:
		alert["tone"], alert["label"] = "warn", "Probe offline"
		alert["headline"] = fmt.Sprintf("The %s probe stopped reporting", t.Name)
		alert["detail"] = fmt.Sprintf("%s still has %d other %s online, so checks continue.", t.Region, peers, probesWord(peers))
	}
	return alert
}

func probesWord(n int) string {
	if n == 1 {
		return "probe"
	}
	return "probes"
}
