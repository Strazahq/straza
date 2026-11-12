package config

import (
	"strings"
	"testing"
)

// TestOAuthClientCredentialsBlock pins the clientCredentials block of a
// provider: it is optional, it has no default audience because Keycloak wants
// its issuer URL and Okta its token endpoint URL, and every refusal says what
// to write.
func TestOAuthClientCredentialsBlock(t *testing.T) {
	const head = `
oauth:
  providers:
    keycloak:
      clientId: straza-connect
      clientSecret: s3cret
      authUrl: https://idp.example/realms/x/protocol/openid-connect/auth
      tokenUrl: https://idp.example/realms/x/protocol/openid-connect/token
`
	cases := []struct {
		name         string
		block        string
		wantErr      string
		wantAudience string
		wantScopes   string
		wantNil      bool
	}{
		{name: "no block", wantNil: true},
		{name: "audience and scopes", block: "      clientCredentials:\n        assertionAudience: https://idp.example/realms/x\n        scopes: [midpoint-mcp, profile]\n",
			wantAudience: "https://idp.example/realms/x", wantScopes: "midpoint-mcp profile"},
		{name: "audience alone", block: "      clientCredentials:\n        assertionAudience: https://idp.example/realms/x\n", wantAudience: "https://idp.example/realms/x"},
		{name: "an empty block has no default audience", block: "      clientCredentials: {}\n",
			wantErr: "oauth.providers.keycloak.clientCredentials: assertionAudience is required. Write the audience this provider wants in a client assertion: Keycloak takes its realm issuer URL and Okta its token endpoint URL. For an Entra agent blueprint, write entraBlueprint instead"},
		{name: "a scope with a space", block: "      clientCredentials:\n        assertionAudience: https://idp.example/realms/x\n        scopes: [\"a b\"]\n",
			wantErr: `oauth.providers.keycloak.clientCredentials: scope "a b" holds a character a scope cannot carry. Write one scope per list entry`},
		{name: "an empty scope", block: "      clientCredentials:\n        assertionAudience: https://idp.example/realms/x\n        scopes: [\"\"]\n",
			wantErr: `oauth.providers.keycloak.clientCredentials: scope "" holds a character a scope cannot carry. Write one scope per list entry`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := writeFile(t, head+tc.block)
			cfg, err := Loader{FilePath: path, ExplicitFile: true, Getenv: noEnv}.Load()
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("err = %v, want it to contain %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("Load: %v", err)
			}
			cc := cfg.OAuth.Providers["keycloak"].ClientCredentials
			if tc.wantNil {
				if cc != nil {
					t.Fatalf("clientCredentials = %+v, want none", cc)
				}
				return
			}
			if cc == nil {
				t.Fatal("clientCredentials is missing")
			}
			if cc.AssertionAudience != tc.wantAudience || strings.Join(cc.Scopes, " ") != tc.wantScopes {
				t.Errorf("clientCredentials = %+v, want audience %q and scopes %q", cc, tc.wantAudience, tc.wantScopes)
			}
		})
	}
}

