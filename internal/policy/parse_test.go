package policy

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestParserAgreesWithSpecExamples pins that every valid-*.yaml in the
// spec corpus parses, every invalid-*.yaml is rejected.
func TestParserAgreesWithSpecExamples(t *testing.T) {
	dir := filepath.Join("..", "..", "spec", "policyset", "examples")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("spec examples missing: %v", err)
	}
	var valid, invalid int
	for _, e := range entries {
		name := e.Name()
		raw, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		switch {
		case strings.HasPrefix(name, "valid-"):
			valid++
			if _, err := Parse(raw); err != nil {
				t.Errorf("%s must parse: %v", name, err)
			}
		case strings.HasPrefix(name, "invalid-"):
			invalid++
			if _, err := Parse(raw); err == nil {
				t.Errorf("%s must be rejected", name)
			}
		default:
			t.Errorf("unclassified example %s (name must start valid-/invalid-)", name)
		}
	}
	if valid < 3 || invalid < 3 {
		t.Errorf("spec corpus too small: %d valid, %d invalid (need ≥3 each)", valid, invalid)
	}
}

// TestParseRefusesRetiredMatchGroups pins the revision 12 retirement: a
// document carrying spec.match.groups fails with a POINTED error naming
// the retirement and the fix, even when the list is empty. The generic
// unknown-key error would reject too, but would not teach.
func TestParseRefusesRetiredMatchGroups(t *testing.T) {
	for _, groups := range []string{`[contractors]`, `[]`} {
		body := `
apiVersion: straza.dev/v1beta1
kind: PolicySet
metadata: { name: t }
spec:
  match: { groups: ` + groups + ` }
  rules:
    - { id: r1, effect: deny, reason: "x" }
`
		_, err := Parse([]byte(body))
		if err == nil {
			t.Fatalf("match.groups %s must be rejected", groups)
		}
		for _, want := range []string{"match.groups", "retired", "roles"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("match.groups %s error must mention %q, got: %v", groups, want, err)
			}
		}
	}
}

// TestParseAPIVersions pins the version-acceptance contract: canonical
// straza.dev/v1beta1 parses, and ids under any other domain are rejected
// with the canonical id in the reason.
func TestParseAPIVersions(t *testing.T) {
	const body = `
kind: PolicySet
metadata: { name: t }
spec:
  rules:
    - { id: r1, effect: deny }
`
	cases := []struct {
		name    string
		version string
		ok      bool
	}{
		{"canonical v1beta1", "straza.dev/v1beta1", true},
		{"pre-rename v1beta1 id rejected", "legacy.example/v1beta1", false},
		{"pre-rename v1alpha1 id rejected", "legacy.example/v1alpha1", false},
		{"unknown future version", "straza.dev/v2", false},
		{"missing apiVersion", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Parse([]byte("apiVersion: " + tc.version + body))
			if tc.ok && err != nil {
				t.Fatalf("Parse: %v", err)
			}
			if !tc.ok {
				if err == nil || !strings.Contains(err.Error(), APIVersion) {
					t.Fatalf("want rejection naming %q, got %v", APIVersion, err)
				}
			}
		})
	}
}

func TestParseDefaultsAndNormalization(t *testing.T) {
	doc, err := Parse([]byte(`
apiVersion: straza.dev/v1beta1
kind: PolicySet
metadata: { name: t }
spec:
  rules:
    - id: r1
      tools: [shell.exec]
      effect: deny
`))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(doc.Spec.Rules[0].Events) != 1 || doc.Spec.Rules[0].Events[0] != EventToolPre {
		t.Errorf("events default = %v, want [tool.pre]", doc.Spec.Rules[0].Events)
	}
	if doc.Spec.Priority != 0 {
		t.Errorf("priority default = %d", doc.Spec.Priority)
	}
}

