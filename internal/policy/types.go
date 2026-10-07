// Package policy implements the Straza policy plane: PolicySet
// parsing and validation, the evaluator, the Rego escape hatch, and
// snapshot compilation. The same package runs inside strazad and straza:
// one engine, no semantic drift.
//
// Wire format authority: spec/policyset (schema + SPEC.md). Tests consume
// the spec examples and conformance fixtures directly.
package policy

import "encoding/json"

// Canonical event kinds (spec/hook-profile).
const (
	EventSessionStart      = "session.start"
	EventPromptSubmit      = "prompt.submit"
	EventToolPre           = "tool.pre"
	EventToolPost          = "tool.post"
	EventPermissionRequest = "permission.request"
	EventSubagentStart     = "subagent.start"
	EventSubagentStop      = "subagent.stop"
	EventSessionEnd        = "session.end"
	EventCompactPre        = "compact.pre"
)

// Canonical tool taxonomy (spec/hook-profile).
const (
	ToolShellExec = "shell.exec"
	ToolFileRead  = "file.read"
	ToolFileWrite = "file.write"
	ToolFileEdit  = "file.edit"
	ToolNetFetch  = "net.fetch"
	ToolMCPCall   = "mcp.call"
	ToolTaskSpawn = "task.spawn"
	ToolOther     = "other"
)

// Effects and obligations.
const (
	EffectAllow = "allow"
	EffectDeny  = "deny"

	ModeServerCheck = "serverCheck"
	ModeClassify    = "classify"
	ModeApprove     = "approve"
	// ModeConfirm (revision 11) is the requester-decides gate: the human
	// behind the session must confirm before the effect is final. It shares
	// the approve knob block (class/timeouts/bind/binding/notify) but the
	// decider pool fields (roles, selfApproval) are REJECTED at parse:
	// "requester" is a fact on the record, not configuration.
	ModeConfirm = "confirm"

	// Decider kinds (`approve.deciders`, revision 13). DeciderSponsor routes
	// decide rights and the announcement to the person behind the agent,
	// resolved at request time: the requester's sponsor, or since revision
	// 19 the requester when that is a person with no sponsor, whose record
	// becomes a confirm record. Since revision 14 it is also the DEFAULT for
	// a bare approve (no roles, no deciders, no selfApproval), and a pool
	// that resolves entirely empty denies at request time
	// (approval.UnroutableError).
	DeciderSponsor = "sponsor"

	// Approve classes (spec/policyset §2 item 8; revision 6, long-running
	// approvals). ClassHold (the default) is the bounded blocking wait,
	// expiry = deny (every pre-revision-6 approve rule). ClassTicket is the
	// day-scale request whose grant a later (possibly fresh) session consumes.
	ClassHold   = "hold"
	ClassTicket = "ticket"

	// Ticket grant bindings (revision 6). BindFingerprint (the default) binds
	// a ticket grant to exactly this call's EventKey. BindPredicate is
	// reserved since revision 22: it parses and every PEP treats it as
	// BindFingerprint.
	BindFingerprint = "fingerprint"
	BindPredicate   = "predicate"

	// Approve fingerprint coverage (`approve.binding`, revision 7). Consulted
	// only when fingerprinting an mcp.call event; every other lane always
	// binds the concrete call. ApproveBindingCall (the secure default) folds
	// the canonicalized tools/call arguments into the fingerprint;
	// ApproveBindingTool is the explicit, diffable opt-out for high-frequency
	// benign tools whose key covers tool identity only. Orthogonal to the
	// ticket `bind` knob above (bind selects WHAT re-matches a grant; binding
	// selects what the fingerprint COVERS).
	ApproveBindingCall = "call"
	ApproveBindingTool = "tool"

	// Notification-routing channels (`approve.notify`, revision 8). The
	// vocabulary is spec-fixed; unknown names are rejected at validation.
	// NotifyConsole names the always-on pull surface; it matches no push
	// notifier, so `notify: [console]` alone silences third-party
	// notification for the rule. Distinct from the `notify` OBLIGATION
	// below, which is a generic decision side-effect, not approval routing.
	NotifyConsole = "console"
	NotifySlack   = "slack"
	NotifyPush    = "push"

	ObligationRedact = "redact"
	ObligationNotify = "notify"

	// Conversation-capture modes (spec/policyset `capture:` block). Verbatim
	// is the default, because a sanitized transcript cannot prove a leak.
	// Redact is the minimization knob for deployments that need it.
	CaptureModeVerbatim = "verbatim"
	CaptureModeRedact   = "redact"
)

