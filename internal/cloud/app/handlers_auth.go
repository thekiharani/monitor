package app

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"math/big"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"uuid"

	"github.com/jackc/pgx/v5"

	"github.com/thekiharani/monitor/internal/cloud/config"
	"github.com/thekiharani/monitor/internal/cloud/dbx"
	"github.com/thekiharani/monitor/internal/cloud/domain"
	"github.com/thekiharani/monitor/internal/cloud/email"
	"github.com/thekiharani/monitor/internal/cloud/httpx"
)

const otpSendTimeout = 8 * time.Second

func (a *App) csrf(w http.ResponseWriter, r *http.Request) bool {
	c, err := r.Cookie("__Host-csrf")
	if err != nil || c.Value == "" {
		httpx.WriteError(w, r, httpx.NewError("unauthorized"))
		return false
	}
	if !httpx.ConstantTimeEqual(c.Value, r.Header.Get("X-CSRF-Token")) {
		httpx.WriteError(w, r, httpx.NewError("unauthorized"))
		return false
	}
	return true
}

func setCSRF(w http.ResponseWriter, ttl time.Duration) error {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return err
	}
	http.SetCookie(w, &http.Cookie{
		Name:     "__Host-csrf",
		Value:    base64.RawURLEncoding.EncodeToString(b),
		Secure:   true,
		SameSite: http.SameSiteStrictMode,
		Path:     "/",
		MaxAge:   int(ttl.Seconds()),
	})
	return nil
}

func clearCSRF(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     "__Host-csrf",
		Value:    "",
		Secure:   true,
		SameSite: http.SameSiteStrictMode,
		Path:     "/",
		MaxAge:   -1,
	})
}

func setSession(w http.ResponseWriter, secret string, ttl time.Duration) {
	http.SetCookie(w, &http.Cookie{
		Name:     "__Host-session",
		Value:    secret,
		Secure:   true,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Path:     "/",
		MaxAge:   int(ttl.Seconds()),
	})
}

func loginDestination(next string) string {
	parsed, err := url.Parse(next)
	if err != nil || parsed.Scheme != "" || parsed.Host != "" || parsed.Opaque != "" {
		return "/app"
	}
	if !isCustomerPage(parsed.Path) && !isControlPage(parsed.Path) {
		return "/app"
	}
	return parsed.Path
}

func (a *App) signedInAtLoginPage(r *http.Request) (string, bool) {
	if r.URL.Path != "/login" || (r.Method != http.MethodGet && r.Method != http.MethodHead) {
		return "", false
	}
	if !httpx.HostMatches(r, a.cfg.DashboardOrigin) {
		return "", false
	}
	if _, err := a.principal(r); err != nil {
		return "", false
	}
	destination := loginDestination(r.URL.Query().Get("next"))
	if isControlPage(destination) && !a.sessionReachesControlPlane(r) {
		return "", false
	}
	return destination, true
}

func (a *App) principal(r *http.Request) (Principal, error) {
	cookie, err := r.Cookie("__Host-session")
	if err != nil {
		return Principal{}, pgx.ErrNoRows
	}
	var p Principal
	var pid, aid, sid, status, plan, role string
	err = a.db.QueryRow(r.Context(),
		`SELECT c.principal_id::text,c.account_id::text,c.id::text,a.status,a.plan,m.role
		 FROM credentials c
		 JOIN principals p ON p.id=c.principal_id AND p.status='active' AND p.deleted_at IS NULL
		 JOIN account_memberships m ON m.principal_id=c.principal_id AND m.account_id=c.account_id AND m.status='active' AND m.deleted_at IS NULL
		 JOIN accounts a ON a.id=c.account_id AND a.deleted_at IS NULL
		 WHERE c.secret_hash=$1 AND c.kind='session' AND c.audience='customer_session'
		   AND c.revoked_at IS NULL AND c.expires_at>now()`,
		config.HashSecret(cookie.Value),
	).Scan(&pid, &aid, &sid, &status, &plan, &role)
	if err != nil {
		return Principal{}, err
	}
	if !domain.IsAccountStatus(status) || !domain.IsPlan(plan) {
		return Principal{}, pgx.ErrNoRows
	}
	p.PrincipalID, _ = uuid.Parse(pid)
	p.AccountID, _ = uuid.Parse(aid)
	p.SessionIDPtr(sid)
	p.Plan = plan
	p.Status = status
	p.Role = role
	return p, nil
}

