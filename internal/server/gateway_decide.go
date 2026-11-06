package server

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/strazahq/straza/internal/authn"
	"github.com/strazahq/straza/internal/objects"
	"github.com/strazahq/straza/internal/policy"
)

// grantEvent is the event of a git grant's straza.audit.mcp record.
const grantEvent = "grant"

// gatewayCall is one call of a tool that the shared gateway decision
// judges: the verified session and its cached subject, the server endpoint
// it came through (nil on /mcp), the tool's name as that endpoint takes it,
// and the arguments. grant is set on the git grant route only, and describe
// only for the MCP call an approval_request describes.
type gatewayCall struct {
	claims   authn.Claims
	sub      policy.Subject
	srv      *serverScope
	name     string
	args     json.RawMessage
	grant    *grantMode
	describe *describeMode
}

// grantMode is what the git grant route asks of the shared decision. app
// and tool name the mapped tool, which the route reaches as /mcp would.
// advertise marks a push without refs, which skips the approval, and wait
// caps the approval hold. The mode also skips the input schema check,
// words its approval sentences for git, never naming the await tool, and
// writes its records with the event grant.
type grantMode struct {
	app, tool, op string
	advertise     bool
	wait          time.Duration
}

// describeMode is what approval_request asks of the shared decision for
// the MCP call it describes. app and tool name the call, which the route
// reaches as /mcp would. The decision then writes no record, because
// approval_request writes the one record of its own call. It spends no
// draft submit budget, skips the input schema check when the call carries
// no arguments, and ends after the schema check, so it spends no approval
// and holds nothing. Every refusal answers its sentence, a tool the session
// cannot call answers nativeUnknownToolMsg, and an engine deny is answered
// as the decision with no refusal, for approval_request to word.
type describeMode struct {
	app, tool string
}

// gatewayVerdict is the shared decision's answer. A refused call has
// refusal set, the sentence its endpoint answers with, and unknown when
// /mcp answers it as an unknown tool. On the grant route a hold that is
// still open sets pending and wrote no record. Every other refusal
// wrote its one record, except in describe mode, and slot is the decision
// slot of the refusal. A refusal of the object gate also carries the
// gate's verdict. A call that goes on carries what the caller needs: the
// target, the object verdict, the decision, the arguments after the
// justification strip, the approval it used, if any, and rec, the record
// the caller writes once the call's own steps are done.
type gatewayVerdict struct {
	target   gwTarget
	gate     objects.Verdict
	decision policy.Decision
	args     json.RawMessage
	approval approveOutcome
	rec      mcpAuditRecord

	refusal string
	unknown bool
	pending bool
	slot    decisionSlot
}

