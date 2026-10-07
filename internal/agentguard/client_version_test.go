package agentguard

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/strazahq/straza/internal/version"
)

// TestCheckinCarriesClientVersion pins the additive client stamp on every
// check-in shape: the body names this build's version and commit.
func TestCheckinCarriesClientVersion(t *testing.T) {
	var got map[string]any
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/checkin" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_ = json.NewDecoder(r.Body).Decode(&got)
		_, _ = w.Write([]byte(`{"session_id":"s1","session_token":"tok","expires_in":300,"attestation":"none","user":"u","roles":[],"snapshot_id":"snap"}`))
	}))
	defer ts.Close()
	c := NewClient(ts.URL)
	ctx := context.Background()
	calls := map[string]func() error{
		"id token": func() error {
			_, err := c.Checkin(ctx, "idt", "", "claude-code", "2.1.0", Attestation{})
			return err
		},
		"device token": func() error {
			_, err := c.CheckinDevice(ctx, "dt", "claude-code", "2.1.0", Attestation{})
			return err
		},
		"refresh": func() error {
			_, err := c.Refresh(ctx, "st", "claude-code", "2.1.0", Attestation{})
			return err
		},
	}
	for name, call := range calls {
		t.Run(name, func(t *testing.T) {
			got = nil
			if err := call(); err != nil {
				t.Fatalf("%s: %v", name, err)
			}
			client, _ := got["client"].(map[string]any)
			if client["version"] != version.Version || client["commit"] != version.Commit {
				t.Fatalf("client block = %v, want version %q commit %q", got["client"], version.Version, version.Commit)
			}
		})
	}
}
