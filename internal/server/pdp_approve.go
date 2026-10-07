package server

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/strazahq/straza/internal/approval"
	"github.com/strazahq/straza/internal/authn"
	"github.com/strazahq/straza/internal/policy"
)

// resolveApproveHook resolves a `mode: approve` allow on the HOOK lane
// (request-then-retry; the gateway lane's blocking resolveApprove lives in
// gateway_approve.go). It first uses the approval a prior human decision left
// for the SAME call (person, session, rule, event key) inside its use window,
// once, on any replica; on a hit the retry allows with the approved-by
// reason. Otherwise it creates (or dedupes onto) a pending record and answers
// deny-with-reference so the model retries after a human approves. Any
// approval-service error denies with the fail-closed reason. It never
// blocks.
func (a *App) resolveApproveHook(ctx context.Context, claims authn.Claims, sub policy.Subject, ev policy.Event, decision policy.Decision) (policy.Decision, string) {
	// Fingerprint: the v2 key (per-rule binding; canonicalized mcp args when
	// the client forwarded them, honest tool-scope degrade when it did not).
	// Ambiguous args have no fingerprint: deny fail-closed.
	key, err := approval.EventKeyV2(ev, decision.Approve.Binding)
	if err != nil {
		a.failClosed(ctx, "hook", decision.RuleID, err)
		return policy.Decision{
			Effect: policy.EffectDeny, RuleID: decision.RuleID, SetName: decision.SetName,
			Reason: "Straza: " + fingerprintUnavailableReason(err),
		}, ""
	}
	// Preview the concrete call now (the one moment the hook payload is in
	// hand): shell commands preview via the redacted command path, forwarded
	// mcp args via the same bounded preview the gateway lane uses, a
	// draft_submit as its digest; the knob-off case returns empties.
	prev := a.commandArgsPreview(ev.Command)
	if ev.Tool == policy.ToolMCPCall && ev.Args != nil {
		prev = a.callPreview(ev.App, ev.ToolName, ev.Args)
	}
	// Ticket class takes a different lane: never block, cash a grant that
	// follows the person rather than the session, or open/attach the one
	// pending ticket and deny-with-ticket.
	if decision.Approve.Class == policy.ClassTicket {
		return a.resolveTicketHook(ctx, claims, sub, ev, decision, key, prev)
	}

	if rec, ok, err := a.approval.ConsumeHold(ctx, claims.Subject, claims.Session, decision.RuleID, key); err != nil {
		a.failClosed(ctx, "hook", decision.RuleID, err)
		return storeDownApproveDeny(decision.RuleID, decision.SetName), ""
	} else if ok {
		if reason, run := a.afterUse(ctx, "hook", decision.RuleID, rec.ID); !run {
			return policy.Decision{Effect: policy.EffectDeny, RuleID: decision.RuleID, SetName: decision.SetName, Reason: reason}, rec.ID
		}
		verb := "approved"
		if rec.Mode == policy.ModeConfirm {
			verb = "confirmed"
		}
		return policy.Decision{
			Effect: policy.EffectAllow, RuleID: decision.RuleID, SetName: decision.SetName,
			Reason:      fmt.Sprintf("Straza: %s by %s (ref %s)", verb, rec.DecidedByName, rec.ID),
			Obligations: decision.Obligations,
		}, rec.ID
	}

	username := claims.Subject
	if sub.User != "" {
		username = sub.User
	}
	rec, err := a.approval.Request(ctx, approval.RequestInput{
		SessionID: claims.Session, UserID: claims.Subject, Username: username,
		RuleID: decision.RuleID, SetName: decision.SetName,
		ArgvHash: key, Lane: "hook",
		Summary:     approveSummary(ev),
		ArgsPreview: prev.preview, ArgsTruncated: prev.truncated, ArgsBytes: prev.bytes,
		Spec:    *decision.Approve,
		Confirm: decision.Confirm,
	})
	if err != nil {
		return a.approveRequestDeny(ctx, err, decision.RuleID, decision.SetName), ""
	}
	if rec.Mode == policy.ModeConfirm {
		// The record, not the rule, says who decides: a sponsor-routed rule
		// lands on a person with no sponsor as a confirm record.
		wait := fmt.Sprintf("retry after it is confirmed (ref %s)", rec.ID)
		if a.awaitToolVisible(sub) {
			wait = fmt.Sprintf("wait for it with the straza__approval_await tool, then retry (ref %s)", rec.ID)
		}
		return policy.Decision{
			Effect: policy.EffectDeny, RuleID: decision.RuleID, SetName: decision.SetName,
			Reason: fmt.Sprintf("Straza: this call needs the requester's confirmation, on %s; %s", a.confirmSurfacesHint(), wait),
		}, rec.ID
	}
	wait := fmt.Sprintf("Retry this exact call after approval (ref %s)", rec.ID)
	if a.awaitToolVisible(sub) {
		wait = fmt.Sprintf("Wait for the decision with the straza__approval_await tool, then retry (ref %s)", rec.ID)
	}
	return policy.Decision{
		Effect: policy.EffectDeny, RuleID: decision.RuleID, SetName: decision.SetName,
		Reason: fmt.Sprintf("Straza: approval requested (%s); a person decides on %s. %s", approvalNotifiedClause(rec), a.approvalSurfacesHint(), wait),
	}, rec.ID
}

