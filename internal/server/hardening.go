package server

import (
	"crypto/subtle"
	"net"
	"net/http"
	"strings"

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
// shadow them: /mcp caps at 4 MiB (gateway.go), /v1/audit/batch at 4 MiB
// (api_audit.go). App manifests self-cap at 1 MiB (api_apps.go) and need no
// exemption while the default cap is not below that.
var bulkSelfCapped = map[string]bool{
	"/mcp":            true,
	"/v1/audit/batch": true,
}

// selfCapped reports whether path caps its own body: a bulkSelfCapped route,
// or a server's own gateway endpoint /mcp/{server}, which is the /mcp
// handler and its 4 MiB cap.
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
// no longer balloon memory, not a prettier status code.
func bodyCap(limit int64, next http.Handler) http.Handler {
	if limit <= 0 {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !selfCapped(r.URL.Path) && r.Body != nil {
			r.Body = http.MaxBytesReader(w, r.Body, limit)
		}
		next.ServeHTTP(w, r)
	})
}

// clientIP is the rate-limit key: the transport peer, never X-Forwarded-For
// (spoofable by the very clients being limited). Behind a reverse proxy all
// clients share the proxy's address; the deployment docs say so and the
// knobs exist to raise/disable the limits there.
func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// perIPLimit throttles every request through it by client IP (rps <= 0 =
// disabled). It wraps the DEDICATED approver listener (the one surface
// designed to face hostile networks) so scanners and floods burn their
// budget before reaching any handler.
func perIPLimit(l *ratelimit.Limiter, rps float64, next http.Handler) http.Handler {
	if rps <= 0 {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !l.Allow(clientIP(r), rps) {
			w.Header().Set("Retry-After", "1")
			apiError(w, http.StatusTooManyRequests, "rate limited. Retry shortly")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// loginLimit throttles ONLY the standalone issuer's password submit (POST
// /oidc/device) by client IP. bcrypt keeps guesses expensive and uniform-
// timed; this bounds the request VOLUME an online guesser can spend at all.
// Other issuer endpoints stay unthrottled: the device-code token poll is
// legitimately frequent.
func loginLimit(l *ratelimit.Limiter, rps float64, next http.Handler) http.Handler {
	if rps <= 0 {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && r.URL.Path == "/oidc/device" && !l.Allow(clientIP(r), rps) {
			w.Header().Set("Retry-After", "1")
			apiError(w, http.StatusTooManyRequests, "too many login attempts. Retry shortly")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// grantLimit throttles the enterprise NHI token endpoint (POST /oidc/token)
// by client IP. Signature checks are cheap for the caller and cost the
// server CPU per attempt; this bounds the request VOLUME an online abuser
// can spend against the one unauthenticated credential route the enterprise
// profile serves. Legit NHIs hit it once per session start.
func grantLimit(l *ratelimit.Limiter, rps float64, next http.Handler) http.Handler {
	if rps <= 0 {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && r.URL.Path == "/oidc/token" && !l.Allow(clientIP(r), rps) {
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
