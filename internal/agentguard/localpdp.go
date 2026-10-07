package agentguard

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/strazahq/straza/internal/agentguard/trace"
	"github.com/strazahq/straza/internal/classifier"
	"github.com/strazahq/straza/internal/policy"
)

// LocalPDP is the client-side Policy Enforcement Point: it
// evaluates tool.pre events against the cached signed snapshot with zero
// network on the fast path, calling the server only for serverCheck rules
// or token refresh. Fail-closed on every uncertain state.
type LocalPDP struct {
	store   *Store
	client  *Client
	cfg     Config
	subject policy.Subject
	engine  *policy.Engine
	session Session
	maxAge  int64
	// classifier resolves classify-flagged allows (mode: classify).
	// It has no config surface: always the embedded heuristic; nil
	// (never in production) fails closed.
	classifier policy.Classifier
	// renewFail + renewDetail record what RefreshIfStale actually observed,
	// so the offline gate's deny reason states the truth instead of assuming
	// unreachability.
	renewFail   int
	renewDetail string
	// Trace facts for the decision journal: which escalation lane
	// ran (the deepest one wins), whether classify also ran, the server
	// answer the online lanes observed (status + correlation id; after a 401
	// bounce the retried call wins), whether that bounce ran, and whether a
	// deny was a fail-closed answer rather than a rule's. Set per decision;
	// a LocalPDP is built per hook invocation, so nothing leaks across.
	escBranch     string
	escCall       trace.Call
	escBounced    bool
	escClassified bool
	escFailClosed bool
	// agentType and agentID are the delegate attribution of the decision in
	// flight, sent with every /v1/decide call so a server-recorded verdict
	// keeps the tag a client record would carry (spec/events rev 13).
	agentType string
	agentID   string
	// serverRecorded is set where an online lane adopts a /v1/decide
	// verdict. The server audits that verdict itself, so the hook flow
	// spools no second record for it.
	serverRecorded bool
}

// pdpTraceFacts is what the journal asks a LocalPDP after Decide.
type pdpTraceFacts struct {
	branch      string // "" = no escalation lane ran
	status      int
	correlation string
	bounced     bool
	classified  bool
	failClosed  bool
}

// traceFacts returns the escalation and server facts of the last Decide.
func (p *LocalPDP) traceFacts() pdpTraceFacts {
	status, corr := p.escCall.Get()
	return pdpTraceFacts{
		branch: p.escBranch, status: status, correlation: corr,
		bounced: p.escBounced, classified: p.escClassified, failClosed: p.escFailClosed,
	}
}

// Renewal failure classes recorded by RefreshIfStale. The zero value
// (renewNone) covers both "not attempted" and "succeeded": either way the
// gate has learned nothing bad about the server this event.
const (
	renewNone      = iota
	renewTransport // unreachable or server-side failure: token state unknown
	renewRejected  // token refused (401) and re-acquire could not complete
	renewTerminal  // a refusal retrying cannot fix (revoked/disabled/re-enroll)
	renewUnjudged  // an answer below 500 that judged nothing: a 408, a 429, or no reason on another status
)

// NewLocalPDP builds the PDP from persisted state, verifying the cached
// snapshot against the pinned keys. It fails if the client is not enrolled
// or has no valid session/snapshot (fail closed).
func NewLocalPDP(store *Store, subject policy.Subject) (*LocalPDP, error) {
	cfg, err := store.LoadConfig()
	if err != nil {
		return nil, err
	}
	ses, err := store.LoadSession()
	if err != nil {
		return nil, fmt.Errorf("no active session (hook session.start first): %w", err)
	}
	signed, err := store.LoadSnapshot()
	if err != nil {
		return nil, fmt.Errorf("no cached snapshot: %w", err)
	}
	lookup, err := keyLookup(cfg.SnapshotKeys)
	if err != nil {
		return nil, err
	}
	eng, snap, err := policy.OpenSnapshot(signed, ses.SnapshotID, lookup)
	if err != nil {
		return nil, fmt.Errorf("snapshot verify failed (refuse to enforce untrusted policy): %w", err)
	}
	return &LocalPDP{
		store: store, cfg: cfg, subject: subject,
		engine: eng, session: ses, maxAge: snap.MaxAgeSecs,
		client:     NewClient(cfg.ServerURL),
		classifier: classifier.NewHeuristic(),
	}, nil
}