// approvalNotifiedClause words WHO the fresh record announced to, matching
// its real persisted routing (revision 14): the user-scoped pool
// person-first, roles after, and the straza-admin fallback named only on the
// one lane that still reaches it (a selfApproval rule with no pool, where
// admins keep decide rights and the announce).
func approvalNotifiedClause(rec approval.Record) string {
	users := strings.Join(rec.ApproverUsers, ", ")
	roles := strings.Join(rec.ApproverRoles, ",")
	switch {
	case users != "" && roles != "":
		return fmt.Sprintf("notified %s and roles [%s]", users, roles)
	case users != "":
		return "notified " + users
	case roles != "":
		return fmt.Sprintf("notified roles [%s]", roles)
	default:
		return fmt.Sprintf("notified roles [%s]", approval.AdminFallbackRole)
	}
}

// resolveTicketHook is the hook-lane ticket resolver (class: ticket). It never
// blocks: it consumes a live grant bound to the requester + fingerprint (any
// session, plan-gate) for an allow-once; else, if a human already denied the
// latest ticket for this exact call and its decision window is still open, it
// denies FINAL without opening a new row, otherwise opens/dedupes the one
// pending ticket and returns a deny-with-ticket the model can act on. A store
// error denies fail-closed. An expired or past-window prior ticket is neither
// consumable nor deny-final, so a fresh call simply opens a new ticket.
func (a *App) resolveTicketHook(ctx context.Context, claims authn.Claims, sub policy.Subject, ev policy.Event, decision policy.Decision, key string, prev argsPreview) (policy.Decision, string) {
	// An error on the lookup denies: uncertainty never opens a ticket.
	if rec, ok, err := a.approval.ConsumeGrant(ctx, claims.Subject, key, claims.Session); err != nil {
		a.failClosed(ctx, "hook", decision.RuleID, err)
		return storeDownApproveDeny(decision.RuleID, decision.SetName), ""
	} else if ok {
		if reason, run := a.afterUse(ctx, "hook", decision.RuleID, rec.ID); !run {
			return policy.Decision{Effect: policy.EffectDeny, RuleID: decision.RuleID, SetName: decision.SetName, Reason: reason}, rec.ID
		}
		return policy.Decision{
			Effect: policy.EffectAllow, RuleID: decision.RuleID, SetName: decision.SetName,
			Reason:      ticketApprovedReason(rec),
			Obligations: decision.Obligations,
		}, rec.ID
	}

	// Terminal deny-final: the latest ticket for this call was denied and its
	// window is still open. Stay blocked rather than re-ask the approvers.
	if rec, ok, err := a.approval.DeniedTicketWithinWindow(ctx, claims.Subject, decision.RuleID, key); err != nil {
		a.failClosed(ctx, "hook", decision.RuleID, err)
		return failClosedApproveDeny(decision.RuleID, decision.SetName), ""
	} else if ok {
		return policy.Decision{
			Effect: policy.EffectDeny, RuleID: decision.RuleID, SetName: decision.SetName,
			Reason: ticketDeniedFinalReason(rec),
		}, rec.ID
	}

	username := claims.Subject
	if sub.User != "" {
		username = sub.User
	}
	rec, err := a.approval.Request(ctx, approval.RequestInput{
		SessionID: claims.Session, UserID: claims.Subject, Username: username,
		RuleID: decision.RuleID, SetName: decision.SetName,
		ArgvHash: key, Lane: "hook",
		Summary:     approveSummary(ev),
		ArgsPreview: prev.preview, ArgsTruncated: prev.truncated, ArgsBytes: prev.bytes,
		Spec:    *decision.Approve,
		Confirm: decision.Confirm,
	})
	if err != nil {
		return a.approveRequestDeny(ctx, err, decision.RuleID, decision.SetName), ""
	}
	return policy.Decision{
		Effect: policy.EffectDeny, RuleID: decision.RuleID, SetName: decision.SetName,
		Reason: a.ticketPendingReason(rec, decision.Approve.TicketTTLSeconds, a.awaitToolVisible(sub)),
	}, rec.ID
}

