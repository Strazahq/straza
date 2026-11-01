package authn

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/strazahq/straza/internal/store"
	"github.com/strazahq/straza/internal/store/storetest"
)

const testRedirect = "http://127.0.0.1:43123/callback"

// noFollow returns a redirect instead of following it, so a test reads the
// Location the endpoint chose.
var noFollow = &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error {
	return http.ErrUseLastResponse
}}

// pkce returns a fresh code verifier and its S256 challenge.
func pkce(t *testing.T) (verifier, challenge string) {
	t.Helper()
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	verifier = base64.RawURLEncoding.EncodeToString(b)
	sum := sha256.Sum256([]byte(verifier))
	return verifier, base64.RawURLEncoding.EncodeToString(sum[:])
}

// authzParams returns a valid authorization request from client straza, in
// the shape the browser lane sends it.
func authzParams(challenge string) url.Values {
	return url.Values{
		"response_type": {"code"}, "client_id": {"straza"}, "redirect_uri": {testRedirect},
		"scope": {"openid profile"}, "state": {"state-1"}, "nonce": {"jkt-part.random-part"},
		"code_challenge": {challenge}, "code_challenge_method": {"S256"},
		"prompt": {"login"}, "max_age": {"0"},
	}
}

// authzAnswer is what /oidc/authorize answered: the status, the redirect
// target when there was one, the page body and the headers.
type authzAnswer struct {
	status   int
	location *url.URL
	page     string
	header   http.Header
}

func readAnswer(t *testing.T, resp *http.Response) authzAnswer {
	t.Helper()
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	a := authzAnswer{status: resp.StatusCode, page: string(body), header: resp.Header}
	if loc := resp.Header.Get("Location"); loc != "" {
		if a.location, err = url.Parse(loc); err != nil {
			t.Fatalf("Location %q does not parse: %v", loc, err)
		}
	}
	return a
}

func (f *issuerFixture) authorizeGet(t *testing.T, params url.Values) authzAnswer {
	t.Helper()
	resp, err := noFollow.Get(f.srv.URL + "/oidc/authorize?" + params.Encode())
	if err != nil {
		t.Fatal(err)
	}
	return readAnswer(t, resp)
}

func (f *issuerFixture) authorizePost(t *testing.T, params url.Values, username, password string) authzAnswer {
	t.Helper()
	form := url.Values{"username": {username}, "password": {password}}
	for k, v := range params {
		form[k] = v
	}
	resp, err := noFollow.PostForm(f.srv.URL+"/oidc/authorize", form)
	if err != nil {
		t.Fatal(err)
	}
	return readAnswer(t, resp)
}

// signIn sends a valid authorization request through the password form as
// kim and returns the code from the redirect.
func (f *issuerFixture) signIn(t *testing.T, params url.Values) string {
	t.Helper()
	a := f.authorizePost(t, params, "kim", "hunter2!")
	if a.status != http.StatusSeeOther || a.location == nil {
		t.Fatalf("sign-in = %d, Location %v, want 303 with a code: %s", a.status, a.location, a.page)
	}
	code := a.location.Query().Get("code")
	if code == "" {
		t.Fatalf("redirect %s carries no code", a.location)
	}
	return code
}

func codeForm(code, verifier string) url.Values {
	return url.Values{
		"grant_type": {"authorization_code"}, "code": {code}, "redirect_uri": {testRedirect},
		"client_id": {"straza"}, "code_verifier": {verifier},
	}
}

func (f *issuerFixture) codeCount() int {
	f.issuer.mu.Lock()
	defer f.issuer.mu.Unlock()
	return len(f.issuer.codes)
}

