package scim

// Wire-groups (spec/scim-profile revision 12, the unified role model):
// /Groups renders EXPORTED Straza roles in the protocol's Group costume,
// because that is the access dialect IdMs speak. Membership IS role
// assignment (a members add creates the direct scim-origin assignment, a
// remove deletes it), and SCIM is the single writer for role holding:
// console grants of exported roles are drift the IdM reconciles away.
// Creating, renaming, and deleting access stays Straza's act: POST and
// DELETE answer 501, displayName is readOnly. Every role renders here,
// control plane included: the IdM masters membership everywhere, so
// straza-admin membership is IdM-writable like any other role's.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"

	"github.com/strazahq/straza/internal/store"
)

// groupDocument is the accepted PUT wire shape. displayName and externalId
// are tolerated and IGNORED (Okta sends the full document, displayName
// included, on every membership update; erroring on it would break
// membership entirely; the response re-renders server truth).
type groupDocument struct {
	Schemas    []string      `json:"schemas"`
	ExternalID string        `json:"externalId"`
	Display    string        `json:"displayName"`
	Members    []groupMember `json:"members"`
}

type groupMember struct {
	Value   string `json:"value"`
	Display string `json:"display,omitempty"`
	Ref     string `json:"$ref,omitempty"`
}

// strazaGroupURNLower is the Group extension URN as normalizePath renders it.
const strazaGroupURNLower = "urn:straza:params:scim:schemas:extension:2.0:group"

// refusal501 is the doctrine detail on the refused lifecycle verbs.
const refusal501 = "roles are born in Straza, so the IdM cannot create or delete one over SCIM. Create the role in Straza, import it as a group, and assign membership from the IdM"

// groupProjectionPath reports whether a normalized (lowercased) PATCH path
// targets the read-only §4.1 access projection: the URN, its qualified
// sub-attributes, or the bare attribute names IdMs may send.
func groupProjectionPath(p string) bool {
	p = strings.TrimPrefix(p, strazaGroupURNLower+":")
	switch p {
	case strazaGroupURNLower, "role", "rolekind", "plane", "description", "apps", "tools", "policies", "administers", "server":
		return true
	}
	return false
}

// protectedUserID resolves the break-glass account's id ("" = none). The
// account is invisible to this surface: never a member, never assignable.
func (s *Server) protectedUserID(ctx context.Context) string {
	if s.deps.ProtectedUsername == "" {
		return ""
	}
	u, err := s.deps.Store.Users().GetByUsername(ctx, s.deps.ProtectedUsername)
	if err != nil {
		return ""
	}
	return u.ID
}

// groupResource renders one exported role as a wire-group. `members` is
// the role's DIRECT user assignments: implication-closure holders are
// deliberately absent (they are not removable through this surface, and
// rendering them would make every IdM desired-vs-current diff
// unconvergeable); what a role implies stays visible in the projection.
func (s *Server) groupResource(ctx context.Context, role store.Role) map[string]any {
	res := map[string]any{
		"schemas":     []string{SchemaGroup},
		"id":          role.ID,
		"displayName": role.Name,
		"meta": map[string]any{
			"resourceType": "Group",
			"created":      role.CreatedAt.UTC(),
			"lastModified": role.UpdatedAt.UTC(),
			"location":     "/scim/v2/Groups/" + role.ID,
		},
	}
	if asg, err := s.deps.Store.Roles().AssignmentsByRole(ctx, role.ID); err == nil {
		protected := s.protectedUserID(ctx)
		ids := make([]string, 0, len(asg))
		for _, a := range asg {
			if a.SubjectKind == store.SubjectUser && a.SubjectID != protected {
				ids = append(ids, a.SubjectID)
			}
		}
		// One bulk lookup for display names instead of a query per member:
		// an IdM sync renders every group on the page, so per-member queries
		// would be N+1.
		names := map[string]string{}
		if users, err := s.deps.Store.Users().GetByIDs(ctx, ids); err == nil {
			for _, u := range users {
				names[u.ID] = u.Username
			}
		}
		members := make([]map[string]any, 0, len(ids))
		for _, id := range ids {
			m := map[string]any{"value": id, "$ref": "/scim/v2/Users/" + id}
			if n, ok := names[id]; ok {
				m["display"] = n
			}
			members = append(members, m)
		}
		res["members"] = members
	}
	// §4.1: the read-only access projection: evidence of what membership
	// grants, never a write surface.
	if s.deps.RoleProjection != nil {
		if block := s.deps.RoleProjection(ctx, role.ID); block != nil {
			res["schemas"] = []string{SchemaGroup, SchemaStrazaGroup}
			res[SchemaStrazaGroup] = block
		}
	}
	return res
}

