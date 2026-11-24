package store

import "testing"

// TestUserIsNHI pins the two signals that make a user a non-human identity,
// either one alone: the type agent or service, and the create-time attrs
// kind nhi. A person, an untyped user and attrs that do not parse are not.
func TestUserIsNHI(t *testing.T) {
	cases := []struct {
		name string
		user User
		want bool
	}{
		{"an agent", User{UserType: UserTypeAgent}, true},
		{"a service account", User{UserType: UserTypeService}, true},
		{"an untyped user of kind nhi", User{Attrs: `{"kind":"nhi"}`}, true},
		{"a person typed human", User{UserType: UserTypeHuman}, false},
		{"an untyped user", User{}, false},
		{"attrs of another kind", User{Attrs: `{"kind":"human"}`}, false},
		{"attrs that do not parse", User{Attrs: `{"kind":`}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.user.IsNHI(); got != tc.want {
				t.Errorf("IsNHI() = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestUserKind pins the three-way kind over every pair of a type and an
// attrs kind: the type service wins, then the type agent or the attrs kind
// nhi make an agent, and every other pair is a person. Attrs that do not
// parse count as no attrs kind.
func TestUserKind(t *testing.T) {
	const nhi, human, broken = `{"kind":"nhi"}`, `{"kind":"human"}`, `{"kind":`
	cases := []struct {
		userType, attrs, want string
	}{
		{"", "", UserKindPerson},
		{"", nhi, UserKindAgent},
		{"", human, UserKindPerson},
		{"", broken, UserKindPerson},
		{UserTypeHuman, "", UserKindPerson},
		{UserTypeHuman, nhi, UserKindAgent},
		{UserTypeHuman, human, UserKindPerson},
		{UserTypeHuman, broken, UserKindPerson},
		{UserTypeAgent, "", UserKindAgent},
		{UserTypeAgent, nhi, UserKindAgent},
		{UserTypeAgent, human, UserKindAgent},
		{UserTypeAgent, broken, UserKindAgent},
		{UserTypeService, "", UserKindService},
		{UserTypeService, nhi, UserKindService},
		{UserTypeService, human, UserKindService},
		{UserTypeService, broken, UserKindService},
	}
	for _, tc := range cases {
		t.Run("type "+tc.userType+" attrs "+tc.attrs, func(t *testing.T) {
			if got := (User{UserType: tc.userType, Attrs: tc.attrs}).Kind(); got != tc.want {
				t.Errorf("Kind() = %q, want %q", got, tc.want)
			}
		})
	}
}
