package store

import (
	"context"
	"testing"
)

// TestSessionClientVersionRoundTrip pins the client build stamp on a session:
// stored as given, read back on get and list, blank for a client that sent
// none (every client older than this column).
func TestSessionClientVersionRoundTrip(t *testing.T) {
	forEachStore(t, func(t *testing.T, s Store) {
		ctx := context.Background()
		owner, err := s.Users().Create(ctx, User{Username: "stamp-owner"})
		if err != nil {
			t.Fatalf("Users.Create: %v", err)
		}
		stamped, err := s.Sessions().Create(ctx, Session{UserID: owner.ID, HarnessName: "claude-code", ClientVersion: "v1.0.0-1080-ga49eca3f"})
		if err != nil {
			t.Fatalf("Create: %v", err)
		}
		blank, err := s.Sessions().Create(ctx, Session{UserID: owner.ID, HarnessName: "claude-code"})
		if err != nil {
			t.Fatalf("Create blank: %v", err)
		}
		got, err := s.Sessions().GetByID(ctx, stamped.ID)
		if err != nil || got.ClientVersion != "v1.0.0-1080-ga49eca3f" {
			t.Fatalf("Get = %q, %v; want the stamp back", got.ClientVersion, err)
		}
		list, err := s.Sessions().List(ctx, "")
		if err != nil {
			t.Fatalf("List: %v", err)
		}
		seen := map[string]string{}
		for _, ses := range list {
			seen[ses.ID] = ses.ClientVersion
		}
		if seen[stamped.ID] != "v1.0.0-1080-ga49eca3f" || seen[blank.ID] != "" {
			t.Fatalf("List stamps = %q / %q, want the stamp and blank", seen[stamped.ID], seen[blank.ID])
		}
	})
}
