package policy

import (
	"fmt"
	"maps"
	"slices"
	"strings"
)

// Approve-spec normalization defaults, applied at compile so a compiled
// Decision.Approve always carries concrete numbers for its class (spec/policyset
// §2 item 8). Hold: an omitted timeoutSeconds/retryTTLSeconds becomes 90/60.
// Ticket (revision 6): an omitted ticketTTLSeconds becomes 24h, grantTTLSeconds
// 1h, and bind fingerprint.
const (
	defaultApproveTimeoutSeconds  = 90
	defaultApproveRetryTTLSeconds = 60
	defaultTicketTTLSeconds       = 24 * 60 * 60 // 86400 (24 h)
	defaultGrantTTLSeconds        = 60 * 60      // 3600 (1 h)
)

// Engine is the compiled PDP: pure in-memory evaluation, zero I/O on the
// decision path. Safe for concurrent use. Swap whole engines atomically
// on snapshot updates.
type Engine struct {
	sets         []compiledSet
	localDefault string // profile default for local tools: allow|deny
}

type compiledSet struct {
	name     string
	priority int
	roles    map[string]bool // nil = selector absent
	users    map[string]bool
	// Identity-typology selectors (revision 9); nil = selector absent. An
	// unset subject field never matches a non-nil selector.
	userTypes   map[string]bool
	agencyModes map[string]bool
	swarmIDs    map[string]bool
	capture     *Capture // nil = set says nothing about capture
	rules       []compiledRule
	escape      *regoEscape // nil when no escape block
}

type compiledRule struct {
	id          string
	events      map[string]bool
	tools       map[string]bool // nil = any tool
	apps        map[string]bool // nil = any app
	tnAllow     []matcher
	tnDeny      []matcher
	cmdAllow    []matcher
	cmdDeny     []matcher
	pathAllow   []matcher
	pathDeny    []matcher
	interpAllow []matcher
	interpDeny  []matcher
	require     *compiledRequire
	serverCheck bool
	classify    bool
	// confirm marks a mode: confirm rule (revision 11): approve carries the
	// shared knob block (Roles empty by validation); the requester decides.
	confirm bool
	// approve is the rule's normalized approve spec (nil unless mode: approve).
	// Value-copied and normalized at compile so the engine never aliases the
	// user's Rule.Approve and Decision.Approve always carries concrete numbers.
	approve    *ApproveSpec
	effect     string
	reason     string
	hasMatcher bool
}

type compiledRequire struct {
	attestation string // "" = not required
	deviceCert  bool
	harness     []harnessConstraint
}

// NewEngine compiles the given PolicySet documents. localDefault is the
// profile default effect for local tools.
func NewEngine(docs []Document, localDefault string) (*Engine, error) {
	if localDefault != EffectAllow && localDefault != EffectDeny {
		return nil, fmt.Errorf("policy: localDefault must be allow or deny, got %q", localDefault)
	}
	e := &Engine{localDefault: localDefault}
	for _, doc := range docs {
		set, err := compileSet(doc)
		if err != nil {
			return nil, err
		}
		e.sets = append(e.sets, set)
	}
	return e, nil
}

func compileSet(doc Document) (compiledSet, error) {
	set := compiledSet{
		name:     doc.Metadata.Name,
		priority: doc.Spec.Priority,
		roles:    toSet(doc.Spec.Match.Roles),
		users:    toSet(doc.Spec.Match.Users),
		capture:  doc.Spec.Capture,
	}
	if id := doc.Spec.Match.Identity; id != nil {
		set.userTypes = toSet(id.UserType)
		set.agencyModes = toSet(id.AgencyMode)
		set.swarmIDs = toSet(id.SwarmID)
	}
	for _, r := range doc.Spec.Rules {
		cr, err := compileRule(doc.Metadata.Name, r)
		if err != nil {
			return compiledSet{}, err
		}
		set.rules = append(set.rules, cr)
	}
	if doc.Spec.Escape != nil {
		esc, err := compileRego(doc.Metadata.Name, doc.Spec.Escape.Rego)
		if err != nil {
			return compiledSet{}, err
		}
		set.escape = esc
	}
	return set, nil
}

