package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/strazahq/straza/internal/authn"
	"github.com/strazahq/straza/internal/manager"
	"github.com/strazahq/straza/internal/policy"
	"github.com/strazahq/straza/internal/store"
)

// serveToolsList serves the per-session catalog, optionally paginated. With
// pageSize <= 0 pagination is off: the whole filtered catalog ships in one
// response with no nextCursor (the historical shape). Otherwise the response
// carries one page of pageSize tools plus an opaque nextCursor whenever more
// remain; the client passes it back in params.cursor to resume. The cursor is
// self-validating against the current catalog, so the data plane holds no page
// state: a catalog that changed under the client fails the cursor with
// -32602 and it re-lists from the start. On a server endpoint the list is
// that server's tools under its own names, its cursor is bound to that
// endpoint, and the record names the server in app.
func (a *App) serveToolsList(w http.ResponseWriter, r *http.Request, req rpcRequest, claims authn.Claims, sub policy.Subject, srv *serverScope) {
	var (
		tier1   *sessionCatalog
		overlay *catalogOverlay
		tools   []gwTool
		server  string
	)
	if srv == nil {
		tier1 = a.catalogFor(sub.Roles)
		overlay = a.overlayFor(claims.Session, sub, tier1)
		tools = overlayTools(tier1, overlay)
	} else {
		tier1, overlay, tools, server = srv.tier1, srv.overlay, srv.tools, srv.name
	}

	pageSize := a.cfg.Apps.Catalog.PageSize
	if pageSize <= 0 {
		if err := a.auditToolsList(r.Context(), claims, server, len(tools)); err != nil {
			a.rpcFail(w, r, req.ID, -32603, auditQueueFullMsg, err)
			return
		}
		writeRPCResult(w, req.ID, map[string]any{"tools": tools})
		return
	}

	offset := 0
	if len(req.Params) > 0 {
		var p struct {
			Cursor string `json:"cursor"`
		}
		if err := json.Unmarshal(req.Params, &p); err == nil && p.Cursor != "" {
			off, ok := parseCatalogCursor(p.Cursor, tier1, overlay, server, len(tools))
			if !ok {
				writeRPCError(w, req.ID, -32602, invalidCursorMessage)
				return
			}
			offset = off
		}
	}

	end := offset + pageSize
	if end > len(tools) {
		end = len(tools)
	}
	result := map[string]any{"tools": tools[offset:end]}
	if end < len(tools) {
		result["nextCursor"] = catalogCursor(tier1, overlay, server, end)
	}
	if err := a.auditToolsList(r.Context(), claims, server, end-offset); err != nil {
		a.rpcFail(w, r, req.ID, -32603, auditQueueFullMsg, err)
		return
	}
	writeRPCResult(w, req.ID, result)
}

// handleToolCall answers a tools/call on the combined endpoint /mcp.
func (a *App) handleToolCall(w http.ResponseWriter, r *http.Request, req rpcRequest, claims authn.Claims, sub policy.Subject) {
	a.callTool(w, r, req, claims, sub, nil)
}

