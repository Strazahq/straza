package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/strazahq/straza/internal/drafts"
	"github.com/strazahq/straza/internal/identity"
	"github.com/strazahq/straza/internal/policy"
	"github.com/strazahq/straza/internal/store"
)

type rolePayload struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	// Kind on the wire is business|application|approver|straza: "straza"
	// spells the control plane (capabilities in Straza itself: console
	// access, self enrollment); "approver"
	// (revision 15) is decide authority only, on the access plane. Storage
	// keeps plane as its own column; wireRoleKind is the
	// single mapping seam.
	Kind string `json:"kind"`
	// Assignment ROWS holding this role (windows included): the roles
	// list's Assigned column, grouped server-side so the
	// console never reads the full assignments list to count.
	AssignedCount int `json:"assigned_count"`
	// HolderCount is the number of subjects holding the role at read time
	// through any path: assignment rows in force, folded through the
	// implication closure, each subject once. Off the list it stays 0.
	HolderCount int `json:"holder_count"`
	// Implies names the roles this role implies directly, sorted; the list
	// alone fills it.
	Implies []string `json:"implies,omitempty"`
	// Areas is a straza role's console grant list from admin.roleAreas as
	// area:verb, or the one word full for the root role; the list alone
	// fills it, and a straza role the config does not map carries none.
	Areas []string `json:"areas,omitempty"`
	// DeciderIn names the active policy sets naming an approver role in
	// approve.roles; the list alone fills it.
	DeciderIn []string `json:"decider_in,omitempty"`
	// Server names the server that owns the role and Tools is the explicit
	// tool list of its one binding to that server; both are absent on a
	// global role.
	Server string   `json:"server,omitempty"`
	Tools  []string `json:"tools,omitempty"`
}

// holderCounts folds the assignment pairs in force through the implication
// closure: a subject holding a role holds every role it implies, and each
// subject counts once per held role.
func holderCounts(pairs []store.AssignmentPair, imps []store.RoleImplication) map[string]int {
	adj := map[string][]string{}
	for _, imp := range imps {
		adj[imp.RoleID] = append(adj[imp.RoleID], imp.ImpliesRoleID)
	}
	reach := map[string]map[string]bool{}
	held := map[string]map[string]struct{}{}
	for _, p := range pairs {
		rs, ok := reach[p.RoleID]
		if !ok {
			rs = identity.Closure(map[string]bool{p.RoleID: true}, adj)
			reach[p.RoleID] = rs
		}
		for id := range rs {
			m := held[id]
			if m == nil {
				m = map[string]struct{}{}
				held[id] = m
			}
			m[p.SubjectID] = struct{}{}
		}
	}
	out := make(map[string]int, len(held))
	for id, m := range held {
		out[id] = len(m)
	}
	return out
}

// roleAreas words a straza role's console grants: full for the root role,
// the sorted area:verb grants of a mapped role, nothing for the rest.
func (a *App) roleAreas(role store.Role) []string {
	if role.Plane != store.RolePlaneControl {
		return nil
	}
	if role.Name == AdminRole {
		return []string{"full"}
	}
	if role.Name == MCPAdminRole {
		return []string{"apps:read", "apps:write"}
	}
	sc, ok := a.roleAreaScopes[role.Name]
	if !ok {
		return nil
	}
	out := make([]string, 0, len(sc.Grants))
	for g := range sc.Grants {
		out = append(out, g)
	}
	sort.Strings(out)
	return out
}

