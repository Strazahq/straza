package store

import (
	"context"
	"errors"
	"testing"
	"time"
)

// walkUsers pages the users list under one sort until the cursor runs dry,
// the way the admin handler does, and returns every row in order.
func walkUsers(t *testing.T, s Store, f UserFilter, srt Sort, value func(User) string, limit int) []User {
	t.Helper()
	var all []User
	c := Cursor{}
	for i := 0; i < 50; i++ {
		page, err := s.Users().Page(context.Background(), f, srt, c, limit+1)
		if err != nil {
			t.Fatalf("page %d under %+v: %v", i, srt, err)
		}
		if len(page) <= limit {
			return append(all, page...)
		}
		page = page[:limit]
		all = append(all, page...)
		last := page[limit-1]
		c = Cursor{Value: value(last), ID: last.ID}
	}
	t.Fatalf("walk under %+v never ended", srt)
	return nil
}

// TestUsersPageSorts pins the sort keys of the users page on both stores:
// a walk under every key in both directions visits each user exactly once
// in an order that never goes backwards, the never-seen users sort as the
// zero time, and the sponsor filter and counts read one query each.
func TestUsersPageSorts(t *testing.T) {
	forEachStore(t, func(t *testing.T, s Store) {
		ctx := context.Background()
		names := []string{"mia", "bob", "zoe", "amy", "kai", "lee", "ana", "joe", "eve", "ida", "max", "ned"}
		seen := map[string]time.Time{}
		base := time.Date(2026, 9, 1, 8, 0, 0, 0, time.UTC)
		for i, n := range names {
			u := User{Username: n, Status: UserActive}
			if i%3 == 0 {
				u.Status = UserDisabled
			}
			if i%4 == 1 {
				u.Sponsor = "mia"
			}
			if i == 5 {
				u.Sponsor = "bob"
			}
			created, err := s.Users().Create(ctx, u)
			if err != nil {
				t.Fatal(err)
			}
			// Two thirds of the users have sessions; the newest one carries
			// a distinct last_seen a whole number of seconds apart.
			if i%3 != 2 {
				for k := 0; k < 2; k++ {
					ses, err := s.Sessions().Create(ctx, Session{UserID: created.ID, HarnessName: "h", HarnessVersion: "1"})
					if err != nil {
						t.Fatal(err)
					}
					at := base.Add(time.Duration(i*7+k*3) * time.Second)
					if err := s.Sessions().Touch(ctx, ses.ID, at); err != nil {
						t.Fatal(err)
					}
					if at.After(seen[created.ID]) {
						seen[created.ID] = at
					}
				}
			}
		}
		lastSeen := func(u User) string { return seen[u.ID].UTC().Format(time.RFC3339Nano) }
		keys := map[string]func(User) string{
			"":          func(u User) string { return "" },
			"created":   func(u User) string { return "" },
			"name":      func(u User) string { return u.Username },
			"status":    func(u User) string { return u.Status },
			"last_seen": lastSeen,
		}
		order := map[string]func(a, b User) int{
			"":          func(a, b User) int { return cmpStr(a.ID, b.ID) },
			"created":   func(a, b User) int { return cmpStr(a.ID, b.ID) },
			"name":      func(a, b User) int { return cmpStr(a.Username, b.Username) },
			"status":    func(a, b User) int { return cmpStr(a.Status, b.Status) },
			"last_seen": func(a, b User) int { return seen[a.ID].Compare(seen[b.ID]) },
		}
		for key, value := range keys {
			for _, asc := range []bool{false, true} {
				srt := Sort{Key: key, Asc: asc}
				got := walkUsers(t, s, UserFilter{}, srt, value, 5)
				ids := map[string]int{}
				for _, u := range got {
					ids[u.ID]++
				}
				// Every seeded user once, plus nobody twice.
				if len(got) != len(names) || len(ids) != len(names) {
					t.Errorf("%+v: walked %d rows over %d ids, want %d once each", srt, len(got), len(ids), len(names))
				}
				for i := 1; i < len(got); i++ {
					c := order[key](got[i-1], got[i])
					if (asc && c > 0) || (!asc && c < 0) {
						t.Errorf("%+v: row %d (%s) before %s breaks the order", srt, i-1, got[i-1].Username, got[i].Username)
					}
					if c == 0 && got[i-1].ID < got[i].ID {
						t.Errorf("%+v: ties on the value must break newest first", srt)
					}
				}
			}
		}
		// Ascending last_seen opens on the never-seen users.
		got := walkUsers(t, s, UserFilter{}, Sort{Key: "last_seen", Asc: true}, lastSeen, 4)
		for i := 0; i < 4; i++ {
			if !seen[got[i].ID].IsZero() {
				t.Errorf("ascending last_seen row %d is %s, seen %v, want a never-seen user first", i, got[i].Username, seen[got[i].ID])
			}
		}

		// The sponsor filter composes with a sort, and the counts read once.
		mine := walkUsers(t, s, UserFilter{Sponsor: "mia"}, Sort{Key: "name", Asc: true}, keys["name"], 2)
		var sponsored []string
		for _, u := range mine {
			sponsored = append(sponsored, u.Username)
		}
		if len(sponsored) != 2 || sponsored[0] != "bob" || sponsored[1] != "ida" {
			t.Errorf("sponsor filter = %v, want [bob ida]", sponsored)
		}
		counts, err := s.Users().SponsoredCounts(ctx, []string{"mia", "bob", "zoe"})
		if err != nil || counts["mia"] != 2 || counts["bob"] != 1 || len(counts) != 2 {
			t.Errorf("SponsoredCounts = %v (err %v), want mia 2 and bob 1 only", counts, err)
		}

		// Refusals: an unknown key, and a time cursor that does not parse.
		if _, err := s.Users().Page(ctx, UserFilter{}, Sort{Key: "email"}, Cursor{}, 5); !errors.Is(err, ErrBadSort) {
			t.Errorf("unknown key err = %v, want ErrBadSort", err)
		}
		if _, err := s.Users().Page(ctx, UserFilter{}, Sort{Key: "last_seen"}, Cursor{Value: "yesterday", ID: "x"}, 5); !errors.Is(err, ErrBadCursor) {
			t.Errorf("bad time cursor err = %v, want ErrBadCursor", err)
		}
	})
}

