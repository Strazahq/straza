package scim

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/strazahq/straza/internal/store"
)

// TestAgenticCreateUserType pins the userType an agentic create stores: an
// agentic create that names no userType is stored as an agent, one that
// names a type keeps it, and a create without the agentic URN stays
// unclassified.
func TestAgenticCreateUserType(t *testing.T) {
	s, st, _ := wireGroupsServer(t)
	mux := http.NewServeMux()
	s.Routes(mux)
	const core = `"urn:ietf:params:scim:schemas:core:2.0:User"`
	const agentic = `"urn:ietf:params:scim:schemas:extension:agent:2.0:Agent"`
	cases := []struct {
		name, body, username, want string
	}{
		{"agentic, no userType", `{"schemas":[` + core + `,` + agentic + `],"userName":"ci-agent"}`, "ci-agent", store.UserTypeAgent},
		{"agentic, userType service", `{"schemas":[` + core + `,` + agentic + `],"userName":"ci-svc","userType":"service"}`, "ci-svc", store.UserTypeService},
		{"agentic, userType human", `{"schemas":[` + core + `,` + agentic + `],"userName":"ci-odd","userType":"human"}`, "ci-odd", store.UserTypeHuman},
		{"no agentic URN, no userType", `{"schemas":[` + core + `],"userName":"dana"}`, "dana", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := scimReq(t, mux, http.MethodPost, "/scim/v2/Users", tc.body)
			if rec.Code != http.StatusCreated {
				t.Fatalf("create = %d %s, want 201", rec.Code, rec.Body)
			}
			var res map[string]any
			if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
				t.Fatal(err)
			}
			if got, _ := res["userType"].(string); got != tc.want {
				t.Fatalf("answer userType = %q, want %q", got, tc.want)
			}
			u, err := st.Users().GetByUsername(context.Background(), tc.username)
			if err != nil {
				t.Fatal(err)
			}
			if u.UserType != tc.want {
				t.Fatalf("stored user_type = %q, want %q", u.UserType, tc.want)
			}
		})
	}
}