func (s *Server) handleGroupsList(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	if raw := r.URL.Query().Get("filter"); raw != "" {
		// externalId retired with revision 12 (nothing IdM-side creates
		// the object that would carry one): displayName is the only
		// filterable attribute.
		attr, value, err := parseFilter(raw, "displayName")
		if err != nil || attr != "displayname" {
			writeError(w, http.StatusBadRequest, "invalidFilter", "only `displayName eq \"...\"` is supported on Groups, because Straza looks roles up by name only. Filter on displayName, or list without a filter")
			return
		}
		role, err := s.deps.Store.Roles().GetByName(ctx, value)
		if errors.Is(err, store.ErrNotFound) {
			listResponse(w, nil, 0, 1)
			return
		}
		if err != nil {
			writeError(w, http.StatusInternalServerError, "", "lookup failed")
			return
		}
		listResponse(w, []map[string]any{s.groupResource(ctx, role)}, 1, 1)
		return
	}

	roles, err := s.deps.Store.Roles().List(ctx)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "", "list failed")
		return
	}
	// Paging before rendering also bounds the member-resolution queries to
	// the page (groupResource resolves each member's display name).
	listPage(w, r, roles, func(role store.Role) map[string]any { return s.groupResource(ctx, role) })
}

// handleGroupCreate and handleGroupDelete refuse with 501 (RFC 7644 §3.12:
// the server does not support the operation; 403 would read as an
// authorization problem and IdM runbooks map it to broken credentials).
func (s *Server) handleGroupCreate(w http.ResponseWriter, _ *http.Request) {
	writeError(w, http.StatusNotImplemented, "", refusal501)
}

func (s *Server) handleGroupDelete(w http.ResponseWriter, _ *http.Request) {
	writeError(w, http.StatusNotImplemented, "", refusal501)
}

// roleByID resolves a role or answers 404.
func (s *Server) roleByID(w http.ResponseWriter, r *http.Request) (store.Role, bool) {
	role, err := s.deps.Store.Roles().GetByID(r.Context(), r.PathValue("id"))
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "", "no such group")
		return store.Role{}, false
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "", "lookup failed")
		return store.Role{}, false
	}
	return role, true
}

func (s *Server) handleGroupGet(w http.ResponseWriter, r *http.Request) {
	if role, ok := s.roleByID(w, r); ok {
		writeSCIM(w, http.StatusOK, s.groupResource(r.Context(), role))
	}
}

// handleGroupReplace is PUT: a TOLERANT full-membership replace. The name
// fields in the body are ignored (see groupDocument); members is the full
// desired set, absent = empty (RFC PUT-as-replace). It is planned and
// written like a PATCH members replace, so it lands whole or not at all.
func (s *Server) handleGroupReplace(w http.ResponseWriter, r *http.Request) {
	role, ok := s.roleByID(w, r)
	if !ok {
		return
	}
	var doc groupDocument
	if err := json.NewDecoder(r.Body).Decode(&doc); err != nil {
		writeError(w, http.StatusBadRequest, "invalidValue", "malformed group document")
		return
	}
	p, ok := s.startPlan(w, r, role)
	if !ok {
		return
	}
	if err := s.planReplace(r.Context(), p, doc.Members); err != nil {
		writeError(w, http.StatusBadRequest, "invalidValue", err.Error())
		return
	}
	s.commitPlan(w, r, p)
}

