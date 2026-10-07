package server

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"net/http"
	"net/url"
	"testing"

	"github.com/strazahq/straza/internal/clientassertion"
	"github.com/strazahq/straza/internal/config"
	"github.com/strazahq/straza/internal/store"
)

// selfRead fetches GET /v1/self as raw keys, so tests can assert field
// ABSENCE (admin_grants omitted) and not just zero values.
func selfRead(t *testing.T, base, bearer string) (int, map[string]any) {
	t.Helper()
	var out map[string]any
	code := adminReq(t, "GET", base+"/v1/self", bearer, nil, &out)
	return code, out
}

// TestSelfRead drives GET /v1/self (openapi 0.83.0): the one whoami read
// both SPAs consult after sign-in. It answers the caller's username, user
// kind, admin standing (checkin vocabulary, omitted when empty), and
// enrollment eligibility (always present, [] legal, browser before mobile),
// resolving roles ONCE through the same helpers the checkin response and the
// mint gate use, and the active users the caller sponsors (always present,
// sorted, [] legal).
func TestSelfRead(t *testing.T) {
	t.Parallel()
	app, base := testApp(t, func(cfg *config.Config) {
		cfg.Admin.RoleAreas = map[string][]string{
			"policy-editor": {"policy:read", "policy:write"},
		}
	})
	ctx := context.Background()
	kim := seedIdentity(t, app)
	grantAdmin(t, app, kim.ID)
	adminTok, _ := checkinToken(t, app, base)
	if _, err := app.store.Roles().Create(ctx, store.Role{Name: "policy-editor"}); err != nil {
		t.Fatal(err)
	}

	hash, err := testPasswordHash("hunter2!")
	if err != nil {
		t.Fatal(err)
	}
	mkHuman := func(name string, roles ...string) string {
		t.Helper()
		u, err := app.store.Users().Create(ctx, store.User{
			Username: name, Email: name + "@x.io", PasswordHash: hash,
		})
		if err != nil {
			t.Fatal(err)
		}
		for _, r := range roles {
			grantRole(t, app, u.ID, r)
		}
		return loginDeviceFlow(t, base, name, "hunter2!")
	}

	mkAgent := func(name, sponsor, status string) {
		t.Helper()
		if _, err := app.store.Users().Create(ctx, store.User{
			Username: name, UserType: store.UserTypeAgent, Sponsor: sponsor, Status: status,
		}); err != nil {
			t.Fatal(err)
		}
	}
	mkAgent("petra-zed-agent", "petra", store.UserActive)
	mkAgent("petra-ada-agent", "petra", store.UserActive)
	mkAgent("petra-old-agent", "petra", store.UserDisabled)

	cases := []struct {
		name          string
		tok           string
		wantUser      string
		wantKind      string
		wantGrants    string // "" = the key must be ABSENT
		wantChannels  []string
		wantSponsored []string
	}{
		{"root admin", loginDeviceFlow(t, base, "kim", "hunter2!"),
			"kim", "human", "full", []string{"browser", "mobile"}, []string{}},
		{"delegated admin", mkHuman("petra", "policy-editor"),
			"petra", "human", "policy:read,policy:write", []string{}, []string{"petra-ada-agent", "petra-zed-agent"}},
		{"enroll-role holder", mkHuman("mia", "straza-enroll-mobile"),
			"mia", "human", "", []string{"mobile"}, []string{}},
		{"role-less human", mkHuman("nora"),
			"nora", "human", "", []string{}, []string{}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			code, got := selfRead(t, base, tc.tok)
			if code != 200 {
				t.Fatalf("GET /v1/self = %d, want 200", code)
			}
			if got["username"] != tc.wantUser {
				t.Errorf("username = %v, want %q", got["username"], tc.wantUser)
			}
			if got["user_kind"] != tc.wantKind {
				t.Errorf("user_kind = %v, want %q", got["user_kind"], tc.wantKind)
			}
			grants, present := got["admin_grants"]
			if tc.wantGrants == "" && present {
				t.Errorf("admin_grants = %v, want the key absent", grants)
			}
			if tc.wantGrants != "" && grants != tc.wantGrants {
				t.Errorf("admin_grants = %v, want %q", grants, tc.wantGrants)
			}
			raw, present := got["enroll_channels"]
			if !present || raw == nil {
				t.Fatalf("enroll_channels missing or null, want an array ([] legal)")
			}
			channels, ok := raw.([]any)
			if !ok || len(channels) != len(tc.wantChannels) {
				t.Fatalf("enroll_channels = %v, want %v", raw, tc.wantChannels)
			}
			for i := range tc.wantChannels {
				if channels[i] != tc.wantChannels[i] {
					t.Fatalf("enroll_channels = %v, want %v (browser before mobile)", raw, tc.wantChannels)
				}
			}
			raw, present = got["sponsored"]
			if !present || raw == nil {
				t.Fatalf("sponsored missing or null, want an array ([] legal)")
			}
			sponsored, ok := raw.([]any)
			if !ok || len(sponsored) != len(tc.wantSponsored) {
				t.Fatalf("sponsored = %v, want %v", raw, tc.wantSponsored)
			}
			for i := range tc.wantSponsored {
				if sponsored[i] != tc.wantSponsored[i] {
					t.Fatalf("sponsored = %v, want %v (sorted, active only)", raw, tc.wantSponsored)
				}
			}
		})
	}

	// Both bearer kinds answer identically: mia's fresh SESSION token reads
	// the same self as her login token above.
	t.Run("session token bearer", func(t *testing.T) {
		miaLogin := loginDeviceFlow(t, base, "mia", "hunter2!")
		code, checkin := postJSON(t, base+"/v1/checkin", map[string]any{
			"id_token":    miaLogin,
			"harness":     map[string]string{"name": "console", "version": "1"},
			"attestation": map[string]any{"managed": false, "hashes": map[string]string{}},
		})
		if code != 200 {
			t.Fatalf("mia checkin = %d %v", code, checkin)
		}
		sesTok, _ := checkin["session_token"].(string)
		code, got := selfRead(t, base, sesTok)
		if code != 200 || got["username"] != "mia" || got["user_kind"] != "human" {
			t.Fatalf("session-token self = %d %v", code, got)
		}
	})

	// An NHI answers 200 (a description, not a gate): kind nhi, no channels.
	t.Run("nhi agent", func(t *testing.T) {
		var created struct {
			ID string `json:"id"`
		}
		if code := adminReq(t, "POST", base+"/v1/admin/users", adminTok,
			map[string]any{"username": "self-bot", "kind": "nhi"}, &created); code != http.StatusCreated {
			t.Fatalf("create nhi = %d", code)
		}
		pub, priv, err := ed25519.GenerateKey(rand.Reader)
		if err != nil {
			t.Fatal(err)
		}
		if code := adminReq(t, "PUT", base+"/v1/admin/users/"+created.ID+"/nhi-key", adminTok,
			map[string]any{"public_key": base64.StdEncoding.EncodeToString(pub)}, nil); code != http.StatusOK {
			t.Fatalf("register nhi key = %d", code)
		}
		assertion, err := clientassertion.Mint(priv, "self-bot", base, 0)
		if err != nil {
			t.Fatal(err)
		}
		resp, err := http.PostForm(base+"/oidc/token", url.Values{
			"grant_type":            {"client_credentials"},
			"client_id":             {"self-bot"},
			"client_assertion_type": {"urn:ietf:params:oauth:client-assertion-type:jwt-bearer"},
			"client_assertion":      {assertion},
		})
		if err != nil {
			t.Fatal(err)
		}
		var grantBody map[string]any
		if err := jsonNewDecoder(resp.Body).Decode(&grantBody); err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		idToken, _ := grantBody["id_token"].(string)
		code, got := selfRead(t, base, idToken)
		if code != 200 {
			t.Fatalf("nhi self read = %d, want 200", code)
		}
		if got["user_kind"] != "nhi" {
			t.Errorf("nhi user_kind = %v", got["user_kind"])
		}
		if raw, ok := got["enroll_channels"].([]any); !ok || len(raw) != 0 {
			t.Errorf("nhi enroll_channels = %v, want []", got["enroll_channels"])
		}
		if _, present := got["admin_grants"]; present {
			t.Errorf("nhi admin_grants present, want absent")
		}
	})

	// wat_ admin API tokens carry no user identity: refused, like every
	// requireIdentified surface.
	t.Run("wat token refused", func(t *testing.T) {
		var minted struct {
			Token string `json:"token"`
		}
		if code := adminReq(t, "POST", base+"/v1/admin/api-tokens", adminTok,
			map[string]any{"name": "self-probe", "scope": "changes:read"}, &minted); code != http.StatusCreated && code != http.StatusOK {
			t.Fatalf("api-token mint = %d", code)
		}
		if code, _ := selfRead(t, base, minted.Token); code != 401 {
			t.Fatalf("wat_ self read = %d, want 401", code)
		}
	})

	// No bearer: 401.
	if code, _ := selfRead(t, base, ""); code != 401 {
		t.Fatalf("unauthenticated self read = %d, want 401", code)
	}
}

// TestRootRedirect pins the door: GET / answers 302 to /approvals/ (302 not
// 301: the door choice must stay revisable without fighting browser caches).
func TestRootRedirect(t *testing.T) {
	t.Parallel()
	_, base := testApp(t)
	client := &http.Client{CheckRedirect: func(req *http.Request, via []*http.Request) error {
		return http.ErrUseLastResponse
	}}
	resp, err := client.Get(base + "/")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusFound {
		t.Fatalf("GET / = %d, want 302", resp.StatusCode)
	}
	if loc := resp.Header.Get("Location"); loc != "/self-service/" {
		t.Fatalf("GET / Location = %q, want /self-service/", loc)
	}
}
