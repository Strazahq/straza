package oidcflow

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"html/template"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
	"unicode"
)

// signInWait is how long SignIn waits for the browser's answer.
var signInWait = 5 * time.Minute

// sentence is an error whose text is a whole sentence the person reads as
// it is.
type sentence string

func (s sentence) Error() string { return string(s) }

func sentencef(format string, a ...any) error { return sentence(fmt.Sprintf(format, a...)) }

// Nonce returns a fresh sign-in nonce: 16 random bytes in base64url, after
// jkt and a dot when jkt is set. With jkt the RFC 7638 thumbprint of the
// key being enrolled, the nonce names that key, which strazad checks at
// /v1/enroll.
func Nonce(jkt string) string {
	r := random(16)
	if jkt == "" {
		return r
	}
	return jkt + "." + r
}

// random returns n random bytes in base64url without padding.
func random(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b) // crypto/rand.Read never fails since Go 1.24
	return base64.RawURLEncoding.EncodeToString(b)
}

// pkcePair returns a PKCE verifier and its S256 challenge (RFC 7636).
func pkcePair() (verifier, challenge string) {
	verifier = random(32)
	sum := sha256.Sum256([]byte(verifier))
	return verifier, base64.RawURLEncoding.EncodeToString(sum[:])
}

// SignIn runs the browser lane at flow and returns the ID token. It listens
// on 127.0.0.1 at flow.LoopbackPort, or on a free port when that is 0,
// hands open the authorization link, and waits for one callback. A
// callback whose state does not match is answered with an error page and
// ignored. The matching one ends the sign-in: SignIn redeems its code with
// the PKCE verifier and checks that the ID token carries nonce. SignIn
// closes the port before it returns and gives up after 5 minutes. Every
// sentence names command, such as "straza enroll", as the one to run
// again. open must not wait for the sign-in. SignIn never accepts a pasted
// redirect, because only a callback on this machine reaches the port.
func SignIn(ctx context.Context, httpc *http.Client, flow Flow, nonce, command string, open func(link string)) (string, error) {
	if err := flow.CheckBrowser(command); err != nil {
		return "", err
	}
	if u, err := url.Parse(flow.AuthorizeURL); err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
		return "", sentencef("The identity provider %s advertises the authorization endpoint %q, which is not an http or https URL, so Straza does not open it. "+
			"Ask IT to check the provider's OIDC discovery document.", flow.Issuer, flow.AuthorizeURL)
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	ln, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(flow.LoopbackPort)))
	if err != nil {
		if flow.LoopbackPort != 0 {
			return "", sentencef("Straza could not wait for the sign-in answer on port %d, because another program uses it. "+
				"Wait a minute and run `%s` again, or ask your Straza admin to change oidc.loopbackPort.", flow.LoopbackPort, command)
		}
		return "", sentencef("Straza could not wait for the sign-in answer on this computer: %v. Run `%s` again.", err, command)
	}
	// srv.Close misses a listener that Serve has not tracked yet, so the
	// port is closed here too before SignIn returns.
	defer func() { _ = ln.Close() }()
	verifier, challenge := pkcePair()
	cb := &callback{
		ctx: ctx, httpc: httpc, flow: flow, command: command, state: random(16), nonce: nonce, verifier: verifier,
		redirect: fmt.Sprintf("http://127.0.0.1:%d/callback", ln.Addr().(*net.TCPAddr).Port),
		done:     make(chan signInResult, 1),
	}
	srv := &http.Server{Handler: cb, ReadHeaderTimeout: 10 * time.Second}
	go func() { _ = srv.Serve(ln) }()
	open(cb.link(challenge))
	timer := time.NewTimer(signInWait)
	defer timer.Stop()
	select {
	case r := <-cb.done:
		// Shutdown closes the port at once and waits for the page to reach
		// the browser.
		sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if srv.Shutdown(sctx) != nil {
			_ = srv.Close()
		}
		return r.idToken, r.err
	case <-timer.C:
		_ = srv.Close()
		return "", sentencef("The browser sign-in did not finish within 5 minutes. Run `%s` again.", command)
	case <-ctx.Done():
		_ = srv.Close()
		return "", ctx.Err()
	}
}

// signInResult is how the one matching callback ended.
type signInResult struct {
	idToken string
	err     error
}

// callback serves the redirect of one sign-in at its loopback port.
type callback struct {
	ctx      context.Context
	httpc    *http.Client
	flow     Flow
	command  string
	state    string
	nonce    string
	verifier string
	redirect string
	claimed  atomic.Bool
	done     chan signInResult
}

// link is the authorization request: the sign-in parameters on the provider's
// endpoint, keeping any query the endpoint carries itself.
func (c *callback) link(challenge string) string {
	u, _ := url.Parse(c.flow.AuthorizeURL) // SignIn checked it
	q := u.Query()
	q.Set("response_type", "code")
	q.Set("client_id", c.flow.ClientID)
	q.Set("redirect_uri", c.redirect)
	q.Set("scope", "openid profile")
	q.Set("state", c.state)
	q.Set("nonce", c.nonce)
	q.Set("code_challenge", challenge)
	q.Set("code_challenge_method", "S256")
	// A fresh sign-in every time, so a live session cookie at the provider
	// cannot answer for the person. max_age also obliges the provider to put
	// auth_time in the ID token (OpenID Connect Core section 3.1.2.1), which
	// strazad holds to 10 minutes. It is that window, 600 seconds, and not 0,
	// because Microsoft Entra answers max_age=0 with an ID token that has
	// already expired.
	q.Set("prompt", "login")
	q.Set("max_age", "600")
	u.RawQuery = q.Encode()
	return u.String()
}

