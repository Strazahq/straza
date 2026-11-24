package server

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/strazahq/straza/internal/config"
	"github.com/strazahq/straza/internal/secrets"
	"github.com/strazahq/straza/internal/store"
)

// TestLoadKEKMakesAKeyOnlyForAnEmptyStore pins when a start may create the
// key-encryption key: only while the store holds no signing key. A store that
// holds one was sealed, or is about to be, under a key that exists somewhere,
// so a start without the file refuses, creates nothing and says what to do.
// The key replacement by 32 new random bytes is offered only where every key
// the next start would open is still a raw seed.
func TestLoadKEKMakesAKeyOnlyForAnEmptyStore(t *testing.T) {
	cases := []struct {
		name    string
		key     *store.SigningKey
		file    bool
		refused bool
	}{
		{name: "an empty store without the file", file: false},
		{name: "an empty store with the file", file: true},
		{name: "a store with a raw seed and the file", file: true,
			key: &store.SigningKey{KID: "ses", Purpose: store.KeyPurposeSession, PrivateKey: make([]byte, 32), PublicKey: []byte{1}}},
		{name: "a store with a raw seed without the file", refused: true,
			key: &store.SigningKey{KID: "ses", Purpose: store.KeyPurposeSession, PrivateKey: make([]byte, 32), PublicKey: []byte{1}}},
		{name: "a store with a snapshot key without the file", refused: true,
			key: &store.SigningKey{KID: "snap", Purpose: store.KeyPurposeSnapshot, PrivateKey: []byte("sealed"), PublicKey: []byte{1}}},
		{name: "a store with a retired client assertion key without the file", refused: true,
			key: &store.SigningKey{KID: "ca", Purpose: store.KeyPurposeClientAssertion, Status: store.KeyRetired, PrivateKey: []byte("sealed"), PublicKey: []byte{1}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			dir := t.TempDir()
			cfg := config.Config{DataDir: dir, Store: config.Store{Driver: config.DriverSQLite, DSN: filepath.Join(dir, "straza.db")}}
			seedStoreTemplate(t, cfg)
			st, err := store.Open(cfg)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = st.Close() })
			if err := st.Migrate(ctx); err != nil {
				t.Fatal(err)
			}
			if tc.key != nil {
				if _, err := st.SigningKeys().Create(ctx, *tc.key); err != nil {
					t.Fatal(err)
				}
			}
			if tc.file {
				if err := os.WriteFile(cfg.KEKFile(), make([]byte, 32), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			kek, err := loadKEK(ctx, cfg, slog.New(slog.DiscardHandler), st)
			_, statErr := os.Stat(cfg.KEKFile())
			if !tc.refused {
				if err != nil || kek == nil || statErr != nil {
					t.Fatalf("loadKEK = %v, file %v, want a key and its file", err, statErr)
				}
				return
			}
			if err == nil {
				t.Fatal("the start made a new key over a store that holds signing keys")
			}
			for _, want := range []string{cfg.KEKFile(), "is missing", "already holds signing keys", "Put back the secret.key", "start strazad again"} {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("the refusal %q does not say %q", err, want)
				}
			}
			if !os.IsNotExist(statErr) {
				t.Error("the refused start created a key file")
			}
			sealed := tc.key.Status != store.KeyRetired && len(tc.key.PrivateKey) != 32
			if offers := strings.Contains(err.Error(), "head -c 32 /dev/urandom"); offers == sealed {
				t.Errorf("the refusal offers new random bytes: %v, over a store with a sealed key: %v", offers, sealed)
			}
		})
	}
}

