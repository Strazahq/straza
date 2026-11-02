package authn

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"fmt"
	"html/template"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/strazahq/straza/internal/store"
)

// codeTTL is how long an authorization code stays redeemable. The program
// that asked for it redeems it right after the redirect lands.
const codeTTL = 60 * time.Second

// The longest redirect_uri and nonce /oidc/authorize takes. A code holds
// both in memory, so neither may be as long as a request can carry.
const (
	maxRedirectBytes = 2048
	maxNonceBytes    = 512
)

// authorizeClients are the client ids /oidc/authorize and
// /oidc/device_authorization serve: the Straza programs a person runs on
// their own machine.
var authorizeClients = map[string]bool{"straza": true, "strazactl": true, "console": true}

// The pages /oidc/authorize shows when it cannot go on. A page, never a
// redirect, because the request's redirect is not trusted at that point.
const (
	unknownClientPage = "This sign-in link is not valid, because it does not name a Straza program. Start the sign-in again from Straza on your computer, for example with straza enroll."
	untrustedRedirect = "This sign-in link is not valid, because it would send the answer somewhere other than a Straza program on this computer. Start the sign-in again from Straza on your computer, for example with straza enroll."
	untrustedConsole  = "This sign-in link is not valid, because it would send the answer somewhere other than this server's console at %s. Open the console at that address and press Sign in again. If people reach the server at another address, set server.publicUrl to it."
	unreadableForm    = "This sign-in form could not be read. Start the sign-in again from Straza on your computer, for example with straza enroll."
)

// authCode is one issued authorization code and everything it is bound to.
type authCode struct {
	clientID    string
	redirectURI string
	challenge   string
	nonce       string
	userID      string
	authTime    time.Time // when the password was checked
	expiresAt   time.Time
}

// authzRequest is an authorization request whose client and redirect are
// trusted. The page carries its exported fields back in hidden fields.
type authzRequest struct {
	ClientID    string
	RedirectURI string
	State       string
	Nonce       string
	Challenge   string
	redirect    *url.URL
}

type authorizeView struct {
	Only  string        // the one account the page signs in, "" for every local user
	Error string        // the sentence shown above the form, or alone
	Req   *authzRequest // nil on a page that shows the error alone
}

var authorizeTmpl = template.Must(template.New("authorize").Parse(pageHead + `{{if .Only}}<p>This page signs in the emergency admin {{.Only}} and nobody else. Everyone else signs in at your identity provider.</p>{{end}}
{{if .Error}}<p class="err">{{.Error}}</p>{{end}}
{{with .Req}}<form method="post" action="/oidc/authorize">
<p>Enter your Straza username and password. {{if eq .ClientID "console"}}Then this browser takes you back to Straza.{{else}}The browser then returns the sign-in to the Straza program on this computer.{{end}}</p>
<input type="hidden" name="response_type" value="code">
<input type="hidden" name="client_id" value="{{.ClientID}}">
<input type="hidden" name="redirect_uri" value="{{.RedirectURI}}">
<input type="hidden" name="state" value="{{.State}}">
<input type="hidden" name="nonce" value="{{.Nonce}}">
<input type="hidden" name="code_challenge" value="{{.Challenge}}">
<input type="hidden" name="code_challenge_method" value="S256">
<label>Username</label><input name="username" autocomplete="username" autofocus>
<label>Password</label><input name="password" type="password" autocomplete="current-password">
<button type="submit">Sign in</button>
</form>{{end}}
`))

// handleAuthorizePage serves GET /oidc/authorize: it checks the request and
// shows the password form, which carries the request in hidden fields.
func (i *Issuer) handleAuthorizePage(w http.ResponseWriter, r *http.Request) {
	req, ok := i.authorizeRequest(w, r, r.URL.Query())
	if !ok {
		return
	}
	renderAuthorize(w, http.StatusOK, authorizeView{Only: i.LoginOnly, Req: &req})
}

