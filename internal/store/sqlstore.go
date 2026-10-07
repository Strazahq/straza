package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/golang-migrate/migrate/v4"
	"github.com/golang-migrate/migrate/v4/database"
	"github.com/golang-migrate/migrate/v4/source/iofs"
	"github.com/google/uuid"
)

type dialect int

const (
	dialectSQLite dialect = iota
	dialectPostgres
)

// sqlStore implements Store for both engines over database/sql. Only opening,
// migrations, and a few type encodings differ per dialect.
type sqlStore struct {
	db            *sql.DB
	d             dialect
	migrations    fs.FS  // the embedded migrations, or a test's own steps
	migrationsDir string // subdir of migrations
	migrateDriver func(*sql.DB) (database.Driver, error)
	migrateName   string
}

var placeholderRe = regexp.MustCompile(`\$(\d+)`)

// q prepares a query for the dialect. The corpus is written with $1..$n used
// exactly once each, in ascending order (validated here), so the sqlite
// rewrite to ? is semantics-preserving.
func (s *sqlStore) q(query string) string {
	matches := placeholderRe.FindAllStringSubmatch(query, -1)
	for i, m := range matches {
		n, err := strconv.Atoi(m[1])
		if err != nil || n != i+1 {
			panic(fmt.Sprintf("store: query placeholders must be $1..$n in order, got %s in %q", m[0], query))
		}
	}
	if s.d == dialectSQLite {
		return placeholderRe.ReplaceAllString(query, "?")
	}
	return query
}

func (s *sqlStore) exec(ctx context.Context, query string, args ...any) (sql.Result, error) {
	res, err := s.db.ExecContext(ctx, s.q(query), args...)
	return res, s.mapErr(err)
}

func (s *sqlStore) query(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	rows, err := s.db.QueryContext(ctx, s.q(query), args...)
	return rows, s.mapErr(err)
}

func (s *sqlStore) queryRow(ctx context.Context, query string, args ...any) *sql.Row {
	return s.db.QueryRowContext(ctx, s.q(query), args...)
}

// mapErr converts driver-specific failures to sentinel errors.
func (s *sqlStore) mapErr(err error) error {
	if err == nil {
		return nil
	}
	msg := err.Error()
	// modernc.org/sqlite: "constraint failed: UNIQUE constraint failed: ..."
	// pgx: SQLSTATE 23505 (unique_violation) appears in the message text.
	if strings.Contains(msg, "UNIQUE constraint failed") ||
		strings.Contains(msg, "SQLSTATE 23505") {
		return fmt.Errorf("%w: %s", ErrConflict, msg)
	}
	// pgx: SQLSTATE class 22 (data exception, e.g. 22021 invalid byte
	// sequence). SQLite stores the same bytes silently, so it never maps.
	if strings.Contains(msg, "SQLSTATE 22") {
		return fmt.Errorf("%w: %s", ErrInvalidData, msg)
	}
	return err
}

// scanErr normalizes row-scan errors.
func scanErr(err error) error {
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	return err
}

// mustAffect converts a 0-rows-affected write into ErrNotFound.
func mustAffect(res sql.Result, err error) error {
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err == nil && n == 0 {
		return ErrNotFound
	}
	return nil
}

// newID returns a UUIDv7 (time-ordered PKs).
func newID() string {
	id, err := uuid.NewV7()
	if err != nil {
		// NewV7 fails only if crypto/rand does; treat as unrecoverable.
		panic(fmt.Sprintf("store: uuidv7: %v", err))
	}
	return id.String()
}

// now returns the canonical write timestamp: UTC, microsecond precision (the
// common denominator between TIMESTAMPTZ and RFC3339 text round-trips).
func now() time.Time {
	return time.Now().UTC().Truncate(time.Microsecond)
}

// tArg encodes a time for the dialect: RFC3339Nano text in sqlite, native
// TIMESTAMPTZ in postgres. The sqlite text drops trailing zeros, so it is not
// fixed width and does not sort as time: "10:00:00Z" sorts after
// "10:00:00.5Z". Only the approval use window compares through timeAfter so
// far, and the store's other time predicates still compare the text.
func (s *sqlStore) tArg(t time.Time) any {
	if s.d == dialectSQLite {
		return t.UTC().Format(time.RFC3339Nano)
	}
	return t.UTC()
}

