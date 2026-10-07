package server

import (
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/strazahq/straza/internal/store"
)

type assignmentPayload struct {
	ID          string     `json:"id"`
	SubjectKind string     `json:"subject_kind"`
	SubjectID   string     `json:"subject_id"`
	RoleID      string     `json:"role_id"`
	ValidFrom   *time.Time `json:"valid_from,omitempty"`
	ValidTo     *time.Time `json:"valid_to,omitempty"`
	// Origin is the lane that wrote the row, scim or admin, read from the
	// row and never taken from a request.
	Origin string `json:"origin"`
}

func (a *App) handleAssignmentsList(w http.ResponseWriter, r *http.Request) {
	var (
		asg []store.RoleAssignment
		err error
	)
	kind, subject := r.URL.Query().Get("subject_kind"), r.URL.Query().Get("subject_id")
	if kind != "" && subject != "" {
		asg, err = a.store.Roles().ListAssignments(r.Context(), kind, subject)
	} else {
		asg, err = a.store.Roles().ListAllAssignments(r.Context())
	}
	if err != nil {
		a.fail(w, r, http.StatusInternalServerError, "list assignments failed", err)
		return
	}
	out := make([]assignmentPayload, len(asg))
	for i, x := range asg {
		out[i] = assignmentPayload{ID: x.ID, SubjectKind: x.SubjectKind, SubjectID: x.SubjectID,
			RoleID: x.RoleID, ValidFrom: x.ValidFrom, ValidTo: x.ValidTo, Origin: x.Origin}
	}
	writeJSON(w, http.StatusOK, out)
}

func (a *App) handleAssignmentsCreate(w http.ResponseWriter, r *http.Request) {
	var req assignmentPayload
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil ||
		req.SubjectID == "" || req.RoleID == "" || req.SubjectKind != store.SubjectUser {
		apiError(w, http.StatusBadRequest, "subject_kind (user), subject_id and role_id are required (group subjects retired with the unified role model)")
		return
	}
	created, err := a.store.Roles().Assign(r.Context(), store.RoleAssignment{
		SubjectKind: req.SubjectKind, SubjectID: req.SubjectID, RoleID: req.RoleID,
		ValidFrom: req.ValidFrom, ValidTo: req.ValidTo,
	})
	if errors.Is(err, store.ErrConflict) {
		apiError(w, http.StatusConflict, "assignment already exists")
		return
	}
	if err != nil {
		apiError(w, http.StatusBadRequest, "assign failed (unknown role or subject?)")
		return
	}
	a.auditAssignment(r.Context(), actionRolesAssign, created, "")
	a.identityChanged(r, "straza.identity.updated", created.SubjectID)
	req.ID = created.ID
	req.Origin = created.Origin
	writeJSON(w, http.StatusCreated, req)
}

func (a *App) handleAssignmentsDelete(w http.ResponseWriter, r *http.Request) {
	// Resolve the SUBJECT before deleting: the identity event must name who
	// lost the role (an IGA liveSync consumer re-syncs the subject shadow;
	// the assignment row id resolves to nothing once deleted).
	as, err := a.store.Roles().Assignment(r.Context(), r.PathValue("id"))
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			apiError(w, http.StatusNotFound, "no such assignment")
			return
		}
		a.fail(w, r, http.StatusInternalServerError, "unassign failed", err)
		return
	}
	if as.SubjectKind == store.SubjectUser && a.isBreakGlass(r.Context(), as.SubjectID) {
		apiError(w, http.StatusForbidden, "the break-glass admin cannot be de-roled (lockout guarantee)")
		return
	}
	if err := a.store.Roles().Unassign(r.Context(), as.ID); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			apiError(w, http.StatusNotFound, "no such assignment")
			return
		}
		a.fail(w, r, http.StatusInternalServerError, "unassign failed", err)
		return
	}
	a.auditAssignment(r.Context(), actionRolesUnassign, as, "")
	a.identityChanged(r, "straza.identity.updated", as.SubjectID)
	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}
