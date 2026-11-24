package config

import (
	"slices"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"
)

const (
	entraTestTenant = "11111111-2222-4333-8444-555555555555"
	entraTestClient = "AAAAAAAA-BBBB-4CCC-8DDD-EEEEEEEEEEEE"
	entraTestGroup  = "99999999-8888-4777-8666-555555555555"
)

// entraDoc returns a config document whose identity.entra section holds the
// given lines, each indented one level under entra.
func entraDoc(lines ...string) string {
	var b strings.Builder
	b.WriteString("identity:\n  entra:\n")
	for _, l := range lines {
		b.WriteString("    " + l + "\n")
	}
	return b.String()
}

// entraValid is the minimal section that turns Entra on.
var entraValid = []string{"tenant: " + entraTestTenant, "clientId: " + entraTestClient}

func loadDoc(t *testing.T, doc string) (Config, error) {
	t.Helper()
	return Loader{FilePath: writeFile(t, doc), ExplicitFile: true, Getenv: noEnv}.Load()
}

// TestEntraOff pins the off state: a config that sets no key of
// identity.entra loads, and the section reports itself off.
func TestEntraOff(t *testing.T) {
	cases := []struct{ name, doc string }{
		{"no identity key", "log:\n  level: info\n"},
		{"an empty identity", "identity: {}\n"},
		{"an empty entra", "identity:\n  entra: {}\n"},
		{"a null entra", "identity:\n  entra:\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg, err := loadDoc(t, tc.doc)
			if err != nil {
				t.Fatalf("Load: %v", err)
			}
			if cfg.Identity.Entra.Enabled() {
				t.Fatalf("Enabled = true for %q, want off", tc.doc)
			}
		})
	}
	t.Run("no file", func(t *testing.T) {
		cfg, err := Loader{Getenv: noEnv}.Load()
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		if cfg.Identity.Entra.Enabled() {
			t.Fatal("Enabled = true with no config file, want off")
		}
	})
	t.Run("a struct that never went through the loader", func(t *testing.T) {
		if (Entra{}).Enabled() {
			t.Fatal("Enabled = true for the zero Entra, want off")
		}
		if err := (Entra{}).validate(); err != nil {
			t.Fatalf("validate of the zero Entra: %v", err)
		}
	})
}

// TestEntraDefaults pins every default of a minimal section in both
// profiles: the tenant and the client ID turn it on, and every other key
// takes its default.
func TestEntraDefaults(t *testing.T) {
	for _, profile := range []string{ProfileStandalone, ProfileEnterprise} {
		t.Run(profile, func(t *testing.T) {
			doc := "profile: " + profile + "\nstore:\n  dsn: postgres://straza@localhost/straza\n" + entraDoc(entraValid...)
			cfg, err := loadDoc(t, doc)
			if err != nil {
				t.Fatalf("Load: %v", err)
			}
			e := cfg.Identity.Entra
			if !e.Enabled() {
				t.Fatal("Enabled = false with tenant and clientId set")
			}
			if e.Tenant != entraTestTenant || e.ClientID != entraTestClient {
				t.Errorf("tenant, clientId = %q, %q, want them as written", e.Tenant, e.ClientID)
			}
			if len(e.Read) != 0 {
				t.Errorf("read = %v, want empty", e.Read)
			}
			if !e.RoleGroups {
				t.Error("roleGroups = false, want the default true")
			}
			if e.GroupNamePrefix != "Straza: " {
				t.Errorf("groupNamePrefix = %q, want %q", e.GroupNamePrefix, "Straza: ")
			}
			if e.DeltaInterval != time.Minute {
				t.Errorf("deltaInterval = %s, want 1m", e.DeltaInterval)
			}
			if e.ReconcileInterval != time.Hour {
				t.Errorf("reconcileInterval = %s, want 1h", e.ReconcileInterval)
			}
			if e.RemovalGuard != (EntraRemovalGuard{Percent: 50, MinMembers: 5}) {
				t.Errorf("removalGuard = %+v, want percent 50 and minMembers 5", e.RemovalGuard)
			}
			if len(e.ScopeGroups) != 0 {
				t.Errorf("scopeGroups = %v, want empty", e.ScopeGroups)
			}
		})
	}
}

