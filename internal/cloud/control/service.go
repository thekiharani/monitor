package control

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json/v2"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"uuid"

	"github.com/thekiharani/monitor/internal/cloud/access"
	"github.com/thekiharani/monitor/internal/cloud/billing"
	"github.com/thekiharani/monitor/internal/cloud/billing/paystack"
	"github.com/thekiharani/monitor/internal/cloud/config"
	"github.com/thekiharani/monitor/internal/cloud/dbx"
	"github.com/thekiharani/monitor/internal/cloud/domain"
	"github.com/thekiharani/monitor/internal/cloud/probe"
	"github.com/thekiharani/monitor/internal/cloud/usage"
)

var (
	ErrNotFound = errors.New("not found")
	ErrInvalid  = errors.New("invalid request")
	ErrConflict = errors.New("resource changed")
)

type Service struct {
	db      *pgxpool.Pool
	billing billing.Provider
	cfg     config.Config
	catalog *usage.Catalog
}

type Actor struct {
	Principal access.Principal
	RequestID string
	SourceIP  string
	UserAgent string
}

type Account struct {
	ID          string `json:"id"`
	PrincipalID string `json:"principal_id"`
	Email       string `json:"email"`
	Status      string `json:"status"`
	Plan        string `json:"plan"`
	CreatedAt   any    `json:"created_at"`
}

type AccountDetail struct {
	Account
	ActiveCredentials      int64   `json:"active_credentials"`
	SubscriptionStatus     *string `json:"subscription_status"`
	SubscriptionPlan       *string `json:"subscription_plan"`
	ComplimentaryPlan      *string `json:"complimentary_plan"`
	ComplimentaryExpiresAt any     `json:"complimentary_expires_at"`
	PeriodAPIRequests      int64   `json:"period_api_requests"`
}

type ComplimentaryInput struct {
	Plan      string `json:"plan"`
	ExpiresAt string `json:"expires_at"`
	Reason    string `json:"reason"`
}

type GrantInput struct {
	PrincipalID string `json:"principal_id"`
	Capability  string `json:"capability"`
	ScopeType   string `json:"scope_type"`
	ScopeID     string `json:"scope_id"`
	Reason      string `json:"reason"`
}

type ProbeInput struct {
	Region string `json:"region"`
	Name   string `json:"name"`
	Reason string `json:"reason"`
}

type CreatedProbe struct {
	ID       string `json:"id"`
	Region   string `json:"region"`
	Name     string `json:"name"`
	Token    string `json:"token"`
	Endpoint string `json:"endpoint"`
}

var probeRegionPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,31}$`)

type Page struct {
	Offset int   `json:"offset"`
	Limit  int   `json:"limit"`
	Total  int64 `json:"total"`
}

func New(db *pgxpool.Pool, provider billing.Provider, cfg config.Config, catalogs ...*usage.Catalog) *Service {
	catalog := usage.NewCatalog(cfg)
	if len(catalogs) > 0 && catalogs[0] != nil {
		catalog = catalogs[0]
	}
	return &Service{db: db, billing: provider, cfg: cfg, catalog: catalog}
}

func (s *Service) Accounts(ctx context.Context, actor Actor, query string, limit, offset int) ([]Account, Page, error) {
	if err := access.Authorize(ctx, s.db, actor.Principal, access.AccountsRead, access.PlatformScope()); err != nil {
		return nil, Page{}, err
	}
	limit, offset, err := parsePage(limit, offset)
	if err != nil {
		return nil, Page{}, err
	}
	page := Page{Limit: limit, Offset: offset}
	q := strings.TrimSpace(query)
	from := `
		FROM accounts a
		LEFT JOIN LATERAL (
			SELECT m.principal_id::text AS principal_id,i.verified_email
			FROM account_memberships m
			LEFT JOIN principal_identities i ON i.principal_id=m.principal_id
				AND i.verified_email IS NOT NULL AND i.deleted_at IS NULL
			WHERE m.account_id=a.id AND m.role='owner'
			ORDER BY (m.deleted_at IS NULL) DESC,(m.status='active') DESC,m.created_at,i.created_at NULLS LAST,m.principal_id
			LIMIT 1
		) owner ON true
		WHERE $1='' OR a.id::text=$1 OR EXISTS (
			SELECT 1 FROM account_memberships search_m
			JOIN principal_identities search_i ON search_i.principal_id=search_m.principal_id
				AND search_i.verified_email IS NOT NULL AND search_i.deleted_at IS NULL
			WHERE search_m.account_id=a.id AND search_m.role='owner'
				AND search_i.verified_email ILIKE '%'||replace(replace(replace($1,'\\','\\\\'),'%','\\%'),'_','\\_')||'%'
		)`
	if err = s.db.QueryRow(ctx, `SELECT count(*)`+from, q).Scan(&page.Total); err != nil {
		return nil, page, err
	}
	rows, err := s.db.Query(ctx, `
		SELECT a.id::text,COALESCE(owner.principal_id,''),COALESCE(owner.verified_email,''),a.status,a.plan,a.created_at`+from+`
		ORDER BY a.created_at DESC, a.id DESC LIMIT $2 OFFSET $3`, q, limit, offset)
	if err != nil {
		return nil, page, err
	}
	defer rows.Close()
	items := make([]Account, 0)
	for rows.Next() {
		var item Account
		if err = rows.Scan(&item.ID, &item.PrincipalID, &item.Email, &item.Status, &item.Plan, &item.CreatedAt); err != nil {
			return nil, page, err
		}
		items = append(items, item)
	}
	return items, page, rows.Err()
}

func (s *Service) Account(ctx context.Context, actor Actor, accountID string) (AccountDetail, error) {
	var item AccountDetail
	if !validUUID(accountID) {
		return item, fmt.Errorf("%w: invalid account id", ErrInvalid)
	}
	if err := access.Authorize(ctx, s.db, actor.Principal, access.AccountsRead, access.AccountScope(accountID)); err != nil {
		return item, err
	}
	err := s.db.QueryRow(ctx, `
		SELECT a.id::text,COALESCE(owner.principal_id,''),COALESCE(owner.verified_email,''),a.status,a.plan,a.created_at,
			(SELECT count(*) FROM credentials WHERE account_id=a.id AND revoked_at IS NULL AND expires_at>now()),
			(SELECT status FROM billing_subscriptions WHERE account_id=a.id AND status IN ('active','past_due','non_renewing') ORDER BY created_at DESC LIMIT 1),
			(SELECT plan FROM billing_subscriptions WHERE account_id=a.id AND status IN ('active','past_due','non_renewing') ORDER BY created_at DESC LIMIT 1),
			(SELECT plan FROM billing_subscriptions WHERE account_id=a.id AND billing_mode='comp' AND status='active' LIMIT 1),
			(SELECT current_period_end FROM billing_subscriptions WHERE account_id=a.id AND billing_mode='comp' AND status='active' LIMIT 1),
			COALESCE((SELECT api_requests FROM account_monthly_usage WHERE account_id=a.id ORDER BY period DESC LIMIT 1),0)
		FROM accounts a
		LEFT JOIN LATERAL (
			SELECT m.principal_id::text AS principal_id,i.verified_email
			FROM account_memberships m
			LEFT JOIN principal_identities i ON i.principal_id=m.principal_id
				AND i.verified_email IS NOT NULL AND i.deleted_at IS NULL
			WHERE m.account_id=a.id AND m.role='owner'
			ORDER BY (m.deleted_at IS NULL) DESC,(m.status='active') DESC,m.created_at,i.created_at NULLS LAST,m.principal_id
			LIMIT 1
		) owner ON true
		WHERE a.id=$1`, accountID).Scan(
		&item.ID, &item.PrincipalID, &item.Email, &item.Status, &item.Plan, &item.CreatedAt,
		&item.ActiveCredentials, &item.SubscriptionStatus, &item.SubscriptionPlan,
		&item.ComplimentaryPlan, &item.ComplimentaryExpiresAt,
		&item.PeriodAPIRequests,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return item, ErrNotFound
	}
	return item, err
}

func (s *Service) Stats(ctx context.Context, actor Actor) (map[string]any, error) {
	out := map[string]any{}
	type metric struct {
		capability, key, query string
	}
	metrics := []metric{
		{access.AccountsRead, "accounts", `SELECT count(*) FROM accounts`},
		{access.CredentialsRead, "active_credentials", `SELECT count(*) FROM credentials WHERE revoked_at IS NULL AND expires_at>now()`},
		{access.JobsRead, "pending_jobs", `SELECT count(*) FROM email_jobs WHERE sent_at IS NULL`},
		{access.BillingRead, "past_due_billing", `SELECT count(*) FROM billing_subscriptions WHERE status='past_due'`},
	}
	metrics = append(metrics,
		metric{access.AccountsRead, "active_accounts", `SELECT count(*) FROM accounts WHERE status='active' AND deleted_at IS NULL`},
		metric{access.AccountsRead, "suspended_accounts", `SELECT count(*) FROM accounts WHERE status='suspended'`},
		metric{access.AccountsRead, "accounts_new_30d", `SELECT count(*) FROM accounts WHERE created_at>now()-interval '30 days'`},
		metric{access.AccountsRead, "accounts_closed_30d", `SELECT count(*) FROM accounts WHERE status='closed' AND deleted_at>now()-interval '30 days'`},
		metric{access.AccountsRead, "closed_pending_erasure", `SELECT count(*) FROM accounts WHERE status='closed' AND purged_at IS NULL`},
		metric{access.MembersRead, "team_members", `SELECT count(*) FROM account_memberships WHERE status='active' AND deleted_at IS NULL AND role<>'owner'`},
		metric{access.MembersRead, "pending_invites", `SELECT count(*) FROM account_invites WHERE accepted_at IS NULL AND revoked_at IS NULL AND expires_at>now()`},
		metric{access.BillingRead, "mrr_minor", `SELECT COALESCE(sum(CASE WHEN currency='USD' THEN amount_minor::numeric*100 ELSE amount_minor END),0) FROM billing_subscriptions WHERE status IN ('active','past_due') AND billing_mode<>'comp'`},
		metric{access.BillingRead, "paying_accounts", `SELECT count(DISTINCT account_id) FROM billing_subscriptions WHERE status IN ('active','past_due') AND billing_mode<>'comp'`},
		metric{access.BillingRead, "complimentary_accounts", `SELECT count(DISTINCT account_id) FROM billing_subscriptions WHERE billing_mode='comp' AND status='active'`},
		metric{access.JobsRead, "failing_jobs", `SELECT count(*) FROM email_jobs WHERE sent_at IS NULL AND attempts>=3`},
	)
	for _, metric := range metrics {
		if err := access.Authorize(ctx, s.db, actor.Principal, metric.capability, access.PlatformScope()); err != nil {
			if errors.Is(err, access.ErrForbidden) {
				continue
			}
			return nil, err
		}
		var n int64
		if err := s.db.QueryRow(ctx, metric.query).Scan(&n); err != nil {
			return nil, err
		}
		out[metric.key] = n
	}
	if err := access.Authorize(ctx, s.db, actor.Principal, access.AccountsRead, access.PlatformScope()); err == nil {
		plans, planErr := s.planDistribution(ctx)
		if planErr != nil {
			return nil, planErr
		}
		out["plans"] = plans
	} else if !errors.Is(err, access.ErrForbidden) {
		return nil, err
	}
	return out, nil
}

func (s *Service) AuthorizePlatform(ctx context.Context, actor Actor, capability string) error {
	return access.Authorize(ctx, s.db, actor.Principal, capability, access.PlatformScope())
}

func (s *Service) planDistribution(ctx context.Context) (map[string]int64, error) {
	rows, err := s.db.Query(ctx,
		`SELECT plan,count(*) FROM accounts WHERE status='active' AND deleted_at IS NULL GROUP BY plan`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]int64{"free": 0, "starter": 0, "pro": 0, "business": 0}
	for rows.Next() {
		var plan string
		var count int64
		if err = rows.Scan(&plan, &count); err != nil {
			return nil, err
		}
		out[plan] = count
	}
	return out, rows.Err()
}

func (s *Service) SetAccountStatus(ctx context.Context, actor Actor, accountID, status, reason string) error {
	if !validUUID(accountID) {
		return fmt.Errorf("%w: invalid account id", ErrInvalid)
	}
	capability := access.AccountsSuspend
	if status == domain.AccountStatusClosed {
		capability = access.AccountsClose
	}
	if !domain.IsAccountStatus(status) {
		return fmt.Errorf("%w: invalid status", ErrInvalid)
	}
	if err := access.Authorize(ctx, s.db, actor.Principal, capability, access.AccountScope(accountID)); err != nil {
		return err
	}
	err := s.mutateScoped(ctx, actor, capability, access.AccountScope(accountID), "account.status_changed", "account", accountID, reason, func(tx pgx.Tx) (any, any, error) {
		var before string
		if err := tx.QueryRow(ctx, `SELECT status FROM accounts WHERE id=$1 FOR UPDATE`, accountID).Scan(&before); errors.Is(err, pgx.ErrNoRows) {
			return nil, nil, ErrNotFound
		} else if err != nil {
			return nil, nil, err
		}
		if err := validateAccountTransition(before, status); err != nil {
			return nil, nil, err
		}
		if _, err := tx.Exec(ctx, `UPDATE accounts SET status=$2,updated_at=now(),deleted_at=CASE WHEN $2::varchar='closed' THEN COALESCE(deleted_at,now()) WHEN $2::varchar='active' THEN NULL ELSE deleted_at END WHERE id=$1`, accountID, status); err != nil {
			return nil, nil, err
		}
		if _, err := tx.Exec(ctx, `UPDATE account_memberships SET deleted_at=CASE WHEN $2::varchar='closed' THEN COALESCE(deleted_at,now()) WHEN $2::varchar='active' THEN NULL ELSE deleted_at END,updated_at=now() WHERE account_id=$1`, accountID, status); err != nil {
			return nil, nil, err
		}
		if status != domain.AccountStatusActive {
			if _, err := tx.Exec(ctx, `UPDATE credentials SET revoked_at=COALESCE(revoked_at,now()) WHERE account_id=$1`, accountID); err != nil {
				return nil, nil, err
			}
		}
		if status == domain.AccountStatusClosed {
			if _, err := tx.Exec(ctx, `UPDATE billing_subscriptions SET status='canceled',grace_until=NULL,cancel_at_period_end=true,updated_at=now() WHERE account_id=$1 AND status IN ('active','past_due','non_renewing')`, accountID); err != nil {
				return nil, nil, err
			}
		}
		return map[string]string{"status": before}, map[string]string{"status": status}, nil
	})
	if err != nil {
		return err
	}
	if status == domain.AccountStatusClosed {
		if err = s.disableAccountSubscriptions(ctx, accountID); err != nil {
			return fmt.Errorf("account closed locally; disable remote subscriptions: %w", err)
		}
	}
	return nil
}

func (s *Service) RemoveMember(ctx context.Context, actor Actor, accountID, principalID, reason string) error {
	if !validUUID(accountID) || !validUUID(principalID) {
		return fmt.Errorf("%w: invalid identifier", ErrInvalid)
	}
	return s.mutateScoped(ctx, actor, access.MembersRemove, access.AccountScope(accountID), "membership.removed", "membership", principalID, reason, func(tx pgx.Tx) (any, any, error) {
		var role, status string
		if err := tx.QueryRow(ctx,
			`SELECT role,status FROM account_memberships
			 WHERE account_id=$1 AND principal_id=$2 AND deleted_at IS NULL FOR UPDATE`,
			accountID, principalID,
		).Scan(&role, &status); errors.Is(err, pgx.ErrNoRows) {
			return nil, nil, ErrNotFound
		} else if err != nil {
			return nil, nil, err
		}
		if role == domain.MembershipRoleOwner {
			return nil, nil, fmt.Errorf("%w: an owner cannot be removed; close the account instead", ErrInvalid)
		}
		if _, err := tx.Exec(ctx,
			`UPDATE account_memberships SET status='removed',deleted_at=now(),updated_at=now()
			 WHERE account_id=$1 AND principal_id=$2`, accountID, principalID); err != nil {
			return nil, nil, err
		}
		if _, err := tx.Exec(ctx,
			`UPDATE credentials SET revoked_at=now()
			 WHERE account_id=$1 AND principal_id=$2 AND revoked_at IS NULL`, accountID, principalID); err != nil {
			return nil, nil, err
		}
		return map[string]any{"role": role, "status": status},
			map[string]any{"status": "removed", "credentials_revoked": true}, nil
	})
}

func (s *Service) Usage(ctx context.Context, actor Actor, accountID string, limit, offset int) ([]map[string]any, Page, error) {
	if accountID != "" && !validUUID(accountID) {
		return nil, Page{}, fmt.Errorf("%w: invalid account id", ErrInvalid)
	}
	scope := access.PlatformScope()
	if accountID != "" {
		scope = access.AccountScope(accountID)
	}
	if err := access.Authorize(ctx, s.db, actor.Principal, access.UsageRead, scope); err != nil {
		return nil, Page{}, err
	}
	where := `FROM account_monthly_usage WHERE ($1='' OR account_id::text=$1)`
	return s.mapPage(ctx, `SELECT count(*) `+where, `SELECT account_id::text,period,api_requests,updated_at `+where+` ORDER BY period DESC, account_id DESC LIMIT $2 OFFSET $3`, []any{accountID}, limit, offset, []string{"account_id", "period", "api_requests", "updated_at"})
}

func (s *Service) Credentials(ctx context.Context, actor Actor, accountID string, limit, offset int) ([]map[string]any, Page, error) {
	if accountID != "" && !validUUID(accountID) {
		return nil, Page{}, fmt.Errorf("%w: invalid account id", ErrInvalid)
	}
	scope := access.PlatformScope()
	if accountID != "" {
		scope = access.AccountScope(accountID)
	}
	if err := access.Authorize(ctx, s.db, actor.Principal, access.CredentialsRead, scope); err != nil {
		return nil, Page{}, err
	}
	where := `FROM credentials WHERE ($1='' OR account_id::text=$1)`
	return s.mapPage(ctx, `SELECT count(*) `+where, `SELECT id::text,principal_id::text,account_id::text,kind,audience,name,key_prefix,assurance,created_at,last_used_at,expires_at,revoked_at `+where+` ORDER BY created_at DESC, id DESC LIMIT $2 OFFSET $3`, []any{accountID}, limit, offset, []string{"id", "principal_id", "account_id", "kind", "audience", "name", "key_prefix", "assurance", "created_at", "last_used_at", "expires_at", "revoked_at"})
}

func (s *Service) RevokeCredential(ctx context.Context, actor Actor, id, reason string) error {
	if !validUUID(id) {
		return fmt.Errorf("%w: invalid credential id", ErrInvalid)
	}
	var accountID *string
	if err := s.db.QueryRow(ctx, `SELECT account_id::text FROM credentials WHERE id=$1`, id).Scan(&accountID); errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	} else if err != nil {
		return err
	}
	scope := access.PlatformScope()
	if accountID != nil {
		scope = access.AccountScope(*accountID)
	}
	return s.mutateScoped(ctx, actor, access.CredentialsRevoke, scope, "credential.revoked", "credential", id, reason, func(tx pgx.Tx) (any, any, error) {
		var before any
		if err := tx.QueryRow(ctx, `SELECT revoked_at FROM credentials WHERE id=$1 FOR UPDATE`, id).Scan(&before); errors.Is(err, pgx.ErrNoRows) {
			return nil, nil, ErrNotFound
		} else if err != nil {
			return nil, nil, err
		}
		_, err := tx.Exec(ctx, `UPDATE credentials SET revoked_at=COALESCE(revoked_at,now()) WHERE id=$1`, id)
		return map[string]any{"revoked_at": before}, map[string]bool{"revoked": true}, err
	})
}

func (s *Service) Probes(ctx context.Context, actor Actor, limit, offset int) ([]map[string]any, Page, error) {
	if err := access.Authorize(ctx, s.db, actor.Principal, access.ProbesManage, access.PlatformScope()); err != nil {
		return nil, Page{}, err
	}
	columns := []string{"id", "region", "name", "key_prefix", "version", "created_at", "last_seen_at", "last_ip", "revoked_at"}
	return s.mapPage(ctx, `SELECT count(*) FROM probes`,
		`SELECT id::text,region,name,key_prefix,version,created_at,last_seen_at,host(last_ip),revoked_at FROM probes
		ORDER BY revoked_at IS NOT NULL, region, created_at DESC LIMIT $1 OFFSET $2`, nil, limit, offset, columns)
}

func (s *Service) CreateProbe(ctx context.Context, actor Actor, in ProbeInput) (CreatedProbe, error) {
	region := strings.TrimSpace(in.Region)
	name := strings.TrimSpace(in.Name)
	if !probeRegionPattern.MatchString(region) {
		return CreatedProbe{}, fmt.Errorf("%w: region must be lowercase letters, digits and dashes, up to 32 characters", ErrInvalid)
	}
	if name == "" || len(name) > 64 {
		return CreatedProbe{}, fmt.Errorf("%w: name must be 1 to 64 characters", ErrInvalid)
	}
	id := uuid.NewV7()
	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		return CreatedProbe{}, err
	}
	created := CreatedProbe{ID: id.String(), Region: region, Name: name, Token: probe.TokenPrefix + hex.EncodeToString(secret), Endpoint: s.cfg.APIOrigin}
	hash := config.HashSecret(created.Token)
	prefix := created.Token[:len(probe.TokenPrefix)+6]
	err := s.mutateScoped(ctx, actor, access.ProbesManage, access.PlatformScope(), "probe.created", "probe", created.ID, in.Reason, func(tx pgx.Tx) (any, any, error) {
		_, err := tx.Exec(ctx, `INSERT INTO probes(id,region,name,key_prefix,secret_hash,creator_id) VALUES($1,$2,$3,$4,$5,$6)`,
			created.ID, region, name, prefix, hash, actor.Principal.ID)
		return nil, map[string]string{"region": region, "name": name, "key_prefix": prefix}, err
	})
	if err != nil {
		return CreatedProbe{}, err
	}
	return created, nil
}

func (s *Service) RevokeProbe(ctx context.Context, actor Actor, id, reason string) error {
	if !validUUID(id) {
		return fmt.Errorf("%w: invalid probe id", ErrInvalid)
	}
	return s.mutateScoped(ctx, actor, access.ProbesManage, access.PlatformScope(), "probe.revoked", "probe", id, reason, func(tx pgx.Tx) (any, any, error) {
		var before *time.Time
		if err := tx.QueryRow(ctx, `SELECT revoked_at FROM probes WHERE id=$1 FOR UPDATE`, id).Scan(&before); errors.Is(err, pgx.ErrNoRows) {
			return nil, nil, ErrNotFound
		} else if err != nil {
			return nil, nil, err
		}
		_, err := tx.Exec(ctx, `UPDATE probes SET revoked_at=COALESCE(revoked_at,now()), revoker_id=COALESCE(revoker_id,$2) WHERE id=$1`, id, actor.Principal.ID)
		return map[string]any{"revoked_at": before}, map[string]bool{"revoked": true}, err
	})
}

func (s *Service) ReconcileBilling(ctx context.Context, actor Actor, accountID, reason string) error {
	if !validUUID(accountID) {
		return fmt.Errorf("%w: invalid account id", ErrInvalid)
	}
	if s.billing == nil {
		return errors.New("billing provider unavailable")
	}
	if err := access.Authorize(ctx, s.db, actor.Principal, access.BillingReconcile, access.AccountScope(accountID)); err != nil {
		return err
	}

	type localSubscription struct {
		code, status, plan, currency, providerCode string
		amount                                     int64
	}
	subscriptions := make([]localSubscription, 0)
	rows, err := s.db.Query(ctx, `SELECT provider_subscription_code,status,plan,currency,amount_minor,COALESCE(provider_plan_code,'') FROM billing_subscriptions WHERE account_id=$1 AND billing_mode='auto' AND status IN ('active','past_due','non_renewing')`, accountID)
	if err != nil {
		return err
	}
	for rows.Next() {
		var item localSubscription
		if err = rows.Scan(&item.code, &item.status, &item.plan, &item.currency, &item.amount, &item.providerCode); err != nil {
			rows.Close()
			return err
		}
		subscriptions = append(subscriptions, item)
	}
	rows.Close()
	if err = rows.Err(); err != nil {
		return err
	}
	type checkout struct {
		reference, plan, currency, status, providerCode, checkoutType string
		amount                                                        int64
	}
	checkouts := make([]checkout, 0)
	checkoutRows, err := s.db.Query(ctx, `SELECT provider_reference,plan,amount_minor,currency,status,COALESCE(provider_plan_code,''),checkout_type FROM billing_checkouts WHERE account_id=$1 AND billing_mode='auto' AND status IN ('pending','initialized') AND created_at>now()-interval '7 days'`, accountID)
	if err != nil {
		return err
	}
	for checkoutRows.Next() {
		var item checkout
		if err = checkoutRows.Scan(&item.reference, &item.plan, &item.amount, &item.currency, &item.status, &item.providerCode, &item.checkoutType); err != nil {
			checkoutRows.Close()
			return err
		}
		checkouts = append(checkouts, item)
	}
	checkoutRows.Close()
	if err = checkoutRows.Err(); err != nil {
		return err
	}

	type subscriptionResult struct {
		code, before, after string
		nextPayment         *time.Time
	}
	type checkoutResult struct {
		reference, before, after string
	}
	subscriptionResults := make([]subscriptionResult, 0, len(subscriptions))
	for _, item := range subscriptions {
		sub, fetchErr := s.billing.FetchSubscription(ctx, item.code)
		if fetchErr != nil {
			return fmt.Errorf("fetch subscription %s: %w", item.code, fetchErr)
		}
		status, ok := paystack.MapSubscriptionStatus(sub.Status)
		if !ok {
			continue
		}
		pricing, validPlan := s.cfg.Pricing(item.plan, item.currency)
		pricing.Amount = item.amount
		if item.providerCode != "" {
			pricing.Code = item.providerCode
		}
		if (status == "active" || status == "past_due" || status == "non_renewing") && (!validPlan || !validSubscription(sub, pricing.Amount, item.currency, pricing.Code)) {
			return fmt.Errorf("subscription %s invariant mismatch", item.code)
		}
		subscriptionResults = append(subscriptionResults, subscriptionResult{item.code, item.status, status, paystack.ParseTime(sub.NextPaymentDate)})
	}
	checkoutResults := make([]checkoutResult, 0, len(checkouts))
	transactionIDs := make(map[string]string, len(checkouts))
	for _, item := range checkouts {
		remote, verifyErr := s.billing.Verify(ctx, item.reference)
		if verifyErr != nil {
			return fmt.Errorf("verify checkout %s: %w", item.reference, verifyErr)
		}
		if remote.Status == "success" {
			pricing, validPlan := s.cfg.Pricing(item.plan, item.currency)
			if item.providerCode != "" {
				pricing.Code = item.providerCode
			}
			if item.checkoutType == "upgrade" {
				pricing.Code = ""
			}
			if !validPlan || !paystack.ValidatePayment(remote, accountID, item.amount, item.currency, pricing.Code, item.plan) {
				return fmt.Errorf("checkout %s invariant mismatch", item.reference)
			}
			transactionIDs[item.reference] = strconv.FormatInt(remote.ID, 10)
		}
		checkoutResults = append(checkoutResults, checkoutResult{item.reference, item.status, remote.Status})
	}

	return s.mutateScoped(ctx, actor, access.BillingReconcile, access.AccountScope(accountID), "billing.reconciled", "account", accountID, reason, func(tx pgx.Tx) (any, any, error) {
		var accountPlan string
		if err := tx.QueryRow(ctx, `SELECT plan FROM accounts WHERE id=$1 FOR UPDATE`, accountID).Scan(&accountPlan); errors.Is(err, pgx.ErrNoRows) {
			return nil, nil, ErrNotFound
		} else if err != nil {
			return nil, nil, err
		}
		changes := make([]map[string]any, 0, len(subscriptionResults)+len(checkoutResults))
		for _, item := range subscriptionResults {
			next := item.nextPayment
			var err error
			switch item.after {
			case "active":
				_, err = tx.Exec(ctx, `UPDATE billing_subscriptions SET status='active',grace_until=NULL,cancel_at_period_end=false,current_period_end=COALESCE($2,current_period_end),updated_at=now() WHERE provider_subscription_code=$1`, item.code, next)
			case "past_due":
				_, err = tx.Exec(ctx, `UPDATE billing_subscriptions SET status='past_due',grace_until=COALESCE(grace_until,now()+$2::interval),current_period_end=COALESCE($3,current_period_end),updated_at=now() WHERE provider_subscription_code=$1`, item.code, s.cfg.BillingGracePeriod.String(), next)
			case "non_renewing":
				_, err = tx.Exec(ctx, `UPDATE billing_subscriptions SET status='non_renewing',cancel_at_period_end=true,current_period_end=COALESCE($2,current_period_end),updated_at=now() WHERE provider_subscription_code=$1`, item.code, next)
			case "canceled", "complete":
				_, err = tx.Exec(ctx, `UPDATE billing_subscriptions SET status=$2,grace_until=NULL,updated_at=now() WHERE provider_subscription_code=$1`, item.code, item.after)
			}
			if err != nil {
				return nil, nil, err
			}
			changes = append(changes, map[string]any{"subscription": item.code, "before": item.before, "after": item.after})
		}
		for _, item := range checkoutResults {
			var err error
			switch item.after {
			case "success":
				_, err = tx.Exec(ctx, `UPDATE billing_checkouts SET status='successful',provider_transaction_id=COALESCE(NULLIF($2,''),provider_transaction_id),completed_at=COALESCE(completed_at,now()),updated_at=now() WHERE provider_reference=$1`, item.reference, transactionIDs[item.reference])
			case "failed", "abandoned", "cancelled", "canceled":
				_, err = tx.Exec(ctx, `UPDATE billing_checkouts SET status='failed',updated_at=now() WHERE provider_reference=$1`, item.reference)
			}
			if err != nil {
				return nil, nil, err
			}
			changes = append(changes, map[string]any{"checkout": item.reference, "before": item.before, "after": item.after})
		}
		var active bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM billing_subscriptions WHERE account_id=$1 AND status IN ('active','past_due','non_renewing') UNION ALL SELECT 1 FROM billing_checkouts WHERE account_id=$1 AND status='successful')`, accountID).Scan(&active); err != nil {
			return nil, nil, err
		}
		newPlan := "free"
		if active {
			_ = tx.QueryRow(ctx, `SELECT plan FROM billing_subscriptions WHERE account_id=$1 AND status IN ('active','past_due','non_renewing') ORDER BY (billing_mode='comp' AND status='active') DESC,created_at DESC LIMIT 1`, accountID).Scan(&newPlan)
			if newPlan == "free" {
				_ = tx.QueryRow(ctx, `SELECT plan FROM billing_checkouts WHERE account_id=$1 AND status='successful' ORDER BY completed_at DESC NULLS LAST,created_at DESC LIMIT 1`, accountID).Scan(&newPlan)
			}
		}
		if _, err := tx.Exec(ctx, `UPDATE accounts SET plan=$2,updated_at=now() WHERE id=$1 AND status='active'`, accountID, newPlan); err != nil {
			return nil, nil, err
		}
		return map[string]any{"plan": accountPlan}, map[string]any{"plan": newPlan, "changes": changes}, nil
	})
}