// TestParseClassifyMode pins the classify surface: `mode: classify` is legal
// beside serverCheck, and the `interpreters:` matcher parses on both sides.
func TestParseClassifyMode(t *testing.T) {
	doc, err := Parse([]byte(`
apiVersion: straza.dev/v1beta1
kind: PolicySet
metadata: { name: t }
spec:
  rules:
    - id: classify-interpreters
      tools: [shell.exec]
      interpreters: { allow: ["*"] }
      effect: allow
      mode: classify
    - id: deny-python
      tools: [shell.exec]
      interpreters: { deny: ["python*"] }
      effect: deny
      reason: "no python for this role"
`))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	r := doc.Spec.Rules[0]
	if r.Mode != ModeClassify {
		t.Errorf("mode = %q, want %q", r.Mode, ModeClassify)
	}
	if r.Interpreters == nil || len(r.Interpreters.Allow) != 1 {
		t.Errorf("interpreters allow side not parsed: %+v", r.Interpreters)
	}
	if d := doc.Spec.Rules[1].Interpreters; d == nil || len(d.Deny) != 1 {
		t.Errorf("interpreters deny side not parsed: %+v", d)
	}
}

// TestParseApproveMode pins the revision-4 acceptance surface: `mode: approve`
// is legal beside serverCheck/classify, the `approve` block parses (explicit
// values are preserved unchanged by the parser: normalization is a compile
// step, not a parse step), and a bare `mode: approve` with no approve block is
// legal (straza-admin fallback resolves at decision time).
func TestParseApproveMode(t *testing.T) {
	doc, err := Parse([]byte(`
apiVersion: straza.dev/v1beta1
kind: PolicySet
metadata: { name: t }
spec:
  rules:
    - id: approve-disable
      tools: [mcp.call]
      apps: [midpoint]
      toolNames: { allow: ["disable_user"] }
      effect: allow
      mode: approve
      approve:
        roles: [sec-approvers]
        timeoutSeconds: 120
        selfApproval: true
        retryTTLSeconds: 30
    - id: approve-bare
      tools: [mcp.call]
      apps: [midpoint]
      toolNames: { allow: ["reset_password"] }
      effect: allow
      mode: approve
`))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	r := doc.Spec.Rules[0]
	if r.Mode != ModeApprove {
		t.Errorf("mode = %q, want %q", r.Mode, ModeApprove)
	}
	if r.Approve == nil {
		t.Fatalf("approve block not parsed")
	}
	if len(r.Approve.Roles) != 1 || r.Approve.Roles[0] != "sec-approvers" {
		t.Errorf("approve.roles = %v, want [sec-approvers]", r.Approve.Roles)
	}
	if r.Approve.TimeoutSeconds != 120 || !r.Approve.SelfApproval || r.Approve.RetryTTLSeconds != 30 {
		t.Errorf("approve block values not preserved verbatim: %+v", r.Approve)
	}
	if bare := doc.Spec.Rules[1]; bare.Mode != ModeApprove || bare.Approve != nil {
		t.Errorf("bare approve rule: mode=%q approve=%+v (want mode approve, nil block)", bare.Mode, bare.Approve)
	}
}

// TestParseConfirmMode pins the revision-11 acceptance surface: mode confirm
// is legal bare, and legal with the shared approve knob block as long as the
// decider-pool fields stay absent (their rejection is pinned in
// TestParseRejections).
func TestParseConfirmMode(t *testing.T) {
	doc, err := Parse([]byte(`
apiVersion: straza.dev/v1beta1
kind: PolicySet
metadata: { name: t }
spec:
  rules:
    - id: confirm-bare
      tools: [mcp.call]
      apps: [github]
      toolNames: { allow: ["create_pull_request"] }
      effect: allow
      mode: confirm
    - id: confirm-knobs
      tools: [shell.exec]
      effect: allow
      mode: confirm
      approve:
        timeoutSeconds: 300
        retryTTLSeconds: 120
        notify: [push]
`))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if r := doc.Spec.Rules[0]; r.Mode != ModeConfirm || r.Approve != nil {
		t.Errorf("bare confirm rule: mode=%q approve=%+v (want mode confirm, nil block)", r.Mode, r.Approve)
	}
	r := doc.Spec.Rules[1]
	if r.Mode != ModeConfirm || r.Approve == nil {
		t.Fatalf("confirm-knobs rule: mode=%q approve=%+v", r.Mode, r.Approve)
	}
	if r.Approve.TimeoutSeconds != 300 || r.Approve.RetryTTLSeconds != 120 {
		t.Errorf("shared knobs not preserved verbatim: %+v", r.Approve)
	}
}

