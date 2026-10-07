package classifier

import (
	"fmt"
	gopath "path"
	"regexp"
	"strings"
)

// Interpreter families. We classify by family (not raw name) because eval-flag
// syntax is per-family: shells take `-c`, python `-c`, node `-e/--eval`, etc.
// This mirrors policy.DetectInterpreter's recognized set; kept local so the
// interpreter token INDEX (needed to find the eval flag) stays consistent with
// our own splitter rather than requiring a second, divergent split.
const (
	famShell  = "shell"
	famPython = "python"
	famNode   = "node"
	famDeno   = "deno"
	famBun    = "bun"
	famPerl   = "perl"
	famRuby   = "ruby"
	famPhp    = "php"
	famPwsh   = "pwsh"
)

var families = map[string]string{
	"sh": famShell, "bash": famShell, "zsh": famShell, "dash": famShell, "ksh": famShell,
	"python": famPython, "python2": famPython, "python3": famPython,
	"node": famNode, "nodejs": famNode,
	"deno": famDeno, "bun": famBun,
	"perl": famPerl, "ruby": famRuby, "php": famPhp,
	"pwsh": famPwsh, "powershell": famPwsh,
}

// evalFlagSet holds the exact eval flags for families whose flag is a plain
// token (compared lowercased, so perl's -E folds onto -e). Shells use bundled
// short-flag detection (isBundledC) and deno an `eval` subcommand instead.
var evalFlagSet = map[string]map[string]bool{
	famPython: {"-c": true},
	famNode:   {"-e": true, "--eval": true},
	famBun:    {"-e": true},
	famPerl:   {"-e": true},
	famRuby:   {"-e": true},
	famPhp:    {"-r": true},
	famPwsh:   {"-command": true, "-c": true},
}

var wrapperNames = map[string]bool{"env": true, "sudo": true, "nohup": true, "nice": true}

// interpFamily reports the family of an interpreter basename, "" if none.
// Version-suffixed pythons (python3.12) fold onto the python family.
func interpFamily(base string) string {
	if f, ok := families[base]; ok {
		return f
	}
	if versionedPython(base) {
		return famPython
	}
	return ""
}

// versionedPython accepts pythonN[.N...] without admitting impostors
// (python-config). Mirrors policy.versionedPython.
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

// baseName reduces a token to a lowercased, .exe-trimmed basename with
// backslashes folded to slashes, so `C:\Windows\System32\curl.exe` and
// `/usr/bin/curl` both read as "curl".
func baseName(w string) string {
	if w == "" {
		return ""
	}
	w = strings.ReplaceAll(w, "\\", "/")
	return strings.TrimSuffix(strings.ToLower(gopath.Base(w)), ".exe")
}

// leadBase returns the basename of a command's leading executable and its
// index in words, skipping VAR=val assignment prefixes and env/sudo/nohup/nice
// wrappers (plus the wrapper's own -flags), the same skip policy.DetectInterpreter
// uses, so `sudo bash -c …` still reads as bash.
func leadBase(words []string) (string, int) {
	afterWrapper := false
	for i, w := range words {
		if w == "" {
			continue
		}
		if j := strings.IndexByte(w, '='); j > 0 && !strings.ContainsAny(w[:j], "/\\") {
			continue // VAR=val
		}
		if afterWrapper && strings.HasPrefix(w, "-") {
			continue // wrapper's own flag
		}
		b := baseName(w)
		if wrapperNames[b] {
			afterWrapper = true
			continue
		}
		return b, i
	}
	return "", -1
}

// evalPayload extracts the inline program an interpreter runs, or ok=false when
// the interpreter is running a script file / REPL (no inline program => nothing
// to scan). Flags may be reordered (`python3 -B -c …`); we scan every token
// after the interpreter for the family's eval flag and take the next token.
func evalPayload(fam string, words []string, idx int) (string, bool) {
	switch fam {
	case famShell:
		for k := idx + 1; k+1 < len(words); k++ {
			// Shells accept bundled short flags (`bash -lc "…"`), so match any
			// single-dash letter bundle ending in c, not just a bare -c.
			if isBundledC(strings.ToLower(words[k])) {
				return words[k+1], true
			}
		}
	case famDeno:
		for k := idx + 1; k+1 < len(words); k++ {
			if strings.ToLower(words[k]) == "eval" { // `deno eval <code>` subcommand
				return words[k+1], true
			}
		}
	default:
		flags := evalFlagSet[fam]
		for k := idx + 1; k < len(words); k++ {
			lw := strings.ToLower(words[k])
			if fam == famNode {
				if v, ok := strings.CutPrefix(words[k], "--eval="); ok {
					return v, true // node --eval=<code>
				}
				if v, ok := strings.CutPrefix(words[k], "-e="); ok {
					return v, true
				}
			}
			if flags[lw] && k+1 < len(words) {
				return words[k+1], true
			}
		}
	}
	return "", false
}

