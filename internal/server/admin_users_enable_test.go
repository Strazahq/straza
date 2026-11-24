package server

import (
	"context"
	"fmt"
	"net/http"
	"slices"
	"testing"

	"github.com/strazahq/straza/internal/store"
)

// DeleteByOrigin fails the selective lift when "revocations.delete" is armed.
func (r faultRevocations) DeleteByOrigin(ctx context.Context, kind, targetID, origin string) error {
	if err := r.f.fault("revocations.delete"); err != nil {
		return err
	}
	return r.RevocationRepo.DeleteByOrigin(ctx, kind, targetID, origin)
}

// TestIdentityManagerEnableMeetsAnAdminDisable pins an admin disable of a
// user the identity manager owns, which the identity manager then enables.
// The disable's row survives, so Straza still refuses the user, records one
// user.lift.blocked that names the row, and shows it to the identity manager
// as a lock of origin admin-disable. An admin enable of the user, now
// active, lifts the row, lets the user in and records one user.reactivated.
func TestIdentityManagerEnableMeetsAnAdminDisable(t *testing.T) {
	t.Parallel()
	app, base := testApp(t)
	ctx := context.Background()
	kim := seedIdentity(t, app)
	grantAdmin(t, app, kim.ID)
	bearer := loginDeviceFlow(t, base, "kim", "hunter2!")
	scimToken := mintProvisioningToken(t, base, bearer)
	u, err := app.store.Users().Create(ctx, store.User{Username: "nia", Origin: store.OriginSCIM, Status: store.UserActive})
	if err != nil {
		t.Fatal(err)
	}
	const urn = "urn:straza:params:scim:schemas:extension:2.0:User"
	block := func(t *testing.T) map[string]any {
		t.Helper()
		code, _, out := callJSON(t, http.MethodGet, base+"/scim/v2/Users/"+u.ID, scimToken, nil)
		b, _ := out[urn].(map[string]any)
		if code != http.StatusOK || b == nil {
			t.Fatalf("SCIM read = %d %v", code, out)
		}
		return b
	}
	identity := func(t *testing.T) []map[string]any {
		t.Helper()
		return eventsNaming(t, app, "straza.audit.identity", "user", u.ID)
	}
	rows := func(t *testing.T) []store.Revocation {
		t.Helper()
		rs, err := app.store.Revocations().ListByTarget(ctx, store.RevokeUser, u.ID)
		if err != nil {
			t.Fatal(err)
		}
		return rs
	}

	if code := adminReq(t, http.MethodPatch, base+"/v1/admin/users/"+u.ID, bearer, map[string]string{"status": store.UserDisabled}, nil); code != http.StatusOK {
		t.Fatalf("admin disable = %d", code)
	}
	if b := block(t); b["locked"] != false {
		t.Errorf("the block of a disabled user = %v, want no lock", b)
	}

	before := len(identity(t))
	code, _, out := callJSON(t, http.MethodPatch, base+"/scim/v2/Users/"+u.ID, scimToken, map[string]any{
		"schemas":    []string{"urn:ietf:params:scim:api:messages:2.0:PatchOp"},
		"Operations": []map[string]any{{"op": "replace", "path": "active", "value": true}},
	})
	if code != http.StatusOK || out["active"] != true {
		t.Fatalf("SCIM enable = %d %v", code, out)
	}
	if rs := rows(t); len(rs) != 1 || rs[0].Origin != store.RevocationOriginAdminDisable || !app.denylist.userBlocked(u.ID) {
		t.Errorf("after the SCIM enable: rows %+v and refused %v, want the disable's row and refused", rs, app.denylist.userBlocked(u.ID))
	}
	recs := identity(t)[before:]
	if len(recs) != 1 || recs[0]["action"] != "user.lift.blocked" || recs[0]["origin"] != store.RevocationOriginSCIM {
		t.Fatalf("the SCIM enable's records = %v, want one user.lift.blocked of origin scim", recs)
	}
	if held, _ := recs[0]["heldBy"].([]any); len(held) != 1 || held[0] != store.RevocationOriginAdminDisable {
		t.Errorf("heldBy = %v, want [admin-disable]", recs[0]["heldBy"])
	}
	if b := block(t); b["locked"] != true || b["lockOrigin"] != store.RevocationOriginAdminDisable || b["lockReason"] != "user disabled by admin" {
		t.Errorf("the block after the SCIM enable = %v, want a lock of origin admin-disable", b)
	}

	before = len(identity(t))
	lifts := len(eventsNaming(t, app, "straza.revocation.lift", "user", u.ID))
	code, _, out = callJSON(t, http.MethodPatch, base+"/v1/admin/users/"+u.ID, bearer, map[string]string{"status": store.UserActive})
	if code != http.StatusOK || out["notice"] != nil {
		t.Fatalf("admin enable = %d %v, want 200 with no notice", code, out)
	}
	if rs := rows(t); len(rs) != 0 || app.denylist.userBlocked(u.ID) {
		t.Errorf("after the admin enable: rows %+v and refused %v, want none and let in", rs, app.denylist.userBlocked(u.ID))
	}
	recs = identity(t)[before:]
	if len(recs) != 1 || recs[0]["action"] != "user.reactivated" || recs[0]["origin"] != store.RevocationOriginAdmin {
		t.Errorf("the admin enable's records = %v, want one user.reactivated of origin admin", recs)
	}
	if got := len(eventsNaming(t, app, "straza.revocation.lift", "user", u.ID)) - lifts; got != 1 {
		t.Errorf("straza.revocation.lift events = %d, want 1", got)
	}
	if b := block(t); b["locked"] != false {
		t.Errorf("the block after the admin enable = %v, want no lock", b)
	}
	// An enable of an active user that holds no disable lifts nothing and
	// records nothing.
	before = len(identity(t))
	if code := adminReq(t, http.MethodPatch, base+"/v1/admin/users/"+u.ID, bearer, map[string]string{"status": store.UserActive}, nil); code != http.StatusOK {
		t.Fatalf("second admin enable = %d", code)
	}
	if recs := identity(t)[before:]; len(recs) != 0 {
		t.Errorf("a second enable recorded %v, want nothing", recs)
	}
}

