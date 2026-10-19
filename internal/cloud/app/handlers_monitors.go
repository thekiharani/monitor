package app

import (
	"context"
	"encoding/json/v2"
	"errors"
	"fmt"
	"net/http"
	"net/textproto"
	"slices"
	"strconv"
	"strings"
	"time"

	"uuid"

	"github.com/jackc/pgx/v5"

	"github.com/thekiharani/monitor/internal/checks"
	"github.com/thekiharani/monitor/internal/cloud/config"
	"github.com/thekiharani/monitor/internal/cloud/domain"
	"github.com/thekiharani/monitor/internal/cloud/httpx"
	"github.com/thekiharani/monitor/internal/cloud/usage"
)

const (
	defaultTimeoutMs    = 10000
	defaultCertWarnDays = 14
	maxMonitorHeaders   = 20
	maxExpectedStatuses = 20
	resultPageSize      = 50
)

type monitorInput struct {
	Name             *string            `json:"name"`
	Kind             *string            `json:"kind"`
	Target           *string            `json:"target"`
	GraceSeconds     *int               `json:"grace_seconds"`
	Method           *string            `json:"method"`
	Headers          *map[string]string `json:"headers"`
	Body             *string            `json:"body"`
	ExpectedStatuses *[]int             `json:"expected_statuses"`
	Keyword          *string            `json:"keyword"`
	KeywordAbsent    *bool              `json:"keyword_absent"`
	FollowRedirects  *bool              `json:"follow_redirects"`
	IntervalSeconds  *int               `json:"interval_seconds"`
	TimeoutMs        *int               `json:"timeout_ms"`
	Regions          *[]string          `json:"regions"`
	CertWarnDays     *int               `json:"cert_warn_days"`
	Paused           *bool              `json:"paused"`
}

type monitor struct {
	ID               string
	Name             string
	Kind             string
	Target           *string
	HeartbeatToken   *string
	GraceSeconds     int
	Method           string
	Headers          map[string]string
	Body             *string
	ExpectedStatuses []int
	Keyword          *string
	KeywordAbsent    bool
	FollowRedirects  bool
	IntervalSeconds  int
	TimeoutMs        int
	Regions          []string
	CertWarnDays     int
	State            string
	StateChangedAt   time.Time
	LastCheckedAt    *time.Time
	LastStatusCode   *int
	LastResponseMs   *float64
	LastError        *string
	CertExpiresAt    *time.Time
	PausedAt         *time.Time
	CreatedAt        time.Time
	UpdatedAt        time.Time
}

const monitorColumns = `id::text,name,kind,target,heartbeat_token,grace_seconds,method,request_encrypted,expected_statuses,keyword,keyword_absent,follow_redirects,
	interval_seconds,timeout_ms,regions,cert_warn_days,state,state_changed_at,last_checked_at,last_status_code,last_response_ms,
	last_error,cert_expires_at,paused_at,created_at,updated_at`

func scanMonitor(row pgx.Row, key []byte) (monitor, error) {
	var m monitor
	var statuses []int16
	var statusCode *int16
	var responseMs *float32
	var sealed *string
	err := row.Scan(&m.ID, &m.Name, &m.Kind, &m.Target, &m.HeartbeatToken, &m.GraceSeconds, &m.Method, &sealed, &statuses, &m.Keyword, &m.KeywordAbsent,
		&m.FollowRedirects, &m.IntervalSeconds, &m.TimeoutMs, &m.Regions, &m.CertWarnDays, &m.State, &m.StateChangedAt,
		&m.LastCheckedAt, &statusCode, &responseMs, &m.LastError, &m.CertExpiresAt, &m.PausedAt, &m.CreatedAt, &m.UpdatedAt)
	m.ExpectedStatuses = make([]int, 0, len(statuses))
	for _, s := range statuses {
		m.ExpectedStatuses = append(m.ExpectedStatuses, int(s))
	}
	if statusCode != nil {
		code := int(*statusCode)
		m.LastStatusCode = &code
	}
	if responseMs != nil {
		value := float64(*responseMs)
		m.LastResponseMs = &value
	}
	if err != nil {
		return m, err
	}
	request, err := openMonitorRequest(key, sealed)
	m.Headers, m.Body = request.Headers, request.Body
	return m, err
}

type monitorRequest struct {
	Headers map[string]string `json:"headers,omitempty"`
	Body    *string           `json:"body,omitempty"`
}

