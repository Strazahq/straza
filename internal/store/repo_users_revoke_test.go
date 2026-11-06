package store

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"
)

// TestUserWritesCarryTheirRevocation pins the user writes that carry a
// revocation, on both drivers. A disable through UpdateFields and a delete
// through SoftDelete write the user row, one revocation row with the reason
// and origin given and one event. When the database refuses any one of the
// three, none of them is written, so the user reads as before and the same
// write lands on a rerun.
func TestUserWritesCarryTheirRevocation(t *testing.T) {
	forEachStore(t, func(t *testing.T, s Store) {
		ctx := context.Background()
		const subject = "straza.revocation.user"
		disabled := UserDisabled
		writes := []struct {
			name, origin string
			write        func(id string, rv *UserRevocation) error
			// landed reports whether the write's change is in the user row.
			landed func(id string) bool
		}{
			{"a disable", RevocationOriginSCIM, func(id string, rv *UserRevocation) error {
				u, err := s.Users().UpdateFields(ctx, id, UserFields{Status: &disabled}, rv)
				if err == nil && (u.ID != id || u.Status != UserDisabled) {
					return fmt.Errorf("UpdateFields answered the row %+v, want %s disabled", u, id)
				}
				return err
			}, func(id string) bool {
				u, err := s.Users().GetByID(ctx, id)
				return err == nil && u.Status == UserDisabled
			}},
			{"a delete", RevocationOriginAdmin, func(id string, rv *UserRevocation) error {
				_, err := s.Users().SoftDelete(ctx, id, rv)
				return err
			}, func(id string) bool {
				_, err := s.Users().GetByID(ctx, id)
				return errors.Is(err, ErrNotFound)
			}},
		}
		for i, w := range writes {
			for j, table := range []string{"users", "revocations", "events_outbox", ""} {
				name := fmt.Sprintf("%s with the %s table gone", w.name, table)
				if table == "" {
					name = w.name + " on a healthy store"
				}
				u, err := s.Users().Create(ctx, User{Username: fmt.Sprintf("l9-%d-%d", i, j)})
				if err != nil {
					t.Fatal(err)
				}
				rv := &UserRevocation{Reason: "l9 test", Origin: w.origin,
					Event: OutboxEvent{Subject: subject, CE: `{"data":{"user":"` + u.ID + `"}}`}}
				before := outboxCount(t, s, subject)
				run := func() { err = w.write(u.ID, rv) }
				if table != "" {
					tableAway(t, s, table, run)
					rows, events := revocationCount(t, s, RevokeUser, u.ID), outboxCount(t, s, subject)-before
					if err == nil || w.landed(u.ID) || rows != 0 || events != 0 {
						t.Errorf("%s: err %v, change landed %v, %d revocation rows, %d events, want an error and nothing written",
							name, err, w.landed(u.ID), rows, events)
					}
				}
				run()
				rows, err2 := s.Revocations().ListByTarget(ctx, RevokeUser, u.ID)
				if err2 != nil {
					t.Fatal(err2)
				}
				events := outboxCount(t, s, subject) - before
				if err != nil || !w.landed(u.ID) || len(rows) != 1 || rows[0].Reason != "l9 test" || rows[0].Origin != w.origin || events != 1 {
					t.Errorf("%s: err %v, change landed %v, revocation rows %+v, %d events, want the change with one row of reason l9 test and origin %s and one event",
						name, err, w.landed(u.ID), rows, events, w.origin)
				}
			}
		}
	})
}

