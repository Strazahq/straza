package server

import (
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
	// CreatedAt is when the row was written, read from the row and never
	// taken from a request. A repeated identity manager write keeps it.
	CreatedAt time.Time `json:"created_at"`
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
			RoleID: x.RoleID, ValidFrom: x.ValidFrom, ValidTo: x.ValidTo, Origin: x.Origin, CreatedAt: x.CreatedAt}
	}
	writeJSON(w, http.StatusOK, out)
}

// assignmentValueRefusedMsg is the 400 of an assignment whose ids hold a
// value the database refuses.
const assignmentValueRefusedMsg = "Straza did not assign the role, because the database refused a value in the request, such as an id that holds a NUL character. " +
	"Send the ids as `strazactl users list` and `strazactl roles list` print them."

func (a *App) handleAssignmentsCreate(w http.ResponseWriter, r *http.Request) {
	var req assignmentPayload
	err := decodeAdmin(r.Body, &req)
	if refusedUnknownField(w, err) {
		return
	}
	if err != nil ||
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
	if errors.Is(err, store.ErrNotFound) {
		apiError(w, http.StatusBadRequest, "Straza did not assign the role, because no user has the id "+cutUnresolved(req.SubjectID)+". Read the ids with `strazactl users list`.")
		return
	}
	if errors.Is(err, store.ErrInvalidData) {
		// The request carries a value the database refuses, so the same
		// request fails the same way.
		apiError(w, http.StatusBadRequest, assignmentValueRefusedMsg)
		return
	}
	if err != nil {
		// An unknown role fails on the database's foreign key, which the
		// store passes on as the driver's error, so a read of the role tells
		// it from a failure of the store.
		if _, rerr := a.store.Roles().GetByID(r.Context(), req.RoleID); errors.Is(rerr, store.ErrNotFound) {
			apiError(w, http.StatusBadRequest, "Straza did not assign the role, because no role has the id "+cutUnresolved(req.RoleID)+". Read the ids with `strazactl roles list`.")
			return
		}
		a.fail(w, r, http.StatusInternalServerError, "Straza could not store the assignment, because its database refused the write or did not answer. "+
			"Try again, and read the strazad log if it keeps failing.", err)
		return
	}
	a.auditAssignment(r.Context(), actionRolesAssign, created, "")
	a.identityChanged(r, "straza.identity.updated", created.SubjectID)
	a.pushRolesNudge(created.SubjectID)
	req.ID = created.ID
	req.Origin = created.Origin
	req.CreatedAt = created.CreatedAt
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
	// The Entra sync writes this row from the role's group and would write
	// it again at its next read, so the member is removed in Entra.
	if as.Origin == store.OriginEntra {
		apiError(w, http.StatusConflict, "this assignment was not removed, because it comes from the role's group in Entra and Straza would write it again at its next read. Remove the user from the group in Entra, and Straza ends the assignment within its next pass")
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
	a.holdingChanged(r.Context(), as.SubjectID)
	a.pushRolesNudge(as.SubjectID)
	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}