// decideCall runs the gateway decision of c, the same on /mcp, on a server
// endpoint, on the git grant route and for a call approval_request
// describes: visibility, the event, the session's overlay, the rate limit,
// the object gate, the engine with classify, the deny record and the
// approval. It writes the record of each refusal itself, except in describe
// mode, and leaves the record of a call that goes on to the caller. It
// reads no database, except through the approval of a gated call.
func (a *App) decideCall(ctx context.Context, c gatewayCall) gatewayVerdict {
	start := time.Now()
	tier1, entry, target, visible, probe := a.resolveTarget(c)
	// The hidden refusal below quotes the same cut name, so a long name
	// cannot tell a hidden tool from one that does not exist.
	unknown := fmt.Sprintf("unknown tool %q", cutUnresolved(c.name))
	if !visible {
		// Invisible = nonexistent: no role binding, no exposure, or no such
		// tool. Indistinguishable by design (default-deny). The record still
		// names the probe, so a sweep of ungranted names reaches the SIEM.
		a.auditCallRefusal(ctx, c, a.refusedRecord(c.claims, probe, gwTarget{},
			unknown+": no access row for this session's roles admits it"))
		if c.grant != nil {
			return gatewayVerdict{refusal: fmt.Sprintf(grantInvisibleMsg, c.grant.op, c.grant.tool, c.grant.app)}
		}
		if c.describe != nil {
			return gatewayVerdict{refusal: fmt.Sprintf(nativeUnknownToolMsg, cutUnresolved(c.describe.app), cutUnresolved(c.describe.tool))}
		}
		return gatewayVerdict{refusal: unknown, unknown: true}
	}
	// The tier-one hit above is the binding check, or for a drafting tool
	// the role check, so the call carries the granted fact grantedOf answers
	// (spec/policyset revisions 17, 20 and 27).
	ev := policy.Event{
		Kind: policy.EventToolPre, Tool: policy.ToolMCPCall, App: target.app, ToolName: target.tool,
		Granted: grantedOf(target),
	}
	// Tier-2 overlay hidden set: a tool the session's policy denies is
	// nonexistent too (policyFilter on), same -32602 as an unbound tool, no
	// information leak. Downstream (live Evaluate, approve gate, audit) is
	// unchanged; with policyFilter off the hidden set is empty and the deny
	// surfaces below as the Straza reason instead. The grant route already
	// named the server, so it answers the denying rule's reason.
	if a.overlayFor(c.claims.Session, c.sub, tier1).hidden[entry] {
		rec := a.hiddenRecord(c.claims, c.sub, ev, target)
		a.auditCallRefusal(ctx, c, rec)
		if c.grant != nil {
			return gatewayVerdict{refusal: policyRefusal(rec.decision.Reason)}
		}
		if c.describe != nil {
			return gatewayVerdict{refusal: fmt.Sprintf(nativeUnknownToolMsg, cutUnresolved(c.describe.app), cutUnresolved(c.describe.tool))}
		}
		return gatewayVerdict{refusal: unknown, unknown: true}
	}

	// Per-(session, app) rate limit from the manifest, and the drafting
	// tools' own rates (draftingRate). Checked before the PDP so a hot loop
	// cannot amplify policy work, and an approval is never spent on a call
	// the rate refuses; the throttle counter keeps the rejection volume
	// visible to operators and each refusal leaves its own record. A
	// described call spends from the same bucket, but a described
	// draft_submit spends no submit budget, because it submits nothing.
	key, rps := c.claims.Session+"|"+target.app, a.appRPS(target.app)
	if draftingTool(target) {
		key, rps = draftingRate(c.claims.Session)
	}
	msg := ""
	if !a.gateway.limiter.Allow(key, rps) {
		msg = fmt.Sprintf("Straza: rate limit exceeded for the MCP server %q (%.0f rps). Wait a few seconds, then try the call once more. If it is denied again, tell the user.", target.app, rps)
	} else if c.describe == nil && target.app == nativeAppName && target.tool == nativeToolDraftSubmit {
		msg = a.submitRateRefusal(c.claims.Subject)
	}
	if msg != "" {
		a.metrics.Throttle(target.app)
		return refusedVerdict(msg, a.auditCallRefusal(ctx, c, a.refusedRecord(c.claims, ev, target, msg)))
	}

	// The object gate of a server that pins an object type runs before the
	// engine, so no rule can allow a call it refuses and the refusal spends
	// no approval. Its grant, not tier one's first role by name, is the
	// role the record and the credential broker name.
	gate, pinned := a.objectGate(tier1, target, c.args)
	if gate.Missed {
		a.objectMissed(target)
	}
	if pinned && gate.Refusal != "" {
		msg := "Straza: " + gate.Refusal
		v := refusedVerdict(msg, a.auditCallRefusal(ctx, c, a.gateRefusedRecord(c.claims, ev, target, c.args, gate, msg)))
		v.gate = gate
		return v
	}
	if pinned {
		target.role, target.bindingID = gate.Grant.Role, gate.Grant.BindingID
	}

	cur := a.snapshots.Current()
	decision := cur.Engine.Evaluate(ev, c.sub)
	if decision.Effect == policy.EffectAllow && decision.Classify {
		// A classify-flagged allow is resolved here, as on the hook lane, so
		// the classifier runs before the record names the rule.
		decision = a.resolveClassify(ctx, "gateway", ev, decision)
	}
	a.metrics.Observe(decision.Effect, time.Since(start))

	// mode:approve gate. For an approval-gated allow, strip the injected
	// justification from the arguments (it rides the approval record, never the
	// audit nor the upstream call) BEFORE auditing, so the cleaned arguments
	// are what audit and upstream see.
	callArgs := c.args
	var justification string
	gated := decision.Effect == policy.EffectAllow && decision.Approve != nil
	if gated {
		callArgs, justification = stripJustification(callArgs)
	}
	rec := mcpAuditRecord{claims: c.claims, ev: ev, decision: decision, snapshotID: cur.ID, args: callArgs, target: target, object: gate.Object}
	if c.grant != nil {
		rec.event = grantEvent
	}

	if decision.Effect != policy.EffectAllow {
		if c.describe != nil {
			return gatewayVerdict{target: target, gate: gate, decision: decision}
		}
		// A policy deny is a tool-level error so the model sees the reason
		// and can adapt, mirroring the hook PEP's exit-2 semantics.
		return refusedVerdict(policyRefusal(decision.Reason), a.auditMCP(ctx, rec))
	}

	// Block on the human decision. approved (or a single-use exemption from a
	// prior approval) ⇒ the call goes on with the cleaned arguments; denied,
	// expired/timeout, or a service error ⇒ the call ends here (fail-closed).
	// A push without refs asks for no approval, because the push grant that
	// names its refs asks once.
	var used approveOutcome
	if gated && (c.grant == nil || !c.grant.advertise) {
		// Arguments that break the tool's own schema can never run, so the
		// caller hears it now and no person is asked to approve them. A
		// grant runs no tool, so its arguments are Straza's own, and a
		// described call without arguments asks for a ticket that binds
		// the tool alone, so it has none to check.
		if c.grant == nil && (c.describe == nil || c.args != nil) {
			if detail := schemaMismatch(tier1.inputSchema(entry), callArgs); detail != "" {
				msg := fmt.Sprintf(argumentsMismatchMsg, c.name, detail)
				rec.decision = refusedDecision(decision, msg)
				return refusedVerdict(msg, a.auditCallRefusal(ctx, c, rec))
			}
		}
		if c.describe != nil {
			return gatewayVerdict{target: target, gate: gate, decision: decision, args: callArgs}
		}
		// The fingerprint binds what the gateway OBSERVED: the post-strip
		// arguments (audit/upstream bytes). Absent arguments are the observed
		// no-argument call ("{}"), never "unobserved"; this lane always sees
		// the payload. Attached only on the gated path; the evaluator and the
		// ungated hot path never touch it.
		ev.Args = callArgs
		if len(ev.Args) == 0 {
			ev.Args = json.RawMessage("{}")
		}
		used = a.approveCall(ctx, c, ev, decision, justification, callArgs, gate)
		if !used.run {
			if c.grant != nil && used.pending && decision.Approve.Class != policy.ClassTicket {
				// The approval stream records the open hold, and the request
				// the person's decision answers writes the record. A ticket
				// can wait for a day, so it is refused as on /mcp, and the
				// same git command after the approval cashes it.
				return gatewayVerdict{refusal: used.reason, pending: true, approval: used}
			}
			rec.decision = refusedDecision(decision, used.reason)
			audited := a.auditMCP(ctx, rec)
			return gatewayVerdict{refusal: used.reason, approval: used, slot: used.slot(audited)}
		}
	}
	return gatewayVerdict{target: target, gate: gate, decision: decision, args: callArgs, approval: used, rec: rec}
}

