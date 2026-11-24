package server

import (
	"context"
	"net/http"
	"slices"
	"testing"
	"time"

	"github.com/strazahq/straza/internal/store"
)

// TestAssignmentAnswersCreatedAt pins created_at on a role assignment: the
// create answers the time the row was written as an RFC 3339 stamp, and
// both lanes of the list answer the same stamp for that row.
func TestAssignmentAnswersCreatedAt(t *testing.T) {
	t.Parallel()
	app, base := testApp(t)
	ctx := context.Background()
	kim := seedIdentity(t, app)
	grantAdmin(t, app, kim.ID)
	adminTok, _ := checkinToken(t, app, base)
	erin, err := app.store.Users().Create(ctx, store.User{Username: "erin"})
	if err != nil {
		t.Fatal(err)
	}
	reader := mustRole(t, app, "reader")

	before := time.Now().Add(-time.Second)
	var created map[string]any
	if code := adminReq(t, http.MethodPost, base+"/v1/admin/assignments", adminTok,
		map[string]any{"subject_kind": "user", "subject_id": erin.ID, "role_id": reader.ID}, &created); code != http.StatusCreated {
		t.Fatalf("create = %d %v", code, created)
	}
	stamp, _ := created["created_at"].(string)
	at, err := time.Parse(time.RFC3339, stamp)
	if err != nil || at.Before(before) || at.After(time.Now().Add(time.Second)) {
		t.Fatalf("create answered created_at %q (%v), want the time of the create", stamp, err)
	}

	for _, path := range []string{
		"/v1/admin/assignments?subject_kind=user&subject_id=" + erin.ID,
		"/v1/admin/assignments",
	} {
		var rows []map[string]any
		if code := adminReq(t, http.MethodGet, base+path, adminTok, nil, &rows); code != http.StatusOK {
			t.Fatalf("GET %s = %d", path, code)
		}
		found := false
		for _, row := range rows {
			if row["id"] == created["id"] {
				found = true
				if row["created_at"] != stamp {
					t.Errorf("GET %s created_at = %v, want %q as the create answered", path, row["created_at"], stamp)
				}
			}
		}
		if !found {
			t.Errorf("GET %s does not list assignment %v", path, created["id"])
		}
	}
}

// TestAssignmentCreateChecksSubject pins what the create route does with the
// user and the role it names: an existing user gets the row, one
// roles.assign record, one identity event and one role nudge. An id no user
// has, or a role that does not exist, answers 400 and leaves no row, no
// record, no identity event and no nudge behind.
func TestAssignmentCreateChecksSubject(t *testing.T) {
	t.Parallel()
	app, base := testApp(t)
	ctx := context.Background()
	kim := seedIdentity(t, app)
	grantAdmin(t, app, kim.ID)
	adminTok, _ := checkinToken(t, app, base)
	erin, err := app.store.Users().Create(ctx, store.User{Username: "erin"})
	if err != nil {
		t.Fatal(err)
	}
	fay, err := app.store.Users().Create(ctx, store.User{Username: "fay"})
	if err != nil {
		t.Fatal(err)
	}
	reader := mustRole(t, app, "reader")
	const ghost = "0197fa3d-5555-7abc-9def-00000000dead"
	nudges := listenRolesNudges(t, app)

	for _, tc := range []struct {
		name, subject, role string
		status              int
		refusal             string
	}{
		{name: "an existing user", subject: erin.ID, role: reader.ID, status: http.StatusCreated},
		{name: "an id no user has", subject: ghost, role: reader.ID, status: http.StatusBadRequest,
			refusal: "Straza did not assign the role, because no user has the id " + ghost + ". Read the ids with `strazactl users list`."},
		{name: "a role that does not exist", subject: fay.ID, role: "no-such-role", status: http.StatusBadRequest,
			refusal: "Straza did not assign the role, because no role has the id no-such-role. Read the ids with `strazactl roles list`."},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var body map[string]any
			code := adminReq(t, http.MethodPost, base+"/v1/admin/assignments", adminTok,
				map[string]any{"subject_kind": "user", "subject_id": tc.subject, "role_id": tc.role}, &body)
			if code != tc.status || (tc.refusal != "" && body["error"] != tc.refusal) {
				t.Fatalf("create = %d %v, want %d with the refusal %q", code, body, tc.status, tc.refusal)
			}
			want, wantNudges := 0, []string(nil)
			if tc.status == http.StatusCreated {
				want, wantNudges = 1, []string{rolesPushSubject(tc.subject)}
			}
			held, err := app.store.Roles().ListAssignments(ctx, store.SubjectUser, tc.subject)
			if err != nil || len(held) != want {
				t.Errorf("rows of %s = %+v (%v), want %d", tc.subject, held, err, want)
			}
			var assigned int
			for _, rec := range eventsNaming(t, app, "straza.audit.admin", "user", tc.subject) {
				if rec["action"] == actionRolesAssign {
					assigned++
				}
			}
			if assigned != want {
				t.Errorf("roles.assign records of %s = %d, want %d", tc.subject, assigned, want)
			}
			if got := eventsNaming(t, app, "straza.identity.updated", "id", tc.subject); len(got) != want {
				t.Errorf("identity events of %s = %v, want %d", tc.subject, got, want)
			}
			var subjects []string
			for _, h := range nudges.take(t, app) {
				subjects = append(subjects, h.subject)
			}
			if !slices.Equal(subjects, wantNudges) {
				t.Errorf("role nudges = %v, want %v", subjects, wantNudges)
			}
		})
	}
}
