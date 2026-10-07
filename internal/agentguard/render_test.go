package agentguard

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

// TestRenderMatchesManagedInstall is the byte-equality contract behind
// server-side rendering: for every Tier-1 harness on every GOOS,
// RenderManagedArtifacts must produce exactly the bytes the managed
// installer writes into an empty layout. The server serves the render, the
// installer writes it, attestation hashes the file: one divergent byte and
// every managed check-in on that platform drops to att=none.
func TestRenderMatchesManagedInstall(t *testing.T) {
	for _, harness := range []string{"claude-code", "codex", "gemini"} {
		for _, goos := range SupportedGOOS {
			t.Run(harness+"/"+goos, func(t *testing.T) {
				setOSName(t, goos)
				sys := t.TempDir()
				t.Setenv("STRAZA_MANAGED_SETTINGS_DIR", filepath.Join(sys, "harness"))
				binDir := filepath.Join(sys, "bin")
				if err := os.MkdirAll(binDir, 0o755); err != nil {
					t.Fatal(err)
				}
				binPath := filepath.Join(binDir, "straza")

				settings, err := managedHookPath(harness)
				if err != nil {
					t.Fatal(err)
				}
				if harness == "codex" {
					if _, err := InstallCodexManagedHooks(settings, binPath); err != nil {
						t.Fatal(err)
					}
				} else if err := InstallManagedHooks(harness, settings, binPath); err != nil {
					t.Fatal(err)
				}
				installed, err := os.ReadFile(settings)
				if err != nil {
					t.Fatal(err)
				}

				rendered, err := RenderManagedArtifacts(harness, goos, binPath)
				if err != nil {
					t.Fatal(err)
				}
				hooks, ok := rendered["hooks."+harness]
				if !ok {
					t.Fatalf("render returned no hooks.%s artifact (got %v)", harness, artifactNames(rendered))
				}
				if !bytes.Equal(hooks, installed) {
					t.Fatalf("render diverges from managed install\nrender:\n%s\ninstall:\n%s", hooks, installed)
				}

				if harness == "claude-code" {
					mcpPath, err := ManagedMCPConfigPath(harness)
					if err != nil {
						t.Fatal(err)
					}
					if _, err := InstallManagedMCPServer(harness, mcpPath, binPath); err != nil {
						t.Fatal(err)
					}
					installedMCP, err := os.ReadFile(mcpPath)
					if err != nil {
						t.Fatal(err)
					}
					if !bytes.Equal(rendered["mcp.claude-code"], installedMCP) {
						t.Fatalf("mcp render diverges\nrender:\n%s\ninstall:\n%s", rendered["mcp.claude-code"], installedMCP)
					}
				} else if _, ok := rendered["mcp."+harness]; ok {
					t.Fatalf("%s has no vendor-documented managed MCP layer but render produced one", harness)
				}
			})
		}
	}
}

// TestRenderDefaultBinPath pins the default-layout render the server
// publishes: deterministic across calls, and the hook command points at the
// per-GOOS default managed binary (the path install --managed copies to).
func TestRenderDefaultBinPath(t *testing.T) {
	cases := []struct {
		goos, wantPath string
	}{
		{"linux", "/usr/local/bin/straza"},
		{"darwin", "/usr/local/bin/straza"},
		{"windows", `C:\\ProgramData\\straza\\bin\\straza.exe`}, // JSON-escaped in settings bytes
	}
	for _, tc := range cases {
		t.Run(tc.goos, func(t *testing.T) {
			a, err := RenderManagedArtifacts("claude-code", tc.goos, "")
			if err != nil {
				t.Fatal(err)
			}
			b, err := RenderManagedArtifacts("claude-code", tc.goos, "")
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(a["hooks.claude-code"], b["hooks.claude-code"]) {
				t.Fatal("render is not deterministic")
			}
			if !bytes.Contains(a["hooks.claude-code"], []byte(tc.wantPath)) {
				t.Fatalf("hooks render does not invoke the default managed binary %s:\n%s", tc.wantPath, a["hooks.claude-code"])
			}
		})
	}
	if _, err := RenderManagedArtifacts("cursor", "linux", ""); err == nil {
		t.Fatal("unknown harness accepted")
	}
	if _, err := RenderManagedArtifacts("claude-code", "plan9", ""); err == nil {
		t.Fatal("unsupported GOOS accepted")
	}
}

func artifactNames(m map[string][]byte) []string {
	names := make([]string, 0, len(m))
	for n := range m {
		names = append(names, n)
	}
	return names
}
