package scim

import "testing"

// FuzzParseFilter hammers the SCIM filter parser. Filters arrive
// from external IdMs (midPoint, Okta, Entra) over the network; anything
// outside the strict supported subset must be rejected with an error, never
// a panic.
func FuzzParseFilter(f *testing.F) {
	for _, seed := range []string{
		`userName eq "kim"`,
		`externalId eq "abc-123"`,
		`displayName eq "Kim \"The\" Admin"`,
		`userName eq "trailing`,
		`userName sw "kim"`,
		`userName eq`,
		``,
		`a eq "b" and c eq "d"`,
		`userName eq "\\\" nested \\ escapes \""`,
	} {
		f.Add(seed)
	}
	f.Fuzz(func(_ *testing.T, raw string) {
		_, _, _ = parseFilter(raw, "userName", "externalId", "displayName", "value")
	})
}
