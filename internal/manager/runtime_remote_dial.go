package manager

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unicode"

	"github.com/strazahq/straza/internal/hostclass"
	"github.com/strazahq/straza/internal/redact"
	"golang.org/x/net/idna"
)

// ErrDialRefused is what every dial refusal matches with errors.Is, so a
// route can answer the refusal's own sentence with no frame around it.
var ErrDialRefused = errors.New("the address is one Straza does not dial")

// dialRefusal is the refusal of a dial for a server: an address of a class
// strazad does not reach, a name for its own host, a host that is neither
// a name nor an address, or an address with no host. Its sentence names
// the server, the host as the manifest names it, the address it resolved
// to when that differs, its class and what an administrator does about it.
type dialRefusal struct {
	server string
	host   string
	addr   netip.Addr // unset for a loopback name
	class  string     // "" for a host Straza does not read
	url    string     // set for an address with no host
}

func (e *dialRefusal) Error() string {
	host := visible(e.host)
	switch {
	case e.url != "":
		return fmt.Sprintf("Straza does not dial the server %s because its address %s has no host. "+
			"An administrator publishes the address with a DNS name or an IP address.", e.server, visible(e.url))
	case e.class == "":
		return fmt.Sprintf("Straza does not dial %s for the server %s because it is neither a DNS name nor an IP address as Straza reads them. "+
			"An administrator publishes the address with a DNS name or an IP address.", host, e.server)
	}
	is, words := "it is", classWords(e.class)
	if !e.addr.IsValid() {
		words = "a name for strazad's own host, as localhost and every name under .localhost are"
	} else if a, err := netip.ParseAddr(strings.TrimSuffix(e.host, ".")); err != nil || a != e.addr {
		is = "it resolves to " + e.addr.String() + ","
	}
	next := "An administrator publishes an address other machines can reach."
	if e.class == hostclass.Loopback {
		next = "An administrator publishes an address other machines can reach, or sets apps.allowLoopbackUpstreams when strazad runs beside the server on purpose."
	}
	return fmt.Sprintf("Straza does not dial %s for the server %s because %s %s. %s", host, e.server, is, words, next)
}

// Is matches ErrDialRefused.
func (e *dialRefusal) Is(target error) bool { return target == ErrDialRefused }

// classWords says what an address of class is and where it reaches, for
// the refusal sentence.
func classWords(class string) string {
	switch class {
	case hostclass.Loopback:
		return "a loopback address on strazad's own host"
	case hostclass.Unspecified:
		return "an unspecified address, which strazad's own host answers"
	case hostclass.LinkLocal:
		return "a link-local address on strazad's own network link"
	}
	return "a cloud metadata address, which reaches the metadata service of strazad's cloud"
}

// visible spells s with each character that prints nothing, such as a
// direction override, as its code point, so the sentence reads as the
// host is stored.
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

// newTransport is the runtime's pooled transport. http.DefaultTransport
// keeps only 2 idle connections per host, which forces a fresh TCP dial for
// nearly every in-flight call at gateway rates, so the pool is wider, and
// every dial goes through dialContext. The proxy of the environment
// applies, as it does for every client strazad builds.
func (r *RemoteRuntime) newTransport() *http.Transport {
	t := http.DefaultTransport.(*http.Transport).Clone()
	t.MaxIdleConns = 512
	t.MaxIdleConnsPerHost = 256
	t.DialContext = r.dialContext
	return t
}

// dialContext dials address with the default transport's timeouts and the
// class check in the dialer's Control hook, which runs on every address a
// name resolves to, after resolution, so a name that rebinds to a refused
// address is refused like a literal. A dial to the proxy that applies to
// the manifest's address is not classed: the proxy is operator config, and
// refuseHost classed the manifest's host before the dial.
func (r *RemoteRuntime) dialContext(ctx context.Context, network, address string) (net.Conn, error) {
	d := net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second}
	if address == r.proxyAddr() {
		return d.DialContext(ctx, network, address)
	}
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return nil, err
	}
	d.Control = func(_, addr string, _ syscall.RawConn) error {
		ap, err := netip.ParseAddrPort(addr)
		if err != nil {
			return err
		}
		return r.refuse(host, ap.Addr())
	}
	return d.DialContext(ctx, network, address)
}

// proxyAddr answers the host and port the transport dials for the proxy
// that applies to the manifest's address, with the scheme's port when the
// proxy names none, as net/http does, or "" when no proxy applies.
func (r *RemoteRuntime) proxyAddr() string {
	if r.transport.Proxy == nil {
		return ""
	}
	req, err := http.NewRequest(http.MethodGet, r.spec.URL, nil)
	if err != nil {
		return ""
	}
	p, err := r.transport.Proxy(req)
	if err != nil || p == nil {
		return ""
	}
	port := p.Port()
	if port == "" {
		port = map[string]string{"http": "80", "https": "443", "socks5": "1080", "socks5h": "1080"}[p.Scheme]
	}
	return net.JoinHostPort(p.Hostname(), port)
}

