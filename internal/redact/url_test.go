package redact

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestURL(t *testing.T) {
	cases := []struct {
		name, in, want string
	}{
		{"plain", "https://siem.example/services/collector", "https://siem.example/services/collector"},
		{"query token masked", "https://siem.example/services/collector?token=SECRET-ABC", "https://siem.example/services/collector?…"},
		{"userinfo dropped", "nats://user:hunter2@nats.internal:4222", "nats://nats.internal:4222"},
		{"userinfo and query", "https://u:p@host/path?tok=x", "https://host/path?…"},
		{"empty", "", ""},
		{"unparseable", "http://bad\x7f%zz", "<unparseable url>"},
		{"fragment masked", "https://host/path#access_token=SECRET-FRAG", "https://host/path#…"},
		{"query and fragment", "https://host/path?tok=SECRET-Q#SECRET-F", "https://host/path?…#…"},
		{"capability segment masked", "https://hooks.example/services/T0AB1CD2E3F4G5H6I7J8K9SECRET/next",
			"https://hooks.example/services/%5BREDACTED%5D/next"},
		{"uuid segment kept", "https://api.example/v1/0b9e8f4a-1c2d-4e5f-8a9b-0c1d2e3f4a5b/mcp", "https://api.example/v1/0b9e8f4a-1c2d-4e5f-8a9b-0c1d2e3f4a5b/mcp"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := URL(c.in)
			if got != c.want {
				t.Fatalf("URL(%q) = %q, want %q", c.in, got, c.want)
			}
			if strings.Contains(got, "SECRET") || strings.Contains(got, "hunter2") {
				t.Fatalf("URL(%q) leaked a secret: %q", c.in, got)
			}
		})
	}
}

// TestURLs pins the mask of every address inside a text: each is written as
// URL writes it, the punctuation that ends a sentence or closes a
// parenthesis after it stays in place, text with no address comes back as
// it is, and a mask already applied reads the same again.
func TestURLs(t *testing.T) {
	cases := []struct {
		name, in, want string
	}{
		{"no address", "status → degraded the server exited", "status → degraded the server exited"},
		{"user and password", "connected https://svc:Pw0rd@mcp.example/v1", "connected https://mcp.example/v1"},
		{"user alone", "connect https://svc@mcp.example/v1 failed", "connect https://mcp.example/v1 failed"},
		{"query", "dial https://mcp.example/v1?session=Qt0k now", "dial https://mcp.example/v1?… now"},
		{"user and query", "https://svc:***@mcp.example/v1?session=Qt0k", "https://mcp.example/v1?…"},
		{"two addresses", "from https://a@one.example/x?k=1 to http://b:c@two.example:8080/y",
			"from https://one.example/x?… to http://two.example:8080/y"},
		{"quoted by net/http", `Post "https://svc:***@mcp.example/v1?session=Qt0k": dial tcp: refused`,
			`Post "https://mcp.example/v1?…": dial tcp: refused`},
		{"closed by a parenthesis and a period", "(see https://svc@mcp.example/v1?session=Qt0k).", "(see https://mcp.example/v1?…)."},
		{"followed by a colon", "connect https://svc@mcp.example/v1?session=Qt0k: refused", "connect https://mcp.example/v1?…: refused"},
		{"already masked", "dial https://mcp.example/v1?… now", "dial https://mcp.example/v1?… now"},
		{"a masked user", "dial https://%5BREDACTED%5D@mcp.example/v1", "dial https://mcp.example/v1"},
		{"unparseable", "dial http://bad\x7f%zz now", "dial <unparseable url> now"},
		{"a quote in the password", "connected http://svc:It'sPw0rd@mcp.example/v1?session=Qt0k", "connected http://mcp.example/v1?…"},
		{"a quote in the query", "connect http://mcp.example/v1?token=ab'Qt0kTail failed", "connect http://mcp.example/v1?… failed"},
		{"an address in single quotes", "dial 'http://svc@mcp.example/v1?session=Qt0k'.", "dial 'http://mcp.example/v1?…'."},
		{"a quoted address at the end of a line", "dial 'http://svc@mcp.example/v1?session=Qt0k'", "dial 'http://mcp.example/v1?…'"},
		{"quoted addresses joined by a comma", "urls=['http://a.example/x','http://svc:Pw0rd@b.example/y?t=Qt0k']",
			"urls=['http://a.example/x','http://b.example/y?…']"},
		{"quoted addresses joined by a comma and a space", "urls=('http://svc@a.example/x?t=Qt0k', 'http://svc:Pw0rd@b.example/y')",
			"urls=('http://a.example/x?…', 'http://b.example/y')"},
		{"a fragment", "dial https://mcp.example/v1#access_token=Qt0kFrag now", "dial https://mcp.example/v1#… now"},
		{"a capability in the path", "Post \"http://127.0.0.1:9/mcp/T0AB1CD2E3F4G5H6I7J8K9L0M1N2P3Qt0k\": refused",
			"Post \"http://127.0.0.1:9/mcp/%5BREDACTED%5D\": refused"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := URLs(c.in)
			if got != c.want {
				t.Fatalf("URLs(%q) = %q, want %q", c.in, got, c.want)
			}
			for _, secret := range []string{"svc", "Pw0rd", "Qt0k", "REDACTED]"} {
				if strings.Contains(c.in, secret) && strings.Contains(got, secret) {
					t.Errorf("URLs(%q) kept %q: %q", c.in, secret, got)
				}
			}
		})
	}
}