var (
	knownEvents = map[string]bool{
		EventSessionStart: true, EventPromptSubmit: true, EventToolPre: true,
		EventToolPost: true, EventPermissionRequest: true, EventSubagentStart: true,
		EventSubagentStop: true, EventSessionEnd: true, EventCompactPre: true,
	}
	knownTools = map[string]bool{
		ToolShellExec: true, ToolFileRead: true, ToolFileWrite: true, ToolFileEdit: true,
		ToolNetFetch: true, ToolMCPCall: true, ToolTaskSpawn: true, ToolOther: true,
	}
	knownObligations = map[string]bool{ObligationRedact: true, ObligationNotify: true}
	// Identity-typology vocabularies (revision 9 match.identity). swarmId is
	// deliberately unenumerated: fleet names are deployment-defined.
	knownUserTypes   = map[string]bool{"human": true, "agent": true, "service": true}
	knownAgencyModes = map[string]bool{"interactive": true, "supervised": true, "autonomous": true}
	// knownNotifyChannels is the revision-8 `approve.notify` vocabulary.
	knownNotifyChannels = map[string]bool{NotifyConsole: true, NotifySlack: true, NotifyPush: true}
)

// Document is a parsed PolicySet (spec/policyset v1beta1).
type Document struct {
	APIVersion string   `yaml:"apiVersion" json:"apiVersion"`
	Kind       string   `yaml:"kind" json:"kind"`
	Metadata   Metadata `yaml:"metadata" json:"metadata"`
	Spec       Spec     `yaml:"spec" json:"spec"`
}

// Metadata names the set.
type Metadata struct {
	Name        string `yaml:"name" json:"name"`
	Description string `yaml:"description,omitempty" json:"description,omitempty"`
}

// Spec is the policy body.
type Spec struct {
	Priority int      `yaml:"priority,omitempty" json:"priority,omitempty"`
	Match    Match    `yaml:"match,omitempty" json:"match,omitempty"`
	Capture  *Capture `yaml:"capture,omitempty" json:"capture,omitempty"`
	Rules    []Rule   `yaml:"rules" json:"rules"`
	Escape   *Escape  `yaml:"escape,omitempty" json:"escape,omitempty"`
}

// Capture opts the set's matched sessions into conversation capture.
// Off unless a matched set turns
// it on; mode defaults to verbatim. Deployment note (SPEC.md §7): clients
// older than this field reject documents that use it; roll clients first.
type Capture struct {
	Conversations bool   `yaml:"conversations" json:"conversations"`
	Mode          string `yaml:"mode,omitempty" json:"mode,omitempty"` // verbatim|redact
}

// Match selects subjects: OR within a list, AND across present lists,
// empty = all sessions.
type Match struct {
	Roles []string `yaml:"roles,omitempty" json:"roles,omitempty"`
	Users []string `yaml:"users,omitempty" json:"users,omitempty"`
	// Groups was the mapped-group selector, retired by revision 12 (the
	// unified role model: groups no longer exist as an object). The field
	// survives ONLY so validate() can refuse it with a pointed error
	// instead of the generic unknown-key failure; it never matches.
	Groups []string `yaml:"groups,omitempty" json:"groups,omitempty"`
	// Identity (revision 9) selects on the identity typology the checkin
	// resolved onto the subject. Same combining: OR within each list, AND
	// across lists and with roles/users/groups. An UNSET subject field
	// matches no listed value; unclassified identities fall outside
	// identity-scoped sets (restrictive postures are therefore written
	// deny-unless-<type>; SPEC.md §1.4). Documents that SET identity require
	// revision-9 clients (strict-parser caveat).
	Identity *IdentityMatch `yaml:"identity,omitempty" json:"identity,omitempty"`
}