// TestAuthorizeCodeFlowEndToEnd walks the browser lane against the built-in
// issuer: the form, the redirect with code and state, the redemption with
// the verifier, an ID token with nonce and auth_time for the client, and a
// replay refused.
func TestAuthorizeCodeFlowEndToEnd(t *testing.T) {
	f := newIssuerFixture(t)
	verifier, challenge := pkce(t)
	params := authzParams(challenge)
	params.Set("state", "st&ate=1 two")

	page := f.authorizeGet(t, params)
	if page.status != http.StatusOK || page.location != nil {
		t.Fatalf("GET = %d, Location %v: %s", page.status, page.location, page.page)
	}
	for _, want := range []string{`action="/oidc/authorize"`, `name="password"`, `name="code_challenge"`} {
		if !strings.Contains(page.page, want) {
			t.Errorf("the page lacks %s: %s", want, page.page)
		}
	}
	if strings.Contains(page.page, `name="user_code"`) {
		t.Errorf("the authorize page asks for a user code: %s", page.page)
	}
	if page.header.Get("X-Frame-Options") != "DENY" || !strings.Contains(page.header.Get("Content-Security-Policy"), "frame-ancestors 'none'") {
		t.Errorf("the page may be framed: X-Frame-Options %q, CSP %q", page.header.Get("X-Frame-Options"), page.header.Get("Content-Security-Policy"))
	}

	before := time.Now()
	answer := f.authorizePost(t, params, "kim", "hunter2!")
	after := time.Now()
	if answer.status != http.StatusSeeOther || answer.location == nil {
		t.Fatalf("POST = %d, Location %v: %s", answer.status, answer.location, answer.page)
	}
	back := *answer.location
	back.RawQuery = ""
	if back.String() != testRedirect {
		t.Errorf("redirect = %s, want %s", back.String(), testRedirect)
	}
	q := answer.location.Query()
	code := q.Get("code")
	if len(code) < 43 {
		t.Errorf("code %q is shorter than 32 random bytes", code)
	}
	if q.Get("state") != "st&ate=1 two" {
		t.Errorf("state = %q, want it echoed exactly", q.Get("state"))
	}
	f.issuer.mu.Lock()
	stored := f.issuer.codes[code]
	f.issuer.mu.Unlock()
	if stored == nil {
		t.Fatal("the code is not in the store")
	}
	if ttl := stored.expiresAt.Sub(before); ttl < 59*time.Second || ttl > 61*time.Second+after.Sub(before) {
		t.Errorf("the code lives %s, want 60 seconds", ttl)
	}

	status, body := f.postForm(t, "/oidc/token", codeForm(code, verifier))
	if status != http.StatusOK || body["id_token"] == nil {
		t.Fatalf("redemption = %d %v", status, body)
	}
	if body["access_token"] != body["id_token"] || body["token_type"] != "Bearer" || body["expires_in"] == nil {
		t.Errorf("the answer is not the device grant's shape: %v", body)
	}
	claims, err := f.tokens.VerifyIDToken(body["id_token"].(string), "straza")
	if err != nil {
		t.Fatalf("VerifyIDToken: %v", err)
	}
	if claims.Subject != f.user.ID || claims.Username != "kim" || claims.Nonce != "jkt-part.random-part" {
		t.Errorf("claims = %+v, want kim with the request's nonce", claims)
	}
	if claims.AuthTime.Before(before.Truncate(time.Second)) || claims.AuthTime.After(after) {
		t.Errorf("auth_time = %s, want the password check between %s and %s", claims.AuthTime, before, after)
	}

	if status, body = f.postForm(t, "/oidc/token", codeForm(code, verifier)); status != http.StatusBadRequest || body["error"] != "invalid_grant" {
		t.Errorf("replay = %d %v, want 400 invalid_grant", status, body)
	}
}

