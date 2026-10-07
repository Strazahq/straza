package policy

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// fixtureFile mirrors the decision-table format in
// spec/conformance/decisions/*.yaml: tests consume spec fixtures, not
// private copies.
type fixtureFile struct {
	Profile  string        `yaml:"profile"`
	Policies []string      `yaml:"policies"`
	Cases    []fixtureCase `yaml:"cases"`
}

type fixtureCase struct {
	Name    string  `yaml:"name"`
	Subject Subject `yaml:"subject"`
	Event   Event   `yaml:"event"`
	Want    struct {
		Effect      string `yaml:"effect"`
		RuleID      string `yaml:"ruleId"`
		SetName     string `yaml:"setName"`
		ServerCheck bool   `yaml:"serverCheck"`
		Classify    bool   `yaml:"classify"`
		// Approve asserts whether Decision.Approve is non-nil; ApproveTimeout
		// and ApproveTtl (when non-zero) assert the normalized spec values.
		Approve        bool `yaml:"approve"`
		ApproveTimeout int  `yaml:"approveTimeout"`
		ApproveTtl     int  `yaml:"approveTtl"`
		// Ticket-class assertions (revision 6): ApproveClass/ApproveBind (when
		// non-empty) and ApproveTicketTTL/ApproveGrantTTL (when non-zero) assert
		// the normalized ticket spec on the winning approve allow.
		ApproveClass     string `yaml:"approveClass"`
		ApproveBind      string `yaml:"approveBind"`
		ApproveTicketTTL int    `yaml:"approveTicketTtl"`
		ApproveGrantTTL  int    `yaml:"approveGrantTtl"`
		// ApproveBinding (revision 7, when non-empty) asserts the normalized
		// fingerprint-coverage knob on the winning approve allow.
		ApproveBinding string `yaml:"approveBinding"`
		// ApproveNotify (revision 8, when non-empty) asserts the verbatim
		// notification routing on the winning approve allow.
		ApproveNotify []string `yaml:"approveNotify"`
		// ApproveSelfApproval (revision 9, when set) asserts the per-subject
		// normalized selfApproval, the autonomous clamp: an agencyMode:
		// autonomous subject never receives selfApproval, whatever the rule
		// says (engine invariant, not policy convention).
		ApproveSelfApproval *bool `yaml:"approveSelfApproval"`
		// Confirm (revision 11, when set) asserts Decision.Confirm: mode
		// confirm sets it alongside the approve marker for a human-driven
		// subject; an autonomous subject turns the same rule into a plain
		// deny (engine invariant, the revision-9 clamp's sibling).
		Confirm *bool `yaml:"confirm"`
		Default bool  `yaml:"default"`
	} `yaml:"want"`
}

func loadFixtures(t *testing.T) map[string]fixtureFile {
	t.Helper()
	dir := filepath.Join("..", "..", "spec", "conformance", "decisions")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("decision fixtures missing: %v", err)
	}
	out := map[string]fixtureFile{}
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".yaml") {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		var f fixtureFile
		if err := yaml.Unmarshal(raw, &f); err != nil {
			t.Fatalf("%s: %v", e.Name(), err)
		}
		out[e.Name()] = f
	}
	return out
}

func engineFor(t *testing.T, f fixtureFile) *Engine {
	t.Helper()
	var docs []Document
	for i, p := range f.Policies {
		doc, err := Parse([]byte(p))
		if err != nil {
			t.Fatalf("policy %d: %v", i, err)
		}
		docs = append(docs, doc)
	}
	localDefault := EffectAllow
	if f.Profile == "enterprise" {
		localDefault = EffectDeny
	}
	eng, err := NewEngine(docs, localDefault)
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}
	return eng
}

