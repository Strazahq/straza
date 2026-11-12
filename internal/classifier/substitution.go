package classifier

import "strings"

// simpleCmd is one simple command as simpleCommands reads it: its words, and
// the substitutions the shell expands in them, in order.
type simpleCmd struct {
	words  []string
	substs []subst
}

// subst is one substitution the shell expands: the index of the word that
// holds it and the command inside it.
type subst struct {
	word  int
	inner string
}

// substSignal detects, in cmds, a $( ), backtick or <( ) substitution whose
// inner command downloads or decodes, in a position where a shell or
// interpreter then runs its output as code. codeSubsts names the positions.
func substSignal(cmds []simpleCmd) (string, bool) {
	for _, c := range cmds {
		for _, inner := range codeSubsts(c) {
			switch substSource(inner) {
			case "fetch":
				return "substitution-to-shell: " + quoteFrag(strings.Join(c.words, " ")) +
					" runs the output of a download as code. " +
					"Download it to a file, read it, and run that file as a separate command.", true
			case "decode":
				return "encoded-substitution: " + quoteFrag(strings.Join(c.words, " ")) +
					" runs decoded text as code. " +
					"Decode it to a file, read it, and run that file as a separate command.", true
			}
		}
	}
	return "", false
}

// codeSubsts returns the inner commands of the substitutions whose output c
// runs as code. Five positions count: any part of the command word, the
// whole inline program of an interpreter family's eval flag, a process
// substitution given as an interpreter family's script, an argument of source
// or . that is wholly a substitution, and any substitution inside an argument
// of eval. eval runs its whole string as code, so each of its arguments is
// read twice: for what the shell expands before eval sees it, and for what
// eval expands itself.
func codeSubsts(c simpleCmd) []string {
	words := c.words
	base, idx := leadBase(words)
	var inners []string
	sole := func(w string, procOnly bool) {
		if open, inner, ok := soleSubst(w); ok && (!procOnly || open == "<(") {
			inners = append(inners, inner)
		}
	}
	// leadBase reads a word like $(echo a=b) as an assignment, so the command
	// word is the first word up to leadBase's that is wholly a substitution.
	end := len(words)
	if idx >= 0 {
		end = idx + 1
	}
	for _, w := range words[:end] {
		if _, inner, ok := soleSubst(w); ok {
			inners = append(inners, inner)
			break
		}
	}
	if k := cmdWord(c, idx); k >= 0 && !isRedirect(words[k]) {
		for _, s := range c.substs {
			if s.word == k {
				inners = append(inners, s.inner)
			}
		}
	}
	switch fam := interpFamily(base); {
	case base == "eval":
		for _, s := range c.substs {
			if s.word > idx {
				inners = append(inners, s.inner)
			}
		}
		for _, w := range words[idx+1:] {
			for _, rc := range simpleCommands(w) {
				for _, s := range rc.substs {
					inners = append(inners, s.inner)
				}
			}
			sole(w, false)
		}
	case base == "source" || base == ".":
		for _, w := range words[idx+1:] {
			sole(w, false)
		}
	case fam != "":
		if p, ok := evalPayload(fam, words, idx); ok {
			sole(p, false)
		} else if s, ok := scriptArg(words, idx); ok {
			sole(s, true)
		}
	}
	return inners
}

// substSource reports what a substitution's inner command produces: "fetch"
// when any stage is a fetcher, else "decode" when any stage is a decoder, else
// "". The stages are read with splitPipelines and analyzeLead, the same reading
// pipelineSignal gives a pipeline.
func substSource(inner string) string {
	decode := false
	for _, stages := range splitPipelines(inner) {
		for _, s := range stages {
			k := analyzeLead(s)
			if isFetcher(k.base) {
				return "fetch"
			}
			decode = decode || isDecoder(k)
		}
	}
	if decode {
		return "decode"
	}
	return ""
}

// scriptArg returns the first word after the interpreter at idx that is not an
// option, which is the script the interpreter runs. A word that starts with -
// or + reads as an option, since shells take +o and +x. An option that takes a
// separate value, such as bash -o pipefail, is not known here, so its value
// reads as the script.
func scriptArg(words []string, idx int) (string, bool) {
	for _, w := range words[idx+1:] {
		if !strings.HasPrefix(w, "-") && !strings.HasPrefix(w, "+") {
			return w, true
		}
	}
	return "", false
}