func (s *Service) GrantComplimentaryPlan(ctx context.Context, actor Actor, accountID string, input ComplimentaryInput) error {
	if !validUUID(accountID) {
		return fmt.Errorf("%w: invalid account id", ErrInvalid)
	}
	plan := strings.TrimSpace(input.Plan)
	if !domain.IsPaidPlan(plan) {
		return fmt.Errorf("%w: complimentary plan must be starter, pro or business", ErrInvalid)
	}
	expiresAt, err := parseComplimentaryExpiry(input.ExpiresAt)
	if err != nil {
		return err
	}
	if err = access.Authorize(ctx, s.db, actor.Principal, access.BillingComp, access.AccountScope(accountID)); err != nil {
		return err
	}
	if err = s.stopProviderBilling(ctx, accountID); err != nil {
		return fmt.Errorf("stop provider billing before granting a complimentary plan: %w", err)
	}
	err = s.mutateScoped(ctx, actor, access.BillingComp, access.AccountScope(accountID), "billing.complimentary_granted", "account", accountID, input.Reason, func(tx pgx.Tx) (any, any, error) {
		var accountStatus, accountPlan string
		if scanErr := tx.QueryRow(ctx, `SELECT status,plan FROM accounts WHERE id=$1 FOR UPDATE`, accountID).Scan(&accountStatus, &accountPlan); errors.Is(scanErr, pgx.ErrNoRows) {
			return nil, nil, ErrNotFound
		} else if scanErr != nil {
			return nil, nil, scanErr
		}
		if accountStatus != domain.AccountStatusActive {
			return nil, nil, fmt.Errorf("%w: only an active account can be granted a complimentary plan", ErrInvalid)
		}
		var replacedPlan *string
		if scanErr := tx.QueryRow(ctx, `SELECT plan FROM billing_subscriptions WHERE account_id=$1 AND billing_mode='comp' AND status='active' FOR UPDATE`, accountID).Scan(&replacedPlan); scanErr != nil && !errors.Is(scanErr, pgx.ErrNoRows) {
			return nil, nil, scanErr
		}
		if _, execErr := tx.Exec(ctx, `UPDATE billing_subscriptions SET status='canceled',grace_until=NULL,updated_at=now() WHERE account_id=$1 AND billing_mode='comp' AND status='active'`, accountID); execErr != nil {
			return nil, nil, execErr
		}
		superseded, execErr := tx.Exec(ctx, `UPDATE billing_subscriptions SET status='canceled',cancel_at_period_end=true,grace_until=NULL,scheduled_plan=NULL,scheduled_plan_effective_at=NULL,updated_at=now() WHERE account_id=$1 AND billing_mode<>'comp' AND status IN ('active','past_due','non_renewing')`, accountID)
		if execErr != nil {
			return nil, nil, execErr
		}
		if _, execErr = tx.Exec(ctx, `UPDATE billing_checkouts SET status='expired',updated_at=now() WHERE account_id=$1 AND status IN ('pending','initialized')`, accountID); execErr != nil {
			return nil, nil, execErr
		}
		if _, execErr = tx.Exec(ctx, `INSERT INTO billing_subscriptions(account_id,provider,provider_subscription_code,plan,status,amount_minor,currency,current_period_start,current_period_end,billing_mode,effective_at) VALUES($1,'internal',$2,$3,'active',0,$4,now(),$5,'comp',now())`, accountID, complimentarySubscriptionCode(), plan, s.cfg.PaystackCurrency, expiresAt); execErr != nil {
			return nil, nil, execErr
		}
		if _, execErr = tx.Exec(ctx, `UPDATE accounts SET plan=$2,updated_at=now() WHERE id=$1`, accountID, plan); execErr != nil {
			return nil, nil, execErr
		}
		before := map[string]any{"plan": accountPlan, "complimentary_plan": replacedPlan}
		after := map[string]any{"plan": plan, "complimentary_plan": plan, "complimentary_expires_at": expiresAt, "superseded_subscriptions": superseded.RowsAffected()}
		return before, after, nil
	})
	if err != nil {
		return err
	}
	return nil
}

