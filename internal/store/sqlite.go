package store

import (
	"database/sql"
	"embed"
	"fmt"
	"net/url"
	"os"
	"path/filepath"

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
