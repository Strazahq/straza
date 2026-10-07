package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/strazahq/straza/internal/manager"
)

// serverBlockManifest is a manifest whose server block carries the scalars
// a YAML round trip can change: numbers, booleans, a string that reads as a
// number and one that reads as a boolean, and nested lists.
const serverBlockManifest = `apiVersion: straza.dev/v1beta1
kind: App
metadata:
  name: scalar-probe
  description: server block scalars
server:
  name: io.example/scalar-probe
  version: "1.0"
  port: 8080
  ratio: 0.25
  enabled: true
  answer: "yes"
  code: "007"
  packages:
    - registryType: npm
      identifier: "@example/probe"
      args: [--port, "8080"]
straza:
  runtime:
    kind: remote
    remote:
      url: https://mcp.example.com/mcp?region=eu
  exposure:
    tools: [read_*, "search"]
  limits:
    rps: 2.5
    timeoutSeconds: 30
`

// TestAppsExportRoundTrips pins apps export against every valid example in
// spec/app-manifest and a server block of tricky scalars: the manifest the
// server stored comes back as a document that strazactl spec validate app
// takes as it is and that parses to the same stored manifest, by name and by
// id, with nothing else on either stream.
func TestAppsExportRoundTrips(t *testing.T) {
	docs := map[string][]byte{"server block scalars": []byte(serverBlockManifest)}
	examples, err := filepath.Glob("../../spec/app-manifest/examples/valid-*.yaml")
	if err != nil || len(examples) < 3 {
		t.Fatalf("spec examples: %v (%d found)", err, len(examples))
	}
	for _, path := range examples {
		raw, err := os.ReadFile(path) // #nosec G304 -- the repo's own spec corpus
		if err != nil {
			t.Fatal(err)
		}
		docs[filepath.Base(path)] = raw
	}
	validate, err := specValidator("app")
	if err != nil {
		t.Fatal(err)
	}
	for name, raw := range docs {
		m, err := manager.Parse(raw)
		if err != nil {
			t.Fatalf("%s does not parse: %v", name, err)
		}
		stored, err := m.JSON()
		if err != nil {
			t.Fatal(err)
		}
		list := `[{"id":"a1","name":"` + m.Metadata.Name + `","status":"running","manifest":` + stored + `}]`
		for _, ref := range []string{m.Metadata.Name, "a1"} {
			t.Run(name+" by "+ref, func(t *testing.T) {
				srv := jsonServer(t, map[string]string{"GET /v1/admin/apps": list})
				t.Setenv("STRAZA_SERVER", "")
				stdout, stderr, err := runCLI(t, writeCreds(t, srv.URL), "apps", "export", ref)
				if err != nil {
					t.Fatalf("apps export: %v", err)
				}
				if stderr != "" {
					t.Errorf("stderr = %q, want nothing", stderr)
				}
				if err := validate([]byte(stdout)); err != nil {
					t.Fatalf("spec validate app refuses the export: %v\n%s", err, stdout)
				}
				again, err := manager.Parse([]byte(stdout))
				if err != nil {
					t.Fatal(err)
				}
				if got, _ := again.JSON(); got != stored {
					t.Errorf("the export parses to\n%s\nwant the stored\n%s", got, stored)
				}
			})
		}
	}
}

// TestAppsExportRefusals pins what apps export says when it cannot export,
// with nothing on stdout: an unknown server, a server whose manifest did not
// come with the list, and a manifest with a field this build does not know,
// which an export would drop.
func TestAppsExportRefusals(t *testing.T) {
	tests := []struct {
		name    string
		list    string
		ref     string
		wantErr string
	}{
		{
			name:    "unknown server",
			list:    `[{"id":"a1","name":"demo-tools","manifest":{"kind":"App"}}]`,
			ref:     "nosuch",
			wantErr: `no MCP server named "nosuch". List them with strazactl apps list`,
		},
		{
			name:    "no manifest in the list",
			list:    `[{"id":"a1","name":"demo-tools"}]`,
			ref:     "demo-tools",
			wantErr: "strazad sent no manifest for the MCP server demo-tools: its stored copy does not decode, or strazad is older than this strazactl",
		},
		{
			name:    "a field this build does not know",
			list:    `[{"id":"a1","name":"demo-tools","manifest":{"apiVersion":"straza.dev/v1beta1","kind":"App","straza":{"runtime":{"kind":"remote"},"sandbox":{"net":"none"}}}}]`,
			ref:     "demo-tools",
			wantErr: "the stored manifest of the MCP server demo-tools does not fit this strazactl",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if !json.Valid([]byte(tc.list)) {
				t.Fatalf("test list is not JSON: %s", tc.list)
			}
			srv := jsonServer(t, map[string]string{"GET /v1/admin/apps": tc.list})
			t.Setenv("STRAZA_SERVER", "")
			stdout, _, err := runCLI(t, writeCreds(t, srv.URL), "apps", "export", tc.ref)
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("err = %v, want it to contain %q", err, tc.wantErr)
			}
			if stdout != "" {
				t.Errorf("stdout = %q, want nothing", stdout)
			}
		})
	}
}