func (s *Service) stopProviderBilling(ctx context.Context, accountID string) error {
	if s.billing == nil {
		return nil
	}
	type subscription struct{ code, token string }
	subscriptions := make([]subscription, 0)
	rows, err := s.db.Query(ctx, `SELECT provider_subscription_code,provider_email_token_encrypted FROM billing_subscriptions WHERE account_id=$1 AND billing_mode='auto' AND status IN ('active','past_due','non_renewing') AND provider_email_token_encrypted IS NOT NULL`, accountID)
	if err != nil {
		return err
	}
	for rows.Next() {
		var item subscription
		if err = rows.Scan(&item.code, &item.token); err != nil {
			rows.Close()
			return err
		}
		subscriptions = append(subscriptions, item)
	}
	rows.Close()
	if err = rows.Err(); err != nil {
		return err
	}
	for _, item := range subscriptions {
		plain, decryptErr := config.Open(s.cfg.AppEncryptionKey, item.token)
		if decryptErr != nil {
			return fmt.Errorf("decrypt subscription token %s: %w", item.code, decryptErr)
		}
		if err = s.billing.DisableSubscription(ctx, item.code, plain); err != nil {
			return fmt.Errorf("disable subscription %s: %w", item.code, err)
		}
	}
	return nil
}

func (s *Service) RevokeComplimentaryPlan(ctx context.Context, actor Actor, accountID, reason string) error {
	if !validUUID(accountID) {
		return fmt.Errorf("%w: invalid account id", ErrInvalid)
	}
	if err := access.Authorize(ctx, s.db, actor.Principal, access.BillingComp, access.AccountScope(accountID)); err != nil {
		return err
	}
	err := s.mutateScoped(ctx, actor, access.BillingComp, access.AccountScope(accountID), "billing.complimentary_revoked", "account", accountID, reason, func(tx pgx.Tx) (any, any, error) {
		var code, plan string
		if scanErr := tx.QueryRow(ctx, `SELECT provider_subscription_code,plan FROM billing_subscriptions WHERE account_id=$1 AND billing_mode='comp' AND status='active' FOR UPDATE`, accountID).Scan(&code, &plan); errors.Is(scanErr, pgx.ErrNoRows) {
			return nil, nil, ErrNotFound
		} else if scanErr != nil {
			return nil, nil, scanErr
		}
		if _, execErr := tx.Exec(ctx, `UPDATE billing_subscriptions SET status='canceled',grace_until=NULL,updated_at=now() WHERE provider_subscription_code=$1`, code); execErr != nil {
			return nil, nil, execErr
		}
		var newPlan string
		if execErr := tx.QueryRow(ctx, `UPDATE accounts SET plan=COALESCE((SELECT plan FROM billing_subscriptions WHERE account_id=$1 AND effective_at<=now() AND status IN ('active','past_due','non_renewing') ORDER BY (billing_mode='comp' AND status='active') DESC,effective_at DESC,created_at DESC LIMIT 1),'free'),updated_at=now() WHERE id=$1 RETURNING plan`, accountID).Scan(&newPlan); execErr != nil {
			return nil, nil, execErr
		}
		return map[string]any{"plan": plan, "complimentary_plan": plan}, map[string]any{"plan": newPlan, "complimentary_plan": nil}, nil
	})
	if err != nil {
		return err
	}
	return nil
}