const maskedHeaderValue = "********"

func sealMonitorRequest(key []byte, headers map[string]string, body *string) (*string, error) {
	if len(headers) == 0 && body == nil {
		return nil, nil
	}
	plain, err := json.Marshal(monitorRequest{Headers: headers, Body: body})
	if err != nil {
		return nil, err
	}
	sealed, err := config.Seal(key, string(plain))
	if err != nil {
		return nil, err
	}
	return &sealed, nil
}

func openMonitorRequest(key []byte, sealed *string) (monitorRequest, error) {
	request := monitorRequest{Headers: map[string]string{}}
	if sealed == nil {
		return request, nil
	}
	plain, err := config.Open(key, *sealed)
	if err != nil {
		return request, err
	}
	if err = json.Unmarshal([]byte(plain), &request); err != nil {
		return request, err
	}
	if request.Headers == nil {
		request.Headers = map[string]string{}
	}
	return request, nil
}

func sensitiveHeader(name string) bool {
	lower := strings.ToLower(name)
	switch lower {
	case "authorization", "proxy-authorization", "cookie":
		return true
	}
	for _, marker := range []string{"token", "secret", "key", "auth", "password", "signature", "session", "credential"} {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}

func maskedHeaders(headers map[string]string) map[string]string {
	out := make(map[string]string, len(headers))
	for name, value := range headers {
		if sensitiveHeader(name) && value != "" {
			value = maskedHeaderValue
		}
		out[name] = value
	}
	return out
}

func (m monitor) view(uptime map[string]float64, pingBase string) map[string]any {
	out := map[string]any{
		"id":                m.ID,
		"name":              m.Name,
		"kind":              m.Kind,
		"target":            m.Target,
		"grace_seconds":     m.GraceSeconds,
		"method":            m.Method,
		"headers":           maskedHeaders(m.Headers),
		"body":              m.Body,
		"expected_statuses": m.ExpectedStatuses,
		"keyword":           m.Keyword,
		"keyword_absent":    m.KeywordAbsent,
		"follow_redirects":  m.FollowRedirects,
		"interval_seconds":  m.IntervalSeconds,
		"timeout_ms":        m.TimeoutMs,
		"regions":           m.Regions,
		"cert_warn_days":    m.CertWarnDays,
		"state":             m.State,
		"state_changed_at":  httpx.RFC3339(&m.StateChangedAt),
		"last_checked_at":   httpx.RFC3339(m.LastCheckedAt),
		"last_status_code":  m.LastStatusCode,
		"last_response_ms":  m.LastResponseMs,
		"last_error":        m.LastError,
		"cert_expires_at":   httpx.RFC3339(m.CertExpiresAt),
		"paused":            m.PausedAt != nil,
		"created_at":        httpx.RFC3339(&m.CreatedAt),
		"updated_at":        httpx.RFC3339(&m.UpdatedAt),
	}
	if uptime != nil {
		out["uptime"] = uptime
	}
	if m.HeartbeatToken != nil {
		out["ping_url"] = strings.TrimRight(pingBase, "/") + "/ping/" + *m.HeartbeatToken
	}
	return out
}

func invalidMonitor(message string) httpx.Error {
	return httpx.Error{Code: "invalid_monitor", Message: message, Status: http.StatusUnprocessableEntity}
}

func (a *App) applyMonitorInput(m *monitor, in monitorInput, limits usage.Limits, creating bool) error {
	if in.Kind != nil {
		if !creating && *in.Kind != m.Kind {
			return invalidMonitor("A monitor's kind cannot change; create a new monitor instead.")
		}
		m.Kind = *in.Kind
	}
	if !domain.IsMonitorKind(m.Kind) {
		return invalidMonitor("kind must be http, keyword, tcp or heartbeat.")
	}
	if in.Name != nil {
		m.Name = strings.TrimSpace(*in.Name)
	}
	if n := len([]rune(m.Name)); n < 1 || n > 128 {
		return invalidMonitor("name must be 1 to 128 characters.")
	}
	if m.Kind == domain.MonitorKindHeartbeat {
		return a.applyHeartbeatInput(m, in)
	}
	if in.Target != nil {
		target, err := checks.NormalizeTarget(m.Kind, *in.Target)
		if err != nil {
			return invalidMonitor(capitalize(err.Error()) + ".")
		}
		m.Target = &target
	}
	if m.Target == nil {
		return invalidMonitor("target is required.")
	}
	if in.Method != nil {
		m.Method = strings.ToUpper(strings.TrimSpace(*in.Method))
	}
	if m.Method == "" {
		m.Method = http.MethodGet
	}
	if !slices.Contains(checks.Methods, m.Method) {
		return invalidMonitor("method must be one of " + strings.Join(checks.Methods, ", ") + ".")
	}
	if in.Headers != nil {
		previous := m.Headers
		headers := make(map[string]string, len(*in.Headers))
		for name, value := range *in.Headers {
			canonical := textproto.CanonicalMIMEHeaderKey(strings.TrimSpace(name))
			if canonical == "" || len(canonical) > 128 || !headerToken(canonical) || len(value) > 1024 || strings.ContainsAny(value, "\r\n") {
				return invalidMonitor(fmt.Sprintf("header %q is not a valid header.", name))
			}
			if value == maskedHeaderValue {
				kept, ok := previous[canonical]
				if !ok {
					return invalidMonitor(fmt.Sprintf("header %q needs a value.", canonical))
				}
				value = kept
			}
			headers[canonical] = value
		}
		if len(headers) > maxMonitorHeaders {
			return invalidMonitor(fmt.Sprintf("A monitor sends at most %d headers.", maxMonitorHeaders))
		}
		m.Headers = headers
	}
	if in.Body != nil {
		body := *in.Body
		if len(body) > 16384 {
			return invalidMonitor("body must be at most 16384 characters.")
		}
		m.Body = &body
		if body == "" {
			m.Body = nil
		}
	}
	if in.ExpectedStatuses != nil {
		statuses := slices.Clone(*in.ExpectedStatuses)
		slices.Sort(statuses)
		statuses = slices.Compact(statuses)
		if len(statuses) > maxExpectedStatuses {
			return invalidMonitor(fmt.Sprintf("List at most %d expected status codes.", maxExpectedStatuses))
		}
		for _, code := range statuses {
			if code < 100 || code > 599 {
				return invalidMonitor("expected_statuses must be HTTP status codes from 100 to 599.")
			}
		}
		m.ExpectedStatuses = statuses
	}
	if in.Keyword != nil {
		keyword := *in.Keyword
		m.Keyword = &keyword
	}
	if m.Kind == domain.MonitorKindKeyword {
		if m.Keyword == nil || *m.Keyword == "" || len(*m.Keyword) > 256 {
			return invalidMonitor("A keyword monitor needs a keyword of 1 to 256 characters.")
		}
	} else {
		m.Keyword = nil
	}
	if in.KeywordAbsent != nil {
		m.KeywordAbsent = *in.KeywordAbsent
	}
	if in.FollowRedirects != nil {
		m.FollowRedirects = *in.FollowRedirects
	}
	if in.IntervalSeconds != nil {
		m.IntervalSeconds = *in.IntervalSeconds
	}
	if m.IntervalSeconds == 0 {
		m.IntervalSeconds = limits.MinIntervalSeconds
	}
	if m.IntervalSeconds < limits.MinIntervalSeconds {
		return httpx.Error{Code: "plan_limit_exceeded", Message: fmt.Sprintf("Your plan checks at most every %d seconds.", limits.MinIntervalSeconds), Status: http.StatusTooManyRequests}
	}
	if m.IntervalSeconds > 86400 {
		return invalidMonitor("interval_seconds must be at most 86400.")
	}
	if in.TimeoutMs != nil {
		m.TimeoutMs = *in.TimeoutMs
	}
	if m.TimeoutMs == 0 {
		m.TimeoutMs = defaultTimeoutMs
	}
	if m.TimeoutMs < 1000 || m.TimeoutMs > 60000 || m.TimeoutMs >= m.IntervalSeconds*1000 {
		return invalidMonitor("timeout_ms must be 1000 to 60000 and shorter than the interval.")
	}
	if in.Regions != nil {
		regions := slices.Clone(*in.Regions)
		slices.Sort(regions)
		m.Regions = slices.Compact(regions)
	}
	if len(m.Regions) == 0 {
		m.Regions = []string{a.defaultRegion()}
	}
	for _, region := range m.Regions {
		if !slices.Contains(a.cfg.ProbeRegions, region) {
			return invalidMonitor(fmt.Sprintf("region %q is not available; choose from %s.", region, strings.Join(a.cfg.ProbeRegions, ", ")))
		}
	}
	if len(m.Regions) > limits.MaxRegions {
		return httpx.Error{Code: "plan_limit_exceeded", Message: fmt.Sprintf("Your plan checks from at most %d region(s).", limits.MaxRegions), Status: http.StatusTooManyRequests}
	}
	if in.CertWarnDays != nil {
		m.CertWarnDays = *in.CertWarnDays
	}
	if m.CertWarnDays < 0 || m.CertWarnDays > 365 {
		return invalidMonitor("cert_warn_days must be 0 to 365.")
	}
	return nil
}

func (a *App) applyHeartbeatInput(m *monitor, in monitorInput) error {
	if in.Target != nil && *in.Target != "" {
		return invalidMonitor("A heartbeat monitor has no target; your job calls its ping URL instead.")
	}
	m.Target, m.Regions, m.Keyword = nil, []string{}, nil
	if in.IntervalSeconds != nil {
		m.IntervalSeconds = *in.IntervalSeconds
	}
	if m.IntervalSeconds < 60 || m.IntervalSeconds > 86400 {
		return invalidMonitor("A heartbeat's interval_seconds is how often your job pings, from 60 to 86400.")
	}
	if in.GraceSeconds != nil {
		m.GraceSeconds = *in.GraceSeconds
	}
	if m.GraceSeconds < 0 || m.GraceSeconds > 86400 {
		return invalidMonitor("grace_seconds must be 0 to 86400.")
	}
	if m.TimeoutMs == 0 {
		m.TimeoutMs = defaultTimeoutMs
	}
	if m.HeartbeatToken == nil {
		token, err := randomToken(24)
		if err != nil {
			return err
		}
		m.HeartbeatToken = &token
	}
	return nil
}

func (a *App) defaultRegion() string {
	if a.cfg.LocalProbeRegion != "" {
		return a.cfg.LocalProbeRegion
	}
	return a.cfg.ProbeRegions[0]
}

func headerToken(name string) bool {
	for _, r := range name {
		if !(r == '-' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9') {
			return false
		}
	}
	return true
}

func capitalize(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

func writeMonitorError(w http.ResponseWriter, r *http.Request, err error) {
	if e, ok := errors.AsType[httpx.Error](err); ok {
		httpx.WriteError(w, r, e)
		return
	}
	if errors.Is(err, pgx.ErrNoRows) {
		httpx.WriteError(w, r, eToHTTP("monitor_not_found"))
		return
	}
	if errors.Is(err, errPlanLimit) {
		httpx.WriteError(w, r, eToHTTP("plan_limit_exceeded"))
		return
	}
	httpx.WriteError(w, r, eToHTTP("service_unavailable"))
}

func (a *App) listMonitors(w http.ResponseWriter, r *http.Request) {
	p, ok := a.auth(w, r)
	if !ok {
		return
	}
	items := make([]map[string]any, 0)
	if err := a.readTenant(r.Context(), p.AccountID.String(), func(tx pgx.Tx) error {
		uptime, err := uptimeByMonitor(r.Context(), tx, p.AccountID.String())
		if err != nil {
			return err
		}
		rows, err := tx.Query(r.Context(), `SELECT `+monitorColumns+` FROM monitors WHERE account_id=$1 AND deleted_at IS NULL ORDER BY created_at DESC`, p.AccountID.String())
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			m, err := scanMonitor(rows, a.cfg.AppEncryptionKey)
			if err != nil {
				return err
			}
			items = append(items, m.view(uptime[m.ID], a.cfg.APIOrigin))
		}
		return rows.Err()
	}); err != nil {
		a.logError("monitor list failed", "monitor_api", "list_monitors", err, "account_id", p.AccountID.String())
		writeMonitorError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"data": items})
}

