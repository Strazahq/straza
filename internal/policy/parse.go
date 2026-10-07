package policy

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"
)

// APIVersion is the canonical document version (spec/policyset v1beta1).
// New documents MUST declare it. An id under any other domain is refused.
const APIVersion = "straza.dev/v1beta1"

// Ticket TTL caps (spec/policyset §2 item 8, revision 6):
// ticketTTLSeconds cap 30 d (long enough for a change window), grantTTLSeconds
// cap 24 h (short enough that a stale grant cannot be cashed weeks later). A
// zero value means "unset" and normalizes to the class default at compile.
const (
	maxTicketTTLSeconds = 30 * 24 * 60 * 60 // 2592000 (30 d)
	maxGrantTTLSeconds  = 24 * 60 * 60      // 86400 (24 h)
)

var nameRe = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,62}[a-z0-9])?$`)

// ParseAll decodes a YAML stream of one or more PolicySet documents
// (`---`-separated), validating each.
func ParseAll(raw []byte) ([]Document, error) {
	dec := yaml.NewDecoder(bytes.NewReader(raw))
	dec.KnownFields(true)
	var docs []Document
	for {
		var doc Document
		err := dec.Decode(&doc)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("policy: parse document %d: %w", len(docs), err)
		}
		if err := validate(&doc); err != nil {
			return nil, fmt.Errorf("policy: document %d (%s): %w", len(docs), doc.Metadata.Name, err)
		}
		docs = append(docs, doc)
	}
	if len(docs) == 0 {
		return nil, fmt.Errorf("policy: no documents in input")
	}
	return docs, nil
}

// Parse strictly decodes and validates one PolicySet YAML document,
// returning it with defaults applied (events → [tool.pre]). The validator
// MUST agree with spec/policyset/policyset.schema.json on the example
// corpus, enforced by TestParserAgreesWithSpecExamples.
func Parse(raw []byte) (Document, error) {
	dec := yaml.NewDecoder(bytes.NewReader(raw))
	dec.KnownFields(true)
	var doc Document
	if err := dec.Decode(&doc); err != nil {
		return Document{}, fmt.Errorf("policy: parse: %w", err)
	}
	if err := validate(&doc); err != nil {
		return Document{}, fmt.Errorf("policy: %w", err)
	}
	return doc, nil
}

func validate(doc *Document) error {
	var errs []error
	fail := func(format string, args ...any) {
		errs = append(errs, fmt.Errorf(format, args...))
	}

	if doc.APIVersion != APIVersion {
		fail("apiVersion must be %q, got %q", APIVersion, doc.APIVersion)
	}
	if doc.Kind != "PolicySet" {
		fail("kind must be PolicySet, got %q", doc.Kind)
	}
	if !nameRe.MatchString(doc.Metadata.Name) {
		fail("metadata.name %q must match %s", doc.Metadata.Name, nameRe)
	}
	if doc.Spec.Priority < 0 || doc.Spec.Priority > 1_000_000 {
		fail("spec.priority %d out of range [0, 1000000]", doc.Spec.Priority)
	}
	if len(doc.Spec.Rules) == 0 {
		fail("spec.rules must contain at least one rule")
	}
	// match.groups (retired, revision 12): presence fails, even empty. The
	// unified role model removed groups, so this selector could only ever
	// match nothing, and a deny that silently matches nothing is the exact
	// failure this parse error prevents.
	if doc.Spec.Match.Groups != nil {
		fail("spec.match.groups was retired by the unified role model (spec/policyset revision 12): groups no longer exist; select on roles instead")
	}
	// match.identity (revision 9): spec-fixed vocabularies fail loudly at
	// parse time; a typoed userType would otherwise silently match nothing.
	if id := doc.Spec.Match.Identity; id != nil {
		for _, v := range id.UserType {
			if !knownUserTypes[v] {
				fail("spec.match.identity.userType %q is not human/agent/service", v)
			}
		}
		for _, v := range id.AgencyMode {
			if !knownAgencyModes[v] {
				fail("spec.match.identity.agencyMode %q is not interactive/supervised/autonomous", v)
			}
		}
		for _, v := range id.SwarmID {
			if v == "" {
				fail("spec.match.identity.swarmId entries must be non-empty")
			}
		}
		if len(id.UserType) == 0 && len(id.AgencyMode) == 0 && len(id.SwarmID) == 0 {
			fail("spec.match.identity must set at least one of userType/agencyMode/swarmId")
		}
	}

	seen := map[string]bool{}
	for i := range doc.Spec.Rules {
		r := &doc.Spec.Rules[i]
		where := fmt.Sprintf("rule %d (%s)", i, r.ID)

		if !nameRe.MatchString(r.ID) {
			fail("%s: id must match %s", where, nameRe)
		}
		if seen[r.ID] {
			fail("%s: duplicate rule id", where)
		}
		seen[r.ID] = true

		if len(r.Events) == 0 {
			r.Events = []string{EventToolPre}
		}
		for _, e := range r.Events {
			if !knownEvents[e] {
				fail("%s: unknown event %q", where, e)
			}
		}
		for _, tool := range r.Tools {
			if !knownTools[tool] {
				fail("%s: unknown tool %q", where, tool)
			}
		}
		if r.Effect != "" && r.Effect != EffectAllow && r.Effect != EffectDeny {
			fail("%s: effect must be allow or deny, got %q", where, r.Effect)
		}
		if r.Mode != "" && r.Mode != ModeServerCheck && r.Mode != ModeClassify && r.Mode != ModeApprove && r.Mode != ModeConfirm {
			fail("%s: mode must be serverCheck, classify, approve, or confirm, got %q", where, r.Mode)
		}
		if r.Mode == ModeConfirm && r.Approve != nil {
			// Confirm (revision 11) shares the approve knob block, but the
			// decider pool is not configurable: the requester IS the decider.
			// Reject the pool fields rather than ignore them (no dead knobs).
			if len(r.Approve.Roles) > 0 {
				fail("%s: mode confirm may not set approve.roles (the requester is the decider)", where)
			}
			if r.Approve.SelfApproval {
				fail("%s: mode confirm may not set approve.selfApproval (implied: the requester decides)", where)
			}
			if len(r.Approve.Deciders) > 0 {
				fail("%s: mode confirm may not set approve.deciders (the requester is the decider)", where)
			}
		}
		if r.Approve != nil {
			// The approve block is meaningful only under mode approve or
			// confirm; a stray block elsewhere is almost always an authoring
			// mistake.
			if r.Mode != ModeApprove && r.Mode != ModeConfirm {
				fail("%s: approve block requires mode: approve or mode: confirm", where)
			}
			// Class selects the shape (revision 6): empty ⇒ hold (default).
			class := r.Approve.Class
			if class != "" && class != ClassHold && class != ClassTicket {
				fail("%s: approve.class must be hold or ticket, got %q", where, class)
			}
			if b := r.Approve.Bind; b != "" && b != BindFingerprint && b != BindPredicate {
				fail("%s: approve.bind must be fingerprint or predicate, got %q", where, b)
			}
			// Fingerprint coverage (revision 7): empty ⇒ call (default) at
			// compile; only the two known scopes are legal.
			if b := r.Approve.Binding; b != "" && b != ApproveBindingCall && b != ApproveBindingTool {
				fail("%s: approve.binding must be call or tool, got %q", where, b)
			}
			// Caps validated when explicitly set; a zero value means "unset"
			// and is normalized to the class default at compile.
			if t := r.Approve.TimeoutSeconds; t < 0 || t > 3600 {
				fail("%s: approve.timeoutSeconds %d out of range [1, 3600]", where, t)
			}
			if t := r.Approve.RetryTTLSeconds; t < 0 || t > 600 {
				fail("%s: approve.retryTTLSeconds %d out of range [1, 600]", where, t)
			}
			if t := r.Approve.TicketTTLSeconds; t < 0 || t > maxTicketTTLSeconds {
				fail("%s: approve.ticketTTLSeconds %d out of range [1, %d]", where, t, maxTicketTTLSeconds)
			}
			if t := r.Approve.GrantTTLSeconds; t < 0 || t > maxGrantTTLSeconds {
				fail("%s: approve.grantTTLSeconds %d out of range [1, %d]", where, t, maxGrantTTLSeconds)
			}
			// The blocking-wait knobs are inert for a ticket (no hold, and
			// grantTTLSeconds is its use window), so flag a set value as an
			// authoring mistake, the same posture as the "approve block
			// requires mode: approve" guard above.
			if class == ClassTicket {
				if r.Approve.TimeoutSeconds != 0 {
					fail("%s: approve.timeoutSeconds is not valid under class: ticket (a ticket never blocks; use ticketTTLSeconds/grantTTLSeconds)", where)
				}
				if r.Approve.RetryTTLSeconds != 0 {
					fail("%s: approve.retryTTLSeconds is not valid under class: ticket (a ticket's approval can be used for grantTTLSeconds after the decision; remove retryTTLSeconds and set grantTTLSeconds instead)", where)
				}
			}
			// Notification routing (revision 8): the channel vocabulary is
			// spec-fixed. Unknown names and duplicates are authoring mistakes
			// (fail-closed typo safety: a misspelled channel must never
			// silently widen back to all-configured).
			seenNotify := map[string]bool{}
			for _, ch := range r.Approve.Notify {
				if !knownNotifyChannels[ch] {
					fail("%s: approve.notify entries must be console, slack, or push, got %q", where, ch)
				}
				if seenNotify[ch] {
					fail("%s: approve.notify has duplicate entry %q", where, ch)
				}
				seenNotify[ch] = true
			}
			// Decider kinds (revision 13): the vocabulary is spec-fixed,
			// today sponsor alone. Same fail-closed typo posture as notify:
			// a misspelled kind must never silently fall back to the admin
			// pool.
			seenDecider := map[string]bool{}
			for _, dk := range r.Approve.Deciders {
				if dk != DeciderSponsor {
					fail("%s: approve.deciders entries must be sponsor, got %q", where, dk)
				}
				if seenDecider[dk] {
					fail("%s: approve.deciders has duplicate entry %q", where, dk)
				}
				seenDecider[dk] = true
			}
		}
		for _, o := range r.Obligations {
			if !knownObligations[o] {
				fail("%s: unknown obligation %q", where, o)
			}
		}
		if r.Require != nil {
			if a := r.Require.Attestation; a != "" && a != "managed" && a != "advisory" && a != "none" {
				fail("%s: require.attestation must be managed|advisory|none, got %q", where, a)
			}
			for _, h := range r.Require.Harness {
				if _, _, err := parseHarnessConstraint(h); err != nil {
					fail("%s: %v", where, err)
				}
			}
			if r.Require.Attestation == "" && !r.Require.DeviceCert && len(r.Require.Harness) == 0 {
				fail("%s: require must set at least one predicate", where)
			}
		}

		hasAllowSide := (r.Command != nil && len(r.Command.AllowPatterns) > 0) ||
			(r.Paths != nil && len(r.Paths.Allow) > 0) ||
			(r.ToolNames != nil && len(r.ToolNames.Allow) > 0) ||
			(r.Interpreters != nil && len(r.Interpreters.Allow) > 0)
		hasDenySide := (r.Command != nil && len(r.Command.DenyPatterns) > 0) ||
			(r.Paths != nil && len(r.Paths.Deny) > 0) ||
			(r.ToolNames != nil && len(r.ToolNames.Deny) > 0) ||
			(r.Interpreters != nil && len(r.Interpreters.Deny) > 0)
		hasMatcher := hasAllowSide || hasDenySide

		if (r.Command != nil && !hasCommandSide(r.Command)) ||
			(r.Paths != nil && len(r.Paths.Allow)+len(r.Paths.Deny) == 0) ||
			(r.ToolNames != nil && len(r.ToolNames.Allow)+len(r.ToolNames.Deny) == 0) ||
			(r.Interpreters != nil && len(r.Interpreters.Allow)+len(r.Interpreters.Deny) == 0) {
			fail("%s: matcher blocks must contain at least one pattern list", where)
		}
		if !hasMatcher && r.Effect == "" && r.Require == nil {
			fail("%s: rule needs an effect, a matcher block, or require (it can never fire)", where)
		}
		// SPEC.md §2.4: an explicit effect must be consistent with the sides.
		if hasMatcher {
			if r.Effect == EffectAllow && !hasAllowSide {
				fail("%s: effect allow with only deny-side patterns", where)
			}
			if r.Effect == EffectDeny && !hasDenySide {
				fail("%s: effect deny with only allow-side patterns", where)
			}
		}

		for _, p := range collectPatterns(r) {
			if p == "" {
				fail("%s: empty pattern", where)
				continue
			}
			if rest, ok := strings.CutPrefix(p, "re:"); ok {
				if _, err := regexp.Compile(rest); err != nil {
					fail("%s: bad regex %q: %v", where, p, err)
				}
			}
		}
	}

	if doc.Spec.Escape != nil && strings.TrimSpace(doc.Spec.Escape.Rego) == "" {
		fail("spec.escape.rego must not be empty")
	}
	if c := doc.Spec.Capture; c != nil {
		if c.Mode != "" && c.Mode != CaptureModeVerbatim && c.Mode != CaptureModeRedact {
			fail("spec.capture.mode must be verbatim or redact, got %q", c.Mode)
		}
		if c.Mode != "" && !c.Conversations {
			fail("spec.capture.mode without conversations: true is inert; remove it or enable capture")
		}
	}
	return errors.Join(errs...)
}

func hasCommandSide(c *CommandMatch) bool {
	return len(c.AllowPatterns)+len(c.DenyPatterns) > 0
}

func collectPatterns(r *Rule) []string {
	var out []string
	if r.Command != nil {
		out = append(out, r.Command.AllowPatterns...)
		out = append(out, r.Command.DenyPatterns...)
	}
	if r.Paths != nil {
		out = append(out, r.Paths.Allow...)
		out = append(out, r.Paths.Deny...)
	}
	if r.ToolNames != nil {
		out = append(out, r.ToolNames.Allow...)
		out = append(out, r.ToolNames.Deny...)
	}
	if r.Interpreters != nil {
		out = append(out, r.Interpreters.Allow...)
		out = append(out, r.Interpreters.Deny...)
	}
	return out
}

// parseHarnessConstraint splits "name" or "name>=version" (SPEC.md §2.1).
func parseHarnessConstraint(s string) (name, minVersion string, err error) {
	name, minVersion, found := strings.Cut(s, ">=")
	if name == "" || (found && minVersion == "") {
		return "", "", fmt.Errorf("bad harness constraint %q (want name or name>=version)", s)
	}
	if found {
		for _, part := range strings.Split(minVersion, ".") {
			if part == "" || strings.Trim(part, "0123456789") != "" {
				return "", "", fmt.Errorf("bad harness version in %q (numeric dotted only)", s)
			}
		}
	}
	return name, minVersion, nil
}