// soleSubst reports whether w is exactly one substitution once spaces and one
// layer of matching quotes are trimmed, and returns its opener ("$(", "`" or
// "<(") and the command inside it. The word is read as fresh shell text, the
// way an inner interpreter or eval reads it, so the kind of quote that carried
// it does not matter.
func soleSubst(w string) (open, inner string, ok bool) {
	w = strings.TrimSpace(w)
	if len(w) >= 2 && (w[0] == '"' || w[0] == '\'') && w[len(w)-1] == w[0] {
		w = strings.TrimSpace(w[1 : len(w)-1])
	}
	if w == "" || substEnd(w, 0, false) != len(w) {
		return "", "", false
	}
	open, inner = splitSubst(w)
	return open, inner, true
}

// splitSubst splits s, which is exactly one substitution, into its opener and
// the command inside it.
func splitSubst(s string) (open, inner string) {
	if s[0] == '`' {
		return "`", s[1 : len(s)-1]
	}
	return s[:2], s[2 : len(s)-1]
}

// cmdWord returns the index of c's command word, given leadBase's index idx,
// or -1 when there is none. leadBase skips any word with a = after its first
// byte, so an earlier word that holds a substitution and is neither a flag nor
// a real NAME=value assignment is the command word instead.
func cmdWord(c simpleCmd, idx int) int {
	for _, s := range c.substs {
		if idx >= 0 && s.word >= idx {
			break
		}
		if w := c.words[s.word]; !strings.HasPrefix(w, "-") && !isAssignment(w) {
			return s.word
		}
	}
	return idx
}

// isAssignment reports a NAME=value, NAME+=value or NAME[i]=value word whose
// name is a literal shell name.
func isAssignment(w string) bool {
	j := strings.IndexByte(w, '=')
	if j <= 0 {
		return false
	}
	name := strings.TrimSuffix(w[:j], "+")
	if k := strings.IndexByte(name, '['); k >= 0 {
		name = name[:k]
	}
	if name == "" {
		return false
	}
	for k, r := range name {
		if r != '_' && (r < 'a' || r > 'z') && (r < 'A' || r > 'Z') && (k == 0 || r < '0' || r > '9') {
			return false
		}
	}
	return true
}

// isRedirect reports a redirection word such as >file, 2>file or <file, which
// is never the command word. A process substitution is no redirection.
func isRedirect(w string) bool {
	t := strings.TrimLeft(w, "0123456789&")
	return (strings.HasPrefix(t, "<") || strings.HasPrefix(t, ">")) && !strings.HasPrefix(t[1:], "(")
}

// evalProgram returns the program eval runs when it leads the first simple
// command of cmds: its arguments joined by spaces, as eval joins them.
func evalProgram(cmds []simpleCmd) (string, bool) {
	if len(cmds) == 0 {
		return "", false
	}
	words := cmds[0].words
	base, idx := leadBase(words)
	if base != "eval" || idx+1 >= len(words) {
		return "", false
	}
	return strings.Join(words[idx+1:], " "), true
}

// simpleCommands splits cmd into simple commands. It breaks commands on | & ;
// and newline and words on blanks, with the quote and escape rules of
// splitWords, quotes stripped. A substitution stays whole and raw inside its
// word, so blanks and operators inside it split nothing, and it is recorded
// when the shell expands it there. An unclosed substitution keeps the rest of
// cmd in its word, because a shell refuses such a line rather than running
// part of it. An unquoted # that starts a word comments out the rest of the
// line, and the & of a redirection such as 2>&1 ends no command. The body of
// a quoted heredoc is split as its own text, after the commands of its line.
// A substitution in it is recorded only when a shell reads the heredoc: the
// shell that reads the command line expands nothing there.
func simpleCommands(cmd string) []simpleCmd { return scanCommands(cmd, true, true) }

