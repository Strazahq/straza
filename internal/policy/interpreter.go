package policy

import (
	gopath "path"
	"strings"
)

// interpreterNames is the recognized interpreter set (spec/hook-profile,
// `interpreter` attribute). Version-suffixed pythons (python3.12) are
// matched by prefix; everything else is exact on the basename.
var interpreterNames = map[string]bool{
	"sh": true, "bash": true, "zsh": true, "dash": true, "ksh": true, "fish": true,
	"python": true, "python2": true, "python3": true,
	"node": true, "nodejs": true, "deno": true, "bun": true,
	"ruby": true, "perl": true, "php": true,
	"pwsh": true, "powershell": true, "lua": true,
}

// wrapperNames are launchers skipped before the interpreter candidate.
// Known limitation (documented in SPEC.md): a wrapper option that consumes
// a separate argument (`sudo -u alice python3`) hides the interpreter;
// the tag is conservative and may under-report, never mis-report.
var wrapperNames = map[string]bool{"env": true, "sudo": true, "nohup": true, "nice": true}

// DetectInterpreter reports the interpreter a shell.exec event invokes
// ("python3", "bash", …) or "" for non-interpreter commands. It is the ONE
// implementation both PEPs (straza normalize, strazad /v1/decide) use,
// so client- and server-side tagging cannot drift. Argv wins when present;
// otherwise the command is split with the same shellWords the matcher uses.
// Leading VAR=val assignments and env/sudo/nohup/nice wrappers (plus their
// own -flags) are skipped; the candidate token is basename-normalized,
// .exe-trimmed, lowercased.
func DetectInterpreter(command string, argv []string) string {
	words := argv
	if len(words) == 0 {
		words = shellWords(command)
	}
	afterWrapper := false
	for _, w := range words {
		if w == "" {
			continue
		}
		if i := strings.IndexByte(w, '='); i > 0 && !strings.ContainsAny(w[:i], "/\\") {
			continue // VAR=val assignment prefix
		}
		if afterWrapper && strings.HasPrefix(w, "-") {
			continue // wrapper's own flag
		}
		base := strings.TrimSuffix(strings.ToLower(gopath.Base(normalize(w))), ".exe")
		if wrapperNames[base] {
			afterWrapper = true
			continue
		}
		if interpreterNames[base] || versionedPython(base) {
			return base
		}
		return ""
	}
	return ""
}

// versionedPython accepts pythonN[.N...] basenames (python3.12) without
// admitting impostors (python-config).
func versionedPython(base string) bool {
	rest, ok := strings.CutPrefix(base, "python")
	if !ok || rest == "" {
		return false
	}
	for _, r := range rest {
		if (r < '0' || r > '9') && r != '.' {
			return false
		}
	}
	return true
}
