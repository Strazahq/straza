package clientcredentials

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/strazahq/straza/internal/authn"
)

var errNoKeyForTest = authn.ErrNoAssertionKey

// TestFetchIsThePlainGrant pins the request every provider must understand:
// the client credentials grant of RFC 6749 section 4.4 with the client
// assertion of RFC 7523 section 2.2, and nothing of Straza's own.
func TestFetchIsThePlainGrant(t *testing.T) {
	r := newRig(t)
	var method, path, query string
	r.idp.setHandler(func(w http.ResponseWriter, req *http.Request) {
		method, path, query = req.Method, req.URL.Path, req.URL.RawQuery
		answer(w, http.StatusOK, map[string]any{"access_token": "tok", "token_type": "bearer", "expires_in": 300})
	})
	if got, err := r.tokens.Token(bg, sam()); err != nil || got != "tok" {
		t.Fatalf("Token = %q, %v", got, err)
	}
	if method != http.MethodPost || path != "/token" || query != "realm=x" {
		t.Errorf("request = %s %s?%s, want POST to the configured token endpoint with its query", method, path, query)
	}
	form := r.idp.lastForm()
	want := map[string]string{
		"grant_type":            "client_credentials",
		"client_id":             "sam-sre-agent",
		"client_assertion_type": "urn:ietf:params:oauth:client-assertion-type:jwt-bearer",
		"client_assertion":      testAssertion + "1",
		"scope":                 "midpoint-mcp profile",
	}
	if len(form) != len(want) {
		t.Errorf("form = %v, want exactly the fields %v", form, want)
	}
	for k, v := range want {
		if form.Get(k) != v {
			t.Errorf("form[%s] = %q, want %q", k, form.Get(k), v)
		}
	}
	r.idp.mu.Lock()
	h := r.idp.headers[0]
	r.idp.mu.Unlock()
	if h.Get("Content-Type") != "application/x-www-form-urlencoded" || h.Get("Accept") != "application/json" || h.Get("Authorization") != "" {
		t.Errorf("headers = %v", h)
	}
	if r.signer.clientID != "sam-sre-agent" || r.signer.audience != testAudience {
		t.Errorf("signed for %q at %q, want the request's client and the configured audience", r.signer.clientID, r.signer.audience)
	}
}

// TestNoScopeFieldWithoutScopes: a provider block without scopes asks for the
// client's defaults, which is an absent field and not an empty one.
func TestNoScopeFieldWithoutScopes(t *testing.T) {
	r := newRig(t)
	p := r.tokens.providers["keycloak"]
	p.Scopes = nil
	r.tokens.providers["keycloak"] = p
	if _, err := r.tokens.Token(bg, sam()); err != nil {
		t.Fatal(err)
	}
	if _, ok := r.idp.lastForm()["scope"]; ok {
		t.Error("the request carries a scope field although none is configured")
	}
}

