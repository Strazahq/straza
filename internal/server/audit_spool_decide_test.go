package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/strazahq/straza/internal/store"
)

// heldAuditApp runs an App whose audit queue is full and stays full, as
// under a database outage: the loop holds one record against a database
// that stops answering at it, and the queue of one holds another. before
// runs after the App is built and before the queue fills, while the
// database still takes every record.
func heldAuditApp(t *testing.T, block bool, bound time.Duration, before func(app *App, base string)) (*App, string, *auditSpool, *syncBuffer) {
	t.Helper()
	log, buf := captureLogger()
	sp := newAuditSpool(block, log, func(c context.Context, e store.OutboxEvent) error {
		if !strings.Contains(e.CE, `"id":"ce-held-`) {
			return nil
		}
		<-c.Done()
		return c.Err()
	})
	sp.ch = make(chan store.OutboxEvent, 1)
	sp.submitBound = bound
	sp.drainBound = 50 * time.Millisecond
	app, base := testAppPreRun(t, []func(*App){func(a *App) { a.log = log; a.audit = sp }})
	before(app, base)
	_ = sp.submit(context.Background(), spoolRecord("ce-held-0", "x"))
	waitFor(t, "the loop to hold the first record", func() bool { return len(sp.ch) == 0 })
	_ = sp.submit(context.Background(), spoolRecord("ce-held-1", "x"))
	return app, base, sp, buf
}

// failClosedLines returns the fail-closed Error records in buf.
func failClosedLines(buf *syncBuffer) []string {
	var out []string
	for _, l := range errorRecords(buf) {
		if strings.Contains(l, "fail-closed: internal failure answered as a deny") {
			out = append(out, l)
		}
	}
	return out
}

// decideCall posts one shell decision with token through c.
func decideCall(c *http.Client, base, token string) (*http.Response, error) {
	req, err := http.NewRequest(http.MethodPost, base+"/v1/decide",
		strings.NewReader(`{"event":{"kind":"tool.pre","tool":"shell.exec","command":"ls -la"}}`))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	return c.Do(req)
}

// TestDecideUnderFullAuditQueue pins /v1/decide while the audit queue stays
// full. Under block the decision waits for room at most the submit bound,
// then answers a deny with auditQueueFullMsg and logs one fail-closed line
// with its correlation id; its record never enters the queue, so nothing
// runs. straza_pdp_decisions_total counts the effect the client got, so a
// refused allow counts as a deny. Under drop-with-counter the decision
// answers at once with the policy's own verdict and its record is dropped
// and counted, as before: the control.
func TestDecideUnderFullAuditQueue(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name       string
		block      bool
		wantEffect string
		wantReason string // "" means the policy's own reason
	}{
		{"block refuses after the bound", true, "deny", auditQueueFullMsg},
		{"drop answers at once", false, "allow", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			const bound = 300 * time.Millisecond
			var token string
			app, base, sp, buf := heldAuditApp(t, tc.block, bound, func(app *App, base string) {
				seedIdentity(t, app)
				token, _ = checkinToken(t, app, base)
			})
			start := time.Now()
			resp, err := decideCall(&http.Client{Timeout: 5 * time.Second}, base, token)
			if err != nil {
				t.Fatalf("POST /v1/decide: %v; the decision waited past the client's timeout", err)
			}
			took := time.Since(start)
			var got DecideResponse
			if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
				t.Fatal(err)
			}
			_ = resp.Body.Close()
			if resp.StatusCode != http.StatusOK || got.Effect != tc.wantEffect || (tc.wantReason != "" && got.Reason != tc.wantReason) {
				t.Fatalf("answer %d %+v, want %s with reason %q", resp.StatusCode, got, tc.wantEffect, tc.wantReason)
			}
			for _, effect := range []string{"allow", "deny"} {
				want := 0.0
				if effect == tc.wantEffect {
					want = 1
				}
				if v, _, _ := metricSample(t, app, "straza_pdp_decisions_total", map[string]string{"effect": effect}); v != want {
					t.Fatalf("straza_pdp_decisions_total{effect=%q} = %v, want %v", effect, v, want)
				}
			}
			lines := failClosedLines(buf)
			if tc.block {
				if took < bound {
					t.Fatalf("answered after %v, before the bound of %v", took, bound)
				}
				id := resp.Header.Get(requestIDHeader)
				if len(lines) != 1 || !strings.Contains(lines[0], "lane=hook") || !strings.Contains(lines[0], "correlation_id="+id) {
					t.Fatalf("fail-closed records %q, want one with lane=hook and correlation_id=%s", lines, id)
				}
				if n, d := len(sp.ch), sp.dropped.Load(); n != 1 || d != 0 {
					t.Fatalf("queue holds %d records and dropped %d, want only the held one and none dropped", n, d)
				}
				return
			}
			if took >= bound || len(lines) != 0 || sp.dropped.Load() != 1 {
				t.Fatalf("answered after %v with fail-closed records %q and dropped %d, want at once, none and 1", took, lines, sp.dropped.Load())
			}
		})
	}
}

