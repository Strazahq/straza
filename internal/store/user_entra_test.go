package store

import (
	"context"
	"errors"
	"testing"
)

func TestNormalizeEntraAgentID(t *testing.T) {
	for _, tc := range []struct {
		name, in, want string
		ok             bool
	}{
		{"lower case passes", "dc013442-2b7a-4c1e-9f1a-0123456789ab", "dc013442-2b7a-4c1e-9f1a-0123456789ab", true},
		{"upper case is lowered", "DC013442-2B7A-4C1E-9F1A-0123456789AB", "dc013442-2b7a-4c1e-9f1a-0123456789ab", true},
		{"spaces around are trimmed", "  dc013442-2b7a-4c1e-9f1a-0123456789ab\n", "dc013442-2b7a-4c1e-9f1a-0123456789ab", true},
		{"empty", "", "", false},
		{"a name", "sam-sre-agent", "", false},
		{"braces", "{dc013442-2b7a-4c1e-9f1a-0123456789ab}", "", false},
		{"no hyphens", "dc0134422b7a4c1e9f1a0123456789ab", "", false},
		{"hyphen moved", "dc01344-22b7a-4c1e-9f1a-0123456789ab", "", false},
		{"not hex", "dc013442-2b7a-4c1e-9f1a-0123456789ag", "", false},
		{"one short", "dc013442-2b7a-4c1e-9f1a-0123456789a", "", false},
		{"one long", "dc013442-2b7a-4c1e-9f1a-0123456789abc", "", false},
		{"space inside", "dc013442-2b7a-4c1e-9f1a 0123456789ab", "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := NormalizeEntraAgentID(tc.in)
			if got != tc.want || ok != tc.ok {
				t.Fatalf("NormalizeEntraAgentID(%q) = %q, %v, want %q, %v", tc.in, got, ok, tc.want, tc.ok)
			}
		})
	}
}

// TestUserEntraAgentID pins the Entra agent id column: it round-trips on
// every read lane, the partial write sets and clears it, two live users
// never hold the same id, and a soft-deleted holder frees its id.
func TestUserEntraAgentID(t *testing.T) {
	const id = "dc013442-2b7a-4c1e-9f1a-0123456789ab"
	forEachStore(t, func(t *testing.T, s Store) {
		ctx := context.Background()
		users := s.Users()

		sam, err := users.Create(ctx, User{Username: "ea-sam", UserType: UserTypeAgent, EntraAgentID: id})
		if err != nil {
			t.Fatal(err)
		}
		for name, read := range map[string]func() (User, error){
			"GetByID":           func() (User, error) { return users.GetByID(ctx, sam.ID) },
			"GetByUsername":     func() (User, error) { return users.GetByUsername(ctx, "ea-sam") },
			"GetByEntraAgentID": func() (User, error) { return users.GetByEntraAgentID(ctx, id) },
		} {
			got, err := read()
			if err != nil || got.ID != sam.ID || got.EntraAgentID != id {
				t.Fatalf("%s = %+v, %v, want ea-sam holding %s", name, got, err, id)
			}
		}
		if _, err := users.GetByEntraAgentID(ctx, ""); !errors.Is(err, ErrNotFound) {
			t.Fatalf("GetByEntraAgentID(empty) err = %v, want ErrNotFound", err)
		}

		// Users without an id never collide with each other.
		for _, name := range []string{"ea-plain-1", "ea-plain-2"} {
			if _, err := users.Create(ctx, User{Username: name, UserType: UserTypeAgent}); err != nil {
				t.Fatalf("create %s without an id: %v", name, err)
			}
		}

		// A second live holder is refused by the index, on create and on
		// the partial write.
		if _, err := users.Create(ctx, User{Username: "ea-joe", UserType: UserTypeAgent, EntraAgentID: id}); !errors.Is(err, ErrConflict) {
			t.Fatalf("create a second holder err = %v, want ErrConflict", err)
		}
		joe, err := users.Create(ctx, User{Username: "ea-joe", UserType: UserTypeAgent})
		if err != nil {
			t.Fatal(err)
		}
		taken := id
		if _, err := users.UpdateFields(ctx, joe.ID, UserFields{EntraAgentID: &taken}, nil); !errors.Is(err, ErrConflict) {
			t.Fatalf("partial write of a held id err = %v, want ErrConflict", err)
		}

		// The partial write clears, and ChangedUserFields names the column.
		cleared := sam
		cleared.EntraAgentID = ""
		f := ChangedUserFields(sam, cleared)
		if f.EntraAgentID == nil || *f.EntraAgentID != "" {
			t.Fatalf("ChangedUserFields did not name the cleared id: %+v", f)
		}
		got, err := users.UpdateFields(ctx, sam.ID, f, nil)
		if err != nil || got.EntraAgentID != "" {
			t.Fatalf("clear = %+v, %v", got, err)
		}
		got, err = users.UpdateFields(ctx, joe.ID, UserFields{EntraAgentID: &taken}, nil)
		if err != nil || got.EntraAgentID != id {
			t.Fatalf("partial write of a freed id = %+v, %v", got, err)
		}

		// A soft-deleted holder frees its id, because no API can read the
		// deleted row to clear it.
		if _, err := users.SoftDelete(ctx, joe.ID, nil); err != nil {
			t.Fatal(err)
		}
		pam, err := users.Create(ctx, User{Username: "ea-pam", UserType: UserTypeAgent, EntraAgentID: id})
		if err != nil {
			t.Fatalf("create after the holder was deleted: %v", err)
		}

		// The whole-row Update carries the column too.
		pam.EntraAgentID = ""
		if got, err := users.Update(ctx, pam); err != nil || got.EntraAgentID != "" {
			t.Fatalf("Update clear = %+v, %v", got, err)
		}
	})
}

