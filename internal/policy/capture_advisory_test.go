package policy

import (
	"strings"
	"testing"
)

// TestCaptureAdvisory pins the match-all verbatim capture warning: a set
// that captures conversations verbatim while selecting every session grows
// conversation storage by GB/day at fleet scale and the janitor only prunes
// past captureRetention, so the author hears it at validate/activate. The
// trigger mirrors the engine's applies() truth: a selector list that
// compiles to nil selects everyone, so absent match and empty selector
// lists both count as match-all. Any non-empty selector scopes the set and
// silences the advisory, as do mode: redact and capture-less sets.
func TestCaptureAdvisory(t *testing.T) {
	const head = `
apiVersion: straza.dev/v1beta1
kind: PolicySet
metadata: { name: t }
spec:
`
	const rules = `  rules:
    - id: keep-guardrails
      events: [tool.pre, permission.request]
      tools: [shell.exec]
      command:
        denyPatterns: ["rm -rf *", "git push --force*", "curl * | *sh*"]
      effect: deny
      reason: "Straza: destructive command blocked"
`
	cases := []struct {
		name string
		body string
		want bool
	}{
		{
			// A transcript-capture set with no match block, verbatim: the
			// shape the advisory exists for.
			name: "prod shape: absent match + verbatim warns",
			body: "  priority: 100\n  capture:\n    conversations: true\n    mode: verbatim\n" + rules,
			want: true,
		},
		{
			name: "absent mode defaults verbatim, still warns",
			body: "  capture:\n    conversations: true\n" + rules,
			want: true,
		},
		{
			name: "empty selector lists compile to select-everyone, warns",
			body: "  match:\n    roles: []\n    users: []\n  capture:\n    conversations: true\n    mode: verbatim\n" + rules,
			want: true,
		},
		// An identity block with all-empty lists cannot exist: parse refuses
		// it ("must set at least one of userType/agencyMode/swarmId"), so the
		// advisory only has to treat a PRESENT identity block as scoping.
		{
			name: "role-scoped verbatim is silent",
			body: "  match:\n    roles: [dev]\n  capture:\n    conversations: true\n    mode: verbatim\n" + rules,
			want: false,
		},
		{
			name: "identity-scoped verbatim is silent",
			body: "  match:\n    identity:\n      userType: [agent]\n  capture:\n    conversations: true\n    mode: verbatim\n" + rules,
			want: false,
		},
		{
			name: "match-all redact is silent",
			body: "  capture:\n    conversations: true\n    mode: redact\n" + rules,
			want: false,
		},
		{
			name: "match-all without capture is silent",
			body: rules,
			want: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			doc, err := Parse([]byte(head + tc.body))
			if err != nil {
				t.Fatalf("Parse: %v", err)
			}
			var hits []Advisory
			for _, adv := range Advisories(doc) {
				if adv.Code == AdvisoryCaptureVerbatim {
					hits = append(hits, adv)
				}
			}
			if tc.want && len(hits) != 1 {
				t.Fatalf("capture advisories = %d %v, want exactly 1", len(hits), hits)
			}
			if !tc.want && len(hits) != 0 {
				t.Fatalf("capture advisories = %v, want none", hits)
			}
			if tc.want {
				for _, must := range []string{"verbatim", "every session", "GB/day", "captureRetention"} {
					if !strings.Contains(hits[0].Text, must) {
						t.Errorf("advisory %q missing %q", hits[0].Text, must)
					}
				}
				if hits[0].Rule != "" {
					t.Errorf("capture advisory rule = %q, want empty (set-level)", hits[0].Rule)
				}
				if hits[0].Severity != AdvisorySeverityWarn {
					t.Errorf("capture advisory severity = %q, want %q", hits[0].Severity, AdvisorySeverityWarn)
				}
			}
		})
	}
}