func (a *App) apiPrincipal(r *http.Request) (Principal, httpx.Error) {
	auth := r.Header.Get("Authorization")
	parts := strings.Fields(auth)
	if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") {
		return Principal{}, httpx.NewError("unauthorized")
	}
	hash := config.HashSecret(parts[1])
	var p Principal
	var pid, aid, kid, status, plan, role string
	err := a.db.QueryRow(r.Context(),
		`SELECT c.principal_id::text,c.id::text,a.id::text,a.status,a.plan,m.role
		 FROM credentials c
		 JOIN principals p ON p.id=c.principal_id AND p.status='active' AND p.deleted_at IS NULL
		 JOIN account_memberships m ON m.principal_id=c.principal_id AND m.account_id=c.account_id AND m.status='active' AND m.deleted_at IS NULL
		 JOIN accounts a ON a.id=c.account_id AND a.deleted_at IS NULL
		 WHERE c.secret_hash=$1 AND c.kind='api_key' AND c.audience='customer_api'
		   AND c.revoked_at IS NULL AND c.expires_at>now()`,
		hash,
	).Scan(&pid, &kid, &aid, &status, &plan, &role)
	if errors.Is(err, pgx.ErrNoRows) {
		return Principal{}, httpx.NewError("invalid_api_key")
	}
	if err != nil {
		return Principal{}, httpx.NewError("service_unavailable")
	}
	if !domain.IsAccountStatus(status) || !domain.IsPlan(plan) {
		return Principal{}, httpx.NewError("invalid_api_key")
	}
	p.PrincipalID, _ = uuid.Parse(pid)
	p.AccountID, _ = uuid.Parse(aid)
	p.APIKeyIDPtr(kid)
	p.Status = status
	p.Plan = plan
	p.Role = role
	_, _ = a.db.Exec(r.Context(),
		`UPDATE credentials SET last_used_at=now()
		 WHERE id=$1 AND (last_used_at IS NULL OR last_used_at<now()-interval '5 minutes')`,
		kid,
	)
	if status == "suspended" {
		return p, httpx.NewError("account_suspended")
	}
	if status == "closed" {
		return Principal{}, httpx.NewError("invalid_api_key")
	}
	return p, httpx.Error{}
}

func (a *App) sessionAuth(w http.ResponseWriter, r *http.Request, mutate bool) (Principal, bool) {
	if !a.requireHost(w, r, a.cfg.AppOrigin) {
		return Principal{}, false
	}
	p, err := a.principal(r)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			httpx.WriteError(w, r, httpx.NewError("unauthorized"))
		} else {
			httpx.WriteError(w, r, httpx.NewError("service_unavailable"))
		}
		return Principal{}, false
	}
	setRequestPrincipal(w, p)
	if p.Status == "closed" {
		httpx.WriteError(w, r, eToHTTP("unauthorized"))
		return Principal{}, false
	}
	if mutate && p.Status == "suspended" {
		httpx.WriteError(w, r, eToHTTP("account_suspended"))
		return Principal{}, false
	}
	if mutate && !a.csrf(w, r) {
		return Principal{}, false
	}
	return p, true
}

func (a *App) apiAuth(w http.ResponseWriter, r *http.Request) (Principal, bool) {
	if !a.requireHost(w, r, a.cfg.APIOrigin) {
		return Principal{}, false
	}
	p, e := a.apiPrincipal(r)
	if e.Code != "" {
		httpx.WriteError(w, r, e)
		return Principal{}, false
	}
	setRequestPrincipal(w, p)
	lim := a.planLimits(p.Plan)
	key := a.clientIPString(r)
	if p.APIKeyID != nil {
		key = "key:" + p.APIKeyID.String()
	}
	if !a.checkLimit(key, lim) || !a.checkLimit("account:"+p.AccountID.String(), lim) ||
		!a.allowShared(r.Context(), "account:"+p.AccountID.String(), lim) {
		a.metrics.rateLimitRejected.Add(1)
		w.Header().Set("RateLimit-Remaining", "0")
		w.Header().Set("RateLimit-Reset", "1")
		w.Header().Set("Retry-After", "1")
		httpx.WriteError(w, r, httpx.NewError("rate_limited"))
		return Principal{}, false
	}
	withinQuota, usageErr := a.incrementAPIUsage(r.Context(), p.AccountID, lim.MonthlyAPIRequests)
	if usageErr != nil {
		httpx.WriteError(w, r, httpx.NewError("service_unavailable"))
		return Principal{}, false
	}
	if !withinQuota {
		a.metrics.quotaRejected.Add(1)
		httpx.WriteError(w, r, httpx.NewError("quota_exceeded"))
		return Principal{}, false
	}
	w.Header().Set("RateLimit-Limit", strconv.FormatFloat(lim.RequestsPerSecond, 'f', -1, 64))
	remaining := a.limiters.remaining(key, lim)
	w.Header().Set("RateLimit-Remaining", strconv.Itoa(remaining))
	w.Header().Set("RateLimit-Reset", "1")
	return p, true
}

