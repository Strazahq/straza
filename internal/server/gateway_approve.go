package server

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/strazahq/straza/internal/approval"
	"github.com/strazahq/straza/internal/authn"
	"github.com/strazahq/straza/internal/policy"
)

// approvalGate is the slice of the approval service the gateway PEP consumes.
// *approval.Service satisfies it; a narrow interface keeps
// resolveApprove testable without standing up the whole service.
type approvalGate interface {
	ConsumeHold(ctx context.Context, userID, sessionID, ruleID, argvHash string) (approval.Record, bool, error)
	ConsumeHeld(ctx context.Context, rec approval.Record) (bool, error)
	ConsumeGrant(ctx context.Context, userID, argvHash, consumingSession string) (approval.Record, bool, error)
	DeniedTicketWithinWindow(ctx context.Context, userID, ruleID, argvHash string) (approval.Record, bool, error)
	Request(ctx context.Context, in approval.RequestInput) (approval.Record, error)
	Await(ctx context.Context, id string) (approval.Record, error)
}

// approveOutcome is the approval gate's answer for one gated gateway call.
// run is true when the call may proceed, and its zero value refuses.
// Otherwise reason is the refusal the model reads and the record keeps. ref
// names the approval the answer is about when there is one, and pending says
// that approval is still undecided, a hold that outlived its in-request wait
// or an open ticket, whose decision window ends at expiresAt.
type approveOutcome struct {
	run       bool
	reason    string
	ref       string
	pending   bool
	expiresAt time.Time
}

// outcomeOf is a (reason, run) answer about the approval ref as an
// approveOutcome.
func outcomeOf(reason string, run bool, ref string) approveOutcome {
	return approveOutcome{run: run, reason: reason, ref: ref}
}

// slot is the decision slot of a refused outcome: held while its approval
// is undecided, denied otherwise.
func (o approveOutcome) slot(audited bool) decisionSlot {
	if o.pending {
		return heldSlot(o.ref, o.expiresAt, audited)
	}
	return deniedSlot(o.reason, audited, o.ref)
}

// resolveApprove is gateApprove on the combined endpoint, answered as
// (reason, run).
func (a *App) resolveApprove(ctx context.Context, gate approvalGate, claims authn.Claims, sub policy.Subject, ev policy.Event, d policy.Decision, justification string, callArgs json.RawMessage) (string, bool) {
	o := a.gateApprove(ctx, gate, claims, sub, ev, d, justification, callArgs, true)
	return o.reason, o.run
}

