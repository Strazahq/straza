package server

import (
	"context"
	"net/http"
	"testing"

	"github.com/strazahq/straza/internal/store"
)

// assignmentAuditRecords returns the straza.audit.admin payloads whose
// action is roles.assign or roles.unassign, oldest first.
func assignmentAuditRecords(t *testing.T, app *App) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, ev := range adminAuditEvents(t, app) {
		if ev["action"] == actionRolesAssign || ev["action"] == actionRolesUnassign {
			out = append(out, ev)
		}
	}
	return out
}

// assertAssignmentRecord checks one chained record against the row it
// describes and the principal that wrote it.
func assertAssignmentRecord(t *testing.T, got map[string]any, action, rowID, userID string, role store.Role, origin, actor, actorID, actorVia string) {
	t.Helper()
	want := map[string]any{
		"action": action, "target": rowID, "user": userID, "role": role.Name, "roleId": role.ID,
		"origin": origin, "actor": actor, "actorId": actorID, "actorVia": actorVia,
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("record %v: %s = %v, want %v", got, k, got[k], v)
		}
	}
	if _, has := got["reason"]; has {
		t.Errorf("record %v carries a reason, want none for a plain grant change", got)
	}
}

// TestSCIMMembershipAudit: every Group membership write chains one
// straza.audit.admin record per assignment it starts or ends, naming the
// user, the role, the row, the scim origin and the admin API token that
// acted, and a write that changes nothing chains nothing.
func TestSCIMMembershipAudit(t *testing.T) {
	t.Parallel()
	app, base, token := scimTestSetup(t)
	ctx := context.Background()
	role, err := app.store.Roles().Create(ctx, store.Role{Name: "audit-dev", Kind: store.RoleKindBusiness})
	if err != nil {
		t.Fatal(err)
	}
	bob := scimCreateUser(t, base, token, `{"userName":"bob@x.io"}`)
	sam := scimCreateUser(t, base, token, `{"userName":"sam@x.io"}`)
	writer := apiTokenByName(t, app, "conformance")

	holders := func() map[string]string {
		out := map[string]string{}
		asg, err := app.store.Roles().AssignmentsByRole(ctx, role.ID)
		if err != nil {
			t.Fatal(err)
		}
		for _, a := range asg {
			out[a.SubjectID] = a.ID
		}
		return out
	}
	patchBody := func(ops string) string {
		return `{"schemas":["urn:ietf:params:scim:api:messages:2.0:PatchOp"],"Operations":` + ops + `}`
	}
	type want struct{ user, action string }
	steps := []struct {
		name, method, body string
		want               []want
	}{
		{"PATCH add", "PATCH",
			patchBody(`[{"op":"add","path":"members","value":[{"value":"` + bob + `"}]}]`),
			[]want{{bob, actionRolesAssign}}},
		{"repeated PATCH add chains nothing", "PATCH",
			patchBody(`[{"op":"add","path":"members","value":[{"value":"` + bob + `"}]}]`),
			nil},
		{"PATCH value-filter remove", "PATCH",
			patchBody(`[{"op":"remove","path":"members[value eq \"` + bob + `\"]"}]`),
			[]want{{bob, actionRolesUnassign}}},
		{"PATCH add before the replace", "PATCH",
			patchBody(`[{"op":"add","path":"members","value":[{"value":"` + bob + `"}]}]`),
			[]want{{bob, actionRolesAssign}}},
		{"PUT replace ends bob and starts sam", "PUT",
			`{"schemas":["urn:ietf:params:scim:schemas:core:2.0:Group"],"displayName":"audit-dev","members":[{"value":"` + sam + `"}]}`,
			[]want{{bob, actionRolesUnassign}, {sam, actionRolesAssign}}},
	}
	for _, step := range steps {
		t.Run(step.name, func(t *testing.T) {
			before, seen := holders(), len(assignmentAuditRecords(t, app))
			if code, out := scimReq(t, step.method, base, token, "/scim/v2/Groups/"+role.ID, step.body); code != http.StatusOK {
				t.Fatalf("%s = %d %v", step.method, code, out)
			}
			after, got := holders(), assignmentAuditRecords(t, app)[seen:]
			if len(got) != len(step.want) {
				t.Fatalf("chained %d assignment records %v, want %v", len(got), got, step.want)
			}
			for i, w := range step.want {
				rowID := after[w.user]
				if w.action == actionRolesUnassign {
					rowID = before[w.user]
				}
				if rowID == "" {
					t.Fatalf("no assignment row for %s around step %q", w.user, step.name)
				}
				assertAssignmentRecord(t, got[i], w.action, rowID, w.user, role, store.OriginSCIM, "conformance", writer.ID, "api-token")
			}
		})
	}
}

// TestAssignmentsRESTAudit: the admin API's assignment create and delete
// chain the same record shape with the admin origin and the human actor, so
// a certifier reads one vocabulary whichever lane granted the role.
func TestAssignmentsRESTAudit(t *testing.T) {
	t.Parallel()
	app, base := testApp(t)
	admin := seedIdentity(t, app)
	grantAdmin(t, app, admin.ID)
	bearer := loginDeviceFlow(t, base, "kim", "hunter2!")
	ctx := context.Background()
	role, err := app.store.Roles().Create(ctx, store.Role{Name: "audit-ops", Kind: store.RoleKindBusiness})
	if err != nil {
		t.Fatal(err)
	}
	bob, err := app.store.Users().Create(ctx, store.User{Username: "bob"})
	if err != nil {
		t.Fatal(err)
	}

	var created assignmentPayload
	if code := adminReq(t, "POST", base+"/v1/admin/assignments", bearer, map[string]string{
		"subject_kind": store.SubjectUser, "subject_id": bob.ID, "role_id": role.ID,
	}, &created); code != http.StatusCreated {
		t.Fatalf("create assignment = %d", code)
	}
	got := assignmentAuditRecords(t, app)
	if len(got) != 1 {
		t.Fatalf("records after create = %v, want one roles.assign", got)
	}
	assertAssignmentRecord(t, got[0], actionRolesAssign, created.ID, bob.ID, role, store.OriginAdmin, "kim", admin.ID, "login")

	if code := adminReq(t, "DELETE", base+"/v1/admin/assignments/"+created.ID, bearer, nil, nil); code != http.StatusOK {
		t.Fatalf("delete assignment = %d", code)
	}
	got = assignmentAuditRecords(t, app)
	if len(got) != 2 {
		t.Fatalf("records after delete = %v, want roles.assign then roles.unassign", got)
	}
	assertAssignmentRecord(t, got[1], actionRolesUnassign, created.ID, bob.ID, role, store.OriginAdmin, "kim", admin.ID, "login")
}
