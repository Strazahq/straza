package server

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/strazahq/straza/internal/config"
	"github.com/strazahq/straza/internal/store"
)

// setPasswordDirect stores a bcrypt hash on the row straight in the store:
// the admin set-password route has its own suite, and these tests are about
// the door the hash opens.
func setPasswordDirect(t *testing.T, app *App, username, password string) store.User {
	t.Helper()
	ctx := context.Background()
	u, err := app.store.Users().GetByUsername(ctx, username)
	if err != nil {
		t.Fatal(err)
	}
	hash, err := testPasswordHash(password)
	if err != nil {
		t.Fatal(err)
	}
	u.PasswordHash = hash
	u, err = app.store.Users().Update(ctx, u)
	if err != nil {
		t.Fatal(err)
	}
	return u
}

// countIdentityAction counts the straza.identity.updated events in the
// outbox whose action matches.
func countIdentityAction(t *testing.T, app *App, action string) int {
	t.Helper()
	n := 0
	for _, d := range identityUpdatedEvents(t, app) {
		if d["action"] == action {
			n++
		}
	}
	return n
}

// submitEmergencyLogin posts the emergency page's form for a fresh code and
// returns the rendered page.
func submitEmergencyLogin(t *testing.T, base, username, password string) string {
	t.Helper()
	resp, err := http.PostForm(base+"/oidc/device_authorization", url.Values{"client_id": {"console"}})
	if err != nil {
		t.Fatal(err)
	}
	var auth map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&auth)
	_ = resp.Body.Close()
	resp, err = http.PostForm(base+"/oidc/device", url.Values{
		"user_code": {auth["user_code"].(string)}, "username": {username}, "password": {password},
	})
	if err != nil {
		t.Fatal(err)
	}
	page, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	return string(page)
}

// TestEnterpriseBreakGlassDoor pins the enterprise emergency sign-in: the
// break-glass admin signs in on the server's own page, the ID token opens
// an admin console session through checkin, the alarm and the login event
// land exactly once each, a wrong password and every other account are
// refused with a failure event that fabricates no identity, and the page
// says who it is for.
func TestEnterpriseBreakGlassDoor(t *testing.T) {
	t.Parallel()
	app, base := testApp(t, func(cfg *config.Config) {
		cfg.Profile = config.ProfileEnterprise
	})
	bg := setPasswordDirect(t, app, BreakGlassUsername, "vaulted-secret")
	alice, err := app.store.Users().Create(context.Background(), store.User{Username: "alice", Email: "alice@x.io", Origin: "scim"})
	if err != nil {
		t.Fatal(err)
	}
	setPasswordDirect(t, app, alice.Username, "alice-secret")

	t.Run("the page names its account", func(t *testing.T) {
		resp, err := http.Get(base + "/oidc/device")
		if err != nil {
			t.Fatal(err)
		}
		page, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusOK || !strings.Contains(string(page), "Emergency sign-in to Straza") ||
			!strings.Contains(string(page), "signs in the emergency admin break-glass and nobody else") {
			t.Fatalf("emergency page = %d: %s", resp.StatusCode, page)
		}
	})

	t.Run("break-glass signs in and reaches the console", func(t *testing.T) {
		idToken := loginDeviceFlow(t, base, BreakGlassUsername, "vaulted-secret")
		code, checkin := postJSON(t, base+"/v1/checkin", map[string]any{
			"id_token":    idToken,
			"harness":     map[string]string{"name": "console", "version": "1"},
			"attestation": map[string]any{"managed": false, "hashes": map[string]string{}},
		})
		if code != http.StatusOK {
			t.Fatalf("checkin = %d %v", code, checkin)
		}
		if grants, _ := checkin["admin_grants"].(string); grants == "" {
			t.Fatalf("break-glass checkin carries no admin grants: %v", checkin)
		}
		success := waitAuthn(t, app, "break-glass login success", func(d map[string]any) bool {
			return d["action"] == "login" && d["outcome"] == "success" && d["userId"] == bg.ID
		})
		if success["via"] != "id-token" || success["user"] != BreakGlassUsername {
			t.Errorf("success payload = %v", success)
		}
		deadline := time.Now().Add(10 * time.Second)
		for countIdentityAction(t, app, "breakglass.login") == 0 && time.Now().Before(deadline) {
			time.Sleep(30 * time.Millisecond)
		}
		if n := countIdentityAction(t, app, "breakglass.login"); n != 1 {
			t.Errorf("breakglass.login alarms = %d, want exactly 1", n)
		}
		if alarm := lastIdentityAction(t, app, "breakglass.login"); alarm["id"] != bg.ID {
			t.Errorf("alarm names %v, want the break-glass row %s", alarm["id"], bg.ID)
		}
		if n := countAuthn(t, app, func(d map[string]any) bool {
			return d["outcome"] == "success" && d["userId"] == bg.ID
		}); n != 1 {
			t.Errorf("break-glass success events = %d, want exactly 1", n)
		}
	})

	t.Run("a wrong password is refused with the account named", func(t *testing.T) {
		page := submitEmergencyLogin(t, base, BreakGlassUsername, "guess")
		if !strings.Contains(page, "Invalid username or password.") {
			t.Fatalf("wrong password page: %s", page)
		}
		failure := waitAuthn(t, app, "break-glass password failure", func(d map[string]any) bool {
			return d["outcome"] == "failure" && d["via"] == "password" && d["user"] == BreakGlassUsername
		})
		if r, _ := failure["reason"].(string); r == "" {
			t.Errorf("password failure carries no reason: %v", failure)
		}
	})

	t.Run("a SCIM-born user with a stored hash is refused", func(t *testing.T) {
		page := submitEmergencyLogin(t, base, alice.Username, "alice-secret")
		if !strings.Contains(page, "This page signs in the account break-glass only. Everyone else signs in at your identity provider.") {
			t.Fatalf("alice on the emergency page: %s", page)
		}
		if n := countAuthn(t, app, func(d map[string]any) bool {
			return d["outcome"] == "success" && d["userId"] == alice.ID
		}); n != 0 {
			t.Fatalf("alice signed in on the emergency page")
		}
		failure := waitAuthn(t, app, "refused username failure", func(d map[string]any) bool {
			_, named := d["user"]
			return d["outcome"] == "failure" && d["via"] == "password" && !named
		})
		if _, present := failure["userId"]; present {
			t.Errorf("refused username fabricated an identity: %v", failure)
		}
	})
}

// TestEnterpriseEmergencyLoginThrottled pins the per-IP throttle on the
// enterprise password submit: the second rapid submit answers 429 while the
// page itself stays free.
func TestEnterpriseEmergencyLoginThrottled(t *testing.T) {
	t.Parallel()
	_, base := testApp(t, func(cfg *config.Config) {
		cfg.Profile = config.ProfileEnterprise
		cfg.Server.LoginPerIPRPS = 1
	})
	form := url.Values{"user_code": {"XXXX-XXXX"}, "username": {BreakGlassUsername}, "password": {"guess"}}
	for i, want := range []int{http.StatusOK, http.StatusTooManyRequests} {
		resp, err := http.PostForm(base+"/oidc/device", form)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != want {
			t.Fatalf("submit %d = %d, want %d", i+1, resp.StatusCode, want)
		}
	}
	resp, err := http.Get(base + "/oidc/device")
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("emergency page throttled: %d", resp.StatusCode)
	}
}
