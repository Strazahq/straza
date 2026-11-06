package service

import (
	"encoding/binary"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/strazahq/straza/internal/agentguard/localapi"
	"github.com/strazahq/straza/internal/policy"
)

// TestToolRecordCapsCommandAndPaths pins what a tool record keeps of the
// command and the paths: at most toolTextMax bytes of each, cut on a
// character boundary, and, only when cut, <field>Truncated and the
// <field>Hash of the whole value, the way a captured turn keeps its text.
// The paths' hash puts each path's length before it, so two different
// lists never hash alike, and their bound counts one byte past each path,
// so many short paths are cut too.
func TestToolRecordCapsCommandAndPaths(t *testing.T) {
	overCommand := "x" + strings.Repeat("é", toolTextMax)
	p1, p2 := "/"+strings.Repeat("a", 40<<10), "/"+strings.Repeat("é", 20<<10)
	empties := make([]string, toolTextMax+10)
	rows := []struct {
		name      string
		command   string
		paths     []string
		cmdCut    bool
		pathsCut  bool
		wantPaths int // paths kept, -1 for all
	}{
		{"a command at the bound stays whole", strings.Repeat("a", toolTextMax), nil, false, false, -1},
		{"a command past the bound is cut on a character boundary", overCommand, nil, true, false, -1},
		{"paths within the bound stay whole", "ls", []string{"/a", "/b"}, false, false, -1},
		{"paths past the bound keep a cut prefix", "ls", []string{p1, p2, "/c"}, false, true, 2},
		{"many empty paths are cut too", "ls", empties, false, true, toolTextMax},
	}
	for _, row := range rows {
		t.Run(row.name, func(t *testing.T) {
			req := localapi.DecideRequest{Harness: localapi.Harness{Name: "claude-code"},
				Event: policy.Event{Kind: policy.EventToolPre, Tool: policy.ToolShellExec, Command: row.command, Paths: row.paths}}
			d := toolRecord(liveSession(), "snap-1", req, policy.Decision{Effect: policy.EffectAllow})
			cmd, _ := d["command"].(string)
			if !utf8.ValidString(cmd) || !strings.HasPrefix(row.command, cmd) || len(cmd) > toolTextMax {
				t.Fatalf("command kept %d bytes, want a valid prefix of at most %d", len(cmd), toolTextMax)
			}
			_, cmdCut := d["commandTruncated"]
			if cmdCut != row.cmdCut || (cmdCut && (d["commandTruncated"] != true || d["commandHash"] != sha(row.command) || len(cmd) < toolTextMax-3)) ||
				(!cmdCut && (cmd != row.command || d["commandHash"] != nil)) {
				t.Fatalf("command fields = %v / %v with %d bytes kept, want cut %v with the whole command's hash",
					d["commandTruncated"], d["commandHash"], len(cmd), row.cmdCut)
			}
			if row.paths == nil {
				return
			}
			paths, _ := d["paths"].([]string)
			used, whole := 0, ""
			for i, p := range paths {
				used += len(p) + 1
				if !utf8.ValidString(p) || !strings.HasPrefix(row.paths[i], p) {
					t.Fatalf("path %d kept %q, want a valid prefix of the path sent", i, p)
				}
			}
			for _, p := range row.paths {
				whole += string(binary.BigEndian.AppendUint64(nil, uint64(len(p)))) + p
			}
			_, pathsCut := d["pathsTruncated"]
			if pathsCut != row.pathsCut || used > toolTextMax || (row.wantPaths >= 0 && len(paths) != row.wantPaths) ||
				(pathsCut && d["pathsHash"] != sha(whole)) || (!pathsCut && (len(paths) != len(row.paths) || d["pathsHash"] != nil)) {
				t.Fatalf("paths kept %d (%d bytes) with cut %v and hash %v, want cut %v, at most %d bytes and the whole list's hash",
					len(paths), used, d["pathsTruncated"], d["pathsHash"], row.pathsCut, toolTextMax)
			}
		})
	}
}

