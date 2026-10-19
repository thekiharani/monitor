package postgres

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/thekiharani/monitor/internal/cloud/config"
)

const connectTimeout = 5 * time.Second

func Open(ctx context.Context, c config.Config) (*pgxpool.Pool, error) {
	return open(ctx, c, c.DatabaseURL, "monitorcloud/"+c.Env)
}

func OpenTenant(ctx context.Context, c config.Config) (*pgxpool.Pool, error) {
	return open(ctx, c, c.AppDatabaseURL, "monitor-tenant/"+c.Env)
}

func open(ctx context.Context, c config.Config, url, name string) (*pgxpool.Pool, error) {
	pc, err := pgxpool.ParseConfig(url)
	if err != nil {
		return nil, err
	}
	pc.MaxConns = c.DBMaxConns
	pc.MinConns = c.DBMinConns
	pc.MaxConnLifetime = c.DBMaxConnLifetime
	pc.MaxConnLifetimeJitter = c.DBMaxConnLifetime / 5
	pc.MaxConnIdleTime = c.DBMaxConnIdleTime
	pc.HealthCheckPeriod = time.Minute
	pc.ConnConfig.ConnectTimeout = connectTimeout

	if pc.ConnConfig.RuntimeParams == nil {
		pc.ConnConfig.RuntimeParams = map[string]string{}
	}
	pc.ConnConfig.RuntimeParams["application_name"] = name
	pc.ConnConfig.RuntimeParams["statement_timeout"] = "30000"
	pc.ConnConfig.RuntimeParams["idle_in_transaction_session_timeout"] = "60000"

	pc.ConnConfig.DefaultQueryExecMode = pgx.QueryExecModeCacheStatement

	return pgxpool.NewWithConfig(ctx, pc)
}