// TestAuthorizeRefusesUntrustedClientOrRedirect pins that /oidc/authorize is
// no open redirect: while the client or the redirect is not trusted, GET and
// POST answer with a page and never a Location, and a POST judges no
// password and stores no code. For straza and strazactl only http on
// 127.0.0.1 or [::1], any port and path, no fragment and no user part, is a
// trusted redirect. TestAuthorizeConsoleRedirect pins the console's.
func TestAuthorizeRefusesUntrustedClientOrRedirect(t *testing.T) {
	cases := []struct {
		name     string
		mutate   func(url.Values)
		trusted  bool
		sentence string
	}{
		{name: "loopback v4 with a port", mutate: func(url.Values) {}, trusted: true},
		{name: "loopback v6 with a port", mutate: func(v url.Values) { v.Set("redirect_uri", "http://[::1]:43123/callback") }, trusted: true},
		{name: "loopback without a port", mutate: func(v url.Values) { v.Set("redirect_uri", "http://127.0.0.1/callback") }, trusted: true},
		{name: "loopback with another path", mutate: func(v url.Values) { v.Set("redirect_uri", "http://127.0.0.1:5/any/path") }, trusted: true},
		{name: "client strazactl", mutate: func(v url.Values) { v.Set("client_id", "strazactl") }, trusted: true},
		{name: "client console with a program's address", mutate: func(v url.Values) { v.Set("client_id", "console") }, sentence: "somewhere other than this server"},
		{name: "a redirect of 2,048 bytes", mutate: func(v url.Values) { v.Set("redirect_uri", testRedirect+strings.Repeat("a", 2048-len(testRedirect))) }, trusted: true},
		{name: "a nonce of 512 bytes", mutate: func(v url.Values) { v.Set("nonce", strings.Repeat("n", 512)) }, trusted: true},
		{name: "a redirect over 2,048 bytes", mutate: func(v url.Values) { v.Set("redirect_uri", testRedirect+strings.Repeat("a", 2049-len(testRedirect))) }, sentence: "would send the answer"},
		{name: "https", mutate: func(v url.Values) { v.Set("redirect_uri", "https://127.0.0.1:43123/callback") }, sentence: "would send the answer"},
		{name: "localhost by name", mutate: func(v url.Values) { v.Set("redirect_uri", "http://localhost:43123/callback") }, sentence: "would send the answer"},
		{name: "another host", mutate: func(v url.Values) { v.Set("redirect_uri", "http://evil.example/callback") }, sentence: "would send the answer"},
		{name: "a host that starts like loopback", mutate: func(v url.Values) { v.Set("redirect_uri", "http://127.0.0.1.evil.example/callback") }, sentence: "would send the answer"},
		{name: "loopback as the user part", mutate: func(v url.Values) { v.Set("redirect_uri", "http://127.0.0.1@evil.example/callback") }, sentence: "would send the answer"},
		{name: "a user part before loopback", mutate: func(v url.Values) { v.Set("redirect_uri", "http://evil.example@127.0.0.1:43123/callback") }, sentence: "would send the answer"},
		{name: "a backslash trick", mutate: func(v url.Values) { v.Set("redirect_uri", `http://127.0.0.1:43123\@evil.example/`) }, sentence: "would send the answer"},
		{name: "a short loopback form", mutate: func(v url.Values) { v.Set("redirect_uri", "http://127.1/callback") }, sentence: "would send the answer"},
		{name: "loopback v4 in brackets", mutate: func(v url.Values) { v.Set("redirect_uri", "http://[127.0.0.1]:43123/callback") }, sentence: "would send the answer"},
		{name: "a fragment", mutate: func(v url.Values) { v.Set("redirect_uri", testRedirect+"#frag") }, sentence: "would send the answer"},
		{name: "an empty fragment", mutate: func(v url.Values) { v.Set("redirect_uri", testRedirect+"#") }, sentence: "would send the answer"},
		{name: "a relative path", mutate: func(v url.Values) { v.Set("redirect_uri", "/callback") }, sentence: "would send the answer"},
		{name: "no redirect", mutate: func(v url.Values) { v.Del("redirect_uri") }, sentence: "would send the answer"},
		{name: "two redirects", mutate: func(v url.Values) { v.Add("redirect_uri", "http://evil.example/") }, sentence: "would send the answer"},
		{name: "a hostile redirect", mutate: func(v url.Values) { v.Set("redirect_uri", `http://evil.example/"><script>alert(1)</script>`) }, sentence: "would send the answer"},
		{name: "no client", mutate: func(v url.Values) { v.Del("client_id") }, sentence: "does not name a Straza program"},
		{name: "an unknown client", mutate: func(v url.Values) { v.Set("client_id", "<script>alert(2)</script>") }, sentence: "does not name a Straza program"},
		{name: "two clients", mutate: func(v url.Values) { v.Add("client_id", "strazactl") }, sentence: "does not name a Straza program"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newIssuerFixture(t)
			var judged int
			f.issuer.OnLoginFailed = func(string, *http.Request) { judged++ }
			f.issuer.OnLogin = func(string, string) { judged++ }
			_, challenge := pkce(t)
			params := authzParams(challenge)
			tc.mutate(params)

			get := f.authorizeGet(t, params)
			post := f.authorizePost(t, params, "kim", "hunter2!")
			if tc.trusted {
				if get.status != http.StatusOK || !strings.Contains(get.page, `name="password"`) {
					t.Fatalf("GET = %d, want the password form: %s", get.status, get.page)
				}
				if post.status != http.StatusSeeOther || post.location == nil || post.location.Query().Get("code") == "" {
					t.Fatalf("POST = %d, Location %v, want a redirect with a code", post.status, post.location)
				}
				return
			}
			for method, a := range map[string]authzAnswer{"GET": get, "POST": post} {
				if a.status != http.StatusBadRequest || a.location != nil {
					t.Errorf("%s = %d, Location %v, want 400 and no redirect", method, a.status, a.location)
				}
				if !strings.Contains(a.page, tc.sentence) || strings.Contains(a.page, `name="password"`) {
					t.Errorf("%s page lacks %q or still shows the form: %s", method, tc.sentence, a.page)
				}
				if strings.Contains(a.page, "<script>") {
					t.Errorf("%s page carries raw markup from the request: %s", method, a.page)
				}
			}
			if judged != 0 || f.codeCount() != 0 {
				t.Errorf("an untrusted request judged %d passwords and stored %d codes, want none", judged, f.codeCount())
			}
		})
	}
}