// isBundledC reports a single-dash lowercase short-flag bundle ending in 'c'
// (-c, -lc, -xc). That trailing c is the shell command flag; the program is the
// next token.
func isBundledC(f string) bool {
	if len(f) < 2 || f[0] != '-' || f[1] == '-' {
		return false
	}
	body := f[1:]
	for _, c := range body {
		if c < 'a' || c > 'z' {
			return false
		}
	}
	return strings.HasSuffix(body, "c")
}

// hasEncodedFlag reports a PowerShell -EncodedCommand flag in any casing or
// unambiguous prefix. We require prefix "-en" (length ≥ 3) so it can't collide
// with -ExecutionPolicy (which needs "-ex"), and demand the token itself be a
// prefix of "-encodedcommand".
func hasEncodedFlag(words []string, idx int) bool {
	for k := idx + 1; k < len(words); k++ {
		l := strings.ToLower(words[k])
		if len(l) >= 3 && strings.HasPrefix(l, "-en") && strings.HasPrefix("-encodedcommand", l) {
			return true
		}
	}
	return false
}

// --- pipeline shapes (pipe-to-shell, decode-then-exec) -----------------------

// pipelineSignal detects a fetcher or a decoder whose output flows through a
// pipe into an interpreter. Only `|` connects stdout to stdin, so we segment on
// `|` within a single pipeline and break pipelines on ; && || & newline: a
// fetcher and an interpreter separated by ; are two commands, not a pipe.
func pipelineSignal(cmd string) (string, bool) {
	for _, stages := range splitPipelines(cmd) {
		kinds := make([]lead, 0, len(stages))
		raw := make([]string, 0, len(stages))
		for _, s := range stages {
			if strings.TrimSpace(s) == "" {
				continue
			}
			kinds = append(kinds, analyzeLead(s))
			raw = append(raw, strings.TrimSpace(s))
		}
		if len(kinds) < 2 {
			continue // no pipe => no cross-stage flow
		}
		fetchIdx, decIdx := -1, -1
		for i, k := range kinds {
			if fetchIdx == -1 && isFetcher(k.base) {
				fetchIdx = i
			}
			if decIdx == -1 && isDecoder(k) {
				decIdx = i
			}
		}
		if fetchIdx >= 0 && sinkAfter(kinds, fetchIdx) {
			return "pipe-to-shell: " + quoteFrag(strings.Join(raw, " | ")), true
		}
		if decIdx >= 0 && sinkAfter(kinds, decIdx) {
			return "encoded-exec: " + quoteFrag(strings.Join(raw, " | ")), true
		}
	}
	return "", false
}

// sinkAfter reports an interpreter/eval sink at a later stage than from.
func sinkAfter(kinds []lead, from int) bool {
	for j := from + 1; j < len(kinds); j++ {
		if isInterpSink(kinds[j].base) {
			return true
		}
	}
	return false
}

type lead struct {
	base  string
	words []string
}

func analyzeLead(raw string) lead {
	w := splitWords(raw)
	b, _ := leadBase(w)
	return lead{base: b, words: w}
}

func isFetcher(base string) bool {
	switch base {
	case "curl", "wget", "fetch", "invoke-webrequest", "iwr":
		return true
	}
	return false
}

// isInterpSink includes every interpreter family plus stdin-eval sinks that
// aren't standalone interpreters (iex, and eval/exec as pipeline stages).
func isInterpSink(base string) bool {
	if interpFamily(base) != "" {
		return true
	}
	switch base {
	case "iex", "eval", "exec", "fish", "lua":
		return true
	}
	return false
}

// isDecoder reports a stage that decodes an obfuscated payload back to bytes:
// base64 -d/-D/--decode, xxd -r, or openssl enc -d.
func isDecoder(k lead) bool {
	switch k.base {
	case "base64":
		return hasTok(k.words, "-d", "-D", "--decode")
	case "xxd":
		return hasTok(k.words, "-r")
	case "openssl":
		return hasTok(k.words, "enc") && hasTok(k.words, "-d")
	}
	return false
}

