package store

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// TestOwnedRolePrefix pins the prefix a server-owned role's name begins
// with: the server's name folded to a-z, 0-9 and single hyphens, then one
// hyphen, so finance/jira and finance-jira share the prefix and the owner
// column, never the name, decides ownership.
func TestOwnedRolePrefix(t *testing.T) {
	cases := []struct{ app, want string }{
		{"demo-tools", "demo-tools-"},
		{"finance/jira", "finance-jira-"},
		{"Demo_Tools", "demo-tools-"},
		{"io.github.acme/My Server", "io-github-acme-my-server-"},
	}
	for _, tc := range cases {
		if got := OwnedRolePrefix(tc.app); got != tc.want {
			t.Errorf("OwnedRolePrefix(%q) = %q, want %q", tc.app, got, tc.want)
		}
	}
}

// TestRoleOwnerRoundTrip pins the owner column: Create stores it, every
// read answers it, a role created without one reads empty, and
// ListByOwner answers one server's roles by name and nothing else.
func TestRoleOwnerRoundTrip(t *testing.T) {
	forEachStore(t, func(t *testing.T, s Store) {
		ctx := context.Background()
		app, err := s.Apps().Create(ctx, App{Name: "demo-tools", RuntimeKind: "remote"})
		if err != nil {
			t.Fatal(err)
		}
		other, err := s.Apps().Create(ctx, App{Name: "other", RuntimeKind: "remote"})
		if err != nil {
			t.Fatal(err)
		}
		mk := func(name, owner string) Role {
			t.Helper()
			role, err := s.Roles().Create(ctx, Role{Name: name, Kind: RoleKindApplication, OwnerAppID: owner})
			if err != nil {
				t.Fatalf("create %s: %v", name, err)
			}
			return role
		}
		writers := mk("demo-tools-writers", app.ID)
		readers := mk("demo-tools-readers", app.ID)
		global := mk("global-readers", "")
		mk("other-readers", other.ID)

		if readers.OwnerAppID != app.ID || global.OwnerAppID != "" {
			t.Errorf("Create answered owners %q and %q, want %q and empty", readers.OwnerAppID, global.OwnerAppID, app.ID)
		}
		byID, err := s.Roles().GetByID(ctx, readers.ID)
		if err != nil || byID.OwnerAppID != app.ID {
			t.Errorf("GetByID owner = %q, %v; want %q", byID.OwnerAppID, err, app.ID)
		}
		byName, err := s.Roles().GetByName(ctx, "global-readers")
		if err != nil || byName.OwnerAppID != "" {
			t.Errorf("GetByName owner = %q, %v; want empty", byName.OwnerAppID, err)
		}
		all, err := s.Roles().List(ctx)
		if err != nil {
			t.Fatal(err)
		}
		owners := map[string]string{}
		for _, ro := range all {
			owners[ro.Name] = ro.OwnerAppID
		}
		if owners["demo-tools-writers"] != app.ID || owners["global-readers"] != "" || owners["other-readers"] != other.ID {
			t.Errorf("List owners = %v", owners)
		}
		updated, err := s.Roles().Update(ctx, Role{ID: writers.ID, Name: writers.Name, Description: "changed", Kind: writers.Kind})
		if err != nil || updated.OwnerAppID != app.ID {
			t.Errorf("Update kept owner %q, %v; want %q", updated.OwnerAppID, err, app.ID)
		}

		owned, err := s.Roles().ListByOwner(ctx, app.ID)
		if err != nil {
			t.Fatal(err)
		}
		var names []string
		for _, ro := range owned {
			names = append(names, ro.Name)
		}
		if strings.Join(names, ",") != "demo-tools-readers,demo-tools-writers" {
			t.Errorf("ListByOwner = %v, want the two demo-tools roles by name", names)
		}
		if rows, err := s.Roles().ListByOwner(ctx, "no-such-app"); err != nil || len(rows) != 0 {
			t.Errorf("ListByOwner(unknown) = %v, %v; want none", rows, err)
		}
	})
}

