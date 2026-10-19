package app

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/thekiharani/monitor/internal/cloud/access"
	"github.com/thekiharani/monitor/internal/cloud/control"
	"github.com/thekiharani/monitor/internal/cloud/httpx"
	"github.com/thekiharani/monitor/internal/cloud/logging"
)

func (a *App) controlActor(w http.ResponseWriter, r *http.Request) (control.Actor, bool) {
	if !a.requireHost(w, r, a.cfg.APIOrigin, a.cfg.AppOrigin) {
		return control.Actor{}, false
	}
	w.Header().Set("Cache-Control", "no-store")
	if !a.checkLimit("control:"+a.clientIPString(r), a.planLimits("control")) {
		a.metrics.rateLimitRejected.Add(1)
		w.Header().Set("Retry-After", "1")
		httpx.WriteError(w, r, httpx.NewError("rate_limited"))
		return control.Actor{}, false
	}
	bearer := access.Bearer(r.Header.Get("Authorization"))
	principal, err := access.Authenticate(r.Context(), a.db, bearer, access.AudienceControlAPI)
	usingSession := bearer == ""
	if errors.Is(err, access.ErrUnauthenticated) && usingSession {
		if session, cookieErr := r.Cookie("__Host-session"); cookieErr == nil {
			principal, err = access.Authenticate(r.Context(), a.db, session.Value, access.AudienceCustomerSession)
			if err == nil {
				principal, err = a.admitSessionToControlPlane(r.Context(), principal)
			}
		}
	}
	if err != nil {
		a.writeControlError(w, r, err)
		return control.Actor{}, false
	}
	if usingSession && r.Method != http.MethodGet && r.Method != http.MethodHead && !a.csrf(w, r) {
		return control.Actor{}, false
	}
	return control.Actor{Principal: principal, RequestID: httpx.RequestID(r), SourceIP: a.clientIPString(r), UserAgent: boundedRunes(r.UserAgent(), 512)}, true
}

const controlSessionMaxAge = 12 * time.Hour

func (a *App) admitSessionToControlPlane(ctx context.Context, principal access.Principal) (access.Principal, error) {
	if principal.Kind != "human" || principal.CreatedAt.Before(time.Now().Add(-controlSessionMaxAge)) {
		return access.Principal{}, access.ErrUnauthenticated
	}
	if err := access.HasAnyGrant(ctx, a.db, principal.ID); err != nil {
		return access.Principal{}, err
	}
	principal.Audience = access.AudienceControlAPI
	return principal, nil
}

func (a *App) sessionReachesControlPlane(r *http.Request) bool {
	session, err := r.Cookie("__Host-session")
	if err != nil {
		return false
	}
	principal, err := access.Authenticate(r.Context(), a.db, session.Value, access.AudienceCustomerSession)
	if err != nil {
		return false
	}
	_, err = a.admitSessionToControlPlane(r.Context(), principal)
	return err == nil
}

func (a *App) controlSession(w http.ResponseWriter, r *http.Request) {
	actor, ok := a.controlActor(w, r)
	if !ok {
		return
	}
	capabilities, err := access.PlatformCapabilities(r.Context(), a.db, actor.Principal.ID)
	if err != nil {
		a.writeControlError(w, r, err)
		return
	}
	var email string
	_ = a.db.QueryRow(r.Context(), `
		SELECT COALESCE(verified_email,'') FROM principal_identities
		WHERE principal_id=$1 AND verified_email IS NOT NULL AND deleted_at IS NULL
		ORDER BY created_at LIMIT 1`, actor.Principal.ID).Scan(&email)
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"data": map[string]any{
		"principal_id":       actor.Principal.ID,
		"account_id":         actor.Principal.AccountID,
		"email":              email,
		"capabilities":       capabilities,
		"known_capabilities": access.KnownCapabilities(),
	}})
}

func (a *App) controlAccounts(w http.ResponseWriter, r *http.Request) {
	actor, ok := a.controlActor(w, r)
	if !ok {
		return
	}
	items, page, err := a.control.Accounts(r.Context(), actor, r.URL.Query().Get("q"), queryLimit(r), queryOffset(r))
	if err != nil {
		a.writeControlError(w, r, err)
		return
	}
	writeControlList(w, items, page)
}

func (a *App) controlAccount(w http.ResponseWriter, r *http.Request) {
	actor, ok := a.controlActor(w, r)
	if !ok {
		return
	}
	item, err := a.control.Account(r.Context(), actor, r.PathValue("id"))
	if err != nil {
		a.writeControlError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"data": item})
}

func (a *App) controlStats(w http.ResponseWriter, r *http.Request) {
	actor, ok := a.controlActor(w, r)
	if !ok {
		return
	}
	items, err := a.control.Stats(r.Context(), actor)
	if err != nil {
		a.writeControlError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"data": items})
}

