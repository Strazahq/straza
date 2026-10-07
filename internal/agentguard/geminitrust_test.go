package agentguard

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// geminiTestHome wires an isolated gemini user scope + straza managed seam and
// returns the two settings paths. Wiring is written through the REAL installer
// so the checks read what install writes, not hand-authored bytes.
func geminiTestHome(t *testing.T, userWired, managedWired bool) (userPath, managedPath string) {
	t.Helper()
	userDir := t.TempDir()
	t.Setenv("STRAZA_GEMINI_CONFIG_DIR", userDir)
	t.Setenv("STRAZA_MANAGED_SETTINGS_DIR", t.TempDir())
	t.Setenv("GEMINI_CLI_SYSTEM_SETTINGS_PATH", "")
	t.Setenv("GEMINI_CLI_TRUST_WORKSPACE", "")
	t.Setenv("GEMINI_CLI_TRUSTED_FOLDERS_PATH", "")
	userPath = filepath.Join(userDir, "settings.json")
	managedPath, err := ManagedSettingsPath("gemini")
	if err != nil {
		t.Fatal(err)
	}
	if userWired {
		if err := InstallHooks("gemini", userPath, "/usr/local/bin/straza"); err != nil {
			t.Fatal(err)
		}
	}
	if managedWired {
		if err := InstallManagedHooks("gemini", managedPath, "/usr/local/bin/straza"); err != nil {
			t.Fatal(err)
		}
	}
	return userPath, managedPath
}

