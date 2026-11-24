package server

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/strazahq/straza/internal/entrasync"
	"github.com/strazahq/straza/internal/store"
)

// TestAdminEntraGroup drives the admin plane of a role's group in Entra:
// the roles list carries the link, the status route says whether the read
// is stale, and the two actions are refused in any state but their own,
// take effect once in it, and leave one record that names the admin.
func TestAdminEntraGroup(t *testing.T) {
	t.Parallel()
	app, base := testApp(t)
	admin := seedIdentity(t, app)
	grantAdmin(t, app, admin.ID)
	bearer := loginDeviceFlow(t, base, "kim", "hunter2!")
	ctx := context.Background()
	role, err := app.store.Roles().Create(ctx, store.Role{Name: "entra-api", Kind: store.RoleKindBusiness})
	if err != nil {
		t.Fatal(err)
	}
	bob, err := app.store.Users().Create(ctx, store.User{Username: "bob"})
	if err != nil {
		t.Fatal(err)
	}
	entraRow, err := app.store.Roles().Assign(ctx, store.RoleAssignment{SubjectKind: store.SubjectUser, SubjectID: bob.ID, RoleID: role.ID, Origin: store.OriginEntra})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := app.store.Roles().Assign(ctx, store.RoleAssignment{SubjectKind: store.SubjectUser, SubjectID: bob.ID, RoleID: role.ID}); err != nil {
		t.Fatal(err)
	}
	read := time.Date(2030, 1, 1, 12, 0, 0, 0, time.UTC)
	name := entrasync.UniqueName("abc123", role.ID)
	put := func(state string) {
		t.Helper()
		if err := app.store.EntraGroups().Put(ctx, store.EntraRoleGroup{
			RoleID: role.ID, UniqueName: name, GroupID: "group-1", State: state, ReadAt: &read,
			Error: "a sentence", NotApplied: `[{"id":"oid-x","kind":"user","reason":"name-taken"}]`,
		}); err != nil {
			t.Fatal(err)
		}
	}
	put(store.EntraGroupLinked)

	var roles []rolePayload
	if code := adminReq(t, "GET", base+"/v1/admin/roles", bearer, nil, &roles); code != http.StatusOK {
		t.Fatalf("roles list = %d", code)
	}
	var got *roleEntraPayload
	for _, r := range roles {
		if r.ID == role.ID {
			got = r.Entra
		} else if r.Entra != nil {
			t.Errorf("role %s carries a group it has no row for: %+v", r.Name, r.Entra)
		}
	}
	if got == nil || got.GroupID != "group-1" || got.State != store.EntraGroupLinked || got.ReadAt == nil || !got.ReadAt.Equal(read) ||
		got.Error != "a sentence" || len(got.NotApplied) != 1 || got.NotApplied[0].Reason != entrasync.ReasonNameTaken {
		t.Fatalf("the role's entra block = %+v", got)
	}

	var status entraStatusPayload
	if code := adminReq(t, "GET", base+"/v1/admin/entra", bearer, nil, &status); code != http.StatusOK {
		t.Fatalf("status = %d", code)
	}
	if status.Enabled || status.StaleSince != nil || status.Held != 0 {
		t.Errorf("status of a deployment with Entra off = %+v", status)
	}

	var refused struct {
		Error string `json:"error"`
	}
	for _, action := range []string{"release", "recreate"} {
		if code := adminReq(t, "POST", base+"/v1/admin/roles/"+role.ID+"/entra-group/"+action, bearer, nil, &refused); code != http.StatusConflict {
			t.Errorf("%s of a linked group = %d, want 409", action, code)
		}
		if !strings.Contains(refused.Error, "nothing changed") {
			t.Errorf("%s refusal = %q, want it to say that nothing changed", action, refused.Error)
		}
	}
	if code := adminReq(t, "POST", base+"/v1/admin/roles/no-such-role/entra-group/release", bearer, nil, nil); code != http.StatusNotFound {
		t.Errorf("release for an unknown role = %d, want 404", code)
	}

	put(store.EntraGroupHeld)
	if code := adminReq(t, "GET", base+"/v1/admin/entra", bearer, nil, &status); code != http.StatusOK || status.Held != 1 {
		t.Errorf("status with one held pass = %d %+v, want held 1", code, status)
	}
	if code := adminReq(t, "POST", base+"/v1/admin/roles/"+role.ID+"/entra-group/release", bearer, nil, nil); code != http.StatusOK {
		t.Fatalf("release of a held pass = %d", code)
	}
	if row, _ := app.store.EntraGroups().Get(ctx, role.ID); row.State != store.EntraGroupReleased {
		t.Errorf("state after the release = %q, want released", row.State)
	}

	put(store.EntraGroupMissing)
	if code := adminReq(t, "POST", base+"/v1/admin/roles/"+role.ID+"/entra-group/recreate", bearer, nil, nil); code != http.StatusOK {
		t.Fatalf("recreate of a missing group = %d", code)
	}
	row, err := app.store.EntraGroups().Get(ctx, role.ID)
	if err != nil {
		t.Fatal(err)
	}
	if row.State != store.EntraGroupCreating || row.GroupID != "" || row.ReadAt != nil || row.UniqueName != name+"-2" || row.Error != "" {
		t.Errorf("row after the recreate = %+v, want creating, no group, no read and the name key %s-2", row, name)
	}
	asg, _ := app.store.Roles().AssignmentsByRole(ctx, role.ID)
	if len(asg) != 1 || asg[0].Origin != store.OriginAdmin {
		t.Errorf("assignments after the recreate = %+v, want the hand-made row alone", asg)
	}

	want := map[string]int{"entra.pass.released": 1, "entra.group.recreate": 1, actionRolesUnassign: 1}
	for _, ev := range adminAuditEvents(t, app) {
		action, _ := ev["action"].(string)
		if _, ok := want[action]; !ok {
			continue
		}
		want[action]--
		if ev["actor"] != "kim" || ev["roleId"] != role.ID {
			t.Errorf("record %v, want the admin kim and the role", ev)
		}
		if action == actionRolesUnassign && (ev["target"] != entraRow.ID || ev["origin"] != store.OriginEntra || ev["reason"] == nil) {
			t.Errorf("unassign record %v, want the Entra row with a reason", ev)
		}
	}
	for action, left := range want {
		if left != 0 {
			t.Errorf("%s was recorded %d times too few or too many", action, left)
		}
	}
}
