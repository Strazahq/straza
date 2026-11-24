package server

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/strazahq/straza/internal/store"
)

// TestAssignmentsDeleteRefusesEntraRow pins that the admin API does not end
// an assignment the Entra sync wrote, says where to end it, leaves the row
// and writes no record, and that a hand-made assignment for a user who also
// holds the role from Entra is stored beside it.
func TestAssignmentsDeleteRefusesEntraRow(t *testing.T) {
	t.Parallel()
	app, base := testApp(t)
	admin := seedIdentity(t, app)
	grantAdmin(t, app, admin.ID)
	bearer := loginDeviceFlow(t, base, "kim", "hunter2!")
	ctx := context.Background()
	role, err := app.store.Roles().Create(ctx, store.Role{Name: "entra-held", Kind: store.RoleKindBusiness})
	if err != nil {
		t.Fatal(err)
	}
	bob, err := app.store.Users().Create(ctx, store.User{Username: "bob"})
	if err != nil {
		t.Fatal(err)
	}
	row, err := app.store.Roles().Assign(ctx, store.RoleAssignment{
		SubjectKind: store.SubjectUser, SubjectID: bob.ID, RoleID: role.ID, Origin: store.OriginEntra,
	})
	if err != nil {
		t.Fatal(err)
	}
	var refused struct {
		Error string `json:"error"`
	}
	if code := adminReq(t, "DELETE", base+"/v1/admin/assignments/"+row.ID, bearer, nil, &refused); code != http.StatusConflict {
		t.Fatalf("delete of an Entra row = %d, want 409", code)
	}
	if want := "Remove the user from the group in Entra"; !strings.Contains(refused.Error, want) {
		t.Errorf("refusal = %q, want it to say %q", refused.Error, want)
	}
	if _, err := app.store.Roles().Assignment(ctx, row.ID); err != nil {
		t.Errorf("the Entra row is gone after the refusal: %v", err)
	}
	if got := assignmentAuditRecords(t, app); len(got) != 0 {
		t.Errorf("records after the refusal = %v, want none", got)
	}
	var created assignmentPayload
	if code := adminReq(t, "POST", base+"/v1/admin/assignments", bearer, map[string]string{
		"subject_kind": store.SubjectUser, "subject_id": bob.ID, "role_id": role.ID,
	}, &created); code != http.StatusCreated {
		t.Fatalf("a hand-made assignment beside the Entra row = %d, want 201", code)
	}
	if created.Origin != store.OriginAdmin {
		t.Errorf("the hand-made row's origin = %q, want admin", created.Origin)
	}
}
