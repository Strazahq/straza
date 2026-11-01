package store

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
)

// pinnedManifestFor answers a remote server's manifest that pins the
// object type repo.
func pinnedManifestFor(name, url string) string {
	return `{"metadata":{"name":"` + name + `","version":"1.0.0"},"straza":{"runtime":{"kind":"remote","url":"` + url + `"},` +
		`"objects":{"type":"repo","plural":"repos","list":{"tool":"repo_list"}}}}`
}

// TestPublishCovers pins the covers line through a publish on both
// drivers. The first pin of a server's type gives its rows without a
// covers line covers every in place, a row that has one keeps it, and
// another server's rows stay as they were. A put that changes only the
// covers line replaces the row and reports both rows with their scopes. An
// unpin clears the server's covers lines in place. Live state reads each
// row's covers line as the gateway will hold it.
func TestPublishCovers(t *testing.T) {
	forEachStore(t, func(t *testing.T, s Store) {
		app := func(name string) RoleConfig {
			return RoleConfig{Role: Role{Name: name, Description: "Uses " + name, Kind: RoleKindApplication}}
		}
		read, write, brave := app("ghapp-read"), app("ghapp-write"), app("brave-read")
		read.Server, read.Tools = "ghapp", []string{"repo_get"}
		write.Server, write.Tools = "ghapp", []string{"pr_create", "repo_get"}
		brave.Server, brave.Tools = "brave", []string{"search"}
		mustPublish(t, s, planOf(t, s, appPut("ghapp", manifestFor("ghapp", "https://gh.example")),
			appPut("brave", manifestFor("brave", "https://brave.example"))))
		mustPublish(t, s, planOf(t, s, rolePut(read), rolePut(write), rolePut(brave)))
		before := liveScopes(t, s)

		plan := planOf(t, s, appPut("ghapp", pinnedManifestFor("ghapp", "https://gh.example")))
		res := mustPublish(t, s, plan)
		assertPublished(t, s, plan, res)
		if o := outcomeOf(t, res, kindApp, "ghapp"); len(o.BindingsAdded)+len(o.BindingsRemoved) != 0 {
			t.Errorf("the first pin reported access rows %+v and %+v, want none", o.BindingsAdded, o.BindingsRemoved)
		}
		every := &ObjectScope{Every: true}
		pinned := liveScopes(t, s)
		assertScopes(t, "after the first pin", pinned, map[string]*ObjectScope{"ghapp-read": every, "ghapp-write": every, "brave-read": nil})
		for role, row := range before {
			if pinned[role].ID != row.ID {
				t.Errorf("the first pin replaced the row of %s, want it kept in place", role)
			}
		}

		narrowed := write
		narrowed.Scope = &ObjectScope{Objects: []CoveredObject{{ID: "12", Value: "org/pay-web"}, {ID: "11", Value: "org/pay-api"}}}
		plan = planOf(t, s, rolePut(narrowed))
		res = mustPublish(t, s, plan)
		assertPublished(t, s, plan, res)
		o := outcomeOf(t, res, kindRole, "ghapp-write")
		stored := &ObjectScope{Objects: []CoveredObject{{ID: "11", Value: "org/pay-api"}, {ID: "12", Value: "org/pay-web"}}}
		if len(o.BindingsRemoved) != 1 || !reflect.DeepEqual(o.BindingsRemoved[0].Scope, every) ||
			len(o.BindingsAdded) != 1 || !reflect.DeepEqual(o.BindingsAdded[0].Scope, stored) {
			t.Errorf("the covers change reported removed %+v and added %+v; want the every row out and the objects row in", o.BindingsRemoved, o.BindingsAdded)
		}
		after := liveScopes(t, s)
		assertScopes(t, "after the covers change", after, map[string]*ObjectScope{"ghapp-read": every, "ghapp-write": stored, "brave-read": nil})
		if after["ghapp-write"].ID == pinned["ghapp-write"].ID || len(o.BindingsAdded) != 1 || after["ghapp-write"].ID != o.BindingsAdded[0].ID {
			t.Errorf("the covers change kept row %s, want the new row it reported", after["ghapp-write"].ID)
		}

		plan = planOf(t, s, appPut("ghapp", manifestFor("ghapp", "https://gh.example")))
		res = mustPublish(t, s, plan)
		assertPublished(t, s, plan, res)
		unpinned := liveScopes(t, s)
		assertScopes(t, "after the unpin", unpinned, map[string]*ObjectScope{"ghapp-read": nil, "ghapp-write": nil, "brave-read": nil})
		if unpinned["ghapp-write"].ID != after["ghapp-write"].ID {
			t.Error("the unpin replaced a row, want it cleared in place")
		}

		execSQL(t, s, `UPDATE tool_bindings SET object_scope = $1 WHERE id = $2`,
			`{"objects":[{"id":"11","value":"org/pay-api"}]}`, unpinned["ghapp-write"].ID)
		plan = planOf(t, s, appPut("ghapp", pinnedManifestFor("ghapp", "https://gh.example")))
		mustPublish(t, s, plan)
		repinned := liveScopes(t, s)
		assertScopes(t, "after a pin over a row with a covers line", repinned, map[string]*ObjectScope{
			"ghapp-read": every, "ghapp-write": {Objects: []CoveredObject{{ID: "11", Value: "org/pay-api"}}}, "brave-read": nil})

		described := read
		described.Role.Description, described.Scope = "Reads repos", every
		plan = planOf(t, s, rolePut(described))
		execSQL(t, s, `UPDATE tool_bindings SET object_scope = $1 WHERE id = $2`,
			`{"objects":[{"id":"12","value":"org/pay-web"}]}`, repinned["ghapp-read"].ID)
		var conflict PublishConflict
		if _, err := s.Drafts().Publish(context.Background(), plan); !errors.As(err, &conflict) ||
			conflict.Object != (ObjectRef{kindRole, "ghapp-read"}) {
			t.Errorf("a publish checked before the covers line moved answered %v, want a conflict on ghapp-read", err)
		}
	})
}

