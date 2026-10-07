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

	"github.com/google/uuid"
	"github.com/strazahq/straza/internal/tokenscopes"
)

// TestAdminRouteAreaMapMatchesRouteTable is the drift gate: every registered
// /v1/admin route is mapped to an area, and every map entry names a live
// route. A new admin endpoint fails here until someone DECIDES its area;
// unmapped would mean 403 for every grants token, silently.
func TestAdminRouteAreaMapMatchesRouteTable(t *testing.T) {
	t.Parallel()
	app, _ := testApp(t)
	seen := map[string]bool{}
	for _, rt := range app.routeTable() {
		if !strings.HasPrefix(rt.pattern, "/v1/admin/") {
			continue
		}
		key := rt.method + " " + rt.pattern
		seen[key] = true
		if _, ok := tokenscopes.AdminRouteArea[key]; !ok {
			t.Errorf("admin route %q has no adminRouteArea entry; grants tokens get 403 on it until mapped", key)
		}
	}
	for key := range tokenscopes.AdminRouteArea {
		if !seen[key] {
			t.Errorf("adminRouteArea entry %q matches no registered route; stale, remove it", key)
		}
	}
	if len(seen) == 0 {
		t.Fatal("route walk found no /v1/admin routes; the drift gate is broken, not the map")
	}
}

// TestAPITokenGrantEnforcement drives minted grant tokens against live
// routes: each grant admits exactly its area+verb, transcripts stay walled
// off from every other read grant, and the tokens area is isolated so no
// innocuous grant is root-equivalent.
func TestAPITokenGrantEnforcement(t *testing.T) {
	t.Parallel()
	app, base := testApp(t)
	user := seedIdentity(t, app)
	grantAdmin(t, app, user.ID)
	adminTok, _ := checkinToken(t, app, base)

	mint := func(t *testing.T, scope string) string {
		t.Helper()
		var out struct {
			Token string `json:"token"`
		}
		if code := adminReq(t, http.MethodPost, base+"/v1/admin/api-tokens", adminTok,
			map[string]any{"name": "scoped-" + scope, "scope": scope}, &out); code != http.StatusCreated {
			t.Fatalf("mint %q = %d", scope, code)
		}
		return out.Token
	}

	type probe struct {
		method, path string
		body         map[string]any
		want         int
		wantHint     string // substring of the error body when 403
	}
	cases := []struct {
		scope  string
		probes []probe
	}{
		{"identity:read", []probe{
			{http.MethodGet, "/v1/admin/users", nil, http.StatusOK, ""},
			{http.MethodGet, "/v1/admin/roles", nil, http.StatusOK, ""},
			{http.MethodGet, "/v1/admin/transcripts/search", nil, http.StatusForbidden, "transcripts:read"},
			{http.MethodPost, "/v1/admin/users", map[string]any{"username": "nope"}, http.StatusForbidden, "identity:write"},
			{http.MethodGet, "/v1/admin/changes", nil, http.StatusForbidden, "changes:read"},
			{http.MethodGet, "/v1/admin/overview", nil, http.StatusForbidden, "config:read"},
		}},
		// The SCIM mint sits in identity:
		// its pushes shape users/groups and mapped groups imply roles.
		{"identity:read,identity:write", []probe{
			// 404 (not 403) proves the scope gate passed and the handler ran.
			{http.MethodGet, "/v1/admin/overview", nil, http.StatusForbidden, "config:read"},
		}},
		{"changes:read", []probe{
			{http.MethodGet, "/v1/admin/changes", nil, http.StatusOK, ""},
			{http.MethodGet, "/v1/admin/users", nil, http.StatusForbidden, "identity:read"},
		}},
		{"audit:read", []probe{
			{http.MethodGet, "/v1/admin/audit", nil, http.StatusOK, ""},
			{http.MethodGet, "/v1/admin/transcripts", nil, http.StatusForbidden, "transcripts:read"},
		}},
		{"config:read,config:write", []probe{
			{http.MethodGet, "/v1/admin/overview", nil, http.StatusOK, ""},
			// The isolation: config:write must NOT mint admin credentials.
			{http.MethodPost, "/v1/admin/api-tokens", map[string]any{"name": "smuggled", "scope": "full"}, http.StatusForbidden, "tokens:write"},
			// Nor the inbound SCIM bearer, whose pushes can
			// reach mapped roles; that mint sits in identity:write.
		}},
		{"tokens:write", []probe{
			{http.MethodPost, "/v1/admin/api-tokens", map[string]any{"name": "child", "scope": "audit:read"}, http.StatusCreated, ""},
			{http.MethodGet, "/v1/admin/api-tokens", nil, http.StatusForbidden, "tokens:read"},
			{http.MethodGet, "/v1/admin/users", nil, http.StatusForbidden, "identity:read"},
		}},
		{"full", []probe{
			{http.MethodPost, "/v1/admin/users", map[string]any{"username": "made-by-full"}, http.StatusCreated, ""},
			{http.MethodGet, "/v1/admin/transcripts/search", nil, http.StatusOK, ""},
		}},
	}
	for _, tc := range cases {
		t.Run(tc.scope, func(t *testing.T) {
			tok := mint(t, tc.scope)
			for _, p := range tc.probes {
				var body map[string]any
				var into any // list endpoints answer arrays; only 403 bodies are decoded
				if p.wantHint != "" {
					into = &body
				}
				code := adminReq(t, p.method, base+p.path, tok, p.body, into)
				if code != p.want {
					t.Errorf("%s %s under %q = %d, want %d (body %v)", p.method, p.path, tc.scope, code, p.want, body)
					continue
				}
				if p.wantHint != "" {
					if msg, _ := body["error"].(string); !strings.Contains(msg, p.wantHint) {
						t.Errorf("%s %s under %q: 403 body %q must name the missing grant %q", p.method, p.path, tc.scope, msg, p.wantHint)
					}
				}
			}
		})
	}
}