// scanCommands is simpleCommands. record false records no substitution, and
// heredocs false reads no heredoc, as for the body of a quoted heredoc, so a
// body is scanned once and a nested heredoc stays plain text.
func scanCommands(cmd string, record, heredocs bool) []simpleCmd {
	var cmds []simpleCmd
	var sc simpleCmd
	var pending []heredoc
	var cur strings.Builder
	inSingle, inDouble, started := false, false, false
	flushWord := func() {
		if started {
			sc.words = append(sc.words, cur.String())
			cur.Reset()
			started = false
		}
	}
	flushCmd := func() {
		flushWord()
		if len(sc.words) > 0 {
			cmds = append(cmds, sc)
		}
		sc = simpleCmd{}
	}
	for i := 0; i < len(cmd); i++ {
		c := cmd[i]
		if !inSingle {
			if end := substEnd(cmd, i, inDouble); end != 0 {
				if end < 0 {
					end = len(cmd)
				} else if record {
					_, inner := splitSubst(cmd[i:end])
					sc.substs = append(sc.substs, subst{word: len(sc.words), inner: inner})
				}
				cur.WriteString(cmd[i:end])
				started = true
				i = end - 1
				continue
			}
		}
		if heredocs && !inSingle && !inDouble {
			if h, ok := quotedHeredocAt(cmd, i); ok {
				h.cmd = len(cmds)
				pending = append(pending, h)
			}
		}
		switch {
		case c == '\\' && !inSingle:
			if i+1 < len(cmd) {
				i++
				cur.WriteByte(cmd[i])
			}
			started = true
		case c == '\'' && !inDouble:
			inSingle = !inSingle
			started = true
		case c == '"' && !inSingle:
			inDouble = !inDouble
			started = true
		case inSingle || inDouble:
			cur.WriteByte(c)
			started = true
		case c == '#' && !started && (i == 0 || cmd[i-1] != '\r'):
			for i+1 < len(cmd) && cmd[i+1] != '\n' {
				i++
			}
		case c == ' ' || c == '\t' || c == '\r':
			flushWord()
		case c == '\n':
			flushCmd()
			start := i + 1
			reader, shell := -1, false
			for _, h := range pending {
				bodyEnd, next := heredocEnd(cmd, start, h)
				// Pending heredocs come in command order, so each reader is
				// judged once, however many heredocs it reads.
				if h.cmd != reader && h.cmd < len(cmds) {
					base, _ := leadBase(cmds[h.cmd].words)
					reader, shell = h.cmd, interpFamily(base) == famShell
				}
				if shell {
					cmds = append(cmds, scanCommands(cmd[start:bodyEnd], record, false)...)
				}
				start = next
			}
			pending = nil
			i = start - 1
		case c == '&' && redirectAmp(cmd, i):
			cur.WriteByte(c)
			started = true
		case c == '|' || c == '&' || c == ';':
			flushCmd()
		default:
			cur.WriteByte(c)
			started = true
		}
	}
	flushCmd()
	return cmds
}

// redirectAmp reports that the & at s[i] belongs to a redirection, as in >&,
// <&, 2>&1 or &>, and not to the & or && that ends a command.
func redirectAmp(s string, i int) bool {
	if i > 0 && (s[i-1] == '>' || s[i-1] == '<') {
		return true
	}
	return i+1 < len(s) && s[i+1] == '>' && (i == 0 || s[i-1] != '&')
}

// substEnd returns the index just past the substitution that opens at s[i], 0
// when none opens there, and -1 when one opens and never closes. The caller is
// outside single quotes. A $( or a backtick opens there, and a <( opens only
// outside double quotes too, as in bash.
func substEnd(s string, i int, inDouble bool) int {
	switch {
	case s[i] == '`':
		for j := i + 1; j < len(s); j++ {
			switch s[j] {
			case '\\':
				j++
			case '`':
				return j + 1
			}
		}
		return -1
	case strings.HasPrefix(s[i:], "$("), !inDouble && strings.HasPrefix(s[i:], "<("):
		return parenEnd(s, i+2)
	}
	return 0
}

// parenEnd returns the index just past the ) that closes a substitution whose
// body starts at s[from], or -1 when nothing closes it. Inside the body, quotes
// and escapes follow splitWords and unquoted parentheses nest. The body of a
// quoted heredoc is skipped, since a quote or a parenthesis there is text. An
// unquoted # that starts a word hides the rest of its line, so a ) or a << in
// a comment counts for nothing.
func parenEnd(s string, from int) int {
	depth := 1
	inSingle, inDouble := false, false
	atWord := true // the next byte would start a word
	var pending []heredoc
	for j := from; j < len(s); j++ {
		c := s[j]
		boundary := false
		switch {
		case c == '\\' && !inSingle:
			j++
		case c == '\'' && !inDouble:
			inSingle = !inSingle
		case c == '"' && !inSingle:
			inDouble = !inDouble
		case inSingle || inDouble:
		case c == '#' && atWord:
			for j+1 < len(s) && s[j+1] != '\n' {
				j++
			}
		case c == '<':
			if h, ok := quotedHeredocAt(s, j); ok {
				pending = append(pending, h)
			}
			boundary = true
		case c == '\n' && len(pending) > 0:
			start := j + 1
			for _, h := range pending {
				_, start = heredocEnd(s, start, h)
			}
			pending = nil
			j, boundary = start-1, true
		case c == '(':
			depth++
			boundary = true
		case c == ')':
			depth--
			if depth == 0 {
				return j + 1
			}
			boundary = true
		default:
			boundary = strings.IndexByte(" \t\n;|&>", c) >= 0
		}
		atWord = boundary
	}
	return -1
}