// --- inline-eval payload scanning --------------------------------------------

var b64BlobRe = regexp.MustCompile(`[A-Za-z0-9+/]{40,}={0,2}`)

// evalCallRe matches eval(/exec( at a token boundary so identifiers ending in
// those letters (retrieval(, …) don't trip it.
var evalCallRe = regexp.MustCompile(`(?i)(?:^|[^\w.])(eval|exec)\s*\(`)

var execAPIMarkers = []struct{ needle, display string }{
	{"shutil.rmtree", "shutil.rmtree"},
	{"os.system", "os.system"},
	{"subprocess.", "subprocess"},
	{"child_process", "child_process"},
	{"execsync", "execSync"},
	{"fs.rmsync", "fs.rmSync"},
	{"rimraf", "rimraf"},
	{"fileutils.rm_rf", "FileUtils.rm_rf"},
}

// scanPayloadSignals scans an extracted inline-eval program for destructive or
// exec constructs. Order is deterministic: token-structured checks (rm -rf,
// dd-to-device, mkfs/shred) first, then substring exec-API markers, then the
// base64-literal-then-execute shape.
func scanPayloadSignals(payload string) (string, bool) {
	words := splitWords(payload)
	if rmForce(words) {
		return "rm -rf: " + quoteFrag(payload), true
	}
	if deviceWrite(words) {
		return "dd-to-device: " + quoteFrag(payload), true
	}
	if m, ok := simpleDestructive(words); ok {
		return m + ": " + quoteFrag(payload), true
	}
	if m, ok := execAPI(payload); ok {
		return m + ": " + quoteFrag(payload), true
	}
	if base64LiteralExec(payload) {
		return "encoded-exec: " + quoteFrag(payload), true
	}
	return "", false
}

// rmForce reports an rm invocation carrying both recursive and force, in any
// form: -rf, -fr, -r -f, --recursive --force, bundles like -rfv. GNU rm accepts
// flags after operands, so we scan a command's whole token run (stopping at a
// shell separator) rather than only its leading flags.
func rmForce(words []string) bool {
	for i, w := range words {
		if baseName(w) != "rm" {
			continue
		}
		rec, force := false, false
		for _, f := range words[i+1:] {
			if isSepTok(f) {
				break
			}
			if !strings.HasPrefix(f, "-") {
				continue // operand; flags may still follow
			}
			lf := strings.ToLower(f)
			switch {
			case lf == "--recursive" || lf == "--recurse":
				rec = true
			case lf == "--force":
				force = true
			case strings.HasPrefix(lf, "--"):
				// other long flag
			default:
				for _, c := range lf[1:] {
					switch c {
					case 'r':
						rec = true
					case 'f':
						force = true
					}
				}
			}
			if rec && force {
				return true
			}
		}
	}
	return false
}

// deviceWrite reports dd writing to a device node (of=/dev/…), the disk-wipe
// shape. dd to a regular file is left to ordinary policy.
func deviceWrite(words []string) bool {
	hasDD := false
	for _, w := range words {
		if baseName(w) == "dd" {
			hasDD = true
			break
		}
	}
	if !hasDD {
		return false
	}
	for _, w := range words {
		if strings.HasPrefix(strings.ToLower(w), "of=/dev/") {
			return true
		}
	}
	return false
}

// simpleDestructive reports mkfs* (filesystem clobber) or shred (secure erase).
func simpleDestructive(words []string) (string, bool) {
	for _, w := range words {
		b := baseName(w)
		if strings.HasPrefix(b, "mkfs") {
			return "mkfs", true
		}
		if b == "shred" {
			return "shred", true
		}
	}
	return "", false
}

// execAPI reports a destructive/dynamic-exec library call in a payload.
func execAPI(payload string) (string, bool) {
	lp := strings.ToLower(payload)
	for _, m := range execAPIMarkers {
		if strings.Contains(lp, m.needle) {
			return m.display, true
		}
	}
	if sub := evalCallRe.FindStringSubmatch(payload); sub != nil {
		return strings.ToLower(sub[1]) + "(", true
	}
	return "", false
}

