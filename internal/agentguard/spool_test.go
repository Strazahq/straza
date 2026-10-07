package agentguard

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/strazahq/straza/internal/agentguard/spool"
	"github.com/strazahq/straza/internal/policy"
)

func spoolRecord(cmd, effect string) Normalized {
	return Normalized{
		HarnessName: "claude-code", HarnessVersion: "2.1",
		Event: policy.Event{Kind: policy.EventToolPre, Tool: policy.ToolShellExec, Command: cmd},
	}
}

// TestSpoolZeroLossAcrossOutage pins zero loss: records appended while the
// server is unreachable survive; once it recovers, Drain uploads them all and
// truncates only on success.
func TestSpoolZeroLossAcrossOutage(t *testing.T) {
	sp := spool.NewSpool(filepath.Join(t.TempDir(), "spool.jsonl"))

	// Append 5 decisions.
	for i := 0; i < 5; i++ {
		if err := spoolAppend(sp, spoolRecord("cmd", "allow"), "s1", "snap-1", policy.Decision{Effect: policy.EffectAllow}); err != nil {
			t.Fatal(err)
		}
	}
	if n, _ := sp.Pending(); n != 5 {
		t.Fatalf("pending = %d, want 5", n)
	}

	// Drain against a DOWN server: fails, spool intact (zero loss).
	down := NewClient("http://127.0.0.1:1")
	if _, err := sp.Drain(context.Background(), down, "tok"); err == nil {
		t.Fatal("drain against down server should fail")
	}
	if n, _ := sp.Pending(); n != 5 {
		t.Fatalf("records lost after failed drain: pending = %d, want 5", n)
	}

	// Bring up a server that records the batch.
	var received atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Events []json.RawMessage `json:"events"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		received.Add(int64(len(body.Events)))
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"accepted":` + itoa(len(body.Events)) + `}`))
	}))
	defer srv.Close()

	n, err := sp.Drain(context.Background(), NewClient(srv.URL), "tok")
	if err != nil {
		t.Fatalf("drain after recovery: %v", err)
	}
	if n != 5 || received.Load() != 5 {
		t.Fatalf("drained %d, server got %d, want 5/5", n, received.Load())
	}
	// Spool truncated after success.
	if p, _ := sp.Pending(); p != 0 {
		t.Fatalf("spool not truncated after successful drain: %d", p)
	}

	// A drain with nothing pending is a no-op.
	if n, err := sp.Drain(context.Background(), NewClient(srv.URL), "tok"); err != nil || n != 0 {
		t.Fatalf("empty drain = %d, %v", n, err)
	}
}

// TestSpoolAppendDuringOutageNeverLost pins the rotation design: a failed
// drain parks records in a rotated pending file, appends made MEANWHILE land
// in a fresh live file, and the next successful drain uploads both. A
// truncate-after-read scheme could eat a record appended by another process
// (hook) between a drainer's read and its truncate.
func TestSpoolAppendDuringOutageNeverLost(t *testing.T) {
	dir := t.TempDir()
	sp := spool.NewSpool(filepath.Join(dir, "audit-spool.jsonl"))
	if err := spoolAppend(sp, spoolRecord("first", "deny"), "s1", "snap-1", policy.Decision{Effect: policy.EffectDeny}); err != nil {
		t.Fatal(err)
	}

	// Failed drain: the record is parked, not lost, and the live file is
	// free for appenders again.
	if _, err := sp.Drain(context.Background(), NewClient("http://127.0.0.1:1"), "tok"); err == nil {
		t.Fatal("drain against down server should fail")
	}
	if n, _ := sp.Pending(); n != 1 {
		t.Fatalf("pending after failed drain = %d, want 1 (parked)", n)
	}

	// An append landing while the previous batch is parked (the cross-process
	// window: hook appends while a daemon/detached drainer is mid-flight).
	if err := spoolAppend(sp, spoolRecord("second", "allow"), "s1", "snap-1", policy.Decision{Effect: policy.EffectAllow}); err != nil {
		t.Fatal(err)
	}
	if n, _ := sp.Pending(); n != 2 {
		t.Fatalf("pending = %d, want 2", n)
	}

	var received atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Events []json.RawMessage `json:"events"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		received.Add(int64(len(body.Events)))
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"accepted":` + itoa(len(body.Events)) + `}`))
	}))
	defer srv.Close()

	n, err := sp.Drain(context.Background(), NewClient(srv.URL), "tok")
	if err != nil || n != 2 || received.Load() != 2 {
		t.Fatalf("recovery drain = %d (server got %d), err %v; want both records", n, received.Load(), err)
	}
	if p, _ := sp.Pending(); p != 0 {
		t.Fatalf("pending after success = %d, want 0", p)
	}
	if left, _ := filepath.Glob(filepath.Join(dir, "audit-pending-*.jsonl")); len(left) != 0 {
		t.Fatalf("pending files not cleaned up: %v", left)
	}
}

