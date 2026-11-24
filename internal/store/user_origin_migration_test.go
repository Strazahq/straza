package store

import (
	"context"
	"errors"
	"testing"
)

// TestUserOriginMigration pins migration 45 on both drivers: users written
// below it survive the move up with every column, and so do the devices and
// sessions that point at them, the origin check takes entra and still
// refuses an unknown origin, the unique keys still hold, and the move down
// turns a user of origin entra into a local one and loses no row.
func TestUserOriginMigration(t *testing.T) {
	forEachStore(t, func(t *testing.T, s Store) {
		ctx := context.Background()
		sq := s.(*sqlStore)
		_, head, err := sq.migrationRange()
		if err != nil {
			t.Fatal(err)
		}
		if head < 45 {
			t.Fatalf("newest migration = %d, want 45 or later", head)
		}
		for v := head; v >= 45; v-- {
			if got, err := sq.MigrateTo(ctx, v-1); err != nil || got != v-1 {
				t.Fatalf("MigrateTo(%d) = %d, %v", v-1, got, err)
			}
		}
		person, err := s.Users().Create(ctx, User{
			Username: "ada@example.com", ExternalID: "d6d6d6d6-0000-4000-8000-000000000006", Email: "ada@example.com",
			Display: "Ada Lind", Title: "Engineer", Origin: OriginSCIM, PasswordHash: "hash", UserType: "human",
		})
		if err != nil {
			t.Fatalf("Users.Create below 45: %v", err)
		}
		agent, err := s.Users().Create(ctx, User{
			Username: "build-agent", Attrs: `{"kind":"nhi"}`, UserType: "agent", AgencyMode: "autonomous",
			Sponsor: "ada@example.com", SwarmID: "fleet-1", Ephemeral: true, EntraAgentID: "d3d3d3d3-0000-4000-8000-000000000003",
		})
		if err != nil {
			t.Fatalf("Users.Create an agent below 45: %v", err)
		}
		// The rows are compared as the store reads them back, not as Create
		// answered them.
		if person, err = s.Users().GetByID(ctx, person.ID); err != nil {
			t.Fatal(err)
		}
		if agent, err = s.Users().GetByID(ctx, agent.ID); err != nil {
			t.Fatal(err)
		}
		if _, err := s.Users().Create(ctx, User{Username: "from-entra", Origin: OriginEntra}); err == nil {
			t.Fatal("a user of origin entra was stored below 45, want the old origin check to refuse it")
		}
		device, err := s.Devices().Create(ctx, Device{UserID: person.ID, Name: "laptop", Fingerprint: "fp-origin"})
		if err != nil {
			t.Fatalf("Devices.Create: %v", err)
		}
		session, err := s.Sessions().Create(ctx, Session{UserID: person.ID, HarnessName: "claude-code"})
		if err != nil {
			t.Fatalf("Sessions.Create: %v", err)
		}
		gone, err := s.Users().Create(ctx, User{Username: "gone", ExternalID: "ext-gone"})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := s.Users().SoftDelete(ctx, gone.ID, nil); err != nil {
			t.Fatalf("SoftDelete: %v", err)
		}

		if got, err := sq.MigrateTo(ctx, head); err != nil || got != head {
			t.Fatalf("MigrateTo(head) = %d, %v", got, err)
		}
		for _, want := range []User{person, agent} {
			got, err := s.Users().GetByID(ctx, want.ID)
			if err != nil {
				t.Fatalf("user %s after the move up: %v", want.Username, err)
			}
			if got != want {
				t.Errorf("user %s after the move up = %+v, want %+v", want.Username, got, want)
			}
		}
		if got, err := s.Devices().GetByID(ctx, device.ID); err != nil || got.UserID != person.ID {
			t.Errorf("the device after the move up = %+v, %v; want it kept", got, err)
		}
		if got, err := s.Sessions().GetByID(ctx, session.ID); err != nil || got.UserID != person.ID {
			t.Errorf("the session after the move up = %+v, %v; want it kept", got, err)
		}
		if _, err := s.Users().GetByID(ctx, gone.ID); !errors.Is(err, ErrNotFound) {
			t.Errorf("the deleted user after the move up reads %v, want it to stay deleted", err)
		}
		if _, err := s.Users().Create(ctx, User{Username: "gone"}); !errors.Is(err, ErrConflict) {
			t.Errorf("a second user named like the deleted one = %v, want the username key to refuse it", err)
		}
		if _, err := s.Users().Create(ctx, User{Username: "ada-twin", ExternalID: person.ExternalID}); !errors.Is(err, ErrConflict) {
			t.Errorf("a second user with ada's external id = %v, want the key to refuse it", err)
		}
		if _, err := s.Users().Create(ctx, User{Username: "agent-twin", UserType: "agent", EntraAgentID: agent.EntraAgentID}); !errors.Is(err, ErrConflict) {
			t.Errorf("a second agent with the same Entra agent id = %v, want the key to refuse it", err)
		}
		if _, err := s.Users().Create(ctx, User{Username: "bogus-origin", Origin: "bogus"}); err == nil {
			t.Error("an unknown origin was stored at 45, want the origin check to refuse it")
		}
		if _, err := s.Users().Create(ctx, User{Username: "bogus-status", Status: "asleep"}); err == nil {
			t.Error("an unknown status was stored at 45, want the status check to refuse it")
		}
		fromEntra, err := s.Users().Create(ctx, User{Username: "dan@example.com", ExternalID: "d4d4d4d4-0000-4000-8000-000000000004", Origin: OriginEntra})
		if err != nil {
			t.Fatalf("a user of origin entra at 45: %v", err)
		}

		if got, err := sq.MigrateTo(ctx, head-1); err != nil || got != head-1 {
			t.Fatalf("MigrateTo(%d) = %d, %v", head-1, got, err)
		}
		for v := head - 1; v >= 45; v-- {
			if got, err := sq.MigrateTo(ctx, v-1); err != nil || got != v-1 {
				t.Fatalf("MigrateTo(%d) = %d, %v", v-1, got, err)
			}
		}
		down, err := s.Users().GetByID(ctx, fromEntra.ID)
		if err != nil || down.Origin != OriginLocal || down.ExternalID != fromEntra.ExternalID {
			t.Errorf("the Entra user after the move down = %+v, %v; want origin local and its external id", down, err)
		}
		if got, err := s.Users().GetByID(ctx, person.ID); err != nil || got.Origin != OriginSCIM {
			t.Errorf("ada after the move down = %+v, %v; want origin scim", got, err)
		}
		if got, err := s.Sessions().GetByID(ctx, session.ID); err != nil || got.UserID != person.ID {
			t.Errorf("the session after the move down = %+v, %v; want it kept", got, err)
		}
		if got, err := s.Devices().GetByID(ctx, device.ID); err != nil || got.UserID != person.ID {
			t.Errorf("the device after the move down = %+v, %v; want it kept", got, err)
		}
		if got, err := sq.MigrateTo(ctx, head); err != nil || got != head {
			t.Fatalf("MigrateTo(head) again = %d, %v", got, err)
		}
	})
}

