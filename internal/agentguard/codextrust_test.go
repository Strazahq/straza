package agentguard

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// codexTrustKey renders a [hooks.state] key exactly as codex writes it
// (codex-rs/hooks/src/lib.rs): declaring file, snake_case event, group index,
// handler index. Spelled out here rather than reused from the code under
// test: if straza's idea of the key drifts from codex's, doctor silently
// reports every hook untrusted, and this literal is what catches that.
func codexTrustKey(hooksPath, snakeEvent string) string {
	quoted := strconv.Quote(hooksPath + ":" + snakeEvent + ":0:0")
	return quoted[1 : len(quoted)-1]
}

var codexEventSnake = map[string]string{
	"SessionStart":     "session_start",
	"PreToolUse":       "pre_tool_use",
	"UserPromptSubmit": "user_prompt_submit",
	"Stop":             "stop",
	"SessionEnd":       "session_end",
	"SubagentStart":    "subagent_start",
	"SubagentStop":     "subagent_stop",
}

// codexTrustAll renders a config.toml trusting every straza hook, in the
// per-key table form codex serializes.
func codexTrustAll(hooksPath string) string {
	var b strings.Builder
	for _, ev := range installs["codex"].allEvents() {
		b.WriteString("[hooks.state.\"" + codexTrustKey(hooksPath, codexEventSnake[ev]) + "\"]\n")
		b.WriteString("trusted_hash = \"sha256:" + strings.ToLower(ev) + "\"\n\n")
	}
	return b.String()
}

func TestCodexEventStateLabel(t *testing.T) {
	for event, want := range codexEventSnake {
		if got := codexEventStateLabel(event); got != want {
			t.Errorf("codexEventStateLabel(%s) = %q, want %q", event, got, want)
		}
	}
}

