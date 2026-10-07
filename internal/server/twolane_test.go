package server

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"testing"

	"github.com/strazahq/straza/internal/store"
)

func jsonBody(t *testing.T, v any) io.Reader {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return bytes.NewReader(raw)
}

// TestTwoLaneActivation pins two-lane activation: effective access =
// IdM lane (users.status over SCIM `active`) AND Straza lane (origin-aware
// revocations). The reactivation lift is scim-origin-selective, so a
// console/SOAR lock survives any IdM enable or reconciliation.
func TestTwoLaneActivation(t *testing.T) {
	t.Parallel()
	app, base := testApp(t)
	ctx := context.Background()
	admin := seedIdentity(t, app)
	grantAdmin(t, app, admin.ID)
	adminBearer := loginDeviceFlow(t, base, "kim", "hunter2!")
	scimToken := mintProvisioningToken(t, base, adminBearer)

	target, err := app.store.Users().Create(ctx, store.User{
		Username: "mara", Origin: store.OriginSCIM, Status: store.UserActive,
	})
	if err != nil {
		t.Fatal(err)
	}

	scimActive := func(active string) int {
		req, _ := http.NewRequest("PATCH", base+"/scim/v2/Users/"+target.ID,
			jsonBody(t, map[string]any{
				"schemas":    []string{"urn:ietf:params:scim:api:messages:2.0:PatchOp"},
				"Operations": []map[string]any{{"op": "replace", "path": "active", "value": active == "true"}},
			}))
		req.Header.Set("Authorization", "Bearer "+scimToken)
		req.Header.Set("Content-Type", "application/scim+json")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = resp.Body.Close() }()
		return resp.StatusCode
	}
	rows := func() []store.Revocation {
		t.Helper()
		rs, err := app.store.Revocations().ListByTarget(ctx, store.RevokeUser, target.ID)
		if err != nil {
			t.Fatal(err)
		}
		return rs
	}
	status := func() string {
		t.Helper()
		u, err := app.store.Users().GetByID(ctx, target.ID)
		if err != nil {
			t.Fatal(err)
		}
		return u.Status
	}

	// Baseline: IdM disable kills, IdM enable fully lifts, because every
	// row involved is scim-origin.
	if code := scimActive("false"); code != http.StatusOK {
		t.Fatalf("scim disable = %d", code)
	}
	if !app.denylist.userBlocked(target.ID) {
		t.Fatal("scim disable did not deny the user")
	}
	if rs := rows(); len(rs) != 1 || rs[0].Origin != store.RevocationOriginSCIM {
		t.Fatalf("scim disable rows = %+v, want one scim-origin row", rs)
	}
	if code := scimActive("true"); code != http.StatusOK {
		t.Fatalf("scim enable = %d", code)
	}
	if app.denylist.userBlocked(target.ID) {
		t.Fatal("scim enable did not lift a scim-only revocation")
	}
	if rs := rows(); len(rs) != 0 {
		t.Fatalf("after scim enable: %d rows, want 0", len(rs))
	}

	// Lock (the Straza lane): denies immediately, leaves users.status ALONE;
	// the two lanes are independent, an admin lock is not an IdM disable.
	var lockOut map[string]any
	code := adminReq(t, "POST", base+"/v1/admin/users/"+target.ID+"/lock", adminBearer,
		map[string]string{"reason": "SOC hold pending review"}, &lockOut)
	if code != http.StatusOK {
		t.Fatalf("lock = %d %v", code, lockOut)
	}
	if !app.denylist.userBlocked(target.ID) {
		t.Fatal("lock did not deny the user")
	}
	if got := status(); got != store.UserActive {
		t.Fatalf("lock flipped users.status to %q: the lanes must stay independent", got)
	}
	if rs := rows(); len(rs) != 1 || rs[0].Origin != store.RevocationOriginAdmin {
		t.Fatalf("lock rows = %+v, want one admin-origin row", rs)
	}

	// An IdM disable + enable cycle around a live lock must not lift it.
	if code := scimActive("false"); code != http.StatusOK {
		t.Fatalf("scim disable (locked) = %d", code)
	}
	if code := scimActive("true"); code != http.StatusOK {
		t.Fatalf("scim enable (locked) = %d", code)
	}
	if got := status(); got != store.UserActive {
		t.Fatalf("IdM lane status = %q, want active (the IdM keeps mastering its lane)", got)
	}
	if !app.denylist.userBlocked(target.ID) {
		t.Fatal("SCIM reactivation lifted an admin lock: the precedence inversion is back")
	}
	if rs := rows(); len(rs) != 1 || rs[0].Origin != store.RevocationOriginAdmin {
		t.Fatalf("after IdM cycle rows = %+v, want exactly the admin lock", rs)
	}

	// The IdM SEES the lock (read-only extension, spec/scim-profile §3.1):
	// GET renders the block, and any PATCH against it is a mutability error.
	scimGet := func() map[string]any {
		t.Helper()
		req, _ := http.NewRequest("GET", base+"/scim/v2/Users/"+target.ID, nil)
		req.Header.Set("Authorization", "Bearer "+scimToken)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = resp.Body.Close() }()
		var body map[string]any
		if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		return body
	}
	const lockURN = "urn:straza:params:scim:schemas:extension:2.0:User"
	got := scimGet()
	block, _ := got[lockURN].(map[string]any)
	if block == nil || block["locked"] != true || block["lockOrigin"] != "admin" || block["lockReason"] != "SOC hold pending review" {
		t.Fatalf("locked user SCIM render = %v, want lock block with locked/origin/reason", got[lockURN])
	}
	lockPatch := func(path string) int {
		req, _ := http.NewRequest("PATCH", base+"/scim/v2/Users/"+target.ID,
			jsonBody(t, map[string]any{
				"schemas":    []string{"urn:ietf:params:scim:api:messages:2.0:PatchOp"},
				"Operations": []map[string]any{{"op": "replace", "path": path, "value": false}},
			}))
		req.Header.Set("Authorization", "Bearer "+scimToken)
		req.Header.Set("Content-Type", "application/scim+json")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = resp.Body.Close() }()
		return resp.StatusCode
	}
	for _, path := range []string{"locked", lockURN + ":locked", lockURN} {
		if code := lockPatch(path); code != http.StatusBadRequest {
			t.Fatalf("PATCH %q = %d, want 400 (read-only lock block)", path, code)
		}
	}
	if !app.denylist.userBlocked(target.ID) {
		t.Fatal("a refused PATCH must not have side effects on the lock")
	}

	// Unlock is the explicit Straza action that lifts everything.
	code = adminReq(t, "POST", base+"/v1/admin/users/"+target.ID+"/unlock", adminBearer, nil, &lockOut)
	if code != http.StatusOK {
		t.Fatalf("unlock = %d %v", code, lockOut)
	}
	if app.denylist.userBlocked(target.ID) {
		t.Fatal("unlock did not lift the lock")
	}
	if rs := rows(); len(rs) != 0 {
		t.Fatalf("after unlock: %d rows, want 0", len(rs))
	}

	// External origin (SOAR/SIEM automation) behaves like admin for survival.
	code = adminReq(t, "POST", base+"/v1/admin/users/"+target.ID+"/lock", adminBearer,
		map[string]string{"reason": "kibana rule straza-denied fired", "origin": "external"}, &lockOut)
	if code != http.StatusOK {
		t.Fatalf("external lock = %d %v", code, lockOut)
	}
	if code := scimActive("false"); code != http.StatusOK {
		t.Fatalf("scim disable (ext-locked) = %d", code)
	}
	if code := scimActive("true"); code != http.StatusOK {
		t.Fatalf("scim enable (ext-locked) = %d", code)
	}
	if !app.denylist.userBlocked(target.ID) {
		t.Fatal("SCIM reactivation lifted an external lock")
	}
	if rs := rows(); len(rs) != 1 || rs[0].Origin != store.RevocationOriginExternal {
		t.Fatalf("external rows = %+v", rs)
	}
	// Admin user-enable (the console Enable button) is ALSO an explicit
	// Straza action: it keeps its full-lift semantics.
	code = adminReq(t, "PATCH", base+"/v1/admin/users/"+target.ID, adminBearer,
		map[string]string{"status": "active"}, &lockOut)
	if code != http.StatusOK {
		t.Fatalf("admin enable = %d %v", code, lockOut)
	}
	// Status was already active, so no reactivation ran; unlock instead.
	code = adminReq(t, "POST", base+"/v1/admin/users/"+target.ID+"/unlock", adminBearer, nil, &lockOut)
	if code != http.StatusOK {
		t.Fatalf("unlock (external) = %d", code)
	}
	if app.denylist.userBlocked(target.ID) {
		t.Fatal("unlock did not lift the external lock")
	}
}

