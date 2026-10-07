package secrets

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/strazahq/straza/internal/config"
	"github.com/strazahq/straza/internal/store"
)

func testKEK(t *testing.T) *Builtin {
	t.Helper()
	b, err := LoadOrCreateKEK(filepath.Join(t.TempDir(), "secret.key"))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestSealOpenRoundTrip(t *testing.T) {
	b := testKEK(t)
	sealed, err := b.Seal([]byte("ghp_super-secret"))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(sealed, []byte("super-secret")) {
		t.Fatal("ciphertext contains plaintext")
	}
	plain, err := b.Open(sealed)
	if err != nil {
		t.Fatal(err)
	}
	if string(plain) != "ghp_super-secret" {
		t.Errorf("round trip = %q", plain)
	}
}

func TestOpenRejectsTamperAndWrongKey(t *testing.T) {
	b := testKEK(t)
	sealed, err := b.Seal([]byte("value"))
	if err != nil {
		t.Fatal(err)
	}
	flipped := append([]byte{}, sealed...)
	flipped[len(flipped)-1] ^= 1
	if _, err := b.Open(flipped); err == nil {
		t.Error("tampered ciphertext opened")
	}
	other := testKEK(t)
	if _, err := other.Open(sealed); err == nil {
		t.Error("wrong KEK opened ciphertext")
	}
	if _, err := b.Open([]byte("short")); err == nil {
		t.Error("truncated ciphertext opened")
	}
}

func TestKEKPersistsAcrossLoads(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "secret.key")
	a, err := LoadOrCreateKEK(path)
	if err != nil {
		t.Fatal(err)
	}
	sealed, err := a.Seal([]byte("v"))
	if err != nil {
		t.Fatal(err)
	}
	b, err := LoadOrCreateKEK(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := b.Open(sealed); err != nil {
		t.Errorf("reloaded KEK cannot open: %v", err)
	}
	if err := os.WriteFile(path, []byte("too short"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadOrCreateKEK(path); err == nil {
		t.Error("corrupt KEK file accepted")
	}
}

func testStore(t *testing.T) store.Store {
	t.Helper()
	cfg := config.Config{
		Profile: config.ProfileStandalone,
		DataDir: t.TempDir(),
		Store:   config.Store{Driver: config.DriverSQLite},
	}
	st, err := store.Open(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

// TestBrokerResolution covers the static resolution rules: role-bound lookup,
// deterministic tie-break by role name, app-level secret, and fail-closed
// behavior for unknown apps/roles.
func TestBrokerResolution(t *testing.T) {
	ctx := context.Background()
	st := testStore(t)
	broker := NewBroker(st, testKEK(t))

	app, err := st.Apps().Create(ctx, store.App{Name: "github", RuntimeKind: "remote"})
	if err != nil {
		t.Fatal(err)
	}
	dev, err := st.Roles().Create(ctx, store.Role{Name: "dev"})
	if err != nil {
		t.Fatal(err)
	}
	ops, err := st.Roles().Create(ctx, store.Role{Name: "ops"})
	if err != nil {
		t.Fatal(err)
	}

	if _, err := broker.Set(ctx, app.ID, dev.ID, "dev-token"); err != nil {
		t.Fatal(err)
	}
	if _, err := broker.Set(ctx, app.ID, ops.ID, "ops-token"); err != nil {
		t.Fatal(err)
	}

	if s := broker.ForRoles(app.ID, []string{"dev"}, ""); s == nil || s.Value != "dev-token" {
		t.Errorf("dev secret = %+v", s)
	}
	if s := broker.ForRoles(app.ID, []string{"ops"}, ""); s == nil || s.Value != "ops-token" {
		t.Errorf("ops secret = %+v", s)
	}
	// Tie-break: lexicographically smallest role name (dev < ops).
	if s := broker.ForRoles(app.ID, []string{"ops", "dev"}, ""); s == nil || s.Value != "dev-token" {
		t.Errorf("tie-break secret = %+v", s)
	}
	if s := broker.ForRoles(app.ID, []string{"finance"}, ""); s != nil {
		t.Errorf("unbound role resolved a secret: %+v", s)
	}
	if s := broker.ForRoles("nonexistent-app", []string{"dev"}, ""); s != nil {
		t.Errorf("unknown app resolved a secret: %+v", s)
	}
	if s := broker.AppSecret(app.ID); s == nil || s.Value != "dev-token" {
		t.Errorf("app secret = %+v", s)
	}

	// Rotation: Set on the same app+role updates in place.
	if _, err := broker.Set(ctx, app.ID, dev.ID, "dev-token-2"); err != nil {
		t.Fatal(err)
	}
	if s := broker.ForRoles(app.ID, []string{"dev"}, ""); s == nil || s.Value != "dev-token-2" {
		t.Errorf("rotated secret = %+v", s)
	}
	creds, err := st.Credentials().ListByApp(ctx, app.ID)
	if err != nil || len(creds) != 2 {
		t.Errorf("credential rows = %d err=%v (rotation must not duplicate)", len(creds), err)
	}

	// Ciphertext at rest never contains the plaintext.
	for _, c := range creds {
		if bytes.Contains(c.EncPayload, []byte("token")) {
			t.Error("enc_payload contains plaintext")
		}
	}
}

// TestBrokerUserGrants covers the per-user OAuth resolution rules: one
// grant per (app, user), distinct users resolve distinct upstream tokens,
// expired grants fail closed, and grants coexist with role-bound statics.
func TestBrokerUserGrants(t *testing.T) {
	ctx := context.Background()
	st := testStore(t)
	broker := NewBroker(st, testKEK(t))

	app, err := st.Apps().Create(ctx, store.App{Name: "github", RuntimeKind: "remote"})
	if err != nil {
		t.Fatal(err)
	}
	alice, err := st.Users().Create(ctx, store.User{Username: "alice"})
	if err != nil {
		t.Fatal(err)
	}
	bob, err := st.Users().Create(ctx, store.User{Username: "bob"})
	if err != nil {
		t.Fatal(err)
	}

	future := time.Now().Add(8 * time.Hour)
	if _, err := broker.SetGrant(ctx, app.ID, alice.ID,
		Grant{AccessToken: "gho_alice", RefreshToken: "ghr_alice"},
		GrantMeta{Provider: "github", ExpiresAt: &future}); err != nil {
		t.Fatal(err)
	}
	if _, err := broker.SetGrant(ctx, app.ID, bob.ID,
		Grant{AccessToken: "gho_bob"}, GrantMeta{Provider: "github"}); err != nil {
		t.Fatal(err)
	}

	// Two users, two distinct upstream identities.
	if s := broker.ForUser(app.ID, alice.ID).Secret; s == nil || s.Value != "gho_alice" {
		t.Errorf("alice grant = %+v", s)
	}
	if s := broker.ForUser(app.ID, bob.ID).Secret; s == nil || s.Value != "gho_bob" {
		t.Errorf("bob grant = %+v (non-expiring grants must resolve)", s)
	}

	// Fail closed: unknown user, unknown app.
	if s := broker.ForUser(app.ID, "nobody").Secret; s != nil {
		t.Errorf("unconnected user resolved a grant: %+v", s)
	}
	if s := broker.ForUser("nonexistent-app", alice.ID).Secret; s != nil {
		t.Errorf("unknown app resolved a grant: %+v", s)
	}

	// Fail closed: an expired grant resolves to nothing.
	past := time.Now().Add(-time.Minute)
	if _, err := broker.SetGrant(ctx, app.ID, alice.ID,
		Grant{AccessToken: "gho_alice_stale"}, GrantMeta{Provider: "github", ExpiresAt: &past}); err != nil {
		t.Fatal(err)
	}
	if s := broker.ForUser(app.ID, alice.ID).Secret; s != nil {
		t.Errorf("expired grant resolved: %+v", s)
	}

	// Upsert: reconnecting rotates in place, no duplicate rows.
	if _, err := broker.SetGrant(ctx, app.ID, alice.ID,
		Grant{AccessToken: "gho_alice_2"}, GrantMeta{Provider: "github", ExpiresAt: &future}); err != nil {
		t.Fatal(err)
	}
	if s := broker.ForUser(app.ID, alice.ID).Secret; s == nil || s.Value != "gho_alice_2" {
		t.Errorf("reconnected grant = %+v", s)
	}
	creds, err := st.Credentials().ListByApp(ctx, app.ID)
	if err != nil || len(creds) != 2 {
		t.Fatalf("credential rows = %d err=%v (upsert must not duplicate)", len(creds), err)
	}

	// At rest: ciphertext hides tokens; plaintext meta carries none.
	for _, c := range creds {
		if bytes.Contains(c.EncPayload, []byte("gho_")) || bytes.Contains(c.EncPayload, []byte("ghr_")) {
			t.Error("enc_payload contains a plaintext token")
		}
		if bytes.Contains([]byte(c.OAuthMeta), []byte("gho_")) || bytes.Contains([]byte(c.OAuthMeta), []byte("ghr_")) {
			t.Error("oauth_meta contains a token")
		}
	}

	// Grants and role statics coexist on one app without cross-talk.
	dev, err := st.Roles().Create(ctx, store.Role{Name: "dev"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := broker.Set(ctx, app.ID, dev.ID, "static-token"); err != nil {
		t.Fatal(err)
	}
	if s := broker.ForRoles(app.ID, []string{"dev"}, ""); s == nil || s.Value != "static-token" {
		t.Errorf("role static = %+v", s)
	}
	if s := broker.ForUser(app.ID, alice.ID).Secret; s == nil || s.Value != "gho_alice_2" {
		t.Errorf("grant after static set = %+v", s)
	}

	// Disconnect: the grant is gone, others untouched.
	if err := broker.DeleteGrant(ctx, app.ID, alice.ID); err != nil {
		t.Fatal(err)
	}
	if s := broker.ForUser(app.ID, alice.ID).Secret; s != nil {
		t.Errorf("deleted grant resolved: %+v", s)
	}
	if s := broker.ForUser(app.ID, bob.ID).Secret; s == nil || s.Value != "gho_bob" {
		t.Errorf("bob grant after alice disconnect = %+v", s)
	}
}
