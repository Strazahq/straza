package agentguard

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/strazahq/straza/internal/policy"
)

// reacquireCheckin fakes /v1/checkin with independent behaviors per credential
// lane: refreshStatus answers the session-token refresh, deviceStatus the
// device-token lane. 0 means success (a fresh session s2/tok2 on the same
// snapshot id, so no snapshot adoption muddies the assertions).
type reacquireCheckin struct {
	refreshStatus int // 0 = 200 OK
	refreshMsg    string
	deviceStatus  int // 0 = 200 OK
	deviceMsg     string
	snapshotID    string
	deviceCalls   atomic.Int32
	// renewedToken, when set, rides the device-lane answer as device_token
	// (a renewal), the way a server judging the credential past half-life
	// answers.
	renewedToken string
}

func (h *reacquireCheckin) handler(t *testing.T) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/checkin" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		var body struct {
			SessionToken string `json:"session_token"`
			DeviceToken  string `json:"device_token"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		w.Header().Set("Content-Type", "application/json")
		switch {
		case body.SessionToken != "":
			if h.refreshStatus != 0 {
				w.WriteHeader(h.refreshStatus)
				_, _ = w.Write([]byte(`{"error":"` + h.refreshMsg + `"}`))
				return
			}
			_, _ = w.Write([]byte(`{"session_id":"s1","session_token":"tok-refreshed","expires_in":300,"roles":["dev"],"snapshot_id":"` + h.snapshotID + `"}`))
		case body.DeviceToken != "":
			h.deviceCalls.Add(1)
			if h.deviceStatus != 0 {
				w.WriteHeader(h.deviceStatus)
				_, _ = w.Write([]byte(`{"error":"` + h.deviceMsg + `"}`))
				return
			}
			renewal := ""
			if h.renewedToken != "" {
				renewal = `,"device_token":"` + h.renewedToken + `","device_token_expires_in":2592000`
			}
			_, _ = w.Write([]byte(`{"session_id":"s2","session_token":"tok2","expires_in":300,"roles":["dev"],"snapshot_id":"` + h.snapshotID + `"` + renewal + `}`))
		default:
			t.Errorf("checkin with neither session_token nor device_token")
			w.WriteHeader(http.StatusBadRequest)
		}
	}
}

// reacquireStore seeds a full client state dir: config, verified snapshot,
// a session whose token expired `expires` ago (negative = expired), and,
// when withIdentity is set, a device-token identity.
func reacquireStore(t *testing.T, serverURL string, keys map[string]string, signed []byte, snapID string, expires time.Duration, withIdentity bool) *Store {
	t.Helper()
	store := daemonStore(t)
	if err := store.SaveConfig(Config{ServerURL: serverURL, SnapshotKeys: keys}); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveSnapshot(signed); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveSession(Session{
		SessionID: "s-old", SessionToken: "tok-old", SnapshotID: snapID,
		User: "kim", Roles: []string{"dev"}, Harness: "claude-code/2.1.0",
		ExpiresAt: time.Now().Add(expires),
	}); err != nil {
		t.Fatal(err)
	}
	if withIdentity {
		if err := store.SaveIdentity(Identity{DeviceID: "d1", Username: "kim", DeviceToken: "dev-tok"}); err != nil {
			t.Fatal(err)
		}
	}
	return store
}

// decideAfterRefresh runs the exact hook-lane sequence liveDecider uses:
// build the PDP from state, RefreshIfStale under the 2 s bound, decide.
func decideAfterRefresh(t *testing.T, store *Store, ev policy.Event) policy.Decision {
	t.Helper()
	ses, err := store.LoadSession()
	if err != nil {
		t.Fatal(err)
	}
	pdp, err := NewLocalPDP(store, ses.Subject())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	pdp.RefreshIfStale(ctx, store)
	return pdp.Decide(Normalized{Event: ev})
}

// TestLocalPDPReacquire encodes the session-continuity contract: a
// 401-refused refresh falls back to the enrolled identity lane. Success continues enforcement seamlessly on a
// fresh session; failure denies with a reason that states what actually
// happened. "platform is unreachable" may appear ONLY on transport failures.
func TestLocalPDPReacquire(t *testing.T) {
	gitStatus := policy.Event{Kind: policy.EventToolPre, Tool: policy.ToolShellExec, Command: "git status"}
	priv, keys := testSnapshotKey(t)
	signed, snapID := testSignedPolicy(t, priv, offlinePolicyDoc)

	cases := []struct {
		name          string
		refreshStatus int
		refreshMsg    string
		deviceStatus  int
		deviceMsg     string
		withIdentity  bool
		expires       time.Duration
		wantAllow     bool
		wantContains  []string // deny-reason fragments (ignored when wantAllow)
		wantAbsent    []string // fragments that must NOT appear in the reason
		wantToken     string   // persisted session token afterwards ("" = don't check)
		wantDevCalls  int32
	}{
		{
			name:          "expired token, 401 refresh, device re-acquire succeeds => allow on fresh session",
			refreshStatus: http.StatusUnauthorized, refreshMsg: "session token rejected: re-enroll or restart the session",
			withIdentity: true, expires: -10 * time.Hour,
			wantAllow: true, wantToken: "tok2", wantDevCalls: 1,
		},
		{
			name:          "expired token, 401 refresh, no enrolled identity => honest renewal-failed deny",
			refreshStatus: http.StatusUnauthorized, refreshMsg: "session token rejected: re-enroll or restart the session",
			withIdentity: false, expires: -10 * time.Hour,
			wantContains: []string{"expired", "renewal failed", "Restart the session"},
			wantAbsent:   []string{"unreachable"},
		},
		{
			name:          "expired token, 401 refresh, device lane 403 (revoked) => terminal deny with the server's words",
			refreshStatus: http.StatusUnauthorized, refreshMsg: "session token rejected: re-enroll or restart the session",
			deviceStatus: http.StatusForbidden, deviceMsg: "Straza: this device or user has been revoked. Contact your administrator",
			withIdentity: true, expires: -10 * time.Hour,
			wantContains: []string{"refused", "revoked", "administrator"},
			wantAbsent:   []string{"unreachable", "restart the session"},
			wantDevCalls: 1,
		},
		{
			name:          "expired token, 401 refresh, device lane 401 (re-enroll needed) => terminal deny with the server's words",
			refreshStatus: http.StatusUnauthorized, refreshMsg: "session token rejected: re-enroll or restart the session",
			deviceStatus: http.StatusUnauthorized, deviceMsg: "device credential rejected. Run `straza enroll` again",
			withIdentity: true, expires: -10 * time.Hour,
			wantContains: []string{"refused", "straza enroll"},
			wantAbsent:   []string{"unreachable"},
			wantDevCalls: 1,
		},
		{
			name:          "expired token, 403 refresh (user disabled) => terminal deny, identity lane never tried",
			refreshStatus: http.StatusForbidden, refreshMsg: "user is disabled. Contact your administrator",
			withIdentity: true, expires: -10 * time.Hour,
			wantContains: []string{"refused", "disabled", "administrator"},
			wantAbsent:   []string{"unreachable"},
			wantDevCalls: 0,
		},
		{
			name:          "refresh succeeds => allow, token adopted, no identity lane",
			refreshStatus: 0, withIdentity: true, expires: 30 * time.Second,
			wantAllow: true, wantToken: "tok-refreshed", wantDevCalls: 0,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := &reacquireCheckin{
				refreshStatus: tc.refreshStatus, refreshMsg: tc.refreshMsg,
				deviceStatus: tc.deviceStatus, deviceMsg: tc.deviceMsg,
				snapshotID: snapID,
			}
			srv := httptest.NewServer(h.handler(t))
			defer srv.Close()
			store := reacquireStore(t, srv.URL, keys, signed, snapID, tc.expires, tc.withIdentity)

			d := decideAfterRefresh(t, store, gitStatus)

			if tc.wantAllow {
				if d.Effect != policy.EffectAllow {
					t.Fatalf("want allow, got %+v", d)
				}
			} else {
				if d.Effect != policy.EffectDeny {
					t.Fatalf("want deny, got %+v", d)
				}
				for _, frag := range tc.wantContains {
					if !strings.Contains(d.Reason, frag) {
						t.Errorf("reason %q missing %q", d.Reason, frag)
					}
				}
			}
			for _, frag := range tc.wantAbsent {
				if strings.Contains(d.Reason, frag) {
					t.Errorf("reason %q must not claim %q", d.Reason, frag)
				}
			}
			if tc.wantToken != "" {
				ses, err := store.LoadSession()
				if err != nil {
					t.Fatal(err)
				}
				if ses.SessionToken != tc.wantToken {
					t.Errorf("persisted token = %q, want %q", ses.SessionToken, tc.wantToken)
				}
				if tc.wantToken == "tok2" && ses.SessionID != "s2" {
					t.Errorf("persisted session id = %q, want the re-acquired s2", ses.SessionID)
				}
			}
			if got := h.deviceCalls.Load(); got != tc.wantDevCalls {
				t.Errorf("device-lane checkins = %d, want %d", got, tc.wantDevCalls)
			}
		})
	}
}

// TestLocalPDPReacquireTransport pins the honesty boundary from the other
// side: when the platform genuinely cannot be reached, the classic
// offline-grace wording (which claims unreachability) is CORRECT and must
// survive: the honest-reason rule removes a false claim, not the message.
func TestLocalPDPReacquireTransport(t *testing.T) {
	gitStatus := policy.Event{Kind: policy.EventToolPre, Tool: policy.ToolShellExec, Command: "git status"}
	priv, keys := testSnapshotKey(t)
	signed, snapID := testSignedPolicy(t, priv, offlinePolicyDoc)
	// 127.0.0.1:1 refuses connections: a pure transport failure.
	store := reacquireStore(t, "http://127.0.0.1:1", keys, signed, snapID, -10*time.Hour, true)

	d := decideAfterRefresh(t, store, gitStatus)
	if d.Effect != policy.EffectDeny {
		t.Fatalf("want offline deny, got %+v", d)
	}
	for _, frag := range []string{"expired", "grace", "unreachable", "doctor"} {
		if !strings.Contains(d.Reason, frag) {
			t.Errorf("offline reason %q missing %q", d.Reason, frag)
		}
	}
}

// TestLocalPDPReacquireWithinGrace: inside the profile's offline grace the
// cached snapshot still decides, per the rule that grace runs
// from token expiry regardless of renewal outcome: a failed renewal must not
// shrink the grace window.
func TestLocalPDPReacquireWithinGrace(t *testing.T) {
	gitStatus := policy.Event{Kind: policy.EventToolPre, Tool: policy.ToolShellExec, Command: "git status"}
	priv, keys := testSnapshotKey(t)
	signed, snapID := testSignedPolicy(t, priv, offlinePolicyDoc)
	h := &reacquireCheckin{refreshStatus: http.StatusUnauthorized, refreshMsg: "session token rejected", snapshotID: snapID}
	srv := httptest.NewServer(h.handler(t))
	defer srv.Close()
	// Expired 60 s ago with no identity to re-acquire through; grace must carry.
	store := reacquireStore(t, srv.URL, keys, signed, snapID, -60*time.Second, false)

	ses, err := store.LoadSession()
	if err != nil {
		t.Fatal(err)
	}
	pdp, err := NewLocalPDP(store, ses.Subject())
	if err != nil {
		t.Fatal(err)
	}
	pdp.maxAge = 900 // pin the standalone grace bound, independent of the helper snapshot's default
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	pdp.RefreshIfStale(ctx, store)
	if d := pdp.Decide(Normalized{Event: gitStatus}); d.Effect != policy.EffectAllow {
		t.Fatalf("within-grace decide = %+v, want the engine's allow", d)
	}
}