// TestUserTakeOver pins the one write that changes a user's origin: it
// stores the Entra object id and the origin entra on a live user, leaves
// every other column, and refuses a deleted user and a taken object id.
func TestUserTakeOver(t *testing.T) {
	forEachStore(t, func(t *testing.T, s Store) {
		ctx := context.Background()
		u, err := s.Users().Create(ctx, User{Username: "ben@example.com", ExternalID: "sub-of-ben", Email: "ben@example.com", Origin: OriginSCIM, PasswordHash: "hash"})
		if err != nil {
			t.Fatal(err)
		}
		got, err := s.Users().TakeOver(ctx, u.ID, "d4d4d4d4-0000-4000-8000-000000000004")
		if err != nil {
			t.Fatalf("TakeOver: %v", err)
		}
		want := u
		want.Origin, want.ExternalID, want.UpdatedAt = OriginEntra, "d4d4d4d4-0000-4000-8000-000000000004", got.UpdatedAt
		if got != want {
			t.Errorf("TakeOver = %+v, want %+v", got, want)
		}
		other, err := s.Users().Create(ctx, User{Username: "cleo@example.com"})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := s.Users().TakeOver(ctx, other.ID, got.ExternalID); !errors.Is(err, ErrConflict) {
			t.Errorf("TakeOver with ben's object id = %v, want ErrConflict", err)
		}
		if _, err := s.Users().SoftDelete(ctx, other.ID, nil); err != nil {
			t.Fatal(err)
		}
		if _, err := s.Users().TakeOver(ctx, other.ID, "d7d7d7d7-0000-4000-8000-000000000007"); !errors.Is(err, ErrNotFound) {
			t.Errorf("TakeOver of a deleted user = %v, want ErrNotFound", err)
		}
	})
}

// TestUserRename pins the write that changes a username: the agents whose
// sponsor was the old name follow the user to the new one in the same
// write, so the old name names nobody's sponsor when another user takes
// it, and a taken name changes nothing.
func TestUserRename(t *testing.T) {
	forEachStore(t, func(t *testing.T, s Store) {
		ctx := context.Background()
		ada, err := s.Users().Create(ctx, User{Username: "ada@example.com"})
		if err != nil {
			t.Fatal(err)
		}
		agent := func(name, sponsor string) User {
			t.Helper()
			u, err := s.Users().Create(ctx, User{Username: name, UserType: "agent", Sponsor: sponsor})
			if err != nil {
				t.Fatal(err)
			}
			return u
		}
		hers, other := agent("ada-agent", "ada@example.com"), agent("ben-agent", "ben@example.com")
		gone := agent("gone-agent", "ada@example.com")
		if _, err := s.Users().SoftDelete(ctx, gone.ID, nil); err != nil {
			t.Fatal(err)
		}

		got, repointed, err := s.Users().Rename(ctx, ada.ID, "ada@example.com (left)")
		if err != nil {
			t.Fatalf("Rename: %v", err)
		}
		if got.Username != "ada@example.com (left)" || len(repointed) != 1 || repointed[0] != hers.ID {
			t.Errorf("Rename = %q with the agents %v, want the new name and ada's one live agent", got.Username, repointed)
		}
		if u, _ := s.Users().GetByID(ctx, hers.ID); u.Sponsor != "ada@example.com (left)" {
			t.Errorf("the sponsor of ada's agent = %q, want it to follow ada", u.Sponsor)
		}
		if u, _ := s.Users().GetByID(ctx, other.ID); u.Sponsor != "ben@example.com" {
			t.Errorf("the sponsor of ben's agent = %q, want it untouched", u.Sponsor)
		}
		if _, _, err := s.Users().Rename(ctx, ada.ID, "ben-agent"); !errors.Is(err, ErrConflict) {
			t.Errorf("Rename to a taken name = %v, want ErrConflict", err)
		}
		if u, _ := s.Users().GetByID(ctx, hers.ID); u.Sponsor != "ada@example.com (left)" {
			t.Errorf("the sponsor of ada's agent after a refused rename = %q, want it unchanged", u.Sponsor)
		}
		if _, _, err := s.Users().Rename(ctx, gone.ID, "anything"); !errors.Is(err, ErrNotFound) {
			t.Errorf("Rename of a deleted user = %v, want ErrNotFound", err)
		}
	})
}
