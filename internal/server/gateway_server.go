package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/strazahq/straza/internal/authn"
	"github.com/strazahq/straza/internal/manager"
	"github.com/strazahq/straza/internal/policy"
)

// serverCatalog is one proxied server's part of a tier-1 catalog: its
// catalog entries in catalog order, with their upstream _meta, whether the
// server's views are on, and the views the manager serves for it.
type serverCatalog struct {
	tools   []gwTool
	viewsOn bool
	views   []manager.View
}

// serverScope is the reach of one request on a server's own endpoint,
// /mcp/{server}: the caller's tier-1 catalog and overlay, the server's part of
// that catalog, and the server's tools the caller can see, under the server's
// own names.
type serverScope struct {
	name    string
	tier1   *sessionCatalog
	overlay *catalogOverlay
	cat     *serverCatalog
	tools   []gwTool
}

// serverScopeFor resolves the endpoint of the server name for one caller.
// It returns nil when the caller's catalog, after the session's overlay hides
// the tools its policy denies, holds no tool of that server, so an unknown
// server, a stopped one and one the caller's roles do not reach look the
// same. It reads the same in-memory catalog and overlay as /mcp, never the
// database.
func (a *App) serverScopeFor(session string, sub policy.Subject, name string) *serverScope {
	tier1 := a.catalogFor(sub.Roles)
	sc := tier1.servers[name]
	if sc == nil {
		return nil
	}
	ov := a.overlayFor(session, sub, tier1)
	tools := serverTools(name, sc, ov)
	if len(tools) == 0 {
		return nil
	}
	return &serverScope{name: name, tier1: tier1, overlay: ov, cat: sc, tools: tools}
}

// serverTools lists one server's catalog entries for its own endpoint: the
// tools the overlay hides left out, the justification schemas swapped in,
// each entry renamed to the server's own tool name, and _meta kept verbatim.
// It builds fresh copies and never mutates the tier-1 entries.
func serverTools(name string, sc *serverCatalog, ov *catalogOverlay) []gwTool {
	out := make([]gwTool, 0, len(sc.tools))
	for _, t := range sc.tools {
		if ov.hidden[t.Name] {
			continue
		}
		if s, ok := ov.schemas[t.Name]; ok {
			t.InputSchema = s
		}
		t.Name = strings.TrimPrefix(t.Name, name+"__")
		out = append(out, t)
	}
	return out
}

// catalogName is the catalog's namespaced name of the server's tool.
func (s *serverScope) catalogName(tool string) string {
	return s.name + "__" + tool
}

// target resolves a tool by the server's own name. A prefixed name, a
// built-in straza tool and a tool of another server are not found, because
// the target must name this server and this exact tool.
func (s *serverScope) target(tool string) (gwTarget, bool) {
	t, ok := s.tier1.targets[s.catalogName(tool)]
	return t, ok && t.app == s.name && t.tool == tool
}

// viewsOn reports whether the endpoint serves the server's views: a server
// endpoint of a server whose views are on. It is false on /mcp (nil scope).
func (s *serverScope) viewsOn() bool {
	return s != nil && s.cat.viewsOn
}

// notInCatalogMsg is the one refusal of a server endpoint whose server the
// caller's catalog does not hold, the same sentence for an unknown, a stopped
// and an ungranted server, so the endpoint does not reveal which servers
// exist.
func notInCatalogMsg(name string) string {
	return fmt.Sprintf("Straza: no MCP server named %q is in your catalog. Check the name, or ask an administrator for access to it.", name)
}

// refuseServer answers any request on the endpoint of a server the caller's
// catalog does not hold with JSON-RPC error -32602 and notInCatalogMsg, and a
// notification, which has no id, with 202 and no body, the same for every
// name. A tools/call and a resources/read still leave one deny record whose
// reason is that sentence, as a probe of an unbound tool does on /mcp, so a
// sweep of server names reaches the SIEM.
func (a *App) refuseServer(w http.ResponseWriter, r *http.Request, req rpcRequest, claims authn.Claims, name string) {
	msg := notInCatalogMsg(name)
	switch req.Method {
	case "tools/call":
		var p struct {
			Name string `json:"name"`
		}
		_ = json.Unmarshal(req.Params, &p)
		a.auditRefused(r.Context(), claims, policy.Event{Kind: policy.EventToolPre, Tool: policy.ToolMCPCall, App: name, ToolName: p.Name}, gwTarget{}, msg)
	case "resources/read":
		var p struct {
			URI string `json:"uri"`
		}
		_ = json.Unmarshal(req.Params, &p)
		a.auditViewDenied(r.Context(), claims, name, p.URI, msg)
	}
	if len(req.ID) == 0 {
		w.WriteHeader(http.StatusAccepted)
		return
	}
	writeRPCError(w, req.ID, -32602, msg)
}
