package scim

import (
	"encoding/json"
	"errors"
	"net/http/httptest"
	"testing"

	"github.com/strazahq/straza/internal/store"
)

// TestListPageBounds pins the 1-based SCIM paging contract of listPage:
// totalResults reflects the full set while only the page is rendered.
func TestListPageBounds(t *testing.T) {
	items := make([]int, 250)
	for i := range items {
		items[i] = i
	}
	render := func(i int) map[string]any { return map[string]any{"i": float64(i)} }

	cases := []struct {
		query     string
		wantStart float64
		wantN     int
		wantFirst float64 // index of the first item on the page; -1 = empty
	}{
		{"", 1, 100, 0},
		{"?startIndex=101&count=100", 101, 100, 100},
		{"?startIndex=201&count=100", 201, 50, 200},
		{"?startIndex=999", 999, 0, -1},
		{"?count=1000", 1, 100, 0}, // out-of-range count falls back to the default
	}
	for _, tc := range cases {
		req := httptest.NewRequest("GET", "/scim/v2/Users"+tc.query, nil)
		rec := httptest.NewRecorder()
		listPage(rec, req, items, render)

		var out struct {
			Total     float64          `json:"totalResults"`
			Start     float64          `json:"startIndex"`
			PerPage   int              `json:"itemsPerPage"`
			Resources []map[string]any `json:"Resources"`
		}
		if err := json.NewDecoder(rec.Body).Decode(&out); err != nil {
			t.Fatalf("%s: %v", tc.query, err)
		}
		if out.Total != 250 || out.Start != tc.wantStart || out.PerPage != tc.wantN || len(out.Resources) != tc.wantN {
			t.Errorf("%s: total=%v start=%v perPage=%d n=%d, want 250/%v/%d/%d",
				tc.query, out.Total, out.Start, out.PerPage, len(out.Resources), tc.wantStart, tc.wantN, tc.wantN)
		}
		if tc.wantFirst >= 0 && out.Resources[0]["i"] != tc.wantFirst {
			t.Errorf("%s: first item = %v, want %v", tc.query, out.Resources[0]["i"], tc.wantFirst)
		}
	}
}

// TestParseFilterUnescapes pins the filter-value unescaping: every backslash
// escape the grammar admits becomes its literal character. Unescaping only
// `\"` would leave AD-style names (CORP\jdoe) unmatched against the stored
// value, and the IdP would loop on lookup-miss → re-POST → 409.
func TestParseFilterUnescapes(t *testing.T) {
	cases := []struct{ raw, wantAttr, wantValue string }{
		{`userName eq "kim"`, "username", "kim"},
		{`userName eq "CORP\\jdoe"`, "username", `CORP\jdoe`},
		{`externalId eq "say \"hi\""`, "externalid", `say "hi"`},
	}
	for _, tc := range cases {
		attr, val, err := parseFilter(tc.raw, "userName", "externalId")
		if err != nil {
			t.Fatalf("parseFilter(%s): %v", tc.raw, err)
		}
		if attr != tc.wantAttr || val != tc.wantValue {
			t.Errorf("parseFilter(%s) = %q, %q, want %q, %q", tc.raw, attr, val, tc.wantAttr, tc.wantValue)
		}
	}
}

// TestStrazaBornFactsReadOnly pins the §3.3 contract (revision 11): `kind`
// and `origin` are server-mastered. Every PATCH shape that could reach them
// (bare path, URN-qualified path, keys smuggled inside a whole-extension or
// no-path object) answers a mutability error, and the render always carries
// both with the correct derivation (kind from the create-time attrs mark,
// origin from the row).
func TestStrazaBornFactsReadOnly(t *testing.T) {
	refusals := []struct {
		name string
		op   patchOp
	}{
		{"bare kind", patchOp{op: "replace", path: "kind", value: "nhi"}},
		{"bare origin", patchOp{op: "replace", path: "origin", value: "local"}},
		{"urn kind", patchOp{op: "replace", path: strazaURNLower + ":kind", value: "nhi"}},
		{"urn origin", patchOp{op: "remove", path: strazaURNLower + ":origin"}},
		{"whole-extension smuggle", patchOp{op: "replace", path: strazaURNLower,
			value: map[string]any{"sponsor": "carol", "kind": "nhi"}}},
		{"no-path smuggle", patchOp{op: "add", path: "",
			value: map[string]any{"origin": "local"}}},
	}
	for _, tc := range refusals {
		u := store.User{Username: "kim", Origin: store.OriginSCIM}
		active := true
		err := applyUserOp(&u, &active, tc.op)
		var me mutabilityError
		if !errors.As(err, &me) {
			t.Errorf("%s: err = %v, want mutabilityError", tc.name, err)
		}
	}

	renders := []struct {
		name       string
		u          store.User
		wantKind   string
		wantOrigin string
	}{
		{"urn-less scim create", store.User{Origin: store.OriginSCIM}, "human", "scim"},
		{"agentic create", store.User{Origin: store.OriginSCIM, Attrs: `{"kind":"nhi"}`}, "nhi", "scim"},
		{"local console user", store.User{Origin: store.OriginLocal}, "human", "local"},
		{"junk attrs stay human", store.User{Origin: store.OriginLocal, Attrs: `{"kind":"root"}`}, "human", "local"},
	}
	for _, tc := range renders {
		block := extensionBlock(tc.u, nil)
		if block["kind"] != tc.wantKind || block["origin"] != tc.wantOrigin {
			t.Errorf("%s: kind=%v origin=%v, want %s/%s",
				tc.name, block["kind"], block["origin"], tc.wantKind, tc.wantOrigin)
		}
	}
}

// TestNormalizePathPreservesFilterValues: attribute names are
// case-insensitive (RFC 7643) but quoted filter values are data. Lowercasing
// the whole path would corrupt member ids inside members[value eq "..."], so
// removals would silently do nothing.
func TestNormalizePathPreservesFilterValues(t *testing.T) {
	cases := []struct{ in, want string }{
		{"ExternalId", "externalid"},
		{"Name.Formatted", "name.formatted"},
		{" active ", "active"},
		{`members[value eq "04B7-Xy"]`, `members[value eq "04B7-Xy"]`},
		{`Members[Value eq "AbC"]`, `members[value eq "AbC"]`},
	}
	for _, tc := range cases {
		if got := normalizePath(tc.in); got != tc.want {
			t.Errorf("normalizePath(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
