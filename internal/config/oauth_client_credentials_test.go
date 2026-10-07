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
			wantErr: "oauth.providers.keycloak.clientCredentials: assertionAudience is required. Write the audience this provider wants in a client assertion: Keycloak takes its realm issuer URL and Okta its token endpoint URL"},
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
