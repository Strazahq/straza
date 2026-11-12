package server

import (
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/strazahq/straza/internal/authn"
	"github.com/strazahq/straza/internal/identity"
	"github.com/strazahq/straza/internal/store"
)

type userPayload struct {
	ID         string `json:"id"`
	Username   string `json:"username"`
	Email      string `json:"email"`
	Display    string `json:"display"`
	Title      string `json:"title,omitempty"` // SCIM core title: job title / agent function
	Status     string `json:"status"`
	Origin     string `json:"origin"`
	Kind       string `json:"kind"` // human|nhi: what the identity IS (origin says who created it)
	ExternalID string `json:"external_id,omitempty"`
	// Identity typology (SCIM-mastered in enterprise, admin-editable on
	// standalone). Empty = unclassified.
	UserType   string `json:"user_type,omitempty"`
	AgencyMode string `json:"agency_mode,omitempty"`
	Sponsor    string `json:"sponsor,omitempty"`
	SwarmID    string `json:"swarm_id,omitempty"`
	Ephemeral  bool   `json:"ephemeral,omitempty"`
	// EntraAgentID is the Entra agent identity an agent acts as at Entra.
	EntraAgentID string `json:"entra_agent_id,omitempty"`
	// Provisioning timestamps.
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

func toUserPayload(u store.User) userPayload {
	return userPayload{ID: u.ID, Username: u.Username, Email: u.Email, Display: u.Display,
		Title:  u.Title,
		Status: u.Status, Origin: u.Origin, Kind: userKind(u), ExternalID: u.ExternalID,
		UserType: u.UserType, AgencyMode: u.AgencyMode, Sponsor: u.Sponsor,
		SwarmID: u.SwarmID, Ephemeral: u.Ephemeral, EntraAgentID: u.EntraAgentID,
		CreatedAt: u.CreatedAt, UpdatedAt: u.UpdatedAt}
}

// userKind surfaces the IGA-facing identity kind: "nhi" when the user was
// created with attrs.kind=nhi (agentic-SCIM extension or admin create), OR
// when the IdM-mastered typology says the identity is non-human (userType
// agent|service; spec/scim-profile §3.2). The derivation exists because the
// two mechanisms can disagree for typology-born agents (attrs.kind is
// create-only and the SCIM connector cannot send the agentic URN), which
// would mirror kind=human into IdM shadows and leave the identity out of
// every kind-gated lane. Deriving at read time serves all consumers (console,
// admin API, pull connector, NHI key endpoints) with no migration; the
// engine-side twin is internal/approval userIsNHI.
func userKind(u store.User) string {
	if u.IsNHI() {
		return "nhi"
	}
	return "human"
}

// pagedUserPayload enriches paged users rows: effective_roles from
// the SAME resolver every admin request runs (direct + implication
// closure, truer than any client-side fold), last_seen from one batched
// session read, locks from one batched revocations read so the console
// never offers Lock on an already-locked user, and the sponsored count from
// one grouped read.
// Empty locks = not locked. writeUsersPage fills it.
type pagedUserPayload struct {
	userPayload
	EffectiveRoles []string      `json:"effective_roles"`
	Locks          []lockPayload `json:"locks"`
	LastSeen       *time.Time    `json:"last_seen,omitempty"`
	SponsoredCount int           `json:"sponsored_count"`
}

// usersFilter parses ?q= / ?status= / ?role= and ?sponsor=, the
// exact username of the sponsor. Filters require the paged lane: on the
// legacy bare lane they would have to be silently ignored, and a filter that
// silently does nothing is a lie about the data.
// ?role= names a role; matching is EFFECTIVE, so the name expands to every
// role that transitively implies it (reverse reachability) before the store
// match: a holder of "admin" that implies "reader" must appear under
// ?role=reader.
func (a *App) usersFilter(w http.ResponseWriter, r *http.Request, paged bool) (store.UserFilter, bool) {
	q := r.URL.Query()
	f := store.UserFilter{Q: strings.TrimSpace(q.Get("q")), Status: q.Get("status"), Sponsor: q.Get("sponsor")}
	role := q.Get("role")
	if !paged {
		if f.Q != "" || f.Status != "" || role != "" || f.Sponsor != "" {
			apiError(w, http.StatusBadRequest, "q/status/role/sponsor filters require pagination: add limit=")
			return f, false
		}
		return f, true
	}
	switch f.Status {
	case "", store.UserActive, store.UserDisabled:
	default:
		apiError(w, http.StatusBadRequest, "status must be active or disabled")
		return f, false
	}
	if role != "" {
		target, err := a.store.Roles().GetByName(r.Context(), role)
		if errors.Is(err, store.ErrNotFound) {
			apiError(w, http.StatusBadRequest, "unknown role: "+role)
			return f, false
		}
		if err != nil {
			a.fail(w, r, http.StatusInternalServerError, "role lookup failed", err)
			return f, false
		}
		imps, err := a.store.Roles().ListImplications(r.Context())
		if err != nil {
			a.fail(w, r, http.StatusInternalServerError, "implication lookup failed", err)
			return f, false
		}
		reverse := map[string][]string{}
		for _, imp := range imps {
			reverse[imp.ImpliesRoleID] = append(reverse[imp.ImpliesRoleID], imp.RoleID)
		}
		closure := identity.Closure(map[string]bool{target.ID: true}, reverse)
		for id := range closure {
			f.RoleIDs = append(f.RoleIDs, id)
		}
		sort.Strings(f.RoleIDs) // deterministic SQL for tests and plans
	}
	return f, true
}

func (a *App) handleUsersList(w http.ResponseWriter, r *http.Request) {
	p, ok := parseSortedPage(w, r, userSortKeys)
	if !ok {
		return
	}
	f, ok := a.usersFilter(w, r, p.paged)
	if !ok {
		return
	}
	if !p.paged {
		users, err := a.store.Users().List(r.Context())
		if err != nil {
			a.fail(w, r, http.StatusInternalServerError, "list users failed", err)
			return
		}
		out := make([]userPayload, len(users))
		for i, u := range users {
			out[i] = toUserPayload(u)
		}
		writeJSON(w, http.StatusOK, out)
		return
	}
	users, err := a.store.Users().Page(r.Context(), f, p.sort, store.Cursor{Value: p.value, ID: p.before}, p.limit+1)
	if errors.Is(err, store.ErrBadCursor) {
		apiError(w, http.StatusBadRequest, "invalid or stale cursor: re-fetch from the start")
		return
	}
	if err != nil {
		a.fail(w, r, http.StatusInternalServerError, "list users failed", err)
		return
	}
	var after *store.User
	if len(users) > p.limit {
		users = users[:p.limit]
		after = &users[p.limit-1]
	}
	a.writeUsersPage(w, r, p, users, after)
}

// The 400 answers to a password for an AI agent or a service account, which
// signs in with its key. Each names the user, its type word from
// userTypeWord and the user again in the command.
const (
	nonPersonPasswordCreateMsg = "user %s was not created: %s signs in with its key and never with a password. " +
		"Create it without the password. Then run straza keygen on its machine and register the public key it prints " +
		"with strazactl users nhi-key set %s <publicKey> or in the console."
	nonPersonPasswordPatchMsg = "user %s was not changed: it is %s, which signs in with its key and never with a password. " +
		"Send the request again without the password. To let it sign in, run straza keygen on its machine and register the public key it prints " +
		"with strazactl users nhi-key set %s <publicKey> or in the console."
)

func (a *App) handleUsersCreate(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Username string `json:"username"`
		Email    string `json:"email"`
		Display  string `json:"display"`
		Title    string `json:"title"`
		Password string `json:"password"`
		// Kind marks non-human identities (headless NHIs) at creation:
		// "nhi" stores attrs.kind exactly like the agentic-SCIM extension
		// does, so IGA and the client_credentials grant see one convention.
		Kind string `json:"kind"`
		// UserType types an nhi as agent or service. An nhi created without
		// it is stored as an agent, so no non-human row is born untyped.
		UserType string `json:"user_type"`
	}
	err := decodeAdmin(r.Body, &req)
	if refusedUnknownField(w, err) {
		return
	}
	if err != nil || req.Username == "" {
		apiError(w, http.StatusBadRequest, "username is required")
		return
	}
	u := store.User{Username: req.Username, Email: req.Email, Display: req.Display, Title: req.Title}
	userType := strings.ToLower(strings.TrimSpace(req.UserType))
	switch req.Kind {
	case "", "human":
		if userType != "" {
			apiError(w, http.StatusBadRequest, "user_type is accepted only when kind is nhi. Leave it out to create a person, or send kind nhi with user_type agent or service")
			return
		}
	case "nhi":
		u.Attrs = `{"kind":"nhi"}`
		switch userType {
		case "":
			u.UserType = store.UserTypeAgent
		case store.UserTypeAgent, store.UserTypeService:
			u.UserType = userType
		default:
			apiError(w, http.StatusBadRequest, "user_type must be agent or service when kind is nhi. Send agent for an AI agent that acts for a person, or service for a technical account with no agency")
			return
		}
	default:
		apiError(w, http.StatusBadRequest, "kind must be human or nhi")
		return
	}
	if req.Password != "" && !personUser(u) {
		apiError(w, http.StatusBadRequest, fmt.Sprintf(nonPersonPasswordCreateMsg, u.Username, userTypeWord(u), u.Username))
		return
	}
	if req.Password != "" {
		hash, err := authn.HashPassword(req.Password)
		if err != nil {
			a.fail(w, r, http.StatusInternalServerError, "hash failed", err)
			return
		}
		u.PasswordHash = hash
	}
	created, err := a.store.Users().Create(r.Context(), u)
	if errors.Is(err, store.ErrConflict) {
		apiError(w, http.StatusConflict, "username already exists")
		return
	}
	if err != nil {
		a.fail(w, r, http.StatusInternalServerError, "create failed", err)
		return
	}
	a.identityChanged(r, "straza.identity.created", created.ID)
	a.auditUserCreate(r.Context(), created, store.OriginAdmin)
	writeJSON(w, http.StatusCreated, toUserPayload(created))
}