func complimentarySubscriptionCode() string {
	return "COMP-" + uuid.NewV7().String()
}

func parseComplimentaryExpiry(raw string) (*time.Time, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	expiresAt, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		return nil, fmt.Errorf("%w: expires_at must be an RFC3339 timestamp", ErrInvalid)
	}
	expiresAt = expiresAt.UTC()
	if !expiresAt.After(time.Now().UTC()) {
		return nil, fmt.Errorf("%w: expires_at must be in the future", ErrInvalid)
	}
	return &expiresAt, nil
}

func (s *Service) Collection(ctx context.Context, actor Actor, kind, status string, limit, offset int, accountID string) ([]map[string]any, Page, error) {
	if accountID != "" && !validUUID(accountID) {
		return nil, Page{}, fmt.Errorf("%w: invalid account id", ErrInvalid)
	}
	var capability, where, selectList, order string
	var columns []string
	switch kind {
	case "jobs":
		capability, where, selectList, order, columns = access.JobsRead, `FROM email_jobs WHERE ($1='' OR ($1='pending' AND sent_at IS NULL) OR ($1='sent' AND sent_at IS NOT NULL)) AND ($2='' OR account_id::text=$2)`, `SELECT id::text,account_id::text,to_email,template,attempts,next_attempt_at,sent_at,last_error,created_at`, `ORDER BY created_at DESC, id DESC`, []string{"id", "account_id", "to_email", "template", "attempts", "next_attempt_at", "sent_at", "last_error", "created_at"}
	case "billing":
		capability, where, selectList, order, columns = access.BillingRead, `FROM billing_subscriptions WHERE ($1='' OR status=$1) AND ($2='' OR account_id::text=$2)`, `SELECT id::text,account_id::text,provider,provider_subscription_code,plan,status,amount_minor,currency,current_period_start,current_period_end,grace_until,cancel_at_period_end,created_at,updated_at`, `ORDER BY updated_at DESC, id DESC`, []string{"id", "account_id", "provider", "provider_subscription_code", "plan", "status", "amount_minor", "currency", "current_period_start", "current_period_end", "grace_until", "cancel_at_period_end", "created_at", "updated_at"}
	case "checkouts":
		capability, where, selectList, order, columns = access.BillingRead, `FROM billing_checkouts WHERE ($1='' OR status=$1) AND ($2='' OR account_id::text=$2)`, `SELECT id::text,account_id::text,provider,provider_reference,plan,amount_minor,currency,status,checkout_type,created_at,completed_at`, `ORDER BY created_at DESC, id DESC`, []string{"id", "account_id", "provider", "provider_reference", "plan", "amount_minor", "currency", "status", "checkout_type", "created_at", "completed_at"}
	case "events":
		capability, where, selectList, order, columns = access.BillingRead, `FROM billing_events WHERE ($1='' OR status=$1) AND ($2='' OR account_id::text=$2)`, `SELECT id::text,provider,event_type,provider_reference,account_id::text,status,error_message,received_at,processed_at`, `ORDER BY received_at DESC, id DESC`, []string{"id", "provider", "event_type", "provider_reference", "account_id", "status", "error_message", "received_at", "processed_at"}
	case "grants":
		capability, where, selectList, order, columns = access.AccessRead, `FROM access_grants WHERE ($1='' OR principal_id::text=$1) AND ($2='' OR scope_id::text=$2)`, `SELECT id::text,principal_id::text,capability,scope_type,scope_id::text,grantor_id::text,reason,created_at,expires_at,revoked_at,revoker_id::text,revocation_reason`, `ORDER BY created_at DESC, id DESC`, []string{"id", "principal_id", "capability", "scope_type", "scope_id", "grantor_id", "reason", "created_at", "expires_at", "revoked_at", "revoker_id", "revocation_reason"}
	case "members":
		capability, where, selectList, order, columns = access.MembersRead, `FROM account_memberships m JOIN accounts a ON a.id=m.account_id LEFT JOIN principal_identities i ON i.principal_id=m.principal_id AND i.deleted_at IS NULL WHERE ($1='' OR m.status=$1) AND ($2='' OR m.account_id::text=$2)`, `SELECT m.principal_id::text,m.account_id::text,a.plan,COALESCE(i.verified_email,''),m.role,m.status,m.created_at,m.deleted_at`, `ORDER BY m.created_at DESC, m.principal_id DESC`, []string{"principal_id", "account_id", "plan", "email", "role", "status", "created_at", "deleted_at"}
	case "invites":
		capability, where, selectList, order, columns = access.MembersRead, `FROM account_invites v WHERE ($1='' OR ($1='pending' AND v.accepted_at IS NULL AND v.revoked_at IS NULL AND v.expires_at>now()) OR ($1='accepted' AND v.accepted_at IS NOT NULL) OR ($1='revoked' AND v.revoked_at IS NOT NULL) OR ($1='expired' AND v.accepted_at IS NULL AND v.revoked_at IS NULL AND v.expires_at<=now())) AND ($2='' OR v.account_id::text=$2)`, `SELECT v.id::text,v.account_id::text,v.email,v.role,v.inviter_id::text,v.expires_at,v.accepted_at,v.revoked_at,v.created_at`, `ORDER BY v.created_at DESC, v.id DESC`, []string{"id", "account_id", "email", "role", "inviter_id", "expires_at", "accepted_at", "revoked_at", "created_at"}
	case "lifecycle":
		capability, where, selectList, order, columns = access.AccountsRead, `FROM accounts a WHERE a.status='closed' AND ($1='' OR ($1='pending' AND a.purged_at IS NULL) OR ($1='erased' AND a.purged_at IS NOT NULL)) AND ($2='' OR a.id::text=$2)`, `SELECT a.id::text,a.plan,a.deleted_at,a.purged_at,(SELECT count(*) FROM credentials c WHERE c.account_id=a.id),(SELECT count(*) FROM account_memberships m WHERE m.account_id=a.id),a.created_at`, `ORDER BY a.deleted_at DESC NULLS LAST, a.id DESC`, []string{"account_id", "plan", "closed_at", "purged_at", "credentials", "memberships", "created_at"}
	case "audit":
		capability, where, selectList, order, columns = access.AuditRead, `FROM audit_events WHERE ($1='' OR principal_id::text=$1 OR target_id=$1) AND ($2='' OR account_id::text=$2)`, `SELECT id::text,account_id::text,principal_id::text,credential_id::text,capability,action,target_type,target_id,reason,before_state,after_state,request_id,source_ip::text,user_agent,created_at`, `ORDER BY created_at DESC, id DESC`, []string{"id", "account_id", "principal_id", "credential_id", "capability", "action", "target_type", "target_id", "reason", "before_state", "after_state", "request_id", "source_ip", "user_agent", "created_at"}
	default:
		return nil, Page{}, fmt.Errorf("%w: unknown collection", ErrInvalid)
	}
	scope := access.PlatformScope()
	if accountID != "" && (kind == "billing" || kind == "checkouts" || kind == "events" || kind == "jobs" || kind == "audit" || kind == "members" || kind == "invites") {
		scope = access.AccountScope(accountID)
	}
	if err := access.Authorize(ctx, s.db, actor.Principal, capability, scope); err != nil {
		return nil, Page{}, err
	}
	return s.mapPage(ctx, `SELECT count(*) `+where, selectList+` `+where+` `+order+` LIMIT $3 OFFSET $4`, []any{status, accountID}, limit, offset, columns)
}

