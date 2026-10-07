package agentguard

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Codex MANAGED hook wiring, the only half of the codex lane that can be
// called fail-closed: hooks from an administrator's requirements.toml are
// marked managed, TRUSTED BY POLICY, and cannot be switched off in the user
// hook browser the way user-scope hooks can (installcodex.go).
//
//	/etc/codex/requirements.toml                     Unix, INCLUDING macOS
//	%ProgramData%\OpenAI\Codex\requirements.toml     Windows
//
// [features] hooks = true survives a user's hooks = false. The event arrays
// ([[hooks.PreToolUse]] and friends) sit directly under [hooks] because the
// parser flattens them into the same struct as managed_dir. requirements.toml
// hook support lands in codex 0.124, the pinned minimum. Deliberately not
// written: allow_managed_hooks_only, which makes codex ignore every user,
// project, session and plugin hook on the machine. That lockdown is an
// administrator's call, so install only prints the line to add.

const (
	// codexReqBeginPrefix is what block detection matches; the parenthetical on
	// codexReqBegin can be reworded later without orphaning existing blocks.
	codexReqBeginPrefix = "# BEGIN straza-managed"
	codexReqBegin       = codexReqBeginPrefix + " (straza install --managed writes this block; do not edit inside)"
	codexReqEnd         = "# END straza-managed"
)

// CodexManagedRequirementsPath returns the requirements.toml codex reads
// administrator-supplied (auto-trusted) hooks from. It is
// ManagedSettingsPath("codex"), including the $STRAZA_MANAGED_SETTINGS_DIR
// test seam, except that on Windows a relocated %ProgramData% is honored,
// since the vendor path is defined in terms of that variable.
func CodexManagedRequirementsPath() (string, error) {
	path, err := ManagedSettingsPath("codex")
	if err != nil || osName() != "windows" || os.Getenv("STRAZA_MANAGED_SETTINGS_DIR") != "" {
		return path, err
	}
	return filepath.Join(programData(), "OpenAI", "Codex", "requirements.toml"), nil
}

// InstallCodexManagedHooks writes straza's hook wiring into codex's
// requirements.toml as a marker-delimited managed block, reporting whether the
// file changed. binPath is the managed straza binary the hooks invoke; its
// directory is declared as the managed hook directory.
//
// managed_dir (and windows_managed_dir) is DISPLAY-ONLY: codex neither
// validates it nor resolves or confines hook commands against it, it only
// SHOWS an operator where its managed hooks came from.
//
// Markers rather than a TOML rewrite, for the reasons installmcptoml.go's
// header gives. This FAILS CLOSED rather than corrupting the file: TOML
// forbids defining a table twice, so appending ours beside an operator's own
// [features] or [hooks] table would make codex reject the whole file and lose
// every managed hook on the machine. Straza refuses, names the table, and
// prints what to merge by hand.
func InstallCodexManagedHooks(path, binPath string) (bool, error) {
	block, err := renderCodexRequirementsBlock(binPath)
	if err != nil {
		return false, err
	}
	shimChanged := false
	if osName() == "windows" {
		// The block's commands reference the shim, so the shim is written
		// FIRST: between the two writes the worst state is a shim nothing
		// invokes yet, never hooks invoking a shim that is not there.
		if shimChanged, err = writeCodexHookShim(binPath); err != nil {
			return false, err
		}
	}
	content, existed, err := readCodexConfig(path)
	if err != nil {
		return false, err
	}
	lines := strings.Split(content, "\n")
	begin, end, err := findCodexManagedRequirementsBlock(lines)
	if err != nil {
		return false, err
	}
	if begin < 0 {
		if table := codexForeignRequirementsTable(lines); table != "" {
			return false, fmt.Errorf("%s already defines [%s]: straza will not append a second one (TOML forbids it, and codex would then reject the whole file and lose every managed hook). Merge this into the existing tables by hand:\n\n%s", path, table, strings.Join(block, "\n"))
		}
	}

	var out string
	if begin < 0 {
		out = appendCodexBlock(content, block)
	} else {
		if codexNewline(content) == "\r\n" {
			for i := range block {
				block[i] += "\r"
			}
		}
		merged := make([]string, 0, len(lines)-(end-begin)+len(block))
		merged = append(merged, lines[:begin]...)
		merged = append(merged, block...)
		merged = append(merged, lines[end+1:]...)
		out = strings.Join(merged, "\n")
	}
	if existed && out == content {
		return shimChanged, nil
	}
	return true, writeCodexManagedFile(path, out)
}

