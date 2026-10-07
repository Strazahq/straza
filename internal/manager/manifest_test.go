package manager

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const (
	specExamples     = "../../spec/app-manifest/examples"
	registryFixtures = "../../spec/conformance/registry"
)

// TestManifestAgreesWithSpecExamples pins that the Go
// validator and the JSON Schema agree on every example in the spec corpus.
func TestManifestAgreesWithSpecExamples(t *testing.T) {
	entries, err := os.ReadDir(specExamples)
	if err != nil {
		t.Fatalf("read spec examples: %v", err)
	}
	if len(entries) == 0 {
		t.Fatal("spec example corpus is empty")
	}
	var valid, invalid int
	for _, e := range entries {
		name := e.Name()
		raw, err := os.ReadFile(filepath.Join(specExamples, name))
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		switch {
		case strings.HasPrefix(name, "valid-"):
			valid++
			if _, err := Parse(raw); err != nil {
				t.Errorf("%s: expected valid, got: %v", name, err)
			}
		case strings.HasPrefix(name, "invalid-"):
			invalid++
			if _, err := Parse(raw); err == nil {
				t.Errorf("%s: expected invalid, parsed cleanly", name)
			}
		default:
			t.Errorf("unclassified example %s (name must start valid-/invalid-)", name)
		}
	}
	if valid < 3 || invalid < 3 {
		t.Errorf("spec corpus needs >=3 valid and >=3 invalid examples, got %d/%d", valid, invalid)
	}
}

// TestManifestAPIVersions pins the version-acceptance contract: canonical
// straza.dev/v1beta1 parses, and ids under any other domain are rejected
// with the canonical id in the reason.
func TestManifestAPIVersions(t *testing.T) {
	const body = `
kind: App
metadata: {name: minimal}
server: {name: example/minimal, version: "1.0.0"}
straza:
  runtime:
    kind: remote
    remote: {url: https://example.com/mcp}
`
	cases := []struct {
		name    string
		version string
		ok      bool
	}{
		{"canonical v1beta1", "straza.dev/v1beta1", true},
		{"pre-rename v1beta1 id rejected", "legacy.example/v1beta1", false},
		{"pre-rename v1alpha1 id rejected", "legacy.example/v1alpha1", false},
		{"unknown future version", "straza.dev/v2", false},
		{"missing apiVersion", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Parse([]byte("apiVersion: " + tc.version + body))
			if tc.ok && err != nil {
				t.Fatalf("Parse: %v", err)
			}
			if !tc.ok {
				if err == nil || !strings.Contains(err.Error(), APIVersion) {
					t.Fatalf("want rejection naming %q, got %v", APIVersion, err)
				}
			}
		})
	}
}

func TestParseDefaults(t *testing.T) {
	raw := []byte(`
apiVersion: straza.dev/v1beta1
kind: App
metadata: {name: minimal}
server: {name: example/minimal, version: "1.0.0"}
straza:
  runtime:
    kind: remote
    remote: {url: https://example.com/mcp}
  credential:
    kind: static
    inject: {as: header, name: Authorization}
`)
	m, err := Parse(raw)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if got := m.ExposedTools(); len(got) != 1 || got[0] != "*" {
		t.Errorf("exposure default = %v, want [*]", got)
	}
	if m.Straza.Runtime.Remote.Auth != AuthInject {
		t.Errorf("remote.auth default = %q, want inject", m.Straza.Runtime.Remote.Auth)
	}
	if m.Straza.Credential.Inject.Template != SecretPlaceholder {
		t.Errorf("inject.template default = %q, want %s", m.Straza.Credential.Inject.Template, SecretPlaceholder)
	}
	if m.ServerName() != "example/minimal" || m.ServerVersion() != "1.0.0" {
		t.Errorf("server accessors = %q/%q", m.ServerName(), m.ServerVersion())
	}
}