// handleAuthorizeSubmit serves POST /oidc/authorize: it checks the request
// in the form body again, judges the password as the device page does, and
// redirects with a fresh code and the state. A refused password shows the
// form again and issues no code.
func (i *Issuer) handleAuthorizeSubmit(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		renderAuthorize(w, http.StatusBadRequest, authorizeView{Only: i.LoginOnly, Error: unreadableForm})
		return
	}
	req, ok := i.authorizeRequest(w, r, r.PostForm)
	if !ok {
		return
	}
	u, refusal := i.judgePassword(r, r.PostForm.Get("username"), r.PostForm.Get("password"))
	if refusal != "" {
		renderAuthorize(w, http.StatusOK, authorizeView{Only: i.LoginOnly, Error: refusal, Req: &req})
		return
	}
	authTime := time.Now()

	code := randomToken(32)
	i.mu.Lock()
	i.gcLocked()
	// The form parser hands out values that share memory with the whole
	// request body, so the code keeps copies of its own.
	i.codes[code] = &authCode{
		clientID: strings.Clone(req.ClientID), redirectURI: strings.Clone(req.RedirectURI),
		challenge: strings.Clone(req.Challenge), nonce: strings.Clone(req.Nonce),
		userID: u.ID, authTime: authTime, expiresAt: authTime.Add(codeTTL),
	}
	i.mu.Unlock()
	if i.OnLogin != nil {
		i.OnLogin(u.ID, u.Username)
	}
	redirectTo(w, r, req, url.Values{"code": {code}})
}

// authorizeRequest checks the authorization request in v and reports whether
// the caller may go on. While the client or the redirect is not trusted it
// answers with an error page, so the endpoint never sends a browser to an
// address it has not checked. After that it answers a bad request with an
// error redirect, as RFC 6749 section 4.1.2.1 says.
func (i *Issuer) authorizeRequest(w http.ResponseWriter, r *http.Request, v url.Values) (authzRequest, bool) {
	req := authzRequest{
		ClientID: v.Get("client_id"), RedirectURI: v.Get("redirect_uri"),
		State: v.Get("state"), Nonce: v.Get("nonce"), Challenge: v.Get("code_challenge"),
	}
	if len(v["client_id"]) != 1 || !authorizeClients[req.ClientID] {
		renderAuthorize(w, http.StatusBadRequest, authorizeView{Only: i.LoginOnly, Error: unknownClientPage})
		return req, false
	}
	// The console runs in a browser at this server, so its one redirect is
	// the server's own console address, and the programs' loopback lane is
	// closed to it.
	console := req.ClientID == "console"
	if len(v["redirect_uri"]) == 1 && len(req.RedirectURI) <= maxRedirectBytes {
		if console {
			req.redirect = i.consoleRedirect(req.RedirectURI)
		} else {
			req.redirect = loopbackRedirect(req.RedirectURI)
		}
	}
	if req.redirect == nil {
		msg := untrustedRedirect
		if console {
			msg = fmt.Sprintf(untrustedConsole, i.consoleAddress())
		}
		renderAuthorize(w, http.StatusBadRequest, authorizeView{Only: i.LoginOnly, Error: msg})
		return req, false
	}
	var code, desc string
	switch {
	case v.Get("response_type") == "":
		code, desc = "invalid_request", "response_type is missing. Straza signs in with response_type code."
	case v.Get("response_type") != "code":
		code, desc = "unsupported_response_type", "Straza signs in with response_type code only."
	case v.Get("code_challenge_method") != "S256":
		code, desc = "invalid_request", "PKCE with code_challenge_method S256 is required."
	case !isChallenge(req.Challenge):
		code, desc = "invalid_request", "code_challenge must be an S256 challenge of 43 base64url characters."
	case len(req.Nonce) > maxNonceBytes:
		code, desc = "invalid_request", "nonce must be at most 512 bytes long."
	default:
		return req, true
	}
	redirectTo(w, r, req, url.Values{"error": {code}, "error_description": {desc}})
	return req, false
}