func (a *App) handleRolesList(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	roles, err := a.rolesForStanding(ctx, standingFrom(ctx))
	if err != nil {
		a.fail(w, r, http.StatusInternalServerError, "list roles failed", err)
		return
	}
	apps, err := a.store.Apps().List(ctx)
	if err != nil {
		a.fail(w, r, http.StatusInternalServerError, "list roles failed", err)
		return
	}
	bindings, err := a.store.ToolBindings().List(ctx)
	if err != nil {
		a.fail(w, r, http.StatusInternalServerError, "list roles failed", err)
		return
	}
	appNames := make(map[string]string, len(apps))
	for _, row := range apps {
		appNames[row.ID] = row.Name
	}
	tools := ownedToolsByRole(roles, bindings)
	counts, err := a.store.Roles().AssignmentCountsByRole(ctx)
	if err != nil {
		a.fail(w, r, http.StatusInternalServerError, "list roles failed", err)
		return
	}
	imps, err := a.store.Roles().ListImplications(ctx)
	if err != nil {
		a.fail(w, r, http.StatusInternalServerError, "list roles failed", err)
		return
	}
	pairs, err := a.store.Roles().AssignmentPairsInForce(ctx, time.Now())
	if err != nil {
		a.fail(w, r, http.StatusInternalServerError, "list roles failed", err)
		return
	}
	holders := holderCounts(pairs, imps)
	names := make(map[string]string, len(roles))
	for _, role := range roles {
		names[role.ID] = role.Name
	}
	implies := map[string][]string{}
	for _, imp := range imps {
		if name, ok := names[imp.ImpliesRoleID]; ok {
			implies[imp.RoleID] = append(implies[imp.RoleID], name)
		}
	}
	out := make([]rolePayload, len(roles))
	for i, role := range roles {
		sort.Strings(implies[role.ID])
		row := rolePayload{ID: role.ID, Name: role.Name, Description: role.Description,
			Kind: wireRoleKind(role), AssignedCount: counts[role.ID], HolderCount: holders[role.ID],
			Implies: implies[role.ID], Areas: a.roleAreas(role),
			Server: appNames[role.OwnerAppID], Tools: tools[role.ID]}
		if row.Kind == store.RoleKindApprover {
			pools, err := a.activeApprovePools(ctx, role.Name)
			if err != nil {
				a.fail(w, r, http.StatusInternalServerError, "list roles failed", err)
				return
			}
			row.DeciderIn = deciderSets(pools)
		}
		out[i] = row
	}
	writeJSON(w, http.StatusOK, out)
}

// roleExistsMsg refuses a create whose name another role already has.
func roleExistsMsg(name string) string {
	return drafts.RoleExists(name).Sentence
}

// handleRolesCreate is POST /v1/admin/roles: the role the body names,
// published as a one-item draft. A global role takes the kind
// sent, business when none is, and a role a server owns is an application
// role with its one access row on that server.
func (a *App) handleRolesCreate(w http.ResponseWriter, r *http.Request) {
	var req roleCreateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Name == "" {
		apiError(w, http.StatusBadRequest, "name is required")
		return
	}
	doc := drafts.RoleDoc{APIVersion: drafts.RoleAPIVersion, Kind: string(drafts.KindRole), Metadata: drafts.RoleDocMeta{Name: req.Name},
		Spec: drafts.RoleDocSpec{Kind: req.Kind, Description: req.Description}}
	switch {
	case req.Server != "":
		doc.Spec.Kind, doc.Spec.Server = drafts.RoleKindApplication, req.Server
		doc.Spec.Bindings = []drafts.RoleBinding{{App: req.Server, Tools: onceEach(req.Tools)}}
	case req.Kind == "":
		doc.Spec.Kind = drafts.RoleKindBusiness
	}
	res, ok := a.publishOne(w, r, directRoute{
		prepare: func(live drafts.World, _ drafts.Principal) (directChange, bool) {
			spec := drafts.RoleSpec{Name: req.Name, Kind: req.Kind, Server: req.Server, Tools: req.Tools}
			if ref := drafts.RoleCreateRefusal(live, draftStanding(standingFrom(r.Context())), spec); ref != nil {
				refuse(w, ref)
				return directChange{}, false
			}
			return a.roleChange(w, r, doc, "create failed")
		},
		bodyStatus: http.StatusBadRequest,
		failed:     func(error) string { return "create failed" },
	})
	if !ok {
		return
	}
	out := rolePayload{ID: res.Item.ID, Name: req.Name, Description: req.Description, Kind: doc.Spec.Kind}
	if req.Server != "" {
		out.Server, out.Tools = req.Server, req.Tools
	}
	writeJSON(w, http.StatusCreated, out)
}