func TestSpoolAppendContent(t *testing.T) {
	p := filepath.Join(t.TempDir(), "s.jsonl")
	sp := spool.NewSpool(p)
	n := spoolRecord("rm -rf /", "deny")
	n.Event.Workspace = "/work/proj"
	if err := spoolAppend(sp, n, "s9", "snap-9",
		policy.Decision{Effect: policy.EffectDeny, RuleID: "no-rm", Reason: "blocked"}); err != nil {
		t.Fatal(err)
	}
	recs, err := spool.ReadRecords(p)
	if err != nil || len(recs) != 1 {
		t.Fatalf("readAll = %d, %v", len(recs), err)
	}
	var ce map[string]any
	_ = json.Unmarshal(recs[0], &ce)
	if ce["id"] == nil || ce["type"] != "straza.audit.tool" {
		t.Errorf("bad CE envelope: %v", ce)
	}
	data := ce["data"].(map[string]any)
	if data["effect"] != "deny" || data["command"] != "rm -rf /" || data["session"] != "s9" {
		t.Errorf("bad audit data: %v", data)
	}
	// The record is evidence: it must say where and under which policy.
	if data["snapshot"] != "snap-9" || data["workspace"] != "/work/proj" {
		t.Errorf("audit context missing (snapshot/workspace): %v", data)
	}
	if _, ok := data["paths"]; ok {
		t.Errorf("empty paths should be omitted: %v", data)
	}
}

// TestSpoolAppendFullEventDetail: a file-tool deny records the exact target
// paths; an mcp.call records the upstream tool. An audit line that can't say
// what was attempted is not evidence.
func TestSpoolAppendFullEventDetail(t *testing.T) {
	p := filepath.Join(t.TempDir(), "s.jsonl")
	sp := spool.NewSpool(p)
	fileEv := Normalized{
		HarnessName: "claude-code", HarnessVersion: "2.1",
		Event: policy.Event{
			Kind: policy.EventToolPre, Tool: policy.ToolFileWrite,
			Paths: []string{`E:\prod\config.yaml`}, Workspace: `E:\prod`,
		},
	}
	mcpEv := Normalized{
		HarnessName: "claude-code", HarnessVersion: "2.1",
		Event: policy.Event{
			Kind: policy.EventToolPre, Tool: policy.ToolMCPCall,
			App: "github", ToolName: "create_issue",
		},
	}
	if err := spoolAppend(sp, fileEv, "s1", "snap-1", policy.Decision{Effect: policy.EffectDeny}); err != nil {
		t.Fatal(err)
	}
	if err := spoolAppend(sp, mcpEv, "s1", "snap-1", policy.Decision{Effect: policy.EffectAllow}); err != nil {
		t.Fatal(err)
	}
	recs, _ := spool.ReadRecords(p)
	if len(recs) != 2 {
		t.Fatalf("records = %d, want 2", len(recs))
	}
	var ce map[string]any
	_ = json.Unmarshal(recs[0], &ce)
	data := ce["data"].(map[string]any)
	paths, _ := data["paths"].([]any)
	if len(paths) != 1 || paths[0] != `E:\prod\config.yaml` {
		t.Errorf("file deny lost its target paths: %v", data)
	}
	_ = json.Unmarshal(recs[1], &ce)
	data = ce["data"].(map[string]any)
	if data["toolName"] != "create_issue" || data["app"] != "github" {
		t.Errorf("mcp audit lost the upstream tool: %v", data)
	}
}

// TestSpoolDrainMarker: drains that leave the spool empty stamp the
// audit-last-drain marker (doctor's "last successful drain" surface); failed
// drains do not: the marker must never claim data flushed when it did not.
func TestSpoolDrainMarker(t *testing.T) {
	dir := t.TempDir()
	sp := spool.NewSpool(filepath.Join(dir, "audit-spool.jsonl"))
	marker := filepath.Join(dir, spool.LastDrainMarkerName)

	if err := spoolAppend(sp, spoolRecord("cmd", "allow"), "s1", "snap-1", policy.Decision{Effect: policy.EffectAllow}); err != nil {
		t.Fatal(err)
	}
	if _, err := sp.Drain(context.Background(), NewClient("http://127.0.0.1:1"), "tok"); err == nil {
		t.Fatal("drain against down server should fail")
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Errorf("failed drain must not stamp the marker (stat err = %v)", err)
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"accepted":1}`))
	}))
	defer srv.Close()
	if _, err := sp.Drain(context.Background(), NewClient(srv.URL), "tok"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Errorf("successful drain must stamp the marker: %v", err)
	}

	// A drain with nothing pending re-confirms the spool is flushed.
	if err := os.Remove(marker); err != nil {
		t.Fatal(err)
	}
	if n, err := sp.Drain(context.Background(), NewClient(srv.URL), "tok"); err != nil || n != 0 {
		t.Fatalf("empty drain = %d, %v", n, err)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Errorf("empty successful drain must stamp the marker: %v", err)
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}
