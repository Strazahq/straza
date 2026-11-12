package server

import (
	"context"
	"fmt"
	"net/http"
	"reflect"
	"testing"
	"time"

	"github.com/strazahq/straza/internal/audit"
	"github.com/strazahq/straza/internal/store"
)

// denialSeed is one straza.audit.tool record as the hook lane writes it.
type denialSeed struct {
	tool, app, toolName, harness, effect, rule string
}

// seedToolRecords chains the seeds in order, one minute apart from first.
func seedToolRecords(t *testing.T, app *App, first time.Time, seeds []denialSeed) {
	t.Helper()
	batch := time.Now().UnixNano()
	events := make([]store.ChainEvent, 0, len(seeds))
	for i, s := range seeds {
		id := fmt.Sprintf("ce-denial-%d-%03d", batch, i)
		events = append(events, store.ChainEvent{CEID: id, CE: fmt.Sprintf(
			`{"specversion":"1.0","id":%q,"type":"straza.audit.tool","source":"straza","time":%q,`+
				`"data":{"session":"s1","user":"u1","harness":%q,"event":"tool.pre","tool":%q,"app":%q,"toolName":%q,`+
				`"effect":%q,"ruleId":%q,"setName":"","reason":"Straza: denied","arguments":"{\"secret\":\"x\"}","snapshot":"snap-1"}}`,
			id, first.Add(time.Duration(i)*time.Minute).Format(time.RFC3339Nano), s.harness, s.tool, s.app, s.toolName, s.effect, s.rule)})
	}
	if _, err := app.store.Audit().AppendChained(context.Background(), events, audit.Genesis, audit.Link); err != nil {
		t.Fatalf("AppendChained: %v", err)
	}
}

