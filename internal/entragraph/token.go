package entragraph

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
)

// guidForm matches the GUID form of the trace id Entra answers with and of
// an Entra object ID.
var guidForm = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// bearer returns a Graph token of the service app and the app's own object
// ID. It serves the cached token while more than renewWindow of its life is
// left, and otherwise fetches a new one while holding c.mu.
func (c *Client) bearer(ctx context.Context) (token, ownerID string, err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := c.now()
	if c.token != "" && now.Before(c.renewAt) {
		return c.token, c.ownerID, nil
	}
	token, lifetime, err := c.fetchToken(ctx, now)
	if err != nil {
		return "", "", err
	}
	ownerID, err = c.objectID(token)
	if err != nil {
		return "", "", err
	}
	c.token, c.ownerID, c.renewAt = token, ownerID, now.Add(lifetime-renewWindow)
	return token, ownerID, nil
}

// fetchToken runs the client credentials grant of the service app at the
// token endpoint, signed in with a certificate assertion whose audience is
// that endpoint. It returns the access token and its lifetime. The assertion
// and the token go nowhere but the request and the return value.
func (c *Client) fetchToken(ctx context.Context, now time.Time) (string, time.Duration, error) {
	assertion, kid, err := c.signer.SignCertificateAssertion(now, c.clientID, c.tokenURL)
	if err != nil {
		return "", 0, errorf("Straza could not sign the client assertion of the Entra service app %s, so it asked Microsoft Graph nothing: %w. An administrator checks the client assertion keys with strazactl signing-keys list, then Straza tries again.", c.clientID, err)
	}
	form := url.Values{
		"grant_type":            {"client_credentials"},
		"client_id":             {c.clientID},
		"client_assertion_type": {assertionType},
		"client_assertion":      {assertion},
		"scope":                 {graphScope},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.tokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return "", 0, errorf("The token endpoint of tenant %s is not a usable address. Check identity.entra.tenant in strazad's config.", c.tenant)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return "", 0, unreached("the token request of the service app", req.URL.Host, err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := readCapped(resp.Body)
	switch {
	case err != nil:
		return "", 0, readFailed("the token request of the service app", req.URL.Host, err)
	case resp.StatusCode >= 300 && resp.StatusCode < 400:
		return "", 0, errorf("Entra answered the token request of the service app %s with a redirect, which Straza never follows with a client assertion. Check identity.entra.tenant in strazad's config, then Straza tries again.", c.clientID)
	case resp.StatusCode != http.StatusOK:
		return "", 0, c.tokenRefused(resp.StatusCode, body, kid)
	}
	var ans struct {
		AccessToken string  `json:"access_token"`
		TokenType   string  `json:"token_type"`
		ExpiresIn   float64 `json:"expires_in"`
	}
	const settle = "Check identity.entra.tenant and identity.entra.clientId in strazad's config, then Straza tries again."
	switch err := json.Unmarshal(body, &ans); {
	case err != nil:
		return "", 0, errorf("Entra answered the token request of the service app %s with something other than the JSON of RFC 6749. %s", c.clientID, settle)
	case ans.AccessToken == "":
		return "", 0, errorf("Entra answered the token request of the service app %s without an access token. %s", c.clientID, settle)
	case !strings.EqualFold(ans.TokenType, "Bearer"):
		return "", 0, errorf("Entra answered the token request of the service app %s with a token that is not a Bearer token. %s", c.clientID, settle)
	case ans.ExpiresIn < 1:
		return "", 0, errorf("Entra answered the token request of the service app %s with a token that names no lifetime, so Straza can neither keep nor renew it. %s", c.clientID, settle)
	}
	return ans.AccessToken, time.Duration(min(ans.ExpiresIn, maxLifetime.Seconds())) * time.Second, nil
}

// objectID reads the oid claim of a token the token endpoint just issued.
// The signature is not checked: the token came from the token endpoint over
// TLS and goes back only to Graph, which checks it.
func (c *Client) objectID(token string) (string, error) {
	var claims struct {
		OID string `json:"oid"`
	}
	parts := strings.Split(token, ".")
	if len(parts) == 3 {
		if payload, err := base64.RawURLEncoding.DecodeString(parts[1]); err == nil {
			_ = json.Unmarshal(payload, &claims)
		}
	}
	if claims.OID == "" {
		return "", errorf("The token Entra issued to the service app %s carries no oid claim, so Straza cannot name the app as the owner of the groups it creates. "+
			"Check that identity.entra.tenant names an Entra tenant and identity.entra.clientId the Straza service app in it, then Straza tries again.", c.clientID)
	}
	return claims.OID, nil
}

// tokenRefused is the sentence for a token answer other than 200, chosen by
// the first numeric code of Entra's error. The codes with a fix of their own
// cover a certificate the app does not hold, an app the tenant does not have
// and an app that is disabled. Entra's description stays out, because it
// names tenants and apps and may repeat what was sent.
func (c *Client) tokenRefused(status int, body []byte, kid string) error {
	var e struct {
		Error   string            `json:"error"`
		Codes   []json.RawMessage `json:"error_codes"`
		TraceID string            `json:"trace_id"`
	}
	if json.Unmarshal(body, &e) != nil || e.Error == "" {
		return errorf("Entra answered the token request of the service app %s with HTTP %d and no OAuth error. Check identity.entra.tenant in strazad's config and that Entra is reachable, then Straza tries again.", c.clientID, status)
	}
	var aadsts int64
	if len(e.Codes) > 0 {
		var f float64
		if json.Unmarshal(e.Codes[0], &f) == nil && f > 0 && f < 1e9 && f == math.Trunc(f) {
			aadsts = int64(f)
		}
	}
	switch aadsts {
	case 700027:
		return errorf("Entra refused the Straza service app %s (AADSTS700027), because the app holds no certificate of the client assertion key %s. "+
			"An administrator uploads the certificate that strazactl signing-keys certificate client_assertion %s prints to that app registration, then Straza tries again.", c.clientID, kid, kid)
	case 700016:
		return errorf("Entra has no app %s in tenant %s (AADSTS700016). Check identity.entra.clientId and identity.entra.tenant in strazad's config against the app registration in the Entra admin centre, then Straza tries again.", c.clientID, c.tenant)
	case 7000112:
		return errorf("Entra says the Straza service app %s is disabled (AADSTS7000112), so Straza can neither read nor write its role groups. Ask the identity team why it was disabled. Straza tries again once it is enabled.", c.clientID)
	}
	with := plainCode(e.Error)
	if with == "" {
		with = "an error code Straza does not repeat"
	}
	if aadsts > 0 {
		with += fmt.Sprintf(" (AADSTS%d)", aadsts)
	}
	find := "by the time of the request"
	if guidForm.MatchString(e.TraceID) {
		find = "by the trace id " + strings.ToLower(e.TraceID)
	}
	return errorf("Entra refused the token request of the Straza service app %s with %s. An administrator finds the request in Entra's sign-in log %s, then Straza tries again.", c.clientID, with, find)
}
