package server

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"testing"

	"github.com/strazahq/straza/internal/authn"
	"github.com/strazahq/straza/internal/store"
)

// TestAdminRefusesPasswordOnNonPerson pins that the admin API sets a
// password on a person only. A create of an AI agent or a service account
// with a password, and a PATCH that sets a password on one, or that makes a
// person an agent and sets a password in the same request, answer 400 with
// the sentence that says why and how such a user signs in, and write
// nothing. A person's password is still set. An agent that holds a
// password from an older version is refused at the password page, with
// exactly one login failure record that names it and the reason.
func TestAdminRefusesPasswordOnNonPerson(t *testing.T) {
	t.Parallel()
	app, base := testApp(t)
	kim := seedIdentity(t, app)
	grantAdmin(t, app, kim.ID)
	admin, _ := checkinToken(t, app, base)
	ctx := context.Background()
	made := 0
	user := func(t *testing.T, u store.User) store.User {
		t.Helper()
		made++
		u.Username = fmt.Sprintf("b303-%d", made)
		created, err := app.store.Users().Create(ctx, u)
		if err != nil {
			t.Fatal(err)
		}
		return created
	}

	t.Run("a create", func(t *testing.T) {
		cases := []struct {
			name, userType string
			status         int
			word           string
		}{
			{"of an agent", "agent", http.StatusBadRequest, "an agent"},
			{"of a service account", "service", http.StatusBadRequest, "a service account"},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				name := "b303-create-" + tc.userType
				code, _, out := callJSON(t, http.MethodPost, base+"/v1/admin/users", admin,
					map[string]string{"username": name, "kind": "nhi", "user_type": tc.userType, "password": "hunter2!"})
				want := fmt.Sprintf(nonPersonPasswordCreateMsg, name, tc.word, name)
				if code != tc.status || out["error"] != want {
					t.Fatalf("the create = %d %v, want %d %q", code, out, tc.status, want)
				}
				if _, err := app.store.Users().GetByUsername(ctx, name); err == nil {
					t.Errorf("the refused create stored the user %s", name)
				}
			})
		}
		code, _, out := callJSON(t, http.MethodPost, base+"/v1/admin/users", admin,
			map[string]string{"username": "b303-person", "password": "hunter2!"})
		if code != http.StatusCreated {
			t.Fatalf("a person's create with a password = %d %v, want 201", code, out)
		}
	})

	t.Run("a PATCH", func(t *testing.T) {
		cases := []struct {
			name   string
			user   store.User
			body   map[string]string
			status int
			word   string
		}{
			{"of an agent", store.User{UserType: store.UserTypeAgent}, map[string]string{"password": "hunter2!"}, http.StatusBadRequest, "an agent"},
			{"of a service account", store.User{UserType: store.UserTypeService}, map[string]string{"password": "hunter2!"}, http.StatusBadRequest, "a service account"},
			{"of an untyped user of kind nhi", store.User{Attrs: `{"kind":"nhi"}`}, map[string]string{"password": "hunter2!"}, http.StatusBadRequest, "an agent"},
			{"that makes a person an agent", store.User{}, map[string]string{"user_type": "agent", "password": "hunter2!"}, http.StatusBadRequest, "an agent"},
			{"of a person", store.User{}, map[string]string{"password": "hunter2!"}, http.StatusOK, ""},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				u := user(t, tc.user)
				code, _, out := callJSON(t, http.MethodPatch, base+"/v1/admin/users/"+u.ID, admin, tc.body)
				after, err := app.store.Users().GetByID(ctx, u.ID)
				if err != nil {
					t.Fatal(err)
				}
				if tc.status == http.StatusOK {
					if code != http.StatusOK || after.PasswordHash == "" {
						t.Fatalf("a person's PATCH = %d %v with hash set %v, want 200 and a hash", code, out, after.PasswordHash != "")
					}
					return
				}
				want := fmt.Sprintf(nonPersonPasswordPatchMsg, u.Username, tc.word, u.Username)
				if code != tc.status || out["error"] != want {
					t.Fatalf("the PATCH = %d %v, want %d %q", code, out, tc.status, want)
				}
				if after.PasswordHash != "" || after.UserType != u.UserType {
					t.Errorf("the refused PATCH wrote the row: hash set %v, type %q, want no hash and type %q",
						after.PasswordHash != "", after.UserType, u.UserType)
				}
			})
		}
	})

	t.Run("a retype to a non-person", func(t *testing.T) {
		hash, err := authn.HashPassword("hunter2!")
		if err != nil {
			t.Fatal(err)
		}
		cases := []struct {
			name   string
			retype func(t *testing.T, u store.User)
		}{
			{"over the admin API", func(t *testing.T, u store.User) {
				if code, _, out := callJSON(t, http.MethodPatch, base+"/v1/admin/users/"+u.ID, admin, map[string]string{"user_type": "agent"}); code != http.StatusOK {
					t.Fatalf("the PATCH = %d %v", code, out)
				}
			}},
			{"over SCIM", func(t *testing.T, u store.User) {
				scim := mintProvisioningToken(t, base, loginDeviceFlow(t, base, "kim", "hunter2!"))
				scimPatch(t, base, scim, u.ID, `{"schemas":["urn:ietf:params:scim:api:messages:2.0:PatchOp"],"Operations":[{"op":"replace","path":"userType","value":"agent"}]}`)
			}},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				u := user(t, store.User{PasswordHash: hash})
				tc.retype(t, u)
				after, err := app.store.Users().GetByID(ctx, u.ID)
				if err != nil {
					t.Fatal(err)
				}
				if after.UserType != store.UserTypeAgent || after.PasswordHash != "" {
					t.Errorf("after the retype the user is %q with a hash set %v, want agent and no hash", after.UserType, after.PasswordHash != "")
				}
				var changed []any
				for _, ev := range adminAuditEvents(t, app) {
					if ev["action"] == actionUserUpdate && ev["target"] == u.ID {
						changed, _ = ev["changed"].([]any)
					}
				}
				if !slices.Contains(changed, any("password")) || !slices.Contains(changed, any("user_type")) {
					t.Errorf("the user.update record names %v, want password and user_type", changed)
				}
			})
		}
	})

	t.Run("the password page", func(t *testing.T) {
		hash, err := authn.HashPassword("hunter2!")
		if err != nil {
			t.Fatal(err)
		}
		bot := user(t, store.User{UserType: store.UserTypeAgent, PasswordHash: hash})
		resp, err := http.PostForm(base+"/oidc/device_authorization", url.Values{"client_id": {"straza"}})
		if err != nil {
			t.Fatal(err)
		}
		var auth map[string]any
		err = json.NewDecoder(resp.Body).Decode(&auth)
		_ = resp.Body.Close()
		if err != nil {
			t.Fatal(err)
		}
		resp, err = http.PostForm(base+"/oidc/device", url.Values{
			"user_code": {auth["user_code"].(string)}, "username": {bot.Username}, "password": {"hunter2!"},
		})
		if err != nil {
			t.Fatal(err)
		}
		page, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if strings.Contains(string(page), "Signed in") {
			t.Errorf("the password page signed the agent in:\n%s", page)
		}
		n := 0
		for _, d := range outboxDataFor(t, app, "straza.audit.authn") {
			if d["action"] == "login" && d["outcome"] == "failure" && d["via"] == "password" && d["user"] == bot.Username &&
				d["reason"] == authn.ReasonNonPersonPassword {
				n++
			}
		}
		if n != 1 {
			t.Errorf("%d password login failures name the agent, want 1", n)
		}
	})
}