// resolveTarget is the visibility step of c: the catalog it resolves in, the
// tool's catalog name, the target and whether the caller sees it, and the
// event the record of an unseen name carries, with that name cut by
// cutUnresolved. On /mcp an unseen name that holds "__" is recorded as the
// server before it and the tool after it, each cut on its own. A server
// endpoint takes the server's own tool name, so
// both name the server. The grant route and a described call reach the
// named tool as /mcp does, and the record names the server and the tool as
// every grant record does. The tool's target must name both, so a server or
// tool name that holds "__" cannot reach another server's tool.
func (a *App) resolveTarget(c gatewayCall) (*sessionCatalog, string, gwTarget, bool, policy.Event) {
	probe := policy.Event{Kind: policy.EventToolPre, Tool: policy.ToolMCPCall, ToolName: cutUnresolved(c.name)}
	app, tool, named := c.named()
	switch {
	case c.srv != nil:
		probe.App = c.srv.name
		t, ok := c.srv.target(c.name)
		return c.srv.tier1, c.srv.catalogName(c.name), t, ok, probe
	case named:
		probe.App, probe.ToolName = app, tool
		tier1 := a.catalogFor(c.sub.Roles)
		entry := app + "__" + tool
		t, ok := tier1.targets[entry]
		return tier1, entry, t, ok && t.app == app && t.tool == tool, probe
	}
	tier1 := a.catalogFor(c.sub.Roles)
	t, ok := tier1.targets[c.name]
	if app, tool, split := strings.Cut(c.name, "__"); !ok && split {
		// A catalog name is the server's name, which never holds "__", then
		// "__" and the tool's name, so the record of an unseen name names
		// the server and the tool apart, as the record of a call does.
		probe.App, probe.ToolName = cutUnresolved(app), cutUnresolved(tool)
	}
	return tier1, c.name, t, ok, probe
}