// TestAdminEnableLiftsOnlyTheDisable pins what an admin enable lifts: the
// row of a disable in Straza and of a deactivation by the identity manager,
// never a lock. A user with nothing else is let in, with one lift event and
// one user.reactivated. A user a lock still holds stays on the denylist,
// gets no lift event and one user.lift.blocked that names the lock's
// origins, and the answer's notice names each lock and how to lift it.
// Unlock then lifts the rest. A lift the database refuses keeps the user
// refused, says so in the notice, and a disable and enable retries it.
func TestAdminEnableLiftsOnlyTheDisable(t *testing.T) {
	t.Parallel()
	app, base, fs := testAppFault(t)
	ctx := context.Background()
	kim := seedIdentity(t, app)
	grantAdmin(t, app, kim.ID)
	bearer := loginDeviceFlow(t, base, "kim", "hunter2!")

	type lock struct{ reason, origin string }
	cases := []struct {
		name, user string
		// scim marks a user the identity manager deactivated, in place of an
		// admin disable.
		scim  bool
		locks []lock
		// failLift arms a refused delete for the enable's lift.
		failLift bool
		// notice is the enable's notice, %s standing for the request id.
		notice string
	}{
		{name: "a disable only", user: "dora"},
		{name: "a disable and an admin lock", user: "abe", locks: []lock{{"SOC hold pending review", ""}},
			notice: `abe is enabled and the disable is lifted, but Straza still refuses every check-in of abe, because abe is locked. An administrator locked abe with the reason "SOC hold pending review". If the lock should end, run strazactl users unlock abe or press Unlock on abe's page in the console.`},
		{name: "a disable and an external lock", user: "eli", locks: []lock{{"kibana rule straza-denied fired", store.RevocationOriginExternal}},
			notice: `eli is enabled and the disable is lifted, but Straza still refuses every check-in of eli, because eli is locked. An external system locked eli with the reason "kibana rule straza-denied fired". If the lock should end, run strazactl users unlock eli or press Unlock on eli's page in the console.`},
		{name: "a disable and two locks", user: "two", locks: []lock{{"incident 42", ""}, {"SIEM rule 7", store.RevocationOriginExternal}},
			notice: `two is enabled and the disable is lifted, but Straza still refuses every check-in of two, because two is locked. An administrator locked two with the reason "incident 42". An external system locked two with the reason "SIEM rule 7". If the locks should end, run strazactl users unlock two or press Unlock on two's page in the console.`},
		{name: "a deactivation by the identity manager", user: "sam", scim: true},
		{name: "a disable whose lift the database refuses", user: "fay", failLift: true,
			notice: "fay is enabled, but Straza could not lift the disable in its database, so it still refuses every check-in of fay. Disable and enable fay again to retry. The strazad log names the cause under request id %s."},
	}
	rows := func(t *testing.T, id string) []store.Revocation {
		t.Helper()
		rs, err := app.store.Revocations().ListByTarget(ctx, store.RevokeUser, id)
		if err != nil {
			t.Fatal(err)
		}
		return rs
	}
	// records answers the records of the user id on subject whose action
	// is action, or every one when action is "".
	records := func(t *testing.T, subject, id, action string) []map[string]any {
		t.Helper()
		var out []map[string]any
		for _, rec := range eventsNaming(t, app, subject, "user", id) {
			if action == "" || rec["action"] == action {
				out = append(out, rec)
			}
		}
		return out
	}
	status := func(t *testing.T, id, to string) (http.Header, map[string]any) {
		t.Helper()
		code, hdr, out := callJSON(t, http.MethodPatch, base+"/v1/admin/users/"+id, bearer, map[string]string{"status": to})
		if code != http.StatusOK {
			t.Fatalf("status %s = %d %v", to, code, out)
		}
		return hdr, out
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			origin := store.OriginLocal
			if tc.scim {
				origin = store.OriginSCIM
			}
			u, err := app.store.Users().Create(ctx, store.User{Username: tc.user, Origin: origin})
			if err != nil {
				t.Fatal(err)
			}
			if tc.scim {
				if _, err := app.scimDeactivate(ctx, u.ID, "deactivated via SCIM"); err != nil {
					t.Fatal(err)
				}
			} else {
				status(t, u.ID, store.UserDisabled)
				if rs := rows(t, u.ID); len(rs) != 1 || rs[0].Origin != store.RevocationOriginAdminDisable || rs[0].Reason != "user disabled by admin" {
					t.Fatalf("the disable's rows = %+v, want one of origin admin-disable", rs)
				}
				if evs := records(t, "straza.revocation.user", u.ID, ""); len(evs) != 1 || evs[0]["origin"] != store.RevocationOriginAdminDisable {
					t.Errorf("the disable's straza.revocation.user = %v, want one of origin admin-disable", evs)
				}
				if recs := records(t, "straza.audit.identity", u.ID, "user.killed"); len(recs) != 1 || recs[0]["origin"] != store.RevocationOriginAdmin {
					t.Errorf("the disable's user.killed = %v, want one of origin admin", recs)
				}
			}
			for _, l := range tc.locks {
				body := map[string]string{"reason": l.reason}
				if l.origin != "" {
					body["origin"] = l.origin
				}
				if code := adminReq(t, http.MethodPost, base+"/v1/admin/users/"+u.ID+"/lock", bearer, body, nil); code != http.StatusOK {
					t.Fatalf("lock = %d", code)
				}
			}
			lifts := len(records(t, "straza.revocation.lift", u.ID, ""))
			identity := len(records(t, "straza.audit.identity", u.ID, ""))
			updates := len(records(t, "straza.audit.admin", u.ID, actionUserUpdate))

			if tc.failLift {
				fs.arm("revocations.delete", pgDown)
			}
			hdr, out := status(t, u.ID, store.UserActive)
			fs.disarm()

			want := tc.notice
			if tc.failLift {
				want = fmt.Sprintf(tc.notice, hdr.Get(requestIDHeader))
			}
			if out["status"] != store.UserActive || out["id"] != u.ID {
				t.Errorf("answer = %v, want the user active", out)
			}
			if got, _ := out["notice"].(string); got != want {
				t.Errorf("notice = %q, want %q", got, want)
			}
			letIn := tc.notice == ""
			var left []lock
			for _, rv := range rows(t, u.ID) {
				left = append(left, lock{rv.Reason, rv.Origin})
			}
			var wantLeft []lock
			for _, l := range tc.locks {
				if l.origin == "" {
					l.origin = store.RevocationOriginAdmin
				}
				wantLeft = append(wantLeft, l)
			}
			if tc.failLift {
				wantLeft = []lock{{"user disabled by admin", store.RevocationOriginAdminDisable}}
			}
			if !slices.Equal(left, wantLeft) {
				t.Errorf("rows left = %v, want %v", left, wantLeft)
			}
			if got := app.denylist.userBlocked(u.ID); got == letIn {
				t.Errorf("the denylist refuses the user: %v, want %v", got, !letIn)
			}
			wantLifts := 0
			if letIn {
				wantLifts = 1
			}
			if got := len(records(t, "straza.revocation.lift", u.ID, "")) - lifts; got != wantLifts {
				t.Errorf("straza.revocation.lift events = %d, want %d", got, wantLifts)
			}
			if got := len(records(t, "straza.audit.admin", u.ID, actionUserUpdate)) - updates; got != 1 {
				t.Errorf("user.update records = %d, want 1", got)
			}
			// Exactly one record says what the enable did to the user's
			// refusal, and none for a lift the database refused.
			recs := records(t, "straza.audit.identity", u.ID, "")[identity:]
			wantRecs := 1
			if tc.failLift {
				wantRecs = 0
			}
			if len(recs) != wantRecs {
				t.Fatalf("straza.audit.identity records = %v, want %d", recs, wantRecs)
			}
			for _, rec := range recs {
				wantAction := "user.reactivated"
				if len(tc.locks) > 0 {
					wantAction = "user.lift.blocked"
					var held []string
					hs, _ := rec["heldBy"].([]any)
					for _, h := range hs {
						held = append(held, fmt.Sprint(h))
					}
					var wantHeld []string
					for _, l := range wantLeft {
						wantHeld = append(wantHeld, l.origin)
					}
					if !slices.Equal(held, wantHeld) {
						t.Errorf("heldBy = %v, want %v", held, wantHeld)
					}
				}
				if rec["action"] != wantAction || rec["origin"] != store.RevocationOriginAdmin {
					t.Errorf("record = %v, want %s of origin admin", rec, wantAction)
				}
			}

			switch {
			case len(tc.locks) > 0:
				// Unlock lifts the locks the enable left.
				if code := adminReq(t, http.MethodPost, base+"/v1/admin/users/"+u.ID+"/unlock", bearer, nil, nil); code != http.StatusOK {
					t.Fatalf("unlock = %d", code)
				}
				if rs := rows(t, u.ID); len(rs) != 0 || app.denylist.userBlocked(u.ID) {
					t.Errorf("after unlock: rows %+v and refused %v, want none and let in", rs, app.denylist.userBlocked(u.ID))
				}
			case tc.failLift:
				// The retry the notice names lets the user in.
				status(t, u.ID, store.UserDisabled)
				if _, out := status(t, u.ID, store.UserActive); out["notice"] != nil {
					t.Errorf("the retry's notice = %v, want none", out["notice"])
				}
				if rs := rows(t, u.ID); len(rs) != 0 || app.denylist.userBlocked(u.ID) {
					t.Errorf("after the retry: rows %+v and refused %v, want none and let in", rs, app.denylist.userBlocked(u.ID))
				}
			}
		})
	}
}
