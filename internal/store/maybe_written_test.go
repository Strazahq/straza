package store

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
)

// TestMaybeWritten pins which write errors leave the record's fate open:
// an answer from either database and a failure before anything was sent
// prove nothing was stored, while a context end, a cut connection, a
// connection error or termination answered after the send, and an unknown
// error may hide a commit.
func TestMaybeWritten(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	_, connErr := pgconn.Connect(ctx, "postgres://straza@127.0.0.1:1/straza?connect_timeout=1")
	if connErr == nil {
		t.Fatal("a connect to port 1 succeeded")
	}
	// The outbox insert through a store whose Postgres is down: the shape the
	// audit spool meets during an outage.
	down, err := openPostgres("postgres://straza@127.0.0.1:1/straza?connect_timeout=1")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = down.Close() })
	_, downErr := down.Outbox().Insert(ctx, OutboxEvent{Subject: "straza.audit.tool", CE: `{}`})
	if downErr == nil {
		t.Fatal("an insert into a Postgres on port 1 succeeded")
	}
	lite, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = lite.Close() })
	_, liteErr := lite.ExecContext(ctx, `INSERT INTO no_such_table VALUES (1)`)
	if liteErr == nil {
		t.Fatal("an insert into a missing table succeeded")
	}

	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"no error", nil, false},
		{"duplicate key", fmt.Errorf("%w: UNIQUE constraint failed", ErrConflict), false},
		{"invalid data", fmt.Errorf("%w: SQLSTATE 22021", ErrInvalidData), false},
		{"bad connection before send", driver.ErrBadConn, false},
		{"postgres refused the connect", connErr, false},
		{"outbox insert with postgres down", downErr, false},
		{"postgres answered", &pgconn.PgError{Code: "57P03", Message: "the database system is starting up"}, false},
		{"pooler lost its server connection", &pgconn.PgError{Code: "08P01", Message: "server conn crashed?"}, true},
		{"postgres terminated the session", &pgconn.PgError{Code: "57P01", Message: "terminating connection due to administrator command"}, true},
		{"postgres crash shutdown", &pgconn.PgError{Code: "57P02"}, true},
		{"sqlite answered", liteErr, false},
		{"deadline", context.DeadlineExceeded, true},
		{"cancel", fmt.Errorf("insert: %w", context.Canceled), true},
		{"connection cut mid-answer", io.ErrUnexpectedEOF, true},
		{"unknown", errors.New("something else"), true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := MaybeWritten(tc.err); got != tc.want {
				t.Fatalf("MaybeWritten(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}

// writeShape is one real write error of a driver and how the store must
// classify it.
type writeShape struct {
	name                   string
	err                    error
	refused, outage, maybe bool
}

// TestWriteErrorsOnBothDrivers drives each driver into a refused row and an
// outage and pins the distinction the audit spool acts on: RowRefused names
// only the refusal of the row itself, IsUnavailable names only the outage,
// and MaybeWritten stays unsure for a termination answered after the send.
// Each driver first writes a good row through the same connection, the
// positive control that the harness reaches the database.
func TestWriteErrorsOnBothDrivers(t *testing.T) {
	t.Parallel()
	for driver, shapes := range map[string]func(*testing.T) []writeShape{
		"sqlite": sqliteWriteShapes, "postgres": postgresWriteShapes,
	} {
		t.Run(driver, func(t *testing.T) {
			t.Parallel()
			for _, s := range shapes(t) {
				if s.err == nil {
					t.Fatalf("%s: the driver returned no error", s.name)
				}
				if got := RowRefused(s.err); got != s.refused {
					t.Errorf("%s: RowRefused(%v) = %v, want %v", s.name, s.err, got, s.refused)
				}
				if got := IsUnavailable(s.err); got != s.outage {
					t.Errorf("%s: IsUnavailable(%v) = %v, want %v", s.name, s.err, got, s.outage)
				}
				if got := MaybeWritten(s.err); got != s.maybe {
					t.Errorf("%s: MaybeWritten(%v) = %v, want %v", s.name, s.err, got, s.maybe)
				}
			}
		})
	}
}

// sqliteWriteShapes returns a NOT NULL refusal from a working SQLite file and
// the cannot-open fault of a file under a missing directory.
func sqliteWriteShapes(t *testing.T) []writeShape {
	ctx := context.Background()
	db, err := sql.Open("sqlite", "file:"+filepath.Join(t.TempDir(), "ok.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.ExecContext(ctx, `CREATE TABLE t (v TEXT NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO t VALUES ('control')`); err != nil {
		t.Fatalf("positive control: a good row did not insert: %v", err)
	}
	_, refused := db.ExecContext(ctx, `INSERT INTO t VALUES (NULL)`)
	down, err := sql.Open("sqlite", "file:"+filepath.Join(t.TempDir(), "missing-dir", "x.db")+"?mode=rw")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = down.Close() })
	_, outage := down.ExecContext(ctx, `INSERT INTO t VALUES ('x')`)
	return []writeShape{
		{"not null refused", refused, true, false, false},
		{"database file cannot be opened", outage, false, true, false},
	}
}

// postgresWriteShapes returns, from the test Postgres, a NOT NULL refusal, an
// invalid byte refused as data, a connect refused on a closed port, and the
// session terminated while its statement runs. It skips without
// STRAZA_TEST_POSTGRES_DSN.
func postgresWriteShapes(t *testing.T) []writeShape {
	dsn := os.Getenv("STRAZA_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("STRAZA_TEST_POSTGRES_DSN not set")
	}
	ctx := context.Background()
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	conn, err := db.Conn(ctx) // the temp table lives on this one connection
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	if _, err := conn.ExecContext(ctx, `CREATE TEMP TABLE t (v TEXT NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.ExecContext(ctx, `INSERT INTO t VALUES ('control')`); err != nil {
		t.Fatalf("positive control: a good row did not insert: %v", err)
	}
	_, notNull := conn.ExecContext(ctx, `INSERT INTO t VALUES (NULL)`)
	_, badByte := conn.ExecContext(ctx, `INSERT INTO t VALUES ($1)`, "a\x00b")
	badByte = (&sqlStore{}).mapErr(badByte)
	down, err := openPostgres("postgres://straza@127.0.0.1:1/straza?connect_timeout=1")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = down.Close() })
	_, connRefused := down.Outbox().Insert(ctx, OutboxEvent{Subject: "straza.audit.tool", CE: `{}`})
	victim, err := db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = victim.Close() })
	_, terminated := victim.ExecContext(ctx, `SELECT pg_terminate_backend(pg_backend_pid())`)
	return []writeShape{
		{"not null refused", notNull, true, false, false},
		{"invalid byte refused as data", badByte, true, false, false},
		{"connect refused", connRefused, false, true, false},
		{"session terminated mid-statement", terminated, false, true, true},
	}
}