// gateApprove drives the mode:approve gate for a gateway tools/call. The
// outcome runs the call when it may proceed to the
// upstream: the held call used the approval it waited for, or a retry of the
// same call used an approval inside its window, the client-retry-after-timeout
// case. One approval runs one call, on any replica. Otherwise it carries the
// refusal the model reads and the audit record keeps: the fail-closed service
// error, the denied-by reason, the expired/timeout reason, the used-already
// reason, the pending reason, or afterUse's reason for a client that left
// while the use was in flight. awaitListed says the endpoint lists the
// straza__approval_await tool, so a pending hold or ticket may name it. It blocks up to
// spec.TimeoutSeconds, bounded by the request context (a client disconnect
// collapses the wait to a timeout). Fail closed on every uncertain state.
func (a *App) gateApprove(ctx context.Context, gate approvalGate, claims authn.Claims, sub policy.Subject, ev policy.Event, d policy.Decision, justification string, callArgs json.RawMessage, awaitListed bool) approveOutcome {
	spec := d.Approve
	// Fingerprint: the v2 key (per-rule binding; canonicalized args).
	// Ambiguous args (duplicate keys, malformed JSON) have NO fingerprint: deny.
	key, err := approval.EventKeyV2(ev, spec.Binding)
	if err != nil {
		a.failClosed(ctx, "gateway", d.RuleID, err)
		return approveOutcome{reason: "Straza: " + fingerprintUnavailableReason(err)}
	}
	// Preview the concrete (post-strip) mcp arguments now: this lane is the one
	// place the real call payload exists server-side. A draft_submit previews
	// as its digest and size, because the handler scans for a secret only
	// after this preview is stored. Knob-off returns empties.
	prev := a.callPreview(ev.App, ev.ToolName, callArgs)
	// Ticket class NEVER blocks: cash a DB-durable grant (user+fingerprint, any
	// session) for an allow-once, else open/dedupe the one pending ticket and
	// return a deny-with-ticket tool error. The blocking Await lane below is
	// hold-only.
	if spec.Class == policy.ClassTicket {
		return a.resolveTicketGateway(ctx, gate, claims, sub, ev, d, key, justification, prev, awaitListed)
	}
	// A retry of a call that already answered pending uses its approval here.
	// A lookup that the store cannot answer denies before any request opens.
	if used, ok, err := gate.ConsumeHold(ctx, claims.Subject, claims.Session, d.RuleID, key); err != nil {
		a.failClosed(ctx, "gateway", d.RuleID, err)
		return approveOutcome{reason: approvalStoreDownReason}
	} else if ok {
		reason, run := a.afterUse(ctx, "gateway", d.RuleID, used.ID)
		return outcomeOf(reason, run, used.ID)
	}
	rec, err := gate.Request(ctx, approval.RequestInput{
		SessionID:     claims.Session,
		UserID:        claims.Subject,
		Username:      sub.User,
		RuleID:        d.RuleID,
		SetName:       d.SetName,
		ArgvHash:      key,
		Lane:          "gateway",
		Summary:       fmt.Sprintf("mcp.call %s:%s", ev.App, ev.ToolName),
		Justification: justification,
		ArgsPreview:   prev.preview,
		ArgsTruncated: prev.truncated,
		ArgsBytes:     prev.bytes,
		Spec:          *spec,
		Confirm:       d.Confirm,
	})
	if err != nil {
		a.failClosed(ctx, "gateway", d.RuleID, err)
		return approveOutcome{reason: a.approveRequestDenyReason(err)}
	}
	hold := a.gatewayHold(time.Duration(spec.TimeoutSeconds) * time.Second)
	awaitCtx, cancel := context.WithTimeout(ctx, hold)
	defer cancel()
	res, err := gate.Await(awaitCtx, rec.ID)
	if err != nil {
		// The bounded in-request hold ended without a verdict. Two distinct
		// truths: if the decision window itself is over, fail closed
		// as the final timeout; if only the hold cap fired, the approval is
		// still live and the model gets the pending path in words (a socket
		// held to the client's transport deadline reads as a network timeout,
		// which a model may paper over with fabricated output). An approval
		// after this gets a use window that
		// the retry uses at the top of this func, so answering early loses
		// nothing.
		if remaining := time.Until(rec.ExpiresAt); remaining > 0 {
			nameAwait := awaitListed && a.awaitToolVisible(sub)
			return approveOutcome{
				reason: a.cappedHoldPendingReason(rec, int(remaining.Round(time.Second).Seconds()), nameAwait),
				ref:    rec.ID, pending: true, expiresAt: rec.ExpiresAt,
			}
		}
		return outcomeOf(fmt.Sprintf("Straza: approval request expired after %ds with no decision (ref %s)", spec.TimeoutSeconds, rec.ID), false, rec.ID)
	}
	switch res.State {
	case approval.StateApproved:
		reason, run := a.useHeldApproval(ctx, gate, d.RuleID, res)
		return outcomeOf(reason, run, rec.ID)
	case approval.StateDenied:
		return outcomeOf(fmt.Sprintf("Straza: approval denied by %s (ref %s)", res.DecidedByName, rec.ID), false, rec.ID)
	default: // StateExpired (or any non-approved terminal state) ⇒ timeout deny
		return outcomeOf(fmt.Sprintf("Straza: approval request expired after %ds with no decision (ref %s)", spec.TimeoutSeconds, rec.ID), false, rec.ID)
	}
}

// useHeldApproval runs a held call on the approval it waited for. One
// approval runs one call, so when a retry or another held call attached to
// the same request used it first, this call is refused and asks again. So is
// a call whose approval a held call cannot use: a ticket's, or one an older
// build decided without a use window. A store error denies fail-closed with
// a sentence that names the database, and because the approval is most
// likely still unused it names the time a retry has left, if any.
func (a *App) useHeldApproval(ctx context.Context, gate approvalGate, ruleID string, rec approval.Record) (string, bool) {
	won, err := gate.ConsumeHeld(ctx, rec)
	if err != nil {
		a.failClosed(ctx, "gateway", ruleID, err)
	}
	switch {
	case won && err == nil:
		return a.afterUse(ctx, "gateway", ruleID, rec.ID)
	case rec.Class == policy.ClassTicket:
		return approvalTicketNotHeldReason(rec.ID), false
	case rec.GrantExpiresAt == nil:
		return approvalNoUseWindowReason(rec.ID), false
	case err != nil:
		return approvalUseUnrecordedReason(rec.ID, time.Until(*rec.GrantExpiresAt).Truncate(time.Second)), false
	}
	return approvalUsedReason(rec.ID), false
}