// TestAuthorizeConsoleRedirect pins the console's one redirect: the issuer's
// own public URL with the path /console/signin, nothing more, and on a
// loopback public URL any loopback host with the same scheme and port. A
// trusted request shows the form with the console's sentence, and an
// untrusted one a page that names the address and server.publicUrl.
func TestAuthorizeConsoleRedirect(t *testing.T) {
	cases := []struct {
		name     string
		base     string // the issuer's public URL; "" keeps the fixture's loopback one
		redirect func(base string) string
		trusted  bool
	}{
		{name: "the own address", redirect: func(b string) string { return b + "/console/signin" }, trusted: true},
		{name: "localhost for a loopback public URL", redirect: func(b string) string { return strings.Replace(b, "127.0.0.1", "localhost", 1) + "/console/signin" }, trusted: true},
		{name: "[::1] for a loopback public URL", redirect: func(b string) string { return strings.Replace(b, "127.0.0.1", "[::1]", 1) + "/console/signin" }, trusted: true},
		{name: "the own address of a named server", base: "https://straza.example.com", redirect: func(b string) string { return b + "/console/signin" }, trusted: true},
		{name: "a public URL with a trailing slash", base: "https://straza.example.com/", redirect: func(string) string { return "https://straza.example.com/console/signin" }, trusted: true},
		{name: "a public URL with the https default port", base: "https://straza.example.com:443", redirect: func(string) string { return "https://straza.example.com/console/signin" }, trusted: true},
		{name: "a public URL with the http default port", base: "http://straza.example.com:80", redirect: func(string) string { return "http://straza.example.com/console/signin" }, trusted: true},
		{name: "a public URL with capitals in the host", base: "https://Straza.Example.com", redirect: func(string) string { return "https://straza.example.com/console/signin" }, trusted: true},
		{name: "the https default port written out", base: "https://straza.example.com", redirect: func(string) string { return "https://straza.example.com:443/console/signin" }, trusted: true},
		{name: "localhost for a named server", base: "https://straza.example.com", redirect: func(string) string { return "https://localhost/console/signin" }},
		{name: "http for an https server", base: "https://straza.example.com", redirect: func(string) string { return "http://straza.example.com/console/signin" }},
		{name: "another port", base: "https://straza.example.com", redirect: func(string) string { return "https://straza.example.com:8443/console/signin" }},
		{name: "a host that starts like the server", base: "https://straza.example.com", redirect: func(string) string { return "https://straza.example.com.evil.example/console/signin" }},
		{name: "another scheme on loopback", redirect: func(b string) string { return strings.Replace(b, "http://", "https://", 1) + "/console/signin" }},
		{name: "another port on loopback", redirect: func(string) string { return "http://127.0.0.1:1/console/signin" }},
		{name: "the console root", redirect: func(b string) string { return b + "/console/" }},
		{name: "a trailing slash", redirect: func(b string) string { return b + "/console/signin/" }},
		{name: "a deeper path", redirect: func(b string) string { return b + "/console/signin/x" }},
		{name: "a dot segment", redirect: func(b string) string { return b + "/console/x/../signin" }},
		{name: "a query", redirect: func(b string) string { return b + "/console/signin?next=x" }},
		{name: "a fragment", redirect: func(b string) string { return b + "/console/signin#x" }},
		{name: "a user part", redirect: func(b string) string { return strings.Replace(b, "http://", "http://kim@", 1) + "/console/signin" }},
		{name: "a program's address", redirect: func(string) string { return testRedirect }},
		{name: "another site", redirect: func(string) string { return "https://evil.example/console/signin" }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newIssuerFixture(t)
			base := f.srv.URL
			if tc.base != "" {
				base = tc.base
				f.issuer.baseURL = tc.base
			}
			_, challenge := pkce(t)
			params := authzParams(challenge)
			params.Set("client_id", "console")
			params.Set("redirect_uri", tc.redirect(base))

			get := f.authorizeGet(t, params)
			post := f.authorizePost(t, params, "kim", "hunter2!")
			if tc.trusted {
				if get.status != http.StatusOK || !strings.Contains(get.page, "takes you back to Straza") || strings.Contains(get.page, "program on this computer") {
					t.Fatalf("GET = %d, want the form with the console's sentence: %s", get.status, get.page)
				}
				if post.status != http.StatusSeeOther || post.location == nil || post.location.Query().Get("code") == "" {
					t.Fatalf("POST = %d, Location %v, want a redirect with a code", post.status, post.location)
				}
				return
			}
			for method, a := range map[string]authzAnswer{"GET": get, "POST": post} {
				if a.status != http.StatusBadRequest || a.location != nil {
					t.Errorf("%s = %d, Location %v, want 400 and no redirect", method, a.status, a.location)
				}
				if !strings.Contains(a.page, "console at ") || !strings.Contains(a.page, "server.publicUrl") || strings.Contains(a.page, `name="password"`) {
					t.Errorf("%s page lacks the console sentence or still shows the form: %s", method, a.page)
				}
			}
			if f.codeCount() != 0 {
				t.Errorf("an untrusted request stored %d codes, want none", f.codeCount())
			}
		})
	}
}

