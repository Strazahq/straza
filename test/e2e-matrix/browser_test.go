package e2ematrix

import (
	"html/template"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os/exec"
	"strings"
	"testing"
)

// fakeIssuer plays the built-in issuer's authorization page: GET renders a
// form that carries the request's parameters as hidden fields, and POST
// checks the password and redirects to where redirect points.
type fakeIssuer struct {
	password string
	noForm   bool
	redirect func(redirectURI, state string) string
}

var fakeAuthorizeTmpl = template.Must(template.New("authorize").Parse(`<!doctype html>
<title>Sign in to Straza</title>
{{if .Error}}<p class="err">{{.Error}}</p>{{end}}
<form method="post" action="/oidc/authorize">
{{range $k, $v := .Fields}}<input type="hidden" name="{{$k}}" value="{{$v}}">
{{end}}<label>Username</label><input name="username" autocomplete="username">
<label>Password</label><input name="password" type="password" autocomplete="current-password">
<button type="submit">Sign in</button>
</form>`))

func (f *fakeIssuer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "malformed form", http.StatusBadRequest)
		return
	}
	fields := map[string]string{}
	for k := range r.Form {
		if k != "username" && k != "password" {
			fields[k] = r.Form.Get(k)
		}
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if r.Method == http.MethodGet {
		if f.noForm {
			_, _ = w.Write([]byte("<!doctype html><p>That sign-in request is not valid.</p>"))
			return
		}
		_ = fakeAuthorizeTmpl.Execute(w, map[string]any{"Fields": fields})
		return
	}
	if r.PostForm.Get("username") != "erin" || r.PostForm.Get("password") != f.password {
		_ = fakeAuthorizeTmpl.Execute(w, map[string]any{"Fields": fields, "Error": "Invalid username or password."})
		return
	}
	redirectURI, state := r.PostForm.Get("redirect_uri"), r.PostForm.Get("state")
	target := redirectURI + "?" + url.Values{"code": {"c1"}, "state": {state}}.Encode()
	if f.redirect != nil {
		target = f.redirect(redirectURI, state)
	}
	http.Redirect(w, r, target, http.StatusFound)
}

// TestBrowserSignIn pins the runner's part of the browser lane: it opens the
// link at the issuer, posts the sign-in form it finds there with the
// person's password, and follows the redirect to the loopback callback the
// link named, with the code and the link's own state, and to nothing else.
func TestBrowserSignIn(t *testing.T) {
	cases := []struct {
		name         string
		issuer       fakeIssuer
		callbackCode int
		want         string
	}{
		{"signs in and lands on the callback", fakeIssuer{password: "Walk-2026!"}, http.StatusOK, ""},
		{"a wrong password shows the form again", fakeIssuer{password: "another"}, http.StatusOK, "instead of a redirect"},
		{"a page without a form", fakeIssuer{password: "Walk-2026!", noForm: true}, http.StatusOK, "carries no form"},
		{"a redirect off the machine", fakeIssuer{password: "Walk-2026!", redirect: func(_, state string) string {
			return "http://203.0.113.9:8080/callback?" + url.Values{"code": {"c1"}, "state": {state}}.Encode()
		}}, http.StatusOK, "redirected to http://203.0.113.9:8080/callback"},
		{"a redirect with another state", fakeIssuer{password: "Walk-2026!", redirect: func(uri, _ string) string {
			return uri + "?code=c1&state=other"
		}}, http.StatusOK, "the state of the link"},
		{"a redirect without a code", fakeIssuer{password: "Walk-2026!", redirect: func(uri, state string) string {
			return uri + "?" + url.Values{"state": {state}}.Encode()
		}}, http.StatusOK, "with a code"},
		{"the callback refuses", fakeIssuer{password: "Walk-2026!"}, http.StatusBadRequest, "callback answered status 400"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var got url.Values
			callback := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/callback" {
					http.NotFound(w, r)
					return
				}
				got = r.URL.Query()
				w.WriteHeader(tc.callbackCode)
				_, _ = w.Write([]byte("You are signed in to Straza on this computer. You can close this tab."))
			}))
			defer callback.Close()
			issuer := httptest.NewServer(&tc.issuer)
			defer issuer.Close()
			// The state carries characters the page escapes, so the test
			// proves the form's fields are posted back as they were sent.
			link := issuer.URL + "/oidc/authorize?" + url.Values{
				"response_type": {"code"}, "client_id": {"straza"}, "redirect_uri": {callback.URL + "/callback"},
				"scope": {"openid profile"}, "state": {"st+1&2"}, "nonce": {"jkt.r"}, "code_challenge": {"cc"},
				"code_challenge_method": {"S256"}, "prompt": {"login"}, "max_age": {"600"},
			}.Encode()
			err := browserSignIn(t.Context(), link, "erin", "Walk-2026!")
			if tc.want == "" {
				if err != nil {
					t.Fatalf("sign-in failed: %v", err)
				}
				if got.Get("code") != "c1" || got.Get("state") != "st+1&2" {
					t.Fatalf("the callback got %v, want code c1 and the link's state", got)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("want an error mentioning %q, got %v", tc.want, err)
			}
			if strings.Contains(err.Error(), "Walk-2026!") {
				t.Fatalf("the error carries the password: %v", err)
			}
		})
	}
}

// TestBrowserHelper pins the BROWSER helper: it hands the runner the one
// argument it was started with, and a start with more than the link alone
// is an error the runner reports.
func TestBrowserHelper(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want string
	}{
		{"the link alone", []string{"http://127.0.0.1:1/oidc/authorize?state=a&nonce=b"}, ""},
		{"two arguments", []string{"--new-window", "http://127.0.0.1:1/oidc/authorize"}, "2 arguments"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			helper, linkFile, err := writeBrowserHelper(dir)
			if err != nil {
				t.Fatal(err)
			}
			if _, ok, err := readLink(linkFile); ok || err != nil {
				t.Fatalf("a link before the helper ran: %v %v", ok, err)
			}
			if out, err := exec.Command(helper, tc.args...).CombinedOutput(); err != nil { // #nosec G204 -- the helper this test wrote
				t.Fatalf("helper: %v: %s", err, out)
			}
			link, ok, err := readLink(linkFile)
			if tc.want == "" {
				if err != nil || !ok || link != tc.args[0] {
					t.Fatalf("readLink = %q %v %v, want %q", link, ok, err, tc.args[0])
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("want an error mentioning %q, got %q %v %v", tc.want, link, ok, err)
			}
		})
	}
}
