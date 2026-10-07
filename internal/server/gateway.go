package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"

	"github.com/strazahq/straza/internal/authn"
	"github.com/strazahq/straza/internal/config"
	"github.com/strazahq/straza/internal/policy"
	"github.com/strazahq/straza/internal/version"
)

// The gateway PEP serves `POST /mcp` (streamable HTTP,
// stateless JSON responses) and an optional `GET /mcp` SSE stream for
// server→client notifications. Every request authenticates with a session
// token verified locally (JWKS-backed key cache) plus the in-memory denylist;
// tools/list serves a per-role-set precomputed catalog; tools/call runs the
// PDP, resolves credentials in memory, calls upstream, and audits
// asynchronously. NOTHING on these paths touches the database (invariant: no
// DB reads on request paths), asserted by TestGatewayNoDBOnRequestPath.

// ---- JSON-RPC plumbing (stateless streamable HTTP, JSON responses) ----

type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func writeRPCResult(w http.ResponseWriter, id json.RawMessage, result any) {
	writeJSON(w, http.StatusOK, map[string]any{"jsonrpc": "2.0", "id": id, "result": result})
}

func writeRPCError(w http.ResponseWriter, id json.RawMessage, code int, msg string) {
	if id == nil {
		id = json.RawMessage("null")
	}
	writeJSON(w, http.StatusOK, map[string]any{"jsonrpc": "2.0", "id": id, "error": rpcError{Code: code, Message: msg}})
}

// rpcFail is writeRPCError for internal-class failures (upstream call failed,
// approval service error; never a policy deny, which is a decision): the
// JSON-RPC body is unchanged, and exactly one Error record lands with the
// correlation id, the route and rpc_code (middleware.go). The HTTP
// status stays 200 (JSON-RPC errors ride 200), so the counter label is the
// rpc code.
func (a *App) rpcFail(w http.ResponseWriter, r *http.Request, id json.RawMessage, code int, msg string, err error) {
	a.logFailure(r, strconv.Itoa(http.StatusOK), msg, err, "rpc_code", code)
	a.countHTTPError(r, "rpc"+strconv.Itoa(code))
	writeRPCError(w, id, code, msg)
}

// gatewayAuth authenticates one gateway request: local token verify +
// denylist + cached subject. Fail closed with an actionable reason.
func (a *App) gatewayAuth(w http.ResponseWriter, r *http.Request) (authn.Claims, policy.Subject, bool) {
	raw := bearerToken(r)
	if raw == "" {
		apiError(w, http.StatusUnauthorized, "missing session token")
		return authn.Claims{}, policy.Subject{}, false
	}
	claims, err := a.tokens.Verify(raw)
	if err != nil || claims.Session == "" {
		apiError(w, http.StatusUnauthorized, "session token rejected")
		return authn.Claims{}, policy.Subject{}, false
	}
	if a.denylist.blocked(claims) {
		apiError(w, http.StatusForbidden, a.denylist.revokedMsg(claims, revokedSessionMsg, revokedIdentityMsg))
		return authn.Claims{}, policy.Subject{}, false
	}
	// The data plane enforces the minimum attestation from token claims
	// (no exemptions here; this is what keeps the checkin-side admin-CLI
	// exemption from becoming a gateway bypass). Claims-only, no DB reads on
	// the request path.
	if minAtt := a.cfg.Governance.MinAttestation; minAtt != "" &&
		config.AttestationRank(claims.Attestation) < config.AttestationRank(minAtt) {
		apiError(w, http.StatusForbidden, fmt.Sprintf(
			"Straza: attestation level %q is below the required level %q. Reinstall with `straza install --managed <harness>`",
			claims.Attestation, minAtt))
		return authn.Claims{}, policy.Subject{}, false
	}
	sub, ok := a.subjects.get(claims.Session)
	if !ok {
		// No cached subject (restart, or a role window edge since the
		// checkin): the token alone cannot prove roles, and guessing would
		// leak tools. Fail closed; the client re-checks in.
		apiError(w, http.StatusUnauthorized, sessionStateExpiredMsg)
		return authn.Claims{}, policy.Subject{}, false
	}
	return claims, sub, true
}

// handleMCP is the gateway endpoint: the combined endpoint /mcp, and a
// server's own endpoint /mcp/{server}, which is the same handler limited to
// that one server.
func (a *App) handleMCP(w http.ResponseWriter, r *http.Request) {
	claims, sub, ok := a.gatewayAuth(w, r)
	if !ok {
		return
	}
	switch r.Method {
	case http.MethodPost:
		a.handleMCPPost(w, r, claims, sub)
	case http.MethodGet:
		a.gateway.streams.serve(w, r, claims.Session)
	case http.MethodDelete:
		w.WriteHeader(http.StatusOK) // stateless: nothing to terminate
	default:
		apiError(w, http.StatusMethodNotAllowed, "POST, GET, or DELETE")
	}
}

