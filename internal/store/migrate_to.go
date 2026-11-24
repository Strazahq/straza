package store

import (
	"context"
	"errors"
	"fmt"
	"io/fs"

	"github.com/golang-migrate/migrate/v4"
	"github.com/golang-migrate/migrate/v4/source/iofs"
)

// ErrSchemaUnchanged reports that MigrateTo found the database at the
// version it was asked for, so it moved nothing.
var ErrSchemaUnchanged = errors.New("store: the database is at that migration already")

// MigrateTo moves the schema up or down to version with the embedded
// migrations and answers the version it ends at. It moves up any distance
// and down one migration per call, and refuses a version further down
// before anything runs, because each down step can drop tables and the
// rows they hold. It refuses a version above the newest embedded migration,
// and a database golang-migrate marked dirty, naming the version and the
// fix. It also refuses a version below the first migration, a database
// with no migration applied, and a database older than the first
// migration or ahead of the newest. A database already at version answers
// version and ErrSchemaUnchanged.
func (s *sqlStore) MigrateTo(_ context.Context, version uint) (uint, error) {
	first, head, err := s.migrationRange()
	if err != nil {
		return 0, err
	}
	switch {
	case version > head:
		return 0, fmt.Errorf("this strazad knows migrations up to %d, so it cannot move the database to %d. "+
			"Run the command with a strazad release that has migration %d", head, version, version)
	case version < first && first == head:
		return 0, fmt.Errorf("this strazad has no migration %d. Its only migration is %d, so there is no older version to move the database to", version, head)
	case version < first:
		return 0, fmt.Errorf("this strazad has no migration %d. Its migrations run from %d to %d, so name one of those", version, first, head)
	}
	m, err := s.migrator()
	if err != nil {
		return 0, err
	}
	cur, dirty, err := m.Version()
	switch {
	case errors.Is(err, migrate.ErrNilVersion):
		// A path or a DSN that names another database opens as an empty
		// one, and building a schema there would leave the real one as it
		// was while the command reported success.
		return 0, errors.New("the database has no Straza migration applied, so there is nothing to move. " +
			"Give the command the same --config, --profile, --data-dir and --store-dsn that strazad serve runs with")
	case err != nil:
		return 0, fmt.Errorf("store: read the migration version: %w", err)
	case dirty:
		return cur, dirtyRefusal(cur)
	case cur < first:
		return cur, belowFirstRefusal(cur, first)
	case cur > head:
		return cur, fmt.Errorf("the database is at migration %d, and this strazad knows migrations up to %d, so it cannot move it. "+
			"Run the command with a strazad release that has migration %d", cur, head, cur)
	case cur == version:
		return cur, ErrSchemaUnchanged
	case version+1 < cur:
		return cur, fmt.Errorf("the database is at migration %d, and the command moves it down one migration per run, "+
			"because each down step can drop tables and the rows they hold. Next run strazad migrate --to %d, with the other flags as they were",
			cur, cur-1)
	}
	if err := s.withMigrationFKs(func() error { return m.Migrate(version) }); err != nil {
		if at, dirty, verr := m.Version(); verr == nil && dirty {
			return at, fmt.Errorf("the move to migration %d failed at migration %d, and the database is marked dirty there: %w. "+dirtyFix,
				version, at, err)
		}
		return cur, fmt.Errorf("store: move the database from migration %d to %d: %w", cur, version, err)
	}
	return version, nil
}

// SchemaCurrent answers nil when the database is at the newest migration
// this binary embeds. Otherwise it answers the sentence that says where the
// database is and what to run first, for a database whose version it
// cannot read, with no migration applied, marked dirty, older than the
// first migration, behind the newest or ahead of it. It moves nothing, so
// a command that writes rows can refuse a schema its queries were not
// written for.
func (s *sqlStore) SchemaCurrent(_ context.Context) error {
	first, head, err := s.migrationRange()
	if err != nil {
		return err
	}
	const unread = "the command could not read the migration version of the database (%w), so nothing changed. " +
		"Check that the database is reachable and that its user may read it, then run the command again"
	m, err := s.migrator()
	if err != nil {
		return fmt.Errorf(unread, err)
	}
	cur, dirty, err := m.Version()
	switch {
	case errors.Is(err, migrate.ErrNilVersion):
		return errors.New("the database has no Straza migration applied, because strazad serve has not booted on it yet, so nothing changed. " +
			"If this is the right database, start strazad serve once with these settings, then run the command again. " +
			"If it is not, give the command the same --config, --profile, --data-dir and --store-dsn that strazad serve runs with")
	case err != nil:
		return fmt.Errorf(unread, err)
	case dirty:
		return dirtyRefusal(cur)
	case cur < first:
		return fmt.Errorf("the database is at migration %d, and this strazad's migrations start at %d, so nothing changed. "+
			"Upgrade the database with an earlier strazad release whose migrations reach %d, then run strazad migrate --to %d with this release and run the command again",
			cur, first, first, head)
	case cur > head:
		return fmt.Errorf("the database is at migration %d, and this strazad knows migrations up to %d, so nothing changed. "+
			"Run the command with a strazad release that has migration %d", cur, head, cur)
	case cur < head:
		return fmt.Errorf("the database is at migration %d, and this strazad expects migration %d, so nothing changed. "+
			"Run strazad migrate --to %d with the same flags first, then run the command again", cur, head, head)
	}
	return nil
}

// dirtyFix is what an operator does about a database marked dirty.
const dirtyFix = "Restore the backup taken before the upgrade, or repair the schema by hand, then run the command again"

// dirtyRefusal words a database golang-migrate marked dirty at version.
func dirtyRefusal(version uint) error {
	return fmt.Errorf("the database is marked dirty at migration %d, because a migration failed part way. "+dirtyFix, version)
}

// belowFirstRefusal words a database older than the first embedded
// migration, which only an earlier release can upgrade.
func belowFirstRefusal(cur, first uint) error {
	return fmt.Errorf("the database is at migration %d, and this strazad's migrations start at %d, so it cannot upgrade it. "+
		"Upgrade the database with an earlier strazad release whose migrations reach %d, then start this one", cur, first, first)
}

// migrationRange answers the first and the newest migration this binary
// embeds for the store's driver.
func (s *sqlStore) migrationRange() (first, head uint, err error) {
	src, err := iofs.New(s.migrations, s.migrationsDir)
	if err != nil {
		return 0, 0, fmt.Errorf("store: load embedded migrations: %w", err)
	}
	defer func() { _ = src.Close() }()
	if first, err = src.First(); err != nil {
		return 0, 0, fmt.Errorf("store: read the first embedded migration: %w", err)
	}
	for head = first; ; {
		next, err := src.Next(head)
		if errors.Is(err, fs.ErrNotExist) {
			return first, head, nil
		}
		if err != nil {
			return 0, 0, fmt.Errorf("store: read the embedded migrations: %w", err)
		}
		head = next
	}
}
