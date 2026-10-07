package store

import (
	"database/sql/driver"
	"errors"
	"strings"

	"github.com/jackc/pgx/v5/pgconn"
	sqlite "modernc.org/sqlite"
)

// MaybeWritten reports whether a write that returned err may still have been
// stored: the statement may have reached the database and no answer came
// back, as when a timeout or a lost connection ends the wait. It is false
// when err proves that nothing was stored: nil, a duplicate key or invalid
// data, a failure before anything was sent, or an error the database
// answered with, except a connection error or a termination. An
// unrecognised error answers true, the cautious reading.
func MaybeWritten(err error) bool {
	if err == nil || errors.Is(err, ErrConflict) || errors.Is(err, ErrInvalidData) ||
		errors.Is(err, driver.ErrBadConn) || pgconn.SafeToRetry(err) {
		return false
	}
	// A failed connect sent nothing. A SQLite result code is the database's
	// own answer, and an autocommit statement that answers with an error
	// stored nothing. A context error stays unsure on both drivers: modernc
	// reports the context's error even when the statement finished before
	// the cancel reached it.
	var connErr *pgconn.ConnectError
	var sqErr *sqlite.Error
	if errors.As(err, &connErr) || errors.As(err, &sqErr) {
		return false
	}
	// A Postgres answer proves nothing was stored, except a connection error
	// (class 08) or a termination (57P01, 57P02). A pooler such as PgBouncer
	// answers 08P01 when its server connection dies after it forwarded the
	// statement, and Postgres can terminate a session after a local commit
	// while it waits for a synchronous standby. The driver cannot tell
	// whether the commit happened.
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return strings.HasPrefix(pgErr.Code, "08") || pgErr.Code == "57P01" || pgErr.Code == "57P02"
	}
	return true
}

// RowRefused reports whether err is the database's refusal of the row
// itself, so that writing the same row again fails the same way while other
// rows still succeed: a duplicate key, invalid data, a violated constraint or
// a value too large. It is false for nil, for an outage that IsUnavailable
// names, and for an error it does not recognise, so a caller that must not
// lose the row keeps trying while the database cannot take it.
func RowRefused(err error) bool {
	if errors.Is(err, ErrConflict) || errors.Is(err, ErrInvalidData) {
		return true
	}
	// Postgres: class 22 data exception, 23 integrity constraint violation
	// and 54 program limit exceeded, such as a row too large for its index.
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && len(pgErr.Code) >= 2 {
		switch pgErr.Code[:2] {
		case "22", "23", "54":
			return true
		}
	}
	// SQLite: primary result codes TOOBIG (18), CONSTRAINT (19) and
	// MISMATCH (20). Extended codes carry the primary code in the low byte.
	var sqErr *sqlite.Error
	if errors.As(err, &sqErr) {
		switch sqErr.Code() & 0xff {
		case 18, 19, 20:
			return true
		}
	}
	return false
}