// callTool is the PEP hot path: visibility (default-deny) → PDP →
// in-memory credential resolution → upstream call → async audit. On a server
// endpoint the name is the server's own tool name and resolves only to that
// server's tools. From there the path is the same, and a refusal of a server
// whose views are on also carries the decision slot (toolRefusal).
func (a *App) callTool(w http.ResponseWriter, r *http.Request, req rpcRequest, claims authn.Claims, sub policy.Subject, srv *serverScope) {
	start := time.Now()
	var params struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	}
	if err := json.Unmarshal(req.Params, &params); err != nil || params.Name == "" {
		writeRPCError(w, req.ID, -32602, "params.name is required")
		return
	}

	// entry is the tool's name in the catalog, and probe is what the record
	// of an unknown name says. A server endpoint takes the server's own tool
	// name, so both name the server.
	var (
		tier1   *sessionCatalog
		target  gwTarget
		visible bool
	)
	entry := params.Name
	probe := policy.Event{Kind: policy.EventToolPre, Tool: policy.ToolMCPCall, ToolName: params.Name}
	if srv == nil {
		tier1 = a.catalogFor(sub.Roles)
		target, visible = tier1.targets[entry]
	} else {
		tier1, entry, probe.App = srv.tier1, srv.catalogName(params.Name), srv.name
		target, visible = srv.target(params.Name)
	}
	if !visible {
		// Invisible = nonexistent: no role binding, no exposure, or no such
		// tool. Indistinguishable by design (default-deny). The record still
		// names the probe, so a sweep of ungranted names reaches the SIEM.
		a.auditRefused(r.Context(), claims, probe, gwTarget{},
			fmt.Sprintf("unknown tool %q: no access row for this session's roles admits it", params.Name))
		writeRPCError(w, req.ID, -32602, fmt.Sprintf("unknown tool %q", params.Name))
		return
	}
	// The tier-one hit above is the binding check, or for a drafting tool
	// the role check, so the call carries the granted fact grantedOf answers
	// (spec/policyset revisions 17 and 20).
	ev := policy.Event{
		Kind: policy.EventToolPre, Tool: policy.ToolMCPCall, App: target.app, ToolName: target.tool,
		Granted: grantedOf(target),
	}
	// Tier-2 overlay hidden set: a tool the session's policy denies is
	// nonexistent too (policyFilter on), same -32602 as an unbound tool, no
	// information leak. Downstream (live Evaluate, approve gate, audit) is
	// unchanged; with policyFilter off the hidden set is empty and the deny
	// surfaces below as the Straza reason instead.
	if a.overlayFor(claims.Session, sub, tier1).hidden[entry] {
		a.auditHidden(r.Context(), claims, sub, ev, target)
		writeRPCError(w, req.ID, -32602, fmt.Sprintf("unknown tool %q", params.Name))
		return
	}

	// Per-(session, app) rate limit from the manifest, and the drafting
	// tools' own rates (draftingRate). Checked before the PDP so a hot loop
	// cannot amplify policy work, and an approval is never spent on a call
	// the rate refuses; the throttle counter keeps the rejection volume
	// visible to operators and each refusal leaves its own record.
	key, rps := claims.Session+"|"+target.app, a.appRPS(target.app)
	if draftingTool(target) {
		key, rps = draftingRate(claims.Session)
	}
	msg := ""
	if !a.gateway.limiter.Allow(key, rps) {
		msg = fmt.Sprintf("Straza: rate limit exceeded for the MCP server %q (%.0f rps). Retry shortly", target.app, rps)
	} else if target.app == nativeAppName && target.tool == nativeToolDraftSubmit {
		msg = a.submitRateRefusal(claims.Subject)
	}
	if msg != "" {
		a.metrics.Throttle(target.app)
		audited := a.auditRefused(r.Context(), claims, ev, target, msg)
		writeRPCResult(w, req.ID, toolRefusal(msg, srv, deniedSlot(msg, audited, "")))
		return
	}

	cur := a.snapshots.Current()
	decision := cur.Engine.Evaluate(ev, sub)
	if decision.Effect == policy.EffectAllow && decision.Classify {
		// A classify-flagged allow is resolved here, as on the hook lane, so
		// the classifier runs before the record names the rule.
		decision = a.resolveClassify(r.Context(), "gateway", ev, decision)
	}
	a.metrics.Observe(decision.Effect, time.Since(start))

	// mode:approve gate. For an approval-gated allow, strip the injected
	// justification from the arguments (it rides the approval record, never the
	// audit nor the upstream call) BEFORE auditing, so the cleaned arguments
	// are what audit and upstream see.
	callArgs := params.Arguments
	var justification string
	gated := decision.Effect == policy.EffectAllow && decision.Approve != nil
	if gated {
		callArgs, justification = stripJustification(callArgs)
	}
	rec := mcpAuditRecord{claims: claims, ev: ev, decision: decision, snapshotID: cur.ID, args: callArgs, target: target}

	if decision.Effect != policy.EffectAllow {
		audited := a.auditMCP(r.Context(), rec)
		reason := decision.Reason
		if reason == "" {
			reason = "denied by policy (no rule allows this call)"
		}
		// A policy deny is a tool-level error so the model sees the reason
		// and can adapt, mirroring the hook PEP's exit-2 semantics. The
		// "Straza: " prefix is the session banner's contract (a refusal that
		// starts with it is policy: relay, never retry); rule authors and the
		// engine's synthesized reason already carry it, so add it only when
		// it is missing, or a reason-less deny would read
		// "Straza: Straza: blocked ...".
		if !strings.HasPrefix(strings.TrimLeft(reason, " "), "Straza:") {
			reason = "Straza: " + reason
		}
		writeRPCResult(w, req.ID, toolRefusal(reason, srv, deniedSlot(reason, audited, "")))
		return
	}

	// Block on the human decision. approved (or a single-use exemption from a
	// prior approval) ⇒ fall through to the upstream call with the cleaned
	// arguments; denied, expired/timeout, or a service error ⇒ a tool-level
	// error ends the call here (fail-closed).
	if gated {
		// Arguments that break the tool's own schema can never run, so the
		// caller hears it now and no person is asked to approve them.
		if detail := schemaMismatch(tier1.inputSchema(entry), callArgs); detail != "" {
			msg := fmt.Sprintf(argumentsMismatchMsg, params.Name, detail)
			rec.decision = refusedDecision(decision, msg)
			audited := a.auditMCP(r.Context(), rec)
			writeRPCResult(w, req.ID, toolRefusal(msg, srv, deniedSlot(msg, audited, "")))
			return
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
		// A server endpoint does not list straza__approval_await, so its
		// pending sentence never names it.
		if o := a.gateApprove(r.Context(), a.approval, claims, sub, ev, decision, justification, callArgs, srv == nil); !o.run {
			rec.decision = refusedDecision(decision, o.reason)
			audited := a.auditMCP(r.Context(), rec)
			writeRPCResult(w, req.ID, toolRefusal(o.reason, srv, o.slot(audited)))
			return
		}
	}

	// Built-in `straza` app: the gateway originates these tools instead of
	// proxying upstream. Governed identically above (visibility + PDP + approve
	// gate); dispatched on r.Context() so approval_await can hold its own bounded
	// wait rather than the upstream-timeout ceiling. A drafting tool checks the
	// role on the cached subject once more before its record, so a wrong
	// tier one refuses instead of allowing, and the record says so.
	if target.app == nativeAppName {
		if _, msg := a.draftingSubject(claims.Session, target.tool); msg != "" {
			rec.decision = refusedDecision(decision, msg)
			a.auditMCP(r.Context(), rec)
			writeRPCResult(w, req.ID, nativeToolError(msg))
			return
		}
		if !a.auditMCP(r.Context(), rec) {
			writeRPCResult(w, req.ID, nativeToolError(auditQueueFullMsg))
			return
		}
		writeRPCResult(w, req.ID, a.callNativeStraza(r.Context(), target.tool, callArgs, claims, sub))
		return
	}

	// The credential is resolved before the record is spooled so the record
	// names the credential row the call uses and whose it is; resolution is
	// in-memory, but for an agent's client token that is not in memory yet,
	// which the provider is asked for within that lane's own limit. Every
	// field of the caller comes from the verified session and none from the
	// request.
	resolved, err := a.manager.Credential(r.Context(), target.app, manager.Caller{
		UserID: claims.Subject, User: sub.User, Roles: sub.Roles, GrantingRole: target.role,
		Agent: sub.UserType == store.UserTypeAgent, Sponsor: sub.Sponsor, SponsorID: sub.SponsorID,
		Session: claims.Session,
	})
	if err != nil {
		rec.decision = refusedDecision(decision, err.Error())
		audited := a.auditMCP(r.Context(), rec)
		if srv != nil {
			// On a server endpoint the refusal is a tool result, so a person
			// in a view reads the sentence that says how to connect.
			text := err.Error()
			if !strings.HasPrefix(text, "Straza:") {
				text = "Straza: " + text
			}
			writeRPCResult(w, req.ID, toolRefusal(text, srv, deniedSlot(text, audited, "")))
			return
		}
		a.rpcFail(w, r, req.ID, -32000, fmt.Sprintf("upstream call failed: %v", err), err)
		return
	}
	secret := resolved.Secret
	if secret != nil {
		rec.credentialID = secret.ID
		rec.credentialSource = resolved.Source
		rec.credentialOwner = resolved.Owner
	}
	if !a.auditMCP(r.Context(), rec) {
		// Under block the record could not be queued, so the upstream call
		// must not run.
		writeRPCResult(w, req.ID, toolRefusal(auditQueueFullMsg, srv, deniedSlot(auditQueueFullMsg, false, "")))
		return
	}

	// Bound the upstream call so a slow/hung MCP server cannot pin a gateway
	// goroutine indefinitely.
	callCtx, cancel := context.WithTimeout(r.Context(), a.upstreamTimeout(target.app))
	defer cancel()
	res, err := a.manager.Call(callCtx, target.app, target.tool, callArgs, secret)
	if err != nil {
		a.rpcFail(w, r, req.ID, -32000, fmt.Sprintf("upstream call failed: %v", err), err)
		return
	}
	if srv != nil {
		res = withoutUpstreamSlot(res)
	}
	writeRPCResult(w, req.ID, res)
}

