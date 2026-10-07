package server

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"
)

// TestAPITokenLifecycle pins the long-lived admin API token kind. An IGA
// task, such as midPoint's async import or reconciliation, authenticates
// with whatever static bearer the resource config holds, and a 5-minute
// session token expires between launching a task and the task executing,
// so /v1/admin takes a durable, revocable, hashed-at-rest bearer.
func TestAPITokenLifecycle(t *testing.T) {
	t.Parallel()
	app, base := testApp(t)
	user := seedIdentity(t, app)
	grantAdmin(t, app, user.ID)
	adminTok, _ := checkinToken(t, app, base)

	// --- mint validation (table) ---
	for _, tc := range []struct {
		name string
		body map[string]any
		want int
	}{
		{"missing name", map[string]any{}, http.StatusBadRequest},
		{"bad scope", map[string]any{"name": "x", "scope": "root"}, http.StatusBadRequest},
		{"retired read word", map[string]any{"name": "connector", "scope": "read"}, http.StatusBadRequest},
		{"missing scope", map[string]any{"name": "defaulted"}, http.StatusBadRequest},
		{"grants scope", map[string]any{"name": "connector", "scope": "identity:read,changes:read"}, http.StatusCreated},
		{"full scope", map[string]any{"name": "automation", "scope": "full"}, http.StatusCreated},
	} {
		var out struct {
			ID    string `json:"id"`
			Name  string `json:"name"`
			Scope string `json:"scope"`
			Token string `json:"token"`
		}
		code := adminReq(t, http.MethodPost, base+"/v1/admin/api-tokens", adminTok, tc.body, &out)
		if code != tc.want {
			t.Fatalf("%s: mint = %d, want %d", tc.name, code, tc.want)
		}
		if tc.want != http.StatusCreated {
			continue
		}
		if !strings.HasPrefix(out.Token, "wat_") {
			t.Errorf("%s: token %q must carry the wat_ prefix", tc.name, out.Token)
		}
		if tc.name == "grants scope" && out.Scope != "changes:read,identity:read" {
			t.Errorf("grants stored as %q, want canonical sorted form", out.Scope)
		}
	}

	// --- grants scope: covered reads pass, everything else refused ---
	// (per-area coverage matrix lives in TestAPITokenGrantEnforcement; this
	// keeps the lifecycle shape: mint → use → list → revoke → dead)
	var minted struct {
		ID    string `json:"id"`
		Token string `json:"token"`
	}
	if code := adminReq(t, http.MethodPost, base+"/v1/admin/api-tokens", adminTok,
		map[string]any{"name": "reader", "scope": "identity:read"}, &minted); code != http.StatusCreated {
		t.Fatalf("mint reader = %d", code)
	}
	if code := adminReq(t, http.MethodGet, base+"/v1/admin/users", minted.Token, nil, nil); code != http.StatusOK {
		t.Fatalf("identity:read GET users = %d, want 200", code)
	}
	// overview is config-area now: the connector testEndpoint that hits it
	// needs config:read granted explicitly (seed.sh mints exactly that).
	if code := adminReq(t, http.MethodGet, base+"/v1/admin/overview", minted.Token, nil, nil); code != http.StatusForbidden {
		t.Errorf("identity:read GET overview = %d, want 403 (config:read is its own grant)", code)
	}
	if code := adminReq(t, http.MethodPost, base+"/v1/admin/users", minted.Token,
		map[string]any{"username": "smuggled"}, nil); code != http.StatusForbidden {
		t.Errorf("identity:read POST = %d, want 403", code)
	}

	// --- full scope mutates ---
	var full struct {
		Token string `json:"token"`
	}
	if code := adminReq(t, http.MethodPost, base+"/v1/admin/api-tokens", adminTok,
		map[string]any{"name": "writer", "scope": "full"}, &full); code != http.StatusCreated {
		t.Fatalf("mint writer = %d", code)
	}
	if code := adminReq(t, http.MethodPost, base+"/v1/admin/users", full.Token,
		map[string]any{"username": "made-by-token"}, nil); code != http.StatusCreated {
		t.Errorf("full-scope POST users = %d, want 201", code)
	}

	// --- list exposes meta, never secrets ---
	var listed []struct {
		ID    string `json:"id"`
		Name  string `json:"name"`
		Scope string `json:"scope"`
		Token string `json:"token"`
	}
	if code := adminReq(t, http.MethodGet, base+"/v1/admin/api-tokens", adminTok, nil, &listed); code != http.StatusOK {
		t.Fatalf("list = %d", code)
	}
	found := false
	for _, m := range listed {
		if m.Token != "" {
			t.Errorf("list leaked a token value for %q", m.Name)
		}
		if m.ID == minted.ID {
			found = true
			if m.Scope != "identity:read" {
				t.Errorf("listed scope = %q, want identity:read", m.Scope)
			}
		}
	}
	if !found {
		t.Error("minted token missing from list")
	}

	// --- revoke kills it durably ---
	if code := adminReq(t, http.MethodDelete, base+"/v1/admin/api-tokens/"+minted.ID, adminTok, nil, nil); code != http.StatusOK {
		t.Fatalf("revoke = %d", code)
	}
	if code := adminReq(t, http.MethodGet, base+"/v1/admin/users", minted.Token, nil, nil); code != http.StatusUnauthorized {
		t.Errorf("revoked token GET = %d, want 401", code)
	}
	if code := adminReq(t, http.MethodDelete, base+"/v1/admin/api-tokens/nope", adminTok, nil, nil); code != http.StatusNotFound {
		t.Errorf("revoke unknown id = %d, want 404", code)
	}

	// --- forgeries with the right shape are still refused ---
	if code := adminReq(t, http.MethodGet, base+"/v1/admin/users", "wat_forgery", nil, nil); code != http.StatusUnauthorized {
		t.Errorf("forged wat_ token = %d, want 401", code)
	}
}

