package app

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"fmt"
	"math/big"
	"net/http"
	"strings"
	"time"
	"unicode"

	"uuid"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/thekiharani/monitor/internal/cloud/dbx"
	"github.com/thekiharani/monitor/internal/cloud/email"
	"github.com/thekiharani/monitor/internal/cloud/httpx"
)

const (
	maxProfileNameRunes    = 128
	maxEmailChangeAttempts = 3
	uniqueViolation        = "23505"
)

type profileInput struct {
	DisplayName *string `json:"display_name"`
	Timezone    *string `json:"timezone"`
}

type workspaceInput struct {
	Name *string `json:"name"`
}

type emailChangeInput struct {
	Email string `json:"email"`
}

type emailVerifyInput struct {
	ChallengeID string `json:"challenge_id"`
	Email       string `json:"email"`
	Code        string `json:"code"`
}

func normalizedProfileName(value string) (*string, error) {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return nil, nil
	}
	count := 0
	for _, r := range trimmed {
		if unicode.IsControl(r) {
			return nil, fmt.Errorf("name")
		}
		count++
	}
	if count > maxProfileNameRunes {
		return nil, fmt.Errorf("name")
	}
	return &trimmed, nil
}

func normalizedTimezone(value string) (*string, error) {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return nil, nil
	}
	if len(trimmed) > 64 || trimmed == "Local" || strings.Contains(trimmed, "..") || strings.HasPrefix(trimmed, "/") {
		return nil, fmt.Errorf("timezone")
	}
	if _, err := time.LoadLocation(trimmed); err != nil {
		return nil, fmt.Errorf("timezone")
	}
	return &trimmed, nil
}

func (a *App) updateProfile(w http.ResponseWriter, r *http.Request) {
	p, ok := a.auth(w, r)
	if !ok {
		return
	}
	body, err := readBody(w, r, 4<<10)
	if err != nil {
		httpx.WriteError(w, r, eToHTTP("invalid_request"))
		return
	}
	var in profileInput
	if decode(body, &in) != nil {
		httpx.WriteError(w, r, eToHTTP("invalid_json"))
		return
	}
	if in.DisplayName == nil && in.Timezone == nil {
		httpx.WriteError(w, r, eToHTTP("invalid_request"))
		return
	}

	assignments := make([]string, 0, 2)
	args := []any{p.PrincipalID.String()}
	if in.DisplayName != nil {
		name, nameErr := normalizedProfileName(*in.DisplayName)
		if nameErr != nil {
			httpx.WriteError(w, r, eToHTTP("invalid_display_name"))
			return
		}
		args = append(args, name)
		assignments = append(assignments, fmt.Sprintf("display_name=$%d", len(args)))
	}
	if in.Timezone != nil {
		zone, zoneErr := normalizedTimezone(*in.Timezone)
		if zoneErr != nil {
			httpx.WriteError(w, r, eToHTTP("invalid_timezone"))
			return
		}
		args = append(args, zone)
		assignments = append(assignments, fmt.Sprintf("timezone=$%d", len(args)))
	}

	statement := `UPDATE principals SET ` + strings.Join(assignments, ",") + `,updated_at=now() WHERE id=$1`
	var updated int64
	if err = a.withTenantAsPrincipal(r.Context(), p.AccountID.String(), p.PrincipalID.String(), func(tx pgx.Tx) error {
		tag, execErr := tx.Exec(r.Context(), statement, args...)
		updated = tag.RowsAffected()
		return execErr
	}); err != nil {
		a.logError("profile update failed", "account_api", "update_profile", err, "principal_id", p.PrincipalID.String())
		httpx.WriteError(w, r, eToHTTP("service_unavailable"))
		return
	}
	if updated == 0 {
		httpx.WriteError(w, r, eToHTTP("unauthorized"))
		return
	}
	a.recordProfileAudit(r, p, "account.self.profile", "profile.update", "principal", p.PrincipalID.String(), "profile updated by its owner")
	a.writeProfile(w, r, p)
}