// base64LiteralExec reports an embedded ≥40-char base64 blob that is decoded
// and executed within one payload (blob + decode verb + exec sink), the
// payload-carried cousin of the decode-then-exec pipeline.
func base64LiteralExec(payload string) bool {
	if !b64BlobRe.MatchString(payload) {
		return false
	}
	lp := strings.ToLower(payload)
	decode := containsAny(lp, "base64 -d", "base64 --decode", "b64decode", "atob(", "xxd -r", "openssl enc")
	sink := containsAny(lp, "| sh", "|sh", "| bash", "|bash", "eval", "exec", "iex", "os.system", "subprocess", "child_process")
	return decode && sink
}

// --- shared lexers -----------------------------------------------------------

// splitWords splits a command into words honoring ”, "" and \ escapes, the
// same rules as policy.shellWords, so our tokenization matches the matcher's.
func splitWords(s string) []string {
	var words []string
	var cur strings.Builder
	inSingle, inDouble, escaped, started := false, false, false, false
	flush := func() {
		if started {
			words = append(words, cur.String())
			cur.Reset()
			started = false
		}
	}
	for _, r := range s {
		switch {
		case escaped:
			cur.WriteRune(r)
			escaped = false
			started = true
		case r == '\\' && !inSingle:
			escaped = true
			started = true
		case r == '\'' && !inDouble:
			inSingle = !inSingle
			started = true
		case r == '"' && !inSingle:
			inDouble = !inDouble
			started = true
		case (r == ' ' || r == '\t' || r == '\n' || r == '\r') && !inSingle && !inDouble:
			flush()
		default:
			cur.WriteRune(r)
			started = true
		}
	}
	flush()
	return words
}

// splitPipelines segments a command into pipelines, each a slice of `|`-joined
// stages. Quotes/escapes suppress operators (a `|` inside "…" is literal). We
// break pipelines on ; && || & and newlines so only true stdin/stdout flow is
// treated as a pipeline.
func splitPipelines(cmd string) [][]string {
	var pipelines [][]string
	var stages []string
	var cur strings.Builder
	inSingle, inDouble, escaped := false, false, false
	runes := []rune(cmd)
	flushStage := func() {
		stages = append(stages, strings.TrimSpace(cur.String()))
		cur.Reset()
	}
	flushPipeline := func() {
		flushStage()
		pipelines = append(pipelines, stages)
		stages = nil
	}
	for i := 0; i < len(runes); i++ {
		r := runes[i]
		switch {
		case escaped:
			cur.WriteRune(r)
			escaped = false
		case r == '\\' && !inSingle:
			cur.WriteRune(r)
			escaped = true
		case r == '\'' && !inDouble:
			inSingle = !inSingle
			cur.WriteRune(r)
		case r == '"' && !inSingle:
			inDouble = !inDouble
			cur.WriteRune(r)
		case inSingle || inDouble:
			cur.WriteRune(r)
		case r == '|':
			if i+1 < len(runes) && runes[i+1] == '|' {
				flushPipeline() // || : logical or, breaks the pipe
				i++
			} else {
				flushStage() // | : stage boundary within a pipeline
			}
		case r == '&':
			if i+1 < len(runes) && runes[i+1] == '&' {
				i++
			}
			flushPipeline()
		case r == ';' || r == '\n':
			flushPipeline()
		default:
			cur.WriteRune(r)
		}
	}
	flushPipeline()
	return pipelines
}

// --- small helpers -----------------------------------------------------------

// joinArgv reconstructs a command string from argv, quoting tokens with spaces
// so splitWords round-trips them. Only used when Event.Command is empty.
func joinArgv(argv []string) string {
	var b strings.Builder
	for i, a := range argv {
		if i > 0 {
			b.WriteByte(' ')
		}
		if a == "" || strings.ContainsAny(a, " \t\n") {
			b.WriteByte('"')
			b.WriteString(a)
			b.WriteByte('"')
		} else {
			b.WriteString(a)
		}
	}
	return b.String()
}

func isSepTok(tok string) bool {
	switch tok {
	case "|", "||", "&&", "&", ";":
		return true
	}
	return false
}

func hasTok(words []string, want ...string) bool {
	for _, w := range words {
		for _, t := range want {
			if w == t {
				return true
			}
		}
	}
	return false
}

func containsAny(s string, subs ...string) bool {
	for _, sub := range subs {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}

// quoteFrag renders an offending fragment for a reason string, bounded so a
// pathological command can't blow up the reason.
func quoteFrag(s string) string {
	const max = 100
	if len(s) > max {
		s = s[:max] + "…"
	}
	return fmt.Sprintf("%q", s)
}