func (a *App) handleMCPPost(w http.ResponseWriter, r *http.Request, claims authn.Claims, sub policy.Subject) {
	var req rpcRequest
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<20))
	if err := dec.Decode(&req); err != nil {
		writeRPCError(w, nil, -32700, "parse error: single JSON-RPC message expected (batching is not supported)")
		return
	}

	// A server endpoint resolves its scope before any method, so every
	// method of a server the caller's catalog does not hold gets the same
	// refusal.
	var srv *serverScope
	if name := r.PathValue("server"); name != "" {
		if srv = a.serverScopeFor(claims.Session, sub, name); srv == nil {
			a.refuseServer(w, r, req, claims, name)
			return
		}
	}
	method, ok := methodFor(req.Method, srv)
	if !ok {
		writeRPCError(w, req.ID, -32601, unknownMethodMsg(req.Method, srv))
		return
	}
	method(a, w, r, req, claims, sub, srv)
}

// mcpMethod answers one JSON-RPC method of the gateway. srv is the scope of
// a server's own endpoint, nil on the combined endpoint /mcp.
type mcpMethod func(a *App, w http.ResponseWriter, r *http.Request, req rpcRequest, claims authn.Claims, sub policy.Subject, srv *serverScope)

// gatewayMethods are the methods every gateway endpoint serves.
var gatewayMethods = map[string]mcpMethod{
	"initialize":                (*App).serveInitialize,
	"notifications/initialized": acceptNotification,
	"notifications/cancelled":   acceptNotification,
	"ping":                      servePing,
	"tools/list":                (*App).serveToolsList,
	"tools/call":                (*App).callTool,
}

// viewMethods are the methods a server endpoint adds when the server's
// views are on.
var viewMethods = map[string]mcpMethod{
	"resources/list":           (*App).serveResourcesList,
	"resources/read":           (*App).serveResourceRead,
	"resources/templates/list": serveResourceTemplates,
}

// methodFor returns the handler of method on the endpoint srv scopes, and
// false when that endpoint does not serve the method.
func methodFor(method string, srv *serverScope) (mcpMethod, bool) {
	if m, ok := gatewayMethods[method]; ok {
		return m, true
	}
	if srv.viewsOn() {
		m, ok := viewMethods[method]
		return m, ok
	}
	return nil, false
}

// unknownMethodMsg is the -32601 refusal of a method the endpoint does not
// serve. /mcp keeps its historical sentence.
func unknownMethodMsg(method string, srv *serverScope) string {
	if srv == nil {
		return fmt.Sprintf("method %q is not served by this gateway (tools only in v0)", method)
	}
	return fmt.Sprintf("Straza: method %q is not served on the endpoint of the MCP server %q. This endpoint serves the server's tools, and its views only when an administrator turns views on for the server.", method, srv.name)
}

// serveInitialize answers initialize. A server endpoint whose views are on
// also advertises resources and the MCP Apps extension.
func (a *App) serveInitialize(w http.ResponseWriter, _ *http.Request, req rpcRequest, _ authn.Claims, _ policy.Subject, srv *serverScope) {
	caps := map[string]any{"tools": map[string]any{"listChanged": true}}
	if srv.viewsOn() {
		caps["resources"] = map[string]any{}
		caps["extensions"] = map[string]any{viewsExtension: map[string]any{}}
	}
	writeRPCResult(w, req.ID, map[string]any{
		"protocolVersion": negotiateVersion(req.Params),
		"capabilities":    caps,
		"serverInfo": map[string]any{
			"name": "straza-gateway", "title": "Straza MCP Gateway", "version": version.Version,
		},
	})
}

// acceptNotification answers a client notification, which carries no reply.
func acceptNotification(_ *App, w http.ResponseWriter, _ *http.Request, _ rpcRequest, _ authn.Claims, _ policy.Subject, _ *serverScope) {
	w.WriteHeader(http.StatusAccepted)
}

// servePing answers ping.
func servePing(_ *App, w http.ResponseWriter, _ *http.Request, req rpcRequest, _ authn.Claims, _ policy.Subject, _ *serverScope) {
	writeRPCResult(w, req.ID, map[string]any{})
}

// negotiateVersion echoes a supported client protocol version or answers with
// the newest one this gateway speaks.
func negotiateVersion(params json.RawMessage) string {
	supported := map[string]bool{"2024-11-05": true, "2025-03-26": true, "2025-06-18": true}
	var p struct {
		ProtocolVersion string `json:"protocolVersion"`
	}
	if err := json.Unmarshal(params, &p); err == nil && supported[p.ProtocolVersion] {
		return p.ProtocolVersion
	}
	return "2025-06-18"
}