// Decide evaluates a normalized event. classify allows resolve through the
// embedded classifier, serverCheck allows escalate to the online PDP;
// offline ⇒ deny. Non-tool.pre events observe-only.
func (p *LocalPDP) Decide(n Normalized) policy.Decision {
	if n.Event.Kind != policy.EventToolPre && n.Event.Kind != policy.EventPermissionRequest {
		return policy.Decision{Effect: policy.EffectAllow, Default: true}
	}
	p.agentType, p.agentID = n.AgentType, n.AgentID
	// Fail-closed offline gate. The session
	// token self-validates for its ≤300 s TTL; past expiry a client may
	// keep deciding from the cached snapshot only within the profile's offline
	// grace bound: snapshot maxAge in seconds, already surfaced as p.maxAge
	// (0 for enterprise, 900 s for standalone). Beyond ExpiresAt+grace Straza
	// can no longer prove the policy is current and must deny. liveDecider.Decide
	// runs RefreshIfStale before this, so an online client's ExpiresAt already
	// reflects a successful refresh or re-acquire and passes; only a client
	// whose renewal failed (offline, or refused by the server) trips the gate.
	// Equality falls on the allow side (strict After).
	grace := time.Duration(p.maxAge) * time.Second
	end := p.session.ExpiresAt.Add(grace)
	if p.renewFail == renewTerminal {
		// A refused renewal is the server's answer, not being offline, so it
		// ends the grace at once. A transport failure, an answer that
		// judged nothing (renewUnjudged) and a 401 whose re-acquire could not
		// complete (renewRejected) keep the grace, because the server's
		// judgment of this session is unknown.
		end = p.session.ExpiresAt
	}
	if time.Now().After(end) {
		p.escFailClosed = true
		return failClosed(p.expiredReason(grace))
	}
	// The same bound holds for a policy the server refused to send: renewing
	// the token does not move PolicyDeadline (settlePolicy).
	if d := p.session.PolicyDeadline; !d.IsZero() && time.Now().After(d.Add(grace)) {
		p.escFailClosed = true
		return failClosed(policyRefusedReason(p.session.PolicyRefused))
	}
	// Single gate, whole decision: a call the harness routes through
	// the Straza gateway registration cannot bypass the gateway PEP; the
	// server evaluates CURRENT policy on this very call with the true
	// app/tool names it serves. The hook lane's copy of those names is
	// untrustworthy on codex: hook payloads carry the model-facing
	// sanitized name (every non-[A-Za-z0-9_] char becomes "_",
	// codex-mcp/src/mcp/mod.rs @rust-v0.146.0), which denies a granted
	// call ("no policy grants MCP tool demo_tools/echo" for
	// app demo-tools). Deciding here would be a second, worse gate (wrong
	// names, a 2 s budget, no blocking hold), so the honest posture is
	// allow-and-observe with the deferral on the audit record. The offline
	// fail-closed gate above deliberately stays in front: an expired client
	// fails closed everywhere. Non-gateway MCP tools are untouched; there
	// the hook is the only gate.
	if n.GatewayProxied {
		p.escBranch = "gateway"
		return policy.Decision{Effect: policy.EffectAllow,
			Reason: "Straza: decision deferred to the gateway PEP, which evaluates current policy server-side on this call (single gate)"}
	}
	return p.escalate(n.Event, p.engine.Evaluate(n.Event, p.subject))
}

// expiredReason words the fail-closed deny honestly: what RefreshIfStale
// actually observed picks the story. The default keeps the offline wording;
// with no renewal attempted (or a genuine transport failure) unreachability
// is the truthful claim; it must never be asserted after the server ANSWERED
// and refused.
func (p *LocalPDP) expiredReason(grace time.Duration) string {
	age := time.Since(p.session.ExpiresAt).Round(time.Second)
	switch p.renewFail {
	case renewRejected:
		return renewFailedReason(age, p.renewDetail)
	case renewTerminal:
		return renewRefusedReason(p.renewDetail)
	case renewUnjudged:
		return renewUnjudgedReason(age, p.renewDetail)
	default:
		return fmt.Sprintf(
			"session token expired %s ago and the offline grace period (%s) is exhausted. "+
				"Straza cannot verify current policy while the platform is unreachable; "+
				"reconnect and run `straza doctor`.", age, grace)
	}
}

