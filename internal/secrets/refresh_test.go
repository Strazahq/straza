package secrets

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/strazahq/straza/internal/store"
)

// TestRefreshDue covers the refresh worker rules: rotate grants
// expiring within the window (including ones already expired, the
// survives-restart case), leave healthy/non-expiring/refreshless grants
// alone, and skip unknown providers without failing the pass.
func TestRefreshDue(t *testing.T) {
	ctx := context.Background()
	st := testStore(t)
	broker := NewBroker(st, testKEK(t))

	var refreshCalls atomic.Int64
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		if r.PostForm.Get("grant_type") != "refresh_token" {
			t.Errorf("grant_type = %q", r.PostForm.Get("grant_type"))
		}
		refreshCalls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"access_token":"gho_fresh_` + r.PostForm.Get("refresh_token") +
			`","refresh_token":"ghr_rotated","expires_in":28800}`))
	}))
	t.Cleanup(provider.Close)

	app, err := st.Apps().Create(ctx, store.App{Name: "github", RuntimeKind: "remote"})
	if err != nil {
		t.Fatal(err)
	}
	mkUser := func(name string) string {
		t.Helper()
		u, err := st.Users().Create(ctx, store.User{Username: name})
		if err != nil {
			t.Fatal(err)
		}
		return u.ID
	}
	due := mkUser("due")
	expired := mkUser("expired")
	healthy := mkUser("healthy")
	forever := mkUser("forever")
	norefresh := mkUser("norefresh")
	orphan := mkUser("orphan")

	in5m := time.Now().Add(5 * time.Minute)
	in5h := time.Now().Add(5 * time.Hour)
	past := time.Now().Add(-time.Hour)
	seed := func(userID, access, refresh, providerName string, exp *time.Time) {
		t.Helper()
		if _, err := broker.SetGrant(ctx, app.ID, userID,
			Grant{AccessToken: access, RefreshToken: refresh},
			GrantMeta{Provider: providerName, ExpiresAt: exp}); err != nil {
			t.Fatal(err)
		}
	}
	seed(due, "gho_due", "r1", "github", &in5m)
	seed(expired, "gho_expired", "r2", "github", &past)
	seed(healthy, "gho_healthy", "r3", "github", &in5h)
	seed(forever, "gho_forever", "r4", "github", nil)
	seed(norefresh, "gho_norefresh", "", "github", &in5m)
	seed(orphan, "gho_orphan", "r5", "not-configured", &in5m)

	ref := NewRefresher(RefresherOpts{
		Store: st, Broker: broker,
		Providers: map[string]ProviderConfig{"github": {
			Name: "github", ClientID: "c", ClientSecret: "s", TokenURL: provider.URL,
		}},
		Log: slog.New(slog.DiscardHandler),
	})
	n, err := ref.RefreshDue(ctx)
	if err != nil {
		t.Fatalf("RefreshDue: %v", err)
	}
	if n != 2 || refreshCalls.Load() != 2 {
		t.Errorf("refreshed = %d (provider calls %d), want 2 (due + expired)", n, refreshCalls.Load())
	}

	// Rotated grants resolve to the fresh tokens, including the one that was
	// already dead, which is exactly what a strazad restart must recover.
	if s := broker.ForUser(app.ID, due).Secret; s == nil || s.Value != "gho_fresh_r1" {
		t.Errorf("due grant after refresh = %+v", s)
	}
	if s := broker.ForUser(app.ID, expired).Secret; s == nil || s.Value != "gho_fresh_r2" {
		t.Errorf("expired grant after refresh = %+v", s)
	}
	// Untouched grants keep their tokens.
	if s := broker.ForUser(app.ID, healthy).Secret; s == nil || s.Value != "gho_healthy" {
		t.Errorf("healthy grant = %+v", s)
	}
	if s := broker.ForUser(app.ID, forever).Secret; s == nil || s.Value != "gho_forever" {
		t.Errorf("non-expiring grant = %+v", s)
	}
	// The refreshless due grant stays stale → keeps failing closed.
	if s := broker.ForUser(app.ID, norefresh).Secret; s == nil || s.Value != "gho_norefresh" {
		t.Errorf("refreshless grant = %+v", s)
	}

	// A second pass finds nothing due (the fresh expiry is 8 h out).
	if n, err := ref.RefreshDue(ctx); err != nil || n != 0 {
		t.Errorf("second pass = %d, %v; want 0", n, err)
	}

	// The rotated refresh token was persisted: expire the row again and the
	// next pass must succeed using ghr_rotated (GitHub rotates on use).
	if creds, err := st.Credentials().ListByApp(ctx, app.ID); err == nil {
		for _, c := range creds {
			if c.OwnerID == due {
				if c.RotatedAt == nil {
					t.Error("rotated_at not set on refresh")
				}
			}
		}
	}
}

// TestRefreshDueProviderOutage: a failing provider must not wedge the pass;
// other grants still rotate, the failed one stays stale (fail closed).
func TestRefreshDueProviderOutage(t *testing.T) {
	ctx := context.Background()
	st := testStore(t)
	broker := NewBroker(st, testKEK(t))

	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	}))
	t.Cleanup(provider.Close)

	app, err := st.Apps().Create(ctx, store.App{Name: "github", RuntimeKind: "remote"})
	if err != nil {
		t.Fatal(err)
	}
	u, err := st.Users().Create(ctx, store.User{Username: "u"})
	if err != nil {
		t.Fatal(err)
	}
	past := time.Now().Add(-time.Minute)
	if _, err := broker.SetGrant(ctx, app.ID, u.ID,
		Grant{AccessToken: "gho_old", RefreshToken: "r"},
		GrantMeta{Provider: "github", ExpiresAt: &past}); err != nil {
		t.Fatal(err)
	}

	ref := NewRefresher(RefresherOpts{
		Store: st, Broker: broker,
		Providers: map[string]ProviderConfig{"github": {
			Name: "github", ClientID: "c", ClientSecret: "s", TokenURL: provider.URL,
		}},
		Log: slog.New(slog.DiscardHandler),
	})
	if n, err := ref.RefreshDue(ctx); err != nil || n != 0 {
		t.Errorf("pass = %d, %v; want 0 refreshed and no hard error", n, err)
	}
	if s := broker.ForUser(app.ID, u.ID).Secret; s != nil {
		t.Errorf("expired grant resolved after failed refresh: %+v (must fail closed)", s)
	}
}
