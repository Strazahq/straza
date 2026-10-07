package server

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/strazahq/straza/internal/config"
)

// TestDecideRefusesUnknownKindAndTool pins the /v1/decide half of fail
// closed under the enterprise local default: an event kind
// outside the canonical set, a tool outside it and a blocking event with no
// tool are each denied with a sentence the person can act on, and each
// refusal lands on the audit chain exactly once as a straza.audit.tool
// record that names the person, the session, the event, the decision and
// the reason. A known observe kind still allows, the positive control.
func TestDecideRefusesUnknownKindAndTool(t *testing.T) {
	t.Parallel()
	app, base := testApp(t, func(c *config.Config) {
		c.Governance.LocalToolDefault = config.EffectDeny
	})
	kim := seedIdentity(t, app)
	token, session := checkinToken(t, app, base)

	const sendTools = "Send one of the known tools: file.edit, file.read, file.write, mcp.call, net.fetch, other, shell.exec, task.spawn."
	cases := []struct {
		name       string
		event      map[string]any
		wantEffect string
		wantReason string
	}{
		{
			name:       "unknown kind",
			event:      map[string]any{"kind": "lf-gi.made-up", "tool": "shell.exec", "command": "rm -rf /tmp/p-kind"},
			wantEffect: "deny",
			wantReason: `Straza: denied, because the event kind "lf-gi.made-up" is not one Straza knows and policy cannot judge it. ` +
				"Send one of the known kinds: compact.pre, permission.request, prompt.submit, session.end, session.start, subagent.start, subagent.stop, tool.post, tool.pre.",
		},
		{
			name:       "unknown tool under tool.pre",
			event:      map[string]any{"kind": "tool.pre", "tool": "lf-gi-made-up-tool", "command": "rm -rf /tmp/p-tool"},
			wantEffect: "deny",
			wantReason: `Straza: denied, because the tool "lf-gi-made-up-tool" is not one Straza knows and policy cannot judge it. ` + sendTools,
		},
		{
			name:       "no tool under tool.pre",
			event:      map[string]any{"kind": "tool.pre", "command": "rm -rf /tmp/p-no-tool"},
			wantEffect: "deny",
			wantReason: "Straza: denied, because this tool.pre event names no tool and policy cannot judge it. " + sendTools,
		},
		{
			name:       "control: known observe kind",
			event:      map[string]any{"kind": "session.end", "command": "p-control"},
			wantEffect: "allow",
		},
	}
	for _, tc := range cases {
		code, dec := decide(t, base, token, tc.event)
		if code != http.StatusOK || dec["effect"] != tc.wantEffect {
			t.Fatalf("%s: decide = %d %v, want %s", tc.name, code, dec, tc.wantEffect)
		}
		if reason, _ := dec["reason"].(string); reason != tc.wantReason {
			t.Errorf("%s: reason\n got %q\nwant %q", tc.name, reason, tc.wantReason)
		}
	}

	markers := make([]string, len(cases))
	for i, tc := range cases {
		markers[i] = `"command":"` + tc.event["command"].(string) + `"`
	}
	waitForCE(t, app, markers...)
	recs, err := app.store.Audit().List(context.Background(), 0, 1000)
	if err != nil {
		t.Fatal(err)
	}
	for i, tc := range cases {
		var hits []string
		for _, r := range recs {
			if strings.Contains(r.CE, markers[i]) {
				hits = append(hits, r.CE)
			}
		}
		if len(hits) != 1 {
			t.Errorf("%s: audit records = %d, want exactly 1", tc.name, len(hits))
			continue
		}
		var ce struct {
			Type string         `json:"type"`
			Data map[string]any `json:"data"`
		}
		if err := json.Unmarshal([]byte(hits[0]), &ce); err != nil {
			t.Fatalf("%s: audit record: %v", tc.name, err)
		}
		tool, _ := tc.event["tool"].(string)
		want := map[string]any{
			"user": kim.ID, "session": session, "event": tc.event["kind"], "tool": tool,
			"effect": tc.wantEffect, "reason": tc.wantReason,
		}
		if ce.Type != "straza.audit.tool" {
			t.Errorf("%s: record type = %q, want straza.audit.tool", tc.name, ce.Type)
		}
		for k, v := range want {
			if ce.Data[k] != v {
				t.Errorf("%s: record %s = %v, want %v", tc.name, k, ce.Data[k], v)
			}
		}
	}
}
