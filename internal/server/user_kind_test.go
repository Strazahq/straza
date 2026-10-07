package server

import (
	"testing"

	"github.com/strazahq/straza/internal/store"
)

// TestUserKindTypologyDerivation pins the read-side half of the typed-agent
// classification: userKind derives "nhi" from the IdM-mastered typology
// (userType agent|service) when attrs.kind was never set, so every consumer
// of the kind field (console badge fallback, admin API rows, the pull
// connector's kind attribute and therefore the IdM's mirror) reads the same
// classification the approval engine enforces. attrs.kind=nhi keeps
// winning where present; nothing is migrated or backfilled.
func TestUserKindTypologyDerivation(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		u    store.User
		want string
	}{
		{"legacy attrs kind=nhi", store.User{Attrs: `{"kind":"nhi"}`}, "nhi"},
		{"typology agent, no attrs", store.User{UserType: store.UserTypeAgent}, "nhi"},
		{"typology service, no attrs", store.User{UserType: store.UserTypeService}, "nhi"},
		{"typology human", store.User{UserType: store.UserTypeHuman}, "human"},
		{"unclassified", store.User{}, "human"},
		{"typology agent beside unrelated attrs", store.User{Attrs: `{"team":"x"}`, UserType: store.UserTypeAgent}, "nhi"},
		{"typology agent beside malformed attrs", store.User{Attrs: `{`, UserType: store.UserTypeAgent}, "nhi"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := userKind(tc.u); got != tc.want {
				t.Fatalf("userKind(%+v) = %q, want %q", tc.u, got, tc.want)
			}
		})
	}
}
