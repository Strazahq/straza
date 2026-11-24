package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/strazahq/straza/internal/store"
)

// TestAssignmentCreateAnswers pins each answer of POST /v1/admin/assignments.
// An existing user and role get the row, one roles.assign record, one
// identity event and one role nudge, and a second write of the same pair
// answers 409. An unknown role or user answers 400 with a sentence that
// names the id, cut past 256 bytes, and the strazactl command that lists the
// ids. When neither exists the user's sentence wins, because the store's
// insert forms no row for an unknown user, so the database never checks the
// role. A value the database refuses answers 400. Any other failure of the
// store answers 500 and leaves one Error record. A refusal stores no row,
// writes no record, publishes no identity event and nudges no one.
func TestAssignmentCreateAnswers(t *testing.T) {
	t.Parallel()
	logger, logs := captureLogger()
	app, ls, bearer, ada, role := adminWithTarget(t, logger)
	ctx := context.Background()
	nudges := listenRolesNudges(t, app)
	const ghost = "0197fa3d-5555-7abc-9def-00000000dead"
	long := strings.Repeat("x", 5000)
	cut := strings.Repeat("x", 256) + "... (5000 bytes)"
	noRole := func(id string) string {
		return "Straza did not assign the role, because no role has the id " + id + ". Read the ids with `strazactl roles list`."
	}
	noUser := func(id string) string {
		return "Straza did not assign the role, because no user has the id " + id + ". Read the ids with `strazactl users list`."
	}
	lostDB := errors.New("the database closed the connection")

	for _, tc := range []struct {
		name          string
		subject, role string
		held          bool  // the subject holds the role before the request
		fail          error // the answer of the store's Assign
		status        int
		refusal       string
		rows          int
	}{
		{name: "an existing user and role", subject: ada.ID, role: role.ID, status: http.StatusCreated, rows: 1},
		{name: "an assignment that exists", subject: mkHuman(t, app, "bo").ID, role: role.ID, held: true,
			status: http.StatusConflict, refusal: "assignment already exists", rows: 1},
		{name: "a role that does not exist", subject: mkHuman(t, app, "cy").ID, role: "no-such-role",
			status: http.StatusBadRequest, refusal: noRole("no-such-role")},
		{name: "a user that does not exist", subject: ghost, role: role.ID,
			status: http.StatusBadRequest, refusal: noUser(ghost)},
		{name: "neither exists", subject: ghost, role: "no-such-role",
			status: http.StatusBadRequest, refusal: noUser(ghost)},
		{name: "a role id of 5,000 characters", subject: mkHuman(t, app, "di").ID, role: long,
			status: http.StatusBadRequest, refusal: noRole(cut)},
		{name: "a user id of 5,000 characters", subject: long, role: role.ID,
			status: http.StatusBadRequest, refusal: noUser(cut)},
		{name: "a value the database refuses", subject: mkHuman(t, app, "flo").ID, role: role.ID, fail: store.ErrInvalidData,
			status: http.StatusBadRequest, refusal: assignmentValueRefusedMsg},
		{name: "a store that fails the write", subject: mkHuman(t, app, "ed").ID, role: role.ID, fail: lostDB,
			status: http.StatusInternalServerError,
			refusal: "Straza could not store the assignment, because its database refused the write or did not answer. " +
				"Try again, and read the strazad log if it keeps failing."},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.held {
				if _, err := app.store.Roles().Assign(ctx, store.RoleAssignment{SubjectKind: store.SubjectUser, SubjectID: tc.subject, RoleID: tc.role}); err != nil {
					t.Fatal(err)
				}
			}
			if tc.fail != nil {
				ls.arm(beforeAssign, func(context.Context) error { return tc.fail })
				defer ls.arm(beforeAssign, nil)
			}
			logs.Reset()
			body := `{"subject_kind":"user","subject_id":"` + tc.subject + `","role_id":"` + tc.role + `"}`
			rec := serveOn(ctx, app, http.MethodPost, "/v1/admin/assignments", bearer, body)
			var answer map[string]any
			_ = json.Unmarshal(rec.Body.Bytes(), &answer)
			if rec.Code != tc.status || (tc.refusal != "" && answer["error"] != tc.refusal) {
				t.Fatalf("create = %d %s, want %d with the refusal %q", rec.Code, rec.Body.String(), tc.status, tc.refusal)
			}
			created := 0
			if tc.status == http.StatusCreated {
				created = 1
			}
			held, err := app.store.Roles().ListAssignments(ctx, store.SubjectUser, tc.subject)
			if err != nil || len(held) != tc.rows {
				t.Errorf("rows of the subject = %d (%v), want %d", len(held), err, tc.rows)
			}
			var assigned int
			for _, ev := range eventsNaming(t, app, "straza.audit.admin", "user", tc.subject) {
				if ev["action"] == actionRolesAssign {
					assigned++
				}
			}
			if assigned != created {
				t.Errorf("roles.assign records of the subject = %d, want %d", assigned, created)
			}
			if got := eventsNaming(t, app, "straza.identity.updated", "id", tc.subject); len(got) != created {
				t.Errorf("identity events of the subject = %d, want %d", len(got), created)
			}
			var subjects []string
			for _, h := range nudges.take(t, app) {
				subjects = append(subjects, h.subject)
			}
			var wantNudges []string
			if created == 1 {
				wantNudges = []string{rolesPushSubject(tc.subject)}
			}
			if !slices.Equal(subjects, wantNudges) {
				t.Errorf("role nudges = %v, want %v", subjects, wantNudges)
			}
			var records []string
			for _, line := range errorRecords(logs) {
				if strings.Contains(line, "/v1/admin/assignments") {
					records = append(records, line)
				}
			}
			switch {
			case tc.status != http.StatusInternalServerError && len(records) != 0:
				t.Errorf("Error records = %v, want none for a %d", records, tc.status)
			case tc.status == http.StatusInternalServerError &&
				(len(records) != 1 || !strings.Contains(records[0], "status=500") || !strings.Contains(records[0], lostDB.Error())):
				t.Errorf("Error records = %v, want one with status=500 and the cause %q", records, lostDB)
			}
		})
	}
}
