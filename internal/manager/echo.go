package manager

import (
	"bytes"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"unicode/utf16"
	"unicode/utf8"
)

// ErrCredentialEcho is the error of a call whose reply Straza withheld,
// because the reply held the credential Straza injected for the call, whole
// or in a full standard encoding. The gateway answers it with a refusal of
// its own and records it, so its text never reaches an agent.
var ErrCredentialEcho = errors.New("the reply held the credential Straza injects for this server, so Straza withheld it")

// echoMin is the length in bytes of the shortest credential a reply is
// checked for. A shorter value, such as a four-digit PIN, turns up by chance
// in ordinary replies, so matching it would withhold replies that echoed
// nothing, and a credential that short is guessable anyway.
const echoMin = 8

// echoValues answers the values a reply must not hold for a credential
// value injected through template: the value as stored, the value as the
// template renders it when that differs, because an upstream may echo the
// whole header or environment value, and the user and password of a
// pre-encoded Basic credential, which an upstream decodes to check it. A
// value shorter than echoMin answers none.
func echoValues(template, value string) []string {
	if len(value) < echoMin {
		return nil
	}
	out := []string{value}
	if rendered := strings.ReplaceAll(template, SecretPlaceholder, value); rendered != value {
		out = append(out, rendered)
	}
	if raw, err := base64.StdEncoding.DecodeString(value); err == nil && len(raw) >= echoMin && utf8.Valid(raw) && bytes.ContainsRune(raw, ':') {
		out = append(out, string(raw))
	}
	return out
}

// echoDepth is how many decodings deep the check reads a reply's text,
// enough for base64 of JSON text that holds a percent-escaped value.
const echoDepth = 4

// echoRunMin is the shortest base64 run the check decodes: echoMin bytes in
// base64, since a shorter run cannot hold a value.
const echoRunMin = 11

// echoes reports whether a reply, any value that encodes as JSON, or err
// holds one of values. It reads every string, key and number of the reply as
// the agent would receive it, which covers content items, structured
// content, every _meta and the error text. A value matches whole or as
// base64 or hex. Text that decodes is read again decoded, to echoDepth: JSON
// string escapes wherever they stand, percent escapes in either case and
// form encoding's + for a space, and every base64 run in either alphabet,
// padded or not. The decoded text
// read is bounded by a multiple of the reply's size, so the cost stays
// linear, and past that bound the reply's own text is still read. A fragment
// of a value never matches, and neither does a value shorter than echoMin.
func echoes(values []string, reply any, err error) bool {
	needles := echoNeedles(values)
	if len(needles) == 0 {
		return false
	}
	wire, mErr := json.Marshal(reply)
	if mErr != nil {
		// The gateway encodes the reply the same way, so a reply that does
		// not encode reaches no agent either.
		wire = nil
	}
	e := &echoScan{needles: needles, budget: 8*len(wire) + 64<<10}
	if err != nil {
		e.budget += 8 * len(err.Error())
		if e.holds(err.Error(), 0) {
			return true
		}
	}
	return len(wire) > 0 && walkJSON(wire, func(s string) bool { return e.holds(s, 0) })
}

// echoNeedles answers each value of at least echoMin bytes as it stands, as
// unpadded base64 in both alphabets and as hex in both cases. The base64
// forms find a value whose encoding sits aligned inside other text, and an
// unpadded form also finds the padded one, which starts with it.
func echoNeedles(values []string) []string {
	var out []string
	for _, v := range values {
		if len(v) < echoMin {
			continue
		}
		b := []byte(v)
		h := hex.EncodeToString(b)
		for _, n := range []string{v, base64.RawStdEncoding.EncodeToString(b), base64.RawURLEncoding.EncodeToString(b), h, strings.ToUpper(h)} {
			if !slices.Contains(out, n) {
				out = append(out, n)
			}
		}
	}
	return out
}

// echoScan is one check of a reply. budget is how many bytes of decoded text
// it may still read.
type echoScan struct {
	needles []string
	budget  int
}

// holds reports whether s, or text s decodes to within echoDepth, holds a
// needle.
func (e *echoScan) holds(s string, depth int) bool {
	if holdsAny(s, e.needles) {
		return true
	}
	if depth == echoDepth {
		return false
	}
	return decodings(s, func(d string) bool {
		if len(d) > e.budget {
			return false
		}
		e.budget -= len(d)
		return e.holds(d, depth+1)
	})
}

