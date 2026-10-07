// Package redact holds the shared, conservative credential-shape battery used
// wherever Straza must scrub secrets before showing text to a human or storing
// a preview: the conversation-capture redactor (internal/agentguard), the
// captured-content leak detector (internal/sentinel), and the approval args
// preview. The patterns are deliberately narrow (recognizable credential
// shapes only) because a false positive destroys the utility of the text it
// scrubs.
//
// The four shapes below are the single source of truth for every caller. The
// PEM matcher is intentionally NOT the whole story: redaction masks a COMPLETE
// block (PEMBlock, below) while the leak DETECTOR matches a bare private-key
// header even with no END delimiter. The jobs differ, so the detector keeps its
// own header matcher rather than sharing this one (both behaviours are pinned
// by their packages' tests).
package redact

import (
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"unicode/utf8"
)

// Mark replaces a matched secret in redacted output.
const Mark = "[REDACTED]"

// PreviewMaxBytes caps a stored approval args preview. It is a fixed const:
// a preview is a human glance, not the forensic copy audit already keeps.
const PreviewMaxBytes = 2048

// The shared credential matchers. Conservative shapes only.
var (
	// AWSKey is an AWS access key id.
	AWSKey = regexp.MustCompile(`AKIA[0-9A-Z]{16}`)
	// GitHubToken covers the ghp_/gho_/ghu_/ghs_/ghr_ token family.
	GitHubToken = regexp.MustCompile(`gh[pousr]_[A-Za-z0-9]{20,}`)
	// BearerToken masks an Authorization: Bearer value.
	BearerToken = regexp.MustCompile(`(?i)bearer\s+[A-Za-z0-9._~+/=-]{16,}`)
	// PEMBlock matches a COMPLETE PEM block (BEGIN…END), so redaction masks the
	// key body, not just its header. The leak detector uses a header-only
	// matcher instead (see the package doc).
	PEMBlock = regexp.MustCompile(`(?s)-----BEGIN [^-]+-----.*?-----END [^-]+-----`)
	// StrazaToken matches a Straza scim/api token (wst_/wat_).
	StrazaToken = regexp.MustCompile(`w(?:st|at)_[A-Za-z0-9_-]{20,}`)
)

// Pattern is one labeled matcher in the redaction battery.
type Pattern struct {
	Label string
	Re    *regexp.Regexp
}

// Patterns is the ordered redaction battery. The order is fixed, so the
// output of Redact stays byte-stable.
var Patterns = []Pattern{
	{"AWS access key id", AWSKey},
	{"GitHub token", GitHubToken},
	{"bearer token", BearerToken},
	{"PEM block", PEMBlock},
	{"Straza token", StrazaToken},
}

// Redact masks every recognizable credential shape in s with Mark.
func Redact(s string) string {
	for _, p := range Patterns {
		s = p.Re.ReplaceAllString(s, Mark)
	}
	return s
}

// URL returns s safe to log: userinfo is dropped entirely (user:password@
// forms; NATS client URLs are the live case), a query string is replaced
// by the marker "?…" (Splunk-HEC-style receivers carry their token in the
// query, which is why the sink URL check deliberately allows one), a
// fragment by "#…", and each path segment Capability reads as a generated
// secret by Mark, written %5BREDACTED%5D. Scheme, host, and the rest of the
// path survive; that is what an operator greps a boot log for.
// An unparseable value returns a fixed placeholder, never the raw input:
// the parse failure is exactly when we cannot say which part is secret.
func URL(s string) string {
	u, err := url.Parse(s)
	if err != nil {
		return "<unparseable url>"
	}
	u.User = nil
	if p := u.EscapedPath(); Capability(p) {
		segments := strings.Split(p, "/")
		for i, seg := range segments {
			if Capability(seg) {
				segments[i] = url.PathEscape(Mark)
			}
		}
		raw := strings.Join(segments, "/")
		if path, err := url.PathUnescape(raw); err == nil {
			u.Path, u.RawPath = path, raw
		}
	}
	tail := ""
	if u.RawQuery != "" || u.ForceQuery {
		u.RawQuery, u.ForceQuery, tail = "", false, "?…"
	}
	if u.Fragment != "" {
		u.Fragment, u.RawFragment, tail = "", "", tail+"#…"
	}
	return u.String() + tail
}

// URLPattern finds the addresses inside a text: a scheme, "://" and every
// character up to a space, a double quote, an angle bracket or a backtick.
// A single quote may stand inside an address, in a password or a query
// value, so it ends one only as its last character or before a comma or a
// closing bracket, as between the quoted items of a list, and the
// punctuation marks that end a sentence or close a parenthesis end one as
// its last character too.
var URLPattern = regexp.MustCompile("(?i)\\b[a-z][a-z0-9+.-]*://(?:[^\\s\"'<>`]|'[^\\s\"'<>`,)\\]])*[^\\s\"'<>`.,;:)]")

// URLs returns s with every address URLPattern finds in it written as URL
// writes it, so a line of text carries no user information, no query, no
// fragment and no capability in the path of an address.
func URLs(s string) string {
	return URLPattern.ReplaceAllStringFunc(s, URL)
}

// Host returns only scheme://host of s, the push-endpoint idiom, where the
// PATH is the secret (an RFC 8030 push resource path is a per-subscription
// capability). Unparseable values return a fixed placeholder.
func Host(s string) string {
	u, err := url.Parse(s)
	if err != nil || u.Host == "" {
		return "<unparseable url>"
	}
	return u.Scheme + "://" + u.Host
}

