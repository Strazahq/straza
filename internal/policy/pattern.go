package policy

import (
	"fmt"
	gopath "path"
	"regexp"
	"strings"
)

// matcher is one compiled pattern. literalPrefix enables cheap rejection
// before the regex runs; the p99<100µs latency budget depends on it.
type matcher struct {
	src           string
	literalPrefix string
	re            *regexp.Regexp
}

func (m *matcher) match(s string) bool {
	if m.literalPrefix != "" && !strings.HasPrefix(s, m.literalPrefix) {
		return false
	}
	return m.re.MatchString(s)
}

func matchAny(ms []matcher, s string) bool {
	for i := range ms {
		if ms[i].match(s) {
			return true
		}
	}
	return false
}

func matchAnyOf(ms []matcher, subjects []string) bool {
	for i := range ms {
		for _, s := range subjects {
			if ms[i].match(s) {
				return true
			}
		}
	}
	return false
}

// compileTextPattern compiles a command/toolName pattern: glob where `*`
// crosses everything and `?` is one rune, or `re:`-prefixed anchored regex
// (SPEC.md §4).
func compileTextPattern(p string) (matcher, error) {
	if rest, ok := strings.CutPrefix(p, "re:"); ok {
		re, err := regexp.Compile("(?s)^(?:" + rest + ")$")
		if err != nil {
			return matcher{}, fmt.Errorf("pattern %q: %w", p, err)
		}
		return matcher{src: p, re: re}, nil
	}
	var sb strings.Builder
	sb.WriteString("(?s)^")
	prefix, inPrefix := "", true
	for _, r := range p {
		switch r {
		case '*':
			sb.WriteString(".*")
			inPrefix = false
		case '?':
			sb.WriteString(".")
			inPrefix = false
		default:
			sb.WriteString(regexp.QuoteMeta(string(r)))
			if inPrefix {
				prefix += string(r)
			}
		}
	}
	sb.WriteString("$")
	re, err := regexp.Compile(sb.String())
	if err != nil {
		return matcher{}, fmt.Errorf("pattern %q: %w", p, err)
	}
	return matcher{src: p, literalPrefix: prefix, re: re}, nil
}

// compilePathPattern compiles a path glob: `**/` spans zero or more
// directories, `**` crosses `/`, `*`/`?` stay within a segment. Patterns are
// matched against cleaned paths where the workspace prefix has been
// rewritten to the literal `${workspace}` (SPEC.md §4).
func compilePathPattern(p string) (matcher, error) {
	if rest, ok := strings.CutPrefix(p, "re:"); ok {
		re, err := regexp.Compile("(?s)^(?:" + rest + ")$")
		if err != nil {
			return matcher{}, fmt.Errorf("pattern %q: %w", p, err)
		}
		return matcher{src: p, re: re}, nil
	}
	var sb strings.Builder
	sb.WriteString("(?s)^")
	prefix, inPrefix := "", true
	i := 0
	for i < len(p) {
		switch {
		case strings.HasPrefix(p[i:], "**/"):
			sb.WriteString("(?:.*/)?")
			i += 3
			inPrefix = false
		case strings.HasPrefix(p[i:], "**"):
			sb.WriteString(".*")
			i += 2
			inPrefix = false
		case p[i] == '*':
			sb.WriteString("[^/]*")
			i++
			inPrefix = false
		case p[i] == '?':
			sb.WriteString("[^/]")
			i++
			inPrefix = false
		default:
			sb.WriteString(regexp.QuoteMeta(string(p[i])))
			if inPrefix {
				prefix += string(p[i])
			}
			i++
		}
	}
	sb.WriteString("$")
	re, err := regexp.Compile(sb.String())
	if err != nil {
		return matcher{}, fmt.Errorf("pattern %q: %w", p, err)
	}
	return matcher{src: p, literalPrefix: prefix, re: re}, nil
}

// workspaceLiteral is what workspace-relative paths are rewritten to before
// matching, so `${workspace}/**` patterns compile once, not per session.
const workspaceLiteral = "${workspace}"

// cleanPath normalizes a path for matching: separators to `/`, drive letter
// lowercased, relative paths resolved against the workspace, `.`/`..`
// segments resolved, and the workspace prefix rewritten to ${workspace}.
func cleanPath(p, workspace string) string {
	cp := normalize(p)
	ws := normalize(workspace)
	if !isAbs(cp) && ws != "" {
		cp = gopath.Clean(ws + "/" + cp)
	}
	if ws != "" {
		if cp == ws {
			return workspaceLiteral
		}
		if strings.HasPrefix(cp, ws+"/") {
			return workspaceLiteral + cp[len(ws):]
		}
	}
	return cp
}

func normalize(p string) string {
	if p == "" {
		return ""
	}
	p = strings.ReplaceAll(p, "\\", "/")
	if len(p) >= 2 && p[1] == ':' {
		p = strings.ToLower(p[:1]) + p[1:]
	}
	return gopath.Clean(p)
}

func isAbs(p string) bool {
	return strings.HasPrefix(p, "/") || (len(p) >= 3 && p[1] == ':' && p[2] == '/')
}

// shellWords splits a command line into words honoring ”, "" and \ escapes
// (SPEC.md §4: argv-joined and token-level command matching).
func shellWords(s string) []string {
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

// attestation ranks for require.attestation minimums.
var attestationRank = map[string]int{"none": 0, "advisory": 1, "managed": 2}

// harnessConstraint is a parsed require.harness entry.
type harnessConstraint struct {
	name       string
	minVersion string // "" = any version
}

func (c harnessConstraint) satisfiedBy(harness string) bool {
	name, version, _ := strings.Cut(harness, "/")
	if name != c.name {
		return false
	}
	if c.minVersion == "" {
		return true
	}
	return versionGE(version, c.minVersion)
}

// versionGE compares dotted numeric versions; non-numeric suffixes within a
// segment are ignored ("2.1.0-beta" → 2.1.0), missing segments are zero.
func versionGE(v, minV string) bool {
	vp, mp := strings.Split(v, "."), strings.Split(minV, ".")
	for i := 0; i < len(vp) || i < len(mp); i++ {
		a, b := 0, 0
		if i < len(vp) {
			a = leadingInt(vp[i])
		}
		if i < len(mp) {
			b = leadingInt(mp[i])
		}
		if a != b {
			return a > b
		}
	}
	return true
}

func leadingInt(s string) int {
	n := 0
	for _, r := range s {
		if r < '0' || r > '9' {
			break
		}
		n = n*10 + int(r-'0')
	}
	return n
}