func uptimeByMonitor(ctx context.Context, tx pgx.Tx, accountID string) (map[string]map[string]float64, error) {
	out := map[string]map[string]float64{}
	rows, err := tx.Query(ctx, `
		SELECT monitor_id::text,
			sum(checks) FILTER (WHERE day >= current_date - 1), sum(failures) FILTER (WHERE day >= current_date - 1),
			sum(checks) FILTER (WHERE day >= current_date - 6), sum(failures) FILTER (WHERE day >= current_date - 6),
			sum(checks), sum(failures)
		FROM monitor_daily WHERE account_id=$1 AND day >= current_date - 29
		GROUP BY monitor_id`, accountID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		var c1, f1, c7, f7, c30, f30 *int64
		if err = rows.Scan(&id, &c1, &f1, &c7, &f7, &c30, &f30); err != nil {
			return nil, err
		}
		out[id] = map[string]float64{}
		for label, pair := range map[string][2]*int64{"1d": {c1, f1}, "7d": {c7, f7}, "30d": {c30, f30}} {
			if pair[0] != nil && *pair[0] > 0 {
				out[id][label] = 100 * float64(*pair[0]-*pair[1]) / float64(*pair[0])
			}
		}
	}
	return out, rows.Err()
}

func (a *App) createMonitor(w http.ResponseWriter, r *http.Request) {
	p, ok := a.auth(w, r)
	if !ok {
		return
	}
	var in monitorInput
	body, err := readBody(w, r, 64<<10)
	if err != nil || decode(body, &in) != nil {
		httpx.WriteError(w, r, eToHTTP("invalid_json"))
		return
	}
	limits := a.planLimits(p.Plan)
	m := monitor{Method: http.MethodGet, FollowRedirects: true, CertWarnDays: defaultCertWarnDays, Headers: map[string]string{}, ExpectedStatuses: []int{}, State: domain.MonitorStatePending}
	if in.Kind == nil {
		httpx.WriteError(w, r, invalidMonitor("kind is required."))
		return
	}
	if err = a.applyMonitorInput(&m, in, limits, true); err != nil {
		writeMonitorError(w, r, err)
		return
	}
	paused := in.Paused != nil && *in.Paused
	if paused {
		m.State = domain.MonitorStatePaused
	}
	var created monitor
	err = a.withTenant(r.Context(), p.AccountID.String(), func(tx pgx.Tx) error {
		var count int
		if err := tx.QueryRow(r.Context(), `SELECT count(*) FROM monitors WHERE account_id=$1 AND deleted_at IS NULL`, p.AccountID.String()).Scan(&count); err != nil {
			return err
		}
		if count >= limits.MaxMonitors {
			return errPlanLimit
		}
		sealed, err := sealMonitorRequest(a.cfg.AppEncryptionKey, m.Headers, m.Body)
		if err != nil {
			return err
		}
		created, err = scanMonitor(tx.QueryRow(r.Context(), `
			INSERT INTO monitors(account_id,name,kind,target,method,request_encrypted,expected_statuses,keyword,keyword_absent,follow_redirects,
				interval_seconds,timeout_ms,regions,cert_warn_days,state,paused_at,heartbeat_token,grace_seconds)
			VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,CASE WHEN $16 THEN now() END,$17,$18)
			RETURNING `+monitorColumns,
			p.AccountID.String(), m.Name, m.Kind, m.Target, m.Method, sealed, m.ExpectedStatuses, m.Keyword, m.KeywordAbsent,
			m.FollowRedirects, m.IntervalSeconds, m.TimeoutMs, m.Regions, m.CertWarnDays, m.State, paused, m.HeartbeatToken, m.GraceSeconds), a.cfg.AppEncryptionKey)
		if err != nil {
			return err
		}
		if !paused && created.Kind != domain.MonitorKindHeartbeat {
			return syncSchedules(r.Context(), tx, created.ID, p.AccountID.String(), created.Regions)
		}
		return nil
	})
	if err != nil {
		if !errors.Is(err, errPlanLimit) {
			a.logError("monitor create failed", "monitor_api", "create_monitor", err, "account_id", p.AccountID.String())
		}
		writeMonitorError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusCreated, map[string]any{"data": created.view(nil, a.cfg.APIOrigin)})
}

