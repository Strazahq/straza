// Package approval implements the human-approval workflow: the server-side service
// behind `mode: approve` rules. A winning allow that carries an ApproveSpec is
// held for a human decision: the gateway lane blocks on Await, the hook lane
// gets a deny-with-reference and lets one identical retry run. The
// console is channel #0 (always on); Slack is an opt-in notifier.
//
// Core invariants: pending records are written on the sanctioned
// /v1/decide escalation lane, never the fast PDP path; resolution fans out over
// core-NATS so the data plane stays stateless; audit rides the outbox; every
// failure mode denies (fail closed).
package approval

import (
	"context"
	"errors"
	"time"

	"github.com/strazahq/straza/internal/policy"
	"github.com/strazahq/straza/internal/store"
)

// State is the lifecycle of an approval record.
type State string

// Approval states (persisted as plain strings; see the approvals migration).
const (
	StatePending  State = "pending"
	StateApproved State = "approved"
	StateDenied   State = "denied"
	StateExpired  State = "expired"
)

// AdminFallbackRole may approve any record whose decide pool is entirely
// empty (no ApproverRoles AND no ApproverUsers); a rule can never become
// unapprovable-by-anyone. Since revision 13 such an
// unrouted record keeps these decide RIGHTS but is announced on no channel
// (the quiet default): rights without noise, so an unconfigured pool cannot
// page every admin at fleet scale.
const AdminFallbackRole = "straza-admin"

// Errors surfaced by Decide, mapped to HTTP status by the API layer.
var (
	// ErrNotFound is returned for an unknown approval id (→404).
	ErrNotFound = store.ErrNotFound
	// ErrSelfApproval: the decider is the requester and selfApproval is off (→403).
	ErrSelfApproval = errors.New("approval: requester may not approve their own request")
	// ErrNotRequester: a mode confirm record decided by anyone but its
	// requester (→403). Confirmation proves the requester's presence and
	// intent; no other identity, root included, can supply that.
	ErrNotRequester = errors.New("approval: only the requester may confirm this request")
	// ErrNotApprover: the decider holds none of the required approver roles (→403).
	ErrNotApprover = errors.New("approval: decider is not an authorized approver")
	// ErrNHIDecider: a non-human identity (kind=nhi) may never decide an
	// approval, even if a role was misassigned (→403). Enforced in Decide so
	// every lane (console, Slack, API/mobile) inherits it.
	ErrNHIDecider = errors.New("approval: non-human identities may not decide approvals")
	// ErrUnsignedOwnDecision: the decider is the requester and no enrolled
	// device signed the decision (→403). An agent on the requester's machine
	// can reach the console and strazactl credentials but not a device key,
	// so only the signed lane proves the person decided. The
	// approval.unsignedOwnDecisions config switch lifts it.
	ErrUnsignedOwnDecision = errors.New("approval: your own request needs a decision signed by your enrolled phone or browser")
	// ErrConflict: the record is already resolved with the other verdict (→409).
	ErrConflict = errors.New("approval: already resolved with a different verdict")
	// ErrExpired: the record timed out before the decision (→410).
	ErrExpired = errors.New("approval: request has expired")
	// ErrBadVerdict: verdict is neither "approved" nor "denied".
	ErrBadVerdict = errors.New("approval: verdict must be approved or denied")
	// ErrBadToken: a decision token failed verification.
	ErrBadToken = errors.New("approval: invalid decision token")
	// ErrTokenExpired: a decision token is past its embedded expiry.
	ErrTokenExpired = errors.New("approval: decision token expired")
	// ErrBadReason: a decider reason failed intake validation (over 500 bytes,
	// invalid UTF-8, control characters beyond \n and \t, or the invisible
	// direction/zero-width set validateReason enumerates). Fail closed, no
	// silent transform: on the signed lane the stored words must be exactly
	// the signed words.
	ErrBadReason = errors.New("approval: reason must be at most 500 bytes of plain text (no control characters, no invisible formatting characters)")
)