// TestDecideAndCaptureBoundEventFields pins the bound on the other strings
// a record copies from a request: at maxEventField each reaches the
// credential half, and one byte more is refused with the sentence that
// names the field, before the service decides or records anything.
func TestDecideAndCaptureBoundEventFields(t *testing.T) {
	long := func(n int) string { return strings.Repeat("x", n) }
	h := localapi.Harness{Name: "claude-code"}
	decide := func(set func(*localapi.DecideRequest, string)) func(string) any {
		return func(v string) any {
			req := localapi.DecideRequest{Harness: h, Event: policy.Event{Kind: policy.EventToolPre, Tool: policy.ToolShellExec}}
			set(&req, v)
			return req
		}
	}
	capture := func(set func(*localapi.CaptureRequest, string)) func(string) any {
		return func(v string) any {
			req := localapi.CaptureRequest{Harness: h, Kind: localapi.CapturePrompt, Text: "hi"}
			set(&req, v)
			return req
		}
	}
	rows := []struct {
		route, field string
		body         func(string) any
	}{
		{localapi.RouteDecide, "tool", decide(func(r *localapi.DecideRequest, v string) { r.Event.Tool = v })},
		{localapi.RouteDecide, "app", decide(func(r *localapi.DecideRequest, v string) { r.Event.App = v })},
		{localapi.RouteDecide, "tool name", decide(func(r *localapi.DecideRequest, v string) { r.Event.ToolName = v })},
		{localapi.RouteDecide, "workspace", decide(func(r *localapi.DecideRequest, v string) { r.Event.Workspace = v })},
		{localapi.RouteDecide, "agent type", decide(func(r *localapi.DecideRequest, v string) { r.AgentType = v })},
		{localapi.RouteDecide, "agent id", decide(func(r *localapi.DecideRequest, v string) { r.AgentID = v })},
		{localapi.RouteCapture, "agent type", capture(func(r *localapi.CaptureRequest, v string) { r.AgentType = v })},
		{localapi.RouteCapture, "agent id", capture(func(r *localapi.CaptureRequest, v string) { r.AgentID = v })},
	}
	for _, row := range rows {
		t.Run(row.route+"/"+row.field, func(t *testing.T) {
			f := &apiDouble{sessionErr: &Refused{Status: http.StatusForbidden, Sentence: notEnrolledSentence}}
			a, _ := testAPI(f, false)
			if w := serveAs(a, "1000", http.MethodPost, row.route, row.body(long(maxEventField))); w.Code != http.StatusForbidden {
				t.Fatalf("a %s at the bound = %d %q, want it to reach the credential half", row.field, w.Code, w.Body.String())
			}
			w := serveAs(a, "1000", http.MethodPost, row.route, row.body(long(maxEventField+1)))
			if w.Code != http.StatusBadRequest || refusalOf(t, w) != fmt.Sprintf(harnessFieldTooLong, row.field, maxEventField+1, maxEventField) {
				t.Fatalf("a %s past the bound = %d %q, want the refusal that names it", row.field, w.Code, w.Body.String())
			}
			if len(f.recorded()) != 0 {
				t.Fatal("a refused request was recorded")
			}
		})
	}
}