// afterUse answers a call on either lane whose approval use has just won:
// the call runs, unless its client left while the use was in flight. The use
// stands, because one approval runs one call, so a call whose client is gone
// is refused with clientLeftReason before anything runs or is recorded as
// allowed, and one Warn line names the approval. It reads only ctx.
func (a *App) afterUse(ctx context.Context, lane, ruleID, ref string) (string, bool) {
	if ctx.Err() == nil {
		return "", true
	}
	a.reqlog(ctx).Warn("approval used, but the client left before its call could run, so the call did not run and the approval is spent",
		"lane", lane, "rule", ruleID, "approval", ref)
	return clientLeftReason(ref), false
}

// clientLeftReason is the refusal a call's record keeps when its approval
// was used after the client that sent the call had gone.
func clientLeftReason(ref string) string {
	return fmt.Sprintf("Straza: approval %s was used for this call, but the AI agent's connection to Straza closed before the call could run, so the call did not run. The approval is spent, because one approval runs one call. Send the call again to ask for a new approval.", ref)
}

// approvalUseUnrecordedReason is the fail-closed deny when a held call's
// approval was granted but the server could not record its use. The approval
// is most likely still unused, so with a second or more of the retry window
// left the same call sent again runs, unless a write that committed while
// its answer was lost spent it. With less left, the call asks again.
func approvalUseUnrecordedReason(ref string, left time.Duration) string {
	next := "Send the call again to ask for a new approval."
	if left >= time.Second {
		next = fmt.Sprintf("The approval may still be usable: send the same call again within %s, and if that call asks for a new approval, the person must approve again.", humanDuration(left))
	}
	return fmt.Sprintf("Straza: approval %s was granted, but the Straza server could not reach its database to record its use, so the call did not run. Denied (fail-closed). %s If this keeps happening, ask your Straza administrator to check the server's database connection.", ref, next)
}

// approvalUsedReason is the deny when a held call's approval already ran
// another call, a retry or a second held call of the same request.
func approvalUsedReason(ref string) string {
	return fmt.Sprintf("Straza: approval %s allows one run of this call, and that run already happened or its time ran out, so this call did not run. Send the call again to ask for a new approval.", ref)
}

// approvalNoUseWindowReason is the deny when a held call's approval was
// decided by a server of an older build, which stamps no use window, so no
// server of this build can use it.
func approvalNoUseWindowReason(ref string) string {
	return fmt.Sprintf("Straza: approval %s was granted on a Straza server that runs an older version, which does not record how long an approval may be used, so this server cannot use it and this call did not run. Send the call again to ask for a new approval. If this keeps happening, ask your Straza administrator to finish upgrading every Straza server to the same version.", ref)
}

// approvalTicketNotHeldReason is the deny when a held call attached to a
// pending ticket of the same session and call, which happens when the rule
// changed from a ticket to a hold while the ticket was open. The ticket's
// grant stays for a later call under a ticket rule.
func approvalTicketNotHeldReason(ref string) string {
	return fmt.Sprintf("Straza: approval %s approved a ticket for this call, and a held call cannot use a ticket's approval, so this call did not run. This happens when the rule changed from a ticket to a hold while the ticket was open. Send the call again to ask for a new approval.", ref)
}

// cappedHoldPendingReason is the structured answer when the bounded in-request
// hold ends while the decision window is still live: it names the
// ref, the seconds left, where a human decides (surfacesHintFor), and the
// await instruction only when nameAwait says the caller can see the native
// tool, which holds when the endpoint lists it and this session's policy
// exposes it (awaitToolVisible). The same honesty rule as the hook lane's
// pending reasons: naming a hidden tool sends the model at a dead affordance.
func (a *App) cappedHoldPendingReason(rec approval.Record, secondsLeft int, nameAwait bool) string {
	if nameAwait {
		return fmt.Sprintf(
			"Straza: approval pending (ref %s), %ds left in the decision window; a person decides on %s. Wait for the decision with the straza__approval_await tool, then retry the call. An approval within the window lets the retry proceed.",
			rec.ID, secondsLeft, a.surfacesHintFor(rec))
	}
	return fmt.Sprintf(
		"Straza: approval pending (ref %s), %ds left in the decision window; a person decides on %s. Retry the call after approval; an approval within the window lets the retry proceed.",
		rec.ID, secondsLeft, a.surfacesHintFor(rec))
}

