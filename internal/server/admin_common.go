package server

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/strazahq/straza/internal/drafts"
	"github.com/strazahq/straza/internal/store"
	"github.com/strazahq/straza/internal/tokenscopes"
)

// AdminRole is the role required for the /v1/admin surface.
const AdminRole = "straza-admin"

// Reserved self-enrollment roles (control plane, boot-ensured): holding one
// authorizes the matching channel on POST /v1/approvals/self/enroll-token.
// AdminRole passes both channels. The straza- prefix is the reserved product
// namespace, refused at role create (handleRolesCreate).
const (
	EnrollMobileRole  = "straza-enroll-mobile"
	EnrollBrowserRole = "straza-enroll-browser"
)

// MCPAdminRole is the product-defined global MCP admin: boot-created,
// undeletable, named so the name says global, and its standing is fixed at
// apps:read and apps:write over every server, so an identity manager imports
// one specific role with zero configuration.
const MCPAdminRole = "straza-global-mcp-admin"

// MCPAdminRoleDescription is the sentence the console and the identity
// manager show beside the role.
const MCPAdminRoleDescription = "Administers every MCP server: registration, changes, credentials and reach. " +
	"It opens the console's MCP servers area, never agent tools."

// DraftConfigRole is the product role that lists the built-in straza
// app's two drafting tools to its holders. It opens no
// console area, straza-admin does not include it, and nothing implies it
// when it is created.
const DraftConfigRole = drafts.DraftConfigRole

// DraftConfigRoleDescription is the sentence the console and the identity
// manager show beside the drafting role.
const DraftConfigRoleDescription = "Lets an agent propose config drafts through the built-in straza MCP server's tools straza__draft_submit and straza__draft_status. " +
	"A person publishes them. It opens no console area, and straza-admin does not include it."

// auditActor is the authenticated principal an admin mutation is attributed
// to (spec/events §2: straza.audit.admin `actor,actorId,actorVia`). Name is
// the human-readable handle (username or admin API token name); Via names the
// credential lane so a leaked-token incident reads WHICH credential acted.
type auditActor struct {
	Name string
	ID   string
	Via  string // "login" | "session" | "api-token"
}

type actorCtxKey struct{}

func withActor(ctx context.Context, act auditActor) context.Context {
	return context.WithValue(ctx, actorCtxKey{}, act)
}

func actorFrom(ctx context.Context) (auditActor, bool) {
	act, ok := ctx.Value(actorCtxKey{}).(auditActor)
	return act, ok
}

// adminStanding is what a request may administer on the apps area: every
// server when the caller holds an area grant, else the servers whose admin
// role the session holds. The wrapper derives it per request and the apps
// handlers read it to filter the list and to refuse a new name.
type adminStanding struct {
	Full bool
	Apps map[string]bool // app ids, by the admin role the session holds
	// AreaRefusal is set when the standing comes from the apps:write grant
	// instead of a minted role: it spans every server's own objects, and
	// this is the sentence for anything that belongs to no server.
	AreaRefusal string
}

type standingCtxKey struct{}

func withStanding(ctx context.Context, st adminStanding) context.Context {
	return context.WithValue(ctx, standingCtxKey{}, st)
}

// standingFrom answers the request's standing; a request that reached a
// handler through requireAdmin administers everything on its area.
func standingFrom(ctx context.Context) adminStanding {
	if st, ok := ctx.Value(standingCtxKey{}).(adminStanding); ok {
		return st
	}
	return adminStanding{Full: true}
}

// adminPrincipal is an authenticated admin request: the audit actor, the
// session's resolved roles on the human lanes (nil for an admin API token),
// and the grants that authorize it, either the token's own or the roles'
// union through admin.roleAreas.
type adminPrincipal struct {
	actor auditActor
	roles []store.Role
	scope tokenscopes.Scope
	root  bool
	// harness is the client name a session declared at check-in, empty for
	// a login token and for an admin API token.
	harness string
}

