package agentguard

import (
	"strings"
	"testing"
	"time"

	"github.com/strazahq/straza/internal/policy"
)

// TestFailClosedDenyCarriesSetName pins that the serverCheck and approveCheck
// lanes keep the firing rule's set name beside its rule id when the server is
// unreachable, so the spooled record names the deciding set the way a
// pattern or classify deny does.
func TestFailClosedDenyCarriesSetName(t *testing.T) {
	ev := policy.Event{Kind: policy.EventToolPre, Tool: policy.ToolMCPCall, App: "midpoint", ToolName: "disable_user"}
	tests := []struct {
		name  string
		local policy.Decision
		want  string
	}{
		{"serverCheck unreachable", policy.Decision{Effect: policy.EffectAllow, RuleID: "server-gated", SetName: "dev-guardrails", ServerCheck: true}, "a server-checked action"},
		{"approveCheck unreachable", policy.Decision{Effect: policy.EffectAllow, RuleID: "needs-human", SetName: "dev-guardrails", Approve: &policy.ApproveSpec{}}, "an approval-gated action"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := &LocalPDP{
				session: Session{SessionToken: "tok-1", ExpiresAt: time.Now().Add(time.Hour)},
				client:  NewClient("http://127.0.0.1:1"),
			}
			d := p.escalate(ev, tt.local)
			if d.Effect != policy.EffectDeny || d.RuleID != tt.local.RuleID || d.SetName != "dev-guardrails" {
				t.Fatalf("fail-closed decision = %+v, want a deny naming rule %q in set dev-guardrails", d, tt.local.RuleID)
			}
			if !strings.Contains(d.Reason, tt.want) {
				t.Errorf("reason %q does not name %q", d.Reason, tt.want)
			}
		})
	}
}
