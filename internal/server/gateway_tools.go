package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/strazahq/straza/internal/approval"
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

// callTool is the PEP hot path: the shared decision (decideCall) → in-memory
// credential resolution → upstream call → async audit. On a server
// endpoint the name is the server's own tool name and resolves only to that
// server's tools. From there the path is the same, and a refusal of a server
// whose views are on also carries the decision slot (toolRefusal).
func (a *App) callTool(w http.ResponseWriter, r *http.Request, req rpcRequest, claims authn.Claims, sub policy.Subject, srv *serverScope) {
	var params struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	}
	if err := json.Unmarshal(req.Params, &params); err != nil || params.Name == "" {
		writeRPCError(w, req.ID, -32602, "params.name is required")
		return
	}
	v := a.decideCall(r.Context(), gatewayCall{claims: claims, sub: sub, srv: srv, name: params.Name, args: params.Arguments})
	switch {
	case v.unknown:
		writeRPCError(w, req.ID, -32602, v.refusal)
		return
	case v.refusal != "":
		writeRPCResult(w, req.ID, toolRefusal(v.refusal, srv, v.slot))
		return
	}
	target, gate, decision, callArgs, rec := v.target, v.gate, v.decision, v.args, v.rec

	// Built-in `straza` app: the gateway originates these tools instead of
	// proxying upstream. Governed identically above (visibility + PDP + approve
	// gate); dispatched on r.Context() so approval_await can hold its own bounded
	// wait rather than the upstream-timeout ceiling. A drafting tool checks the
	// role on the cached subject once more before its record, so a wrong
	// tier one refuses instead of allowing, and the record says so. An
	// approval_request decides the call it describes and who could approve it
	// before its record too, so a refusal is the record's decision and no ticket opens unrecorded.
	if target.app == nativeAppName {
		_, msg := a.draftingSubject(claims.Session, target.tool)
		var ticket approval.RequestInput
		if msg == "" && target.tool == nativeToolApprovalRequest {
			ticket, msg = a.describeApprovalRequest(r.Context(), callArgs, claims, sub)
		}
		if msg != "" {
			rec.decision = refusedDecision(decision, msg)
			a.auditMCP(r.Context(), rec)
			writeRPCResult(w, req.ID, nativeToolError(msg))
			return
		}
		if !a.auditMCP(r.Context(), rec) {
			writeRPCResult(w, req.ID, nativeToolError(auditQueueFullMsg))
			return
		}
		if target.tool == nativeToolApprovalRequest {
			writeRPCResult(w, req.ID, a.openApprovalTicket(r.Context(), ticket))
			return
		}
		writeRPCResult(w, req.ID, a.callNativeStraza(r.Context(), target.tool, callArgs, claims))
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
	if err == nil && resolved.Source == manager.SourceSponsor && a.denylist.userBlocked(resolved.Owner) {
		// A lock leaves the sponsor's status active, and the cached subject
		// names the sponsor until the agent checks in again, so a lock or a
		// disable must bite here, on every call.
		err = blockedSponsorRefusal(sub, target.app)
	}
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
	res, err := a.manager.Call(callCtx, target.app, target.tool, callArgs, secret, objectMeta(gate))
	if err != nil {
		a.rpcFail(w, r, req.ID, -32000, fmt.Sprintf("upstream call failed: %v", err), err)
		return
	}
	if srv != nil {
		res = withoutUpstreamSlot(res)
	}
	writeRPCResult(w, req.ID, res)
}

// blockedSponsorRefusal is the deny of an agent's call to server that would
// run on its sponsor's own row while the denylist blocks that sponsor, as a
// lock, a disable and a deactivation do.
func blockedSponsorRefusal(sub policy.Subject, server string) error {
	return fmt.Errorf("agent %s runs on the %s connection of its sponsor %s, and %s is locked or disabled in Straza, so the call is refused. "+
		"An administrator gives %s a credential of its own or a sponsor who is active, or unlocks or enables %s",
		sub.User, server, sub.Sponsor, sub.Sponsor, sub.User, sub.Sponsor)
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
