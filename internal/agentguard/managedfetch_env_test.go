package agentguard

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestInstallManagedOverOlderWiringTakesServedLane: a re-install over the
// wiring an older Straza wrote, its hooks and its own git env entries and
// nothing else, takes the server-published lane and writes the served
// bytes, so it never asks to register a hash the server already holds. An
// env entry of the operator's still forces the local lane and survives.
func TestInstallManagedOverOlderWiringTakesServedLane(t *testing.T) {
	cases := []struct {
		name     string
		edit     func(settings map[string]any)
		wantLane string
		keep     string
	}{
		{"older Straza wiring", func(map[string]any) {}, "server-published", ""},
		{"an operator env entry", func(s map[string]any) {
			s["env"].(map[string]any)["OPERATOR_SETTING"] = "1"
		}, "local render", "OPERATOR_SETTING"},
		{"Straza's env entry changed by hand", func(s map[string]any) {
			s["env"].(map[string]any)["GIT_CONFIG_VALUE_0"] = "/srv/other.conf"
		}, "local render", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newFetchFixture(t, "linux")
			arts, err := renderManagedArtifacts("claude-code", "linux", f.binPath, gitHostsFileHere())
			if err != nil {
				t.Fatal(err)
			}
			f.serve(t, "claude-code", "linux", map[string][]byte{
				"hooks.claude-code": arts["hooks.claude-code"], "mcp.claude-code": arts["mcp.claude-code"],
			})

			// The older wiring: the same render without the onFailure key.
			var older map[string]any
			if err := json.Unmarshal(arts["hooks.claude-code"], &older); err != nil {
				t.Fatal(err)
			}
			pre := older["hooks"].(map[string]any)["PreToolUse"].([]any)[0].(map[string]any)
			delete(pre["hooks"].([]any)[0].(map[string]any), "onFailure")
			c.edit(older)
			raw, err := json.MarshalIndent(older, "", "  ")
			if err != nil {
				t.Fatal(err)
			}
			path, err := ManagedHookPath("claude-code")
			if err != nil {
				t.Fatal(err)
			}
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, raw, 0o644); err != nil {
				t.Fatal(err)
			}

			out, err := f.install(t, "claude-code")
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(out, c.wantLane) {
				t.Fatalf("want the %q lane, got:\n%s", c.wantLane, out)
			}
			got := readManaged(t, "claude-code")
			if c.wantLane == "server-published" {
				if !bytes.Equal(got, arts["hooks.claude-code"]) {
					t.Fatalf("managed settings are not the served bytes:\n%s", got)
				}
				if strings.Contains(out, "attestation add") {
					t.Fatalf("asks to register a hash the server holds:\n%s", out)
				}
			}
			if c.keep != "" && !strings.Contains(string(got), c.keep) {
				t.Fatalf("operator content %s was dropped:\n%s", c.keep, got)
			}
		})
	}
}