// areaRefusalNoGrants is the sentence a human admin gets when no role of
// theirs opens any area at all.
const areaRefusalNoGrants = "requires role " + AdminRole + ", " + MCPAdminRole + " or a role mapped in admin.roleAreas. " +
	"The admin role of a server opens that server's own routes only."

// areaAllows checks the principal's grants against the route's area,
// fail closed: a route outside adminRouteArea never passes on grants.
func (a *App) areaAllows(p adminPrincipal, r *http.Request) (bool, string) {
	if p.root {
		return true, ""
	}
	if p.roles != nil && len(p.scope.Grants) == 0 {
		return false, areaRefusalNoGrants
	}
	allowed, needed, mapped := p.scope.Allows(r.Method, r.Pattern)
	switch {
	case allowed:
		return true, ""
	case !mapped && p.roles == nil:
		a.log.Warn("route missing from adminRouteArea, api-token refused (fail closed)", "pattern", r.Pattern)
		return false, "route is not covered by the token's scopes"
	case !mapped:
		a.log.Warn("route missing from adminRouteArea, delegated admin refused (fail closed)", "pattern", r.Pattern)
		return false, "route is not covered by the admin role's scopes"
	case p.roles == nil:
		return false, "token lacks scope " + needed
	}
	return false, "session lacks scope " + needed
}

// codingHarnessRefusal is the sentence a session that a coding harness
// checked in reads on every admin route.
const codingHarnessRefusal = "this session belongs to the coding harness %q, and a coding harness cannot use the admin API. " +
	"Run strazactl from your own terminal after `strazactl login`, or use an admin API token (strazactl api-token create)."

// refuseCodingHarness answers 403 and reports true when the principal is a
// session that a coding harness checked in, whatever roles the user holds:
// the agent in that harness runs as the user and holds that session token.
// A login token, an admin API token and the sessions of the human clients
// in adminHarnesses pass. The harness name is the one the client declared
// at check-in.
func (a *App) refuseCodingHarness(w http.ResponseWriter, r *http.Request, p adminPrincipal) bool {
	if p.harness == "" || adminHarnesses[p.harness] {
		return false
	}
	a.log.Warn("coding-harness session refused on an admin route",
		"user", p.actor.ID, "harness", p.harness, "path", r.URL.Path)
	apiError(w, http.StatusForbidden, fmt.Sprintf(codingHarnessRefusal, p.harness))
	return true
}

// requireAdmin authenticates the request (session token or ID token in the
// Authorization header) and authorizes it against the admin plane: the
// straza-admin role is root, and roles mapped in admin.roleAreas get their
// areas only (delegated lane, admin_rbac.go), enforced at the same
// adminRouteArea seam as admin API token grants, fail closed. A session that
// a coding harness checked in is refused before any grant is read. The
// authenticated principal lands in the request context as the audit actor:
// every straza.audit.admin event emitted downstream names it (emitEventCtx),
// so admin mutations are never anonymous in the chain.
func (a *App) requireAdmin(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		p, ok := a.authenticateAdmin(w, r)
		if !ok {
			return
		}
		if a.refuseCodingHarness(w, r, p) {
			return
		}
		if allowed, refusal := a.areaAllows(p, r); !allowed {
			apiError(w, http.StatusForbidden, refusal)
			return
		}
		a.serveAdmin(w, r.WithContext(withActor(r.Context(), p.actor)), next)
	}
}

// fullAdminRefusal is the sentence a credential short of root reads on a
// route that manages the client assertion key.
const fullAdminRefusal = "this route manages the client assertion key, which can sign in as any agent at your identity provider, " +
	"so it needs a full administrator: the role " + AdminRole + " or an admin API token with the scope full. " +
	"Ask a holder of " + AdminRole + " to run the command."

// requireFullAdmin guards the routes that manage the client assertion key.
// It passes root only, the straza-admin role or an admin API token with the
// scope full, so no area grant and no server's admin role opens them. A
// session that a coding harness checked in is refused even for root.
func (a *App) requireFullAdmin(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		p, ok := a.authenticateAdmin(w, r)
		if !ok {
			return
		}
		if !p.root {
			apiError(w, http.StatusForbidden, fullAdminRefusal)
			return
		}
		if a.refuseCodingHarness(w, r, p) {
			return
		}
		a.serveAdmin(w, r.WithContext(withActor(r.Context(), p.actor)), next)
	}
}