// timeAfter returns the SQL predicate "left > right" with both sides compared
// as points in time. On sqlite both sides go through julianday(), which reads
// the stored text in any width and rounds it to the nearest millisecond.
// Rounding never reverses the order of two times, so a left at or before
// right never passes, and a left that rounds to right's millisecond fails
// closed. A NULL or unparsable side yields NULL, which matches no row.
// Postgres compares TIMESTAMPTZ natively, to the microsecond.
func (s *sqlStore) timeAfter(left, right string) string {
	if s.d == dialectSQLite {
		return "julianday(" + left + ") > julianday(" + right + ")"
	}
	return left + " > " + right
}

func (s *sqlStore) tArgPtr(t *time.Time) any {
	if t == nil {
		return nil
	}
	return s.tArg(*t)
}

// scanTime scans a non-null timestamp from either engine.
type scanTime struct{ t time.Time }

// Scan implements sql.Scanner accepting time.Time (pgx) or text (sqlite).
func (st *scanTime) Scan(v any) error {
	switch x := v.(type) {
	case time.Time:
		st.t = x.UTC()
		return nil
	case string:
		t, err := time.Parse(time.RFC3339Nano, x)
		if err != nil {
			return fmt.Errorf("store: parse time %q: %w", x, err)
		}
		st.t = t.UTC()
		return nil
	case []byte:
		return st.Scan(string(x))
	default:
		return fmt.Errorf("store: cannot scan %T into time", v)
	}
}

// scanTimePtr scans a nullable timestamp.
type scanTimePtr struct{ t *time.Time }

// Scan implements sql.Scanner.
func (st *scanTimePtr) Scan(v any) error {
	if v == nil {
		st.t = nil
		return nil
	}
	var inner scanTime
	if err := inner.Scan(v); err != nil {
		return err
	}
	st.t = &inner.t
	return nil
}

func (s *sqlStore) Ping(ctx context.Context) error {
	return s.db.PingContext(ctx)
}

func (s *sqlStore) Close() error {
	return s.db.Close()
}

func (s *sqlStore) migrator() (*migrate.Migrate, error) {
	src, err := iofs.New(s.migrations, s.migrationsDir)
	if err != nil {
		return nil, fmt.Errorf("store: load embedded migrations: %w", err)
	}
	drv, err := s.migrateDriver(s.db)
	if err != nil {
		return nil, fmt.Errorf("store: init migration driver: %w", err)
	}
	m, err := migrate.NewWithInstance("iofs", src, s.migrateName, drv)
	if err != nil {
		return nil, fmt.Errorf("store: init migrator: %w", err)
	}
	return m, nil
}

// Migrate applies all pending embedded migrations. It refuses a database
// older than the first embedded migration, which no step here upgrades.
func (s *sqlStore) Migrate(_ context.Context) error {
	first, _, err := s.migrationRange()
	if err != nil {
		return err
	}
	m, err := s.migrator()
	if err != nil {
		return err
	}
	if cur, _, err := m.Version(); err == nil && cur < first {
		return belowFirstRefusal(cur, first)
	}
	return s.withMigrationFKs(func() error {
		if err := m.Up(); err != nil && !errors.Is(err, migrate.ErrNoChange) {
			return fmt.Errorf("store: migrate up: %w", err)
		}
		return nil
	})
}

// migrateDown reverts all migrations. Test-harness only; deliberately not on
// the Store interface.
func (s *sqlStore) migrateDown() error {
	m, err := s.migrator()
	if err != nil {
		return err
	}
	return s.withMigrationFKs(func() error {
		if err := m.Down(); err != nil && !errors.Is(err, migrate.ErrNoChange) {
			return fmt.Errorf("store: migrate down: %w", err)
		}
		return nil
	})
}

