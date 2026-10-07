package agentguard

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// seedDaemonReacquire builds a daemon store with an enrolled device-token identity and
// an active session pointed at srv.
func seedDaemonReacquire(t *testing.T, serverURL string) *Store {
	t.Helper()
	store := daemonStore(t)
	if err := store.SaveConfig(Config{ServerURL: serverURL}); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveSession(Session{
		SessionID: "s-old", SessionToken: "tok-old", SnapshotID: "snap",
		Harness: "claude-code/2.1.0", ExpiresAt: time.Now().Add(time.Hour),
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveIdentity(Identity{DeviceID: "d1", Username: "kim", DeviceToken: "dev-tok"}); err != nil {
		t.Fatal(err)
	}
	return store
}

// TestDaemonReacquireOnRefusedRefresh pins parity with the hook lane: a 401-refused poll
// refresh with an enrolled identity re-acquires a fresh session instead of
// dropping state, so the daemonless hook lane and the daemon then tell one
// story. The identity kill lanes stay terminal (companion test below).
func TestDaemonReacquireOnRefusedRefresh(t *testing.T) {
	h := &reacquireCheckin{
		refreshStatus: http.StatusUnauthorized, refreshMsg: "session token rejected: re-enroll or restart the session",
		snapshotID: "snap",
	}
	srv := httptest.NewServer(h.handler(t))
	defer srv.Close()
	store := seedDaemonReacquire(t, srv.URL)

	d := NewDaemon(store, nil)
	if gone := d.refreshOnce(t); gone {
		t.Fatal("refused refresh with a live identity dropped the session, want re-acquire")
	}
	ses, err := store.LoadSession()
	if err != nil {
		t.Fatalf("session state gone after re-acquire: %v", err)
	}
	if ses.SessionID != "s2" || ses.SessionToken != "tok2" {
		t.Errorf("session = %s/%s, want the re-acquired s2/tok2", ses.SessionID, ses.SessionToken)
	}
	if got := h.deviceCalls.Load(); got != 1 {
		t.Errorf("device-lane checkins = %d, want 1", got)
	}
}

// TestDaemonReacquireTerminalRefusal: when the identity lane itself is
// refused (device revoked / user disabled), the daemon drops state exactly as
// before. Re-acquire must never soften the identity kill switch. On a
// headless AI agent's key lane, the issuer's invalid_client and
// unauthorized_client are that refusal. Its invalid_grant, its
// unsupported_grant_type and the grant limiter's 429 are not, and the daemon
// keeps the session and retries.
func TestDaemonReacquireTerminalRefusal(t *testing.T) {
	rows := []struct {
		name      string
		headless  bool
		device    int // the device check-in answers this status with deviceMsg
		deviceMsg string
		grant     int // the key lane's token request answers this status with body
		body      map[string]string
		wantGone  bool
	}{
		{name: "device lane, the device check-in is refused", device: http.StatusForbidden,
			deviceMsg: "Straza: this device or user has been revoked. Contact your administrator", wantGone: true},
		{name: "key lane, the issuer answers invalid_client", headless: true, grant: http.StatusBadRequest,
			body: map[string]string{"error": "invalid_client", "error_description": "client authentication failed"}, wantGone: true},
		{name: "key lane, the issuer answers unauthorized_client", headless: true, grant: http.StatusUnauthorized,
			body: map[string]string{"error": "unauthorized_client", "error_description": "client authentication failed"}, wantGone: true},
		{name: "key lane, the issuer answers invalid_grant", headless: true, grant: http.StatusBadRequest,
			body: map[string]string{"error": "invalid_grant", "error_description": "client assertion replayed: sign a fresh assertion per request"}},
		{name: "key lane, the grant limiter answers 429", headless: true, grant: http.StatusTooManyRequests,
			body: map[string]string{"error": "too many token requests. Retry shortly"}},
		{name: "key lane, the issuer answers unsupported_grant_type", headless: true, grant: http.StatusBadRequest,
			body: map[string]string{"error": "unsupported_grant_type", "error_description": "supported grants: device_code (interactive) and client_credentials (NHI headless)"}},
	}
	for _, row := range rows {
		t.Run(row.name, func(t *testing.T) {
			h := &reacquireCheckin{
				refreshStatus: http.StatusUnauthorized, refreshMsg: "session token rejected: re-enroll or restart the session",
				deviceStatus: row.device, deviceMsg: row.deviceMsg, snapshotID: "snap",
			}
			checkin := h.handler(t)
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/oidc/token" {
					checkin(w, r) // idp.json answers 404, so the key lane uses the server's own issuer
					return
				}
				raw, _ := json.Marshal(row.body)
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(row.grant)
				_, _ = w.Write(raw)
			}))
			defer srv.Close()
			store := seedDaemonReacquire(t, srv.URL)
			if row.headless {
				if err := Keygen(store, "u3-bot", false, io.Discard); err != nil {
					t.Fatal(err)
				}
				if err := store.SaveIdentity(Identity{Username: "u3-bot", Headless: HeadlessKey}); err != nil {
					t.Fatal(err)
				}
			}

			d := NewDaemon(store, nil)
			if gone := d.refreshOnce(t); gone != row.wantGone {
				t.Fatalf("the daemon reported the session gone %v, want %v", gone, row.wantGone)
			}
			_, serr := store.LoadSession()
			rev, rerr := store.LoadRevocation()
			switch {
			case row.wantGone && serr == nil:
				t.Error("session state survived a terminal refusal")
			case row.wantGone && (rerr != nil || rev.Reason == ""):
				t.Errorf("no revocation marker after terminal refusal (rev %+v, err %v)", rev, rerr)
			case !row.wantGone && serr != nil:
				t.Errorf("session state dropped after an answer that retrying can fix: %v", serr)
			case !row.wantGone && rerr == nil:
				t.Errorf("revocation marker %+v after an answer that retrying can fix", rev)
			}
		})
	}
}
