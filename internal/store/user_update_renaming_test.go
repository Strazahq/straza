package store

import (
	"context"
	"errors"
	"testing"
)

// TestUpdateRenaming pins the one write that stores a whole user and moves
// the sponsor of the users its old username sponsors: a rename moves them
// and answers their ids, a write that keeps the name moves nobody, and a
// write that fails, on a taken username or a taken external id, stores
// nothing and moves nobody.
func TestUpdateRenaming(t *testing.T) {
	forEachStore(t, func(t *testing.T, s Store) {
		ctx := context.Background()
		mk := func(u User) User {
			t.Helper()
			got, err := s.Users().Create(ctx, u)
			if err != nil {
				t.Fatal(err)
			}
			return got
		}
		ada := mk(User{Username: "ada", ExternalID: "idm-ada", Origin: OriginSCIM})
		mk(User{Username: "kim", ExternalID: "idm-kim", Origin: OriginSCIM})
		one := mk(User{Username: "ada-agent-1", UserType: UserTypeAgent, Sponsor: "ada"})
		two := mk(User{Username: "ada-agent-2", UserType: UserTypeAgent, Sponsor: "ada"})
		other := mk(User{Username: "kim-agent", UserType: UserTypeAgent, Sponsor: "kim"})
		sponsors := func(what, want string) {
			t.Helper()
			for _, a := range []User{one, two} {
				if got, err := s.Users().GetByID(ctx, a.ID); err != nil || got.Sponsor != want {
					t.Errorf("%s: sponsor of %s = %q, %v; want %q", what, a.Username, got.Sponsor, err, want)
				}
			}
			if got, err := s.Users().GetByID(ctx, other.ID); err != nil || got.Sponsor != "kim" {
				t.Errorf("%s: sponsor of kim's agent = %q, %v; want kim", what, got.Sponsor, err)
			}
		}

		for _, bad := range []struct{ what, username, externalID string }{
			{"a taken username", "kim", "idm-ada"},
			{"a taken external id", "ada.new", "idm-kim"},
		} {
			w := ada
			w.Username, w.ExternalID, w.Title = bad.username, bad.externalID, "Staff engineer"
			if _, moved, err := s.Users().UpdateRenaming(ctx, w); !errors.Is(err, ErrConflict) || moved != nil {
				t.Errorf("%s: UpdateRenaming = %v, %v; want ErrConflict and nobody moved", bad.what, moved, err)
			}
			if got, err := s.Users().GetByID(ctx, ada.ID); err != nil || got != ada {
				t.Errorf("%s: ada = %+v, %v; want her unchanged", bad.what, got, err)
			}
			sponsors(bad.what, "ada")
		}

		w := ada
		w.Title = "Staff engineer"
		kept, moved, err := s.Users().UpdateRenaming(ctx, w)
		if err != nil || len(moved) != 0 || kept.Title != "Staff engineer" || kept.Username != "ada" {
			t.Errorf("a write that keeps the name = %+v, %v, %v; want the title stored and nobody moved", kept, moved, err)
		}
		sponsors("a write that keeps the name", "ada")

		w.Username = "ada.new"
		renamed, moved, err := s.Users().UpdateRenaming(ctx, w)
		if err != nil || renamed.Username != "ada.new" || renamed.Title != "Staff engineer" {
			t.Fatalf("a rename = %+v, %v; want the new name with the title", renamed, err)
		}
		if len(moved) != 2 || (moved[0] != one.ID && moved[0] != two.ID) || moved[0] == moved[1] {
			t.Errorf("a rename moved %v, want ada's two agents", moved)
		}
		sponsors("a rename", "ada.new")

		if _, _, err := s.Users().UpdateRenaming(ctx, User{ID: "no-such-user", Username: "x"}); !errors.Is(err, ErrNotFound) {
			t.Errorf("a user that does not exist = %v, want ErrNotFound", err)
		}
	})
}
