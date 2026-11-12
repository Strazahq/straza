package server

import (
	"errors"
	"net/http"
	"slices"

	"github.com/strazahq/straza/internal/drafts"
	"github.com/strazahq/straza/internal/store"
)

// handleImplicationCreate is POST /v1/admin/roles/{id}/implications,
// published as the source role's live document with the implied role
// added. Both ids must name roles, and an edge that exists
// answers 400.
func (a *App) handleImplicationCreate(w http.ResponseWriter, r *http.Request) {
	roleID := r.PathValue("id")
	var req struct {
		ImpliesRoleID string `json:"implies_role_id"`
	}
	err := decodeAdmin(r.Body, &req)
	if refusedUnknownField(w, err) {
		return
	}
	if err != nil || req.ImpliesRoleID == "" {
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