// TestRefusalSentences pins every refusal whole. Each says what failed, why,
// and what to do next, and none repeats text the provider chose.
func TestRefusalSentences(t *testing.T) {
	const open = "agent sam-sre-agent could not get a midpoint token. "
	oauthErr := func(status int, code, desc string) http.HandlerFunc {
		return func(w http.ResponseWriter, _ *http.Request) {
			answer(w, status, map[string]any{"error": code, "error_description": desc})
		}
	}
	ok := func(body map[string]any) http.HandlerFunc {
		return func(w http.ResponseWriter, _ *http.Request) { answer(w, http.StatusOK, body) }
	}
	cases := []struct {
		name    string
		signer  error
		handler http.HandlerFunc
		want    string
	}{
		{name: "no key", signer: authn.ErrNoAssertionKey,
			want: "This Straza deployment has no client assertion key that signs: none was created yet, or the last one was retired. An administrator runs strazactl signing-keys rotate client_assertion, and agents can call about a minute later."},
		{name: "staged key", signer: authn.ErrAssertionKeyStaged,
			want: "The client assertion key was created moments ago and starts signing within a minute. Call again then."},
		{name: "stale replica", signer: authn.ErrAssertionKeysStale,
			want: "This Straza replica has lost contact with its key store and signs nothing until it reads the keys again. An administrator checks the replica's database connection in the strazad log, then the agent calls again."},
		{name: "another signer failure", signer: errors.New("rsa: internal error with secret material"),
			want: "Straza could not sign the client assertion. An administrator reads the reason in the strazad log, then the agent calls again."},
		{name: "invalid_client", handler: oauthErr(401, "invalid_client", "Invalid client or Invalid client credentials"),
			want: "keycloak refused the client sam-sre-agent: invalid_client. The provider has no client of that name that trusts this deployment's client assertion keys. Ask the identity team to create the client sam-sre-agent there, signing in with a signed JWT (private_key_jwt) and the key address " + testKeysURL + ", then call again."},
		{name: "unauthorized_client", handler: oauthErr(400, "unauthorized_client", "Client not enabled to retrieve service account"),
			want: "keycloak refused the client sam-sre-agent: unauthorized_client. The client exists and may not use the client credentials grant. Ask the identity team to allow that grant (service accounts) for the client sam-sre-agent, then call again."},
		{name: "invalid_scope", handler: oauthErr(400, "invalid_scope", "Invalid scopes: midpoint-mcp"),
			want: "keycloak refused the client sam-sre-agent: invalid_scope. The provider does not grant this client the scopes in oauth.providers.keycloak.clientCredentials.scopes. Ask the identity team to assign them to the client, or correct the list in strazad's config, then call again."},
		{name: "another oauth error", handler: oauthErr(400, "invalid_request", "Missing form parameter: grant_type"),
			want: "keycloak refused the client sam-sre-agent: invalid_request. An administrator reads the provider's own words in the strazad log, then the agent calls again."},
		{name: "an error code that is not a code", handler: oauthErr(400, "Invalid <script>alert(1)</script>", ""),
			want: "keycloak refused the client sam-sre-agent with an error code Straza does not repeat. An administrator reads the provider's own words in the strazad log, then the agent calls again."},
		{name: "a status without an oauth error", handler: func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, "<html>bad gateway</html>", http.StatusBadGateway)
		},
			want: "keycloak answered the token request at %URL%/token with HTTP 502 and no OAuth error. Check oauth.providers.keycloak.tokenUrl in strazad's config and that the provider is healthy, then call again."},
		{name: "a redirect is never followed", handler: func(w http.ResponseWriter, req *http.Request) {
			http.Redirect(w, req, "https://evil.example/token", http.StatusTemporaryRedirect)
		},
			want: "keycloak answered the token request at %URL%/token with a redirect, which Straza never follows with a client assertion. Set oauth.providers.keycloak.tokenUrl in strazad's config to the provider's final token endpoint, then call again."},
		{name: "token type mac", handler: ok(map[string]any{"access_token": "tok", "token_type": "mac", "expires_in": 300}),
			want: "keycloak answered with a token that is not a Bearer token, so the call is refused. Ask the identity team to issue Bearer access tokens to the client sam-sre-agent, then call again."},
		{name: "no token type", handler: ok(map[string]any{"access_token": "tok", "expires_in": 300}),
			want: "keycloak answered with a token that is not a Bearer token, so the call is refused. Ask the identity team to issue Bearer access tokens to the client sam-sre-agent, then call again."},
		{name: "no expiry", handler: ok(map[string]any{"access_token": "tok", "token_type": "Bearer"}),
			want: "keycloak answered with a token that names no lifetime (expires_in), so Straza can neither keep nor renew it and the call is refused. Ask the identity team to set an access token lifetime for the client sam-sre-agent, then call again."},
		{name: "a negative expiry", handler: ok(map[string]any{"access_token": "tok", "token_type": "Bearer", "expires_in": -5}),
			want: "keycloak answered with a token that names no lifetime (expires_in), so Straza can neither keep nor renew it and the call is refused. Ask the identity team to set an access token lifetime for the client sam-sre-agent, then call again."},
		{name: "no access token", handler: ok(map[string]any{"token_type": "Bearer", "expires_in": 300}),
			want: "keycloak answered the token request without an access token, so the call is refused. Ask the identity team to check the client sam-sre-agent at the provider, then call again."},
		{name: "not json", handler: func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte("access_token=tok&token_type=bearer"))
		},
			want: "keycloak answered the token request with something other than the JSON of RFC 6749, so the call is refused. Check oauth.providers.keycloak.tokenUrl in strazad's config, then call again."},
		{name: "an answer over the size cap", handler: ok(map[string]any{"access_token": strings.Repeat("a", maxAnswer), "token_type": "Bearer", "expires_in": 300}),
			want: "keycloak answered the token request with more than 64 KB, which no token answer needs, so the call is refused. Check oauth.providers.keycloak.tokenUrl in strazad's config, then call again."},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := newRig(t)
			r.signer.err = tc.signer
			r.idp.setHandler(tc.handler)
			got, err := r.tokens.Token(bg, sam())
			want := open + strings.ReplaceAll(tc.want, "%URL%", r.idp.srv.URL)
			if err == nil || err.Error() != want {
				t.Fatalf("err = %v\nwant  %s", err, want)
			}
			if got != "" {
				t.Fatalf("a refusal handed out %q", got)
			}
			if tc.signer != nil && r.idp.requests.Load() != 0 {
				t.Error("a request left Straza although nothing was signed")
			}
		})
	}
}

