// Package scim implements Straza's SCIM 2.0 server: the
// strict, documented subset in spec/scim-profile: Users, Groups,
// ServiceProviderConfig, Schemas, ResourceTypes; eq-only filters; PATCH
// add/replace/remove on mapped attributes; DELETE(=deactivate); no bulk.
// Everything outside the subset is rejected with a proper SCIM error.
// Conformance: spec/conformance/scim transcripts, replayed by the server
// test suite.
package scim

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"regexp"
	"strconv"
	"strings"

	"github.com/strazahq/straza/internal/store"
)

// SCIM schema URNs.
const (
	SchemaUser         = "urn:ietf:params:scim:schemas:core:2.0:User"
	SchemaGroup        = "urn:ietf:params:scim:schemas:core:2.0:Group"
	SchemaList         = "urn:ietf:params:scim:api:messages:2.0:ListResponse"
	SchemaPatch        = "urn:ietf:params:scim:api:messages:2.0:PatchOp"
	SchemaError        = "urn:ietf:params:scim:api:messages:2.0:Error"
	SchemaSPConfig     = "urn:ietf:params:scim:schemas:core:2.0:ServiceProviderConfig"
	SchemaResourceType = "urn:ietf:params:scim:schemas:core:2.0:ResourceType"
	// AgentSchemaPrefix accepts the IETF agentic-SCIM draft extension; the
	// exact URN is pinned when the draft stabilizes (spec/scim-profile §5).
	AgentSchemaPrefix = "urn:ietf:params:scim:schemas:extension:agent"
	// SchemaStrazaUser is Straza's own User extension (spec/scim-profile
	// §3.1): the READ-ONLY lock block. The IdM can see and ingest the
	// Straza-lane lock but can never flip it. `active` and `locked` are
	// different attributes, so reconciliation cannot collide.
	SchemaStrazaUser = "urn:straza:params:scim:schemas:extension:2.0:User"
	// SchemaStrazaGroup is the READ-ONLY Group extension (spec/scim-profile
	// §4.1, revision 9): a mapped group's access projection (role identity +
	// apps/tools/policies over the implication closure), so the entitlement
	// an IdM certifies carries real meaning instead of a bare name.
	// Evidence only: membership stays the sole assignable fact on this wire.
	SchemaStrazaGroup = "urn:straza:params:scim:schemas:extension:2.0:Group"
)

// Deps are the seams the SCIM server drives. All of them run on the control
// plane; DB access is expected here, never on decision paths.
type Deps struct {
	Store store.Store
	// Authenticate verifies the bearer a SCIM request presents: an admin API
	// token whose scope covers the scim area for the request method. On success the returned context carries the caller's audit
	// identity, and every write the request drives runs on it. On refusal it
	// returns the HTTP status (401 unknown, 403 known but lacking the grant)
	// and the detail sentence for the SCIM error body.
	Authenticate func(r *http.Request, bearer string) (context.Context, int, string)
	// Deactivate runs the kill-switch cascade for a user: disable sessions,
	// feed denylists, emit straza.revocation.user.
	Deactivate func(ctx context.Context, userID, reason string)
	// Reactivate is the SCIM-lane lift: origin-selective (spec/scim-profile
	// §3): it removes scim-origin revocations only, so an
	// admin/external lock survives IdM enables. Sessions already revoked stay
	// revoked; the user re-enrolls.
	Reactivate func(ctx context.Context, userID string)
	// IdentityChanged bumps role-resolution caches and emits the
	// straza.identity.* event.
	IdentityChanged func(ctx context.Context, subject, id string)
	// MembershipChanged reports one role assignment a Group write started
	// or ended, once per changed row in write order, so the server chains
	// the audit record for it. nil means no audit consumer.
	MembershipChanged func(ctx context.Context, change MembershipChange)
	// UserWritten reports one user write after its store write, so the
	// server chains the audit record for it: UserCreated with the new or
	// revived row, or UserUpdated with the written row and the admin API
	// names of the fields that changed, for a write that changed at least
	// one. nil means no audit consumer.
	UserWritten func(ctx context.Context, action string, u store.User, changed []string)
	// ProtectedUsername names the break-glass local admin (revision 12).
	// That account is invisible to this surface end to end: never
	// rendered, never a member, and every id-addressed op answers as if
	// it does not exist. "" = no protected account.
	ProtectedUsername string
	// RoleProjection renders the §4.1 Group extension block for a role
	// (by role id): identity of the role plus its transitive access
	// projection. nil provider or nil result = no block.
	// Control-plane reads only; SCIM is polled by IdMs, never by decisions.
	RoleProjection func(ctx context.Context, roleID string) map[string]any
	Log            *slog.Logger
}