// refusedDecision is the verdict the record keeps for a call policy allowed
// and a later gate refused, the approval hold or the credential resolution:
// a deny that keeps the allowing rule and set and carries the refusal as its
// reason, so the chain says the call never ran and why, as the hook lane's
// record does for the same hold.
func refusedDecision(d policy.Decision, reason string) policy.Decision {
	return policy.Decision{Effect: policy.EffectDeny, RuleID: d.RuleID, SetName: d.SetName, Default: d.Default, Reason: reason}
}

// appRPS returns the per-app rate limit from the manifest (0 = unlimited).
// Narrow accessor, not manager.View: a full AppView glob-matches the whole
// tool inventory per call, which is wasted work on the hot path.
func (a *App) appRPS(appName string) float64 {
	return a.manager.RPS(appName)
}

// upstreamTimeout is the per-call ceiling on an upstream MCP invocation:
// the app manifest's limits.timeoutSeconds wins (a long-running tool opts
// itself in without raising the fleet-wide ceiling), then the server-wide
// apps.upstreamTimeout, then 30 s.
func (a *App) upstreamTimeout(appName string) time.Duration {
	if t := a.manager.UpstreamTimeout(appName); t > 0 {
		return t
	}
	if a.cfg.Apps.UpstreamTimeout > 0 {
		return a.cfg.Apps.UpstreamTimeout
	}
	return 30 * time.Second
}
