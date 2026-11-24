package scim

import (
	"cmp"
	"context"
	"encoding/json"
	"net/http"
	"reflect"
	"slices"
	"testing"

	"github.com/strazahq/straza/internal/store"
)

// renameCountStore counts the user writes that may move a sponsor.
type renameCountStore struct {
	store.Store
	renames *int
}

func (s renameCountStore) Users() store.UserRepo { return renameCountUsers{s.Store.Users(), s} }

type renameCountUsers struct {
	store.UserRepo
	s renameCountStore
}

func (u renameCountUsers) UpdateRenaming(ctx context.Context, row store.User) (store.User, []string, error) {
	*u.s.renames++
	return u.UserRepo.UpdateRenaming(ctx, row)
}

// TestUserRenameMovesSponsoredAgents pins that a PUT or a PATCH that changes
// a userName writes the user through the store's UpdateRenaming, which moves
// the sponsor of the two agents the old name sponsors to the new name and
// leaves an agent with another sponsor alone. The write reports the update
// of the renamed user, then one update of the sponsor for each agent it
// moved. A rename to a taken name answers the 409 it answers without
// sponsored agents and changes nothing. A write that keeps the userName
// goes the same way and moves nobody.
func TestUserRenameMovesSponsoredAgents(t *testing.T) {
	const core = `"urn:ietf:params:scim:schemas:core:2.0:User"`
	put := func(name, title string) string {
		return `{"schemas":[` + core + `],"userName":"` + name + `","externalId":"idm-ada","displayName":"Ada","title":"` + title + `",` +
			`"emails":[{"value":"ada@x.io","primary":true}],"active":true}`
	}
	patch := func(ops string) string {
		return `{"schemas":["urn:ietf:params:scim:api:messages:2.0:PatchOp"],"Operations":[` + ops + `]}`
	}
	ctx := context.Background()
	for _, tc := range []struct {
		name   string
		method string
		body   string
		code   int
		// renamed says that ada is stored as ada.new and that the two agents
		// she sponsored moved with her.
		renamed bool
		renames int
		// changed is the changed list reported for ada, nil for no report.
		changed []string
	}{
		{"a PUT rename", http.MethodPut, put("ada.new", "Engineer"), http.StatusOK, true, 1, []string{"username"}},
		{"a PATCH rename", http.MethodPatch, patch(`{"op":"replace","path":"userName","value":"ada.new"}`), http.StatusOK, true, 1, []string{"username"}},
		{"a PUT rename to a taken name", http.MethodPut, put("kim", "Staff engineer"), http.StatusConflict, false, 1, nil},
		{"a PATCH rename to a taken name", http.MethodPatch,
			patch(`{"op":"replace","path":"userName","value":"kim"},{"op":"replace","path":"title","value":"Staff engineer"}`),
			http.StatusConflict, false, 1, nil},
		{"a PUT that keeps the name", http.MethodPut, put("ada", "Staff engineer"), http.StatusOK, false, 1, []string{"title"}},
		{"a PATCH that keeps the name", http.MethodPatch, patch(`{"op":"replace","path":"title","value":"Staff engineer"}`), http.StatusOK, false, 1, []string{"title"}},
	} {
		st := newPlanesTestStore(t)
		renames := 0
		mux, writes := userWrittenServer(t, renameCountStore{st, &renames})
		ada := seedSCIMUser(t, st, "ada")
		seedSCIMUser(t, st, "kim")
		agents := map[string]string{}
		for _, a := range []struct{ name, sponsor string }{{"ada-agent-1", "ada"}, {"ada-agent-2", "ada"}, {"kim-agent", "kim"}} {
			u, err := st.Users().Create(ctx, store.User{Username: a.name, Status: store.UserActive, Origin: store.OriginSCIM,
				UserType: store.UserTypeAgent, Attrs: `{"kind":"nhi"}`, Sponsor: a.sponsor})
			if err != nil {
				t.Fatal(err)
			}
			agents[a.name] = u.ID
		}
		before, err := st.Users().GetByID(ctx, ada.ID)
		if err != nil {
			t.Fatal(err)
		}

		rec := scimReq(t, mux, tc.method, "/scim/v2/Users/"+ada.ID, tc.body)
		if rec.Code != tc.code {
			t.Fatalf("%s: status %d, want %d: %s", tc.name, rec.Code, tc.code, rec.Body)
		}
		if renames != tc.renames {
			t.Errorf("%s: %d renames, want %d", tc.name, renames, tc.renames)
		}
		stored, err := st.Users().GetByID(ctx, ada.ID)
		if err != nil {
			t.Fatal(err)
		}
		name := "ada"
		if tc.renamed {
			name = "ada.new"
		}
		if stored.Username != name {
			t.Errorf("%s: stored userName %q, want %q", tc.name, stored.Username, name)
		}
		if tc.code == http.StatusConflict {
			var body map[string]any
			if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			if body["scimType"] != "uniqueness" || body["detail"] != "userName already exists" {
				t.Errorf("%s: answer %s, want the uniqueness 409", tc.name, rec.Body)
			}
			if !reflect.DeepEqual(before, stored) {
				t.Errorf("%s: stored row\n got %+v\nwant %+v", tc.name, stored, before)
			}
		}
		for agent, sponsor := range map[string]string{"ada-agent-1": name, "ada-agent-2": name, "kim-agent": "kim"} {
			if u, err := st.Users().GetByID(ctx, agents[agent]); err != nil || u.Sponsor != sponsor {
				t.Errorf("%s: sponsor of %s = %q (%v), want %q", tc.name, agent, u.Sponsor, err, sponsor)
			}
		}

		var want []userWrite
		if tc.changed != nil {
			want = append(want, userWrite{UserUpdated, ada.ID, tc.changed})
		}
		if tc.renamed {
			moved := []userWrite{{UserUpdated, agents["ada-agent-1"], []string{"sponsor"}}, {UserUpdated, agents["ada-agent-2"], []string{"sponsor"}}}
			slices.SortFunc(moved, func(a, b userWrite) int { return cmp.Compare(a.id, b.id) })
			want = append(want, moved...)
		}
		if !reflect.DeepEqual(*writes, want) {
			t.Errorf("%s: reported\n got %v\nwant %v", tc.name, *writes, want)
		}
	}
}