// TestDecisionTables is the decision acceptance suite: every fixture case in the
// spec conformance corpus must produce the expected decision.
func TestDecisionTables(t *testing.T) {
	files := loadFixtures(t)
	total := 0
	for name, f := range files {
		t.Run(name, func(t *testing.T) {
			eng := engineFor(t, f)
			for _, tc := range f.Cases {
				total++
				t.Run(tc.Name, func(t *testing.T) {
					// Subjects with unset attestation mean "none".
					sub := tc.Subject
					if sub.Attestation == "" {
						sub.Attestation = "none"
					}
					got := eng.Evaluate(tc.Event, sub)
					if got.Effect != tc.Want.Effect {
						t.Fatalf("effect = %q (rule %s), want %q\ndecision: %+v",
							got.Effect, got.RuleID, tc.Want.Effect, got)
					}
					if got.RuleID != tc.Want.RuleID {
						t.Errorf("ruleId = %q, want %q (%+v)", got.RuleID, tc.Want.RuleID, got)
					}
					if tc.Want.SetName != "" && got.SetName != tc.Want.SetName {
						t.Errorf("setName = %q, want %q", got.SetName, tc.Want.SetName)
					}
					if got.ServerCheck != tc.Want.ServerCheck {
						t.Errorf("serverCheck = %v, want %v", got.ServerCheck, tc.Want.ServerCheck)
					}
					if got.Classify != tc.Want.Classify {
						t.Errorf("classify = %v, want %v", got.Classify, tc.Want.Classify)
					}
					if (got.Approve != nil) != tc.Want.Approve {
						t.Errorf("approve present = %v, want %v (%+v)", got.Approve != nil, tc.Want.Approve, got)
					}
					if tc.Want.Approve && got.Approve != nil {
						if tc.Want.ApproveTimeout != 0 && got.Approve.TimeoutSeconds != tc.Want.ApproveTimeout {
							t.Errorf("approve.timeoutSeconds = %d, want %d", got.Approve.TimeoutSeconds, tc.Want.ApproveTimeout)
						}
						if tc.Want.ApproveTtl != 0 && got.Approve.RetryTTLSeconds != tc.Want.ApproveTtl {
							t.Errorf("approve.retryTTLSeconds = %d, want %d", got.Approve.RetryTTLSeconds, tc.Want.ApproveTtl)
						}
						if tc.Want.ApproveClass != "" && got.Approve.Class != tc.Want.ApproveClass {
							t.Errorf("approve.class = %q, want %q", got.Approve.Class, tc.Want.ApproveClass)
						}
						if tc.Want.ApproveBind != "" && got.Approve.Bind != tc.Want.ApproveBind {
							t.Errorf("approve.bind = %q, want %q", got.Approve.Bind, tc.Want.ApproveBind)
						}
						if tc.Want.ApproveBinding != "" && got.Approve.Binding != tc.Want.ApproveBinding {
							t.Errorf("approve.binding = %q, want %q", got.Approve.Binding, tc.Want.ApproveBinding)
						}
						if tc.Want.ApproveTicketTTL != 0 && got.Approve.TicketTTLSeconds != tc.Want.ApproveTicketTTL {
							t.Errorf("approve.ticketTTLSeconds = %d, want %d", got.Approve.TicketTTLSeconds, tc.Want.ApproveTicketTTL)
						}
						if tc.Want.ApproveGrantTTL != 0 && got.Approve.GrantTTLSeconds != tc.Want.ApproveGrantTTL {
							t.Errorf("approve.grantTTLSeconds = %d, want %d", got.Approve.GrantTTLSeconds, tc.Want.ApproveGrantTTL)
						}
						if len(tc.Want.ApproveNotify) != 0 && !slices.Equal(got.Approve.Notify, tc.Want.ApproveNotify) {
							t.Errorf("approve.notify = %v, want %v", got.Approve.Notify, tc.Want.ApproveNotify)
						}
						if tc.Want.ApproveSelfApproval != nil && got.Approve.SelfApproval != *tc.Want.ApproveSelfApproval {
							t.Errorf("approve.selfApproval = %v, want %v (subject agencyMode %q)",
								got.Approve.SelfApproval, *tc.Want.ApproveSelfApproval, tc.Subject.AgencyMode)
						}
					}
					if tc.Want.Confirm != nil && got.Confirm != *tc.Want.Confirm {
						t.Errorf("confirm = %v, want %v (subject agencyMode %q)",
							got.Confirm, *tc.Want.Confirm, tc.Subject.AgencyMode)
					}
					if got.Default != tc.Want.Default {
						t.Errorf("default = %v, want %v", got.Default, tc.Want.Default)
					}
					if got.Effect == EffectDeny && got.Reason == "" {
						t.Error("deny decisions must carry an actionable reason")
					}
				})
			}
		})
	}
	if total < 60 {
		t.Errorf("decision corpus has %d cases, plan requires ≥60", total)
	}
}

