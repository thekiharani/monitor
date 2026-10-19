package app

import (
	"net/http"

	"github.com/jackc/pgx/v5"

	"github.com/thekiharani/monitor/internal/cloud/httpx"
)

type closeInput struct {
	Confirm string `json:"confirm"`
}

func (a *App) closeAccount(w http.ResponseWriter, r *http.Request) {
	p, ok := a.auth(w, r)
	if !ok {
		return
	}
	if p.Role != "owner" {
		httpx.WriteError(w, r, eToHTTP("forbidden_role"))
		return
	}
	body, err := readBody(w, r, 4<<10)
	if err != nil {
		httpx.WriteError(w, r, eToHTTP("invalid_request"))
		return
	}
	var in closeInput
	if decode(body, &in) != nil {
		httpx.WriteError(w, r, eToHTTP("invalid_request"))
		return
	}

	account := p.AccountID.String()
	var ownerEmail string
	if err = a.tenantRow(r.Context(), account,
		`SELECT COALESCE(verified_email,'') FROM principal_identities
		 WHERE principal_id=$1 AND deleted_at IS NULL LIMIT 1`,
		[]any{p.PrincipalID.String()},
		&ownerEmail); err != nil {
		httpx.WriteError(w, r, eToHTTP("service_unavailable"))
		return
	}
	if ownerEmail == "" || !httpx.ConstantTimeEqual(in.Confirm, ownerEmail) {
		httpx.WriteError(w, r, eToHTTP("confirmation_mismatch"))
		return
	}

	var subscribed bool
	if err = a.tenantRow(r.Context(), account,
		`SELECT EXISTS(SELECT 1 FROM billing_subscriptions
		 WHERE account_id=$1 AND status IN ('active','past_due','non_renewing') AND billing_mode<>'comp')`,
		[]any{account},
		&subscribed); err != nil {
		httpx.WriteError(w, r, eToHTTP("service_unavailable"))
		return
	}
	if subscribed {
		httpx.WriteError(w, r, eToHTTP("subscription_active"))
		return
	}

	statements := []string{
		`UPDATE accounts SET status='closed',deleted_at=COALESCE(deleted_at,now()),updated_at=now() WHERE id=$1`,
		`UPDATE credentials SET revoked_at=now() WHERE account_id=$1 AND revoked_at IS NULL`,
		`DELETE FROM monitor_schedules WHERE account_id=$1`,
		`UPDATE status_pages SET published=false,custom_domain=NULL,domain_verified_at=NULL,updated_at=now() WHERE account_id=$1`,
		`UPDATE account_memberships SET status='removed',deleted_at=COALESCE(deleted_at,now()),updated_at=now() WHERE account_id=$1 AND deleted_at IS NULL`,
		`UPDATE account_invites SET revoked_at=now() WHERE account_id=$1 AND accepted_at IS NULL AND revoked_at IS NULL`,
		`UPDATE billing_subscriptions SET status='canceled',grace_until=NULL,updated_at=now() WHERE account_id=$1 AND billing_mode='comp' AND status='active'`,
	}
	if err = a.withTenant(r.Context(), account, func(tx pgx.Tx) error {
		for index, statement := range statements {
			if _, execErr := tx.Exec(r.Context(), statement, account); execErr != nil {
				a.logError("account close failed", "account_api", "close_account", execErr, "account_id", account, "statement_index", index)
				return execErr
			}
		}
		_, execErr := tx.Exec(r.Context(),
			`INSERT INTO audit_events(principal_id,account_id,capability,action,target_type,target_id,reason,request_id,source_ip,user_agent)
			 VALUES($1,$2,'account.self.close','account.close','account',$3,'closed by the account owner',$4,$5,$6)`,
			p.PrincipalID.String(), account, account, httpx.RequestID(r), a.clientIPValue(r), boundedRunes(r.UserAgent(), 512))
		if execErr != nil {
			a.logError("account close audit failed", "account_api", "record_close_audit", execErr, "account_id", account)
		}
		return execErr
	}); err != nil {
		httpx.WriteError(w, r, eToHTTP("service_unavailable"))
		return
	}

	if isDashboardAPI(r) {
		http.SetCookie(w, &http.Cookie{Name: "__Host-session", Value: "", Path: "/", MaxAge: -1, Secure: true, HttpOnly: true, SameSite: http.SameSiteLaxMode})
		clearCSRF(w)
	}
	w.WriteHeader(http.StatusNoContent)
}