func compileRule(setName string, r Rule) (compiledRule, error) {
	cr := compiledRule{
		id:          r.ID,
		events:      toSet(r.Events),
		tools:       toSet(r.Tools),
		apps:        toSet(r.Apps),
		serverCheck: r.Mode == ModeServerCheck,
		classify:    r.Mode == ModeClassify,
		effect:      r.Effect,
		reason:      r.Reason,
	}
	if (r.Mode == ModeApprove || r.Mode == ModeConfirm) && r.Approve != nil {
		cr.approve = normalizeApprove(r.Approve)
	} else if r.Mode == ModeApprove || r.Mode == ModeConfirm {
		// mode: approve with no block ⇒ all-default gate (straza-admin
		// fallback, 90/60), so a winning allow still carries a concrete spec.
		// A bare mode: confirm gets the same concrete defaults; its empty
		// Roles are never consulted (the requester is the decider).
		cr.approve = normalizeApprove(&ApproveSpec{})
	}
	cr.confirm = r.Mode == ModeConfirm
	var err error
	compileAll := func(patterns []string, path bool) ([]matcher, error) {
		out := make([]matcher, 0, len(patterns))
		for _, p := range patterns {
			var m matcher
			var cerr error
			if path {
				m, cerr = compilePathPattern(p)
			} else {
				m, cerr = compileTextPattern(p)
			}
			if cerr != nil {
				return nil, fmt.Errorf("policy: set %s rule %s: %w", setName, r.ID, cerr)
			}
			out = append(out, m)
		}
		return out, nil
	}
	if r.Command != nil {
		if cr.cmdAllow, err = compileAll(r.Command.AllowPatterns, false); err != nil {
			return compiledRule{}, err
		}
		if cr.cmdDeny, err = compileAll(r.Command.DenyPatterns, false); err != nil {
			return compiledRule{}, err
		}
	}
	if r.ToolNames != nil {
		if cr.tnAllow, err = compileAll(r.ToolNames.Allow, false); err != nil {
			return compiledRule{}, err
		}
		if cr.tnDeny, err = compileAll(r.ToolNames.Deny, false); err != nil {
			return compiledRule{}, err
		}
	}
	if r.Paths != nil {
		if cr.pathAllow, err = compileAll(r.Paths.Allow, true); err != nil {
			return compiledRule{}, err
		}
		if cr.pathDeny, err = compileAll(r.Paths.Deny, true); err != nil {
			return compiledRule{}, err
		}
	}
	if r.Interpreters != nil {
		if cr.interpAllow, err = compileAll(r.Interpreters.Allow, false); err != nil {
			return compiledRule{}, err
		}
		if cr.interpDeny, err = compileAll(r.Interpreters.Deny, false); err != nil {
			return compiledRule{}, err
		}
	}
	cr.hasMatcher = len(cr.cmdAllow)+len(cr.cmdDeny)+len(cr.tnAllow)+len(cr.tnDeny)+
		len(cr.pathAllow)+len(cr.pathDeny)+len(cr.interpAllow)+len(cr.interpDeny) > 0

	if r.Require != nil {
		req := &compiledRequire{attestation: r.Require.Attestation, deviceCert: r.Require.DeviceCert}
		for _, h := range r.Require.Harness {
			name, minV, err := parseHarnessConstraint(h)
			if err != nil {
				return compiledRule{}, fmt.Errorf("policy: set %s rule %s: %w", setName, r.ID, err)
			}
			req.harness = append(req.harness, harnessConstraint{name: name, minVersion: minV})
		}
		cr.require = req
	}
	return cr, nil
}