// TestAuthorizeConsoleEndToEnd redeems a console sign-in: the code goes back
// to the console's address, and the ID token is for the client console.
func TestAuthorizeConsoleEndToEnd(t *testing.T) {
	f := newIssuerFixture(t)
	verifier, challenge := pkce(t)
	own := f.srv.URL + "/console/signin"
	params := authzParams(challenge)
	params.Set("client_id", "console")
	params.Set("redirect_uri", own)
	params.Del("prompt")
	params.Del("max_age")

	code := f.signIn(t, params)
	form := codeForm(code, verifier)
	form.Set("client_id", "console")
	form.Set("redirect_uri", own)
	status, body := f.postForm(t, "/oidc/token", form)
	if status != http.StatusOK || body["id_token"] == nil {
		t.Fatalf("redemption = %d %v", status, body)
	}
	if _, err := f.tokens.VerifyIDToken(body["id_token"].(string), "console"); err != nil {
		t.Errorf("the ID token is not for the console: %v", err)
	}
}

// TestAuthorizeProgramSentence keeps the program's sentence on the form for
// straza and strazactl, whose answer goes to a program on this machine.
func TestAuthorizeProgramSentence(t *testing.T) {
	f := newIssuerFixture(t)
	_, challenge := pkce(t)
	page := f.authorizeGet(t, authzParams(challenge))
	if !strings.Contains(page.page, "program on this computer") || strings.Contains(page.page, "takes you back to Straza") {
		t.Errorf("the straza form lacks the program's sentence: %s", page.page)
	}
}

