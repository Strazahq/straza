package server

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/strazahq/straza/internal/scim"
)

// TestSCIMDeactivationRemovesPassword pins that an identity manager's
// deactivation removes the user's local password with the grants: a user
// provisioned over SCIM, given a password and signed in with it, is
// deactivated by a PATCH of active false, a PUT with active false, a DELETE
// or the Entra read, and after an admin enables the user again the old
// password gets the ordinary wrong-password answer. An admin's own disable
// keeps the password.
func TestSCIMDeactivationRemovesPassword(t *testing.T) {
	t.Parallel()
	app, base := testApp(t)
	kim := seedIdentity(t, app)
	grantAdmin(t, app, kim.ID)
	bearer := loginDeviceFlow(t, base, "kim", "hunter2!")
	token := mintProvisioningToken(t, base, bearer)
	ctx := context.Background()
	const password = "correct-horse-7"

	scimDeactivation := func(method string, body func(username string) string, want int) func(*testing.T, string, string) {
		return func(t *testing.T, id, username string) {
			if code, _, out := scimSend(t, method, base+"/scim/v2/Users/"+id, token, body(username)); code != want {
				t.Fatalf("SCIM %s = %d %v, want %d", method, code, out, want)
			}
		}
	}
	setStatus := func(t *testing.T, id, status string) {
		t.Helper()
		if code := adminReq(t, http.MethodPatch, base+"/v1/admin/users/"+id, bearer, map[string]any{"status": status}, nil); code != http.StatusOK {
			t.Fatalf("admin PATCH status %s = %d", status, code)
		}
	}
	cases := []struct {
		name       string
		deactivate func(t *testing.T, id, username string)
		keeps      bool
	}{
		{name: "SCIM PATCH active false", deactivate: scimDeactivation(http.MethodPatch, func(string) string {
			return `{"schemas":["` + scim.SchemaPatch + `"],"Operations":[{"op":"replace","path":"active","value":false}]}`
		}, http.StatusOK)},
		{name: "SCIM PUT with active false", deactivate: scimDeactivation(http.MethodPut, func(username string) string {
			return `{"schemas":["` + scim.SchemaUser + `"],"userName":"` + username + `","externalId":"idm-` + username + `","active":false}`
		}, http.StatusOK)},
		{name: "SCIM DELETE", deactivate: scimDeactivation(http.MethodDelete, func(string) string { return "" }, http.StatusNoContent)},
		{name: "the Entra read's deactivation", deactivate: func(t *testing.T, id, _ string) {
			if _, err := app.entraPeople().Disable(ctx, id, "the account is disabled in Entra"); err != nil {
				t.Fatalf("Entra deactivation: %v", err)
			}
		}},
		{name: "an admin's own disable", keeps: true, deactivate: func(t *testing.T, id, _ string) { setStatus(t, id, "disabled") }},
	}
	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			username := "pat" + string(rune('a'+i))
			id := scimCreateUser(t, base, token,
				`{"schemas":["`+scim.SchemaUser+`"],"userName":"`+username+`","externalId":"idm-`+username+`","active":true}`)
			if code := adminReq(t, http.MethodPatch, base+"/v1/admin/users/"+id, bearer, map[string]any{"password": password}, nil); code != http.StatusOK {
				t.Fatalf("admin PATCH password = %d", code)
			}
			if page := submitEmergencyLogin(t, base, username, password); !strings.Contains(page, "Signed in") {
				t.Fatalf("the new password does not sign in:\n%s", page)
			}
			tc.deactivate(t, id, username)
			setStatus(t, id, "active")
			page := submitEmergencyLogin(t, base, username, password)
			row, err := app.store.Users().GetByID(ctx, id)
			if err != nil {
				t.Fatal(err)
			}
			if tc.keeps {
				if !strings.Contains(page, "Signed in") || row.PasswordHash == "" {
					t.Errorf("after an admin's disable and enable the password does not sign in (hash kept %v):\n%s", row.PasswordHash != "", page)
				}
				return
			}
			if strings.Contains(page, "Signed in") || !strings.Contains(page, "Invalid username or password.") {
				t.Errorf("after the deactivation and an admin enable the old password reads:\n%s\nwant the wrong-password answer", page)
			}
			if row.PasswordHash != "" {
				t.Error("the user row still holds a password hash after the deactivation")
			}
		})
	}
}