// handleGroupPatch plans every operation against the group's holders in
// memory and stores the result in one write (RFC 7644 §3.5.2: a PATCH is
// atomic), so a refused operation leaves the group exactly as it was.
func (s *Server) handleGroupPatch(w http.ResponseWriter, r *http.Request) {
	role, ok := s.roleByID(w, r)
	if !ok {
		return
	}
	ops, err := decodePatch(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalidValue", err.Error())
		return
	}
	p, ok := s.startPlan(w, r, role)
	if !ok {
		return
	}
	for _, op := range ops {
		if err := s.planGroupOp(r.Context(), p, op); err != nil {
			var me mutabilityError
			if errors.As(err, &me) {
				writeError(w, http.StatusBadRequest, "mutability", me.Error())
				return
			}
			writeError(w, http.StatusBadRequest, "invalidValue", err.Error())
			return
		}
	}
	s.commitPlan(w, r, p)
}

// membershipPlan is one Group write worked out in memory before anything
// is stored. It starts from the role's direct user holders and records, in
// operation order, every add and remove that changed the planned set.
type membershipPlan struct {
	role      store.Role
	protected string                          // the break-glass user id, never a member
	start     map[string]store.RoleAssignment // holder id to the row it starts with
	holds     map[string]bool                 // the planned holders
	order     []string                        // holders in start order, then each later add
	listed    map[string]bool                 // who is in order
	steps     []membershipStep
}

// membershipStep is one planned add or remove of a user.
type membershipStep struct {
	userID string
	action string // MembershipAssign or MembershipUnassign
}

// planFor reads the role's direct user holders once and opens a plan on
// them. The break-glass account is left out: it is never a member here.
func (s *Server) planFor(ctx context.Context, role store.Role) (*membershipPlan, error) {
	asg, err := s.deps.Store.Roles().AssignmentsByRole(ctx, role.ID)
	if err != nil {
		return nil, err
	}
	p := &membershipPlan{
		role: role, protected: s.protectedUserID(ctx),
		start: map[string]store.RoleAssignment{}, holds: map[string]bool{}, listed: map[string]bool{},
	}
	for _, a := range asg {
		if a.SubjectKind != store.SubjectUser || a.SubjectID == p.protected {
			continue
		}
		p.start[a.SubjectID] = a
		p.holds[a.SubjectID] = true
		p.listed[a.SubjectID] = true
		p.order = append(p.order, a.SubjectID)
	}
	return p, nil
}

// startPlan is planFor for a handler: when the holders cannot be read it
// answers 500 and nothing is planned.
func (s *Server) startPlan(w http.ResponseWriter, r *http.Request, role store.Role) (*membershipPlan, bool) {
	p, err := s.planFor(r.Context(), role)
	if err != nil {
		s.deps.Log.Warn("scim group: read the members", "group", role.Name, "err", err)
		writeError(w, http.StatusInternalServerError, "", "the members of group "+role.Name+" could not be read, so nothing changed. Retry the request")
		return nil, false
	}
	return p, true
}

func (p *membershipPlan) add(userID string) {
	if p.holds[userID] {
		return
	}
	p.holds[userID] = true
	if !p.listed[userID] {
		p.listed[userID] = true
		p.order = append(p.order, userID)
	}
	p.steps = append(p.steps, membershipStep{userID: userID, action: MembershipAssign})
}

// remove is idempotent, like the group-membership remove it replaces:
// removing a non-holder, an empty id or the break-glass account is a no-op.
func (p *membershipPlan) remove(userID string) {
	if !p.holds[userID] {
		return
	}
	p.holds[userID] = false
	p.steps = append(p.steps, membershipStep{userID: userID, action: MembershipUnassign})
}

