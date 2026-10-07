package identity

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/strazahq/straza/internal/store"
)

// TestResolverTimeWindowEdges pins that a cached resolution ends at the next
// window edge without an admin write: an assignment that ends stops counting
// at valid_to, and one that starts later counts from valid_from.
func TestResolverTimeWindowEdges(t *testing.T) {
	ctx := context.Background()
	s := testStore(t)
	u, _ := s.Users().Create(ctx, store.User{Username: "kim"})
	dev, _ := s.Roles().Create(ctx, store.Role{Name: "dev"})
	ops, _ := s.Roles().Create(ctx, store.Role{Name: "ops"})
	t0 := time.Date(2026, 9, 22, 12, 0, 0, 0, time.UTC)
	ends := t0.Add(time.Hour)
	starts := t0.Add(2 * time.Hour)
	mustAssign(t, s, store.RoleAssignment{SubjectKind: store.SubjectUser, SubjectID: u.ID, RoleID: dev.ID, ValidTo: &ends})
	mustAssign(t, s, store.RoleAssignment{SubjectKind: store.SubjectUser, SubjectID: u.ID, RoleID: ops.ID, ValidFrom: &starts})

	r := NewResolver(s)
	// The order matters: each row after the first reads through the cache
	// the row before it filled.
	for _, tc := range []struct {
		name string
		at   time.Time
		want []string
	}{
		{"before the end", t0, []string{"dev"}},
		{"a second before the end", ends.Add(-time.Second), []string{"dev"}},
		{"at the end", ends, nil},
		{"a second before the start", starts.Add(-time.Second), nil},
		{"at the start", starts, []string{"ops"}},
		{"a day later", starts.Add(24 * time.Hour), []string{"ops"}},
	} {
		roles, err := r.ResolveRoles(ctx, u.ID, tc.at)
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if got := names(roles); !slices.Equal(got, tc.want) {
			t.Errorf("%s: roles = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// bumpingStore is a store whose assignment read runs a hook after it, the
// way a revocation that lands while a resolve is in flight does.
type bumpingStore struct {
	store.Store
	roles *bumpingRoles
}

func (b bumpingStore) Roles() store.RoleRepo { return b.roles }

type bumpingRoles struct {
	store.RoleRepo
	afterList func()
}

func (b *bumpingRoles) ListAssignments(ctx context.Context, subjectKind, subjectID string) ([]store.RoleAssignment, error) {
	out, err := b.RoleRepo.ListAssignments(ctx, subjectKind, subjectID)
	if b.afterList != nil {
		b.afterList()
	}
	return out, err
}

// TestResolverBumpDuringResolveIsNotCached pins that a resolution that
// started before an identity write is not cached past it: the roles it read
// may be revoked by that write, and the next lookup must resolve again.
func TestResolverBumpDuringResolveIsNotCached(t *testing.T) {
	ctx := context.Background()
	s := testStore(t)
	u, _ := s.Users().Create(ctx, store.User{Username: "kim"})
	dev, _ := s.Roles().Create(ctx, store.Role{Name: "dev"})
	assignment, err := s.Roles().Assign(ctx, store.RoleAssignment{SubjectKind: store.SubjectUser, SubjectID: u.ID, RoleID: dev.ID})
	if err != nil {
		t.Fatal(err)
	}

	roles := &bumpingRoles{RoleRepo: s.Roles()}
	r := NewResolver(bumpingStore{Store: s, roles: roles})
	roles.afterList = func() {
		// One shot: the revocation lands once, after the first read.
		roles.afterList = nil
		if err := s.Roles().Unassign(ctx, assignment.ID); err != nil {
			t.Fatal(err)
		}
		r.Bump()
	}
	if _, err := r.ResolveRoles(ctx, u.ID, time.Now()); err != nil {
		t.Fatal(err)
	}
	got, err := r.ResolveRoles(ctx, u.ID, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("after a revocation during the resolve, roles = %v, want none", names(got))
	}
}