// onceEach is tools sorted, each tool once, as a Role document lists them.
// The routes stored a repeated tool before drafts, and the document parser
// refuses one, so a route passes each tool once and answers the list as
// sent.
func onceEach(tools []string) []string {
	return slices.Compact(slices.Sorted(slices.Values(tools)))
}

// roleChange is the Role put of doc as a direct route's change, and
// answers the route's 500 sentence failed itself when doc cannot be
// written, which a document of strings never meets.
func (a *App) roleChange(w http.ResponseWriter, r *http.Request, doc drafts.RoleDoc, failed string) (directChange, bool) {
	text, err := doc.Marshal()
	if err != nil {
		a.fail(w, r, http.StatusInternalServerError, failed, err)
		return directChange{}, false
	}
	return directChange{Item: drafts.Item{Kind: drafts.KindRole, Name: doc.Metadata.Name, Op: drafts.OpPut, Doc: string(text)}}, true
}

// liveRoleDoc is the live document of the role with the id, and false,
// with the request answered 404 in the words notFound, when no role has it.
func liveRoleDoc(w http.ResponseWriter, live drafts.World, id, notFound string) (drafts.RoleDoc, bool) {
	name, ok := roleNameOf(live, id)
	if !ok {
		apiError(w, http.StatusNotFound, notFound)
		return drafts.RoleDoc{}, false
	}
	doc, _ := drafts.RoleDocOf(live, name)
	return doc, true
}