// withMigrationFKs runs a migration batch the way the sqlite manual
// prescribes for schema changes that rebuild a table (lang_altertable.html,
// "making other kinds of table schema changes": foreign_keys OFF, rebuild
// inside a transaction, foreign_key_check, foreign_keys ON). Why it lives
// here and not in the migration files: the pragma is a no-op inside a
// transaction and golang-migrate wraps every sqlite migration in one, while
// with enforcement ON a DROP TABLE of a parent such as roles performs an
// implicit DELETE that cascades through every child. The sqlite pool holds
// exactly one connection (SetMaxOpenConns(1)), so the pragma lands on the
// connection the migrator uses; the read-back below refuses to run if it did
// not. Enforcement is restored whatever the batch did, and a batch that
// leaves a dangling reference fails loudly here (fail closed) instead of
// surfacing later as a silent-miss read. Postgres constraints are
// transactional and need none of this.
func (s *sqlStore) withMigrationFKs(fn func() error) error {
	if s.d != dialectSQLite {
		return fn()
	}
	if _, err := s.db.Exec("PRAGMA foreign_keys = OFF"); err != nil {
		return fmt.Errorf("store: migrate: foreign_keys off: %w", err)
	}
	var on int
	if err := s.db.QueryRow("PRAGMA foreign_keys").Scan(&on); err != nil || on != 0 {
		return fmt.Errorf("store: migrate: foreign-key enforcement still on (%d, %v); refusing to rebuild under cascade", on, err)
	}
	runErr := fn()
	if _, err := s.db.Exec("PRAGMA foreign_keys = ON"); err != nil {
		return errors.Join(runErr, fmt.Errorf("store: migrate: foreign_keys on: %w", err))
	}
	if runErr != nil {
		return runErr
	}
	rows, err := s.db.Query("PRAGMA foreign_key_check")
	if err != nil {
		return fmt.Errorf("store: migrate: foreign_key_check: %w", err)
	}
	defer func() { _ = rows.Close() }()
	if rows.Next() {
		var table, rowid, parent, fkid any
		_ = rows.Scan(&table, &rowid, &parent, &fkid)
		return fmt.Errorf("store: migrate: referential integrity broken after migration (table %v row %v -> %v); refusing to serve", table, rowid, parent)
	}
	return rows.Err()
}

// Repo accessors: every repo is a view over the same sqlStore.

func (s *sqlStore) Users() UserRepo                 { return userRepo{s} }
func (s *sqlStore) Roles() RoleRepo                 { return roleRepo{s} }
func (s *sqlStore) Devices() DeviceRepo             { return deviceRepo{s} }
func (s *sqlStore) Sessions() SessionRepo           { return sessionRepo{s} }
func (s *sqlStore) Packs() PackRepo                 { return packRepo{s} }
func (s *sqlStore) Apps() AppRepo                   { return appRepo{s} }
func (s *sqlStore) ToolBindings() ToolBindingRepo   { return toolBindingRepo{s} }
func (s *sqlStore) Credentials() CredentialRepo     { return credentialRepo{s} }
func (s *sqlStore) Policies() PolicyRepo            { return policyRepo{s} }
func (s *sqlStore) Snapshots() SnapshotRepo         { return snapshotRepo{s} }
func (s *sqlStore) Outbox() OutboxRepo              { return outboxRepo{s} }
func (s *sqlStore) Audit() AuditRepo                { return auditRepo{s} }
func (s *sqlStore) Conversations() ConversationRepo { return conversationRepo{s} }
func (s *sqlStore) Approvals() ApprovalRepo         { return approvalRepo{s} }
func (s *sqlStore) Approvers() ApproverRepo         { return approverRepo{s} }
func (s *sqlStore) Drafts() DraftRepo               { return draftRepo{s} }
func (s *sqlStore) Revocations() RevocationRepo     { return revocationRepo{s} }
func (s *sqlStore) SigningKeys() SigningKeyRepo     { return signingKeyRepo{s} }
func (s *sqlStore) Settings() SettingsRepo          { return settingsRepo{s} }

func (s *sqlStore) AttestationHashes() AttestationRepo { return attestationRepo{s} }
