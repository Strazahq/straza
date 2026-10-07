package agentguard

import (
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"strings"
)

// Codex MCP registration. Codex is the one Tier-1 harness whose MCP servers do
// NOT live in the JSON settings file the hooks are written to: it reads them
// from $CODEX_HOME/config.toml. This file owns that file, so install writes
// the registration, doctor proves it, and uninstall reverts it.
//
// A marker-delimited managed block, not a TOML parse-and-rewrite: config.toml
// is the user's own file (model, sandbox and approval policy, profiles, their
// other MCP servers, their comments and formatting), and no Go TOML library
// round-trips comments and layout, so decode plus re-encode would rewrite the
// whole document and cost a dependency besides. Straza owns the bytes between
// its own markers and treats every other byte as immutable, which keeps the
// write idempotent, reversible and legible in a diff. We never parse TOML: the
// only questions asked of the file are "is our block here" and "did the user
// already register straza themselves", both line-shape questions.
const (
	// codexMCPBeginPrefix is what block detection matches, so the parenthetical
	// on codexMCPBegin can be reworded later without orphaning the blocks older
	// installs wrote. codexMCPBegin is what install writes today.
	codexMCPBeginPrefix = "# BEGIN straza-managed"
	codexMCPBegin       = codexMCPBeginPrefix + " (straza install writes this block; do not edit inside)"
	codexMCPEnd         = "# END straza-managed"
	codexConfigFile     = "config.toml"
)

// CodexMCPState is what a codex config.toml says about the straza MCP server.
type CodexMCPState int

const (
	// CodexMCPMissing means there is no straza registration at all (or no file
	// yet).
	CodexMCPMissing CodexMCPState = iota
	// CodexMCPManaged means the straza-managed block is present: install
	// rewrites it, uninstall removes it, doctor reports the binary it names.
	CodexMCPManaged
	// CodexMCPUnmanaged means an mcp_servers.straza registration exists outside
	// our markers. It is the user's; straza reports it and never touches it.
	CodexMCPUnmanaged
)

// String renders the state for install output and doctor.
func (s CodexMCPState) String() string {
	switch s {
	case CodexMCPManaged:
		return "managed"
	case CodexMCPUnmanaged:
		return "unmanaged"
	default:
		return "missing"
	}
}

// CodexMCPConfigPath returns the config.toml holding codex's MCP server
// registrations. It sits beside the settings.json the hooks go into, so the
// same $CODEX_HOME the hook wiring honors moves both together.
func CodexMCPConfigPath() (string, error) {
	settings, err := SettingsPath("codex")
	if err != nil {
		return "", err
	}
	return filepath.Join(filepath.Dir(settings), codexConfigFile), nil
}

// CodexMCPConfigPathForInvoker is CodexMCPConfigPath for `install --managed`,
// which runs as root/administrator. codex has no system-scope MCP config
// (adapters/codex.yaml: the managed layer covers hooks only), so the
// registration necessarily lands in a user's config.toml, and root's own is
// the one file no operator wants written. Under sudo this resolves $SUDO_USER's
// home. $CODEX_HOME still wins (an operator who sets it means it), and a user
// the CGO-free os/user cannot resolve falls back to the current home rather
// than failing an otherwise-good install.
func CodexMCPConfigPathForInvoker() (string, error) {
	if os.Getenv(installs["codex"].envDir) != "" {
		return CodexMCPConfigPath()
	}
	home := invokingUserHome()
	if home == "" {
		return CodexMCPConfigPath()
	}
	rel := installs["codex"].relPath
	return filepath.Join(append(append([]string{home}, rel[:len(rel)-1]...), codexConfigFile)...), nil
}

// invokingUserHome returns the home directory of the user behind a sudo
// invocation, or "" when there is none to speak of: not under sudo, sudo from
// root itself, or a directory service the pure-Go os/user cannot read.
func invokingUserHome() string {
	name := os.Getenv("SUDO_USER")
	if name == "" || name == "root" {
		return ""
	}
	u, err := user.Lookup(name)
	if err != nil {
		return ""
	}
	return u.HomeDir
}

