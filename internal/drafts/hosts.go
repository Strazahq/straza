package drafts

import (
	"fmt"
	"net/netip"
	"net/url"
	"strings"
	"unicode"

	"golang.org/x/net/idna"

	"github.com/strazahq/straza/internal/hostclass"
)

// The classes of a host a request from strazad should not reach: its own
// machine, a private network, a link-local address, or a cloud's metadata
// service. hostClass names them in the words of the risk sentences, and
// the manager's dial guard reads the same classes from hostclass.
const (
	hostLoopback    = hostclass.Loopback
	hostUnspecified = hostclass.Unspecified
	hostPrivate     = hostclass.Private
	hostLinkLocal   = hostclass.LinkLocal
	hostMetadata    = hostclass.Metadata
)

// hostOf answers the host of the address raw as written, without user
// information, port or brackets, and "" when raw names no host. An address
// the URL parser refuses is cut by hand, so a malformed path cannot hide
// its host from a check.
func hostOf(raw string) string {
	if u, err := url.Parse(raw); err == nil && u.Host != "" {
		return u.Hostname()
	}
	_, rest, ok := strings.Cut(raw, "://")
	if !ok {
		return ""
	}
	if end := strings.IndexAny(rest, "/?#"); end >= 0 {
		rest = rest[:end]
	}
	if at := strings.LastIndex(rest, "@"); at >= 0 {
		rest = rest[at+1:]
	}
	if strings.HasPrefix(rest, "[") {
		if end := strings.Index(rest, "]"); end > 0 {
			return rest[1:end]
		}
	}
	if colon := strings.LastIndex(rest, ":"); colon >= 0 {
		rest = rest[:colon]
	}
	return rest
}

// asciiHost answers host the way strazad's HTTP client dials it, in lower
// case and without a trailing dot, so github.com. and github.com read as
// one host. An address and a name written in ASCII are dialed as written,
// xn-- labels included, and a name with a character outside ASCII takes
// its ASCII form from the IDNA profile net/http uses for such a name. A
// name the profile refuses comes back lower-cased, with the profile's
// error.
func asciiHost(host string) (string, error) {
	h := strings.TrimSuffix(host, ".")
	if _, foreign := foreignLetter(h); !foreign {
		return strings.ToLower(h), nil
	}
	a, err := idna.Lookup.ToASCII(h)
	if err != nil {
		return strings.ToLower(h), err
	}
	return a, nil
}

// unicodeHost answers how host reads when a label is written in its
// punycode form, and false when no label is, or a label does not decode to
// printable text.
func unicodeHost(host string) (string, bool) {
	lower := strings.ToLower(host)
	if !strings.HasPrefix(lower, "xn--") && !strings.Contains(lower, ".xn--") {
		return "", false
	}
	// The lookup profile refuses a label that decodes to plain ASCII, such as
	// xn--github-, yet still returns the decoded text. That label reads as a
	// familiar name, so its reading stands whenever every character prints.
	u, err := idna.Lookup.ToUnicode(lower)
	if u == lower || (err != nil && strings.ContainsFunc(u, func(r rune) bool { return !unicode.IsPrint(r) })) {
		return "", false
	}
	return u, true
}

// hostClass names the class of host that a request from strazad should not
// reach, or answers "" for a host outside every class. It reads the name as
// written and contacts nothing, so only an address literal, the localhost
// names and the metadata name of Google Cloud are classed.
func hostClass(host string) string {
	h := strings.TrimSuffix(strings.ToLower(host), ".")
	switch {
	case h == "localhost" || strings.HasSuffix(h, ".localhost"):
		return hostLoopback
	case h == "metadata.google.internal":
		return hostMetadata
	}
	addr, err := netip.ParseAddr(h)
	if err != nil {
		return ""
	}
	return addrClass(addr)
}

// ContactClass names the class of addr that Contact refuses to dial:
// loopback, unspecified, link-local or cloud metadata, reading an
// IPv4-mapped IPv6 address as its IPv4 address first, and a NAT64
// (64:ff9b::/96) or 6to4 (2002::/16) address as the IPv4 address it
// embeds, since a gateway forwards it there, or answers "". A private
// address passes, because a proposed server may run on the operator's own
// network. The classes are hostclass's, so Contact refuses what the
// gateway would not dial.
func ContactClass(addr netip.Addr) string {
	return hostclass.Refused(addr)
}

// ASCIIHost answers host as strazad's HTTP client dials it, as asciiHost
// does, and lower-cased when the IDNA profile refuses it.
func ASCIIHost(host string) string {
	h, _ := asciiHost(host)
	return h
}

// addrClass names the class of addr, reading an IPv4-mapped IPv6 address
// as its IPv4 address first, or answers "".
func addrClass(addr netip.Addr) string {
	return hostclass.Addr(addr)
}

// foreignLetter answers the first character of host outside ASCII, which
// can make a name pass for another, and false when host is plain ASCII.
func foreignLetter(host string) (rune, bool) {
	for _, r := range host {
		if r > unicode.MaxASCII {
			return r, true
		}
	}
	return 0, false
}

// visible spells s with each character that prints nothing, such as a
// direction override, as its code point, so a sentence that quotes s reads
// the way it is stored.
func visible(s string) string {
	var b strings.Builder
	for _, r := range s {
		if unicode.IsGraphic(r) {
			b.WriteRune(r)
		} else {
			fmt.Fprintf(&b, "U+%04X", r)
		}
	}
	return b.String()
}

// runeWords spells one character for a sentence: the character and its
// code point, or the code point alone when the character prints nothing.
func runeWords(r rune) string {
	if unicode.IsGraphic(r) && !unicode.IsSpace(r) {
		return fmt.Sprintf("%c (U+%04X)", r, r)
	}
	return fmt.Sprintf("U+%04X", r)
}