func TestReadCodexHookTrust(t *testing.T) {
	setOSName(t, "linux")
	install := func(t *testing.T) (home, hooks string) {
		t.Helper()
		home = t.TempDir()
		hooks = filepath.Join(home, codexHooksFile)
		if _, err := InstallCodexHooks(hooks, testStraza); err != nil {
			t.Fatal(err)
		}
		return home, hooks
	}
	writeConfig := func(t *testing.T, home, body string) string {
		t.Helper()
		path := filepath.Join(home, codexConfigFile)
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		return path
	}

	t.Run("no config.toml: trust is unknown, never assumed", func(t *testing.T) {
		home, hooks := install(t)
		got := ReadCodexHookTrust(hooks, filepath.Join(home, codexConfigFile))
		if got.Readable || got.Enforcing() {
			t.Errorf("= %+v, want unreadable and not enforcing", got)
		}
	})

	t.Run("no hooks.json: nothing to ask about", func(t *testing.T) {
		home := t.TempDir()
		writeConfig(t, home, "model = \"gpt-5\"\n")
		got := ReadCodexHookTrust(filepath.Join(home, codexHooksFile), filepath.Join(home, codexConfigFile))
		if got.Readable {
			t.Errorf("= %+v, want unreadable", got)
		}
	})

	t.Run("every hook trusted", func(t *testing.T) {
		home, hooks := install(t)
		cfg := writeConfig(t, home, codexTrustAll(hooks))
		got := ReadCodexHookTrust(hooks, cfg)
		if !got.Enforcing() {
			t.Fatalf("= %+v, want enforcing", got)
		}
		if len(got.Trusted) != len(installs["codex"].allEvents()) {
			t.Errorf("trusted = %v, want all %d events", got.Trusted, len(installs["codex"].allEvents()))
		}
	})

	t.Run("one event trusted, the rest awaiting /hooks", func(t *testing.T) {
		home, hooks := install(t)
		cfg := writeConfig(t, home, "[hooks.state.\""+codexTrustKey(hooks, "session_start")+"\"]\ntrusted_hash = \"sha256:abc\"\n")
		got := ReadCodexHookTrust(hooks, cfg)
		if !got.Readable || got.Enforcing() {
			t.Fatalf("= %+v, want readable but not enforcing", got)
		}
		if len(got.Trusted) != 1 || got.Trusted[0] != "SessionStart" {
			t.Errorf("trusted = %v, want [SessionStart]", got.Trusted)
		}
		if len(got.Untrusted) != len(installs["codex"].allEvents())-1 {
			t.Errorf("untrusted = %v", got.Untrusted)
		}
	})

	t.Run("an operator switched one off", func(t *testing.T) {
		home, hooks := install(t)
		cfg := writeConfig(t, home, codexTrustAll(hooks)+
			"[hooks.state.\""+codexTrustKey(hooks, "pre_tool_use")+"\"]\nenabled = false\n")
		got := ReadCodexHookTrust(hooks, cfg)
		if got.Enforcing() {
			t.Errorf("= %+v, want NOT enforcing: the blocking event is disabled", got)
		}
		if len(got.Disabled) != 1 || got.Disabled[0] != "PreToolUse" {
			t.Errorf("disabled = %v, want [PreToolUse]", got.Disabled)
		}
	})

	t.Run("inline state rows under a [hooks.state] header", func(t *testing.T) {
		home, hooks := install(t)
		body := "[hooks.state]\n"
		for _, ev := range installs["codex"].allEvents() {
			body += "\"" + codexTrustKey(hooks, codexEventSnake[ev]) + "\" = { trusted_hash = \"sha256:x\" }\n"
		}
		got := ReadCodexHookTrust(hooks, writeConfig(t, home, body))
		if !got.Enforcing() {
			t.Errorf("= %+v, want enforcing (inline rows are the documented shape too)", got)
		}
	})

	t.Run("inline row as a dotted key, disabled", func(t *testing.T) {
		// Top-level dotted key, before any table header: after one, TOML reads
		// a dotted key as a field OF that table, so this is the only position
		// where the shape means what it looks like.
		home, hooks := install(t)
		body := "hooks.state.\"" + codexTrustKey(hooks, "stop") + "\" = { enabled = false }\n\n" +
			"[features]\nhooks = true\n"
		got := ReadCodexHookTrust(hooks, writeConfig(t, home, body))
		if len(got.Disabled) != 1 || got.Disabled[0] != "Stop" {
			t.Errorf("disabled = %v, want [Stop]", got.Disabled)
		}
		if len(got.Untrusted) != len(installs["codex"].allEvents())-1 {
			t.Errorf("untrusted = %v, want every other event", got.Untrusted)
		}
	})

	t.Run("straza's entry is not first in the file", func(t *testing.T) {
		// The group index is the entry's position in the event array, so a
		// foreign hook ahead of ours shifts the key codex records.
		home := t.TempDir()
		hooks := filepath.Join(home, codexHooksFile)
		pre := `{"hooks":{"SessionStart":[{"hooks":[{"type":"command","command":"python3 ~/first.py"}]}]}}`
		if err := os.WriteFile(hooks, []byte(pre), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := InstallCodexHooks(hooks, testStraza); err != nil {
			t.Fatal(err)
		}
		cfg := writeConfig(t, home, "[hooks.state."+strconv.Quote(hooks+":session_start:1:0")+"]\ntrusted_hash = \"sha256:abc\"\n")
		got := ReadCodexHookTrust(hooks, cfg)
		if len(got.Trusted) != 1 || got.Trusted[0] != "SessionStart" {
			t.Errorf("trusted = %v, want [SessionStart] at group index 1", got.Trusted)
		}
	})

	t.Run("the whole hook lane switched off", func(t *testing.T) {
		for _, body := range []string{
			"[features]\nhooks = false\n",
			"[features]\ncodex_hooks = false\n",
			"features.hooks = false\n",
		} {
			home, hooks := install(t)
			got := ReadCodexHookTrust(hooks, writeConfig(t, home, body))
			if !got.HooksFeatureOff {
				t.Errorf("config %q: HooksFeatureOff = false, want true", body)
			}
			if got.Enforcing() {
				t.Errorf("config %q: enforcing with hooks disabled", body)
			}
		}
	})

	t.Run("hooks enabled is not hooks disabled", func(t *testing.T) {
		home, hooks := install(t)
		got := ReadCodexHookTrust(hooks, writeConfig(t, home, "[features]\nhooks = true\n"+codexTrustAll(hooks)))
		if got.HooksFeatureOff {
			t.Error("`hooks = true` read as disabled")
		}
	})
}

// TestCodexHooksCheckStates walks doctor's codex lane. The rule under test:
// no green light without evidence.
func TestCodexHooksCheckStates(t *testing.T) {
	setOSName(t, "linux")
	codexSession := Session{SessionID: "s1", Harness: "codex/0.146.0", ExpiresAt: time.Now().Add(time.Hour)}

	tests := []struct {
		name        string
		hooks       bool   // write hooks.json
		partial     bool   // write an older straza's partial hooks.json instead
		stale       bool   // leave the dead settings.json behind
		managed     bool   // write a managed requirements.toml (no user hooks)
		config      string // config.toml body ("" = no file); %s is the hooks path
		session     Session
		haveSession bool
		wantCheck   bool
		wantStatus  string
		wantDetail  string
	}{
		{
			name:      "codex not in use: no line at all",
			wantCheck: false,
		},
		{
			// Managed only: an empty user hooks.json is the CORRECT state when
			// requirements.toml governs, and a "run `straza install codex`" hint
			// would add an unwanted user layer.
			name:       "managed-only box: empty user hooks.json is correct",
			managed:    true,
			wantCheck:  true,
			wantStatus: checkOK,
			wantDetail: "managed lane",
		},
		{
			name:       "the incident: dead settings.json, nothing else",
			stale:      true,
			wantCheck:  true,
			wantStatus: checkWarn,
			wantDetail: "no codex release reads settings.json",
		},
		{
			name:       "dead settings.json survives beside a good install",
			hooks:      true,
			stale:      true,
			wantCheck:  true,
			wantStatus: checkWarn,
			wantDetail: "dead Straza wiring",
		},
		{
			name:       "wiring written, trust unknowable: NOT ok",
			hooks:      true,
			wantCheck:  true,
			wantStatus: checkWarn,
			wantDetail: "trust cannot be verified from outside",
		},
		{
			name:       "wiring written and codex says untrusted",
			hooks:      true,
			config:     "[hooks.state.\"%s:session_start:0:0\"]\ntrusted_hash = \"sha256:abc\"\n",
			wantCheck:  true,
			wantStatus: checkWarn,
			wantDetail: "no trust record",
		},
		{
			name:       "codex records every hook trusted: ok",
			hooks:      true,
			config:     "TRUST_ALL",
			wantCheck:  true,
			wantStatus: checkOK,
			wantDetail: "records all 7 Straza hooks as trusted",
		},
		{
			name:       "hooks switched off wholesale",
			hooks:      true,
			config:     "[features]\nhooks = false\n",
			wantCheck:  true,
			wantStatus: checkWarn,
			wantDetail: "switched OFF",
		},
		{
			name:       "an operator disabled one",
			hooks:      true,
			config:     "TRUST_ALL+DISABLE_PRE",
			wantCheck:  true,
			wantStatus: checkWarn,
			wantDetail: "DISABLED by an operator",
		},
		{
			name:        "no trust record but a codex session checked in: ok on evidence",
			hooks:       true,
			session:     codexSession,
			haveSession: true,
			wantCheck:   true,
			wantStatus:  checkOK,
			wantDetail:  "a codex session checked in",
		},
		{
			name:        "another harness's session is not codex evidence",
			hooks:       true,
			session:     Session{SessionID: "s2", Harness: "claude-code/2.1.214"},
			haveSession: true,
			wantCheck:   true,
			wantStatus:  checkWarn,
			wantDetail:  "trust cannot be verified from outside",
		},
		{
			name:       "an older straza wired only some events",
			partial:    true,
			wantCheck:  true,
			wantStatus: checkWarn,
			wantDetail: "is missing",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("CODEX_HOME", home)
			t.Setenv("STRAZA_MANAGED_SETTINGS_DIR", t.TempDir())
			hooksPath := filepath.Join(home, codexHooksFile)

			if tc.managed {
				managedPath, err := ManagedSettingsPath("codex")
				if err != nil {
					t.Fatal(err)
				}
				if err := os.MkdirAll(filepath.Dir(managedPath), 0o750); err != nil {
					t.Fatal(err)
				}
				body := "[[hooks.SessionStart]]\n[[hooks.SessionStart.hooks]]\ncommand = '" +
					testStraza + " hook --harness codex'\n"
				if err := os.WriteFile(managedPath, []byte(body), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			if tc.hooks {
				if _, err := InstallCodexHooks(hooksPath, testStraza); err != nil {
					t.Fatal(err)
				}
			}
			if tc.partial {
				body := `{"hooks":{"SessionStart":[{"hooks":[{"type":"command","command":"` +
					testStraza + ` hook --harness codex"}]}]}}`
				if err := os.WriteFile(hooksPath, []byte(body), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			if tc.stale {
				if err := InstallHooks("codex", filepath.Join(home, codexStaleHooksFile), testStraza); err != nil {
					t.Fatal(err)
				}
			}
			if tc.config != "" {
				body := tc.config
				switch {
				case body == "TRUST_ALL":
					body = codexTrustAll(hooksPath)
				case body == "TRUST_ALL+DISABLE_PRE":
					body = codexTrustAll(hooksPath) +
						"[hooks.state.\"" + codexTrustKey(hooksPath, "pre_tool_use") + "\"]\nenabled = false\n"
				case strings.Contains(body, "%s"):
					quoted := strconv.Quote(hooksPath)
					body = strings.ReplaceAll(body, "%s", quoted[1:len(quoted)-1])
				}
				if err := os.WriteFile(filepath.Join(home, codexConfigFile), []byte(body), 0o600); err != nil {
					t.Fatal(err)
				}
			}

			got := codexHooksCheck(tc.session, tc.haveSession)
			if !tc.wantCheck {
				if got != nil {
					t.Fatalf("want no check, got %+v", got)
				}
				return
			}
			if got == nil {
				t.Fatal("want a check, got none")
			}
			if got.Name != "codex-hooks" {
				t.Errorf("name = %q, want codex-hooks", got.Name)
			}
			if got.Status != tc.wantStatus {
				t.Errorf("status = %q, want %q (detail %q)", got.Status, tc.wantStatus, got.Detail)
			}
			if !strings.Contains(got.Detail, tc.wantDetail) {
				t.Errorf("detail = %q, want it to mention %q", got.Detail, tc.wantDetail)
			}
			if got.Status != checkOK && got.Hint == "" {
				t.Error("a non-ok check without a next step is noise")
			}
			if got.Status == checkOK && strings.Contains(got.Detail, "governed") {
				t.Errorf("detail claims codex is governed: %q", got.Detail)
			}
		})
	}
}

// TestWiringCheckCodexLabel: the roster line must not read as a
// clean bill of health for codex on its own: the trust gate lives one check
// down, and the label has to send the reader there.
func TestWiringCheckCodexLabel(t *testing.T) {
	setOSName(t, "linux")
	home := t.TempDir()
	t.Setenv("CODEX_HOME", home)
	t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())
	t.Setenv("STRAZA_GEMINI_CONFIG_DIR", t.TempDir())
	t.Setenv("STRAZA_MANAGED_SETTINGS_DIR", t.TempDir())
	if _, err := InstallCodexHooks(filepath.Join(home, codexHooksFile), testStraza); err != nil {
		t.Fatal(err)
	}
	got := wiringCheck()
	if !strings.Contains(got.Detail, "trust gate") {
		t.Errorf("wiring detail = %q, want the codex entry to name the trust gate", got.Detail)
	}
}