// InstallCodexMCPServer ensures codex's config.toml carries the straza MCP
// registration, returning the state the file is in afterwards and whether
// anything was written.
//
// An existing managed block is REWRITTEN IN PLACE, since the binary may have
// moved, with every byte around it preserved. Rewritten WHOLE, never merged
// into, so a key an operator hand-added inside the markers converges instead
// of accumulating a second spelling. The same holds for a key added just BELOW
// the block: the END marker is a comment, not a table boundary, so that line
// is still inside [mcp_servers.straza] and would become a duplicate key that
// costs the operator their whole config (stripCodexTableTailDuplicates).
// Absent, the block is appended after one blank separator line, creating the
// file (0600, like the hook settings) and its directory as needed. A
// registration the user wrote themselves is never touched, updated or
// duplicated: CodexMCPUnmanaged comes back for the caller to report.
func InstallCodexMCPServer(path, strazaPath string) (CodexMCPState, bool, error) {
	block, err := renderCodexMCPBlock(strazaPath)
	if err != nil {
		return CodexMCPMissing, false, err
	}
	content, existed, err := readCodexConfig(path)
	if err != nil {
		return CodexMCPMissing, false, err
	}
	lines := strings.Split(content, "\n")
	begin, end, err := findCodexManagedBlock(lines)
	if err != nil {
		return CodexMCPMissing, false, err
	}
	if begin < 0 && codexHasForeignRegistration(lines) {
		return CodexMCPUnmanaged, false, nil
	}

	var out string
	if begin < 0 {
		out = appendCodexBlock(content, block)
	} else {
		tail := stripCodexTableTailDuplicates(lines[end+1:], codexOwnedKeys(block))
		if codexNewline(content) == "\r\n" {
			for i := range block {
				block[i] += "\r" // splicing into CRLF lines keeps them CRLF
			}
		}
		merged := make([]string, 0, len(lines)-(end-begin)+len(block))
		merged = append(merged, lines[:begin]...)
		merged = append(merged, block...)
		merged = append(merged, tail...)
		out = strings.Join(merged, "\n")
	}
	if existed && out == content {
		return CodexMCPManaged, false, nil // already current: do not touch the file
	}
	return CodexMCPManaged, true, writeCodexConfig(path, out)
}

// UninstallCodexMCPServer removes the straza-managed block from codex's
// config.toml (markers included, plus the blank separator line install put in
// front of it) and nothing else. A registration the user wrote themselves
// survives (CodexMCPUnmanaged comes back so the caller can say so); a missing
// file or block is a no-op. The file itself is never deleted, even when the
// block was all it held: whether an empty config.toml stays is the user's call.
func UninstallCodexMCPServer(path string) (CodexMCPState, bool, error) {
	content, existed, err := readCodexConfig(path)
	if err != nil || !existed {
		return CodexMCPMissing, false, err
	}
	lines := strings.Split(content, "\n")
	begin, end, err := findCodexManagedBlock(lines)
	if err != nil {
		return CodexMCPMissing, false, err
	}
	if begin < 0 {
		if codexHasForeignRegistration(lines) {
			return CodexMCPUnmanaged, false, nil
		}
		return CodexMCPMissing, false, nil
	}
	if begin > 0 && strings.TrimSpace(lines[begin-1]) == "" {
		begin-- // the separator line install owns
	}
	kept := make([]string, 0, len(lines))
	kept = append(kept, lines[:begin]...)
	kept = append(kept, lines[end+1:]...)
	state := CodexMCPMissing
	if codexHasForeignRegistration(kept) {
		state = CodexMCPUnmanaged
	}
	return state, true, writeCodexConfig(path, strings.Join(kept, "\n"))
}

// ReadCodexMCPRegistration reports what a codex config.toml says about the
// straza MCP server: the state and, for a managed block, the binary path it
// registers; doctor prints that, because a block naming a binary that has
// since moved is exactly the silent failure this loop exists to surface. A
// missing file is CodexMCPMissing, not an error.
func ReadCodexMCPRegistration(path string) (CodexMCPState, string, error) {
	content, existed, err := readCodexConfig(path)
	if err != nil || !existed {
		return CodexMCPMissing, "", err
	}
	lines := strings.Split(content, "\n")
	begin, end, err := findCodexManagedBlock(lines)
	if err != nil {
		return CodexMCPMissing, "", err
	}
	if begin >= 0 {
		for _, raw := range lines[begin : end+1] {
			if key, value, ok := strings.Cut(strings.TrimSpace(raw), "="); ok && strings.TrimSpace(key) == "command" {
				return CodexMCPManaged, codexMCPCommandPath(value), nil
			}
		}
		return CodexMCPManaged, "", nil
	}
	if codexHasForeignRegistration(lines) {
		return CodexMCPUnmanaged, "", nil
	}
	return CodexMCPMissing, "", nil
}

