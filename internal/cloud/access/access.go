package access

import (
	"context"
	"errors"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/thekiharani/monitor/internal/cloud/config"
	"github.com/thekiharani/monitor/internal/cloud/domain"
)

var (
	ErrUnauthenticated = errors.New("unauthenticated")
	ErrForbidden       = errors.New("forbidden")
)

const (
	AudienceCustomerSession = domain.CredentialAudienceCustomerSession
	AudienceCustomerAPI     = domain.CredentialAudienceCustomerAPI
	AudienceControlAPI      = domain.CredentialAudienceControlAPI

	ScopePlatform = domain.ScopeTypePlatform
	ScopeAccount  = domain.ScopeTypeAccount
)

const (
	AccountsRead       = "platform.accounts.read"
	AccountsSuspend    = "platform.accounts.suspend"
	AccountsClose      = "platform.accounts.close"
	MonitorsRead       = "platform.monitors.read"
	MonitorsModerate   = "platform.monitors.moderate"
	ProbesManage       = "platform.probes.manage"
	MembersRead        = "platform.members.read"
	MembersRemove      = "platform.members.remove"
	HealthRead         = "platform.health.read"
	PlansManage        = "platform.plans.manage"
	BillingRead        = "platform.billing.read"
	BillingReconcile   = "platform.billing.reconcile"
	BillingComp        = "platform.billing.comp"
	CredentialsRead    = "platform.credentials.read"
	CredentialsRevoke  = "platform.credentials.revoke"
	UsageRead          = "platform.usage.read"
	JobsRead           = "platform.jobs.read"
	JobsRetry          = "platform.jobs.retry"
	AccessRead         = "platform.access.read"
	AccessGrant        = "platform.access.grant"
	AuditRead          = "platform.audit.read"
	PlatformEverything = "platform.*"
)

var PermissionSets = map[string][]string{
	"platform.owner": {
		AccountsRead, AccountsSuspend, AccountsClose,
		MonitorsRead, MonitorsModerate, ProbesManage,
		MembersRead, MembersRemove,
		HealthRead,
		BillingRead, BillingReconcile, BillingComp, PlansManage,
		CredentialsRead, CredentialsRevoke,
		UsageRead,
		JobsRead, JobsRetry,
		AccessRead, AccessGrant, AuditRead,
	},
	"support.operator": {AccountsRead, AccountsSuspend, MonitorsRead, MembersRead, CredentialsRead, CredentialsRevoke, UsageRead, HealthRead},
	"trust.reviewer":   {AccountsRead, MonitorsRead, MonitorsModerate, MembersRead, MembersRemove},
	"billing.operator": {AccountsRead, BillingRead, BillingReconcile, UsageRead},
	"platform.viewer":  {AccountsRead, MonitorsRead, MembersRead, BillingRead, CredentialsRead, UsageRead, JobsRead, AccessRead, AuditRead, HealthRead},
}

type Principal struct {
	ID           string
	CredentialID string
	Kind         string
	Audience     string
	Assurance    string
	AccountID    string
	CreatedAt    time.Time
}

type Scope struct {
	Type string
	ID   string
}

func PlatformScope() Scope { return Scope{Type: ScopePlatform} }

func AccountScope(id string) Scope { return Scope{Type: ScopeAccount, ID: id} }

func Bearer(header string) string {
	parts := strings.Fields(header)
	if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") {
		return ""
	}
	return parts[1]
}

