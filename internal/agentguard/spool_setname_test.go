package agentguard

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/strazahq/straza/internal/agentguard/spool"
	"github.com/strazahq/straza/internal/policy"
)

// TestSpoolCarriesSetName pins the revision-20 minimum on the client spool
// writer: a rule-decided record names the deciding SET, and a default
// decision carries setName "" (present, empty: the same honesty contract as
// the empty ruleId).
func TestSpoolCarriesSetName(t *testing.T) {
	path := filepath.Join(t.TempDir(), "spool.jsonl")
	sp := spool.NewSpool(path)

	if err := spoolAppend(sp, spoolRecord("cmd-ruled", "deny"), "s1", "snap-1",
		policy.Decision{Effect: policy.EffectDeny, RuleID: "dev-shell-rm", SetName: "dev-guardrails"}); err != nil {
		t.Fatal(err)
	}
	if err := spoolAppend(sp, spoolRecord("cmd-default", "allow"), "s1", "snap-1",
		policy.Decision{Effect: policy.EffectAllow}); err != nil {
		t.Fatal(err)
	}

	raw, err := os.ReadFile(path) // #nosec G304 -- test-owned temp path
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
	if len(lines) != 2 {
		t.Fatalf("spool lines = %d, want 2", len(lines))
	}
	if !strings.Contains(lines[0], `"setName":"dev-guardrails"`) {
		t.Errorf("rule-decided spool record misses setName:\n%s", lines[0])
	}
	if !strings.Contains(lines[1], `"setName":""`) {
		t.Errorf("default-decision spool record misses the empty setName:\n%s", lines[1])
	}
}
