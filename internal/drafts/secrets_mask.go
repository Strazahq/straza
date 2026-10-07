package drafts

import (
	"encoding/json"
	"sort"
	"strings"
	"unicode/utf8"

	"gopkg.in/yaml.v3"

	"github.com/strazahq/straza/internal/redact"
)

// WithoutSecrets answers doc, an App document, with every place ScanSecrets
// flags in it replaced by redact.Mark, and whether it replaced any. A
// flagged string is replaced whole by a quoted mark, except one that only
// the address rules flagged: its addresses keep their hosts, and only the
// flagged parts are masked, as secretAddress masks them. In a comment, and
// in text that does not decode, each credential shape, private key block
// and flagged part of an address is replaced where it stands. Every other
// byte stays as it was, so a document with nothing to flag comes back as it
// is. When the extent of a flagged string in the text cannot be told, the
// answer is redact.Mark alone, because no secret may stay.
func WithoutSecrets(doc string) (string, bool) {
	s := &secretScan{doc: doc, app: true, seen: map[string]bool{}}
	out := doc
	if s.document() {
		var ok bool
		if out, ok = maskNodes(doc, s.flagged, s.whole); !ok {
			return redact.Mark, true
		}
	}
	out = maskLines(out)
	return out, out != doc
}

// maskAddresses answers v with each address in it masked part by part, as
// secretAddress masks it.
func maskAddresses(v string) string {
	return secretURLRe.ReplaceAllStringFunc(v, func(u string) string {
		_, masked := secretAddress(u, secretAddresses)
		return masked
	})
}

// maskNodes answers doc with the text of every node in flagged replaced:
// by redact.Mark, in double quotes where the node was written in them and
// in single quotes otherwise, or, for a node whole does not hold, by its
// value with its addresses masked, written as a JSON string, which reads
// the same as YAML in any place a scalar stands. It reports false when the
// extent of a node's text cannot be told, or when two extents overlap.
func maskNodes(doc string, flagged []*yaml.Node, whole map[*yaml.Node]bool) (string, bool) {
	if len(flagged) == 0 {
		return doc, true
	}
	starts := []int{0}
	for i := 0; i < len(doc); i++ {
		if doc[i] == '\n' {
			starts = append(starts, i+1)
		}
	}
	type cut struct {
		from, to int
		with     string
	}
	var cuts []cut
	seen := map[*yaml.Node]bool{}
	for _, n := range flagged {
		if seen[n] {
			continue
		}
		seen[n] = true
		from, to, ok := scalarExtent(doc, starts, n)
		if !ok {
			return "", false
		}
		with := "'" + redact.Mark + "'"
		if n.Style&yaml.DoubleQuotedStyle != 0 {
			with = `"` + redact.Mark + `"`
		}
		if !whole[n] {
			var b strings.Builder
			enc := json.NewEncoder(&b)
			enc.SetEscapeHTML(false)
			if enc.Encode(maskAddresses(n.Value)) != nil {
				return "", false
			}
			with = strings.TrimSuffix(b.String(), "\n")
		}
		cuts = append(cuts, cut{from, to, with})
	}
	sort.Slice(cuts, func(i, j int) bool { return cuts[i].from < cuts[j].from })
	var b strings.Builder
	last := 0
	for _, c := range cuts {
		if c.from < last {
			return "", false
		}
		b.WriteString(doc[last:c.from])
		b.WriteString(c.with)
		last = c.to
	}
	b.WriteString(doc[last:])
	return b.String(), true
}

// scalarExtent answers where the text of the scalar node n starts and ends
// in doc, whose lines start at starts. yaml.v3 counts a node's column in
// characters, from its anchor or tag when it has one. It reports false for
// a block scalar with an explicit indentation indicator, and for text that
// does not read as the node's style and value say.
func scalarExtent(doc string, starts []int, n *yaml.Node) (int, int, bool) {
	if n.Kind != yaml.ScalarNode || n.Line < 1 || n.Line > len(starts) || n.Column < 1 {
		return 0, 0, false
	}
	i := starts[n.Line-1]
	for c := 1; c < n.Column; c++ {
		if i >= len(doc) || doc[i] == '\n' {
			return 0, 0, false
		}
		_, size := utf8.DecodeRuneInString(doc[i:])
		i += size
	}
	for i < len(doc) && (doc[i] == '&' || doc[i] == '!') {
		for i < len(doc) && !strings.ContainsRune(" \t\r\n", rune(doc[i])) {
			i++
		}
		for i < len(doc) && (doc[i] == ' ' || doc[i] == '\t') {
			i++
		}
	}
	switch {
	case n.Style&yaml.DoubleQuotedStyle != 0:
		return quotedExtent(doc, i, '"')
	case n.Style&yaml.SingleQuotedStyle != 0:
		return quotedExtent(doc, i, '\'')
	case n.Style&(yaml.LiteralStyle|yaml.FoldedStyle) != 0:
		return blockExtent(doc, i, n.Value)
	}
	return plainExtent(doc, i, n.Value)
}

