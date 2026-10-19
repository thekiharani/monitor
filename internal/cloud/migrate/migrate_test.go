package migrate

import (
	"bytes"
	"context"
	"io/fs"
	"net/url"
	"reflect"
	"testing"
	"testing/fstest"

	"github.com/thekiharani/monitor/cloud/db"
)

func TestMissingMigrationsRejectsOnlyAbsentVersions(t *testing.T) {
	files := fstest.MapFS{"migrations/001_initial.sql": {}, "migrations/002_accounts.sql": {}}
	got, err := missingMigrations([]string{"002", "004", "001", "003"}, files)
	if err != nil || !reflect.DeepEqual(got, []string{"003", "004"}) {
		t.Fatalf("missing=%v err=%v", got, err)
	}
}

func TestEmbeddedMigrationsAreAvailableWithoutFilesystemConfiguration(t *testing.T) {
	u, _ := url.Parse("postgres://localhost/monitor")
	m := newMigrator(u, &bytes.Buffer{})
	files, err := fs.Glob(m.FS, "migrations/*.sql")
	if err != nil || len(files) == 0 {
		t.Fatalf("files=%v err=%v", files, err)
	}
	missing, err := missingMigrations(nil, db.Migrations)
	if err != nil || len(missing) != 0 {
		t.Fatalf("missing=%v err=%v", missing, err)
	}
	if m.AutoDumpSchema {
		t.Fatal("migration must not write a schema dump")
	}
}

func TestCommandsValidateBeforeDatabaseConfiguration(t *testing.T) {
	t.Setenv("DATABASE_URL", "")
	for _, tc := range []struct {
		args []string
		code int
	}{
		{[]string{"--help"}, 0}, {[]string{"unknown"}, 2}, {[]string{"up", "extra"}, 2}, {[]string{"up"}, 2},
	} {
		var out, errOut bytes.Buffer
		if got := Run(context.Background(), tc.args, &out, &errOut); got != tc.code {
			t.Fatalf("args=%v code=%d stderr=%s", tc.args, got, &errOut)
		}
		if out.Len()+errOut.Len() == 0 {
			t.Fatal("missing diagnostic")
		}
	}
}
