package app

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json/v2"
	"errors"
	"net/http"
	"time"

	"uuid"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/thekiharani/monitor/internal/cloud/config"
	"github.com/thekiharani/monitor/internal/cloud/dbx"
	"github.com/thekiharani/monitor/internal/cloud/domain"
	"github.com/thekiharani/monitor/internal/cloud/httpx"
)

const inviteTTL = 7 * 24 * time.Hour

type inviteInput struct {
	Email string `json:"email"`
	Role  string `json:"role"`
}

type roleInput struct {
	Role string `json:"role"`
}

type memberView struct {
	PrincipalID string `json:"principal_id"`
	Email       string `json:"email"`
	DisplayName string `json:"display_name"`
	Role        string `json:"role"`
	IsYou       bool   `json:"is_you"`
	JoinedAt    any    `json:"joined_at"`
}

type inviteView struct {
	ID        string `json:"id"`
	Email     string `json:"email"`
	Role      string `json:"role"`
	ExpiresAt any    `json:"expires_at"`
	CreatedAt any    `json:"created_at"`
}

type accountView struct {
	AccountID string `json:"account_id"`
	Name      string `json:"name"`
	Role      string `json:"role"`
	Plan      string `json:"plan"`
	IsCurrent bool   `json:"is_current"`
}

func assignableRole(role string) bool { return domain.IsAssignableRole(role) }

func (a *App) canManageMembers(p Principal) bool {
	return domain.CanManageMembers(p.Role)
}

func (a *App) auditTeam(ctx context.Context, tx pgx.Tx, r *http.Request, p Principal, action, targetID, reason string) error {
	_, err := tx.Exec(ctx,
		`INSERT INTO audit_events(principal_id,account_id,capability,action,target_type,target_id,reason,request_id,source_ip,user_agent)
		 VALUES($1,$2,'account.self.team',$3,'membership',$4,$5,$6,$7,$8)`,
		p.PrincipalID.String(), p.AccountID.String(), action, targetID, reason,
		httpx.RequestID(r), a.clientIPValue(r), boundedRunes(r.UserAgent(), 512),
	)
	return err
}