// SanitizeURLError rewrites err when it IS a *url.Error so its embedded URL
// is redacted via redactURL (URL for general targets, Host for
// capability-path endpoints). net/http wraps every transport failure in
// *url.Error, whose Error() prints the FULL request URL, so a failing
// webhook sink or push endpoint would otherwise echo its token into the log
// on every retry. Call it directly on http.Client.Do's error, BEFORE adding
// outer context: it deliberately touches only a top-level *url.Error (a
// wrapped one would mean dropping someone else's context to reach it) and
// returns anything else unchanged. The rebuilt error keeps the same
// *url.Error type and the same inner error, so errors.As(*url.Error),
// errors.Is(context.DeadlineExceeded) and friends keep working; only the
// printed URL changes.
func SanitizeURLError(err error, redactURL func(string) string) error {
	ue, ok := err.(*url.Error) //nolint:errorlint // top-level only, by design (see doc)
	if !ok {
		return err
	}
	return &url.Error{Op: ue.Op, URL: redactURL(ue.URL), Err: ue.Err}
}

// offending reports whether r is a display-hostile code point that must be
// escaped before the text is rendered verbatim in a mono block: a C0/C1 control
// (except \n and \t, which are legitimate in JSON indent and multiline
// commands), a bidi control, or a zero-width/invisible mark. These let an agent
// make the DISPLAYED call differ from the real one (ANSI rewrites, RLO
// reordering, hidden joiners), defeating informed consent. Homoglyphs are
// deliberately out of scope (the honesty line + audit carry that residual risk).
func offending(r rune) bool {
	switch {
	case r == '\n' || r == '\t':
		return false
	case r < 0x20: // C0 controls (incl. ESC, CR)
		return true
	case r >= 0x80 && r <= 0x9F: // C1 controls
		return true
	case r >= 0x202A && r <= 0x202E: // bidi embeddings/overrides
		return true
	case r >= 0x2066 && r <= 0x2069: // bidi isolates
		return true
	}
	switch r {
	case 0x061C, 0x200E, 0x200F, // bidi marks (ALM, LRM, RLM)
		0x200B, 0x200C, 0x200D, 0x2060, 0xFEFF: // zero-width / invisible
		return true
	}
	return false
}

// Neutralize makes s safe to render verbatim in a mono block: every offending
// code point (see offending) becomes its uniform visible escape `\uXXXX`
// (uppercase 4-hex), and each byte of invalid UTF-8 becomes `\xNN`. Benign text
// (including \n and \t) is untouched. The result is inert ASCII wherever the
// input was dangerous, so a truncation cut through an escape only ever leaves
// harmless ASCII, never re-exposes the original character.
func Neutralize(s string) string {
	if !needsNeutralize(s) {
		return s
	}
	var b strings.Builder
	b.Grow(len(s) + 8)
	for i := 0; i < len(s); {
		r, size := utf8.DecodeRuneInString(s[i:])
		switch {
		case r == utf8.RuneError && size == 1:
			fmt.Fprintf(&b, `\x%02X`, s[i])
			i++
		case offending(r):
			fmt.Fprintf(&b, `\u%04X`, r)
			i += size
		default:
			b.WriteString(s[i : i+size])
			i += size
		}
	}
	return b.String()
}

func needsNeutralize(s string) bool {
	for i := 0; i < len(s); {
		r, size := utf8.DecodeRuneInString(s[i:])
		if (r == utf8.RuneError && size == 1) || offending(r) {
			return true
		}
		i += size
	}
	return false
}

// Preview returns a bounded, redaction-first, display-safe preview of raw for a
// human glance on an approval surface. The pipeline is redact → neutralize →
// measure → cap: secrets are masked and display-hostile code points escaped
// BEFORE the length is taken, so a secret or a bidi trick straddling the
// truncation cut is neutralized first (never half-stored) and bytes reflects the
// FINAL displayable size: the redacted+neutralized length before truncation,
// NOT the raw size and NOT the audit copy's size. When that exceeds max the
// middle is elided around a "\n...[N bytes elided]...\n" marker with a ~3:1
// head:tail split, and truncated is true. The result never exceeds max bytes and
// never splits a UTF-8 rune. max <= 0 disables the cap.
func Preview(raw string, max int) (preview string, truncated bool, bytes int) {
	red := Neutralize(Redact(raw))
	bytes = len(red)
	if max <= 0 || bytes <= max {
		return red, false, bytes
	}
	// Reserve the marker at its widest (N up to the full redacted length) so the
	// assembled head+marker+tail can never exceed max even after rune-safe cuts
	// shrink the kept slices (which only grows N, never the reserved width).
	reserve := len(fmt.Sprintf("\n...[%d bytes elided]...\n", bytes))
	room := max - reserve
	if room < 0 {
		room = 0
	}
	head := room * 3 / 4 // ~3:1 so the head (tool name, first args) dominates
	tail := room - head
	headStr := safeHead(red, head)
	tailStr := safeTail(red, tail)
	elided := bytes - len(headStr) - len(tailStr)
	marker := fmt.Sprintf("\n...[%d bytes elided]...\n", elided)
	return headStr + marker + tailStr, true, bytes
}

// safeHead returns the longest prefix of s at most n bytes long that ends on a
// UTF-8 rune boundary.
func safeHead(s string, n int) string {
	if n >= len(s) {
		return s
	}
	if n <= 0 {
		return ""
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}

// safeTail returns the longest suffix of s at most n bytes long that starts on
// a UTF-8 rune boundary.
func safeTail(s string, n int) string {
	if n >= len(s) {
		return s
	}
	if n <= 0 {
		return ""
	}
	start := len(s) - n
	for start < len(s) && !utf8.RuneStart(s[start]) {
		start++
	}
	return s[start:]
}
