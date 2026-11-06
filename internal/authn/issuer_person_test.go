package authn

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/strazahq/straza/internal/store"
	"github.com/strazahq/straza/internal/store/storetest"
)

// TestPasswordPageRefusesNonPersons pins that the password page signs in a
// person only. An AI agent or a service account that holds a password,
// typed or marked kind nhi, is refused with the right password and with a
// wrong one. The right password gets the sentence that names the key lane,
// and a wrong one the usual answer. Each refusal fires OnLoginFailed once
// with the account's username and a reason that names the key lane only
// when the password matched, and the grant stays pending, so no ID token is
// minted.
func TestPasswordPageRefusesNonPersons(t *testing.T) {
	cases := []struct {
		name     string
		user     store.User
		password string
		page     string
		reason   string
	}{
		{"an agent with its password", store.User{Username: "bot", UserType: store.UserTypeAgent}, "hunter2!", nonPersonPasswordRefused, ReasonNonPersonPassword},
		{"a service account with its password", store.User{Username: "svc", UserType: store.UserTypeService}, "hunter2!", nonPersonPasswordRefused, ReasonNonPersonPassword},
		{"an untyped nhi with its password", store.User{Username: "nhi", Attrs: `{"kind":"nhi"}`}, "hunter2!", nonPersonPasswordRefused, ReasonNonPersonPassword},
		{"an agent with a wrong password", store.User{Username: "bot", UserType: store.UserTypeAgent}, "nope", "Invalid username or password.", ReasonBadPassword},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newIssuerFixture(t)
			hash, err := storetest.PasswordHash("hunter2!")
			if err != nil {
				t.Fatal(err)
			}
			tc.user.PasswordHash = hash
			if _, err := f.users.Create(context.Background(), tc.user); err != nil {
				t.Fatal(err)
			}
			var fired []string
			f.issuer.OnLoginFailed = func(username, reason string, r *http.Request) {
				if r == nil {
					t.Error("OnLoginFailed request = nil, want the submit request")
				}
				fired = append(fired, username+": "+reason)
			}
			_, auth := f.postForm(t, "/oidc/device_authorization", url.Values{"client_id": {"straza"}})
			page := f.submitLogin(t, auth["user_code"].(string), tc.user.Username, tc.password)
			if !strings.Contains(page, tc.page) {
				t.Errorf("the page does not say %q:\n%s", tc.page, page)
			}
			if want := tc.user.Username + ": " + tc.reason; len(fired) != 1 || fired[0] != want {
				t.Errorf("OnLoginFailed fired with %q, want once with %q", fired, want)
			}
			if code, out := f.pollToken(t, auth["device_code"].(string), "straza"); out["error"] != "authorization_pending" {
				t.Errorf("the token poll = %d with error %v, want authorization_pending", code, out["error"])
			}
		})
	}
}
