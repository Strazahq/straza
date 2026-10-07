package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/strazahq/straza/internal/authn"
	"github.com/strazahq/straza/internal/manager"
	"github.com/strazahq/straza/internal/policy"
)

// viewsExtension is the MCP Apps extension a server endpoint advertises in
// its initialize when the server's views are on.
const viewsExtension = "io.modelcontextprotocol/ui"

// The straza.audit.mcp event values of a server endpoint's view answers
// (spec/events revision 42).
const (
	eventResourcesList = "resources.list"
	eventResourcesRead = "resources.read"
)

// linkedViews returns the views that a tool in tools links through
// _meta.ui.resourceUri or the legacy flat key _meta["ui/resourceUri"], in the
// order views holds them. tools are the caller's visible tools of one server,
// so a view is served exactly when the caller can see a tool that opens it.
func linkedViews(tools []gwTool, views []manager.View) []manager.View {
	linked := map[string]bool{}
	for _, t := range tools {
		if ui, ok := t.Meta["ui"].(map[string]any); ok {
			if uri, ok := ui["resourceUri"].(string); ok {
				linked[uri] = true
			}
		}
		if uri, ok := t.Meta["ui/resourceUri"].(string); ok {
			linked[uri] = true
		}
	}
	var out []manager.View
	for _, v := range views {
		if linked[v.URI] {
			out = append(out, v)
		}
	}
	return out
}

// viewUnavailableMsg is the refusal of a resources/read whose URI is not a
// view the caller may see on this server endpoint. It quotes the URI cut by
// cappedURI.
func viewUnavailableMsg(uri string) string {
	shown, _ := cappedURI(uri)
	return fmt.Sprintf("Straza: the view %q is not available to you on this server. A view is shown only to people who can use a tool that opens it.", shown)
}

// cappedURI cuts a requested URI to auditArgsMax bytes, the cap a record's
// arguments have, and reports whether it cut, so one request cannot write
// megabytes into the audit chain or into a refusal sentence.
func cappedURI(uri string) (string, bool) {
	if len(uri) <= auditArgsMax {
		return uri, false
	}
	return uri[:auditArgsMax], true
}

// uriRequiredMsg is the refusal of a resources/read that names no URI.
const uriRequiredMsg = "Straza: resources/read needs params.uri. Send the uri of a view that resources/list returned."

// serveResourcesList answers resources/list on a server endpoint with the
// views the caller's visible tools link. Its record is queued before the
// answer is served, as tools/list's is, so under audit backpressure block a
// full queue refuses the list.
func (a *App) serveResourcesList(w http.ResponseWriter, r *http.Request, req rpcRequest, claims authn.Claims, _ policy.Subject, srv *serverScope) {
	views := linkedViews(srv.tools, srv.cat.views)
	out := make([]*mcp.Resource, 0, len(views))
	for _, v := range views {
		out = append(out, &mcp.Resource{
			URI: v.URI, Name: v.Name, Title: v.Title, Description: v.Description, MIMEType: v.MIMEType, Meta: v.Meta,
		})
	}
	rec := a.viewAuditRecord(claims, eventResourcesList, srv.name, policy.Decision{Effect: policy.EffectAllow, Reason: "views served"})
	count := len(out)
	rec.listCount = &count
	if err := a.spoolMCP(r.Context(), rec); err != nil {
		a.rpcFail(w, r, req.ID, -32603, auditQueueFullMsg, err)
		return
	}
	writeRPCResult(w, req.ID, map[string]any{"resources": out})
}

// serveResourceRead answers resources/read on a server endpoint. A URI that
// a visible tool of the caller links is served from the manager's in-memory
// views. Any other URI is refused with JSON-RPC error -32002, the MCP code
// for a resource that is not found. Both leave one record, and an allowed
// read whose record cannot be queued is refused.
func (a *App) serveResourceRead(w http.ResponseWriter, r *http.Request, req rpcRequest, claims authn.Claims, _ policy.Subject, srv *serverScope) {
	var p struct {
		URI string `json:"uri"`
	}
	if err := json.Unmarshal(req.Params, &p); err != nil || p.URI == "" {
		writeRPCError(w, req.ID, -32602, uriRequiredMsg)
		return
	}
	for _, v := range linkedViews(srv.tools, srv.cat.views) {
		if v.URI != p.URI {
			continue
		}
		rec := a.viewAuditRecord(claims, eventResourcesRead, srv.name, policy.Decision{Effect: policy.EffectAllow, Reason: "view served"})
		rec.uri = p.URI
		if err := a.spoolMCP(r.Context(), rec); err != nil {
			a.rpcFail(w, r, req.ID, -32603, auditQueueFullMsg, err)
			return
		}
		writeRPCResult(w, req.ID, &mcp.ReadResourceResult{Contents: []*mcp.ResourceContents{{
			URI: v.URI, MIMEType: v.MIMEType, Text: v.Text, Meta: v.Meta,
		}}})
		return
	}
	a.auditViewDenied(r.Context(), claims, srv.name, p.URI, viewUnavailableMsg(p.URI))
	writeRPCError(w, req.ID, -32002, viewUnavailableMsg(p.URI))
}

// serveResourceTemplates answers resources/templates/list on a server
// endpoint: views have fixed URIs, so the list is always empty.
func serveResourceTemplates(_ *App, w http.ResponseWriter, _ *http.Request, req rpcRequest, _ authn.Claims, _ policy.Subject, _ *serverScope) {
	writeRPCResult(w, req.ID, map[string]any{"resourceTemplates": []any{}})
}

// viewAuditRecord is the straza.audit.mcp record of one view answer on the
// endpoint of the server app: the event, the server in app, and the decision.
// The caller sets listCount on a list and uri on a read.
func (a *App) viewAuditRecord(claims authn.Claims, event, app string, d policy.Decision) mcpAuditRecord {
	return mcpAuditRecord{
		claims: claims, ev: policy.Event{Kind: event, App: app}, decision: d,
		snapshotID: a.snapshots.Current().ID,
	}
}

// auditViewDenied records a refused resources/read of uri on the endpoint of
// the server app, with the refusal sentence as its reason. A record that
// stays out of the queue logs the fail-closed line, and the read stays
// refused.
func (a *App) auditViewDenied(ctx context.Context, claims authn.Claims, app, uri, reason string) {
	rec := a.viewAuditRecord(claims, eventResourcesRead, app, policy.Decision{Effect: policy.EffectDeny, Reason: reason})
	rec.uri = uri
	a.auditMCP(ctx, rec)
}
