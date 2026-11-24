package main

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"strconv"
	"strings"
	"testing"

	"golang.org/x/crypto/bcrypt"

	"github.com/strazahq/straza/internal/config"
	"github.com/strazahq/straza/internal/server"
	"github.com/strazahq/straza/internal/store"
)

// shownOnce is the sentence the command prints under the password.
const shownOnce = "It is shown only this once and signs in the account break-glass under Emergency sign-in on the console's sign-in card, so store it in your vault now."

// enterpriseSQLite points the config loader at the sqlite driver, which the
// enterprise profile does not default to, and answers the flags that select
// the enterprise profile and the database at path.
func enterpriseSQLite(t *testing.T, path string) []string {
	t.Helper()
	t.Setenv("STRAZA_STORE_DRIVER", "sqlite")
	return []string{"--profile", "enterprise", "--store-dsn", path}
}

// addBreakGlass writes the break-glass admin with hash into the database at
// path, as the first boot of strazad serve would.
func addBreakGlass(t *testing.T, path, hash string) {
	t.Helper()
	st, err := store.Open(config.Config{Store: config.Store{Driver: config.DriverSQLite, DSN: path}})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	if _, err := st.Users().Create(context.Background(), store.User{Username: server.BreakGlassUsername, Display: "Break-glass admin", PasswordHash: hash}); err != nil {
		t.Fatal(err)
	}
}

// breakGlassHash reads the stored hash of the break-glass admin straight
// from the database at path, so a schema this binary does not expect is
// read too. A database without the row answers "".
func breakGlassHash(t *testing.T, path string) string {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	var hash string
	err = db.QueryRow(`SELECT password_hash FROM users WHERE username = ?`, server.BreakGlassUsername).Scan(&hash)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		t.Fatal(err)
	}
	return hash
}

// TestBreakGlassSetPasswordPrintsOnce pins the command on an enterprise
// store, for an admin with no password and for one whose password an
// operator lost: it prints a new password and the sentence under it on
// stdout, and the stored hash opens with that password and no other.
func TestBreakGlassSetPasswordPrintsOnce(t *testing.T) {
	old, err := bcrypt.GenerateFromPassword([]byte("lost-password"), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name, hash string
	}{
		{name: "an admin with no password", hash: ""},
		{name: "an admin whose password was lost", hash: string(old)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path, _ := migratedStore(t)
			addBreakGlass(t, path, tc.hash)
			out, err := runStrazad(t, append([]string{"break-glass", "set-password"}, enterpriseSQLite(t, path)...)...)
			if err != nil {
				t.Fatalf("set-password failed: %v", err)
			}
			lines := strings.Split(strings.TrimSuffix(out, "\n"), "\n")
			if len(lines) != 2 || !strings.HasPrefix(lines[0], "New break-glass password: ") || lines[1] != shownOnce {
				t.Fatalf("printed %q, want the password line and the sentence", out)
			}
			password := strings.TrimPrefix(lines[0], "New break-glass password: ")
			hash := breakGlassHash(t, path)
			if password == "" || bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)) != nil {
				t.Errorf("the stored hash does not open with the printed password")
			}
			if bcrypt.CompareHashAndPassword([]byte(hash), []byte("lost-password")) == nil {
				t.Errorf("the old password still opens the stored hash")
			}
		})
	}
}

