package dbx

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
)

const CleanupTimeout = 15 * time.Second

func Cleanup(parent context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(parent), CleanupTimeout)
}

func Rollback(parent context.Context, tx pgx.Tx) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(parent), 5*time.Second)
	defer cancel()
	_ = tx.Rollback(ctx)
}
