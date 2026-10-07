package identity

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/strazahq/straza/internal/store"
)

// TestResolveRolesUntilReportsTheNextEdge pins the edge that callers who
// keep roles past the resolve time rely on: the next valid_from or valid_to
// after the asked time, zero when no window lies ahead, and the same edge
// whether the answer came from the store or from the cache.
func TestResolveRolesUntilReportsTheNextEdge(t *testing.T) {
	ctx := context.Background()
	s := testStore(t)
	kim, _ := s.Users().Create(ctx, store.User{Username: "kim"})
	lee, _ := s.Users().Create(ctx, store.User{Username: "lee"})
	dev, _ := s.Roles().Create(ctx, store.Role{Name: "dev"})
	ops, _ := s.Roles().Create(ctx, store.Role{Name: "ops"})
	t0 := time.Date(2026, 9, 24, 12, 0, 0, 0, time.UTC)
	ends := t0.Add(time.Hour)
	starts := t0.Add(2 * time.Hour)
	mustAssign(t, s, store.RoleAssignment{SubjectKind: store.SubjectUser, SubjectID: kim.ID, RoleID: dev.ID, ValidTo: &ends})
	mustAssign(t, s, store.RoleAssignment{SubjectKind: store.SubjectUser, SubjectID: kim.ID, RoleID: ops.ID, ValidFrom: &starts})
	mustAssign(t, s, store.RoleAssignment{SubjectKind: store.SubjectUser, SubjectID: lee.ID, RoleID: dev.ID})

	r := NewResolver(s)
	// Rows for one user run in order, so a row can read through the cache
	// the row before it filled.
	for _, tc := range []struct {
		name, user string
		at         time.Time
		wantRoles  []string
		wantUntil  time.Time
	}{
		{"first resolve reads the end", kim.ID, t0, []string{"dev"}, ends},
		{"cached answer keeps the end", kim.ID, t0.Add(30 * time.Minute), []string{"dev"}, ends},
		{"at the end the start is next", kim.ID, ends, nil, starts},
		{"at the start no edge is left", kim.ID, starts, []string{"ops"}, time.Time{}},
		{"no window has no edge", lee.ID, t0, []string{"dev"}, time.Time{}},
	} {
		roles, until, err := r.ResolveRolesUntil(ctx, tc.user, tc.at)
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if got := names(roles); !slices.Equal(got, tc.wantRoles) {
			t.Errorf("%s: roles = %v, want %v", tc.name, got, tc.wantRoles)
		}
		if !until.Equal(tc.wantUntil) {
			t.Errorf("%s: until = %v, want %v", tc.name, until, tc.wantUntil)
		}
	}
}