// failClosedApproveDeny is the hook lane's fail-closed deny for an
// approval-service error on the ticket's deny-final lookup, byte-identical to
// the gateway lane's.
func failClosedApproveDeny(ruleID, setName string) policy.Decision {
	return policy.Decision{
		Effect: policy.EffectDeny, RuleID: ruleID, SetName: setName,
		Reason: "Straza: approval service error. Denied (fail-closed)",
	}
}

// approvalStoreDownReason is the fail-closed deny on either lane, for a hold
// and a ticket alike, when the server cannot reach its database to look up or
// use an approval before any request opens.
const approvalStoreDownReason = "Straza: this call needs an approval, and the Straza server could not reach its database to check for one, so the call did not run. Denied (fail-closed). Retry the call in a moment, and if this keeps happening ask your Straza administrator to check the server's database connection."

// storeDownApproveDeny is approvalStoreDownReason in policy.Decision shape
// (the hook lane).
func storeDownApproveDeny(ruleID, setName string) policy.Decision {
	return policy.Decision{
		Effect: policy.EffectDeny, RuleID: ruleID, SetName: setName,
		Reason: approvalStoreDownReason,
	}
}

// approveRequestDenyReason maps a Request() error onto the PEP deny words,
// shared by all three surfaces (hook, gateway, native) so the lanes cannot
// drift: an UnroutableError (revision 14: no roles, no usable sponsor)
// carries its cause and fix to the calling agent and bumps the unroutable
// counter; anything else stays the generic fail-closed service error.
func (a *App) approveRequestDenyReason(err error) string {
	var un *approval.UnroutableError
	if errors.As(err, &un) {
		a.metrics.UnroutableInc()
		return "Straza: approval cannot be routed: " + un.Cause + ". Denied (fail-closed)"
	}
	return "Straza: approval service error. Denied (fail-closed)"
}

// approveRequestDeny is approveRequestDenyReason in policy.Decision shape
// (the hook lanes); it also writes the one fail-closed Error record for the
// service-error case (an UnroutableError is a decision: no record).
func (a *App) approveRequestDeny(ctx context.Context, err error, ruleID, setName string) policy.Decision {
	a.failClosed(ctx, "hook", ruleID, err)
	return policy.Decision{
		Effect: policy.EffectDeny, RuleID: ruleID, SetName: setName,
		Reason: a.approveRequestDenyReason(err),
	}
}

// awaitToolVisible mirrors the catalog overlay's visibility probe for the
// native await tool (buildOverlay: policy authorization is the native tools'
// only visibility gate; there is no binding row). Pending reasons name
// straza__approval_await only when this holds: for a session whose policy
// hides the straza app the instruction is a dead affordance. In-memory
// snapshot probe, request-path safe.
func (a *App) awaitToolVisible(sub policy.Subject) bool {
	if a.snapshots == nil {
		return false
	}
	eng := a.snapshots.Current().Engine
	if eng == nil {
		return false
	}
	d := eng.Evaluate(policy.Event{
		Kind: policy.EventToolPre, Tool: policy.ToolMCPCall,
		App: nativeAppName, ToolName: nativeToolApprovalAwait,
	}, sub)
	return d.Effect == policy.EffectAllow
}

// approvalSurfacesHint names where a human can decide, the self-service
// page first: it is the only self-serve surface (the console is admin-scoped
// and often VPN-bound; a phone may not be enrolled). The page rides the main
// listener, so publicUrl is its base; an unset publicUrl degrades to the bare
// path.
func (a *App) approvalSurfacesHint() string {
	return "the self-service page (" + a.selfServicePage() + "), the console, or an enrolled phone"
}

// confirmSurfacesHint names where a requester can confirm their own call.
// With approval.unsignedOwnDecisions off only a device that signs decides
// it, so the console is left out.
func (a *App) confirmSurfacesHint() string {
	if a.cfg.Approval.UnsignedOwnDecisions {
		return a.approvalSurfacesHint()
	}
	return "their enrolled phone, or the self-service page under This browser (" + a.selfServicePage() + ")"
}

