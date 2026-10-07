package ctl

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
)

// TestCheckinAdoptsRenewedDeviceToken pins the strazactl half of the device
// credential renewal: a check-in answer carrying device_token replaces the
// stored credential, and an answer without one (older servers, fresh
// credentials, the session-token refresh lane) keeps the stored credential
// as before.
func TestCheckinAdoptsRenewedDeviceToken(t *testing.T) {
	cases := []struct {
		name       string
		auth       map[string]any
		renewed    string // device_token in the answer, "" = none
		wantStored string
	}{
		{name: "device lane, renewal in the answer", auth: map[string]any{"device_token": "dev-old"}, renewed: "dev-new", wantStored: "dev-new"},
		{name: "device lane, no renewal", auth: map[string]any{"device_token": "dev-old"}, renewed: "", wantStored: "dev-old"},
		{name: "session refresh lane keeps the credential", auth: map[string]any{"session_token": "ses-old"}, renewed: "", wantStored: "dev-old"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/v1/checkin" {
					w.WriteHeader(http.StatusNotFound)
					return
				}
				answer := map[string]any{"session_id": "s2", "session_token": "ses-new", "expires_in": 300}
				if tc.renewed != "" {
					answer["device_token"] = tc.renewed
					answer["device_token_expires_in"] = 2592000
				}
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(answer)
			}))
			defer srv.Close()
			c := NewClient(srv.URL)
			c.CredsPath = filepath.Join(t.TempDir(), "credentials.json")
			if err := c.saveCreds(credentials{Server: srv.URL, SessionToken: "ses-old", SessionID: "s1", DeviceToken: "dev-old"}); err != nil {
				t.Fatal(err)
			}
			if _, err := c.checkin(context.Background(), tc.auth); err != nil {
				t.Fatalf("checkin: %v", err)
			}
			creds, err := c.loadCreds()
			if err != nil {
				t.Fatal(err)
			}
			if creds.DeviceToken != tc.wantStored {
				t.Errorf("stored device token = %q, want %q", creds.DeviceToken, tc.wantStored)
			}
			if creds.SessionToken != "ses-new" || creds.SessionID != "s2" {
				t.Errorf("session not adopted: %+v", creds)
			}
		})
	}
}