// IdentityMatch is the revision-9 typology selector. Vocabulary is
// spec-fixed for userType/agencyMode; swarmId is deployment-defined.
type IdentityMatch struct {
	UserType   []string `yaml:"userType,omitempty" json:"userType,omitempty"`     // human|agent|service
	AgencyMode []string `yaml:"agencyMode,omitempty" json:"agencyMode,omitempty"` // interactive|supervised|autonomous
	SwarmID    []string `yaml:"swarmId,omitempty" json:"swarmId,omitempty"`
}

// Rule is one declarative policy rule (semantics: spec/policyset SPEC.md §2).
type Rule struct {
	ID           string        `yaml:"id" json:"id"`
	Events       []string      `yaml:"events,omitempty" json:"events,omitempty"`
	Tools        []string      `yaml:"tools,omitempty" json:"tools,omitempty"`
	Apps         []string      `yaml:"apps,omitempty" json:"apps,omitempty"`
	ToolNames    *AllowDeny    `yaml:"toolNames,omitempty" json:"toolNames,omitempty"`
	Command      *CommandMatch `yaml:"command,omitempty" json:"command,omitempty"`
	Paths        *AllowDeny    `yaml:"paths,omitempty" json:"paths,omitempty"`
	Interpreters *AllowDeny    `yaml:"interpreters,omitempty" json:"interpreters,omitempty"`
	Require      *Require      `yaml:"require,omitempty" json:"require,omitempty"`
	Mode         string        `yaml:"mode,omitempty" json:"mode,omitempty"`
	Approve      *ApproveSpec  `yaml:"approve,omitempty" json:"approve,omitempty"`
	Effect       string        `yaml:"effect,omitempty" json:"effect,omitempty"`
	Reason       string        `yaml:"reason,omitempty" json:"reason,omitempty"`
	Obligations  []string      `yaml:"obligations,omitempty" json:"obligations,omitempty"`
}

// ApproveSpec configures a `mode: approve` rule (spec/policyset §2.8): a
// winning allow needs a human decision before the effect is final. Empty
// Roles with no Deciders and no SelfApproval defaults to `deciders: [sponsor]`,
// so the record routes to the requester's accountable human and a request
// with no usable sponsor is denied at request time with the cause. Rules
// that want admin deciders say roles: [straza-admin].
//
// Class selects the shape. ClassHold (the default) is the bounded blocking
// wait with a hook-lane single-use exemption: TimeoutSeconds and
// RetryTTLSeconds apply, normalized to 90/60, and the ticket knobs are inert.
// ClassTicket is the day-scale request: TicketTTLSeconds (default 24h, cap
// 30d) is the decision window, GrantTTLSeconds (default 1h, cap 24h) the
// post-approval consume window, Bind (default fingerprint) how a later call
// re-matches the grant, and the blocking-wait knobs are inert. Compilation
// normalizes zero values so a compiled Decision carries concrete numbers.
type ApproveSpec struct {
	Roles           []string `yaml:"roles,omitempty" json:"roles,omitempty"`
	Class           string   `yaml:"class,omitempty" json:"class,omitempty"` // hold|ticket (default hold)
	TimeoutSeconds  int      `yaml:"timeoutSeconds,omitempty" json:"timeoutSeconds,omitempty"`
	SelfApproval    bool     `yaml:"selfApproval,omitempty" json:"selfApproval,omitempty"`
	RetryTTLSeconds int      `yaml:"retryTTLSeconds,omitempty" json:"retryTTLSeconds,omitempty"`
	// Ticket-class knobs (class: ticket only); zero ⇒ compile-time default.
	TicketTTLSeconds int    `yaml:"ticketTTLSeconds,omitempty" json:"ticketTTLSeconds,omitempty"`
	GrantTTLSeconds  int    `yaml:"grantTTLSeconds,omitempty" json:"grantTTLSeconds,omitempty"`
	Bind             string `yaml:"bind,omitempty" json:"bind,omitempty"` // fingerprint (default); predicate is reserved and behaves the same
	// Binding (revision 7) selects what the approval fingerprint COVERS for
	// mcp.call events: call (default; canonicalized arguments fold into the
	// key) or tool (identity only, an explicit loosening). Ignored for
	// non-mcp lanes, which always bind the concrete call. Documents that SET
	// it require revision-7 clients (strict-parser caveat).
	Binding string `yaml:"binding,omitempty" json:"binding,omitempty"` // call|tool (default call)
	// Deciders (revision 13) names decider KINDS resolved per record at
	// request time, composable with Roles. The only kind today is
	// DeciderSponsor: the requester's accountable human (User.Sponsor, the
	// IdM-owned edge), resolved when the record is created and persisted on
	// it, so a later sponsor change never silently retargets an open record.
	// Rejected under mode: confirm exactly like the pool fields (the
	// requester is the decider there). Documents that SET it require
	// revision-13 clients (strict-parser caveat).
	Deciders []string `yaml:"deciders,omitempty" json:"deciders,omitempty"`
	// Notify (revision 8) narrows which notification channels announce this
	// rule's approvals: members are
	// NotifyConsole/NotifySlack/NotifyPush; empty/omitted means every
	// configured channel (pre-revision-8 behavior). Routing narrows the
	// ANNOUNCEMENT only; WHO may decide stays Roles/SelfApproval, and every
	// decision surface (console, CLI, API) keeps working regardless. Unknown
	// members, empties, and duplicates are rejected at validation. Documents
	// that SET it require revision-8 clients (strict-parser caveat).
	Notify []string `yaml:"notify,omitempty" json:"notify,omitempty"`
}

