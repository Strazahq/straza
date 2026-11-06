package server

import (
	"crypto/subtle"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"time"

	"github.com/strazahq/straza/internal/config"
	"github.com/strazahq/straza/internal/server/ratelimit"
)

// Public-surface hardening: request-body
// caps everywhere, a per-IP rate limit on the world-open approver listener,
// a throttle on the standalone password login, and an opt-in bearer token
// for /metrics. All knobs live under config.Server; every middleware here
// is inert when its knob is zero/empty, so bare test configs and existing
// deployments keep byte-identical behavior until the Loader defaults apply.

// bulkSelfCapped are the routes that legitimately carry large bodies and
// already enforce their own bounds; the tighter global default must not
// shadow them: /mcp caps at server.mcpMaxBodyBytes, 4 MiB by default
// (gateway.go), /v1/audit/batch at 4 MiB (api_audit.go). App manifests
// self-cap at 1 MiB (api_apps.go) and need no exemption while the default
// cap is not below that.
var bulkSelfCapped = map[string]bool{
	"/mcp":            true,
	"/v1/audit/batch": true,
}

// selfCapped reports whether path caps its own body: a bulkSelfCapped route,
// or a server's own gateway endpoint /mcp/{server}, which is the /mcp
// handler and its server.mcpMaxBodyBytes cap.
func selfCapped(path string) bool {
	if bulkSelfCapped[path] {
		return true
	}
	server, ok := strings.CutPrefix(path, "/mcp/")
	return ok && server != "" && !strings.Contains(server, "/")
}

// bodyCap bounds every request body at limit bytes (0 = disabled). Reads
// past the cap fail, so an oversize body surfaces as the handler's own
// malformed-body 400. The point is that a giant unauthenticated POST can
// no longer balloon memory, not a prettier status code. A self-capped
// route takes no byte cap here and keeps the server's bodyReadTimeout, and
// its handler extends the read itself once the session verified.
func bodyCap(limit int64, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if limit > 0 && r.Body != nil && !selfCapped(r.URL.Path) {
			r.Body = http.MaxBytesReader(w, r.Body, limit)
		}
		next.ServeHTTP(w, r)
	})
}

// bodyReadTimeout is the ReadTimeout of both servers. It bounds the time to
// read a request's header and body, so a client that stops short of the
// last byte of its body cannot hold the buffered body and its connection for
// ever. It is the 30 seconds the straza and strazactl clients give a whole
// request, so a body still arriving after it belongs to a request its client
// has given up on, and the 5 second header bound would cut a multi-MiB
// upload on a slow uplink. It spares the long replies: net/http clears the
// read deadline once the body is read to its end, or at once for a request
// without a body, and a handler that stops reading early reads nothing more
// under it. TestReadTimeoutSparesLongReplies pins that on HTTP/1.1 and
// HTTP/2.
const bodyReadTimeout = 30 * time.Second

// bulkBodyReadTimeout is the time the body of a self-capped route gets to
// arrive. bodyReadTimeout is sized to the 1 MiB default cap of the other
// routes, and these take up to config.MCPMaxBodyBytesCeiling, so they get
// that many times as long at the same pace: 4 minutes for 8 MiB. The
// handlers of these routes, handleMCPPost and handleAuditBatch, give it
// with extendBodyRead once the session token verified, right before they
// read the body. A request refused before that keeps bodyReadTimeout.
const bulkBodyReadTimeout = bodyReadTimeout * (config.MCPMaxBodyBytesCeiling >> 20)

// extendBodyRead gives the body of r until d from now to arrive, past the
// server's ReadTimeout. A handler calls it only after every refusal that
// reads no body, the session check first. On HTTP/1.1 net/http reads an
// unread body of under 256 KiB to its end before it writes the answer's
// header, so a refused client's stalled body would hold its answer for d.
// On HTTP/1.1 a request without a body is left alone, because net/http
// already reads past its end with no deadline, and a deadline set now would
// end its stream at d. On HTTP/2 a request's Body is never http.NoBody, so
// the deadline is set, and it is harmless there: it only ends the stream's
// request body, never its context. A writer that takes no deadline keeps
// the server's.
func extendBodyRead(w http.ResponseWriter, r *http.Request, d time.Duration) {
	if r.Body != nil && r.Body != http.NoBody {
		_ = http.NewResponseController(w).SetReadDeadline(time.Now().Add(d))
	}
}

// clientIP is the transport peer's address, never X-Forwarded-For
// (spoofable by the very clients being limited). It is the source address
// an audit record carries, in full. The per-address limits key by limitKey.
func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// limitKey is the key every per-address limit counts under: an IPv4 peer's
// full address, and an IPv6 peer's /64, because one IPv6 host can take any
// address of the /64 its network hands it and would otherwise get a fresh
// budget per address. An IPv4 address mapped into IPv6, or translated into
// the well-known NAT64 prefix 64:ff9b::/96, counts as that IPv4 address. A
// peer that names no address keys as clientIP gives it. Behind a reverse
// proxy all clients share the proxy's address. The deployment docs say so,
// and the knobs exist to raise or disable the limits there.
func limitKey(r *http.Request) string {
	host := clientIP(r)
	addr, err := netip.ParseAddr(host)
	if err != nil {
		return host
	}
	if addr = addr.Unmap(); addr.Is4() {
		return addr.String()
	}
	if b := addr.As16(); nat64.Contains(addr.WithZone("")) {
		return netip.AddrFrom4([4]byte(b[12:])).String()
	}
	return netip.PrefixFrom(addr.WithZone(""), 64).Masked().String()
}