// TestClassifyAndServerCheckIndependent asserts the two escalation flags are
// threaded independently: when a serverCheck rule and a classify rule both
// contribute to a winning allow, the decision carries BOTH flags; a deny
// carries neither.
func TestClassifyAndServerCheckIndependent(t *testing.T) {
	doc, err := Parse([]byte(`
apiVersion: straza.dev/v1beta1
kind: PolicySet
metadata: { name: escalations }
spec:
  rules:
    - id: check-python-commands
      tools: [shell.exec]
      command: { allowPatterns: ["python3 *"] }
      mode: serverCheck
      effect: allow
    - id: classify-python
      tools: [shell.exec]
      interpreters: { allow: ["python*"] }
      mode: classify
      effect: allow
    - id: block-secrets-script
      tools: [shell.exec]
      command: { denyPatterns: ["python3 secrets.py*"] }
      effect: deny
      reason: "secrets.py is off limits"
`))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	eng, err := NewEngine([]Document{doc}, EffectAllow)
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}
	sub := Subject{User: "kim", Roles: []string{"dev"}, Attestation: "none"}

	tests := []struct {
		name            string
		ev              Event
		wantEffect      string
		wantServerCheck bool
		wantClassify    bool
	}{
		{
			name: "both-allow-rules-fire-both-flags-set",
			ev: Event{Kind: EventToolPre, Tool: ToolShellExec,
				Command: "python3 run.py", Interpreter: "python3"},
			wantEffect:      EffectAllow,
			wantServerCheck: true,
			wantClassify:    true,
		},
		{
			name: "deny-wins-and-carries-neither-flag",
			ev: Event{Kind: EventToolPre, Tool: ToolShellExec,
				Command: "python3 secrets.py", Interpreter: "python3"},
			wantEffect: EffectDeny,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := eng.Evaluate(tc.ev, sub)
			if got.Effect != tc.wantEffect {
				t.Fatalf("effect = %q, want %q (%+v)", got.Effect, tc.wantEffect, got)
			}
			if got.ServerCheck != tc.wantServerCheck {
				t.Errorf("serverCheck = %v, want %v", got.ServerCheck, tc.wantServerCheck)
			}
			if got.Classify != tc.wantClassify {
				t.Errorf("classify = %v, want %v", got.Classify, tc.wantClassify)
			}
		})
	}
}

