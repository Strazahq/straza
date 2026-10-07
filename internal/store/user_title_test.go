package store

import (
	"context"
	"testing"
)

// TestUserTitleRoundTrip pins the title column and its plumbing:
// title survives create, update, and clear on both dialects (the PG arm
// runs when STRAZA_TEST_POSTGRES_DSN is set, like every store test).
func TestUserTitleRoundTrip(t *testing.T) {
	forEachStore(t, func(t *testing.T, s Store) {
		ctx := context.Background()
		u, err := s.Users().Create(ctx, User{Username: "titled", Title: "Deploy bot"})
		if err != nil {
			t.Fatal(err)
		}
		got, err := s.Users().GetByID(ctx, u.ID)
		if err != nil || got.Title != "Deploy bot" {
			t.Fatalf("created title = %q err %v, want Deploy bot", got.Title, err)
		}
		got.Title = "Release manager"
		if got, err = s.Users().Update(ctx, got); err != nil || got.Title != "Release manager" {
			t.Fatalf("updated title = %q err %v", got.Title, err)
		}
		got.Title = ""
		if got, err = s.Users().Update(ctx, got); err != nil || got.Title != "" {
			t.Fatalf("cleared title = %q err %v, want empty", got.Title, err)
		}
	})
}
