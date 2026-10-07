package config

import "errors"

// OIDC section: the external identity provider. Type, validation.
// The env faces stay in config.go's applyEnv.

// OIDC configures the external identity provider. Empty Issuer means
// no external IdP; the standalone profile then relies on the built-in
// issuer. Enterprise deployments must set it.
type OIDC struct {
	// Issuer is the IdP's OIDC discovery base URL.
	Issuer string `yaml:"issuer"`
	// DiscoveryURL is where strazad fetches the IdP's discovery document when
	// it cannot reach the issuer's own address, as on a private network. The
	// document must still name Issuer, and tokens must still carry it. Empty
	// means Issuer plus /.well-known/openid-configuration.
	DiscoveryURL string `yaml:"discoveryUrl"`
	// ClientID is the audience expected on ID tokens.
	ClientID string `yaml:"clientId"`
	// JITProvision creates unknown-but-verified users on first login.
	// Enterprise default is false: SCIM is the source of truth.
	JITProvision bool `yaml:"jitProvision"`
	// BootstrapAdmin breaks the enterprise first-admin circle:
	// the first IdP-verified login whose preferred_username equals this value
	// is provisioned (even with JIT off) and granted straza-admin, but only
	// while NO straza-admin assignment exists, so the knob goes inert the
	// moment real role management is in place. Remove it after bootstrap.
	BootstrapAdmin string `yaml:"bootstrapAdmin"`
}

// validate holds the oidc section's checks.
func (o OIDC) validate() error {
	// oidc.issuer must match the IdP byte-for-byte: paths (Keycloak realms)
	// and a trailing slash (Auth0) pass verbatim, never normalized, but
	// query/fragment are forbidden by OpenID Connect Discovery, and the rest
	// of the family floor applies (http(s), host, no credentials).
	if o.Issuer != "" {
		if err := checkEndpointURL("oidc.issuer", o.Issuer, false, ""); err != nil {
			return err
		}
	}
	if o.DiscoveryURL != "" {
		if o.Issuer == "" {
			return errors.New("oidc.discoveryUrl is set but oidc.issuer is empty. Set oidc.issuer to the issuer your identity provider puts in its tokens, or remove oidc.discoveryUrl")
		}
		// A query passes: some providers select a policy there.
		if err := checkEndpointURL("oidc.discoveryUrl", o.DiscoveryURL, true, ""); err != nil {
			return err
		}
	}
	return nil
}

// applyEnvOIDC binds the oidc section's env faces; applyEnv
// (config.go) sequences the binders, and each face writes a field no other
// face writes.
func applyEnvOIDC(cfg *Config, getenv func(string) string) {
	set := envSet(getenv)
	set("STRAZA_OIDC_ISSUER", func(v string) { cfg.OIDC.Issuer = v })
	set("STRAZA_OIDC_DISCOVERY_URL", func(v string) { cfg.OIDC.DiscoveryURL = v })
	set("STRAZA_OIDC_CLIENT_ID", func(v string) { cfg.OIDC.ClientID = v })
	set("STRAZA_OIDC_JIT", func(v string) { cfg.OIDC.JITProvision = v == "true" })
	set("STRAZA_OIDC_BOOTSTRAP_ADMIN", func(v string) { cfg.OIDC.BootstrapAdmin = v })
}