// TestApproveClassifyServerCheckIndependent asserts the approve marker is
// threaded independently of serverCheck and classify: when all three
// escalation rules contribute to a winning allow, the decision carries the
// normalized approve spec AND both booleans; a deny drops all three. Mirrors
// TestClassifyAndServerCheckIndependent for the approve lane.
func TestApproveClassifyServerCheckIndependent(t *testing.T) {
	doc, err := Parse([]byte(`
apiVersion: straza.dev/v1beta1
kind: PolicySet
metadata: { name: escalations }
spec:
  rules:
    - id: check-python-commands
      tools: [shell.exec]
      command: { allowPatterns: ["python3 *"] }
      mode: serverCheck
      effect: allow
    - id: classify-python
      tools: [shell.exec]
      interpreters: { allow: ["python*"] }
      mode: classify
      effect: allow
    - id: approve-python
      tools: [shell.exec]
      command: { allowPatterns: ["python3 *"] }
      mode: approve
      effect: allow
      approve: { roles: [sec-approvers], timeoutSeconds: 120 }
    - id: block-secrets-script
      tools: [shell.exec]
      command: { denyPatterns: ["python3 secrets.py*"] }
      effect: deny
      reason: "secrets.py is off limits"
`))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	eng, err := NewEngine([]Document{doc}, EffectAllow)
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}
	sub := Subject{User: "kim", Roles: []string{"dev"}, Attestation: "none"}

	tests := []struct {
		name            string
		ev              Event
		wantEffect      string
		wantServerCheck bool
		wantClassify    bool
		wantApprove     bool
		wantTimeout     int
		wantTTL         int
	}{
		{
			name: "all-three-allow-rules-fire",
			ev: Event{Kind: EventToolPre, Tool: ToolShellExec,
				Command: "python3 run.py", Interpreter: "python3"},
			wantEffect:      EffectAllow,
			wantServerCheck: true,
			wantClassify:    true,
			wantApprove:     true,
			wantTimeout:     120,
			wantTTL:         60, // retryTTLSeconds omitted ⇒ normalized default
		},
		{
			name: "deny-wins-and-carries-no-escalation-marker",
			ev: Event{Kind: EventToolPre, Tool: ToolShellExec,
				Command: "python3 secrets.py", Interpreter: "python3"},
			wantEffect: EffectDeny,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := eng.Evaluate(tc.ev, sub)
			if got.Effect != tc.wantEffect {
				t.Fatalf("effect = %q, want %q (%+v)", got.Effect, tc.wantEffect, got)
			}
			if got.ServerCheck != tc.wantServerCheck {
				t.Errorf("serverCheck = %v, want %v", got.ServerCheck, tc.wantServerCheck)
			}
			if got.Classify != tc.wantClassify {
				t.Errorf("classify = %v, want %v", got.Classify, tc.wantClassify)
			}
			if (got.Approve != nil) != tc.wantApprove {
				t.Fatalf("approve present = %v, want %v (%+v)", got.Approve != nil, tc.wantApprove, got)
			}
			if got.Approve != nil {
				if got.Approve.TimeoutSeconds != tc.wantTimeout {
					t.Errorf("approve.timeoutSeconds = %d, want %d", got.Approve.TimeoutSeconds, tc.wantTimeout)
				}
				if got.Approve.RetryTTLSeconds != tc.wantTTL {
					t.Errorf("approve.retryTTLSeconds = %d, want %d", got.Approve.RetryTTLSeconds, tc.wantTTL)
				}
			}
		})
	}
}