// TestAuthorizeErrorsRedirectOnceTrusted pins RFC 6749 section 4.1.2.1: with
// a trusted client and redirect, a bad request goes back to the redirect
// with error, error_description and the state, never with a code. PKCE is
// S256 only.
func TestAuthorizeErrorsRedirectOnceTrusted(t *testing.T) {
	cases := []struct {
		name    string
		mutate  func(url.Values)
		wantErr string
	}{
		{name: "response type token", mutate: func(v url.Values) { v.Set("response_type", "token") }, wantErr: "unsupported_response_type"},
		{name: "no response type", mutate: func(v url.Values) { v.Del("response_type") }, wantErr: "invalid_request"},
		{name: "the plain method", mutate: func(v url.Values) { v.Set("code_challenge_method", "plain") }, wantErr: "invalid_request"},
		{name: "no method", mutate: func(v url.Values) { v.Del("code_challenge_method") }, wantErr: "invalid_request"},
		{name: "no challenge", mutate: func(v url.Values) { v.Del("code_challenge") }, wantErr: "invalid_request"},
		{name: "a short challenge", mutate: func(v url.Values) { v.Set("code_challenge", "abc") }, wantErr: "invalid_request"},
		{name: "a challenge outside base64url", mutate: func(v url.Values) { v.Set("code_challenge", strings.Repeat("+", 42)+"=") }, wantErr: "invalid_request"},
		{name: "a nonce over 512 bytes", mutate: func(v url.Values) { v.Set("nonce", strings.Repeat("n", 513)) }, wantErr: "invalid_request"},
		{name: "a huge nonce", mutate: func(v url.Values) { v.Set("nonce", strings.Repeat("n", 900_000)) }, wantErr: "invalid_request"},
		{name: "a hostile state", mutate: func(v url.Values) {
			v.Set("state", `"><script>alert(1)</script>&x=`)
			v.Set("code_challenge_method", "plain")
		}, wantErr: "invalid_request"},
		{name: "no state", mutate: func(v url.Values) {
			v.Del("state")
			v.Set("response_type", "token")
		}, wantErr: "unsupported_response_type"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newIssuerFixture(t)
			var judged int
			f.issuer.OnLoginFailed = func(string, *http.Request) { judged++ }
			f.issuer.OnLogin = func(string, string) { judged++ }
			_, challenge := pkce(t)
			params := authzParams(challenge)
			tc.mutate(params)
			for method, a := range map[string]authzAnswer{
				"GET":  f.authorizeGet(t, params),
				"POST": f.authorizePost(t, params, "kim", "hunter2!"),
			} {
				if a.status != http.StatusSeeOther || a.location == nil {
					t.Fatalf("%s = %d, Location %v, want 303 to the redirect: %s", method, a.status, a.location, a.page)
				}
				q := a.location.Query()
				if q.Get("error") != tc.wantErr || q.Get("error_description") == "" || q.Has("code") {
					t.Errorf("%s redirect query = %v, want error %s with a description and no code", method, q, tc.wantErr)
				}
				if params.Has("state") != q.Has("state") || q.Get("state") != params.Get("state") {
					t.Errorf("%s state = %q (present %v), want %q", method, q.Get("state"), q.Has("state"), params.Get("state"))
				}
			}
			if judged != 0 || f.codeCount() != 0 {
				t.Errorf("a bad request judged %d passwords and stored %d codes, want none", judged, f.codeCount())
			}
		})
	}
}

// TestAuthorizePasswordSubmit pins the password half of /oidc/authorize: it
// judges the password the way /oidc/device does, honors LoginOnly, fires
// OnLoginFailed once with the post-zeroing username on each refusal, gives
// no code on a refusal and shows the form again with the request kept.
func TestAuthorizePasswordSubmit(t *testing.T) {
	cases := []struct {
		name      string
		only      string
		user      string
		password  string
		wantCode  bool
		wantError string
		wantFired []string // OnLoginFailed usernames, in order
	}{
		{name: "the right password", user: "kim", password: "hunter2!", wantCode: true},
		{name: "a wrong password", user: "kim", password: "nope", wantError: "Invalid username or password.", wantFired: []string{"kim"}},
		{name: "an unknown user", user: "ghost", password: "hunter2!", wantError: "Invalid username or password.", wantFired: []string{""}},
		{name: "the emergency account", only: "kim", user: "kim", password: "hunter2!", wantCode: true},
		{name: "the emergency account with a wrong password", only: "kim", user: "kim", password: "nope", wantError: "Invalid username or password.", wantFired: []string{"kim"}},
		{name: "another user on the emergency page", only: "kim", user: "bob", password: "hunter2!",
			wantError: "This page signs in the account kim only. Everyone else signs in at your identity provider.", wantFired: []string{""}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newIssuerFixture(t)
			f.issuer.LoginOnly = tc.only
			hash, err := storetest.PasswordHash("hunter2!")
			if err != nil {
				t.Fatal(err)
			}
			if _, err := f.users.Create(context.Background(), store.User{Username: "bob", Email: "bob@x.io", PasswordHash: hash}); err != nil {
				t.Fatal(err)
			}
			var fired []string
			var logins []string
			f.issuer.OnLoginFailed = func(username string, _ *http.Request) { fired = append(fired, username) }
			f.issuer.OnLogin = func(_, username string) { logins = append(logins, username) }
			_, challenge := pkce(t)
			params := authzParams(challenge)

			if tc.only != "" {
				page := f.authorizeGet(t, params).page
				if !strings.Contains(page, "Emergency sign-in to Straza") || !strings.Contains(page, "signs in the emergency admin kim and nobody else") {
					t.Fatalf("the emergency page does not name its account: %s", page)
				}
			}
			a := f.authorizePost(t, params, tc.user, tc.password)
			if tc.wantCode {
				if a.status != http.StatusSeeOther || a.location == nil || a.location.Query().Get("code") == "" {
					t.Fatalf("POST = %d, Location %v, want a code", a.status, a.location)
				}
				if len(logins) != 1 || logins[0] != tc.user {
					t.Errorf("OnLogin saw %v, want %s once", logins, tc.user)
				}
			} else {
				if a.location != nil || f.codeCount() != 0 {
					t.Fatalf("a refused password redirected to %v with %d codes stored", a.location, f.codeCount())
				}
				if !strings.Contains(a.page, tc.wantError) {
					t.Errorf("the page lacks %q: %s", tc.wantError, a.page)
				}
				if !strings.Contains(a.page, `name="state" value="state-1"`) || !strings.Contains(a.page, `name="password"`) {
					t.Errorf("the form does not come back with the request kept: %s", a.page)
				}
				if len(logins) != 0 {
					t.Errorf("OnLogin fired on a refusal: %v", logins)
				}
			}
			if strings.Join(fired, ",") != strings.Join(tc.wantFired, ",") || len(fired) != len(tc.wantFired) {
				t.Errorf("OnLoginFailed saw %q, want %q", fired, tc.wantFired)
			}
		})
	}
}