func (s *Service) RetryJob(ctx context.Context, actor Actor, id, reason string) error {
	if !validUUID(id) {
		return fmt.Errorf("%w: invalid email job id", ErrInvalid)
	}
	var accountID *string
	if err := s.db.QueryRow(ctx, `SELECT account_id::text FROM email_jobs WHERE id=$1`, id).Scan(&accountID); errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	} else if err != nil {
		return err
	}
	scope := access.PlatformScope()
	if accountID != nil {
		scope = access.AccountScope(*accountID)
	}
	return s.mutateScoped(ctx, actor, access.JobsRetry, scope, "email_job.retried", "email_job", id, reason, func(tx pgx.Tx) (any, any, error) {
		var sent any
		if err := tx.QueryRow(ctx, `SELECT sent_at FROM email_jobs WHERE id=$1 FOR UPDATE`, id).Scan(&sent); errors.Is(err, pgx.ErrNoRows) {
			return nil, nil, ErrNotFound
		} else if err != nil {
			return nil, nil, err
		}
		if sent != nil {
			return nil, nil, fmt.Errorf("%w: sent email jobs cannot be retried", ErrInvalid)
		}
		_, err := tx.Exec(ctx, `UPDATE email_jobs SET sent_at=NULL,attempts=0,last_error=NULL,next_attempt_at=now() WHERE id=$1`, id)
		return map[string]any{"sent_at": sent}, map[string]bool{"queued": true}, err
	})
}

