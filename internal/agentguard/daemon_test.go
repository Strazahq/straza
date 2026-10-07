package agentguard

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/strazahq/straza/internal/agentguard/spool"
	"github.com/strazahq/straza/internal/policy"
)

// refreshOnce runs one poll tick synchronously (the loop's body without the
// ticker), reporting whether the session ended.
func (d *Daemon) refreshOnce(t *testing.T) bool {
	t.Helper()
	cfg, err := d.store.LoadConfig()
	if err != nil {
		t.Fatal(err)
	}
	d.client = NewClient(cfg.ServerURL)
	return d.refresh(context.Background())
}

func daemonStore(t *testing.T) *Store {
	t.Helper()
	t.Setenv("STRAZA_HOME", t.TempDir())
	s, err := OpenStore()
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// TestDaemonPollRefused: with no push configured, the daemon poll-refreshes;
// a 401 (revoked/expired session) drops state and exits the daemon.
func TestDaemonPollRefused(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":"session is no longer active"}`))
	}))
	defer srv.Close()

	store := daemonStore(t)
	if err := store.SaveConfig(Config{ServerURL: srv.URL}); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveSession(Session{
		SessionID: "s1", SessionToken: "tok", Harness: "claude-code/2.1.0",
		ExpiresAt: time.Now().Add(time.Hour),
	}); err != nil {
		t.Fatal(err)
	}

	d := NewDaemon(store, nil)
	d.PollInterval = 50 * time.Millisecond
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- d.Run(ctx) }()

	deadline := time.Now().Add(3 * time.Second)
	for {
		if _, err := store.LoadSession(); err != nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("state not dropped after refused refresh")
		}
		time.Sleep(20 * time.Millisecond)
	}
	cancel()
	<-done
}

// TestDaemonAdoptsNewSnapshot pins mid-session policy propagation: when a
// refresh answers with a NEW snapshot id, the daemon must download and verify
// the new blob BEFORE advancing the session's id. Writing the id alone would
// fail the next hook's id-vs-blob check and deny every action until a session
// restart. When the snapshot fetch fails, the session keeps the OLD id:
// stale-but-working beats bricked, and the next tick retries.
func TestDaemonAdoptsNewSnapshot(t *testing.T) {
	priv, keys := testSnapshotKey(t)
	oldSigned, oldID := testSignedPolicy(t, priv, `
apiVersion: straza.dev/v1beta1
kind: PolicySet
metadata: {name: v1}
spec:
  rules:
    - {id: r1, tools: [shell.exec], command: {denyPatterns: ["old-cmd *"]}, effect: deny, reason: "old"}
`)
	newSigned, newID := testSignedPolicy(t, priv, `
apiVersion: straza.dev/v1beta1
kind: PolicySet
metadata: {name: v2}
spec:
  rules:
    - {id: r2, tools: [shell.exec], command: {denyPatterns: ["new-cmd *"]}, effect: deny, reason: "new"}
`)

	var snapshotBroken atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/checkin":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"session_id":"s1","session_token":"tok2","expires_in":300,"snapshot_id":"` + newID + `"}`))
		case "/v1/snapshot":
			if snapshotBroken.Load() {
				w.WriteHeader(http.StatusInternalServerError)
				return
			}
			w.Header().Set("X-Straza-Snapshot-Id", newID)
			_, _ = w.Write(newSigned)
		case "/v1/audit/batch":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"accepted":0}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	seed := func(t *testing.T) *Store {
		t.Helper()
		store := daemonStore(t)
		if err := store.SaveConfig(Config{ServerURL: srv.URL, SnapshotKeys: keys}); err != nil {
			t.Fatal(err)
		}
		if err := store.SaveSnapshot(oldSigned); err != nil {
			t.Fatal(err)
		}
		if err := store.SaveSession(Session{
			SessionID: "s1", SessionToken: "tok", SnapshotID: oldID,
			User: "u1", Roles: []string{"dev"}, Harness: "claude-code/2.1.0",
			ExpiresAt: time.Now().Add(time.Hour),
		}); err != nil {
			t.Fatal(err)
		}
		return store
	}

	t.Run("policy change adopted, hooks keep working", func(t *testing.T) {
		snapshotBroken.Store(false)
		store := seed(t)
		d := NewDaemon(store, nil)
		if gone := d.refreshOnce(t); gone {
			t.Fatal("session dropped by a healthy refresh")
		}
		ses, err := store.LoadSession()
		if err != nil || ses.SnapshotID != newID {
			t.Fatalf("session snapshot id = %q (err %v), want the new id", ses.SnapshotID, err)
		}
		// The id-vs-blob pair must stay consistent: a hook boots fine and
		// enforces the NEW policy.
		pdp, err := NewLocalPDP(store, ses.Subject())
		if err != nil {
			t.Fatalf("hook PDP bricked after policy update: %v", err)
		}
		dec := pdp.Decide(Normalized{HarnessName: "claude-code", HarnessVersion: "2.1.0",
			Event: policy.Event{Kind: policy.EventToolPre, Tool: policy.ToolShellExec, Command: "new-cmd /x"}})
		if dec.Effect != policy.EffectDeny || dec.RuleID != "r2" {
			t.Errorf("new policy not enforced: %+v", dec)
		}
	})

	t.Run("fetch failure keeps the old snapshot working", func(t *testing.T) {
		snapshotBroken.Store(true)
		store := seed(t)
		d := NewDaemon(store, nil)
		_ = d.refreshOnce(t)
		ses, err := store.LoadSession()
		if err != nil || ses.SnapshotID != oldID {
			t.Fatalf("session snapshot id = %q (err %v), want the OLD id kept (no brick)", ses.SnapshotID, err)
		}
		if _, err := NewLocalPDP(store, ses.Subject()); err != nil {
			t.Fatalf("hook PDP bricked by a failed snapshot fetch: %v", err)
		}
	})
}

