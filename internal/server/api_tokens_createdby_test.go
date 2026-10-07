package server

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"testing"
)

// TestAPITokenCreatedBy pins who minted an admin API token. The name comes
// off the one actor seam requireAdmin fills, so a console or CLI session
// records the username and a token minting a token records the minting
// token's name. A row stored before the field existed lists unchanged.
func TestAPITokenCreatedBy(t *testing.T) {
	t.Parallel()
	app, base := testApp(t)
	user := seedIdentity(t, app)
	grantAdmin(t, app, user.ID)
	sessionTok, _ := checkinToken(t, app, base)

	type listed struct {
		ID        string `json:"id"`
		Name      string `json:"name"`
		CreatedBy string `json:"created_by"`
	}
	list := func() map[string]listed {
		t.Helper()
		var rows []listed
		if code := adminReq(t, http.MethodGet, base+"/v1/admin/api-tokens", sessionTok, nil, &rows); code != http.StatusOK {
			t.Fatalf("list = %d", code)
		}
		out := map[string]listed{}
		for _, r := range rows {
			out[r.Name] = r
		}
		return out
	}
	mint := func(bearer, name, scope string) string {
		t.Helper()
		var out struct {
			Token string `json:"token"`
		}
		if code := adminReq(t, http.MethodPost, base+"/v1/admin/api-tokens", bearer,
			map[string]any{"name": name, "scope": scope}, &out); code != http.StatusCreated {
			t.Fatalf("mint %s = %d", name, code)
		}
		return out.Token
	}

	// A pre-field row: the stored meta has no created_by at all.
	ctx := context.Background()
	sum := sha256.Sum256([]byte("wat_legacyrow"))
	if err := app.store.Settings().Set(ctx, apiTokenKeyPrefix+hex.EncodeToString(sum[:]),
		`{"id":"legacy-1","name":"legacy","scope":"full","created":"2026-08-01T10:00:00Z"}`); err != nil {
		t.Fatalf("seed legacy row: %v", err)
	}

	minter := mint(sessionTok, "by-session", "full")
	mint(minter, "by-token", "identity:read")

	for _, tc := range []struct{ name, want string }{
		{"by-session", "kim"},
		{"by-token", "by-session"},
		{"legacy", ""},
	} {
		got, ok := list()[tc.name]
		if !ok {
			t.Fatalf("%s is missing from the list", tc.name)
		}
		if got.CreatedBy != tc.want {
			t.Errorf("%s created_by = %q, want %q", tc.name, got.CreatedBy, tc.want)
		}
	}
}