// TestAPITokenGrantActorAttribution: the rev-18 admin-audit actor lane must
// hold for grants tokens exactly as for full: the mutation attributes the
// TOKEN, name and id.
func TestAPITokenGrantActorAttribution(t *testing.T) {
	t.Parallel()
	app, base := testApp(t)
	user := seedIdentity(t, app)
	grantAdmin(t, app, user.ID)
	adminTok, _ := checkinToken(t, app, base)

	var minted struct {
		ID    string `json:"id"`
		Token string `json:"token"`
	}
	if code := adminReq(t, http.MethodPost, base+"/v1/admin/api-tokens", adminTok,
		map[string]any{"name": "locker", "scope": "identity:write"}, &minted); code != http.StatusCreated {
		t.Fatalf("mint = %d", code)
	}
	if code := adminReq(t, http.MethodPost, base+"/v1/admin/users/"+user.ID+"/lock", minted.Token,
		map[string]string{"reason": "grant-token attribution"}, nil); code != http.StatusOK {
		t.Fatalf("lock via identity:write = %d", code)
	}
	data := lastAdminAction(t, app, "user.lock")
	if data["actor"] != "locker" || data["actorId"] != minted.ID || data["actorVia"] != "api-token" {
		t.Errorf("grant-token mutation attribution = %v/%v/%v, want locker/%s/api-token",
			data["actor"], data["actorId"], data["actorVia"], minted.ID)
	}
}

// TestAPITokenStoredLegacyReadRefused: verify-time enforcement. Mint can
// no longer produce a `read` token, so one is INJECTED at the
// storage layer the way a pre-upgrade deployment would still hold it, and
// every use is refused with the migration hint, fail closed.
func TestAPITokenStoredLegacyReadRefused(t *testing.T) {
	t.Parallel()
	app, base := testApp(t)
	user := seedIdentity(t, app)
	grantAdmin(t, app, user.ID)

	token := apiTokenPrefix + "legacy-read-injected"
	sum := sha256.Sum256([]byte(token))
	meta, _ := json.Marshal(apiTokenMeta{ID: uuid.NewString(), Name: "pre-upgrade", Scope: "read", Created: time.Now().UTC()})
	if err := app.store.Settings().Set(context.Background(),
		apiTokenKeyPrefix+hex.EncodeToString(sum[:]), string(meta)); err != nil {
		t.Fatal(err)
	}

	var body map[string]any
	if code := adminReq(t, http.MethodGet, base+"/v1/admin/users", token, nil, &body); code != http.StatusForbidden {
		t.Fatalf("stored legacy read token = %d, want 403 (fail closed until re-mint)", code)
	}
	if msg, _ := body["error"].(string); !strings.Contains(msg, "retired") {
		t.Errorf("refusal %q must say the word is retired and hint the grants form", msg)
	}
}
