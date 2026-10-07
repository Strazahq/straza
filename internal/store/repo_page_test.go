package store

import (
	"context"
	"strconv"
	"testing"
	"time"
)

// Keyset pages: newest-first by UUIDv7 id, filters compose, empty cursor
// = newest page, and walking past the end terminates cleanly. Runs on both
// drivers via forEachStore.
func TestPageKeysets(t *testing.T) {
	forEachStore(t, func(t *testing.T, s Store) {
		ctx := context.Background()

		// Users: 5 rows, ids ascend with creation order.
		var userIDs []string
		for i := 0; i < 5; i++ {
			u, err := s.Users().Create(ctx, User{Username: "u" + strconv.Itoa(i)})
			if err != nil {
				t.Fatalf("user %d: %v", i, err)
			}
			userIDs = append(userIDs, u.ID)
		}
		page, err := s.Users().Page(ctx, UserFilter{}, Sort{}, Cursor{}, 2)
		if err != nil || len(page) != 2 || page[0].ID != userIDs[4] || page[1].ID != userIDs[3] {
			t.Fatalf("users page 1 = %v (err %v), want newest two", page, err)
		}
		page, err = s.Users().Page(ctx, UserFilter{}, Sort{}, Cursor{ID: page[1].ID}, 2)
		if err != nil || len(page) != 2 || page[0].ID != userIDs[2] {
			t.Fatalf("users page 2 = %v (err %v)", page, err)
		}
		page, err = s.Users().Page(ctx, UserFilter{}, Sort{}, Cursor{ID: userIDs[0]}, 2)
		if err != nil || len(page) != 0 {
			t.Fatalf("users past-the-end = %v (err %v), want empty", page, err)
		}

		// Sessions: two owners, mixed statuses; filters compose with the cursor.
		var sesIDs []string
		for i := 0; i < 4; i++ {
			owner := userIDs[i%2]
			status := SessionActive
			if i == 3 {
				status = SessionRevoked
			}
			ses, err := s.Sessions().Create(ctx, Session{UserID: owner, HarnessName: "h", HarnessVersion: "1", Status: status})
			if err != nil {
				t.Fatalf("session %d: %v", i, err)
			}
			sesIDs = append(sesIDs, ses.ID)
		}
		got, err := s.Sessions().Page(ctx, "", "", Sort{}, Cursor{}, 10)
		if err != nil || len(got) != 4 || got[0].ID != sesIDs[3] || got[3].ID != sesIDs[0] {
			t.Fatalf("sessions page = %v (err %v), want all newest-first", got, err)
		}
		got, err = s.Sessions().Page(ctx, SessionActive, userIDs[0], Sort{}, Cursor{}, 10)
		if err != nil || len(got) != 2 || got[0].ID != sesIDs[2] || got[1].ID != sesIDs[0] {
			t.Fatalf("sessions filtered = %v (err %v), want u0's two active", got, err)
		}
		got, err = s.Sessions().Page(ctx, SessionActive, userIDs[0], Sort{}, Cursor{ID: sesIDs[2]}, 10)
		if err != nil || len(got) != 1 || got[0].ID != sesIDs[0] {
			t.Fatalf("sessions filtered+cursor = %v (err %v)", got, err)
		}

		// Approvals: state filter composes with the cursor.
		base := time.Now().UTC()
		var appIDs []string
		for i := 0; i < 4; i++ {
			state := "approved"
			if i == 2 {
				state = "denied"
			}
			a, err := s.Approvals().Insert(ctx, Approval{
				SessionID: "s", RuleID: "r", ArgvHash: "h" + strconv.Itoa(i), State: state,
				CreatedAt: base, ExpiresAt: base.Add(time.Hour),
			})
			if err != nil {
				t.Fatalf("approval %d: %v", i, err)
			}
			appIDs = append(appIDs, a.ID)
		}
		apps, err := s.Approvals().PageByState(ctx, "approved", "", 2)
		if err != nil || len(apps) != 2 || apps[0].ID != appIDs[3] || apps[1].ID != appIDs[1] {
			t.Fatalf("approvals page 1 = %v (err %v), want newest approved two", apps, err)
		}
		apps, err = s.Approvals().PageByState(ctx, "approved", apps[1].ID, 2)
		if err != nil || len(apps) != 1 || apps[0].ID != appIDs[0] {
			t.Fatalf("approvals page 2 = %v (err %v)", apps, err)
		}
		apps, err = s.Approvals().PageByState(ctx, "", appIDs[1], 10)
		if err != nil || len(apps) != 1 || apps[0].ID != appIDs[0] {
			t.Fatalf("approvals all-states cursor = %v (err %v)", apps, err)
		}
	})
}

// CountByUser rides the (user_id, id) index.
func TestApprovalsCountByUser(t *testing.T) {
	forEachStore(t, func(t *testing.T, s Store) {
		ctx := context.Background()
		base := time.Now().UTC()
		for i := 0; i < 3; i++ {
			owner := "u-a"
			if i == 2 {
				owner = "u-b"
			}
			if _, err := s.Approvals().Insert(ctx, Approval{
				SessionID: "s", UserID: owner, RuleID: "r", ArgvHash: "c" + strconv.Itoa(i),
				State: "approved", CreatedAt: base, ExpiresAt: base.Add(time.Hour),
			}); err != nil {
				t.Fatal(err)
			}
		}
		for owner, want := range map[string]int{"u-a": 2, "u-b": 1, "u-none": 0} {
			if n, err := s.Approvals().CountByUser(ctx, owner); err != nil || n != want {
				t.Errorf("CountByUser(%s) = %d, %v; want %d", owner, n, err, want)
			}
		}
	})
}