// AllowDeny is a two-sided pattern matcher (deny side wins).
type AllowDeny struct {
	Allow []string `yaml:"allow,omitempty" json:"allow,omitempty"`
	Deny  []string `yaml:"deny,omitempty" json:"deny,omitempty"`
}

// CommandMatch matches shell commands (SPEC.md §4 command semantics).
type CommandMatch struct {
	AllowPatterns []string `yaml:"allowPatterns,omitempty" json:"allowPatterns,omitempty"`
	DenyPatterns  []string `yaml:"denyPatterns,omitempty" json:"denyPatterns,omitempty"`
}

// Require gates a rule on session claims.
type Require struct {
	Attestation string   `yaml:"attestation,omitempty" json:"attestation,omitempty"`
	DeviceCert  bool     `yaml:"deviceCert,omitempty" json:"deviceCert,omitempty"`
	Harness     []string `yaml:"harness,omitempty" json:"harness,omitempty"`
}

// Escape is the per-set Rego tightening hook.
type Escape struct {
	Rego string `yaml:"rego" json:"rego"`
}

// Event is the canonical input to the evaluator (spec/hook-profile).
type Event struct {
	Kind     string   `json:"kind" yaml:"kind"`
	Tool     string   `json:"tool,omitempty" yaml:"tool"`
	App      string   `json:"app,omitempty" yaml:"app"`           // mcp.call: app name
	ToolName string   `json:"toolName,omitempty" yaml:"toolName"` // mcp.call: upstream tool
	Command  string   `json:"command,omitempty" yaml:"command"`   // shell.exec: raw command
	Argv     []string `json:"argv,omitempty" yaml:"argv"`         // shell.exec: parsed argv (optional)
	// Args carries the mcp.call tools/call arguments verbatim (spec/hook-profile
	// `args`, revision 2026-07-25): the gateway sets it from the post-strip
	// payload, the hook lane forwards the harness tool_input. nil = the args
	// were NOT observed (old client, argless hook dialect); the approval
	// fingerprint then honestly degrades to tool scope; "{}" = the observed
	// no-argument call. The evaluator ignores it (matchers never read it in
	// v1beta1); only the approval fingerprint consumes it, off the fast path.
	// YAML-excluded: decision fixtures don't express it, JSON fixtures can.
	Args json.RawMessage `json:"args,omitempty" yaml:"-"`
	// Interpreter tags shell.exec events that invoke a known interpreter
	// ("python3", "bash", …) so policy can match them without any model
	// (spec/hook-profile). Computed by DetectInterpreter.
	Interpreter string   `json:"interpreter,omitempty" yaml:"interpreter"`
	Paths       []string `json:"paths,omitempty" yaml:"paths"`         // file.*: affected paths
	Workspace   string   `json:"workspace,omitempty" yaml:"workspace"` // ${workspace} substitution root
	// Granted marks an mcp.call whose subject holds a role with access to
	// the tool (spec/policyset revision 17). Only a binding-checking path
	// sets it: the gateway after the tier-one binding lookup, the catalog
	// overlay and the admin preview after the matcher test, and simulate
	// after its own access check. The hook lane never sets it, /v1/decide
	// zeroes it on ingress, and the native straza app never carries it.
	// With no rule firing a granted mcp.call allows and an ungranted one
	// denies; a rule that fires wins either way.
	Granted bool `json:"granted,omitempty" yaml:"granted"`
}

