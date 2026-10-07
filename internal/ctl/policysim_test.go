package ctl

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/strazahq/straza/internal/policy"
)

// TestSimulatePolicyWire pins the request shape of POST
// /v1/admin/policies/simulate: field names, omission of empty subject fields,
// and the draft passthrough. The console sends the identical shape, so this
// pin keeps the two from drifting.
func TestSimulatePolicyWire(t *testing.T) {
	rec := &wireRecorder{}
	srv := httptest.NewServer(http.HandlerFunc(rec.handle))
	t.Cleanup(srv.Close)
	c := loggedInClient(t, srv.URL)
	canned := `{"active":{"effect":"deny","ruleId":"no-rm-rf","setName":"dev-guardrails","reason":"x"},` +
		`"subject":{"user":"bob","roles":["dev"],"attestation":""},"snapshot":"4f2a"}`

	rec.script(canned)
	req := SimulateRequest{
		Event:   policy.Event{Kind: "tool.pre", Tool: "shell.exec", Command: "rm -rf /tmp/x"},
		Subject: SimulateSubject{User: "bob"},
		Draft:   "apiVersion: straza.dev/v1beta1",
	}
	res, err := c.SimulatePolicy(context.Background(), req)
	if err != nil {
		t.Fatalf("SimulatePolicy: %v", err)
	}
	if res.Active.RuleID != "no-rm-rf" || res.Snapshot != "4f2a" || res.Draft != nil {
		t.Errorf("decoded result = %+v", res)
	}
	method, uri, _, body := rec.last()
	if method != http.MethodPost || uri != "/v1/admin/policies/simulate" {
		t.Errorf("wire = %s %s, want POST /v1/admin/policies/simulate", method, uri)
	}
	var got map[string]any
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("request body not JSON: %v", err)
	}
	ev, _ := got["event"].(map[string]any)
	if ev["kind"] != "tool.pre" || ev["tool"] != "shell.exec" || ev["command"] != "rm -rf /tmp/x" {
		t.Errorf("event on the wire = %v", ev)
	}
	sub, _ := got["subject"].(map[string]any)
	if sub["user"] != "bob" {
		t.Errorf("subject.user = %v", sub["user"])
	}
	if _, has := sub["roles"]; has {
		t.Errorf("empty roles must be omitted, got %v", sub["roles"])
	}
	if _, has := sub["attestation"]; has {
		t.Errorf("empty attestation must be omitted")
	}
	if got["draft"] != "apiVersion: straza.dev/v1beta1" {
		t.Errorf("draft = %v", got["draft"])
	}

	// Roles-only hypothetical: user omitted, roles present.
	rec.script(canned)
	req = SimulateRequest{
		Event:   policy.Event{Kind: "tool.pre"},
		Subject: SimulateSubject{Roles: []string{"dev", "ops"}, Attestation: "managed"},
	}
	if _, err := c.SimulatePolicy(context.Background(), req); err != nil {
		t.Fatalf("SimulatePolicy: %v", err)
	}
	_, _, _, body = rec.last()
	got = nil
	_ = json.Unmarshal(body, &got)
	sub, _ = got["subject"].(map[string]any)
	if _, has := sub["user"]; has {
		t.Errorf("empty user must be omitted")
	}
	if sub["attestation"] != "managed" {
		t.Errorf("attestation = %v", sub["attestation"])
	}
	if _, has := got["draft"]; has {
		t.Errorf("empty draft must be omitted")
	}
}

// TestOverviewLiteWire pins the overview read the rendering degrades without.
func TestOverviewLiteWire(t *testing.T) {
	rec := &wireRecorder{}
	srv := httptest.NewServer(http.HandlerFunc(rec.handle))
	t.Cleanup(srv.Close)
	c := loggedInClient(t, srv.URL)
	rec.script(`{"profile":"enterprise","snapshot_id":"4f2a9c01d7e6b3a8","extra":1}`)
	ov, err := c.OverviewLite(context.Background())
	if err != nil {
		t.Fatalf("OverviewLite: %v", err)
	}
	if ov.Profile != "enterprise" || ov.SnapshotID != "4f2a9c01d7e6b3a8" {
		t.Errorf("decoded overview = %+v", ov)
	}
	method, uri, _, _ := rec.last()
	if method != http.MethodGet || uri != "/v1/admin/overview" {
		t.Errorf("wire = %s %s, want GET /v1/admin/overview", method, uri)
	}
}

