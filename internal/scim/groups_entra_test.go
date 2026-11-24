package scim

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"

	"github.com/strazahq/straza/internal/store"
)

// TestGroupsLeaveEntraHoldersAlone pins that SCIM neither lists nor changes
// a role assignment the Entra sync wrote: the group's members and a user's
// groups leave such a row out, and no replace, remove or add ends it or
// reports it.
func TestGroupsLeaveEntraHoldersAlone(t *testing.T) {
	g := newAtomicGroupServer(t, nil)
	ctx := context.Background()
	role, err := g.st.Roles().Create(ctx, store.Role{Name: "mixed", Kind: store.RoleKindBusiness})
	if err != nil {
		t.Fatal(err)
	}
	ids := map[string]string{}
	for _, name := range []string{"kim", "bob", "eve"} {
		u, err := g.st.Users().Create(ctx, store.User{Username: name, Origin: store.OriginSCIM})
		if err != nil {
			t.Fatal(err)
		}
		ids[name] = u.ID
	}
	row := func(name, origin string) store.RoleAssignment {
		return store.RoleAssignment{SubjectKind: store.SubjectUser, SubjectID: ids[name], Origin: origin}
	}
	if _, _, err := g.st.Roles().ApplyMembership(ctx, role.ID, []store.RoleAssignment{
		row("kim", store.OriginSCIM), row("bob", store.OriginSCIM), row("bob", store.OriginEntra), row("eve", store.OriginEntra),
	}, nil); err != nil {
		t.Fatal(err)
	}
	do := func(method, path, body string) map[string]any {
		t.Helper()
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer t")
		g.mux.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s %s = %d %s", method, path, rec.Code, rec.Body.String())
		}
		var out map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
		return out
	}
	names := map[string]string{}
	for n, id := range ids {
		names[id] = n
	}
	listed := func(res map[string]any) []string {
		var out []string
		ms, _ := res["members"].([]any)
		for _, m := range ms {
			out = append(out, names[m.(map[string]any)["value"].(string)])
		}
		sort.Strings(out)
		return out
	}
	stored := func() []string {
		t.Helper()
		asg, err := g.st.Roles().AssignmentsByRole(ctx, role.ID)
		if err != nil {
			t.Fatal(err)
		}
		var out []string
		for _, a := range asg {
			out = append(out, names[a.SubjectID]+"/"+a.Origin)
		}
		sort.Strings(out)
		return out
	}
	patch := func(ops string) string {
		return `{"schemas":["urn:ietf:params:scim:api:messages:2.0:PatchOp"],"Operations":` + ops + `}`
	}
	group := "/scim/v2/Groups/" + role.ID
	steps := []struct {
		name, method, body string
		wantListed         string
		wantStored         string
		wantChanges        int
	}{
		{"a read lists the holders SCIM may change", "GET", "",
			"bob,kim", "bob/entra,bob/scim,eve/entra,kim/scim", 0},
		{"a replace ends only the SCIM row of a holder it leaves out", "PUT",
			`{"schemas":["` + SchemaGroup + `"],"displayName":"mixed","members":[{"value":"` + ids["kim"] + `"}]}`,
			"kim", "bob/entra,eve/entra,kim/scim", 1},
		{"a remove of a holder only Entra wrote changes nothing", "PATCH",
			patch(`[{"op":"remove","path":"members[value eq \"` + ids["eve"] + `\"]"}]`),
			"kim", "bob/entra,eve/entra,kim/scim", 0},
		{"a remove of every member ends only SCIM's rows", "PATCH",
			patch(`[{"op":"remove","path":"members"}]`),
			"", "bob/entra,eve/entra", 1},
		{"an add of a holder Entra wrote starts a SCIM row beside it", "PATCH",
			patch(`[{"op":"add","path":"members","value":[{"value":"` + ids["eve"] + `"}]}]`),
			"eve", "bob/entra,eve/entra,eve/scim", 1},
	}
	for _, st := range steps {
		_, before := g.reported()
		res := do(st.method, group, st.body)
		if got := strings.Join(listed(res), ","); got != st.wantListed {
			t.Errorf("%s: members = %q, want %q", st.name, got, st.wantListed)
		}
		if got := strings.Join(stored(), ","); got != st.wantStored {
			t.Errorf("%s: stored = %q, want %q", st.name, got, st.wantStored)
		}
		_, after := g.reported()
		if n := len(after) - len(before); n != st.wantChanges {
			t.Errorf("%s: %d membership changes reported, want %d", st.name, n, st.wantChanges)
		}
		for _, c := range after[len(before):] {
			if c.Origin == store.OriginEntra {
				t.Errorf("%s: a change of an Entra row was reported: %+v", st.name, c)
			}
		}
	}
	for name, want := range map[string]int{"bob": 0, "eve": 1} {
		res := do("GET", "/scim/v2/Users/"+ids[name], "")
		groups, _ := res["groups"].([]any)
		if len(groups) != want {
			t.Errorf("%s's groups = %v, want %d: a role held only from Entra is left out and a role held twice shows once", name, groups, want)
		}
	}
}