// refuse answers the refusal of dialing addr, reached for host, or nil when
// its class is one strazad reaches. The loopback allowance lifts that class
// alone.
func (r *RemoteRuntime) refuse(host string, addr netip.Addr) error {
	class := hostclass.Refused(addr)
	if class == "" || (class == hostclass.Loopback && r.AllowLoopback) {
		return nil
	}
	return &dialRefusal{server: r.name, host: host, addr: addr, class: class}
}

// refuseHost classes the host of the manifest's address before the dial,
// because when a proxy applies the dial hook sees the proxy's address and
// the proxy resolves the host. A name outside ASCII is converted as
// net/http converts it before the dial, and refused when that fails. An
// address literal is classed as written, a trailing dot dropped, and a
// host a C resolver reads as an IPv4 address, such as 2130706433 or 127.1,
// is classed as that address. localhost and every name under .localhost
// name strazad's own host by definition, so the loopback allowance governs
// them. Any other name passes, host.docker.internal included, which names
// a private address the dial hook classes once resolved. A host that is
// neither a DNS name nor an address is refused, since Straza cannot say
// what a proxy makes of it, and an address with no host is refused as such.
func (r *RemoteRuntime) refuseHost() error {
	u, err := url.Parse(r.spec.URL)
	if err != nil {
		return nil
	}
	written := u.Hostname()
	if written == "" {
		return &dialRefusal{server: r.name, url: r.shownURL()}
	}
	host := strings.TrimSuffix(written, ".")
	if !isASCII(host) {
		if host, err = idna.Lookup.ToASCII(host); err != nil {
			return &dialRefusal{server: r.name, host: written}
		}
	}
	if addr, err := netip.ParseAddr(host); err == nil {
		return r.refuse(written, addr)
	}
	if addr, ok := inetAton(host); ok {
		return r.refuse(written, addr)
	}
	if lower := strings.ToLower(host); lower == "localhost" || strings.HasSuffix(lower, ".localhost") {
		if r.AllowLoopback {
			return nil
		}
		return &dialRefusal{server: r.name, host: written, class: hostclass.Loopback}
	}
	if !dnsName(host) {
		return &dialRefusal{server: r.name, host: written}
	}
	return nil
}

// isASCII reports whether s holds ASCII characters only.
func isASCII(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] >= 0x80 {
			return false
		}
	}
	return true
}

// inetAton reads host the way a C resolver reads an IPv4 address: one to
// four dot-separated parts, each decimal, 0x hex or 0-led octal, the last
// part filling the remaining bytes. It answers false for any other host.
func inetAton(host string) (netip.Addr, bool) {
	parts := strings.Split(host, ".")
	if len(parts) > 4 {
		return netip.Addr{}, false
	}
	vals := make([]uint64, len(parts))
	for i, p := range parts {
		base, digits := 10, p
		switch {
		case strings.HasPrefix(p, "0x") || strings.HasPrefix(p, "0X"):
			base, digits = 16, p[2:]
		case len(p) > 1 && p[0] == '0':
			base, digits = 8, p[1:]
		}
		v, err := strconv.ParseUint(digits, base, 32)
		if err != nil {
			return netip.Addr{}, false
		}
		vals[i] = v
	}
	last := len(parts) - 1
	for _, v := range vals[:last] {
		if v > 255 {
			return netip.Addr{}, false
		}
	}
	if vals[last] >= 1<<(8*(4-last)) {
		return netip.Addr{}, false
	}
	var a uint64
	for i, v := range vals[:last] {
		a |= v << (24 - 8*i)
	}
	a |= vals[last]
	if a > math.MaxUint32 {
		return netip.Addr{}, false
	}
	var b [4]byte
	binary.BigEndian.PutUint32(b[:], uint32(a))
	return netip.AddrFrom4(b), true
}

// dnsName reports whether host is a DNS name as a resolver reads one:
// labels of one to 63 ASCII letters, digits, hyphens and underscores that
// neither start nor end with a hyphen, at most 253 characters in all.
func dnsName(host string) bool {
	if host == "" || len(host) > 253 {
		return false
	}
	for _, label := range strings.Split(host, ".") {
		if label == "" || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, c := range label {
			if (c < 'a' || c > 'z') && (c < 'A' || c > 'Z') && (c < '0' || c > '9') && c != '-' && c != '_' {
				return false
			}
		}
	}
	return true
}

// refusalOf answers the dial refusal inside a connect error, or nil.
func refusalOf(err error) error {
	var refusal *dialRefusal
	if errors.As(err, &refusal) {
		return refusal
	}
	return nil
}

// shownURL is the manifest's address as a sentence may carry it, written
// as redact.URL writes it, with no user information, query, fragment or
// capability in its path, so a health detail, the ring and a probe's error
// never print one, whatever the manager's text mask reads.
func (r *RemoteRuntime) shownURL() string {
	return redact.URL(r.spec.URL)
}