// renderCodexMCPBlock builds the managed block, keeping the key names codex
// documents (mcp_servers.<name>, command, args, startup_timeout_sec).
//
// WHY startup_timeout_sec: codex gives an MCP server a short default budget to
// finish initializing, and `straza mcp` does real work before it answers:
// loading the client config and checking in with the gateway. On a cold start
// (first run of the day, a gateway that has to wake, a slow link) that lands
// past the default, and codex reports the straza server as "not initialized"
// while the very same command run by hand sits there serving stdio perfectly
// well: a governance surface silently absent for the whole session, with a
// symptom pointing at the wrong thing. 60 s buys the cold path room (codex's
// own node_repl example spends 120 on a server that does far less) and costs a
// healthy start nothing: the timeout is a ceiling, not a delay.
func renderCodexMCPBlock(strazaPath string) ([]string, error) {
	command, err := codexMCPCommandValue(strazaPath)
	if err != nil {
		return nil, err
	}
	lines := []string{
		codexMCPBegin,
		"[mcp_servers." + mcpServerName + "]",
		"command = " + command,
		`args = ["mcp", "--harness", "codex"]`,
		"startup_timeout_sec = 60",
	}
	if osName() == "windows" {
		// Codex 0.146 spawns stdio MCP servers with a stripped environment
		// (the upstream #3311 class, where every server fails init, codex's own
		// included), and a Windows child without SystemRoot loses networking
		// and DNS. Passing it explicitly is harmless on healthy codex releases,
		// and writing it HERE keeps install idempotent against an operator's
		// hand-added line: the block is rewritten whole, so an unrendered key
		// would be stripped on re-run.
		root := os.Getenv("SystemRoot")
		if root == "" {
			root = `C:\Windows`
		}
		rootValue, err := codexMCPCommandValue(root)
		if err != nil {
			return nil, err
		}
		lines = append(lines, "env = { SystemRoot = "+rootValue+" }")
	}
	return append(lines, codexMCPEnd), nil
}

