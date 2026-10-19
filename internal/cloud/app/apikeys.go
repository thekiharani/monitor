package app

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"

	"uuid"

	"github.com/jackc/pgx/v5"

	"github.com/thekiharani/monitor/internal/cloud/config"
)

type issuedKey struct {
	id, name, secret, prefix string
}

func (a *App) keyMarker() string {
	if a.cfg.Env == "production" {
		return "usk_live_"
	}
	return "usk_test_"
}

func (a *App) mintAPIKey(name string) (issuedKey, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return issuedKey{}, err
	}
	marker := a.keyMarker()
	secret := marker + base64.RawURLEncoding.EncodeToString(raw)
	return issuedKey{
		id:     uuid.NewV7().String(),
		name:   name,
		secret: secret,
		prefix: keyPrefix(secret, marker),
	}, nil
}

func insertAPIKey(ctx context.Context, tx pgx.Tx, key issuedKey, principalID, accountID string, allowed int) error {
	if _, err := tx.Exec(ctx, `SELECT id FROM accounts WHERE id=$1 FOR UPDATE`, accountID); err != nil {
		return err
	}
	var live int
	if err := tx.QueryRow(ctx,
		`SELECT count(*) FROM credentials
		 WHERE account_id=$1 AND audience='customer_api' AND revoked_at IS NULL AND expires_at>now()`,
		accountID,
	).Scan(&live); err != nil {
		return err
	}
	if live >= allowed {
		return errPlanLimit
	}
	_, err := tx.Exec(ctx,
		`INSERT INTO credentials(id,principal_id,account_id,kind,audience,name,key_prefix,secret_hash,expires_at)
		 VALUES($1,$2,$3,'api_key','customer_api',$4,$5,$6,now()+interval '1 year')`,
		key.id, principalID, accountID, key.name, key.prefix, config.HashSecret(key.secret),
	)
	return err
}

func (a *App) workspaceForKey(ctx context.Context, principalID, accountID string) (string, string, error) {
	var plan, role string
	err := a.tenantRow(ctx, accountID,
		`SELECT acc.plan,m.role FROM account_memberships m
		 JOIN accounts acc ON acc.id=m.account_id AND acc.deleted_at IS NULL AND acc.status='active'
		 WHERE m.principal_id=$1 AND m.account_id=$2 AND m.status='active' AND m.deleted_at IS NULL`,
		[]any{principalID, accountID}, &plan, &role)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", "", errNoSuchWorkspace
	}
	if err != nil {
		return "", "", err
	}
	return plan, role, nil
}

func (a *App) issueAPIKey(ctx context.Context, name, principalID, accountID, plan string) (issuedKey, error) {
	key, err := a.mintAPIKey(name)
	if err != nil {
		return issuedKey{}, err
	}
	err = a.withTenant(ctx, accountID, func(tx pgx.Tx) error {
		return insertAPIKey(ctx, tx, key, principalID, accountID, a.planLimits(plan).MaxAPIKeys)
	})
	return key, err
}
