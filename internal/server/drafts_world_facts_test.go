package server

import (
	"reflect"
	"testing"
)

// TestManifestFactsScopes pins the OAuth scopes manifestFacts reads, which
// server.scopes-wider compares: the scopes of an oauth credential as the
// manifest lists them, and none for a credential of another kind.
func TestManifestFactsScopes(t *testing.T) {
	t.Parallel()
	head := "apiVersion: straza.dev/v1beta1\nkind: App\nmetadata: {name: mf}\nserver: {name: example.com/mf, version: \"1.0.0\"}\n" +
		"straza:\n  runtime: {kind: remote, remote: {url: \"https://api.example.com/mcp\"}}\n"
	cases := []struct {
		name, credential string
		want             []string
	}{
		{"an oauth credential with scopes", "  credential:\n    kind: oauth\n    oauth: {provider: entra, scopes: [repo, read:user]}\n" +
			"    inject: {as: header, name: Authorization, template: \"Bearer {{secret}}\"}\n", []string{"repo", "read:user"}},
		{"an oauth credential without scopes", "  credential:\n    kind: oauth\n    oauth: {provider: entra}\n" +
			"    inject: {as: header, name: Authorization, template: \"Bearer {{secret}}\"}\n", nil},
		{"a static credential", "  credential:\n    kind: static\n    inject: {as: header, name: X-Api-Key}\n", nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := appFactsOf(t, head+tc.credential).Scopes; !reflect.DeepEqual(got, tc.want) {
				t.Errorf("scopes %q, want %q", got, tc.want)
			}
		})
	}
}