// renewRefusedReason words the deny of a session whose renewal the server
// refused. detail is the server's own sentence, which names the remedy.
func renewRefusedReason(detail string) string {
	return "session renewal was refused: " + detail
}

// renewFailedReason words the deny of a session whose token expired age ago
// and whose renewal could not complete. detail says what failed.
func renewFailedReason(age time.Duration, detail string) string {
	return fmt.Sprintf("session token expired %s ago and automatic renewal failed (%s). "+
		"Restart the session, or run `straza doctor`.", age, detail)
}

// renewUnjudgedReason words the deny of a session whose token expired age
// ago and whose renewal got an answer that judged nothing about it
// (renewUnjudged). detail says what came back, as one sentence.
func renewUnjudgedReason(age time.Duration, detail string) string {
	return fmt.Sprintf("session token expired %s ago, and the renewal got an answer that does not say whether this session is still accepted: %s "+
		"The next tool call tries the renewal again. If this keeps happening, check that the configured server URL reaches strazad "+
		"with nothing in front of it that limits or blocks straza, and run `straza doctor`.", age, detail)
}

// policyRefusedReason words the deny of a session whose newer policy straza
// could not fetch, once the session time held at the refusal has run out.
// why is the refusal's own reason (refusalReason), which names the remedy.
func policyRefusedReason(why string) string {
	return "this session's policy is out of date: the server holds a newer policy that straza could not fetch. " +
		why + " Tool calls stay denied until straza can fetch the current policy, and `straza doctor` shows this reason."
}

// policyDenyAt returns when the hooks start to deny for the refusal on ses:
// its deadline plus the offline grace of the snapshot ses pins, the bound
// Decide applies. When that snapshot does not open, the hooks deny already,
// and the bare deadline is returned.
func policyDenyAt(store *Store, cfg Config, ses Session) time.Time {
	signed, err := store.LoadSnapshot()
	if err != nil {
		return ses.PolicyDeadline
	}
	lookup, err := keyLookup(cfg.SnapshotKeys)
	if err != nil {
		return ses.PolicyDeadline
	}
	_, snap, err := policy.OpenSnapshot(signed, ses.SnapshotID, lookup)
	if err != nil {
		return ses.PolicyDeadline
	}
	return ses.PolicyDeadline.Add(time.Duration(snap.MaxAgeSecs) * time.Second)
}

// escalate applies the rule-opted-in escalation lanes to an engine verdict:
// classify first (local, cheap), then approve (network, human),
// then serverCheck (network). A classify deny is final and never reaches the
// online gate; a classify allow continues so a decision carrying both flags
// still gets the server's verdict. approve resolves on the server (record +
// exemption); its verdict is final for the hook lane, so it returns before
// serverCheck. Decisions without a flag pay zero (the fast path).
//
// Gateway-proxied calls never reach this step: Decide defers their ENTIRE
// decision to the gateway PEP (single gate), so one call never costs two
// approvals whose exemptions do not cross-satisfy.
func (p *LocalPDP) escalate(ev policy.Event, d policy.Decision) policy.Decision {
	if d.Effect == policy.EffectAllow && d.Classify {
		p.escBranch, p.escClassified = "classify", true
		d = p.classifyCheck(ev, d)
	}
	if d.Effect == policy.EffectAllow && d.Approve != nil {
		p.escBranch = "approve"
		return p.approveCheck(ev, d)
	}
	if d.Effect == policy.EffectAllow && d.ServerCheck {
		p.escBranch = "server"
		return p.serverCheck(ev, d)
	}
	return d
}

// Session returns the current session state.
func (p *LocalPDP) Session() Session { return p.session }

// RefreshIfStale renews the session token when it is within 120 s of expiry
// (renewSession) and records what happened for the gate's honest deny
// reason.
func (p *LocalPDP) RefreshIfStale(ctx context.Context, store *Store) {
	p.renewFail, p.renewDetail = renewSession(ctx, p.client, store, p.cfg, &p.session)
}

