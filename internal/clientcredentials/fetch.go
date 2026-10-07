package clientcredentials

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/strazahq/straza/internal/authn"
)

const (
	// maxAnswer caps what is read of a token answer. A token answer is a few
	// kilobytes, and the body is untrusted.
	maxAnswer = 64 << 10
	// maxCode and maxDescription cap what is kept of an OAuth error.
	maxCode        = 40
	maxDescription = 200
	// maxLifetime caps how long an entry is kept, whatever lifetime the
	// provider names, so the arithmetic cannot overflow and a token that
	// lives for years is still fetched again within a day.
	maxLifetime   = 24 * time.Hour
	assertionType = "urn:ietf:params:oauth:client-assertion-type:jwt-bearer"
)

// longRun matches text that may be a credential: an unbroken run of the
// characters tokens are made of. A JWT and an opaque token both match, and
// the words of an error description do not.
var longRun = regexp.MustCompile(`[A-Za-z0-9_+=.-]{32,}`)

// fetch signs one assertion for the request's client and runs the grant. It
// returns the token and its lifetime, or a refusal. The assertion and the
// token go nowhere but the request and the return value.
func (t *Tokens) fetch(ctx context.Context, p Provider, r Request) (string, time.Duration, error) {
	assertion, err := t.signer.SignAssertion(t.now(), r.ClientID, p.Audience)
	if err != nil {
		return "", 0, t.unsigned(r, err)
	}
	form := url.Values{
		"grant_type":            {"client_credentials"},
		"client_id":             {r.ClientID},
		"client_assertion_type": {assertionType},
		"client_assertion":      {assertion},
	}
	if len(p.Scopes) > 0 {
		form.Set("scope", strings.Join(p.Scopes, " "))
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.TokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		t.log.Warn("client credentials: the token endpoint is not a usable URL", "provider", r.Provider)
		return "", 0, refuse(r, "The token endpoint of %s is not a usable URL. Correct oauth.providers.%s.tokenUrl in strazad's config, then call again.", r.Provider, r.Provider)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")

	at := shown(p.TokenURL)
	resp, err := t.http.Do(req)
	if err != nil {
		return "", 0, t.unanswered(r, at, err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxAnswer+1))
	if err != nil {
		return "", 0, t.unanswered(r, at, err)
	}
	warn := func(what string, args ...any) {
		t.log.Warn("client credentials: "+what, append([]any{"provider", r.Provider, "server", r.Server, "client", r.ClientID, "status", resp.StatusCode}, args...)...)
	}
	switch {
	case len(body) > maxAnswer:
		warn("the answer is over the size cap")
		return "", 0, asked(r, "%s answered the token request with more than 64 KB, which no token answer needs, so the call is refused. Check oauth.providers.%s.tokenUrl in strazad's config, then call again.", r.Provider, r.Provider)
	case resp.StatusCode >= 300 && resp.StatusCode < 400:
		warn("the token endpoint answered with a redirect")
		return "", 0, asked(r, "%s answered the token request at %s with a redirect, which Straza never follows with a client assertion. Set oauth.providers.%s.tokenUrl in strazad's config to the provider's final token endpoint, then call again.", r.Provider, at, r.Provider)
	case resp.StatusCode != http.StatusOK:
		return "", 0, t.refused(r, at, resp.StatusCode, body, assertion, warn)
	}

	var ans struct {
		AccessToken string  `json:"access_token"`
		TokenType   string  `json:"token_type"`
		ExpiresIn   float64 `json:"expires_in"`
	}
	switch err := json.Unmarshal(body, &ans); {
	case err != nil:
		warn("the answer is not the JSON of RFC 6749")
		return "", 0, asked(r, "%s answered the token request with something other than the JSON of RFC 6749, so the call is refused. Check oauth.providers.%s.tokenUrl in strazad's config, then call again.", r.Provider, r.Provider)
	case ans.AccessToken == "":
		warn("the answer holds no access token")
		return "", 0, asked(r, "%s answered the token request without an access token, so the call is refused. Ask the identity team to check the client %s at the provider, then call again.", r.Provider, r.ClientID)
	case !strings.EqualFold(ans.TokenType, "Bearer"):
		warn("the token is not a Bearer token")
		return "", 0, asked(r, "%s answered with a token that is not a Bearer token, so the call is refused. Ask the identity team to issue Bearer access tokens to the client %s, then call again.", r.Provider, r.ClientID)
	case ans.ExpiresIn < 1:
		warn("the token names no lifetime")
		return "", 0, asked(r, "%s answered with a token that names no lifetime (expires_in), so Straza can neither keep nor renew it and the call is refused. Ask the identity team to set an access token lifetime for the client %s, then call again.", r.Provider, r.ClientID)
	}
	lifetime := time.Duration(min(ans.ExpiresIn, maxLifetime.Seconds())) * time.Second
	t.log.Debug("client credentials: token fetched", "provider", r.Provider, "server", r.Server, "client", r.ClientID, "lifetime", lifetime.String())
	return ans.AccessToken, lifetime, nil
}

// unsigned is the refusal for an assertion that could not be signed. Each key
// state has its own next step, and any other failure is logged and not told.
func (t *Tokens) unsigned(r Request, err error) *refusal {
	switch {
	case errors.Is(err, authn.ErrAssertionKeyStaged):
		return refuse(r, "The client assertion key was created moments ago and starts signing within a minute. Call again then.")
	case errors.Is(err, authn.ErrNoAssertionKey):
		return refuse(r, "This Straza deployment has no client assertion key that signs: none was created yet, or the last one was retired. An administrator runs strazactl signing-keys rotate client_assertion, and agents can call about a minute later.")
	case errors.Is(err, authn.ErrAssertionKeysStale):
		return refuse(r, "This Straza replica has lost contact with its key store and signs nothing until it reads the keys again. An administrator checks the replica's database connection in the strazad log, then the agent calls again.")
	}
	t.log.Error("client credentials: the client assertion was not signed", "provider", r.Provider, "server", r.Server, "client", r.ClientID, "err", err)
	return refuse(r, "Straza could not sign the client assertion. An administrator reads the reason in the strazad log, then the agent calls again.")
}

// unanswered is the refusal for a request that got no HTTP answer. The error
// of the HTTP client names the URL and the cause and never the request body.
func (t *Tokens) unanswered(r Request, at string, err error) *refusal {
	var verify *tls.CertificateVerificationError
	var unknown x509.UnknownAuthorityError
	var host x509.HostnameError
	if errors.As(err, &verify) || errors.As(err, &unknown) || errors.As(err, &host) {
		t.log.Warn("client credentials: the provider's certificate is not trusted", "provider", r.Provider, "server", r.Server, "client", r.ClientID)
		return asked(r, "This machine does not trust the certificate %s presents at %s, so the call is refused. Add the provider's certificate authority to the trust store of the machine that runs strazad, then call again.", r.Provider, at)
	}
	// The cause is the transport's own error under the URL, which is left out
	// because an operator's token URL may carry a query.
	cause := err
	var ue *url.Error
	if errors.As(err, &ue) {
		cause = ue.Err
	}
	t.log.Warn("client credentials: the provider did not answer", "provider", r.Provider, "server", r.Server, "client", r.ClientID, "cause", cause.Error())
	return asked(r, "%s did not answer at %s within 3 seconds, so the call is refused. Check that the provider is up and reachable from Straza, then call again.", r.Provider, at)
}

// refused is the refusal for an HTTP status other than 200. Only the OAuth
// error code reaches the sentence, and only when it is a plain code. The
// provider's description goes to the log, bounded and only when it cannot be
// a credential.
func (t *Tokens) refused(r Request, at string, status int, body []byte, assertion string, warn func(string, ...any)) *refusal {
	var oauth struct {
		Error       string `json:"error"`
		Description string `json:"error_description"`
	}
	if json.Unmarshal(body, &oauth) != nil || oauth.Error == "" {
		warn("the token endpoint answered without an OAuth error")
		return asked(r, "%s answered the token request at %s with HTTP %d and no OAuth error. Check oauth.providers.%s.tokenUrl in strazad's config and that the provider is healthy, then call again.", r.Provider, at, status, r.Provider)
	}
	code := plainCode(oauth.Error)
	warn("the provider refused the client", "error", code, "description", logSafe(oauth.Description, assertion))
	const readLog = "An administrator reads the provider's own words in the strazad log, then the agent calls again."
	switch code {
	case "":
		return asked(r, "%s refused the client %s with an error code Straza does not repeat. %s", r.Provider, r.ClientID, readLog)
	case "invalid_client":
		return asked(r, "%s refused the client %s: invalid_client. The provider has no client of that name that trusts this deployment's client assertion keys. "+
			"Ask the identity team to create the client %s there, signing in with a signed JWT (private_key_jwt) and the key address %s, then call again.",
			r.Provider, r.ClientID, r.ClientID, t.keysURL)
	case "unauthorized_client":
		return asked(r, "%s refused the client %s: unauthorized_client. The client exists and may not use the client credentials grant. "+
			"Ask the identity team to allow that grant (service accounts) for the client %s, then call again.", r.Provider, r.ClientID, r.ClientID)
	case "invalid_scope":
		return asked(r, "%s refused the client %s: invalid_scope. The provider does not grant this client the scopes in oauth.providers.%s.clientCredentials.scopes. "+
			"Ask the identity team to assign them to the client, or correct the list in strazad's config, then call again.", r.Provider, r.ClientID, r.Provider)
	}
	return asked(r, "%s refused the client %s: %s. %s", r.Provider, r.ClientID, code, readLog)
}

// plainCode returns an OAuth error code that is safe to repeat: lower case
// letters and underscores, as every code of RFC 6749 section 5.2 is. Anything
// else answers the empty string.
func plainCode(code string) string {
	if len(code) > maxCode {
		return ""
	}
	for _, c := range code {
		if (c < 'a' || c > 'z') && c != '_' {
			return ""
		}
	}
	return code
}

// logSafe prepares a provider's error description for the log: printable
// ASCII only, cut to maxDescription, and withheld as a whole when it repeats
// the assertion or holds a run of characters that could be a token.
func logSafe(desc, assertion string) string {
	if strings.Contains(desc, assertion) || longRun.MatchString(desc) {
		return "withheld, it may carry a credential"
	}
	clean := strings.Map(func(c rune) rune {
		if c < ' ' || c > '~' {
			return ' '
		}
		return c
	}, desc)
	if len(clean) > maxDescription {
		clean = clean[:maxDescription]
	}
	return clean
}

// shown is the token endpoint as a sentence may name it: no query and no
// user information, which an operator's URL may carry.
func shown(tokenURL string) string {
	u, err := url.Parse(tokenURL)
	if err != nil {
		return "its token endpoint"
	}
	u.User, u.RawQuery, u.Fragment = nil, "", ""
	return u.String()
}
