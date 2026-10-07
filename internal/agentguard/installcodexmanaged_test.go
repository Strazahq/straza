package agentguard

import (
	"os"
	"path"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// codexRequirementsWant renders the managed block EXACTLY as it must appear in
// requirements.toml. Spelled out, not generated (the codexWant pattern): this
// is the contract with codex's requirements parser and with every block already
// deployed to a fleet, so a change to it has to break a test.
//
// The shape is a working form byte-captured (fc.exe) on Windows against
// codex-cli 0.146.0: per event, `matcher = "*"` then a
// [[hooks.<Event>.hooks]] entry whose command is a TOML literal with an
// UNQUOTED exe: 0.146's requirements re-tokenizer rejects a double-quoted exe
// head (exit 1 at spawn), and the docs' array command form is rejected by the
// requirements layer ("invalid type: sequence, expected a string"). The
// command spelling is identical on every OS. Still no
// allow_managed_hooks_only: that lockdown disables the user's own hooks and
// is the administrator's call, not ours.
func codexRequirementsWant(binPath string) string {
	dir := "'" + filepath.Dir(binPath) + "'"
	if osName() != "windows" {
		dir = "'" + path.Dir(binPath) + "'"
	}
	command := "'" + binPath + " hook --harness codex'"
	out := "# BEGIN straza-managed (straza install --managed writes this block; do not edit inside)\n" +
		"[features]\nhooks = true\n\n" +
		"[hooks]\nmanaged_dir = " + dir + "\n"
	if osName() == "windows" {
		out += "windows_managed_dir = " + dir + "\n"
		// On Windows the command is the single-token shim, not the direct
		// form (installcodexshim.go; the seam-driven branch tests pin this
		// from Linux too; this keeps the golden honest on a real host).
		command = "'" + codexHookShimPath(binPath) + "'"
	}
	for _, event := range []string{"SessionStart", "PreToolUse", "UserPromptSubmit", "Stop", "SessionEnd", "SubagentStart", "SubagentStop"} {
		out += "\n[[hooks." + event + "]]\nmatcher = \"*\"\n[[hooks." + event + ".hooks]]\n" +
			"type = \"command\"\ncommand = " + command + "\n"
		if event == "SessionEnd" {
			out += "timeout = 3\n"
		}
	}
	return out + "# END straza-managed\n"
}

const testManagedStraza = "/usr/local/bin/straza"

// TestInstallCodexManagedHooksExactBytes pins the managed file byte for byte,
// including that Go writes it BOM-less, which the codex requirements parser
// demands (verified live).
func TestInstallCodexManagedHooksExactBytes(t *testing.T) {
	setOSName(t, "linux")
	path := filepath.Join(t.TempDir(), "requirements.toml")
	changed, err := InstallCodexManagedHooks(path, testManagedStraza)
	if err != nil || !changed {
		t.Fatalf("InstallCodexManagedHooks = (%v, %v), want (true, nil)", changed, err)
	}
	got := readFileString(t, path)
	if strings.HasPrefix(got, "\xef\xbb\xbf") {
		t.Error("requirements.toml starts with a UTF-8 BOM; codex rejects that")
	}
	if got != codexRequirementsWant(testManagedStraza) {
		t.Errorf("requirements.toml =\n%s\nwant\n%s", got, codexRequirementsWant(testManagedStraza))
	}
}

// TestCodexManagedCommandWindowsByteForm pins the direct command form for a
// Windows layout, the bytes of a working hand-edit (codex-cli 0.146.0). The
// managed BLOCK does not carry this string on Windows (it invokes the
// single-token shim; TestCodexManagedWindowsBranchLines), but the form
// matters twice over: it is the line INSIDE the shim, and the command every
// non-Windows block carries. Pure string work, so it runs on every OS.
func TestCodexManagedCommandWindowsByteForm(t *testing.T) {
	cmd, warning := codexHookCommand(`C:\ProgramData\straza\bin\straza.exe`)
	if warning != "" {
		t.Fatalf("space-free managed path produced a warning: %q", warning)
	}
	if want := `C:\ProgramData\straza\bin\straza.exe hook --harness codex`; cmd != want {
		t.Errorf("codexHookCommand = %q, want %q", cmd, want)
	}
	val, err := codexMCPCommandValue(cmd)
	if err != nil {
		t.Fatal(err)
	}
	if want := `'C:\ProgramData\straza\bin\straza.exe hook --harness codex'`; val != want {
		t.Errorf("TOML command value = %q, want the form %q", val, want)
	}
}

// TestCodexManagedMatcherPresent states the matcher fact so it cannot be lost
// in a diff of the big literal: every event table ships `matcher = "*"`,
// byte-matching a working file that spawned on codex 0.146. The user lane
// omits the key, and installcodex.go says why.
func TestCodexManagedMatcherPresent(t *testing.T) {
	setOSName(t, "linux")
	path := filepath.Join(t.TempDir(), "requirements.toml")
	if _, err := InstallCodexManagedHooks(path, testManagedStraza); err != nil {
		t.Fatal(err)
	}
	if got := strings.Count(readFileString(t, path), "matcher = \"*\""); got != 7 {
		t.Errorf("matcher = \"*\" appears %d times, want 7 (one per event)", got)
	}
}

// TestCodexManagedRefusesWhitespacePath is the fail-closed case for a spaced
// managed path: on codex >= 0.146 there is NO working string form (quoted exe
// dies at spawn, array form is rejected), so writing anything would wire a
// hook that can never run. Straza refuses with the remediation instead.
func TestCodexManagedRefusesWhitespacePath(t *testing.T) {
	path := filepath.Join(t.TempDir(), "requirements.toml")
	changed, err := InstallCodexManagedHooks(path, "/opt/straza bin/straza")
	if err == nil {
		t.Fatal("want a refusal for a whitespace path, got success")
	}
	if changed {
		t.Error("refused, yet reported a change")
	}
	if !strings.Contains(err.Error(), "space-free") {
		t.Errorf("the error must name the remediation (space-free path): %v", err)
	}
	if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
		t.Error("a refused install must not create the file")
	}
}