func Authenticate(ctx context.Context, db *pgxpool.Pool, raw, audience string) (Principal, error) {
	if raw == "" || !domain.IsCredentialAudience(audience) {
		return Principal{}, ErrUnauthenticated
	}
	var principal Principal
	var accountID *string
	err := db.QueryRow(ctx, `
		SELECT p.id::text,c.id::text,p.kind,c.audience,c.assurance,c.account_id::text,c.created_at
		FROM credentials c
		JOIN principals p ON p.id=c.principal_id
		WHERE c.secret_hash=$1 AND c.audience=$2 AND c.revoked_at IS NULL AND c.expires_at>now() AND p.status='active' AND p.deleted_at IS NULL
		  AND ($2<>'control_api' OR c.assurance IN ('multi_factor','workload'))
	`, config.HashSecret(raw), audience).Scan(
		&principal.ID,
		&principal.CredentialID,
		&principal.Kind,
		&principal.Audience,
		&principal.Assurance,
		&accountID,
		&principal.CreatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return Principal{}, ErrUnauthenticated
	}
	if err != nil {
		return Principal{}, err
	}
	if !domain.IsPrincipalKind(principal.Kind) || !domain.IsCredentialAudience(principal.Audience) || !domain.IsCredentialAssurance(principal.Assurance) {
		return Principal{}, ErrUnauthenticated
	}
	if accountID != nil {
		principal.AccountID = *accountID
	}
	_, _ = db.Exec(ctx, `UPDATE credentials SET last_used_at=now() WHERE id=$1 AND (last_used_at IS NULL OR last_used_at<now()-interval '5 minutes')`, principal.CredentialID)
	return principal, nil
}

type QueryRower interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}

func Authorize(ctx context.Context, db QueryRower, principal Principal, capability string, scope Scope) error {
	if principal.ID == "" || principal.Audience != AudienceControlAPI {
		return ErrForbidden
	}
	if !domain.IsScopeType(scope.Type) {
		return ErrForbidden
	}
	var allowed bool
	err := db.QueryRow(ctx, `
		SELECT EXISTS(
			SELECT 1 FROM access_grants
			WHERE principal_id=$1
			  AND capability IN ($2,$3)
			  AND revoked_at IS NULL
			  AND (expires_at IS NULL OR expires_at>now())
			  AND (
			    ($4='platform' AND scope_type='platform' AND scope_id IS NULL)
			    OR ($4='account' AND (
			      (scope_type='platform' AND scope_id IS NULL)
			      OR (scope_type='account' AND scope_id=$5::uuid)
			    ))
			  )
		)
	`, principal.ID, capability, PlatformEverything, scope.Type, nullableScopeID(scope)).Scan(&allowed)
	if err != nil {
		return err
	}
	if !allowed {
		return ErrForbidden
	}
	return nil
}

func HasAnyGrant(ctx context.Context, db QueryRower, principalID string) error {
	var allowed bool
	if err := db.QueryRow(ctx, `
		SELECT EXISTS(
			SELECT 1 FROM access_grants
			WHERE principal_id=$1 AND scope_type='platform' AND revoked_at IS NULL
			  AND (expires_at IS NULL OR expires_at>now())
		)`, principalID).Scan(&allowed); err != nil {
		return err
	}
	if !allowed {
		return ErrForbidden
	}
	return nil
}

func PlatformCapabilities(ctx context.Context, db *pgxpool.Pool, principalID string) ([]string, error) {
	rows, err := db.Query(ctx, `
		SELECT DISTINCT capability
		FROM access_grants
		WHERE principal_id=$1 AND scope_type='platform' AND revoked_at IS NULL
		  AND (expires_at IS NULL OR expires_at>now())
		ORDER BY capability
	`, principalID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	capabilities := make([]string, 0)
	for rows.Next() {
		var capability string
		if err = rows.Scan(&capability); err != nil {
			return nil, err
		}
		capabilities = append(capabilities, capability)
	}
	return capabilities, rows.Err()
}

func Capabilities(permissionSet string) ([]string, bool) {
	items, ok := PermissionSets[permissionSet]
	if !ok {
		return nil, false
	}
	return slices.Clone(items), true
}

func KnownCapabilities() []string {
	seen := make(map[string]struct{})
	for _, set := range PermissionSets {
		for _, capability := range set {
			seen[capability] = struct{}{}
		}
	}
	return slices.Sorted(maps.Keys(seen))
}

func CredentialExpiry(kind string, now time.Time) time.Time {
	if kind == domain.PrincipalKindService {
		return now.Add(90 * 24 * time.Hour)
	}
	return now.Add(12 * time.Hour)
}

func nullableScopeID(scope Scope) any {
	if scope.Type == ScopePlatform {
		return nil
	}
	return scope.ID
}