// normalizeApprove deep-copies a user's ApproveSpec and fills zero-value knobs
// with the compile defaults for its class, so the engine owns its own data
// (never aliases Rule.Approve) and every winning approve allow carries concrete
// numbers.
//
// Class normalizes empty ⇒ hold. A HOLD spec is byte-identical to pre-revision-6
// behavior: only timeout/retry are defaulted (90/60) and the ticket-only knobs
// are never populated. A TICKET spec defaults ticketTTL/grantTTL/bind and leaves
// the blocking-wait knobs untouched (inert for a ticket; the parser already
// rejects a ticket that sets them).
func normalizeApprove(a *ApproveSpec) *ApproveSpec {
	out := *a
	if len(a.Roles) > 0 {
		out.Roles = append([]string(nil), a.Roles...)
	}
	if len(a.Notify) > 0 {
		out.Notify = append([]string(nil), a.Notify...)
	}
	if len(a.Deciders) > 0 {
		out.Deciders = append([]string(nil), a.Deciders...)
	}
	if out.Class == "" {
		out.Class = ClassHold
	}
	// Fingerprint coverage (revision 7): every winning approve allow carries a
	// concrete Binding so the PEPs never re-derive the default. Applies to both
	// classes: the fingerprint is minted on hold and ticket lanes alike.
	if out.Binding == "" {
		out.Binding = ApproveBindingCall
	}
	if out.Class == ClassTicket {
		if out.TicketTTLSeconds <= 0 {
			out.TicketTTLSeconds = defaultTicketTTLSeconds
		}
		if out.GrantTTLSeconds <= 0 {
			out.GrantTTLSeconds = defaultGrantTTLSeconds
		}
		if out.Bind == "" {
			out.Bind = BindFingerprint
		}
		return &out
	}
	// Hold (default): unchanged from pre-ticket normalization.
	if out.TimeoutSeconds <= 0 {
		out.TimeoutSeconds = defaultApproveTimeoutSeconds
	}
	if out.RetryTTLSeconds <= 0 {
		out.RetryTTLSeconds = defaultApproveRetryTTLSeconds
	}
	return &out
}

func toSet(list []string) map[string]bool {
	if len(list) == 0 {
		return nil
	}
	m := make(map[string]bool, len(list))
	for _, s := range list {
		m[s] = true
	}
	return m
}

// CaptureDirective is the session-level conversation-capture decision:
// whether this subject's prompts/replies are recorded, and in which mode.
type CaptureDirective struct {
	Conversations bool   `json:"conversations"`
	Mode          string `json:"mode,omitempty"` // verbatim|redact
}

// Capture resolves the subject's capture directive across every matched set.
// Any matched set with `conversations: true` enables capture; when matched
// sets disagree on mode, REDACT wins: a jurisdiction-mandated minimization
// must not be overridable by a second, looser set.
func (e *Engine) Capture(sub Subject) CaptureDirective {
	d := CaptureDirective{}
	for i := range e.sets {
		s := &e.sets[i]
		if s.capture == nil || !s.capture.Conversations || !s.applies(sub) {
			continue
		}
		d.Conversations = true
		if s.capture.Mode == CaptureModeRedact {
			d.Mode = CaptureModeRedact
		} else if d.Mode == "" {
			d.Mode = CaptureModeVerbatim
		}
	}
	return d
}

func (s *compiledSet) applies(sub Subject) bool {
	if s.roles != nil && !anyIn(sub.Roles, s.roles) {
		return false
	}
	if s.users != nil && !s.users[sub.User] {
		return false
	}
	// Identity selectors (revision 9). An unclassified subject ("" field)
	// fails any present selector; the map never contains "".
	if s.userTypes != nil && !s.userTypes[sub.UserType] {
		return false
	}
	if s.agencyModes != nil && !s.agencyModes[sub.AgencyMode] {
		return false
	}
	if s.swarmIDs != nil && !s.swarmIDs[sub.SwarmID] {
		return false
	}
	return true
}

