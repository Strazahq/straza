package main

import (
	"bytes"
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/strazahq/straza/internal/config"
	"github.com/strazahq/straza/internal/store"
	"github.com/strazahq/straza/internal/store/storetest"
)

// migratedStore writes a sqlite database migrated to the newest embedded
// migration and answers its path and that migration's version. It clears
// the environment the config loader reads, so that the shell running the
// test cannot point the command at another database.
func migratedStore(t *testing.T) (string, int) {
	t.Helper()
	for _, name := range []string{"STRAZA_CONFIG", "STRAZA_PROFILE", "STRAZA_DATA_DIR", "STRAZA_STORE_DRIVER", "STRAZA_STORE_DSN"} {
		t.Setenv(name, "")
	}
	path := filepath.Join(t.TempDir(), "straza.db")
	storetest.SeedSQLite(t, path)
	st, err := store.Open(config.Config{Store: config.Store{Driver: config.DriverSQLite, DSN: path}})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	if err := st.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	return path, schemaVersionAt(t, path)
}

// schemaVersionAt reads the migration version golang-migrate recorded in
// the sqlite database at path.
func schemaVersionAt(t *testing.T, path string) int {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	var v int
	if err := db.QueryRow(`SELECT version FROM schema_migrations`).Scan(&v); err != nil {
		t.Fatal(err)
	}
	return v
}

// runStrazad runs the strazad root command with args and answers what it
// printed and its error.
func runStrazad(t *testing.T, args ...string) (string, error) {
	t.Helper()
	root := rootCmd()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs(args)
	err := root.Execute()
	return out.String(), err
}

