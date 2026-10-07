package store

import (
	"context"
	"testing"
)

// TestUserTypology pins the identity-typology columns: typed columns,
// never Attrs JSON: policy match reads them from the checkin-built subject
// and the engine must never parse JSON on a request path.
func TestUserTypology(t *testing.T) {
	forEachStore(t, func(t *testing.T, s Store) {
		ctx := context.Background()

		// Defaults: a plain user carries no typology.
		plain, err := s.Users().Create(ctx, User{Username: "ty-plain"})
		if err != nil {
			t.Fatal(err)
		}
		if plain.UserType != "" || plain.AgencyMode != "" || plain.Sponsor != "" || plain.SwarmID != "" || plain.Ephemeral {
			t.Fatalf("fresh user carries typology: %+v", plain)
		}

		full, err := s.Users().Create(ctx, User{
			Username: "ty-atlas", UserType: UserTypeAgent, AgencyMode: AgencyAutonomous,
			Sponsor: "kim", SwarmID: "scan-fleet-1", Ephemeral: true,
		})
		if err != nil {
			t.Fatal(err)
		}
		got, err := s.Users().GetByID(ctx, full.ID)
		if err != nil {
			t.Fatal(err)
		}
		if got.UserType != "agent" || got.AgencyMode != "autonomous" || got.Sponsor != "kim" ||
			got.SwarmID != "scan-fleet-1" || !got.Ephemeral {
			t.Fatalf("round-trip = %+v", got)
		}

		// Update persists changes AND clears (the IdM can reclassify).
		got.AgencyMode = AgencyInteractive
		got.SwarmID = ""
		got.Ephemeral = false
		if _, err := s.Users().Update(ctx, got); err != nil {
			t.Fatal(err)
		}
		got, err = s.Users().GetByID(ctx, full.ID)
		if err != nil {
			t.Fatal(err)
		}
		if got.AgencyMode != "interactive" || got.SwarmID != "" || got.Ephemeral || got.UserType != "agent" {
			t.Fatalf("after update = %+v", got)
		}

		// List and username lookup carry the columns too (the SCIM render and
		// the checkin subject both read through these lanes).
		byName, err := s.Users().GetByUsername(ctx, "ty-atlas")
		if err != nil || byName.UserType != "agent" {
			t.Fatalf("GetByUsername typology = %+v, %v", byName, err)
		}
	})
}
