package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"

	"github.com/strazahq/straza/internal/drafts"
	"github.com/strazahq/straza/internal/store"
)

// handleRoleExport is GET /v1/admin/roles/{id}/export (0.59.0).
func (a *App) handleRoleExport(w http.ResponseWriter, r *http.Request) {
	role, err := a.store.Roles().GetByID(r.Context(), r.PathValue("id"))
	if errors.Is(err, store.ErrNotFound) {
		apiError(w, http.StatusNotFound, "no such role")
		return
	}
	if err != nil {
		a.fail(w, r, http.StatusInternalServerError, "get failed", err)
		return
	}
	doc, err := a.buildRoleExport(r, role)
	if err != nil {
		a.fail(w, r, http.StatusInternalServerError, "export failed", err)
		return
	}
	out, err := doc.Marshal()
	if err != nil {
		a.fail(w, r, http.StatusInternalServerError, "export failed", err)
		return
	}
	w.Header().Set("Content-Type", "application/yaml")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", "role-"+role.Name+".yaml"))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(out)
}

// buildRoleExport reads the canonical document of role, the spec/objects
// face of a role that drafts.RoleDoc's one serializer writes for the export
// and for every draft alike.
func (a *App) buildRoleExport(r *http.Request, role store.Role) (drafts.RoleDoc, error) {
	ctx := r.Context()
	doc := drafts.RoleDoc{
		APIVersion: drafts.RoleAPIVersion,
		Kind:       string(drafts.KindRole),
		Metadata:   drafts.RoleDocMeta{Name: role.Name},
		Spec:       drafts.RoleDocSpec{Kind: wireRoleKind(role), Description: role.Description},
	}
	if role.OwnerAppID != "" {
		if row, err := a.store.Apps().GetByID(ctx, role.OwnerAppID); err == nil {
			doc.Spec.Server = row.Name
		}
	}

	roles, err := a.store.Roles().List(ctx)
	if err != nil {
		return doc, err
	}
	roleName := make(map[string]string, len(roles))
	for _, ro := range roles {
		roleName[ro.ID] = ro.Name
	}
	imps, err := a.store.Roles().ListImplications(ctx)
	if err != nil {
		return doc, err
	}
	for _, imp := range imps {
		if imp.RoleID == role.ID {
			doc.Spec.Implies = append(doc.Spec.Implies, roleName[imp.ImpliesRoleID])
		}
	}
	sort.Strings(doc.Spec.Implies)

	bindings, err := a.store.ToolBindings().List(ctx)
	if err != nil {
		return doc, err
	}
	for _, b := range bindings {
		if b.RoleID != role.ID {
			continue
		}
		app, err := a.store.Apps().GetByID(ctx, b.AppID)
		if err != nil {
			continue // a racing app delete drops the row, not the export
		}
		var tools []string
		_ = json.Unmarshal([]byte(b.ToolMatcher), &tools)
		sort.Strings(tools)
		doc.Spec.Bindings = append(doc.Spec.Bindings, drafts.RoleBinding{App: app.Name, Tools: tools})
	}
	sort.Slice(doc.Spec.Bindings, func(i, j int) bool { return doc.Spec.Bindings[i].App < doc.Spec.Bindings[j].App })

	packBindings, err := a.store.Packs().ListBindings(ctx)
	if err != nil {
		return doc, err
	}
	if len(packBindings) > 0 {
		packs, err := a.store.Packs().List(ctx)
		if err != nil {
			return doc, err
		}
		packName := make(map[string]string, len(packs))
		for _, p := range packs {
			packName[p.ID] = p.Name
		}
		for _, pb := range packBindings {
			if pb.RoleID == role.ID {
				doc.Spec.Packs = append(doc.Spec.Packs, packName[pb.PackID])
			}
		}
		sort.Strings(doc.Spec.Packs)
	}
	return doc, nil
}