// Golden fixtures reused across rendering cases: a dev-guardrails rule set
// kept inline, so the pinned output does not depend on any seed file.
var (
	simSnap = "4f2a9c01d7e6b3a8"
	simLive = SimContext{Profile: "enterprise", LiveSnapshot: "4f2a9c01d7e6b3a8"}
	denyRm  = policy.Decision{
		Effect: "deny", RuleID: "no-rm-rf", SetName: "dev-guardrails",
		Reason: "Straza: destructive command blocked for role dev",
	}
	subjBob = policy.Subject{User: "bob", Roles: []string{"dev"}}
)

// TestRenderSimulationGolden pins the terminal rendering byte for byte. The
// sentences mirror whyOf in web/ui/src/lib/audit-words.ts. A diff here means
// the two surfaces stopped telling one story, so fix the wording in BOTH
// places or revert.
func TestRenderSimulationGolden(t *testing.T) {
	ticketAllow := policy.Decision{
		Effect: "allow", RuleID: "dev-deploy-ticket", SetName: "dev-guardrails",
		Reason: "Straza: deploy needs a day-scale approval ticket. Request it, a human decides, re-run to consume the grant",
		Approve: &policy.ApproveSpec{
			Roles: []string{"sec-approvers", "straza-admin"}, Class: "ticket",
			TicketTTLSeconds: 86400, GrantTTLSeconds: 3600,
		},
	}
	holdAllow := policy.Decision{
		Effect: "allow", RuleID: "dev-echo-approval-showcase", SetName: "dev-guardrails",
		Reason: "Straza: echo held for approval, the 2-minute showcase (approve on your phone)",
		Approve: &policy.ApproveSpec{
			Roles: []string{"sec-approvers", "straza-admin"}, TimeoutSeconds: 120,
		},
	}
	localAllow := policy.Decision{Effect: "allow", RuleID: "dev-local-tools", SetName: "dev-guardrails"}

	tests := []struct {
		name string
		res  SimulateResult
		now  SimContext
		want string
	}{
		{
			name: "rule deny, K1 first block",
			res:  SimulateResult{Active: denyRm, Subject: subjBob, Snapshot: simSnap},
			now:  simLive,
			want: `This call would be denied.
Decided by rule no-rm-rf in policy dev-guardrails: Straza: destructive command blocked for role dev.
Simulated against the policies live right now, your file included if you passed one. This is today's answer, not a replay of any past decision.

subject   bob · roles dev · attestation none
snapshot  4f2a9c01d7e6b3a8 (live)
wire      effect=deny · ruleId=no-rm-rf · setName=dev-guardrails · snapshot=4f2a9c01
note      this verdict applies once this snapshot reaches the client;
          straza doctor proves a client is governed
`,
		},
		{
			name: "ticket-gated allow with gate line, K1 second block",
			res:  SimulateResult{Active: ticketAllow, Subject: subjBob, Snapshot: simSnap},
			now:  simLive,
			want: `This call would be allowed.
gate      needs approval: day-scale approval ticket, deciders
          sec-approvers or straza-admin, the approval is good for 1 h
Decided by rule dev-deploy-ticket in policy dev-guardrails: Straza: deploy needs a day-scale approval ticket. Request it, a human decides, re-run to consume the grant.
Simulated against the policies live right now, your file included if you passed one. This is today's answer, not a replay of any past decision.

subject   bob · roles dev · attestation none
snapshot  4f2a9c01d7e6b3a8 (live)
wire      effect=allow · ruleId=dev-deploy-ticket · setName=dev-guardrails · snapshot=4f2a9c01
note      this verdict applies once this snapshot reaches the client;
          straza doctor proves a client is governed
`,
		},
		{
			name: "hold-gated allow",
			res:  SimulateResult{Active: holdAllow, Subject: subjBob, Snapshot: simSnap},
			now:  simLive,
			want: `This call would be allowed.
gate      needs approval: 2-minute approval hold, deciders sec-approvers
          or straza-admin
Decided by rule dev-echo-approval-showcase in policy dev-guardrails: Straza: echo held for approval, the 2-minute showcase (approve on your phone).
Simulated against the policies live right now, your file included if you passed one. This is today's answer, not a replay of any past decision.

subject   bob · roles dev · attestation none
snapshot  4f2a9c01d7e6b3a8 (live)
wire      effect=allow · ruleId=dev-echo-approval-showcase · setName=dev-guardrails · snapshot=4f2a9c01
note      this verdict applies once this snapshot reaches the client;
          straza doctor proves a client is governed
`,
		},
		{
			// A granted MCP call with no rule: the engine's own reason is the
			// decided-by sentence, never "allowed by the profile default".
			name: "no-rule allow by role access",
			res: SimulateResult{
				Active:   policy.Decision{Effect: "allow", Default: true, Reason: "allowed by role access. No policy rule gates this tool."},
				Subject:  subjBob,
				Snapshot: simSnap,
			},
			now: simLive,
			want: `This call would be allowed.
No policy matched this call. Allowed by role access. No policy rule gates this tool.
Simulated against the policies live right now, your file included if you passed one. This is today's answer, not a replay of any past decision.

subject   bob · roles dev · attestation none
snapshot  4f2a9c01d7e6b3a8 (live)
wire      effect=allow · ruleId=(none) · setName=(none) · snapshot=4f2a9c01
note      this verdict applies once this snapshot reaches the client;
          straza doctor proves a client is governed
`,
		},
		{
			name: "no-rule deny under enterprise with remedy, K3",
			res: SimulateResult{
				Active:   policy.Decision{Effect: "deny", Default: true},
				Subject:  policy.Subject{User: "alice", Roles: []string{"straza-admin"}},
				Snapshot: simSnap,
			},
			now: simLive,
			want: `This call would be denied.
No policy matched this call. Denied by the enterprise profile default: a call nothing allows is denied.
Simulated against the policies live right now, your file included if you passed one. This is today's answer, not a replay of any past decision.
To allow an MCP tool, list it in a role of its MCP server that alice holds, or create one with strazactl roles create <server>-<word> --app <server> --tools <tool> and assign it. It then runs unless a policy gates it. A local tool needs an allow rule. Test either change with a file: -f policy.yaml

subject   alice · roles straza-admin · attestation none
snapshot  4f2a9c01d7e6b3a8 (live)
wire      effect=deny · ruleId=(none) · setName=(none) · snapshot=4f2a9c01
note      this verdict applies once this snapshot reaches the client;
          straza doctor proves a client is governed
`,
		},
		{
			name: "no-rule allow default, no remedy",
			res: SimulateResult{
				Active:   policy.Decision{Effect: "allow", Default: true},
				Subject:  policy.Subject{Roles: []string{"dev"}},
				Snapshot: simSnap,
			},
			now: SimContext{Profile: "standalone", LiveSnapshot: simSnap},
			want: `This call would be allowed.
No policy matched this call. Allowed by the profile default: no policy matched.
Simulated against the policies live right now, your file included if you passed one. This is today's answer, not a replay of any past decision.

subject   roles dev · attestation none
snapshot  4f2a9c01d7e6b3a8 (live)
wire      effect=allow · ruleId=(none) · setName=(none) · snapshot=4f2a9c01
note      this verdict applies once this snapshot reaches the client;
          straza doctor proves a client is governed
`,
		},
		{
			// A mistyped --event: the engine's own refusal is the sentence,
			// never a profile default the standalone profile does not have,
			// and no remedy that no rule could deliver.
			name: "unknown kind names the known kinds, no remedy",
			res: SimulateResult{
				Active:   unknownKindDeny(t),
				Subject:  policy.Subject{Roles: []string{"dev"}},
				Snapshot: simSnap,
			},
			now: SimContext{Profile: "standalone", LiveSnapshot: simSnap},
			want: `This call would be denied.
No policy matched this call. Denied, because the event kind "PreToolUse" is not one Straza knows and policy cannot judge it. Send one of the known kinds: compact.pre, permission.request, prompt.submit, session.end, session.start, subagent.start, subagent.stop, tool.post, tool.pre.
Simulated against the policies live right now, your file included if you passed one. This is today's answer, not a replay of any past decision.

subject   roles dev · attestation none
snapshot  4f2a9c01d7e6b3a8 (live)
wire      effect=deny · ruleId=(none) · setName=(none) · snapshot=4f2a9c01
note      this verdict applies once this snapshot reaches the client;
          straza doctor proves a client is governed
`,
		},
		{
			// A mistyped --tool under the enterprise default: no rule can allow a
			// tool outside the taxonomy, so the engine's sentence that lists the
			// known tools replaces the remedy.
			name: "unknown tool under enterprise names the known tools, no remedy",
			res: SimulateResult{
				Active:   enterpriseDefault(t, policy.Event{Kind: policy.EventToolPre, Tool: "shel.exec", Command: "ls"}),
				Subject:  policy.Subject{Roles: []string{"dev"}},
				Snapshot: simSnap,
			},
			now: simLive,
			want: `This call would be denied.
No policy matched this call. Denied by the enterprise profile default: a call nothing allows is denied.
Simulated against the policies live right now, your file included if you passed one. This is today's answer, not a replay of any past decision.
Denied, because the tool "shel.exec" is not one Straza knows and policy cannot judge it. Send one of the known tools: file.edit, file.read, file.write, mcp.call, net.fetch, other, shell.exec, task.spawn.

subject   roles dev · attestation none
snapshot  4f2a9c01d7e6b3a8 (live)
wire      effect=deny · ruleId=(none) · setName=(none) · snapshot=4f2a9c01
note      this verdict applies once this snapshot reaches the client;
          straza doctor proves a client is governed
`,
		},
		{
			name: "no tool under enterprise names the known tools, no remedy",
			res: SimulateResult{
				Active:   enterpriseDefault(t, policy.Event{Kind: policy.EventToolPre}),
				Subject:  policy.Subject{Roles: []string{"dev"}},
				Snapshot: simSnap,
			},
			now: simLive,
			want: `This call would be denied.
No policy matched this call. Denied by the enterprise profile default: a call nothing allows is denied.
Simulated against the policies live right now, your file included if you passed one. This is today's answer, not a replay of any past decision.
Denied, because this tool.pre event names no tool and policy cannot judge it. Send one of the known tools: file.edit, file.read, file.write, mcp.call, net.fetch, other, shell.exec, task.spawn.

subject   roles dev · attestation none
snapshot  4f2a9c01d7e6b3a8 (live)
wire      effect=deny · ruleId=(none) · setName=(none) · snapshot=4f2a9c01
note      this verdict applies once this snapshot reaches the client;
          straza doctor proves a client is governed
`,
		},
		{
			// The engine's default deny of a known local tool: a rule can allow
			// it, so the remedy stays.
			name: "known local tool under enterprise keeps the remedy",
			res: SimulateResult{
				Active:   enterpriseDefault(t, policy.Event{Kind: policy.EventToolPre, Tool: policy.ToolShellExec, Command: "ls"}),
				Subject:  policy.Subject{Roles: []string{"dev"}},
				Snapshot: simSnap,
			},
			now: simLive,
			want: `This call would be denied.
No policy matched this call. Denied by the enterprise profile default: a call nothing allows is denied.
Simulated against the policies live right now, your file included if you passed one. This is today's answer, not a replay of any past decision.
To allow an MCP tool, list it in a role of its MCP server that the subject holds, or create one with strazactl roles create <server>-<word> --app <server> --tools <tool> and assign it. It then runs unless a policy gates it. A local tool needs an allow rule. Test either change with a file: -f policy.yaml

subject   roles dev · attestation none
snapshot  4f2a9c01d7e6b3a8 (live)
wire      effect=deny · ruleId=(none) · setName=(none) · snapshot=4f2a9c01
note      this verdict applies once this snapshot reaches the client;
          straza doctor proves a client is governed
`,
		},
		{
			name: "dual agree",
			res: SimulateResult{
				Active: denyRm, Draft: &denyRm,
				Subject: subjBob, Snapshot: simSnap,
			},
			now: simLive,
			want: `live    DENY   Decided by rule no-rm-rf in policy dev-guardrails: Straza: destructive command blocked for role dev.
file    DENY   Decided by rule no-rm-rf in policy dev-guardrails: Straza: destructive command blocked for role dev.

live and file agree: DENY
Simulated against the policies live right now, your file included if you passed one. This is today's answer, not a replay of any past decision.

subject   bob · roles dev · attestation none
snapshot  4f2a9c01d7e6b3a8 (live)
`,
		},
		{
			name: "dual differ, K2",
			res: SimulateResult{
				Active: localAllow, Draft: &denyRm,
				Subject: subjBob, Snapshot: simSnap,
			},
			now: simLive,
			want: `live    ALLOW  Decided by rule dev-local-tools in policy dev-guardrails.
file    DENY   Decided by rule no-rm-rf in policy dev-guardrails: Straza: destructive command blocked for role dev.

the live policy says ALLOW; this file says DENY
Simulated against the policies live right now, your file included if you passed one. This is today's answer, not a replay of any past decision.

subject   bob · roles dev · attestation none
snapshot  4f2a9c01d7e6b3a8 (live)
`,
		},
		{
			name: "overview unreachable degrades: generic profile, no live marker",
			res: SimulateResult{
				Active:  policy.Decision{Effect: "deny", Default: true},
				Subject: subjBob, Snapshot: simSnap,
			},
			now: SimContext{},
			want: `This call would be denied.
No policy matched this call. Denied by the profile default: no policy allowed this call.
Simulated against the policies live right now, your file included if you passed one. This is today's answer, not a replay of any past decision.
To allow an MCP tool, list it in a role of its MCP server that bob holds, or create one with strazactl roles create <server>-<word> --app <server> --tools <tool> and assign it. It then runs unless a policy gates it. A local tool needs an allow rule. Test either change with a file: -f policy.yaml

subject   bob · roles dev · attestation none
snapshot  4f2a9c01d7e6b3a8
wire      effect=deny · ruleId=(none) · setName=(none) · snapshot=4f2a9c01
note      this verdict applies once this snapshot reaches the client;
          straza doctor proves a client is governed
`,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var sb strings.Builder
			if err := RenderSimulation(&sb, tc.res, tc.now); err != nil {
				t.Fatalf("RenderSimulation: %v", err)
			}
			if sb.String() != tc.want {
				t.Errorf("output mismatch\n--- got ---\n%s\n--- want ---\n%s", sb.String(), tc.want)
			}
		})
	}
}

