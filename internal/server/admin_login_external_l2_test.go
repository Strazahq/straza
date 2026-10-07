package server

import (
	"context"
	"net/http"
	"testing"

	"github.com/strazahq/straza/internal/config"
	"github.com/strazahq/straza/internal/store"
)

// TestDisabledUserExternalIDTokenRefused pins the same refusal on the
// external identity provider's lane, the one an enterprise deployment
// signs people in with: a disabled user's ID token from the provider is
// refused on the admin API, a person route, enroll and check-in, each with
// 403, the sentence and exactly one login failure record that names the
// user, and the same token passes while the user is active.
func TestDisabledUserExternalIDTokenRefused(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	idpApp, idpBase := testApp(t)
	seedIdentity(t, idpApp)
	platform, platformBase := testApp(t, func(cfg *config.Config) {
		cfg.OIDC.Issuer = idpBase
		cfg.OIDC.ClientID = "straza"
		cfg.OIDC.JITProvision = true
	})
	idToken := loginDeviceFlow(t, idpBase, "kim", "hunter2!")
	checkin := map[string]any{
		"id_token":    idToken,
		"harness":     map[string]string{"name": "console", "version": "1"},
		"attestation": map[string]any{"managed": false, "hashes": map[string]string{}},
	}
	if code, body := postJSON(t, platformBase+"/v1/checkin", checkin); code != http.StatusOK {
		t.Fatalf("first sign-in on the platform = %d %v", code, body)
	}
	kim, err := platform.store.Users().GetByUsername(ctx, "kim")
	if err != nil {
		t.Fatal(err)
	}
	grantAdmin(t, platform, kim.ID)
	if code, _, out := callJSON(t, http.MethodGet, platformBase+"/v1/admin/users", idToken, nil); code != http.StatusOK {
		t.Fatalf("control, kim's provider token while active = %d %v, want 200", code, out)
	}
	kim.Status = store.UserDisabled
	if _, err := platform.store.Users().Update(ctx, kim); err != nil {
		t.Fatal(err)
	}

	// failures counts every login failure record, so a lane that writes
	// one that does not name kim, or two, fails as well.
	failures := func() int {
		n := 0
		for _, d := range outboxDataFor(t, platform, "straza.audit.authn") {
			if d["action"] == "login" && d["outcome"] == "failure" {
				n++
			}
		}
		return n
	}
	lanes := []struct {
		name string
		fire func() (int, any)
	}{
		{"the admin API", func() (int, any) {
			code, _, out := callJSON(t, http.MethodGet, platformBase+"/v1/admin/users", idToken, nil)
			return code, out["error"]
		}},
		{"a person route", func() (int, any) {
			code, _, out := callJSON(t, http.MethodGet, platformBase+"/v1/self", idToken, nil)
			return code, out["error"]
		}},
		{"enroll", func() (int, any) {
			code, out := postJSON(t, platformBase+"/v1/enroll", map[string]any{"id_token": idToken, "client_kind": "human"})
			return code, out["error"]
		}},
		{"check-in", func() (int, any) {
			code, out := postJSON(t, platformBase+"/v1/checkin", checkin)
			return code, out["error"]
		}},
	}
	for _, l := range lanes {
		before, beforeAll := len(authnFailures(t, platform, kim.ID, "user is disabled")), failures()
		code, msg := l.fire()
		if want := "user is disabled. Contact your administrator"; code != http.StatusForbidden || msg != want {
			t.Errorf("%s: answer = %d %v, want 403 %q", l.name, code, msg, want)
		}
		recs := authnFailures(t, platform, kim.ID, "user is disabled")
		if len(recs) != before+1 || failures() != beforeAll+1 {
			t.Errorf("%s: login failure records naming kim = %d, all = %d, want exactly one more than %d and %d",
				l.name, len(recs), failures(), before, beforeAll)
			continue
		}
		if rec := recs[len(recs)-1]; rec["via"] != "id-token" || rec["user"] != "kim" {
			t.Errorf("%s: record = %v, want via id-token and user kim", l.name, rec)
		}
	}
}
