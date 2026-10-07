package agentguard

import (
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/strazahq/straza/internal/policy"
)

// TestDrainOnceSyncsSnapshot pins the per-decision policy pulse: the
// detached helper adopts a changed snapshot (so a busy session runs at most
// one tool call + lag behind the active policy, no daemon needed), throttles
// itself via the marker file, and answers 304s with no state change.
func TestDrainOnceSyncsSnapshot(t *testing.T) {
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

	var snapshotHits atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/snapshot":
			snapshotHits.Add(1)
			if strings.Trim(r.Header.Get("If-None-Match"), `"`) == newID {
				w.WriteHeader(http.StatusNotModified)
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

	t.Setenv("STRAZA_HOME", t.TempDir())
	store, err := OpenStore()
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SaveConfig(Config{ServerURL: srv.URL, SnapshotKeys: keys, SnapshotLagSeconds: 3600}); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveSnapshot(oldSigned); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveSession(Session{
		SessionID: "s1", SessionToken: "tok", SnapshotID: oldID,
		User: "u1", Roles: []string{"dev"}, Harness: "claude-code/2.1",
		IssuedAt: time.Now(), ExpiresAt: time.Now().Add(time.Hour),
	}); err != nil {
		t.Fatal(err)
	}

	// First pulse: adopts the new snapshot, id+blob stay consistent, the new
	// policy is what the next hook enforces.
	if _, err := DrainOnce(5 * time.Second); err != nil {
		t.Fatalf("DrainOnce: %v", err)
	}
	ses, err := store.LoadSession()
	if err != nil || ses.SnapshotID != newID {
		t.Fatalf("session snapshot id = %q (err %v), want adopted", ses.SnapshotID, err)
	}
	pdp, err := NewLocalPDP(store, ses.Subject())
	if err != nil {
		t.Fatalf("hook PDP after adoption: %v", err)
	}
	dec := pdp.Decide(Normalized{HarnessName: "claude-code", HarnessVersion: "2.1",
		Event: policy.Event{Kind: policy.EventToolPre, Tool: policy.ToolShellExec, Command: "new-cmd /x"}})
	if dec.Effect != policy.EffectDeny || dec.RuleID != "r2" {
		t.Errorf("new policy not enforced after pulse: %+v", dec)
	}
	if snapshotHits.Load() != 1 {
		t.Fatalf("snapshot endpoint hits = %d, want 1", snapshotHits.Load())
	}

	// Within the lag window: throttled, no server traffic.
	if _, err := DrainOnce(5 * time.Second); err != nil {
		t.Fatalf("DrainOnce (throttled): %v", err)
	}
	if snapshotHits.Load() != 1 {
		t.Fatalf("throttle failed: snapshot endpoint hits = %d, want still 1", snapshotHits.Load())
	}

	// Lag elapsed (age the marker): one cheap 304, nothing changes.
	past := time.Now().Add(-2 * time.Hour)
	if err := os.Chtimes(store.statePath("snapshot-checked"), past, past); err != nil {
		t.Fatal(err)
	}
	if _, err := DrainOnce(5 * time.Second); err != nil {
		t.Fatalf("DrainOnce (304): %v", err)
	}
	if snapshotHits.Load() != 2 {
		t.Fatalf("snapshot endpoint hits = %d, want 2", snapshotHits.Load())
	}
	if ses, _ := store.LoadSession(); ses.SnapshotID != newID {
		t.Errorf("304 changed the session snapshot id to %q", ses.SnapshotID)
	}
}