// TestRevocationCommitted pins the read that settles a user write whose
// answer was lost, on both drivers. It reports a disable or a delete as
// committed only when the write's own revocation event is stored, and it
// answers the user's row as stored then. A write the database refused, and
// a disable another writer committed under a different event, both read as
// not committed, although that user reads disabled. A store that cannot
// read the outbox, or a user with no row, answers an error.
func TestRevocationCommitted(t *testing.T) {
	forEachStore(t, func(t *testing.T, s Store) {
		ctx := context.Background()
		disabled := UserDisabled
		revocation := func(id string) *UserRevocation {
			return &UserRevocation{Reason: "settle test", Origin: RevocationOriginAdmin, Event: OutboxEvent{
				ID: newID(), Subject: "straza.revocation.user", CE: `{"data":{"user":"` + id + `"}}`}}
		}
		disable := func(id string, rv *UserRevocation) {
			if _, err := s.Users().UpdateFields(ctx, id, UserFields{Status: &disabled}, rv); err != nil {
				t.Fatal(err)
			}
		}
		cases := []struct {
			name string
			// write runs the write and answers the user id and the event id
			// to ask about.
			write                     func(id string) (string, string)
			committed, deleted, fails bool
			status                    string
		}{
			{name: "a disable that committed", committed: true, status: UserDisabled,
				write: func(id string) (string, string) {
					rv := revocation(id)
					disable(id, rv)
					return id, rv.Event.ID
				}},
			{name: "a delete that committed", committed: true, deleted: true, status: UserDisabled,
				write: func(id string) (string, string) {
					rv := revocation(id)
					if _, err := s.Users().SoftDelete(ctx, id, rv); err != nil {
						t.Fatal(err)
					}
					return id, rv.Event.ID
				}},
			{name: "a disable the database refused", status: UserActive,
				write: func(id string) (string, string) {
					rv := revocation(id)
					tableAway(t, s, "events_outbox", func() {
						if _, err := s.Users().UpdateFields(ctx, id, UserFields{Status: &disabled}, rv); err == nil {
							t.Fatal("a disable with the outbox gone succeeded")
						}
					})
					return id, rv.Event.ID
				}},
			{name: "a disable another writer committed", status: UserDisabled,
				write: func(id string) (string, string) {
					disable(id, revocation(id))
					return id, revocation(id).Event.ID
				}},
			{name: "a store that cannot read the outbox", fails: true,
				write: func(id string) (string, string) {
					rv := revocation(id)
					disable(id, rv)
					return id, rv.Event.ID
				}},
			{name: "a user with no row", fails: true,
				write: func(string) (string, string) { return newID(), newID() }},
		}
		for i, tc := range cases {
			u, err := s.Users().Create(ctx, User{Username: fmt.Sprintf("settle-%d", i)})
			if err != nil {
				t.Fatal(err)
			}
			userID, eventID := tc.write(u.ID)
			var row User
			var committed bool
			ask := func() { row, committed, err = s.Users().RevocationCommitted(ctx, userID, eventID) }
			if tc.name == "a store that cannot read the outbox" {
				tableAway(t, s, "events_outbox", ask)
			} else {
				ask()
			}
			if tc.fails {
				if err == nil {
					t.Errorf("%s: RevocationCommitted answered %v and no error, want an error", tc.name, committed)
				}
				continue
			}
			if err != nil || committed != tc.committed || row.ID != u.ID || row.Status != tc.status || (row.DeletedAt != nil) != tc.deleted {
				t.Errorf("%s: RevocationCommitted = %v, row %s %s deleted %v, %v, want %v and the row %s %s deleted %v",
					tc.name, committed, row.ID, row.Status, row.DeletedAt != nil, err, tc.committed, u.ID, tc.status, tc.deleted)
			}
		}
		if s.(*sqlStore).d == dialectPostgres {
			revocationCommittedWaits(t, s)
		}
	})
}

// revocationCommittedWaits pins, on Postgres, that RevocationCommitted waits
// for a write that still holds the user's row, as a commit does while it
// waits for a synchronous standby after its client lost the connection, and
// reads the event once that write has committed. Without the wait the read
// would find no event and call a committed write lost.
func revocationCommittedWaits(t *testing.T, s Store) {
	t.Helper()
	ctx := context.Background()
	u, err := s.Users().Create(ctx, User{Username: "settle-in-flight"})
	if err != nil {
		t.Fatal(err)
	}
	sq := s.(*sqlStore)
	tx, err := sq.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	eventID := newID()
	if _, err := tx.ExecContext(ctx, `UPDATE users SET status = 'disabled' WHERE id = $1`, u.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO events_outbox (id, subject, ce, published, attempts, created_at)
		VALUES ($1, 'straza.revocation.user', '{}', FALSE, 0, $2)`, eventID, sq.tArg(now())); err != nil {
		t.Fatal(err)
	}
	type answer struct {
		committed bool
		err       error
	}
	got := make(chan answer, 1)
	go func() {
		_, committed, err := s.Users().RevocationCommitted(ctx, u.ID, eventID)
		got <- answer{committed, err}
	}()
	select {
	case a := <-got:
		t.Fatalf("RevocationCommitted answered %v (%v) while the write still held the user's row, want it to wait", a.committed, a.err)
	case <-time.After(300 * time.Millisecond):
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if a := <-got; a.err != nil || !a.committed {
		t.Errorf("RevocationCommitted after the write committed = %v (%v), want true", a.committed, a.err)
	}
}