// liveScopes answers the live access rows by role name, as LiveState
// reads them.
func liveScopes(t *testing.T, s Store) map[string]LiveAccess {
	t.Helper()
	st, err := s.Drafts().LiveState(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]LiveAccess{}
	for _, la := range st.Access {
		out[la.Role] = la
	}
	return out
}

func assertScopes(t *testing.T, when string, got map[string]LiveAccess, want map[string]*ObjectScope) {
	t.Helper()
	for role, scope := range want {
		row, ok := got[role]
		if !ok {
			t.Errorf("%s: live state holds no row of %s", when, role)
			continue
		}
		if !reflect.DeepEqual(row.Scope, scope) {
			t.Errorf("%s: the row of %s covers %+v, want %+v", when, role, row.Scope, scope)
		}
	}
}

// TestPublishFirstPinWithAKeptRole pins one plan that pins a server's type
// for the first time and puts a role whose row on that server has no
// covers line and an After of every, as the draft check plans it. The
// publish lands, the row keeps its id and covers every, and no access row
// is reported, so no binding record is written. The same holds for an
// unpin with a role whose row loses its covers line.
func TestPublishFirstPinWithAKeptRole(t *testing.T) {
	forEachStore(t, func(t *testing.T, s Store) {
		read := RoleConfig{Role: Role{Name: "ghapp-read", Description: "Reads", Kind: RoleKindApplication},
			Server: "ghapp", Tools: []string{"repo_get"}}
		mustPublish(t, s, planOf(t, s, appPut("ghapp", manifestFor("ghapp", "https://gh.example"))))
		mustPublish(t, s, planOf(t, s, rolePut(read)))
		before := liveScopes(t, s)["ghapp-read"]

		kept := read
		kept.Role.Description, kept.Scope = "Reads repos", &ObjectScope{Every: true}
		plan := planOf(t, s, appPut("ghapp", pinnedManifestFor("ghapp", "https://gh.example")), rolePut(kept))
		res := mustPublish(t, s, plan)
		assertPublished(t, s, plan, res)
		if o := outcomeOf(t, res, kindRole, "ghapp-read"); len(o.BindingsAdded)+len(o.BindingsRemoved) != 0 {
			t.Errorf("the first pin with a kept role reported access rows %+v and %+v, want none", o.BindingsAdded, o.BindingsRemoved)
		}
		pinned := liveScopes(t, s)["ghapp-read"]
		if pinned.ID != before.ID || !reflect.DeepEqual(pinned.Scope, &ObjectScope{Every: true}) {
			t.Errorf("after the first pin the row of ghapp-read is %s covering %+v, want %s covering every", pinned.ID, pinned.Scope, before.ID)
		}

		unpinned := kept
		unpinned.Role.Description, unpinned.Scope = "Reads", nil
		plan = planOf(t, s, appPut("ghapp", manifestFor("ghapp", "https://gh.example")), rolePut(unpinned))
		res = mustPublish(t, s, plan)
		assertPublished(t, s, plan, res)
		if o := outcomeOf(t, res, kindRole, "ghapp-read"); len(o.BindingsAdded)+len(o.BindingsRemoved) != 0 {
			t.Errorf("the unpin with a kept role reported access rows %+v and %+v, want none", o.BindingsAdded, o.BindingsRemoved)
		}
		if row := liveScopes(t, s)["ghapp-read"]; row.ID != before.ID || row.Scope != nil {
			t.Errorf("after the unpin the row of ghapp-read is %s covering %+v, want %s with no covers line", row.ID, row.Scope, before.ID)
		}
	})
}