// TestCodexManagedConvergesOldQuotedForm: a block written by an older
// installer (quoted exe, no matcher: the form codex 0.146 kills at spawn)
// must read as stale and be rewritten to the working form, markers
// preserved, exactly once.
func TestCodexManagedConvergesOldQuotedForm(t *testing.T) {
	setOSName(t, "linux")
	path := filepath.Join(t.TempDir(), "requirements.toml")
	old := codexReqBegin + "\n" +
		"[features]\nhooks = true\n\n" +
		"[hooks]\nmanaged_dir = '/usr/local/bin'\n\n" +
		"[[hooks.SessionStart]]\n[[hooks.SessionStart.hooks]]\n" +
		"type = \"command\"\ncommand = '\"/usr/local/bin/straza\" hook --harness codex'\n" +
		codexReqEnd + "\n"
	if err := os.WriteFile(path, []byte(old), 0o644); err != nil {
		t.Fatal(err)
	}
	changed, err := InstallCodexManagedHooks(path, testManagedStraza)
	if err != nil || !changed {
		t.Fatalf("InstallCodexManagedHooks over the old form = (%v, %v), want (true, nil)", changed, err)
	}
	body := readFileString(t, path)
	if body != codexRequirementsWant(testManagedStraza) {
		t.Errorf("old quoted-form block did not converge:\n%s\nwant\n%s", body, codexRequirementsWant(testManagedStraza))
	}
	if strings.Count(body, codexReqBeginPrefix) != 1 {
		t.Errorf("convergence produced a second block:\n%s", body)
	}
}

// TestCodexManagedWindowsBranchLines exercises the windows-only render branch
// through the osName seam: the block gains windows_managed_dir (same dir,
// display-only either way), and every command is now the
// single-token shim path, with the shim written beside the binary. A
// unix-shaped path in a writable temp dir keeps filepath.Dir and the shim
// write honest on the CI host; the windows path bytes are pinned above.
func TestCodexManagedWindowsBranchLines(t *testing.T) {
	setOSName(t, "windows")
	bin := filepath.Join(t.TempDir(), "straza")
	path := filepath.Join(t.TempDir(), "requirements.toml")
	if _, err := InstallCodexManagedHooks(path, bin); err != nil {
		t.Fatal(err)
	}
	body := readFileString(t, path)
	dir := filepath.Dir(bin)
	if !strings.Contains(body, "windows_managed_dir = '"+dir+"'") {
		t.Errorf("windows render must carry windows_managed_dir:\n%s", body)
	}
	shim := codexHookShimPath(bin)
	if want := "command = '" + shim + "'"; !strings.Contains(body, want) {
		t.Errorf("windows block must invoke the single-token shim (%s):\n%s", want, body)
	}
	if strings.Contains(body, "hook --harness codex'") {
		t.Errorf("windows block must not carry the direct spaced form (PowerShell hook shells kill it at spawn):\n%s", body)
	}
	got, err := os.ReadFile(shim)
	if err != nil {
		t.Fatalf("install did not write the shim: %v", err)
	}
	if string(got) != codexHookShimContent(bin) {
		t.Errorf("shim bytes =\n%q\nwant\n%q", got, codexHookShimContent(bin))
	}
}

