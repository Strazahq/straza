package policy

import (
	"strings"
	"testing"
)

// TestAdvisories pins the non-fatal author warnings: a rule requiring
// deviceCert cannot be satisfied by ANY subject while the device-certificate
// factor is not implemented (both PEPs report false), so parse stays green
// but the author hears it loudly. Advisories never invent problems: sets
// without the predicate get none. Each advisory carries a stable code, a
// severity, and the offending rule id beside the prose.
func TestAdvisories(t *testing.T) {
	const head = `
apiVersion: straza.dev/v1beta1
kind: PolicySet
metadata: { name: t }
spec:
  rules:
`
	cases := []struct {
		name      string
		rules     string
		wantRules []string // rule ids each advisory must name, in order
	}{
		{
			name: "deviceCert require warns",
			rules: `    - id: cert-gate
      tools: [shell.exec]
      effect: allow
      require: { deviceCert: true }
`,
			wantRules: []string{"cert-gate"},
		},
		{
			name: "attestation-only require is silent",
			rules: `    - id: att-gate
      tools: [shell.exec]
      effect: allow
      require: { attestation: managed }
`,
			wantRules: nil,
		},
		{
			name: "no require is silent",
			rules: `    - id: plain
      tools: [shell.exec]
      effect: deny
`,
			wantRules: nil,
		},
		{
			name: "every offending rule is named",
			rules: `    - id: first
      tools: [shell.exec]
      effect: allow
      require: { deviceCert: true }
    - id: second
      tools: [mcp.call]
      effect: allow
      require: { attestation: managed, deviceCert: true }
`,
			wantRules: []string{"first", "second"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			doc, err := Parse([]byte(head + tc.rules))
			if err != nil {
				t.Fatalf("Parse: %v", err)
			}
			got := Advisories(doc)
			if len(got) != len(tc.wantRules) {
				t.Fatalf("Advisories = %d %v, want %d", len(got), got, len(tc.wantRules))
			}
			for i, id := range tc.wantRules {
				if got[i].Code != AdvisoryRequireUnsatisfiable {
					t.Errorf("advisory %d code = %q, want %q", i, got[i].Code, AdvisoryRequireUnsatisfiable)
				}
				if got[i].Severity != AdvisorySeverityWarn {
					t.Errorf("advisory %d severity = %q, want %q", i, got[i].Severity, AdvisorySeverityWarn)
				}
				if got[i].Rule != id {
					t.Errorf("advisory %d rule = %q, want %q", i, got[i].Rule, id)
				}
				if !strings.Contains(got[i].Text, `"`+id+`"`) {
					t.Errorf("advisory %d text = %q, want it to name rule %q", i, got[i].Text, id)
				}
				if !strings.Contains(got[i].Text, "deviceCert") || !strings.Contains(got[i].Text, "not implemented") {
					t.Errorf("advisory %d text = %q, want the deviceCert not-implemented wording", i, got[i].Text)
				}
			}
		})
	}
}

