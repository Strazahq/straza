package store

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

// TestFingerprintAppPairs pins what the App fingerprint reads: two rows
// agree when only a column other than the manifest differs, or when the
// manifest is rendered another way, and differ on any change of a value.
func TestFingerprintAppPairs(t *testing.T) {
	base := App{ID: "a1", Name: "demo", Version: "1.0.0", RuntimeKind: "remote", Status: "running", Source: AppSourceAPI,
		AdminRoleID: "r1", Manifest: `{"metadata":{"name":"demo"},"straza":{"limits":{"rps":5e-7}}}`,
		CreatedAt: time.Unix(1, 0), UpdatedAt: time.Unix(2, 0)}
	with := func(edit func(*App)) App {
		a := base
		edit(&a)
		return a
	}
	manifest := func(m string) App { return with(func(a *App) { a.Manifest = m }) }
	tests := []struct {
		name  string
		a, b  App
		agree bool
	}{
		{"status changed", base, with(func(a *App) { a.Status = "degraded" }), true},
		{"updated_at changed", base, with(func(a *App) { a.UpdatedAt = time.Unix(3, 0) }), true},
		{"source changed", base, with(func(a *App) { a.Source = AppSourceGitops }), true},
		{"admin role changed", base, with(func(a *App) { a.AdminRoleID = "r2" }), true},
		{"version column changed", base, with(func(a *App) { a.Version = "2.0.0" }), true},
		{"keys in another order with other spacing", manifest(`{"a":1,"b":{"c":"x","d":[1,2]}}`),
			manifest(`{ "b": {"d": [1, 2], "c": "x"},
				"a": 1 }`), true},
		{"a number in exponent form and in plain decimals", manifest(`{"rps":5e-7}`), manifest(`{"rps":0.0000005}`), true},
		{"a large exponent and the plain integer", manifest(`{"n":1e+21}`), manifest(`{"n":1000000000000000000000}`), true},
		{"a number with a scale and without", manifest(`{"n":1.50e1}`), manifest(`{"n":15}`), true},
		{"negative zero and zero", manifest(`{"n":-0}`), manifest(`{"n":0.0}`), true},
		{"escaped and literal characters", manifest(`{"s":"<a&b>\u00e9\/"}`), manifest(`{"s":"<a&b>é/"}`), true},
		{"a manifest leaf", manifest(`{"url":"https://a.example/mcp"}`), manifest(`{"url":"https://b.example/mcp"}`), false},
		{"a number", manifest(`{"rps":5e-7}`), manifest(`{"rps":6e-7}`), false},
		{"the last digit of a large integer", manifest(`{"n":12345678901234567890}`),
			manifest(`{"n":12345678901234567891}`), false},
		{"a number and the same digits as a string", manifest(`{"n":1}`), manifest(`{"n":"1"}`), false},
		{"the order of an array", manifest(`{"args":["a","b"]}`), manifest(`{"args":["b","a"]}`), false},
		{"a key renamed", manifest(`{"a":1}`), manifest(`{"b":1}`), false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fa, err := FingerprintApp(tc.a.Manifest)
			if err != nil {
				t.Fatalf("fingerprint %s: %v", tc.a.Manifest, err)
			}
			fb, err := FingerprintApp(tc.b.Manifest)
			if err != nil {
				t.Fatalf("fingerprint %s: %v", tc.b.Manifest, err)
			}
			if fa == "" || fb == "" {
				t.Fatalf("a stored manifest fingerprints empty: %q and %q", fa, fb)
			}
			if (fa == fb) != tc.agree {
				t.Errorf("fingerprints agree = %v, want %v:\n%s\n%s", fa == fb, tc.agree, tc.a.Manifest, tc.b.Manifest)
			}
		})
	}
}

// TestFingerprintAppRefusesWhatItCannotCompare pins the failures: a manifest
// that is not one JSON value, and a number whose exponent no stored manifest
// can hold.
func TestFingerprintAppRefusesWhatItCannotCompare(t *testing.T) {
	for _, manifest := range []string{`{"a":1} {"b":2}`, `{"a":`, `{"n":1e99999999}`} {
		if fp, err := FingerprintApp(manifest); err == nil {
			t.Errorf("FingerprintApp(%s) = %q, want an error", manifest, fp)
		}
	}
}