func newInviteToken() (string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

func (a *App) listMembers(w http.ResponseWriter, r *http.Request) {
	p, ok := a.auth(w, r)
	if !ok {
		return
	}
	members := make([]memberView, 0, 8)
	if err := a.tenantRows(r.Context(), p.AccountID.String(),
		`SELECT m.principal_id::text,COALESCE(i.verified_email,''),COALESCE(pr.display_name,''),m.role,m.created_at
		 FROM account_memberships m
		 LEFT JOIN principals pr ON pr.id=m.principal_id
		 LEFT JOIN principal_identities i ON i.principal_id=m.principal_id AND i.deleted_at IS NULL
		 WHERE m.account_id=$1 AND m.status='active' AND m.deleted_at IS NULL
		 ORDER BY (m.role='owner') DESC,m.created_at`,
		[]any{p.AccountID.String()},
		func(rows pgx.Rows) error {
			var view memberView
			var joined *time.Time
			if scanErr := rows.Scan(&view.PrincipalID, &view.Email, &view.DisplayName, &view.Role, &joined); scanErr != nil {
				return scanErr
			}
			view.IsYou = view.PrincipalID == p.PrincipalID.String()
			view.JoinedAt = httpx.RFC3339(joined)
			members = append(members, view)
			return nil
		}); err != nil {
		httpx.WriteError(w, r, eToHTTP("service_unavailable"))
		return
	}

	invites := make([]inviteView, 0, 8)
	if err := a.tenantRows(r.Context(), p.AccountID.String(),
		`SELECT id::text,email,role,expires_at,created_at FROM account_invites
		 WHERE account_id=$1 AND accepted_at IS NULL AND revoked_at IS NULL AND expires_at>now()
		 ORDER BY created_at DESC`,
		[]any{p.AccountID.String()},
		func(rows pgx.Rows) error {
			var view inviteView
			var expires, created *time.Time
			if scanErr := rows.Scan(&view.ID, &view.Email, &view.Role, &expires, &created); scanErr != nil {
				return scanErr
			}
			view.ExpiresAt = httpx.RFC3339(expires)
			view.CreatedAt = httpx.RFC3339(created)
			invites = append(invites, view)
			return nil
		}); err != nil {
		httpx.WriteError(w, r, eToHTTP("service_unavailable"))
		return
	}

	httpx.WriteJSON(w, http.StatusOK, map[string]any{
		"data": map[string]any{
			"members":     members,
			"invites":     invites,
			"limit":       a.planLimits(p.Plan).MaxMembers,
			"your_role":   p.Role,
			"can_manage":  a.canManageMembers(p),
			"seats_taken": len(members) + len(invites),
		},
	})
}

func (a *App) inviteMember(w http.ResponseWriter, r *http.Request) {
	p, ok := a.auth(w, r)
	if !ok {
		return
	}
	if !a.canManageMembers(p) {
		httpx.WriteError(w, r, eToHTTP("forbidden_role"))
		return
	}
	body, err := readBody(w, r, 8<<10)
	if err != nil {
		httpx.WriteError(w, r, eToHTTP("invalid_request"))
		return
	}
	var in inviteInput
	if decode(body, &in) != nil {
		httpx.WriteError(w, r, eToHTTP("invalid_request"))
		return
	}
	emailValue, err := normalizedEmail(in.Email)
	if err != nil {
		httpx.WriteError(w, r, eToHTTP("invalid_request"))
		return
	}
	role := in.Role
	if role == "" {
		role = "member"
	}
	if !assignableRole(role) {
		httpx.WriteError(w, r, eToHTTP("invalid_role"))
		return
	}
	token, err := newInviteToken()
	if err != nil {
		httpx.WriteError(w, r, eToHTTP("internal_error"))
		return
	}

	var id string
	var created, expires time.Time
	err = a.withTenant(r.Context(), p.AccountID.String(), func(tx pgx.Tx) error {
		var seats int
		if scanErr := tx.QueryRow(r.Context(),
			`SELECT (SELECT count(*) FROM account_memberships WHERE account_id=$1 AND status='active' AND deleted_at IS NULL)
			      + (SELECT count(*) FROM account_invites WHERE account_id=$1 AND accepted_at IS NULL AND revoked_at IS NULL AND expires_at>now())`,
			p.AccountID.String(),
		).Scan(&seats); scanErr != nil {
			return scanErr
		}
		if seats >= a.planLimits(p.Plan).MaxMembers {
			return errPlanLimit
		}
		var alreadyMember bool
		if scanErr := tx.QueryRow(r.Context(),
			`SELECT EXISTS(
				SELECT 1 FROM account_memberships m
				JOIN principal_identities i ON i.principal_id=m.principal_id AND i.deleted_at IS NULL
				WHERE m.account_id=$1 AND m.status='active' AND m.deleted_at IS NULL AND i.verified_email=$2)`,
			p.AccountID.String(), emailValue,
		).Scan(&alreadyMember); scanErr != nil {
			return scanErr
		}
		if alreadyMember {
			return errAlreadyMember
		}
		if scanErr := tx.QueryRow(r.Context(),
			`INSERT INTO account_invites(account_id,email,role,token_hash,inviter_id,expires_at)
			 VALUES($1,$2,$3,$4,$5,now()+$6::interval) RETURNING id::text,created_at,expires_at`,
			p.AccountID.String(), emailValue, role, config.HashSecret(token), p.PrincipalID.String(), inviteTTL.String(),
		).Scan(&id, &created, &expires); scanErr != nil {
			return scanErr
		}
		payload, marshalErr := json.Marshal(map[string]any{
			"accept_url": a.cfg.AppOrigin + "/app/invite/" + token,
			"role":       role,
			"expires_at": expires.UTC().Format(time.RFC3339),
		})
		if marshalErr != nil {
			return marshalErr
		}
		_, execErr := tx.Exec(r.Context(),
			`INSERT INTO email_jobs(account_id,to_email,template,payload,next_attempt_at) VALUES($1,$2,'team_invite',$3,now())`,
			p.AccountID.String(), emailValue, payload)
		return execErr
	})
	pgErr, isPgErr := errors.AsType[*pgconn.PgError](err)
	switch {
	case errors.Is(err, errPlanLimit):
		httpx.WriteError(w, r, eToHTTP("plan_limit_exceeded"))
		return
	case errors.Is(err, errAlreadyMember):
		httpx.WriteError(w, r, eToHTTP("already_member"))
		return
	case isPgErr && pgErr.Code == "23505":
		httpx.WriteError(w, r, eToHTTP("invite_pending"))
		return
	case err != nil:
		httpx.WriteError(w, r, eToHTTP("service_unavailable"))
		return
	}
	a.notifyEmail()
	httpx.WriteJSON(w, http.StatusCreated, map[string]any{
		"data": inviteView{
			ID:        id,
			Email:     emailValue,
			Role:      role,
			ExpiresAt: httpx.RFC3339(&expires),
			CreatedAt: httpx.RFC3339(&created),
		},
	})
}

func (a *App) revokeInvite(w http.ResponseWriter, r *http.Request) {
	p, ok := a.auth(w, r)
	if !ok {
		return
	}
	if !a.canManageMembers(p) {
		httpx.WriteError(w, r, eToHTTP("forbidden_role"))
		return
	}
	affected, err := a.tenantExec(r.Context(), p.AccountID.String(),
		`UPDATE account_invites SET revoked_at=now()
		 WHERE id=$1 AND account_id=$2 AND accepted_at IS NULL AND revoked_at IS NULL`,
		r.PathValue("id"), p.AccountID.String())
	if err != nil {
		httpx.WriteError(w, r, eToHTTP("service_unavailable"))
		return
	}
	if affected == 0 {
		httpx.WriteError(w, r, eToHTTP("not_found"))
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *App) resendInvite(w http.ResponseWriter, r *http.Request) {
	p, ok := a.auth(w, r)
	if !ok {
		return
	}
	if !a.canManageMembers(p) {
		httpx.WriteError(w, r, eToHTTP("forbidden_role"))
		return
	}
	token, err := newInviteToken()
	if err != nil {
		httpx.WriteError(w, r, eToHTTP("internal_error"))
		return
	}

	err = a.withTenant(r.Context(), p.AccountID.String(), func(tx pgx.Tx) error {
		var email, role string
		var expires time.Time
		if scanErr := tx.QueryRow(r.Context(),
			`UPDATE account_invites SET token_hash=$3,expires_at=now()+$4::interval
			 WHERE id=$1 AND account_id=$2 AND accepted_at IS NULL AND revoked_at IS NULL
			 RETURNING email,role,expires_at`,
			r.PathValue("id"), p.AccountID.String(), config.HashSecret(token), inviteTTL.String(),
		).Scan(&email, &role, &expires); scanErr != nil {
			return scanErr
		}
		payload, marshalErr := json.Marshal(map[string]any{
			"accept_url": a.cfg.AppOrigin + "/app/invite/" + token,
			"role":       role,
			"expires_at": expires.UTC().Format(time.RFC3339),
		})
		if marshalErr != nil {
			return marshalErr
		}
		_, execErr := tx.Exec(r.Context(),
			`INSERT INTO email_jobs(account_id,to_email,template,payload,next_attempt_at) VALUES($1,$2,'team_invite',$3,now())`,
			p.AccountID.String(), email, payload)
		return execErr
	})
	if errors.Is(err, pgx.ErrNoRows) {
		httpx.WriteError(w, r, eToHTTP("not_found"))
		return
	}
	if err != nil {
		httpx.WriteError(w, r, eToHTTP("service_unavailable"))
		return
	}
	a.notifyEmail()
	w.WriteHeader(http.StatusNoContent)
}

func (a *App) acceptInvite(w http.ResponseWriter, r *http.Request) {
	p, ok := a.auth(w, r)
	if !ok {
		return
	}
	token := r.PathValue("token")
	if len(token) < 16 || len(token) > 128 || !ascii(token) {
		httpx.WriteError(w, r, eToHTTP("invalid_invite"))
		return
	}

	tx, err := a.db.Begin(r.Context())
	if err != nil {
		httpx.WriteError(w, r, eToHTTP("service_unavailable"))
		return
	}
	defer dbx.Rollback(r.Context(), tx)

	var inviteID, accountID, email, role string
	var accepted *time.Time
	err = tx.QueryRow(r.Context(),
		`SELECT id::text,account_id::text,email,role,accepted_at FROM account_invites
		 WHERE token_hash=$1 AND revoked_at IS NULL AND expires_at>now() FOR UPDATE`,
		config.HashSecret(token),
	).Scan(&inviteID, &accountID, &email, &role, &accepted)
	if errors.Is(err, pgx.ErrNoRows) {
		httpx.WriteError(w, r, eToHTTP("invalid_invite"))
		return
	}
	if err != nil {
		httpx.WriteError(w, r, eToHTTP("service_unavailable"))
		return
	}
	if accepted != nil {
		var joined bool
		if err = tx.QueryRow(r.Context(),
			`SELECT EXISTS(SELECT 1 FROM account_memberships
			 WHERE principal_id=$1 AND account_id=$2 AND status='active' AND deleted_at IS NULL)`,
			p.PrincipalID.String(), accountID,
		).Scan(&joined); err != nil {
			httpx.WriteError(w, r, eToHTTP("service_unavailable"))
			return
		}
		if !joined {
			httpx.WriteError(w, r, eToHTTP("invalid_invite"))
			return
		}
		httpx.WriteJSON(w, http.StatusOK, map[string]any{
			"data": map[string]any{"account_id": accountID, "role": role},
		})
		return
	}

	var signedInAs string
	if err = tx.QueryRow(r.Context(),
		`SELECT COALESCE(verified_email,'') FROM principal_identities
		 WHERE principal_id=$1 AND deleted_at IS NULL LIMIT 1`,
		p.PrincipalID.String(),
	).Scan(&signedInAs); err != nil {
		httpx.WriteError(w, r, eToHTTP("service_unavailable"))
		return
	}
	if !httpx.ConstantTimeEqual(signedInAs, email) {
		httpx.WriteError(w, r, eToHTTP("invite_wrong_account"))
		return
	}

	if _, err = tx.Exec(r.Context(),
		`INSERT INTO account_memberships(principal_id,account_id,role) VALUES($1,$2,$3)
		 ON CONFLICT(principal_id,account_id)
		 DO UPDATE SET role=EXCLUDED.role,status='active',deleted_at=NULL,updated_at=now()`,
		p.PrincipalID.String(), accountID, role,
	); err != nil {
		httpx.WriteError(w, r, eToHTTP("service_unavailable"))
		return
	}
	if _, err = tx.Exec(r.Context(),
		`UPDATE account_invites SET accepted_at=now() WHERE id=$1`, inviteID,
	); err != nil {
		httpx.WriteError(w, r, eToHTTP("service_unavailable"))
		return
	}
	if err = tx.Commit(r.Context()); err != nil {
		httpx.WriteError(w, r, eToHTTP("service_unavailable"))
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{
		"data": map[string]any{"account_id": accountID, "role": role},
	})
}

func (a *App) updateMemberRole(w http.ResponseWriter, r *http.Request) {
	p, ok := a.auth(w, r)
	if !ok {
		return
	}
	if p.Role != "owner" {
		httpx.WriteError(w, r, eToHTTP("forbidden_role"))
		return
	}
	target := r.PathValue("id")
	if target == p.PrincipalID.String() {
		httpx.WriteError(w, r, eToHTTP("cannot_change_own_role"))
		return
	}
	body, err := readBody(w, r, 4<<10)
	if err != nil {
		httpx.WriteError(w, r, eToHTTP("invalid_request"))
		return
	}
	var in roleInput
	if decode(body, &in) != nil || !assignableRole(in.Role) {
		httpx.WriteError(w, r, eToHTTP("invalid_role"))
		return
	}
	err = a.withTenant(r.Context(), p.AccountID.String(), func(tx pgx.Tx) error {
		tag, execErr := tx.Exec(r.Context(),
			`UPDATE account_memberships SET role=$3,updated_at=now()
			 WHERE principal_id=$1 AND account_id=$2 AND role<>'owner' AND status='active' AND deleted_at IS NULL`,
			target, p.AccountID.String(), in.Role)
		if execErr != nil {
			return execErr
		}
		if tag.RowsAffected() == 0 {
			return errMemberNotFound
		}
		if auditErr := a.auditTeam(r.Context(), tx, r, p, "membership.role_changed", target, "role set to "+in.Role); auditErr != nil {
			a.logError("team audit failed", "members_api", "record_role_audit", auditErr, "account_id", p.AccountID.String())
			return auditErr
		}
		return nil
	})
	if errors.Is(err, errMemberNotFound) {
		httpx.WriteError(w, r, eToHTTP("not_found"))
		return
	}
	if err != nil {
		httpx.WriteError(w, r, eToHTTP("service_unavailable"))
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *App) removeMember(w http.ResponseWriter, r *http.Request) {
	p, ok := a.auth(w, r)
	if !ok {
		return
	}
	target := r.PathValue("id")
	if !a.canManageMembers(p) && target != p.PrincipalID.String() {
		httpx.WriteError(w, r, eToHTTP("forbidden_role"))
		return
	}
	if target == p.PrincipalID.String() && p.Role == "owner" {
		httpx.WriteError(w, r, eToHTTP("owner_cannot_leave"))
		return
	}

	reason := "removed by a workspace manager"
	if target == p.PrincipalID.String() {
		reason = "left the workspace"
	}
	err := a.withTenant(r.Context(), p.AccountID.String(), func(tx pgx.Tx) error {
		tag, execErr := tx.Exec(r.Context(),
			`UPDATE account_memberships SET status='removed',deleted_at=now(),updated_at=now()
			 WHERE principal_id=$1 AND account_id=$2 AND role<>'owner' AND status='active' AND deleted_at IS NULL`,
			target, p.AccountID.String())
		if execErr != nil {
			return execErr
		}
		if tag.RowsAffected() == 0 {
			return errMemberNotFound
		}
		if _, execErr = tx.Exec(r.Context(),
			`UPDATE credentials SET revoked_at=now()
			 WHERE principal_id=$1 AND account_id=$2 AND revoked_at IS NULL`,
			target, p.AccountID.String()); execErr != nil {
			return execErr
		}
		if auditErr := a.auditTeam(r.Context(), tx, r, p, "membership.removed", target, reason); auditErr != nil {
			a.logError("team audit failed", "members_api", "record_removal_audit", auditErr, "account_id", p.AccountID.String())
			return auditErr
		}
		return nil
	})
	if errors.Is(err, errMemberNotFound) {
		httpx.WriteError(w, r, eToHTTP("not_found"))
		return
	}
	if err != nil {
		httpx.WriteError(w, r, eToHTTP("service_unavailable"))
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *App) listAccounts(w http.ResponseWriter, r *http.Request) {
	p, ok := a.auth(w, r)
	if !ok {
		return
	}
	rows, err := a.db.Query(r.Context(),
		`SELECT m.account_id::text,COALESCE(a.name,''),m.role,a.plan FROM account_memberships m
		 JOIN accounts a ON a.id=m.account_id AND a.deleted_at IS NULL
		 WHERE m.principal_id=$1 AND m.status='active' AND m.deleted_at IS NULL
		 ORDER BY (m.role='owner') DESC,m.created_at`,
		p.PrincipalID.String(),
	)
	if err != nil {
		httpx.WriteError(w, r, eToHTTP("service_unavailable"))
		return
	}
	defer rows.Close()
	out := make([]accountView, 0, 4)
	for rows.Next() {
		var view accountView
		if err = rows.Scan(&view.AccountID, &view.Name, &view.Role, &view.Plan); err != nil {
			httpx.WriteError(w, r, eToHTTP("service_unavailable"))
			return
		}
		view.IsCurrent = view.AccountID == p.AccountID.String()
		out = append(out, view)
	}
	if err = rows.Err(); err != nil {
		httpx.WriteError(w, r, eToHTTP("service_unavailable"))
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"data": out})
}

func (a *App) switchAccount(w http.ResponseWriter, r *http.Request) {
	p, ok := a.sessionAuth(w, r, true)
	if !ok {
		return
	}
	target := r.PathValue("id")
	var role string
	err := a.tenantRow(r.Context(), target,
		`SELECT m.role FROM account_memberships m
		 JOIN accounts a ON a.id=m.account_id AND a.deleted_at IS NULL
		 WHERE m.principal_id=$1 AND m.account_id=$2 AND m.status='active' AND m.deleted_at IS NULL`,
		[]any{p.PrincipalID.String(), target},
		&role)
	if errors.Is(err, pgx.ErrNoRows) {
		httpx.WriteError(w, r, eToHTTP("not_found"))
		return
	}
	if err != nil {
		httpx.WriteError(w, r, eToHTTP("service_unavailable"))
		return
	}

	secretBytes := make([]byte, 32)
	if _, err = rand.Read(secretBytes); err != nil {
		httpx.WriteError(w, r, eToHTTP("internal_error"))
		return
	}
	secret := base64.RawURLEncoding.EncodeToString(secretBytes)
	if _, err = a.tenantExec(r.Context(), target,
		`INSERT INTO credentials(id,principal_id,account_id,kind,audience,secret_hash,created_ip,user_agent,expires_at)
		 VALUES($1,$2,$3,'session','customer_session',$4,$5,$6,now()+$7::interval)`,
		uuid.NewV7().String(), p.PrincipalID.String(), target, config.HashSecret(secret),
		a.clientIPValue(r), boundedRunes(r.UserAgent(), 512), a.cfg.SessionTTL.String(),
	); err != nil {
		httpx.WriteError(w, r, eToHTTP("service_unavailable"))
		return
	}
	if p.SessionID != nil {
		_, _ = a.tenantExec(r.Context(), p.AccountID.String(),
			`UPDATE credentials SET revoked_at=now() WHERE id=$1 AND account_id=$2`,
			p.SessionID.String(), p.AccountID.String())
	}
	setSession(w, secret, a.cfg.SessionTTL)
	if err = setCSRF(w, a.cfg.SessionTTL); err != nil {
		httpx.WriteError(w, r, eToHTTP("internal_error"))
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{
		"data": map[string]any{"account_id": target, "role": role},
	})
}
