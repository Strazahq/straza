package scim

// The write half of the /Users surface: PUT, PATCH, DELETE, the activation
// cascade they share, and the create-conflict revive. users.go keeps
// list/get/create and resource rendering.

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/strazahq/straza/internal/store"
)

// reviveDeactivated turns a create that conflicts with a DEACTIVATED
// SCIM-origin row of the same userName into an in-place revive. SCIM DELETE
// is deliberately a deactivate (handleUserDelete: the kill-switch keeps
// the row), so without this an IdM's delete-then-reprovision cycle (a rehire,
// or a provisioning retry after an interrupted run, as midPoint does) would
// answer 409 on the userName forever. The row keeps its ID so audit history
// stays attached to one identity, the incoming document overwrites every
// mapped attribute, and the local password hash is wiped: deprovisioning must
// not resurrect stale credentials. Fail-closed edges: an ACTIVE row keeps the
// 409 (a live username is never stolen), a non-SCIM origin keeps the 409 (a
// local account cannot be captured through the provisioning lane), and an
// externalId held by another row still surfaces ErrConflict from Update.
func (s *Server) reviveDeactivated(ctx context.Context, u store.User) (store.User, bool) {
	existing, err := s.deps.Store.Users().GetByUsername(ctx, u.Username)
	if err != nil || existing.Status != store.UserDisabled || existing.Origin != store.OriginSCIM {
		return store.User{}, false
	}
	u.ID = existing.ID
	if u.Attrs == "" {
		u.Attrs = "{}"
	}
	revived, err := s.deps.Store.Users().Update(ctx, u)
	if err != nil {
		return store.User{}, false
	}
	if revived.Status == store.UserActive {
		s.deps.Reactivate(ctx, revived.ID)
	}
	return revived, true
}

// handleUserReplace is PUT: full replace of the mapped attributes.
func (s *Server) handleUserReplace(w http.ResponseWriter, r *http.Request) {
	u, ok := s.userByID(w, r)
	if !ok {
		return
	}
	before := u
	var doc userDocument
	if err := json.NewDecoder(r.Body).Decode(&doc); err != nil || doc.UserName == "" {
		writeError(w, http.StatusBadRequest, "invalidValue", "userName is required")
		return
	}
	active, err := parseActive(doc.Active)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalidValue", err.Error())
		return
	}

	u.Username = doc.UserName
	u.ExternalID = doc.ExternalID
	u.Display = doc.displayName()
	u.Title = doc.Title
	u.Email = doc.email()
	// Typology on PUT: present values set, absent values stay (§3.2: an IdM
	// that does not map the extension must not clobber classification on
	// every recon; clearing goes through PATCH remove).
	if err := applyTypologyDoc(&u, doc); err != nil {
		writeError(w, http.StatusBadRequest, "invalidValue", err.Error())
		return
	}
	updated, err := s.deps.Store.Users().Update(r.Context(), u)
	if errors.Is(err, store.ErrConflict) {
		writeError(w, http.StatusConflict, "uniqueness", "userName already exists")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "", "update failed")
		return
	}
	updated, err = s.applyActive(r.Context(), updated, active)
	if err != nil {
		s.userUpdated(r.Context(), before, u)
		writeError(w, http.StatusInternalServerError, "", "status update failed")
		return
	}
	u.Status = activeStatus(u.Status, active)
	s.deps.IdentityChanged(r.Context(), "straza.identity.updated", updated.ID)
	s.userUpdated(r.Context(), before, u)
	writeSCIM(w, http.StatusOK, s.userResourceCtx(r.Context(), updated))
}

func (s *Server) handleUserPatch(w http.ResponseWriter, r *http.Request) {
	u, ok := s.userByID(w, r)
	if !ok {
		return
	}
	before := u
	ops, err := decodePatch(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalidValue", err.Error())
		return
	}

	activeTarget := u.Status == store.UserActive
	for _, op := range ops {
		if err := applyUserOp(&u, &activeTarget, op); err != nil {
			var me mutabilityError
			if errors.As(err, &me) {
				writeError(w, http.StatusBadRequest, "mutability", err.Error())
				return
			}
			writeError(w, http.StatusBadRequest, "invalidValue", err.Error())
			return
		}
	}

	updated, err := s.deps.Store.Users().Update(r.Context(), u)
	if errors.Is(err, store.ErrConflict) {
		writeError(w, http.StatusConflict, "uniqueness", "userName already exists")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "", "update failed")
		return
	}
	updated, err = s.applyActive(r.Context(), updated, activeTarget)
	if err != nil {
		s.userUpdated(r.Context(), before, u)
		writeError(w, http.StatusInternalServerError, "", "status update failed")
		return
	}
	u.Status = activeStatus(u.Status, activeTarget)
	s.deps.IdentityChanged(r.Context(), "straza.identity.updated", updated.ID)
	s.userUpdated(r.Context(), before, u)
	writeSCIM(w, http.StatusOK, s.userResourceCtx(r.Context(), updated))
}

