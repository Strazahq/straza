package identity

import (
	"context"
	"fmt"
	"math/rand"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/strazahq/straza/internal/config"
	"github.com/strazahq/straza/internal/store"
)

func testStore(t *testing.T) store.Store {
	t.Helper()
	s, err := store.Open(config.Config{Store: config.Store{
		Driver: config.DriverSQLite,
		DSN:    filepath.Join(t.TempDir(), "t.db"),
	}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	if err := s.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	return s
}

func TestResolveBasics(t *testing.T) {
	ctx := context.Background()
	s := testStore(t)
	u, _ := s.Users().Create(ctx, store.User{Username: "kim"})

	dev, _ := s.Roles().Create(ctx, store.Role{Name: "dev"})
	reader, _ := s.Roles().Create(ctx, store.Role{Name: "reader"})
	ops, _ := s.Roles().Create(ctx, store.Role{Name: "ops"})
	expired, _ := s.Roles().Create(ctx, store.Role{Name: "expired-role"})
	future, _ := s.Roles().Create(ctx, store.Role{Name: "future-role"})

	// dev ⇒ reader (implication closure).
	_ = s.Roles().AddImplication(ctx, dev.ID, reader.ID)

	nowT := time.Now().UTC()
	past := nowT.Add(-time.Hour)
	morePast := nowT.Add(-2 * time.Hour)
	futureT := nowT.Add(time.Hour)

	// Direct: dev and ops (open windows). Expired + future windows ignored.
	mustAssign(t, s, store.RoleAssignment{SubjectKind: store.SubjectUser, SubjectID: u.ID, RoleID: dev.ID})
	mustAssign(t, s, store.RoleAssignment{SubjectKind: store.SubjectUser, SubjectID: u.ID, RoleID: ops.ID})
	mustAssign(t, s, store.RoleAssignment{SubjectKind: store.SubjectUser, SubjectID: u.ID, RoleID: expired.ID, ValidFrom: &morePast, ValidTo: &past})
	mustAssign(t, s, store.RoleAssignment{SubjectKind: store.SubjectUser, SubjectID: u.ID, RoleID: future.ID, ValidFrom: &futureT})

	r := NewResolver(s)
	roles, err := r.ResolveRoles(ctx, u.ID, nowT)
	if err != nil {
		t.Fatalf("ResolveRoles: %v", err)
	}
	if got := names(roles); !slices.Equal(got, []string{"dev", "ops", "reader"}) {
		t.Errorf("roles = %v, want [dev ops reader]", got)
	}

	// Boundary semantics: valid_from inclusive, valid_to exclusive.
	if !assignmentValidAt(store.RoleAssignment{ValidFrom: &nowT}, nowT) {
		t.Error("valid_from must be inclusive")
	}
	if assignmentValidAt(store.RoleAssignment{ValidTo: &nowT}, nowT) {
		t.Error("valid_to must be exclusive")
	}

	// Unknown user resolves to no roles, not an error.
	empty, err := r.ResolveRoles(ctx, "no-such-user", nowT)
	if err != nil || len(empty) != 0 {
		t.Errorf("unknown user = %v, %v", empty, err)
	}
}

func TestResolverCacheEpochs(t *testing.T) {
	ctx := context.Background()
	s := testStore(t)
	u, _ := s.Users().Create(ctx, store.User{Username: "kim"})
	dev, _ := s.Roles().Create(ctx, store.Role{Name: "dev"})

	r := NewResolver(s)
	roles, _ := r.ResolveRoles(ctx, u.ID, time.Now())
	if len(roles) != 0 {
		t.Fatalf("initial roles = %v", roles)
	}

	mustAssign(t, s, store.RoleAssignment{SubjectKind: store.SubjectUser, SubjectID: u.ID, RoleID: dev.ID})

	// Same epoch → cached (stale by design).
	roles, _ = r.ResolveRoles(ctx, u.ID, time.Now())
	if len(roles) != 0 {
		t.Fatalf("cache did not hold within epoch: %v", roles)
	}
	before := r.Epoch()
	r.Bump()
	if r.Epoch() != before+1 {
		t.Errorf("epoch = %d, want %d", r.Epoch(), before+1)
	}
	roles, _ = r.ResolveRoles(ctx, u.ID, time.Now())
	if got := names(roles); !slices.Equal(got, []string{"dev"}) {
		t.Errorf("after Bump roles = %v, want [dev]", got)
	}
}

func TestClosureTerminatesOnCycles(t *testing.T) {
	adj := map[string][]string{
		"a": {"b"}, "b": {"c"}, "c": {"a", "d"}, // a→b→c→a cycle plus tail
	}
	out := Closure(map[string]bool{"a": true}, adj)
	for _, id := range []string{"a", "b", "c", "d"} {
		if !out[id] {
			t.Errorf("closure missing %s", id)
		}
	}
	if len(out) != 4 {
		t.Errorf("closure = %v", out)
	}
}

// TestPropertyRandomGraphs cross-checks the resolver against an independent
// reference implementation over randomized identity graphs, including
// implication cycles and mixed validity windows.
func TestPropertyRandomGraphs(t *testing.T) {
	ctx := context.Background()
	nowT := time.Now().UTC()
	past := nowT.Add(-time.Hour)
	morePast := nowT.Add(-2 * time.Hour)
	futureT := nowT.Add(time.Hour)
	windows := []struct{ from, to *time.Time }{
		{nil, nil},         // open
		{&past, nil},       // started
		{&past, &futureT},  // spanning
		{&morePast, &past}, // expired
		{&futureT, nil},    // not yet
		{&nowT, &futureT},  // starts exactly now (inclusive)
		{&past, &nowT},     // ends exactly now (exclusive)
	}

	for seed := int64(0); seed < 15; seed++ {
		t.Run(fmt.Sprintf("seed-%d", seed), func(t *testing.T) {
			rng := rand.New(rand.NewSource(seed))
			s := testStore(t)

			nRoles := 3 + rng.Intn(10)
			roleIDs := make([]string, nRoles)
			roleNames := make(map[string]string, nRoles)
			for i := range roleIDs {
				role, err := s.Roles().Create(ctx, store.Role{Name: fmt.Sprintf("role-%02d", i)})
				if err != nil {
					t.Fatal(err)
				}
				roleIDs[i] = role.ID
				roleNames[role.ID] = role.Name
			}

			// Random implication edges; cycles allowed on purpose.
			adj := map[string][]string{}
			for i := 0; i < nRoles*2; i++ {
				from := roleIDs[rng.Intn(nRoles)]
				to := roleIDs[rng.Intn(nRoles)]
				if from == to {
					continue
				}
				if err := s.Roles().AddImplication(ctx, from, to); err != nil {
					continue // duplicate edge
				}
				adj[from] = append(adj[from], to)
			}

			u, _ := s.Users().Create(ctx, store.User{Username: "user"})

			// Random direct assignments.
			expected := map[string]bool{} // reference seed set
			assign := func(subjectID string) {
				role := roleIDs[rng.Intn(nRoles)]
				w := windows[rng.Intn(len(windows))]
				_, err := s.Roles().Assign(ctx, store.RoleAssignment{
					SubjectKind: store.SubjectUser, SubjectID: subjectID, RoleID: role,
					ValidFrom: w.from, ValidTo: w.to,
				})
				if err != nil {
					return // duplicate (subject, role)
				}
				valid := (w.from == nil || !nowT.Before(*w.from)) && (w.to == nil || nowT.Before(*w.to))
				if valid {
					expected[role] = true
				}
			}
			for i := 0; i < 1+rng.Intn(5); i++ {
				assign(u.ID)
			}

			// Reference closure: independent DFS.
			ref := map[string]bool{}
			var dfs func(string)
			dfs = func(id string) {
				if ref[id] {
					return
				}
				ref[id] = true
				for _, next := range adj[id] {
					dfs(next)
				}
			}
			for id := range expected {
				dfs(id)
			}

			got, err := NewResolver(s).ResolveRoles(ctx, u.ID, nowT)
			if err != nil {
				t.Fatalf("ResolveRoles: %v", err)
			}
			var want []string
			for id := range ref {
				want = append(want, roleNames[id])
			}
			sortStrings(want)
			if !slices.Equal(names(got), want) {
				t.Errorf("seed %d mismatch:\nresolver %v\nreference %v", seed, names(got), want)
			}
		})
	}
}

func mustAssign(t *testing.T, s store.Store, a store.RoleAssignment) {
	t.Helper()
	if _, err := s.Roles().Assign(context.Background(), a); err != nil {
		t.Fatalf("Assign: %v", err)
	}
}

func names(roles []store.Role) []string {
	out := make([]string, len(roles))
	for i, r := range roles {
		out[i] = r.Name
	}
	return out
}

func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j] < s[j-1]; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}