// TestCodexManagedHooksNoLockdown: straza pins the features flag (so a user
// cannot switch managed governance off) but never writes the line that ignores
// the user's OWN hooks. That is policy, and it belongs to the administrator.
func TestCodexManagedHooksNoLockdown(t *testing.T) {
	setOSName(t, "linux")
	path := filepath.Join(t.TempDir(), "requirements.toml")
	if _, err := InstallCodexManagedHooks(path, testManagedStraza); err != nil {
		t.Fatal(err)
	}
	body := readFileString(t, path)
	if strings.Contains(body, CodexManagedLockdownLine) {
		t.Errorf("install wrote the lockdown line itself:\n%s", body)
	}
	if !strings.Contains(body, "hooks = true") {
		t.Errorf("the [features] pin is missing: a user's `hooks = false` would switch the fleet off:\n%s", body)
	}
}

// TestInstallCodexManagedHooksIdempotent: re-running is a no-op, and a moved
// managed binary rewrites the block in place rather than appending a second.
func TestInstallCodexManagedHooksIdempotent(t *testing.T) {
	setOSName(t, "linux")
	path := filepath.Join(t.TempDir(), "requirements.toml")
	if _, err := InstallCodexManagedHooks(path, testManagedStraza); err != nil {
		t.Fatal(err)
	}
	changed, err := InstallCodexManagedHooks(path, testManagedStraza)
	if err != nil || changed {
		t.Errorf("re-install = (%v, %v), want (false, nil)", changed, err)
	}
	if _, err := InstallCodexManagedHooks(path, "/opt/straza/bin/straza"); err != nil {
		t.Fatal(err)
	}
	body := readFileString(t, path)
	if strings.Count(body, codexReqBeginPrefix) != 1 {
		t.Errorf("a moved binary produced a second block:\n%s", body)
	}
	if strings.Contains(body, testManagedStraza) {
		t.Errorf("the old binary path survived the rewrite:\n%s", body)
	}
}

// TestInstallCodexManagedHooksPreservesOperatorContent: requirements.toml is
// the administrator's file. Everything outside our markers, comments included,
// survives verbatim.
func TestInstallCodexManagedHooksPreservesOperatorContent(t *testing.T) {
	setOSName(t, "linux")
	path := filepath.Join(t.TempDir(), "requirements.toml")
	pre := "# fleet policy, do not edit\n" + CodexManagedLockdownLine + "\n\n[sandbox]\nmode = \"workspace-write\"\n"
	if err := os.WriteFile(path, []byte(pre), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := InstallCodexManagedHooks(path, testManagedStraza); err != nil {
		t.Fatal(err)
	}
	body := readFileString(t, path)
	if !strings.HasPrefix(body, pre) {
		t.Errorf("the operator's content was not preserved verbatim:\n%s", body)
	}
	if !strings.Contains(body, "[[hooks.PreToolUse]]") {
		t.Errorf("our block was not appended:\n%s", body)
	}
}

// TestInstallCodexManagedHooksRefusesDuplicateTable is the fail-closed case.
// TOML forbids defining a table twice, so appending our [hooks] beside an
// operator's would make codex reject the WHOLE requirements file: every
// managed hook on the fleet gone, silently. Straza refuses and says what to do.
func TestInstallCodexManagedHooksRefusesDuplicateTable(t *testing.T) {
	setOSName(t, "linux")
	for _, table := range []string{"[hooks]", "[features]", "[hooks.PreToolUse]"} {
		t.Run(table, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "requirements.toml")
			pre := table + "\nfoo = 1\n"
			if err := os.WriteFile(path, []byte(pre), 0o644); err != nil {
				t.Fatal(err)
			}
			changed, err := InstallCodexManagedHooks(path, testManagedStraza)
			if err == nil {
				t.Fatal("want a refusal, got success")
			}
			if changed {
				t.Error("refused, yet reported a change")
			}
			if !strings.Contains(err.Error(), "[[hooks.PreToolUse]]") {
				t.Errorf("the error must print the block to merge by hand: %v", err)
			}
			if got := readFileString(t, path); got != pre {
				t.Errorf("the file was modified despite the refusal:\n%s", got)
			}
		})
	}
}

