package agentguard

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// The pin wedge: a writer of session.json that pins a SnapshotID decoupled
// from the blob actually on disk makes hooks (which verify blob-against-pin
// in NewLocalPDP) fail closed on every decision and stay wedged across
// restarts. These tests pin the one invariant that prevents it:
// session.SnapshotID is only ever persisted equal to the VERIFIED content id
// of the snapshot blob on disk at write time (the pin follows the blob).

const pinPolicyA = `
apiVersion: straza.dev/v1beta1
kind: PolicySet
metadata: {name: pin-a}
spec:
  rules:
    - {id: ra, tools: [shell.exec], command: {denyPatterns: ["a-cmd *"]}, effect: deny, reason: "a"}
`

const pinPolicyB = `
apiVersion: straza.dev/v1beta1
kind: PolicySet
metadata: {name: pin-b}
spec:
  rules:
    - {id: rb, tools: [shell.exec], command: {denyPatterns: ["b-cmd *"]}, effect: deny, reason: "b"}
`

const pinPolicyX = `
apiVersion: straza.dev/v1beta1
kind: PolicySet
metadata: {name: pin-x}
spec:
  rules:
    - {id: rx, tools: [shell.exec], command: {denyPatterns: ["x-cmd *"]}, effect: deny, reason: "x"}
`

// pinStore builds an enrolled store (device token + config with pinned keys)
// pointed at srvURL, the shape SessionStart and ensureSession both check in
// against.
func pinStore(t *testing.T, srvURL string, keys map[string]string) *Store {
	t.Helper()
	t.Setenv("STRAZA_HOME", t.TempDir())
	store, err := OpenStore()
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SaveConfig(Config{ServerURL: srvURL, SnapshotKeys: keys}); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveIdentity(Identity{DeviceToken: "dev-token", DeviceID: "d1", Username: "u1"}); err != nil {
		t.Fatal(err)
	}
	return store
}

// TestSessionStartHealsSnapshotWedge: a client wedged with blob A on disk but
// a session pinning B heals on the next session.start. Advertising the old
// PIN (B) in If-None-Match would get a 304 and keep blob A while re-pinning
// B, bricking the client whenever the server's active id equals the bad pin.
// The conditional fetch advertises the VERIFIED disk id (A), so a divergent
// server id forces a full download and the pin lands on the blob we now hold.
func TestSessionStartHealsSnapshotWedge(t *testing.T) {
	priv, keys := testSnapshotKey(t)
	blobA, idA := testSignedPolicy(t, priv, pinPolicyA)
	blobB, idB := testSignedPolicy(t, priv, pinPolicyB)
	if idA == idB {
		t.Fatal("test setup: A and B must be different snapshots to form the wedge")
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/checkin":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"session_id":"s1","session_token":"tok2","expires_in":300,` +
				`"user":"u1","roles":["dev"],"snapshot_id":"` + idB + `"}`))
		case "/v1/snapshot":
			// Serve blob B, honoring If-None-Match: a 304 only when the client
			// truly already holds B.
			if strings.Trim(r.Header.Get("If-None-Match"), `"`) == idB {
				w.WriteHeader(http.StatusNotModified)
				return
			}
			w.Header().Set("X-Straza-Snapshot-Id", idB)
			_, _ = w.Write(blobB)
		case "/v1/audit/batch":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"accepted":0}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	store := pinStore(t, srv.URL, keys)
	if err := store.SaveSnapshot(blobA); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveSession(Session{
		SessionID: "s0", SessionToken: "tok", SnapshotID: idB, // pin B, hold A: the wedge
		User: "u1", Roles: []string{"dev"}, Harness: "claude-code/2.1.0",
		IssuedAt: time.Now(), ExpiresAt: time.Now().Add(time.Hour),
	}); err != nil {
		t.Fatal(err)
	}

	// Precondition: the wedge really does brick the hook lane.
	preSes, _ := store.LoadSession()
	if _, err := NewLocalPDP(store, preSes.Subject()); err == nil {
		t.Fatal("precondition: blob A vs pin B must brick NewLocalPDP")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	_, info, err := SessionStart(ctx, store, "claude-code", "2.1.0")
	if err != nil {
		t.Fatalf("SessionStart: %v", err)
	}
	if info.SnapshotID != idB {
		t.Errorf("info.SnapshotID = %q, want %q (the id we enforce with)", info.SnapshotID, idB)
	}

	ses, err := store.LoadSession()
	if err != nil {
		t.Fatal(err)
	}
	if ses.SnapshotID != idB {
		t.Errorf("session pin = %q, want %q (healed to the blob on disk)", ses.SnapshotID, idB)
	}
	cfg, _ := store.LoadConfig()
	if diskID, ok := diskSnapshotID(store, cfg); !ok || diskID != idB {
		t.Errorf("disk blob id = %q ok=%v, want %q (blob B downloaded)", diskID, ok, idB)
	}
	if _, err := NewLocalPDP(store, ses.Subject()); err != nil {
		t.Fatalf("hook PDP still bricked after the heal: %v", err)
	}
}

