// Package storetest holds fixtures for tests that need a Straza store: a
// SQLite database already migrated to the current schema, and password
// hashes cheap enough to compute under the race detector. Under -race every
// migration runs instrumented inside the transpiled SQLite and every bcrypt
// round at the production cost is instrumented too, which made a fresh store
// and a seeded user cost seconds per test.
package storetest

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"golang.org/x/crypto/bcrypt"

	"github.com/strazahq/straza/internal/config"
	"github.com/strazahq/straza/internal/store"
)

var (
	templateOnce  sync.Once
	templateBytes []byte
	templateErr   error
)

// SeedSQLite writes a fully migrated SQLite database to path, so a store
// opened there finds the schema current and its Migrate has nothing to
// apply. It does nothing when a file is already at path, which keeps a test
// that reopens a database on its own file. The template is migrated once per
// test binary through the product's own store.Open and Migrate, so a test
// that must prove migrating from an empty file opens its store without this.
func SeedSQLite(tb testing.TB, path string) {
	tb.Helper()
	if _, err := os.Stat(path); !errors.Is(err, fs.ErrNotExist) {
		return
	}
	templateOnce.Do(func() { templateBytes, templateErr = buildTemplate() })
	if templateErr != nil {
		tb.Fatalf("storetest: migrated template: %v", templateErr)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		tb.Fatal(err)
	}
	if err := os.WriteFile(path, templateBytes, 0o600); err != nil {
		tb.Fatal(err)
	}
}

// buildTemplate migrates a fresh SQLite file and returns its bytes. Close
// checkpoints the write-ahead log into the main file; a log left behind would
// mean a partial copy, so it refuses one.
func buildTemplate() ([]byte, error) {
	dir, err := os.MkdirTemp("", "straza-store-template-")
	if err != nil {
		return nil, err
	}
	defer func() { _ = os.RemoveAll(dir) }()
	path := filepath.Join(dir, "straza.db")
	st, err := store.Open(config.Config{Store: config.Store{Driver: config.DriverSQLite, DSN: path}})
	if err != nil {
		return nil, err
	}
	if err := st.Migrate(context.Background()); err != nil {
		_ = st.Close()
		return nil, err
	}
	if err := st.Close(); err != nil {
		return nil, err
	}
	if fi, err := os.Stat(path + "-wal"); err == nil && fi.Size() > 0 {
		return nil, fmt.Errorf("write-ahead log of %d bytes left after close", fi.Size())
	}
	return os.ReadFile(path) // #nosec G304 -- the file this function created in its own temp directory
}

// PasswordHash hashes plain for a seeded test user at the lowest cost bcrypt
// allows, with the signature of authn.HashPassword. Login reads the cost from
// the stored hash, so the user signs in through the same path as with a
// production hash.
func PasswordHash(plain string) (string, error) {
	b, err := bcrypt.GenerateFromPassword([]byte(plain), bcrypt.MinCost)
	return string(b), err
}
