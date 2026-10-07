package store

import (
	"database/sql"
	"fmt"
	"runtime"
	"time"

	"github.com/golang-migrate/migrate/v4/database"
	migratepgx "github.com/golang-migrate/migrate/v4/database/pgx/v5"
	_ "github.com/jackc/pgx/v5/stdlib" // database/sql driver "pgx" (pure Go)
)

// openPostgres connects to the enterprise-profile store.
func openPostgres(dsn string) (Store, error) {
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		return nil, fmt.Errorf("store: open postgres: %w", err)
	}
	// Pool bounds: database/sql defaults are unlimited open
	// connections and 2 idle. Unlimited means a latency spike balloons into
	// Postgres max_connections exhaustion and 500s across the whole control
	// plane, including /v1/checkin, which fails closed; 2 idle means
	// constant dial/TLS churn at any concurrency. Bounded and recycled, a
	// burst queues inside the pool instead of taking the server down.
	maxConns := 4 * runtime.GOMAXPROCS(0)
	if maxConns < 16 {
		maxConns = 16
	}
	db.SetMaxOpenConns(maxConns)
	db.SetMaxIdleConns(maxConns)
	db.SetConnMaxLifetime(30 * time.Minute)
	db.SetConnMaxIdleTime(5 * time.Minute)
	return &sqlStore{
		db:            db,
		d:             dialectPostgres,
		migrations:    migrationsFS,
		migrationsDir: "migrations/postgres",
		migrateName:   "pgx",
		migrateDriver: func(db *sql.DB) (database.Driver, error) {
			return migratepgx.WithInstance(db, &migratepgx.Config{})
		},
	}, nil
}