func (a *App) updateWorkspace(w http.ResponseWriter, r *http.Request) {
	p, ok := a.authWorkspaceManager(w, r)
	if !ok {
		return
	}
	body, err := readBody(w, r, 4<<10)
	if err != nil {
		httpx.WriteError(w, r, eToHTTP("invalid_request"))
		return
	}
	var in workspaceInput
	if decode(body, &in) != nil || in.Name == nil {
		httpx.WriteError(w, r, eToHTTP("invalid_json"))
		return
	}
	name, err := normalizedProfileName(*in.Name)
	if err != nil {
		httpx.WriteError(w, r, eToHTTP("invalid_workspace_name"))
		return
	}
	if _, err = a.tenantExec(r.Context(), p.AccountID.String(),
		`UPDATE accounts SET name=$2,updated_at=now() WHERE id=$1`, p.AccountID.String(), name); err != nil {
		a.logError("workspace rename failed", "account_api", "update_workspace", err, "account_id", p.AccountID.String())
		httpx.WriteError(w, r, eToHTTP("service_unavailable"))
		return
	}
	a.recordProfileAudit(r, p, "account.self.rename", "account.rename", "account", p.AccountID.String(), "workspace renamed by a member who may manage it")
	a.writeProfile(w, r, p)
}

func (a *App) requestEmailChange(w http.ResponseWriter, r *http.Request) {
	p, ok := a.auth(w, r)
	if !ok {
		return
	}
	if !a.checkLimit("ip:"+a.clientIPString(r), a.planLimits("free")) {
		a.metrics.rateLimitRejected.Add(1)
		httpx.WriteError(w, r, httpx.NewError("rate_limited"))
		return
	}
	body, err := readBody(w, r, 4<<10)
	if err != nil {
		httpx.WriteError(w, r, eToHTTP("invalid_request"))
		return
	}
	var in emailChangeInput
	if decode(body, &in) != nil {
		httpx.WriteError(w, r, eToHTTP("invalid_json"))
		return
	}
	target, err := normalizedEmail(in.Email)
	if err != nil {
		httpx.WriteError(w, r, eToHTTP("invalid_request"))
		return
	}

	current, err := a.principalEmail(r.Context(), p)
	if err != nil {
		httpx.WriteError(w, r, eToHTTP("service_unavailable"))
		return
	}
	if current == target {
		httpx.WriteError(w, r, eToHTTP("email_unchanged"))
		return
	}

	tx, err := a.db.Begin(r.Context())
	if err != nil {
		httpx.WriteError(w, r, eToHTTP("service_unavailable"))
		return
	}
	defer dbx.Rollback(r.Context(), tx)
	if _, err = tx.Exec(r.Context(),
		`SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, p.PrincipalID.String()); err != nil {
		httpx.WriteError(w, r, eToHTTP("service_unavailable"))
		return
	}

	var taken bool
	if err = tx.QueryRow(r.Context(),
		`SELECT EXISTS(SELECT 1 FROM principal_identities WHERE verified_email=$1 AND deleted_at IS NULL)`,
		target).Scan(&taken); err != nil {
		httpx.WriteError(w, r, eToHTTP("service_unavailable"))
		return
	}
	if taken {
		httpx.WriteError(w, r, eToHTTP("email_taken"))
		return
	}

	var recent int
	if err = tx.QueryRow(r.Context(),
		`SELECT count(*) FROM auth_challenges
		 WHERE principal_id=$1 AND purpose='email_change' AND created_at>now()-interval '15 minutes'`,
		p.PrincipalID.String()).Scan(&recent); err != nil {
		httpx.WriteError(w, r, eToHTTP("service_unavailable"))
		return
	}
	if recent >= maxEmailChangeAttempts {
		a.metrics.rateLimitRejected.Add(1)
		httpx.WriteError(w, r, httpx.NewError("rate_limited"))
		return
	}

	id := uuid.NewV7()
	n, err := rand.Int(rand.Reader, big.NewInt(1_000_000))
	if err != nil {
		httpx.WriteError(w, r, eToHTTP("internal_error"))
		return
	}
	otp := fmt.Sprintf("%06d", n.Int64())
	mac := hmac.New(sha256.New, a.cfg.OTPHMACKey)
	mac.Write([]byte(id.String() + ":" + otp))

	if _, err = tx.Exec(r.Context(),
		`UPDATE auth_challenges SET consumed_at=now()
		 WHERE principal_id=$1 AND purpose='email_change' AND consumed_at IS NULL`,
		p.PrincipalID.String()); err != nil {
		httpx.WriteError(w, r, eToHTTP("service_unavailable"))
		return
	}
	if _, err = tx.Exec(r.Context(),
		`INSERT INTO auth_challenges(id,email,code_mac,request_ip,expires_at,purpose,principal_id)
		 VALUES($1,$2,$3,$4,now()+$5::interval,'email_change',$6)`,
		id.String(), target, mac.Sum(nil), a.clientIPValue(r), a.cfg.OTPTTL.String(), p.PrincipalID.String()); err != nil {
		httpx.WriteError(w, r, eToHTTP("service_unavailable"))
		return
	}
	if err = tx.Commit(r.Context()); err != nil {
		httpx.WriteError(w, r, eToHTTP("service_unavailable"))
		return
	}

	message, renderErr := email.Render("email_change_code", email.EmailChangeCodeData{
		Code:      otp,
		ExpiresIn: email.HumanDuration(a.cfg.OTPTTL),
		Current:   current,
	})
	if renderErr != nil {
		httpx.WriteError(w, r, eToHTTP("internal_error"))
		return
	}
	message.To = target
	sendCtx, cancelSend := context.WithTimeout(r.Context(), otpSendTimeout)
	defer cancelSend()
	if err = a.mail.Send(sendCtx, message); err != nil {
		a.logError("email change code delivery failed", "account_api", "send_email_change_code", err, "request_id", httpx.RequestID(r))
		consumeCtx, cancelConsume := dbx.Cleanup(r.Context())
		defer cancelConsume()
		if _, consumeErr := a.db.Exec(consumeCtx, `UPDATE auth_challenges SET consumed_at=now() WHERE id=$1`, id.String()); consumeErr != nil {
			a.logError("undelivered email change challenge could not be consumed", "account_api", "consume_undelivered_challenge", consumeErr, "challenge_id", id.String())
		}
		httpx.WriteError(w, r, eToHTTP("service_unavailable"))
		return
	}
	httpx.WriteJSON(w, http.StatusAccepted, map[string]any{"challenge_id": id.String(), "expires_in": int(a.cfg.OTPTTL.Seconds())})
}

func (a *App) verifyEmailChange(w http.ResponseWriter, r *http.Request) {
	p, ok := a.auth(w, r)
	if !ok {
		return
	}
	body, err := readBody(w, r, 4<<10)
	if err != nil {
		httpx.WriteError(w, r, eToHTTP("invalid_request"))
		return
	}
	var in emailVerifyInput
	if decode(body, &in) != nil || len(in.Code) != 6 {
		httpx.WriteError(w, r, eToHTTP("invalid_request"))
		return
	}
	target, err := normalizedEmail(in.Email)
	if err != nil {
		httpx.WriteError(w, r, eToHTTP("invalid_request"))
		return
	}
	challenge, err := uuid.Parse(in.ChallengeID)
	if err != nil {
		httpx.WriteError(w, r, eToHTTP("invalid_request"))
		return
	}
	previous, err := a.principalEmail(r.Context(), p)
	if err != nil {
		httpx.WriteError(w, r, eToHTTP("service_unavailable"))
		return
	}

	tx, err := a.db.Begin(r.Context())
	if err != nil {
		httpx.WriteError(w, r, eToHTTP("service_unavailable"))
		return
	}
	defer dbx.Rollback(r.Context(), tx)

	var stored []byte
	var attempts int
	var expires time.Time
	var consumed *time.Time
	var challengeEmail, purpose string
	var owner *string
	err = tx.QueryRow(r.Context(),
		`SELECT email,code_mac,attempts,expires_at,consumed_at,purpose,principal_id::text
		 FROM auth_challenges WHERE id=$1 FOR UPDATE`,
		challenge.String()).Scan(&challengeEmail, &stored, &attempts, &expires, &consumed, &purpose, &owner)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			httpx.WriteError(w, r, eToHTTP("unauthorized"))
		} else {
			httpx.WriteError(w, r, eToHTTP("service_unavailable"))
		}
		return
	}
	if purpose != "email_change" || owner == nil || *owner != p.PrincipalID.String() ||
		consumed != nil || time.Now().After(expires) || attempts >= 5 || challengeEmail != target {
		httpx.WriteError(w, r, eToHTTP("unauthorized"))
		return
	}

	mac := hmac.New(sha256.New, a.cfg.OTPHMACKey)
	mac.Write([]byte(challenge.String() + ":" + in.Code))
	if !hmac.Equal(stored, mac.Sum(nil)) {
		if _, err = tx.Exec(r.Context(),
			`UPDATE auth_challenges SET attempts=attempts+1,consumed_at=CASE WHEN attempts+1>=5 THEN now() ELSE consumed_at END WHERE id=$1`,
			challenge.String()); err != nil {
			httpx.WriteError(w, r, eToHTTP("service_unavailable"))
			return
		}
		if err = tx.Commit(r.Context()); err != nil {
			httpx.WriteError(w, r, eToHTTP("service_unavailable"))
			return
		}
		httpx.WriteError(w, r, eToHTTP("unauthorized"))
		return
	}

	tag, err := tx.Exec(r.Context(),
		`UPDATE principal_identities SET verified_email=$2,provider_subject=$2,last_used_at=now()
		 WHERE principal_id=$1 AND provider='email_otp' AND deleted_at IS NULL`,
		p.PrincipalID.String(), target)
	if err != nil {
		if pgErr, ok := errors.AsType[*pgconn.PgError](err); ok && pgErr.Code == uniqueViolation {
			httpx.WriteError(w, r, eToHTTP("email_taken"))
			return
		}
		a.logError("email change failed", "account_api", "update_principal_identity", err, "principal_id", p.PrincipalID.String())
		httpx.WriteError(w, r, eToHTTP("service_unavailable"))
		return
	}
	if tag.RowsAffected() == 0 {
		httpx.WriteError(w, r, eToHTTP("unauthorized"))
		return
	}
	if _, err = tx.Exec(r.Context(), `UPDATE auth_challenges SET consumed_at=now() WHERE id=$1`, challenge.String()); err != nil {
		httpx.WriteError(w, r, eToHTTP("service_unavailable"))
		return
	}
	currentSession := ""
	if p.SessionID != nil {
		currentSession = p.SessionID.String()
	}
	if _, err = tx.Exec(r.Context(),
		`UPDATE credentials SET revoked_at=now()
		 WHERE principal_id=$1 AND audience='customer_session' AND revoked_at IS NULL AND id::text<>$2`,
		p.PrincipalID.String(), currentSession); err != nil {
		httpx.WriteError(w, r, eToHTTP("service_unavailable"))
		return
	}
	if _, err = tx.Exec(r.Context(),
		`INSERT INTO audit_events(principal_id,account_id,capability,action,target_type,target_id,reason,request_id,source_ip,user_agent)
		 VALUES($1,$2,'account.self.email','email.change','principal',$3,'sign-in address changed after a code sent to the new address',$4,$5,$6)`,
		p.PrincipalID.String(), p.AccountID.String(), p.PrincipalID.String(),
		httpx.RequestID(r), a.clientIPValue(r), boundedRunes(r.UserAgent(), 512)); err != nil {
		a.logError("email change audit failed", "account_api", "record_email_change_audit", err, "principal_id", p.PrincipalID.String())
		httpx.WriteError(w, r, eToHTTP("service_unavailable"))
		return
	}
	if err = tx.Commit(r.Context()); err != nil {
		httpx.WriteError(w, r, eToHTTP("service_unavailable"))
		return
	}

	if previous != "" {
		notice, renderErr := email.Render("email_changed", email.EmailChangedData{Previous: previous, Current: target})
		if renderErr == nil {
			notice.To = previous
			noticeCtx, cancelNotice := context.WithTimeout(r.Context(), otpSendTimeout)
			defer cancelNotice()
			if sendErr := a.mail.Send(noticeCtx, notice); sendErr != nil {
				a.logError("email change notice delivery failed", "account_api", "send_email_change_notice", sendErr, "principal_id", p.PrincipalID.String())
			}
		} else {
			a.logError("email change notice render failed", "account_api", "render_email_change_notice", renderErr)
		}
	}

	a.writeProfile(w, r, p)
}

func (a *App) principalEmail(ctx context.Context, p Principal) (string, error) {
	var current string
	err := a.db.QueryRow(ctx,
		`SELECT COALESCE(verified_email,'') FROM principal_identities
		 WHERE principal_id=$1 AND provider='email_otp' AND deleted_at IS NULL
		 ORDER BY created_at LIMIT 1`,
		p.PrincipalID.String()).Scan(&current)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	return current, err
}

func (a *App) recordProfileAudit(r *http.Request, p Principal, capability, action, targetType, targetID, reason string) {
	ctx, cancel := dbx.Cleanup(r.Context())
	defer cancel()
	if _, err := a.db.Exec(ctx,
		`INSERT INTO audit_events(principal_id,account_id,capability,action,target_type,target_id,reason,request_id,source_ip,user_agent)
		 VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`,
		p.PrincipalID.String(), p.AccountID.String(), capability, action, targetType, targetID, reason,
		httpx.RequestID(r), a.clientIPValue(r), boundedRunes(r.UserAgent(), 512)); err != nil {
		a.logError("profile audit failed", "account_api", "record_profile_audit", err, "action", action)
	}
}

func (a *App) writeProfile(w http.ResponseWriter, r *http.Request, p Principal) {
	var emailValue, displayName, timezone, workspace string
	if err := a.tenantRow(r.Context(), p.AccountID.String(),
		`SELECT i.verified_email,COALESCE(p.display_name,''),COALESCE(p.timezone,''),COALESCE(acc.name,'')
		 FROM principal_identities i
		 JOIN principals p ON p.id=i.principal_id
		 JOIN accounts acc ON acc.id=$2
		 WHERE i.principal_id=$1 AND i.verified_email IS NOT NULL AND i.deleted_at IS NULL
		 ORDER BY i.created_at LIMIT 1`,
		[]any{p.PrincipalID.String(), p.AccountID.String()},
		&emailValue, &displayName, &timezone, &workspace); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			httpx.WriteError(w, r, eToHTTP("unauthorized"))
			return
		}
		a.logError("dashboard identity lookup failed", "dashboard_api", "read_principal_identity", err, "principal_id", p.PrincipalID.String())
		httpx.WriteError(w, r, eToHTTP("service_unavailable"))
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{
		"data": map[string]any{
			"account": map[string]any{
				"id":           p.AccountID.String(),
				"email":        emailValue,
				"plan":         p.Plan,
				"status":       p.Status,
				"role":         p.Role,
				"name":         workspace,
				"display_name": displayName,
				"timezone":     timezone,
			},
		},
	})
}