// surfacesHintFor names where rec can be decided: a confirm record is its
// requester's own, so it takes confirmSurfacesHint, and every other record
// takes approvalSurfacesHint.
func (a *App) surfacesHintFor(rec approval.Record) string {
	if rec.Mode == policy.ModeConfirm {
		return a.confirmSurfacesHint()
	}
	return a.approvalSurfacesHint()
}

// selfServicePage is the self-service page's address as a person can open
// it: under publicUrl when that is set, else the bare path.
func (a *App) selfServicePage() string {
	if u := strings.TrimRight(a.cfg.Server.PublicURL, "/"); u != "" {
		return u + "/self-service/"
	}
	return "/self-service/"
}

// ticketPendingReason is the deny-with-ticket reason for a ticket-class rule
// with no live grant: it names the ref, the day-scale decision window, and
// where a human decides (surfacesHintFor), and tells the model how to
// wait with the native straza__approval_await tool only when nameAwait says
// the caller can see it: this session's policy exposes it
// (awaitToolVisible), and on the gateway the endpoint lists it.
// Harness-agnostic plain instruction, shared by both PEP lanes so they
// never drift.
func (a *App) ticketPendingReason(rec approval.Record, ticketTTLSeconds int, nameAwait bool) string {
	ref := rec.ID
	window := humanDuration(time.Duration(ticketTTLSeconds) * time.Second)
	if nameAwait {
		return fmt.Sprintf(
			"Straza: approval ticket %s is pending. A person decides within %s, on %s. Wait for the decision with the straza__approval_await tool (ref %s), or retry this exact call after approval; it fails if denied or expired.",
			ref, window, a.surfacesHintFor(rec), ref)
	}
	return fmt.Sprintf(
		"Straza: approval ticket %s is pending. A person decides within %s, on %s. Retry this exact call after approval; it fails if denied or expired.",
		ref, window, a.surfacesHintFor(rec))
}

// humanDuration renders a decision window in words for a reason a person
// reads: "90 seconds", "2 minutes", "1 hour 30 minutes", "24 hours". Windows
// under two minutes stay in seconds so a short window keeps its exact count.
// Fractions of a second are dropped, since windows are configured in seconds.
func humanDuration(d time.Duration) string {
	unit := func(n int, name string) string {
		if n == 1 {
			return "1 " + name
		}
		return fmt.Sprintf("%d %ss", n, name)
	}
	total := int(d / time.Second)
	if total < 120 {
		return unit(total, "second")
	}
	hours, rest := total/3600, total%3600
	minutes, seconds := rest/60, rest%60
	var parts []string
	if hours > 0 {
		parts = append(parts, unit(hours, "hour"))
	}
	if minutes > 0 {
		parts = append(parts, unit(minutes, "minute"))
	}
	if seconds > 0 {
		parts = append(parts, unit(seconds, "second"))
	}
	return strings.Join(parts, " ")
}

// ticketApprovedReason is the allow-once reason when a ticket grant is cashed.
func ticketApprovedReason(rec approval.Record) string {
	who := rec.DecidedByName
	if who == "" {
		who = "an approver"
	}
	return fmt.Sprintf("Straza: approved by %s. Ticket %s consumed (allow once)", who, rec.ID)
}

// ticketDeniedFinalReason is the terminal deny-final reason: a human denied the
// latest ticket for this exact call and its decision window is still open, so
// the call stays blocked (no fresh ticket) until that window closes. Shared by
// both PEP lanes so they never drift.
func ticketDeniedFinalReason(rec approval.Record) string {
	who := rec.DecidedByName
	if who == "" {
		who = "an approver"
	}
	return fmt.Sprintf(
		"Straza: ticket %s was denied by %s; this call stays denied until %s",
		rec.ID, who, rec.ExpiresAt.UTC().Format(time.RFC3339))
}

// approveSummary is the human-facing label for an approval card: it names the
// action WITHOUT leaking argument/transcript detail beyond the tool identity
// (privacy rule). The hook lane has no schema for a justification, so the
// summary carries the command context: bounded, redacted, and display-safe
// (the fuller redacted view rides args_preview).
func approveSummary(ev policy.Event) string {
	switch ev.Tool {
	case policy.ToolMCPCall:
		return "mcp.call " + ev.App + ":" + ev.ToolName
	case policy.ToolShellExec:
		return "shell.exec: " + clampShellSummary(ev.Command)
	default:
		s := ev.Tool
		if len(ev.Paths) > 0 {
			s += ": " + strings.Join(ev.Paths, ", ")
		}
		return s
	}
}