func syncSchedules(ctx context.Context, tx pgx.Tx, monitorID, accountID string, regions []string) error {
	if _, err := tx.Exec(ctx, `DELETE FROM monitor_schedules WHERE monitor_id=$1 AND NOT (region = ANY($2))`, monitorID, regions); err != nil {
		return err
	}
	_, err := tx.Exec(ctx, `
		INSERT INTO monitor_schedules(monitor_id,account_id,region,next_run_at)
		SELECT $1,$2,region,now() FROM unnest($3::text[]) AS region
		ON CONFLICT (monitor_id,region) DO NOTHING`, monitorID, accountID, regions)
	return err
}

func monitorID(r *http.Request) (string, bool) {
	id := r.PathValue("id")
	_, err := uuid.Parse(id)
	return id, err == nil
}

func (a *App) getMonitor(w http.ResponseWriter, r *http.Request) {
	p, ok := a.auth(w, r)
	if !ok {
		return
	}
	id, valid := monitorID(r)
	if !valid {
		httpx.WriteError(w, r, eToHTTP("monitor_not_found"))
		return
	}
	var out map[string]any
	if err := a.readTenant(r.Context(), p.AccountID.String(), func(tx pgx.Tx) error {
		m, err := scanMonitor(tx.QueryRow(r.Context(), `SELECT `+monitorColumns+` FROM monitors WHERE id=$1 AND account_id=$2 AND deleted_at IS NULL`, id, p.AccountID.String()), a.cfg.AppEncryptionKey)
		if err != nil {
			return err
		}
		uptime, err := uptimeByMonitor(r.Context(), tx, p.AccountID.String())
		if err != nil {
			return err
		}
		out = m.view(uptime[m.ID], a.cfg.APIOrigin)
		return nil
	}); err != nil {
		writeMonitorError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"data": out})
}

