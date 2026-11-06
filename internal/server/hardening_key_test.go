package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/strazahq/straza/internal/server/ratelimit"
)

// TestLimitKey pins the key every per-address limit counts under: an IPv4
// peer by its full address, an IPv6 peer by its /64, and an IPv4 address
// mapped into IPv6 as IPv4. A RemoteAddr that names no address keys as
// itself, as it did before.
func TestLimitKey(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name, remote, want string
	}{
		{"ipv4 keeps the full address", "198.51.100.7:51234", "198.51.100.7"},
		{"ipv6 keys by its /64", "[2001:db8:1:2:aaaa:bbbb:cccc:dddd]:443", "2001:db8:1:2::/64"},
		{"ipv6 host bits do not matter", "[2001:db8:1:2::1]:443", "2001:db8:1:2::/64"},
		{"the next /64 is another key", "[2001:db8:1:3::1]:443", "2001:db8:1:3::/64"},
		{"a mapped ipv4 address is ipv4", "[::ffff:198.51.100.7]:51234", "198.51.100.7"},
		{"a zone does not split a /64", "[fe80::1%eth0]:443", "fe80::/64"},
		{"a NAT64 address is its embedded ipv4", "[64:ff9b::198.51.100.7]:443", "198.51.100.7"},
		{"a NAT64 address in hex is its embedded ipv4", "[64:ff9b::c633:6408]:443", "198.51.100.8"},
		{"an address beside the NAT64 prefix stays a /64", "[64:ff9b:1::c633:6408]:443", "64:ff9b:1::/64"},
		{"no port keeps the raw value", "198.51.100.7", "198.51.100.7"},
		{"a host that is no address keys as itself", "pipe:0", "pipe"},
		{"an empty RemoteAddr keys as empty", "", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			r := httptest.NewRequest(http.MethodPost, "/oidc/device_authorization", nil)
			r.RemoteAddr = c.remote
			if got := limitKey(r); got != c.want {
				t.Fatalf("limitKey(%q) = %q, want %q", c.remote, got, c.want)
			}
		})
	}
}

// TestLimitKeyIsNotTheAuditAddress pins that the audit source address stays
// the full peer address while the limit key widens an IPv6 peer to its /64.
func TestLimitKeyIsNotTheAuditAddress(t *testing.T) {
	t.Parallel()
	r := httptest.NewRequest(http.MethodPost, "/oidc/device", nil)
	r.RemoteAddr = "[2001:db8:1:2::77]:443"
	if got := clientIP(r); got != "2001:db8:1:2::77" {
		t.Fatalf("clientIP = %q, want the full address for the audit record", got)
	}
}

// TestPerAddressLimitsShareABudgetPerIPv6Network drives every per-address
// limit with two addresses of one IPv6 /64, which must share one budget,
// and an address of another /64 and two IPv4 addresses, which must not.
func TestPerAddressLimitsShareABudgetPerIPv6Network(t *testing.T) {
	t.Parallel()
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	limits := []struct {
		name, path string
		wrap       func(l *ratelimit.Limiter, rps float64, next http.Handler) http.Handler
	}{
		{"approver listener", "/v1/approver/pending", perIPLimit},
		{"password submit", "/oidc/device", loginLimit},
		{"device sign-in start", "/oidc/device_authorization", deviceStartLimit},
		{"enterprise token grant", "/oidc/token", grantLimit},
	}
	for _, lim := range limits {
		t.Run(lim.name, func(t *testing.T) {
			t.Parallel()
			h := lim.wrap(ratelimit.New(), 0.0001, next)
			code := func(remote string) int {
				r := httptest.NewRequest(http.MethodPost, lim.path, nil)
				r.RemoteAddr = remote
				w := httptest.NewRecorder()
				h.ServeHTTP(w, r)
				return w.Code
			}
			steps := []struct {
				remote string
				want   int
			}{
				{"[2001:db8:1:2::1]:443", http.StatusOK},
				{"[2001:db8:1:2:ffff::9]:443", http.StatusTooManyRequests},
				{"[2001:db8:1:3::1]:443", http.StatusOK},
				{"198.51.100.7:40000", http.StatusOK},
				{"198.51.100.8:40000", http.StatusOK},
				{"[::ffff:198.51.100.7]:40001", http.StatusTooManyRequests},
			}
			for _, s := range steps {
				if got := code(s.remote); got != s.want {
					t.Fatalf("request from %s = %d, want %d", s.remote, got, s.want)
				}
			}
		})
	}
}

// TestDeviceStartRefusalNamesWhatWasCounted pins that the throttled device
// sign-in start says "address" when one IPv4 address was counted and
// "network" when an IPv6 /64 was.
func TestDeviceStartRefusalNamesWhatWasCounted(t *testing.T) {
	t.Parallel()
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	cases := []struct{ remote, want string }{
		{"198.51.100.7:40000", "Too many sign-in starts from this address. Wait a few seconds and sign in again."},
		{"[2001:db8:1:2::1]:443", "Too many sign-in starts from this network. Wait a few seconds and sign in again."},
	}
	for _, c := range cases {
		h := deviceStartLimit(ratelimit.New(), 0.0001, next)
		var last *httptest.ResponseRecorder
		for range 2 {
			r := httptest.NewRequest(http.MethodPost, "/oidc/device_authorization", nil)
			r.RemoteAddr = c.remote
			last = httptest.NewRecorder()
			h.ServeHTTP(last, r)
		}
		if last.Code != http.StatusTooManyRequests || !strings.Contains(last.Body.String(), c.want) {
			t.Errorf("second start from %s = %d %s, want 429 with %q", c.remote, last.Code, last.Body.String(), c.want)
		}
	}
}
