package migrate

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/url"
	"os"
	"path"
	"slices"
	"strings"
	"time"

	"github.com/amacneil/dbmate/v2/pkg/dbmate"
	_ "github.com/amacneil/dbmate/v2/pkg/driver/postgres"
	"github.com/jackc/pgx/v5"
	"github.com/thekiharani/monitor/cloud/db"
)

const (
	lockKey     = 7_314_927_183_640_112_233
	lockTimeout = "5min"
)

var errUnknownCommand = errors.New("unknown command; use up, migrate, rollback or status")

func Run(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	command := "up"
	if len(args) > 1 {
		fmt.Fprintln(stderr, errUnknownCommand)
		return 2
	}
	if len(args) == 1 {
		command = args[0]
	}
	switch command {
	case "help", "--help", "-h":
		fmt.Fprintln(stdout, "usage: monitor-cloud migrate [up|migrate|status|rollback|down]")
		return 0
	case "up", "migrate", "status", "rollback", "down":
	default:
		fmt.Fprintln(stderr, errUnknownCommand)
		return 2
	}

	databaseURL := os.Getenv("DATABASE_URL")
	if databaseURL == "" {
		fmt.Fprintln(stderr, "migrate: DATABASE_URL must be set")
		return 2
	}

	conn, err := connect(ctx, databaseURL)
	if err != nil {
		fmt.Fprintln(stderr, "migrate: cannot reach the database:", err)
		return 1
	}
	defer conn.Close(context.WithoutCancel(ctx))

	if _, err = conn.Exec(ctx, `SET lock_timeout = '`+lockTimeout+`'`); err != nil {
		fmt.Fprintln(stderr, "migrate: cannot set a lock timeout:", err)
		return 1
	}
	if _, err = conn.Exec(ctx, `SELECT pg_advisory_lock($1)`, int64(lockKey)); err != nil {
		fmt.Fprintln(stderr, "migrate: another migration is already running:", err)
		return 1
	}
	defer func() {
		release, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		defer cancel()
		if _, unlockErr := conn.Exec(release, `SELECT pg_advisory_unlock($1)`, int64(lockKey)); unlockErr != nil {
			fmt.Fprintln(stderr, "migrate: could not release the migration lock:", unlockErr)
		}
	}()

	parsed, err := url.Parse(databaseURL)
	if err != nil {
		fmt.Fprintln(stderr, "migrate: DATABASE_URL is not a valid URL:", err)
		return 2
	}
	if command == "up" || command == "migrate" {
		if err := refuseRewrittenHistory(ctx, conn, db.Migrations); err != nil {
			fmt.Fprintln(stderr, "migrate:", err)
			return 1
		}
	}
	if err = runCommand(newMigrator(parsed, stdout), command); err != nil {
		fmt.Fprintln(stderr, "migrate:", err)
		if errors.Is(err, errUnknownCommand) {
			return 2
		}
		return 1
	}
	return 0
}

func newMigrator(databaseURL *url.URL, out io.Writer) *dbmate.DB {
	migrator := dbmate.New(databaseURL)
	migrator.AutoDumpSchema = false
	migrator.FS = db.Migrations
	migrator.MigrationsDir = []string{"migrations"}
	migrator.Log = out
	return migrator
}

func runCommand(migrator *dbmate.DB, command string) error {
	switch command {
	case "up":
		return migrator.CreateAndMigrate()
	case "migrate":
		return migrator.Migrate()
	case "rollback", "down":
		return migrator.Rollback()
	case "status":
		_, err := migrator.Status(false)
		return err
	default:
		return fmt.Errorf("%w: %q", errUnknownCommand, command)
	}
}

func connect(ctx context.Context, url string) (*pgx.Conn, error) {
	deadline := time.Now().Add(waitTimeout())
	var lastErr error
	for {
		attempt, cancel := context.WithTimeout(ctx, 5*time.Second)
		conn, err := pgx.Connect(attempt, url)
		cancel()
		if err == nil {
			return conn, nil
		}
		lastErr = err
		if ctx.Err() != nil || time.Now().After(deadline) {
			return nil, lastErr
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(time.Second):
		}
	}
}

func waitTimeout() time.Duration {
	if raw := os.Getenv("DBMATE_WAIT_TIMEOUT"); raw != "" {
		if parsed, err := time.ParseDuration(raw); err == nil {
			return parsed
		}
	}
	return 60 * time.Second
}

func missingMigrations(applied []string, migrations fs.FS) ([]string, error) {
	files, err := fs.Glob(migrations, "migrations/*.sql")
	if err != nil {
		return nil, err
	}
	shipped := make(map[string]bool, len(files))
	for _, file := range files {
		version, _, _ := strings.Cut(path.Base(file), "_")
		shipped[version] = true
	}
	var missing []string
	for _, version := range applied {
		if !shipped[version] {
			missing = append(missing, version)
		}
	}
	slices.Sort(missing)
	return missing, nil
}

func refuseRewrittenHistory(ctx context.Context, conn *pgx.Conn, migrations fs.FS) error {
	var ledger *string
	if err := conn.QueryRow(ctx, `SELECT to_regclass('public.schema_migrations')::text`).Scan(&ledger); err != nil || ledger == nil {
		return err
	}
	rows, err := conn.Query(ctx, `SELECT version FROM public.schema_migrations`)
	if err != nil {
		return err
	}
	applied, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return err
	}
	missing, err := missingMigrations(applied, migrations)
	if err != nil || len(missing) == 0 {
		return err
	}
	return fmt.Errorf("applied migrations are missing from this release: %s", strings.Join(missing, ", "))
}