func (a *App) updateMonitor(w http.ResponseWriter, r *http.Request) {
	p, ok := a.auth(w, r)
	if !ok {
		return
	}
	id, valid := monitorID(r)
	if !valid {
		httpx.WriteError(w, r, eToHTTP("monitor_not_found"))
		return
	}
	var in monitorInput
	body, err := readBody(w, r, 64<<10)
	if err != nil || decode(body, &in) != nil {
		httpx.WriteError(w, r, eToHTTP("invalid_json"))
		return
	}
	limits := a.planLimits(p.Plan)
	var updated monitor
	err = a.withTenant(r.Context(), p.AccountID.String(), func(tx pgx.Tx) error {
		m, err := scanMonitor(tx.QueryRow(r.Context(), `SELECT `+monitorColumns+` FROM monitors WHERE id=$1 AND account_id=$2 AND deleted_at IS NULL FOR UPDATE`, id, p.AccountID.String()), a.cfg.AppEncryptionKey)
		if err != nil {
			return err
		}
		if err = a.applyMonitorInput(&m, in, limits, false); err != nil {
			return err
		}
		pausing := in.Paused != nil && *in.Paused && m.PausedAt == nil
		resuming := in.Paused != nil && !*in.Paused && m.PausedAt != nil
		state := m.State
		switch {
		case pausing:
			state = domain.MonitorStatePaused
		case resuming:
			state = domain.MonitorStatePending
		}
		sealed, err := sealMonitorRequest(a.cfg.AppEncryptionKey, m.Headers, m.Body)
		if err != nil {
			return err
		}
		updated, err = scanMonitor(tx.QueryRow(r.Context(), `
			UPDATE monitors SET name=$3,target=$4,method=$5,request_encrypted=$6,expected_statuses=$7,keyword=$8,keyword_absent=$9,
				follow_redirects=$10,interval_seconds=$11,timeout_ms=$12,regions=$13,cert_warn_days=$14,grace_seconds=$16,
				state=$15::varchar,state_changed_at=CASE WHEN state<>$15::varchar THEN now() ELSE state_changed_at END,
				paused_at=CASE WHEN $15::varchar='paused' THEN COALESCE(paused_at,now()) ELSE NULL END,updated_at=now()
			WHERE id=$1 AND account_id=$2
			RETURNING `+monitorColumns,
			id, p.AccountID.String(), m.Name, m.Target, m.Method, sealed, m.ExpectedStatuses, m.Keyword, m.KeywordAbsent,
			m.FollowRedirects, m.IntervalSeconds, m.TimeoutMs, m.Regions, m.CertWarnDays, state, m.GraceSeconds), a.cfg.AppEncryptionKey)
		if err != nil {
			return err
		}
		if updated.PausedAt != nil || updated.Kind == domain.MonitorKindHeartbeat {
			_, err = tx.Exec(r.Context(), `DELETE FROM monitor_schedules WHERE monitor_id=$1`, id)
			return err
		}
		return syncSchedules(r.Context(), tx, id, p.AccountID.String(), updated.Regions)
	})
	if err != nil {
		if _, isHTTP := errors.AsType[httpx.Error](err); !isHTTP && !errors.Is(err, pgx.ErrNoRows) {
			a.logError("monitor update failed", "monitor_api", "update_monitor", err, "monitor_id", id)
		}
		writeMonitorError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"data": updated.view(nil, a.cfg.APIOrigin)})
}