// Record is one approval, in the shape the PEPs and channels consume. It
// mirrors store.Approval with a typed State.
type Record struct {
	ID            string
	SessionID     string
	UserID        string // requester user id
	Username      string // requester username (display)
	RuleID        string
	SetName       string
	ArgvHash      string // EventKey(ev)
	Lane          string // "hook" | "gateway"
	Summary       string
	Justification string // gateway lane only, may be ""
	ApproverRoles []string
	// ApproverUsers: the user-scoped decide pool (revision 13
	// approve.deciders), USERNAMES resolved at request time (today: the
	// requester's sponsor) and persisted so a later sponsor change never
	// retargets an open record.
	ApproverUsers   []string
	SelfApproval    bool
	Mode            string // approve|confirm ("" reads as approve)
	TimeoutSeconds  int
	RetryTTLSeconds int
	State           State
	DecidedBy       string // decider user id
	DecidedByName   string
	Channel         string // "console" | "slack" | "phone" | "browser" (legacy rows: "api")
	ChannelRefs     map[string]string
	// Decided attribution. DecidedReason is the
	// decider's own words: on the signed lane its sha256 rides the signed
	// string (5th line), so the words carry the same key binding as the
	// verdict; the console lane stores it unsigned. DecidedDeviceID names the
	// enrolled device whose key signed the decision ("" off the signed lane).
	DecidedReason   string
	DecidedDeviceID string
	CreatedAt       time.Time
	ExpiresAt       time.Time
	DecidedAt       *time.Time
	// Class and use fields (spec/policyset revisions 6 and 23). Class is
	// "hold" (one run of the call by the held call or one identical retry of
	// its session) or "ticket" (a user+fingerprint-bound grant a later,
	// possibly fresh, session consumes). GrantExpiresAt is the use deadline,
	// set when either class is approved; ConsumedAt/ConsumedBy record the
	// single use win.
	Class           string
	ConsumedAt      *time.Time
	ConsumedBy      string
	GrantExpiresAt  *time.Time
	GrantTTLSeconds int // configured post-approval consume window (seconds); 0 for a hold
	// Args preview. A redaction-first, display-safe, 2 KiB-capped glance of
	// the concrete call arguments the fingerprint binds, computed at request
	// time. ArgsBytes is the redacted+neutralized length BEFORE truncation.
	// All zero when the approval.preview knob is off or no concrete args
	// exist.
	ArgsPreview   string
	ArgsTruncated bool
	ArgsBytes     int
	// Notify is the winning rule's notification routing (spec/policyset
	// revision 8 `approve.notify`), persisted at request time. Empty = every
	// configured channel. Immutable for the record's lifetime: the later
	// announcement lanes (terminal status, ticket reminder) honor exactly
	// what the rule said when the approval was raised.
	Notify []string
}

// RequestInput is the pending-record request (from the PEP escalation lane).
// Spec is the winning decision's normalized ApproveSpec (compile-time
// normalization guarantees concrete Timeout/RetryTTL numbers). ArgsPreview and
// its two companions carry the bounded, redacted args preview the PEP computed
// from the concrete call (empty when the preview knob is off).
type RequestInput struct {
	SessionID, UserID, Username, RuleID, SetName, ArgvHash, Lane, Summary, Justification string
	ArgsPreview                                                                          string
	ArgsTruncated                                                                        bool
	ArgsBytes                                                                            int
	Spec                                                                                 policy.ApproveSpec
	// Confirm marks a mode confirm decision (Decision.Confirm): the record
	// snapshots mode 'confirm' and the requester becomes the sole decider.
	Confirm bool
}

// RoleResolver is the identity-plane read used for the fresh approver check at
// decision time (identity.Resolver satisfies it).
type RoleResolver interface {
	ResolveRoles(ctx context.Context, userID string, t time.Time) ([]store.Role, error)
}

func recordFromStore(a store.Approval) Record {
	return Record{
		ID: a.ID, SessionID: a.SessionID, UserID: a.UserID, Username: a.Username,
		RuleID: a.RuleID, SetName: a.SetName, ArgvHash: a.ArgvHash, Lane: a.Lane,
		Summary: a.Summary, Justification: a.Justification, ApproverRoles: a.ApproverRoles,
		ApproverUsers: a.ApproverUsers,
		SelfApproval:  a.SelfApproval, Mode: a.Mode,
		TimeoutSeconds: a.TimeoutSeconds, RetryTTLSeconds: a.RetryTTLSeconds,
		State: State(a.State), DecidedBy: a.DecidedBy, DecidedByName: a.DecidedByName,
		Channel: a.Channel, ChannelRefs: a.ChannelRefs,
		DecidedReason: a.DecidedReason, DecidedDeviceID: a.DecidedDeviceID,
		CreatedAt: a.CreatedAt, ExpiresAt: a.ExpiresAt, DecidedAt: a.DecidedAt,
		Class: a.Class, ConsumedAt: a.ConsumedAt, ConsumedBy: a.ConsumedBy,
		GrantExpiresAt: a.GrantExpiresAt, GrantTTLSeconds: a.GrantTTLSeconds,
		ArgsPreview: a.ArgsPreview, ArgsTruncated: a.ArgsTruncated, ArgsBytes: a.ArgsBytes,
		Notify: a.Notify,
	}
}
