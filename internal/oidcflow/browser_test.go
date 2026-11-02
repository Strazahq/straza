package oidcflow

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeAuthServer is an identity provider's browser lane: /authorize keeps
// the request and redirects to its redirect_uri with a code and the state,
// and /token redeems the code for an ID token that carries the request's
// nonce, after checking the PKCE verifier and the redirect.
type fakeAuthServer struct {
	srv *httptest.Server

	mu    sync.Mutex
	asked url.Values            // the last authorization request
	codes map[string]url.Values // code to its authorization request
	// nonce, when set, is the nonce every ID token carries in place of the
	// request's. refuse, when set, is the error /authorize answers, with
	// refuseText as its error_description. forget makes /token treat every
	// code as unknown. redeemed counts code grants.
	nonce      string
	refuse     string
	refuseText string
	forget     bool
	redeemed   int
}

func newFakeAuthServer(t *testing.T) *fakeAuthServer {
	t.Helper()
	f := &fakeAuthServer{codes: map[string]url.Values{}}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /authorize", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		f.mu.Lock()
		f.asked = q
		refuse, text := f.refuse, f.refuseText
		code := fmt.Sprintf("code-%d", len(f.codes)+1)
		f.codes[code] = q
		f.mu.Unlock()
		back := url.Values{"state": {q.Get("state")}}
		if refuse != "" {
			back.Set("error", refuse)
			back.Set("error_description", text)
		} else {
			back.Set("code", code)
		}
		http.Redirect(w, r, q.Get("redirect_uri")+"?"+back.Encode(), http.StatusFound)
	})
	mux.HandleFunc("POST /token", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		f.mu.Lock()
		f.redeemed++
		asked, ok := f.codes[r.PostForm.Get("code")]
		ok = ok && !f.forget
		delete(f.codes, r.PostForm.Get("code"))
		nonce := f.nonce
		f.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		sum := sha256.Sum256([]byte(r.PostForm.Get("code_verifier")))
		switch {
		case !ok || r.PostForm.Get("grant_type") != "authorization_code":
			w.WriteHeader(http.StatusBadRequest)
			_, _ = io.WriteString(w, `{"error":"invalid_grant","error_description":"Code not valid"}`)
			return
		case base64.RawURLEncoding.EncodeToString(sum[:]) != asked.Get("code_challenge"),
			r.PostForm.Get("redirect_uri") != asked.Get("redirect_uri"),
			r.PostForm.Get("client_id") != asked.Get("client_id"):
			w.WriteHeader(http.StatusBadRequest)
			_, _ = io.WriteString(w, `{"error":"invalid_grant","error_description":"PKCE verification failed"}`)
			return
		}
		if nonce == "" {
			nonce = asked.Get("nonce")
		}
		payload, _ := json.Marshal(map[string]any{"sub": "alice", "nonce": nonce})
		_ = json.NewEncoder(w).Encode(map[string]string{"id_token": "e30." + base64.RawURLEncoding.EncodeToString(payload) + ".sig"})
	})
	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeAuthServer) flow() Flow {
	return Flow{Issuer: f.srv.URL, AuthorizeURL: f.srv.URL + "/authorize", TokenURL: f.srv.URL + "/token", ClientID: "straza"}
}

// browserFollow is the person's browser: it follows the link, the
// redirect to the callback included, and keeps the page it ends on.
type browserFollow struct {
	mu     sync.Mutex
	links  []string
	status int
	page   string
	done   chan struct{}
}

func newBrowserFollow() *browserFollow { return &browserFollow{done: make(chan struct{})} }

func (b *browserFollow) open(link string) {
	b.mu.Lock()
	b.links = append(b.links, link)
	b.mu.Unlock()
	go func() {
		defer close(b.done)
		resp, err := http.Get(link)
		if err != nil {
			return
		}
		defer func() { _ = resp.Body.Close() }()
		body, _ := io.ReadAll(resp.Body)
		b.mu.Lock()
		b.status, b.page = resp.StatusCode, string(body)
		b.mu.Unlock()
	}()
}