// TestCheckEntraAgentID pins the rules both write lanes share: only an
// agent holds an Entra agent id, an agent that holds one keeps its type,
// and an id another live user holds is refused with that user named.
func TestCheckEntraAgentID(t *testing.T) {
	const id, other = "dc013442-2b7a-4c1e-9f1a-0123456789ab", "5f8c2a1e-3b4d-4e6f-9a7b-1c2d3e4f5a6b"
	forEachStore(t, func(t *testing.T, s Store) {
		ctx := context.Background()
		joe, err := s.Users().Create(ctx, User{Username: "ck-joe", UserType: UserTypeAgent, EntraAgentID: id})
		if err != nil {
			t.Fatal(err)
		}
		agent := User{ID: "self", Username: "ck-sam", UserType: UserTypeAgent}
		withID := func(u User, v string) User { u.EntraAgentID = v; return u }
		withType := func(u User, v string) User { u.UserType = v; return u }
		for _, tc := range []struct {
			name          string
			before, after User
			want          EntraAgentRefusal
			holder        string
		}{
			{"no id, nothing to check", agent, agent, EntraAgentOK, ""},
			{"agent takes a free id", agent, withID(agent, other), EntraAgentOK, ""},
			{"agent clears its id", withID(agent, other), agent, EntraAgentOK, ""},
			{"person takes an id", withType(agent, UserTypeHuman), withID(withType(agent, UserTypeHuman), other), EntraAgentNotAgent, ""},
			{"service takes an id", withType(agent, UserTypeService), withID(withType(agent, UserTypeService), other), EntraAgentNotAgent, ""},
			{"unclassified takes an id", withType(agent, ""), withID(withType(agent, ""), other), EntraAgentNotAgent, ""},
			{"agent holding an id turns person", withID(agent, other), withType(withID(agent, other), UserTypeHuman), EntraAgentTypeChange, ""},
			{"agent holding an id turns person and clears it", withID(agent, other), withType(agent, UserTypeHuman), EntraAgentOK, ""},
			{"agent takes an id joe holds", agent, withID(agent, id), EntraAgentHeld, "ck-joe"},
			{"joe keeps its own id", joe, joe, EntraAgentOK, ""},
		} {
			t.Run(tc.name, func(t *testing.T) {
				got, holder, err := CheckEntraAgentID(ctx, s.Users(), tc.before, tc.after)
				if err != nil {
					t.Fatal(err)
				}
				if got != tc.want || holder.Username != tc.holder {
					t.Fatalf("CheckEntraAgentID = %v, holder %q, want %v, holder %q", got, holder.Username, tc.want, tc.holder)
				}
			})
		}
	})
}