func mutatingMethod(r *http.Request) bool {
	return r.Method != http.MethodGet && r.Method != http.MethodHead
}

func (a *App) authWorkspaceManager(w http.ResponseWriter, r *http.Request) (Principal, bool) {
	p, ok := a.auth(w, r)
	if !ok {
		return Principal{}, false
	}
	if !domain.CanManageWorkspace(p.Role) {
		httpx.WriteError(w, r, eToHTTP("forbidden_role"))
		return Principal{}, false
	}
	return p, true
}

func (a *App) authBillingManager(w http.ResponseWriter, r *http.Request) (Principal, bool) {
	p, ok := a.auth(w, r)
	if !ok {
		return Principal{}, false
	}
	if !domain.CanManageBilling(p.Role) {
		httpx.WriteError(w, r, eToHTTP("forbidden_role"))
		return Principal{}, false
	}
	return p, true
}

func (a *App) auth(w http.ResponseWriter, r *http.Request) (Principal, bool) {
	if isDashboardAPI(r) {
		return a.sessionAuth(w, r, mutatingMethod(r))
	}
	return a.apiAuth(w, r)
}

func (a *App) signInOrigin(w http.ResponseWriter, r *http.Request) bool {
	if isJSONAPI(r) {
		return a.requireHost(w, r, a.cfg.APIOrigin)
	}
	return a.requireHost(w, r, a.cfg.AppOrigin)
}