// renewSession renews ses when it is within 120 s of expiry and returns the
// renewal failure class with its detail, renewNone when nothing failed. A
// transport failure keeps the old token (the server is authoritative on
// expiry; the offline gate speaks for the grace bound). A 401 refusal means
// the token itself is dead; the enrollment may still be alive, so the
// identity lane a session START uses gets one bounded try before the gate
// fails closed. A status answer below 500
// is classed by renewalAnswer. A renewal that lands is saved to store with
// its snapshot adopted blob-first. RefreshIfStale and renewBeforeFetch, the
// renewal of an expired hook with no snapshot, both run it.
func renewSession(ctx context.Context, client *Client, store *Store, cfg Config, ses *Session) (int, string) {
	if time.Until(ses.ExpiresAt) > 120*time.Second {
		return renewNone, ""
	}
	resp, err := client.Refresh(ctx, ses.SessionToken,
		harnessName(ses.Harness), harnessVersion(ses.Harness), measureAttestation(harnessName(ses.Harness)))
	if err == nil {
		adoptRefresh(ctx, client, store, cfg, ses, resp)
		return renewNone, ""
	}
	var refuse *StatusError
	switch {
	case !errors.As(err, &refuse) || refuse.Status >= 500:
		return renewTransport, err.Error()
	case refuse.Status == http.StatusUnauthorized:
		rerr := reacquire(ctx, client, store, cfg, ses)
		var rrefuse *StatusError
		switch {
		case rerr == nil:
			return renewNone, ""
		case errors.As(rerr, &rrefuse) && rrefuse.Status < 500:
			// The server judged the ENROLLMENT and said no: its message
			// is the actionable one (revoked / disabled / re-enroll).
			return renewalAnswer(rrefuse)
		}
		return renewRejected, rerr.Error()
	}
	// Any other answer below 500 judged the session itself, unless
	// renewalAnswer finds it judged nothing.
	return renewalAnswer(refuse)
}

// renewalAnswer classes a status answer below 500 to a renewal. A 408 or a
// 429 comes from something in front of strazad, whose check-in route has no
// limiter, and a status with no reason says nothing about the session, so
// both are renewUnjudged, which keeps the offline grace like a transport
// failure. A 401 or a 403 is a refusal whoever sends it: strazad always
// gives it a reason, so a bare one means a proxy replaced strazad's answer.
// Any other answer is the server's refusal, renewTerminal, with its own
// sentence as the detail.
func renewalAnswer(refuse *StatusError) (int, string) {
	msg := strings.TrimPrefix(refuse.Msg, "Straza: ")
	bare := msg == fmt.Sprintf("HTTP %d", refuse.Status)
	switch {
	case bare && (refuse.Status == http.StatusUnauthorized || refuse.Status == http.StatusForbidden):
		return renewTerminal, fmt.Sprintf("the server answered HTTP %d and gave no reason. "+
			"Ask your administrator whether this user or device is still enabled, "+
			"and check whether a proxy in front of strazad replaces its answers.", refuse.Status)
	case bare:
		return renewUnjudged, fmt.Sprintf("the server answered HTTP %d and gave no reason.", refuse.Status)
	case refuse.Status == http.StatusRequestTimeout || refuse.Status == http.StatusTooManyRequests:
		return renewUnjudged, fmt.Sprintf("the server answered HTTP %d: %s", refuse.Status, endSentence(msg))
	}
	return renewTerminal, msg
}

// reacquire establishes a NEW session on ses through the enrolled identity
// after the server refused the expired session token: the same credential
// lane a session start uses (device token, headless key, or the stored
// login token), so success means the enrollment is intact and enforcement
// continues seamlessly. The server re-runs its kill-switch denylist,
// device/user status, and attestation gates on this check-in; nothing is
// decided client-side. A decision in flight still evaluates with the subject
// and engine its PDP booted with (identical posture to a plain refresh); the
// next hook builds both from the adopted state.
func reacquire(ctx context.Context, client *Client, store *Store, cfg Config, ses *Session) error {
	id, err := store.LoadIdentity()
	if err != nil {
		return fmt.Errorf("no enrolled identity: %w", err)
	}
	resp, err := checkinWithIdentity(ctx, client, store, cfg, id,
		harnessName(ses.Harness), harnessVersion(ses.Harness))
	if err != nil {
		return err
	}
	adoptCheckin(ctx, client, store, cfg, ses, resp, time.Now())
	_ = store.SaveSession(*ses)
	// a successful checkin supersedes any previous kill (same as session start)
	_ = store.ClearRevocation()
	return nil
}

