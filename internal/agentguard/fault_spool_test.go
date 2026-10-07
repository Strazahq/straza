package agentguard_test

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/strazahq/straza/internal/agentguard"
)

// TestSpoolUnwritableNeverBlocksDecisions is a fault-injection test: a full or
// unwritable disk under the audit spool must not change a single decision.
// Client-side audit is best-effort and async, and the spool is retried by
// later triggers, never inline with the verdict. Simulated by squatting a
// DIRECTORY on the spool path so every append fails.
func TestSpoolUnwritableNeverBlocksDecisions(t *testing.T) {
	base, _ := bootStrazad(t)
	home := t.TempDir()
	t.Setenv("STRAZA_HOME", home)
	env := []string{"STRAZA_HOME=" + home, "CLAUDECODE=1"}

	agStore, err := agentguard.OpenStore()
	if err != nil {
		t.Fatal(err)
	}
	driveEnroll(t, agStore, base)

	if _, code := hook(t, "claude-code", `{"hook_event_name":"SessionStart","session_id":"s1","cwd":"/work"}`, env); code != 0 {
		t.Fatalf("session.start blocked: %d", code)
	}

	// Disk goes bad: the spool path cannot be opened for append anymore.
	if err := os.MkdirAll(agStore.SpoolPath(), 0o755); err != nil {
		t.Fatal(err)
	}

	// The deny still denies, with its reason intact.
	rmOut, rmCode := hook(t, "claude-code",
		`{"hook_event_name":"PreToolUse","session_id":"s1","cwd":"/work","tool_name":"Bash","tool_input":{"command":"rm -rf /"}}`, env)
	if rmCode != 2 {
		t.Fatalf("rm -rf not blocked with spool unwritable (exit %d): %s", rmCode, rmOut)
	}
	var resp struct {
		HookSpecificOutput struct {
			PermissionDecision       string `json:"permissionDecision"`
			PermissionDecisionReason string `json:"permissionDecisionReason"`
		} `json:"hookSpecificOutput"`
	}
	if err := json.Unmarshal([]byte(rmOut), &resp); err != nil {
		t.Fatalf("deny output not JSON: %v: %s", err, rmOut)
	}
	if resp.HookSpecificOutput.PermissionDecision != "deny" || resp.HookSpecificOutput.PermissionDecisionReason == "" {
		t.Fatalf("deny lost its shape under spool failure: %+v", resp.HookSpecificOutput)
	}

	// And the allow still allows: no spurious fail-closed from audit I/O.
	lsOut, lsCode := hook(t, "claude-code",
		`{"hook_event_name":"PreToolUse","session_id":"s1","cwd":"/work","tool_name":"Bash","tool_input":{"command":"ls -la"}}`, env)
	if lsCode != 0 {
		t.Fatalf("benign command blocked with spool unwritable (exit %d): %s", lsCode, lsOut)
	}
	if strings.Contains(lsOut, `"deny"`) {
		t.Fatalf("allow answered deny: %s", lsOut)
	}
}
