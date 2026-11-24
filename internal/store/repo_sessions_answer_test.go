package store

import (
	"context"
	"strconv"
	"strings"
	"testing"
	"time"
)

// TestSessionPageSearch pins the search of the sessions page: a part of the
// owner's username or the start of the session id, both without regard to
// case, a LIKE wildcard that means itself, AND with the status and user
// filters, and a cursor that walks the matches. Runs on both drivers.
func TestSessionPageSearch(t *testing.T) {
	forEachStore(t, func(t *testing.T, s Store) {
		ctx := context.Background()
		users := map[string]string{}
		for _, name := range []string{"Kim.Lee", "amy", "bob_x", "gone"} {
			u, err := s.Users().Create(ctx, User{Username: name})
			if err != nil {
				t.Fatal(err)
			}
			users[name] = u.ID
		}
		ses := map[string][]string{}
		for _, name := range []string{"Kim.Lee", "Kim.Lee", "Kim.Lee", "amy", "bob_x", "gone"} {
			row, err := s.Sessions().Create(ctx, Session{UserID: users[name], HarnessName: "h", HarnessVersion: "1"})
			if err != nil {
				t.Fatal(err)
			}
			ses[name] = append(ses[name], row.ID)
		}
		if err := s.Sessions().SetStatus(ctx, ses["Kim.Lee"][0], SessionRevoked); err != nil {
			t.Fatal(err)
		}
		if _, err := s.Users().SoftDelete(ctx, users["gone"], nil); err != nil {
			t.Fatal(err)
		}
		amyID := ses["amy"][0]

		cases := []struct {
			name string
			f    SessionFilter
			want []string
		}{
			{"a part of a name in another case", SessionFilter{Q: "kim.l"}, ses["Kim.Lee"]},
			{"the start of an id", SessionFilter{Q: amyID[:len(amyID)-4]}, []string{amyID}},
			{"the start of an id in upper case", SessionFilter{Q: strings.ToUpper(amyID[:len(amyID)-4])}, []string{amyID}},
			{"the end of an id is no match", SessionFilter{Q: amyID[24:]}, nil},
			{"an underscore means itself", SessionFilter{Q: "_"}, ses["bob_x"]},
			{"a percent sign means itself", SessionFilter{Q: "%"}, nil},
			{"the name of a deleted user", SessionFilter{Q: "gone"}, nil},
			{"with a status", SessionFilter{Q: "kim", Status: SessionActive}, ses["Kim.Lee"][1:]},
			{"with a user", SessionFilter{Q: "kim", UserID: users["amy"]}, nil},
			{"with the matching user", SessionFilter{Q: "KIM", UserID: users["Kim.Lee"]}, ses["Kim.Lee"]},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				got, err := s.Sessions().Page(ctx, tc.f, Sort{}, Cursor{}, 10)
				if err != nil {
					t.Fatal(err)
				}
				if !sameIDs(sessionIDs(got), tc.want) {
					t.Errorf("Page(%+v) = %v, want %v", tc.f, sessionIDs(got), tc.want)
				}
			})
		}

		// A walk of one row a page under the sort by user reads each match once.
		var walked []string
		c := Cursor{}
		for i := 0; i < 10; i++ {
			page, err := s.Sessions().Page(ctx, SessionFilter{Q: "kim"}, Sort{Key: "user", Asc: true}, c, 1)
			if err != nil {
				t.Fatal(err)
			}
			if len(page) == 0 {
				break
			}
			walked = append(walked, page[0].ID)
			c = Cursor{Value: "Kim.Lee", ID: page[0].ID}
		}
		if !sameIDs(walked, ses["Kim.Lee"]) || len(walked) != 3 {
			t.Errorf("walk under q = %v, want each of %v once", walked, ses["Kim.Lee"])
		}
	})
}

// TestApprovalWaitingBySession pins the waiting count: pending approvals
// whose expiry is ahead, per listed session. A decided, an expired and an
// unlisted session's request are not counted, a session with none is
// absent, and a list longer than one query's chunk counts in every chunk.
// Runs on both drivers.
func TestApprovalWaitingBySession(t *testing.T) {
	forEachStore(t, func(t *testing.T, s Store) {
		ctx := context.Background()
		now := time.Now().UTC()
		for i, a := range []struct {
			session, state string
			expires        time.Time
		}{
			{"ses-a", "pending", now.Add(time.Hour)},
			{"ses-a", "pending", now.Add(time.Minute)},
			{"ses-a", "pending", now.Add(-time.Minute)},
			{"ses-a", "approved", now.Add(time.Hour)},
			{"ses-a", "denied", now.Add(time.Hour)},
			{"ses-a", "expired", now.Add(-time.Hour)},
			{"ses-b", "pending", now.Add(time.Hour)},
			{"ses-c", "denied", now.Add(time.Hour)},
			{"ses-unlisted", "pending", now.Add(time.Hour)},
		} {
			if _, err := s.Approvals().Insert(ctx, Approval{
				SessionID: a.session, RuleID: "r", ArgvHash: "h" + strconv.Itoa(i), State: a.state,
				CreatedAt: now.Add(-2 * time.Hour), ExpiresAt: a.expires,
			}); err != nil {
				t.Fatalf("approval %d: %v", i, err)
			}
		}
		ids := make([]string, 0, waitingChunk+100)
		for i := 0; i < waitingChunk+100; i++ {
			ids = append(ids, "ses-none-"+strconv.Itoa(i))
		}
		ids[3], ids[waitingChunk+50], ids[waitingChunk+60] = "ses-b", "ses-a", "ses-c"

		got, err := s.Approvals().WaitingBySession(ctx, ids, now)
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != 2 || got["ses-a"] != 2 || got["ses-b"] != 1 {
			t.Errorf("WaitingBySession = %v, want ses-a 2 and ses-b 1 only", got)
		}
		if got, err := s.Approvals().WaitingBySession(ctx, nil, now); err != nil || len(got) != 0 {
			t.Errorf("WaitingBySession(nil) = %v, %v, want an empty map", got, err)
		}
	})
}

func sessionIDs(rows []Session) []string {
	out := make([]string, len(rows))
	for i, r := range rows {
		out[i] = r.ID
	}
	return out
}

// sameIDs reports whether a and b hold the same ids, in any order.
func sameIDs(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	count := map[string]int{}
	for _, id := range a {
		count[id]++
	}
	for _, id := range b {
		count[id]--
	}
	for _, n := range count {
		if n != 0 {
			return false
		}
	}
	return true
}
