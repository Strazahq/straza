package store

import (
	"context"
	"testing"
)

// TestRoleKindApproverRoundTrip runs on every dialect: the approver kind
// persists, defaults to the access plane, and the CHECK refuses values
// outside the enum (the API edge guards first; this pins the floor).
func TestRoleKindApproverRoundTrip(t *testing.T) {
	forEachStore(t, func(t *testing.T, s Store) {
		ctx := context.Background()
		if _, err := s.Roles().Create(ctx, Role{Name: "sec-approvers", Kind: RoleKindApprover, Description: "deciders"}); err != nil {
			t.Fatalf("Create: %v", err)
		}
		got, err := s.Roles().GetByName(ctx, "sec-approvers")
		if err != nil || got.Kind != RoleKindApprover || got.Plane != RolePlaneAccess {
			t.Fatalf("GetByName = %+v, %v", got, err)
		}
		if _, err := s.Roles().Create(ctx, Role{Name: "bad", Kind: "banana"}); err == nil {
			t.Fatal("kind banana accepted by the CHECK")
		}
	})
}
