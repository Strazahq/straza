package agentguard

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestRenderBlockOnFailure pins which rendered hook entries ask the harness
// to block the call when the straza hook fails: the claude-code pre-tool
// entry alone, on every GOOS. Every other claude-code event keeps the vendor
// default, and codex and gemini, which document no such setting, carry no
// key that their parsers could refuse.
func TestRenderBlockOnFailure(t *testing.T) {
	for _, harness := range []string{"claude-code", "codex", "gemini"} {
		for _, goos := range SupportedGOOS {
			t.Run(harness+"/"+goos, func(t *testing.T) {
				rendered, err := RenderManagedArtifacts(harness, goos, defaultManagedBinaryPath(goos))
				if err != nil {
					t.Fatal(err)
				}
				raw := rendered["hooks."+harness]
				if harness == "codex" {
					if strings.Contains(string(raw), "onFailure") {
						t.Fatalf("codex render carries onFailure:\n%s", raw)
					}
					return
				}
				var doc struct {
					Hooks map[string][]struct {
						Hooks []map[string]any `json:"hooks"`
					} `json:"hooks"`
				}
				if err := json.Unmarshal(raw, &doc); err != nil {
					t.Fatal(err)
				}
				seen := 0
				for event, groups := range doc.Hooks {
					for _, g := range groups {
						for _, hk := range g.Hooks {
							want := harness == "claude-code" && event == "PreToolUse"
							got, has := hk["onFailure"]
							if want && got != "block" {
								t.Errorf("%s: onFailure = %v, want block", event, got)
							}
							if !want && has {
								t.Errorf("%s: carries onFailure %v, want none", event, got)
							}
							if want {
								seen++
							}
						}
					}
				}
				if harness == "claude-code" && seen != 1 {
					t.Errorf("claude-code PreToolUse hooks with onFailure = %d, want 1", seen)
				}
			})
		}
	}
}

// TestInstallHooksUpgradesEntryInPlace: a re-install over wiring that an
// older install wrote adds the setting to Straza's own pre-tool hook, keeps
// one Straza hook in the group, and leaves an operator's hook in the same
// group as it was.
func TestInstallHooksUpgradesEntryInPlace(t *testing.T) {
	cases := []struct {
		harness, preEvent string
		wantBlock         bool
	}{
		{"claude-code", "PreToolUse", true},
		{"gemini", "BeforeTool", false},
	}
	for _, c := range cases {
		t.Run(c.harness, func(t *testing.T) {
			setOSName(t, "linux")
			cmd := "/opt/straza hook --harness " + c.harness
			settings := filepath.Join(t.TempDir(), "settings.json")
			body := `{"hooks":{"` + c.preEvent + `":[{"matcher":"*","hooks":[` +
				`{"type":"command","command":"` + cmd + `"},` +
				`{"type":"command","command":"echo mine"}]}]}}`
			if err := os.WriteFile(settings, []byte(body), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := InstallHooks(c.harness, settings, "/opt/straza"); err != nil {
				t.Fatal(err)
			}
			hooks, _ := readSettings(t, settings)["hooks"].(map[string]any)
			groups, _ := hooks[c.preEvent].([]any)
			if len(groups) != 1 {
				t.Fatalf("%s groups = %d, want the one group kept", c.preEvent, len(groups))
			}
			ours := strazaHookIn(groups[0], cmd)
			mine := strazaHookIn(groups[0], "echo mine")
			if ours == nil || mine == nil {
				t.Fatalf("group = %v, want both hooks kept", groups[0])
			}
			if got := ours["onFailure"] == "block"; got != c.wantBlock {
				t.Errorf("straza hook onFailure = %v, want block %v", ours["onFailure"], c.wantBlock)
			}
			if _, has := mine["onFailure"]; has {
				t.Errorf("the operator's hook gained onFailure: %v", mine)
			}
			if n := len(groups[0].(map[string]any)["hooks"].([]any)); n != 2 {
				t.Errorf("group holds %d hooks, want 2", n)
			}
		})
	}
}