// quotedExtent answers the extent of the scalar quoted by q that opens at i:
// a backslash escapes the next byte in double quotes, and two single quotes
// stand for one in single quotes.
func quotedExtent(doc string, i int, q byte) (int, int, bool) {
	if i >= len(doc) || doc[i] != q {
		return 0, 0, false
	}
	for j := i + 1; j < len(doc); j++ {
		switch {
		case q == '"' && doc[j] == '\\':
			j++
		case doc[j] == q && q == '\'' && j+1 < len(doc) && doc[j+1] == '\'':
			j++
		case doc[j] == q:
			return i, j + 1, true
		}
	}
	return 0, 0, false
}

// blockExtent answers the extent of the literal or folded block scalar whose
// indicator stands at i: its header and every line of its content, which is
// indented as its first line that is not blank, up to the last such line.
func blockExtent(doc string, i int, value string) (int, int, bool) {
	if i >= len(doc) || (doc[i] != '|' && doc[i] != '>') {
		return 0, 0, false
	}
	eol := strings.IndexByte(doc[i:], '\n')
	if eol < 0 {
		return 0, 0, false
	}
	header := strings.TrimRight(doc[i:i+eol], "\r")
	if cut := strings.IndexByte(header, '#'); cut >= 0 {
		header = header[:cut]
	}
	if strings.ContainsAny(header, "123456789") {
		return 0, 0, false
	}
	end, indent := 0, -1
	for pos := i + eol + 1; pos < len(doc); {
		next := strings.IndexByte(doc[pos:], '\n')
		line := doc[pos:]
		if next >= 0 {
			line = doc[pos : pos+next]
		}
		line = strings.TrimRight(line, "\r")
		if body := strings.TrimLeft(line, " "); strings.TrimSpace(body) != "" {
			width := len(line) - len(body)
			if indent < 0 {
				if indent = width; !strings.HasPrefix(value, line[width:]) {
					return 0, 0, false
				}
			}
			if width < indent {
				break
			}
			end = pos + len(line)
		}
		if next < 0 {
			break
		}
		pos += next + 1
	}
	if indent < 0 {
		return 0, 0, false
	}
	return i, end, true
}

// plainExtent answers the extent of the plain scalar that starts at i and
// reads value: its lines, each trimmed, which the value joins with one
// space, or with line breaks where blank lines stand between them.
func plainExtent(doc string, i int, value string) (int, int, bool) {
	rest, pos := value, i
	for !strings.HasPrefix(doc[pos:], rest) {
		eol := strings.IndexByte(doc[pos:], '\n')
		if eol < 0 {
			return 0, 0, false
		}
		seg := strings.TrimRight(doc[pos:pos+eol], " \t\r")
		if seg == "" || !strings.HasPrefix(rest, seg) {
			return 0, 0, false
		}
		rest, pos = rest[len(seg):], pos+eol+1
		sep := " "
		for {
			next := strings.IndexByte(doc[pos:], '\n')
			if next < 0 || strings.TrimSpace(doc[pos:pos+next]) != "" {
				break
			}
			sep, pos = strings.TrimPrefix(sep, " ")+"\n", pos+next+1
		}
		if !strings.HasPrefix(rest, sep) {
			return 0, 0, false
		}
		rest = rest[len(sep):]
		for pos < len(doc) && (doc[pos] == ' ' || doc[pos] == '\t') {
			pos++
		}
	}
	return i, pos + len(rest), true
}

// MaskText answers text, which is no document, such as a line a server
// wrote to its log, masked as WithoutSecrets masks a comment (maskLines).
func MaskText(text string) string {
	return maskLines(text)
}

// maskLines answers text with every complete private key block replaced by
// redact.Mark, and, on each line the raw reading of ScanSecrets flags, each
// credential shape and each value after a word that names a secret replaced
// by it, and each address masked part by part.
func maskLines(text string) string {
	text = redact.PEMBlock.ReplaceAllStringFunc(text, func(m string) string {
		if secretKeyHeaderRe.MatchString(m) {
			return redact.Mark
		}
		return m
	})
	lines := strings.SplitAfter(text, "\n")
	for i, line := range lines {
		if len(secretTextRules(line, secretAddresses)) == 0 {
			continue
		}
		for _, p := range secretShapes {
			line = p.Re.ReplaceAllStringFunc(line, func(m string) string {
				if secretCounts(m) {
					return redact.Mark
				}
				return m
			})
		}
		line = maskAddresses(line)
		for _, v := range secretFreeValues(line) {
			line = strings.ReplaceAll(line, v, redact.Mark)
		}
		lines[i] = line
	}
	return strings.Join(lines, "")
}