// TestTokenListsNewestFirst pins deterministic list order for both token
// kinds. The handlers assemble their payload from a settings-key map, and Go
// randomizes map iteration, so an unsorted list reshuffles under the console's
// 15s poll. Newest-first, name as the
// tie-break.
func TestTokenListsNewestFirst(t *testing.T) {
	t.Parallel()
	app, base := testApp(t)
	user := seedIdentity(t, app)
	grantAdmin(t, app, user.ID)
	adminTok, _ := checkinToken(t, app, base)

	names := []string{"alpha", "bravo", "charlie", "delta", "echo", "foxtrot"}
	for _, n := range names {
		if code := adminReq(t, http.MethodPost, base+"/v1/admin/api-tokens", adminTok,
			map[string]any{"name": n, "scope": "audit:read"}, nil); code != http.StatusCreated {
			t.Fatalf("mint api-token %s = %d", n, code)
		}
	}
	for _, path := range []string{"/v1/admin/api-tokens"} {
		var out []struct {
			Name string `json:"name"`
		}
		if code := adminReq(t, http.MethodGet, base+path, adminTok, nil, &out); code != http.StatusOK {
			t.Fatalf("list %s = %d", path, code)
		}
		if len(out) != len(names) {
			t.Fatalf("list %s = %d rows, want %d", path, len(out), len(names))
		}
		for i, n := range names {
			if got := out[len(names)-1-i].Name; got != n {
				t.Fatalf("%s order: minted %v, listed %+v (want newest first)", path, names, out)
			}
		}
	}
}

// Token lifecycle: optional expiry at
// mint, fail-closed verification of expired credentials, a throttled
// last-used stamp, and duplicate names refused.
func TestAPITokenExpiryAndLastUsed(t *testing.T) {
	t.Parallel()
	app, base := testApp(t)
	ctx := context.Background()
	user := seedIdentity(t, app)
	grantAdmin(t, app, user.ID)
	adminTok, _ := checkinToken(t, app, base)

	// Mint with a TTL: the response and the list both carry the deadline.
	var minted struct {
		Token   string `json:"token"`
		Expires string `json:"expires"`
	}
	if code := adminReq(t, http.MethodPost, base+"/v1/admin/api-tokens", adminTok,
		map[string]any{"name": "expiring", "scope": "audit:read", "expires_in": 3600}, &minted); code != http.StatusCreated {
		t.Fatalf("mint with ttl = %d", code)
	}
	if minted.Expires == "" {
		t.Errorf("mint response misses the expiry deadline")
	}
	// Negative TTL is refused.
	if code := adminReq(t, http.MethodPost, base+"/v1/admin/api-tokens", adminTok,
		map[string]any{"name": "bad-ttl", "scope": "audit:read", "expires_in": -5}, nil); code != http.StatusBadRequest {
		t.Errorf("negative expires_in = %d, want 400", code)
	}
	// Duplicate names are refused.
	if code := adminReq(t, http.MethodPost, base+"/v1/admin/api-tokens", adminTok,
		map[string]any{"name": "expiring", "scope": "audit:read"}, nil); code != http.StatusConflict {
		t.Errorf("duplicate api-token name = %d, want 409", code)
	}

	// An expired credential is no credential: seed a token whose deadline
	// passed and watch verification fail closed.
	expiredTok := apiTokenPrefix + "expired-fixture"
	sum := sha256.Sum256([]byte(expiredTok))
	past := time.Now().Add(-time.Hour).UTC()
	meta, _ := json.Marshal(apiTokenMeta{ID: "tok-x", Name: "dead", Scope: "audit:read", Created: past, Expires: &past})
	if err := app.store.Settings().Set(ctx, apiTokenKeyPrefix+hex.EncodeToString(sum[:]), string(meta)); err != nil {
		t.Fatal(err)
	}
	if code := adminReq(t, http.MethodGet, base+"/v1/admin/audit", expiredTok, nil, nil); code != http.StatusUnauthorized {
		t.Errorf("expired token = %d, want 401", code)
	}

	// Last-used: one authenticated request stamps the row (throttled, so the
	// first use writes it).
	if code := adminReq(t, http.MethodGet, base+"/v1/admin/audit", minted.Token, nil, nil); code != http.StatusOK {
		t.Fatalf("fresh token use = %d", code)
	}
	var rows []struct {
		Name     string  `json:"name"`
		LastUsed *string `json:"lastUsed"`
		Expires  *string `json:"expires"`
	}
	if code := adminReq(t, http.MethodGet, base+"/v1/admin/api-tokens", adminTok, nil, &rows); code != http.StatusOK {
		t.Fatalf("list = %d", code)
	}
	for _, r := range rows {
		if r.Name == "expiring" {
			if r.LastUsed == nil || *r.LastUsed == "" {
				t.Errorf("used token has no lastUsed stamp: %+v", r)
			}
			if r.Expires == nil || *r.Expires == "" {
				t.Errorf("list row misses expires: %+v", r)
			}
		}
	}
}
