package config

// Identity section: Microsoft Graph as a source of role holders. Types,
// defaults, validation. Every key is set in the file only, so the section
// has no env binder.

import (
	"fmt"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"gopkg.in/yaml.v3"
)

// The kinds of account identity.entra.read may list.
const (
	EntraReadPeople          = "people"
	EntraReadAgentUsers      = "agentUsers"
	EntraReadAgentIdentities = "agentIdentities"
)

// entraNoScopeGroups is the reason shared by the two settings this build
// refuses, roleGroups false and a non-empty scopeGroups.
const entraNoScopeGroups = "this build of Straza writes a group in Entra for each role and cannot yet pull accounts from groups you name"

// Identity configures the directories strazad reaches over their own API
// rather than over SCIM.
type Identity struct {
	// Entra is the Microsoft Entra tenant strazad reaches over Microsoft Graph.
	Entra Entra `yaml:"entra"`
}

// Entra configures Microsoft Graph as a source of role holders: the tenant
// and the service app strazad signs in as, the kinds of account Entra owns,
// the security group strazad writes for each role, and the pace and the
// removal guard of the sync that reads the groups' members. The section is
// off until a key of it is set, which Enabled reports. Validate refuses an
// enabled section that this build cannot act on, because a setting that is
// accepted and does nothing would mislead an operator.
type Entra struct {
	// Tenant is the directory (tenant) ID, a GUID.
	Tenant string `yaml:"tenant"`
	// ClientID is the application (client) ID of the service app, a GUID.
	ClientID string `yaml:"clientId"`
	// Read lists the kinds of account Entra owns, each EntraRead constant
	// at most once. Empty applies no member of a role group.
	Read []string `yaml:"read"`
	// RoleGroups makes strazad write one security group in Entra for each
	// role. Default true, the only value this build accepts.
	RoleGroups bool `yaml:"roleGroups"`
	// GroupNamePrefix starts the display name of every role group. Default
	// "Straza: ", and an explicitly empty value stays empty.
	GroupNamePrefix string `yaml:"groupNamePrefix"`
	// DeltaInterval paces the delta pass (default 1m, at least 10s).
	DeltaInterval time.Duration `yaml:"deltaInterval"`
	// ReconcileInterval paces the full reconcile (default 1h, at least
	// DeltaInterval).
	ReconcileInterval time.Duration `yaml:"reconcileInterval"`
	// RemovalGuard holds a pass that would remove many of a role's Entra
	// holders at once.
	RemovalGuard EntraRemovalGuard `yaml:"removalGuard"`
	// ScopeGroups is reserved for the object IDs of Entra groups whose
	// members strazad pulls when it writes no role groups. This build
	// refuses any entry.
	ScopeGroups []string `yaml:"scopeGroups"`

	// inFile records that the config file set a key of the section, which
	// turns the section on even when that key holds its default.
	inFile bool
}

// EntraRemovalGuard holds a sync pass that would remove more than Percent
// percent of a role's Entra holders and at least MinMembers of them.
type EntraRemovalGuard struct {
	Percent    int `yaml:"percent"`
	MinMembers int `yaml:"minMembers"`
}

// defaultEntra returns the identity.entra defaults shared by both profiles.
// The section stays off with them, so turning it on takes a tenant and a
// client ID.
func defaultEntra() Entra {
	return Entra{
		RoleGroups:        true,
		GroupNamePrefix:   "Straza: ",
		DeltaInterval:     time.Minute,
		ReconcileInterval: time.Hour,
		RemovalGuard:      EntraRemovalGuard{Percent: 50, MinMembers: 5},
	}
}

// Enabled reports whether the section is on: the tenant or the client ID is
// set, or the config file set any other key of it. After Load an enabled
// section names a tenant and a client ID that are GUIDs.
func (e Entra) Enabled() bool {
	return e.inFile || e.Tenant != "" || e.ClientID != ""
}

// entraSection is Entra without its UnmarshalYAML method, so decoding into
// it fills the fields the default way.
type entraSection Entra

// UnmarshalYAML decodes the section over the defaults already in place and
// records whether the mapping holds a key, so a section that sets only keys
// at their defaults is still on and is refused without a tenant.
func (e *Entra) UnmarshalYAML(n *yaml.Node) error {
	if err := n.Decode((*entraSection)(e)); err != nil {
		return err
	}
	e.inFile = len(n.Content) > 0
	return nil
}