// TestEntraAccepted pins the edges a valid section may take, among them an
// explicitly empty prefix, which must not fall back to the default.
func TestEntraAccepted(t *testing.T) {
	cases := []struct {
		name  string
		lines []string
		check func(Entra) bool
	}{
		{"an empty prefix stays empty", []string{`groupNamePrefix: ""`}, func(e Entra) bool { return e.GroupNamePrefix == "" }},
		{"a prefix of 64 characters in 128 bytes", []string{`groupNamePrefix: "` + strings.Repeat("é", 64) + `"`},
			func(e Entra) bool { return e.GroupNamePrefix == strings.Repeat("é", 64) }},
		{"every kind", []string{"read: [people, agentUsers, agentIdentities]"},
			func(e Entra) bool { return slices.Equal(e.Read, []string{"people", "agentUsers", "agentIdentities"}) }},
		{"an empty read", []string{"read: []"}, func(e Entra) bool { return len(e.Read) == 0 }},
		{"roleGroups true", []string{"roleGroups: true"}, func(e Entra) bool { return e.RoleGroups }},
		{"the floor of both intervals", []string{"deltaInterval: 10s", "reconcileInterval: 10s"},
			func(e Entra) bool { return e.DeltaInterval == 10*time.Second && e.ReconcileInterval == 10*time.Second }},
		{"the lowest guard", []string{"removalGuard:", "  percent: 1", "  minMembers: 1"},
			func(e Entra) bool { return e.RemovalGuard == EntraRemovalGuard{Percent: 1, MinMembers: 1} }},
		{"the highest percent", []string{"removalGuard:", "  percent: 100"},
			func(e Entra) bool { return e.RemovalGuard == EntraRemovalGuard{Percent: 100, MinMembers: 5} }},
		{"an empty scopeGroups", []string{"scopeGroups: []"}, func(e Entra) bool { return len(e.ScopeGroups) == 0 }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg, err := loadDoc(t, entraDoc(append(slices.Clone(entraValid), tc.lines...)...))
			if err != nil {
				t.Fatalf("Load: %v", err)
			}
			if !tc.check(cfg.Identity.Entra) {
				t.Fatalf("section = %+v, does not hold %v", cfg.Identity.Entra, tc.lines)
			}
		})
	}
}

// TestEntraRefusals pins each refusal and its exact sentence. Every sentence
// names the full key and the bad value, says why it is refused and what to
// write instead.
func TestEntraRefusals(t *testing.T) {
	const (
		tenantEmpty = "identity.entra.tenant is empty while the identity.entra section is set, and Straza signs in to Microsoft Graph in the tenant whose ID this key holds. Write the directory (tenant) ID from the overview page of the Entra admin centre, or delete the identity.entra section to leave Entra off"
		pullsNot    = "this build of Straza writes a group in Entra for each role and cannot yet pull accounts from groups you name"
	)
	long := strings.Repeat("x", 65)
	cases := []struct {
		name  string
		lines []string
		want  string
	}{
		{"read alone", []string{"read: [people]"}, tenantEmpty},
		{"roleGroups alone at its default", []string{"roleGroups: true"}, tenantEmpty},
		{"an empty prefix alone", []string{`groupNamePrefix: ""`}, tenantEmpty},
		{"clientId without tenant", []string{"clientId: " + entraTestClient}, tenantEmpty},
		{"a tenant domain", []string{"tenant: contoso.onmicrosoft.com", "clientId: " + entraTestClient},
			`identity.entra.tenant "contoso.onmicrosoft.com" is not a GUID, and Straza signs in to Microsoft Graph in the tenant whose ID this key holds. Write the directory (tenant) ID from the overview page of the Entra admin centre`},
		{"tenant without clientId", []string{"tenant: " + entraTestTenant},
			"identity.entra.clientId is empty while the identity.entra section is set, and Straza signs in to Microsoft Graph as the service app whose ID this key holds. Write the application (client) ID of the service app from the Entra admin centre, or delete the identity.entra section to leave Entra off"},
		{"a client name", []string{"tenant: " + entraTestTenant, "clientId: straza-sync"},
			`identity.entra.clientId "straza-sync" is not a GUID, and Straza signs in to Microsoft Graph as the service app whose ID this key holds. Write the application (client) ID of the service app from the Entra admin centre`},
		{"an unknown kind", append(slices.Clone(entraValid), "read: [people, users]"),
			`identity.entra.read lists "users", which is not a kind of account Straza reads from Entra. Write people, agentUsers or agentIdentities`},
		{"a kind in another case", append(slices.Clone(entraValid), "read: [AgentUsers]"),
			`identity.entra.read lists "AgentUsers", which is not a kind of account Straza reads from Entra. Write people, agentUsers or agentIdentities`},
		{"a repeated kind", append(slices.Clone(entraValid), "read: [people, agentUsers, people]"),
			`identity.entra.read lists "people" twice, and a kind of account either belongs to Entra or does not. List each kind once`},
		{"roleGroups false", append(slices.Clone(entraValid), "roleGroups: false"),
			"identity.entra.roleGroups is false, and " + pullsNot + ". Set roleGroups to true, or delete the key to take the default"},
		{"a prefix of 65 characters", append(slices.Clone(entraValid), "groupNamePrefix: "+long),
			`identity.entra.groupNamePrefix "` + long + `" is 65 characters long, and the prefix starts the name of every role group, beside the role's own name. Write a prefix of at most 64 characters`},
		{"a prefix with a tab", append(slices.Clone(entraValid), `groupNamePrefix: "Straza:\t"`),
			`identity.entra.groupNamePrefix "Straza:\t" holds a control character, which Entra would carry into the name of every role group. Write the prefix with printable characters only`},
		{"a delta below the floor", append(slices.Clone(entraValid), "deltaInterval: 5s"),
			"identity.entra.deltaInterval is 5s, below the floor of 10s, because each pass sends requests to Microsoft Graph and Graph slows down an app that asks too often. Write 10s or more, such as the default 1m"},
		{"a negative delta", append(slices.Clone(entraValid), "deltaInterval: -1m"),
			"identity.entra.deltaInterval is -1m0s, below the floor of 10s, because each pass sends requests to Microsoft Graph and Graph slows down an app that asks too often. Write 10s or more, such as the default 1m"},
		{"a reconcile below the default delta", append(slices.Clone(entraValid), "reconcileInterval: 30s"),
			"identity.entra.reconcileInterval is 30s, shorter than identity.entra.deltaInterval 1m0s, and the full reconcile is the slower pass that repairs what the delta passes missed. Write 1m0s or more, such as the default 1h"},
		{"a reconcile below a raised delta", append(slices.Clone(entraValid), "deltaInterval: 5m", "reconcileInterval: 2m"),
			"identity.entra.reconcileInterval is 2m0s, shorter than identity.entra.deltaInterval 5m0s, and the full reconcile is the slower pass that repairs what the delta passes missed. Write 5m0s or more, such as the default 1h"},
		{"a percent of 0", append(slices.Clone(entraValid), "removalGuard:", "  percent: 0"),
			"identity.entra.removalGuard.percent is 0, and it is the share of a role's Entra holders above which the guard holds a pass, so it must be from 1 to 100. Write a percentage from 1 to 100, such as the default 50"},
		{"a percent of 101", append(slices.Clone(entraValid), "removalGuard:", "  percent: 101"),
			"identity.entra.removalGuard.percent is 101, and it is the share of a role's Entra holders above which the guard holds a pass, so it must be from 1 to 100. Write a percentage from 1 to 100, such as the default 50"},
		{"minMembers of 0", append(slices.Clone(entraValid), "removalGuard:", "  minMembers: 0"),
			"identity.entra.removalGuard.minMembers is 0, and it is the fewest removals that can make the guard hold a pass, so it must be at least 1. Write 1 or more, such as the default 5"},
		{"a scope group by name", append(slices.Clone(entraValid), "scopeGroups: [finance]"),
			`identity.entra.scopeGroups lists "finance", which is not a GUID, and Straza names an Entra group by its object ID. Copy the object ID of the group from the Entra admin centre`},
		{"a scope group by ID", append(slices.Clone(entraValid), "scopeGroups: ["+entraTestGroup+"]"),
			"identity.entra.scopeGroups lists " + entraTestGroup + ", and " + pullsNot + ". Leave scopeGroups empty, and add the accounts to the groups Straza writes for the roles"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := loadDoc(t, entraDoc(tc.lines...))
			if err == nil {
				t.Fatalf("Load accepted %v, want the refusal %q", tc.lines, tc.want)
			}
			if err.Error() != tc.want {
				t.Fatalf("err = %q\nwant  %q", err, tc.want)
			}
		})
	}
}

