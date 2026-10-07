package policy

import "fmt"

// Advisory codes: stable wire identifiers (pkg/api/openapi.yaml 0.84.0) the
// console keys tone and placement on. Never derived from the prose.
const (
	AdvisoryRequireUnsatisfiable = "require-unsatisfiable"
	AdvisoryApproveUnrouted      = "approve-unrouted"
	AdvisoryCaptureVerbatim      = "capture-verbatim"
	AdvisoryBindReserved         = "bind-reserved"
	// AdvisoryEventsNeverFire (0.85.0) is emitted by the server layer,
	// which owns the harness coverage data this package deliberately does
	// not import; the constant lives here because advisory codes are one
	// vocabulary.
	AdvisoryEventsNeverFire = "events-never-fire"
)

// AdvisorySeverityWarn is the only advisory severity today; the field exists
// so a future informational advisory needs no wire change.
const AdvisorySeverityWarn = "warn"

// Advisory is one non-fatal truth about a parsed set. Code identifies the
// class, Severity carries the tone, Rule names the offending rule (empty for
// set-level advisories like capture), Text is the human sentence.
type Advisory struct {
	Code     string `json:"code"`
	Severity string `json:"severity"`
	Rule     string `json:"rule,omitempty"`
	Text     string `json:"text"`
}

// Advisories returns non-fatal truths a policy author should hear before
// activating a set. Four classes today: rules whose require predicates
// cannot currently be satisfied by ANY subject (require-unsatisfiable: the
// device-certificate factor is not implemented, so every subject carries
// deviceCert=false and a rule requiring it denies wherever it gates an
// allow), approval rules that set the reserved bind: predicate
// (bind-reserved), approve rules with no approver named (approve-unrouted:
// the sponsor default routes each request to the person behind the agent),
// and a capture block that records every session verbatim (capture-verbatim:
// unbounded storage growth). Advisories never block parse or compile: the
// field stays legal so policies written ahead of the feature keep
// round-tripping, and the deny-on-unsatisfiable direction is fail-closed.
func Advisories(doc Document) []Advisory {
	var out []Advisory
	for _, r := range doc.Spec.Rules {
		if r.Require != nil && r.Require.DeviceCert {
			out = append(out, Advisory{
				Code:     AdvisoryRequireUnsatisfiable,
				Severity: AdvisorySeverityWarn,
				Rule:     r.ID,
				Text: fmt.Sprintf(
					"rule %q: require.deviceCert is not implemented yet: every subject carries deviceCert=false, so this rule cannot be satisfied and denies wherever it gates an allow",
					r.ID),
			})
		}
		// Revision 22 reserves bind: predicate, because no approval lane
		// reads bind and every grant is keyed by the exact call fingerprint.
		// The check sits above the approve-mode skip because a confirm rule
		// shares the approve block.
		if r.Approve != nil && r.Approve.Bind == BindPredicate {
			out = append(out, Advisory{
				Code:     AdvisoryBindReserved,
				Severity: AdvisorySeverityWarn,
				Rule:     r.ID,
				Text: fmt.Sprintf(
					"rule %q: approve.bind: predicate is reserved and works exactly like bind: fingerprint, so it does not widen which later call may use an approval. Remove approve.bind so the rule says what it does. For an MCP tool whose arguments change on every call, approve.binding: tool lets one approval cover any arguments",
					r.ID),
			})
		}
		// Revision 14 routing truth, approve mode only (confirm targets the
		// requester by construction). Declared intent stays quiet: explicit
		// deciders, selfApproval, and role pools of any class draw nothing,
		// because design advice is not validation.
		if r.Mode != ModeApprove {
			continue
		}
		a := r.Approve
		if a == nil || (len(a.Roles) == 0 && len(a.Deciders) == 0 && !a.SelfApproval) {
			out = append(out, Advisory{
				Code:     AdvisoryApproveUnrouted,
				Severity: AdvisorySeverityWarn,
				Rule:     r.ID,
				Text: fmt.Sprintf(
					"rule %q: no approver named, so each request routes to the person behind the agent: an agent's sponsor, or the person themself when they run their own agent. An agent with no usable sponsor is denied immediately. Give every agent this rule can match a sponsor, or name approve.roles / approve.deciders",
					r.ID),
			})
		}
	}
	if c := doc.Spec.Capture; c != nil && c.Conversations &&
		(c.Mode == "" || c.Mode == CaptureModeVerbatim) && matchesEveryone(doc.Spec.Match) {
		out = append(out, Advisory{
			Code:     AdvisoryCaptureVerbatim,
			Severity: AdvisorySeverityWarn,
			Text:     "capture: verbatim conversations with no match selectors records every session's full transcript: storage grows by GB/day at fleet scale and the janitor only prunes past captureRetention. Scope the match or use mode: redact if that is not the intent",
		})
	}
	return out
}

// matchesEveryone mirrors the engine's applies() truth: a selector list that
// compiles to nil selects everyone (toSet returns nil for empty lists), and
// parse refuses an identity block with all-empty lists, so a present
// identity block always scopes.
func matchesEveryone(m Match) bool {
	return len(m.Roles) == 0 && len(m.Users) == 0 && m.Identity == nil
}