func (a *App) requestOTP(w http.ResponseWriter, r *http.Request) {
	if !a.signInOrigin(w, r) {
		return
	}
	if !a.checkLimit("ip:"+a.clientIPString(r), a.planLimits("free")) {
		a.metrics.rateLimitRejected.Add(1)
		httpx.WriteError(w, r, httpx.NewError("rate_limited"))
		return
	}
	body, e := formOrJSONBody(w, r, map[string]string{"email": ""})
	if e != nil {
		httpx.WriteError(w, r, httpx.NewError("invalid_json"))
		return
	}
	var in otpRequest
	if decode(body, &in) != nil {
		httpx.WriteError(w, r, httpx.NewError("invalid_json"))
		return
	}
	emailValue, e := normalizedEmail(in.Email)
	if e != nil {
		httpx.WriteError(w, r, httpx.NewError("invalid_request"))
		return
	}
	var emailCount, ipCount int
	ip := a.clientIPValue(r)
	ipKey := a.clientIPString(r)
	tx, e := a.db.Begin(r.Context())
	if e != nil {
		httpx.WriteError(w, r, httpx.NewError("service_unavailable"))
		return
	}
	defer dbx.Rollback(r.Context(), tx)
	if _, e = tx.Exec(r.Context(),
		`SELECT pg_advisory_xact_lock(hashtextextended($1,0)), pg_advisory_xact_lock(hashtextextended($2,0))`,
		emailValue, ipKey,
	); e != nil {
		httpx.WriteError(w, r, httpx.NewError("service_unavailable"))
		return
	}
	if err := tx.QueryRow(r.Context(),
		`SELECT count(*) FROM auth_challenges WHERE email=$1 AND created_at>now()-interval '15 minutes'`,
		emailValue,
	).Scan(&emailCount); err != nil {
		httpx.WriteError(w, r, httpx.NewError("service_unavailable"))
		return
	}
	if err := tx.QueryRow(r.Context(),
		`SELECT count(*) FROM auth_challenges WHERE request_ip=$1 AND created_at>now()-interval '1 hour'`,
		ip,
	).Scan(&ipCount); err != nil {
		httpx.WriteError(w, r, httpx.NewError("service_unavailable"))
		return
	}
	if emailCount >= 3 || ipCount >= 10 {
		a.metrics.rateLimitRejected.Add(1)
		httpx.WriteError(w, r, httpx.NewError("rate_limited"))
		return
	}
	id := uuid.NewV7()
	n, e := rand.Int(rand.Reader, big.NewInt(1_000_000))
	if e != nil {
		httpx.WriteError(w, r, httpx.NewError("internal_error"))
		return
	}
	otp := fmt.Sprintf("%06d", n.Int64())
	mac := hmac.New(sha256.New, a.cfg.OTPHMACKey)
	mac.Write([]byte(id.String() + ":" + otp))
	macBytes := mac.Sum(nil)
	if _, e = tx.Exec(r.Context(),
		`UPDATE auth_challenges SET consumed_at=now() WHERE email=$1 AND consumed_at IS NULL`,
		emailValue,
	); e != nil {
		httpx.WriteError(w, r, httpx.NewError("service_unavailable"))
		return
	}
	_, e = tx.Exec(r.Context(),
		`INSERT INTO auth_challenges(id,email,code_mac,request_ip,expires_at) VALUES($1,$2,$3,$4,now()+$5::interval)`,
		id.String(), emailValue, macBytes, ip, a.cfg.OTPTTL.String(),
	)
	if e != nil {
		httpx.WriteError(w, r, httpx.NewError("service_unavailable"))
		return
	}
	if e = tx.Commit(r.Context()); e != nil {
		httpx.WriteError(w, r, httpx.NewError("service_unavailable"))
		return
	}
	message, renderErr := email.Render("login_code", email.LoginCodeData{Code: otp, ExpiresIn: email.HumanDuration(a.cfg.OTPTTL)})
	if renderErr != nil {
		httpx.WriteError(w, r, eToHTTP("internal_error"))
		return
	}
	message.To = emailValue
	sendCtx, cancelSend := context.WithTimeout(r.Context(), otpSendTimeout)
	defer cancelSend()
	if e = a.mail.Send(sendCtx, message); e != nil {
		a.logError("login code delivery failed", "auth", "send_login_code", e, "request_id", httpx.RequestID(r))
		consumeCtx, cancelConsume := dbx.Cleanup(r.Context())
		defer cancelConsume()
		if _, consumeErr := a.db.Exec(consumeCtx, `UPDATE auth_challenges SET consumed_at=now() WHERE id=$1`, id.String()); consumeErr != nil {
			a.logError("undelivered login challenge could not be consumed", "auth", "consume_undelivered_challenge", consumeErr, "challenge_id", id.String())
		}
		httpx.WriteError(w, r, httpx.NewError("service_unavailable"))
		return
	}
	a.metrics.otpRequests.Add(1)
	httpx.WriteJSON(w, http.StatusAccepted, map[string]any{"challenge_id": id.String(), "expires_in": int(a.cfg.OTPTTL.Seconds())})
}

