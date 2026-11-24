package store

import (
	"context"
	"errors"
	"fmt"
	"testing"
)

// TestSetPasswordHashCarriesItsEvent pins SetPasswordHash on both drivers.
// It stores the hash and one unpublished event. When the database refuses
// either write, neither is stored, so the user keeps the hash it had and a
// rerun lands both. A missing user is ErrNotFound and writes no event.
func TestSetPasswordHashCarriesItsEvent(t *testing.T) {
	forEachStore(t, func(t *testing.T, s Store) {
		ctx := context.Background()
		const subject = "straza.audit.admin"
		hashOf := func(id string) string {
			u, err := s.Users().GetByID(ctx, id)
			if err != nil {
				t.Fatal(err)
			}
			return u.PasswordHash
		}
		for i, table := range []string{"users", "events_outbox", ""} {
			name := "with the " + table + " table gone"
			if table == "" {
				name = "on a healthy store"
			}
			u, err := s.Users().Create(ctx, User{Username: fmt.Sprintf("pw-%d", i), PasswordHash: "old-hash"})
			if err != nil {
				t.Fatal(err)
			}
			ev := OutboxEvent{Subject: subject, CE: `{"data":{"user":"` + u.ID + `"}}`}
			before := outboxCount(t, s, subject)
			run := func() { err = s.Users().SetPasswordHash(ctx, u.ID, "new-hash", ev) }
			if table != "" {
				tableAway(t, s, table, run)
				if events := outboxCount(t, s, subject) - before; err == nil || hashOf(u.ID) != "old-hash" || events != 0 {
					t.Errorf("%s: err %v, hash %q, %d events, want an error, the old hash and no event", name, err, hashOf(u.ID), events)
				}
			}
			run()
			if events := outboxCount(t, s, subject) - before; err != nil || hashOf(u.ID) != "new-hash" || events != 1 {
				t.Errorf("%s: err %v, hash %q, %d events, want the new hash and one event", name, err, hashOf(u.ID), events)
			}
		}
		before := outboxCount(t, s, subject)
		err := s.Users().SetPasswordHash(ctx, newID(), "new-hash", OutboxEvent{Subject: subject, CE: `{}`})
		if !errors.Is(err, ErrNotFound) || outboxCount(t, s, subject) != before {
			t.Errorf("a missing user: err %v, events %d, want ErrNotFound and no event", err, outboxCount(t, s, subject)-before)
		}
	})
}