// TestFingerprintRolePairs pins what the Role fingerprint reads: name,
// description, wire kind, owner name, access row and implied names, never
// the row's id, times or owner id, nor the order of its lists.
func TestFingerprintRolePairs(t *testing.T) {
	base := RoleConfig{
		Role: Role{ID: "r1", Name: "demo-readers", Description: "Reads demo", Kind: RoleKindApplication,
			Plane: RolePlaneAccess, OwnerAppID: "a1", CreatedAt: time.Unix(1, 0), UpdatedAt: time.Unix(2, 0)},
		Owner: "demo", Server: "demo", Tools: []string{"list_*", "get_*"}, Implies: []string{"b", "a"},
	}
	with := func(edit func(*RoleConfig)) RoleConfig {
		c := base
		c.Tools = append([]string(nil), base.Tools...)
		c.Implies = append([]string(nil), base.Implies...)
		edit(&c)
		return c
	}
	tests := []struct {
		name  string
		b     RoleConfig
		agree bool
	}{
		{"the row id changed", with(func(c *RoleConfig) { c.Role.ID = "r2" }), true},
		{"updated_at changed", with(func(c *RoleConfig) { c.Role.UpdatedAt = time.Unix(3, 0) }), true},
		{"the owner id changed under the same owner name", with(func(c *RoleConfig) { c.Role.OwnerAppID = "a2" }), true},
		{"tools in another order", with(func(c *RoleConfig) { c.Tools = []string{"get_*", "list_*"} }), true},
		{"implied names in another order", with(func(c *RoleConfig) { c.Implies = []string{"a", "b"} }), true},
		{"a description", with(func(c *RoleConfig) { c.Role.Description = "Reads demo." }), false},
		{"a tool", with(func(c *RoleConfig) { c.Tools = []string{"list_*"} }), false},
		{"an edge", with(func(c *RoleConfig) { c.Implies = []string{"a"} }), false},
		{"the kind", with(func(c *RoleConfig) { c.Role.Kind = RoleKindBusiness }), false},
		{"the plane", with(func(c *RoleConfig) { c.Role.Plane = RolePlaneControl }), false},
		{"the owner", with(func(c *RoleConfig) { c.Owner = "" }), false},
		{"the access row's server", with(func(c *RoleConfig) { c.Server = "other" }), false},
		{"the name", with(func(c *RoleConfig) { c.Role.Name = "demo-writers" }), false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fa, fb := FingerprintRole(base), FingerprintRole(tc.b)
			if (fa == fb) != tc.agree {
				t.Errorf("fingerprints agree = %v, want %v", fa == fb, tc.agree)
			}
		})
	}
	control := func(kind string) RoleConfig {
		return RoleConfig{Role: Role{Name: "auditor", Kind: kind, Plane: RolePlaneControl}}
	}
	if FingerprintRole(control(RoleKindBusiness)) != FingerprintRole(control(RoleKindApplication)) {
		t.Error("a control-plane role fingerprints its kind column, want the wire kind straza whatever the column holds")
	}
	if FingerprintRole(RoleConfig{Role: Role{Name: "x"}}) != FingerprintRole(RoleConfig{Role: Role{Name: "x"}, Tools: []string{}}) {
		t.Error("an absent tool list and an empty one fingerprint differently")
	}
}

// TestFingerprintPolicySetPairs pins what the PolicySet fingerprint reads:
// the name, on or off, and every byte of the stored text.
func TestFingerprintPolicySetPairs(t *testing.T) {
	base := PolicySet{ID: "p1", Name: "guard", Priority: 10, YAMLSource: "kind: PolicySet\n", CompiledHash: "h1",
		Status: "active", CreatedAt: time.Unix(1, 0), UpdatedAt: time.Unix(2, 0)}
	with := func(edit func(*PolicySet)) PolicySet {
		p := base
		edit(&p)
		return p
	}
	tests := []struct {
		name  string
		a, b  PolicySet
		agree bool
	}{
		{"the row id changed", base, with(func(p *PolicySet) { p.ID = "p2" }), true},
		{"the compiled hash changed", base, with(func(p *PolicySet) { p.CompiledHash = "h2" }), true},
		{"the priority column changed", base, with(func(p *PolicySet) { p.Priority = 20 }), true},
		{"updated_at changed", base, with(func(p *PolicySet) { p.UpdatedAt = time.Unix(3, 0) }), true},
		{"off as draft and off as empty", with(func(p *PolicySet) { p.Status = "draft" }),
			with(func(p *PolicySet) { p.Status = "" }), true},
		{"on and off", base, with(func(p *PolicySet) { p.Status = "draft" }), false},
		{"one byte of text", base, with(func(p *PolicySet) { p.YAMLSource = "kind: PolicySet \n" }), false},
		{"the name", base, with(func(p *PolicySet) { p.Name = "guard2" }), false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fa, fb := FingerprintPolicySet(tc.a), FingerprintPolicySet(tc.b)
			if (fa == fb) != tc.agree {
				t.Errorf("fingerprints agree = %v, want %v", fa == fb, tc.agree)
			}
		})
	}
}

// TestFingerprintOfAnAbsentObjectIsEmpty pins that an object live state
// does not hold fingerprints as "", the base of a draft item that creates
// it.
func TestFingerprintOfAnAbsentObjectIsEmpty(t *testing.T) {
	if fp, err := FingerprintApp(""); err != nil || fp != "" {
		t.Errorf("FingerprintApp(\"\") = %q, %v; want \"\"", fp, err)
	}
	if fp := FingerprintRole(RoleConfig{}); fp != "" {
		t.Errorf("FingerprintRole(zero) = %q, want \"\"", fp)
	}
	if fp := FingerprintPolicySet(PolicySet{}); fp != "" {
		t.Errorf("FingerprintPolicySet(zero) = %q, want \"\"", fp)
	}
}