// MembershipChange is one (user, role) assignment a Group write started or
// ended: the row id, the role, the user, the row's origin and the
// direction, MembershipAssign or MembershipUnassign.
type MembershipChange struct {
	AssignmentID string
	RoleID       string
	UserID       string
	Origin       string
	Action       string
}

// Membership change directions reported through Deps.MembershipChanged.
const (
	MembershipAssign   = "assign"
	MembershipUnassign = "unassign"
)

// User write actions reported through Deps.UserWritten.
const (
	UserCreated = "create"
	UserUpdated = "update"
)

// Server is the mounted SCIM subset.
type Server struct{ deps Deps }

// New builds the SCIM server.
func New(deps Deps) *Server {
	if deps.Log == nil {
		deps.Log = slog.Default()
	}
	return &Server{deps: deps}
}

// Routes mounts /scim/v2 on the mux.
func (s *Server) Routes(mux *http.ServeMux) {
	auth := s.requireToken
	mux.HandleFunc("GET /scim/v2/ServiceProviderConfig", auth(s.handleSPConfig))
	mux.HandleFunc("GET /scim/v2/Schemas", auth(s.handleSchemas))
	mux.HandleFunc("GET /scim/v2/Schemas/{id}", auth(s.handleSchema))
	mux.HandleFunc("GET /scim/v2/ResourceTypes", auth(s.handleResourceTypes))
	mux.HandleFunc("GET /scim/v2/ResourceTypes/{id}", auth(s.handleResourceType))
	mux.HandleFunc("/scim/v2/Bulk", auth(s.handleBulk))

	mux.HandleFunc("GET /scim/v2/Users", auth(s.handleUsersList))
	mux.HandleFunc("POST /scim/v2/Users", auth(s.handleUserCreate))
	mux.HandleFunc("GET /scim/v2/Users/{id}", auth(s.handleUserGet))
	mux.HandleFunc("PUT /scim/v2/Users/{id}", auth(s.handleUserReplace))
	mux.HandleFunc("PATCH /scim/v2/Users/{id}", auth(s.handleUserPatch))
	mux.HandleFunc("DELETE /scim/v2/Users/{id}", auth(s.handleUserDelete))

	mux.HandleFunc("GET /scim/v2/Groups", auth(s.handleGroupsList))
	mux.HandleFunc("POST /scim/v2/Groups", auth(s.handleGroupCreate))
	mux.HandleFunc("GET /scim/v2/Groups/{id}", auth(s.handleGroupGet))
	mux.HandleFunc("PUT /scim/v2/Groups/{id}", auth(s.handleGroupReplace))
	mux.HandleFunc("PATCH /scim/v2/Groups/{id}", auth(s.handleGroupPatch))
	mux.HandleFunc("DELETE /scim/v2/Groups/{id}", auth(s.handleGroupDelete))
}

// MissingTokenDetail is the 401 detail for a SCIM request with no usable
// bearer; it names the one credential that opens the plane and its mint.
const MissingTokenDetail = "valid token required: an admin API token whose scope carries the scim area (strazactl api-token create --scope scim:read,scim:write for an IdM)" // #nosec G101 -- an error sentence, not a credential

func (s *Server) requireToken(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		h := r.Header.Get("Authorization")
		const p = "Bearer "
		if !strings.HasPrefix(h, p) {
			writeError(w, http.StatusUnauthorized, "", MissingTokenDetail)
			return
		}
		ctx, status, detail := s.deps.Authenticate(r, h[len(p):])
		if status != 0 {
			writeError(w, status, "", detail)
			return
		}
		r = r.WithContext(ctx)
		// Bound every SCIM body (mirrors the gateway's cap): IdM payloads are
		// small, and an unbounded json.Decode is a one-request memory bomb.
		r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
		next(w, r)
	}
}