// unknownKindDeny is the engine's answer to a harness-native event name sent
// as the kind, the decision simulate returns for a mistyped --event.
func unknownKindDeny(t *testing.T) policy.Decision {
	t.Helper()
	eng, err := policy.NewEngine(nil, policy.EffectAllow)
	if err != nil {
		t.Fatal(err)
	}
	return eng.Evaluate(policy.Event{Kind: "PreToolUse", Tool: policy.ToolShellExec, Command: "ls"}, policy.Subject{Roles: []string{"dev"}})
}

// enterpriseDefault is the enterprise engine's answer to ev when no policy
// is loaded, the decision simulate returns for a call no rule matches.
func enterpriseDefault(t *testing.T, ev policy.Event) policy.Decision {
	t.Helper()
	eng, err := policy.NewEngine(nil, policy.EffectDeny)
	if err != nil {
		t.Fatal(err)
	}
	return eng.Evaluate(ev, policy.Subject{Roles: []string{"dev"}})
}

// TestGateWords pins the duration humanizer the gate line speaks.
func TestGateWords(t *testing.T) {
	scale := map[int]string{
		86400: "day-scale", 3600: "hour-scale", 120: "2-minute",
		45: "45-second", 2700: "45-minute", 7200: "2-hour", 259200: "3-day",
	}
	for sec, want := range scale {
		if got := scaleWord(sec); got != want {
			t.Errorf("scaleWord(%d) = %q, want %q", sec, got, want)
		}
	}
	dur := map[int]string{3600: "1 h", 7200: "2 h", 1800: "30 min", 45: "45 s", 86400: "1 day", 172800: "2 days"}
	for sec, want := range dur {
		if got := durShort(sec); got != want {
			t.Errorf("durShort(%d) = %q, want %q", sec, got, want)
		}
	}
}

