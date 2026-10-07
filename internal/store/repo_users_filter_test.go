package store

import (
	"context"
	"testing"
	"time"
)

// Server-side user filters: Q substring (case-insensitive, over
// username/email/external_id), Status exact, RoleIDs effective at Now
// (direct assignments, validity windows evaluated in SQL with the
// resolver's semantics: from inclusive, to exclusive). Runs on both drivers.
func TestUsersPageFilters(t *testing.T) {
	forEachStore(t, func(t *testing.T, s Store) {
		ctx := context.Background()
		now := time.Now().UTC()
		page := func(f UserFilter) []string {
			t.Helper()
			rows, err := s.Users().Page(ctx, f, Sort{}, Cursor{}, 50)
			if err != nil {
				t.Fatalf("page %+v: %v", f, err)
			}
			names := make([]string, len(rows))
			for i, u := range rows {
				names[i] = u.Username
			}
			return names
		}
		has := func(names []string, want ...string) bool {
			set := map[string]bool{}
			for _, n := range names {
				set[n] = true
			}
			for _, w := range want {
				if !set[w] {
					return false
				}
			}
			return len(names) == len(want)
		}

		alice, err := s.Users().Create(ctx, User{Username: "alice", Email: "Alice@corp.example", ExternalID: "ext-alpha"})
		if err != nil {
			t.Fatal(err)
		}
		bob, _ := s.Users().Create(ctx, User{Username: "bob", Email: "bob@corp.example"})
		carol, _ := s.Users().Create(ctx, User{Username: "carol"})
		dana, _ := s.Users().Create(ctx, User{Username: "dana"})
		bob.Status = UserDisabled
		if _, err := s.Users().Update(ctx, bob); err != nil {
			t.Fatal(err)
		}

		reader, _ := s.Roles().Create(ctx, Role{Name: "reader", Kind: RoleKindBusiness})
		admin, _ := s.Roles().Create(ctx, Role{Name: "admin", Kind: RoleKindBusiness})
		// alice: direct reader inside a live window. bob: direct admin.
		// carol: direct reader (open window). dana: reader EXPIRED yesterday.
		from, to := now.Add(-time.Hour), now.Add(time.Hour)
		yesterday := now.Add(-24 * time.Hour)
		mustAssign := func(a RoleAssignment) {
			t.Helper()
			if _, err := s.Roles().Assign(ctx, a); err != nil {
				t.Fatal(err)
			}
		}
		mustAssign(RoleAssignment{SubjectKind: SubjectUser, SubjectID: alice.ID, RoleID: reader.ID, ValidFrom: &from, ValidTo: &to})
		mustAssign(RoleAssignment{SubjectKind: SubjectUser, SubjectID: bob.ID, RoleID: admin.ID})
		mustAssign(RoleAssignment{SubjectKind: SubjectUser, SubjectID: carol.ID, RoleID: reader.ID})
		mustAssign(RoleAssignment{SubjectKind: SubjectUser, SubjectID: dana.ID, RoleID: reader.ID, ValidTo: &yesterday})

		// Q: username, email (cross-case), external_id; substring not prefix.
		if got := page(UserFilter{Q: "LIC"}); !has(got, "alice") {
			t.Errorf("Q=LIC = %v, want alice via username substring", got)
		}
		if got := page(UserFilter{Q: "alice@CORP"}); !has(got, "alice") {
			t.Errorf("Q=alice@CORP = %v, want alice via email", got)
		}
		if got := page(UserFilter{Q: "ext-alpha"}); !has(got, "alice") {
			t.Errorf("Q=ext-alpha = %v, want alice via external_id", got)
		}
		if got := page(UserFilter{Q: "corp.example"}); !has(got, "alice", "bob") {
			t.Errorf("Q=corp.example = %v, want both mail holders", got)
		}

		if got := page(UserFilter{Status: UserDisabled}); !has(got, "bob") {
			t.Errorf("status=disabled = %v, want bob only", got)
		}

		// Role lane: direct match, expired window excluded, and the
		// admin holder appears only when the caller passes the closure.
		if got := page(UserFilter{RoleIDs: []string{reader.ID}, Now: now}); !has(got, "alice", "carol") {
			t.Errorf("role=reader = %v, want alice (direct, windowed) and carol, never expired dana", got)
		}
		if got := page(UserFilter{RoleIDs: []string{reader.ID, admin.ID}, Now: now}); !has(got, "alice", "carol", "bob") {
			t.Errorf("role closure = %v, want bob joining via the implied-role id", got)
		}

		// Filters compose with each other and with the keyset cursor.
		if got := page(UserFilter{RoleIDs: []string{reader.ID, admin.ID}, Status: UserDisabled, Now: now}); !has(got, "bob") {
			t.Errorf("role+status = %v, want bob only", got)
		}
		p1, err := s.Users().Page(ctx, UserFilter{RoleIDs: []string{reader.ID, admin.ID}, Now: now}, Sort{}, Cursor{}, 2)
		if err != nil || len(p1) != 2 {
			t.Fatalf("filtered page 1 = %v (err %v)", p1, err)
		}
		p2, err := s.Users().Page(ctx, UserFilter{RoleIDs: []string{reader.ID, admin.ID}, Now: now}, Sort{}, Cursor{ID: p1[1].ID}, 2)
		if err != nil || len(p2) != 1 {
			t.Fatalf("filtered page 2 = %v (err %v), want the one remaining holder", p2, err)
		}
	})
}

// Batched last-seen: one grouped query answers a whole page, users
// with no sessions are simply absent.
func TestSessionsLastSeenByUsers(t *testing.T) {
	forEachStore(t, func(t *testing.T, s Store) {
		ctx := context.Background()
		u1, _ := s.Users().Create(ctx, User{Username: "seen"})
		u2, _ := s.Users().Create(ctx, User{Username: "busy"})
		u3, _ := s.Users().Create(ctx, User{Username: "never"})
		if _, err := s.Sessions().Create(ctx, Session{UserID: u1.ID}); err != nil {
			t.Fatal(err)
		}
		s1, _ := s.Sessions().Create(ctx, Session{UserID: u2.ID})
		s2, _ := s.Sessions().Create(ctx, Session{UserID: u2.ID})
		later := time.Now().UTC().Add(time.Hour)
		if err := s.Sessions().Touch(ctx, s2.ID, later); err != nil {
			t.Fatal(err)
		}
		_ = s1

		got, err := s.Sessions().LastSeenByUsers(ctx, []string{u1.ID, u2.ID, u3.ID})
		if err != nil {
			t.Fatal(err)
		}
		if _, ok := got[u1.ID]; !ok {
			t.Errorf("u1 missing from last-seen map")
		}
		if ls, ok := got[u2.ID]; !ok || ls.Unix() != later.Unix() {
			t.Errorf("u2 last-seen = %v (ok %v), want the touched MAX %v", ls, ok, later)
		}
		if _, ok := got[u3.ID]; ok {
			t.Errorf("sessionless u3 must be absent, got an entry")
		}
		if empty, err := s.Sessions().LastSeenByUsers(ctx, nil); err != nil || len(empty) != 0 {
			t.Errorf("empty id list = %v (err %v), want empty map", empty, err)
		}
	})
}
