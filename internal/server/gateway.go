package server

import (
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"slices"
	"strconv"
	"strings"

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
	raw, scheme := sessionCredential(r)
	if raw == "" {
		apiError(w, http.StatusUnauthorized, "missing session token")
		return authn.Claims{}, policy.Subject{}, false
	}
	claims, err := a.verifySessionToken(w, r, raw, scheme)
	if errors.Is(err, errAnswered) {
		return authn.Claims{}, policy.Subject{}, false
	}
	if err != nil || claims.Session == "" {
		apiError(w, http.StatusUnauthorized, "session token rejected")
		return authn.Claims{}, policy.Subject{}, false
	}
	if a.denylist.blocked(claims) {
		msg := a.denylist.revokedMsg(claims, revokedSessionMsg, revokedIdentityMsg)
		a.auditDoorRefusal(r, claims, "revoked", msg)
		apiError(w, http.StatusForbidden, msg)
		return authn.Claims{}, policy.Subject{}, false
	}
	// The data plane enforces the minimum attestation from token claims
	// (no exemptions here; this is what keeps the checkin-side admin-CLI
	// exemption from becoming a gateway bypass). Claims-only, no DB reads on
	// the request path.
	if minAtt := a.cfg.Governance.MinAttestation; minAtt != "" &&
		config.AttestationRank(claims.Attestation) < config.AttestationRank(minAtt) {
		msg := fmt.Sprintf("Straza: attestation level %q is below the required level %q. "+managedInstallStep, claims.Attestation, minAtt)
		a.auditDoorRefusal(r, claims, "attestation", msg)
		apiError(w, http.StatusForbidden, msg)
		return authn.Claims{}, policy.Subject{}, false
	}
	e, ok := a.subjects.forToken(claims)
	if !ok {
		// No cached subject for the token's roles (restart, a role window
		// edge since the checkin, or a token another replica minted after a
		// role changed): the token alone cannot prove roles, and guessing
		// would leak tools. Fail closed; the client re-checks in.
		apiError(w, http.StatusUnauthorized, sessionStateExpiredMsg)
		return authn.Claims{}, policy.Subject{}, false
	}
	return claims, e.sub, true
}

// handleMCP is the gateway endpoint: the combined endpoint /mcp, and a
// server's own endpoint /mcp/{server}, which is the same handler limited to
// that one server. A request from a web page of another origin gets HTTP
// 403 and originRefusedMsg on every method, before authentication, and no
// record, since no caller is known yet. A request with a malformed
// Mcp-Session-Id gets HTTP 400 and badConnLabelMsg on every method, before
// its body is read. A GET or a DELETE whose MCP-Protocol-Version the
// gateway does not speak gets HTTP 400 and badVersionMsg.
func (a *App) handleMCP(w http.ResponseWriter, r *http.Request) {
	if origin := r.Header.Get("Origin"); origin != "" &&
		!slices.ContainsFunc(a.proofOrigins(r), func(o string) bool { return strings.EqualFold(o, origin) }) {
		apiError(w, http.StatusForbidden, fmt.Sprintf(originRefusedMsg, cutUnresolved(origin)))
		return
	}
	claims, sub, ok := a.gatewayAuth(w, r)
	if !ok {
		return
	}
	if _, ok := connLabel(r); !ok {
		apiError(w, http.StatusBadRequest, badConnLabelMsg)
		return
	}
	switch r.Method {
	case http.MethodPost:
		a.handleMCPPost(w, r, claims, sub)
	case http.MethodGet:
		if !refuseVersion(w, r) {
			a.gateway.streams.serve(w, r, claims.Session)
		}
	case http.MethodDelete:
		if !refuseVersion(w, r) {
			w.WriteHeader(http.StatusOK) // stateless: nothing to terminate
		}
	default:
		apiError(w, http.StatusMethodNotAllowed, "POST, GET, or DELETE")
	}
}

// originRefusedMsg refuses a request whose Origin header names a web page
// that is not at the gateway's own address. It takes that origin.
const originRefusedMsg = "Straza: this request was not taken, because it came from a web page at %q, and the Straza gateway takes MCP requests from MCP clients and from Straza's own address only. Connect an MCP client to the gateway instead of calling it from a web page."

// mcpVersions are the versions of the MCP protocol the gateway speaks,
// newest first. negotiateVersion answers one of them, and refuseVersion
// takes no other in the MCP-Protocol-Version header.
var mcpVersions = []string{"2025-06-18", "2025-03-26", "2024-11-05"}