// TestApproveTicketNormalization pins the revision-6 ticket compile-time
// normalization: a `class: ticket` rule's winning
// allow carries ticketTTLSeconds/grantTTLSeconds/bind, defaulting an omitted
// ticketTTL to 24h (86400), grantTTL to 1h (3600), and bind to fingerprint;
// explicit values survive. The blocking-wait knobs (timeout/retry) are inert
// for a ticket and stay zero; a ticket never blocks.
func TestApproveTicketNormalization(t *testing.T) {
	doc, err := Parse([]byte(`
apiVersion: straza.dev/v1beta1
kind: PolicySet
metadata: { name: tickets }
spec:
  rules:
    - id: ticket-explicit
      tools: [shell.exec]
      command: { allowPatterns: ["deploy-prod*"] }
      mode: approve
      effect: allow
      approve:
        class: ticket
        roles: [change-approvers]
        ticketTTLSeconds: 172800
        grantTTLSeconds: 7200
        bind: predicate
    - id: ticket-defaults
      tools: [shell.exec]
      command: { allowPatterns: ["rotate-key*"] }
      mode: approve
      effect: allow
      approve:
        class: ticket
`))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	eng, err := NewEngine([]Document{doc}, EffectAllow)
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}
	sub := Subject{User: "kim", Roles: []string{"dev"}, Attestation: "none"}

	explicit := eng.Evaluate(Event{Kind: EventToolPre, Tool: ToolShellExec, Command: "deploy-prod --now"}, sub)
	if explicit.Approve == nil {
		t.Fatalf("ticket allow carried no approve spec: %+v", explicit)
	}
	if explicit.Approve.Class != ClassTicket {
		t.Errorf("class = %q, want ticket", explicit.Approve.Class)
	}
	if explicit.Approve.TicketTTLSeconds != 172800 || explicit.Approve.GrantTTLSeconds != 7200 {
		t.Errorf("explicit ttls not preserved: %+v", explicit.Approve)
	}
	if explicit.Approve.Bind != BindPredicate {
		t.Errorf("bind = %q, want predicate", explicit.Approve.Bind)
	}
	if explicit.Approve.TimeoutSeconds != 0 || explicit.Approve.RetryTTLSeconds != 0 {
		t.Errorf("ticket must not carry blocking-wait knobs: %+v", explicit.Approve)
	}

	defs := eng.Evaluate(Event{Kind: EventToolPre, Tool: ToolShellExec, Command: "rotate-key prod"}, sub)
	if defs.Approve == nil {
		t.Fatalf("default ticket allow carried no approve spec: %+v", defs)
	}
	if defs.Approve.Class != ClassTicket {
		t.Errorf("default class = %q, want ticket", defs.Approve.Class)
	}
	if defs.Approve.TicketTTLSeconds != 86400 {
		t.Errorf("ticketTTLSeconds default = %d, want 86400 (24h)", defs.Approve.TicketTTLSeconds)
	}
	if defs.Approve.GrantTTLSeconds != 3600 {
		t.Errorf("grantTTLSeconds default = %d, want 3600 (1h)", defs.Approve.GrantTTLSeconds)
	}
	if defs.Approve.Bind != BindFingerprint {
		t.Errorf("bind default = %q, want fingerprint", defs.Approve.Bind)
	}
	if defs.Approve.TimeoutSeconds != 0 || defs.Approve.RetryTTLSeconds != 0 {
		t.Errorf("ticket must not carry blocking-wait knobs: %+v", defs.Approve)
	}
}

// TestApproveBindingNormalization (revision 7): every winning approve allow
// carries a concrete Binding: empty normalizes to call (the secure default)
// for BOTH classes, and an explicit tool opt-out is carried through verbatim.
func TestApproveBindingNormalization(t *testing.T) {
	doc, err := Parse([]byte(`
apiVersion: straza.dev/v1beta1
kind: PolicySet
metadata: { name: bindings }
spec:
  rules:
    - id: hold-default-binding
      tools: [mcp.call]
      apps: [midpoint]
      toolNames: { allow: ["disable_user"] }
      mode: approve
      effect: allow
      approve:
        roles: [sec-approvers]
    - id: ticket-default-binding
      tools: [mcp.call]
      apps: [vault]
      toolNames: { allow: ["rotate_root_key"] }
      mode: approve
      effect: allow
      approve:
        class: ticket
    - id: hold-tool-binding
      tools: [mcp.call]
      apps: [github]
      toolNames: { allow: ["search_code"] }
      mode: approve
      effect: allow
      approve:
        binding: tool
`))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	eng, err := NewEngine([]Document{doc}, EffectAllow)
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}
	sub := Subject{User: "kim", Roles: []string{"dev"}, Attestation: "none"}

	hold := eng.Evaluate(Event{Kind: EventToolPre, Tool: ToolMCPCall, App: "midpoint", ToolName: "disable_user"}, sub)
	if hold.Approve == nil || hold.Approve.Binding != ApproveBindingCall {
		t.Errorf("hold binding = %+v, want normalized call", hold.Approve)
	}
	ticket := eng.Evaluate(Event{Kind: EventToolPre, Tool: ToolMCPCall, App: "vault", ToolName: "rotate_root_key"}, sub)
	if ticket.Approve == nil || ticket.Approve.Binding != ApproveBindingCall {
		t.Errorf("ticket binding = %+v, want normalized call", ticket.Approve)
	}
	tool := eng.Evaluate(Event{Kind: EventToolPre, Tool: ToolMCPCall, App: "github", ToolName: "search_code"}, sub)
	if tool.Approve == nil || tool.Approve.Binding != ApproveBindingTool {
		t.Errorf("explicit binding = %+v, want tool carried verbatim", tool.Approve)
	}
}