// adoptRefresh applies a successful REFRESH response to ses: same session,
// new token. Same as the daemon path: the refresh is also where a daemonless
// client picks up an IdM role change. A decision in flight still uses the
// subject its PDP booted with; the next hook builds one from the adopted
// roles. The snapshot is adopted blob-first (verified and saved before the
// id advances; on failure the old id stays: stale-but-working beats
// bricked); a decision in flight still uses the engine its PDP booted with.
func adoptRefresh(ctx context.Context, client *Client, store *Store, cfg Config, ses *Session, resp CheckinResponse) {
	held := ses.ExpiresAt
	ses.SessionToken = resp.SessionToken
	ses.ExpiresAt = time.Now().Add(time.Duration(resp.ExpiresIn) * time.Second)
	adoptIdentity(ses, resp)
	adopted, err := adoptSnapshot(ctx, client, resp.SessionToken, store, cfg, ses.SnapshotID, resp.SnapshotID)
	ses.SnapshotID = adopted
	ses.settlePolicy(resp.SnapshotID, err, held)
	_ = store.SaveSession(*ses)
}

func harnessName(h string) string {
	if i := indexByte(h, '/'); i >= 0 {
		return h[:i]
	}
	return h
}

func harnessVersion(h string) string {
	if i := indexByte(h, '/'); i >= 0 {
		return h[i+1:]
	}
	return ""
}

func indexByte(s string, b byte) int {
	for i := 0; i < len(s); i++ {
		if s[i] == b {
			return i
		}
	}
	return -1
}

// decideOnline runs POST /v1/decide for an escalation lane and, on a 401,
// re-establishes the server's session state and retries ONCE. A 401 from the
// PDP means the server verified nothing about this session beyond the token:
// after a restart (or on another pod) its checkin-built subject cache is
// gone, and the server refuses to decide role-less rather than degrade
// (TestDecideSubjectCacheMissBounces). The
// bounce is the documented any-401 recovery (the TestMultiPodHA contract): a
// refresh check-in first, carrying the still-valid session token so the
// session id, and any pending approval exemptions keyed on it, survive; the
// identity lane only when the token itself is refused. Any error comes back
// to the caller for honest wording (onlineDenyReason).
func (p *LocalPDP) decideOnline(ev policy.Event) (DecideResponse, error) {
	// Every decide and bounce call below carries p.escCall so Client.do can
	// record the status and X-Request-Id the server answered with (the last
	// response wins: after a bounce, the retried decide).
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	resp, err := p.client.Decide(trace.WithCall(ctx, &p.escCall), p.session.SessionToken, ev, p.agentType, p.agentID)
	cancel()
	var refuse *StatusError
	if err == nil || !errors.As(err, &refuse) || refuse.Status != http.StatusUnauthorized {
		return resp, err
	}
	p.escBounced = true
	bctx, bcancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer bcancel()
	bctx = trace.WithCall(bctx, &p.escCall)
	name := harnessName(p.session.Harness)
	r, rerr := p.client.Refresh(bctx, p.session.SessionToken, name, harnessVersion(p.session.Harness), measureAttestation(name))
	switch {
	case rerr == nil:
		adoptRefresh(bctx, p.client, p.store, p.cfg, &p.session, r)
	case !errors.As(rerr, &refuse) || refuse.Status != http.StatusUnauthorized:
		return DecideResponse{}, rerr // transport or terminal refusal: nothing restored
	default: // the token itself was refused: the identity lane is the last door
		if rerr := reacquire(bctx, p.client, p.store, p.cfg, &p.session); rerr != nil {
			var rrefuse *StatusError
			if errors.As(rerr, &rrefuse) {
				return DecideResponse{}, rerr // the server's own words are the actionable ones
			}
			// A local failure (no enrolled identity, transport mid-bounce)
			// must not eclipse what actually stopped the decision: the
			// server's refusal. The original 401 keeps the wording honest.
			return DecideResponse{}, err
		}
	}
	rctx, rcancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer rcancel()
	return p.client.Decide(trace.WithCall(rctx, &p.escCall), p.session.SessionToken, ev, p.agentType, p.agentID)
}

