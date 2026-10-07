package agentguard_test

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/strazahq/straza/internal/agentguard"
)

// TestEdgePushKillE2E is the edge push acceptance test: a REAL strazad with
// no client-reachable NATS, a REAL daemon subscribed over plain HTTP SSE,
// and the admin kill switch: state drops within the push budget, provably NOT via
// polling (the poll interval is an hour).
func TestEdgePushKillE2E(t *testing.T) {
	base, _ := bootStrazad(t)
	home := t.TempDir()
	t.Setenv("STRAZA_HOME", home)

	store, err := agentguard.OpenStore()
	if err != nil {
		t.Fatal(err)
	}
	driveEnroll(t, store, base)
	// SessionStart hook → checkin: the session the kill switch will target.
	env := []string{"STRAZA_HOME=" + home, "CLAUDECODE=1"}
	if _, code := hook(t, "claude-code", `{"hook_event_name":"SessionStart","session_id":"s1","cwd":"/work"}`, env); code != 0 {
		t.Fatalf("session.start blocked: %d", code)
	}
	ses, err := store.LoadSession()
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	daemon := agentguard.NewDaemon(store, nil)
	daemon.PollInterval = time.Hour // only push can explain a fast drop
	done := make(chan error, 1)
	go func() { done <- daemon.Run(ctx) }()

	// Let the SSE subscription establish, then throw the kill switch.
	time.Sleep(300 * time.Millisecond)
	idToken := deviceLogin(t, base, "strazactl", "kim", "hunter2!")
	req, _ := http.NewRequest("POST", base+"/v1/admin/sessions/"+ses.SessionID+"/revoke", nil)
	req.Header.Set("Authorization", "Bearer "+idToken)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("revoke = %d", resp.StatusCode)
	}
	revokedAt := time.Now()

	deadline := time.Now().Add(4 * time.Second)
	for {
		if _, err := store.LoadSession(); err != nil {
			break // dropped
		}
		if time.Now().After(deadline) {
			t.Fatal("kill switch never reached the daemon over the edge lane")
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Logf("edge kill latency: %s (revoke POST → local state dropped)", time.Since(revokedAt))

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Error("daemon did not exit after revocation")
	}
	if rev, err := store.LoadRevocation(); err != nil || rev.Reason == "" {
		t.Errorf("revocation marker missing/empty: %+v err=%v", rev, err)
	}
}