func (a *App) handleUsersGet(w http.ResponseWriter, r *http.Request) {
	u, err := a.store.Users().GetByID(r.Context(), r.PathValue("id"))
	if errors.Is(err, store.ErrNotFound) {
		apiError(w, http.StatusNotFound, "no such user")
		return
	}
	if err != nil {
		a.fail(w, r, http.StatusInternalServerError, "get failed", err)
		return
	}
	payload := toUserPayload(u)
	// Enrichment: everything the backend knows about a user. Effective roles
	// come from the SAME resolver every
	// admin request already runs (direct + implication); locks are
	// the revocation rows the lock API writes; counts ride existing seams
	// plus CountByUser (idx_approvals_user). Best-effort sub-reads degrade
	// to absent, never to a failed detail read.
	detail := userDetailPayload{userPayload: payload}
	if roles, err := a.resolver.ResolveRoles(r.Context(), u.ID, time.Now()); err == nil {
		for _, ro := range roles {
			detail.EffectiveRoles = append(detail.EffectiveRoles, ro.Name)
		}
	}
	if detail.EffectiveRoles == nil {
		detail.EffectiveRoles = []string{}
	}
	detail.Locks = []lockPayload{}
	if locks, err := a.store.Revocations().ListByTarget(r.Context(), store.RevokeUser, u.ID); err == nil {
		for _, rv := range locks {
			detail.Locks = append(detail.Locks, lockPayload{
				Origin: rv.Origin, Reason: rv.Reason, CreatedAt: rv.CreatedAt})
		}
	}
	if ses, err := a.store.Sessions().ListByUser(r.Context(), u.ID); err == nil {
		detail.Counts.Sessions = len(ses)
		var lastSeen time.Time
		for _, se := range ses {
			if se.Status == store.SessionActive {
				detail.Counts.ActiveSessions++
			}
			if se.LastSeen.After(lastSeen) {
				lastSeen = se.LastSeen
			}
		}
		if !lastSeen.IsZero() {
			detail.LastSeen = &lastSeen
		}
	}
	if devs, err := a.store.Devices().ListByUser(r.Context(), u.ID); err == nil {
		detail.Counts.Devices = len(devs)
	}
	if phones, err := a.store.Approvers().ListDevices(r.Context(), u.ID); err == nil {
		detail.Counts.ApproverDevices = len(phones)
	}
	if n, err := a.store.Approvals().CountByUser(r.Context(), u.ID); err == nil {
		detail.Counts.Approvals = n
	}
	if userKind(u) == "nhi" {
		_, err := a.store.Settings().Get(r.Context(), nhiKeyPrefix+u.ID)
		reg := err == nil
		detail.NHIKeyRegistered = &reg
	}
	if n, err := a.store.Users().SponsoredCounts(r.Context(), []string{u.Username}); err == nil {
		detail.SponsoredCount = n[u.Username]
	}
	writeJSON(w, http.StatusOK, detail)
}

