package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

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
	// wait rather than the upstream-timeout ceiling. Every refusal of the
	// tool itself comes before the call's one record (nativeCall), so a
	// refusal is the record's deny with the refusal's reason, an answered
	// call is its allow, and no ticket opens and no draft is stored
	// unrecorded.
	if target.app == nativeAppName {
		answer, no := a.nativeCall(r.Context(), target.tool, callArgs, claims, sub)
		if answer == nil {
			rec.decision = refusedDecision(decision, no.reason)
			a.auditMCP(r.Context(), rec)
			writeRPCResult(w, req.ID, nativeToolError(no.text))
			return
		}
		if !a.auditMCP(r.Context(), rec) {
			writeRPCResult(w, req.ID, nativeToolError(auditQueueFullMsg))
			return
		}
		writeRPCResult(w, req.ID, answer())
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
		a.rpcFail(w, r, req.ID, -32000, fmt.Sprintf(credentialRefusedMsg, target.app, sentenceOf(err)), err)
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
	timeout := a.upstreamTimeout(target.app)
	callCtx, cancel := context.WithTimeout(r.Context(), timeout)
	defer cancel()
	res, err := a.manager.Call(callCtx, target.app, target.tool, callArgs, secret, objectMeta(gate))
	if errors.Is(err, manager.ErrCredentialEcho) {
		a.withholdEcho(w, r, req, rec, srv)
		return
	}
	if err != nil {
		a.rpcFail(w, r, req.ID, -32000, callFailedMsg(callCtx, target.app, timeout, credentialFix(target.app, resolved.Source, sub), err), err)
		return
	}
	if srv != nil {
		res = withoutUpstreamSlot(res)
	}
	writeRPCResult(w, req.ID, res)
}

// The JSON-RPC error messages of a call on /mcp that did not reach a result.
// credentialRefusedMsg takes the server and the refusal of its credential,
// which says how to connect or who sets the credential, so no retry is
// offered. upstreamFailedMsg takes the server and the runtime's error,
// which names the cause, and ends with the bounded next step. mayHaveRunMsg
// takes the server and what it did not do, and offers no retry, since the
// call may have run. credentialRejectedMsg takes the server, the HTTP status
// and its text, and who renews the credential, and refusedCallMsg the
// refusal of a call the server answered with another 4xx status.
const (
	credentialRefusedMsg = "Straza: the call was not sent to the MCP server %s: %s." // #nosec G101 -- a refusal sentence, not a credential
	upstreamFailedMsg    = "Straza: the call to the MCP server %s failed: %s. Try the call once more in a minute. If it fails again, tell the user."
	mayHaveRunMsg        = "Straza: the MCP server %s %s, so the call may have taken effect, and Straza does not send it again. " +
		"Check whether it took effect before you try it once more."
	credentialRejectedMsg = "Straza: the MCP server %s refused the credential Straza sent with the call (HTTP %d %s), so the call did not run. " +
		"Tell the user, so that %s."
	refusedCallMsg = "Straza: %s. The same call gets the same answer, so tell the user."
)

// callFailedMsg is the answer on /mcp to a call of the MCP server app that
// failed with err under callCtx, whose time was timeout. A call that may
// have run, because the runtime says so with manager.ErrMayHaveRun or
// callCtx ended before an answer came, says so and to check whether it took
// effect, and never advises a retry. A call the server refused with a 4xx
// status says it did not run: a refused credential names who renews it,
// credFix, a 429 offers the bounded retry, and any other status says the
// same call gets the same answer. Any other failure names its cause and the
// bounded retry.
func callFailedMsg(callCtx context.Context, app string, timeout time.Duration, credFix string, err error) string {
	switch {
	case errors.Is(err, manager.ErrMayHaveRun):
		return "Straza: " + sentenceOf(err) + "."
	case errors.Is(callCtx.Err(), context.DeadlineExceeded):
		return fmt.Sprintf(mayHaveRunMsg, app, "did not answer the call within "+timeout.String())
	case callCtx.Err() != nil:
		return fmt.Sprintf(mayHaveRunMsg, app, "did not answer the call before the request ended")
	}
	if refused := (*manager.RefusedError)(nil); errors.As(err, &refused) {
		switch refused.Status {
		case http.StatusUnauthorized, http.StatusForbidden:
			return fmt.Sprintf(credentialRejectedMsg, app, refused.Status, http.StatusText(refused.Status), credFix)
		case http.StatusTooManyRequests:
			return fmt.Sprintf(upstreamFailedMsg, app, refused.Error())
		}
		return fmt.Sprintf(refusedCallMsg, refused.Error())
	}
	return fmt.Sprintf(upstreamFailedMsg, app, sentenceOf(err))
}

