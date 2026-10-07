package server

import (
	"net/http"
	"strings"
	"testing"
)

// TestPolicyEventSupport pins GET /v1/admin/policies/event-support: the
// served harness support matrix. One source of truth, assembled
// from the embedded adapters, so the console never hard-codes which harness
// emits which event.
func TestPolicyEventSupport(t *testing.T) {
	t.Parallel()
	app, base := testApp(t)
	user := seedIdentity(t, app)
	grantAdmin(t, app, user.ID)
	adminTok, _ := checkinToken(t, app, base)

	if code := adminReq(t, "GET", base+"/v1/admin/policies/event-support", "", nil, nil); code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated = %d, want 401", code)
	}

	var out struct {
		Events []struct {
			Kind      string   `json:"kind"`
			Blocking  bool     `json:"blocking"`
			Harnesses []string `json:"harnesses"`
		} `json:"events"`
		Harnesses []string `json:"harnesses"`
	}
	if code := adminReq(t, "GET", base+"/v1/admin/policies/event-support", adminTok, nil, &out); code != http.StatusOK {
		t.Fatalf("event-support = %d, want 200", code)
	}
	wantOrder := []string{
		"session.start", "prompt.submit", "tool.pre", "tool.post",
		"permission.request", "subagent.start", "subagent.stop",
		"session.end", "compact.pre",
	}
	if len(out.Events) != len(wantOrder) {
		t.Fatalf("events = %d rows, want %d", len(out.Events), len(wantOrder))
	}
	for i, k := range wantOrder {
		if out.Events[i].Kind != k {
			t.Fatalf("events[%d].kind = %q, want %q (engine const order)", i, out.Events[i].Kind, k)
		}
	}
	all := []string{"claude-code", "codex", "gemini", "python-sdk"}
	if got := strings.Join(out.Harnesses, ","); got != strings.Join(all, ",") {
		t.Errorf("harnesses = %v, want %v", out.Harnesses, all)
	}
	rows := map[string]struct {
		blocking  bool
		harnesses []string
	}{}
	for _, e := range out.Events {
		rows[e.Kind] = struct {
			blocking  bool
			harnesses []string
		}{e.Blocking, e.Harnesses}
	}
	if r := rows["subagent.start"]; strings.Join(r.harnesses, ",") != "claude-code,codex" {
		t.Errorf("subagent.start harnesses = %v, want claude-code+codex", r.harnesses)
	}
	if r := rows["compact.pre"]; strings.Join(r.harnesses, ",") != "claude-code" {
		t.Errorf("compact.pre harnesses = %v, want claude-code only", r.harnesses)
	}
	if r := rows["tool.pre"]; strings.Join(r.harnesses, ",") != strings.Join(all, ",") {
		t.Errorf("tool.pre harnesses = %v, want all four", r.harnesses)
	}
	// Only tool.pre and permission.request may block (spec/hook-profile).
	for _, e := range out.Events {
		wantBlocking := e.Kind == "tool.pre" || e.Kind == "permission.request"
		if e.Blocking != wantBlocking {
			t.Errorf("%s blocking = %v, want %v", e.Kind, e.Blocking, wantBlocking)
		}
	}
}

// eventsYAML builds a one-rule set for the coverage-advisory table.
func eventsYAML(rule string) string {
	return `apiVersion: straza.dev/v1beta1
kind: PolicySet
metadata: { name: ev-probe }
spec:
  priority: 100
  match: { roles: [dev] }
  rules:
` + rule
}