// userDetailPayload is the enriched single-user read. Locks empty
// = not locked; LastSeen absent = never checked in (a pointer, same as the
// list lane: a zero time.Time survives omitempty and would reach the drawer
// as year 1).
type userDetailPayload struct {
	userPayload
	EffectiveRoles []string      `json:"effective_roles"`
	Locks          []lockPayload `json:"locks"`
	LastSeen       *time.Time    `json:"last_seen,omitempty"`
	Counts         struct {
		Sessions        int `json:"sessions"`
		ActiveSessions  int `json:"active_sessions"`
		Devices         int `json:"devices"`
		ApproverDevices int `json:"approver_devices"`
		Approvals       int `json:"approvals"`
	} `json:"counts"`
	NHIKeyRegistered *bool `json:"nhi_key_registered,omitempty"` // kind=nhi only
	SponsoredCount   int   `json:"sponsored_count"`
}

type lockPayload struct {
	Origin    string    `json:"origin"`
	Reason    string    `json:"reason"`
	CreatedAt time.Time `json:"created_at"`
}

// handleRevocationsList serves GET /v1/admin/revocations: the rows the
// lock/kill lanes write.
func (a *App) handleRevocationsList(w http.ResponseWriter, r *http.Request) {
	rows, err := a.store.Revocations().List(r.Context())
	if err != nil {
		a.fail(w, r, http.StatusInternalServerError, "list revocations failed", err)
		return
	}
	type revocationPayload struct {
		ID        string    `json:"id"`
		Kind      string    `json:"kind"`
		TargetID  string    `json:"target_id"`
		Reason    string    `json:"reason"`
		Origin    string    `json:"origin"`
		CreatedAt time.Time `json:"created_at"`
	}
	out := make([]revocationPayload, len(rows))
	for i, rv := range rows {
		out[i] = revocationPayload{ID: rv.ID, Kind: rv.Kind, TargetID: rv.TargetID,
			Reason: rv.Reason, Origin: rv.Origin, CreatedAt: rv.CreatedAt}
	}
	writeJSON(w, http.StatusOK, out)
}