// TestDecideWaitersEndWithTheirClients pins that under block a decision
// waiting for room ends when its client gives up, so a long outage does not
// pile up goroutines and connections in strazad. Each decision that reached
// the queue logs one fail-closed line, and none of their records enters it.
// A client can give up before its request becomes a decision, so the lines
// are counted by correlation id. The test does not run in parallel, because
// it counts the process's goroutines.
func TestDecideWaitersEndWithTheirClients(t *testing.T) {
	var token string
	_, base, sp, buf := heldAuditApp(t, true, time.Hour, func(app *App, base string) {
		seedIdentity(t, app)
		token, _ = checkinToken(t, app, base)
	})
	fds := func() int {
		e, err := os.ReadDir("/proc/self/fd")
		if err != nil {
			return 0 // not Linux: the goroutine count carries the check
		}
		return len(e)
	}
	g0, f0 := runtime.NumGoroutine(), fds()
	const n = 200
	var answered atomic.Int64
	var clients sync.WaitGroup
	for range n {
		clients.Add(1)
		go func() {
			defer clients.Done()
			c := &http.Client{Timeout: 200 * time.Millisecond, Transport: &http.Transport{DisableKeepAlives: true}}
			if resp, err := decideCall(c, base, token); err == nil {
				answered.Add(1)
				_ = resp.Body.Close()
			}
		}()
	}
	clients.Wait()
	if got := answered.Load(); got != 0 {
		t.Fatalf("%d decisions answered while the queue was full, want none", got)
	}
	var g, f int
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(50 * time.Millisecond) {
		if g, f = runtime.NumGoroutine(), fds(); g <= g0+20 && f <= f0+20 {
			break
		}
	}
	if g > g0+20 || f > f0+20 {
		t.Fatalf("after %d clients gave up, strazad keeps %d more goroutines and %d more open files, want the waits ended", n, g-g0, f-f0)
	}
	ids := map[string]bool{}
	lines := failClosedLines(buf)
	for _, l := range lines {
		if i := strings.Index(l, "correlation_id="); i >= 0 {
			ids[strings.Fields(l[i:])[0]] = true
		}
	}
	if len(lines) < n/2 || len(ids) != len(lines) {
		t.Fatalf("fail-closed records = %d for %d correlation ids after %d decisions, want one per decision", len(lines), len(ids), n)
	}
	if len(sp.ch) != 1 {
		t.Fatalf("queue holds %d records, want only the held one", len(sp.ch))
	}
}

// TestGatewayRefusedWhileAuditQueueStaysFull pins the gateway under block
// with a full queue. The same allowed call first runs the upstream tool
// while the queue has room, the positive control. With the queue full it
// is refused with auditQueueFullMsg, the upstream tool does not run again,
// and one fail-closed line names the gateway lane. tools/list is refused with the same sentence, because its record is
// queued before the list is served, and rpcFail logs its one Error record.
func TestGatewayRefusedWhileAuditQueueStaysFull(t *testing.T) {
	t.Parallel()
	var echoes atomic.Int64
	srv := mcp.NewServer(&mcp.Implementation{Name: "held-upstream", Version: "1.0.0"}, nil)
	mcp.AddTool(srv, &mcp.Tool{Name: "echo", Description: "echo"},
		func(context.Context, *mcp.CallToolRequest, struct{}) (*mcp.CallToolResult, any, error) {
			echoes.Add(1)
			return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "echo"}}}, nil, nil
		})
	up := &gatewayUpstream{Server: httptest.NewServer(mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return srv }, nil))}
	t.Cleanup(up.Close)
	var token string
	_, base, _, buf := heldAuditApp(t, true, 300*time.Millisecond, func(app *App, base string) {
		seedGatewayUser(t, app, "kim", "dev")
		installEchoApp(t, app, up, "heldapp", 0)
		catActivate(t, app, "held-policy", `
apiVersion: straza.dev/v1beta1
kind: PolicySet
metadata: {name: held-policy}
spec:
  match: {roles: [dev]}
  rules:
    - id: allow-echo
      tools: [mcp.call]
      apps: [heldapp]
      toolNames: {allow: ["echo"]}
      effect: allow
`)
		token, _ = gatewaySession(t, base, "kim")
		if _, _, raw := mcpCall(t, base, token, "tools/call", map[string]any{"name": "heldapp__echo", "arguments": map[string]any{}}); echoes.Load() != 1 {
			t.Fatalf("with room in the queue the call answered %s and the upstream ran %d times, want once", raw, echoes.Load())
		}
	})

	_, call, raw := mcpCall(t, base, token, "tools/call", map[string]any{"name": "heldapp__echo", "arguments": map[string]any{}})
	result, _ := call["result"].(map[string]any)
	if result["isError"] != true || !strings.Contains(string(raw), `"text":"`+auditQueueFullMsg+`"`) {
		t.Fatalf("tools/call answer %s, want a tool error with auditQueueFullMsg", raw)
	}
	if n := echoes.Load(); n != 1 {
		t.Fatalf("the upstream tool ran %d times, want only the control's run", n)
	}
	_, list, raw := mcpCall(t, base, token, "tools/list", nil)
	rpcErr, _ := list["error"].(map[string]any)
	if rpcErr["message"] != auditQueueFullMsg {
		t.Fatalf("tools/list answer %s, want an error with auditQueueFullMsg", raw)
	}
	if lines := failClosedLines(buf); len(lines) != 1 || !strings.Contains(lines[0], "lane=gateway") {
		t.Fatalf("fail-closed records %q, want one on the gateway lane", lines)
	}
	var listLines []string
	for _, l := range errorRecords(buf) {
		if strings.Contains(l, "rpc_code=-32603") && strings.Contains(l, "the audit queue stayed full") {
			listLines = append(listLines, l)
		}
	}
	if len(listLines) != 1 {
		t.Fatalf("tools/list refusal records %q, want one", listLines)
	}
}
