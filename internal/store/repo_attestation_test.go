package store

import (
	"context"
	"errors"
	"testing"
)

// TestAttestationHashes pins the expected-hash registry contract:
// rows are allowed-set entries per (artifact, harness, platform); exact
// duplicates conflict, alternative hashes for the same artifact coexist.
func TestAttestationHashes(t *testing.T) {
	forEachStore(t, func(t *testing.T, s Store) {
		ctx := context.Background()

		selfHash, err := s.AttestationHashes().Create(ctx, AttestationHash{
			Artifact: "self", Platform: "linux/amd64", Hash: "sha256:aa11", Note: "straza v0.5.0",
		})
		if err != nil {
			t.Fatalf("Create: %v", err)
		}
		if selfHash.ID == "" || selfHash.CreatedAt.IsZero() {
			t.Errorf("Create did not populate id/created_at: %+v", selfHash)
		}

		// The exact same measurement registered twice is a conflict...
		if _, err := s.AttestationHashes().Create(ctx, AttestationHash{
			Artifact: "self", Platform: "linux/amd64", Hash: "sha256:aa11",
		}); !errors.Is(err, ErrConflict) {
			t.Errorf("duplicate Create = %v, want ErrConflict", err)
		}
		// ...but another allowed hash for the same artifact is fine (allowed-set
		// semantics: e.g. two rolled-out straza versions during upgrade).
		if _, err := s.AttestationHashes().Create(ctx, AttestationHash{
			Artifact: "self", Platform: "linux/amd64", Hash: "sha256:bb22",
		}); err != nil {
			t.Fatalf("second allowed hash: %v", err)
		}
		// Harness-scoped wiring measurement.
		wiring, err := s.AttestationHashes().Create(ctx, AttestationHash{
			Artifact: "hooks.claude-code", Harness: "claude-code", Hash: "sha256:cc33",
		})
		if err != nil {
			t.Fatalf("harness-scoped Create: %v", err)
		}

		all, err := s.AttestationHashes().List(ctx)
		if err != nil || len(all) != 3 {
			t.Fatalf("List = %d rows, %v; want 3", len(all), err)
		}

		if err := s.AttestationHashes().Delete(ctx, wiring.ID); err != nil {
			t.Fatalf("Delete: %v", err)
		}
		if err := s.AttestationHashes().Delete(ctx, wiring.ID); !errors.Is(err, ErrNotFound) {
			t.Errorf("Delete(gone) = %v, want ErrNotFound", err)
		}
		if all, _ = s.AttestationHashes().List(ctx); len(all) != 2 {
			t.Errorf("after Delete: %d rows, want 2", len(all))
		}
	})
}
