package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/strazahq/straza/internal/config"
	"github.com/strazahq/straza/internal/store"
)

// TestRequireFullAdmin pins who may manage the client assertion key: the role
// straza-admin or an admin API token with the scope full, and never through a
// session that a coding harness checked in. Every lesser credential is
// refused with a sentence that says what the route needs, and the guarded
// handler is never reached for it. The delegated rows hold every grant a
// role or a token can hold short of full, so no area opens this route.
func TestRequireFullAdmin(t *testing.T) {
	t.Parallel()
	everyGrant := []string{
		"identity:write", "sessions:write", "policy:write", "apps:write", "audit:read",
		"transcripts:read", "approvals:write", "config:write", "tokens:write", "changes:read", "scim:write",
	}
	app, base := testApp(t, func(cfg *config.Config) {
		cfg.Admin.RoleAreas = map[string][]string{"key-clerk": everyGrant}
	})
	ctx := context.Background()
	kim := seedIdentity(t, app)
	grantAdmin(t, app, kim.ID)
	kimLogin := loginDeviceFlow(t, base, "kim", "hunter2!")

	hash, err := testPasswordHash("hunter2!")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := app.store.Roles().Create(ctx, store.Role{Name: "key-clerk"}); err != nil {
		t.Fatal(err)
	}
	loginAs := func(name, role string) string {
		t.Helper()
		u, err := app.store.Users().Create(ctx, store.User{Username: name, Email: name + "@x.io", PasswordHash: hash})
		if err != nil {
			t.Fatal(err)
		}
		grantRole(t, app, u.ID, role)
		return loginDeviceFlow(t, base, name, "hunter2!")
	}
	mint := func(name, scope string) string {
		t.Helper()
		var minted struct {
			Token string `json:"token"`
		}
		if code := adminReq(t, "POST", base+"/v1/admin/api-tokens", kimLogin,
			map[string]any{"name": name, "scope": scope}, &minted); code != http.StatusCreated && code != http.StatusOK {
			t.Fatalf("mint %s = %d", name, code)
		}
		return minted.Token
	}
	session := func(harness string) string {
		t.Helper()
		tok, _ := checkinTokenAs(t, base, harness)
		return tok
	}

	const needsFull = "needs a full administrator"
	tests := []struct {
		name   string
		bearer string
		want   int
		says   string
	}{
		{"a full administrator's login", kimLogin, http.StatusNoContent, ""},
		{"a full administrator's console session", session("console"), http.StatusNoContent, ""},
		{"a full administrator's strazactl session", session("strazactl"), http.StatusNoContent, ""},
		{"an admin API token with the scope full", mint("root", "full"), http.StatusNoContent, ""},
		{"a full administrator's coding harness session", session("claude-code"), http.StatusForbidden, `coding harness "claude-code"`},
		{"an admin API token with every grant", mint("grants", strings.Join(everyGrant, ",")), http.StatusForbidden, needsFull},
		{"a delegated administrator with every area", loginAs("lee", "key-clerk"), http.StatusForbidden, needsFull},
		{"the global MCP administrator", loginAs("max", MCPAdminRole), http.StatusForbidden, needsFull},
		{"a user with an application role", loginAs("noa", "dev"), http.StatusForbidden, needsFull},
		{"no bearer", "", http.StatusUnauthorized, "missing bearer token"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			reached := false
			guarded := app.requireFullAdmin(func(w http.ResponseWriter, r *http.Request) {
				reached = true
				if act, ok := actorFrom(r.Context()); !ok || act.Name == "" {
					t.Error("the handler got no audit actor")
				}
				w.WriteHeader(http.StatusNoContent)
			})
			req := httptest.NewRequest("POST", base+"/v1/admin/signing-keys/client-assertion/rotate", nil)
			if tc.bearer != "" {
				req.Header.Set("Authorization", "Bearer "+tc.bearer)
			}
			rec := httptest.NewRecorder()
			guarded(rec, req)
			if rec.Code != tc.want {
				t.Fatalf("status = %d, want %d (%s)", rec.Code, tc.want, rec.Body.String())
			}
			if reached != (tc.want == http.StatusNoContent) {
				t.Errorf("handler reached = %v for status %d", reached, rec.Code)
			}
			if tc.says != "" {
				var out struct {
					Error string `json:"error"`
				}
				_ = json.Unmarshal(rec.Body.Bytes(), &out)
				if !strings.Contains(out.Error, tc.says) {
					t.Errorf("refusal %q does not say %q", out.Error, tc.says)
				}
			}

			// The two real routes sit behind the wrapper: a credential it
			// refuses gets the same status there, and one it passes reaches
			// the handlers, which answer 200 and 404 for an unknown kid.
			wantRotate, wantRetire := tc.want, tc.want
			if tc.want == http.StatusNoContent {
				wantRotate, wantRetire = http.StatusOK, http.StatusNotFound
			}
			if code := adminReq(t, "POST", base+"/v1/admin/signing-keys/client-assertion/rotate", tc.bearer, nil, nil); code != wantRotate {
				t.Errorf("the rotate route = %d, want %d", code, wantRotate)
			}
			if code := adminReq(t, "POST", base+"/v1/admin/signing-keys/client-assertion/no-such-kid/retire", tc.bearer, nil, nil); code != wantRetire {
				t.Errorf("the retire route = %d, want %d", code, wantRetire)
			}
		})
	}
}
