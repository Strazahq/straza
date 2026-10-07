package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/strazahq/straza/internal/secrets"
)

// TestOAuthClientFollowsNoRedirect pins that the people's OAuth exchange
// posts the client secret to the token endpoint it was given and nowhere
// else: a token endpoint that answers 307 gets no second post. The plain
// client is the positive control that shows the second post carries the
// secret.
func TestOAuthClientFollowsNoRedirect(t *testing.T) {
	t.Parallel()
	var forwarded atomic.Int32
	var leaked atomic.Bool
	mux := http.NewServeMux()
	mux.HandleFunc("POST /token", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/elsewhere", http.StatusTemporaryRedirect)
	})
	mux.HandleFunc("POST /elsewhere", func(w http.ResponseWriter, r *http.Request) {
		forwarded.Add(1)
		_ = r.ParseForm()
		if r.PostForm.Get("client_secret") != "" {
			leaked.Store(true)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"access_token":"gho_forwarded"}`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	provider := secrets.ProviderConfig{Name: "test", ClientID: "cid", ClientSecret: "csec", TokenURL: srv.URL + "/token"}

	for _, tc := range []struct {
		name          string
		client        *http.Client
		wantForwarded int32
		wantLeaked    bool
		wantErr       string
	}{
		{"a plain client, the positive control", &http.Client{}, 1, true, ""},
		{"the OAuth client", newOAuthHTTPClient(), 0, false, "HTTP 307"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			forwarded.Store(0)
			leaked.Store(false)
			_, _, err := secrets.ExchangeCode(context.Background(), tc.client, provider, "code", "https://straza.test/cb")
			if tc.wantErr == "" && err != nil {
				t.Fatalf("exchange: %v", err)
			}
			if tc.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tc.wantErr)) {
				t.Fatalf("exchange error = %v, want it to name %q", err, tc.wantErr)
			}
			if forwarded.Load() != tc.wantForwarded || leaked.Load() != tc.wantLeaked {
				t.Fatalf("redirect target: posts = %d, secret seen = %v, want %d and %v",
					forwarded.Load(), leaked.Load(), tc.wantForwarded, tc.wantLeaked)
			}
		})
	}
}