func (a *App) deleteMonitor(w http.ResponseWriter, r *http.Request) {
	p, ok := a.auth(w, r)
	if !ok {
		return
	}
	id, valid := monitorID(r)
	if !valid {
		httpx.WriteError(w, r, eToHTTP("monitor_not_found"))
		return
	}
	var affected int64
	if err := a.withTenant(r.Context(), p.AccountID.String(), func(tx pgx.Tx) error {
		tag, err := tx.Exec(r.Context(), `UPDATE monitors SET deleted_at=now(),updated_at=now() WHERE id=$1 AND account_id=$2 AND deleted_at IS NULL`, id, p.AccountID.String())
		if err != nil {
			return err
		}
		affected = tag.RowsAffected()
		_, err = tx.Exec(r.Context(), `DELETE FROM monitor_schedules WHERE monitor_id=$1`, id)
		return err
	}); err != nil {
		writeMonitorError(w, r, err)
		return
	}
	if affected == 0 {
		httpx.WriteError(w, r, eToHTTP("monitor_not_found"))
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *App) monitorResults(w http.ResponseWriter, r *http.Request) {
	p, ok := a.auth(w, r)
	if !ok {
		return
	}
	id, valid := monitorID(r)
	if !valid {
		httpx.WriteError(w, r, eToHTTP("monitor_not_found"))
		return
	}
	limit := resultPageSize
	if raw := r.URL.Query().Get("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 || n > 500 {
			httpx.WriteError(w, r, eToHTTP("invalid_request"))
			return
		}
		limit = n
	}
	before := time.Now().Add(time.Minute)
	if raw := r.URL.Query().Get("before"); raw != "" {
		t, err := time.Parse(time.RFC3339Nano, raw)
		if err != nil {
			httpx.WriteError(w, r, eToHTTP("invalid_request"))
			return
		}
		before = t
	}
	region := r.URL.Query().Get("region")
	items := make([]map[string]any, 0, limit)
	if err := a.readTenant(r.Context(), p.AccountID.String(), func(tx pgx.Tx) error {
		if err := requireMonitor(r.Context(), tx, id, p.AccountID.String()); err != nil {
			return err
		}
		rows, err := tx.Query(r.Context(), `
			SELECT region,checked_at,up,status_code,total_ms,dns_ms,connect_ms,tls_ms,ttfb_ms,error,cert_expires_at
			FROM check_results
			WHERE monitor_id=$1 AND account_id=$2 AND checked_at<$3 AND checked_at>$3-interval '31 days' AND ($4='' OR region=$4)
			ORDER BY checked_at DESC LIMIT $5`, id, p.AccountID.String(), before, region, limit)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var regionName string
			var checkedAt time.Time
			var up bool
			var status *int16
			var total, dns, connect, tlsMs, ttfb *float32
			var message *string
			var certExpires *time.Time
			if err = rows.Scan(&regionName, &checkedAt, &up, &status, &total, &dns, &connect, &tlsMs, &ttfb, &message, &certExpires); err != nil {
				return err
			}
			items = append(items, map[string]any{
				"region": regionName, "checked_at": checkedAt.UTC().Format(time.RFC3339Nano), "up": up, "status_code": status,
				"total_ms": total, "dns_ms": dns, "connect_ms": connect, "tls_ms": tlsMs, "ttfb_ms": ttfb,
				"error": message, "cert_expires_at": httpx.RFC3339(certExpires),
			})
		}
		return rows.Err()
	}); err != nil {
		writeMonitorError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"data": items})
}

