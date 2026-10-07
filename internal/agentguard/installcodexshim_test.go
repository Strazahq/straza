package agentguard

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The Windows codex hook shim (installcodexshim.go): a single-token .cmd is
// the one command spelling every hook shell spawns. The direct spaced form
// dies at PreToolUse spawn under codex's PowerShell hook shell while the
// .cmd runs end to end.

// TestCodexShimContentExactBytes pins the shim byte for byte: three CRLF
// lines, no redirects. The no-redirect property is governance, not style:
// codex honors an exit-2 block only when stderr reaches it, so a redirect
// turns every deny into an allow (a diagnostic wrapper proved it live).
func TestCodexShimContentExactBytes(t *testing.T) {
	got := codexHookShimContent(`C:\ProgramData\straza\bin\straza.exe`)
	want := "@echo off\r\n" +
		`C:\ProgramData\straza\bin\straza.exe hook --harness codex` + "\r\n" +
		"exit /b %ERRORLEVEL%\r\n"
	if got != want {
		t.Errorf("shim content =\n%q\nwant\n%q", got, want)
	}
	for _, forbidden := range []string{">", "<", "|", "2>"} {
		if strings.Contains(got, forbidden) {
			t.Errorf("shim contains a redirect-ish token %q: stderr must reach codex or denies fail open", forbidden)
		}
	}
}