// TestEntraRoundTrip loads a section that sets every key away from its
// default through the real loader, writes the loaded section back out as
// YAML and loads that again, and wants the same values both times.
func TestEntraRoundTrip(t *testing.T) {
	doc := entraDoc(
		"tenant: "+entraTestTenant,
		"clientId: "+entraTestClient,
		"read: [agentIdentities, people]",
		"roleGroups: true",
		`groupNamePrefix: "Agents: "`,
		"deltaInterval: 30s",
		"reconcileInterval: 2h",
		"removalGuard:",
		"  percent: 25",
		"  minMembers: 3",
		"scopeGroups: []",
	)
	want := Entra{
		Tenant:            entraTestTenant,
		ClientID:          entraTestClient,
		Read:              []string{"agentIdentities", "people"},
		RoleGroups:        true,
		GroupNamePrefix:   "Agents: ",
		DeltaInterval:     30 * time.Second,
		ReconcileInterval: 2 * time.Hour,
		RemovalGuard:      EntraRemovalGuard{Percent: 25, MinMembers: 3},
	}
	same := func(got Entra) bool {
		return got.Tenant == want.Tenant && got.ClientID == want.ClientID && slices.Equal(got.Read, want.Read) &&
			got.RoleGroups == want.RoleGroups && got.GroupNamePrefix == want.GroupNamePrefix &&
			got.DeltaInterval == want.DeltaInterval && got.ReconcileInterval == want.ReconcileInterval &&
			got.RemovalGuard == want.RemovalGuard && len(got.ScopeGroups) == 0 && got.Enabled()
	}
	first, err := loadDoc(t, doc)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !same(first.Identity.Entra) {
		t.Fatalf("first load = %+v, want %+v", first.Identity.Entra, want)
	}
	out, err := yaml.Marshal(struct {
		Identity Identity `yaml:"identity"`
	}{first.Identity})
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	second, err := loadDoc(t, string(out))
	if err != nil {
		t.Fatalf("Load of the written section:\n%s\n%v", out, err)
	}
	if !same(second.Identity.Entra) {
		t.Fatalf("second load = %+v from\n%s\nwant %+v", second.Identity.Entra, out, want)
	}
}