func (b *browserFollow) result() (int, string) {
	<-b.done
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.status, b.page
}

func nonceOf(t *testing.T, idToken string) string {
	t.Helper()
	parts := strings.Split(idToken, ".")
	raw, _ := base64.RawURLEncoding.DecodeString(parts[1])
	var claims struct {
		Nonce string `json:"nonce"`
	}
	if err := json.Unmarshal(raw, &claims); err != nil {
		t.Fatal(err)
	}
	return claims.Nonce
}

// TestSignInRequest pins the authorization request of the browser lane: the
// loopback redirect, the scopes, the state, the caller's nonce, a fresh
// sign-in, and a PKCE S256 challenge whose verifier the token request
// carries, so the provider's check in the fake passes.
func TestSignInRequest(t *testing.T) {
	idp := newFakeAuthServer(t)
	b := newBrowserFollow()
	tok, err := SignIn(context.Background(), http.DefaultClient, idp.flow(), "jkt.nonce-part-0123456789ab", "straza enroll", b.open)
	if err != nil {
		t.Fatalf("SignIn: %v", err)
	}
	if got := nonceOf(t, tok); got != "jkt.nonce-part-0123456789ab" {
		t.Errorf("ID token nonce = %q", got)
	}
	q := idp.asked
	want := map[string]string{
		"response_type": "code", "client_id": "straza", "scope": "openid profile",
		"nonce": "jkt.nonce-part-0123456789ab", "code_challenge_method": "S256",
		"prompt": "login", "max_age": "0",
	}
	for k, v := range want {
		if q.Get(k) != v {
			t.Errorf("%s = %q, want %q", k, q.Get(k), v)
		}
	}
	if !regexp.MustCompile(`^http://127\.0\.0\.1:\d+/callback$`).MatchString(q.Get("redirect_uri")) {
		t.Errorf("redirect_uri = %q, want http://127.0.0.1:<port>/callback", q.Get("redirect_uri"))
	}
	if len(q.Get("state")) < 22 || len(q.Get("code_challenge")) != 43 {
		t.Errorf("state %q or challenge %q is too short", q.Get("state"), q.Get("code_challenge"))
	}
	if status, page := b.result(); status != http.StatusOK || !strings.Contains(page, "You are signed in to Straza on this computer. You can close this tab.") {
		t.Errorf("callback page = %d %q", status, page)
	}
	if len(b.links) != 1 || !strings.HasPrefix(b.links[0], idp.srv.URL+"/authorize?") {
		t.Errorf("opened %v, want one link at the authorization endpoint", b.links)
	}
}

// TestSignInKeepsTheEndpointQuery pins that a provider whose authorization
// endpoint carries its own query, such as a policy name, keeps it.
func TestSignInKeepsTheEndpointQuery(t *testing.T) {
	idp := newFakeAuthServer(t)
	flow := idp.flow()
	flow.AuthorizeURL += "?p=signin_policy"
	b := newBrowserFollow()
	if _, err := SignIn(context.Background(), http.DefaultClient, flow, "n-0123456789abcdefghijkl", "straza enroll", b.open); err != nil {
		t.Fatalf("SignIn: %v", err)
	}
	if idp.asked.Get("p") != "signin_policy" || idp.asked.Get("response_type") != "code" {
		t.Errorf("request = %v, want the endpoint's own query beside the sign-in's", idp.asked)
	}
}

// TestPKCEPair pins the pair: a 43-character verifier, and a challenge that
// is its SHA-256 in base64url without padding.
func TestPKCEPair(t *testing.T) {
	verifier, challenge := pkcePair()
	if len(verifier) != 43 || strings.ContainsAny(verifier, "+/=") {
		t.Fatalf("verifier %q is not 43 base64url characters", verifier)
	}
	sum := sha256.Sum256([]byte(verifier))
	if challenge != base64.RawURLEncoding.EncodeToString(sum[:]) {
		t.Fatalf("challenge %q is not the S256 of the verifier", challenge)
	}
	if again, _ := pkcePair(); again == verifier {
		t.Fatal("two pairs share a verifier")
	}
}

