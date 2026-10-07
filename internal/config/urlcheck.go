package config

// URL-shape boot guards. A mis-shaped URL field otherwise boots and
// surfaces the damage later: a trailing slash on the token issuer turns
// device-flow POSTs into redirect-eaten 301s, an embedded user:password@
// leaks into boot logs and enroll QRs, and a schemed bodystore endpoint
// fails on the first externalized body. Every URL-shaped field therefore
// gets the same announce and verify contract: VERIFY the shape here at boot
// (fail closed with the field path in the message), and ANNOUNCE the
// accepted value in the serve-time boot log (internal/server) so what the
// deployment stands on is one grep away.

import (
	"fmt"
	"net/url"
	"strings"
)

// parseHTTPURL is the shared floor every http(s)-URL field must clear: no
// whitespace, parseable, an http/https scheme, a named host, no embedded
// credentials. Field-specific rules (base-only, query, fragment) layer on
// top. credHint extends the userinfo rejection with the field's proper
// credential lane (e.g. sinks carry receiver auth in headers:).
func parseHTTPURL(field, raw string, httpsOnly bool, credHint string) (*url.URL, error) {
	// url.Parse tolerates spaces in the path, so a copy-paste artifact would
	// otherwise survive to the first dead link.
	if strings.ContainsAny(raw, " \t") {
		return nil, fmt.Errorf("%s must not contain whitespace, got %q", field, raw)
	}
	u, err := url.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("%s is not a valid URL: %v", field, err)
	}
	switch {
	case httpsOnly && u.Scheme != "https":
		return nil, fmt.Errorf("%s must be https:// (the approver app is https+pin only), got %q", field, raw)
	case !httpsOnly && u.Scheme != "http" && u.Scheme != "https":
		return nil, fmt.Errorf("%s must be an http:// or https:// URL, got %q", field, raw)
	}
	if u.Host == "" {
		return nil, fmt.Errorf("%s must name a host, got %q", field, raw)
	}
	if u.User != nil {
		return nil, fmt.Errorf("%s must not embed credentials (user:password@ leaks into boot logs and error messages%s), got %q", field, credHint, raw)
	}
	return u, nil
}

// checkBaseURL verifies a field other URLs are built ON TOP OF:
// scheme://host[:port] only. allowRoot tolerates a bare trailing slash for
// the fields whose every consumer trims it (the approver URLs); the fields
// consumed VERBATIM as an issuer string reject it instead: normalizing
// would silently rotate the deployment's token-issuer identity, and a
// refused boot beats a rotated issuer.
func checkBaseURL(field, raw string, httpsOnly, allowRoot bool) error {
	u, err := parseHTTPURL(field, raw, httpsOnly, "")
	if err != nil {
		return err
	}
	pathOK := u.Path == "" || (allowRoot && u.Path == "/")
	if !pathOK || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" {
		if allowRoot {
			return fmt.Errorf("%s is a base URL: scheme and host only, no path, query, or fragment (a trailing slash is fine), got %q", field, raw)
		}
		return fmt.Errorf("%s is a base URL consumed verbatim as an issuer string: scheme and host only (no path, query, fragment, or trailing slash), got %q", field, raw)
	}
	return nil
}

// checkEndpointURL verifies a field naming a full endpoint: paths are
// expected (Keycloak realms, Splunk HEC routes) and pass VERBATIM (an
// endpoint identity is never normalized here), while a #fragment never
// reaches any server and can only be an authoring mistake. Query strings are
// field-specific: webhook receivers legitimately carry tokens there, but an
// OIDC issuer must not have one (OpenID Connect Discovery forbids both query
// and fragment).
func checkEndpointURL(field, raw string, allowQuery bool, credHint string) error {
	u, err := parseHTTPURL(field, raw, false, credHint)
	if err != nil {
		return err
	}
	if u.Fragment != "" {
		return fmt.Errorf("%s must not carry a #fragment (it never reaches the server), got %q", field, raw)
	}
	if !allowQuery && (u.RawQuery != "" || u.ForceQuery) {
		return fmt.Errorf("%s must not carry a query string (copy the issuer exactly as the IdP's discovery document states it), got %q", field, raw)
	}
	return nil
}

// checkHostPort verifies a field the consumer dials as host[:port] with NO
// scheme and NO path (the S3 body store's minio client; disableSSL selects
// the transport, bucket and prefix have their own fields).
func checkHostPort(field, raw string) error {
	if strings.ContainsAny(raw, " \t") {
		return fmt.Errorf("%s must not contain whitespace, got %q", field, raw)
	}
	if strings.Contains(raw, "://") {
		return fmt.Errorf("%s is host[:port] only. Drop the scheme (disableSSL selects http vs https), got %q", field, raw)
	}
	if strings.Contains(raw, "/") {
		return fmt.Errorf("%s is host[:port] only, no path (bucket and prefix have their own fields), got %q", field, raw)
	}
	return nil
}
