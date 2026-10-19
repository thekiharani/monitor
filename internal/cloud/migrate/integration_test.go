package migrate

import (
	"bytes"
	"context"
	"fmt"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

func TestIntegrationEmbeddedMigrationLifecycleAndLock(t *testing.T) {
	raw := os.Getenv("TEST_DATABASE_URL")
	if raw == "" {
		t.Skip("TEST_DATABASE_URL not set")
	}
	ctx := context.Background()
	admin, err := pgx.Connect(ctx, raw)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close(ctx)
	name := fmt.Sprintf("monitor_migration_test_%d", time.Now().UnixNano())
	identifier := pgx.Identifier{name}.Sanitize()
	if _, err = admin.Exec(ctx, "CREATE DATABASE "+identifier); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if _, err := admin.Exec(ctx, "DROP DATABASE "+identifier+" WITH (FORCE)"); err != nil {
			t.Error(err)
		}
	}()
	cfg, err := pgx.ParseConfig(raw)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Database = name
	parsed, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	parsed.Path = "/" + name
	databaseURL := parsed.String()
	t.Setenv("DATABASE_URL", databaseURL)
	t.Setenv("DBMATE_MIGRATIONS_DIR", t.TempDir())
	conn, err := pgx.ConnectConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(ctx)
	for _, command := range []string{"up", "up", "status"} {
		var stdout, stderr bytes.Buffer
		if code := Run(ctx, []string{command}, &stdout, &stderr); code != 0 {
			t.Fatalf("%s exit=%d stdout=%s stderr=%s", command, code, &stdout, &stderr)
		}
	}
	if _, err = conn.Exec(ctx, `INSERT INTO schema_migrations(version) VALUES ('99999999999999')`); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	if code := Run(ctx, []string{"up"}, &stdout, &stderr); code == 0 || !strings.Contains(stderr.String(), "missing from this release") {
		t.Fatalf("history guard: exit=%d stderr=%s", code, &stderr)
	}
	if _, err = conn.Exec(ctx, `DELETE FROM schema_migrations WHERE version = '99999999999999'`); err != nil {
		t.Fatal(err)
	}
	if _, err = conn.Exec(ctx, `SELECT pg_advisory_lock($1)`, int64(lockKey)); err != nil {
		t.Fatal(err)
	}
	locked, cancel := context.WithTimeout(ctx, 200*time.Millisecond)
	defer cancel()
	stderr.Reset()
	if code := Run(locked, []string{"status"}, &stdout, &stderr); code == 0 || !strings.Contains(stderr.String(), "another migration") {
		t.Fatalf("lock guard: exit=%d stderr=%s", code, &stderr)
	}
	if _, err = conn.Exec(ctx, `SELECT pg_advisory_unlock($1)`, int64(lockKey)); err != nil {
		t.Fatal(err)
	}
	stderr.Reset()
	if code := Run(ctx, []string{"status"}, &stdout, &stderr); code != 0 {
		t.Fatalf("post-lock status exit=%d stderr=%s", code, &stderr)
	}
}