// TestOAuthEntraBlueprint pins entraBlueprint, which switches a provider to
// the Entra agent identity exchange. The tenant is the one tokenUrl names, so
// a tokenUrl of no single tenant is refused, and so are a blueprint that is
// not a GUID, scopes other than one /.default scope and an assertionAudience
// beside it. Every refusal says what to write.
func TestOAuthEntraBlueprint(t *testing.T) {
	const (
		tenant    = "3c6bff24-ce9b-405c-8251-fe86232353b6"
		blueprint = "76568ffa-de8d-4d35-986d-4efe2a9f8787"
		field     = "oauth.providers.entra-lab.clientCredentials: "
		scopeErr  = field + "entraBlueprint needs exactly one scope that ends in /.default, such as api://<resource app id>/.default, because Entra's client credentials grant takes only that form. Write the resource's /.default scope under scopes"
	)
	v2 := "https://login.microsoftonline.com/" + tenant + "/oauth2/v2.0/token"
	block := func(tokenURL, cc string) string {
		return "\noauth:\n  providers:\n    entra-lab:\n      clientId: cf0611b0-e3d2-454b-acfe-4e2c1f40036b\n      clientSecret: s3cret\n" +
			"      authUrl: https://login.microsoftonline.com/" + tenant + "/oauth2/v2.0/authorize\n      tokenUrl: " + tokenURL + "\n      clientCredentials:\n" + cc
	}
	good := "        entraBlueprint: " + strings.ToUpper(blueprint) + "\n        scopes: [\"api://ffffffff-6666-4666-8666-ffffffffffff/.default\"]\n"
	shape := func(path string) string {
		return field + "entraBlueprint needs tokenUrl in the form https://login.microsoftonline.com/<tenant id>/oauth2/v2.0/token, the v2.0 token endpoint of one tenant, and its path is \"" + path + "\". Write the token endpoint of your tenant"
	}
	oneTenant := func(named string) string {
		return field + "entraBlueprint needs a tokenUrl with one tenant, and this one names " + named + ". Write the token endpoint of your tenant, https://login.microsoftonline.com/<tenant id>/oauth2/v2.0/token"
	}
	cases := []struct {
		name, tokenURL, cc, wantErr, wantTenant string
	}{
		{name: "a tenant id", tokenURL: v2, cc: good, wantTenant: tenant},
		{name: "a tenant domain", tokenURL: "https://login.microsoftonline.com/contoso.onmicrosoft.com/oauth2/v2.0/token", cc: good, wantTenant: "contoso.onmicrosoft.com"},
		{name: "a national cloud", tokenURL: "https://login.microsoftonline.us/" + tenant + "/oauth2/v2.0/token", cc: good, wantTenant: tenant},
		{name: "common", tokenURL: "https://login.microsoftonline.com/common/oauth2/v2.0/token", cc: good, wantErr: oneTenant("common")},
		{name: "organizations in any case", tokenURL: "https://login.microsoftonline.com/Organizations/oauth2/v2.0/token", cc: good, wantErr: oneTenant("Organizations")},
		{name: "consumers", tokenURL: "https://login.microsoftonline.com/consumers/oauth2/v2.0/token", cc: good, wantErr: oneTenant("consumers")},
		{name: "the v1 endpoint", tokenURL: "https://login.microsoftonline.com/" + tenant + "/oauth2/token", cc: good, wantErr: shape("/" + tenant + "/oauth2/token")},
		{name: "another path", tokenURL: "https://idp.example/realms/x/protocol/openid-connect/token", cc: good, wantErr: shape("/realms/x/protocol/openid-connect/token")},
		{name: "a blueprint that is not a GUID", tokenURL: v2, cc: "        entraBlueprint: straza-agents\n        scopes: [\"api://x/.default\"]\n",
			wantErr: field + `entraBlueprint "straza-agents" is not a GUID. Copy the application (client) id of the agent blueprint from the Entra admin centre`},
		{name: "no scope", tokenURL: v2, cc: "        entraBlueprint: " + blueprint + "\n", wantErr: scopeErr},
		{name: "two scopes", tokenURL: v2, cc: "        entraBlueprint: " + blueprint + "\n        scopes: [\"api://x/.default\", \"api://y/.default\"]\n", wantErr: scopeErr},
		{name: "a scope that is not .default", tokenURL: v2, cc: "        entraBlueprint: " + blueprint + "\n        scopes: [\"api://x/read\"]\n", wantErr: scopeErr},
		{name: "an assertion audience beside it", tokenURL: v2, cc: good + "        assertionAudience: " + v2 + "\n",
			wantErr: field + "entraBlueprint and assertionAudience exclude each other, because Entra takes the token endpoint as the assertion's audience. Remove assertionAudience"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := writeFile(t, block(tc.tokenURL, tc.cc))
			cfg, err := Loader{FilePath: path, ExplicitFile: true, Getenv: noEnv}.Load()
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("err = %v\nwant it to contain %s", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("Load: %v", err)
			}
			p := cfg.OAuth.Providers["entra-lab"]
			if p.ClientCredentials == nil || !strings.EqualFold(p.ClientCredentials.EntraBlueprint, blueprint) {
				t.Fatalf("clientCredentials = %+v, want the blueprint", p.ClientCredentials)
			}
			if got, err := EntraTenant(p.TokenURL); err != nil || got != tc.wantTenant {
				t.Fatalf("EntraTenant = %q, %v, want %q", got, err, tc.wantTenant)
			}
		})
	}
}
