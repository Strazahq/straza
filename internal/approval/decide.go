package approval

import (
	"context"
	"encoding/json"
	"errors"
	"time"
	"unicode/utf8"

	"github.com/strazahq/straza/internal/policy"
)

// The decision lane: the blocking wait a hold parks in (Await), the human
// verdict that resolves a record (Decide), the core-NATS fan-out that carries
// the resolution to every pod (broadcast/handleResolved), and the waiter
// registry the two ends meet in.

// Await blocks until the record resolves, expires, or ctx is done (gateway
// lane). It relies on the resolution broadcast to wake; a missed broadcast just
// falls through to the expiry timer and denies (fail closed, acceptable).
func (s *Service) Await(ctx context.Context, id string) (Record, error) {
	cur, err := s.st.Approvals().GetByID(ctx, id)
	if err != nil {
		return Record{}, err
	}
	rec := recordFromStore(cur)
	if rec.State != StatePending {
		return rec, nil
	}

	ch := s.addWaiter(id)
	defer s.removeWaiter(id, ch)
	// Close the register-after-resolve gap: re-read once now that the waiter is
	// installed, so a decision that landed between the first Get and the
	// registration is not lost.
	if again, err := s.st.Approvals().GetByID(ctx, id); err == nil {
		if r := recordFromStore(again); r.State != StatePending {
			return r, nil
		}
	}

	d := time.Until(rec.ExpiresAt)
	if d <= 0 {
		return s.getOr(ctx, id, rec), nil
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ch:
		return s.getOr(ctx, id, rec), nil
	case <-timer.C:
		return s.getOr(ctx, id, rec), nil
	case <-ctx.Done():
		return Record{}, ctx.Err()
	}
}

// Decide validates and applies a human verdict ("approved"|"denied"). Order:
// exists → pending/not-expired (idempotent same-verdict no-op; different-verdict
// conflict; expired) → self-approval → own decision signed by a device →
// fresh approver-role check → atomic transition (the one-time cross-pod
// gate) → broadcast → audit.
// reason is the decider's optional own words (both verdicts, validated
// here so every lane inherits the same intake gate); deviceID names the
// enrolled signing device on the signed lane ("" for console/Slack). Both ride
// the MarkDecided flip, so they can only ever attach to the winning decision.
func (s *Service) Decide(ctx context.Context, id, verdict, deciderUserID, channel, reason, deviceID string) (Record, error) {
	rec, err := s.decide(ctx, id, verdict, deciderUserID, channel, reason, deviceID)
	if errors.Is(err, errAlreadyDecided) {
		return rec, nil
	}
	return rec, err
}

// errAlreadyDecided marks a decision that found the record already resolved
// with the same verdict. Decide answers it as the idempotent success the
// phone and console lanes rely on. The Slack lane calls decide and reads it,
// so a repeated tap is not told that it recorded the verdict.
var errAlreadyDecided = errors.New("approval: already decided with this verdict")

