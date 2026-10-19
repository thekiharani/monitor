package app

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"

	"github.com/thekiharani/monitor/internal/cloud/dbx"
)

var (
	errNoTenant           = errors.New("tenant scope requires an account")
	errPlanLimit          = errors.New("plan limit reached")
	errAlreadyMember      = errors.New("already a member of the account")
	errMemberNotFound     = errors.New("no such active membership")
	errBillingInvalid     = errors.New("billing request rejected")
	errBillingUnavailable = errors.New("billing state unusable")
	errCheckoutPending    = errors.New("another checkout is already open")
	errNoSuchWorkspace    = errors.New("no active membership on the requested workspace")
)

func (a *App) withTenant(ctx context.Context, accountID string, run func(pgx.Tx) error) error {
	return a.tenantTx(ctx, accountID, pgx.TxOptions{}, run)
}

func (a *App) readTenant(ctx context.Context, accountID string, run func(pgx.Tx) error) error {
	return a.tenantTx(ctx, accountID, pgx.TxOptions{AccessMode: pgx.ReadOnly}, run)
}

func (a *App) withTenantAsPrincipal(ctx context.Context, accountID, principalID string, run func(pgx.Tx) error) error {
	if accountID == "" || principalID == "" {
		return errNoTenant
	}
	tx, err := a.tenant.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return err
	}
	defer dbx.Rollback(ctx, tx)
	if _, err = tx.Exec(ctx, `SELECT set_config('monitor.account_id', $1, true), set_config('monitor.principal_id', $2, true)`, accountID, principalID); err != nil {
		return err
	}
	if err = run(tx); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (a *App) tenantTx(ctx context.Context, accountID string, options pgx.TxOptions, run func(pgx.Tx) error) error {
	if accountID == "" {
		return errNoTenant
	}
	tx, err := a.tenant.BeginTx(ctx, options)
	if err != nil {
		return err
	}
	defer dbx.Rollback(ctx, tx)
	if _, err = tx.Exec(ctx, `SELECT set_config('monitor.account_id', $1, true)`, accountID); err != nil {
		return err
	}
	if err = run(tx); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (a *App) tenantRow(ctx context.Context, accountID, query string, args []any, dest ...any) error {
	return a.readTenant(ctx, accountID, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, query, args...).Scan(dest...)
	})
}

func (a *App) tenantRows(ctx context.Context, accountID, query string, args []any, scan func(pgx.Rows) error) error {
	return a.readTenant(ctx, accountID, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, query, args...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			if err = scan(rows); err != nil {
				return err
			}
		}
		return rows.Err()
	})
}

func (a *App) tenantExec(ctx context.Context, accountID, query string, args ...any) (int64, error) {
	var affected int64
	err := a.withTenant(ctx, accountID, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, query, args...)
		affected = tag.RowsAffected()
		return err
	})
	return affected, err
}