// TestMigrateCommandAnswersTheMigrationItFinds pins what the command
// prints for a database already at the version it names, through each way
// serve finds its database: the --store-dsn flag, the config file and
// --data-dir. A command that looked at any other database would refuse,
// because that one holds no migration. The embedded migrations are one
// baseline, so the moves themselves are pinned in the store's tests.
func TestMigrateCommandAnswersTheMigrationItFinds(t *testing.T) {
	cases := []struct {
		name string
		args func(t *testing.T, path string) []string
	}{
		{name: "the --store-dsn flag", args: func(_ *testing.T, path string) []string { return []string{"--store-dsn", path} }},
		{name: "the config file", args: func(t *testing.T, path string) []string {
			file := filepath.Join(t.TempDir(), "straza.yaml")
			if err := os.WriteFile(file, []byte("store:\n  driver: sqlite\n  dsn: "+path+"\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			return []string{"--config", file}
		}},
		{name: "serve's --data-dir", args: func(_ *testing.T, path string) []string { return []string{"--data-dir", filepath.Dir(path)} }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path, head := migratedStore(t)
			at := strconv.Itoa(head)
			out, err := runStrazad(t, append([]string{"migrate", "--to", at}, tc.args(t, path)...)...)
			if err != nil {
				t.Fatalf("migrate failed: %v", err)
			}
			if want := "The database is at migration " + at + " already. Nothing changed.\n"; out != want {
				t.Errorf("printed %q, want %q", out, want)
			}
			if got := schemaVersionAt(t, path); got != head {
				t.Errorf("the database is at migration %d, want %d", got, head)
			}
		})
	}
}

// TestMigrateCommandRefuses pins the command's refusals: each says what
// failed and what to do, and leaves the database where it was.
func TestMigrateCommandRefuses(t *testing.T) {
	cases := []struct {
		name    string
		args    func(head int) []string
		prepare func(t *testing.T, path string)
		want    func(head int) string
	}{
		{
			name: "no version",
			args: func(int) []string { return nil },
			want: func(int) string {
				return "the command needs --to <version>, the migration to move the database to. " +
					"The upgrade notes of each strazad release name its newest migration"
			},
		},
		{
			name: "a version above the newest migration",
			args: func(head int) []string { return []string{"--to", strconv.Itoa(head + 1)} },
			want: func(head int) string {
				return "this strazad knows migrations up to " + strconv.Itoa(head) + ", so it cannot move the database to " +
					strconv.Itoa(head+1) + ". Run the command with a strazad release that has migration " + strconv.Itoa(head+1)
			},
		},
		{
			name: "a version below the one migration this strazad has",
			args: func(head int) []string { return []string{"--to", strconv.Itoa(head - 1)} },
			want: func(head int) string {
				return "this strazad has no migration " + strconv.Itoa(head-1) + ". Its only migration is " + strconv.Itoa(head) +
					", so there is no older version to move the database to"
			},
		},
		{
			name: "a database marked dirty",
			args: func(head int) []string { return []string{"--to", strconv.Itoa(head)} },
			prepare: func(t *testing.T, path string) {
				db, err := sql.Open("sqlite", path)
				if err != nil {
					t.Fatal(err)
				}
				defer func() { _ = db.Close() }()
				if _, err := db.Exec(`UPDATE schema_migrations SET dirty = 1`); err != nil {
					t.Fatal(err)
				}
			},
			want: func(head int) string {
				return "the database is marked dirty at migration " + strconv.Itoa(head) + ", because a migration failed part way. " +
					"Restore the backup taken before the upgrade, or repair the schema by hand, then run the command again"
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path, head := migratedStore(t)
			if tc.prepare != nil {
				tc.prepare(t, path)
			}
			out, err := runStrazad(t, append([]string{"migrate", "--store-dsn", path}, tc.args(head)...)...)
			if err == nil {
				t.Fatalf("migrate succeeded and printed %q, want a refusal", out)
			}
			if want := tc.want(head); !strings.Contains(err.Error(), want) {
				t.Errorf("refusal %q lacks %q", err, want)
			}
			if got := schemaVersionAt(t, path); got != head {
				t.Errorf("the refused command moved the database to migration %d, want %d", got, head)
			}
		})
	}
}

// TestMigrateCommandCreatesNothingAndNamesNoPassword pins the command's
// refusals when the store settings point at no database: it creates no
// directory and no file, and no sentence it prints carries the password a
// DSN holds, also when the sqlite driver reads a Postgres DSN as a path.
func TestMigrateCommandCreatesNothingAndNamesNoPassword(t *testing.T) {
	const dsn = "postgres://straza:s3cret@127.0.0.1:1/straza?sslmode=disable"
	cases := []struct {
		name string
		args func(dir string) []string
		want string
	}{
		{
			name: "a Postgres DSN read by the sqlite driver",
			args: func(string) []string { return []string{"--store-dsn", dsn} },
			want: "the store driver is sqlite, and no database file exists where the store settings point, so nothing moved. " +
				"Give the command the same --config, --profile, --data-dir and --store-dsn that strazad serve runs with",
		},
		{
			name: "a mistyped sqlite path",
			args: func(dir string) []string { return []string{"--store-dsn", filepath.Join(dir, "typo", "straza.db")} },
			want: "the store driver is sqlite, and no database file exists where the store settings point, so nothing moved",
		},
		{
			name: "a Postgres DSN with serve's --profile",
			args: func(string) []string { return []string{"--profile", "enterprise", "--store-dsn", dsn} },
			want: "failed to connect",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, head := migratedStore(t)
			dir := t.TempDir()
			t.Chdir(dir)
			out, err := runStrazad(t, append([]string{"migrate", "--to", strconv.Itoa(head)}, tc.args(dir)...)...)
			if err == nil {
				t.Fatalf("migrate succeeded and printed %q, want a refusal", out)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("refusal %q lacks %q", err, tc.want)
			}
			if strings.Contains(err.Error()+out, "s3cret") {
				t.Errorf("the command printed the DSN's password: %q %q", out, err)
			}
			left, rerr := os.ReadDir(dir)
			if rerr != nil {
				t.Fatal(rerr)
			}
			for _, e := range left {
				t.Errorf("the command left %s in its working directory", e.Name())
			}
		})
	}
}
