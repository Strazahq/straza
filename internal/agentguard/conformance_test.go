package agentguard

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Conformance mode has no server and no approval service by construction, so
// every escalation mode that needs one must DENY there, not sail through
// (spec/policyset SPEC §2 item 8 fail-closed posture). An approve-gated call
// must never answer plain allow with exit 0. serverCheck already denied, and
// approve is the same class. The plain-allow control proves the denies are
// rule-driven, not a broken policy load.
func TestConformanceModeEscalationsDeny(t *testing.T) {
	policyFile := filepath.Join(t.TempDir(), "policy.yaml")
	if err := os.WriteFile(policyFile, []byte(`apiVersion: straza.dev/v1beta1
kind: PolicySet
metadata: { name: conf-escalations }
spec:
  rules:
    - id: gated-deploy
      tools: [shell.exec]
      command:
        allowPatterns: ["deploy *"]
      effect: allow
      mode: approve
      reason: "deploys need a human"
    - id: gated-escalate
      tools: [shell.exec]
      command:
        allowPatterns: ["escalate *"]
      effect: allow
      mode: serverCheck
      reason: "escalations are re-checked live"
`), 0o600); err != nil {
		t.Fatal(err)
	}

	run := func(command string) (stdout, stderr string, err error) {
		var out, errb bytes.Buffer
		err = RunHookConformance(HookIO{
			Stdin: strings.NewReader(`{"hook_event_name":"PreToolUse","session_id":"s","cwd":"/w",` +
				`"tool_name":"Bash","tool_input":{"command":"` + command + `"}}`),
			Stdout:  &out,
			Stderr:  &errb,
			Environ: func() []string { return []string{"CLAUDECODE=1"} },
		}, policyFile)
		return out.String(), errb.String(), err
	}

	cases := []struct {
		name, command, wantReason string
	}{
		{"approve rule denies", "deploy prod", "approve rules cannot be satisfied in conformance mode"},
		{"serverCheck rule denies", "escalate sev1", "serverCheck rules cannot be satisfied in conformance mode"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			stdout, stderr, err := run(tc.command)
			if strings.Contains(stdout, `"allow"`) {
				t.Fatalf("conformance mode allowed an escalation-gated call: stdout=%q err=%v", stdout, err)
			}
			if !strings.Contains(stdout+stderr, tc.wantReason) {
				t.Errorf("deny must carry the explicit reason %q, got stdout=%q stderr=%q",
					tc.wantReason, stdout, stderr)
			}
		})
	}

	t.Run("plain command still allows (control)", func(t *testing.T) {
		stdout, stderr, err := run("git status")
		if err != nil {
			t.Fatalf("plain allow must not error: %v (stderr=%q)", err, stderr)
		}
		if strings.Contains(stdout+stderr, "denied") {
			t.Fatalf("control case denied (the policy load itself is broken): stdout=%q stderr=%q", stdout, stderr)
		}
	})
}