// TestProviderWithoutTheBlockIsRefused: a manifest can outlive the config
// block it was installed against, and the resolver then says what to add.
func TestProviderWithoutTheBlockIsRefused(t *testing.T) {
	r := newRig(t)
	q := sam()
	q.Provider = "okta"
	const want = "agent sam-sre-agent could not get a midpoint token. Server midpoint sets credential.agents to client_credentials, and the provider okta has no clientCredentials settings. Add oauth.providers.okta.clientCredentials.assertionAudience to strazad's config, or set credential.agents to own, sponsor or shared."
	if _, err := r.tokens.Token(bg, q); err == nil || err.Error() != want {
		t.Fatalf("err = %v\nwant  %s", err, want)
	}
	if r.signer.calls != 0 {
		t.Error("an assertion was signed for a provider the operator never named")
	}
}

// TestProviderThatDoesNotAnswer pins the fetch limit and its sentence, with
// the limit shortened so the test stays fast.
func TestProviderThatDoesNotAnswer(t *testing.T) {
	r := newRig(t)
	r.tokens.limit = 50 * time.Millisecond
	hold := make(chan struct{})
	t.Cleanup(func() { close(hold) })
	r.idp.setHandler(func(http.ResponseWriter, *http.Request) { <-hold })
	start := time.Now()
	_, err := r.tokens.Token(bg, sam())
	want := "agent sam-sre-agent could not get a midpoint token. keycloak did not answer at " + r.idp.srv.URL + "/token within 3 seconds, so the call is refused. Check that the provider is up and reachable from Straza, then call again."
	if err == nil || err.Error() != want {
		t.Fatalf("err = %v\nwant  %s", err, want)
	}
	if took := time.Since(start); took > 2*time.Second {
		t.Fatalf("the refusal took %v, the limit did not hold", took)
	}
	if fetchLimit != 3*time.Second {
		t.Fatalf("fetchLimit = %v, want the three seconds the sentence names", fetchLimit)
	}
}

// TestProviderThatIsDown: a closed port is the same refusal, at once.
func TestProviderThatIsDown(t *testing.T) {
	r := newRig(t)
	url := r.idp.srv.URL
	r.idp.srv.Close()
	_, err := r.tokens.Token(bg, sam())
	want := "agent sam-sre-agent could not get a midpoint token. keycloak did not answer at " + url + "/token within 3 seconds, so the call is refused. Check that the provider is up and reachable from Straza, then call again."
	if err == nil || err.Error() != want {
		t.Fatalf("err = %v\nwant  %s", err, want)
	}
}

// TestProviderCertificateIsVerified: a certificate this machine does not
// trust is refused with its own sentence, and the same server passes once its
// CA is trusted, which proves the refusal was the verification.
func TestProviderCertificateIsVerified(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		answer(w, http.StatusOK, map[string]any{"access_token": "tok-tls", "token_type": "Bearer", "expires_in": 300})
	}))
	t.Cleanup(srv.Close)
	build := func(transport http.RoundTripper) *Tokens {
		return New(Options{
			Providers: map[string]Provider{"keycloak": {TokenURL: srv.URL + "/token", Audience: testAudience}},
			Signer:    &fakeSigner{}, KeysURL: testKeysURL, Transport: transport,
		})
	}
	_, err := build(nil).Token(bg, sam())
	want := "agent sam-sre-agent could not get a midpoint token. This machine does not trust the certificate keycloak presents at " + srv.URL + "/token, so the call is refused. Add the provider's certificate authority to the trust store of the machine that runs strazad, then call again."
	if err == nil || err.Error() != want {
		t.Fatalf("err = %v\nwant  %s", err, want)
	}
	pool := x509.NewCertPool()
	pool.AddCert(srv.Certificate())
	trusted := &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}}
	if got, err := build(trusted).Token(bg, sam()); err != nil || got != "tok-tls" {
		t.Fatalf("Token with the CA trusted = %q, %v", got, err)
	}
}

