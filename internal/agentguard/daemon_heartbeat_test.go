package agentguard

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// readHeartbeatFile reads and parses the store's heartbeat, reporting whether
// one is there. Parsed by hand on purpose: this pins the on-disk shape doctor
// (and any future reader) depends on.
func readHeartbeatFile(t *testing.T, store *Store) (daemonHeartbeat, bool) {
	t.Helper()
	raw, err := os.ReadFile(store.heartbeatPath())
	if err != nil {
		return daemonHeartbeat{}, false
	}
	var hb daemonHeartbeat
	if err := json.Unmarshal(raw, &hb); err != nil {
		t.Fatalf("heartbeat file is not JSON: %v (%s)", err, raw)
	}
	return hb, true
}

// TestDaemonHeartbeat: a running daemon must leave a liveness trace doctor can
// read: written when the loop starts, refreshed on the poll tick, and removed
// on a clean exit so doctor flips to "no live daemon" immediately instead of
// waiting out the staleness bound. The server is unreachable throughout: a
// heartbeat is about the DAEMON being alive, not the network being healthy.
func TestDaemonHeartbeat(t *testing.T) {
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
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- d.Run(ctx) }()

	// Written at loop start.
	deadline := time.Now().Add(3 * time.Second)
	var first daemonHeartbeat
	for {
		if hb, ok := readHeartbeatFile(t, store); ok {
			first = hb
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("daemon never wrote a heartbeat")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if first.PID != os.Getpid() {
		t.Errorf("heartbeat pid = %d, want this process (%d)", first.PID, os.Getpid())
	}
	if first.At.IsZero() {
		t.Error("heartbeat carries no timestamp")
	}
	if first.IntervalSeconds < 1 {
		t.Errorf("heartbeat interval = %ds, want >= 1 (sub-second poll intervals round up, never 0)", first.IntervalSeconds)
	}

	// Refreshed on the tick, even though every refresh call is failing.
	for {
		if hb, ok := readHeartbeatFile(t, store); ok && hb.At.After(first.At) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("heartbeat never refreshed on the poll tick")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if _, err := store.LoadSession(); err != nil {
		t.Fatal("session state lost while only the heartbeat should have changed")
	}

	// Removed on clean exit (pid-guarded; see clearHeartbeat).
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("daemon did not stop on ctx cancel")
	}
	if _, err := os.Stat(store.heartbeatPath()); !os.IsNotExist(err) {
		t.Errorf("heartbeat survived a clean exit (stat err %v): doctor would report a daemon that is gone as merely aging", err)
	}
}

// TestDaemonHeartbeatBestEffort pins the contract that makes the heartbeat
// safe to add at all: a failed write changes NOTHING about the daemon's real
// work and is logged once, not once per tick. Heartbeat trouble (a full disk,
// a permissions accident) must not flood the operator's terminal or take the
// revocation loop down with it.
func TestDaemonHeartbeatBestEffort(t *testing.T) {
	store := daemonStore(t)
	// A regular FILE where the state DIRECTORY belongs makes every heartbeat
	// write fail identically on all platforms.
	if err := os.MkdirAll(store.root, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(store.root, "state"), []byte("in the way"), 0o600); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	d := NewDaemon(store, &out)
	d.beat()
	d.beat()
	d.beat()
	if n := strings.Count(out.String(), "heartbeat"); n != 1 {
		t.Errorf("heartbeat failure logged %d times across 3 beats, want exactly once:\n%s", n, out.String())
	}
}

// TestDaemonHeartbeatClearIsPidGuarded: two daemons on one straza home are
// last-writer-wins on the heartbeat (same rule as the session state they both
// maintain), but an EXITING daemon must not erase the survivor's claim, only
// its own pid's file.
func TestDaemonHeartbeatClearIsPidGuarded(t *testing.T) {
	store := daemonStore(t)
	other := daemonHeartbeat{PID: os.Getpid() + 1, At: time.Now(), IntervalSeconds: 30}
	if err := store.writeJSON(store.heartbeatPath(), other); err != nil {
		t.Fatal(err)
	}
	NewDaemon(store, nil).clearHeartbeat()
	if _, ok := readHeartbeatFile(t, store); !ok {
		t.Fatal("clearHeartbeat removed another daemon's heartbeat")
	}

	mine := daemonHeartbeat{PID: os.Getpid(), At: time.Now(), IntervalSeconds: 30}
	if err := store.writeJSON(store.heartbeatPath(), mine); err != nil {
		t.Fatal(err)
	}
	NewDaemon(store, nil).clearHeartbeat()
	if _, ok := readHeartbeatFile(t, store); ok {
		t.Fatal("clearHeartbeat left this daemon's own heartbeat behind")
	}
}