// nat64 is the well-known NAT64 prefix of RFC 6052, whose last 32 bits are
// the IPv4 address a translator stands in for.
var nat64 = netip.MustParsePrefix("64:ff9b::/96")

// limitScope names what limitKey counted under key, for a refusal sentence:
// a network for an IPv6 /64, and an address otherwise.
func limitScope(key string) string {
	if strings.Contains(key, "/") {
		return "network"
	}
	return "address"
}

// perIPLimit throttles every request through it by limitKey (rps <= 0 =
// disabled). It wraps the DEDICATED approver listener (the one surface
// designed to face hostile networks) so scanners and floods burn their
// budget before reaching any handler.
func perIPLimit(l *ratelimit.Limiter, rps float64, next http.Handler) http.Handler {
	if rps <= 0 {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !l.Allow(limitKey(r), rps) {
			w.Header().Set("Retry-After", "1")
			apiError(w, http.StatusTooManyRequests, "rate limited. Retry shortly")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// loginLimit throttles ONLY the built-in issuer's password submits, POST
// /oidc/device and POST /oidc/authorize, by limitKey from one budget.
// bcrypt keeps guesses expensive and uniform-timed, and this bounds the
// request VOLUME an online guesser can spend at all. Other issuer endpoints
// stay unthrottled: the device-code token poll is legitimately frequent.
func loginLimit(l *ratelimit.Limiter, rps float64, next http.Handler) http.Handler {
	if rps <= 0 {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && (r.URL.Path == "/oidc/device" || r.URL.Path == "/oidc/authorize") &&
			!l.Allow(limitKey(r), rps) {
			w.Header().Set("Retry-After", "1")
			apiError(w, http.StatusTooManyRequests, "too many login attempts. Retry shortly")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// deviceStartLimit throttles the built-in issuer's device sign-in starts,
// POST /oidc/device_authorization, by limitKey. Each start holds a grant in
// memory until it expires, so this bounds how fast one address fills the
// issuer's table. It takes a limiter of its own, so starts never spend the
// password-guess budget of loginLimit.
func deviceStartLimit(l *ratelimit.Limiter, rps float64, next http.Handler) http.Handler {
	if rps <= 0 {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && r.URL.Path == "/oidc/device_authorization" {
			if key := limitKey(r); !l.Allow(key, rps) {
				w.Header().Set("Retry-After", "1")
				apiError(w, http.StatusTooManyRequests, "Too many sign-in starts from this "+limitScope(key)+". Wait a few seconds and sign in again.")
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

// grantLimit throttles the enterprise NHI token endpoint (POST /oidc/token)
// by limitKey. Signature checks are cheap for the caller and cost the
// server CPU per attempt; this bounds the request VOLUME an online abuser
// can spend against the one unauthenticated credential route the enterprise
// profile serves. Legit NHIs hit it once per session start.
func grantLimit(l *ratelimit.Limiter, rps float64, next http.Handler) http.Handler {
	if rps <= 0 {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && r.URL.Path == "/oidc/token" && !l.Allow(limitKey(r), rps) {
			w.Header().Set("Retry-After", "1")
			apiError(w, http.StatusTooManyRequests, "too many token requests. Retry shortly")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// securityHeaders sets the browser-hardening baseline on every response:
// MIME sniffing off, no framing ever (both the
// legacy header and its CSP successor, one for old proxies and one for
// current browsers; strazad serves the Approve button, so clickjacking is
// the concrete threat), and no Referrer leakage from the SPAs to the IdP or
// anywhere else. Global on purpose: JSON APIs ignore the extra headers, and
// a carve-out list would rot. A full resource CSP (script/style/connect
// sources) is deliberately NOT set here: the SPAs use inline styles and an
// enterprise deployment fetches a cross-origin IdP, so a global CSP would
// break deployments silently.
func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("Content-Security-Policy", "frame-ancestors 'none'")
		next.ServeHTTP(w, r)
	})
}

// metricsAuth gates /metrics behind a bearer token when one is configured
// (empty = today's open behavior, for private-network scrapes). Constant-
// time compare; Prometheus carries the token via bearer_token in the scrape
// config.
func metricsAuth(token string, next http.Handler) http.Handler {
	if token == "" {
		return next
	}
	want := []byte(token)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got := []byte(bearerToken(r))
		if len(got) != len(want) || subtle.ConstantTimeCompare(got, want) != 1 {
			w.Header().Set("WWW-Authenticate", "Bearer")
			apiError(w, http.StatusUnauthorized, "metrics token required (server.metricsToken)")
			return
		}
		next.ServeHTTP(w, r)
	})
}
