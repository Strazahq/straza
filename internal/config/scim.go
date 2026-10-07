package config

// SCIM, Admin and Secrets sections: inbound provisioning, the human
// admin plane and the credential plane. Types, validation.
// The env face stays in config.go's applyEnv.

import (
	"fmt"
)

// SCIM configures inbound identity provisioning. The
// exposeControlPlaneRoles knob is retired: every role renders on the
// wire-group surface, the IdM masters membership everywhere; a leftover key
// boots with one ignored-with-notice line (removedKeyNotices).
type SCIM struct {
	// Tombstones (removed by the unified role model, spec/scim-profile
	// revision 12): presence FAILS validation loudly so an operator
	// upgrading with the old config learns immediately instead of
	// discovering silently-changed provisioning semantics. Groups no
	// longer exist; roles render as wire-groups and membership is
	// assignment.
	GroupRolePrefix string            `yaml:"groupRolePrefix"`
	GroupRoleMap    map[string]string `yaml:"groupRoleMap"`
	AutoCreateRoles *bool             `yaml:"autoCreateRoles"`
}

// Admin configures the human admin plane (delegated administration).
// RoleAreas maps ordinary role names (delivered from the IdM like any other
// membership) to per-area grants in the admin API token vocabulary ("area:verb",
// internal/tokenscopes). Meaning is authored straza-side by
// design: the IdM decides WHO holds a role, this map decides WHAT it may
// administer; an unmapped role grants nothing. straza-admin stays the one
// root spelling: it may not appear here and "full" is not grantable.
// Validated at server construction; a bad entry refuses startup.
type Admin struct {
	RoleAreas map[string][]string `yaml:"roleAreas"`
	// SecondPerson makes a change that widens access need a publisher
	// other than its authors: the drafts route refuses a publisher who
	// wrote a revision of the draft that did not only take live values,
	// minted a token that wrote one, or sponsors an agent that wrote one,
	// and a direct admin write that widens access is refused so that it
	// goes through a draft. It is set in the file only, and false by
	// default in both profiles. The publish route and the direct routes
	// enforce it.
	SecondPerson bool `yaml:"secondPerson"`
}

// Secrets configures the credential plane.
type Secrets struct {
	// KEKFile is the 32-byte key-encryption key for the builtin secretbox
	// provider; generated on first boot when absent. Default
	// "<dataDir>/secret.key".
	KEKFile string `yaml:"kekFile"`
}

// validate holds the scim section's checks.
func (s SCIM) validate() error {
	// Unified role model tombstones (spec/scim-profile revision 12): fail
	// loud, never silent-ignore, so an upgrade cannot quietly change what
	// the IdM lane may do.
	if s.GroupRolePrefix != "" || s.GroupRoleMap != nil || s.AutoCreateRoles != nil {
		return fmt.Errorf("scim.groupRolePrefix, scim.groupRoleMap and scim.autoCreateRoles were removed by the unified role model (spec/scim-profile revision 12): groups no longer exist; roles render as wire-groups and membership is assignment. Delete the keys")
	}
	return nil
}

// applyEnvSecrets binds the secrets section's env face; applyEnv
// (config.go) sequences the binders, and each face writes a field no other
// face writes.
func applyEnvSecrets(cfg *Config, getenv func(string) string) {
	set := envSet(getenv)
	set("STRAZA_SECRETS_KEK_FILE", func(v string) { cfg.Secrets.KEKFile = v })
}
