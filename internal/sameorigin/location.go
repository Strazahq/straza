package sameorigin

import (
	"errors"
	"net/url"
	"strings"
)

// goBadLocation opens the error Go's client returns for a redirect whose
// Location header does not parse.
const goBadLocation = "failed to parse Location header"

// BadLocation reports whether err is Go's error for a redirect whose Location
// header does not parse, and returns the scheme://host[:port] of the request
// that received it. Go returns that error before any redirect policy runs and
// gives it no type, so its text is the only handle.
func BadLocation(err error) (origin string, ok bool) {
	var ue *url.Error
	if !errors.As(err, &ue) || !strings.HasPrefix(ue.Err.Error(), goBadLocation) {
		return "", false
	}
	if u, perr := url.Parse(ue.URL); perr == nil {
		origin = u.Scheme + "://" + u.Host
	}
	return origin, true
}
