package agentguard

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/strazahq/straza/internal/oidcflow"
)

// TestHeadlessTokenErrorsCarryNoPrefix pins the headless token errors
// without a "straza: " opening. The straza command prefixes every error
// once where it exits, so an inner prefix would print "straza: straza: ...".
func TestHeadlessTokenErrorsCarryNoPrefix(t *testing.T) {
	tests := []struct {
		name    string
		token   func(w http.ResponseWriter)
		withKey bool
		mode    string
		want    string
	}{
		{
			name: "issuer refuses the client",
			token: func(w http.ResponseWriter) {
				w.WriteHeader(http.StatusUnauthorized)
				_ = json.NewEncoder(w).Encode(map[string]string{
					"error": "invalid_client", "error_description": "no key is registered for this NHI"})
			},
			withKey: true, mode: HeadlessKey,
			want: "headless login refused (invalid_client): no key is registered for this NHI. Check the registered key/client and that the identity is an active NHI",
		},
		{
			name:  "no local key",
			token: func(w http.ResponseWriter) { w.WriteHeader(http.StatusInternalServerError) },
			mode:  HeadlessKey,
			want:  "no NHI key. Run `straza keygen` and register the public key: ",
		},
		{
			name:    "issuer answers without a token",
			token:   func(w http.ResponseWriter) { _ = json.NewEncoder(w).Encode(map[string]string{}) },
			withKey: true, mode: HeadlessKey,
			want: "identity provider returned no token",
		},
		{
			name:  "unknown mode",
			token: func(w http.ResponseWriter) { w.WriteHeader(http.StatusInternalServerError) },
			mode:  "carrier-pigeon",
			want:  "unknown headless mode \"carrier-pigeon\". Re-run `straza enroll --headless`",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("STRAZA_HOME", t.TempDir())
			store, err := OpenStore()
			if err != nil {
				t.Fatal(err)
			}
			if tt.withKey {
				if err := Keygen(store, "q069-bot", false, io.Discard); err != nil {
					t.Fatal(err)
				}
			}
			mux := http.NewServeMux()
			mux.HandleFunc("POST /token", func(w http.ResponseWriter, _ *http.Request) { tt.token(w) })
			srv := httptest.NewServer(mux)
			defer srv.Close()
			flow := oidcflow.Flow{Issuer: srv.URL, TokenURL: srv.URL + "/token"}

			_, err = fetchHeadlessToken(context.Background(), NewClient(srv.URL), store, flow, tt.mode, "q069-bot")
			if err == nil {
				t.Fatal("fetchHeadlessToken succeeded, want an error")
			}
			if strings.HasPrefix(err.Error(), "straza:") {
				t.Errorf("error carries the inner prefix the command adds again: %q", err)
			}
			if !strings.HasPrefix(err.Error(), tt.want) {
				t.Errorf("error = %q, want it to open with %q", err, tt.want)
			}
		})
	}
}
