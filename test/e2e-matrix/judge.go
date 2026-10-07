package e2ematrix

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// hookOutcome is what one straza hook invocation produced.
type hookOutcome struct {
	Exit    int
	Stdout  []byte
	Stderr  []byte
	Elapsed time.Duration
}

// hookDoc is the union of the two decision encodings: gemini's strict JSON
// (decision, reason) and the claude-code family's hookSpecificOutput.
type hookDoc struct {
	Decision           string `json:"decision"`
	Reason             string `json:"reason"`
	HookSpecificOutput struct {
		PermissionDecision       string `json:"permissionDecision"`
		PermissionDecisionReason string `json:"permissionDecisionReason"`
	} `json:"hookSpecificOutput"`
}

// judgeHook applies the per-dialect rules the header of
// spec/conformance/tier1/cases.yaml states: allow is exit 0 with no deny in
// the response; a gemini deny is strict JSON with decision deny and a reason;
// a claude-code deny is exit 2 and/or permissionDecision deny with the reason
// in permissionDecisionReason or on stderr; a codex deny is exit 2 with the
// reason on stderr; block is any refusal; silent means empty stdout on an
// allow. python-sdk is judged like claude-code, the encoding its mapping
// table names. contextContains is checked against the stdout document.
func judgeHook(dialect string, want map[string]any, o hookOutcome) (bool, string) {
	var doc hookDoc
	stdout := bytes.TrimSpace(o.Stdout)
	if len(stdout) > 0 {
		_ = json.Unmarshal(stdout, &doc)
	}
	denied := doc.Decision == "deny" || doc.HookSpecificOutput.PermissionDecision == "deny" ||
		(dialect != "gemini" && o.Exit == 2)
	reason := doc.Reason
	if reason == "" {
		reason = doc.HookSpecificOutput.PermissionDecisionReason
	}
	if reason == "" {
		reason = strings.TrimSpace(string(o.Stderr))
	}
	wantDecision, _ := want["decision"].(string)
	silent, _ := want["silent"].(bool)
	reasonWant, _ := want["reasonContains"].(string)
	contextWant, _ := want["contextContains"].(string)

	switch wantDecision {
	case "allow":
		if o.Exit != 0 {
			return false, fmt.Sprintf("want allow, got exit %d (stderr: %s)", o.Exit, trim(string(o.Stderr)))
		}
		if denied {
			return false, "want allow, got a deny response"
		}
	case "deny":
		switch dialect {
		case "gemini":
			if doc.Decision != "deny" {
				return false, fmt.Sprintf("gemini deny must be strict JSON {\"decision\":\"deny\"} on stdout, got %q (exit %d)", trim(string(stdout)), o.Exit)
			}
			reason = doc.Reason
		case "codex":
			if o.Exit != 2 {
				return false, fmt.Sprintf("codex honours only exit code 2 as a block, got exit %d", o.Exit)
			}
			reason = strings.TrimSpace(string(o.Stderr))
			if reason == "" {
				return false, "codex reads the deny reason from stderr only, and stderr is empty"
			}
		default:
			if !denied {
				return false, fmt.Sprintf("want deny, got allow (exit %d, stdout %q)", o.Exit, trim(string(stdout)))
			}
		}
		if reason == "" {
			return false, "deny carried no reason"
		}
	case "block":
		if o.Exit == 0 && !denied {
			return false, "want a refusal (non-zero exit or a deny response), got a clean allow"
		}
	case "":
	default:
		return false, fmt.Sprintf("want.decision %q is not allow, deny or block", wantDecision)
	}
	if silent && len(stdout) > 0 {
		return false, fmt.Sprintf("want silence (empty stdout), got %q", trim(string(stdout)))
	}
	if reasonWant != "" && !strings.Contains(strings.ToLower(reason), strings.ToLower(reasonWant)) {
		return false, fmt.Sprintf("reason %q does not mention %q", reason, reasonWant)
	}
	if contextWant != "" && !strings.Contains(string(o.Stdout), contextWant) {
		return false, fmt.Sprintf("injected context does not contain %q (stdout %q)", contextWant, trim(string(stdout)))
	}
	return true, ""
}

// trim shortens a string for a failure message.
func trim(s string) string {
	s = strings.TrimSpace(s)
	if len(s) > 300 {
		return s[:300] + "..."
	}
	return s
}