// TestAuthorizePageEscapesHostileValues pins that the state and the nonce,
// which the page carries back in hidden fields, reach the page escaped, on
// the first page and on the page after a wrong password.
func TestAuthorizePageEscapesHostileValues(t *testing.T) {
	f := newIssuerFixture(t)
	_, challenge := pkce(t)
	params := authzParams(challenge)
	params.Set("state", `"><script>alert(1)</script>`)
	params.Set("nonce", `<img src=x onerror=alert(2)>`)
	for name, a := range map[string]authzAnswer{
		"first page":           f.authorizeGet(t, params),
		"after a bad password": f.authorizePost(t, params, "kim", "nope"),
	} {
		if strings.Contains(a.page, "<script>") || strings.Contains(a.page, "<img") {
			t.Errorf("%s carries raw markup: %s", name, a.page)
		}
		if !strings.Contains(a.page, "&lt;script&gt;") || !strings.Contains(a.page, "&lt;img") {
			t.Errorf("%s lost the escaped values: %s", name, a.page)
		}
	}
}

// TestAuthorizationCodeGrantRefusals pins the authorization_code grant: the
// code, the client, the redirect and the S256 verifier must all match, an
// expired code or a user no longer active is invalid_grant, and any judged
// attempt spends the code. A request with a parameter missing judges nothing
// and leaves the code alone.
func TestAuthorizationCodeGrantRefusals(t *testing.T) {
	cases := []struct {
		name    string
		mutate  func(form url.Values, challenge string)
		prepare func(t *testing.T, f *issuerFixture, code string)
		wantErr string
		spent   bool
	}{
		{name: "another verifier", mutate: func(v url.Values, _ string) {
			v.Set("code_verifier", strings.Repeat("a", 43))
		}, wantErr: "invalid_grant", spent: true},
		{name: "the challenge as the verifier", mutate: func(v url.Values, challenge string) {
			v.Set("code_verifier", challenge)
		}, wantErr: "invalid_grant", spent: true},
		{name: "a verifier too short", mutate: func(v url.Values, _ string) {
			v.Set("code_verifier", "short")
		}, wantErr: "invalid_grant", spent: true},
		{name: "another client", mutate: func(v url.Values, _ string) {
			v.Set("client_id", "strazactl")
		}, wantErr: "invalid_grant", spent: true},
		{name: "another redirect", mutate: func(v url.Values, _ string) {
			v.Set("redirect_uri", "http://127.0.0.1:43124/callback")
		}, wantErr: "invalid_grant", spent: true},
		{name: "an unknown code", mutate: func(v url.Values, _ string) {
			v.Set("code", randomToken(32))
		}, wantErr: "invalid_grant"},
		{name: "an expired code", prepare: func(_ *testing.T, f *issuerFixture, code string) {
			f.issuer.mu.Lock()
			f.issuer.codes[code].expiresAt = time.Now().Add(-time.Second)
			f.issuer.mu.Unlock()
		}, wantErr: "invalid_grant", spent: true},
		{name: "a user disabled after the sign-in", prepare: func(t *testing.T, f *issuerFixture, _ string) {
			u := f.user
			u.Status = store.UserDisabled
			if _, err := f.users.Update(context.Background(), u); err != nil {
				t.Fatal(err)
			}
		}, wantErr: "invalid_grant", spent: true},
		{name: "no verifier", mutate: func(v url.Values, _ string) { v.Del("code_verifier") }, wantErr: "invalid_request"},
		{name: "no code", mutate: func(v url.Values, _ string) { v.Del("code") }, wantErr: "invalid_request"},
		{name: "no redirect", mutate: func(v url.Values, _ string) { v.Del("redirect_uri") }, wantErr: "invalid_request"},
		{name: "no client", mutate: func(v url.Values, _ string) { v.Del("client_id") }, wantErr: "invalid_request"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newIssuerFixture(t)
			verifier, challenge := pkce(t)
			code := f.signIn(t, authzParams(challenge))
			if tc.prepare != nil {
				tc.prepare(t, f, code)
			}
			form := codeForm(code, verifier)
			if tc.mutate != nil {
				tc.mutate(form, challenge)
			}
			status, body := f.postForm(t, "/oidc/token", form)
			if status != http.StatusBadRequest || body["error"] != tc.wantErr || body["id_token"] != nil {
				t.Fatalf("redemption = %d %v, want 400 %s", status, body, tc.wantErr)
			}
			if desc, _ := body["error_description"].(string); desc == "" {
				t.Errorf("the refusal has no error_description: %v", body)
			}
			if tc.prepare != nil {
				return
			}
			status, body = f.postForm(t, "/oidc/token", codeForm(code, verifier))
			if spent := status != http.StatusOK; spent != tc.spent {
				t.Errorf("the right redemption afterwards = %d %v, want spent %v", status, body, tc.spent)
			}
		})
	}
}

