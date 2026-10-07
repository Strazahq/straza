// Package oidcflow implements the client side of login discovery:
// ask strazad which OIDC issuer to log in at (`/.well-known/straza/idp.json`),
// then resolve that issuer's RFC 8628 endpoints via standard OIDC discovery.
// Used by `straza enroll` and `strazactl login`; the embedded console
// mirrors the same two fetches in JS. Stdlib only: both CLI clients embed it.
package oidcflow

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// Flow is where a client runs the device-code login.
type Flow struct {
	// Issuer is the OIDC issuer base URL (external IdP, or strazad itself
	// for the built-in issuer). Informational for progress output.
	Issuer string
	// DeviceAuthURL and TokenURL are the absolute RFC 8628 endpoints.
	DeviceAuthURL string
	TokenURL      string
	// ClientID is the id to present: server-dictated for an external IdP
	// (it is the audience strazad requires on ID tokens), else the caller's
	// default (the built-in issuer accepts per-client audiences).
	ClientID string
}

// Builtin is the device flow at the strazad's own issuer, skipping idp.json:
// the emergency sign-in for the break-glass admin, which must work when the
// advertised identity provider cannot sign anyone in.
func Builtin(base, clientID string) Flow {
	base = strings.TrimSuffix(base, "/")
	return Flow{
		Issuer:        base,
		DeviceAuthURL: base + "/oidc/device_authorization",
		TokenURL:      base + "/oidc/token",
		ClientID:      clientID,
	}
}

// Discover resolves the login flow for the strazad at base. A server without
// idp.json, an older one, falls back to the built-in issuer paths on the
// server itself, so new clients keep working against old servers.
func Discover(ctx context.Context, httpc *http.Client, base, defaultClientID string) (Flow, error) {
	base = strings.TrimSuffix(base, "/")
	doc, status, err := getJSON(ctx, httpc, base+"/.well-known/straza/idp.json")
	if err != nil {
		return Flow{}, fmt.Errorf("oidcflow: reach %s: %w", base, err)
	}
	switch {
	case status == http.StatusNotFound:
		// A server without idp.json: the built-in issuer at the server's own paths.
		return Builtin(base, defaultClientID), nil
	case status != http.StatusOK:
		msg := doc["error"]
		if msg == "" {
			msg = fmt.Sprintf("HTTP %d", status)
		}
		return Flow{}, fmt.Errorf("oidcflow: server cannot say where to log in: %s", msg)
	}
	issuer := strings.TrimSuffix(doc["issuer"], "/")
	if issuer == "" {
		return Flow{}, fmt.Errorf("oidcflow: server's idp.json names no issuer")
	}
	clientID := doc["client_id"]
	if clientID == "" {
		clientID = defaultClientID
	}

	disc, status, err := getJSON(ctx, httpc, issuer+"/.well-known/openid-configuration")
	if err != nil {
		return Flow{}, fmt.Errorf("oidcflow: reach identity provider %s: %w. Check that this machine can open %s, for example in a browser here, then sign in again", issuer, err, issuer)
	}
	if status != http.StatusOK {
		return Flow{}, fmt.Errorf("oidcflow: identity provider %s discovery failed: HTTP %d", issuer, status)
	}
	if disc["device_authorization_endpoint"] == "" {
		return Flow{}, fmt.Errorf("oidcflow: identity provider %s does not advertise a device_authorization_endpoint. Enable the OAuth 2.0 device authorization grant for client %q", issuer, clientID)
	}
	if disc["token_endpoint"] == "" {
		return Flow{}, fmt.Errorf("oidcflow: identity provider %s does not advertise a token_endpoint", issuer)
	}
	return Flow{
		Issuer:        issuer,
		DeviceAuthURL: disc["device_authorization_endpoint"],
		TokenURL:      disc["token_endpoint"],
		ClientID:      clientID,
	}, nil
}

// NHIOffer is the key-lane offer read from idp.json: whether the server
// names a Straza issuer for headless NHIs and where. Reading the offer never
// dials the named issuer, so a client that will not pick the key lane needs
// no route to it, and reachability can never change which lane is chosen.
type NHIOffer struct {
	// Offered reports whether the server offers the key lane at all.
	Offered bool
	// Issuer is the issuer base URL the offer names (empty when not offered).
	Issuer string
	// tokenURL is set on the legacy self-issuer shapes whose endpoint is
	// fixed by convention; empty means ResolveNHI must discover it.
	tokenURL string
}