// TestPolicyEventsAdvisories pins the events-never-fire advisory:
// class S, fired at validate when a rule's event set cannot fire for some
// mapped harness, or excludes the only event the MCP gateway evaluates.
func TestPolicyEventsAdvisories(t *testing.T) {
	t.Parallel()
	app, base := testApp(t)
	user := seedIdentity(t, app)
	grantAdmin(t, app, user.ID)
	adminTok, _ := checkinToken(t, app, base)

	type adv struct {
		Code     string `json:"code"`
		Severity string `json:"severity"`
		Rule     string `json:"rule"`
		Text     string `json:"text"`
	}
	validate := func(t *testing.T, yaml string) (advs []adv, warnings []string) {
		t.Helper()
		var out struct {
			OK         bool     `json:"ok"`
			Warnings   []string `json:"warnings"`
			Advisories []adv    `json:"advisories"`
		}
		if code := yamlReq(t, http.MethodPost, base+"/v1/admin/policies/validate", adminTok, yaml, &out); code != http.StatusOK {
			t.Fatalf("validate = %d, want 200", code)
		}
		if !out.OK {
			t.Fatalf("validate not ok")
		}
		return out.Advisories, out.Warnings
	}

	t.Run("subagent-only rule names the uncovered harnesses", func(t *testing.T) {
		advs, _ := validate(t, eventsYAML(
			"    - id: audit-subagents\n      events: [subagent.start]\n      tools: [shell.exec]\n      effect: allow\n"))
		if len(advs) != 1 {
			t.Fatalf("advisories = %+v, want exactly 1", advs)
		}
		a := advs[0]
		if a.Code != "events-never-fire" || a.Severity != "warn" || a.Rule != "audit-subagents" {
			t.Errorf("advisory = %+v, want events-never-fire/warn/audit-subagents", a)
		}
		if !strings.Contains(a.Text, "its only events (subagent.start) never fire") ||
			!strings.Contains(a.Text, "gemini or python-sdk harnesses") ||
			!strings.Contains(a.Text, "sessions from those harnesses are not covered by this rule") {
			t.Errorf("advisory text = %q, want the decided H6 sentence", a.Text)
		}
	})

	t.Run("mcp rule excluding tool.pre never fires on the gateway", func(t *testing.T) {
		advs, _ := validate(t, eventsYAML(
			"    - id: mcp-post-audit\n      events: [tool.post]\n      tools: [mcp.call]\n      effect: allow\n"))
		if len(advs) != 1 {
			t.Fatalf("advisories = %+v, want exactly 1 (tool.post is mapped on every harness, so no gap arm)", advs)
		}
		a := advs[0]
		if a.Code != "events-never-fire" || a.Rule != "mcp-post-audit" {
			t.Errorf("advisory = %+v, want events-never-fire on mcp-post-audit", a)
		}
		if !strings.Contains(a.Text, "its events exclude tool.pre, the only event the MCP gateway evaluates") ||
			!strings.Contains(a.Text, "never fires for MCP calls") {
			t.Errorf("advisory text = %q, want the MCP gateway sentence", a.Text)
		}
	})

	t.Run("tool.pre rule draws nothing", func(t *testing.T) {
		advs, warnings := validate(t, eventsYAML(
			"    - id: plain\n      tools: [shell.exec]\n      command: { denyPatterns: [\"rm -rf *\"] }\n      effect: deny\n      reason: \"Straza: blocked\"\n"))
		if len(advs) != 0 || len(warnings) != 0 {
			t.Errorf("advisories = %+v warnings = %v, want none (default events = tool.pre)", advs, warnings)
		}
	})

	t.Run("a set containing tool.pre never gaps", func(t *testing.T) {
		advs, _ := validate(t, eventsYAML(
			"    - id: wide\n      events: [tool.pre, subagent.start]\n      tools: [shell.exec]\n      effect: allow\n"))
		if len(advs) != 0 {
			t.Errorf("advisories = %+v, want none (tool.pre fires on every harness)", advs)
		}
	})

	t.Run("combined with an A5 advisory, one list, twin matches", func(t *testing.T) {
		advs, warnings := validate(t, eventsYAML(
			"    - id: cert-gate\n      tools: [shell.exec]\n      effect: allow\n      require: { deviceCert: true }\n"+
				"    - id: audit-subagents\n      events: [subagent.stop]\n      tools: [shell.exec]\n      effect: allow\n"))
		if len(advs) != 2 {
			t.Fatalf("advisories = %+v, want 2 (require-unsatisfiable + events-never-fire)", advs)
		}
		codes := advs[0].Code + "," + advs[1].Code
		if !strings.Contains(codes, "require-unsatisfiable") || !strings.Contains(codes, "events-never-fire") {
			t.Errorf("codes = %s, want both classes", codes)
		}
		if len(warnings) != 2 || warnings[0] != advs[0].Text || warnings[1] != advs[1].Text {
			t.Errorf("warnings twin = %v, want the two advisory texts in order", warnings)
		}
	})
}
