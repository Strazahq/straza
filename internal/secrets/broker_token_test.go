package secrets

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/strazahq/straza/internal/store"
)

// TestBrokerTokenRows pins each caller's own pasted token: SetToken stores
// one user-scoped token row per app and user, ForUser resolves it with
// Present set, an expired row is present and unusable, the agents opt-in
// rides the row, DeleteGrant takes either user kind, a grant written over
// a token replaces it, and a fresh broker loads token rows from the store.
func TestBrokerTokenRows(t *testing.T) {
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

	if got := broker.ForUser(app.ID, alice.ID); got.Present || got.Secret != nil {
		t.Fatalf("no row yet, got %+v", got)
	}
	future := time.Now().Add(48 * time.Hour).UTC()
	row, err := broker.SetToken(ctx, app.ID, alice.ID, "tok-alice-NEVER-LEAK", GrantMeta{ExpiresAt: &future, SetBy: bob.ID})
	if err != nil {
		t.Fatal(err)
	}
	if row.Scope != store.CredScopeUser || row.OwnerID != alice.ID || row.Kind != store.CredToken {
		t.Fatalf("token row = %+v, want a user-scoped token row owned by alice", row)
	}
	got := broker.ForUser(app.ID, alice.ID)
	if !got.Present || got.Secret == nil || got.Secret.Value != "tok-alice-NEVER-LEAK" || got.Secret.ID != row.ID {
		t.Fatalf("alice token = %+v, want present and resolvable", got)
	}
	if got.ExpiresAt == nil || !got.ExpiresAt.Equal(future) || got.AllowAgents {
		t.Errorf("alice meta = expires %v allow %v, want the stored expiry and no opt-in", got.ExpiresAt, got.AllowAgents)
	}
	ur, ok, err := broker.UserRowFor(ctx, app.ID, alice.ID)
	if err != nil || !ok {
		t.Fatalf("UserRowFor = %v %v", ok, err)
	}
	if ur.Kind != store.CredToken || ur.Fingerprint != Fingerprint("tok-alice-NEVER-LEAK") || ur.Meta.SetBy != bob.ID {
		t.Errorf("user row = %+v, want kind token, the fingerprint and the setter", ur)
	}

	// The opt-in rides the row and is visible on the request path.
	if _, err := broker.SetAllowAgents(ctx, app.ID, alice.ID, true); err != nil {
		t.Fatal(err)
	}
	if got := broker.ForUser(app.ID, alice.ID); !got.AllowAgents || got.Secret == nil {
		t.Errorf("after opt-in = %+v, want allowAgents with the secret intact", got)
	}
	if _, err := broker.SetAllowAgents(ctx, app.ID, bob.ID, true); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("opt-in with no row: err = %v, want ErrNotFound", err)
	}

	// A re-paste rotates in place: same row id, new value.
	row2, err := broker.SetToken(ctx, app.ID, alice.ID, "tok-alice-ROTATED", GrantMeta{AllowAgents: true})
	if err != nil {
		t.Fatal(err)
	}
	if row2.ID != row.ID {
		t.Errorf("rotation created a new row %s, want %s rotated in place", row2.ID, row.ID)
	}
	if got := broker.ForUser(app.ID, alice.ID); got.Secret == nil || got.Secret.Value != "tok-alice-ROTATED" || got.ExpiresAt != nil {
		t.Errorf("after rotation = %+v, want the new value and no expiry", got)
	}

	// An expired row is present and unusable, never absent.
	past := time.Now().Add(-time.Hour)
	if _, err := broker.SetToken(ctx, app.ID, bob.ID, "tok-bob-OLD", GrantMeta{ExpiresAt: &past}); err != nil {
		t.Fatal(err)
	}
	if got := broker.ForUser(app.ID, bob.ID); !got.Present || got.Secret != nil || got.ExpiresAt == nil {
		t.Errorf("expired row = %+v, want present without a secret", got)
	}

	// A fresh broker loads token rows from the store.
	again := NewBroker(st, testKEKFrom(t, broker))
	if err := again.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	if got := again.ForUser(app.ID, alice.ID); got.Secret == nil || got.Secret.Value != "tok-alice-ROTATED" || !got.AllowAgents {
		t.Errorf("reloaded alice = %+v, want the token and the opt-in", got)
	}

	// A grant written over a token replaces it: one row per user and app.
	if _, err := broker.SetGrant(ctx, app.ID, alice.ID, Grant{AccessToken: "gho_alice"}, GrantMeta{Provider: "github"}); err != nil {
		t.Fatal(err)
	}
	rows, err := st.Credentials().ListByOwner(ctx, store.CredScopeUser, alice.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].Kind != store.CredOAuth {
		t.Errorf("rows after a grant over a token = %+v, want one oauth row", rows)
	}
	if got := broker.ForUser(app.ID, alice.ID); got.Secret == nil || got.Secret.Value != "gho_alice" {
		t.Errorf("grant over token = %+v", got)
	}

	// DeleteGrant takes either kind.
	if err := broker.DeleteGrant(ctx, app.ID, bob.ID); err != nil {
		t.Fatal(err)
	}
	if got := broker.ForUser(app.ID, bob.ID); got.Present {
		t.Errorf("bob after delete = %+v, want absent", got)
	}
	if _, ok, _ := broker.UserRowFor(ctx, app.ID, bob.ID); ok {
		t.Error("bob's row still listed after delete")
	}
}

// testKEKFrom hands a second broker the first one's provider so a reload
// opens the rows the first sealed.
func testKEKFrom(t *testing.T, b *Broker) Provider {
	t.Helper()
	return b.provider
}

// TestRefresherSkipsTokenRows: a pasted token has no refresh handle and no
// provider, so the refresh worker never touches it, whatever its expiry.
func TestRefresherSkipsTokenRows(t *testing.T) {
	ctx := context.Background()
	st := testStore(t)
	broker := NewBroker(st, testKEK(t))
	var calls atomic.Int64
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		http.Error(w, "unexpected", http.StatusInternalServerError)
	}))
	t.Cleanup(provider.Close)

	app, err := st.Apps().Create(ctx, store.App{Name: "github", RuntimeKind: "remote"})
	if err != nil {
		t.Fatal(err)
	}
	u, err := st.Users().Create(ctx, store.User{Username: "alice"})
	if err != nil {
		t.Fatal(err)
	}
	soon := time.Now().Add(2 * time.Minute)
	if _, err := broker.SetToken(ctx, app.ID, u.ID, "tok-soon", GrantMeta{ExpiresAt: &soon}); err != nil {
		t.Fatal(err)
	}
	ref := NewRefresher(RefresherOpts{
		Store: st, Broker: broker,
		Providers: map[string]ProviderConfig{"github": {Name: "github", ClientID: "c", ClientSecret: "s", TokenURL: provider.URL}},
		Log:       slog.New(slog.DiscardHandler),
	})
	n, err := ref.RefreshDue(ctx)
	if err != nil || n != 0 || calls.Load() != 0 {
		t.Errorf("RefreshDue = %d, %v with %d provider calls; want nothing refreshed", n, err, calls.Load())
	}
	if got := broker.ForUser(app.ID, u.ID); got.Secret == nil || got.Secret.Value != "tok-soon" {
		t.Errorf("token after the pass = %+v, want untouched", got)
	}
}