// badVersionMsg refuses a request whose MCP-Protocol-Version header names a
// version not in mcpVersions. It takes the header's value.
const badVersionMsg = "Straza: this request was not taken, because its MCP-Protocol-Version header names %q, a version of the MCP protocol the Straza gateway does not speak. The gateway speaks 2025-06-18, 2025-03-26 and 2024-11-05. Reconnect the MCP client so it agrees on one of them."

// refuseVersion answers HTTP 400 and badVersionMsg, and true, when the
// MCP-Protocol-Version header of r names a version not in mcpVersions. An
// absent or empty header passes.
func refuseVersion(w http.ResponseWriter, r *http.Request) bool {
	v := r.Header.Get("MCP-Protocol-Version")
	if v == "" || slices.Contains(mcpVersions, v) {
		return false
	}
	apiError(w, http.StatusBadRequest, fmt.Sprintf(badVersionMsg, cutUnresolved(v)))
	return true
}

// withoutID reports whether a JSON-RPC message carries no id, its id member
// absent or JSON null. Such a message is a notification, which the gateway
// never runs and never answers with a body.
func withoutID(id json.RawMessage) bool {
	return len(id) == 0 || string(id) == "null"
}

// mcpSessionHeader is the MCP streamable HTTP header that carries the label
// of a client's MCP connection. serveInitialize gives one out, and the
// client sends it back on every later request of the connection.
const mcpSessionHeader = "Mcp-Session-Id"

// badConnLabelMsg refuses a request whose Mcp-Session-Id no initialize of
// the gateway could have answered.
const badConnLabelMsg = "Straza: the Mcp-Session-Id header of this request is not a session id Straza gave out. Reconnect the MCP client so it starts a new session."

// connLabel answers the label of the caller's MCP connection, the
// Mcp-Session-Id header of r, and empty when the header is absent or empty.
// It answers false for a value that is not 1 to 64 letters, digits, '-' or
// '_'. The gateway keeps no record of the labels it gave out, so any
// instance takes any well-formed one. A label only picks one of the caller's
// own upstream sessions and never changes what the caller may do.
func connLabel(r *http.Request) (string, bool) {
	v := r.Header.Get(mcpSessionHeader)
	if len(v) > 64 {
		return "", false
	}
	for i := range len(v) {
		switch c := v[i]; {
		case 'a' <= c && c <= 'z', 'A' <= c && c <= 'Z', '0' <= c && c <= '9', c == '-', c == '_':
		default:
			return "", false
		}
	}
	return v, true
}