// TestBreakGlassSetPasswordRefuses pins the command's refusals: each says
// what failed and what to do, prints no password, and leaves the stored
// hash as it was.
func TestBreakGlassSetPasswordRefuses(t *testing.T) {
	const kept = "kept-hash"
	exec := func(stmt string) func(t *testing.T, path string, head int) {
		return func(t *testing.T, path string, head int) {
			db, err := sql.Open("sqlite", path)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = db.Close() }()
			if _, err := db.Exec(strings.ReplaceAll(stmt, "HEAD+1", strconv.Itoa(head+1))); err != nil {
				t.Fatal(err)
			}
		}
	}
	cases := []struct {
		name       string
		standalone bool
		noAdmin    bool
		prepare    func(t *testing.T, path string, head int)
		want       func(head int) string
	}{
		{
			name: "the standalone profile", standalone: true,
			want: func(int) string {
				return "the settings this command read select the standalone profile, and the command works on the enterprise profile only, so nothing changed. " +
					"On standalone the first boot prints the break-glass password in the server log, and an admin sets a new one with `strazactl users set-password break-glass --password <new password>`. " +
					"For an enterprise server, give the command the same --config, --profile, --data-dir and --store-dsn that strazad serve runs with"
			},
		},
		{
			name: "a store strazad serve never booted on", noAdmin: true,
			want: func(int) string {
				return "the database holds no break-glass admin, because strazad serve has not booted on it yet, so nothing changed. " +
					"Start strazad serve once with these settings, which creates the admin, then run the command again"
			},
		},
		{
			name: "a database behind this binary",
			prepare: func(t *testing.T, path string, head int) {
				st, err := store.Open(config.Config{Store: config.Store{Driver: config.DriverSQLite, DSN: path}})
				if err != nil {
					t.Fatal(err)
				}
				defer func() { _ = st.Close() }()
				if _, err := st.(schemaMover).MigrateTo(context.Background(), uint(head-1)); err != nil {
					t.Fatal(err)
				}
			},
			want: func(head int) string {
				return "the database is at migration " + strconv.Itoa(head-1) + ", and this strazad expects migration " + strconv.Itoa(head) +
					", so nothing changed. Run strazad migrate --to " + strconv.Itoa(head) + " with the same flags first, then run the command again"
			},
		},
		{
			name:    "a database ahead of this binary",
			prepare: exec(`UPDATE schema_migrations SET version = HEAD+1`),
			want: func(head int) string {
				return "the database is at migration " + strconv.Itoa(head+1) + ", and this strazad knows migrations up to " + strconv.Itoa(head) +
					", so nothing changed. Run the command with a strazad release that has migration " + strconv.Itoa(head+1)
			},
		},
		{
			name:    "a database marked dirty",
			prepare: exec(`UPDATE schema_migrations SET dirty = 1`),
			want: func(head int) string {
				return "the database is marked dirty at migration " + strconv.Itoa(head) + ", because a migration failed part way. " +
					"Restore the backup taken before the upgrade, or repair the schema by hand, then run the command again"
			},
		},
		{
			name:    "a database older than this binary's first migration",
			prepare: exec(`UPDATE schema_migrations SET version = 36`),
			want: func(head int) string {
				return "the database is at migration 36, and this strazad's migrations start at 37, so nothing changed. " +
					"Upgrade the database with an earlier strazad release whose migrations reach 37, then run strazad migrate --to " +
					strconv.Itoa(head) + " with this release and run the command again"
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path, head := migratedStore(t)
			if !tc.noAdmin {
				addBreakGlass(t, path, kept)
			}
			if tc.prepare != nil {
				tc.prepare(t, path, head)
			}
			args := enterpriseSQLite(t, path)
			if tc.standalone {
				args = []string{"--store-dsn", path}
			}
			out, err := runStrazad(t, append([]string{"break-glass", "set-password"}, args...)...)
			if err == nil {
				t.Fatalf("set-password succeeded and printed %q, want a refusal", out)
			}
			if want := tc.want(head); !strings.Contains(err.Error(), want) {
				t.Errorf("refusal %q lacks %q", err, want)
			}
			if strings.Contains(out, "password:") {
				t.Errorf("a refused command printed a password: %q", out)
			}
			want := kept
			if tc.noAdmin {
				want = ""
			}
			if got := breakGlassHash(t, path); got != want {
				t.Errorf("the refused command left the stored hash %q, want %q", got, want)
			}
		})
	}
}

// TestBreakGlassSetPasswordUnreachableDatabase pins the refusal of a
// Postgres database the command cannot open or reach: one sentence says
// what failed, that nothing changed and what to check, and it carries
// neither the DSN nor its password.
func TestBreakGlassSetPasswordUnreachableDatabase(t *testing.T) {
	for _, name := range []string{"STRAZA_CONFIG", "STRAZA_PROFILE", "STRAZA_DATA_DIR", "STRAZA_STORE_DRIVER", "STRAZA_STORE_DSN"} {
		t.Setenv(name, "")
	}
	const want = "the command could not open or reach the database, so nothing changed. " +
		"Check that the database is running and reachable from this host and that its user and password are right, " +
		"and give the command the same --config, --profile, --data-dir and --store-dsn that strazad serve runs with"
	cases := []struct{ name, dsn string }{
		{"a refused connection", "postgres://straza:s3cret@127.0.0.1:1/straza?sslmode=disable"},
		{"a URL that does not parse", "postgres://straza:s3cret@[bad/straza"},
		{"a key and value DSN", "host=127.0.0.1 port=1 user=straza password=s3cret dbname=straza"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Chdir(t.TempDir())
			out, err := runStrazad(t, "break-glass", "set-password", "--profile", "enterprise", "--store-dsn", tc.dsn)
			if err == nil || err.Error() != want {
				t.Fatalf("set-password = %v, printed %q, want %q", err, out, want)
			}
			if strings.Contains(err.Error()+out, "s3cret") || strings.Contains(err.Error()+out, "127.0.0.1") {
				t.Errorf("the refusal names the DSN or its password: %q %q", err, out)
			}
		})
	}
}

// TestBreakGlassSetPasswordRefusesAnEmptyDatabase pins the refusal of a
// sqlite file that holds no Straza migration, the database a mistyped
// --data-dir or --store-dsn opens. migratedStore clears the environment,
// and its file is then emptied.
func TestBreakGlassSetPasswordRefusesAnEmptyDatabase(t *testing.T) {
	path, _ := migratedStore(t)
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	out, err := runStrazad(t, append([]string{"break-glass", "set-password"}, enterpriseSQLite(t, path)...)...)
	const want = "the database has no Straza migration applied, because strazad serve has not booted on it yet, so nothing changed. " +
		"If this is the right database, start strazad serve once with these settings, then run the command again. " +
		"If it is not, give the command the same --config, --profile, --data-dir and --store-dsn that strazad serve runs with"
	if err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("set-password = %v, printed %q, want the refusal %q", err, out, want)
	}
}
