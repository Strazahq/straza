package tokenscopes

import (
	"strings"
	"testing"
)

// TestTokenScopeParse pins the scope grammar: full | area:verb list,
// canonical sorted storage form, the retired `read` word refused with the
// migration hint, and reserved qualifier syntax refused until implemented.
func TestTokenScopeParse(t *testing.T) {
	for _, tc := range []struct {
		in        string
		wantErr   string // substring; "" = must parse
		canonical string // String() when parsed
	}{
		{"full", "", "full"},
		{"identity:read", "", "identity:read"},
		{"changes:read , identity:read", "", "changes:read,identity:read"},
		{"identity:read,identity:read", "", "identity:read"},
		{"tokens:write", "", "tokens:write"},
		{"scim:write", "", "scim:write"},
		{"scim:read,apps:read", "", "apps:read,scim:read"},
		{"drafts:read", "", "drafts:read"},
		{"drafts:write,apps:read", "", "apps:read,drafts:write"},
		{"", "scope is required", ""},
		{"read", "retired", ""},
		{"root", `invalid grant "root"`, ""},
		{"identity", `invalid grant "identity"`, ""},
		{"identity:admin", `invalid grant "identity:admin"`, ""},
		{"nonsense:read", `invalid grant "nonsense:read"`, ""},
		{"apps/github:read", "invalid grant", ""}, // reserved, not implemented
		{"identity:read,,changes:read", "invalid grant", ""},
	} {
		ts, err := Parse(tc.in)
		if tc.wantErr != "" {
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("parse(%q) err = %v, want containing %q", tc.in, err, tc.wantErr)
			}
			continue
		}
		if err != nil {
			t.Errorf("parse(%q) unexpected err: %v", tc.in, err)
			continue
		}
		if got := ts.String(); got != tc.canonical {
			t.Errorf("parse(%q).String() = %q, want %q", tc.in, got, tc.canonical)
		}
	}
}

// TestDraftRoutesNeedTheDraftsGrant pins the drafts area of the config
// drafts routes: a GET needs drafts:read and every other method
// drafts:write, a grant on the areas a draft changes opens none of them,
// and full opens all.
func TestDraftRoutesNeedTheDraftsGrant(t *testing.T) {
	routes := []string{
		"GET /v1/admin/drafts", "POST /v1/admin/drafts", "POST /v1/admin/drafts/check",
		"GET /v1/admin/drafts/{id}", "PUT /v1/admin/drafts/{id}",
		"POST /v1/admin/drafts/{id}/discard", "POST /v1/admin/drafts/{id}/revert",
		"POST /v1/admin/drafts/{id}/publish", "POST /v1/admin/drafts/{id}/rebase",
		"POST /v1/admin/drafts/{id}/contact",
	}
	for _, route := range routes {
		method, _, _ := strings.Cut(route, " ")
		verb := "write"
		if method == "GET" {
			verb = "read"
		}
		for _, tc := range []struct {
			scope string
			want  bool
		}{
			{"drafts:" + verb, true},
			{"apps:write,identity:write,policy:write", false},
			{"full", true},
		} {
			ts, err := Parse(tc.scope)
			if err != nil {
				t.Fatalf("parse(%q): %v", tc.scope, err)
			}
			got, needed, mapped := ts.Allows(method, route)
			if !mapped {
				t.Fatalf("%s is not mapped to an area", route)
			}
			if got != tc.want {
				t.Errorf("%s under %q: allowed %v, want %v", route, tc.scope, got, tc.want)
			}
			if !tc.want && needed != "drafts:"+verb {
				t.Errorf("%s under %q names %q as missing, want drafts:%s", route, tc.scope, needed, verb)
			}
		}
		other := map[string]string{"read": "write", "write": "read"}[verb]
		ts, _ := Parse("drafts:" + other)
		if got, _, _ := ts.Allows(method, route); got {
			t.Errorf("%s is open to drafts:%s, want drafts:%s only", route, other, verb)
		}
	}
}

// TestScopeHintNamesEveryArea pins that the hint a refused scope reads
// names every area a grant may carry, so an operator can find a new one.
func TestScopeHintNamesEveryArea(t *testing.T) {
	_, list, _ := strings.Cut(tokenScopeHint, "(areas: ")
	named := map[string]bool{}
	for _, area := range strings.Split(strings.TrimSuffix(list, ")"), ", ") {
		named[area] = true
	}
	for area := range tokenScopeAreas {
		if !named[area] {
			t.Errorf("the scope hint does not name the area %q", area)
		}
	}
	if len(named) != len(tokenScopeAreas) {
		t.Errorf("the scope hint names %d areas, and a grant may carry %d", len(named), len(tokenScopeAreas))
	}
}

// TestScopeAllowsArea pins the plane-wide check the SCIM server uses: every
// /scim/v2 route is the scim area, the verb derives from the method exactly
// as on the admin plane, and full covers it.
func TestScopeAllowsArea(t *testing.T) {
	for _, tc := range []struct {
		scope, method string
		want          bool
		needed        string
	}{
		{"scim:read", "GET", true, "scim:read"},
		{"scim:read", "PATCH", false, "scim:write"},
		{"scim:write", "POST", true, "scim:write"},
		{"scim:write", "GET", false, "scim:read"},
		{"apps:read", "GET", false, "scim:read"},
		{"full", "DELETE", true, ""},
	} {
		ts, err := Parse(tc.scope)
		if err != nil {
			t.Fatalf("parse(%q): %v", tc.scope, err)
		}
		got, needed := ts.AllowsArea("scim", tc.method)
		if got != tc.want || needed != tc.needed {
			t.Errorf("%q %s scim: allowed %v needed %q, want %v %q", tc.scope, tc.method, got, needed, tc.want, tc.needed)
		}
	}
}