// resolveTicketGateway is the gateway-lane ticket resolver (class: ticket). It
// never blocks (no Await): a live grant bound to the requester + fingerprint is
// consumed for an allow-once (the outcome runs the call); else, if a human
// already denied the latest ticket for this exact call and its decision window
// is still open, it denies FINAL without opening a new row, otherwise it
// opens/dedupes the one pending ticket and answers the deny-with-ticket reason
// as a pending outcome, naming straza__approval_await only when awaitListed
// says the endpoint lists it. A store error fails closed. An expired or
// past-window prior ticket just re-opens a fresh one on retry.
func (a *App) resolveTicketGateway(ctx context.Context, gate approvalGate, claims authn.Claims, sub policy.Subject, ev policy.Event, d policy.Decision, key string, justification string, prev argsPreview, awaitListed bool) approveOutcome {
	// A service error on the lookup denies: uncertainty never opens a ticket.
	if grant, ok, err := gate.ConsumeGrant(ctx, claims.Subject, key, claims.Session); err != nil {
		a.failClosed(ctx, "gateway", d.RuleID, err)
		return approveOutcome{reason: approvalStoreDownReason}
	} else if ok {
		reason, run := a.afterUse(ctx, "gateway", d.RuleID, grant.ID)
		return outcomeOf(reason, run, grant.ID)
	}
	// Terminal deny-final: the latest ticket for this call was denied and its
	// window is still open. Stay blocked rather than re-ask the approvers.
	if rec, ok, err := gate.DeniedTicketWithinWindow(ctx, claims.Subject, d.RuleID, key); err != nil {
		a.failClosed(ctx, "gateway", d.RuleID, err)
		return approveOutcome{reason: "Straza: approval service error. Denied (fail-closed)"}
	} else if ok {
		return outcomeOf(ticketDeniedFinalReason(rec), false, rec.ID)
	}
	rec, err := gate.Request(ctx, approval.RequestInput{
		SessionID:     claims.Session,
		UserID:        claims.Subject,
		Username:      sub.User,
		RuleID:        d.RuleID,
		SetName:       d.SetName,
		ArgvHash:      key,
		Lane:          "gateway",
		Summary:       fmt.Sprintf("mcp.call %s:%s", ev.App, ev.ToolName),
		Justification: justification,
		ArgsPreview:   prev.preview,
		ArgsTruncated: prev.truncated,
		ArgsBytes:     prev.bytes,
		Spec:          *d.Approve,
		Confirm:       d.Confirm,
	})
	if err != nil {
		a.failClosed(ctx, "gateway", d.RuleID, err)
		return approveOutcome{reason: a.approveRequestDenyReason(err)}
	}
	return approveOutcome{
		reason: a.ticketPendingReason(rec, d.Approve.TicketTTLSeconds, awaitListed && a.awaitToolVisible(sub)),
		ref:    rec.ID, pending: true, expiresAt: rec.ExpiresAt,
	}
}

// fingerprintUnavailableReason words the fail-closed deny for a call whose
// arguments cannot be canonicalized (duplicate keys, malformed JSON, depth):
// no fingerprint means nothing a human decision could safely bind to.
func fingerprintUnavailableReason(err error) string {
	return fmt.Sprintf("approval fingerprint unavailable (%v). Denied (fail-closed); re-send the call with well-formed arguments", err)
}

// gatewayHoldCap bounds how long a mode:approve hold may keep the tools/call
// request itself open. The rule's decision window keeps governing the
// approval; this only caps the SOCKET wait. 120s is the default: long
// enough that a human actually reaches the phone, and equal to the window
// most hold rules ship with. Deployments whose clients time out sooner (the
// python kit fails closed at 30s, some MCP clients give up at 30-60s)
// shorten it via approval.gatewayHoldSeconds: a socket held past the
// client's own deadline reads as a network timeout, which models paper
// over. Past the cap the caller gets the structured pending reason and the
// straza__approval_await path. Var, not const: tests shrink it.
var gatewayHoldCap = 120 * time.Second

// gatewayHold is the effective in-request hold for a rule's decision window:
// the window itself, capped by approval.gatewayHoldSeconds when set (operator
// knob) or the built-in default. The window is always the ceiling:
// config can shorten the socket wait relative to it, never outlive it.
func (a *App) gatewayHold(window time.Duration) time.Duration {
	cap := gatewayHoldCap
	if s := a.cfg.Approval.GatewayHoldSeconds; s > 0 {
		cap = time.Duration(s) * time.Second
	}
	if window < cap {
		return window
	}
	return cap
}