// TestUninstallCodexManagedHooks: our block goes, the operator's file stays.
func TestUninstallCodexManagedHooks(t *testing.T) {
	setOSName(t, "linux")
	path := filepath.Join(t.TempDir(), "requirements.toml")
	pre := "# fleet policy\n" + CodexManagedLockdownLine + "\n"
	if err := os.WriteFile(path, []byte(pre), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := InstallCodexManagedHooks(path, testManagedStraza); err != nil {
		t.Fatal(err)
	}
	changed, err := UninstallCodexManagedHooks(path)
	if err != nil || !changed {
		t.Fatalf("UninstallCodexManagedHooks = (%v, %v), want (true, nil)", changed, err)
	}
	if got := readFileString(t, path); got != pre {
		t.Errorf("uninstall did not restore the operator's file:\n%q\nwant\n%q", got, pre)
	}
	if changed, err := UninstallCodexManagedHooks(path); changed || err != nil {
		t.Errorf("second uninstall = (%v, %v), want (false, nil)", changed, err)
	}
}

// TestUninstallCodexManagedHooksAbsent: no file, no block, no error.
func TestUninstallCodexManagedHooksAbsent(t *testing.T) {
	changed, err := UninstallCodexManagedHooks(filepath.Join(t.TempDir(), "requirements.toml"))
	if changed || err != nil {
		t.Errorf("= (%v, %v), want (false, nil)", changed, err)
	}
}

// TestInstallCodexManagedHooksUnterminatedBlock: a half-deleted block is a
// human's problem, not a reason to write a second one.
func TestInstallCodexManagedHooksUnterminatedBlock(t *testing.T) {
	path := filepath.Join(t.TempDir(), "requirements.toml")
	if err := os.WriteFile(path, []byte(codexReqBegin+"\n[features]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := InstallCodexManagedHooks(path, testManagedStraza); err == nil {
		t.Error("want an error for an unterminated block")
	}
}

// TestCodexManagedPathIsRequirementsToml pins the vendor path per platform.
// macOS has NO /Library/Application Support path for codex; it uses /etc
// like every other Unix.
func TestCodexManagedPathIsRequirementsToml(t *testing.T) {
	t.Setenv("STRAZA_MANAGED_SETTINGS_DIR", "")
	got, err := CodexManagedRequirementsPath()
	if err != nil {
		t.Fatal(err)
	}
	want := "/etc/codex/requirements.toml"
	if runtime.GOOS == "windows" {
		want = filepath.Join(programData(), "OpenAI", "Codex", "requirements.toml")
	}
	if got != want {
		t.Errorf("CodexManagedRequirementsPath = %q, want %q", got, want)
	}
	if strings.Contains(got, "managed-settings.json") {
		t.Error("codex has no managed-settings.json on any platform")
	}
}

// setOSName swaps the osName seam for one test. Tests using it must not run
// in parallel (package-level seam).
func setOSName(t *testing.T, name string) {
	t.Helper()
	old := osName
	osName = func() string { return name }
	t.Cleanup(func() { osName = old })
}

// TestInstallManagedCodexWritesRequirements is the end-to-end managed lane: the
// codex branch must write TOML into requirements.toml (the JSON writer would
// produce a file codex cannot parse) and measure THAT file for attestation,
// because hashing a file no codex release reads would make the codex
// attestation row vacuous.
func TestInstallManagedCodexWritesRequirements(t *testing.T) {
	sysDir := t.TempDir()
	t.Setenv("STRAZA_SYSTEM", sysDir)
	t.Setenv("STRAZA_MANAGED_SETTINGS_DIR", filepath.Join(sysDir, "harness"))
	t.Setenv("STRAZA_MANAGED_BIN_DIR", filepath.Join(sysDir, "bin"))
	t.Setenv("STRAZA_HOME", t.TempDir())
	t.Setenv("CODEX_HOME", t.TempDir())

	var out strings.Builder
	err := InstallManaged(t.Context(), ManagedInstallOptions{Harnesses: []string{"codex"},
		ServerURL: snapshotKeyServer(t, map[string]string{"k1": "AAAA"})}, &out)
	if err != nil {
		t.Fatalf("InstallManaged: %v", err)
	}

	path, err := CodexManagedRequirementsPath()
	if err != nil {
		t.Fatal(err)
	}
	body := readFileString(t, path)
	if !strings.Contains(body, "[[hooks.PreToolUse]]") || !strings.Contains(body, "hooks = true") {
		t.Errorf("managed requirements file is not the TOML codex reads:\n%s", body)
	}
	if strings.Contains(body, `"hooks": {`) {
		t.Errorf("JSON was written into a TOML file:\n%s", body)
	}

	// The measured artifact must be the file codex actually loads.
	hash, err := fileSHA256(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := measureAttestation("codex").Hashes["hooks.codex"]; got != hash {
		t.Errorf("hooks.codex measures %q, not the requirements file straza wrote", got)
	}

	// And the operator is told the two things that make this lane different.
	printed := out.String()
	for _, want := range []string{"auto-trusts", CodexManagedLockdownLine, "0.124"} {
		if !strings.Contains(printed, want) {
			t.Errorf("install output does not mention %q:\n%s", want, printed)
		}
	}

	// Reversal.
	var undo strings.Builder
	if err := UninstallManaged([]string{"codex"}, "", &undo); err != nil {
		t.Fatalf("UninstallManaged: %v", err)
	}
	if body := readFileString(t, path); strings.Contains(body, "straza") {
		t.Errorf("managed uninstall left straza wiring behind:\n%s", body)
	}
}