func requireMonitor(ctx context.Context, tx pgx.Tx, id, accountID string) error {
	var found bool
	return tx.QueryRow(ctx, `SELECT true FROM monitors WHERE id=$1 AND account_id=$2 AND deleted_at IS NULL`, id, accountID).Scan(&found)
}

var seriesRanges = map[string]struct {
	window, bucket time.Duration
}{
	"1h":  {time.Hour, time.Minute},
	"24h": {24 * time.Hour, 15 * time.Minute},
	"7d":  {7 * 24 * time.Hour, 2 * time.Hour},
	"30d": {30 * 24 * time.Hour, 6 * time.Hour},
}

func (a *App) monitorSeries(w http.ResponseWriter, r *http.Request) {
	p, ok := a.auth(w, r)
	if !ok {
		return
	}
	id, valid := monitorID(r)
	if !valid {
		httpx.WriteError(w, r, eToHTTP("monitor_not_found"))
		return
	}
	name := r.URL.Query().Get("range")
	if name == "" {
		name = "24h"
	}
	window, known := seriesRanges[name]
	if !known {
		httpx.WriteError(w, r, eToHTTP("invalid_request"))
		return
	}
	points := make([]map[string]any, 0)
	if err := a.readTenant(r.Context(), p.AccountID.String(), func(tx pgx.Tx) error {
		if err := requireMonitor(r.Context(), tx, id, p.AccountID.String()); err != nil {
			return err
		}
		rows, err := tx.Query(r.Context(), `
			SELECT date_bin($4::interval, checked_at, 'epoch'::timestamptz) AS bucket, region,
				count(*), count(*) FILTER (WHERE NOT up), avg(total_ms) FILTER (WHERE up), max(total_ms) FILTER (WHERE up)
			FROM check_results
			WHERE monitor_id=$1 AND account_id=$2 AND checked_at>now()-$3::interval
			GROUP BY bucket, region ORDER BY bucket, region`,
			id, p.AccountID.String(), window.window.String(), window.bucket.String())
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var bucket time.Time
			var region string
			var count, failures int64
			var avg, peak *float64
			if err = rows.Scan(&bucket, &region, &count, &failures, &avg, &peak); err != nil {
				return err
			}
			points = append(points, map[string]any{
				"t": bucket.UTC().Format(time.RFC3339), "region": region, "checks": count, "failures": failures,
				"avg_ms": avg, "max_ms": peak,
			})
		}
		return rows.Err()
	}); err != nil {
		writeMonitorError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"data": map[string]any{"range": name, "bucket_seconds": int(window.bucket.Seconds()), "points": points}})
}

