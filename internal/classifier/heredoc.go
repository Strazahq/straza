package classifier

import "strings"

// heredoc is a here-document whose delimiter is quoted, so the shell that
// reads the command line expands nothing in its body: the delimiter after
// quote removal, whether <<- strips leading tabs from the delimiter line, the
// index just past the delimiter word, and the index of the simple command
// that reads it.
type heredoc struct {
	delim string
	strip bool
	end   int
	cmd   int
}

// quotedHeredocAt reports a here-document operator at s[i] whose delimiter
// word is quoted in any part, as in <<'X', <<"X" or <<\X, and returns it. A
// here-string (<<<) and an unquoted delimiter are not reported, so an unquoted
// body keeps being read as shell text, which the shell expands.
func quotedHeredocAt(s string, i int) (heredoc, bool) {
	if !strings.HasPrefix(s[i:], "<<") || strings.HasPrefix(s[i:], "<<<") || (i > 0 && s[i-1] == '<') {
		return heredoc{}, false
	}
	var h heredoc
	j := i + 2
	if j < len(s) && s[j] == '-' {
		h.strip = true
		j++
	}
	for j < len(s) && (s[j] == ' ' || s[j] == '\t') {
		j++
	}
	var b strings.Builder
	quoted := false
	for ; j < len(s) && strings.IndexByte(" \t\r\n;&|<>()", s[j]) < 0; j++ {
		switch c := s[j]; c {
		case '\'', '"':
			k := strings.IndexByte(s[j+1:], c)
			if k < 0 {
				return heredoc{}, false
			}
			b.WriteString(s[j+1 : j+1+k])
			j += k + 1
			quoted = true
		case '\\':
			if j+1 < len(s) {
				j++
				b.WriteByte(s[j])
			}
			quoted = true
		default:
			b.WriteByte(c)
		}
	}
	h.delim, h.end = b.String(), j
	return h, quoted && h.delim != ""
}

// heredocEnd finds the end of the body of h that starts at s[from], the start
// of a line. It returns the index where the delimiter line starts and the
// index just past that line, or len(s) twice when no line closes the body.
func heredocEnd(s string, from int, h heredoc) (bodyEnd, next int) {
	for pos := from; pos < len(s); pos = next {
		line := s[pos:]
		next = len(s)
		if k := strings.IndexByte(line, '\n'); k >= 0 {
			line, next = line[:k], pos+k+1
		}
		if h.strip {
			line = strings.TrimLeft(line, "\t")
		}
		if line == h.delim {
			return pos, next
		}
	}
	return len(s), len(s)
}

// catBody reports an inner command that is only cat reading quoted heredocs,
// as in "$(cat <<'EOF' ... EOF)": cat and its heredoc operators alone on the
// first line, and nothing but blanks after the bodies. It returns its output,
// the text of the bodies, in which the shell expands nothing.
func catBody(inner string) (string, bool) {
	rest, ok := strings.CutPrefix(strings.TrimLeft(inner, " \t\n"), "cat")
	if !ok {
		return "", false
	}
	var hs []heredoc
	i := 0
	for {
		for i < len(rest) && (rest[i] == ' ' || rest[i] == '\t') {
			i++
		}
		if i >= len(rest) || rest[i] == '\n' {
			break
		}
		h, ok := quotedHeredocAt(rest, i)
		if !ok {
			return "", false
		}
		hs = append(hs, h)
		i = h.end
	}
	if len(hs) == 0 || i >= len(rest) {
		return "", false
	}
	var body strings.Builder
	start := i + 1
	for _, h := range hs {
		bodyEnd, next := heredocEnd(rest, start, h)
		body.WriteString(rest[start:bodyEnd])
		start = next
	}
	return body.String(), strings.TrimSpace(rest[start:]) == ""
}
