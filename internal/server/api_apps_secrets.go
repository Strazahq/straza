package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/strazahq/straza/internal/drafts"
	"github.com/strazahq/straza/internal/secrets"
	"github.com/strazahq/straza/internal/store"
)

// secretPayload is one stored static secret on the admin surface: never the
// value, only a fingerprint of it.
type secretPayload struct {
	ID          string `json:"id"`
	App         string `json:"app,omitempty"`
	Scope       string `json:"scope"`
	Role        string `json:"role"`
	Kind        string `json:"kind,omitempty"`
	Fingerprint string `json:"fingerprint"`
	SetAt       string `json:"set_at,omitempty"`
}

// handleAppSecretSet stores a static secret for an app: the server's own
// secret when the body names no role, a role's override otherwise. The
// response never echoes the value; the row stores ciphertext only,
// because credentials never reach the agent.
func (a *App) handleAppSecretSet(w http.ResponseWriter, r *http.Request) {
	row, err := a.appByRef(r)
	if err != nil {
		apiError(w, http.StatusNotFound, "unknown server")
		return
	}
	var req struct {
		Role  string `json:"role"`
		Value string `json:"value"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Value == "" {
		apiError(w, http.StatusBadRequest, "value is required")
		return
	}
	app, err := withManifest(worldApp(row, ""))
	if err != nil {
		a.fail(w, r, http.StatusInternalServerError, "stored manifest unreadable (reinstall the server)", err)
		return
	}
	// The rules of the server's own secret read no role, so they run first,
	// and the role table is read only for a role's override they let pass.
	var world drafts.World
	ref := drafts.SecretRefusal(world, app, "")
	if ref == nil && req.Role != "" {
		if world.Roles, err = a.readRoles(r.Context()); err != nil {
			a.fail(w, r, http.StatusInternalServerError, "role lookup failed", err)
			return
		}
		ref = drafts.SecretRefusal(world, app, req.Role)
	}
	if ref != nil {
		refuse(w, ref)
		return
	}
	roleID, scope := "", store.CredScopeApp
	if req.Role != "" {
		roleID, scope = world.Roles[req.Role].ID, store.CredScopeRole
	}
	cred, err := a.broker.Set(r.Context(), row.ID, roleID, req.Value)
	if err != nil {
		a.fail(w, r, http.StatusInternalServerError, "store secret failed", err)
		return
	}
	// A newly-credentialed app may have been waiting to start; a running one
	// gets a fresh probe under the new secret.
	a.manager.SecretUpdated(r.Context(), row.ID)
	a.emitEvent(r, "straza.audit.admin", map[string]any{
		"action": "apps.secret.set", "app": row.Name, "role": req.Role, "scope": scope,
	})
	// Convergence hint: other pods reload the broker cache so the
	// rotated secret reaches their upstream calls too.
	a.emitEvent(r, "straza.apps.updated", map[string]any{"change": "secret", "app": row.Name})
	writeJSON(w, http.StatusCreated, secretPayload{
		ID: cred.ID, App: row.Name, Scope: scope, Role: req.Role, Fingerprint: secrets.Fingerprint(req.Value),
	})
}

// handleAppSecretsList lists an app's static secrets, the server's own row
// first, with fingerprints and never the values. OAuth grants are per user
// and stay on /v1/connect.
func (a *App) handleAppSecretsList(w http.ResponseWriter, r *http.Request) {
	row, err := a.appByRef(r)
	if err != nil {
		apiError(w, http.StatusNotFound, "unknown server")
		return
	}
	statics, err := a.broker.Statics(r.Context(), row.ID)
	if err != nil {
		a.fail(w, r, http.StatusInternalServerError, "list secrets failed", err)
		return
	}
	out := make([]secretPayload, 0, len(statics))
	for _, s := range statics {
		p := secretPayload{ID: s.ID, Scope: s.Scope, Kind: store.CredStatic, Fingerprint: s.Fingerprint, SetAt: s.SetAt.UTC().Format(time.RFC3339)}
		if s.RoleID != "" {
			if role, err := a.store.Roles().GetByID(r.Context(), s.RoleID); err == nil {
				p.Role = role.Name
			}
		}
		out = append(out, p)
	}
	writeJSON(w, http.StatusOK, out)
}

// handleAppSecretDelete removes the server's own secret.
func (a *App) handleAppSecretDelete(w http.ResponseWriter, r *http.Request) {
	a.removeSecret(w, r, "", "")
}

// handleAppRoleSecretDelete removes one role's override.
func (a *App) handleAppRoleSecretDelete(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("role")
	role, err := a.store.Roles().GetByName(r.Context(), name)
	if err != nil {
		apiError(w, http.StatusNotFound, drafts.UnknownRoleMessage(name))
		return
	}
	a.removeSecret(w, r, role.ID, role.Name)
}

// removeSecret deletes one static row (the app row when roleID is empty),
// re-probes the app under what remains, and records the removal with the
// actor.
func (a *App) removeSecret(w http.ResponseWriter, r *http.Request, roleID, roleName string) {
	row, err := a.appByRef(r)
	if err != nil {
		apiError(w, http.StatusNotFound, "unknown server")
		return
	}
	cred, err := a.broker.Remove(r.Context(), row.ID, roleID)
	if errors.Is(err, store.ErrNotFound) {
		which := "the server's own secret"
		if roleName != "" {
			which = "role " + roleName
		}
		apiError(w, http.StatusNotFound, fmt.Sprintf("no secret is stored for the MCP server %s (%s)", row.Name, which))
		return
	}
	if err != nil {
		a.fail(w, r, http.StatusInternalServerError, "remove secret failed", err)
		return
	}
	a.manager.SecretUpdated(r.Context(), row.ID)
	a.emitEvent(r, "straza.audit.admin", map[string]any{
		"action": "apps.secret.remove", "app": row.Name, "role": roleName,
	})
	a.emitEvent(r, "straza.apps.updated", map[string]any{"change": "secret", "app": row.Name})
	writeJSON(w, http.StatusOK, map[string]string{"id": cred.ID, "status": "deleted"})
}