// TestSignInRefusals pins each way a callback ends the sign-in with a
// sentence: the provider's refusal, an ID token made for another nonce, a
// code the provider will not redeem and an answer with no ID token. Each
// names the command to run again, and the browser page says what failed.
func TestSignInRefusals(t *testing.T) {
	cases := []struct {
		name     string
		setup    func(f *fakeAuthServer, flow *Flow)
		wantErr  string
		wantPage string
	}{
		{
			name: "the provider refused",
			setup: func(f *fakeAuthServer, _ *Flow) {
				f.refuse, f.refuseText = "access_denied", "The user cancelled."
			},
			wantErr:  "Your identity provider refused the sign-in and said: The user cancelled (access_denied). Run `straza enroll` again, and ask IT if it keeps happening.",
			wantPage: "Your identity provider refused the sign-in and said: The user cancelled (access_denied).",
		},
		{
			name:     "a token made for another sign-in",
			setup:    func(f *fakeAuthServer, _ *Flow) { f.nonce = "other-key.0123456789abcdefghijkl" },
			wantErr:  "Straza refused this sign-in because the identity provider's answer was made for a different sign-in. Run `straza enroll` again and finish the sign-in in the browser it opens.",
			wantPage: "made for a different sign-in",
		},
		{
			name:     "the code is not redeemed",
			setup:    func(f *fakeAuthServer, _ *Flow) { f.forget = true },
			wantErr:  "Your identity provider refused the sign-in and said: Code not valid (invalid_grant). Run `straza enroll` again, and ask IT if it keeps happening.",
			wantPage: "Code not valid (invalid_grant)",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			idp := newFakeAuthServer(t)
			flow := idp.flow()
			b := newBrowserFollow()
			tc.setup(idp, &flow)
			_, err := SignIn(context.Background(), http.DefaultClient, flow, "n.0123456789abcdefghijkl", "straza enroll", b.open)
			if err == nil || err.Error() != tc.wantErr {
				t.Fatalf("err = %v\nwant %q", err, tc.wantErr)
			}
			if status, page := b.result(); status != http.StatusBadRequest || !strings.Contains(page, tc.wantPage) {
				t.Errorf("page = %d %q, want it to say %q", status, page, tc.wantPage)
			}
		})
	}
}