// TestUserLockValidation pins the lock/unlock endpoint contract.
func TestUserLockValidation(t *testing.T) {
	t.Parallel()
	app, base := testApp(t)
	admin := seedIdentity(t, app)
	grantAdmin(t, app, admin.ID)
	adminBearer := loginDeviceFlow(t, base, "kim", "hunter2!")

	cases := []struct {
		name   string
		path   string
		bearer string
		body   any
		want   int
	}{
		{"missing reason", "/v1/admin/users/" + admin.ID + "/lock", adminBearer, map[string]string{}, http.StatusBadRequest},
		{"bad origin", "/v1/admin/users/" + admin.ID + "/lock", adminBearer, map[string]string{"reason": "r", "origin": "scim"}, http.StatusBadRequest},
		{"unknown user lock", "/v1/admin/users/nope/lock", adminBearer, map[string]string{"reason": "r"}, http.StatusNotFound},
		{"unknown user unlock", "/v1/admin/users/nope/unlock", adminBearer, nil, http.StatusNotFound},
		{"unauthenticated", "/v1/admin/users/" + admin.ID + "/lock", "", map[string]string{"reason": "r"}, http.StatusUnauthorized},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var out map[string]any
			if code := adminReq(t, "POST", base+tc.path, tc.bearer, tc.body, &out); code != tc.want {
				t.Fatalf("code = %d, want %d (%v)", code, tc.want, out)
			}
		})
	}
}