// UninstallCodexManagedHooks removes straza's block from requirements.toml,
// markers and separator line included, and nothing else. The file itself
// survives even when the block was all it held: an empty requirements.toml
// still expresses "this fleet is managed" to the administrator who deployed
// it, and deleting an /etc file straza did not create is not ours to do.
func UninstallCodexManagedHooks(path string) (bool, error) {
	content, existed, err := readCodexConfig(path)
	if err != nil || !existed {
		return false, err
	}
	lines := strings.Split(content, "\n")
	begin, end, err := findCodexManagedRequirementsBlock(lines)
	if err != nil {
		return false, err
	}
	if begin < 0 {
		return false, nil
	}
	if begin > 0 && strings.TrimSpace(lines[begin-1]) == "" {
		begin-- // the separator line install owns
	}
	kept := make([]string, 0, len(lines))
	kept = append(kept, lines[:begin]...)
	kept = append(kept, lines[end+1:]...)
	return true, writeCodexManagedFile(path, strings.Join(kept, "\n"))
}

// renderCodexRequirementsBlock builds the managed block: the features pin, the
// managed hook directory, and one array-of-tables pair per registered event.
// The event roster is installs["codex"].allEvents(), the same list the user
// lane walks, so the two lanes cannot drift apart.
//
// The per-event shape is a working form captured from a live box: `matcher =
// "*"` plus a command whose exe is UNQUOTED, since a quoted exe is what codex's
// re-tokenizer kills at spawn (exit 1, every hook dead while governance still
// looks wired). See codexHookCommand (installcodex.go) for the full quoting
// rule, and note the deliberate divergence: the user lane omits matcher.
//
// On WINDOWS the command is the single-token shim path, not the direct spaced
// form: the direct form spawns under a cmd-shaped hook shell and dies under a
// PowerShell one, and codex derives that shell from the detected user shell
// (installcodexshim.go).
func renderCodexRequirementsBlock(binPath string) ([]string, error) {
	return renderCodexRequirementsBlockFor(osName(), binPath)
}