// TestApproveHoldByteIdentical pins that the ticket class leaves a HOLD
// rule's normalized spec byte-identical to pre-ticket behavior. A hold allow
// (default class) carries TimeoutSeconds 90 / RetryTTLSeconds 60, Class
// normalizes to "hold", and the ticket-only knobs
// (TicketTTLSeconds/GrantTTLSeconds/Bind) are NEVER populated for a hold rule.
func TestApproveHoldByteIdentical(t *testing.T) {
	doc, err := Parse([]byte(`
apiVersion: straza.dev/v1beta1
kind: PolicySet
metadata: { name: holds }
spec:
  rules:
    - id: hold-explicit
      tools: [mcp.call]
      apps: [midpoint]
      toolNames: { allow: ["disable_user"] }
      mode: approve
      effect: allow
      approve:
        roles: [sec-approvers]
        timeoutSeconds: 120
        retryTTLSeconds: 30
    - id: hold-bare
      tools: [mcp.call]
      apps: [midpoint]
      toolNames: { allow: ["reset_password"] }
      mode: approve
      effect: allow
`))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	eng, err := NewEngine([]Document{doc}, EffectAllow)
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}
	sub := Subject{User: "kim", Roles: []string{"dev"}, Attestation: "none"}

	assertHold := func(name string, d Decision, wantTimeout, wantTTL int) {
		t.Helper()
		if d.Approve == nil {
			t.Fatalf("%s: hold allow carried no approve spec: %+v", name, d)
		}
		if d.Approve.Class != ClassHold {
			t.Errorf("%s: class = %q, want hold", name, d.Approve.Class)
		}
		if d.Approve.TimeoutSeconds != wantTimeout {
			t.Errorf("%s: timeoutSeconds = %d, want %d (unchanged from pre-ticket)", name, d.Approve.TimeoutSeconds, wantTimeout)
		}
		if d.Approve.RetryTTLSeconds != wantTTL {
			t.Errorf("%s: retryTTLSeconds = %d, want %d (unchanged from pre-ticket)", name, d.Approve.RetryTTLSeconds, wantTTL)
		}
		if d.Approve.TicketTTLSeconds != 0 || d.Approve.GrantTTLSeconds != 0 || d.Approve.Bind != "" {
			t.Errorf("%s: hold rule leaked ticket-only knobs: %+v", name, d.Approve)
		}
	}
	assertHold("explicit",
		eng.Evaluate(Event{Kind: EventToolPre, Tool: ToolMCPCall, App: "midpoint", ToolName: "disable_user"}, sub),
		120, 30)
	assertHold("bare-defaults",
		eng.Evaluate(Event{Kind: EventToolPre, Tool: ToolMCPCall, App: "midpoint", ToolName: "reset_password"}, sub),
		defaultApproveTimeoutSeconds, defaultApproveRetryTTLSeconds)
}

// TestEvaluateConcurrent asserts the engine is safe for concurrent use. It
// runs under -race.
func TestEvaluateConcurrent(t *testing.T) {
	files := loadFixtures(t)
	f := files["shell-standalone.yaml"]
	eng := engineFor(t, f)
	done := make(chan bool)
	for w := 0; w < 8; w++ {
		go func() {
			defer func() { done <- true }()
			for i := 0; i < 200; i++ {
				for _, tc := range f.Cases {
					sub := tc.Subject
					if sub.Attestation == "" {
						sub.Attestation = "none"
					}
					_ = eng.Evaluate(tc.Event, sub)
				}
			}
		}()
	}
	for w := 0; w < 8; w++ {
		<-done
	}
}

