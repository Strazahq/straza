package server

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/strazahq/straza/internal/store"
)

// scimTestSetup boots a strazad and mints a SCIM token via the real admin
// surface (shared by the regression tests below).
func scimTestSetup(t *testing.T) (*App, string, string) {
	t.Helper()
	app, base := testApp(t)
	user := seedIdentity(t, app)
	grantAdmin(t, app, user.ID)
	adminBearer := loginDeviceFlow(t, base, "kim", "hunter2!")
	return app, base, mintProvisioningToken(t, base, adminBearer)
}

// TestGroupReplaceRejectsBadMemberWithoutWipe: one unknown member id in an
// IdM full-document PUT must reject the request and leave the current
// (role-granting) membership intact. Removing members before validating the
// new list would let a single stale id wipe the holders, and every member
// would lose role access until the next sync.
func TestGroupReplaceRejectsBadMemberWithoutWipe(t *testing.T) {
	t.Parallel()
	app, base, token := scimTestSetup(t)
	ctx := context.Background()

	role, err := app.store.Roles().Create(ctx, store.Role{Name: "regress-dev", Kind: store.RoleKindBusiness})
	if err != nil {
		t.Fatal(err)
	}
	u1 := scimCreateUser(t, base, token, `{"userName":"alice@x.io"}`)
	u2 := scimCreateUser(t, base, token, `{"userName":"bob@x.io"}`)
	scimGroupPatch(t, base, token, role.ID, `{
		"schemas":["urn:ietf:params:scim:api:messages:2.0:PatchOp"],
		"Operations":[{"op":"add","path":"members","value":[{"value":"`+u1+`"},{"value":"`+u2+`"}]}]}`)

	code, out := scimReq(t, "PUT", base, token, "/scim/v2/Groups/"+role.ID,
		`{"displayName":"regress-dev","members":[{"value":"`+u1+`"},{"value":"not-a-user"}]}`)
	if code != http.StatusBadRequest {
		t.Fatalf("bad-member PUT = %d %v, want 400", code, out)
	}
	asg, err := app.store.Roles().AssignmentsByRole(ctx, role.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(asg) != 2 {
		t.Fatalf("holders after rejected PUT = %v, want the original 2", asg)
	}
}

// TestSCIMBodyCapped: a SCIM request body beyond the 1 MiB cap is rejected
// instead of buffered (a valid bearer token must not be a memory-exhaustion
// primitive against the control plane).
func TestSCIMBodyCapped(t *testing.T) {
	t.Parallel()
	_, base, token := scimTestSetup(t)

	big := `{"userName":"big@x.io","displayName":"` + strings.Repeat("a", 2<<20) + `"}`
	code, _ := scimReq(t, "POST", base, token, "/scim/v2/Users", big)
	if code != http.StatusBadRequest {
		t.Fatalf("oversized body = %d, want 400", code)
	}
}

// TestSCIMDeleteThenRecreateRevives: SCIM DELETE is deliberately a deactivate
// (kill-switch cascade), which leaves the userName held by the users unique
// constraint, so an IdM's delete-then-reprovision cycle (a rehire, or a
// provisioning retry after an interrupted run) would answer 409 forever. A
// create that conflicts with a DISABLED SCIM-origin row of the same
// userName revives that row in place: same ID (audit continuity), document
// attributes overwrite, stale local password wiped, SCIM-lane revocations
// lifted, and the per-user OAuth grants the deactivation wiped stay gone.
// Fail-closed edges pinned: an ACTIVE row keeps the 409, a disabled LOCAL
// row keeps the 409.
func TestSCIMDeleteThenRecreateRevives(t *testing.T) {
	t.Parallel()
	app, base, token := scimTestSetup(t)
	ctx := context.Background()

	uid := scimCreateUser(t, base, token,
		`{"userName":"rehire@x.io","externalId":"idm-1","active":true}`)
	setUserPassword(t, app, uid, "hunter2!")
	grantUserApp(t, app, "github", uid)

	code, _ := scimReq(t, "DELETE", base, token, "/scim/v2/Users/"+uid, "")
	if code != http.StatusNoContent {
		t.Fatalf("DELETE = %d, want 204", code)
	}
	if u, err := app.store.Users().GetByID(ctx, uid); err != nil || u.Status != store.UserDisabled {
		t.Fatalf("post-DELETE user status = %q err=%v, want disabled", u.Status, err)
	}

	// The IdM provisions the same userName again: revive, not 409.
	code, out := scimReq(t, "POST", base, token, "/scim/v2/Users",
		`{"userName":"rehire@x.io","externalId":"idm-2","active":true}`)
	if code != http.StatusCreated {
		t.Fatalf("recreate after DELETE = %d %v, want 201", code, out)
	}
	if got, _ := out["id"].(string); got != uid {
		t.Errorf("revived id = %q, want the original %q (audit continuity)", got, uid)
	}
	u, err := app.store.Users().GetByID(ctx, uid)
	if err != nil {
		t.Fatal(err)
	}
	if u.Status != store.UserActive || u.ExternalID != "idm-2" || u.Origin != store.OriginSCIM {
		t.Errorf("revived user = status %q extid %q origin %q, want active/idm-2/scim",
			u.Status, u.ExternalID, u.Origin)
	}
	if u.PasswordHash != "" {
		t.Errorf("revive kept a stale local password hash; deprovision must not resurrect credentials")
	}
	if rows, err := app.store.Revocations().ListByTarget(ctx, store.RevokeUser, uid); err != nil || len(rows) != 0 {
		t.Errorf("revocations after revive = %v err=%v, want none", rows, err)
	}
	if rows := userGrants(t, app, uid); len(rows) != 0 {
		t.Errorf("revive returned with %d per-user grant rows; deprovisioning must wipe them", len(rows))
	}

	// Fail-closed: an ACTIVE username is never stolen by create.
	if code, _ := scimReq(t, "POST", base, token, "/scim/v2/Users",
		`{"userName":"rehire@x.io"}`); code != http.StatusConflict {
		t.Errorf("create over ACTIVE user = %d, want 409", code)
	}

	// Fail-closed: a disabled LOCAL account cannot be captured through the
	// provisioning lane.
	if _, err := app.store.Users().Create(ctx, store.User{
		Username: "local.dormant", Status: store.UserDisabled,
	}); err != nil {
		t.Fatal(err)
	}
	if code, _ := scimReq(t, "POST", base, token, "/scim/v2/Users",
		`{"userName":"local.dormant"}`); code != http.StatusConflict {
		t.Errorf("create over disabled local-origin user = %d, want 409", code)
	}
}
