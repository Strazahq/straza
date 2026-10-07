package server

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/strazahq/straza/internal/drafts"
	"github.com/strazahq/straza/internal/store"
)

// bindingPayload is the admin API tool-binding representation.
type bindingPayload struct {
	ID    string   `json:"id"`
	App   string   `json:"app"`
	Role  string   `json:"role"`
	Tools []string `json:"tools"`
}

// handleAppBindingCreate exposes a subset of an app's tools to a role
// (no binding row = the app is invisible to that role), published as the
// role's live document with the row.
func (a *App) handleAppBindingCreate(w http.ResponseWriter, r *http.Request) {
	row, err := a.appByRef(r)
	if err != nil {
		apiError(w, http.StatusNotFound, "unknown server")
		return
	}
	var req struct {
		Role  string   `json:"role"`
		Tools []string `json:"tools"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Role == "" {
		apiError(w, http.StatusBadRequest, "role is required")
		return
	}
	if len(req.Tools) == 0 {
		req.Tools = []string{"*"}
	}
	st := draftStanding(standingFrom(r.Context()))
	res, ok := a.publishOne(w, r, directRoute{
		prepare: func(live drafts.World, _ drafts.Principal) (directChange, bool) {
			if _, ok := live.Apps[row.Name]; !ok {
				apiError(w, http.StatusNotFound, "unknown server")
				return directChange{}, false
			}
			if ref := drafts.AccessCreateRefusal(live, st, req.Role, row.Name, req.Tools); ref != nil {
				refuseAccessRow(w, live, req.Role, ref)
				return directChange{}, false
			}
			doc, _ := drafts.RoleDocOf(live, req.Role)
			doc.Spec.Bindings = []drafts.RoleBinding{{App: row.Name, Tools: onceEach(req.Tools)}}
			return a.roleChange(w, r, doc, "the access row could not be created")
		},
		bodyStatus: http.StatusBadRequest,
		failed:     func(error) string { return "the access row could not be created" },
	})
	if !ok {
		return
	}
	id := ""
	if added := res.Item.BindingsAdded; len(added) > 0 {
		id = added[0].ID
	}
	writeJSON(w, http.StatusCreated, bindingPayload{ID: id, App: row.Name, Role: req.Role, Tools: req.Tools})
}

// refuseAccessRow answers a refused access row. A row the role has on this
// server already is named by id, because the console routes that 409 into
// editing the row's tools.
func refuseAccessRow(w http.ResponseWriter, world drafts.World, role string, ref *drafts.Refusal) {
	if ref.Kind == drafts.RefusalExists {
		writeJSON(w, http.StatusConflict, map[string]string{"error": ref.Sentence, "binding_id": world.Access[role].ID})
		return
	}
	refuse(w, ref)
}

// bindingByID resolves one binding row by id, ErrNotFound when none.
func (a *App) bindingByID(ctx context.Context, id string) (store.ToolBinding, error) {
	rows, err := a.store.ToolBindings().List(ctx)
	if err != nil {
		return store.ToolBinding{}, err
	}
	for _, b := range rows {
		if b.ID == id {
			return b, nil
		}
	}
	return store.ToolBinding{}, store.ErrNotFound
}

func (a *App) handleBindingsList(w http.ResponseWriter, r *http.Request) {
	rows, err := a.store.ToolBindings().List(r.Context())
	if err != nil {
		a.fail(w, r, http.StatusInternalServerError, "the access rows could not be listed", err)
		return
	}
	// A server admin sees the bindings of their own servers and no other.
	st := standingFrom(r.Context())
	out := make([]bindingPayload, 0, len(rows))
	for _, b := range rows {
		if !st.Full && !st.Apps[b.AppID] {
			continue
		}
		p := bindingPayload{ID: b.ID}
		_ = json.Unmarshal([]byte(b.ToolMatcher), &p.Tools)
		if role, err := a.store.Roles().GetByID(r.Context(), b.RoleID); err == nil {
			p.Role = role.Name
		}
		if app, err := a.store.Apps().GetByID(r.Context(), b.AppID); err == nil {
			p.App = app.Name
		}
		out = append(out, p)
	}
	writeJSON(w, http.StatusOK, out)
}

// handleBindingDelete removes one access row, published as its role's live
// document without the row.
func (a *App) handleBindingDelete(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if _, err := a.bindingByID(r.Context(), id); err != nil {
		apiError(w, http.StatusNotFound, "no access row with that id")
		return
	}
	st := draftStanding(standingFrom(r.Context()))
	if _, ok := a.publishOne(w, r, directRoute{
		prepare: func(live drafts.World, _ drafts.Principal) (directChange, bool) {
			role, server := "", ""
			for name, acc := range live.Access {
				if acc.ID == id {
					role, server = name, acc.Server
				}
			}
			if role == "" {
				apiError(w, http.StatusNotFound, "no access row with that id")
				return directChange{}, false
			}
			if server != "" {
				if ref := drafts.AccessRemoveRefusal(live, st, role, server); ref != nil {
					refuse(w, ref)
					return directChange{}, false
				}
			}
			doc, _ := drafts.RoleDocOf(live, role)
			doc.Spec.Bindings = nil
			return a.roleChange(w, r, doc, "role lookup failed")
		},
		bodyStatus: http.StatusBadRequest,
		failed:     func(error) string { return "role lookup failed" },
	}); !ok {
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"id": id, "status": "deleted"})
}
