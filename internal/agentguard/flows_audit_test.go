package agentguard

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/strazahq/straza/internal/agentguard/spool"
	"github.com/strazahq/straza/internal/policy"
)

// TestDecideRecordsOnce pins that every hook decision leaves exactly one
// straza.audit.tool record: the client spools a decision it made itself and
// spools nothing for a verdict it adopted from /v1/decide, which the server
// audits. The drain kick stays unconditional so the snapshot pulse survives.
func TestDecideRecordsOnce(t *testing.T) {
	priv, keys := testSnapshotKey(t)
	signed, id := testSignedPolicy(t, priv, `
apiVersion: straza.dev/v1beta1
kind: PolicySet
metadata: {name: once}
spec:
  match: {roles: [dev]}
  rules:
    - id: no-rm
      tools: [shell.exec]
      command: {denyPatterns: ["rm -rf *"]}
      effect: deny
      reason: "blocked"
    - id: classify-scripts
      tools: [shell.exec]
      command: {allowPatterns: ["python *"]}
      effect: allow
      mode: classify
    - id: gated-fetch
      tools: [net.fetch]
      effect: allow
      mode: serverCheck
    - id: gated-deploy
      tools: [shell.exec]
      command: {allowPatterns: ["deploy-prod*"]}
      effect: allow
      mode: approve
`)

	kicked := 0
	old := drainSpawner
	drainSpawner = func() { kicked++ }
	t.Cleanup(func() { drainSpawner = old })

	shell := func(cmd string) policy.Event {
		return policy.Event{Kind: policy.EventToolPre, Tool: policy.ToolShellExec, Command: cmd}
	}
	fetch := policy.Event{Kind: policy.EventToolPre, Tool: policy.ToolNetFetch, Paths: []string{"https://x.io"}}

	lanes := []struct {
		name       string
		event      policy.Event
		verdict    map[string]any // the fake's 200 answer; nil means no server answers
		wantEffect string
		wantSpool  int
		wantCalls  int32
	}{
		{"local allow", shell("ls -la"), nil, policy.EffectAllow, 1, 0},
		{"local deny", shell("rm -rf /x"), nil, policy.EffectDeny, 1, 0},
		{"classify", shell("python build.py"), nil, policy.EffectAllow, 1, 0},
		{"serverCheck online", fetch, map[string]any{"effect": "allow", "ruleId": "gated-fetch"}, policy.EffectAllow, 0, 1},
		{"serverCheck offline", fetch, nil, policy.EffectDeny, 1, 0},
		{"approve online allowed", shell("deploy-prod v1"),
			map[string]any{"effect": "allow", "ruleId": "gated-deploy", "reason": "Straza: approved by kim (ref abc123)"},
			policy.EffectAllow, 0, 1},
		{"approve online denied", shell("deploy-prod v1"),
			map[string]any{"effect": "deny", "ruleId": "gated-deploy", "reason": "Straza: approval requested; retry after approval (ref abc123)"},
			policy.EffectDeny, 0, 1},
		{"revoked", fetch,
			map[string]any{"effect": "deny", "ruleId": "revoked", "reason": "Straza: your session has been revoked. Re-enroll"},
			policy.EffectDeny, 0, 1},
	}
	for _, lane := range lanes {
		t.Run(lane.name, func(t *testing.T) {
			var calls atomic.Int32
			var body atomic.Value
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/v1/decide" {
					t.Errorf("unexpected path %s", r.URL.Path)
				}
				calls.Add(1)
				var m map[string]any
				_ = json.NewDecoder(r.Body).Decode(&m)
				body.Store(m)
				_ = json.NewEncoder(w).Encode(lane.verdict)
			}))
			if lane.verdict == nil {
				srv.Close() // a closed port: connection refused, the offline branch
			} else {
				defer srv.Close()
			}

			t.Setenv("STRAZA_HOME", t.TempDir())
			store, err := OpenStore()
			if err != nil {
				t.Fatal(err)
			}
			if err := store.SaveConfig(Config{ServerURL: srv.URL, SnapshotKeys: keys}); err != nil {
				t.Fatal(err)
			}
			if err := store.SaveSnapshot(signed); err != nil {
				t.Fatal(err)
			}
			if err := store.SaveSession(Session{
				SessionID: "s-once", SessionToken: "tok", SnapshotID: id,
				User: "kim", Roles: []string{"dev"}, Harness: "claude-code/2.1",
				IssuedAt: time.Now(), ExpiresAt: time.Now().Add(time.Hour),
			}); err != nil {
				t.Fatal(err)
			}
			kicked = 0

			dec := liveDecider{store: store}.Decide(Normalized{
				Event: lane.event, HarnessName: "claude-code", HarnessVersion: "2.1",
				AgentType: "researcher", AgentID: "a-1",
			})
			if dec.Effect != lane.wantEffect {
				t.Fatalf("decision = %+v, want %s", dec, lane.wantEffect)
			}
			if got := calls.Load(); got != lane.wantCalls {
				t.Errorf("/v1/decide calls = %d, want %d", got, lane.wantCalls)
			}
			recs, err := spool.ReadRecords(store.SpoolPath())
			if err != nil {
				t.Fatal(err)
			}
			if len(recs) != lane.wantSpool {
				t.Fatalf("spooled records = %d, want %d: %s", len(recs), lane.wantSpool, recs)
			}
			if kicked != 1 {
				t.Errorf("drain kicked %d times, want 1 for every governed decision", kicked)
			}
			// Whichever layer records the decision, the delegate tag rides
			// with it: the spool record carries it, and the decide body
			// hands it to the server for its record.
			for _, rec := range recs {
				var ce struct {
					Data map[string]any `json:"data"`
				}
				_ = json.Unmarshal(rec, &ce)
				if ce.Data["agentType"] != "researcher" || ce.Data["agentId"] != "a-1" {
					t.Errorf("spool record lost the delegate tag: %s", rec)
				}
			}
			if lane.wantCalls > 0 {
				m, _ := body.Load().(map[string]any)
				if m["agentType"] != "researcher" || m["agentId"] != "a-1" {
					t.Errorf("decide body lacks the delegate tag: %v", m)
				}
			}
		})
	}
}
