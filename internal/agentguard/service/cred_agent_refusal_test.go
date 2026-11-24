package service

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/strazahq/straza/internal/agentguard"
	"github.com/strazahq/straza/internal/oidcflow"
)

// TestFetchAgentTokenRefusal pins the sentence of a refused agent sign-in:
// the issuer's code and description with the hint, or strazad's own
// sentence alone when the description carries the Straza prefix on the key
// lane, which only strazad answers.
func TestFetchAgentTokenRefusal(t *testing.T) {
	t.Parallel()
	_, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, lane, code, desc, want string
		judged                       bool
	}{
		{"an issuer's own description keeps the hint", laneKey, "invalid_client", "client authentication failed",
			"the identity provider refused the agent's sign-in (invalid_client): client authentication failed. " +
				"Check the registered key or client, and that the user is an active agent", true},
		{"a sentence of strazad is shown alone", laneKey, "invalid_client",
			"Straza: this agent is disabled on the server. Ask a Straza admin to enable it, then start the session again.",
			"this agent is disabled on the server. Ask a Straza admin to enable it, then start the session again.", true},
		{"the prefix inside a description is no mark", laneKey, "invalid_grant", "replayed, says Straza: twice",
			"the identity provider refused the agent's sign-in (invalid_grant): replayed, says Straza: twice. " +
				"Check the registered key or client, and that the user is an active agent", false},
		{"the prefix with nothing after it is no mark", laneKey, "invalid_client", "Straza: ",
			"the identity provider refused the agent's sign-in (invalid_client): Straza: . " +
				"Check the registered key or client, and that the user is an active agent", true},
		{"another issuer's prefix keeps the wrapper", laneSecret, "unauthorized_client", "Straza: all is well",
			"the identity provider refused the agent's sign-in (unauthorized_client): Straza: all is well. " +
				"Check the registered key or client, and that the user is an active agent", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusBadRequest)
				_, _ = w.Write([]byte(`{"error":"` + tc.code + `","error_description":"` + tc.desc + `"}`))
			}))
			defer srv.Close()
			flow := oidcflow.Flow{Issuer: srv.URL, TokenURL: srv.URL + "/oidc/token"}
			_, err := fetchAgentToken(context.Background(), agentguard.NewClient(srv.URL), flow, tc.lane, key, "off-bot", agentSecret{ClientID: "off-bot", ClientSecret: "s"})
			if err == nil || err.Error() != tc.want {
				t.Fatalf("error = %v, want %q", err, tc.want)
			}
			var status *agentguard.StatusError
			if errors.As(err, &status) != tc.judged {
				t.Errorf("counted as the issuer's judgement = %v, want %v", !tc.judged, tc.judged)
			}
		})
	}
}