func setJSONKey(t *testing.T, path string, mutate func(doc map[string]any)) {
	t.Helper()
	doc := map[string]any{}
	if raw, err := os.ReadFile(path); err == nil {
		if err := json.Unmarshal(raw, &doc); err != nil {
			t.Fatal(err)
		}
	}
	mutate(doc)
	out, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, out, 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestGeminiHooksCheck(t *testing.T) {
	cwd := filepath.Join(t.TempDir(), "work", "proj")

	t.Run("not in use → nil", func(t *testing.T) {
		geminiTestHome(t, false, false)
		if c := geminiHooksCheck(Session{}, false, cwd); c != nil {
			t.Fatalf("expected nil for an unwired machine, got %+v", c)
		}
	})

	t.Run("user kill switch → warn naming key and file", func(t *testing.T) {
		userPath, _ := geminiTestHome(t, true, false)
		setJSONKey(t, userPath, func(doc map[string]any) {
			doc["hooksConfig"] = map[string]any{"enabled": false}
		})
		c := geminiHooksCheck(Session{}, false, cwd)
		if c == nil || c.Status != checkWarn {
			t.Fatalf("expected warn, got %+v", c)
		}
		if !strings.Contains(c.Detail, "hooksConfig.enabled = false") || !strings.Contains(c.Detail, userPath) {
			t.Fatalf("detail must name the key and file: %q", c.Detail)
		}
		if !strings.Contains(c.Hint, "--managed --server <server-url> gemini") {
			t.Fatalf("hint must name the managed pin: %q", c.Hint)
		}
	})

	t.Run("managed pin defeats user kill switch", func(t *testing.T) {
		userPath, _ := geminiTestHome(t, true, true)
		setJSONKey(t, userPath, func(doc map[string]any) {
			doc["hooksConfig"] = map[string]any{"enabled": false}
		})
		c := geminiHooksCheck(Session{}, false, cwd)
		if c == nil || c.Status != checkOK {
			t.Fatalf("system-scope pin overrides the user opt-out (vendor precedence): %+v", c)
		}
	})

	t.Run("env redirect abandons managed wiring → warn naming both paths", func(t *testing.T) {
		_, managedPath := geminiTestHome(t, false, true)
		redirect := filepath.Join(t.TempDir(), "elsewhere.json")
		t.Setenv("GEMINI_CLI_SYSTEM_SETTINGS_PATH", redirect)
		c := geminiHooksCheck(Session{}, false, cwd)
		if c == nil || c.Status != checkWarn {
			t.Fatalf("expected warn, got %+v", c)
		}
		if !strings.Contains(c.Detail, redirect) || !strings.Contains(c.Detail, managedPath) {
			t.Fatalf("detail must name both paths: %q", c.Detail)
		}
	})

	t.Run("untrusted folder → warn, safe mode named", func(t *testing.T) {
		userPath, _ := geminiTestHome(t, true, false)
		store := filepath.Join(filepath.Dir(userPath), "trustedFolders.json")
		setJSONKey(t, store, func(doc map[string]any) { doc[cwd] = "DO_NOT_TRUST" })
		c := geminiHooksCheck(Session{}, false, cwd)
		if c == nil || c.Status != checkWarn || !strings.Contains(c.Detail, "UNTRUSTED") {
			t.Fatalf("expected untrusted warn, got %+v", c)
		}
	})

	t.Run("trust store exists, cwd unrecorded → warn (first run asks)", func(t *testing.T) {
		userPath, _ := geminiTestHome(t, true, false)
		store := filepath.Join(filepath.Dir(userPath), "trustedFolders.json")
		if err := os.WriteFile(store, []byte(`{"/other":"TRUST_FOLDER"}`), 0o600); err != nil {
			t.Fatal(err)
		}
		c := geminiHooksCheck(Session{}, false, cwd)
		if c == nil || c.Status != checkWarn || !strings.Contains(c.Detail, "no trust record") {
			t.Fatalf("expected no-record warn, got %+v", c)
		}
	})

	t.Run("cwd trusted via ancestor TRUST_FOLDER → ok", func(t *testing.T) {
		userPath, _ := geminiTestHome(t, true, false)
		store := filepath.Join(filepath.Dir(userPath), "trustedFolders.json")
		setJSONKey(t, store, func(doc map[string]any) { doc[filepath.Dir(cwd)] = "TRUST_FOLDER" })
		c := geminiHooksCheck(Session{}, false, cwd)
		if c == nil || c.Status != checkOK || !strings.Contains(c.Detail, "trusted") {
			t.Fatalf("expected trusted ok, got %+v", c)
		}
	})

	t.Run("vendor-relocated trust store is honored", func(t *testing.T) {
		geminiTestHome(t, true, false)
		relocated := filepath.Join(t.TempDir(), "relocated-trust.json")
		setJSONKey(t, relocated, func(doc map[string]any) { doc[filepath.Dir(cwd)] = "TRUST_FOLDER" })
		t.Setenv("GEMINI_CLI_TRUSTED_FOLDERS_PATH", relocated)
		// No store at the default path: only the relocated one can answer.
		c := geminiHooksCheck(Session{}, false, cwd)
		if c == nil || c.Status != checkOK || !strings.Contains(c.Detail, "trusted") {
			t.Fatalf("GEMINI_CLI_TRUSTED_FOLDERS_PATH must be read where gemini reads it, got %+v", c)
		}
	})

	t.Run("headless trust bypass mutes the folder warn", func(t *testing.T) {
		userPath, _ := geminiTestHome(t, true, false)
		store := filepath.Join(filepath.Dir(userPath), "trustedFolders.json")
		if err := os.WriteFile(store, []byte(`{"/other":"TRUST_FOLDER"}`), 0o600); err != nil {
			t.Fatal(err)
		}
		t.Setenv("GEMINI_CLI_TRUST_WORKSPACE", "true")
		c := geminiHooksCheck(Session{}, false, cwd)
		if c == nil || c.Status != checkOK {
			t.Fatalf("bypass active: trust checks are skipped, got %+v", c)
		}
	})

	t.Run("gemini session checked in → ok on evidence", func(t *testing.T) {
		geminiTestHome(t, true, false)
		c := geminiHooksCheck(Session{Harness: "gemini/0.51.0"}, true, cwd)
		if c == nil || c.Status != checkOK || !strings.Contains(c.Detail, "checked in") {
			t.Fatalf("expected session-evidence ok, got %+v", c)
		}
	})

	t.Run("unreadable trust store answers unknown, not a verdict", func(t *testing.T) {
		userPath, _ := geminiTestHome(t, true, false)
		store := filepath.Join(filepath.Dir(userPath), "trustedFolders.json")
		if err := os.WriteFile(store, []byte(`{not json`), 0o600); err != nil {
			t.Fatal(err)
		}
		// Store exists ⇒ feature evidence; unreadable ⇒ unknown ⇒ the honest
		// no-record warn, never trusted, never untrusted.
		c := geminiHooksCheck(Session{}, false, cwd)
		if c == nil || c.Status != checkWarn || !strings.Contains(c.Detail, "no trust record") {
			t.Fatalf("unreadable store must degrade to unknown: %+v", c)
		}
	})
}

// TestGeminiManagedPinsHooksConfig pins the managed-lane hooksConfig.enabled
// write: managed gemini gets it, user gemini does not, other harnesses never
// do, operator keys beside it survive, and uninstall leaves the pin.
func TestGeminiManagedPinsHooksConfig(t *testing.T) {
	readDoc := func(t *testing.T, path string) map[string]any {
		t.Helper()
		raw, err := os.ReadFile(path) // #nosec G304 -- test file
		if err != nil {
			t.Fatal(err)
		}
		doc := map[string]any{}
		if err := json.Unmarshal(raw, &doc); err != nil {
			t.Fatal(err)
		}
		return doc
	}
	pin := func(doc map[string]any) (enabled, present bool) {
		hc, ok := doc["hooksConfig"].(map[string]any)
		if !ok {
			return false, false
		}
		v, ok := hc["enabled"].(bool)
		return v, ok
	}

	t.Run("managed gemini pins true, preserving operator keys", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "settings.json")
		if err := os.WriteFile(path, []byte(`{"hooksConfig":{"enabled":false,"timeout":9}}`), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := InstallManagedHooks("gemini", path, "/usr/local/bin/straza"); err != nil {
			t.Fatal(err)
		}
		doc := readDoc(t, path)
		if enabled, present := pin(doc); !present || !enabled {
			t.Fatalf("managed install must pin hooksConfig.enabled=true, got %v", doc["hooksConfig"])
		}
		if hc := doc["hooksConfig"].(map[string]any); hc["timeout"] != float64(9) {
			t.Fatalf("operator's other hooksConfig keys must survive: %v", hc)
		}
	})

	t.Run("user gemini does not write the key", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "settings.json")
		if err := InstallHooks("gemini", path, "/usr/local/bin/straza"); err != nil {
			t.Fatal(err)
		}
		if _, present := pin(readDoc(t, path)); present {
			t.Fatal("user-scope install must not touch hooksConfig")
		}
	})

	t.Run("managed claude-code does not write the key", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "settings.json")
		if err := InstallManagedHooks("claude-code", path, "/usr/local/bin/straza"); err != nil {
			t.Fatal(err)
		}
		if _, present := pin(readDoc(t, path)); present {
			t.Fatal("the pin is a gemini key; claude-code must not get it")
		}
	})

	t.Run("uninstall leaves the pin (default-true residue, not ours to take)", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "settings.json")
		if err := InstallManagedHooks("gemini", path, "/usr/local/bin/straza"); err != nil {
			t.Fatal(err)
		}
		if err := UninstallHooks("gemini", path, "/usr/local/bin/straza"); err != nil {
			t.Fatal(err)
		}
		if enabled, present := pin(readDoc(t, path)); !present || !enabled {
			t.Fatal("uninstall must leave hooksConfig.enabled=true in place")
		}
	})
}