// TestParseTicketMode pins the revision-6 acceptance surface (long-running
// approvals): the `approve` block
// gains `class` (hold|ticket, default hold), `ticketTTLSeconds`,
// `grantTTLSeconds`, and `bind` (fingerprint|predicate, default fingerprint).
// The parser preserves explicit values verbatim (normalization is a compile
// step); a bare `class: ticket` with no ttls is legal (defaults at compile).
func TestParseTicketMode(t *testing.T) {
	doc, err := Parse([]byte(`
apiVersion: straza.dev/v1beta1
kind: PolicySet
metadata: { name: t }
spec:
  rules:
    - id: ticket-explicit
      tools: [shell.exec]
      command: { allowPatterns: ["deploy-prod*"] }
      effect: allow
      mode: approve
      approve:
        class: ticket
        roles: [change-approvers]
        ticketTTLSeconds: 172800
        grantTTLSeconds: 7200
        bind: predicate
    - id: ticket-bare
      tools: [mcp.call]
      apps: [vault]
      toolNames: { allow: ["rotate_root_key"] }
      effect: allow
      mode: approve
      approve:
        class: ticket
    - id: hold-default
      tools: [mcp.call]
      apps: [midpoint]
      toolNames: { allow: ["disable_user"] }
      effect: allow
      mode: approve
      approve:
        roles: [sec-approvers]
`))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	explicit := doc.Spec.Rules[0]
	if explicit.Approve == nil {
		t.Fatalf("ticket approve block not parsed")
	}
	if explicit.Approve.Class != ClassTicket {
		t.Errorf("class = %q, want %q", explicit.Approve.Class, ClassTicket)
	}
	if explicit.Approve.TicketTTLSeconds != 172800 || explicit.Approve.GrantTTLSeconds != 7200 {
		t.Errorf("ttl values not preserved verbatim: %+v", explicit.Approve)
	}
	if explicit.Approve.Bind != BindPredicate {
		t.Errorf("bind = %q, want %q", explicit.Approve.Bind, BindPredicate)
	}
	if bare := doc.Spec.Rules[1].Approve; bare == nil || bare.Class != ClassTicket ||
		bare.TicketTTLSeconds != 0 || bare.Bind != "" {
		t.Errorf("bare ticket rule: values not left for compile-time normalization: %+v", bare)
	}
	// A hold rule (no class) parses with an empty Class: the parser never
	// invents defaults; class "" means hold and normalizes at compile.
	if hold := doc.Spec.Rules[2].Approve; hold == nil || hold.Class != "" {
		t.Errorf("hold rule class = %q, want empty (default hold at compile)", classOf(hold))
	}
}

func classOf(a *ApproveSpec) string {
	if a == nil {
		return "<nil>"
	}
	return a.Class
}

// TestParseApproveBinding (revision 7): `approve.binding` parses verbatim;
// call and tool are legal, anything else is rejected, and the parser never
// invents the default (empty normalizes to call at compile, like class/bind).
func TestParseApproveBinding(t *testing.T) {
	doc, err := Parse([]byte(`
apiVersion: straza.dev/v1beta1
kind: PolicySet
metadata: { name: b }
spec:
  rules:
    - id: binding-tool
      tools: [mcp.call]
      apps: [github]
      toolNames: { allow: ["search_*"] }
      effect: allow
      mode: approve
      approve:
        binding: tool
    - id: binding-call
      tools: [mcp.call]
      apps: [midpoint]
      toolNames: { allow: ["disable_user"] }
      effect: allow
      mode: approve
      approve:
        binding: call
    - id: binding-bare
      tools: [mcp.call]
      apps: [vault]
      toolNames: { allow: ["rotate_root_key"] }
      effect: allow
      mode: approve
      approve:
        class: ticket
`))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if b := doc.Spec.Rules[0].Approve.Binding; b != ApproveBindingTool {
		t.Errorf("binding = %q, want tool", b)
	}
	if b := doc.Spec.Rules[1].Approve.Binding; b != ApproveBindingCall {
		t.Errorf("binding = %q, want call", b)
	}
	if b := doc.Spec.Rules[2].Approve.Binding; b != "" {
		t.Errorf("bare binding = %q, want empty (default call at compile)", b)
	}

	if _, err := Parse([]byte(`
apiVersion: straza.dev/v1beta1
kind: PolicySet
metadata: { name: bad }
spec:
  rules:
    - id: binding-bad
      tools: [mcp.call]
      apps: [github]
      effect: allow
      mode: approve
      approve:
        binding: everything
`)); err == nil || !strings.Contains(err.Error(), "approve.binding must be call or tool") {
		t.Errorf("bad binding: err = %v, want approve.binding rejection", err)
	}
}