// decide is Decide, answering the resolved record with errAlreadyDecided when
// this call did not decide it.
func (s *Service) decide(ctx context.Context, id, verdict, deciderUserID, channel, reason, deviceID string) (Record, error) {
	if verdict != string(StateApproved) && verdict != string(StateDenied) {
		return Record{}, ErrBadVerdict
	}
	if err := validateReason(reason); err != nil {
		return Record{}, err
	}
	cur, err := s.st.Approvals().GetByID(ctx, id)
	if err != nil {
		return Record{}, err // ErrNotFound → 404
	}
	rec := recordFromStore(cur)

	if rec.State != StatePending {
		return resolvedOutcome(rec, verdict)
	}
	if s.now().After(rec.ExpiresAt) {
		return Record{}, ErrExpired
	}
	if rec.Mode == policy.ModeConfirm && deciderUserID != rec.UserID {
		// mode confirm (revision 11): confirmation proves the requester's
		// presence and intent; no other identity, root included, can supply
		// that. The inverse ban below never applies to confirm records.
		return Record{}, ErrNotRequester
	}
	if rec.Mode != policy.ModeConfirm && !rec.SelfApproval && deciderUserID == rec.UserID {
		return Record{}, ErrSelfApproval
	}
	// Hard-block non-human deciders on EVERY lane: an NHI must
	// never resolve an approval even if an approve role was misassigned to it.
	// Enforced here, ahead of the role check, so console/Slack/API all inherit
	// it; it also covers confirm records whose requester is an NHI.
	nhi, err := s.isNHI(ctx, deciderUserID)
	if err != nil {
		return Record{}, err
	}
	if nhi {
		return Record{}, ErrNHIDecider
	}
	if deciderUserID == rec.UserID && deviceID == "" && !s.cfg.UnsignedOwnDecisions {
		// The requester's agent runs as the requester and can reach every
		// credential on that machine, the console's and strazactl's included.
		// Only the signed lane carries proof the agent cannot produce, a
		// signature from an enrolled device key, so an unsigned decision on
		// one's own request is refused on every lane.
		return Record{}, ErrUnsignedOwnDecision
	}
	var name string
	if rec.Mode == policy.ModeConfirm {
		// The requester needs no pool membership: on a confirm record the
		// identity match above IS the authorization.
		name = s.displayName(ctx, deciderUserID)
	} else {
		n, ok, err := s.approver(ctx, deciderUserID, rec.ApproverRoles, rec.ApproverUsers)
		if err != nil {
			return Record{}, err
		}
		if !ok {
			return Record{}, ErrNotApprover
		}
		name = n
	}

	at := s.now()
	// An approval materializes its use deadline in the same write that flips
	// the state, so it is usable once from any replica: decidedAt + grantTTL
	// for a ticket, decidedAt + retryTTL for a hold. cur carries the window
	// persisted at Request time; a zero (legacy/direct caller) falls back to
	// the same default Request would have used. A denial passes nil so
	// MarkDecided leaves grant_expires_at untouched.
	var grantExp *time.Time
	if verdict == string(StateApproved) {
		ttl, fallback := cur.RetryTTLSeconds, defaultRetryTTLSeconds
		if rec.Class == policy.ClassTicket {
			ttl, fallback = cur.GrantTTLSeconds, defaultGrantTTLSeconds
		}
		if ttl <= 0 {
			ttl = fallback
		}
		exp := at.Add(time.Duration(ttl) * time.Second)
		grantExp = &exp
	}
	won, err := s.st.Approvals().MarkDecided(ctx, id, verdict, deciderUserID, name, channel, at, grantExp, reason, deviceID)
	if err != nil {
		return Record{}, err
	}
	if !won {
		// A concurrent decider (or the sweep) resolved it between our checks and
		// the write. Re-read and reconcile.
		after, e := s.st.Approvals().GetByID(ctx, id)
		if e != nil {
			return Record{}, e
		}
		return resolvedOutcome(recordFromStore(after), verdict)
	}

	// Debug, not Info, like every state-transition line: ids only, never the
	// decider's reason text.
	s.log.Debug("approval decided", "component", "approval", "id", id, "verdict", verdict,
		"channel", channel, "decider", deciderUserID, "device", deviceID)
	final, err := s.st.Approvals().GetByID(ctx, id)
	if err != nil {
		return Record{}, err
	}
	fr := recordFromStore(final)
	s.broadcast(fr, verdict, at)
	s.auditPhase(ctx, "resolution", fr)
	// Terminal notification lane. Fired here (on the deciding pod, after the
	// one-time MarkDecided win), NOT from handleResolved, which every pod runs
	// and would duplicate it; idempotent surface updates ride reconcile there.
	s.notifyResolved(fr)
	return fr, nil
}

