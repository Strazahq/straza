package store

import (
	"database/sql"
	"embed"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"path/filepath"
	"runtime"

	"github.com/golang-migrate/migrate/v4/database"
	migratesqlite "github.com/golang-migrate/migrate/v4/database/sqlite"
	_ "modernc.org/sqlite" // pure-Go sqlite driver (CGO stays off)
)

//go:embed migrations/sqlite/*.sql migrations/postgres/*.sql
var migrationsFS embed.FS

// openSQLite opens (creating if needed) the sqlite database at path.
func openSQLite(path string) (Store, error) {
	if dir := filepath.Dir(path); dir != "." {
		if err := os.MkdirAll(dir, 0o750); err != nil {
			return nil, fmt.Errorf("store: create data dir: %w", err)
		}
	}
	// sqlite gives the WAL and SHM files it creates the mode of the main
	// file, so creating that one 0600 keeps all three owner-only.
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600) // #nosec G304 -- operator-configured database path
	if err != nil {
		return nil, fmt.Errorf("store: create sqlite %s: %w", path, err)
	}
	if err := f.Close(); err != nil {
		return nil, fmt.Errorf("store: create sqlite %s: %w", path, err)
	}
	// WAL + busy_timeout make concurrent control-plane access sane; foreign
	// keys are off by default in sqlite and the schema relies on them.
	dsn := "file:" + url.PathEscape(filepath.ToSlash(path)) +
		"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("store: open sqlite %s: %w", path, err)
	}
	// modernc sqlite serializes writes; a single connection avoids
	// SQLITE_BUSY surprises under the race detector and keeps WAL simple.
	db.SetMaxOpenConns(1)
	return &sqlStore{
		db:            db,
		d:             dialectSQLite,
		migrations:    migrationsFS,
		migrationsDir: "migrations/sqlite",
		migrateName:   "sqlite",
		migrateDriver: func(db *sql.DB) (database.Driver, error) {
			return migratesqlite.WithInstance(db, &migratesqlite.Config{})
		},
	}, nil
}

// OwnerOnly narrows the sqlite database at path, and the WAL and SHM files
// beside it, to 0600 when one grants its group or others any access, as a
// database created before strazad created it 0600 can. The file holds audit
// records, password hashes and server settings. Each change is logged with
// the file and its old mode. A missing file is skipped, and Windows keeps
// its ACLs, so there it does nothing.
func OwnerOnly(path string, log *slog.Logger) error {
	if runtime.GOOS == "windows" {
		return nil
	}
	for _, p := range []string{path, path + "-wal", path + "-shm"} {
		fi, err := os.Stat(p)
		if err != nil || fi.Mode().Perm()&0o077 == 0 {
			continue
		}
		if err := os.Chmod(p, 0o600); err != nil {
			return fmt.Errorf("store: narrow %s to owner only: %w", p, err)
		}
		log.Info("store: narrowed a database file to owner only", "path", p, "was", fi.Mode().Perm().String())
	}
	return nil
}
