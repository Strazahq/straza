package service

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"log/slog"
	"maps"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/strazahq/straza/internal/agentguard"
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

// TestToolRecordKeepsToolInput pins what a tool record says about a call
// beyond its kind. It keeps the arguments an mcp.call or an unclassified
// tool carried on its event, at most agentguard.ArgumentsMax bytes of them,
// shortened so the text stays JSON, with the hash of the whole text. It
// keeps the input the hook sent beside a classified tool's event, with the
// hook's hash when the hook shortened it, and the harness's own name of
// that tool. A call the harness routes through the Straza gateway keeps
// none, because the gateway's own record holds its arguments and masks a
// draft, which this record would not, and neither does a call of the
// gateway's drafting tools under another server name.
func TestToolRecordKeepsToolInput(t *testing.T) {
	long := `{"q":"` + strings.Repeat("é", agentguard.ArgumentsMax) + `"}`
	rows := []struct {
		name    string
		ev      policy.Event
		input   *localapi.ToolInput
		proxied bool
		args    string // "" means the record carries no arguments
		hash    string // "" means the record says nothing was cut
		native  string
	}{
		{"an unclassified tool keeps its arguments",
			policy.Event{Kind: policy.EventToolPre, Tool: policy.ToolOther, ToolName: "LSP", Args: []byte(`{"operation":"hover"}`)},
			nil, false, `{"operation":"hover"}`, "", ""},
		{"an observed call with no arguments says so",
			policy.Event{Kind: policy.EventToolPre, Tool: policy.ToolOther, ToolName: "LSP", Args: []byte(`{}`)},
			nil, false, `{}`, "", ""},
		{"arguments past the bound are shortened and stay JSON",
			policy.Event{Kind: policy.EventToolPre, Tool: policy.ToolOther, ToolName: "LSP", Args: []byte(long)},
			nil, false, long, sha(long), ""},
		{"arguments that were not observed write no field",
			policy.Event{Kind: policy.EventToolPre, Tool: policy.ToolOther, ToolName: "LSP"},
			nil, false, "", "", ""},
		{"a call to a server the harness runs keeps the text it typed",
			policy.Event{Kind: policy.EventToolPre, Tool: policy.ToolMCPCall, App: "claude-in-chrome", ToolName: "form_input", Args: []byte(`{"ref":"e12","value":"hunter2"}`)},
			nil, false, `{"ref":"e12","value":"hunter2"}`, "", ""},
		{"a call the gateway serves keeps none",
			policy.Event{Kind: policy.EventToolPre, Tool: policy.ToolMCPCall, App: "straza", ToolName: "draft_submit", Args: []byte(`{"documents":["token: x"]}`)},
			nil, true, "", "", ""},
		{"a drafting tool under another server name keeps none",
			policy.Event{Kind: policy.EventToolPre, Tool: policy.ToolMCPCall, App: "gw", ToolName: "straza__draft_submit", Args: []byte(`{"documents":["token: x"]}`)},
			nil, false, "", "", ""},
		{"a drafting tool a dialect could not split keeps none",
			policy.Event{Kind: policy.EventToolPre, Tool: policy.ToolOther, ToolName: "mcp_gw_straza__approval_request", Args: []byte(`{"action":{"args":{"documents":["token: x"]}}}`)},
			nil, false, "", "", ""},
		{"a classified tool keeps the input the hook sent",
			policy.Event{Kind: policy.EventToolPre, Tool: policy.ToolFileRead, NativeTool: "Skill"},
			&localapi.ToolInput{Text: `{"skill":"deploy"}`}, false, `{"skill":"deploy"}`, "", "Skill"},
		{"an input the hook cut keeps the hook's hash",
			policy.Event{Kind: policy.EventToolPre, Tool: policy.ToolFileWrite, NativeTool: "Write"},
			&localapi.ToolInput{Text: `{"content":"abc`, Hash: "sha256:" + strings.Repeat("ab", 32)}, false, `{"content":"abc`, "sha256:" + strings.Repeat("ab", 32), "Write"},
		{"a hash that is not a SHA-256 is dropped",
			policy.Event{Kind: policy.EventToolPre, Tool: policy.ToolFileWrite, NativeTool: "Write"},
			&localapi.ToolInput{Text: `{"content":"abc"}`, Hash: "sha256:" + strings.Repeat("zz", 32)}, false, `{"content":"abc"}`, "", "Write"},
		{"an input longer than the hook sends is cut again",
			policy.Event{Kind: policy.EventToolPre, Tool: policy.ToolFileWrite, NativeTool: "Write"},
			&localapi.ToolInput{Text: long, Hash: "sha256:" + strings.Repeat("ab", 32)}, false, long, sha(long), "Write"},
		{"the event's arguments win over an input beside it",
			policy.Event{Kind: policy.EventToolPre, Tool: policy.ToolOther, ToolName: "LSP", Args: []byte(`{"operation":"hover"}`)},
			&localapi.ToolInput{Text: `{"other":1}`}, false, `{"operation":"hover"}`, "", ""},
		{"a classified tool whose input was not sent names the harness's tool",
			policy.Event{Kind: policy.EventToolPre, Tool: policy.ToolFileRead, NativeTool: "Skill"},
			nil, false, "", "", "Skill"},
	}
	for _, row := range rows {
		t.Run(row.name, func(t *testing.T) {
			req := localapi.DecideRequest{Harness: localapi.Harness{Name: "claude-code"}, Event: row.ev, ToolInput: row.input, GatewayProxied: row.proxied}
			d := toolRecord(liveSession(), "snap-1", req, policy.Decision{Effect: policy.EffectAllow})
			got, has := d["arguments"].(string)
			if has != (row.args != "") || len(got) > agentguard.ArgumentsMax {
				t.Fatalf("arguments kept %d bytes (present %v), want at most %d bytes of the %d sent", len(got), has, agentguard.ArgumentsMax, len(row.args))
			}
			_, cut := d["argumentsTruncated"]
			if cut != (row.hash != "") || (cut && (d["argumentsTruncated"] != true || d["argumentsHash"] != row.hash)) || (!cut && d["argumentsHash"] != nil) {
				t.Fatalf("arguments fields = %v / %v with %d bytes kept, want hash %q", d["argumentsTruncated"], d["argumentsHash"], len(got), row.hash)
			}
			if short := len(row.args) <= agentguard.ArgumentsMax; (short && got != row.args) || (!short && (!json.Valid([]byte(got)) || len(got) < agentguard.ArgumentsMax-3)) {
				t.Fatalf("arguments kept %d of %d bytes, want them whole within the bound and shortened JSON at the bound past it", len(got), len(row.args))
			}
			if native, _ := d["nativeTool"].(string); native != row.native {
				t.Fatalf("nativeTool = %q, want %q", native, row.native)
			}
			if _, has := d["nativeTool"]; has != (row.native != "") {
				t.Fatalf("nativeTool present = %v, want it only for a classified tool with a name", has)
			}
		})
	}
}