// TestFingerprintsAgreeOnBothDrivers stores a server, two roles with an
// owner, an access row and an edge, and a set, reads them back and
// fingerprints what it read, as the server's World does. Postgres renders
// the manifest from JSONB in its own key order, spacing and number form,
// and every fingerprint must still equal the one of the rows as written.
func TestFingerprintsAgreeOnBothDrivers(t *testing.T) {
	const manifest = `{"metadata":{"name":"demo","version":"1.0.0"},"server":{"big":1e+21,"title":"é <demo> & co"},` +
		`"straza":{"limits":{"rps":5e-7,"timeoutSeconds":30},"runtime":{"kind":"remote","url":"https://demo.example/mcp?a=1&b=2"}}}`
	wantApp, err := FingerprintApp(manifest)
	if err != nil {
		t.Fatal(err)
	}
	readers := RoleConfig{Role: Role{Name: "demo-readers", Description: "Reads <demo> & more", Kind: RoleKindApplication,
		Plane: RolePlaneAccess}, Owner: "demo", Server: "demo", Tools: []string{"get_*", "list_*"}}
	developer := RoleConfig{Role: Role{Name: "developer", Kind: RoleKindBusiness, Plane: RolePlaneAccess},
		Implies: []string{"demo-readers"}}
	set := PolicySet{Name: "guard", Priority: 10, YAMLSource: "kind: PolicySet\nmetadata:\n  name: guard\n", Status: "active"}

	forEachStore(t, func(t *testing.T, s Store) {
		ctx := context.Background()
		app, err := s.Apps().Create(ctx, App{Name: "demo", RuntimeKind: "remote", Manifest: manifest})
		if err != nil {
			t.Fatalf("create the server: %v", err)
		}
		readRole, _, err := s.Roles().CreateOwned(ctx, Role{Name: readers.Role.Name, Description: readers.Role.Description,
			OwnerAppID: app.ID}, `["list_*","get_*"]`)
		if err != nil {
			t.Fatalf("create the owned role: %v", err)
		}
		dev, err := s.Roles().Create(ctx, Role{Name: developer.Role.Name})
		if err != nil {
			t.Fatalf("create the business role: %v", err)
		}
		if err := s.Roles().AddImplication(ctx, dev.ID, readRole.ID); err != nil {
			t.Fatalf("add the edge: %v", err)
		}
		if _, err := s.Policies().Create(ctx, set); err != nil {
			t.Fatalf("create the set: %v", err)
		}

		got, err := s.Apps().GetByName(ctx, "demo")
		if err != nil {
			t.Fatal(err)
		}
		postgres := s.(*sqlStore).d == dialectPostgres
		if rendered := got.Manifest != manifest; rendered != postgres {
			t.Fatalf("the manifest read back rendered anew = %v on this driver, and the test needs Postgres to re-render it: %s", rendered, got.Manifest)
		}
		if fp, err := FingerprintApp(got.Manifest); err != nil || fp != wantApp {
			t.Errorf("the App fingerprint of the row read back = %q, %v; want %q", fp, err, wantApp)
		}
		for _, want := range []RoleConfig{readers, developer} {
			if fp := FingerprintRole(readRoleConfig(t, s, want.Role.Name)); fp != FingerprintRole(want) {
				t.Errorf("the Role fingerprint of %s read back differs from the rows as written", want.Role.Name)
			}
		}
		gotSet, err := s.Policies().GetByName(ctx, "guard")
		if err != nil || FingerprintPolicySet(gotSet) != FingerprintPolicySet(set) {
			t.Errorf("the PolicySet fingerprint of the row read back differs from the row as written (%v)", err)
		}
	})
}

// readRoleConfig reads one role's config from the store the way the
// server's World reads it: the row, the owner's name, its access row with
// the server's name and the tools decoded, and its implied names.
func readRoleConfig(t *testing.T, s Store, name string) RoleConfig {
	t.Helper()
	ctx := context.Background()
	role, err := s.Roles().GetByName(ctx, name)
	if err != nil {
		t.Fatalf("read role %s: %v", name, err)
	}
	c := RoleConfig{Role: role}
	appName := func(id string) string {
		a, err := s.Apps().GetByID(ctx, id)
		if err != nil {
			t.Fatalf("read server %s: %v", id, err)
		}
		return a.Name
	}
	if role.OwnerAppID != "" {
		c.Owner = appName(role.OwnerAppID)
	}
	bindings, err := s.ToolBindings().ListByRole(ctx, role.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, b := range bindings {
		c.Server = appName(b.AppID)
		if err := json.Unmarshal([]byte(b.ToolMatcher), &c.Tools); err != nil {
			t.Fatalf("decode the tools %s: %v", b.ToolMatcher, err)
		}
	}
	edges, err := s.Roles().ListImplications(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range edges {
		if e.RoleID == role.ID {
			implied, err := s.Roles().GetByID(ctx, e.ImpliesRoleID)
			if err != nil {
				t.Fatal(err)
			}
			c.Implies = append(c.Implies, implied.Name)
		}
	}
	return c
}
