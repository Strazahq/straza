package agentguard

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
)

// Gemini hook HONESTY, read from outside gemini: what cannot be read answers
// UNKNOWN, never "governed". Three ways the whole hook lane dies while the
// wiring file still reads perfect:
//
//  1. FOLDER TRUST (gate: security.folderTrust.enabled, shipped disabled). An
//     untrusted cwd makes headless gemini refuse to start (exit 55), so no
//     governed session runs where gemini does not trust the folder. The store
//     is trustedFolders.json, {path: TRUST_FOLDER | TRUST_PARENT |
//     DO_NOT_TRUST}, bypassed by GEMINI_CLI_TRUST_WORKSPACE=true or --skip-trust.
//  2. KILL SWITCH: hooksConfig.enabled=false runs zero hooks (default true),
//     beside a granular disabled-hooks list. SYSTEM settings outrank user and
//     workspace and MERGE their registrations, so the managed install pins
//     hooksConfig.enabled=true there (installHooks).
//  3. REDIRECT: GEMINI_CLI_SYSTEM_SETTINGS_PATH aims gemini at another system
//     settings file, dropping that pin; we can only detect and name it.

// geminiSettingsDoc parses one gemini settings.json, tolerantly. A file that
// is absent or unparseable yields the zero value (nothing known).
type geminiSettingsDoc struct {
	// hooksEnabled is hooksConfig.enabled; hooksEnabledSet says the key was
	// present at all (absent means vendor default, which is true today).
	hooksEnabled    bool
	hooksEnabledSet bool
	// folderTrustOn is security.folderTrust.enabled, same set-flag pattern.
	folderTrustOn    bool
	folderTrustOnSet bool
}

func readGeminiSettings(path string) geminiSettingsDoc {
	var out geminiSettingsDoc
	raw, err := os.ReadFile(path) // #nosec G304 G703 -- reading the harness's own settings file, including the env-redirected system path: detecting that redirect is this file's job; doctor is read-only and never executes or writes what it reads
	if err != nil {
		return out
	}
	var doc map[string]any
	if json.Unmarshal(raw, &doc) != nil {
		return out
	}
	if hc, ok := doc["hooksConfig"].(map[string]any); ok {
		if v, ok := hc["enabled"].(bool); ok {
			out.hooksEnabled, out.hooksEnabledSet = v, true
		}
	}
	if sec, ok := doc["security"].(map[string]any); ok {
		if ft, ok := sec["folderTrust"].(map[string]any); ok {
			if v, ok := ft["enabled"].(bool); ok {
				out.folderTrustOn, out.folderTrustOnSet = v, true
			}
		}
	}
	return out
}

// geminiTrustVerdict is what trustedFolders.json says about one directory.
type geminiTrustVerdict int

const (
	geminiTrustUnknown geminiTrustVerdict = iota // no record / unreadable store
	geminiTrustTrusted
	geminiTrustUntrusted
)

// readGeminiFolderTrust reports the recorded trust for dir. The store is a
// flat {path: level} object (levels TRUST_FOLDER / TRUST_PARENT /
// DO_NOT_TRUST); a TRUST_* record on dir or any ancestor trusts dir, an
// explicit DO_NOT_TRUST on dir itself untrusts it. Anything unreadable or
// unrecognized is UNKNOWN: the reader never manufactures a verdict.
// The store shape is not merely doc-derived: planting each level into a real
// gemini's trustedFolders.json changed whether hooks fired (verified against
// gemini-cli 0.53.0 in the harness-matrix gemini-trust lane).
func readGeminiFolderTrust(storePath, dir string) geminiTrustVerdict {
	raw, err := os.ReadFile(storePath) // #nosec G304 -- reading the harness's own trust store
	if err != nil {
		return geminiTrustUnknown
	}
	var store map[string]string
	if json.Unmarshal(raw, &store) != nil {
		return geminiTrustUnknown
	}
	dir = filepath.Clean(dir)
	if lvl, ok := store[dir]; ok && strings.HasPrefix(lvl, "DO_NOT_TRUST") {
		return geminiTrustUntrusted
	}
	for probe := dir; ; {
		if lvl, ok := store[probe]; ok && strings.HasPrefix(lvl, "TRUST") {
			return geminiTrustTrusted
		}
		parent := filepath.Dir(probe)
		if parent == probe {
			return geminiTrustUnknown
		}
		probe = parent
	}
}

// geminiTrustStorePath is where gemini reads folder-trust records:
// $GEMINI_CLI_TRUSTED_FOLDERS_PATH when set (the vendor relocation, and the
// value is a FILE path, not a directory), else trustedFolders.json beside
// whatever SettingsPath("gemini") resolved to.
//
// Gemini has NO config-dir variable: the only settings-path overrides it reads
// are GEMINI_CLI_SYSTEM_SETTINGS_PATH, GEMINI_CLI_SYSTEM_DEFAULTS_PATH and
// GEMINI_CLI_TRUSTED_FOLDERS_PATH, so gemini's user scope moves with $HOME and
// nothing else. Straza's own user-scope seam is $STRAZA_GEMINI_CONFIG_DIR
// (install.go), deliberately not a vendor-looking name an operator could set
// expecting gemini to follow, which would aim installs at files gemini never
// opens.
func geminiTrustStorePath() (string, error) {
	if p := os.Getenv("GEMINI_CLI_TRUSTED_FOLDERS_PATH"); p != "" {
		return p, nil
	}
	settings, err := SettingsPath("gemini")
	if err != nil {
		return "", err
	}
	return filepath.Join(filepath.Dir(settings), "trustedFolders.json"), nil
}