// onlineDenyReason words a dead online gate honestly: claim
// "unreachable" only when the transport failed; when the server ANSWERED and
// refused, before or after the decideOnline bounce, its refusal is the story.
// The unreachable wording stays byte-identical to the long-pinned strings.
func onlineDenyReason(err error, action, tail string) string {
	var refuse *StatusError
	if errors.As(err, &refuse) {
		return fmt.Sprintf(
			"Straza: the server refused this session for %s (%s) and re-checkin could not restore it. Denied (fail-closed); restart the session or run `straza doctor`",
			action, strings.TrimPrefix(refuse.Msg, "Straza: "))
	}
	return fmt.Sprintf("Straza: security layer unreachable for %s. %s", action, tail)
}

// serverCheck routes a serverCheck-flagged allow to POST /v1/decide (with the
// decideOnline 401 bounce). Any unrecovered failure is a deny: the rule
// author opted into an online gate (spec/policyset SPEC.md §2.6).
func (p *LocalPDP) serverCheck(ev policy.Event, local policy.Decision) policy.Decision {
	resp, err := p.decideOnline(ev)
	if err != nil {
		p.escFailClosed = true
		return policy.Decision{
			Effect: policy.EffectDeny, RuleID: local.RuleID, SetName: local.SetName,
			Reason: onlineDenyReason(err, "a server-checked action", "Denied (run `straza doctor`)"),
		}
	}
	p.serverRecorded = true
	return policy.Decision{
		Effect: resp.Effect, RuleID: resp.RuleID, Reason: resp.Reason, Obligations: resp.Obligations,
	}
}

// approveCheck resolves a mode:approve allow (human-in-the-loop) through the
// online PDP (with the decideOnline 401 bounce). The hook lane never blocks
// (harnesses kill hooks on their own clock): the server creates or looks up
// the pending approval record, consumes any single-use exemption from a prior
// approval, and answers immediately: deny-with-reference ("retry after
// approval") or an exemption-backed allow. Its verdict passes through
// verbatim so the model reads the reason and retries. Unrecovered failures
// fail closed exactly like serverCheck. The
// server owns everything else (record, exemption, approver resolution), so
// hook.go needs no change: encodeDeny relays the reason as-is.
func (p *LocalPDP) approveCheck(ev policy.Event, local policy.Decision) policy.Decision {
	resp, err := p.decideOnline(ev)
	if err != nil {
		p.escFailClosed = true
		return policy.Decision{
			Effect: policy.EffectDeny, RuleID: local.RuleID, SetName: local.SetName,
			Reason: onlineDenyReason(err, "an approval-gated action", "Denied (fail-closed)"),
		}
	}
	p.serverRecorded = true
	return policy.Decision{
		Effect: resp.Effect, RuleID: resp.RuleID, Reason: resp.Reason, Obligations: resp.Obligations,
	}
}

// classifyCheck resolves a classify-flagged allow (mode: classify)
// through the embedded classifier. Fail closed, same posture as serverCheck:
// a missing backend, an error, or a blown deadline is a deny with the firing
// rule preserved; a not-allowed verdict denies with the classifier's reason.
func (p *LocalPDP) classifyCheck(ev policy.Event, local policy.Decision) policy.Decision {
	unavailable := policy.Decision{
		Effect: policy.EffectDeny, RuleID: local.RuleID, SetName: local.SetName,
		Reason: "Straza: classifier unavailable for a classify-gated action. Denied (run `straza doctor`)",
	}
	if p.classifier == nil {
		p.escFailClosed = true
		return unavailable
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	v, err := p.classifier.Classify(ctx, ev)
	if err != nil {
		p.escFailClosed = true
		return unavailable
	}
	if !v.Allowed {
		return policy.Decision{
			Effect: policy.EffectDeny, RuleID: local.RuleID, SetName: local.SetName,
			Reason: "Straza: classifier: " + v.Reason,
		}
	}
	return local
}

// keyLookup builds a policy.KeyLookup from base64-encoded pinned keys.
func keyLookup(keys map[string]string) (policy.KeyLookup, error) {
	decoded := map[string]ed25519.PublicKey{}
	for kid, b64 := range keys {
		raw, err := base64.StdEncoding.DecodeString(b64)
		if err != nil {
			return nil, fmt.Errorf("bad pinned key %s: %w", kid, err)
		}
		decoded[kid] = ed25519.PublicKey(raw)
	}
	return func(kid string) (ed25519.PublicKey, bool) {
		k, ok := decoded[kid]
		return k, ok
	}, nil
}