// DiscoverNHIOffer reads idp.json at base and reports whether the server
// offers the NHI key lane (the client_credentials grant against a
// Straza-registered Ed25519 key) and at which issuer. Offered=false with a
// nil error means the server names no Straza issuer for NHIs; the caller
// then falls back to the human IdP's client-secret lane. Nothing beyond
// base is dialed here: neither the human IdP nor the offered issuer, which
// ResolveNHI dials only once the key lane is actually picked.
func DiscoverNHIOffer(ctx context.Context, httpc *http.Client, base string) (NHIOffer, error) {
	base = strings.TrimSuffix(base, "/")
	doc, status, err := getJSON(ctx, httpc, base+"/.well-known/straza/idp.json")
	if err != nil {
		return NHIOffer{}, fmt.Errorf("oidcflow: reach %s: %w", base, err)
	}
	self := NHIOffer{Offered: true, Issuer: base, tokenURL: base + "/oidc/token"}
	switch {
	case status == http.StatusNotFound:
		// A server without idp.json: the built-in issuer at the server's own paths.
		return self, nil
	case status != http.StatusOK:
		msg := doc["error"]
		if msg == "" {
			msg = fmt.Sprintf("HTTP %d", status)
		}
		return NHIOffer{}, fmt.Errorf("oidcflow: server cannot say where to log in: %s", msg)
	}
	if issuer := strings.TrimSuffix(doc["nhi_issuer"], "/"); issuer != "" {
		return NHIOffer{Offered: true, Issuer: issuer}, nil
	}
	// Old server without the field: a standalone that is its own login
	// issuer serves the grant itself; an enterprise one names only the
	// human IdP and offers no key lane.
	if strings.TrimSuffix(doc["issuer"], "/") == base {
		return self, nil
	}
	return NHIOffer{}, nil
}

// ResolveNHI turns a key-lane offer into the concrete flow, dialing the
// offered issuer's OIDC discovery when the endpoint is not fixed by
// convention. Called only once the key lane is the picked lane: an
// unreachable issuer is a hard, actionable error on that lane, never a
// reason to change lanes.
func ResolveNHI(ctx context.Context, httpc *http.Client, offer NHIOffer) (Flow, error) {
	if !offer.Offered {
		return Flow{}, fmt.Errorf("oidcflow: the server offers no NHI key lane")
	}
	if offer.tokenURL != "" {
		return Flow{Issuer: offer.Issuer, TokenURL: offer.tokenURL}, nil
	}
	disc, status, err := getJSON(ctx, httpc, offer.Issuer+"/.well-known/openid-configuration")
	if err != nil {
		return Flow{}, fmt.Errorf("oidcflow: reach NHI issuer %s: %w. The server advertises this address (server.publicUrl / STRAZA_PUBLIC_URL); make it dialable from this machine, or enroll on the IdP client-secret lane (STRAZA_CLIENT_ID and STRAZA_CLIENT_SECRET)", offer.Issuer, err)
	}
	if status != http.StatusOK {
		return Flow{}, fmt.Errorf("oidcflow: NHI issuer %s discovery failed: HTTP %d", offer.Issuer, status)
	}
	if disc["token_endpoint"] == "" {
		return Flow{}, fmt.Errorf("oidcflow: NHI issuer %s does not advertise a token_endpoint", offer.Issuer)
	}
	return Flow{Issuer: offer.Issuer, TokenURL: disc["token_endpoint"]}, nil
}

// DiscoverNHI resolves where a headless NHI authenticates with the key
// lane, composing DiscoverNHIOffer and ResolveNHI for the caller already
// committed to that lane (a key-lane session start). offered=false with a
// nil error means the server does not name a Straza issuer for NHIs.
func DiscoverNHI(ctx context.Context, httpc *http.Client, base string) (Flow, bool, error) {
	offer, err := DiscoverNHIOffer(ctx, httpc, base)
	if err != nil || !offer.Offered {
		return Flow{}, false, err
	}
	flow, err := ResolveNHI(ctx, httpc, offer)
	if err != nil {
		return Flow{}, false, err
	}
	return flow, true, nil
}

// getJSON fetches url and decodes a flat string-valued JSON object, tolerating
// non-string values (ignored; OIDC discovery documents carry arrays too).
func getJSON(ctx context.Context, httpc *http.Client, url string) (map[string]string, int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, 0, err
	}
	resp, err := httpc.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer func() { _ = resp.Body.Close() }()
	buf, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, 0, err
	}
	var raw map[string]any
	out := map[string]string{}
	if len(buf) > 0 {
		if err := json.Unmarshal(buf, &raw); err != nil {
			if resp.StatusCode == http.StatusOK {
				return nil, 0, fmt.Errorf("%s: malformed JSON: %w", url, err)
			}
			return out, resp.StatusCode, nil // non-200 with junk body: status is the answer
		}
		for k, v := range raw {
			if s, ok := v.(string); ok {
				out[k] = s
			}
		}
	}
	return out, resp.StatusCode, nil
}