// Subject is the session identity the evaluator decides for.
type Subject struct {
	User        string   `json:"user" yaml:"user"`   // username
	Roles       []string `json:"roles" yaml:"roles"` // resolved role names
	Attestation string   `json:"attestation" yaml:"attestation"`
	DeviceCert  bool     `json:"deviceCert" yaml:"deviceCert"`
	Harness     string   `json:"harness" yaml:"harness"` // "<name>/<version>"
	// Identity typology (revision 9): resolved at checkin from the user row,
	// matched by Match.Identity, and consumed by the autonomous selfApproval
	// clamp. Empty = unclassified (matches no identity selector).
	UserType   string `json:"userType,omitempty" yaml:"userType"`
	AgencyMode string `json:"agencyMode,omitempty" yaml:"agencyMode"`
	SwarmID    string `json:"swarmId,omitempty" yaml:"swarmId"`
	// Sponsor is the accountable human of an agent, resolved at checkin
	// from the user row: the username for deny texts and the user id for
	// the credential broker, which keys rows by id. Not a match selector.
	// Empty when the user has no sponsor or the sponsor is unknown.
	Sponsor   string `json:"sponsor,omitempty" yaml:"sponsor"`
	SponsorID string `json:"sponsorId,omitempty" yaml:"sponsorId"`
}

// Decision is the evaluation outcome.
type Decision struct {
	Effect      string   `json:"effect"` // allow|deny
	RuleID      string   `json:"ruleId,omitempty"`
	SetName     string   `json:"setName,omitempty"`
	Reason      string   `json:"reason,omitempty"`
	Obligations []string `json:"obligations,omitempty"`
	// ServerCheck marks decisions a local PDP must not act on alone: the
	// caller asks POST /v1/decide, offline ⇒ deny.
	ServerCheck bool `json:"serverCheck,omitempty"`
	// Classify marks allows that need a classifier verdict before the
	// effect is final (mode: classify): the PEP runs its Classifier, and an
	// error or deadline ⇒ deny (fail-closed, same posture as ServerCheck).
	Classify bool `json:"classify,omitempty"`
	// Approve marks allows that need a human decision before the effect is
	// final (mode: approve): the PEP resolves it through the approval
	// service: the gateway blocks until resolution or timeout, the hook
	// lane answers deny-with-reference and honors a single-use exemption on
	// retry. Carries the rule's normalized ApproveSpec; error, timeout, or
	// unreachable service ⇒ deny (fail-closed). Set only on winning allows.
	Approve *ApproveSpec `json:"approve,omitempty"`
	// Confirm marks an Approve decision as mode: confirm (revision 11): the
	// requester is the SOLE decider; PEP flow is identical to approve. The
	// engine never sets it for an autonomous-agency subject (such a rule
	// evaluates to a plain deny: there is no human to confirm).
	Confirm bool `json:"confirm,omitempty"`
	// Default is set when no rule fired and a profile default applied.
	Default bool `json:"default,omitempty"`
}