// TestReadRefusesBodyNotUTF8 pins the read of every local API body: one
// that is not UTF-8 is refused before it is decoded, because the decoder
// would turn each stray byte into a three-byte replacement character and
// triple what the call holds. The same text escaped as JSON reaches the
// credential half.
func TestReadRefusesBodyNotUTF8(t *testing.T) {
	for _, row := range []struct {
		name string
		body string
		code int
	}{
		{"stray bytes", `{"harness":{"name":"claude-code"},"event":{"kind":"tool.pre","tool":"shell.exec","command":"ls ` + "\xff\xfe" + `"}}`, http.StatusBadRequest},
		{"escaped replacement characters", `{"harness":{"name":"claude-code"},"event":{"kind":"tool.pre","tool":"shell.exec","command":"ls �"}}`, http.StatusForbidden},
	} {
		t.Run(row.name, func(t *testing.T) {
			f := &apiDouble{sessionErr: &Refused{Status: http.StatusForbidden, Sentence: notEnrolledSentence}}
			a, _ := testAPI(f, false)
			w := serveAs(a, "1000", http.MethodPost, localapi.RouteDecide, row.body)
			if w.Code != row.code {
				t.Fatalf("answer = %d %q, want %d", w.Code, w.Body.String(), row.code)
			}
			if row.code == http.StatusBadRequest && refusalOf(t, w) != "The Straza background service could not read the request (the body is not UTF-8). "+
				"Update Straza so the client and the service are the same version." {
				t.Fatalf("refusal = %q", refusalOf(t, w))
			}
			if len(f.sessionCalls) != 0 && row.code == http.StatusBadRequest {
				t.Fatal("a body that is not UTF-8 reached the credential half")
			}
		})
	}
}

// TestRefusedRequestsAreNoted pins that a decide or capture the service
// refuses before it can record it, for its size, its encoding, its JSON or
// a field, is one note in the caller's loss ledger, and that a capture it
// records is none.
func TestRefusedRequestsAreNoted(t *testing.T) {
	h := localapi.Harness{Name: "claude-code"}
	rows := []struct {
		name, route string
		body        any
		code        int
		note        string
	}{
		{"a capture over the body bound", localapi.RouteCapture,
			localapi.CaptureRequest{Harness: h, Kind: localapi.CapturePrompt, Text: strings.Repeat("x", textBodyMax)}, http.StatusRequestEntityTooLarge, "capture"},
		{"a capture that is not JSON", localapi.RouteCapture, "{not json", http.StatusBadRequest, "capture"},
		{"a capture that is not UTF-8", localapi.RouteCapture, `{"harness":{"name":"claude-code"},"kind":"prompt","text":"` + "\xff" + `"}`, http.StatusBadRequest, "capture"},
		{"a capture of an unknown kind", localapi.RouteCapture, localapi.CaptureRequest{Harness: h, Kind: "summary", Text: "hi"}, http.StatusBadRequest, "capture"},
		{"a capture with no harness", localapi.RouteCapture, localapi.CaptureRequest{Kind: localapi.CapturePrompt, Text: "hi"}, http.StatusBadRequest, "capture"},
		{"a capture with a field past its bound", localapi.RouteCapture,
			localapi.CaptureRequest{Harness: h, Kind: localapi.CapturePrompt, Text: "hi", AgentID: strings.Repeat("x", maxEventField+1)}, http.StatusBadRequest, "capture"},
		{"a decide that is not JSON", localapi.RouteDecide, "{not json", http.StatusBadRequest, "decide"},
		{"a decide with a field past its bound", localapi.RouteDecide,
			localapi.DecideRequest{Harness: h, Event: policy.Event{Kind: policy.EventToolPre, Workspace: strings.Repeat("x", maxEventField+1)}}, http.StatusBadRequest, "decide"},
		{"a capture it records", localapi.RouteCapture, localapi.CaptureRequest{Harness: h, Kind: localapi.CapturePrompt, Text: "hi"}, http.StatusNoContent, ""},
	}
	for _, row := range rows {
		t.Run(row.name, func(t *testing.T) {
			f := gateDouble(t, 900, time.Hour, Renewal{})
			l := &ledgerDouble{apiDouble: f}
			a := newAPI(l, Options{Log: slog.New(slog.DiscardHandler)})
			if w := serveAs(a, "1000", http.MethodPost, row.route, row.body); w.Code != row.code {
				t.Fatalf("answer = %d %.200q, want %d", w.Code, w.Body.String(), row.code)
			}
			want := ""
			if row.note != "" {
				want = "1000 " + row.note
			}
			l.mu.Lock()
			defer l.mu.Unlock()
			if got := strings.Join(l.refused, ", "); got != want {
				t.Fatalf("ledger notes = %q, want %q", got, want)
			}
		})
	}
}