func TestManifestJSONRoundTrip(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join(specExamples, "valid-github-remote.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	m, err := Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	js, err := m.JSON()
	if err != nil {
		t.Fatal(err)
	}
	back, err := FromJSON(js)
	if err != nil {
		t.Fatal(err)
	}
	if canonicalJSON(t, back) != canonicalJSON(t, m) {
		t.Error("JSON round trip changed the manifest")
	}
}

// TestRegistryImportGoldens replays the real registry captures in
// spec/conformance/registry and requires byte-identical (canonical-JSON)
// agreement with the golden manifests: every real entry imports and
// validates.
func TestRegistryImportGoldens(t *testing.T) {
	entries, err := os.ReadDir(registryFixtures)
	if err != nil {
		t.Fatalf("read registry fixtures: %v", err)
	}
	n := 0
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".server.json") {
			continue
		}
		n++
		base := strings.TrimSuffix(e.Name(), ".server.json")
		t.Run(base, func(t *testing.T) {
			serverJSON, err := os.ReadFile(filepath.Join(registryFixtures, e.Name()))
			if err != nil {
				t.Fatal(err)
			}
			goldenYAML, err := os.ReadFile(filepath.Join(registryFixtures, base+".app.yaml"))
			if err != nil {
				t.Fatalf("missing golden: %v", err)
			}
			imported, err := Import(serverJSON, ImportOptions{})
			if err != nil {
				t.Fatalf("import: %v", err)
			}
			golden, err := Parse(goldenYAML)
			if err != nil {
				t.Fatalf("golden parse: %v", err)
			}
			got, want := canonicalJSON(t, imported), canonicalJSON(t, golden)
			if got != want {
				t.Errorf("import mismatch\n got: %s\nwant: %s", got, want)
			}
		})
	}
	if n < 3 {
		t.Errorf("need >=3 registry fixtures, got %d", n)
	}
}

func TestRegistryImportOverrides(t *testing.T) {
	serverJSON, err := os.ReadFile(filepath.Join(registryFixtures, "github-mcp-server.server.json"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Import(serverJSON, ImportOptions{Runtime: RuntimeRemote}); err == nil {
		t.Error("expected error: github-mcp-server capture has no remotes")
	}
	m, err := Import(serverJSON, ImportOptions{Name: "github"})
	if err != nil {
		t.Fatal(err)
	}
	if m.Metadata.Name != "github" {
		t.Errorf("name override = %q", m.Metadata.Name)
	}
	if _, err := Import(serverJSON, ImportOptions{Name: "Bad_Name"}); err == nil {
		t.Error("expected error for invalid name override")
	}
	if _, err := Import([]byte(`{"name":"x/y"}`), ImportOptions{}); err == nil {
		t.Error("expected error: no version")
	}
	if _, err := Import([]byte(`{"name":"x/y","version":"1.0"}`), ImportOptions{}); err == nil {
		t.Error("expected error: no usable runtime")
	}
}

// canonicalJSON renders a manifest as sorted-key JSON for comparison
// (encoding/json sorts map keys, so server blocks compare structurally).
func canonicalJSON(t *testing.T, m Manifest) string {
	t.Helper()
	js, err := m.JSON()
	if err != nil {
		t.Fatal(err)
	}
	var v any
	if err := json.Unmarshal([]byte(js), &v); err != nil {
		t.Fatal(err)
	}
	out, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(out)
}

// TestParseRefusesUnstorableManifest: a manifest whose stored JSON form
// cannot be produced is refused at parse time with a sentence that names
// the problem and the fix, so the install answers it as a manifest error.
func TestParseRefusesUnstorableManifest(t *testing.T) {
	doc := func(server, limits string) string {
		return "apiVersion: straza.dev/v1beta1\nkind: App\nmetadata: {name: a}\n" +
			"server: {name: straza.test/a, version: \"1.0.0\"" + server + "}\n" +
			"straza:\n  runtime:\n    kind: remote\n    remote: {url: \"http://127.0.0.1:1/mcp\"}\n" + limits
	}
	cases := []struct {
		name string
		raw  string
		want string
	}{
		{name: "a key that is not a string in the server block", raw: doc(", _meta: {1: one}", ""),
			want: "manifest: the server block holds a key that is not a string, which cannot be stored. Quote the key, then install again"},
		{name: "an infinite number", raw: doc("", "  limits: {rps: .inf}\n"),
			want: "manifest: the number +Inf cannot be stored. Write a finite number, then install again"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Parse([]byte(tc.raw))
			if err == nil || err.Error() != tc.want {
				t.Errorf("Parse err = %v\nwant %s", err, tc.want)
			}
		})
	}
	if _, err := Parse([]byte(doc(`, _meta: {"1": one}`, ""))); err != nil {
		t.Errorf("a quoted key is refused: %v", err)
	}
}
