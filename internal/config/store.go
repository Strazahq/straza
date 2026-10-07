package config

// Store and Log sections: persistence driver/DSN and structured logging.
// Types, validation. The env faces stay in config.go's applyEnv.

import (
	"fmt"
	"slices"
)

// Log configures structured logging.
type Log struct {
	Level  string `yaml:"level"`  // debug|info|warn|error
	Format string `yaml:"format"` // json|text
}

// Store configures persistence.
type Store struct {
	Driver string `yaml:"driver"` // sqlite|postgres
	// DSN is the postgres connection string, or the sqlite file path.
	// Empty with driver=sqlite derives "<dataDir>/straza.db".
	DSN string `yaml:"dsn"`
}

// validate holds the log section's checks.
func (l Log) validate() error {
	if !slices.Contains([]string{"debug", "info", "warn", "error"}, l.Level) {
		return fmt.Errorf("unknown log level %q: expected debug|info|warn|error", l.Level)
	}
	if l.Format != "json" && l.Format != "text" {
		return fmt.Errorf("unknown log format %q: expected json|text", l.Format)
	}
	return nil
}

// validateDriver is the store check Validate runs early (before the scim,
// governance and capture checks); validateDSN below is the one it runs
// after them. Two methods so the first-error order holds.
func (s Store) validateDriver() error {
	if s.Driver != DriverSQLite && s.Driver != DriverPostgres {
		return fmt.Errorf("unknown store driver %q: expected %q or %q", s.Driver, DriverSQLite, DriverPostgres)
	}
	return nil
}

// validateDSN: see validateDriver for why this is a separate method.
func (s Store) validateDSN() error {
	if s.Driver == DriverPostgres && s.DSN == "" {
		return fmt.Errorf("store driver %q requires store.dsn (postgres connection string)", DriverPostgres)
	}
	return nil
}

// applyEnvLog binds the log section's env faces; applyEnv
// (config.go) sequences the binders, and each face writes a field no other
// face writes.
func applyEnvLog(cfg *Config, getenv func(string) string) {
	set := envSet(getenv)
	set("STRAZA_LOG_LEVEL", func(v string) { cfg.Log.Level = v })
	set("STRAZA_LOG_FORMAT", func(v string) { cfg.Log.Format = v })
}

// applyEnvStore binds the store section's env faces; applyEnv
// (config.go) sequences the binders, and each face writes a field no other
// face writes.
func applyEnvStore(cfg *Config, getenv func(string) string) {
	set := envSet(getenv)
	set("STRAZA_STORE_DRIVER", func(v string) { cfg.Store.Driver = v })
	set("STRAZA_STORE_DSN", func(v string) { cfg.Store.DSN = v })
}