func (a *App) monitorDaily(w http.ResponseWriter, r *http.Request) {
	p, ok := a.auth(w, r)
	if !ok {
		return
	}
	id, valid := monitorID(r)
	if !valid {
		httpx.WriteError(w, r, eToHTTP("monitor_not_found"))
		return
	}
	retention := a.planLimits(p.Plan).RetentionDays
	days := min(90, retention)
	if raw := r.URL.Query().Get("days"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 || n > retention {
			httpx.WriteError(w, r, eToHTTP("invalid_request"))
			return
		}
		days = n
	}
	items := make([]map[string]any, 0, days)
	if err := a.readTenant(r.Context(), p.AccountID.String(), func(tx pgx.Tx) error {
		if err := requireMonitor(r.Context(), tx, id, p.AccountID.String()); err != nil {
			return err
		}
		rows, err := tx.Query(r.Context(), `
			SELECT day,checks,failures,CASE WHEN response_count>0 THEN response_ms_sum/response_count END
			FROM monitor_daily WHERE monitor_id=$1 AND account_id=$2 AND day>current_date-$3::int
			ORDER BY day`, id, p.AccountID.String(), days)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var day time.Time
			var checksRun, failures int
			var avg *float64
			if err = rows.Scan(&day, &checksRun, &failures, &avg); err != nil {
				return err
			}
			var uptime *float64
			if checksRun > 0 {
				value := 100 * float64(checksRun-failures) / float64(checksRun)
				uptime = &value
			}
			items = append(items, map[string]any{"day": day.Format(time.DateOnly), "checks": checksRun, "failures": failures, "uptime": uptime, "avg_ms": avg})
		}
		return rows.Err()
	}); err != nil {
		writeMonitorError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"data": map[string]any{"days": days, "items": items}})
}

func (a *App) listRegions(w http.ResponseWriter, r *http.Request) {
	if _, ok := a.auth(w, r); !ok {
		return
	}
	live, err := a.onlineRegions(r.Context())
	if err != nil {
		a.logError("online regions unavailable", "monitors_api", "list_regions", err)
	}
	online := make([]string, 0, len(a.cfg.ProbeRegions))
	for _, region := range a.cfg.ProbeRegions {
		if live[region] {
			online = append(online, region)
		}
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"data": map[string]any{"regions": a.cfg.ProbeRegions, "default": a.defaultRegion(), "online": online}})
}
