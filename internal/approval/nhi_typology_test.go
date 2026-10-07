package approval

import (
	"testing"

	"github.com/strazahq/straza/internal/store"
)

// TestUserIsNHITypology pins the two-signal NHI classification: attrs
// kind=nhi OR a non-human userType
// (agent|service) marks the identity non-human. The OR is deliberate and
// fail-closed: when the two mechanisms disagree, the non-human signal wins,
// so a typology-typed agent can never become an approval decider just
// because its creation lane could not send the agentic SCIM URN.
func TestUserIsNHITypology(t *testing.T) {
	cases := []struct {
		name string
		u    store.User
		want bool
	}{
		{"legacy attrs kind=nhi", store.User{Attrs: `{"kind":"nhi"}`}, true},
		{"typology agent, no attrs", store.User{UserType: store.UserTypeAgent}, true},
		{"typology service, no attrs", store.User{UserType: store.UserTypeService}, true},
		{"typology human", store.User{UserType: store.UserTypeHuman}, false},
		{"unclassified", store.User{}, false},
		{"attrs nhi beside typology human still blocks", store.User{Attrs: `{"kind":"nhi"}`, UserType: store.UserTypeHuman}, true},
		{"typology agent beside unrelated attrs", store.User{Attrs: `{"team":"x"}`, UserType: store.UserTypeAgent}, true},
		{"typology agent beside malformed attrs", store.User{Attrs: `{`, UserType: store.UserTypeAgent}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := userIsNHI(tc.u); got != tc.want {
				t.Fatalf("userIsNHI(%+v) = %v, want %v", tc.u, got, tc.want)
			}
		})
	}
}