// TestGateLineConfirmAndSponsor covers the confirm wording and the sponsor
// decider kind (revision 13) rendered as words.
func TestGateLineConfirmAndSponsor(t *testing.T) {
	confirm := policy.Decision{
		Effect: "allow", Approve: &policy.ApproveSpec{TimeoutSeconds: 300}, Confirm: true,
	}
	got := gateLines(confirm)
	if len(got) != 1 || got[0] != "needs approval: the requester confirms on their own device" {
		t.Errorf("confirm gate = %q", got)
	}
	sponsor := policy.Decision{
		Effect: "allow",
		Approve: &policy.ApproveSpec{
			Deciders: []string{policy.DeciderSponsor}, TimeoutSeconds: 600,
		},
	}
	got = gateLines(sponsor)
	want := "needs approval: 10-minute approval hold, deciders the person behind the agent"
	if len(got) != 1 || got[0] != want {
		t.Errorf("sponsor gate = %q, want %q", got, want)
	}
}

// TestSimVerdict pins the delta sentence under the live and file rows. The
// two agree only when the effect and every gate line match, so a file that
// adds, drops or changes a hold is counted as a change by anything that reads
// the differ line, the agent skill's replay script included. Each side is
// named by its effect, plus "after approval" when a human gate holds the call.
func TestSimVerdict(t *testing.T) {
	allow := policy.Decision{Effect: "allow", RuleID: "r-allow", SetName: "s"}
	deny := policy.Decision{Effect: "deny", RuleID: "r-deny", SetName: "s"}
	hold := func(roles ...string) policy.Decision {
		return policy.Decision{Effect: "allow", RuleID: "r-hold", SetName: "s",
			Approve: &policy.ApproveSpec{Roles: roles, TimeoutSeconds: 300}}
	}
	classified := policy.Decision{Effect: "allow", RuleID: "r-classify", SetName: "s", Classify: true}
	tests := []struct {
		name        string
		live, draft policy.Decision
		want        string
	}{
		{"no gate on either side, allow", allow, allow, "live and file agree: ALLOW"},
		{"no gate on either side, deny", deny, deny, "live and file agree: DENY"},
		{"the file adds a hold", allow, hold("sec-approvers"),
			"the live policy says ALLOW; this file says ALLOW after approval"},
		{"the file drops a hold", hold("sec-approvers"), allow,
			"the live policy says ALLOW after approval; this file says ALLOW"},
		{"the same hold on both sides", hold("sec-approvers"), hold("sec-approvers"),
			"live and file agree: ALLOW after approval"},
		{"a different hold", hold("sec-approvers"), hold("release-approvers"),
			"the live policy says ALLOW after approval; this file says ALLOW after a different approval, see the gate lines above"},
		{"deny against allow", deny, allow, "the live policy says DENY; this file says ALLOW"},
		{"deny against a hold", deny, hold("sec-approvers"),
			"the live policy says DENY; this file says ALLOW after approval"},
		{"only the checks differ", allow, classified,
			"the live policy says ALLOW; this file says ALLOW with different checks, see the gate lines above"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := simVerdict(tc.live, tc.draft)
			if got != tc.want {
				t.Errorf("simVerdict = %q, want %q", got, tc.want)
			}
			if !strings.HasPrefix(got, SimVerdictAgree) && !strings.HasPrefix(got, SimVerdictDiffer) {
				t.Errorf("%q opens with neither pinned prefix, so the replay script cannot read it", got)
			}
		})
	}
}