// BenchmarkEvaluate10kRules is the latency budget probe: p99 < 100µs on a
// 10k-rule snapshot. `make bench` tracks it.
func BenchmarkEvaluate10kRules(b *testing.B) {
	var docs []Document
	// 100 sets × 100 rules = 10k rules across mixed tools and roles.
	for s := 0; s < 100; s++ {
		var rules strings.Builder
		for r := 0; r < 100; r++ {
			fmt.Fprintf(&rules, `
    - id: rule-%03d
      tools: [shell.exec]
      command: { denyPatterns: ["dangerous-%d-*"] }
      effect: deny
      reason: "no"
`, r, r)
		}
		doc, err := Parse([]byte(fmt.Sprintf(`
apiVersion: straza.dev/v1beta1
kind: PolicySet
metadata: { name: bench-set-%03d }
spec:
  match: { roles: [role-%d] }
  rules:%s`, s, s%10, rules.String())))
		if err != nil {
			b.Fatal(err)
		}
		docs = append(docs, doc)
	}
	eng, err := NewEngine(docs, EffectAllow)
	if err != nil {
		b.Fatal(err)
	}
	ev := Event{Kind: EventToolPre, Tool: ToolShellExec, Command: "git status --porcelain"}
	sub := Subject{User: "kim", Roles: []string{"role-3"}, Attestation: "advisory"}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		d := eng.Evaluate(ev, sub)
		if d.Effect != EffectAllow {
			b.Fatalf("unexpected decision %+v", d)
		}
	}
}

// TestNormalizeApproveNotifyCopied: the engine owns its data, so mutating the
// source document's notify slice after compile must not reach the compiled
// spec (the Roles deep-copy contract, extended to revision-8 routing).
func TestNormalizeApproveNotifyCopied(t *testing.T) {
	src := &ApproveSpec{Notify: []string{NotifyPush}}
	out := normalizeApprove(src)
	src.Notify[0] = "mutated"
	if len(out.Notify) != 1 || out.Notify[0] != NotifyPush {
		t.Errorf("normalized notify = %v, want [push] unaffected by source mutation", out.Notify)
	}
}

