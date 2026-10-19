package app

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/thekiharani/monitor/internal/cloud/usage"
)

const sharedLimitTimeout = 2 * time.Second

func (a *App) allowShared(ctx context.Context, key string, limits usage.Limits) bool {
	if limits.Burst <= 0 || limits.RequestsPerSecond <= 0 {
		return true
	}
	bucketCtx, cancel := context.WithTimeout(ctx, sharedLimitTimeout)
	defer cancel()

	var tokens float64
	err := a.db.QueryRow(bucketCtx,
		`INSERT INTO rate_buckets AS b (bucket_key, tokens, refilled_at)
		 VALUES ($1, $2::float8 - 1, now())
		 ON CONFLICT (bucket_key) DO UPDATE
		 SET tokens = LEAST($2::float8,
		         b.tokens + EXTRACT(EPOCH FROM (now() - b.refilled_at)) * $3::float8) - 1,
		     refilled_at = now()
		 WHERE LEAST($2::float8,
		         b.tokens + EXTRACT(EPOCH FROM (now() - b.refilled_at)) * $3::float8) >= 1
		 RETURNING tokens`,
		key, float64(limits.Burst), limits.RequestsPerSecond,
	).Scan(&tokens)

	if errors.Is(err, pgx.ErrNoRows) {
		return false
	}
	if err != nil {
		a.logError("shared rate limit unavailable", "rate_limit", "consume_shared_token", err, "bucket", key)
		return true
	}
	return true
}