func (c *callback) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/callback" || r.Method != http.MethodGet {
		http.NotFound(w, r)
		return
	}
	q := r.URL.Query()
	if subtle.ConstantTimeCompare([]byte(q.Get("state")), []byte(c.state)) != 1 {
		page(w, http.StatusBadRequest, "This answer does not belong to the sign-in Straza is waiting for, so Straza ignored it. "+
			"Finish the sign-in in the browser tab that Straza opened.")
		return
	}
	if !c.claimed.CompareAndSwap(false, true) {
		page(w, http.StatusConflict, "This sign-in has already finished. You can close this tab.")
		return
	}
	tok, err := c.finish(q)
	if err != nil {
		page(w, http.StatusBadRequest, err.Error())
	} else {
		page(w, http.StatusOK, "You are signed in to Straza on this computer. You can close this tab.")
	}
	c.done <- signInResult{tok, err}
}

// finish turns the matching callback into the ID token.
func (c *callback) finish(q url.Values) (string, error) {
	if code := q.Get("error"); code != "" {
		return "", providerRefused(code, q.Get("error_description"), c.command)
	}
	if q.Get("code") == "" {
		return "", sentencef("The identity provider answered the sign-in without a code. Run `%s` again, and ask IT if it keeps happening.", c.command)
	}
	tok, err := c.redeem(q.Get("code"))
	if err != nil {
		return "", err
	}
	got, ok := idTokenNonce(tok)
	switch {
	case !ok:
		return "", sentencef("The identity provider sent an ID token Straza cannot read, so Straza cannot check it. "+
			"Run `%s` again, and ask IT if it keeps happening.", c.command)
	case subtle.ConstantTimeCompare([]byte(got), []byte(c.nonce)) != 1:
		return "", sentencef("Straza refused this sign-in because the identity provider's answer was made for a different sign-in. "+
			"Run `%s` again and finish the sign-in in the browser it opens.", c.command)
	}
	return tok, nil
}

// redeem trades the code for the ID token at the token endpoint, with the
// verifier that proves this listener asked for it.
func (c *callback) redeem(code string) (string, error) {
	form := url.Values{
		"grant_type": {"authorization_code"}, "code": {code}, "redirect_uri": {c.redirect},
		"client_id": {c.flow.ClientID}, "code_verifier": {c.verifier},
	}
	req, err := http.NewRequestWithContext(c.ctx, http.MethodPost, c.flow.TokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	resp, err := c.httpc.Do(req)
	if err != nil {
		return "", sentencef("Straza could not finish the sign-in at %s: %v. Run `%s` again.", c.flow.Issuer, err, c.command)
	}
	defer func() { _ = resp.Body.Close() }()
	var out struct {
		IDToken     string `json:"id_token"`
		Error       string `json:"error"`
		Description string `json:"error_description"`
	}
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	_ = json.Unmarshal(body, &out)
	switch {
	case out.Error != "":
		return "", providerRefused(out.Error, out.Description, c.command)
	case resp.StatusCode != http.StatusOK:
		return "", sentencef("The identity provider %s answered the sign-in with HTTP %d. Run `%s` again, and ask IT if it keeps happening.",
			c.flow.Issuer, resp.StatusCode, c.command)
	case out.IDToken == "":
		return "", sentencef("The identity provider finished the sign-in and sent no ID token. "+
			"Ask IT to allow the openid scope for the Straza client, then run `%s` again.", c.command)
	}
	return out.IDToken, nil
}

// idTokenNonce reads the nonce claim of a JWT without verifying it. strazad
// verifies the token, and the client only checks it answers its request.
func idTokenNonce(tok string) (string, bool) {
	parts := strings.Split(tok, ".")
	if len(parts) != 3 {
		return "", false
	}
	raw, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(parts[1], "="))
	var claims struct {
		Nonce string `json:"nonce"`
	}
	if err != nil || json.Unmarshal(raw, &claims) != nil {
		return "", false
	}
	return claims.Nonce, true
}

// providerRefused is the sentence for an OAuth error from the provider, its
// text cut short and stripped of control characters before a terminal or
// a page shows it.
func providerRefused(code, description, command string) error {
	said := printable(code)
	if d := strings.TrimRight(printable(description), ". "); d != "" {
		said = d + " (" + said + ")"
	}
	return sentencef("Your identity provider refused the sign-in and said: %s. Run `%s` again, and ask IT if it keeps happening.", said, command)
}

func printable(s string) string {
	s = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, strings.TrimSpace(s))
	if r := []rune(s); len(r) > 300 {
		s = string(r[:300])
	}
	return s
}

var pageTmpl = template.Must(template.New("page").Parse(`<!doctype html>
<meta charset="utf-8">
<title>Straza sign-in</title>
<style>body{font-family:system-ui;max-width:32rem;margin:4rem auto;padding:0 1rem}</style>
<h1>Straza sign-in</h1>
<p>{{.}}</p>
`))

// page answers the browser with one sentence, escaped by html/template, on
// a page that loads nothing and sends no referrer, since its address
// carries the code.
func page(w http.ResponseWriter, status int, text string) {
	h := w.Header()
	h.Set("Content-Type", "text/html; charset=utf-8")
	h.Set("Cache-Control", "no-store")
	h.Set("Referrer-Policy", "no-referrer")
	h.Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'")
	w.WriteHeader(status)
	_ = pageTmpl.Execute(w, text)
}
