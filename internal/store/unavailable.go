package store

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"io"
	"net"

	"github.com/jackc/pgx/v5/pgconn"
	sqlite "modernc.org/sqlite"
)

// IsUnavailable reports whether err means the store (or the network in front
// of it) is not there right now, as opposed to a data-level outcome such as
// not-found, a constraint, or a bad query. The login lanes use it to answer
// an outage as 503 "temporarily unavailable" instead of blaming the person's
// credential.
//
// The list is POSITIVE: an unrecognised error answers false, so a caller's
// safe default ("not an outage, keep today's refusal") holds for shapes this
// function has never seen. Driver knowledge lives here because this package
// is the only one that knows which drivers sit behind the interface.
func IsUnavailable(err error) bool {
	if err == nil || errors.Is(err, ErrNotFound) || errors.Is(err, ErrConflict) {
		return false
	}
	// Transport and pool shapes common to both drivers.
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, driver.ErrBadConn) ||
		errors.Is(err, sql.ErrConnDone) || errors.Is(err, io.ErrUnexpectedEOF) {
		return true
	}
	var netErr net.Error
	if errors.As(err, &netErr) {
		return true
	}
	// pgx: a failed connect (dial, TLS, startup) or a server-side state that
	// means "not now": class 08 connection exception, 28 the SERVER's own
	// database credentials refused (misconfiguration, still not the person's
	// fault), 3D database missing, 53 insufficient resources (disk, too many
	// connections), 57 operator intervention (57P03 cannot_connect_now,
	// 57P01 admin_shutdown), 58 system error (I/O).
	var connErr *pgconn.ConnectError
	if errors.As(err, &connErr) {
		return true
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && len(pgErr.Code) >= 2 {
		switch pgErr.Code[:2] {
		case "08", "28", "3D", "53", "57", "58":
			return true
		}
	}
	// modernc sqlite: primary result codes that mean the file or the engine
	// is not usable right now (busy, locked, out of memory, I/O, corrupt,
	// full, cannot open, protocol). Extended codes carry the primary code in
	// the low byte.
	var sqErr *sqlite.Error
	if errors.As(err, &sqErr) {
		switch sqErr.Code() & 0xff {
		case 5, 6, 7, 10, 11, 13, 14, 15:
			return true
		}
	}
	return false
}