// TestApproveRuleNamedOnDecision pins the revision 18 attribution rule: when
// an approve rule fires on the winning allow, the decision reports that
// rule's id, set and reason whatever its priority, because the hold is what
// the caller experiences and the approval record must name the rule that
// mandated it. A deny still wins with its own attribution, and a plain allow
// with no approve rule beside it keeps the priority order.
func TestApproveRuleNamedOnDecision(t *testing.T) {
	var docs []Document
	for _, src := range []string{`
apiVersion: straza.dev/v1beta1
kind: PolicySet
metadata: { name: a-allow }
spec:
  priority: 100
  rules:
    - id: local-tools
      tools: [shell.exec]
      command: { allowPatterns: ["kubectl *", "helm *"] }
      effect: allow
      reason: "kubectl is fine"
`, `
apiVersion: straza.dev/v1beta1
kind: PolicySet
metadata: { name: b-hold }
spec:
  priority: 100
  rules:
    - id: hold-kubectl-apply
      tools: [shell.exec]
      command: { allowPatterns: ["kubectl apply *"] }
      effect: allow
      mode: approve
      approve: { roles: [approvers], timeoutSeconds: 300 }
      reason: "kubectl apply is held"
`, `
apiVersion: straza.dev/v1beta1
kind: PolicySet
metadata: { name: c-low-hold }
spec:
  priority: 1
  rules:
    - id: hold-helm-install
      tools: [shell.exec]
      command: { allowPatterns: ["helm install *"] }
      effect: allow
      mode: approve
      reason: "helm install is held"
`, `
apiVersion: straza.dev/v1beta1
kind: PolicySet
metadata: { name: d-deny }
spec:
  rules:
    - id: deny-prod
      tools: [shell.exec]
      command: { denyPatterns: ["kubectl apply *prod*"] }
      effect: deny
      reason: "prod is off limits"
`} {
		doc, err := Parse([]byte(src))
		if err != nil {
			t.Fatalf("Parse: %v", err)
		}
		docs = append(docs, doc)
	}
	eng, err := NewEngine(docs, EffectAllow)
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}
	sub := Subject{User: "kim", Roles: []string{"dev"}, Attestation: "none"}

	tests := []struct {
		name        string
		command     string
		wantEffect  string
		wantRule    string
		wantSet     string
		wantReason  string
		wantApprove bool
		wantTimeout int
	}{
		{
			name:        "tied-priority-earlier-set-plain-allow-does-not-hide-the-hold",
			command:     "kubectl apply -f app.yaml",
			wantEffect:  EffectAllow,
			wantRule:    "hold-kubectl-apply",
			wantSet:     "b-hold",
			wantReason:  "kubectl apply is held",
			wantApprove: true,
			wantTimeout: 300,
		},
		{
			name:        "lower-priority-approve-rule-is-still-named",
			command:     "helm install web ./chart",
			wantEffect:  EffectAllow,
			wantRule:    "hold-helm-install",
			wantSet:     "c-low-hold",
			wantReason:  "helm install is held",
			wantApprove: true,
			wantTimeout: 90,
		},
		{
			name:       "deny-wins-with-its-own-attribution",
			command:    "kubectl apply -f prod.yaml",
			wantEffect: EffectDeny,
			wantRule:   "deny-prod",
			wantSet:    "d-deny",
			wantReason: "prod is off limits",
		},
		{
			name:       "plain-allow-alone-keeps-the-priority-order",
			command:    "kubectl get pods",
			wantEffect: EffectAllow,
			wantRule:   "local-tools",
			wantSet:    "a-allow",
			wantReason: "kubectl is fine",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := eng.Evaluate(Event{Kind: EventToolPre, Tool: ToolShellExec, Command: tc.command}, sub)
			if got.Effect != tc.wantEffect {
				t.Fatalf("effect = %q, want %q (%+v)", got.Effect, tc.wantEffect, got)
			}
			if got.RuleID != tc.wantRule || got.SetName != tc.wantSet {
				t.Errorf("reported %s/%s, want %s/%s", got.SetName, got.RuleID, tc.wantSet, tc.wantRule)
			}
			if got.Reason != tc.wantReason {
				t.Errorf("reason = %q, want %q", got.Reason, tc.wantReason)
			}
			if (got.Approve != nil) != tc.wantApprove {
				t.Fatalf("approve present = %v, want %v (%+v)", got.Approve != nil, tc.wantApprove, got)
			}
			if got.Approve != nil && got.Approve.TimeoutSeconds != tc.wantTimeout {
				t.Errorf("approve.timeoutSeconds = %d, want %d", got.Approve.TimeoutSeconds, tc.wantTimeout)
			}
		})
	}
}

// TestObligationsNeverRideDecision pins revision 18: a rule that carries the
// retired obligations list still parses and still fires, but the decision
// reports an empty list, because no lane ever ran an obligation.
func TestObligationsNeverRideDecision(t *testing.T) {
	doc, err := Parse([]byte(`
apiVersion: straza.dev/v1beta1
kind: PolicySet
metadata: { name: legacy-obligations }
spec:
  rules:
    - id: watch-echo
      tools: [shell.exec]
      command: { allowPatterns: ["echo *"] }
      effect: allow
      obligations: [notify, redact]
`))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if got := doc.Spec.Rules[0].Obligations; len(got) != 2 {
		t.Fatalf("parsed obligations = %v, want the stored list kept", got)
	}
	eng, err := NewEngine([]Document{doc}, EffectAllow)
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}
	got := eng.Evaluate(Event{Kind: EventToolPre, Tool: ToolShellExec, Command: "echo hi"},
		Subject{User: "kim", Attestation: "none"})
	if got.Effect != EffectAllow || got.RuleID != "watch-echo" {
		t.Fatalf("decision = %+v, want an allow by watch-echo", got)
	}
	if len(got.Obligations) != 0 {
		t.Errorf("decision obligations = %v, want none", got.Obligations)
	}
}