func anyIn(items []string, set map[string]bool) bool {
	for _, it := range items {
		if set[it] {
			return true
		}
	}
	return false
}

type fired struct {
	effect      string
	serverCheck bool
	classify    bool
	approve     *ApproveSpec
	priority    int
	setName     string
	ruleIdx     int
	rule        *compiledRule
}

// knownKindList and knownToolList are the canonical kinds and tools a rule
// may name, sorted and joined for the unknown-kind and unknown-tool sentences.
var (
	knownKindList = strings.Join(slices.Sorted(maps.Keys(knownEvents)), ", ")
	knownToolList = strings.Join(slices.Sorted(maps.Keys(knownTools)), ", ")
)

// Evaluate runs the full decision procedure (spec/policyset SPEC.md §2–§5)
// for one canonical event. An event whose kind is outside the canonical set
// is denied before any rule is read, in both profiles.
func (e *Engine) Evaluate(ev Event, sub Subject) Decision {
	if !knownEvents[ev.Kind] {
		// No rule can name such a kind, so policy has nothing to judge it
		// by, and unknown state is a deny.
		return Decision{Effect: EffectDeny, Reason: fmt.Sprintf(
			"Straza: denied, because the event kind %q is not one Straza knows and policy cannot judge it. Send one of the known kinds: %s.",
			ev.Kind, knownKindList)}
	}
	// Precompute command forms and cleaned paths once per event.
	var cmdForms []string
	if ev.Tool == ToolShellExec {
		argv := ev.Argv
		if len(argv) == 0 && ev.Command != "" {
			argv = shellWords(ev.Command)
		}
		cmdForms = make([]string, 0, len(argv)+2)
		if ev.Command != "" {
			cmdForms = append(cmdForms, ev.Command)
		}
		if len(argv) > 0 {
			cmdForms = append(cmdForms, joinWords(argv))
			cmdForms = append(cmdForms, argv...)
		}
	}
	var paths []string
	if len(ev.Paths) > 0 {
		paths = make([]string, len(ev.Paths))
		for i, p := range ev.Paths {
			paths[i] = cleanPath(p, ev.Workspace)
		}
	}

	var all []fired
	var best *fired
	better := func(a, b *fired) bool { // is a better than b for reporting?
		if a.priority != b.priority {
			return a.priority > b.priority
		}
		if a.setName != b.setName {
			return a.setName < b.setName
		}
		return a.ruleIdx < b.ruleIdx
	}

	denySeen := false
	for si := range e.sets {
		set := &e.sets[si]
		if !set.applies(sub) {
			continue
		}
		for ri := range set.rules {
			r := &set.rules[ri]
			if !r.events[ev.Kind] {
				continue
			}
			if r.tools != nil && !r.tools[ev.Tool] {
				continue
			}
			if r.apps != nil && (ev.Tool != ToolMCPCall || !r.apps[ev.App]) {
				continue
			}
			effect, ok := r.verdict(ev, sub, cmdForms, paths)
			if !ok {
				continue
			}
			f := fired{effect: effect, serverCheck: r.serverCheck, classify: r.classify,
				approve: r.approve, priority: set.priority, setName: set.name, ruleIdx: ri, rule: r}
			all = append(all, f)
			if effect == EffectDeny {
				denySeen = true
			}
		}
	}

	if len(all) == 0 {
		d := e.defaultDecision(ev)
		return e.applyEscapes(ev, sub, d)
	}

	winning := EffectAllow
	if denySeen {
		winning = EffectDeny
	}
	serverCheck := false
	classify := false
	var approveFired *fired
	for i := range all {
		if all[i].effect != winning {
			continue
		}
		if winning == EffectAllow && all[i].serverCheck {
			serverCheck = true
		}
		if winning == EffectAllow && all[i].classify {
			classify = true
		}
		// The approve spec is threaded only on a winning allow (a deny is
		// final and drops the marker); among several approve rules the
		// highest-priority one by the same tie-break wins, so the reported
		// spec is deterministic.
		if winning == EffectAllow && all[i].approve != nil {
			if approveFired == nil || better(&all[i], approveFired) {
				approveFired = &all[i]
			}
		}
		if best == nil || better(&all[i], best) {
			best = &all[i]
		}
	}

	d := Decision{
		Effect:      winning,
		RuleID:      best.rule.id,
		SetName:     best.setName,
		Reason:      best.rule.reason,
		ServerCheck: serverCheck,
		Classify:    classify,
	}
	if approveFired != nil {
		// Engine invariant (revision 11, parallel of the revision-9 clamp
		// below): a mode: confirm rule cannot be satisfied by an
		// autonomous-agency subject: there is no human behind the session to
		// give the confirmation, the requester identity is often an NHI that
		// no decide surface will ever accept, and a record would only sit
		// until expiry. Fail fast and honestly with a plain deny instead of
		// minting an undecidable hold.
		if approveFired.rule.confirm && sub.AgencyMode == "autonomous" {
			d.Effect = EffectDeny
			d.RuleID = approveFired.rule.id
			d.SetName = approveFired.setName
			d.Reason = fmt.Sprintf("Straza: rule %s/%s requires the requester's confirmation; an autonomous session has no human to confirm", approveFired.setName, approveFired.rule.id)
			d.ServerCheck = false
			d.Classify = false
			return e.applyEscapes(ev, sub, d)
		}
		// The hold is what the caller experiences, so the decision and the
		// approval record built from it name the rule that mandated the hold,
		// whatever its priority or set order against a plain allow that also
		// fired (revision 18).
		d.RuleID = approveFired.rule.id
		d.SetName = approveFired.setName
		d.Reason = approveFired.rule.reason
		// Hand out a per-Decision struct copy (Roles slice aliased read-only)
		// so callers cannot mutate the engine's compiled spec.
		spec := *approveFired.approve
		// Engine invariant (revision 9): an autonomous-agency subject never
		// receives selfApproval, whatever the rule says: there is no human
		// behind the session to BE the self, so honoring it would let the
		// agent approve its own gated calls. Clamped per-decision (the same
		// evaluation for an interactive subject keeps the rule's value);
		// recorded approvals then persist the clamped bit, so every decide
		// surface refuses uniformly (ErrSelfApproval).
		if sub.AgencyMode == "autonomous" {
			spec.SelfApproval = false
		}
		d.Approve = &spec
		d.Confirm = approveFired.rule.confirm
	}
	if d.Effect == EffectDeny && d.Reason == "" {
		d.Reason = fmt.Sprintf("Straza: blocked by policy rule %s/%s", best.setName, best.rule.id)
	}
	return e.applyEscapes(ev, sub, d)
}

