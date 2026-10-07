package store

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"io"
	"net"
	"path/filepath"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
)

// TestIsUnavailable pins the outage classifier: driver and transport shapes
// that mean "the store is not there right now" answer true, data-level
// outcomes (not found, conflict, constraint, syntax) answer false, and an
// unknown error answers false (the caller's safe default is "not an outage").
func TestIsUnavailable(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"not found", ErrNotFound, false},
		{"conflict", fmt.Errorf("%w: dup", ErrConflict), false},
		{"opaque", errors.New("boom"), false},
		{"deadline", context.DeadlineExceeded, true},
		{"deadline wrapped", fmt.Errorf("store: users get: %w", context.DeadlineExceeded), true},
		{"bad conn", driver.ErrBadConn, true},
		{"conn done", sql.ErrConnDone, true},
		{"unexpected eof", io.ErrUnexpectedEOF, true},
		{"net dial refused", &net.OpError{Op: "dial", Net: "tcp", Err: errors.New("connection refused")}, true},
		{"net wrapped by pgconn", &pgconn.ConnectError{}, true},
		{"pg cannot connect now 57P03", &pgconn.PgError{Code: "57P03", Message: "the database system is starting up"}, true},
		{"pg admin shutdown 57P01", &pgconn.PgError{Code: "57P01"}, true},
		{"pg connection exception 08006", &pgconn.PgError{Code: "08006"}, true},
		{"pg too many connections 53300", &pgconn.PgError{Code: "53300"}, true},
		{"pg disk full 53100", &pgconn.PgError{Code: "53100"}, true},
		{"pg io error 58030", &pgconn.PgError{Code: "58030"}, true},
		{"pg server auth misconfigured 28P01", &pgconn.PgError{Code: "28P01"}, true},
		{"pg database missing 3D000", &pgconn.PgError{Code: "3D000"}, true},
		{"pg unique violation 23505", &pgconn.PgError{Code: "23505"}, false},
		{"pg syntax error 42601", &pgconn.PgError{Code: "42601"}, false},
		{"pg wrapped 57P03", fmt.Errorf("authn: link account: %w", &pgconn.PgError{Code: "57P03"}), true},
		{"pg short code", &pgconn.PgError{Code: "5"}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsUnavailable(tc.err); got != tc.want {
				t.Fatalf("IsUnavailable(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}

// TestIsUnavailableSQLiteLive drives the real pure-Go sqlite driver into a
// cannot-open fault (a database file under a directory that does not exist)
// and requires the classifier to call it an outage; the modernc error type has
// no public constructor, so the live driver is the only honest fixture.
func TestIsUnavailableSQLiteLive(t *testing.T) {
	dsn := "file:" + filepath.Join(t.TempDir(), "missing-dir", "straza.db") + "?mode=rw"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	err = db.PingContext(context.Background())
	if err == nil {
		t.Fatal("expected the ping to fail on a missing directory")
	}
	if !IsUnavailable(err) {
		t.Fatalf("sqlite cannot-open %q not classified as an outage", err)
	}
	// Control: a data-level failure on a healthy database is not an outage.
	ok, err := sql.Open("sqlite", "file:"+filepath.Join(t.TempDir(), "ok.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ok.Close() }()
	_, err = ok.ExecContext(context.Background(), "SELECT * FROM no_such_table")
	if err == nil {
		t.Fatal("expected a query on a missing table to fail")
	}
	if IsUnavailable(err) {
		t.Fatalf("missing-table error %q wrongly classified as an outage", err)
	}
}