func TestHost(t *testing.T) {
	cases := []struct {
		name, in, want string
	}{
		{"capability path dropped", "https://push.example/wp/CAPABILITY-TOKEN", "https://push.example"},
		{"port kept", "https://ntfy.internal:8443/topicSecret", "https://ntfy.internal:8443"},
		{"non-https still host only", "http://host/secret", "http://host"},
		{"unparseable", "http://bad\x7f%zz", "<unparseable url>"},
		{"no host", "not-a-url", "<unparseable url>"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := Host(c.in)
			if got != c.want {
				t.Fatalf("Host(%q) = %q, want %q", c.in, got, c.want)
			}
		})
	}
}

func TestSanitizeURLError(t *testing.T) {
	inner := errors.New("connection refused")
	ue := &url.Error{Op: "Post", URL: "https://push.example/wp/CAPTOKEN?k=v", Err: inner}

	got := SanitizeURLError(ue, Host)
	if s := got.Error(); strings.Contains(s, "CAPTOKEN") {
		t.Fatalf("sanitized error still carries the capability path: %q", s)
	}
	if s := got.Error(); !strings.Contains(s, "push.example") {
		t.Fatalf("sanitized error lost the host operators grep for: %q", s)
	}
	// Type and chain survive: errors.As sees a *url.Error, errors.Is reaches
	// the inner error; the redaction changes printed text only.
	var out *url.Error
	if !errors.As(got, &out) {
		t.Fatalf("sanitized error is no longer a *url.Error")
	}
	if !errors.Is(got, inner) {
		t.Fatalf("sanitized error lost its inner error")
	}

	// Non-url.Error and nil pass through untouched.
	plain := errors.New("plain")
	if SanitizeURLError(plain, Host) != plain {
		t.Fatalf("plain error must pass through unchanged")
	}
	if SanitizeURLError(nil, Host) != nil {
		t.Fatalf("nil must stay nil")
	}
	// A WRAPPED url.Error passes through (top-level only, by design): the
	// caller owns its outer context; sanitize at the Do site instead.
	wrapped := fmt.Errorf("outer: %w", ue)
	if SanitizeURLError(wrapped, Host) != wrapped {
		t.Fatalf("wrapped url.Error must pass through (top-level only)")
	}
}

// TestSanitizeURLErrorLiveDo pins the real-world shape: an http.Client.Do
// failure against a secret-bearing URL, sanitized at the Do site, keeps
// errors.Is(context.DeadlineExceeded) working and drops the secret.
func TestSanitizeURLErrorLiveDo(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Nanosecond)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://192.0.2.1/services/collector?token=SECRET-LIVE", nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	_, doErr := (&http.Client{}).Do(req)
	if doErr == nil {
		t.Fatalf("expected a Do error")
	}
	got := SanitizeURLError(doErr, URL)
	if s := got.Error(); strings.Contains(s, "SECRET-LIVE") {
		t.Fatalf("live Do error leaked the query token: %q", s)
	}
	if !errors.Is(got, context.DeadlineExceeded) {
		t.Fatalf("sanitized live error lost context.DeadlineExceeded")
	}
}
