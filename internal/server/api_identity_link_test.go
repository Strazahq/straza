package server

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/strazahq/straza/internal/config"
	"github.com/strazahq/straza/internal/store"
)

// TestLinkMismatchLoginRefused pins the refusal of an external ID token whose
// username names an account linked to another identity at the identity
// provider. Every lane that takes an ID token answers 401 with a sentence
// that names the account and says what to correct, writes one login failure
// record with the user and the reason, and leaves the account's externalId
// as it was.
func TestLinkMismatchLoginRefused(t *testing.T) {
	t.Parallel()
	idpApp, idpBase := testApp(t)
	seedIdentity(t, idpApp) // kim at the identity provider
	platform, base := testApp(t, func(cfg *config.Config) {
		cfg.OIDC.Issuer = idpBase
		cfg.OIDC.ClientID = "straza"
		cfg.OIDC.JITProvision = false
	})
	kim, err := platform.store.Users().Create(context.Background(), store.User{Username: "kim", ExternalID: "elsewhere"})
	if err != nil {
		t.Fatal(err)
	}
	idToken := loginDeviceFlow(t, idpBase, "kim", "hunter2!")

	call := func(method, path, bearer string, body any) (int, string) {
		var out any
		code := adminReq(t, method, base+path, bearer, body, &out)
		m, _ := out.(map[string]any)
		msg, _ := m["error"].(string)
		return code, msg
	}
	lanes := []struct {
		name string
		call func() (int, string)
	}{
		{"admin plane", func() (int, string) { return call("GET", "/v1/admin/apps", idToken, nil) }},
		{"person routes", func() (int, string) { return call("POST", "/v1/approvals/self/enroll-token", idToken, nil) }},
		{"enroll", func() (int, string) {
			return call("POST", "/v1/enroll", "", map[string]any{"id_token": idToken,
				"device": map[string]any{"name": "kim-laptop", "platform": "linux", "fingerprint": "fp-kim"}})
		}},
		{"session exchange", func() (int, string) {
			return call("POST", "/v1/checkin", "", map[string]any{
				"id_token":    idToken,
				"harness":     map[string]string{"name": "console", "version": "1"},
				"attestation": map[string]any{"managed": false, "hashes": map[string]string{}},
			})
		}},
	}
	want := "the account kim in Straza is linked to another identity at your identity provider, so this sign-in is refused. " +
		"Your identity manager must set the account's externalId to your id at the identity provider, which on Entra's global service is your object ID, " +
		"or clear it, so that your next sign-in links the account. Ask your administrator to correct it."
	for _, lane := range lanes {
		t.Run(lane.name, func(t *testing.T) {
			if code, msg := lane.call(); code != http.StatusUnauthorized || msg != want {
				t.Errorf("answer = %d %q, want 401 %q", code, msg, want)
			}
		})
	}

	mismatch := func(d map[string]any) bool {
		return d["action"] == "login" && d["outcome"] == "failure" &&
			d["reason"] == "the account is linked to another identity at the identity provider"
	}
	waitAuthn(t, platform, "link mismatch login failure", mismatch)
	deadline := time.Now().Add(10 * time.Second)
	for countAuthn(t, platform, mismatch) < len(lanes) && time.Now().Before(deadline) {
		time.Sleep(30 * time.Millisecond)
	}
	time.Sleep(200 * time.Millisecond)
	if n := countAuthn(t, platform, mismatch); n != len(lanes) {
		t.Errorf("link mismatch records = %d, want one per refused call, %d", n, len(lanes))
	}
	if n := countAuthn(t, platform, func(d map[string]any) bool {
		return mismatch(d) && (d["via"] != "id-token" || d["user"] != "kim" || d["userId"] != kim.ID)
	}); n != 0 {
		t.Errorf("link mismatch records without via id-token, user kim and userId %s = %d, want 0", kim.ID, n)
	}
	if n := countAuthn(t, platform, func(d map[string]any) bool { return mismatch(d) && d["harness"] == "console/1" }); n != 1 {
		t.Errorf("link mismatch records with the harness console/1 = %d, want 1, the session exchange's", n)
	}
	if n := countAuthn(t, platform, func(d map[string]any) bool { return d["reason"] == "login not accepted" }); n != 0 {
		t.Errorf("records that name no one = %d, want 0", n)
	}
	after, err := platform.store.Users().GetByID(context.Background(), kim.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.ExternalID != "elsewhere" {
		t.Errorf("externalId after the refusals = %q, want elsewhere", after.ExternalID)
	}
}
