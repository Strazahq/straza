package server

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"net/http"
	"testing"

	"github.com/strazahq/straza/internal/config"
	"github.com/strazahq/straza/internal/store"
)

// TestDisabledNHIGrant pins the sign-in of a disabled agent: no token, a
// record that names the user and says why, and the word disabled only for a
// caller that proves the agent's registered key.
func TestDisabledNHIGrant(t *testing.T) {
	t.Parallel()
	app, base := testApp(t, func(cfg *config.Config) {
		cfg.Profile = config.ProfileEnterprise
	})
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	_, wrongPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	nhi := seedNHIWithKey(t, app, "off-bot", pub)
	nhi.Status = store.UserDisabled
	if _, err := app.store.Users().Update(context.Background(), nhi); err != nil {
		t.Fatal(err)
	}
	named := func(d map[string]any) bool {
		return d["outcome"] == "failure" && d["via"] == "client-assertion" && d["user"] == "off-bot"
	}

	for i, tc := range []struct {
		name     string
		key      ed25519.PrivateKey
		wantDesc string
	}{
		{"its registered key", priv,
			"Straza: this agent is disabled on the server. Ask a Straza admin to enable it, then start the session again."},
		{"another key", wrongPriv, "client authentication failed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			code, body := postGrantForm(t, base, assertionForm(t, tc.key, "off-bot", base))
			if code != http.StatusBadRequest || body["error"] != "invalid_client" {
				t.Fatalf("grant = %d %v, want 400 invalid_client", code, body)
			}
			if body["id_token"] != nil || body["access_token"] != nil {
				t.Fatalf("a disabled agent got a token: %v", body)
			}
			if got := body["error_description"]; got != tc.wantDesc {
				t.Errorf("error_description = %q, want %q", got, tc.wantDesc)
			}
			rec := waitAuthn(t, app, "the failure that names the disabled agent", func(d map[string]any) bool {
				return named(d) && countAuthn(t, app, named) == i+1
			})
			if rec["userId"] != nhi.ID || rec["reason"] != "user is disabled" {
				t.Errorf("record = %v, want userId %s and the reason user is disabled", rec, nhi.ID)
			}
			if n := countAuthn(t, app, func(d map[string]any) bool {
				return d["outcome"] == "failure" && d["via"] == "client-assertion" && d["user"] == nil
			}); n != 0 {
				t.Errorf("%d failure records name no user, want none", n)
			}
		})
	}
}
