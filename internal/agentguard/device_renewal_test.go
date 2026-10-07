package agentguard

import (
	"context"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

// TestCheckinWithIdentityAdoptsRenewedCredential pins the client half of
// device-token renewal: a device-lane check-in whose answer carries device_token rewrites
// identity.json with it, an answer without one (every server before renewal,
// every fresh credential) leaves the file byte-identical, and a write that
// fails keeps the check-in green on the still-valid old credential.
func TestCheckinWithIdentityAdoptsRenewedCredential(t *testing.T) {
	priv, keys := testSnapshotKey(t)
	signed, snapID := testSignedPolicy(t, priv, offlinePolicyDoc)

	cases := []struct {
		name         string
		renewedToken string
		readOnly     bool   // the state dir refuses writes
		wantStored   string // device token in identity.json afterwards
	}{
		{name: "renewed credential replaces the stored one", renewedToken: "dev-tok-2", wantStored: "dev-tok-2"},
		{name: "no renewal keeps identity.json byte-identical", renewedToken: "", wantStored: "dev-tok"},
		{name: "unwritable state keeps the check-in green on the old credential", renewedToken: "dev-tok-2", readOnly: true, wantStored: "dev-tok"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fake := &reacquireCheckin{snapshotID: snapID, renewedToken: tc.renewedToken}
			srv := httptest.NewServer(fake.handler(t))
			defer srv.Close()
			store := reacquireStore(t, srv.URL, keys, signed, snapID, time.Hour, true)
			idPath := filepath.Join(store.root, "state", "identity.json")
			before, err := os.ReadFile(idPath)
			if err != nil {
				t.Fatal(err)
			}
			if tc.readOnly && runtime.GOOS == "windows" {
				// An open reader prevents replacement on Windows; chmod does not.
				f, err := os.Open(idPath)
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = f.Close() })
			} else if tc.readOnly {
				dir := filepath.Dir(idPath)
				if err := os.Chmod(dir, 0o500); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
			}
			cfg, err := store.LoadConfig()
			if err != nil {
				t.Fatal(err)
			}
			id, err := store.LoadIdentity()
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			resp, err := checkinWithIdentity(ctx, NewClient(srv.URL), store, cfg, id, "claude-code", "2.1.0")
			if err != nil {
				t.Fatalf("checkinWithIdentity: %v", err)
			}
			if resp.SessionToken != "tok2" {
				t.Errorf("session token = %q, want tok2", resp.SessionToken)
			}
			if fake.deviceCalls.Load() != 1 {
				t.Errorf("device-lane calls = %d, want 1", fake.deviceCalls.Load())
			}
			after, err := os.ReadFile(idPath)
			if err != nil {
				t.Fatal(err)
			}
			stored, err := store.LoadIdentity()
			if err != nil {
				t.Fatal(err)
			}
			if stored.DeviceToken != tc.wantStored {
				t.Errorf("stored device token = %q, want %q", stored.DeviceToken, tc.wantStored)
			}
			if tc.renewedToken == "" && string(before) != string(after) {
				t.Errorf("identity.json changed on a no-renewal answer:\n%s\n%s", before, after)
			}
			if stored.DeviceID != "d1" || stored.Username != "kim" {
				t.Errorf("identity fields lost across adoption: %+v", stored)
			}
		})
	}
}