// validate holds the identity.entra checks in a fixed order, the first
// error winning. An off section has nothing to check.
func (e Entra) validate() error {
	if !e.Enabled() {
		return nil
	}
	if err := entraID("identity.entra.tenant", e.Tenant, "in the tenant whose ID this key holds",
		"the directory (tenant) ID from the overview page of the Entra admin centre"); err != nil {
		return err
	}
	if err := entraID("identity.entra.clientId", e.ClientID, "as the service app whose ID this key holds",
		"the application (client) ID of the service app from the Entra admin centre"); err != nil {
		return err
	}
	if err := e.validateRead(); err != nil {
		return err
	}
	if !e.RoleGroups {
		return fmt.Errorf("identity.entra.roleGroups is false, and %s. Set roleGroups to true, or delete the key to take the default", entraNoScopeGroups)
	}
	if n := utf8.RuneCountInString(e.GroupNamePrefix); n > 64 {
		return fmt.Errorf("identity.entra.groupNamePrefix %q is %d characters long, and the prefix starts the name of every role group, beside the role's own name. Write a prefix of at most 64 characters", e.GroupNamePrefix, n)
	}
	if strings.ContainsFunc(e.GroupNamePrefix, unicode.IsControl) {
		return fmt.Errorf("identity.entra.groupNamePrefix %q holds a control character, which Entra would carry into the name of every role group. Write the prefix with printable characters only", e.GroupNamePrefix)
	}
	if e.DeltaInterval < 10*time.Second {
		return fmt.Errorf("identity.entra.deltaInterval is %s, below the floor of 10s, because each pass sends requests to Microsoft Graph and Graph slows down an app that asks too often. Write 10s or more, such as the default 1m", e.DeltaInterval)
	}
	if e.ReconcileInterval < e.DeltaInterval {
		return fmt.Errorf("identity.entra.reconcileInterval is %s, shorter than identity.entra.deltaInterval %s, and the full reconcile is the slower pass that repairs what the delta passes missed. Write %s or more, such as the default 1h", e.ReconcileInterval, e.DeltaInterval, e.DeltaInterval)
	}
	if p := e.RemovalGuard.Percent; p < 1 || p > 100 {
		return fmt.Errorf("identity.entra.removalGuard.percent is %d, and it is the share of a role's Entra holders above which the guard holds a pass, so it must be from 1 to 100. Write a percentage from 1 to 100, such as the default 50", p)
	}
	if m := e.RemovalGuard.MinMembers; m < 1 {
		return fmt.Errorf("identity.entra.removalGuard.minMembers is %d, and it is the fewest removals that can make the guard hold a pass, so it must be at least 1. Write 1 or more, such as the default 5", m)
	}
	return e.validateScopeGroups()
}

// entraID checks a key that holds an Entra ID: set, and a GUID. why says
// how Straza uses the ID and where says where the admin finds it.
func entraID(key, value, why, where string) error {
	if value == "" {
		return fmt.Errorf("%s is empty while the identity.entra section is set, and Straza signs in to Microsoft Graph %s. Write %s, or delete the identity.entra section to leave Entra off", key, why, where)
	}
	if !guidForm.MatchString(value) {
		return fmt.Errorf("%s %q is not a GUID, and Straza signs in to Microsoft Graph %s. Write %s", key, value, why, where)
	}
	return nil
}

// validateRead refuses a kind outside the three and a kind listed twice.
func (e Entra) validateRead() error {
	seen := map[string]bool{}
	for _, kind := range e.Read {
		switch kind {
		case EntraReadPeople, EntraReadAgentUsers, EntraReadAgentIdentities:
		default:
			return fmt.Errorf("identity.entra.read lists %q, which is not a kind of account Straza reads from Entra. Write %s, %s or %s",
				kind, EntraReadPeople, EntraReadAgentUsers, EntraReadAgentIdentities)
		}
		if seen[kind] {
			return fmt.Errorf("identity.entra.read lists %q twice, and a kind of account either belongs to Entra or does not. List each kind once", kind)
		}
		seen[kind] = true
	}
	return nil
}

// validateScopeGroups checks that every entry is a GUID, then refuses any
// entry, because this build cannot pull accounts from the groups named.
func (e Entra) validateScopeGroups() error {
	for _, id := range e.ScopeGroups {
		if !guidForm.MatchString(id) {
			return fmt.Errorf("identity.entra.scopeGroups lists %q, which is not a GUID, and Straza names an Entra group by its object ID. Copy the object ID of the group from the Entra admin centre", id)
		}
	}
	if len(e.ScopeGroups) > 0 {
		return fmt.Errorf("identity.entra.scopeGroups lists %s, and %s. Leave scopeGroups empty, and add the accounts to the groups Straza writes for the roles",
			strings.Join(e.ScopeGroups, ", "), entraNoScopeGroups)
	}
	return nil
}