// named answers the server and the tool that the grant route or a
// described call names, and false for a call that names its tool by the
// name its endpoint takes.
func (c gatewayCall) named() (app, tool string, ok bool) {
	switch {
	case c.grant != nil:
		return c.grant.app, c.grant.tool, true
	case c.describe != nil:
		return c.describe.app, c.describe.tool, true
	}
	return "", "", false
}

// approveCall runs the approval of a gated call. A server endpoint does not
// list straza__approval_await, so its pending sentence never names it. The
// grant route's sentences speak of git commands, and its hold ends at its
// request's wait when that comes first.
func (a *App) approveCall(ctx context.Context, c gatewayCall, ev policy.Event, d policy.Decision, justification string, callArgs json.RawMessage, gate objects.Verdict) approveOutcome {
	if c.grant == nil {
		return a.gateApprove(ctx, a.approval, c.claims, c.sub, ev, d, justification, callArgs, approvalObject(gate), c.srv == nil)
	}
	hold := min(a.gatewayHold(time.Duration(d.Approve.TimeoutSeconds)*time.Second), c.grant.wait)
	return a.gateApproveWithin(ctx, a.approval, c.claims, c.sub, ev, d, justification, callArgs, approvalObject(gate), wordsGit, hold)
}

// auditCallRefusal writes the record of a refusal, with the event grant on
// the grant route, and reports whether it was queued. A described call
// writes none, because approval_request writes the one record of its own
// call.
func (a *App) auditCallRefusal(ctx context.Context, c gatewayCall, rec mcpAuditRecord) bool {
	if c.describe != nil {
		return false
	}
	if c.grant != nil {
		rec.event = grantEvent
	}
	return a.auditMCP(ctx, rec)
}

// refusedVerdict is the verdict of a refusal whose sentence is msg and
// whose record entered the queue when audited is set.
func refusedVerdict(msg string, audited bool) gatewayVerdict {
	return gatewayVerdict{refusal: msg, slot: deniedSlot(msg, audited, "")}
}

// policyRefusal is the sentence of a policy deny whose reason is reason,
// with a stock reason when the rule gave none. It carries the "Straza: "
// prefix the session banner's contract needs: a refusal that starts with it
// is policy, which a client relays and never retries. Rule authors and the
// engine's own reason already carry it, so it is added only when it is
// missing, or a reason-less deny would read "Straza: Straza: blocked ...".
func policyRefusal(reason string) string {
	if reason == "" {
		reason = "denied by policy (no rule allows this call)"
	}
	if strings.HasPrefix(strings.TrimLeft(reason, " "), "Straza:") {
		return reason
	}
	return "Straza: " + reason
}