// decodings calls f with each text s decodes to, and stops at the first f
// that answers true, which it then answers: s with its JSON escapes undone,
// s with its percent escapes undone, again with each + read as a space as
// form encoding writes one, and the bytes of each base64 run in s, and of
// the part of a run after its last - or _, since a standard run can follow
// a hyphenated word.
func decodings(s string, f func(string) bool) bool {
	if strings.Contains(s, `\`) {
		if u := unescapeJSON(s); u != s && f(u) {
			return true
		}
	}
	if strings.Contains(s, "%") {
		if u := unescapePercent(s); u != s && f(u) {
			return true
		}
	}
	if strings.Contains(s, "+") && f(unescapePercent(strings.ReplaceAll(s, "+", " "))) {
		return true
	}
	for i := 0; i < len(s); {
		if !isBase64(s[i]) {
			i++
			continue
		}
		j := i
		for j < len(s) && isBase64(s[j]) {
			j++
		}
		run := s[i:j]
		i = j
		if len(run) < echoRunMin {
			continue
		}
		if raw, ok := decodeBase64Run(run); ok && f(raw) {
			return true
		}
		if k := strings.LastIndexAny(run, "-_"); k >= 0 && len(run)-k-1 >= echoRunMin {
			if raw, ok := decodeBase64Run(run[k+1:]); ok && f(raw) {
				return true
			}
		}
	}
	return false
}

// unescapeJSON undoes the JSON string escapes of s wherever they stand,
// \" \\ \/ \b \f \n \r \t and \uXXXX, a surrogate pair included, and
// leaves every other backslash as it is.
func unescapeJSON(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); i++ {
		if s[i] != '\\' || i+1 == len(s) {
			b.WriteByte(s[i])
			continue
		}
		if c, ok := jsonEscapes[s[i+1]]; ok {
			b.WriteByte(c)
			i++
			continue
		}
		r, ok := hex4(s, i+2)
		if s[i+1] != 'u' || !ok {
			b.WriteByte(s[i])
			continue
		}
		i += 5
		if utf16.IsSurrogate(r) && i+6 < len(s) && s[i+1] == '\\' && s[i+2] == 'u' {
			if lo, ok := hex4(s, i+3); ok {
				if pair := utf16.DecodeRune(r, lo); pair != utf8.RuneError {
					r = pair
					i += 6
				}
			}
		}
		b.WriteRune(r)
	}
	return b.String()
}

// jsonEscapes maps the character after a backslash in a JSON string to the
// byte it stands for, for every escape but \u.
var jsonEscapes = map[byte]byte{'"': '"', '\\': '\\', '/': '/', 'b': '\b', 'f': '\f', 'n': '\n', 'r': '\r', 't': '\t'}

// hex4 answers the four hex digits of s at i as a rune.
func hex4(s string, i int) (rune, bool) {
	if i+4 > len(s) {
		return 0, false
	}
	var r rune
	for _, c := range []byte(s[i : i+4]) {
		v, ok := unhex(c)
		if !ok {
			return 0, false
		}
		r = r<<4 | rune(v)
	}
	return r, true
}

// walkJSON calls f with every string, key and number of the JSON text data
// and stops at the first f that answers true, which it then answers. Text
// that stops being JSON ends the walk.
func walkJSON(data []byte, f func(string) bool) bool {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	for {
		tok, err := dec.Token()
		if err != nil {
			return false
		}
		switch t := tok.(type) {
		case string:
			if f(t) {
				return true
			}
		case json.Number:
			if f(string(t)) {
				return true
			}
		}
	}
}

// unescapePercent undoes every percent escape of s, in either case, and
// leaves a % that starts no escape, and every +, as they are.
func unescapePercent(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); i++ {
		if s[i] == '%' && i+2 < len(s) {
			hi, ok1 := unhex(s[i+1])
			lo, ok2 := unhex(s[i+2])
			if ok1 && ok2 {
				b.WriteByte(hi<<4 | lo)
				i += 2
				continue
			}
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

// unhex answers the value of the hex digit c.
func unhex(c byte) (byte, bool) {
	switch {
	case '0' <= c && c <= '9':
		return c - '0', true
	case 'a' <= c && c <= 'f':
		return c - 'a' + 10, true
	case 'A' <= c && c <= 'F':
		return c - 'A' + 10, true
	}
	return 0, false
}

// isBase64 reports whether c belongs to either base64 alphabet. Padding
// ends a run, so a run is decoded unpadded.
func isBase64(c byte) bool {
	return 'A' <= c && c <= 'Z' || 'a' <= c && c <= 'z' || '0' <= c && c <= '9' || c == '+' || c == '/' || c == '-' || c == '_'
}

// decodeBase64Run decodes a run of base64 characters without padding, in the
// URL alphabet when it holds - or _, and answers false when it does not
// decode. A run one character longer than base64 can be lost its last
// character, which belongs to the text after it.
func decodeBase64Run(run string) (string, bool) {
	enc := base64.RawStdEncoding
	if strings.ContainsAny(run, "-_") {
		enc = base64.RawURLEncoding
	}
	if len(run)%4 == 1 {
		run = run[:len(run)-1]
	}
	raw, err := enc.DecodeString(run)
	return string(raw), err == nil
}

// holdsAny reports whether s holds one of needles.
func holdsAny(s string, needles []string) bool {
	for _, n := range needles {
		if strings.Contains(s, n) {
			return true
		}
	}
	return false
}
