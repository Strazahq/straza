package server

import (
	"testing"

	"github.com/strazahq/straza/internal/config"
	"github.com/strazahq/straza/internal/store/storetest"
)

// seedStoreTemplate seeds the SQLite database cfg names with the migrated
// template, so the boot's Migrate finds the schema current and has nothing
// to apply. Postgres configs are left alone, and so is a file already there.
// The tests that call New directly still migrate from an empty file, so the
// boot-from-empty path stays covered here.
func seedStoreTemplate(t *testing.T, cfg config.Config) {
	t.Helper()
	if cfg.Store.Driver == config.DriverSQLite {
		storetest.SeedSQLite(t, cfg.SQLitePath())
	}
}

// testPasswordHash hashes plain for a seeded test user at the lowest bcrypt
// cost, with the signature of authn.HashPassword.
func testPasswordHash(plain string) (string, error) {
	return storetest.PasswordHash(plain)
}
