package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/strazahq/straza/internal/authn"
	"github.com/strazahq/straza/internal/config"
	"github.com/strazahq/straza/internal/secrets"
	"github.com/strazahq/straza/internal/store"
)

// TestResealSigningKeysCommand pins the command that replaces the KEK under
// the signing keys: its refusals change nothing and create no file, a run
// moves every key to the new KEK, and a second run finds nothing to do.
func TestResealSigningKeysCommand(t *testing.T) {
	path, _ := migratedStore(t)
	dir := filepath.Dir(path)
	oldFile, newFile := filepath.Join(dir, "secret.key"), filepath.Join(dir, "secret.key.new")
	oldKEK, err := secrets.LoadOrCreateKEK(oldFile)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	load := func(kek *secrets.Builtin) error {
		t.Helper()
		st, err := store.Open(config.Config{Store: config.Store{Driver: config.DriverSQLite, DSN: path}})
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = st.Close() }()
		if _, err := authn.NewTokenService(ctx, st.SigningKeys(), kek, "https://straza.local", 0); err != nil {
			return err
		}
		_, err = authn.NewSnapshotKeys(ctx, st.SigningKeys(), kek)
		return err
	}
	if err := load(oldKEK); err != nil {
		t.Fatal(err)
	}
	oldBytes, err := os.ReadFile(oldFile)
	if err != nil {
		t.Fatal(err)
	}

	refusals := []struct {
		name    string
		args    []string
		prepare func()
		want    []string
	}{
		{name: "no new key file named", args: nil,
			want: []string{"--new-kek-file", "head -c 32 /dev/urandom"}},
		{name: "a new key file that does not exist", args: []string{"--new-kek-file", newFile},
			want: []string{"no file", "head -c 32 /dev/urandom", "nothing changed"}},
		{name: "a new key file of the wrong size", args: []string{"--new-kek-file", newFile},
			prepare: func() { writeFile(t, newFile, []byte("short")) },
			want:    []string{"must be exactly 32 bytes", "nothing changed"}},
		{name: "the key in use as the new key", args: []string{"--new-kek-file", newFile},
			prepare: func() { writeFile(t, newFile, oldBytes) },
			want:    []string{"is the key in use", "nothing changed"}},
	}
	for _, tc := range refusals {
		_ = os.Remove(newFile)
		if tc.prepare != nil {
			tc.prepare()
		}
		_, err := runStrazad(t, append([]string{"reseal-signing-keys", "--data-dir", dir}, tc.args...)...)
		if err == nil {
			t.Fatalf("%s: the command succeeded", tc.name)
		}
		for _, want := range tc.want {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("%s: the refusal %q does not say %q", tc.name, err, want)
			}
		}
		if err := load(oldKEK); err != nil {
			t.Fatalf("%s: the refused command changed a key: %v", tc.name, err)
		}
		if _, statErr := os.Stat(newFile); tc.prepare == nil && !os.IsNotExist(statErr) {
			t.Errorf("%s: the command created the new key file", tc.name)
		}
	}
	_ = os.Remove(newFile)
	newKEK, err := secrets.LoadOrCreateKEK(newFile)
	if err != nil {
		t.Fatal(err)
	}
	out, err := runStrazad(t, "reseal-signing-keys", "--data-dir", dir, "--new-kek-file", newFile)
	if err != nil {
		t.Fatalf("reseal: %v", err)
	}
	want := "Sealed 2 signing keys under the key in " + newFile + ".\n" +
		"Put that file in place of " + oldFile + " on every replica, then start strazad.\n"
	if out != want {
		t.Errorf("printed %q, want %q", out, want)
	}
	if err := load(newKEK); err != nil {
		t.Errorf("the keys do not open under the new KEK: %v", err)
	}
	if err := load(oldKEK); err == nil {
		t.Error("the keys still open under the old KEK")
	}
	out, err = runStrazad(t, "reseal-signing-keys", "--data-dir", dir, "--new-kek-file", newFile)
	if err != nil || out != "No signing key in the database is sealed under the key in "+oldFile+". Nothing changed.\n" {
		t.Errorf("a second run printed %q, %v", out, err)
	}
}

func writeFile(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
}