// TestWriteCodexHookShimIdempotent: writing twice reports (true, false); a
// drifted file is restored and reported changed.
func TestWriteCodexHookShimIdempotent(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "straza")
	changed, err := writeCodexHookShim(bin)
	if err != nil || !changed {
		t.Fatalf("first write = (%v, %v), want (true, nil)", changed, err)
	}
	changed, err = writeCodexHookShim(bin)
	if err != nil || changed {
		t.Errorf("second write = (%v, %v), want (false, nil)", changed, err)
	}
	if err := os.WriteFile(codexHookShimPath(bin), []byte("@echo off\r\nrem drifted\r\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	changed, err = writeCodexHookShim(bin)
	if err != nil || !changed {
		t.Errorf("restore over drift = (%v, %v), want (true, nil)", changed, err)
	}
	if body := readFileString(t, codexHookShimPath(bin)); body != codexHookShimContent(bin) {
		t.Errorf("drifted shim was not restored:\n%q", body)
	}
}

// TestCodexShimTarget: the reader recovers the binary from the shim's own
// bytes, and refuses a reshaped file rather than guessing.
func TestCodexShimTarget(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "straza")
	if _, err := writeCodexHookShim(bin); err != nil {
		t.Fatal(err)
	}
	target, ok := codexShimTarget(codexHookShimPath(bin))
	if !ok || target != bin {
		t.Errorf("codexShimTarget = (%q, %v), want (%q, true)", target, ok, bin)
	}
	mangled := filepath.Join(t.TempDir(), codexShimName)
	if err := os.WriteFile(mangled, []byte("@echo off\r\nsomething else entirely\r\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, ok := codexShimTarget(mangled); ok {
		t.Error("a shim with no hook line must not yield a target")
	}
	if _, ok := codexShimTarget(filepath.Join(t.TempDir(), "absent.cmd")); ok {
		t.Error("a missing shim must not yield a target")
	}
}

// TestInstallCodexManagedHooksWindowsShimConverge: a windows block carrying
// the direct spaced form (every pre-shim install) converges to the shim
// form on re-install, exactly once, and re-running after that is a no-op,
// including the shim file (a deleted shim alone makes re-install report a
// change again).
func TestInstallCodexManagedHooksWindowsShimConverge(t *testing.T) {
	setOSName(t, "windows")
	bin := filepath.Join(t.TempDir(), "straza")
	path := filepath.Join(t.TempDir(), "requirements.toml")
	old := codexReqBegin + "\n" +
		"[features]\nhooks = true\n\n" +
		"[hooks]\nmanaged_dir = '" + filepath.Dir(bin) + "'\n\n" +
		"[[hooks.PreToolUse]]\nmatcher = \"*\"\n[[hooks.PreToolUse.hooks]]\n" +
		"type = \"command\"\ncommand = '" + bin + " hook --harness codex'\n" +
		codexReqEnd + "\n"
	if err := os.WriteFile(path, []byte(old), 0o644); err != nil {
		t.Fatal(err)
	}
	changed, err := InstallCodexManagedHooks(path, bin)
	if err != nil || !changed {
		t.Fatalf("converge = (%v, %v), want (true, nil)", changed, err)
	}
	body := readFileString(t, path)
	if strings.Contains(body, "hook --harness codex'") {
		t.Errorf("direct form survived convergence:\n%s", body)
	}
	if !strings.Contains(body, "command = '"+codexHookShimPath(bin)+"'") {
		t.Errorf("converged block does not invoke the shim:\n%s", body)
	}
	changed, err = InstallCodexManagedHooks(path, bin)
	if err != nil || changed {
		t.Errorf("re-install = (%v, %v), want (false, nil)", changed, err)
	}
	if err := os.Remove(codexHookShimPath(bin)); err != nil {
		t.Fatal(err)
	}
	changed, err = InstallCodexManagedHooks(path, bin)
	if err != nil || !changed {
		t.Errorf("re-install with a deleted shim = (%v, %v), want (true, nil): the shim must come back", changed, err)
	}
}

// TestInstallCodexHooksWindowsUserShim: the user lane takes the shim too on a
// space-free Windows path (user-scope hooks ride the identical spawn path),
// and the shim-form entries are still recognized as ours (uninstall removes
// them; a spaced path keeps the quoted direct form).
func TestInstallCodexHooksWindowsUserShim(t *testing.T) {
	setOSName(t, "windows")
	bin := filepath.Join(t.TempDir(), "straza.exe")
	path := filepath.Join(t.TempDir(), "hooks.json")
	if _, err := InstallCodexHooks(path, bin); err != nil {
		t.Fatal(err)
	}
	body := readFileString(t, path)
	if !strings.Contains(body, codexShimName) {
		t.Errorf("user-lane entries must invoke the shim:\n%s", body)
	}
	if strings.Contains(body, "hook --harness codex") {
		t.Errorf("user-lane entries must not carry the direct spaced form on windows:\n%s", body)
	}
	if _, err := os.Stat(codexHookShimPath(bin)); err != nil {
		t.Errorf("user-lane install did not write the shim: %v", err)
	}
	wired, missing := CodexHooksWritten(path)
	if !wired || len(missing) != 0 {
		t.Errorf("CodexHooksWritten over shim entries = (%v, %v), want (true, none)", wired, missing)
	}
	changed, err := UninstallCodexHooks(path)
	if err != nil || !changed {
		t.Fatalf("uninstall over shim entries = (%v, %v), want (true, nil)", changed, err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Error("uninstall must remove a hooks.json that held only straza entries")
	}
}

// TestCodexShimWiringDetection: the doctor readers recognize the shim command
// as straza wiring, in the TOML managed file (event registration + command
// listing) and via the shared matcher, and hookCommandBinary resolves
// THROUGH the shim to the real binary, falling back to the shim path itself
// when the shim is missing (a ghost is a ghost).
func TestCodexShimWiringDetection(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "straza")
	if _, err := writeCodexHookShim(bin); err != nil {
		t.Fatal(err)
	}
	shim := codexHookShimPath(bin)
	req := filepath.Join(t.TempDir(), "requirements.toml")
	body := "[[hooks.PreToolUse]]\nmatcher = \"*\"\n[[hooks.PreToolUse.hooks]]\n" +
		"type = \"command\"\ncommand = '" + shim + "'\n"
	if err := os.WriteFile(req, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	regs, err := tomlHookRegistrations(req, "codex")
	if err != nil || len(regs) != 1 || regs[0].event != "PreToolUse" || regs[0].command != shim {
		t.Errorf("tomlHookRegistrations = (%v, %v), want the PreToolUse shim registration", regs, err)
	}
	if !strazaHookCommand(shim, "codex") {
		t.Error("strazaHookCommand must match the shim form")
	}
	if strazaHookCommand(shim, "claude-code") {
		t.Error("the shim is codex wiring, not claude-code's")
	}
	binary, exact := hookCommandBinary(shim, "codex")
	if !exact || binary != bin {
		t.Errorf("hookCommandBinary through the shim = (%q, %v), want (%q, true)", binary, exact, bin)
	}
	missingShim := filepath.Join(t.TempDir(), codexShimName)
	binary, exact = hookCommandBinary(missingShim, "codex")
	if !exact || binary != missingShim {
		t.Errorf("hookCommandBinary over a missing shim = (%q, %v), want the shim path itself (stat reports the ghost)", binary, exact)
	}
}

// TestCodexManagedShimCheck covers the doctor lens: healthy shim = nil (the
// managed-ok message stands), missing shim = FAIL, reshaped shim = FAIL,
// direct form = WARN recommending convergence, and non-windows = nil always.
func TestCodexManagedShimCheck(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "straza")
	shim := codexHookShimPath(bin)
	shimReq := filepath.Join(t.TempDir(), "shim-req.toml")
	writeReq := func(t *testing.T, path, command string) {
		t.Helper()
		body := "[[hooks.PreToolUse]]\nmatcher = \"*\"\n[[hooks.PreToolUse.hooks]]\n" +
			"type = \"command\"\ncommand = '" + command + "'\n"
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	writeReq(t, shimReq, shim)

	t.Run("non-windows is silent", func(t *testing.T) {
		setOSName(t, "linux")
		if c := codexManagedShimCheck(shimReq); c != nil {
			t.Errorf("non-windows check = %+v, want nil (the direct form is correct on unix)", c)
		}
	})

	setOSName(t, "windows")
	t.Run("missing shim fails", func(t *testing.T) {
		c := codexManagedShimCheck(shimReq)
		if c == nil || c.Status != checkFail || !strings.Contains(c.Detail, shim) {
			t.Errorf("missing shim = %+v, want a FAIL naming %s", c, shim)
		}
	})
	t.Run("healthy shim is silent", func(t *testing.T) {
		if _, err := writeCodexHookShim(bin); err != nil {
			t.Fatal(err)
		}
		if c := codexManagedShimCheck(shimReq); c != nil {
			t.Errorf("healthy shim = %+v, want nil", c)
		}
	})
	t.Run("reshaped shim fails", func(t *testing.T) {
		drifted := codexHookShimContent(bin) + "echo extra 2>nul\r\n"
		if err := os.WriteFile(shim, []byte(drifted), 0o755); err != nil {
			t.Fatal(err)
		}
		c := codexManagedShimCheck(shimReq)
		if c == nil || c.Status != checkFail {
			t.Errorf("reshaped shim = %+v, want a FAIL (a reshaped shim can redirect the deny's stderr away)", c)
		}
		if _, err := writeCodexHookShim(bin); err != nil { // restore for later subtests
			t.Fatal(err)
		}
	})
	t.Run("direct form warns", func(t *testing.T) {
		directReq := filepath.Join(t.TempDir(), "direct-req.toml")
		writeReq(t, directReq, bin+" hook --harness codex")
		c := codexManagedShimCheck(directReq)
		if c == nil || c.Status != checkWarn || !strings.Contains(c.Hint, codexShimName) {
			t.Errorf("direct form = %+v, want a WARN pointing at the shim convergence", c)
		}
	})
}

// TestCodexShimUserEntriesSurviveMixedFile: shim-form entries beside foreign
// hooks are removed precisely: the foreign hook and the rest of the file
// survive (the containsMarker extension must not over-match).
func TestCodexShimUserEntriesSurviveMixedFile(t *testing.T) {
	setOSName(t, "windows")
	bin := filepath.Join(t.TempDir(), "straza.exe")
	path := filepath.Join(t.TempDir(), "hooks.json")
	foreign := map[string]any{
		"hooks": map[string]any{
			"PreToolUse": []any{
				map[string]any{"hooks": []any{map[string]any{"type": "command", "command": "linter --fix"}}},
			},
		},
	}
	raw, err := json.Marshal(foreign)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := InstallCodexHooks(path, bin); err != nil {
		t.Fatal(err)
	}
	if _, err := UninstallCodexHooks(path); err != nil {
		t.Fatal(err)
	}
	body := readFileString(t, path)
	if !strings.Contains(body, "linter --fix") {
		t.Errorf("the foreign hook must survive uninstall:\n%s", body)
	}
	if strings.Contains(body, codexShimName) {
		t.Errorf("straza's shim entries must be gone after uninstall:\n%s", body)
	}
}