// loopbackRedirect parses raw as a redirect to a program on this machine,
// as RFC 8252 section 7.3 describes: http on 127.0.0.1 or [::1], any port
// and path, with no user part and no fragment. It returns nil for anything
// else, localhost by name included.
func loopbackRedirect(raw string) *url.URL {
	if strings.Contains(raw, "#") {
		return nil
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "http" || u.User != nil || u.Opaque != "" {
		return nil
	}
	if host := strings.TrimSuffix(u.Host, ":"+u.Port()); host != "127.0.0.1" && host != "[::1]" {
		return nil
	}
	return u
}

// consoleAddress is the console's sign-in address under the public URL,
// the one redirect the client console may name.
func (i *Issuer) consoleAddress() string {
	return strings.TrimRight(i.baseURL, "/") + "/console/signin"
}

// consoleRedirect parses raw as the console's sign-in address: the scheme,
// host and port of the public URL with the path /console/signin, no query,
// no user part and no fragment. Hosts compare without case and a scheme's
// default port counts as written, as a browser's address drops both forms
// of a public URL. On a loopback public URL any loopback host
// with the same scheme and port is accepted too, because one machine reaches
// itself as localhost, 127.0.0.1 or [::1], and none of them leaves it. It
// returns nil for anything else.
func (i *Issuer) consoleRedirect(raw string) *url.URL {
	if strings.Contains(raw, "#") {
		return nil
	}
	u, err := url.Parse(raw)
	own, oerr := url.Parse(i.consoleAddress())
	if err != nil || oerr != nil || u.User != nil || u.Opaque != "" || u.RawQuery != "" || u.ForceQuery {
		return nil
	}
	if u.Scheme != own.Scheme || u.Path != own.Path || u.RawPath != "" || portOf(u) != portOf(own) {
		return nil
	}
	if strings.EqualFold(u.Hostname(), own.Hostname()) || (loopbackName(own.Hostname()) && loopbackName(u.Hostname())) {
		return u
	}
	return nil
}

// portOf is u's port, or its scheme's default port when none is written.
func portOf(u *url.URL) string {
	switch {
	case u.Port() != "":
		return u.Port()
	case u.Scheme == "https":
		return "443"
	case u.Scheme == "http":
		return "80"
	}
	return ""
}

// loopbackName reports whether host names this machine only: localhost,
// 127.0.0.1 or ::1, in the forms a URL carries them.
func loopbackName(host string) bool {
	return host == "localhost" || host == "127.0.0.1" || host == "::1"
}

// isChallenge reports whether s has the form of an S256 code challenge, 43
// base64url characters (RFC 7636 section 4.2).
func isChallenge(s string) bool {
	if len(s) != 43 {
		return false
	}
	_, err := base64.RawURLEncoding.DecodeString(s)
	return err == nil
}

// redirectTo sends the browser to the request's trusted redirect with params
// and the state, keeping any query the redirect already has.
func redirectTo(w http.ResponseWriter, r *http.Request, req authzRequest, params url.Values) {
	u := *req.redirect
	q := u.Query()
	for k, vs := range params {
		q[k] = vs
	}
	if req.State != "" {
		q.Set("state", req.State)
	}
	u.RawQuery = q.Encode()
	http.Redirect(w, r, u.String(), http.StatusSeeOther)
}

func renderAuthorize(w http.ResponseWriter, status int, v authorizeView) {
	pageHeaders(w)
	w.WriteHeader(status)
	_ = authorizeTmpl.Execute(w, v)
}

// handleCodeGrant redeems an authorization code (RFC 6749 section 4.1.3,
// with the RFC 7636 S256 verifier). The first attempt that names a code
// spends it, whatever the outcome. The ID token is for the client and
// carries the request's nonce and the moment the password was checked.
func (i *Issuer) handleCodeGrant(w http.ResponseWriter, r *http.Request) {
	f := r.PostForm
	code, clientID, redirectURI, verifier := f.Get("code"), f.Get("client_id"), f.Get("redirect_uri"), f.Get("code_verifier")
	if code == "" || clientID == "" || redirectURI == "" || verifier == "" {
		oauthError(w, "invalid_request", "code, redirect_uri, client_id and code_verifier are required")
		return
	}
	i.mu.Lock()
	c := i.codes[code]
	delete(i.codes, code)
	i.mu.Unlock()

	sum := sha256.Sum256([]byte(verifier))
	switch {
	case c == nil:
		oauthError(w, "invalid_grant", "the code is unknown or was already used. Start the sign-in again")
	case time.Now().After(c.expiresAt):
		oauthError(w, "invalid_grant", "the code expired. Start the sign-in again")
	case c.clientID != clientID || c.redirectURI != redirectURI:
		oauthError(w, "invalid_grant", "the code was issued to another client_id or redirect_uri. Start the sign-in again")
	case subtle.ConstantTimeCompare([]byte(base64.RawURLEncoding.EncodeToString(sum[:])), []byte(c.challenge)) != 1:
		oauthError(w, "invalid_grant", "code_verifier does not match the code_challenge. Start the sign-in again")
	default:
		u, err := i.users.GetByID(r.Context(), c.userID)
		if err != nil || u.Status != store.UserActive {
			oauthError(w, "invalid_grant", "the user is no longer active. Ask your Straza admin")
			return
		}
		i.issueIDToken(w, u, clientID, LoginFacts{Nonce: c.nonce, AuthTime: c.authTime})
	}
}