// TestSessionsPageSorts pins the session sort keys the same way, with the
// owner's username read through the join.
func TestSessionsPageSorts(t *testing.T) {
	forEachStore(t, func(t *testing.T, s Store) {
		ctx := context.Background()
		owners := []string{"zed", "amy", "kim"}
		nameOf := map[string]string{}
		var ids []string
		for _, n := range owners {
			u, err := s.Users().Create(ctx, User{Username: n})
			if err != nil {
				t.Fatal(err)
			}
			ids = append(ids, u.ID)
			nameOf[u.ID] = n
		}
		base := time.Date(2026, 9, 1, 8, 0, 0, 0, time.UTC)
		levels := []string{AttestationManaged, AttestationAdvisory, AttestationNone}
		statuses := []string{SessionActive, SessionRevoked, SessionClosed}
		byID := map[string]Session{}
		for i := 0; i < 11; i++ {
			ses, err := s.Sessions().Create(ctx, Session{UserID: ids[i%3], HarnessName: "h", HarnessVersion: "1",
				AttestationLevel: levels[i%3], Status: statuses[(i/2)%3]})
			if err != nil {
				t.Fatal(err)
			}
			at := base.Add(time.Duration((i*5)%17) * time.Second)
			if err := s.Sessions().Touch(ctx, ses.ID, at); err != nil {
				t.Fatal(err)
			}
			ses.LastSeen = at
			byID[ses.ID] = ses
		}
		value := map[string]func(Session) string{
			"":            func(Session) string { return "" },
			"started":     func(Session) string { return "" },
			"last_seen":   func(x Session) string { return byID[x.ID].LastSeen.Format(time.RFC3339Nano) },
			"status":      func(x Session) string { return x.Status },
			"attestation": func(x Session) string { return x.AttestationLevel },
			"user":        func(x Session) string { return nameOf[x.UserID] },
		}
		order := map[string]func(a, b Session) int{
			"":            func(a, b Session) int { return cmpStr(a.ID, b.ID) },
			"started":     func(a, b Session) int { return cmpStr(a.ID, b.ID) },
			"last_seen":   func(a, b Session) int { return byID[a.ID].LastSeen.Compare(byID[b.ID].LastSeen) },
			"status":      func(a, b Session) int { return cmpStr(a.Status, b.Status) },
			"attestation": func(a, b Session) int { return cmpStr(a.AttestationLevel, b.AttestationLevel) },
			"user":        func(a, b Session) int { return cmpStr(nameOf[a.UserID], nameOf[b.UserID]) },
		}
		for key := range value {
			for _, asc := range []bool{false, true} {
				srt := Sort{Key: key, Asc: asc}
				var all []Session
				c := Cursor{}
				for i := 0; i < 20; i++ {
					page, err := s.Sessions().Page(ctx, "", "", srt, c, 4)
					if err != nil {
						t.Fatalf("%+v page %d: %v", srt, i, err)
					}
					if len(page) < 4 {
						all = append(all, page...)
						break
					}
					page = page[:3]
					all = append(all, page...)
					last := page[2]
					c = Cursor{Value: value[key](last), ID: last.ID}
				}
				seenIDs := map[string]bool{}
				for _, x := range all {
					seenIDs[x.ID] = true
				}
				if len(all) != 11 || len(seenIDs) != 11 {
					t.Errorf("%+v: walked %d rows over %d ids, want 11 once each", srt, len(all), len(seenIDs))
				}
				for i := 1; i < len(all); i++ {
					if c := order[key](all[i-1], all[i]); (asc && c > 0) || (!asc && c < 0) {
						t.Errorf("%+v: row %d breaks the order", srt, i)
					}
				}
			}
		}
		// The status filter still composes with a sorted cursor.
		page, err := s.Sessions().Page(ctx, SessionActive, "", Sort{Key: "user", Asc: true}, Cursor{}, 10)
		if err != nil {
			t.Fatal(err)
		}
		for i, x := range page {
			if x.Status != SessionActive {
				t.Errorf("row %d status %s under the active filter", i, x.Status)
			}
			if i > 0 && nameOf[page[i-1].UserID] > nameOf[x.UserID] {
				t.Errorf("row %d (%s) after %s breaks the user order", i, nameOf[x.UserID], nameOf[page[i-1].UserID])
			}
		}
		if _, err := s.Sessions().Page(ctx, "", "", Sort{Key: "harness"}, Cursor{}, 5); !errors.Is(err, ErrBadSort) {
			t.Errorf("unknown key err = %v, want ErrBadSort", err)
		}
	})
}

func cmpStr(a, b string) int {
	if a < b {
		return -1
	}
	if a > b {
		return 1
	}
	return 0
}