// TestRoleHolderCount pins the freeze count: direct assignments plus the
// assignments of every role whose implication closure reaches the role,
// each subject once, and zero for a role nobody reaches.
func TestRoleHolderCount(t *testing.T) {
	forEachStore(t, func(t *testing.T, s Store) {
		ctx := context.Background()
		mk := func(name, kind string) Role {
			t.Helper()
			role, err := s.Roles().Create(ctx, Role{Name: name, Kind: kind})
			if err != nil {
				t.Fatalf("create %s: %v", name, err)
			}
			return role
		}
		owned := mk("demo-tools-readers", RoleKindApplication)
		team := mk("finance-team", RoleKindBusiness)
		org := mk("finance-org", RoleKindBusiness)
		idle := mk("idle", RoleKindBusiness)
		for _, edge := range [][2]string{{org.ID, team.ID}, {team.ID, owned.ID}} {
			if err := s.Roles().AddImplication(ctx, edge[0], edge[1]); err != nil {
				t.Fatal(err)
			}
		}
		for _, pair := range [][2]string{{"u1", owned.ID}, {"u1", team.ID}, {"u2", team.ID}, {"u3", org.ID}} {
			if _, err := s.Roles().Assign(ctx, RoleAssignment{SubjectKind: SubjectUser, SubjectID: pair[0], RoleID: pair[1]}); err != nil {
				t.Fatal(err)
			}
		}
		cases := []struct {
			name string
			id   string
			want int
		}{
			{"owned, through two edges, u1 once", owned.ID, 3},
			{"team, direct and through org", team.ID, 3},
			{"org, direct only", org.ID, 1},
			{"nobody", idle.ID, 0},
			{"unknown role", "no-such-role", 0},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				got, err := s.Roles().HolderCount(ctx, tc.id)
				if err != nil || got != tc.want {
					t.Errorf("HolderCount = %d, %v; want %d", got, err, tc.want)
				}
			})
		}
	})
}

// TestCreateOwnedIsOneTransaction pins that a server-owned role and its
// binding land together: a binding the store refuses leaves no role row,
// an owner is required, and a taken name answers ErrConflict.
func TestCreateOwnedIsOneTransaction(t *testing.T) {
	forEachStore(t, func(t *testing.T, s Store) {
		ctx := context.Background()
		app, err := s.Apps().Create(ctx, App{Name: "demo-tools", RuntimeKind: "remote"})
		if err != nil {
			t.Fatal(err)
		}
		role, b, err := s.Roles().CreateOwned(ctx, Role{Name: "demo-tools-readers", OwnerAppID: app.ID}, `["echo"]`)
		if err != nil {
			t.Fatalf("CreateOwned: %v", err)
		}
		if role.OwnerAppID != app.ID || role.Kind != RoleKindApplication || role.Plane != RolePlaneAccess {
			t.Errorf("role = %+v, want an application role on the access plane owned by %s", role, app.ID)
		}
		if b.RoleID != role.ID || b.AppID != app.ID || b.ToolMatcher != `["echo"]` || b.Effect != "allow" {
			t.Errorf("binding = %+v, want role %s on app %s with the matcher", b, role.ID, app.ID)
		}
		bindings, err := s.ToolBindings().ListByRole(ctx, role.ID)
		if err != nil || len(bindings) != 1 || bindings[0].ID != b.ID {
			t.Errorf("ListByRole = %v, %v; want the one binding", bindings, err)
		}

		if _, _, err := s.Roles().CreateOwned(ctx, Role{Name: "demo-tools-ghost", OwnerAppID: "no-such-app"}, `["echo"]`); err == nil {
			t.Fatal("CreateOwned with an unknown owner succeeded")
		}
		if _, err := s.Roles().GetByName(ctx, "demo-tools-ghost"); !errors.Is(err, ErrNotFound) {
			t.Errorf("a refused binding left the role row: %v", err)
		}
		if _, _, err := s.Roles().CreateOwned(ctx, Role{Name: "demo-tools-loose"}, `["echo"]`); err == nil {
			t.Error("CreateOwned without an owner succeeded")
		}
		if _, _, err := s.Roles().CreateOwned(ctx, Role{Name: "demo-tools-readers", OwnerAppID: app.ID}, `["echo"]`); !errors.Is(err, ErrConflict) {
			t.Errorf("duplicate name = %v, want ErrConflict", err)
		}
	})
}