// codexMCPCommandValue renders a path or command line as a TOML string: the
// MCP registration's `command`, and the managed hook block's command and
// managed_dir (installcodexmanaged.go). Normally a
// LITERAL (single-quoted) one: literals have no escape sequences at all, so a
// Windows path goes in verbatim (C:\Users\alice\straza.exe) where a basic
// "…" string needs every backslash doubled and breaks the day one is missed.
// The assumption that buys is that the path holds no single quote, and TOML
// gives literals no way to escape one; that is usually true but NOT guaranteed
// (Windows permits ' in a path; C:\Users\O'Brien\straza.exe is a real shape),
// so such a path falls back to a basic string with \ and " escaped. A control
// character has no TOML spelling we are willing to guess at, and no real
// executable path contains one: fail closed with a reason.
func codexMCPCommandValue(strazaPath string) (string, error) {
	if strings.ContainsFunc(strazaPath, func(r rune) bool { return r < 0x20 || r == 0x7f }) {
		return "", fmt.Errorf("cannot write the codex registration: %q contains a control character. Move the binary somewhere sane and re-run install", strazaPath)
	}
	if !strings.Contains(strazaPath, "'") {
		return "'" + strazaPath + "'", nil
	}
	escaped := strings.ReplaceAll(strazaPath, `\`, `\\`)
	escaped = strings.ReplaceAll(escaped, `"`, `\"`)
	return `"` + escaped + `"`, nil
}

// codexMCPCommandPath is codexMCPCommandValue in reverse, for reporting.
func codexMCPCommandPath(value string) string {
	v := strings.TrimSpace(value)
	if len(v) >= 2 {
		switch {
		case v[0] == '\'' && v[len(v)-1] == '\'':
			return v[1 : len(v)-1]
		case v[0] == '"' && v[len(v)-1] == '"':
			unescaped := strings.ReplaceAll(v[1:len(v)-1], `\"`, `"`)
			return strings.ReplaceAll(unescaped, `\\`, `\`)
		}
	}
	return v
}

// codexOwnedKeys returns the top-level keys the rendered block defines. Derived
// from the block itself rather than listed here, so the set cannot drift from
// what install actually writes the day a key is added or renamed.
func codexOwnedKeys(block []string) map[string]bool {
	owned := make(map[string]bool, len(block))
	for _, raw := range block {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, "[") {
			continue
		}
		if key, _, ok := strings.Cut(line, "="); ok {
			if path := codexQuotedKeyPath(key); len(path) > 0 && path[0] != "" {
				owned[path[0]] = true
			}
		}
	}
	return owned
}

// stripCodexTableTailDuplicates takes back, from the lines FOLLOWING the
// managed block, any assignment of a key the block already defines.
//
// The block ends at a comment, and a comment is not a table boundary: every
// line between the END marker and the next [table] header is still inside
// [mcp_servers.straza]. A key an operator politely adds just BELOW the block
// therefore collides once install writes that key itself, and a duplicate key
// in one table makes codex reject the WHOLE config file.
//
// Everything else is the user's and survives: foreign keys, comments, blank
// lines, and every byte from the next table header on. A multi-line value is
// handled as one unit in BOTH directions, so a wrapped `args = [...]` goes
// entirely and a foreign array is never misread line by line. A value that
// never closes is left alone with everything after it: the file is already
// invalid TOML, and guessing is how an installer eats a config.
func stripCodexTableTailDuplicates(tail []string, owned map[string]bool) []string {
	out := make([]string, 0, len(tail))
	for i := 0; i < len(tail); i++ {
		line := strings.TrimSpace(tail[i])
		if strings.HasPrefix(line, "[") {
			return append(out, tail[i:]...) // a new table: not our table any more
		}
		if line == "" || strings.HasPrefix(line, "#") {
			out = append(out, tail[i])
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			out = append(out, tail[i])
			continue
		}
		end := i
		for depth := codexValueDepth(value); depth > 0; {
			end++
			if end >= len(tail) {
				return append(out, tail[i:]...) // unterminated value: keep every byte
			}
			depth += codexValueDepth(tail[end])
		}
		if path := codexQuotedKeyPath(key); len(path) == 0 || !owned[path[0]] {
			out = append(out, tail[i:end+1]...)
		}
		i = end
	}
	return out
}

// codexValueDepth reports how many bracket or brace levels a line leaves open,
// which is how a multi-line value is followed to its end. Quoted text and
// trailing comments hold no structure: a "]" inside a string closes nothing.
func codexValueDepth(s string) int {
	depth := 0
	var quote rune
	escaped := false
	for _, r := range s {
		switch {
		case escaped:
			escaped = false
		case quote == '"' && r == '\\':
			escaped = true // only BASIC strings have escapes; literals take '\' verbatim
		case quote != 0:
			if r == quote {
				quote = 0
			}
		case r == '"' || r == '\'':
			quote = r
		case r == '#':
			return depth
		case r == '[' || r == '{':
			depth++
		case r == ']' || r == '}':
			depth--
		}
	}
	return depth
}

// findCodexManagedBlock returns the line range of the managed block, or
// (-1, -1) when there is none. An opened-but-unterminated block is an error
// rather than a reason to append a second one: straza would then own two
// blocks and rewrite only the first, so fail closed and let a human look.
func findCodexManagedBlock(lines []string) (int, int, error) {
	begin := -1
	for i, raw := range lines {
		line := strings.TrimSpace(raw)
		switch {
		case begin < 0 && strings.HasPrefix(line, codexMCPBeginPrefix):
			begin = i
		case begin >= 0 && strings.HasPrefix(line, codexMCPEnd):
			return begin, i, nil
		}
	}
	if begin >= 0 {
		return -1, -1, fmt.Errorf("the codex config has a %q line with no %q line after it. Repair or delete that block by hand; refusing to write a second one", codexMCPBeginPrefix, codexMCPEnd)
	}
	return -1, -1, nil
}

// codexHasForeignRegistration reports whether the file registers the straza
// MCP server OUTSIDE the managed block: the user wrote their own, so straza
// must not touch it or add a second one beside it (two mcp_servers.straza
// tables is a duplicate-key error that stops codex loading the file at all).
// Our own block is located and skipped here rather than by the caller, so no
// caller can accidentally ask this whether straza registered straza.
//
// Line-shape matching, not TOML parsing (see the file header). It recognizes a
// [mcp_servers.straza] table header (quoted, spaced, and deeper forms like
// [mcp_servers.straza.env] included), a `straza = { ... }` key under an
// [mcp_servers] header, the dotted `mcp_servers.straza = { ... }`, and an
// inline `mcp_servers = { straza = ... }` matched on the name alone. That last
// one is a deliberately generous heuristic: a false positive only makes straza
// keep its hands off a file it does not fully understand.
func codexHasForeignRegistration(lines []string) bool {
	blockBegin, blockEnd, err := findCodexManagedBlock(lines)
	if err != nil {
		blockBegin, blockEnd = -1, -1 // unterminated: the caller already errors
	}
	var table []string
	for i, raw := range lines {
		if blockBegin >= 0 && i >= blockBegin && i <= blockEnd {
			continue
		}
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if strings.HasPrefix(line, "[") {
			path, ok := codexTableHeader(line)
			table = path // an unreadable header owns nothing we can reason about
			if ok && codexIsStrazaMCP(path) {
				return true
			}
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		full := append(append([]string{}, table...), codexDottedKey(key)...)
		if codexIsStrazaMCP(full) {
			return true
		}
		if len(full) == 1 && full[0] == "mcp_servers" && strings.Contains(value, mcpServerName) {
			return true
		}
	}
	return false
}

// codexTableHeader splits a TOML table header line ("[a.b]", "[[a.b]]") into
// its normalized key path. A header whose name contains a quoted "]" is not
// understood (false): pathological, and the only thing we ask of a header is
// whether it is straza's.
func codexTableHeader(line string) ([]string, bool) {
	name := strings.TrimPrefix(strings.TrimPrefix(line, "["), "[")
	idx := strings.Index(name, "]")
	if idx < 0 {
		return nil, false
	}
	return codexDottedKey(name[:idx]), true
}

// codexDottedKey normalizes a dotted TOML key (`mcp_servers . "straza"`) into
// its segments, unquoting each. A dot inside a quoted segment is not honored;
// same pathological class as codexTableHeader.
func codexDottedKey(key string) []string {
	parts := strings.Split(key, ".")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if len(p) >= 2 && (p[0] == '"' && p[len(p)-1] == '"' || p[0] == '\'' && p[len(p)-1] == '\'') {
			p = p[1 : len(p)-1]
		}
		out = append(out, p)
	}
	return out
}

func codexIsStrazaMCP(path []string) bool {
	return len(path) >= 2 && path[0] == "mcp_servers" && path[1] == mcpServerName
}

// appendCodexBlock puts the block at the end of the file, preserving every
// byte already there: one blank separator line (which uninstall takes back)
// and a terminating newline, in whichever line ending the file already uses.
func appendCodexBlock(content string, block []string) string {
	nl := codexNewline(content)
	var b strings.Builder
	b.WriteString(content)
	if content != "" {
		if !strings.HasSuffix(content, "\n") {
			b.WriteString(nl)
		}
		b.WriteString(nl)
	}
	b.WriteString(strings.Join(block, nl))
	b.WriteString(nl)
	return b.String()
}

// codexNewline reports the file's line ending. A config.toml edited on Windows
// is CRLF, and a managed block written with bare LF into it would show up as a
// mangled single line in Notepad: cosmetic, but this file is meant to be read.
func codexNewline(content string) string {
	if strings.Contains(content, "\r\n") {
		return "\r\n"
	}
	return "\n"
}

// readCodexConfig reads the config, reporting whether it exists at all.
func readCodexConfig(path string) (string, bool, error) {
	raw, err := os.ReadFile(path) // #nosec G304 -- managing the harness's own config file
	if os.IsNotExist(err) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return string(raw), true, nil
}

// writeCodexConfig writes the config, creating its directory when absent. A
// fresh file gets 0600 like the hook settings file; an existing one keeps the
// permissions its owner chose; a rewrite must not silently retighten or
// loosen the user's own config.
func writeCodexConfig(path, content string) error {
	mode := os.FileMode(0o600)
	if fi, err := os.Stat(path); err == nil {
		mode = fi.Mode().Perm()
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(content), mode) // #nosec G306 -- user-scope config file, existing mode preserved
}