// verdict computes a rule's outcome; ok=false means the rule did not fire.
func (r *compiledRule) verdict(ev Event, sub Subject, cmdForms, paths []string) (string, bool) {
	if r.require != nil && !r.require.satisfiedBy(sub) {
		return EffectDeny, true
	}
	if !r.hasMatcher {
		if r.effect == "" {
			return "", false // require-only rule whose predicates passed
		}
		return r.effect, true
	}
	// Deny side first (SPEC.md §2.2).
	if matchAnyOf(r.cmdDeny, cmdForms) {
		return EffectDeny, true
	}
	if len(r.pathDeny) > 0 {
		for _, p := range paths {
			if matchAny(r.pathDeny, p) {
				return EffectDeny, true
			}
		}
	}
	if ev.Tool == ToolMCPCall && len(r.tnDeny) > 0 && matchAny(r.tnDeny, ev.ToolName) {
		return EffectDeny, true
	}
	// Interpreter sides are consulted only when the event carries an
	// interpreter tag: `interpreters: {deny: ["*"]}` must never fire on a
	// non-interpreter event (glob `*` would otherwise match the empty string).
	if ev.Interpreter != "" && len(r.interpDeny) > 0 && matchAny(r.interpDeny, ev.Interpreter) {
		return EffectDeny, true
	}
	// Allow side: any allow block hit grants; paths require ALL paths in.
	if matchAnyOf(r.cmdAllow, cmdForms) {
		return EffectAllow, true
	}
	if len(r.pathAllow) > 0 && len(paths) > 0 {
		allIn := true
		for _, p := range paths {
			if !matchAny(r.pathAllow, p) {
				allIn = false
				break
			}
		}
		if allIn {
			return EffectAllow, true
		}
	}
	if ev.Tool == ToolMCPCall && len(r.tnAllow) > 0 && matchAny(r.tnAllow, ev.ToolName) {
		return EffectAllow, true
	}
	if ev.Interpreter != "" && len(r.interpAllow) > 0 && matchAny(r.interpAllow, ev.Interpreter) {
		return EffectAllow, true
	}
	return "", false
}