// TestSignInNoIDToken pins the answer of a provider that redeems the code
// and sends no ID token.
func TestSignInNoIDToken(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /authorize", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		http.Redirect(w, r, q.Get("redirect_uri")+"?code=c1&state="+url.QueryEscape(q.Get("state")), http.StatusFound)
	})
	mux.HandleFunc("POST /token", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"access_token":"at"}`)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	flow := Flow{Issuer: srv.URL, AuthorizeURL: srv.URL + "/authorize", TokenURL: srv.URL + "/token", ClientID: "strazactl"}
	_, err := SignIn(context.Background(), http.DefaultClient, flow, "n.0123456789abcdefghijkl", "strazactl login", newBrowserFollow().open)
	want := "The identity provider finished the sign-in and sent no ID token. Ask IT to allow the openid scope for the Straza client, then run `strazactl login` again."
	if err == nil || err.Error() != want {
		t.Fatalf("err = %v, want %q", err, want)
	}
}

// TestSignInIgnoresAWrongState pins that a callback with another state is
// answered with an error page and does not end the sign-in, which the
// right callback then finishes.
func TestSignInIgnoresAWrongState(t *testing.T) {
	idp := newFakeAuthServer(t)
	links := make(chan string, 1)
	type outcome struct {
		tok string
		err error
	}
	done := make(chan outcome, 1)
	go func() {
		tok, err := SignIn(context.Background(), http.DefaultClient, idp.flow(), "n.0123456789abcdefghijkl", "straza enroll", func(link string) { links <- link })
		done <- outcome{tok, err}
	}()
	link := <-links
	u, _ := url.Parse(link)
	callback := u.Query().Get("redirect_uri")
	resp, err := http.Get(callback + "?code=forged&state=not-the-state")
	if err != nil {
		t.Fatal(err)
	}
	page, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest || !strings.Contains(string(page), "does not belong to the sign-in Straza is waiting for") {
		t.Fatalf("wrong-state page = %d %q", resp.StatusCode, page)
	}
	select {
	case o := <-done:
		t.Fatalf("a wrong state ended the sign-in: %v", o.err)
	case <-time.After(100 * time.Millisecond):
	}
	idp.mu.Lock()
	redeemed := idp.redeemed
	idp.mu.Unlock()
	if redeemed != 0 {
		t.Fatalf("a wrong-state code was redeemed")
	}
	b := newBrowserFollow()
	b.open(link)
	if o := <-done; o.err != nil || o.tok == "" {
		t.Fatalf("the right callback did not finish the sign-in: %v", o.err)
	}
}

// TestSignInClosesThePort pins that the listener is gone once the sign-in
// answered, whether it succeeded, timed out or was cancelled.
func TestSignInClosesThePort(t *testing.T) {
	cases := []struct {
		name   string
		follow bool
		wait   time.Duration
		cancel bool
		want   string
	}{
		{name: "signed in", follow: true, wait: time.Minute},
		{name: "timed out", wait: 300 * time.Millisecond, want: "The browser sign-in did not finish within 5 minutes. Run `straza enroll` again."},
		{name: "cancelled", wait: time.Minute, cancel: true, want: context.Canceled.Error()},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			old := signInWait
			signInWait = tc.wait
			t.Cleanup(func() { signInWait = old })
			idp := newFakeAuthServer(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			var callback string
			b := newBrowserFollow()
			_, err := SignIn(ctx, http.DefaultClient, idp.flow(), "n.0123456789abcdefghijkl", "straza enroll", func(link string) {
				u, _ := url.Parse(link)
				callback = u.Query().Get("redirect_uri")
				switch {
				case tc.follow:
					b.open(link)
				case tc.cancel:
					cancel()
				}
			})
			switch {
			case tc.want == "" && err != nil:
				t.Fatalf("SignIn: %v", err)
			case tc.want != "" && (err == nil || err.Error() != tc.want):
				t.Fatalf("err = %v, want %q", err, tc.want)
			}
			u, _ := url.Parse(callback)
			if conn, err := net.DialTimeout("tcp", u.Host, time.Second); err == nil {
				_ = conn.Close()
				t.Fatalf("the callback port %s still accepts connections", u.Host)
			}
		})
	}
}

// TestSignInLoopbackPort pins the published port: the redirect uses it,
// and a port another program holds stops the sign-in with the sentence.
func TestSignInLoopbackPort(t *testing.T) {
	idp := newFakeAuthServer(t)
	busy, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = busy.Close() })
	port := busy.Addr().(*net.TCPAddr).Port

	flow := idp.flow()
	flow.LoopbackPort = port
	_, err = SignIn(context.Background(), http.DefaultClient, flow, "n.0123456789abcdefghijkl", "straza enroll", func(string) { t.Error("opened a link with no listener") })
	want := fmt.Sprintf("Straza could not wait for the sign-in answer on port %d, because another program uses it. Wait a minute and run `straza enroll` again, or ask your Straza admin to change oidc.loopbackPort.", port)
	if err == nil || err.Error() != want {
		t.Fatalf("err = %v, want %q", err, want)
	}

	_ = busy.Close()
	b := newBrowserFollow()
	if _, err := SignIn(context.Background(), http.DefaultClient, flow, "n.0123456789abcdefghijkl", "straza enroll", b.open); err != nil {
		t.Fatalf("SignIn on the published port: %v", err)
	}
	if got := idp.asked.Get("redirect_uri"); got != "http://127.0.0.1:"+strconv.Itoa(port)+"/callback" {
		t.Errorf("redirect_uri = %q, want the published port %d", got, port)
	}
}

// TestCallbackPageEscapesTheProvider pins that the provider's error text
// reaches the browser page as text, never as markup.
func TestCallbackPageEscapesTheProvider(t *testing.T) {
	idp := newFakeAuthServer(t)
	idp.refuse, idp.refuseText = "access_denied", `<script>alert("x")</script>`
	b := newBrowserFollow()
	_, err := SignIn(context.Background(), http.DefaultClient, idp.flow(), "n.0123456789abcdefghijkl", "straza enroll", b.open)
	if err == nil {
		t.Fatal("a refused sign-in succeeded")
	}
	_, page := b.result()
	if strings.Contains(page, "<script>") || !strings.Contains(page, "&lt;script&gt;") {
		t.Fatalf("page = %q, want the provider's text escaped", page)
	}
}

// TestSignInChecksTheFlow pins the checks before any listener: a flow
// without an authorization endpoint gets the older-server sentence when the
// server is its own issuer, and an endpoint that is not http or https is
// never opened.
func TestSignInChecksTheFlow(t *testing.T) {
	cases := []struct {
		name string
		flow Flow
		want string
	}{
		{
			name: "an older server",
			flow: Builtin("https://straza.example", "straza"),
			want: "The Straza server at https://straza.example does not offer a browser sign-in yet. Run `straza enroll --use-device-code`, or ask your Straza admin to upgrade the server.",
		},
		{
			name: "a provider without the endpoint",
			flow: Flow{Issuer: "https://idp.example", TokenURL: "https://idp.example/token", ClientID: "straza", server: "https://straza.example"},
			want: "The identity provider https://idp.example advertises no authorization_endpoint, so Straza cannot sign you in through a browser there. Run `straza enroll --use-device-code`, or ask IT to turn on the authorization code flow for client \"straza\".",
		},
		{
			name: "an endpoint that is not a web address",
			flow: Flow{Issuer: "https://idp.example", AuthorizeURL: "file:///etc/passwd", TokenURL: "https://idp.example/token", ClientID: "straza"},
			want: "The identity provider https://idp.example advertises the authorization endpoint \"file:///etc/passwd\", which is not an http or https URL, so Straza does not open it. Ask IT to check the provider's OIDC discovery document.",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := SignIn(context.Background(), http.DefaultClient, tc.flow, "n.0123456789abcdefghijkl", "straza enroll", func(string) { t.Error("opened a link") })
			if err == nil || err.Error() != tc.want {
				t.Fatalf("err = %v\nwant %q", err, tc.want)
			}
		})
	}
}

// TestNonce pins the C2 form: the key's thumbprint, a dot and at least 16
// random bytes in base64url, or the random part alone without a key.
func TestNonce(t *testing.T) {
	keyed := Nonce("NzbLsXh8uDCcd-6MNwXF4W_7noWXFZAfHkxZsRGC9Xs")
	jkt, r, ok := strings.Cut(keyed, ".")
	if !ok || jkt != "NzbLsXh8uDCcd-6MNwXF4W_7noWXFZAfHkxZsRGC9Xs" || len(r) < 22 {
		t.Fatalf("keyed nonce %q is not <jkt>.<r>", keyed)
	}
	if raw, err := base64.RawURLEncoding.DecodeString(r); err != nil || len(raw) < 16 {
		t.Fatalf("random part %q is not 16 bytes of base64url: %v", r, err)
	}
	plain := Nonce("")
	if strings.Contains(plain, ".") || len(plain) < 22 || plain == r {
		t.Fatalf("plain nonce %q", plain)
	}
}

// TestSignInContextErrors keeps errors.Is working on a cancelled sign-in.
func TestSignInContextErrors(t *testing.T) {
	idp := newFakeAuthServer(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := SignIn(ctx, http.DefaultClient, idp.flow(), "n.0123456789abcdefghijkl", "straza enroll", func(string) {})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
}
