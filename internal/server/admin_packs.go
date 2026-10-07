package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/strazahq/straza/internal/authn"
	"github.com/strazahq/straza/internal/drafts"
	"github.com/strazahq/straza/internal/store"
)

type packAdminPayload struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Version string `json:"version"`
	Content string `json:"content"`
	// Bindings: which roles receive this pack (0.58.0; empty = bound to
	// nothing, so no session ever sees it). The edge has no row id
	// (composite PK), so each binding's `id` IS the role id: the natural
	// key within this pack's scope, and what the unbind DELETE consumes.
	Bindings []packBindingPayload `json:"bindings"`
}

type packBindingPayload struct {
	ID       string `json:"id"`
	RoleID   string `json:"role_id"`
	RoleName string `json:"role_name"`
}

func (a *App) handlePacksList(w http.ResponseWriter, r *http.Request) {
	packs, err := a.store.Packs().List(r.Context())
	if err != nil {
		a.fail(w, r, http.StatusInternalServerError, "list packs failed", err)
		return
	}
	// One query for every pack's role edges; a failure refuses the list
	// rather than rendering every pack as bound to nothing.
	bindings, err := a.store.Packs().ListBindings(r.Context())
	if err != nil {
		a.fail(w, r, http.StatusInternalServerError, "list packs failed", err)
		return
	}
	byPack := map[string][]packBindingPayload{}
	for _, b := range bindings {
		byPack[b.PackID] = append(byPack[b.PackID], packBindingPayload{ID: b.RoleID, RoleID: b.RoleID, RoleName: b.RoleName})
	}
	out := make([]packAdminPayload, len(packs))
	for i, p := range packs {
		out[i] = packAdminPayload{ID: p.ID, Name: p.Name, Version: p.Version, Content: p.Content,
			Bindings: byPack[p.ID]}
		if out[i].Bindings == nil {
			out[i].Bindings = []packBindingPayload{}
		}
	}
	writeJSON(w, http.StatusOK, out)
}

func (a *App) handlePacksCreate(w http.ResponseWriter, r *http.Request) {
	var req packAdminPayload
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Name == "" {
		apiError(w, http.StatusBadRequest, "name is required")
		return
	}
	p, err := a.store.Packs().Create(r.Context(), store.KnowledgePack{
		Name: req.Name, Version: req.Version, Content: req.Content,
		Checksum: authn.Checksum(req.Content),
	})
	if errors.Is(err, store.ErrConflict) {
		apiError(w, http.StatusConflict, "pack already exists")
		return
	}
	if err != nil {
		a.fail(w, r, http.StatusInternalServerError, "create failed", err)
		return
	}
	writeJSON(w, http.StatusCreated, packAdminPayload{ID: p.ID, Name: p.Name, Version: p.Version, Content: p.Content})
}