// checkMember refuses a member id that names no user the IdM may assign:
// an empty id, an unknown one, or the break-glass account.
func (s *Server) checkMember(ctx context.Context, p *membershipPlan, userID string) error {
	if userID == "" {
		return fmt.Errorf("member value (user id) is required")
	}
	if _, err := s.deps.Store.Users().GetByID(ctx, userID); err != nil || userID == p.protected {
		return fmt.Errorf("member %q is not a known user id", userID)
	}
	return nil
}

// planGroupOp plans one PATCH operation. It writes nothing: every refusal
// returns before the plan is stored, so the request fails whole.
func (s *Server) planGroupOp(ctx context.Context, p *membershipPlan, op patchOp) error {
	// The §4.1 access projection is read-only over SCIM (mirrors the §3.1
	// lock block): grants change through roles and bindings in Straza, and
	// membership stays the only assignable fact on this wire.
	if groupProjectionPath(op.path) {
		return mutabilityError{"the Straza Group extension is a read-only projection of what the role grants, so SCIM cannot change it. Change grants through Straza roles and bindings"}
	}
	// members[value eq "<id>"] removal form.
	if m := memberFilterRe.FindStringSubmatch(op.path); m != nil {
		if op.op != "remove" {
			return fmt.Errorf("value filters are only supported with op remove")
		}
		p.remove(m[1])
		return nil
	}

	switch op.path {
	case "":
		obj, ok := op.value.(map[string]any)
		if !ok {
			return fmt.Errorf("no-path PATCH value must be an object")
		}
		// Keys are planned in a fixed order, so the same request always
		// meets the same refusal first.
		keys := make([]string, 0, len(obj))
		for k := range obj {
			keys = append(keys, k)
		}
		sort.Slice(keys, func(i, j int) bool {
			if a, b := strings.ToLower(keys[i]), strings.ToLower(keys[j]); a != b {
				return a < b
			}
			return keys[i] < keys[j]
		})
		for _, k := range keys {
			if err := s.planGroupOp(ctx, p, patchOp{op: op.op, path: strings.ToLower(k), value: obj[k]}); err != nil {
				return err
			}
		}
		return nil
	case "displayname":
		// Revision 12: renaming access is Straza's act; the IdM can never
		// rename a role over the wire (and policy references can never
		// dangle against a drifting name).
		return mutabilityError{"displayName is readOnly, because roles are born and renamed in Straza and policies reference them by name. Rename the role in Straza and the group follows"}
	case "members":
		members, err := decodeMembers(op.value)
		if err != nil {
			return err
		}
		switch op.op {
		case "add":
			for _, m := range members {
				if err := s.checkMember(ctx, p, m.Value); err != nil {
					return err
				}
				p.add(m.Value)
			}
		case "replace":
			return s.planReplace(ctx, p, members)
		case "remove":
			if len(members) == 0 {
				return s.planReplace(ctx, p, nil)
			}
			for _, m := range members {
				p.remove(m.Value)
			}
		}
		return nil
	default:
		return fmt.Errorf("PATCH path %q is not mapped on Groups: members is the only attribute an identity manager can change over SCIM. Send a members operation, or change the role in Straza", op.path)
	}
}

// planReplace plans a members replace. Every member is checked before the
// plan changes, so one bad id in an IdM full-document PUT refuses the
// request instead of stripping holders. Holders the list leaves out are
// removed in holder order, then new members are added in list order, so
// the audit records chain in the order the IdM wrote them.
func (s *Server) planReplace(ctx context.Context, p *membershipPlan, members []groupMember) error {
	desired := make(map[string]bool, len(members))
	for _, m := range members {
		if err := s.checkMember(ctx, p, m.Value); err != nil {
			return err
		}
		desired[m.Value] = true
	}
	for _, id := range append([]string(nil), p.order...) {
		if p.holds[id] && !desired[id] {
			p.remove(id)
		}
	}
	for _, m := range members {
		p.add(m.Value)
	}
	return nil
}