// credentialFix answers who renews the credential a call to the MCP server
// app ran on, by source, where the credential came from, as the end of a
// sentence. sub is the caller. A person connects again, an administrator
// sets the own token of an agent or a service account again, and the
// sponsor of sub names whose connection an agent's call ran on.
func credentialFix(app, source string, sub policy.Subject) string {
	switch source {
	case manager.SourceOwn:
		if sub.UserType == store.UserTypeAgent || sub.UserType == store.UserTypeService {
			return fmt.Sprintf("an administrator sets the token of %[2]s again with strazactl connect %[1]s --user %[2]s", app, sub.User)
		}
		return fmt.Sprintf("the user connects to %s again with straza connect %s", app, app)
	case manager.SourceSponsor:
		return fmt.Sprintf("the sponsor %s connects to %s again on their credentials page", sub.Sponsor, app)
	case manager.SourceClientCredentials:
		return "an administrator checks the client of this agent at the provider of " + app
	}
	return "an administrator sets a working credential with strazactl apps secret set " + app
}

// sentenceOf answers the text of err without a final period, for a message
// that ends the sentence itself.
func sentenceOf(err error) string { return strings.TrimSuffix(err.Error(), ".") }

// nativeRefusal is the refusal of a call of a built-in straza tool: text,
// the sentence the agent reads, and reason, the words the call's record
// keeps. The code that builds each refusal says what its reason leaves out
// of text.
type nativeRefusal struct{ text, reason string }

// plainRefusal is the refusal msg, which quotes nothing the agent sent, so
// the record keeps it whole.
func plainRefusal(msg string) nativeRefusal { return nativeRefusal{text: msg, reason: msg} }

// The reasons the record of approval_status, approval_await and
// draft_status keeps for a ref or a draft id that names nothing of the
// caller's.
const (
	noSuchApprovalRecord = "Straza: no such approval."
	noSuchDraftRecord    = "Straza: no such draft."
)

// quotingRefusal is the refusal msg of approval_status, approval_await or
// draft_status. The agent reads msg. The record keeps msg, except that the
// refusal of a ref or a draft id that names nothing of the caller's keeps
// its sentence without the value, which the record's arguments field holds
// as far as its cut allows. Every other refusal of these tools quotes
// nothing the agent sent.
func quotingRefusal(msg string) nativeRefusal {
	reason := msg
	switch {
	case strings.HasPrefix(msg, noSuchApprovalMsg):
		reason = noSuchApprovalRecord
	case strings.HasPrefix(msg, strings.TrimSuffix(draftNoSuchRefusal, "%s")):
		reason = noSuchDraftRecord
	}
	return nativeRefusal{text: msg, reason: reason}
}

// nativeCall runs the steps of a call of the built-in straza app's tool
// that come before the call's record, and answers the step that answers
// the call once the record is queued, or nil and the refusal of the
// call. approval_request decides the call it describes,
// approval_await reads its ticket and draft_submit judges its draft
// before the record, and each opens its ticket, waits or stores its draft
// only after it. approval_status, draft_status and whoami only read, so
// each runs whole before the record, and its answer waits for the record.
func (a *App) nativeCall(ctx context.Context, tool string, args json.RawMessage, claims authn.Claims, sub policy.Subject) (func() *mcp.CallToolResult, nativeRefusal) {
	switch tool {
	case nativeToolApprovalRequest:
		ticket, msg := a.describeApprovalRequest(ctx, args, claims, sub)
		if msg != "" {
			// The record keeps the sentence whole. Each name it quotes is
			// cut by cutUnresolved, but the schema check's refusal quotes
			// up to 512 bytes of the described call's arguments, which
			// this record does not withhold yet.
			return nil, nativeRefusal{text: msg, reason: msg}
		}
		return func() *mcp.CallToolResult { return a.openApprovalTicket(ctx, ticket) }, nativeRefusal{}
	case nativeToolApprovalAwait:
		wait, msg := a.awaitTicket(ctx, args, claims)
		return wait, quotingRefusal(msg)
	case nativeToolDraftSubmit:
		return a.judgeDraftSubmit(ctx, args, claims)
	case nativeToolWhoami:
		res, msg := a.whoami(claims, sub)
		if msg != "" {
			return nil, nativeRefusal{text: msg, reason: msg}
		}
		return func() *mcp.CallToolResult { return res }, nativeRefusal{}
	}
	res := a.callNativeStraza(ctx, tool, args, claims)
	if !res.IsError {
		return func() *mcp.CallToolResult { return res }, nativeRefusal{}
	}
	// nativeToolError writes the refusal as the result's one text.
	msg := ""
	if len(res.Content) > 0 {
		if t, ok := res.Content[0].(*mcp.TextContent); ok {
			msg = t.Text
		}
	}
	return nil, quotingRefusal(msg)
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
