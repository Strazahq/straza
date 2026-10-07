package server

import (
	"context"
	"net/http"
	"testing"

	"github.com/strazahq/straza/internal/store"
)

// TestAssignmentsListCarriesOrigin pins the additive origin field on the
// assignments list: a row the SCIM lane wrote reads scim, a row the admin
// lane wrote reads admin on both the create response and the list, and the
// subject filter carries the same field.
func TestAssignmentsListCarriesOrigin(t *testing.T) {
	t.Parallel()
	app, base := testApp(t)
	admin := seedIdentity(t, app)
	grantAdmin(t, app, admin.ID)
	adminTok, _ := checkinToken(t, app, base)
	ctx := context.Background()

	role, err := app.store.Roles().Create(ctx, store.Role{Name: "origin-ops", Kind: store.RoleKindBusiness})
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name     string
		username string
		origin   string
		viaAPI   bool
	}{
		{"scim-born row", "scim-born", store.OriginSCIM, false},
		{"admin-born row", "admin-born", store.OriginAdmin, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			u, err := app.store.Users().Create(ctx, store.User{Username: tc.username})
			if err != nil {
				t.Fatal(err)
			}
			var rowID string
			if tc.viaAPI {
				var created assignmentPayload
				if code := adminReq(t, http.MethodPost, base+"/v1/admin/assignments", adminTok, map[string]string{
					"subject_kind": store.SubjectUser, "subject_id": u.ID, "role_id": role.ID,
				}, &created); code != http.StatusCreated {
					t.Fatalf("create assignment = %d", code)
				}
				if created.Origin != tc.origin {
					t.Errorf("create response origin = %q, want %q", created.Origin, tc.origin)
				}
				rowID = created.ID
			} else {
				as, err := app.store.Roles().Assign(ctx, store.RoleAssignment{
					SubjectKind: store.SubjectUser, SubjectID: u.ID, RoleID: role.ID, Origin: tc.origin,
				})
				if err != nil {
					t.Fatal(err)
				}
				rowID = as.ID
			}
			for _, query := range []string{"", "?subject_kind=user&subject_id=" + u.ID} {
				var listed []assignmentPayload
				if code := adminReq(t, http.MethodGet, base+"/v1/admin/assignments"+query, adminTok, nil, &listed); code != http.StatusOK {
					t.Fatalf("list assignments%s = %d", query, code)
				}
				found := false
				for _, row := range listed {
					if row.ID != rowID {
						continue
					}
					found = true
					if row.Origin != tc.origin {
						t.Errorf("list%s origin = %q, want %q", query, row.Origin, tc.origin)
					}
				}
				if !found {
					t.Fatalf("assignment %s missing from list%s: %+v", rowID, query, listed)
				}
			}
		})
	}
}