// writeError emits an RFC 7644 error body.
func writeError(w http.ResponseWriter, status int, scimType, detail string) {
	body := map[string]any{
		"schemas": []string{SchemaError},
		"status":  fmt.Sprintf("%d", status),
		"detail":  detail,
	}
	if scimType != "" {
		body["scimType"] = scimType
	}
	writeSCIM(w, status, body)
}

func writeSCIM(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/scim+json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

// listResponse wraps one already-paged slice of resources in the standard
// envelope. total is the full result count, not the page length.
func listResponse(w http.ResponseWriter, page []map[string]any, total, startIndex int) {
	if startIndex < 1 {
		startIndex = 1
	}
	if page == nil {
		page = []map[string]any{}
	}
	writeSCIM(w, http.StatusOK, map[string]any{
		"schemas":      []string{SchemaList},
		"totalResults": total,
		"startIndex":   startIndex,
		"itemsPerPage": len(page),
		"Resources":    page,
	})
}

// listPage renders one page of a full result set, slicing BEFORE rendering:
// an IdM full sync pages through the collection, and rendering all N
// resources to serve 100 of them turns every sync into O(N²) work.
func listPage[T any](w http.ResponseWriter, r *http.Request, items []T, render func(T) map[string]any) {
	start, _ := strconv.Atoi(r.URL.Query().Get("startIndex"))
	count, _ := strconv.Atoi(r.URL.Query().Get("count"))
	if start < 1 {
		start = 1
	}
	if count <= 0 || count > 200 {
		count = 100
	}
	lo := min(start-1, len(items))
	hi := min(lo+count, len(items))
	page := make([]map[string]any, 0, hi-lo)
	for _, it := range items[lo:hi] {
		page = append(page, render(it))
	}
	listResponse(w, page, len(items), start)
}

// filterRe accepts the ONLY supported filter grammar: `attr eq "value"`.
var filterRe = regexp.MustCompile(`^(\w+)\s+eq\s+"((?:[^"\\]|\\.)*)"$`)

// parseFilter returns (attr lowercase, value). allowed lists legal attrs.
func parseFilter(raw string, allowed ...string) (attr, value string, err error) {
	m := filterRe.FindStringSubmatch(strings.TrimSpace(raw))
	if m == nil {
		return "", "", fmt.Errorf("unsupported filter %q: only `attr eq \"value\"` on %v is supported, because Straza looks identities up by exact match only. Send one equality filter on one of those attributes, or list without a filter", raw, allowed)
	}
	attr = strings.ToLower(m[1])
	for _, a := range allowed {
		if attr == strings.ToLower(a) {
			return attr, unescapeFilterValue(m[2]), nil
		}
	}
	return "", "", fmt.Errorf("filter attribute %q is not supported: Straza filters on %v only. Filter on one of those attributes, or list without a filter", m[1], allowed)
}

// unescapeFilterValue reverses the filter-value quoting: every backslash
// escape the grammar admits (`\"`, `\\`, ...) becomes its literal character.
// Unescaping only `\"` would leave AD-style names (`CORP\\jdoe`) mismatching
// the stored value and loop the IdP on lookup-miss → re-POST → 409.
func unescapeFilterValue(v string) string {
	if !strings.ContainsRune(v, '\\') {
		return v
	}
	var b strings.Builder
	b.Grow(len(v))
	for i := 0; i < len(v); i++ {
		if v[i] == '\\' && i+1 < len(v) {
			i++
		}
		b.WriteByte(v[i])
	}
	return b.String()
}

func (s *Server) handleBulk(w http.ResponseWriter, _ *http.Request) {
	writeError(w, http.StatusNotImplemented, "", "bulk operations are outside the Straza SCIM subset (spec/scim-profile §1)")
}

// --- static discovery documents ---

func (s *Server) handleSPConfig(w http.ResponseWriter, _ *http.Request) {
	writeSCIM(w, http.StatusOK, map[string]any{
		"schemas":          []string{SchemaSPConfig},
		"documentationUri": "https://straza.dev/spec/scim-profile",
		"patch":            map[string]any{"supported": true},
		"bulk":             map[string]any{"supported": false, "maxOperations": 0, "maxPayloadSize": 0},
		"filter":           map[string]any{"supported": true, "maxResults": 200},
		"changePassword":   map[string]any{"supported": false},
		"sort":             map[string]any{"supported": false},
		"etag":             map[string]any{"supported": false},
		"authenticationSchemes": []map[string]any{{
			"type": "oauthbearertoken", "name": "Bearer token",
			"description": "admin API token whose scope carries the scim area, minted by strazactl api-token create",
		}},
	})
}

func schemaDoc(id, name, desc string) map[string]any {
	return map[string]any{"id": id, "name": name, "description": desc}
}

func (s *Server) handleSchemas(w http.ResponseWriter, _ *http.Request) {
	docs := []map[string]any{
		schemaDoc(SchemaUser, "User", "Straza mapped subset (spec/scim-profile §3)"),
		schemaDoc(SchemaGroup, "Group", "Straza mapped subset (spec/scim-profile §4)"),
		schemaDoc(SchemaStrazaUser, "StrazaUser", "Straza User extension: read-only lock block and Straza-born facts, read-write typology (spec/scim-profile §3.1-3.3)"),
		schemaDoc(SchemaStrazaGroup, "StrazaGroup", "Straza Group extension: read-only access projection (spec/scim-profile §4.1)"),
	}
	listResponse(w, docs, len(docs), 1)
}

func (s *Server) handleSchema(w http.ResponseWriter, r *http.Request) {
	switch r.PathValue("id") {
	case SchemaUser:
		writeSCIM(w, http.StatusOK, schemaDoc(SchemaUser, "User", "Straza mapped subset (spec/scim-profile §3)"))
	case SchemaGroup:
		writeSCIM(w, http.StatusOK, schemaDoc(SchemaGroup, "Group", "Straza mapped subset (spec/scim-profile §4)"))
	case SchemaStrazaUser:
		writeSCIM(w, http.StatusOK, schemaDoc(SchemaStrazaUser, "StrazaUser", "Straza User extension: read-only lock block and Straza-born facts, read-write typology (spec/scim-profile §3.1-3.3)"))
	case SchemaStrazaGroup:
		writeSCIM(w, http.StatusOK, schemaDoc(SchemaStrazaGroup, "StrazaGroup", "Straza Group extension: read-only access projection (spec/scim-profile §4.1)"))
	default:
		writeError(w, http.StatusNotFound, "", "unknown schema")
	}
}

func resourceTypeDoc(id, endpoint, schema string) map[string]any {
	doc := map[string]any{
		"schemas": []string{SchemaResourceType},
		"id":      id, "name": id, "endpoint": endpoint, "schema": schema,
	}
	if id == "User" {
		doc["schemaExtensions"] = []map[string]any{{"schema": SchemaStrazaUser, "required": false}}
	}
	if id == "Group" {
		doc["schemaExtensions"] = []map[string]any{{"schema": SchemaStrazaGroup, "required": false}}
	}
	return doc
}

func (s *Server) handleResourceTypes(w http.ResponseWriter, _ *http.Request) {
	docs := []map[string]any{
		resourceTypeDoc("User", "/Users", SchemaUser),
		resourceTypeDoc("Group", "/Groups", SchemaGroup),
	}
	listResponse(w, docs, len(docs), 1)
}

func (s *Server) handleResourceType(w http.ResponseWriter, r *http.Request) {
	switch r.PathValue("id") {
	case "User":
		writeSCIM(w, http.StatusOK, resourceTypeDoc("User", "/Users", SchemaUser))
	case "Group":
		writeSCIM(w, http.StatusOK, resourceTypeDoc("Group", "/Groups", SchemaGroup))
	default:
		writeError(w, http.StatusNotFound, "", "unknown resource type")
	}
}