// handleRolesUpdate is PATCH /v1/admin/roles/{id} (0.58.0): description and
// kind only, published as the live Role document with the new description.
// Name is deliberately immutable here: role names are policy selectors and
// IGA correlation keys, so a rename is a design act, not a field edit.
func (a *App) handleRolesUpdate(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	_, err := a.store.Roles().GetByID(r.Context(), id)
	if errors.Is(err, store.ErrNotFound) {
		apiError(w, http.StatusNotFound, "no such role")
		return
	}
	if err != nil {
		a.fail(w, r, http.StatusInternalServerError, "get failed", err)
		return
	}
	var req struct {
		Description *string `json:"description"`
		Kind        *string `json:"kind"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || (req.Description == nil && req.Kind == nil) {
		apiError(w, http.StatusBadRequest, "description or kind is required")
		return
	}
	st := draftStanding(standingFrom(r.Context()))
	if _, ok := a.publishOne(w, r, directRoute{
		prepare: func(live drafts.World, _ drafts.Principal) (directChange, bool) {
			doc, ok := liveRoleDoc(w, live, id, "no such role")
			if !ok {
				return directChange{}, false
			}
			// An explicit empty kind is a 400, not a silent keep: PATCH absent
			// means keep, PATCH present means set, and "" is not in the enum.
			if req.Kind != nil {
				if ref := drafts.RoleKindChangeRefusal(live.Roles[doc.Metadata.Name], st, *req.Kind); ref != nil {
					refuse(w, ref)
					return directChange{}, false
				}
			}
			if req.Description != nil {
				doc.Spec.Description = *req.Description
			}
			return a.roleChange(w, r, doc, "update failed")
		},
		bodyStatus: http.StatusBadRequest,
		failed:     func(error) string { return "update failed" },
	}); !ok {
		return
	}
	updated, err := a.store.Roles().GetByID(r.Context(), id)
	if err != nil {
		a.fail(w, r, http.StatusInternalServerError, "update failed", err)
		return
	}
	counts, err := a.store.Roles().AssignmentCountsByRole(r.Context())
	if err != nil {
		a.fail(w, r, http.StatusInternalServerError, "update failed", err)
		return
	}
	server, tools := a.ownedRoleFields(r.Context(), updated)
	writeJSON(w, http.StatusOK, rolePayload{ID: updated.ID, Name: updated.Name,
		Description: updated.Description, Kind: wireRoleKind(updated), AssignedCount: counts[updated.ID],
		Server: server, Tools: tools})
}

// roleDeleted is the answer of a role's deletion: the status, and the
// names of the sets the publish turned off with the role.
type roleDeleted struct {
	Status  string   `json:"status"`
	SetsOff []string `json:"sets_off,omitempty"`
}

// handleRolesDelete is DELETE /v1/admin/roles/{id}, published as the
// role's removal, after the refusals in their order,
// with every live policy set that would match nobody without the role
// turned off in the same publish.
func (a *App) handleRolesDelete(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if _, err := a.store.Roles().GetByID(r.Context(), id); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			apiError(w, http.StatusNotFound, "no such role")
			return
		}
		a.fail(w, r, http.StatusInternalServerError, "role lookup failed", err)
		return
	}
	res, ok := a.publishOne(w, r, directRoute{
		prepare: func(live drafts.World, _ drafts.Principal) (directChange, bool) {
			name, ok := roleNameOf(live, id)
			if !ok {
				apiError(w, http.StatusNotFound, "no such role")
				return directChange{}, false
			}
			if !a.roleDeleteAllowed(w, r, id, name) {
				return directChange{}, false
			}
			ch := directChange{Item: drafts.Item{Kind: drafts.KindRole, Name: name, Op: drafts.OpRemove},
				Also: setsLeftMatchingNothing(live, name)}
			// A set turned off closes its saved edit in the same transaction
			// as activate's off does, and the reason says what the set
			// keeps while it is off.
			for _, it := range ch.Also {
				ch.Close = append(ch.Close, store.SlotClose{Slot: "policy:" + it.Name,
					Reason: it.Name + " was turned off with the role " + name + ", the only role it matched, and it keeps its published text while it is off"})
			}
			return ch, true
		},
		bodyStatus: http.StatusBadRequest,
		failed:     func(error) string { return "role lookup failed" },
	})
	if !ok {
		return
	}
	out := roleDeleted{Status: "deleted"}
	for _, o := range res.Outcome.Items {
		if o.Ref.Kind == string(drafts.KindPolicySet) && o.Op == string(drafts.OpOff) {
			out.SetsOff = append(out.SetsOff, o.Ref.Name)
		}
	}
	writeJSON(w, http.StatusOK, out)
}

// setsLeftMatchingNothing is an off item, with its live text, for every
// live policy set whose match.roles names role and no other live role, in
// name order. The match lists of a set combine with AND and the roles
// within a list with OR, so such a set matches nobody once role is gone,
// whatever its other lists say, and turning it off changes no one's
// access. A live set whose text does not parse is left as it is.
func setsLeftMatchingNothing(live drafts.World, role string) []drafts.Item {
	var out []drafts.Item
	for _, name := range slices.Sorted(maps.Keys(live.Policies)) {
		doc, err := policy.Parse([]byte(live.Policies[name].Text))
		if err != nil || !slices.Contains(doc.Spec.Match.Roles, role) {
			continue
		}
		if slices.ContainsFunc(doc.Spec.Match.Roles, func(r string) bool { _, known := live.Roles[r]; return known && r != role }) {
			continue
		}
		out = append(out, drafts.Item{Kind: drafts.KindPolicySet, Name: name, Op: drafts.OpOff, Doc: live.Policies[name].Text})
	}
	return out
}

// roleDeleteAllowed runs the refusals of a role's deletion in today's order
// and words for the role name with the id, and answers the request itself
// and reports false when one holds.
func (a *App) roleDeleteAllowed(w http.ResponseWriter, r *http.Request, id, name string) bool {
	// The reserved product namespace is refused at delete as well as at
	// create: straza-admin and the straza-enroll-* pair are boot-created and
	// carry console access and self-enrollment, so a delete would cascade
	// their assignments and take that away from every holder at once.
	if strings.HasPrefix(strings.ToLower(name), "straza-") {
		apiError(w, http.StatusConflict, fmt.Sprintf(
			"role %q comes with the product and cannot be deleted. To take someone's access away, remove their assignment instead",
			name))
		return false
	}
	// A role named in approve.roles
	// of an ACTIVE set is its deciders; deleting it would leave every record
	// that rule holds to expire unanswered, silently. Refuse with the set,
	// the rule and the fix. Drafts govern nothing and do not block. The read
	// is the admin plane's own role/policy tables, never a request path.
	naming, err := a.activeApprovePoolsNaming(r.Context(), name)
	if err != nil {
		a.fail(w, r, http.StatusInternalServerError, "policy lookup failed", err)
		return false
	}
	if len(naming) > 0 {
		apiError(w, http.StatusConflict, fmt.Sprintf(
			"role %q is named among the deciders of a live policy (%s): deleting it would leave those approvals to expire unanswered. Remove it from approve.roles and publish the set again, or turn the set off, then delete the role",
			name, strings.Join(naming, ", ")))
		return false
	}
	// A server's admin role is set once at mint and goes with the server:
	// deleting it on its own would leave the server with no admin role.
	named, err := a.store.Apps().ListByAdminRoles(r.Context(), []string{id})
	if err != nil {
		a.fail(w, r, http.StatusInternalServerError, "server lookup failed", err)
		return false
	}
	if len(named) > 0 {
		names := make([]string, len(named))
		for i, row := range named {
			names[i] = row.Name
		}
		apiError(w, http.StatusConflict, fmt.Sprintf(
			"role %q is the admin role of server %s and lives as long as the server does. Remove the server instead.",
			name, strings.Join(names, ", ")))
		return false
	}
	// A server admin's delete is refused while anyone holds the role,
	// directly or through a business role, because the holders were
	// assigned and certified on its meaning.
	if !standingFrom(r.Context()).Full {
		n, err := a.store.Roles().HolderCount(r.Context(), id)
		if err != nil {
			a.fail(w, r, http.StatusInternalServerError, "holder count failed", err)
			return false
		}
		if n > 0 {
			apiError(w, http.StatusConflict, fmt.Sprintf(
				"the role %s has %s. The identity manager removes them first, then delete it", name, drafts.HoldersPhrase(n)))
			return false
		}
	}
	return true
}

// handleImplicationCreate is POST /v1/admin/roles/{id}/implications,
// published as the source role's live document with the implied role
// added. Both ids must name roles, and an edge that exists
// answers 400.
func (a *App) handleImplicationCreate(w http.ResponseWriter, r *http.Request) {
	roleID := r.PathValue("id")
	var req struct {
		ImpliesRoleID string `json:"implies_role_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.ImpliesRoleID == "" {
		apiError(w, http.StatusBadRequest, "implies_role_id is required")
		return
	}
	if _, ok := a.publishOne(w, r, directRoute{
		prepare: func(live drafts.World, _ drafts.Principal) (directChange, bool) {
			// An id no role has is the empty name to the rules, which pass it
			// by every rule but the cycle rule, and two such ids close a cycle
			// only when they are one id. Two different ones skip the rules.
			from, fromKnown := roleNameOf(live, roleID)
			to, toKnown := roleNameOf(live, req.ImpliesRoleID)
			if fromKnown || toKnown || roleID == req.ImpliesRoleID {
				if ref := drafts.ImplicationRefusal(live, from, to); ref != nil {
					refuse(w, ref)
					return directChange{}, false
				}
			}
			for _, id := range []string{roleID, req.ImpliesRoleID} {
				if _, known := roleNameOf(live, id); !known {
					apiError(w, http.StatusBadRequest, "add implication failed: no role has the id "+id+". Read the ids with strazactl roles list.")
					return directChange{}, false
				}
			}
			if slices.Contains(live.Implies[from], to) {
				apiError(w, http.StatusBadRequest, "add implication failed: "+from+" implies "+to+" already.")
				return directChange{}, false
			}
			doc, _ := drafts.RoleDocOf(live, from)
			doc.Spec.Implies = append(doc.Spec.Implies, to)
			return a.roleChange(w, r, doc, "list implications failed")
		},
		bodyStatus: http.StatusBadRequest,
		failed:     func(error) string { return "list implications failed" },
	}); !ok {
		return
	}
	writeJSON(w, http.StatusCreated, map[string]string{"role_id": roleID, "implies_role_id": req.ImpliesRoleID})
}