// TestParseApproveNotify pins the revision-8 routing surface: `approve.notify`
// parses verbatim with the fixed channel vocabulary; omitted stays nil (= all
// configured channels at dispatch); unknown members, duplicates, and empty
// strings are authoring mistakes (fail-closed typo safety: a misspelled
// channel must never silently widen back to all-configured).
func TestParseApproveNotify(t *testing.T) {
	doc, err := Parse([]byte(`
apiVersion: straza.dev/v1beta1
kind: PolicySet
metadata: { name: n }
spec:
  rules:
    - id: notify-narrowed
      tools: [mcp.call]
      apps: [midpoint]
      toolNames: { allow: ["approve_work_item"] }
      effect: allow
      mode: approve
      approve:
        selfApproval: true
        notify: [push, console]
    - id: notify-omitted
      tools: [mcp.call]
      apps: [midpoint]
      toolNames: { allow: ["disable_user"] }
      effect: allow
      mode: approve
      approve:
        roles: [sec-approvers]
`))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	got := doc.Spec.Rules[0].Approve.Notify
	if len(got) != 2 || got[0] != NotifyPush || got[1] != NotifyConsole {
		t.Errorf("notify = %v, want [push console] verbatim", got)
	}
	if doc.Spec.Rules[1].Approve.Notify != nil {
		t.Errorf("omitted notify = %v, want nil (all configured channels)", doc.Spec.Rules[1].Approve.Notify)
	}

	rejections := []struct {
		name, notify, want string
	}{
		{"unknown channel", `[phone]`, "approve.notify entries must be console, slack, or push"},
		{"empty member", `[""]`, "approve.notify entries must be console, slack, or push"},
		{"duplicate", `[push, push]`, "approve.notify has duplicate entry"},
	}
	for _, tc := range rejections {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Parse([]byte(`
apiVersion: straza.dev/v1beta1
kind: PolicySet
metadata: { name: bad }
spec:
  rules:
    - id: r1
      tools: [mcp.call]
      apps: [midpoint]
      toolNames: { allow: ["disable_user"] }
      effect: allow
      mode: approve
      approve:
        notify: ` + tc.notify + `
`))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("err = %v, want %q", err, tc.want)
			}
		})
	}
}

func TestParseApproveDeciders(t *testing.T) {
	doc, err := Parse([]byte(`
apiVersion: straza.dev/v1beta1
kind: PolicySet
metadata: { name: n }
spec:
  rules:
    - id: sponsor-decides
      tools: [mcp.call]
      apps: [midpoint]
      toolNames: { allow: ["disable_user"] }
      effect: allow
      mode: approve
      approve:
        deciders: [sponsor]
    - id: deciders-omitted
      tools: [mcp.call]
      apps: [midpoint]
      toolNames: { allow: ["enable_user"] }
      effect: allow
      mode: approve
      approve:
        roles: [sec-approvers]
`))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	got := doc.Spec.Rules[0].Approve.Deciders
	if len(got) != 1 || got[0] != DeciderSponsor {
		t.Errorf("deciders = %v, want [sponsor] verbatim", got)
	}
	if doc.Spec.Rules[1].Approve.Deciders != nil {
		t.Errorf("omitted deciders = %v, want nil (roles/admin semantics unchanged)", doc.Spec.Rules[1].Approve.Deciders)
	}

	rejections := []struct {
		name, deciders, want string
	}{
		{"unknown kind", `[manager]`, "approve.deciders entries must be sponsor"},
		{"empty member", `[""]`, "approve.deciders entries must be sponsor"},
		{"duplicate", `[sponsor, sponsor]`, "approve.deciders has duplicate entry"},
	}
	for _, tc := range rejections {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Parse([]byte(`
apiVersion: straza.dev/v1beta1
kind: PolicySet
metadata: { name: bad }
spec:
  rules:
    - id: r1
      tools: [mcp.call]
      apps: [midpoint]
      toolNames: { allow: ["disable_user"] }
      effect: allow
      mode: approve
      approve:
        deciders: ` + tc.deciders + `
`))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("err = %v, want %q", err, tc.want)
			}
		})
	}

	// normalizeApprove hands the engine its own copy: mutating the parsed
	// document after compile must not reach the compiled spec.
	norm := normalizeApprove(doc.Spec.Rules[0].Approve)
	if len(norm.Deciders) != 1 || norm.Deciders[0] != DeciderSponsor {
		t.Fatalf("normalized deciders = %v, want [sponsor]", norm.Deciders)
	}
	doc.Spec.Rules[0].Approve.Deciders[0] = "mutated"
	if norm.Deciders[0] != DeciderSponsor {
		t.Errorf("normalized deciders alias the parsed slice (mutation leaked)")
	}
}