// TestDefaultDenials drives GET /v1/admin/default-denials: it lists the
// tools of the decision window that no rule decided, MCP tools under the
// server Straza has not registered and harness tools by name, newest first.
// A tool a newer record allowed or a rule decided is left out, so is a
// registered server and the built-in one, and the answer carries no caller
// and no arguments. A seat with no grant on the policy area is refused.
func TestDefaultDenials(t *testing.T) {
	t.Parallel()
	app, base := testApp(t)
	user := seedIdentity(t, app)
	if code := adminReq(t, http.MethodGet, base+"/v1/admin/default-denials", loginDeviceFlow(t, base, "kim", "hunter2!"), nil, nil); code != http.StatusForbidden {
		t.Errorf("a user with no admin grant = %d, want 403", code)
	}
	grantAdmin(t, app, user.ID)
	idToken := loginDeviceFlow(t, base, "kim", "hunter2!")
	if _, err := app.store.Apps().Create(context.Background(), store.App{Name: "github", RuntimeKind: "remote"}); err != nil {
		t.Fatalf("register github: %v", err)
	}
	deny, chrome := "deny", "claude-in-chrome"
	seedToolRecords(t, app, time.Now().UTC().Add(-2*time.Hour), []denialSeed{
		{"mcp.call", chrome, "navigate", "claude-code/2.1.0", deny, ""},
		{"mcp.call", chrome, "form_input", "claude-code/2.1.0", deny, ""},
		{"mcp.call", chrome, "navigate", "codex", deny, ""},
		// A registered server's ungranted tool and the built-in server's are
		// role access's to decide, not a rule's.
		{"mcp.call", "github", "push_files", "claude-code", deny, ""},
		{"mcp.call", "straza", "draft_submit", "claude-code", deny, ""},
		{"other", "", "LSP", "claude-code/2.1.0", deny, ""},
		{"other", "", "", "python-sdk/0.4.0", deny, ""},
		// A rule's deny, a hold and a classified tool are not default denials.
		{"other", "", "refund_order", "python-sdk/0.4.0", deny, "hold-refund"},
		{"shell.exec", "", "", "claude-code", deny, ""},
		// A newer allow covers an earlier default deny of the same tool.
		{"other", "", "Skill", "claude-code", deny, ""},
		{"other", "", "Skill", "claude-code", "allow", "allow-skill"},
		{"mcp.call", chrome, "read_page", "claude-code", deny, ""},
		{"mcp.call", chrome, "read_page", "claude-code", "allow", "allow-chrome"},
		// An allow that names no rule covers nothing, because a client can
		// spool one for a call the gateway decides.
		{"other", "", "Probe", "", deny, ""},
		{"other", "", "Probe", "", "allow", ""},
	})

	var got struct {
		Since     time.Time `json:"since"`
		Truncated bool      `json:"truncated"`
		Servers   []struct {
			Server    string           `json:"server"`
			Harnesses []string         `json:"harnesses"`
			Count     int              `json:"count"`
			Tools     []map[string]any `json:"tools"`
		} `json:"servers"`
		Tools []map[string]any `json:"tools"`
	}
	if code := adminReq(t, http.MethodGet, base+"/v1/admin/default-denials", idToken, nil, &got); code != http.StatusOK {
		t.Fatalf("GET default-denials = %d", code)
	}
	if got.Since.IsZero() || got.Truncated {
		t.Errorf("since = %v, truncated = %v, want a window start and no cut", got.Since, got.Truncated)
	}
	if len(got.Servers) != 1 || got.Servers[0].Server != chrome || got.Servers[0].Count != 3 || !reflect.DeepEqual(got.Servers[0].Harnesses, []string{"claude-code", "codex"}) {
		t.Fatalf("servers = %+v, want claude-in-chrome alone with 3 denials from claude-code and codex", got.Servers)
	}
	names := func(tools []map[string]any) []string {
		out := []string{}
		for _, tool := range tools {
			out = append(out, fmt.Sprintf("%v x%v", tool["name"], tool["count"]))
			for _, k := range []string{"user", "arguments", "reason", "session"} {
				if _, ok := tool[k]; ok {
					t.Errorf("tool %v carries %s, which belongs to the audit area", tool["name"], k)
				}
			}
		}
		return out
	}
	if want := []string{"navigate x2", "form_input x1"}; !reflect.DeepEqual(names(got.Servers[0].Tools), want) {
		t.Errorf("tools of the server = %v, want %v", names(got.Servers[0].Tools), want)
	}
	if want := []string{"Probe x1", " x1", "LSP x1"}; !reflect.DeepEqual(names(got.Tools), want) {
		t.Errorf("harness tools = %v, want %v", names(got.Tools), want)
	}
}

// TestDefaultDenialCap pins that the scan tracks at most defaultDenialMax
// tools, whatever names a harness sends, and says so.
func TestDefaultDenialCap(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name  string
		names int
		cut   bool
	}{
		{"as many tools as the scan tracks", defaultDenialMax, false},
		{"more tools than the scan tracks", defaultDenialMax + 50, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tally := denialTally{groups: map[string]*defaultDenial{}}
			for i := 0; i < tc.names; i++ {
				var ce decisionCE
				ce.Type, ce.Data.Tool, ce.Data.ToolName, ce.Data.Effect = "straza.audit.tool", "other", fmt.Sprintf("tool-%d", i), "deny"
				tally.tally(ce, int64(i+1))
			}
			denials := make([]defaultDenial, 0, len(tally.groups))
			for _, d := range tally.groups {
				denials = append(denials, *d)
			}
			out := defaultDenialsOf(time.Time{}, denials, tally.cut, nil)
			if out.Truncated != tc.cut || len(out.Tools) != defaultDenialMax {
				t.Errorf("truncated = %v with %d tools, want %v with %d", out.Truncated, len(out.Tools), tc.cut, defaultDenialMax)
			}
		})
	}
	// A server whose records name no harness still answers a list.
	out := defaultDenialsOf(time.Time{}, []defaultDenial{{kind: "mcp.call", app: "x", tool: "t", harnesses: map[string]bool{}, count: 1, lastSeq: 1}}, false, nil)
	if len(out.Servers) != 1 || out.Servers[0].Harnesses == nil {
		t.Errorf("servers = %+v, want one server with an empty harness list, never null", out.Servers)
	}
}