// handleUserDelete deactivates (spec/scim-profile §1: SCIM never hard-deletes
// Straza identities).
func (s *Server) handleUserDelete(w http.ResponseWriter, r *http.Request) {
	u, ok := s.userByID(w, r)
	if !ok {
		return
	}
	if _, err := s.applyActive(r.Context(), u, false); err != nil {
		// A 5xx makes the IdP retry the deprovision; a 204 here would leave
		// an active user the IdP believes is gone.
		writeError(w, http.StatusInternalServerError, "", "deactivation failed")
		return
	}
	s.deps.IdentityChanged(r.Context(), "straza.identity.deactivated", u.ID)
	written := u
	written.Status = activeStatus(u.Status, false)
	s.userUpdated(r.Context(), u, written)
	w.WriteHeader(http.StatusNoContent)
}

// userCreated reports a create, or the revive of a deactivated row, through
// Deps.UserWritten.
func (s *Server) userCreated(ctx context.Context, u store.User) {
	if s.deps.UserWritten != nil {
		s.deps.UserWritten(ctx, UserCreated, u, nil)
	}
}

// userUpdated reports a write through Deps.UserWritten with the fields that
// differ between before, the row the handler read, and written, the row it
// wrote, never the row read back, so a concurrent write to the same user
// never enters this report. A write that changed nothing reports nothing.
// A handler whose status write failed after its field write passes the
// stored status, so the fields it stored are still reported.
func (s *Server) userUpdated(ctx context.Context, before, written store.User) {
	changed := changedFields(before, written)
	if len(changed) == 0 || s.deps.UserWritten == nil {
		return
	}
	s.deps.UserWritten(ctx, UserUpdated, written, changed)
}

// changedFields answers the names of the fields that differ between before
// and after, in one fixed order. The names are the admin API's user fields,
// the names the admin PATCH records, so one reader reads both lanes. It
// never answers a value.
func changedFields(before, after store.User) []string {
	var changed []string
	for _, f := range []struct {
		name    string
		differs bool
	}{
		{"username", before.Username != after.Username},
		{"external_id", before.ExternalID != after.ExternalID},
		{"email", before.Email != after.Email},
		{"display", before.Display != after.Display},
		{"title", before.Title != after.Title},
		{"status", before.Status != after.Status},
		{"user_type", before.UserType != after.UserType},
		{"agency_mode", before.AgencyMode != after.AgencyMode},
		{"sponsor", before.Sponsor != after.Sponsor},
		{"swarm_id", before.SwarmID != after.SwarmID},
		{"ephemeral", before.Ephemeral != after.Ephemeral},
	} {
		if f.differs {
			changed = append(changed, f.name)
		}
	}
	return changed
}

// activeStatus answers the status applyActive writes for the requested
// active state: the current status when it already matches, else active or
// disabled.
func activeStatus(status string, active bool) string {
	if (status == store.UserActive) == active {
		return status
	}
	if active {
		return store.UserActive
	}
	return store.UserDisabled
}

// applyActive reconciles the stored status with the requested active state,
// running the kill-switch cascade on deactivation. A store failure is
// returned, never swallowed: reporting success on a failed deactivation would
// make the IdP mark the user deprovisioned and stop retrying (fail open).
func (s *Server) applyActive(ctx context.Context, u store.User, active bool) (store.User, error) {
	status := activeStatus(u.Status, active)
	if status == u.Status {
		return u, nil
	}
	u.Status = status
	updated, err := s.deps.Store.Users().Update(ctx, u)
	if err != nil {
		s.deps.Log.Error("scim: status update failed", "user", u.ID, "err", err)
		return u, err
	}
	if active {
		s.deps.Reactivate(ctx, u.ID)
	} else {
		s.deps.Deactivate(ctx, u.ID, "deactivated via SCIM")
	}
	return updated, nil
}