// reasonMaxBytes caps a decider reason at intake. 500 bytes covers a few
// sentences of prose; anything longer belongs in a ticket, and an unbounded
// column on the hot approvals table is how transcript-class bloat starts.
const reasonMaxBytes = 500

// validateReason is the single intake gate for a decider reason, every lane.
// Reject, never transform: on the signed lane the stored words must be exactly
// the words whose hash the device key signed, so a silent trim would break the
// key binding. Prose keeps \n and \t, and every other control character fails.
//
// Unicode format characters are not control characters, so the bidi direction
// set passes the rule above. An embedded override can visually reorder the
// decider's quoted words on every surface, and HTML escaping neutralizes
// markup, not reordering. Intake rejects the nine direction controls
// U+202A..U+202E and U+2066..U+2069, the marks U+200E, U+200F and U+061C, and
// the invisibles U+200B and U+FEFF, so visually identical reasons cannot differ
// byte for byte on an audit record. ZWJ and ZWNJ stay legal because emoji
// sequences and Persian or Indic orthography are words a decider legitimately
// types. The approver app mirrors this exact set; it changes only together
// with the openapi reason description.
func validateReason(reason string) error {
	if reason == "" {
		return nil
	}
	if len(reason) > reasonMaxBytes || !utf8.ValidString(reason) {
		return ErrBadReason
	}
	for _, r := range reason {
		if r < 0x20 && r != '\n' && r != '\t' {
			return ErrBadReason
		}
		if r == 0x7f {
			return ErrBadReason
		}
		switch r {
		case 0x202A, 0x202B, 0x202C, 0x202D, 0x202E, // bidi embeddings/overrides + PDF
			0x2066, 0x2067, 0x2068, 0x2069, // bidi isolates
			0x200E, 0x200F, 0x061C, // direction marks (LRM, RLM, ALM)
			0x200B, 0xFEFF: // zero-width space, ZWNBSP/BOM
			return ErrBadReason
		}
	}
	return nil
}

// resolvedOutcome maps an already-terminal record to the idempotency contract:
// same verdict is the record with errAlreadyDecided, expired is ErrExpired,
// otherwise conflict.
func resolvedOutcome(rec Record, verdict string) (Record, error) {
	switch {
	case string(rec.State) == verdict:
		return rec, errAlreadyDecided // double-tap no-op
	case rec.State == StateExpired:
		return Record{}, ErrExpired
	default:
		return Record{}, ErrConflict
	}
}

// approver reports whether deciderUserID may decide a record whose pool is
// (approverRoles, approverUsers) and returns the decider's display name for
// the record: the user-scoped pool first (revision 13, matched by username),
// then the roles (freshly resolved). An ENTIRELY empty pool falls back to
// straza-admin; a pool with users but no roles does not (the sponsor decides,
// not every admin).
func (s *Service) approver(ctx context.Context, deciderUserID string, approverRoles, approverUsers []string) (string, bool, error) {
	if len(approverUsers) > 0 {
		name := s.deciderUsername(ctx, deciderUserID)
		for _, n := range approverUsers {
			if name != "" && n == name {
				return s.displayName(ctx, deciderUserID), true, nil
			}
		}
	}
	roles, err := s.resolver.ResolveRoles(ctx, deciderUserID, s.now())
	if err != nil {
		return "", false, err
	}
	want := approverRoles
	if len(want) == 0 {
		if len(approverUsers) > 0 {
			return "", false, nil
		}
		want = []string{AdminFallbackRole}
	}
	wantSet := make(map[string]bool, len(want))
	for _, r := range want {
		wantSet[r] = true
	}
	allowed := false
	for _, r := range roles {
		if wantSet[r.Name] {
			allowed = true
			break
		}
	}
	if !allowed {
		return "", false, nil
	}
	return s.displayName(ctx, deciderUserID), true, nil
}