func (a *App) handlePackBind(w http.ResponseWriter, r *http.Request) {
	var req struct {
		RoleID string `json:"role_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.RoleID == "" {
		apiError(w, http.StatusBadRequest, "role_id is required")
		return
	}
	if role, err := a.store.Roles().GetByID(r.Context(), req.RoleID); err == nil {
		// Packs are agent session context; the control plane never carries
		// them, and neither does an approver role
		// (revision 15: decide authority only). Unknown roles fall through
		// to Bind's own error.
		if role.Plane == store.RolePlaneControl {
			apiError(w, http.StatusBadRequest, "Straza role: it governs Straza itself and cannot carry knowledge packs")
			return
		}
		if role.Kind == store.RoleKindApprover {
			apiError(w, http.StatusBadRequest, "approver role: it decides approval requests and cannot carry knowledge packs")
			return
		}
	}
	if err := a.store.Packs().Bind(r.Context(), req.RoleID, r.PathValue("id")); err != nil {
		apiError(w, http.StatusBadRequest, "could not bind the knowledge pack (unknown role or pack?)")
		return
	}
	writeJSON(w, http.StatusCreated, map[string]string{"pack_id": r.PathValue("id"), "role_id": req.RoleID})
}

// handlePackUnbind is DELETE /v1/admin/packs/{id}/bindings/{bindingId}
// (0.58.0). The binding id is the role id (composite-PK edge, see
// packAdminPayload). Like Bind, no event and no refresh: packs are read at
// checkin, not compiled into the request-path snapshot.
func (a *App) handlePackUnbind(w http.ResponseWriter, r *http.Request) {
	if err := a.store.Packs().Unbind(r.Context(), r.PathValue("bindingId"), r.PathValue("id")); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			apiError(w, http.StatusNotFound, "this role has no such knowledge pack")
			return
		}
		a.fail(w, r, http.StatusInternalServerError, "unbind failed", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}

// handlePackDelete is DELETE /v1/admin/packs/{id}. A pack a role is still
// bound to is refused with the roles and the fix, because the delete would
// otherwise cascade the bindings and change what those roles' sessions
// receive. The store affects no row while a binding exists, so a bind that
// lands between the bindings read and the delete answers zero rows, and the
// bindings are read again to tell that case from a missing pack. Like bind
// and unbind, no event and no refresh: packs are read at checkin.
func (a *App) handlePackDelete(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if a.refusePackDeleteWhileBound(w, r, id) {
		return
	}
	err := a.store.Packs().Delete(r.Context(), id)
	if errors.Is(err, store.ErrNotFound) {
		if a.refusePackDeleteWhileBound(w, r, id) {
			return
		}
		apiError(w, http.StatusNotFound, "no such knowledge pack")
		return
	}
	if err != nil {
		a.fail(w, r, http.StatusInternalServerError, "delete failed", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}

// refusePackDeleteWhileBound answers 409 and reports true when a role is
// bound to the pack, or 500 when the bindings or the caller's grants cannot
// be read. Role names are identity objects, so they reach only a caller who
// may read the identity area; any other caller reads how many roles hold
// the pack. It reports false, having written nothing, when no role is bound.
func (a *App) refusePackDeleteWhileBound(w http.ResponseWriter, r *http.Request, packID string) bool {
	bindings, err := a.store.Packs().ListBindings(r.Context())
	if err != nil {
		a.fail(w, r, http.StatusInternalServerError, "delete failed", err)
		return true
	}
	var roles []string
	for _, b := range bindings {
		if b.PackID == packID {
			roles = append(roles, b.RoleName)
		}
	}
	if len(roles) == 0 {
		return false
	}
	act, _ := actorFrom(r.Context())
	caller, err := a.proposerCaller(r.Context(), drafts.Principal{UserID: act.ID, Username: act.Name, Via: act.Via})
	if err != nil {
		a.fail(w, r, http.StatusInternalServerError, "delete failed", err)
		return true
	}
	apiError(w, http.StatusConflict, packBoundRefusal(roles, caller.p.root || caller.p.scope.Grants["identity:read"]))
	return true
}

// packBoundRefusal words the 409 of a pack delete: the roles by name for a
// caller who may read them, else their count and the grant that lists them.
func packBoundRefusal(roles []string, named bool) string {
	switch {
	case !named && len(roles) == 1:
		return "the knowledge pack is still bound to a role, and deleting it would change what that role's sessions receive. Unbind it from the role first, then delete it. Listing the roles needs the identity:read grant"
	case !named:
		return fmt.Sprintf("the knowledge pack is still bound to %d roles, and deleting it would change what their sessions receive. Unbind it from each role first, then delete it. Listing the roles needs the identity:read grant", len(roles))
	case len(roles) == 1:
		return fmt.Sprintf("the knowledge pack is still bound to the role %s, and deleting it would change what that role's sessions receive. Unbind it from the role first, then delete it", roles[0])
	default:
		last := len(roles) - 1
		return fmt.Sprintf("the knowledge pack is still bound to the roles %s and %s, and deleting it would change what their sessions receive. Unbind it from each role first, then delete it",
			strings.Join(roles[:last], ", "), roles[last])
	}
}