// requireServerAdmin guards a server's own verbs: an area grant passes as
// on requireAdmin, and otherwise a session passes when it holds the admin
// role of at least one server, scoped to those servers. A route naming a
// server the session does not administer is refused with the role it would
// need. Admin API tokens never take this lane, they carry grants or nothing.
func (a *App) requireServerAdmin(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		p, ok := a.authenticateAdmin(w, r)
		if !ok {
			return
		}
		if a.refuseCodingHarness(w, r, p) {
			return
		}
		ctx := withActor(r.Context(), p.actor)
		allowed, refusal := a.areaAllows(p, r)
		if allowed {
			a.serveAdmin(w, r.WithContext(withStanding(ctx, adminStanding{Full: true})), next)
			return
		}
		if p.roles == nil {
			apiError(w, http.StatusForbidden, refusal)
			return
		}
		st, ok := a.serverAdminStanding(w, r, p, refusal)
		if !ok {
			return
		}
		if r.PathValue("id") != "" {
			row, ok := a.serverAdminTarget(w, r, st)
			if !ok {
				return
			}
			if !st.Apps[row.ID] {
				apiError(w, http.StatusForbidden, a.serverAdminRefusal(r.Context(), row))
				return
			}
		}
		a.serveAdmin(w, r.WithContext(withStanding(ctx, st)), next)
	}
}

// serverAdminStanding builds the standing of a session that holds no grant
// for the route: every live server when the session holds apps:write, so
// the global MCP admin administers every server's own roles and bindings
// and nothing global, else the servers whose minted admin role it holds.
// A session that administers nothing is refused before any lookup, so a
// stranger learns no server name.
func (a *App) serverAdminStanding(w http.ResponseWriter, r *http.Request, p adminPrincipal, refusal string) (adminStanding, bool) {
	var rows []store.App
	var err error
	st := adminStanding{}
	if p.scope.Grants["apps:write"] {
		rows, err = a.store.Apps().List(r.Context())
		st.AreaRefusal = refusal
	} else {
		ids := make([]string, len(p.roles))
		for i, role := range p.roles {
			ids[i] = role.ID
		}
		rows, err = a.store.Apps().ListByAdminRoles(r.Context(), ids)
	}
	if err != nil {
		a.fail(w, r, http.StatusInternalServerError, "server lookup failed", err)
		return st, false
	}
	if len(rows) == 0 {
		apiError(w, http.StatusForbidden, refusal)
		return st, false
	}
	st.Apps = make(map[string]bool, len(rows))
	for _, row := range rows {
		st.Apps[row.ID] = true
	}
	return st, true
}