// TestDaemonPollDrainsSpool: every poll tick must drain the audit spool,
// since the daemon's whole point is near-live audit plus the kill switch.
// Without the drain, hook decisions reach the audit log only at session end,
// and never if the terminal is killed.
func TestDaemonPollDrainsSpool(t *testing.T) {
	var mu sync.Mutex
	batches := 0
	received := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v1/checkin":
			_, _ = w.Write([]byte(`{"session_id":"s1","session_token":"tok2","expires_in":300,"snapshot_id":"snap"}`))
		case "/v1/audit/batch":
			var body struct {
				Events []json.RawMessage `json:"events"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			mu.Lock()
			batches++
			received += len(body.Events)
			mu.Unlock()
			_, _ = w.Write([]byte(`{"accepted":` + strconv.Itoa(len(body.Events)) + `}`))
		case "/v1/push":
			// The daemon legitimately probes the edge push lane. This fake is
			// an older server without /v1/push, so the lane 404s and parks.
			w.WriteHeader(http.StatusNotFound)
		case "/v1/snapshot":
			// No blob is on disk, so every tick asks for the snapshot the
			// session pins. This fake has none to serve.
			w.WriteHeader(http.StatusServiceUnavailable)
		default:
			t.Errorf("unexpected daemon request %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	store := daemonStore(t)
	if err := store.SaveConfig(Config{ServerURL: srv.URL}); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveSession(Session{
		SessionID: "s1", SessionToken: "tok", Harness: "claude-code/2.1.0",
		SnapshotID: "snap", // matches the mock checkin, so the pin never moves in this test
		ExpiresAt:  time.Now().Add(time.Hour),
	}); err != nil {
		t.Fatal(err)
	}
	// Two decisions wait in the spool, as if hooks ran without any drain.
	sp := spool.NewSpool(store.SpoolPath())
	for i := 0; i < 2; i++ {
		if err := spoolAppend(sp, Normalized{HarnessName: "claude-code"}, "s1", "snap-1",
			policy.Decision{Effect: policy.EffectDeny, Reason: "test"}); err != nil {
			t.Fatal(err)
		}
	}

	d := NewDaemon(store, nil)
	d.PollInterval = 50 * time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- d.Run(ctx) }()

	deadline := time.Now().Add(3 * time.Second)
	for {
		mu.Lock()
		got := received
		mu.Unlock()
		if got == 2 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("daemon ticks never drained the spool")
		}
		time.Sleep(20 * time.Millisecond)
	}
	// The spool is truncated after the successful upload; later ticks must
	// not re-send (no duplicate batches with events).
	pendDeadline := time.Now().Add(2 * time.Second)
	for {
		if n, err := sp.Pending(); err == nil && n == 0 {
			break
		}
		if time.Now().After(pendDeadline) {
			t.Fatal("spool not truncated after drain")
		}
		time.Sleep(20 * time.Millisecond)
	}
	time.Sleep(150 * time.Millisecond) // a few more ticks
	mu.Lock()
	finalReceived := received
	mu.Unlock()
	if finalReceived != 2 {
		t.Errorf("drained records = %d, want exactly 2 (no re-uploads)", finalReceived)
	}
	cancel()
	<-done
}

// TestDaemonPollKeepsSessionOnTransportError: an unreachable server is a
// network blip, not a revocation. The daemon must keep the session state
// and retry (parity with LocalPDP.RefreshIfStale and its offline grace).
func TestDaemonPollKeepsSessionOnTransportError(t *testing.T) {
	store := daemonStore(t)
	if err := store.SaveConfig(Config{ServerURL: "http://127.0.0.1:1"}); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveSession(Session{
		SessionID: "s1", SessionToken: "tok", Harness: "claude-code/2.1.0",
		ExpiresAt: time.Now().Add(time.Hour),
	}); err != nil {
		t.Fatal(err)
	}

	d := NewDaemon(store, nil)
	d.PollInterval = 50 * time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- d.Run(ctx) }()

	// Let several failing poll ticks elapse, then check the state survived.
	time.Sleep(400 * time.Millisecond)
	if _, err := store.LoadSession(); err != nil {
		t.Fatal("transport error dropped the session state: a blip must not kill a healthy session")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Error("daemon did not stop on ctx cancel")
	}
}