func (rq *compiledRequire) satisfiedBy(sub Subject) bool {
	if rq.attestation != "" && attestationRank[sub.Attestation] < attestationRank[rq.attestation] {
		return false
	}
	if rq.deviceCert && !sub.DeviceCert {
		return false
	}
	if len(rq.harness) > 0 {
		ok := false
		for _, c := range rq.harness {
			if c.satisfiedBy(sub.Harness) {
				ok = true
				break
			}
		}
		if !ok {
			return false
		}
	}
	return true
}

// defaultDecision applies SPEC.md §3.3 when no rule fired.
func (e *Engine) defaultDecision(ev Event) Decision {
	blocking := ev.Kind == EventToolPre || ev.Kind == EventPermissionRequest
	if !blocking {
		return Decision{Effect: EffectAllow, Default: true}
	}
	switch {
	case ev.Tool == ToolMCPCall && ev.Granted:
		// Access grants, policies gate (revision 17): a role's access row
		// is the grant, so a granted call with no rule runs. The reason
		// names the fact so the audit record can tell it from a rule allow.
		return Decision{
			Effect:  EffectAllow,
			Default: true,
			Reason:  "allowed by role access. No policy rule gates this tool.",
		}
	case ev.Tool == ToolMCPCall:
		return Decision{
			Effect:  EffectDeny,
			Default: true,
			Reason:  fmt.Sprintf("Straza: no role of yours has access to MCP tool %s/%s. Ask an admin to give a role you hold access to it, or to allow it by policy.", ev.App, ev.ToolName),
		}
	case e.localDefault == EffectDeny:
		// Every tool but mcp.call takes the profile default: a canonical
		// local tool, a tool outside the canonical set and no tool alike,
		// so an unknown tool is never an unconditional allow. Only a
		// canonical tool can be allowed by a rule that names it, so the
		// other two are told to send a known tool instead.
		reason := fmt.Sprintf("Straza: %s is not permitted by default in this profile. Ask an admin for a policy rule that allows it.", ev.Tool)
		switch {
		case ev.Tool == "":
			reason = fmt.Sprintf("Straza: denied, because this %s event names no tool and policy cannot judge it. Send one of the known tools: %s.", ev.Kind, knownToolList)
		case !knownTools[ev.Tool]:
			reason = fmt.Sprintf("Straza: denied, because the tool %q is not one Straza knows and policy cannot judge it. Send one of the known tools: %s.", ev.Tool, knownToolList)
		}
		return Decision{Effect: EffectDeny, Default: true, Reason: reason}
	default:
		return Decision{Effect: EffectAllow, Default: true}
	}
}

func joinWords(argv []string) string {
	n := 0
	for _, w := range argv {
		n += len(w) + 1
	}
	b := make([]byte, 0, n)
	for i, w := range argv {
		if i > 0 {
			b = append(b, ' ')
		}
		b = append(b, w...)
	}
	return string(b)
}
