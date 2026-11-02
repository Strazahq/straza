// Package oidcflow implements the client side of a person's sign-in: ask
// strazad which OIDC issuer to sign in at (`/.well-known/straza/idp.json`),
// resolve that issuer's endpoints through standard OIDC discovery, and run
// the browser lane (SignIn) or hand the device-code endpoints to the
// caller. Used by `straza service` for `straza enroll`. The embedded
// console mirrors the two fetches in JS. Stdlib only: the client embeds it.
package oidcflow

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
)

// Flow is where a client signs a person in.
type Flow struct {
	// Issuer is the OIDC issuer base URL (external IdP, or strazad itself
	// for the built-in issuer). Informational for progress output.
	Issuer string
	// AuthorizeURL, DeviceAuthURL and TokenURL are the absolute endpoints
	// the issuer advertises. AuthorizeURL or DeviceAuthURL is empty when
	// the issuer offers no browser lane or no device-code lane.
	AuthorizeURL  string
	DeviceAuthURL string
	TokenURL      string
	// ClientID is the id to present: server-dictated for an external IdP
	// (it is the audience strazad requires on ID tokens), else the caller's
	// default (the built-in issuer accepts per-client audiences).
	ClientID string
	// LoopbackPort is idp.json's loopback_port, the port the browser
	// lane's redirect must use, and 0 for any port.
	LoopbackPort int
	// MinSignIn is idp.json's min_sign_in, the lowest sign-in lane the
	// server accepts for a person's key, empty from an older server.
	MinSignIn string
	// server is the strazad the flow was discovered at, equal to Issuer
	// when the issuer is the server's own.
	server string
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
		server:        base,
	}
}

// CheckDevice reports whether the issuer offers the device-code lane, with
// the sentence that says how to turn it on when it does not.
func (f Flow) CheckDevice() error {
	if f.DeviceAuthURL == "" {
		return fmt.Errorf("oidcflow: identity provider %s does not advertise a device_authorization_endpoint. Enable the OAuth 2.0 device authorization grant for client %q", f.Issuer, f.ClientID)
	}
	return nil
}

// CheckBrowser reports whether the issuer offers the browser lane. Without
// it the sentence names command, such as "straza enroll", with
// --use-device-code: an older Straza server when the server is its own
// issuer, or the provider's missing endpoint otherwise.
func (f Flow) CheckBrowser(command string) error {
	switch {
	case f.AuthorizeURL != "":
		return nil
	case f.server != "" && f.Issuer == f.server:
		return sentencef("The Straza server at %s does not offer a browser sign-in yet. "+
			"Run `%s --use-device-code`, or ask your Straza admin to upgrade the server.", f.server, command)
	}
	return sentencef("The identity provider %s advertises no authorization_endpoint, so Straza cannot sign you in through a browser there. "+
		"Run `%s --use-device-code`, or ask IT to turn on the authorization code flow for client %q.", f.Issuer, command, f.ClientID)
}

// Discover resolves the login flow for the strazad at base. A server without
// idp.json, an older one, falls back to the built-in issuer paths on the
// server itself, so new clients keep working against old servers. It needs
// only the token endpoint, and each lane checks its own (CheckDevice,
// CheckBrowser).
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
	port := 0
	if p := doc["loopback_port"]; p != "" {
		if port, err = strconv.Atoi(p); err != nil || port < 0 || port > 65535 {
			return Flow{}, fmt.Errorf("oidcflow: the server's idp.json names loopback_port %s, which is not a port number. Ask your Straza admin to check oidc.loopbackPort", p)
		}
	}
	flow, err := DiscoverIssuer(ctx, httpc, issuer, clientID)
	if err != nil {
		return Flow{}, err
	}
	flow.LoopbackPort, flow.MinSignIn, flow.server = port, doc["min_sign_in"], base
	if strings.TrimSuffix(doc["nhi_issuer"], "/") == issuer {
		// A standalone server names its own issuer for both, under its
		// public URL, which can differ from the base the client dialed.
		flow.server = issuer
	}
	return flow, nil
}

// DiscoverIssuer resolves the endpoints of the issuer at its OIDC
// discovery document, needing only the token endpoint. Discover calls it
// for the issuer idp.json names, and the break-glass browser sign-in for
// the strazad's own issuer, skipping idp.json.
func DiscoverIssuer(ctx context.Context, httpc *http.Client, issuer, clientID string) (Flow, error) {
	issuer = strings.TrimSuffix(issuer, "/")
	disc, status, err := getJSON(ctx, httpc, issuer+"/.well-known/openid-configuration")
	if err != nil {
		return Flow{}, fmt.Errorf("oidcflow: reach identity provider %s: %w. Check that this machine can open %s, for example in a browser here, then sign in again", issuer, err, issuer)
	}
	if status != http.StatusOK {
		return Flow{}, fmt.Errorf("oidcflow: identity provider %s discovery failed: HTTP %d", issuer, status)
	}
	if disc["token_endpoint"] == "" {
		return Flow{}, fmt.Errorf("oidcflow: identity provider %s does not advertise a token_endpoint", issuer)
	}
	return Flow{
		Issuer:        issuer,
		AuthorizeURL:  disc["authorization_endpoint"],
		DeviceAuthURL: disc["device_authorization_endpoint"],
		TokenURL:      disc["token_endpoint"],
		ClientID:      clientID,
		server:        issuer,
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
		return Flow{}, fmt.Errorf("oidcflow: reach NHI issuer %s: %w. The server advertises this address in server.publicUrl, or STRAZA_PUBLIC_URL, so make it reachable from this machine. "+
			"Or enroll the agent with its client secret at your identity provider instead, which the Straza background service reads from agent-<uid>.json "+
			"in the directory systemd's LoadCredential names, or in the credentials directory of the service's state directory", offer.Issuer, err)
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

// getJSON fetches url and decodes a flat JSON object into strings. A number
// keeps its decimal text, and other values are ignored, since OIDC
// discovery documents carry arrays too.
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
			switch v := v.(type) {
			case string:
				out[k] = v
			case float64:
				out[k] = strconv.FormatFloat(v, 'f', -1, 64)
			}
		}
	}
	return out, resp.StatusCode, nil
}