// TestNothingLeaks runs a success and every kind of hostile answer, then
// looks for the token, the assertion and the provider's chosen text in every
// error and every log line.
func TestNothingLeaks(t *testing.T) {
	const token = "eyJhbGciOiJSUzI1NiJ9.eyJhenAiOiJzYW0ifQ.dG9rZW4tc2lnbmF0dXJl"
	const opaque = "0123456789abcdef0123456789abcdef0123456789abcdef"
	echo := func(field string) http.HandlerFunc {
		return func(w http.ResponseWriter, req *http.Request) {
			assertion := req.PostForm.Get("client_assertion")
			body := map[string]any{"error": "invalid_client", "error_description": "plain words"}
			switch field {
			case "description":
				body["error_description"] = "you sent " + assertion
			case "code":
				body["error"] = assertion
			case "token in description":
				body["error_description"] = "issued " + token + " earlier"
			case "opaque in description":
				body["error_description"] = "session " + opaque
			case "control characters":
				body["error_description"] = "line one\nlevel=ERROR msg=forged\x1b[31m"
			case "long":
				body["error_description"] = strings.Repeat("word ", 200)
			}
			answer(w, http.StatusUnauthorized, body)
		}
	}
	for _, field := range []string{"description", "code", "token in description", "opaque in description", "control characters", "long", "plain"} {
		t.Run(field, func(t *testing.T) {
			r := newRig(t)
			r.idp.setHandler(echo(field))
			_, err := r.tokens.Token(bg, sam())
			if err == nil {
				t.Fatal("the refused client got a token")
			}
			logs := r.logs.String()
			for _, secret := range []string{testAssertion, token, opaque, "eyJ", "\x1b"} {
				if strings.Contains(err.Error(), secret) {
					t.Errorf("the error carries %q: %v", secret, err)
				}
				if strings.Contains(logs, secret) {
					t.Errorf("the log carries %q: %s", secret, logs)
				}
			}
			if strings.Contains(err.Error(), "plain words") {
				t.Errorf("the error repeats the provider's description: %v", err)
			}
			if n := strings.Count(strings.TrimSpace(logs), "\n"); n != 0 {
				t.Errorf("the provider's text made %d extra log lines: %s", n, logs)
			}
			if field == "plain" && !strings.Contains(logs, "plain words") {
				t.Errorf("the log lost a harmless description: %s", logs)
			}
			if field == "long" && strings.Count(logs, "word ") > 45 {
				t.Errorf("the log kept more than 200 characters of the description: %d words", strings.Count(logs, "word "))
			}
		})
	}
	t.Run("success", func(t *testing.T) {
		r := newRig(t)
		r.idp.setHandler(func(w http.ResponseWriter, _ *http.Request) {
			answer(w, http.StatusOK, map[string]any{"access_token": token, "token_type": "Bearer", "expires_in": 300})
		})
		if got, err := r.tokens.Token(bg, sam()); err != nil || got != token {
			t.Fatalf("Token = %q, %v", got, err)
		}
		logs := r.logs.String()
		if strings.Contains(logs, token) || strings.Contains(logs, "eyJ") {
			t.Errorf("the log carries the token or the assertion: %s", logs)
		}
		if !strings.Contains(logs, "sam-sre-agent") {
			t.Errorf("the fetch left no log line naming the client: %s", logs)
		}
		if s := fmt.Sprintf("%v %+v", r.tokens, r.tokens); strings.Contains(s, token) {
			t.Errorf("printing the cache shows the token: %s", s)
		}
	})
}

