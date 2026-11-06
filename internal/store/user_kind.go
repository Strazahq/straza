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
