package agentguard

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/strazahq/straza/internal/agentguard/trace"
)

// The mcp lane's journal: one "call" record per proxied tool call
// carrying the gateway's answer (status + correlation id), lifecycle records
// in a debug window, and, above all, nothing trace-shaped ever on the
// protocol channel or the process's stdout/stderr.

func debugToggleOn(t *testing.T, store *Store) {
	t.Helper()
	if err := trace.WriteToggle(store.stateDir(), trace.Toggle{Level: "debug", Until: time.Now().Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
}

func TestMCPProxyCallJournal(t *testing.T) {
	g := newFakeGateway(t)
	store := proxyTestStore(t, g.URL)
	ctx, p, harness := buildProxy(t, g, store)
	p.tr, p.harness = store.Trace(), "claude-code"

	if _, err := echoCall(ctx, harness, "hi"); err != nil {
		t.Fatalf("call: %v", err)
	}
	recs := recordsNamed(journalRecords(t, store), "call")
	if len(recs) != 1 {
		t.Fatalf("call records = %d, want 1: %+v", len(recs), recs)
	}
	assertAttrs(t, "call", recs[0], map[string]any{
		"v": float64(1), "lane": "mcp", "harness": "claude-code", "tool": "everything__echo",
		"outcome": "ok", "status": float64(200),
	}, "revived")
	assertDuration(t, "call", recs[0])
	if corr, _ := recs[0].Attrs["correlation"].(string); !strings.HasPrefix(corr, "gw-") {
		t.Errorf("correlation = %v, want the gateway's X-Request-Id", recs[0].Attrs["correlation"])
	}

	// Token rotation mid-session: the proxy refreshes and retries; the ONE
	// logical call leaves ONE record naming the retried answer.
	g.rotate()
	if _, err := echoCall(ctx, harness, "after-rotate"); err != nil {
		t.Fatalf("post-rotation call: %v", err)
	}
	recs = recordsNamed(journalRecords(t, store), "call")
	if len(recs) != 2 {
		t.Fatalf("call records after rotation = %d, want 2", len(recs))
	}
	assertAttrs(t, "rotated call", recs[1], map[string]any{"outcome": "ok", "status": float64(200)})
	assertDuration(t, "rotated call", recs[1])
	if first, second := recs[0].Attrs["correlation"], recs[1].Attrs["correlation"]; first == second || second == "" {
		t.Errorf("rotated call correlation = %v (first %v), want the retried answer's own id", second, first)
	}
}

func TestMCPProxyDebugLifecycle(t *testing.T) {
	g := newFakeGateway(t)
	store := proxyTestStore(t, g.URL)
	debugToggleOn(t, store)
	ctx, p, harness := buildProxy(t, g, store)
	p.tr, p.harness = store.Trace(), "claude-code"
	p.jitterMax = 0 // revive arms a scheduled resync; fire it immediately
	if !p.tr.DebugOn() {
		t.Fatal("debug window not in force")
	}

	p.debugSession(Session{SessionID: "s-mcp", SnapshotID: "snap-1", ExpiresAt: time.Now().Add(5 * time.Minute)})
	p.resync(ctx)
	_ = p.session().Close() // a corpse: the next call revives
	if _, err := echoCall(ctx, harness, "revive me"); err != nil {
		t.Fatalf("call through a dead session must heal: %v", err)
	}

	recs := journalRecords(t, store)
	byKind := map[string][]trace.Record{}
	for _, r := range recs {
		byKind[r.Msg] = append(byKind[r.Msg], r)
	}
	if len(byKind["mcp.session"]) != 1 {
		t.Errorf("mcp.session records = %d, want 1", len(byKind["mcp.session"]))
	} else {
		assertAttrs(t, "mcp.session", byKind["mcp.session"][0], map[string]any{"session": "s-mcp", "snapshot": "snap-1"})
	}
	if len(byKind["mcp.resync"]) == 0 {
		t.Error("no mcp.resync record")
	} else {
		assertAttrs(t, "mcp.resync", byKind["mcp.resync"][0], map[string]any{"tools": float64(1), "ok": true})
	}
	if len(byKind["mcp.reconcile"]) == 0 {
		t.Error("no mcp.reconcile record")
	}
	if len(byKind["mcp.redial"]) != 1 {
		t.Errorf("mcp.redial records = %d, want 1", len(byKind["mcp.redial"]))
	} else {
		assertAttrs(t, "mcp.redial", byKind["mcp.redial"][0], map[string]any{"attempt": float64(1), "ok": true}, "err_class")
	}
	calls := byKind["call"]
	if len(calls) != 1 {
		t.Fatalf("call records = %d, want 1", len(calls))
	}
	assertAttrs(t, "revived call", calls[0], map[string]any{"outcome": "ok", "revived": true})
	assertDuration(t, "revived call", calls[0])

	// A failing revival is a debug record too, with the error's class only.
	p.dial = func(context.Context) (*mcp.ClientSession, error) {
		return nil, errors.New("gateway down: http://secret.internal/mcp")
	}
	_ = p.session().Close()
	if _, err := echoCall(ctx, harness, "cannot heal"); err == nil {
		t.Fatal("call with a dead session and a dead dial must fail")
	}
	redials := recordsNamed(journalRecords(t, store), "mcp.redial")
	if len(redials) != 2 {
		t.Fatalf("mcp.redial records = %d, want 2", len(redials))
	}
	assertAttrs(t, "failed redial", redials[1], map[string]any{"ok": false, "err_class": "other", "attempt": float64(1)})
	raw, _ := os.ReadFile(store.TracePath())
	if bytes.Contains(raw, []byte("secret.internal")) {
		t.Fatalf("error text leaked into the trace: %s", raw)
	}
}

func TestSessionAuthTransportObservesCall(t *testing.T) {
	g := newFakeGateway(t)
	store := proxyTestStore(t, g.URL)
	client := NewClient(g.URL)
	ctx := context.Background()
	if _, err := ensureSession(ctx, store, client, "claude-code"); err != nil {
		t.Fatal(err)
	}
	tr := &sessionAuthTransport{base: http.DefaultTransport, store: store, client: client, harness: "claude-code"}
	observe := func() (int, string) {
		t.Helper()
		call := &trace.Call{}
		req, err := http.NewRequestWithContext(trace.WithCall(ctx, call), http.MethodGet, g.URL+"/mcp", nil)
		if err != nil {
			t.Fatal(err)
		}
		resp, err := tr.RoundTrip(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		return call.Get()
	}
	if st, id := observe(); id != "gw-1" || st == 0 || st == http.StatusUnauthorized {
		t.Errorf("observed (%d, %q), want the gateway's first authorized answer gw-1", st, id)
	}
	// Rotation: the first send 401s, the transport refreshes and retries,
	// and the journal sees the RETRIED answer, never the refusal.
	g.rotate()
	if st, id := observe(); id != "gw-2" || st == http.StatusUnauthorized {
		t.Errorf("observed (%d, %q) after rotation, want the retried answer gw-2", st, id)
	}
}

// lockedBuffer is a concurrency-safe bytes.Buffer for the captured streams.
type lockedBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

// teeWriteCloser copies everything the proxy writes to the harness into a
// buffer the test can inspect, then forwards it.
type teeWriteCloser struct {
	w   io.WriteCloser
	tee *lockedBuffer
}

func (t *teeWriteCloser) Write(p []byte) (int, error) {
	_, _ = t.tee.Write(p)
	return t.w.Write(p)
}

func (t *teeWriteCloser) Close() error { return t.w.Close() }

// TestMCPProxyStdoutPurity runs the REAL proxy entrypoint (runMCPProxy, the
// one behind `straza mcp`) with the debug window open, over in-memory pipes
// standing in for stdio, with the process's own stdout and stderr captured:
// every byte the harness receives is a JSON-RPC frame, nothing trace-shaped
// reaches stdout or stderr, and the trace file holds the records.
func TestMCPProxyStdoutPurity(t *testing.T) {
	g := newFakeGateway(t)
	store := proxyTestStore(t, g.URL)
	debugToggleOn(t, store)

	origOut, origErr := os.Stdout, os.Stderr
	outR, outW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	errR, errW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout, os.Stderr = outW, errW
	restored := false
	restore := func() {
		if !restored {
			os.Stdout, os.Stderr = origOut, origErr
			restored = true
		}
	}
	t.Cleanup(restore)
	var procOut, procErr lockedBuffer
	var copies sync.WaitGroup
	copies.Add(2)
	go func() { defer copies.Done(); _, _ = io.Copy(&procOut, outR) }()
	go func() { defer copies.Done(); _, _ = io.Copy(&procErr, errR) }()

	// harness -> proxy and proxy -> harness pipes; the proxy's writes are teed.
	h2pR, h2pW := io.Pipe()
	p2hR, p2hW := io.Pipe()
	var wire lockedBuffer
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- runMCPProxy(ctx, "claude-code", "", &mcp.IOTransport{Reader: h2pR, Writer: &teeWriteCloser{w: p2hW, tee: &wire}})
	}()
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "harness", Version: "test"}, nil).
		Connect(ctx, &mcp.IOTransport{Reader: p2hR, Writer: h2pW}, nil)
	if err != nil {
		t.Fatalf("harness connect: %v", err)
	}
	tools, err := cs.ListTools(ctx, nil)
	if err != nil || len(tools.Tools) != 1 || tools.Tools[0].Name != "everything__echo" {
		t.Fatalf("tools = %+v, %v", tools, err)
	}
	out, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "everything__echo", Arguments: map[string]any{"text": "pure"}})
	if err != nil {
		t.Fatalf("call: %v", err)
	}
	if txt := out.Content[0].(*mcp.TextContent).Text; txt != "gateway: pure" {
		t.Errorf("result = %q", txt)
	}
	_ = cs.Close()
	cancel()
	select {
	case <-done:
	case <-time.After(15 * time.Second):
		t.Fatal("runMCPProxy did not return after the harness hung up")
	}
	restore()
	_ = outW.Close()
	_ = errW.Close()
	copies.Wait()

	// Every frame on the wire is JSON-RPC.
	frames := 0
	for _, line := range strings.Split(strings.TrimSpace(wire.String()), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var frame struct {
			JSONRPC string `json:"jsonrpc"`
		}
		if err := json.Unmarshal([]byte(line), &frame); err != nil || frame.JSONRPC != "2.0" {
			t.Fatalf("non-JSON-RPC bytes reached the harness: %q (%v)", line, err)
		}
		frames++
	}
	if frames < 3 {
		t.Errorf("only %d frames on the wire, expected at least initialize + tools/list + tools/call answers", frames)
	}
	// Nothing trace-shaped (or frame-shaped) on the process streams.
	for name, s := range map[string]string{"stdout": procOut.String(), "stderr": procErr.String()} {
		for _, bad := range []string{`"v":1`, `"msg":`, `"jsonrpc"`, "everything__echo"} {
			if strings.Contains(s, bad) {
				t.Errorf("%s carries %q: %q", name, bad, s)
			}
		}
	}
	// And the records are in the file.
	recs := journalRecords(t, store)
	if calls := recordsNamed(recs, "call"); len(calls) != 1 {
		t.Errorf("call records = %d, want 1", len(calls))
	} else {
		assertAttrs(t, "call", calls[0], map[string]any{"tool": "everything__echo", "outcome": "ok", "harness": "claude-code"})
		assertDuration(t, "call", calls[0])
	}
	if len(recordsNamed(recs, "mcp.session")) != 1 || len(recordsNamed(recs, "mcp.resync")) == 0 {
		t.Errorf("debug lifecycle records missing: %+v", recs)
	}
}