// TestToolRecordMatchesTheSpecExamples pins the tool record examples of
// spec/events revisions 64 and 65 to the record the client writes, as
// strazad's own test pins them to its record, so the two producers cannot
// drift apart or away from the spec. The examples show a stored record,
// which carries the user the reference producer binds at upload. The
// arguments of an mcp.call or an unclassified tool reach the record on the
// event, and a classified tool's beside it.
func TestToolRecordMatchesTheSpecExamples(t *testing.T) {
	for _, name := range []string{"valid-audit-tool-other-arguments.json", "valid-audit-tool-native-named.json",
		"valid-audit-tool-classified-arguments.json", "valid-audit-tool-mcp-arguments.json"} {
		raw, err := os.ReadFile(filepath.Join("..", "..", "..", "spec", "events", "examples", name))
		if err != nil {
			t.Fatal(err)
		}
		var ce struct {
			Data map[string]any `json:"data"`
		}
		if err := json.Unmarshal(raw, &ce); err != nil {
			t.Fatal(err)
		}
		want := ce.Data
		str := func(k string) string { s, _ := want[k].(string); return s }
		req := localapi.DecideRequest{Harness: localapi.Harness{Name: "claude-code"}, Event: policy.Event{Kind: str("event"),
			Tool: str("tool"), App: str("app"), ToolName: str("toolName"), NativeTool: str("nativeTool"), Workspace: str("workspace")}}
		if args, ok := want["arguments"].(string); ok && (req.Event.Tool == policy.ToolOther || req.Event.Tool == policy.ToolMCPCall) {
			req.Event.Args = []byte(args)
		} else if ok {
			req.ToolInput = &localapi.ToolInput{Text: args}
		}
		got := toolRecord(liveSession(), "snap-1", req, policy.Decision{Effect: policy.EffectAllow})
		got["user"] = want["user"]
		gotKeys, wantKeys := slices.Sorted(maps.Keys(got)), slices.Sorted(maps.Keys(want))
		if !slices.Equal(gotKeys, wantKeys) {
			t.Errorf("%s: the record's fields are %v, the example's %v", name, gotKeys, wantKeys)
		}
		for _, k := range []string{"event", "tool", "toolName", "nativeTool", "arguments", "command", "app", "workspace"} {
			if got[k] != want[k] {
				t.Errorf("%s: %s = %v, the example has %v", name, k, got[k], want[k])
			}
		}
	}
}