// displayName resolves a user's display name for the decided record
// (username, then display, then the raw id).
func (s *Service) displayName(ctx context.Context, userID string) string {
	name := userID
	if u, e := s.st.Users().GetByID(ctx, userID); e == nil {
		switch {
		case u.Username != "":
			name = u.Username
		case u.Display != "":
			name = u.Display
		}
	}
	return name
}

// handleResolved is the ONLY place resolutions are applied locally (uniform
// loopback: even the deciding pod reacts here, not inline). It wakes Await
// waiters and nudges the Slack card. It grants nothing: the use of an
// approval is the atomic write in the store (grant.go), so a replica that
// misses this message still lets the retry run exactly once.
func (s *Service) handleResolved(_ string, data []byte) {
	var b resolvedBroadcast
	if json.Unmarshal(data, &b) != nil || b.ID == "" {
		return
	}
	var rec Record
	if full, err := s.st.Approvals().GetByID(context.Background(), b.ID); err == nil {
		rec = recordFromStore(full)
	} else {
		rec = Record{ID: b.ID, SessionID: b.Session, RuleID: b.RuleID, ArgvHash: b.ArgvHash, DecidedBy: b.DecidedBy, State: State(b.State)}
	}

	s.signalWaiters(b.ID)

	// Every pod reconciles its channel surfaces (idempotent by contract; each
	// notifier self-guards on missing refs/state).
	s.notifyReconcile(rec)
}

// broadcast publishes the resolution over core-NATS (notify-only fan-out).
func (s *Service) broadcast(rec Record, verdict string, at time.Time) {
	payload, err := json.Marshal(resolvedBroadcast{
		ID: rec.ID, State: verdict, DecidedBy: rec.DecidedBy,
		Session: rec.SessionID, RuleID: rec.RuleID, ArgvHash: rec.ArgvHash,
		RetryTTLSeconds: rec.RetryTTLSeconds, DecidedAt: at.UTC().Format(time.RFC3339),
	})
	if err != nil {
		return
	}
	if err := s.bus.PublishCore(resolvedSubjectPrefix+rec.ID, payload); err != nil {
		s.log.Warn("approval resolution broadcast failed", "id", rec.ID, "err", err)
	}
}

// getOr returns the latest record, or fallback when the read fails.
func (s *Service) getOr(ctx context.Context, id string, fallback Record) Record {
	if a, err := s.st.Approvals().GetByID(ctx, id); err == nil {
		return recordFromStore(a)
	}
	return fallback
}

func (s *Service) addWaiter(id string) chan struct{} {
	ch := make(chan struct{})
	s.mu.Lock()
	s.waiters[id] = append(s.waiters[id], ch)
	s.mu.Unlock()
	return ch
}

func (s *Service) removeWaiter(id string, ch chan struct{}) {
	s.mu.Lock()
	defer s.mu.Unlock()
	ws := s.waiters[id]
	for i, w := range ws {
		if w == ch {
			s.waiters[id] = append(ws[:i], ws[i+1:]...)
			break
		}
	}
	if len(s.waiters[id]) == 0 {
		delete(s.waiters, id)
	}
}

func (s *Service) signalWaiters(id string) {
	s.mu.Lock()
	ws := s.waiters[id]
	delete(s.waiters, id)
	s.mu.Unlock()
	for _, w := range ws {
		close(w)
	}
}

// resolvedBroadcast is the core-NATS resolution payload (spec/events §5):
// straza.approval.resolved.<id>, notify-only, in-cluster. RetryTTLSeconds
// stays on the wire because spec/events §5.1 lists it and a replica of an
// older build reads it during a rolling upgrade.
type resolvedBroadcast struct {
	ID              string `json:"id"`
	State           string `json:"state"`
	DecidedBy       string `json:"decidedBy"`
	Session         string `json:"session"`
	RuleID          string `json:"ruleId"`
	ArgvHash        string `json:"argvHash"`
	RetryTTLSeconds int    `json:"retryTTLSeconds"`
	DecidedAt       string `json:"decidedAt"`
}