func (s *Service) Grant(ctx context.Context, actor Actor, input GrantInput) error {
	if !validUUID(input.PrincipalID) {
		return fmt.Errorf("%w: invalid principal id", ErrInvalid)
	}
	if _, ok := knownCapability(input.Capability); !ok {
		return fmt.Errorf("%w: unknown capability", ErrInvalid)
	}
	if !domain.IsScopeType(input.ScopeType) {
		return fmt.Errorf("%w: invalid scope", ErrInvalid)
	}
	if input.ScopeType == domain.ScopeTypeAccount && input.ScopeID == "" {
		return fmt.Errorf("%w: account scope requires scope_id", ErrInvalid)
	}
	if input.ScopeID != "" && !validUUID(input.ScopeID) {
		return fmt.Errorf("%w: invalid scope id", ErrInvalid)
	}
	if input.ScopeType == domain.ScopeTypePlatform && input.ScopeID != "" {
		return fmt.Errorf("%w: platform scope cannot have scope_id", ErrInvalid)
	}
	target := input.PrincipalID + ":" + input.Capability
	return s.mutate(ctx, actor, access.AccessGrant, "access.granted", "principal", target, input.Reason, func(tx pgx.Tx) (any, any, error) {
		var scopeID any
		if input.ScopeType == "account" {
			scopeID = input.ScopeID
		}
		_, err := tx.Exec(ctx, `INSERT INTO access_grants(principal_id,capability,scope_type,scope_id,grantor_id,reason) VALUES($1,$2,$3,$4,$5,$6) ON CONFLICT(principal_id,capability,scope_type,scope_id) DO UPDATE SET revoked_at=NULL,revoker_id=NULL,revocation_reason=NULL,grantor_id=EXCLUDED.grantor_id,reason=EXCLUDED.reason`, input.PrincipalID, input.Capability, input.ScopeType, scopeID, actor.Principal.ID, input.Reason)
		return nil, input, err
	})
}

func (s *Service) RevokeGrant(ctx context.Context, actor Actor, id, reason string) error {
	if !validUUID(id) {
		return fmt.Errorf("%w: invalid grant id", ErrInvalid)
	}
	return s.mutate(ctx, actor, access.AccessGrant, "access.revoked", "access_grant", id, reason, func(tx pgx.Tx) (any, any, error) {
		var principalID, capability string
		var revokedAt any
		if err := tx.QueryRow(ctx, `SELECT principal_id::text,capability,revoked_at FROM access_grants WHERE id=$1 FOR UPDATE`, id).Scan(&principalID, &capability, &revokedAt); errors.Is(err, pgx.ErrNoRows) {
			return nil, nil, ErrNotFound
		} else if err != nil {
			return nil, nil, err
		}
		if principalID == actor.Principal.ID && capability == access.AccessGrant {
			return nil, nil, fmt.Errorf("%w: cannot revoke your own access delegation capability", ErrInvalid)
		}
		_, err := tx.Exec(ctx, `UPDATE access_grants SET revoked_at=COALESCE(revoked_at,now()),revoker_id=$2,revocation_reason=$3 WHERE id=$1`, id, actor.Principal.ID, reason)
		return map[string]any{"revoked_at": revokedAt}, map[string]bool{"revoked": true}, err
	})
}

func (s *Service) mutate(ctx context.Context, actor Actor, capability, action, targetType, targetID, reason string, fn func(pgx.Tx) (any, any, error)) error {
	return s.mutateScoped(ctx, actor, capability, access.PlatformScope(), action, targetType, targetID, reason, fn)
}