// serverAdminTarget resolves the object a route's {id} names to the server
// it belongs to, by route family: the app itself under /v1/admin/apps, a
// role's owning server under /v1/admin/roles and a binding's server under
// /v1/admin/bindings. It answers the request itself when the object is
// unknown or belongs to no server, and refuses a family it does not know.
func (a *App) serverAdminTarget(w http.ResponseWriter, r *http.Request, st adminStanding) (store.App, bool) {
	ctx := r.Context()
	_, path, _ := strings.Cut(r.Pattern, " ")
	switch {
	case strings.HasPrefix(path, "/v1/admin/apps/"):
		row, err := a.appByRef(r)
		if err != nil {
			apiError(w, http.StatusNotFound, "unknown server")
			return store.App{}, false
		}
		return row, true
	case strings.HasPrefix(path, "/v1/admin/roles/"):
		role, err := a.store.Roles().GetByID(ctx, r.PathValue("id"))
		if errors.Is(err, store.ErrNotFound) {
			apiError(w, http.StatusNotFound, "no such role")
			return store.App{}, false
		}
		if err != nil {
			a.fail(w, r, http.StatusInternalServerError, "role lookup failed", err)
			return store.App{}, false
		}
		if role.OwnerAppID != "" {
			if row, err := a.store.Apps().GetByID(ctx, role.OwnerAppID); err == nil {
				return row, true
			}
		}
		if st.AreaRefusal != "" {
			apiError(w, http.StatusForbidden, st.AreaRefusal)
			return store.App{}, false
		}
		apiError(w, http.StatusForbidden, "the role "+role.Name+" belongs to no server, so a server admin cannot change it. Ask an identity administrator.")
		return store.App{}, false
	case strings.HasPrefix(path, "/v1/admin/bindings/"):
		b, err := a.bindingByID(ctx, r.PathValue("id"))
		if errors.Is(err, store.ErrNotFound) {
			apiError(w, http.StatusNotFound, "no access row with that id")
			return store.App{}, false
		}
		if err != nil {
			a.fail(w, r, http.StatusInternalServerError, "binding lookup failed", err)
			return store.App{}, false
		}
		row, err := a.store.Apps().GetByID(ctx, b.AppID)
		if err != nil {
			apiError(w, http.StatusNotFound, "unknown server")
			return store.App{}, false
		}
		return row, true
	}
	a.log.Warn("route under requireServerAdmin names an object of no known family, refused (fail closed)", "pattern", r.Pattern)
	apiError(w, http.StatusForbidden, "route is not covered by the admin role's scopes")
	return store.App{}, false
}

// serverAdminRefusal names the role a server admin would need for row.
func (a *App) serverAdminRefusal(ctx context.Context, row store.App) string {
	name := "unset"
	if role, err := a.store.Roles().GetByID(ctx, row.AdminRoleID); err == nil {
		name = role.Name
	}
	return drafts.ServerAdminRefusal(name)
}

// authenticateAdmin resolves the bearer into a principal, answering the
// request itself on failure: 401 for a missing, unknown or dead credential
// and 403 for a refused sign-in or a token whose stored scope no longer parses.
func (a *App) authenticateAdmin(w http.ResponseWriter, r *http.Request) (adminPrincipal, bool) {
	raw := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	if raw == "" || raw == r.Header.Get("Authorization") {
		apiError(w, http.StatusUnauthorized, "missing bearer token: /v1/admin takes an admin session or ID token, or an admin API token (strazactl api-token create)")
		return adminPrincipal{}, false
	}
	// Admin API tokens (wat_...) are their own principal: durable,
	// revocable, hashed at rest. No user, no roles: the scope on the
	// token is the whole authorization: per-area grants or `full`
	// (internal/tokenscopes). Scopes gate machine tokens ONLY; human admins
	// (session/login lanes below) are gated by the role, not grants.
	if strings.HasPrefix(raw, apiTokenPrefix) {
		meta, ok := a.apiTokenAuthenticate(r.Context(), raw)
		if !ok {
			apiError(w, http.StatusUnauthorized, "admin API token rejected: unknown, expired or revoked (strazactl api-token list)")
			return adminPrincipal{}, false
		}
		scope, err := tokenscopes.Parse(meta.Scope)
		if err != nil {
			// A stored pre-grammar token (the retired `read` word) or a
			// corrupted scope: refuse with the mint hint (fail closed,
			// re-minting is the migration).
			apiError(w, http.StatusForbidden, "token scope no longer valid: "+err.Error())
			return adminPrincipal{}, false
		}
		return adminPrincipal{actor: auditActor{Name: meta.Name, ID: meta.ID, Via: "api-token"}, scope: scope, root: scope.Full}, true
	}
	var u store.User
	var ses store.Session
	var via, harness string
	// Session tokens carry a `ses` claim; ID tokens carry an audience.
	// Both are EdDSA under the same keys, so disambiguate by claim shape.
	// The session lane judges the session as a refresh does on every
	// request. Admin plane, so store reads allowed.
	claims, err := a.tokens.Verify(raw)
	if err == nil && claims.Session != "" {
		var ok bool
		if u, ses, ok = a.judgeSession(w, r, claims, "admin bearer", claims.Harness); !ok {
			return adminPrincipal{}, false
		}
		via, harness = "session", ses.HarnessName
	} else if u, err = a.verifyLogin(r, raw); err == nil {
		via = "login"
	} else {
		var refused *loginRefusal
		if errors.As(err, &refused) {
			a.refuseLogin(w, r, refused, "")
			return adminPrincipal{}, false
		}
		// A store/issuer outage is not a rejected token.
		if a.answerOutage(w, r, "admin bearer", err) {
			return adminPrincipal{}, false
		}
		apiError(w, http.StatusUnauthorized, "token rejected: /v1/admin takes an admin session or ID token, or an admin API token (strazactl api-token create)")
		return adminPrincipal{}, false
	}
	if !personUser(u) {
		a.refuseNonPerson(w, r, u, ses)
		return adminPrincipal{}, false
	}

	userID, username := u.ID, u.Username
	roles, err := a.resolver.ResolveRoles(r.Context(), userID, time.Now())
	if err != nil {
		a.fail(w, r, http.StatusInternalServerError, "role resolution failed", err)
		return adminPrincipal{}, false
	}
	if roles == nil {
		roles = []store.Role{}
	}
	p := adminPrincipal{actor: auditActor{Name: username, ID: userID, Via: via}, roles: roles, harness: harness}
	for _, role := range roles {
		if role.Name == AdminRole {
			p.root = true
		}
	}
	// Delegated lane (admin_rbac.go): union the session's roles
	// through admin.roleAreas and enforce at the same seam as wat_
	// grants: same route map, same verbs, same fail-closed posture.
	p.scope = a.scopeForRoles(roles)
	return p, true
}