// renderCodexRequirementsBlockFor renders for an explicit GOOS so the server
// can publish another platform's artifact (RenderManagedArtifacts) without
// touching the process-global osName seam.
func renderCodexRequirementsBlockFor(goos, binPath string) ([]string, error) {
	if strings.ContainsAny(binPath, " \t") {
		// FAIL CLOSED: on codex >= 0.146 a spaced path has no working string
		// spelling: the quoted exe dies at spawn and the docs' array command
		// form is rejected by the requirements layer ("invalid type:
		// sequence, expected a string", proven live). The
		// Windows shim inherits this refusal rather than lifting it: a shim
		// beside a spaced binary has a spaced path of its own, which a
		// PowerShell hook shell quotes into the same dead spawn. Writing
		// either would wire hooks that can never run.
		return nil, fmt.Errorf("cannot write codex managed hooks: %q contains whitespace, and codex >= 0.146 cannot spawn a quoted or spaced requirements command (there is no working spelling, and a hook shim beside it would carry the same spaces). Install the managed binary under a space-free directory and re-run", binPath)
	}
	cmd, _ := codexHookCommand(binPath) // never warns: whitespace was refused above
	if goos == "windows" {
		// On Windows the command is the SINGLE-TOKEN shim path, not the
		// spaced direct form: codex hands hook commands to the session's
		// detected user shell, and PowerShell (the Windows default) kills
		// a multi-token spawn that only cmd-shaped shells get vendor quoting
		// protection for (installcodexshim.go has the full finding; the
		// shim itself is written by InstallCodexManagedHooks).
		cmd = codexShimPathFor(goos, binPath)
	}
	command, err := codexMCPCommandValue(cmd)
	if err != nil {
		return nil, err
	}
	dir, err := codexMCPCommandValue(dirFor(goos, binPath))
	if err != nil {
		return nil, err
	}
	out := []string{
		codexReqBegin,
		"[features]",
		// Pinned so a user's own `hooks = false` cannot switch the fleet's
		// governance off; the vendor documents this as the way to enforce
		// managed hooks against a local opt-out.
		"hooks = true",
		"",
		"[hooks]",
		"managed_dir = " + dir,
	}
	if goos == "windows" {
		// On Windows the vendor reads windows_managed_dir; write both, same
		// directory, so the file reads correctly whichever key is shown
		// (display-only either way; see InstallCodexManagedHooks).
		out = append(out, "windows_managed_dir = "+dir)
	}
	for _, event := range installs["codex"].allEvents() {
		out = append(out,
			"",
			"[[hooks."+event+"]]",
			`matcher = "*"`,
			"[[hooks."+event+".hooks]]",
			"type = \"command\"",
			"command = "+command,
		)
		if event == "SessionEnd" {
			out = append(out, "timeout = 3") // codex defaults SessionEnd to 1s, max 3
		}
	}
	return append(out, codexReqEnd), nil
}

// CodexManagedLockdownLine is the one-line lockdown an administrator adds to
// requirements.toml themselves: it makes codex ignore every user, project,
// session and plugin hook and honor only managed ones. Straza prints it and
// never writes it: silently disabling hooks the user installed is a policy
// decision, not an installer's business.
const CodexManagedLockdownLine = "allow_managed_hooks_only = true"

// findCodexManagedRequirementsBlock returns the line range of straza's block,
// or (-1, -1) when there is none. An unterminated block is an error rather
// than a reason to write a second one (same contract as the MCP block).
func findCodexManagedRequirementsBlock(lines []string) (int, int, error) {
	begin := -1
	for i, raw := range lines {
		line := strings.TrimSpace(raw)
		switch {
		case begin < 0 && strings.HasPrefix(line, codexReqBeginPrefix):
			begin = i
		case begin >= 0 && strings.HasPrefix(line, codexReqEnd):
			return begin, i, nil
		}
	}
	if begin >= 0 {
		return -1, -1, fmt.Errorf("the codex requirements file has a %q line with no %q line after it. Repair or delete that block by hand; refusing to write a second one", codexReqBeginPrefix, codexReqEnd)
	}
	return -1, -1, nil
}

// codexForeignRequirementsTable returns the name of a [features] or [hooks]
// table the operator defined outside straza's block, or "" when there is none.
// Either one collides with what the block defines, and a duplicate table makes
// codex reject the entire requirements file.
func codexForeignRequirementsTable(lines []string) string {
	for _, raw := range lines {
		line := strings.TrimSpace(raw)
		if !strings.HasPrefix(line, "[") {
			continue
		}
		path, ok := codexTableHeader(line)
		if !ok || len(path) == 0 {
			continue
		}
		if path[0] == "features" || path[0] == "hooks" {
			return strings.Join(path, ".")
		}
	}
	return ""
}

// writeCodexManagedFile writes a root-owned managed file: 0644 when fresh (the
// managed layout is deliberately world-readable: every user's codex must read
// it, and only root may write it), preserving an existing file's mode.
func writeCodexManagedFile(path, content string) error {
	mode := os.FileMode(0o644)
	if fi, err := os.Stat(path); err == nil {
		mode = fi.Mode().Perm()
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil { // #nosec G301 -- every user's harness must read the managed layout
		return fmt.Errorf("create codex managed dir (need sudo/admin?): %w", err)
	}
	return os.WriteFile(path, []byte(content), mode) // #nosec G306 -- managed file is deliberately world-readable
}