func (a *App) verifyOTP(w http.ResponseWriter, r *http.Request) {
	if !a.signInOrigin(w, r) {
		return
	}
	body, e := formOrJSONBody(w, r, map[string]string{"challenge_id": "", "email": "", "code": "", "key_name": ""})
	if e != nil {
		httpx.WriteError(w, r, httpx.NewError("invalid_json"))
		return
	}
	var in otpVerify
	if decode(body, &in) != nil || len(in.Code) != 6 {
		httpx.WriteError(w, r, httpx.NewError("invalid_request"))
		return
	}
	emailValue, e := normalizedEmail(in.Email)
	if e != nil {
		httpx.WriteError(w, r, eToHTTP("invalid_request"))
		return
	}
	challenge, err := uuid.Parse(in.ChallengeID)
	if err != nil {
		httpx.WriteError(w, r, eToHTTP("invalid_request"))
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
	var chEmail string
	err = tx.QueryRow(r.Context(),
		`SELECT email,code_mac,attempts,expires_at,consumed_at FROM auth_challenges WHERE id=$1 FOR UPDATE`,
		challenge.String(),
	).Scan(&chEmail, &stored, &attempts, &expires, &consumed)
	if err != nil {
		a.metrics.otpVerifyFailures.Add(1)
		if errors.Is(err, pgx.ErrNoRows) {
			httpx.WriteError(w, r, eToHTTP("unauthorized"))
		} else {
			httpx.WriteError(w, r, eToHTTP("service_unavailable"))
		}
		return
	}
	if consumed != nil || time.Now().After(expires) || attempts >= 5 || chEmail != emailValue {
		a.metrics.otpVerifyFailures.Add(1)
		httpx.WriteError(w, r, eToHTTP("unauthorized"))
		return
	}
	mac := hmac.New(sha256.New, a.cfg.OTPHMACKey)
	mac.Write([]byte(challenge.String() + ":" + in.Code))
	if !hmac.Equal(stored, mac.Sum(nil)) {
		if _, err = tx.Exec(r.Context(),
			`UPDATE auth_challenges SET attempts=attempts+1,consumed_at=CASE WHEN attempts+1>=5 THEN now() ELSE consumed_at END WHERE id=$1`,
			challenge.String(),
		); err != nil {
			httpx.WriteError(w, r, eToHTTP("service_unavailable"))
			return
		}
		if err = tx.Commit(r.Context()); err != nil {
			httpx.WriteError(w, r, eToHTTP("service_unavailable"))
			return
		}
		a.metrics.otpVerifyFailures.Add(1)
		httpx.WriteError(w, r, eToHTTP("unauthorized"))
		return
	}
	var pid, aid, status, plan string
	err = tx.QueryRow(r.Context(),
		`SELECT i.principal_id::text,m.account_id::text,a.status,a.plan
		 FROM principal_identities i
		 JOIN account_memberships m ON m.principal_id=i.principal_id AND m.status='active' AND m.deleted_at IS NULL
		 JOIN accounts a ON a.id=m.account_id AND a.deleted_at IS NULL
		 WHERE i.provider='email_otp' AND i.provider_subject=$1 AND i.deleted_at IS NULL
		 ORDER BY (m.role='owner') DESC,m.created_at LIMIT 1`, emailValue,
	).Scan(&pid, &aid, &status, &plan)
	if errors.Is(err, pgx.ErrNoRows) {
		pid = uuid.NewV7().String()
		if _, err = tx.Exec(r.Context(), `INSERT INTO principals(id,kind) VALUES($1,'human')`, pid); err == nil {
			_, err = tx.Exec(r.Context(), `INSERT INTO principal_identities(principal_id,provider,provider_subject,verified_email,last_used_at) VALUES($1,'email_otp',$2,$2,now())`, pid, emailValue)
		}
		var inviteID, invitedRole string
		if err == nil {
			inviteErr := tx.QueryRow(r.Context(),
				`SELECT i.id::text,i.account_id::text,i.role,a.status,a.plan
				 FROM account_invites i JOIN accounts a ON a.id=i.account_id AND a.deleted_at IS NULL
				 WHERE i.email=$1 AND i.accepted_at IS NULL AND i.revoked_at IS NULL AND i.expires_at>now()
				 ORDER BY i.created_at DESC LIMIT 1 FOR UPDATE OF i`,
				emailValue,
			).Scan(&inviteID, &aid, &invitedRole, &status, &plan)
			if inviteErr != nil && !errors.Is(inviteErr, pgx.ErrNoRows) {
				err = inviteErr
			}
		}
		switch {
		case err != nil:
		case inviteID != "":
			if _, err = tx.Exec(r.Context(), `INSERT INTO account_memberships(principal_id,account_id,role) VALUES($1,$2,$3)`, pid, aid, invitedRole); err == nil {
				_, err = tx.Exec(r.Context(), `UPDATE account_invites SET accepted_at=now() WHERE id=$1`, inviteID)
			}
		default:
			aid = uuid.NewV7().String()
			if err = tx.QueryRow(r.Context(), `INSERT INTO accounts(id) VALUES($1) RETURNING status,plan`, aid).Scan(&status, &plan); err == nil {
				_, err = tx.Exec(r.Context(), `INSERT INTO account_memberships(principal_id,account_id,role) VALUES($1,$2,'owner')`, pid, aid)
			}
		}
	} else if err == nil {
		_, err = tx.Exec(r.Context(), `UPDATE principal_identities SET last_used_at=now() WHERE provider='email_otp' AND provider_subject=$1 AND deleted_at IS NULL`, emailValue)
	}
	if err != nil {
		httpx.WriteError(w, r, eToHTTP("service_unavailable"))
		return
	}
	_, err = tx.Exec(r.Context(), `UPDATE auth_challenges SET consumed_at=now() WHERE id=$1`, challenge.String())
	if err != nil {
		httpx.WriteError(w, r, eToHTTP("service_unavailable"))
		return
	}
	if isJSONAPI(r) {
		keyName := strings.TrimSpace(in.KeyName)
		if keyName == "" {
			keyName = "API sign-in"
		}
		if len(keyName) > 64 {
			httpx.WriteError(w, r, eToHTTP("invalid_request"))
			return
		}
		key, mintErr := a.mintAPIKey(keyName)
		if mintErr != nil {
			httpx.WriteError(w, r, eToHTTP("internal_error"))
			return
		}
		if insertErr := insertAPIKey(r.Context(), tx, key, pid, aid, a.planLimits(plan).MaxAPIKeys); insertErr != nil {
			if errors.Is(insertErr, errPlanLimit) {
				httpx.WriteError(w, r, eToHTTP("plan_limit_exceeded"))
				return
			}
			httpx.WriteError(w, r, eToHTTP("service_unavailable"))
			return
		}
		if err = tx.Commit(r.Context()); err != nil {
			httpx.WriteError(w, r, eToHTTP("service_unavailable"))
			return
		}
		httpx.WriteJSON(w, http.StatusOK, map[string]any{
			"data": map[string]any{
				"id":         key.id,
				"name":       key.name,
				"key":        key.secret,
				"prefix":     key.prefix,
				"account_id": aid,
				"email":      emailValue,
				"plan":       plan,
				"status":     status,
			},
		})
		return
	}

	secretBytes := make([]byte, 32)
	if _, err = rand.Read(secretBytes); err != nil {
		httpx.WriteError(w, r, eToHTTP("internal_error"))
		return
	}
	secret := base64.RawURLEncoding.EncodeToString(secretBytes)
	sid := uuid.NewV7()
	_, err = tx.Exec(r.Context(),
		`INSERT INTO credentials(id,principal_id,account_id,kind,audience,secret_hash,created_ip,user_agent,expires_at)
		 VALUES($1,$2,$3,'session','customer_session',$4,$5,$6,now()+$7::interval)`,
		sid.String(), pid, aid, config.HashSecret(secret), a.clientIPValue(r), boundedRunes(r.UserAgent(), 512), a.cfg.SessionTTL.String(),
	)
	if err != nil {
		httpx.WriteError(w, r, eToHTTP("service_unavailable"))
		return
	}
	_, err = tx.Exec(r.Context(),
		`UPDATE credentials SET revoked_at=now()
		 WHERE principal_id=$1 AND account_id=$2 AND audience='customer_session' AND revoked_at IS NULL AND expires_at>now()
		   AND id NOT IN (SELECT id FROM credentials WHERE principal_id=$1 AND account_id=$2 AND audience='customer_session' AND revoked_at IS NULL AND expires_at>now() ORDER BY created_at DESC LIMIT 10)`,
		pid, aid,
	)
	if err != nil {
		httpx.WriteError(w, r, eToHTTP("service_unavailable"))
		return
	}
	if err = tx.Commit(r.Context()); err != nil {
		httpx.WriteError(w, r, eToHTTP("service_unavailable"))
		return
	}
	setSession(w, secret, a.cfg.SessionTTL)
	if err = setCSRF(w, a.cfg.SessionTTL); err != nil {
		httpx.WriteError(w, r, eToHTTP("internal_error"))
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"account": map[string]any{"email": emailValue, "plan": plan, "status": status}})
}

func (a *App) logout(w http.ResponseWriter, r *http.Request) {
	if !a.requireHost(w, r, a.cfg.AppOrigin) {
		return
	}
	if !a.csrf(w, r) {
		return
	}
	c, err := r.Cookie("__Host-session")
	if err == nil {
		_, _ = a.db.Exec(r.Context(), `UPDATE credentials SET revoked_at=now() WHERE secret_hash=$1 AND audience='customer_session'`, config.HashSecret(c.Value))
	}
	http.SetCookie(w, &http.Cookie{Name: "__Host-session", Value: "", Path: "/", MaxAge: -1, Secure: true, HttpOnly: true, SameSite: http.SameSiteLaxMode})
	clearCSRF(w)
	w.WriteHeader(http.StatusNoContent)
}
