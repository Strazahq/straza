package main

import (
	"bufio"
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// cliPage is one generated reference page: the flags its Options blocks list,
// long names without the dashes and single-letter shorts, whether the command
// runs by itself, which Cobra shows with a use line, and whether pages of
// verbs under it exist.
type cliPage struct {
	long     map[string]bool
	short    map[string]bool
	runnable bool
	group    bool
}

// cliIndex maps a command path such as "strazactl policy simulate" to its page.
type cliIndex map[string]cliPage

var (
	binaries = map[string]bool{"strazactl": true, "straza": true, "strazad": true}
	flagLine = regexp.MustCompile(`^\s*(?:-([A-Za-z0-9]),\s+)?--([A-Za-z0-9-]+)`)
	assign   = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*=`)
	inline   = regexp.MustCompile("`([^`\n]+)`")
	fence    = regexp.MustCompile("^```\\s*([A-Za-z0-9_-]*)")
)

// loadCLIIndex reads every generated page under website/content/reference/cli.
// The file name is the command path with underscores: strazactl_policy_simulate.md.
func loadCLIIndex(root string) (cliIndex, error) {
	idx := cliIndex{}
	dir := filepath.Join(root, cliDir)
	err := filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || filepath.Ext(p) != ".md" || d.Name() == "_index.md" {
			return err
		}
		data, err := os.ReadFile(p) //nolint:gosec // G304: reading the generated reference pages is the tool's job
		if err != nil {
			return err
		}
		key := strings.ReplaceAll(strings.TrimSuffix(d.Name(), ".md"), "_", " ")
		page := cliPage{long: map[string]bool{}, short: map[string]bool{}}
		lines := strings.Split(string(data), "\n")
		// Cobra prints the use line above the Examples and Options sections,
		// and an example there can start with the command path too.
		usePart := true
		for i, line := range lines {
			usePart = usePart && line != "## Examples" && !strings.HasPrefix(line, "## Options")
			if m := flagLine.FindStringSubmatch(line); m != nil {
				page.long[m[2]] = true
				if m[1] != "" {
					page.short[m[1]] = true
				}
			}
			page.runnable = page.runnable || (usePart && line == "```" && i+1 < len(lines) && isUseLine(lines[i+1], key))
		}
		idx[key] = page
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", cliDir, err)
	}
	if len(idx) == 0 {
		return nil, fmt.Errorf("no generated CLI pages under %s; run make docs-gen", cliDir)
	}
	for key := range idx {
		if k := strings.LastIndexByte(key, ' '); k > 0 {
			if parent, ok := idx[key[:k]]; ok {
				parent.group = true
				idx[key[:k]] = parent
			}
		}
	}
	return idx, nil
}

// isUseLine reports whether line is the use line Cobra prints in a fence for
// a command that runs by itself: the command path alone or followed by its
// operands and flags.
func isUseLine(line, key string) bool {
	return line == key || strings.HasPrefix(line, key+" ")
}

// checkCommands finds every strazactl, straza and strazad invocation in a
// Markdown file (shell fences and inline code) or a shell script and checks
// its verb path and flags against the reference pages.
func checkCommands(rel string, data []byte, script bool, idx cliIndex) []finding {
	var out []finding
	check := func(line int, text string) {
		for _, seg := range segments(text) {
			if msg := checkSegment(seg, idx); msg != "" {
				out = append(out, finding{rel, line, msg})
			}
		}
	}
	joined := joinContinuations(data)
	if script {
		for _, l := range joined {
			if strings.HasPrefix(strings.TrimSpace(l.text), "#") {
				continue
			}
			check(l.line, l.text)
		}
		return out
	}
	inShell, inOther := false, false
	for _, l := range joined {
		if m := fence.FindStringSubmatch(l.text); m != nil {
			switch {
			case inShell || inOther:
				inShell, inOther = false, false
			case m[1] == "sh" || m[1] == "bash" || m[1] == "shell":
				inShell = true
			default:
				inOther = true
			}
			continue
		}
		switch {
		case inShell:
			check(l.line, l.text)
		case inOther:
			continue
		default:
			for _, m := range inline.FindAllStringSubmatch(l.text, -1) {
				check(l.line, m[1])
			}
		}
	}
	return out
}

type numbered struct {
	line int
	text string
}

// joinContinuations folds a trailing backslash onto the next line and keeps
// the first line's number.
func joinContinuations(data []byte) []numbered {
	var out []numbered
	sc := bufio.NewScanner(bytes.NewReader(data))
	sc.Buffer(make([]byte, 1024*1024), 1024*1024)
	n := 0
	var cur *numbered
	for sc.Scan() {
		n++
		t := sc.Text()
		if cur != nil {
			cur.text += " " + strings.TrimSpace(strings.TrimSuffix(t, "\\"))
		} else {
			out = append(out, numbered{n, strings.TrimSuffix(t, "\\")})
			cur = &out[len(out)-1]
		}
		if !strings.HasSuffix(t, "\\") {
			cur = nil
		}
	}
	return out
}

// segments tokenizes one shell line, honoring quotes, and splits it at the
// unquoted separators |, ||, &&, ;, ( and ).
func segments(text string) [][]string {
	var segs [][]string
	var cur []string
	var tok strings.Builder
	inTok := false
	flush := func() {
		if inTok {
			cur = append(cur, tok.String())
			tok.Reset()
			inTok = false
		}
	}
	cut := func() {
		flush()
		if len(cur) > 0 {
			segs = append(segs, cur)
			cur = nil
		}
	}
	quote := byte(0)
	for i := 0; i < len(text); i++ {
		c := text[i]
		switch {
		case quote != 0:
			if c == quote {
				quote = 0
			} else {
				tok.WriteByte(c)
			}
		case c == '\'' || c == '"':
			quote = c
			inTok = true
		case c == ' ' || c == '\t':
			flush()
		case c == '|' || c == '&' || c == ';' || c == '(' || c == ')':
			cut()
		default:
			tok.WriteByte(c)
			inTok = true
		}
	}
	cut()
	return segs
}

// checkSegment returns a finding message for one command, or "" when the
// segment is not a Straza command or is consistent with its reference page.
// A word after a group that runs no command of its own must name a verb of
// that group, since the group has no operand for it to be.
func checkSegment(tokens []string, idx cliIndex) string {
	for len(tokens) > 0 && (assign.MatchString(tokens[0]) || tokens[0] == "sudo" || tokens[0] == "env") {
		tokens = tokens[1:]
	}
	if len(tokens) < 2 || !binaries[tokens[0]] || strings.HasPrefix(tokens[1], "-") {
		return ""
	}
	key := tokens[0]
	matched := false
	next := ""
	for _, t := range tokens[1:] {
		if strings.HasPrefix(t, "-") {
			break
		}
		if _, ok := idx[key+" "+t]; !ok {
			next = t
			break
		}
		key += " " + t
		matched = true
	}
	if !matched {
		return fmt.Sprintf("unknown command %q: no page for it under %s", tokens[0]+" "+tokens[1], cliDir)
	}
	page := idx[key]
	if next != "" && page.group && !page.runnable {
		return fmt.Sprintf("unknown command %q: no page for it under %s", key+" "+next, cliDir)
	}
	for _, t := range tokens[1:] {
		switch {
		case strings.HasPrefix(t, "--") && len(t) > 2:
			name := strings.TrimPrefix(t, "--")
			if k := strings.IndexByte(name, '='); k >= 0 {
				name = name[:k]
			}
			if !page.long[name] {
				return fmt.Sprintf("flag --%s is not on the %s reference page", name, key)
			}
		case len(t) == 2 && t[0] == '-' && isLetter(t[1]):
			if !page.short[t[1:]] {
				return fmt.Sprintf("flag %s is not on the %s reference page", t, key)
			}
		}
	}
	return ""
}

func isLetter(c byte) bool {
	return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
}
