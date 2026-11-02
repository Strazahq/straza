package store

import (
	"context"
	"database/sql"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"
)

// TestObjectScopeKey pins the scope key every binding comparison reads: ""
// for no covers line, "every", or the sorted ids, so a rename of an object
// is not a change and the order a file lists objects in is not either.
func TestObjectScopeKey(t *testing.T) {
	tests := []struct {
		name  string
		scope *ObjectScope
		want  string
	}{
		{"no covers line", nil, ""},
		{"every", &ObjectScope{Every: true}, "every"},
		{"one object", &ObjectScope{Objects: []CoveredObject{{ID: "7", Value: "org/a"}}}, "objects:7"},
		{"ids sorted", &ObjectScope{Objects: []CoveredObject{{ID: "9", Value: "org/a"}, {ID: "10", Value: "org/b"}}}, "objects:10\x009"},
		{"a renamed object keeps its key", &ObjectScope{Objects: []CoveredObject{{ID: "9", Value: "org/renamed"}, {ID: "10", Value: "org/b"}}}, "objects:10\x009"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.scope.Key(); got != tc.want {
				t.Errorf("Key() = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestObjectScopeColumn pins the stored text of the object_scope column:
// NULL for no covers line, {"every":true}, or the objects sorted by value
// with id and value, and that a reader gets back what was written. A form
// the gate cannot read is refused on write and read as a fault.
func TestObjectScopeColumn(t *testing.T) {
	two := &ObjectScope{Objects: []CoveredObject{{ID: "2", Value: "org/web"}, {ID: "1", Value: "org/api"}}}
	tests := []struct {
		name  string
		scope *ObjectScope
		text  any
		back  *ObjectScope
	}{
		{"no covers line", nil, nil, nil},
		{"every", &ObjectScope{Every: true}, `{"every":true}`, &ObjectScope{Every: true}},
		{"objects sorted by value", two, `{"objects":[{"id":"1","value":"org/api"},{"id":"2","value":"org/web"}]}`,
			&ObjectScope{Objects: []CoveredObject{{ID: "1", Value: "org/api"}, {ID: "2", Value: "org/web"}}}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			text, err := encodeScope(tc.scope)
			if err != nil {
				t.Fatal(err)
			}
			if text != tc.text {
				t.Fatalf("encodeScope = %#v, want %#v", text, tc.text)
			}
			ns := sql.NullString{}
			if s, ok := text.(string); ok {
				ns = sql.NullString{String: s, Valid: true}
			}
			back, fault := decodeScope("the row b1", ns)
			if fault != "" || !reflect.DeepEqual(back, tc.back) {
				t.Errorf("decodeScope = %+v, %q; want %+v", back, fault, tc.back)
			}
		})
	}
	if two.Objects[0].Value != "org/web" {
		t.Error("encodeScope sorted the caller's slice in place")
	}
	for _, bad := range []*ObjectScope{
		{},
		{Every: true, Objects: []CoveredObject{{ID: "1", Value: "org/api"}}},
		{Objects: []CoveredObject{{ID: "", Value: "org/api"}}},
	} {
		if text, err := encodeScope(bad); err == nil {
			t.Errorf("encodeScope(%+v) = %v, want an error", bad, text)
		}
	}
	for _, bad := range []string{`not json`, `{}`, `{"every":true,"objects":[{"id":"1","value":"a"}]}`, `{"objects":[{"id":"","value":"a"}]}`} {
		if s, fault := decodeScope("the row b1", sql.NullString{String: bad, Valid: true}); s != nil || !strings.Contains(fault, "the row b1") {
			t.Errorf("decodeScope(%s) = %+v, %q; want no scope and the row's fault", bad, s, fault)
		}
	}
}

// TestFingerprintRoleScope pins that a role with no covers line keeps the
// fingerprint it had before covers existed, so open drafts stay valid on
// upgrade, and that the fingerprint reads the scope key: every, the ids,
// never the values or their order.
func TestFingerprintRoleScope(t *testing.T) {
	base := RoleConfig{Role: Role{Name: "ghapp-write", Description: "Writes", Kind: RoleKindApplication, Plane: RolePlaneAccess},
		Owner: "ghapp", Server: "ghapp", Tools: []string{"pr_create", "get_me"}, Implies: []string{"ghapp-read"}}
	// The hex FingerprintRole answered for base before the covers line.
	const golden = "7aa5f14066ef4ae4c042646b981a67fe2c279db39413d4e7d0013fe929cd0bc9"
	if got := FingerprintRole(base); got != golden {
		t.Fatalf("FingerprintRole without a covers line = %s, want the pre-covers %s", got, golden)
	}
	with := func(s *ObjectScope) RoleConfig { c := base; c.Scope = s; return c }
	ab := with(&ObjectScope{Objects: []CoveredObject{{ID: "1", Value: "org/a"}, {ID: "2", Value: "org/b"}}})
	tests := []struct {
		name  string
		b     RoleConfig
		agree bool
	}{
		{"every", with(&ObjectScope{Every: true}), false},
		{"no covers line", with(nil), false},
		{"objects in another order", with(&ObjectScope{Objects: []CoveredObject{{ID: "2", Value: "org/b"}, {ID: "1", Value: "org/a"}}}), true},
		{"a renamed object", with(&ObjectScope{Objects: []CoveredObject{{ID: "1", Value: "org/a2"}, {ID: "2", Value: "org/b"}}}), true},
		{"another object", with(&ObjectScope{Objects: []CoveredObject{{ID: "1", Value: "org/a"}, {ID: "3", Value: "org/b"}}}), false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fa, fb := FingerprintRole(ab), FingerprintRole(tc.b)
			if (fa == fb) != tc.agree {
				t.Errorf("fingerprints agree = %v, want %v", fa == fb, tc.agree)
			}
		})
	}
	if FingerprintRole(with(&ObjectScope{Every: true})) == golden {
		t.Error("every fingerprints as no covers line")
	}
}

// TestToolBindingScopeRoundTrip pins that the binding repo writes and reads
// the covers line on both drivers: none, every, and objects.
func TestToolBindingScopeRoundTrip(t *testing.T) {
	forEachStore(t, func(t *testing.T, s Store) {
		ctx := context.Background()
		app := mustApp(t, s, "ghapp")
		scopes := map[string]*ObjectScope{
			"plain":   nil,
			"every":   {Every: true},
			"payment": {Objects: []CoveredObject{{ID: "11", Value: "org/pay-api"}, {ID: "12", Value: "org/pay-web"}}},
		}
		for name, scope := range scopes {
			ro, err := s.Roles().Create(ctx, Role{Name: name, Kind: RoleKindApplication})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := s.ToolBindings().Create(ctx, ToolBinding{RoleID: ro.ID, AppID: app.ID, ToolMatcher: `["*"]`, Scope: scope}); err != nil {
				t.Fatal(err)
			}
		}
		roles, err := s.Roles().List(ctx)
		if err != nil {
			t.Fatal(err)
		}
		nameOf := map[string]string{}
		for _, ro := range roles {
			nameOf[ro.ID] = ro.Name
		}
		rows, err := s.ToolBindings().List(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if len(rows) != len(scopes) {
			t.Fatalf("bindings = %d, want %d", len(rows), len(scopes))
		}
		for _, b := range rows {
			if want := scopes[nameOf[b.RoleID]]; !reflect.DeepEqual(b.Scope, want) {
				t.Errorf("binding of %s reads scope %+v, want %+v", nameOf[b.RoleID], b.Scope, want)
			}
		}
	})
}

// TestObjectsByValuesAndIDs pins the two lookups drafts and export read: a
// value to its id and an id to its current value, byte for byte, with
// absent keys left out, scoped to one server, across more than one batch.
func TestObjectsByValuesAndIDs(t *testing.T) {
	forEachStore(t, func(t *testing.T, s Store) {
		ctx := context.Background()
		ol := s.ObjectLists()
		app, other := mustApp(t, s, "ghapp"), mustApp(t, s, "gitlab")
		t0 := now()
		many := make([]ListedObject, 1100)
		for i := range many {
			many[i] = ListedObject{ID: fmt.Sprintf("id-%04d", i), Value: fmt.Sprintf("org/repo-%04d", i), Attributes: "{}"}
		}
		many = append(many, ListedObject{ID: "x1", Value: "Org/Pay-API", Attributes: "{}"})
		for _, id := range []string{app.ID, other.ID} {
			if err := ol.Ensure(ctx, id, "repo", "h1", t0); err != nil {
				t.Fatal(err)
			}
			if _, ok, err := ol.Claim(ctx, id, "pod-a", t0, t0.Add(time.Minute)); err != nil || !ok {
				t.Fatalf("claim = %v, %v", ok, err)
			}
		}
		if _, err := ol.Replace(ctx, app.ID, "pod-a", "h1", "l1", many, Listing{Outcome: ObjectsOK, At: t0, Due: t0}); err != nil {
			t.Fatal(err)
		}
		if _, err := ol.Replace(ctx, other.ID, "pod-a", "h1", "l2", []ListedObject{{ID: "z9", Value: "org/only-there", Attributes: "{}"}},
			Listing{Outcome: ObjectsOK, At: t0, Due: t0}); err != nil {
			t.Fatal(err)
		}

		values := []string{"Org/Pay-API", "org/pay-api", "org/only-there", "org/missing"}
		for i := range many[:1000] {
			values = append(values, many[i].Value)
		}
		byValue, err := ol.ObjectsByValues(ctx, app.ID, values)
		if err != nil {
			t.Fatal(err)
		}
		if len(byValue) != 1001 || byValue["Org/Pay-API"] != "x1" || byValue[many[999].Value] != many[999].ID {
			t.Errorf("ObjectsByValues = %d entries, Org/Pay-API=%q; want 1001 and x1", len(byValue), byValue["Org/Pay-API"])
		}
		for _, absent := range []string{"org/pay-api", "org/only-there", "org/missing"} {
			if id, ok := byValue[absent]; ok {
				t.Errorf("ObjectsByValues holds %q as %q, want it absent", absent, id)
			}
		}

		ids := []string{"x1", "z9", "nope"}
		for i := range many[:1000] {
			ids = append(ids, many[i].ID)
		}
		byID, err := ol.ObjectsByIDs(ctx, app.ID, ids)
		if err != nil {
			t.Fatal(err)
		}
		if len(byID) != 1001 || byID["x1"] != "Org/Pay-API" || byID[many[0].ID] != many[0].Value {
			t.Errorf("ObjectsByIDs = %d entries, x1=%q; want 1001 and Org/Pay-API", len(byID), byID["x1"])
		}
		if _, ok := byID["z9"]; ok {
			t.Error("ObjectsByIDs answered another server's object")
		}

		for _, keys := range [][]string{nil, {}} {
			if got, err := ol.ObjectsByValues(ctx, app.ID, keys); err != nil || len(got) != 0 {
				t.Errorf("ObjectsByValues(%v) = %v, %v; want an empty map", keys, got, err)
			}
			if got, err := ol.ObjectsByIDs(ctx, app.ID, keys); err != nil || len(got) != 0 {
				t.Errorf("ObjectsByIDs(%v) = %v, %v; want an empty map", keys, got, err)
			}
		}
	})
}

// TestPinnedType pins which stored manifests a publish reads as pinning
// an object type, the same ones migration 39 backfills: a type that is a
// string and not empty.
func TestPinnedType(t *testing.T) {
	tests := []struct {
		manifest string
		want     string
	}{
		{`{"straza":{"objects":{"type":"repo","plural":"repos"}}}`, "repo"},
		{`{"straza":{"runtime":{"kind":"remote"}}}`, ""},
		{`{"straza":{"objects":{"type":""}}}`, ""},
		{`{"straza":{"objects":{"type":5}}}`, ""},
		{`{"straza":{"objects":null}}`, ""},
		{`{}`, ""},
		{`not json`, ""},
	}
	for _, tc := range tests {
		if got := pinnedType(tc.manifest); got != tc.want {
			t.Errorf("pinnedType(%s) = %q, want %q", tc.manifest, got, tc.want)
		}
	}
}

// TestPublishNamesRepinnedRoles pins that a server put names in Repinned
// the roles with an access row on the server, in id order, when it pins a
// first type, changes the type or unpins, because each rewrites in place
// what those roles' SCIM groups render. A put that keeps the type, a new
// server and another server's roles name none.
func TestPublishNamesRepinnedRoles(t *testing.T) {
	forEachStore(t, func(t *testing.T, s Store) {
		ctx := context.Background()
		app := func(name, server string) RoleConfig {
			return RoleConfig{Role: Role{Name: name, Kind: RoleKindApplication}, Server: server, Tools: []string{"*"}}
		}
		mustPublish(t, s, planOf(t, s, appPut("ghapp", manifestFor("ghapp", "https://gh.example")),
			appPut("brave", manifestFor("brave", "https://brave.example"))))
		mustPublish(t, s, planOf(t, s, rolePut(app("ghapp-read", "ghapp")), rolePut(app("ghapp-write", "ghapp")),
			rolePut(app("brave-read", "brave"))))
		var ids []string
		for _, name := range []string{"ghapp-read", "ghapp-write"} {
			ro, err := s.Roles().GetByName(ctx, name)
			if err != nil {
				t.Fatal(err)
			}
			ids = append(ids, ro.ID)
		}
		slices.Sort(ids)
		projects := strings.Replace(pinnedManifestFor("ghapp", "https://gh.example"), `"type":"repo"`, `"type":"project"`, 1)
		steps := []struct {
			name, server, manifest string
			want                   []string
		}{
			{"a first pin", "ghapp", pinnedManifestFor("ghapp", "https://gh.example"), ids},
			{"a put that keeps the type", "ghapp", pinnedManifestFor("ghapp", "https://gh2.example"), nil},
			{"a type change", "ghapp", projects, ids},
			{"an unpin", "ghapp", manifestFor("ghapp", "https://gh.example"), ids},
			{"a put that keeps no type", "ghapp", manifestFor("ghapp", "https://gh2.example"), nil},
			{"a new server that pins a type", "jira", pinnedManifestFor("jira", "https://jira.example"), nil},
		}
		for _, step := range steps {
			plan := planOf(t, s, appPut(step.server, step.manifest))
			res := mustPublish(t, s, plan)
			assertPublished(t, s, plan, res)
			if got := outcomeOf(t, res, kindApp, step.server).Repinned; !reflect.DeepEqual(got, step.want) {
				t.Errorf("%s: Repinned = %v, want %v", step.name, got, step.want)
			}
		}
	})
}

// TestUnreadableCoversLine pins a stored covers line the gate cannot read,
// such as {}: live state leaves the row out, so the gateway grants nothing
// through it, and names the row in Unreadable with what to do. The binding
// repo still lists the row, without a scope and with the same sentence,
// and every other row reads as before. A publish reads the role as the
// World does, with no covers line, so giving it a covers line replaces
// the row.
func TestUnreadableCoversLine(t *testing.T) {
	forEachStore(t, func(t *testing.T, s Store) {
		ctx := context.Background()
		app := mustApp(t, s, "ghapp")
		var ids []string
		for _, name := range []string{"ghapp-read", "ghapp-write"} {
			ro, err := s.Roles().Create(ctx, Role{Name: name, Kind: RoleKindApplication})
			if err != nil {
				t.Fatal(err)
			}
			b, err := s.ToolBindings().Create(ctx, ToolBinding{RoleID: ro.ID, AppID: app.ID, ToolMatcher: `["*"]`, Scope: &ObjectScope{Every: true}})
			if err != nil {
				t.Fatal(err)
			}
			ids = append(ids, b.ID)
		}
		execSQL(t, s, `UPDATE tool_bindings SET object_scope = '{}' WHERE id = $1`, ids[1])

		st, err := s.Drafts().LiveState(ctx, "")
		if err != nil {
			t.Fatalf("LiveState with one unreadable covers line: %v", err)
		}
		if len(st.Access) != 1 || st.Access[0].ID != ids[0] {
			t.Errorf("live access = %+v, want only the readable row %s", st.Access, ids[0])
		}
		if len(st.Unreadable) != 1 || !strings.Contains(st.Unreadable[0], ids[1]) || !strings.Contains(st.Unreadable[0], "ghapp-write") ||
			!strings.Contains(st.Unreadable[0], "grants nothing") {
			t.Errorf("unreadable = %q, want one sentence naming %s of ghapp-write", st.Unreadable, ids[1])
		}

		rows, err := s.ToolBindings().List(ctx)
		if err != nil {
			t.Fatalf("List with one unreadable covers line: %v", err)
		}
		for _, b := range rows {
			switch b.ID {
			case ids[0]:
				if b.ScopeFault != "" || b.Scope == nil || !b.Scope.Every {
					t.Errorf("the readable row reads %+v, want covers every and no fault", b)
				}
			case ids[1]:
				if b.Scope != nil || !strings.Contains(b.ScopeFault, ids[1]) {
					t.Errorf("the unreadable row reads scope %+v and fault %q, want no scope and its sentence", b.Scope, b.ScopeFault)
				}
			}
		}

		write := RoleConfig{Role: Role{Name: "ghapp-write", Kind: RoleKindApplication}, Server: "ghapp", Tools: []string{"*"}}
		if got := fpOf(t, s, ObjectRef{kindRole, "ghapp-write"}); got != FingerprintRole(write) {
			t.Fatalf("the role with an unreadable covers line fingerprints %s, want it read with none", got)
		}
		write.Scope = &ObjectScope{Every: true}
		plan := planOf(t, s, rolePut(write))
		res := mustPublish(t, s, plan)
		assertPublished(t, s, plan, res)
		if o := outcomeOf(t, res, kindRole, "ghapp-write"); len(o.BindingsRemoved) != 1 || o.BindingsRemoved[0].ID != ids[1] || len(o.BindingsAdded) != 1 {
			t.Errorf("the repair publish reported removed %+v and added %+v, want the unreadable row replaced", o.BindingsRemoved, o.BindingsAdded)
		}
		if st, err := s.Drafts().LiveState(ctx, ""); err != nil || len(st.Access) != 2 || len(st.Unreadable) != 0 {
			t.Errorf("after the repair live state reads %d rows and %q, %v; want both rows and nothing unreadable", len(st.Access), st.Unreadable, err)
		}
	})
}

// TestObjectsLookupDropsImpossibleKeys pins that a key no object list can
// hold, one with a NUL byte or bytes that are not UTF-8, is absent from
// the answer on both drivers rather than an error, because Postgres
// refuses such text in a query.
func TestObjectsLookupDropsImpossibleKeys(t *testing.T) {
	forEachStore(t, func(t *testing.T, s Store) {
		ctx := context.Background()
		ol := s.ObjectLists()
		app := mustApp(t, s, "ghapp")
		t0 := now()
		if err := ol.Ensure(ctx, app.ID, "repo", "h1", t0); err != nil {
			t.Fatal(err)
		}
		if _, ok, err := ol.Claim(ctx, app.ID, "pod-a", t0, t0.Add(time.Minute)); err != nil || !ok {
			t.Fatalf("claim = %v, %v", ok, err)
		}
		if _, err := ol.Replace(ctx, app.ID, "pod-a", "h1", "l1", []ListedObject{{ID: "1", Value: "org/api", Attributes: "{}"}},
			Listing{Outcome: ObjectsOK, At: t0, Due: t0}); err != nil {
			t.Fatal(err)
		}
		keys := []string{"org/api", "org/\x00api", "org/\xffapi", "1", "\x001", "\xc3"}
		byValue, err := ol.ObjectsByValues(ctx, app.ID, keys)
		if err != nil || len(byValue) != 1 || byValue["org/api"] != "1" {
			t.Errorf("ObjectsByValues = %v, %v; want only org/api", byValue, err)
		}
		byID, err := ol.ObjectsByIDs(ctx, app.ID, keys)
		if err != nil || len(byID) != 1 || byID["1"] != "org/api" {
			t.Errorf("ObjectsByIDs = %v, %v; want only 1", byID, err)
		}
	})
}
