package agentguard_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/strazahq/straza/internal/agentguard"
	"github.com/strazahq/straza/internal/config"
)

// TestHookBurstReachesChain pins zero audit loss for a burst of governed
// decisions taken by one enrolled session with no daemon and no
// session.end. Nine back-to-back hook decisions produce nine
// straza.audit.tool records, and every one of them must reach the chain on
// the post-decision drain alone: the server's per-session ingest limiter
// refuses part of the burst with 429, and a refusal that only parks records
// is loss in every session that never ends.
func TestHookBurstReachesChain(t *testing.T) {
	base, _ := bootStrazad(t, func(c *config.Config) {
		// The shipped profile default (internal/config/config.go): rate 5
		// per second, burst 5, so a nine-decision burst trips it.
		c.Governance.AuditIngestPerSessionRPS = 5
	})

	home := t.TempDir()
	t.Setenv("STRAZA_HOME", home)
	env := []string{"STRAZA_HOME=" + home, "CLAUDECODE=1"}

	st, err := agentguard.OpenStore()
	if err != nil {
		t.Fatal(err)
	}
	driveEnroll(t, st, base)

	ssPayload := `{"hook_event_name":"SessionStart","session_id":"burst","cwd":"/work"}`
	if _, code := hook(t, "claude-code", ssPayload, env); code != 0 {
		t.Fatalf("session.start blocked: %d", code)
	}

	// The burst a harness produces: five refusals interleaved with four
	// allowed calls, every decision taken locally and spooled.
	calls := []struct {
		command  string
		wantCode int
	}{
		{"rm -rf /tmp/a", 2},
		{"ls -la", 0},
		{"rm -rf /tmp/b", 2},
		{"echo one", 0},
		{"rm -rf /tmp/c", 2},
		{"echo two", 0},
		{"rm -rf /tmp/d", 2},
		{"echo three", 0},
		{"rm -rf /tmp/e", 2},
	}
	for i, call := range calls {
		payload := fmt.Sprintf(
			`{"hook_event_name":"PreToolUse","session_id":"burst","cwd":"/work","tool_name":"Bash","tool_input":{"command":%q}}`,
			call.command)
		out, code := hook(t, "claude-code", payload, env)
		if code != call.wantCode {
			t.Fatalf("decision %d (%s): exit %d, want %d: %s", i, call.command, code, call.wantCode, out)
		}
		// Stand-in for the detached `straza drain` the hook spawns after
		// every decision (drain_spawn.go): the same work the sibling process
		// does (cmd/straza/main.go drainCmd), run inline because the spawn
		// refuses to re-exec a test binary.
		if _, err := agentguard.DrainOnce(10 * time.Second); err != nil {
			t.Logf("post-decision drain %d: %v", i, err)
		}
	}

	// No session.end and no daemon: the post-decision drain is the only
	// delivery path, and it has to finish the job.
	deadline := time.Now().Add(30 * time.Second)
	var got int
	for time.Now().Before(deadline) {
		got = countToolRecords(fetchAudit(t, base))
		if got >= len(calls) {
			break
		}
		time.Sleep(250 * time.Millisecond)
	}
	if got != len(calls) {
		t.Errorf("tool audit records on the chain = %d, want %d", got, len(calls))
	}
}

// countToolRecords counts straza.audit.tool CloudEvents in a chain page.
func countToolRecords(ces []string) int {
	n := 0
	for _, ce := range ces {
		var m struct {
			Type string `json:"type"`
		}
		if json.Unmarshal([]byte(ce), &m) == nil && m.Type == "straza.audit.tool" {
			n++
		}
	}
	return n
}

// TestAuditBatchRetriesBackpressure pins which audit-batch refusals the
// client retries in place. A 429 or 503 is the server asking for a retry and
// the spool has no other trigger to unpark the batch, so the upload waits
// and re-sends; anything else is the server rejecting the batch and comes
// straight back, leaving the records spooled for the next trigger.
func TestAuditBatchRetriesBackpressure(t *testing.T) {
	lanes := []struct {
		name     string
		statuses []int
		wantReqs int
		wantErr  bool
	}{
		{"ingest limit clears", []int{429, 200}, 2, false},
		{"audit store recovers", []int{503, 200}, 2, false},
		{"session refused", []int{401}, 1, true},
		{"batch rejected", []int{400}, 1, true},
		{"refused throughout", []int{429, 429, 429, 429, 429, 429, 429}, 6, true},
	}
	for _, lane := range lanes {
		t.Run(lane.name, func(t *testing.T) {
			var reqs atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				i := int(reqs.Add(1)) - 1
				if i >= len(lane.statuses) {
					t.Errorf("request %d past the scripted answers", i+1)
					i = len(lane.statuses) - 1
				}
				status := lane.statuses[i]
				w.WriteHeader(status)
				if status == http.StatusOK {
					_, _ = w.Write([]byte(`{"accepted":1}`))
				} else {
					_, _ = w.Write([]byte(`{"error":"refused"}`))
				}
			}))
			defer srv.Close()

			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			accepted, err := agentguard.NewClient(srv.URL).AuditBatch(ctx, "tok",
				[]json.RawMessage{json.RawMessage(`{"id":"e1"}`)})
			if (err != nil) != lane.wantErr {
				t.Fatalf("AuditBatch error = %v, want error %v", err, lane.wantErr)
			}
			if got := int(reqs.Load()); got != lane.wantReqs {
				t.Errorf("requests = %d, want %d", got, lane.wantReqs)
			}
			if !lane.wantErr && accepted != 1 {
				t.Errorf("accepted = %d, want 1", accepted)
			}
		})
	}
}
