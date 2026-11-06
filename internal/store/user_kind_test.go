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