func TestParseRejections(t *testing.T) {
	cases := []struct {
		name string
		yaml string
		want string
	}{
		{"bad mode", `
apiVersion: straza.dev/v1beta1
kind: PolicySet
metadata: { name: t }
spec:
  rules:
    - id: r1
      effect: deny
      mode: bogus
`, "mode must be serverCheck, classify, approve, or confirm"},
		{"confirm with roles", `
apiVersion: straza.dev/v1beta1
kind: PolicySet
metadata: { name: t }
spec:
  rules:
    - id: r1
      tools: [mcp.call]
      apps: [midpoint]
      toolNames: { allow: ["disable_user"] }
      effect: allow
      mode: confirm
      approve: { roles: [sec-approvers] }
`, "mode confirm may not set approve.roles"},
		{"confirm with deciders", `
apiVersion: straza.dev/v1beta1
kind: PolicySet
metadata: { name: t }
spec:
  rules:
    - id: r1
      tools: [mcp.call]
      apps: [midpoint]
      toolNames: { allow: ["disable_user"] }
      effect: allow
      mode: confirm
      approve: { deciders: [sponsor] }
`, "mode confirm may not set approve.deciders"},
		{"confirm with selfApproval", `
apiVersion: straza.dev/v1beta1
kind: PolicySet
metadata: { name: t }
spec:
  rules:
    - id: r1
      tools: [mcp.call]
      apps: [midpoint]
      toolNames: { allow: ["disable_user"] }
      effect: allow
      mode: confirm
      approve: { selfApproval: true }
`, "mode confirm may not set approve.selfApproval"},
		{"approve block without mode approve", `
apiVersion: straza.dev/v1beta1
kind: PolicySet
metadata: { name: t }
spec:
  rules:
    - id: r1
      tools: [mcp.call]
      apps: [midpoint]
      toolNames: { allow: ["disable_user"] }
      effect: allow
      mode: classify
      approve: { roles: [sec-approvers] }
`, "approve block requires mode: approve or mode: confirm"},
		{"approve timeout over cap", `
apiVersion: straza.dev/v1beta1
kind: PolicySet
metadata: { name: t }
spec:
  rules:
    - id: r1
      tools: [mcp.call]
      apps: [midpoint]
      toolNames: { allow: ["disable_user"] }
      effect: allow
      mode: approve
      approve: { timeoutSeconds: 99999 }
`, "approve.timeoutSeconds"},
		{"approve retryTTL over cap", `
apiVersion: straza.dev/v1beta1
kind: PolicySet
metadata: { name: t }
spec:
  rules:
    - id: r1
      tools: [mcp.call]
      apps: [midpoint]
      toolNames: { allow: ["disable_user"] }
      effect: allow
      mode: approve
      approve: { retryTTLSeconds: 601 }
`, "approve.retryTTLSeconds"},
		{"ticket bad class", `
apiVersion: straza.dev/v1beta1
kind: PolicySet
metadata: { name: t }
spec:
  rules:
    - id: r1
      tools: [mcp.call]
      apps: [vault]
      toolNames: { allow: ["rotate_root_key"] }
      effect: allow
      mode: approve
      approve: { class: bogus }
`, "approve.class must be hold or ticket"},
		{"ticket bad bind", `
apiVersion: straza.dev/v1beta1
kind: PolicySet
metadata: { name: t }
spec:
  rules:
    - id: r1
      tools: [mcp.call]
      apps: [vault]
      toolNames: { allow: ["rotate_root_key"] }
      effect: allow
      mode: approve
      approve: { class: ticket, bind: sideways }
`, "approve.bind must be fingerprint or predicate"},
		{"ticket ttl over cap", `
apiVersion: straza.dev/v1beta1
kind: PolicySet
metadata: { name: t }
spec:
  rules:
    - id: r1
      tools: [mcp.call]
      apps: [vault]
      toolNames: { allow: ["rotate_root_key"] }
      effect: allow
      mode: approve
      approve: { class: ticket, ticketTTLSeconds: 9999999 }
`, "approve.ticketTTLSeconds"},
		{"grant ttl over cap", `
apiVersion: straza.dev/v1beta1
kind: PolicySet
metadata: { name: t }
spec:
  rules:
    - id: r1
      tools: [mcp.call]
      apps: [vault]
      toolNames: { allow: ["rotate_root_key"] }
      effect: allow
      mode: approve
      approve: { class: ticket, grantTTLSeconds: 99999 }
`, "approve.grantTTLSeconds"},
		{"timeoutSeconds under ticket", `
apiVersion: straza.dev/v1beta1
kind: PolicySet
metadata: { name: t }
spec:
  rules:
    - id: r1
      tools: [mcp.call]
      apps: [vault]
      toolNames: { allow: ["rotate_root_key"] }
      effect: allow
      mode: approve
      approve: { class: ticket, timeoutSeconds: 120 }
`, "timeoutSeconds is not valid under class: ticket"},
		{"retryTTLSeconds under ticket", `
apiVersion: straza.dev/v1beta1
kind: PolicySet
metadata: { name: t }
spec:
  rules:
    - id: r1
      tools: [mcp.call]
      apps: [vault]
      toolNames: { allow: ["rotate_root_key"] }
      effect: allow
      mode: approve
      approve: { class: ticket, retryTTLSeconds: 300 }
`, "retryTTLSeconds is not valid under class: ticket (a ticket's approval can be used for grantTTLSeconds after the decision; remove retryTTLSeconds and set grantTTLSeconds instead)"},
		{"empty interpreters block", `
apiVersion: straza.dev/v1beta1
kind: PolicySet
metadata: { name: t }
spec:
  rules:
    - id: r1
      interpreters: {}
      effect: deny
`, "at least one pattern list"},
		{"interpreters bad regex", `
apiVersion: straza.dev/v1beta1
kind: PolicySet
metadata: { name: t }
spec:
  rules:
    - id: r1
      interpreters: { deny: ["re:[unclosed"] }
      effect: deny
`, "bad regex"},
		{"unknown field", `
apiVersion: straza.dev/v1beta1
kind: PolicySet
metadata: { name: t }
spec:
  rules:
    - id: r1
      effect: deny
      surprise: true
`, "field surprise not found"},
		{"duplicate rule ids", `
apiVersion: straza.dev/v1beta1
kind: PolicySet
metadata: { name: t }
spec:
  rules:
    - { id: r1, effect: deny }
    - { id: r1, effect: allow }
`, "duplicate rule id"},
		{"bad regex", `
apiVersion: straza.dev/v1beta1
kind: PolicySet
metadata: { name: t }
spec:
  rules:
    - id: r1
      command: { denyPatterns: ["re:[unclosed"] }
      effect: deny
`, "bad regex"},
		{"effect allow with deny-only side", `
apiVersion: straza.dev/v1beta1
kind: PolicySet
metadata: { name: t }
spec:
  rules:
    - id: r1
      command: { denyPatterns: ["rm *"] }
      effect: allow
`, "effect allow with only deny-side"},
		{"bad harness constraint", `
apiVersion: straza.dev/v1beta1
kind: PolicySet
metadata: { name: t }
spec:
  rules:
    - id: r1
      effect: allow
      require: { harness: ["claude-code>=two"] }
`, "bad harness version"},
		{"empty require", `
apiVersion: straza.dev/v1beta1
kind: PolicySet
metadata: { name: t }
spec:
  rules:
    - id: r1
      effect: allow
      require: {}
`, "at least one predicate"},
		{"bad name", `
apiVersion: straza.dev/v1beta1
kind: PolicySet
metadata: { name: "Bad Name!" }
spec:
  rules:
    - { id: r1, effect: deny }
`, "metadata.name"},
		{"bad priority", `
apiVersion: straza.dev/v1beta1
kind: PolicySet
metadata: { name: t }
spec:
  priority: -1
  rules:
    - { id: r1, effect: deny }
`, "priority"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Parse([]byte(tc.yaml))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("want error containing %q, got %v", tc.want, err)
			}
		})
	}
}
