package store

import "encoding/json"

// IsNHI reports whether u is a non-human identity, an AI agent or a service
// account. Either of two signals is enough: the type agent or service, which
// an identity manager masters, and the create-time attrs kind nhi, which an
// admin create or the agentic SCIM extension sets and which a typed write
// never clears.
func (u User) IsNHI() bool {
	if u.UserType == UserTypeAgent || u.UserType == UserTypeService {
		return true
	}
	if u.Attrs == "" {
		return false
	}
	var attrs map[string]any
	return json.Unmarshal([]byte(u.Attrs), &attrs) == nil && attrs["kind"] == "nhi"
}

// The three words for what a user is, as the admin answers send them.
const (
	UserKindPerson  = "person"
	UserKindAgent   = "agent"
	UserKindService = "service"
)

// Kind says what u is, in one of the three UserKind words: service when its
// type is service, agent when its type is agent or its attrs kind is nhi,
// and person otherwise. It is the rule the console's kind word follows, and
// the users list's kind filter is its SQL twin.
func (u User) Kind() string {
	switch {
	case u.UserType == UserTypeService:
		return UserKindService
	case u.IsNHI():
		return UserKindAgent
	}
	return UserKindPerson
}
