package server

import (
	"slices"
	"testing"
)

// TestGatewayNarrowDropsOnlyWhatKeepRefuses pins Narrow: it drops the rows
// keep refuses and keeps the rest in their order, it rebuilds the cached
// catalogs of the roles that lost a row and no other, and a call that drops
// nothing leaves every catalog in place.
func TestGatewayNarrowDropsOnlyWhatKeepRefuses(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name      string
		keep      func(gwBinding) bool
		wantIDs   []string
		wantBuilt []string // the roles whose catalog a Narrow dropped
	}{
		{name: "the rows of one server", keep: func(b gwBinding) bool { return b.App != "x" },
			wantIDs: []string{"2", "3"}, wantBuilt: []string{"A"}},
		{name: "the rows of one role", keep: func(b gwBinding) bool { return b.Role != "B" },
			wantIDs: []string{"1", "2"}, wantBuilt: []string{"B"}},
		{name: "no row", keep: func(gwBinding) bool { return true },
			wantIDs: []string{"1", "2", "3"}},
		{name: "every row", keep: func(gwBinding) bool { return false },
			wantBuilt: []string{"A", "B"}},
	}
	app, _, _ := testAppCounting(t)
	g := app.gateway
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			g.SetBindings([]gwBinding{
				{ID: "1", App: "x", Role: "A", Matchers: []string{"*"}},
				{ID: "2", App: "y", Role: "A", Matchers: []string{"*"}},
				{ID: "3", App: "y", Role: "B", Matchers: []string{"*"}},
			})
			before := map[string]uint64{"A": app.catalogFor([]string{"A"}).id, "B": app.catalogFor([]string{"B"}).id}

			g.Narrow(tc.keep)
			var ids []string
			for _, b := range g.bindings.Load().([]gwBinding) {
				ids = append(ids, b.ID)
			}
			if !slices.Equal(ids, tc.wantIDs) {
				t.Errorf("rows after Narrow = %v, want %v", ids, tc.wantIDs)
			}
			for role, id := range before {
				rebuilt := app.catalogFor([]string{role}).id != id
				if want := slices.Contains(tc.wantBuilt, role); rebuilt != want {
					t.Errorf("catalog of role %s rebuilt = %v, want %v", role, rebuilt, want)
				}
			}
		})
	}
}
