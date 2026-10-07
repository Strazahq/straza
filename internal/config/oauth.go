package config

import (
	"fmt"
	"strings"
	"time"
)

// OAuthConnect configures per-user OAuth connect:
// upstream provider registrations for the connect sign-in and the refresh
// worker that rotates expiring grants.
type OAuthConnect struct {
	// Providers maps a provider name (referenced by app manifests via
	// straza.credential.oauth.provider) to its OAuth client registration.
	// The provider named "github" defaults its endpoints to github.com.
	Providers map[string]OAuthProvider `yaml:"providers"`
	// RefreshInterval paces the refresh worker passes (default 1m).
	RefreshInterval time.Duration `yaml:"refreshInterval"`
	// RefreshWindow rotates grants expiring within this horizon (default 10m).
	RefreshWindow time.Duration `yaml:"refreshWindow"`
}

// OAuthProvider is one upstream OAuth client registration.
type OAuthProvider struct {
	// ClientID identifies the OAuth app registered at the provider.
	ClientID string `yaml:"clientId"`
	// ClientSecret authenticates the token exchange. It never leaves strazad.
	ClientSecret string `yaml:"clientSecret"`
	// ClientSecretFile reads the secret from a file instead (wins when set).
	ClientSecretFile string `yaml:"clientSecretFile"`
	// AuthURL is the authorization endpoint (defaulted for "github").
	AuthURL string `yaml:"authUrl"`
	// TokenURL is the token endpoint (defaulted for "github").
	TokenURL string `yaml:"tokenUrl"`
	// Scopes are the default scopes when an app manifest declares none.
	Scopes []string `yaml:"scopes"`
	// ClientCredentials is the operator's statement that this provider
	// trusts Straza's client assertion keys, so an agent may get a token of
	// its own here (manifest credential.agents: client_credentials). Unset
	// means the provider serves people's sign-ins only.
	ClientCredentials *ClientCredentials `yaml:"clientCredentials"`
}

// ClientCredentials configures the client credentials grant an agent's own
// client runs at a provider, authenticated by an assertion Straza signs
// (RFC 7523 section 2.2). The token endpoint is the provider's TokenURL.
type ClientCredentials struct {
	// AssertionAudience is the aud claim the provider wants in a client
	// assertion. There is no default: Keycloak wants its realm issuer URL
	// and Okta its token endpoint URL.
	AssertionAudience string `yaml:"assertionAudience"`
	// Scopes are asked for with the grant. Empty asks for the client's
	// default scopes at the provider.
	Scopes []string `yaml:"scopes"`
}

// normalizeOAuth fills endpoint defaults for the GitHub reference provider
// so operators only supply the client id/secret.
func normalizeOAuth(o *OAuthConnect) {
	gh, ok := o.Providers["github"]
	if !ok {
		return
	}
	if gh.AuthURL == "" {
		gh.AuthURL = "https://github.com/login/oauth/authorize"
	}
	if gh.TokenURL == "" {
		gh.TokenURL = "https://github.com/login/oauth/access_token"
	}
	o.Providers["github"] = gh
}

func (o OAuthConnect) validate() error {
	if o.RefreshInterval < 0 || o.RefreshWindow < 0 {
		return fmt.Errorf("oauth.refreshInterval and oauth.refreshWindow must be positive durations")
	}
	for name, p := range o.Providers {
		if p.ClientID == "" {
			return fmt.Errorf("oauth.providers.%s: clientId is required", name)
		}
		if p.ClientSecret == "" && p.ClientSecretFile == "" {
			return fmt.Errorf("oauth.providers.%s: clientSecret or clientSecretFile is required", name)
		}
		if p.AuthURL == "" || p.TokenURL == "" {
			return fmt.Errorf("oauth.providers.%s: authUrl and tokenUrl are required (defaulted only for \"github\")", name)
		}
		// Endpoint shape (RFC 6749 §3.1): query strings pass, a #fragment is
		// forbidden, and client credentials never ride the URL; they have
		// clientSecret/clientSecretFile.
		if err := checkEndpointURL("oauth.providers."+name+".authUrl", p.AuthURL, true, ""); err != nil {
			return err
		}
		if err := checkEndpointURL("oauth.providers."+name+".tokenUrl", p.TokenURL, true, ""); err != nil {
			return err
		}
		if err := p.ClientCredentials.validate("oauth.providers." + name + ".clientCredentials"); err != nil {
			return err
		}
	}
	return nil
}

// validate checks the block when it is set. A scope is an RFC 6749 section
// 3.3 scope-token, so it holds no space, quote or backslash.
func (c *ClientCredentials) validate(field string) error {
	if c == nil {
		return nil
	}
	if c.AssertionAudience == "" {
		return fmt.Errorf("%s: assertionAudience is required. Write the audience this provider wants in a client assertion: Keycloak takes its realm issuer URL and Okta its token endpoint URL", field)
	}
	for _, scope := range c.Scopes {
		if scope == "" || strings.ContainsFunc(scope, func(r rune) bool { return r <= ' ' || r > '~' || r == '"' || r == '\\' }) {
			return fmt.Errorf("%s: scope %q holds a character a scope cannot carry. Write one scope per list entry", field, scope)
		}
	}
	return nil
}
