package agentguard

import (
	"os"

	"github.com/strazahq/straza/internal/policy"
)

// RunHookConformance is the hook entrypoint for spec-conformance runs:
// it decides against a PolicySet YAML file with standalone profile
// defaults instead of the enrolled session's signed snapshot: no
// checkin, no audit spool, no server. The policy source is explicit argv
// only, never an environment variable: whoever launches the harness controls
// the hook's env, and a managed install must not be redirectable to a
// different policy source that way.
func RunHookConformance(hio HookIO, policyFile string) error {
	raw, err := os.ReadFile(policyFile) // #nosec G304 -- explicit operator-supplied flag is the feature
	if err != nil {
		return fail(hio, "Straza: conformance policy unreadable: %v", err)
	}
	docs, err := policy.ParseAll(raw)
	if err != nil {
		return fail(hio, "Straza: conformance policy invalid: %v", err)
	}
	eng, err := policy.NewEngine(docs, policy.EffectAllow)
	if err != nil {
		return fail(hio, "Straza: conformance policy compile failed: %v", err)
	}
	return runHook(hio, nil, conformanceDecider{engine: eng})
}

// conformanceDecider evaluates against a local engine with a fixed anonymous
// subject (the Tier-1 policy carries no match selectors). An escalation mode
// that needs a service this mode does not have is a deny, because the client
// fails closed: a serverCheck allow has no server to ask, and an approve-marked
// allow has no approval service to resolve it (spec/policyset §2 item 8:
// unreachable approval is fail-closed).
type conformanceDecider struct{ engine *policy.Engine }

func (c conformanceDecider) Decide(n Normalized) policy.Decision {
	if n.Event.Kind != policy.EventToolPre && n.Event.Kind != policy.EventPermissionRequest {
		return policy.Decision{Effect: policy.EffectAllow, Default: true}
	}
	d := c.engine.Evaluate(n.Event, policy.Subject{User: "conformance", Attestation: "none"})
	if d.Effect == policy.EffectAllow && d.ServerCheck {
		return policy.Decision{
			Effect: policy.EffectDeny, RuleID: d.RuleID,
			Reason: "Straza: serverCheck rules cannot be satisfied in conformance mode: denied",
		}
	}
	if d.Effect == policy.EffectAllow && d.Approve != nil {
		return policy.Decision{
			Effect: policy.EffectDeny, RuleID: d.RuleID,
			Reason: "Straza: approve rules cannot be satisfied in conformance mode: denied",
		}
	}
	return d
}