// TestSessionStartPinsSavedBlobNotCheckinID: activation skew, where the
// checkin reports snapshot id X while /v1/snapshot serves Y. The pin must
// equal Y (the blob we actually saved and can verify), never the checkin's
// X: pinning X over a blob whose id is Y bricks the very first decision.
func TestSessionStartPinsSavedBlobNotCheckinID(t *testing.T) {
	priv, keys := testSnapshotKey(t)
	blobY, idY := testSignedPolicy(t, priv, pinPolicyA)
	_, idX := testSignedPolicy(t, priv, pinPolicyX) // a different, skewed id the checkin reports

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/checkin":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"session_id":"s1","session_token":"tok2","expires_in":300,` +
				`"user":"u1","roles":["dev"],"snapshot_id":"` + idX + `"}`))
		case "/v1/snapshot":
			w.Header().Set("X-Straza-Snapshot-Id", idY)
			_, _ = w.Write(blobY)
		case "/v1/audit/batch":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"accepted":0}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	store := pinStore(t, srv.URL, keys) // fresh: empty snapshot cache

	if idX == idY {
		t.Fatal("test setup: X and Y must differ to exercise the skew")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	_, info, err := SessionStart(ctx, store, "claude-code", "2.1.0")
	if err != nil {
		t.Fatalf("SessionStart: %v", err)
	}
	if info.SnapshotID != idY {
		t.Errorf("info.SnapshotID = %q, want the saved blob id %q (not checkin id %q)", info.SnapshotID, idY, idX)
	}

	ses, err := store.LoadSession()
	if err != nil {
		t.Fatal(err)
	}
	if ses.SnapshotID != idY {
		t.Errorf("session pin = %q, want the saved blob id %q (not checkin id %q)", ses.SnapshotID, idY, idX)
	}
	cfg, _ := store.LoadConfig()
	if diskID, ok := diskSnapshotID(store, cfg); !ok || diskID != idY {
		t.Errorf("disk blob id = %q ok=%v, want %q", diskID, ok, idY)
	}
	if _, err := NewLocalPDP(store, ses.Subject()); err != nil {
		t.Fatalf("hook PDP bricked by the pin/blob skew: %v", err)
	}
}

// TestEnsureSessionAdoptsSnapshot: the MCP proxy mints a session from a bare
// state dir and a hook then reads it. A mint that pins resp.SnapshotID but
// NEVER fetches the blob leaves the hook a pin with no blob, and it denies
// everything. The mint adopts the blob, so the pin names a snapshot on disk.
func TestEnsureSessionAdoptsSnapshot(t *testing.T) {
	priv, keys := testSnapshotKey(t)
	blobY, idY := testSignedPolicy(t, priv, pinPolicyA)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/checkin":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"session_id":"s-mcp","session_token":"tok","expires_in":300,` +
				`"user":"u1","roles":["dev"],"snapshot_id":"` + idY + `"}`))
		case "/v1/snapshot":
			w.Header().Set("X-Straza-Snapshot-Id", idY)
			_, _ = w.Write(blobY)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	store := pinStore(t, srv.URL, keys) // no session, no snapshot on disk

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	ses, err := ensureSession(ctx, store, NewClient(srv.URL), "claude-code")
	if err != nil {
		t.Fatalf("ensureSession: %v", err)
	}
	if ses.SnapshotID != idY {
		t.Errorf("minted session pin = %q, want %q", ses.SnapshotID, idY)
	}
	if _, err := store.LoadSnapshot(); err != nil {
		t.Fatalf("mint left no blob on disk: %v", err)
	}
	cfg, _ := store.LoadConfig()
	if diskID, ok := diskSnapshotID(store, cfg); !ok || diskID != idY {
		t.Errorf("disk blob id = %q ok=%v, want %q", diskID, ok, idY)
	}
	// The hook that reads this freshly minted session must not brick.
	if _, err := NewLocalPDP(store, ses.Subject()); err != nil {
		t.Fatalf("hook PDP bricked reading the minted session: %v", err)
	}
}

// TestEnsureSessionKeepsDiskSnapshotOnFetchFailure: the proxy fronts the gateway
// PEP and needs no local blob to serve calls, so a snapshot fetch failure must
// NOT fail the mint. With a valid blob A already on disk and the snapshot
// endpoint down, the mint succeeds, keeps A as the pin (stale-but-working), and
// the hook lane keeps working off A.
func TestEnsureSessionKeepsDiskSnapshotOnFetchFailure(t *testing.T) {
	priv, keys := testSnapshotKey(t)
	blobA, idA := testSignedPolicy(t, priv, pinPolicyA)
	_, idB := testSignedPolicy(t, priv, pinPolicyB)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/checkin":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"session_id":"s-mcp","session_token":"tok","expires_in":300,` +
				`"user":"u1","roles":["dev"],"snapshot_id":"` + idB + `"}`))
		case "/v1/snapshot":
			w.WriteHeader(http.StatusInternalServerError)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	store := pinStore(t, srv.URL, keys)
	if err := store.SaveSnapshot(blobA); err != nil { // valid blob A on disk, no session
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	ses, err := ensureSession(ctx, store, NewClient(srv.URL), "claude-code")
	if err != nil {
		t.Fatalf("ensureSession must not fail when the snapshot fetch fails: %v", err)
	}
	if ses.SnapshotID != idA {
		t.Errorf("minted session pin = %q, want the stable disk id %q (not checkin id %q)", ses.SnapshotID, idA, idB)
	}
	if _, err := NewLocalPDP(store, ses.Subject()); err != nil {
		t.Fatalf("hook PDP bricked despite a valid blob A on disk: %v", err)
	}
}