// TestAuthorizationCodesCollected pins that expired codes are dropped with
// the expired device grants, and a live code stays.
func TestAuthorizationCodesCollected(t *testing.T) {
	f := newIssuerFixture(t)
	_, challenge := pkce(t)
	stale := f.signIn(t, authzParams(challenge))
	live := f.signIn(t, authzParams(challenge))
	f.issuer.mu.Lock()
	f.issuer.codes[stale].expiresAt = time.Now().Add(-time.Second)
	f.issuer.mu.Unlock()

	f.issuer.mu.Lock()
	f.issuer.lastSweep = time.Time{} // the next start sweeps, as it would a second later
	f.issuer.mu.Unlock()

	_, _ = f.postForm(t, "/oidc/device_authorization", url.Values{"client_id": {"straza"}})
	f.issuer.mu.Lock()
	_, staleKept := f.issuer.codes[stale]
	_, liveKept := f.issuer.codes[live]
	f.issuer.mu.Unlock()
	if staleKept || !liveKept {
		t.Errorf("after collection the expired code is kept %v and the live one %v, want false and true", staleKept, liveKept)
	}
}

// TestAuthorizationCodeDoesNotPinTheBody pins that an authorization code
// keeps copies of the client id, the redirect, the challenge and the nonce.
// The form parser hands out values that share memory with the whole body,
// so a code that kept them would hold every byte of padding sent beside
// them until the code is spent or expires.
func TestAuthorizationCodeDoesNotPinTheBody(t *testing.T) {
	const (
		signIns = 200
		padding = 256 << 10
		budget  = 5 << 20 // signIns*padding is 50 MiB when the body stays pinned
	)
	f := newIssuerFixture(t)
	_, challenge := pkce(t)
	params := authzParams(challenge)
	params.Set("pad", strings.Repeat("x", padding))

	before := liveHeap()
	for range signIns {
		_ = f.signIn(t, params)
	}
	growth := liveHeap() - before
	t.Logf("%d codes grew the live heap by %d bytes", signIns, growth)
	if n := f.codeCount(); n != signIns {
		t.Fatalf("the issuer holds %d codes, want %d, so the heap reading measures nothing", n, signIns)
	}
	if growth >= budget {
		t.Errorf("%d codes grew the live heap by %d bytes, want under %d: a code pins its request body", signIns, growth, budget)
	}
}
