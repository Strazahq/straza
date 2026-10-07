package store

import (
	"context"
	"errors"
	"testing"
)

// TestRolePlaneRoundTrip pins the plane column: control-plane
// roles persist and read back, and the default is the access plane.
func TestRolePlaneRoundTrip(t *testing.T) {
	forEachStore(t, func(t *testing.T, s Store) {
		ctx := context.Background()

		auditor, err := s.Roles().Create(ctx, Role{Name: "auditor", Plane: RolePlaneControl, Kind: RoleKindBusiness})
		if err != nil {
			t.Fatalf("create control role: %v", err)
		}
		got, err := s.Roles().GetByID(ctx, auditor.ID)
		if err != nil {
			t.Fatalf("get: %v", err)
		}
		if got.Plane != RolePlaneControl {
			t.Errorf("plane = %q, want control", got.Plane)
		}

		dev, err := s.Roles().Create(ctx, Role{Name: "dev"})
		if err != nil {
			t.Fatalf("create default role: %v", err)
		}
		if dev.Plane != RolePlaneAccess {
			t.Errorf("default plane = %q, want access", dev.Plane)
		}

		// Update must not be able to flip the plane (the SET list omits it).
		dev.Plane = RolePlaneControl
		dev.Description = "updated"
		after, err := s.Roles().Update(ctx, dev)
		if err != nil {
			t.Fatalf("update: %v", err)
		}
		if after.Plane != RolePlaneAccess {
			t.Errorf("plane after update = %q, want access (plane is create-time only)", after.Plane)
		}
	})
}

// TestBindingUniquePerRoleApp pins two unique indexes on the access rows: a
// second binding for the same (role, app) answers ErrConflict, which the
// API layer turns into 409 + route-to-edit, and a binding of the same role
// on any other app answers ErrConflict, because an application role
// reaches one server.
func TestBindingUniquePerRoleApp(t *testing.T) {
	forEachStore(t, func(t *testing.T, s Store) {
		ctx := context.Background()
		role, err := s.Roles().Create(ctx, Role{Name: "dev"})
		if err != nil {
			t.Fatalf("role: %v", err)
		}
		app, err := s.Apps().Create(ctx, App{Name: "demo-tools", RuntimeKind: "remote"})
		if err != nil {
			t.Fatalf("app: %v", err)
		}
		if _, err := s.ToolBindings().Create(ctx, ToolBinding{RoleID: role.ID, AppID: app.ID, ToolMatcher: `["echo"]`}); err != nil {
			t.Fatalf("first binding: %v", err)
		}
		if _, err := s.ToolBindings().Create(ctx, ToolBinding{RoleID: role.ID, AppID: app.ID, ToolMatcher: `["add"]`}); !errors.Is(err, ErrConflict) {
			t.Errorf("duplicate (role, app) binding = %v, want ErrConflict", err)
		}
		// A second server for the same role is refused by the index alone,
		// so two concurrent creates can never land two rows.
		app2, err := s.Apps().Create(ctx, App{Name: "midpoint", RuntimeKind: "remote"})
		if err != nil {
			t.Fatalf("app2: %v", err)
		}
		if _, err := s.ToolBindings().Create(ctx, ToolBinding{RoleID: role.ID, AppID: app2.ID, ToolMatcher: `[]`}); !errors.Is(err, ErrConflict) {
			t.Errorf("second server for the same role = %v, want ErrConflict", err)
		}
		// Another role reaches the second server as before.
		role2, err := s.Roles().Create(ctx, Role{Name: "dev-midpoint"})
		if err != nil {
			t.Fatalf("role2: %v", err)
		}
		if _, err := s.ToolBindings().Create(ctx, ToolBinding{RoleID: role2.ID, AppID: app2.ID, ToolMatcher: `[]`}); err != nil {
			t.Errorf("another role on the second server: %v", err)
		}
	})
}