// implicationPayload is one outgoing edge of a role. The edge has no row id
// (composite PK role_id+implies_role_id), so `id` IS the implied role's id:
// the natural key within this role's scope, and exactly what the DELETE
// path consumes.
type implicationPayload struct {
	ID          string `json:"id"`
	ImpliesID   string `json:"implies_id"`
	ImpliesName string `json:"implies_name"`
}

// handleImplicationsList is GET /v1/admin/roles/{id}/implications (0.58.0):
// the edges the resolver applies, until now write-only on the wire.
func (a *App) handleImplicationsList(w http.ResponseWriter, r *http.Request) {
	roleID := r.PathValue("id")
	if _, err := a.store.Roles().GetByID(r.Context(), roleID); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			apiError(w, http.StatusNotFound, "no such role")
			return
		}
		a.fail(w, r, http.StatusInternalServerError, "get failed", err)
		return
	}
	imps, err := a.store.Roles().ListImplications(r.Context())
	if err != nil {
		a.fail(w, r, http.StatusInternalServerError, "list implications failed", err)
		return
	}
	roles, err := a.store.Roles().List(r.Context())
	if err != nil {
		a.fail(w, r, http.StatusInternalServerError, "list implications failed", err)
		return
	}
	names := make(map[string]string, len(roles))
	for _, ro := range roles {
		names[ro.ID] = ro.Name
	}
	out := []implicationPayload{}
	for _, imp := range imps {
		if imp.RoleID != roleID {
			continue
		}
		out = append(out, implicationPayload{
			ID: imp.ImpliesRoleID, ImpliesID: imp.ImpliesRoleID, ImpliesName: names[imp.ImpliesRoleID]})
	}
	writeJSON(w, http.StatusOK, out)
}

// handleImplicationDelete removes one edge, published as the source
// role's live document without it. The publish bumps the
// resolver like the create does: a stale cache would keep granting the
// implied role until an unrelated write.
func (a *App) handleImplicationDelete(w http.ResponseWriter, r *http.Request) {
	roleID, impliesID := r.PathValue("id"), r.PathValue("implicationId")
	if _, ok := a.publishOne(w, r, directRoute{
		prepare: func(live drafts.World, _ drafts.Principal) (directChange, bool) {
			doc, ok := liveRoleDoc(w, live, roleID, "no such implication")
			if !ok {
				return directChange{}, false
			}
			to, known := roleNameOf(live, impliesID)
			at := slices.Index(doc.Spec.Implies, to)
			if !known || at < 0 {
				apiError(w, http.StatusNotFound, "no such implication")
				return directChange{}, false
			}
			doc.Spec.Implies = slices.Delete(doc.Spec.Implies, at, at+1)
			return a.roleChange(w, r, doc, "remove implication failed")
		},
		bodyStatus: http.StatusBadRequest,
		failed:     func(error) string { return "remove implication failed" },
	}); !ok {
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}
