package ctl

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/strazahq/straza/internal/version"
)

// TestCheckinCarriesBuildStamp pins strazactl's own check-in body: the
// harness version is this build, and the client block names it the way the
// straza client does (spec/attestation v1beta1).
func TestCheckinCarriesBuildStamp(t *testing.T) {
	var got map[string]any
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/checkin", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&got)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"session_id":"ses-1","session_token":"stok-1"}`)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	c := NewClient(srv.URL)
	c.CredsPath = filepath.Join(t.TempDir(), "credentials.json")
	if _, err := c.checkin(context.Background(), map[string]any{"device_token": "dt"}); err != nil {
		t.Fatalf("checkin: %v", err)
	}
	harness, _ := got["harness"].(map[string]any)
	client, _ := got["client"].(map[string]any)
	if harness["version"] != version.Version || client["version"] != version.Version || client["commit"] != version.Commit {
		t.Fatalf("body = %v, want harness and client stamped with %q / %q", got, version.Version, version.Commit)
	}
}