func (s *Service) mutateScoped(ctx context.Context, actor Actor, capability string, scope access.Scope, action, targetType, targetID, reason string, fn func(pgx.Tx) (any, any, error)) error {
	reason = strings.TrimSpace(reason)
	if len(reason) < 3 || len(reason) > 1000 {
		return fmt.Errorf("%w: reason must be between 3 and 1000 characters", ErrInvalid)
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer dbx.Rollback(ctx, tx)
	if err = access.Authorize(ctx, tx, actor.Principal, capability, scope); err != nil {
		return err
	}
	before, after, err := fn(tx)
	if err != nil {
		return err
	}
	beforeJSON, err := json.Marshal(before)
	if err != nil {
		return fmt.Errorf("marshal audit before state: %w", err)
	}
	afterJSON, err := json.Marshal(after)
	if err != nil {
		return fmt.Errorf("marshal audit after state: %w", err)
	}
	_, err = tx.Exec(ctx, `INSERT INTO audit_events(account_id,principal_id,credential_id,capability,action,target_type,target_id,reason,before_state,after_state,request_id,source_ip,user_agent) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,NULLIF($11,''),NULLIF($12,'')::inet,NULLIF($13,''))`, nullableAuditAccountID(scope), actor.Principal.ID, actor.Principal.CredentialID, capability, action, targetType, targetID, reason, nullableJSON(beforeJSON), nullableJSON(afterJSON), actor.RequestID, actor.SourceIP, actor.UserAgent)
	if err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func scanMaps(rows pgx.Rows, columns []string) ([]map[string]any, error) {
	items := make([]map[string]any, 0)
	for rows.Next() {
		values, err := rows.Values()
		if err != nil {
			return nil, err
		}
		item := make(map[string]any, len(columns))
		for i, name := range columns {
			item[name] = values[i]
		}
		items = append(items, item)
	}
	return items, rows.Err()
}
func boundedLimit(n int) int {
	if n <= 0 {
		return 10
	}
	if n > 500 {
		return 500
	}
	return n
}

func parsePage(limit, offset int) (int, int, error) {
	if offset < 0 {
		return 0, 0, fmt.Errorf("%w: invalid offset", ErrInvalid)
	}
	if offset > 100_000 {
		return 0, 0, fmt.Errorf("%w: invalid offset", ErrInvalid)
	}
	return boundedLimit(limit), offset, nil
}

func (s *Service) mapPage(ctx context.Context, countSQL, listSQL string, args []any, limit, offset int, columns []string) ([]map[string]any, Page, error) {
	limit, offset, err := parsePage(limit, offset)
	if err != nil {
		return nil, Page{}, err
	}
	page := Page{Limit: limit, Offset: offset}
	if err = s.db.QueryRow(ctx, countSQL, args...).Scan(&page.Total); err != nil {
		return nil, page, err
	}
	listArgs := append(append([]any{}, args...), limit, offset)
	rows, err := s.db.Query(ctx, listSQL, listArgs...)
	if err != nil {
		return nil, page, err
	}
	defer rows.Close()
	items, err := scanMaps(rows, columns)
	return items, page, err
}
func nullableJSON(raw []byte) any {
	if string(raw) == "null" {
		return nil
	}
	return raw
}
func nullableAuditAccountID(scope access.Scope) any {
	if scope.Type == domain.ScopeTypeAccount && scope.ID != "" {
		return scope.ID
	}
	return nil
}
func validateAccountTransition(before, after string) error {
	if before == domain.AccountStatusClosed && after != domain.AccountStatusClosed {
		return fmt.Errorf("%w: closed accounts cannot be reopened", ErrInvalid)
	}
	return nil
}
func knownCapability(value string) (string, bool) {
	for _, set := range access.PermissionSets {
		if slices.Contains(set, value) {
			return value, true
		}
	}
	return "", false
}

func validSubscription(sub paystack.Subscription, amount int64, currency, planCode string) bool {
	providerAmount := sub.Amount
	if providerAmount == 0 {
		providerAmount = sub.Plan.Amount
	}
	providerCurrency := sub.Currency
	if providerCurrency == "" {
		providerCurrency = sub.Plan.Currency
	}
	return providerAmount == amount && providerCurrency == currency && (planCode == "" || sub.Plan.Code == planCode)
}

func (s *Service) disableAccountSubscriptions(ctx context.Context, accountID string) error {
	if s.billing == nil {
		return nil
	}
	rows, err := s.db.Query(ctx, `SELECT provider_subscription_code,COALESCE(provider_email_token_encrypted,'') FROM billing_subscriptions WHERE account_id=$1 AND status IN ('active','past_due','non_renewing','canceled') AND cancel_at_period_end=true`, accountID)
	if err != nil {
		return err
	}
	defer rows.Close()
	type item struct{ code, token string }
	subscriptions := make([]item, 0)
	for rows.Next() {
		var row item
		if err = rows.Scan(&row.code, &row.token); err != nil {
			return err
		}
		subscriptions = append(subscriptions, row)
	}
	if err = rows.Err(); err != nil {
		return err
	}
	for _, sub := range subscriptions {
		if sub.token == "" {
			continue
		}
		plain, decryptErr := config.Open(s.cfg.AppEncryptionKey, sub.token)
		if decryptErr != nil {
			return fmt.Errorf("decrypt subscription token %s: %w", sub.code, decryptErr)
		}
		if err = s.billing.DisableSubscription(ctx, sub.code, plain); err != nil {
			return fmt.Errorf("disable subscription %s: %w", sub.code, err)
		}
	}
	return nil
}

func validUUID(value string) bool { _, err := uuid.Parse(value); return err == nil }

type PlanInput struct {
	usage.Plan
	Reason string `json:"reason"`
}

func (s *Service) Plans(ctx context.Context, actor Actor) ([]usage.Plan, error) {
	if err := access.Authorize(ctx, s.db, actor.Principal, access.BillingRead, access.PlatformScope()); err != nil {
		return nil, err
	}
	if err := s.catalog.Reload(ctx, s.db); err != nil {
		return nil, err
	}
	return s.catalog.List(), nil
}

func (s *Service) UpdatePlan(ctx context.Context, actor Actor, name string, input PlanInput) error {
	input.Plan.Plan = name
	input.Currency = "KES"
	if err := input.Plan.Validate(); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	err := s.mutate(ctx, actor, access.PlansManage, "plan.updated", "plan", name, input.Reason, func(tx pgx.Tx) (any, any, error) {
		before, ok := s.catalog.Get(name)
		if !ok {
			return nil, nil, ErrNotFound
		}
		var raw []byte
		var version int64
		queryErr := tx.QueryRow(ctx, `SELECT definition,version FROM plan_catalog WHERE plan=$1 FOR UPDATE`, name).Scan(&raw, &version)
		if queryErr != nil && !errors.Is(queryErr, pgx.ErrNoRows) {
			return nil, nil, queryErr
		}
		if queryErr == nil {
			if err := json.Unmarshal(raw, &before); err != nil {
				return nil, nil, err
			}
			before.Version = version
		}
		if input.Version != before.Version {
			return nil, nil, ErrConflict
		}
		pricingChanged := input.PriceMinor != before.PriceMinor || input.KESPlanCode != before.KESPlanCode || input.USDPlanCode != before.USDPlanCode
		if s.cfg.PaymentsEnabled() && name != "free" && pricingChanged {
			if input.KESPlanCode == "" {
				return nil, nil, fmt.Errorf("%w: a KES provider plan is required", ErrInvalid)
			}
			if input.PriceMinor != before.PriceMinor && ((before.KESPlanCode != "" && before.KESPlanCode == input.KESPlanCode) || (before.USDPlanCode != "" && before.USDPlanCode == input.USDPlanCode)) {
				return nil, nil, fmt.Errorf("%w: new prices require new provider plan codes", ErrInvalid)
			}
			for currency, code := range map[string]string{"KES": input.KESPlanCode, "USD": input.USDPlanCode} {
				if code == "" {
					continue
				}
				expected := input.PriceMinor
				if currency == "USD" {
					expected = expected/100 + (expected%100+50)/100
				}
				remote, err := s.billing.FetchPlan(ctx, code)
				if err != nil {
					return nil, nil, fmt.Errorf("%w: cannot verify the %s provider plan", ErrInvalid, currency)
				}
				if remote.PlanCode != code || remote.Currency != currency || remote.Amount != expected || remote.Interval != "monthly" {
					return nil, nil, fmt.Errorf("%w: the %s provider plan must match the monthly price", ErrInvalid, currency)
				}
			}
		}
		for currency, code := range map[string]string{"KES": before.KESPlanCode, "USD": before.USDPlanCode} {
			if code == "" {
				continue
			}
			if _, err := tx.Exec(ctx, `UPDATE billing_checkouts SET provider_plan_code=$3 WHERE plan=$1 AND currency=$2 AND provider_plan_code IS NULL`, name, currency, code); err != nil {
				return nil, nil, err
			}
			if _, err := tx.Exec(ctx, `UPDATE billing_subscriptions SET provider_plan_code=$3 WHERE plan=$1 AND currency=$2 AND provider_plan_code IS NULL`, name, currency, code); err != nil {
				return nil, nil, err
			}
		}
		input.Version = before.Version + 1
		definition, err := json.Marshal(input.Plan)
		if err != nil {
			return nil, nil, err
		}
		tag, err := tx.Exec(ctx, `INSERT INTO plan_catalog(plan,definition,version,editor_id) VALUES($1,$2,1,$3) ON CONFLICT(plan) DO UPDATE SET definition=EXCLUDED.definition,version=plan_catalog.version+1,editor_id=EXCLUDED.editor_id,updated_at=now() WHERE plan_catalog.version=$4`, name, definition, actor.Principal.ID, before.Version)
		if err != nil {
			return nil, nil, err
		}
		if tag.RowsAffected() != 1 {
			return nil, nil, ErrConflict
		}
		return before, input.Plan, nil
	})
	if err != nil {
		return err
	}
	return s.catalog.Reload(ctx, s.db)
}