// TestLoadKEKRefusesAKeyThatOpensNoSealedKey pins the check that runs before
// any signing key is loaded: the key file must open every key the store
// already holds sealed, so that a start with another key seals no raw seed
// under it and then stops at a key it cannot open. A retired session or
// client assertion key, which no start opens, does not count.
func TestLoadKEKRefusesAKeyThatOpensNoSealedKey(t *testing.T) {
	ctx := context.Background()
	seal := func(kek *secrets.Builtin) []byte {
		t.Helper()
		out, err := kek.Seal([]byte("client_assertion ca\nkey"))
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	raw := store.SigningKey{KID: "ses", Purpose: store.KeyPurposeSession, PrivateKey: make([]byte, 32), PublicKey: []byte{1}}
	ca := func(status string, sealed []byte) store.SigningKey {
		return store.SigningKey{KID: "ca", Purpose: store.KeyPurposeClientAssertion, Status: status, PrivateKey: sealed, PublicKey: []byte{1}}
	}
	cases := []struct {
		name    string
		keys    func(right, other *secrets.Builtin) []store.SigningKey
		file    string // "right", "none" or "short"
		refused string
		absent  string
	}{
		{name: "a sealed key the file opens beside a raw seed", file: "right",
			keys: func(right, _ *secrets.Builtin) []store.SigningKey {
				return []store.SigningKey{raw, ca(store.KeyActive, seal(right))}
			}},
		{name: "a sealed key the file does not open beside a raw seed", file: "right",
			keys: func(_, other *secrets.Builtin) []store.SigningKey {
				return []store.SigningKey{raw, ca(store.KeyActive, seal(other))}
			},
			refused: "the client_assertion signing key ca does not open: it was sealed under another secret.key or changed in the database. strazad changed nothing and does not start"},
		{name: "a retired key sealed under a key that is gone", file: "right",
			keys: func(_, other *secrets.Builtin) []store.SigningKey {
				return []store.SigningKey{raw, ca(store.KeyRetired, seal(other))}
			}},
		{name: "no file over a store with a sealed key", file: "none",
			keys: func(right, _ *secrets.Builtin) []store.SigningKey {
				return []store.SigningKey{raw, ca(store.KeyActive, seal(right))}
			},
			refused: "Put back the secret.key that belongs to this database", absent: "head -c 32"},
		{name: "no file over a store with raw seeds only", file: "none",
			keys: func(_, _ *secrets.Builtin) []store.SigningKey { return []store.SigningKey{raw} }, refused: "head -c 32 /dev/urandom"},
		{name: "a file of the wrong size", file: "short",
			keys:    func(_, _ *secrets.Builtin) []store.SigningKey { return []store.SigningKey{raw} },
			refused: "must be exactly 32 bytes, got 5. Put a readable file of exactly 32 raw bytes there, not text such as base64, the secret.key that belongs to this database"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			cfg := config.Config{DataDir: dir, Store: config.Store{Driver: config.DriverSQLite, DSN: filepath.Join(dir, "straza.db")}}
			seedStoreTemplate(t, cfg)
			st, err := store.Open(cfg)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = st.Close() })
			if err := st.Migrate(ctx); err != nil {
				t.Fatal(err)
			}
			right, err := secrets.LoadOrCreateKEK(cfg.KEKFile())
			if err != nil {
				t.Fatal(err)
			}
			other, err := secrets.LoadOrCreateKEK(filepath.Join(t.TempDir(), "other"))
			if err != nil {
				t.Fatal(err)
			}
			for _, k := range tc.keys(right, other) {
				if _, err := st.SigningKeys().Create(ctx, k); err != nil {
					t.Fatal(err)
				}
			}
			switch tc.file {
			case "none":
				if err := os.Remove(cfg.KEKFile()); err != nil {
					t.Fatal(err)
				}
			case "short":
				if err := os.WriteFile(cfg.KEKFile(), []byte("short"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			kek, err := loadKEK(ctx, cfg, slog.New(slog.DiscardHandler), st)
			if tc.refused == "" {
				if err != nil || kek == nil {
					t.Fatalf("loadKEK = %v, want the key", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.refused) {
				t.Fatalf("err = %v, want one that says %q", err, tc.refused)
			}
			if tc.absent != "" && strings.Contains(err.Error(), tc.absent) {
				t.Fatalf("the refusal %q offers %q over a store with a sealed key", err, tc.absent)
			}
		})
	}
}