// geminiHooksCheck reports gemini's hook lane. Like codexHooksCheck it is
// FORBIDDEN to claim more than the evidence: wiring-file presence is only the
// start, because all three holes above kill the lane while the file reads
// perfect. cwd is a parameter for testability; the doctor wrapper passes the
// process working directory.
func geminiHooksCheck(ses Session, haveSession bool, cwd string) *Check {
	layers := wiredLayers("gemini")
	if len(layers) == 0 {
		return nil // gemini is simply not in use on this machine
	}
	userPath, err := SettingsPath("gemini")
	if err != nil {
		return nil
	}
	managedPath, _ := managedHookPath("gemini")
	user := readGeminiSettings(userPath)

	// Hole 3: the env redirect. When set and pointing away from the managed
	// file while managed wiring exists, that wiring is abandoned this session.
	redirect := os.Getenv("GEMINI_CLI_SYSTEM_SETTINGS_PATH")
	systemPath := managedPath
	if redirect != "" {
		systemPath = redirect
	}
	system := readGeminiSettings(systemPath)
	if redirect != "" && managedPath != "" && filepath.Clean(redirect) != filepath.Clean(managedPath) {
		for _, l := range layers {
			if l.layer == "managed" {
				return &Check{"gemini-hooks", checkWarn,
					"GEMINI_CLI_SYSTEM_SETTINGS_PATH redirects gemini's system settings to " + redirect + ". The managed wiring at " + managedPath + " is ABANDONED for sessions with this environment",
					"unset the variable, or point it at the managed file. Gemini's own enterprise guidance is a wrapper script that pins it; nothing outside gemini can stop a user setting it (vendor limitation, documented)"}
			}
		}
	}

	// Hole 2: the kill switch, with gemini's own precedence applied: the
	// system scope overrides user, so a managed pin defeats a user opt-out.
	effectiveEnabled, effectiveSet := user.hooksEnabled, user.hooksEnabledSet
	killFile := userPath
	if system.hooksEnabledSet {
		effectiveEnabled, effectiveSet = system.hooksEnabled, true
		killFile = systemPath
	}
	if effectiveSet && !effectiveEnabled {
		return &Check{"gemini-hooks", checkWarn,
			"wiring written, but hooks are switched OFF (hooksConfig.enabled = false in " + killFile + "), so gemini runs none of them",
			"remove that setting. The managed lane (`sudo straza install --managed --server <server-url> gemini`) pins hooksConfig.enabled = true in the system file, which gemini's precedence puts above a user's opt-out"}
	}

	// Hole 1: folder-trust safe mode. Raised only on positive evidence the
	// feature is in play (the gate set true, or a trust store on disk):
	// vendor defaults have flipped across releases, so absence of both is
	// reported as nothing rather than guessed at.
	storePath, storeErr := geminiTrustStorePath()
	storeExists := false
	if storeErr == nil {
		if _, err := os.Stat(storePath); err == nil {
			storeExists = true
		}
	}
	featureActive := user.folderTrustOn || system.folderTrustOn || storeExists
	if os.Getenv("GEMINI_CLI_TRUST_WORKSPACE") == "true" {
		featureActive = false // headless bypass: trust checks skipped entirely
	}
	if featureActive && cwd != "" {
		switch readGeminiFolderTrust(storePath, cwd) {
		case geminiTrustUntrusted:
			return &Check{"gemini-hooks", checkWarn,
				cwd + " is recorded UNTRUSTED in " + storePath + ". No governed gemini session runs here: headless (`gemini -p`) refuses to start at all (exit 55, \"not running in a trusted directory\"), interactive drops to safe mode (live 2026-07-31, gemini-cli 0.53.0)",
				"open gemini in this folder and trust it in the dialog (or remove the DO_NOT_TRUST record). This is a vendor behavior straza cannot override"}
		case geminiTrustUnknown:
			return &Check{"gemini-hooks", checkWarn,
				"folder trust is active and " + cwd + " has no trust record. A headless gemini refuses to start here and an interactive one asks first, so until the folder is trusted this wiring governs nothing (live 2026-07-31, gemini-cli 0.53.0)",
				"open gemini in this folder once and trust it; decisions persist in " + storePath}
		}
	}

	// Positive evidence: a gemini session that checked in proves the
	// SessionStart hook ran at least once.
	if haveSession && strings.HasPrefix(ses.Harness, "gemini/") {
		return &Check{"gemini-hooks", checkOK,
			"wiring present and a gemini session checked in (" + ses.Harness + "), so gemini is running the hooks", ""}
	}
	detail := "wiring present; no kill switch, no settings redirect"
	if featureActive {
		detail += ", and this folder is trusted"
	}
	return &Check{"gemini-hooks", checkOK, detail, ""}
}