// nonPersonAdminRefusal is the sentence a user who is not a person reads on
// every admin route and every route that decides a request, formatted with
// the username and userTypeWord.
const nonPersonAdminRefusal = "the user %s is %s, and only a person can use the admin API or decide a request. " +
	"For automation, a person mints an admin API token with strazactl api-token create, " +
	"and an AI agent or a service account proposes config changes through the built-in straza MCP server's drafting tools, " +
	"which the role " + DraftConfigRole + " lists."

// refuseNonPerson answers 403 to a user who is not a person on an admin
// route or a route that decides, whatever roles it holds, before the route
// reads anything, and writes one login failure record that names the user
// and the reason. ses is the session on the session lane, whose id and
// harness the record carries, and zero on the ID-token lane.
func (a *App) refuseNonPerson(w http.ResponseWriter, r *http.Request, u store.User, ses store.Session) {
	f := authnFields{Via: "id-token", User: u.Username, UserID: u.ID, Reason: "only a person can use the admin API or decide a request"}
	if ses.ID != "" {
		f.Via, f.Session, f.Harness = "session-token", ses.ID, joinHarness(ses.HarnessName, ses.HarnessVersion)
	}
	a.emitAuthnLogin(r, "failure", f)
	apiError(w, http.StatusForbidden, fmt.Sprintf(nonPersonAdminRefusal, u.Username, userTypeWord(u)))
}

// identityChanged emits the event and invalidates the role-resolution cache.
func (a *App) identityChanged(r *http.Request, subject, id string) {
	a.identityChangedCtx(r.Context(), subject, id)
}

// identityChangedCtx is identityChanged for a caller that has a context and
// no request.
func (a *App) identityChangedCtx(ctx context.Context, subject, id string) {
	a.resolver.Bump()
	a.emitEventCtx(ctx, subject, map[string]any{"id": id})
}

// RoleKindStraza is the wire spelling of the control plane: capabilities in
// Straza itself (console administration, self enrollment), as opposed to the
// access plane that models the org's world. The class is wider than the
// console, because the straza-enroll-* roles belong to it.
const RoleKindStraza = "straza"

func wireRoleKind(r store.Role) string {
	if r.Plane == store.RolePlaneControl {
		return RoleKindStraza
	}
	return r.Kind
}
