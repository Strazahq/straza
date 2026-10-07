// Package sameorigin holds the redirect rule of Straza's own clients, straza
// and strazactl, and of strazad's webhook sink, Slack channel and push
// delivery: a request that carries a credential follows a redirect only while
// the scheme, host and port stay those of its first request.
package sameorigin

import (
	"errors"
	"net/http"
	"net/url"
	"strings"
)

// maxRedirects is the bound Go's default redirect policy applies.
const maxRedirects = 10

// ErrTooManyRedirects stops a request after maxRedirects redirects, so a
// caller can tell a redirect loop, which a server answered, from a network
// failure.
var ErrTooManyRedirects = errors.New("stopped after 10 redirects")

// RedirectError refuses a redirect that would take a credential to another
// origin. From and To are scheme://host[:port] only, never a path, a query or
// a credential.
type RedirectError struct {
	From, To string
}

func (e *RedirectError) Error() string {
	return e.From + " answered with a redirect to " + e.To + ", which is another scheme, host or port, " +
		"so the request was not followed and its credentials were not sent there. " +
		"Check that the configured server URL is the address strazad serves on, and that no proxy in front of strazad redirects"
}

// Check is the redirect policy of a client whose every request carries a
// credential, including one its transport adds, which a redirect policy
// cannot see. Go's default forwards the Authorization header to every port
// and scheme of the same host name and to its subdomains, and re-sends the
// body on a 307 or 308 to any host, so a redirect is followed only within
// the first request's origin. It stops after 10 redirects, as Go's does.
func Check(req *http.Request, via []*http.Request) error {
	if len(via) >= maxRedirects {
		return ErrTooManyRedirects
	}
	from := via[0].URL
	if same(from, req.URL) {
		return nil
	}
	return &RedirectError{From: from.Scheme + "://" + from.Host, To: req.URL.Scheme + "://" + req.URL.Host}
}

// CheckCredentialed is the redirect policy of a client that also sends
// requests with no credential, such as discovery at an identity provider. A
// request whose first hop carried an Authorization header or a body follows
// Check, because the refresh, check-in and enrol requests carry their token
// in the body. Any other request keeps Go's default.
func CheckCredentialed(req *http.Request, via []*http.Request) error {
	if first := via[0]; first.Header.Get("Authorization") != "" || first.ContentLength != 0 {
		return Check(req, via)
	}
	if len(via) >= maxRedirects {
		return ErrTooManyRedirects
	}
	return nil
}

// same reports whether a and b name the same scheme, host and port, an
// absent port read as the scheme's default.
func same(a, b *url.URL) bool {
	return strings.EqualFold(a.Scheme, b.Scheme) && strings.EqualFold(a.Hostname(), b.Hostname()) && port(a) == port(b)
}

func port(u *url.URL) string {
	if p := u.Port(); p != "" {
		return p
	}
	if strings.EqualFold(u.Scheme, "https") {
		return "443"
	}
	return "80"
}