// TestAnswerIsNotReadPastTheCap: the cap bounds what is read, not only what
// is accepted, so an endless answer costs no memory and no time.
func TestAnswerIsNotReadPastTheCap(t *testing.T) {
	r := newRig(t)
	const total = 64 << 20
	written := make(chan int, 1)
	r.idp.setHandler(func(w http.ResponseWriter, _ *http.Request) {
		chunk := []byte(strings.Repeat("a", 1<<20))
		n := 0
		for n < total {
			m, err := w.Write(chunk)
			n += m
			if err != nil {
				break
			}
		}
		written <- n
	})
	if _, err := r.tokens.Token(bg, sam()); err == nil || !strings.Contains(err.Error(), "more than 64 KB") {
		t.Fatalf("err = %v, want the size refusal", err)
	}
	select {
	case n := <-written:
		if n >= total {
			t.Fatalf("the whole %d byte answer was read", n)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the provider is still writing: the answer was not cut off")
	}
}

// TestLifetimeCountsFromBeforeTheRequest: a slow answer must not stretch the
// entry past the token's own end.
func TestLifetimeCountsFromBeforeTheRequest(t *testing.T) {
	r := newRig(t)
	r.idp.setHandler(func(w http.ResponseWriter, _ *http.Request) {
		n := r.idp.requests.Load()
		if n == 1 {
			r.clock.add(10 * time.Second)
		}
		answer(w, http.StatusOK, map[string]any{"access_token": fmt.Sprintf("tok-%d", n), "token_type": "Bearer", "expires_in": 300})
	})
	if got, err := r.tokens.Token(bg, sam()); err != nil || got != "tok-1" {
		t.Fatalf("Token = %q, %v", got, err)
	}
	r.clock.add(290 * time.Second)
	if got, err := r.tokens.Token(bg, sam()); err != nil || got != "tok-2" {
		t.Fatalf("300 s after the request was sent Token = %q, %v, want a fresh token and never the first", got, err)
	}
}

// TestDefaultsAreTheLimitsTheSentencesName pins what New sets, because every
// other test may shorten a limit.
func TestDefaultsAreTheLimitsTheSentencesName(t *testing.T) {
	tk := New(Options{Signer: &fakeSigner{}})
	if tk.limit != 3*time.Second {
		t.Errorf("limit = %v, want 3 s", tk.limit)
	}
	if tk.http.Timeout != 0 && tk.http.Timeout < tk.limit {
		t.Errorf("the client's own timeout %v undercuts the limit", tk.http.Timeout)
	}
	if tk.http.CheckRedirect == nil || tk.http.Jar != nil {
		t.Error("the client follows redirects or keeps cookies")
	}
	if tr, ok := tk.http.Transport.(*http.Transport); ok && tr.TLSClientConfig != nil && tr.TLSClientConfig.InsecureSkipVerify {
		t.Error("the client skips certificate verification")
	}
}

// TestAbsurdLifetimeIsCapped: a lifetime that would overflow the arithmetic
// is kept for a day, not dropped at once and not kept for ever.
func TestAbsurdLifetimeIsCapped(t *testing.T) {
	r := newRig(t)
	r.idp.setHandler(func(w http.ResponseWriter, _ *http.Request) {
		answer(w, http.StatusOK, map[string]any{"access_token": fmt.Sprintf("tok-%d", r.idp.requests.Load()), "token_type": "Bearer", "expires_in": 1e300})
	})
	for range 3 {
		if got, err := r.tokens.Token(bg, sam()); err != nil || got != "tok-1" {
			t.Fatalf("Token = %q, %v, want the cached first token", got, err)
		}
	}
	r.clock.add(maxLifetime)
	if got, err := r.tokens.Token(bg, sam()); err != nil || got != "tok-2" {
		t.Fatalf("Token a day later = %q, %v, want a fresh token", got, err)
	}
}

// TestProviderTextIsMadeSafe pins the two filters for text the provider
// chose, directly, because the log handler escapes on its own and would hide
// a filter that stopped working.
func TestProviderTextIsMadeSafe(t *testing.T) {
	for in, want := range map[string]string{
		"invalid_client":            "invalid_client",
		"unsupported_grant_type":    "unsupported_grant_type",
		"":                          "",
		"Invalid_Client":            "",
		"invalid client":            "",
		"invalid_client\n":          "",
		"invalid-client":            "",
		"x1":                        "",
		strings.Repeat("a", 40):     strings.Repeat("a", 40),
		strings.Repeat("a", 41):     "",
		"<script>alert(1)</script>": "",
	} {
		if got := plainCode(in); got != want {
			t.Errorf("plainCode(%q) = %q, want %q", in, got, want)
		}
	}
	const withheld = "withheld, it may carry a credential"
	for in, want := range map[string]string{
		"Signature on JWT token failed validation":                                        "Signature on JWT token failed validation",
		"line one\nlevel=ERROR msg=forged\x1b[31m":                                        "line one level=ERROR msg=forged [31m",
		"tab\there and a null \x00 and é":                                                 "tab here and a null   and  ",
		"you sent " + testAssertion:                                                       withheld,
		"key 0123456789abcdef0123456789abcdef":                                            withheld,
		"at http://host.docker.internal:8480/realms/straza/protocol/openid-connect/token": "at http://host.docker.internal:8480/realms/straza/protocol/openid-connect/token",
		strings.Repeat("word ", 60):                                                       strings.Repeat("word ", 40),
	} {
		if got := logSafe(in, testAssertion); got != want {
			t.Errorf("logSafe(%q) = %q, want %q", in, got, want)
		}
	}
}