// commitPlan stores the plan and reports it: the identity events and one
// membership change per row the write really changed. A failed write
// answers 500 and reports nothing, because nothing changed.
func (s *Server) commitPlan(w http.ResponseWriter, r *http.Request, p *membershipPlan) {
	ctx := r.Context()
	changed, err := s.writePlan(ctx, p)
	if err != nil {
		s.deps.Log.Warn("scim group: write the membership change", "group", p.role.Name, "err", err)
		writeError(w, http.StatusInternalServerError, "", "the membership change to group "+p.role.Name+" was not applied, because writing it failed, so nothing changed. Retry the request")
		return
	}
	s.emitMembership(ctx, p.role.ID, changed)
	writeSCIM(w, http.StatusOK, s.groupResource(ctx, p.role))
}

// writePlan stores the plan's net change in one write and returns the
// membership changes that really landed, in the order the operations
// produced them. A user's last step decides: it counts only when the user
// ends the request holding differently than it began, so an add and a
// remove of the same member cancel out. A row a concurrent write already
// changed is skipped by the store and reported by nobody here.
func (s *Server) writePlan(ctx context.Context, p *membershipPlan) ([]MembershipChange, error) {
	last := make(map[string]int, len(p.steps))
	for i, st := range p.steps {
		last[st.userID] = i
	}
	var adds []store.RoleAssignment
	var removeIDs []string
	var net []membershipStep
	for i, st := range p.steps {
		if last[st.userID] != i {
			continue
		}
		row, held := p.start[st.userID]
		switch {
		case p.holds[st.userID] && !held:
			adds = append(adds, store.RoleAssignment{SubjectKind: store.SubjectUser, SubjectID: st.userID, Origin: store.OriginSCIM})
		case !p.holds[st.userID] && held:
			removeIDs = append(removeIDs, row.ID)
		default:
			continue
		}
		net = append(net, st)
	}
	if len(net) == 0 {
		return nil, nil
	}
	added, removed, err := s.deps.Store.Roles().ApplyMembership(ctx, p.role.ID, adds, removeIDs)
	if err != nil {
		return nil, err
	}
	landed := make(map[string]store.RoleAssignment, len(added)+len(removed))
	for _, a := range added {
		landed[MembershipAssign+"/"+a.SubjectID] = a
	}
	for _, a := range removed {
		landed[MembershipUnassign+"/"+a.SubjectID] = a
	}
	var changed []MembershipChange
	for _, st := range net {
		a, ok := landed[st.action+"/"+st.userID]
		if !ok {
			continue
		}
		changed = append(changed, MembershipChange{
			AssignmentID: a.ID, RoleID: p.role.ID, UserID: st.userID, Origin: a.Origin, Action: st.action,
		})
	}
	return changed, nil
}

// emitMembership publishes the change to both consumers: the role id (the
// wire-group render: LiveSync re-reads members) and each affected user id
// once (the account shadow's read-only `groups` reflection, and the
// resolver bump that reaches the PDP). It then reports every started or
// ended assignment through the audit seam, one call per row in write
// order, so a write that changed nothing chains no record.
func (s *Server) emitMembership(ctx context.Context, roleID string, changed []MembershipChange) {
	s.deps.IdentityChanged(ctx, "straza.identity.updated", roleID)
	seen := map[string]bool{}
	for _, c := range changed {
		if seen[c.UserID] {
			continue
		}
		seen[c.UserID] = true
		s.deps.IdentityChanged(ctx, "straza.identity.updated", c.UserID)
	}
	if s.deps.MembershipChanged == nil {
		return
	}
	for _, c := range changed {
		s.deps.MembershipChanged(ctx, c)
	}
}

func decodeMembers(v any) ([]groupMember, error) {
	if v == nil {
		return nil, nil
	}
	raw, err := json.Marshal(v)
	if err != nil {
		return nil, fmt.Errorf("members value is not JSON")
	}
	var members []groupMember
	if err := json.Unmarshal(raw, &members); err != nil {
		var one groupMember
		if err2 := json.Unmarshal(raw, &one); err2 != nil {
			return nil, fmt.Errorf("members must be a list of {value}")
		}
		members = []groupMember{one}
	}
	return members, nil
}