func (a *App) controlAccountStatus(w http.ResponseWriter, r *http.Request) {
	actor, ok := a.controlActor(w, r)
	if !ok {
		return
	}
	var in struct {
		Status string `json:"status"`
		Reason string `json:"reason"`
	}
	if body, e := readBody(w, r, 64<<10); e != nil || decode(body, &in) != nil {
		a.writeControlError(w, r, control.ErrInvalid)
		return
	}
	if err := a.control.SetAccountStatus(r.Context(), actor, r.PathValue("id"), in.Status, in.Reason); err != nil {
		a.writeControlError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *App) controlUsage(w http.ResponseWriter, r *http.Request) {
	actor, ok := a.controlActor(w, r)
	if !ok {
		return
	}
	items, page, err := a.control.Usage(r.Context(), actor, r.URL.Query().Get("account_id"), queryLimit(r), queryOffset(r))
	if err != nil {
		a.writeControlError(w, r, err)
		return
	}
	writeControlList(w, items, page)
}

func (a *App) controlCredentials(w http.ResponseWriter, r *http.Request) {
	actor, ok := a.controlActor(w, r)
	if !ok {
		return
	}
	items, page, err := a.control.Credentials(r.Context(), actor, r.URL.Query().Get("account_id"), queryLimit(r), queryOffset(r))
	if err != nil {
		a.writeControlError(w, r, err)
		return
	}
	writeControlList(w, items, page)
}

func (a *App) controlRevokeCredential(w http.ResponseWriter, r *http.Request) {
	actor, ok := a.controlActor(w, r)
	if !ok {
		return
	}
	reason, valid := controlReason(w, r)
	if !valid {
		return
	}
	if err := a.control.RevokeCredential(r.Context(), actor, r.PathValue("id"), reason); err != nil {
		a.writeControlError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *App) controlReconcileBilling(w http.ResponseWriter, r *http.Request) {
	actor, ok := a.controlActor(w, r)
	if !ok {
		return
	}
	reason, valid := controlReason(w, r)
	if !valid {
		return
	}
	if err := a.control.ReconcileBilling(r.Context(), actor, r.PathValue("accountID"), reason); err != nil {
		a.writeControlError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *App) controlGrantComplimentary(w http.ResponseWriter, r *http.Request) {
	actor, ok := a.controlActor(w, r)
	if !ok {
		return
	}
	var in control.ComplimentaryInput
	if body, e := readBody(w, r, 64<<10); e != nil || decode(body, &in) != nil {
		a.writeControlError(w, r, control.ErrInvalid)
		return
	}
	if err := a.control.GrantComplimentaryPlan(r.Context(), actor, r.PathValue("accountID"), in); err != nil {
		a.writeControlError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *App) controlRevokeComplimentary(w http.ResponseWriter, r *http.Request) {
	actor, ok := a.controlActor(w, r)
	if !ok {
		return
	}
	reason, valid := controlReason(w, r)
	if !valid {
		return
	}
	if err := a.control.RevokeComplimentaryPlan(r.Context(), actor, r.PathValue("accountID"), reason); err != nil {
		a.writeControlError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *App) controlCollection(kind string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		actor, ok := a.controlActor(w, r)
		if !ok {
			return
		}
		filter := r.URL.Query().Get("status")
		if kind == "grants" || kind == "audit" {
			filter = r.URL.Query().Get("q")
		}
		items, page, err := a.control.Collection(r.Context(), actor, kind, filter, queryLimit(r), queryOffset(r), r.URL.Query().Get("account_id"))
		if err != nil {
			a.writeControlError(w, r, err)
			return
		}
		writeControlList(w, items, page)
	}
}

func (a *App) controlRetryJob(w http.ResponseWriter, r *http.Request) {
	actor, ok := a.controlActor(w, r)
	if !ok {
		return
	}
	reason, valid := controlReason(w, r)
	if !valid {
		return
	}
	if err := a.control.RetryJob(r.Context(), actor, r.PathValue("id"), reason); err != nil {
		a.writeControlError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *App) controlGrant(w http.ResponseWriter, r *http.Request) {
	actor, ok := a.controlActor(w, r)
	if !ok {
		return
	}
	var in control.GrantInput
	if body, e := readBody(w, r, 64<<10); e != nil || decode(body, &in) != nil {
		a.writeControlError(w, r, control.ErrInvalid)
		return
	}
	if err := a.control.Grant(r.Context(), actor, in); err != nil {
		a.writeControlError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusCreated)
}

func (a *App) controlRevokeGrant(w http.ResponseWriter, r *http.Request) {
	actor, ok := a.controlActor(w, r)
	if !ok {
		return
	}
	reason, valid := controlReason(w, r)
	if !valid {
		return
	}
	if err := a.control.RevokeGrant(r.Context(), actor, r.PathValue("id"), reason); err != nil {
		a.writeControlError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *App) writeControlError(w http.ResponseWriter, r *http.Request, err error) {
	status, code, message := http.StatusServiceUnavailable, "service_unavailable", "The service is temporarily unavailable."
	if errors.Is(err, access.ErrUnauthenticated) {
		status, code, message = http.StatusUnauthorized, "unauthorized", "Authentication is required."
	} else if errors.Is(err, access.ErrForbidden) {
		status, code, message = http.StatusForbidden, "forbidden", "You do not have permission to perform this action."
	} else if errors.Is(err, control.ErrNotFound) {
		status, code, message = http.StatusNotFound, "not_found", "The requested resource was not found."
	} else if errors.Is(err, control.ErrConflict) {
		status, code, message = http.StatusConflict, "conflict", "This plan changed. Refresh and try again."
	} else if errors.Is(err, control.ErrInvalid) {
		status, code, message = http.StatusBadRequest, "invalid_request", err.Error()
	}
	if status >= http.StatusInternalServerError {
		a.logError("control request failed", "control_api", "handle_request_error", err, append(logging.RequestAttrs(r), "status", status, "error_code", code, "path_value_id", r.PathValue("id"))...)
	}
	httpx.WriteJSON(w, status, map[string]any{"error": map[string]string{"code": code, "message": message, "request_id": httpx.RequestID(r)}})
}
func queryLimit(r *http.Request) int {
	value, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	return value
}
func queryOffset(r *http.Request) int {
	raw := r.URL.Query().Get("offset")
	if raw == "" {
		return 0
	}
	value, err := strconv.Atoi(raw)
	if err != nil {
		return -1
	}
	return value
}
func writeControlList(w http.ResponseWriter, data any, page control.Page) {
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"data": data, "pagination": page})
}
func controlReason(w http.ResponseWriter, r *http.Request) (string, bool) {
	var in struct {
		Reason string `json:"reason"`
	}
	body, e := readBody(w, r, 64<<10)
	if e != nil || decode(body, &in) != nil {
		httpx.WriteError(w, r, httpx.NewError("invalid_request"))
		return "", false
	}
	return in.Reason, true
}

func boundedRunes(value string, limit int) string {
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	return string(runes[:limit])
}

func (a *App) controlRemoveMember(w http.ResponseWriter, r *http.Request) {
	actor, ok := a.controlActor(w, r)
	if !ok {
		return
	}
	var in struct {
		Reason string `json:"reason"`
	}
	if body, e := readBody(w, r, 64<<10); e != nil || decode(body, &in) != nil {
		a.writeControlError(w, r, control.ErrInvalid)
		return
	}
	if err := a.control.RemoveMember(r.Context(), actor, r.PathValue("accountID"), r.PathValue("principalID"), in.Reason); err != nil {
		a.writeControlError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *App) controlHealth(w http.ResponseWriter, r *http.Request) {
	actor, ok := a.controlActor(w, r)
	if !ok {
		return
	}
	if err := a.control.AuthorizePlatform(r.Context(), actor, access.HealthRead); err != nil {
		a.writeControlError(w, r, err)
		return
	}
	snapshot := a.snapshotMetrics()
	snapshot["db_pool_max"] = int64(a.cfg.DBMaxConns)
	if stat := a.db.Stat(); stat != nil {
		snapshot["db_pool_acquired"] = int64(stat.AcquiredConns())
		snapshot["db_pool_idle"] = int64(stat.IdleConns())
		snapshot["db_pool_total"] = int64(stat.TotalConns())
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"data": snapshot})
}

func (a *App) controlProbes(w http.ResponseWriter, r *http.Request) {
	actor, ok := a.controlActor(w, r)
	if !ok {
		return
	}
	items, page, err := a.control.Probes(r.Context(), actor, queryLimit(r), queryOffset(r))
	if err != nil {
		a.writeControlError(w, r, err)
		return
	}
	writeControlList(w, items, page)
}

func (a *App) controlCreateProbe(w http.ResponseWriter, r *http.Request) {
	actor, ok := a.controlActor(w, r)
	if !ok {
		return
	}
	var in control.ProbeInput
	if body, e := readBody(w, r, 16<<10); e != nil || decode(body, &in) != nil {
		a.writeControlError(w, r, control.ErrInvalid)
		return
	}
	created, err := a.control.CreateProbe(r.Context(), actor, in)
	if err != nil {
		a.writeControlError(w, r, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	httpx.WriteJSON(w, http.StatusCreated, map[string]any{"data": created})
}

func (a *App) controlRevokeProbe(w http.ResponseWriter, r *http.Request) {
	actor, ok := a.controlActor(w, r)
	if !ok {
		return
	}
	reason, valid := controlReason(w, r)
	if !valid {
		return
	}
	if err := a.control.RevokeProbe(r.Context(), actor, r.PathValue("id"), reason); err != nil {
		a.writeControlError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *App) controlPlans(w http.ResponseWriter, r *http.Request) {
	actor, ok := a.controlActor(w, r)
	if !ok {
		return
	}
	plans, err := a.control.Plans(r.Context(), actor)
	if err != nil {
		a.writeControlError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"data": plans})
}

func (a *App) controlUpdatePlan(w http.ResponseWriter, r *http.Request) {
	actor, ok := a.controlActor(w, r)
	if !ok {
		return
	}
	var input control.PlanInput
	if body, err := readBody(w, r, 64<<10); err != nil || decode(body, &input) != nil {
		a.writeControlError(w, r, control.ErrInvalid)
		return
	}
	if err := a.control.UpdatePlan(r.Context(), actor, r.PathValue("plan"), input); err != nil {
		a.writeControlError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