// TestRoutingAdvisories pins the revision 14 approval-routing warning: a
// bare approve rule is told it rides the sponsor default and that a
// requester with no usable sponsor is denied immediately. Declared intent
// stays quiet: explicit deciders, selfApproval, confirm, and role pools of
// ANY class draw no advisory, because design advice is not validation.
func TestRoutingAdvisories(t *testing.T) {
	const head = `
apiVersion: straza.dev/v1beta1
kind: PolicySet
metadata: { name: t }
spec:
  rules:
`
	cases := []struct {
		name      string
		rules     string
		want      []string // Text substrings, one per expected advisory, in order
		wantCodes []string // codes, parallel to want
	}{
		{
			name: "bare approve gets the sponsor-default advisory",
			rules: `    - id: bare-hold
      tools: [shell.exec]
      effect: allow
      mode: approve
`,
			want:      []string{"routes to the person behind the agent: an agent's sponsor, or the person themself when they run their own agent. An agent with no usable sponsor is denied immediately"},
			wantCodes: []string{AdvisoryApproveUnrouted},
		},
		{
			name: "hold role pool is quiet (D11 killed the clock opinion)",
			rules: `    - id: pool-hold
      tools: [shell.exec]
      effect: allow
      mode: approve
      approve: { roles: [sec-approvers] }
`,
			want: nil,
		},
		{
			name: "ticket role pool is quiet",
			rules: `    - id: pool-ticket
      tools: [mcp.call]
      effect: allow
      mode: approve
      approve: { roles: [sec-approvers], class: ticket }
`,
			want: nil,
		},
		{
			name: "explicit sponsor deciders are quiet",
			rules: `    - id: sponsor-hold
      tools: [shell.exec]
      effect: allow
      mode: approve
      approve: { deciders: [sponsor] }
`,
			want: nil,
		},
		{
			name: "selfApproval is quiet",
			rules: `    - id: self-hold
      tools: [shell.exec]
      effect: allow
      mode: approve
      approve: { selfApproval: true }
`,
			want: nil,
		},
		{
			name: "confirm is quiet",
			rules: `    - id: confirm-rule
      tools: [shell.exec]
      effect: allow
      mode: confirm
`,
			want: nil,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			doc, err := Parse([]byte(head + tc.rules))
			if err != nil {
				t.Fatalf("Parse: %v", err)
			}
			got := Advisories(doc)
			if len(got) != len(tc.want) {
				t.Fatalf("Advisories = %d %v, want %d", len(got), got, len(tc.want))
			}
			for i, sub := range tc.want {
				if !strings.Contains(got[i].Text, sub) {
					t.Errorf("advisory %d text = %q, want substring %q", i, got[i].Text, sub)
				}
				if got[i].Code != tc.wantCodes[i] {
					t.Errorf("advisory %d code = %q, want %q", i, got[i].Code, tc.wantCodes[i])
				}
				if got[i].Severity != AdvisorySeverityWarn {
					t.Errorf("advisory %d severity = %q, want %q", i, got[i].Severity, AdvisorySeverityWarn)
				}
			}
		})
	}
	// The sponsor advisory speaks plainly: the consequence is stated
	// once, in sentence case, and still names the fix.
	doc, err := Parse([]byte(head + `    - id: bare-hold
      tools: [shell.exec]
      effect: allow
      mode: approve
`))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	adv := Advisories(doc)
	if len(adv) != 1 {
		t.Fatalf("Advisories = %v, want exactly the sponsor advisory", adv)
	}
	for _, must := range []string{"denied immediately", "approve.roles / approve.deciders"} {
		if !strings.Contains(adv[0].Text, must) {
			t.Errorf("sponsor advisory %q missing %q", adv[0].Text, must)
		}
	}
	if strings.Contains(adv[0].Text, "DENIED") {
		t.Errorf("sponsor advisory %q still shouts", adv[0].Text)
	}
	if adv[0].Rule != "bare-hold" {
		t.Errorf("sponsor advisory rule = %q, want bare-hold", adv[0].Rule)
	}
}

// TestBindReservedAdvisory pins the bind-reserved warning: every approve or
// confirm rule that sets approve.bind: predicate hears that the value is
// reserved and works exactly like fingerprint, whatever its class and
// binding. A rule that sets fingerprint or leaves bind out hears nothing.
func TestBindReservedAdvisory(t *testing.T) {
	const head = `
apiVersion: straza.dev/v1beta1
kind: PolicySet
metadata: { name: t }
spec:
  rules:
    - id: gate
      tools: [mcp.call]
      effect: allow
`
	const text = `rule "gate": approve.bind: predicate is reserved and works exactly like bind: fingerprint, so it does not widen which later call may use an approval. ` +
		`Remove approve.bind so the rule says what it does. For an MCP tool whose arguments change on every call, approve.binding: tool lets one approval cover any arguments`
	cases := []struct {
		name  string
		rule  string
		codes []string // advisory codes, in order
	}{
		{"a ticket bound by predicate", "      mode: approve\n      approve: { roles: [sec], class: ticket, bind: predicate }\n", []string{AdvisoryBindReserved}},
		{"a ticket bound by fingerprint", "      mode: approve\n      approve: { roles: [sec], class: ticket, bind: fingerprint }\n", nil},
		{"a ticket with no bind", "      mode: approve\n      approve: { roles: [sec], class: ticket }\n", nil},
		{"a hold bound by predicate", "      mode: approve\n      approve: { roles: [sec], bind: predicate }\n", []string{AdvisoryBindReserved}},
		{"a confirm bound by predicate", "      mode: confirm\n      approve: { class: ticket, bind: predicate }\n", []string{AdvisoryBindReserved}},
		{"a ticket bound by predicate for any arguments", "      mode: approve\n      approve: { roles: [sec], class: ticket, bind: predicate, binding: tool }\n", []string{AdvisoryBindReserved}},
		{"a bare approve bound by predicate", "      mode: approve\n      approve: { class: ticket, bind: predicate }\n", []string{AdvisoryBindReserved, AdvisoryApproveUnrouted}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			doc, err := Parse([]byte(head + tc.rule))
			if err != nil {
				t.Fatalf("Parse: %v", err)
			}
			got := Advisories(doc)
			if len(got) != len(tc.codes) {
				t.Fatalf("Advisories = %d %v, want codes %v", len(got), got, tc.codes)
			}
			for i, code := range tc.codes {
				if got[i].Code != code || got[i].Severity != AdvisorySeverityWarn || got[i].Rule != "gate" {
					t.Errorf("advisory %d = %s/%s/%s, want %s/%s/gate", i, got[i].Code, got[i].Severity, got[i].Rule, code, AdvisorySeverityWarn)
				}
				if code == AdvisoryBindReserved && got[i].Text != text {
					t.Errorf("advisory %d text\n got %q\nwant %q", i, got[i].Text, text)
				}
			}
		})
	}
}
