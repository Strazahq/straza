package store

import (
	"context"
	"strconv"
	"testing"
)

// TestUsersKindFilterMatchesKind pins the users list's kind filter and its
// counts to User.Kind over every pair of a type and an attrs kind: each
// user is listed under exactly the kind Go gives it, the counts agree with
// the lists, a deleted user is in neither, the other filters narrow the
// counts, and the kind filter does not. Attrs that do not parse are stored
// only on SQLite, whose attrs column is text. Runs on both drivers.
func TestUsersKindFilterMatchesKind(t *testing.T) {
	forEachStore(t, func(t *testing.T, s Store) {
		ctx := context.Background()
		attrs := []string{"{}", `{"kind":"nhi"}`, `{"kind":"human"}`, `{"kind":1}`, `["nhi"]`}
		if s.(*sqlStore).d == dialectSQLite {
			attrs = append(attrs, `{"kind":`)
		}
		want := map[string]string{}
		n := 0
		for _, typ := range []string{"", UserTypeHuman, UserTypeAgent, UserTypeService} {
			for _, a := range attrs {
				u := User{Username: "u" + strconv.Itoa(n), UserType: typ, Attrs: a}
				if n%2 == 1 {
					u.Sponsor = "kim"
				}
				if _, err := s.Users().Create(ctx, u); err != nil {
					t.Fatalf("create %+v: %v", u, err)
				}
				want[u.Username] = u.Kind()
				n++
			}
		}
		gone, err := s.Users().Create(ctx, User{Username: "gone", UserType: UserTypeService})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := s.Users().SoftDelete(ctx, gone.ID, nil); err != nil {
			t.Fatal(err)
		}

		listed, byGo := map[string]int{}, map[string]int{}
		for _, kind := range want {
			byGo[kind]++
		}
		for _, kind := range []string{UserKindPerson, UserKindAgent, UserKindService} {
			rows, err := s.Users().Page(ctx, UserFilter{Kind: kind}, Sort{}, Cursor{}, 100)
			if err != nil {
				t.Fatal(err)
			}
			for _, u := range rows {
				if want[u.Username] != kind {
					t.Errorf("%s (type %q, attrs %s) listed under %s, Kind says %s", u.Username, u.UserType, u.Attrs, kind, want[u.Username])
				}
			}
			listed[kind] = len(rows)
		}
		counts, err := s.Users().KindCounts(ctx, UserFilter{Kind: UserKindService})
		if err != nil {
			t.Fatal(err)
		}
		for _, kind := range []string{UserKindPerson, UserKindAgent, UserKindService} {
			if listed[kind] != byGo[kind] || counts[kind] != byGo[kind] {
				t.Errorf("%s: listed %d, counted %d, Kind gives %d", kind, listed[kind], counts[kind], byGo[kind])
			}
		}
		sponsored, err := s.Users().KindCounts(ctx, UserFilter{Sponsor: "kim"})
		if err != nil {
			t.Fatal(err)
		}
		if sum := sponsored[UserKindPerson] + sponsored[UserKindAgent] + sponsored[UserKindService]; sum != n/2 {
			t.Errorf("counts under sponsor kim = %v, %d in all, want %d", sponsored, sum, n/2)
		}
	})
}