// TestUnpinKeepsAnUnreadableRow pins that an unpin clears the covers lines
// it can read and leaves a line it cannot read as it is, so that row keeps
// granting nothing rather than reaching every tool once the line is gone.
func TestUnpinKeepsAnUnreadableRow(t *testing.T) {
	forEachStore(t, func(t *testing.T, s Store) {
		read := RoleConfig{Role: Role{Name: "ghapp-read", Kind: RoleKindApplication}, Server: "ghapp", Tools: []string{"repo_get"}}
		odd := RoleConfig{Role: Role{Name: "ghapp-x", Kind: RoleKindApplication}, Server: "ghapp", Tools: []string{"*"}}
		mustPublish(t, s, planOf(t, s, appPut("ghapp", manifestFor("ghapp", "https://gh.example"))))
		mustPublish(t, s, planOf(t, s, rolePut(read), rolePut(odd)))
		mustPublish(t, s, planOf(t, s, appPut("ghapp", pinnedManifestFor("ghapp", "https://gh.example"))))
		bad := liveScopes(t, s)["ghapp-x"].ID
		execSQL(t, s, `UPDATE tool_bindings SET object_scope = '{}' WHERE id = $1`, bad)

		plan := planOf(t, s, appPut("ghapp", manifestFor("ghapp", "https://gh.example")))
		assertPublished(t, s, plan, mustPublish(t, s, plan))
		rows, err := s.ToolBindings().List(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		for _, b := range rows {
			switch {
			case b.ID == bad && (b.Scope != nil || b.ScopeFault == ""):
				t.Errorf("after the unpin ghapp-x reads scope %+v and fault %q, want its unreadable line kept", b.Scope, b.ScopeFault)
			case b.ID != bad && (b.Scope != nil || b.ScopeFault != ""):
				t.Errorf("after the unpin the row %s reads scope %+v and fault %q, want no covers line", b.ID, b.Scope, b.ScopeFault)
			}
		}
		st, err := s.Drafts().LiveState(context.Background(), "")
		if err != nil || len(st.Unreadable) != 1 || !strings.Contains(st.Unreadable[0], bad) {
			t.Errorf("live state after the unpin names %q, %v; want the row %s as unreadable", st.Unreadable, err, bad)
		}
		if _, ok := liveScopes(t, s)["ghapp-x"]; ok {
			t.Error("after the unpin live state grants through ghapp-x")
		}
	})
}