func (a *App) handleUsersUpdate(w http.ResponseWriter, r *http.Request) {
	u, err := a.store.Users().GetByID(r.Context(), r.PathValue("id"))
	if errors.Is(err, store.ErrNotFound) {
		apiError(w, http.StatusNotFound, "no such user")
		return
	}
	if err != nil {
		a.fail(w, r, http.StatusInternalServerError, "get failed", err)
		return
	}
	before := u
	var req struct {
		Email    *string `json:"email"`
		Display  *string `json:"display"`
		Title    *string `json:"title"`
		Status   *string `json:"status"`
		Password *string `json:"password"`
		// Identity typology: pointer = "field present"; an empty
		// string clears (back to unclassified). Enterprise deployments master
		// these over SCIM; this lane is the standalone-admin equivalent.
		UserType   *string `json:"user_type"`
		AgencyMode *string `json:"agency_mode"`
		Sponsor    *string `json:"sponsor"`
		SwarmID    *string `json:"swarm_id"`
		Ephemeral  *bool   `json:"ephemeral"`
		// EntraAgentID sets the Entra agent identity of an agent, and an
		// empty string clears it.
		EntraAgentID *string `json:"entra_agent_id"`
	}
	err = decodeAdmin(r.Body, &req)
	if refusedUnknownField(w, err) {
		return
	}
	if err != nil {
		apiError(w, http.StatusBadRequest, "malformed JSON body")
		return
	}
	if u.Username == BreakGlassUsername && req.Status != nil {
		apiError(w, http.StatusForbidden, "the break-glass admin cannot be deactivated (lockout guarantee)")
		return
	}
	if req.Email != nil {
		u.Email = *req.Email
	}
	if req.Display != nil {
		u.Display = *req.Display
	}
	if req.Title != nil {
		u.Title = *req.Title
	}
	if req.UserType != nil {
		v := strings.ToLower(strings.TrimSpace(*req.UserType))
		if v != "" && v != store.UserTypeHuman && v != store.UserTypeAgent && v != store.UserTypeService {
			apiError(w, http.StatusBadRequest, "user_type must be human, agent, service, or empty to clear")
			return
		}
		u.UserType = v
	}
	if req.AgencyMode != nil {
		v := strings.ToLower(strings.TrimSpace(*req.AgencyMode))
		if v != "" && v != store.AgencyInteractive && v != store.AgencySupervised && v != store.AgencyAutonomous {
			apiError(w, http.StatusBadRequest, "agency_mode must be interactive, supervised, autonomous, or empty to clear")
			return
		}
		u.AgencyMode = v
	}
	if req.Sponsor != nil {
		u.Sponsor = *req.Sponsor
	}
	if req.SwarmID != nil {
		u.SwarmID = *req.SwarmID
	}
	if req.Ephemeral != nil {
		u.Ephemeral = *req.Ephemeral
	}
	if req.EntraAgentID != nil {
		id := ""
		if strings.TrimSpace(*req.EntraAgentID) != "" {
			var ok bool
			if id, ok = store.NormalizeEntraAgentID(*req.EntraAgentID); !ok {
				apiError(w, http.StatusBadRequest, fmt.Sprintf(entraNotGUIDMsg, *req.EntraAgentID))
				return
			}
		}
		u.EntraAgentID = id
	}
	deactivated, reactivated := false, false
	if req.Status != nil {
		if *req.Status != store.UserActive && *req.Status != store.UserDisabled {
			apiError(w, http.StatusBadRequest, "status must be active or disabled")
			return
		}
		deactivated = u.Status == store.UserActive && *req.Status == store.UserDisabled
		reactivated = u.Status == store.UserDisabled && *req.Status == store.UserActive
		u.Status = *req.Status
	}
	if req.Password != nil {
		if *req.Password == "" {
			apiError(w, http.StatusBadRequest, "password must not be empty")
			return
		}
		// Judged on the row as the request leaves it, so a request that also
		// makes the user an agent is refused too.
		if !personUser(u) {
			apiError(w, http.StatusBadRequest, fmt.Sprintf(nonPersonPasswordPatchMsg, u.Username, userTypeWord(u), u.Username))
			return
		}
		hash, err := authn.HashPassword(*req.Password)
		if err != nil {
			a.fail(w, r, http.StatusInternalServerError, "hash failed", err)
			return
		}
		u.PasswordHash = hash
	}
	// An AI agent or a service account stores no password, so a write that
	// leaves the user one clears a stored hash, and the record names it.
	cleared := !personUser(u) && u.PasswordHash != ""
	if cleared {
		u.PasswordHash = ""
	}
	if a.refuseEntraAgentID(w, r, before, u) {
		return
	}
	// Only the fields the request changes against the row it read are
	// written, the set the record names, so a concurrent PATCH keeps what it
	// wrote to any other field, and a value sent unchanged never restores one.
	f := store.ChangedUserFields(before, u)
	if req.Password != nil {
		f.PasswordHash = &u.PasswordHash
	}
	// A disable commits with its revocation row and event, so a refused
	// write leaves the user active and the same request disables it again.
	var revoke *store.UserRevocation
	if deactivated {
		revoke, err = a.userRevocation(r.Context(), u.ID, "user disabled by admin", store.RevocationOriginAdmin)
	}
	var updated store.User
	if err == nil {
		updated, err = a.updateUserFields(r.Context(), u.ID, f, revoke)
	}
	// An id another user took after the check above is refused here.
	if errors.Is(err, store.ErrConflict) && u.EntraAgentID != "" && a.refuseEntraAgentID(w, r, before, u) {
		return
	}
	if err != nil {
		switch {
		case errors.Is(err, store.ErrInvalidData):
			// The request carries a value the database refuses, so the same
			// request fails the same way, and this server refuses no one.
			apiError(w, http.StatusBadRequest, fmt.Sprintf(userValueRefusedMsg, u.Username))
		case deactivated && !errors.Is(err, store.ErrNotFound):
			a.failUserRevoke(w, r, u.ID, u.Username, "disable", userNotDisabledMsg, err)
		default:
			a.fail(w, r, http.StatusInternalServerError, "update failed", err)
		}
		return
	}
	// The request applied to the read row, never the read-back updated, is
	// compared, so a concurrent write to this user never enters the record.
	a.auditUserUpdate(r.Context(), updated, userChanges(before, u, req.Password != nil || cleared))
	if deactivated {
		a.userRevokedCtx(r.Context(), updated.ID, "user disabled by admin", store.RevocationOriginAdmin)
	}
	if reactivated {
		a.allowUserCtx(r.Context(), updated.ID)
	}
	if before.EntraAgentID != u.EntraAgentID {
		a.entraAgentIDChanged(r.Context(), updated.ID)
	}
	a.identityChanged(r, "straza.identity.updated", updated.ID)
	writeJSON(w, http.StatusOK, toUserPayload(updated))
}

// handleUserUnlock lifts EVERY revocation lane for the user, the explicit
// Straza action that admin/external locks require. users.status is untouched:
// a user the IdM disabled stays disabled (their next checkin 401s on status),
// but the lock lane is clear.
func (a *App) handleUserUnlock(w http.ResponseWriter, r *http.Request) {
	u, err := a.store.Users().GetByID(r.Context(), r.PathValue("id"))
	if errors.Is(err, store.ErrNotFound) {
		apiError(w, http.StatusNotFound, "no such user")
		return
	}
	if err != nil {
		a.fail(w, r, http.StatusInternalServerError, "get failed", err)
		return
	}
	a.allowUserCtx(r.Context(), u.ID)
	a.emitEvent(r, "straza.audit.admin", map[string]any{"action": "user.unlock", "target": u.ID})
	writeJSON(w, http.StatusOK, map[string]any{"id": u.ID, "locked": false})
}