func (a *App) handleMCPPost(w http.ResponseWriter, r *http.Request, claims authn.Claims, sub policy.Subject) {
	// gatewayAuth verified the session, so the body gets the bulk window.
	extendBodyRead(w, r, bulkBodyReadTimeout)
	var req rpcRequest
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, a.cfg.Server.MCPBodyLimit()))
	if err := dec.Decode(&req); err != nil {
		var tooLarge *http.MaxBytesError
		var netErr net.Error
		switch {
		case errors.As(err, &tooLarge):
			a.refuseLargeBody(w, r, claims, bufferedRequestID(dec.Buffered()), tooLarge.Limit)
			return
		case errors.Is(err, os.ErrDeadlineExceeded) || errors.As(err, &netErr) && netErr.Timeout():
			a.refuseSlowBody(w, r, claims, bufferedRequestID(dec.Buffered()))
			return
		}
		writeRPCError(w, nil, -32700, "parse error: single JSON-RPC message expected (batching is not supported)")
		return
	}
	// A client may send its own newest version with initialize, before any
	// version is agreed.
	if req.Method != "initialize" && refuseVersion(w, r) {
		return
	}
	// A message without an id is answered 202 before the server scope, so
	// it is never decided, called or recorded, on any endpoint and for any
	// server name.
	if withoutID(req.ID) {
		w.WriteHeader(http.StatusAccepted)
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

// largeBodyMsg is the agent's answer to an MCP request whose body is over
// server.mcpMaxBodyBytes, and largeBodyLog the operator's Warn line of it.
// Both take the limit, and largeBodyLog also the ceiling.
const (
	largeBodyMsg = "Straza: this MCP request was not read, because its body is larger than %d bytes, the most the Straza gateway reads in one request (server.mcpMaxBodyBytes). " +
		"Send less in one call, for example a file's path or address instead of its content, or ask a Straza administrator to raise server.mcpMaxBodyBytes."
	largeBodyLog = "strazad refused an MCP request whose body is over server.mcpMaxBodyBytes (%d bytes). Raise the setting, up to %d, if agents need larger calls."
)

// refuseLargeBody answers an MCP request whose body is over limit with
// JSON-RPC error -32600 and largeBodyMsg, and logs largeBodyLog once with
// the session. With the request's id the answer is HTTP 200, as every
// other JSON-RPC error of this endpoint is, because the go-sdk MCP client
// that the straza client's bridge uses keeps only the status text of a
// non-2xx answer and fails its whole connection. With no id it is HTTP 413
// and id null. The request is never read, so it is never decided and
// writes no audit record, as a request whose body does not parse writes
// none.
func (a *App) refuseLargeBody(w http.ResponseWriter, r *http.Request, claims authn.Claims, id json.RawMessage, limit int64) {
	a.reqlog(r.Context()).Warn(fmt.Sprintf(largeBodyLog, limit, config.MCPMaxBodyBytesCeiling), "session", claims.Session)
	status := http.StatusOK
	if id == nil {
		status, id = http.StatusRequestEntityTooLarge, json.RawMessage("null")
	}
	writeJSON(w, status, map[string]any{"jsonrpc": "2.0", "id": id, "error": rpcError{Code: -32600, Message: fmt.Sprintf(largeBodyMsg, limit)}})
}

// slowBodyMsg is the agent's answer to an MCP request whose body stopped
// arriving until the read deadline of its connection passed, and
// slowBodyLog the operator's Warn line of it.
const (
	slowBodyMsg = "Straza: this MCP request was not read, because its body arrived too slowly and Straza cut it off. " +
		"Check the network between this machine and Straza, or send less in one call, then try the call once more. If it fails again, tell the user."
	slowBodyLog = "strazad cut an MCP request whose body arrived too slowly. One client on a slow or stalled link causes this. " +
		"When many clients do, check the network in front of strazad"
)

// refuseSlowBody answers an MCP request whose body read hit its deadline
// with JSON-RPC error -32600 and slowBodyMsg, and logs slowBodyLog once with
// the session. With the request's id, from the part that arrived, the
// answer is HTTP 200, as refuseLargeBody's is, and with no id it is HTTP 408
// and id null. The request is never read, so it writes no audit record.
func (a *App) refuseSlowBody(w http.ResponseWriter, r *http.Request, claims authn.Claims, id json.RawMessage) {
	a.reqlog(r.Context()).Warn(slowBodyLog, "session", claims.Session)
	status := http.StatusOK
	if id == nil {
		status, id = http.StatusRequestTimeout, json.RawMessage("null")
	}
	writeJSON(w, status, map[string]any{"jsonrpc": "2.0", "id": id, "error": rpcError{Code: -32600, Message: slowBodyMsg}})
}

// bufferedRequestID answers the id of a JSON-RPC request from prefix, the
// part of its body read before the limit: it walks the top-level keys in
// order and answers the value of id when it is a string or a number that
// comes before params. It answers nil otherwise, also for a prefix that is
// not an object and for an id with nothing after it, which the limit may
// have cut. Keys match without regard to case, as encoding/json matches
// them when it decodes a whole request.
func bufferedRequestID(prefix io.Reader) json.RawMessage {
	dec := json.NewDecoder(prefix)
	if t, err := dec.Token(); err != nil || t != json.Delim('{') {
		return nil
	}
	for dec.More() {
		t, err := dec.Token()
		key, ok := t.(string)
		if err != nil || !ok || strings.EqualFold(key, "params") {
			return nil
		}
		var v json.RawMessage
		if err := dec.Decode(&v); err != nil {
			return nil
		}
		if strings.EqualFold(key, "id") {
			if !dec.More() {
				return nil
			}
			if v[0] == '"' || v[0] == '-' || (v[0] >= '0' && v[0] <= '9') {
				return v
			}
			return nil
		}
	}
	return nil
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

// serveInitialize answers initialize, with a fresh random label for the
// connection in the Mcp-Session-Id header (connLabel). A server endpoint
// whose views are on also advertises resources and the MCP Apps extension.
func (a *App) serveInitialize(w http.ResponseWriter, _ *http.Request, req rpcRequest, _ authn.Claims, _ policy.Subject, srv *serverScope) {
	w.Header().Set(mcpSessionHeader, rand.Text())
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
	var p struct {
		ProtocolVersion string `json:"protocolVersion"`
	}
	if err := json.Unmarshal(params, &p); err == nil && slices.Contains(mcpVersions, p.ProtocolVersion) {
		return p.ProtocolVersion
	}
	return mcpVersions[0]
}
