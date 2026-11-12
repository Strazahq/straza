package service

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/strazahq/straza/internal/agentguard/localapi"
)

// authorize is the fake's /oidc/authorize: it keeps the request and sends
// the browser back to its loopback redirect with a code and the state.
func (f *fakeStrazad) authorize(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	if !regexp.MustCompile(`^http://127\.0\.0\.1:\d+/callback$`).MatchString(q.Get("redirect_uri")) {
		f.fail(w, http.StatusBadRequest, "redirect_uri is not a loopback callback")
		return
	}
	code := "code-" + randHex(8)
	f.mu.Lock()
	f.authorized, f.codes[code] = q, q
	f.mu.Unlock()
	back := url.Values{"code": {code}, "state": {q.Get("state")}}
	http.Redirect(w, r, q.Get("redirect_uri")+"?"+back.Encode(), http.StatusFound)
}

// codeGrant is the fake's authorization_code grant: the code is spent once,
// the verifier must hash to the request's challenge, and the ID token
// carries the request's nonce.
func (f *fakeStrazad) codeGrant(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	asked, ok := f.codes[r.PostForm.Get("code")]
	delete(f.codes, r.PostForm.Get("code"))
	f.mu.Unlock()
	sum := sha256.Sum256([]byte(r.PostForm.Get("code_verifier")))
	if !ok || base64.RawURLEncoding.EncodeToString(sum[:]) != asked.Get("code_challenge") ||
		r.PostForm.Get("redirect_uri") != asked.Get("redirect_uri") || r.PostForm.Get("client_id") != asked.Get("client_id") {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, `{"error":"invalid_grant"}`)
		return
	}
	payload, _ := json.Marshal(map[string]any{"sub": "alice", "nonce": asked.Get("nonce"), "auth_time": f.clock.Now().Unix()})
	tok := "e30." + base64.RawURLEncoding.EncodeToString(payload) + ".sig-" + randHex(4)
	f.mu.Lock()
	f.idTokens[tok] = asked.Get("nonce")
	f.mu.Unlock()
	f.ok(w, map[string]string{"id_token": tok, "token_type": "Bearer"})
}

// enrollEvents is what one enrollment streamed, safe for the goroutine
// that follows the browser link.
type enrollEvents struct {
	mu     sync.Mutex
	events []localapi.EnrollEvent
	pages  []string
	wg     sync.WaitGroup
}

// emit records ev, and on the browser event plays the person's browser:
// it follows the link over HTTP, the redirect to the service's callback
// included, and keeps the page it ends on.
func (e *enrollEvents) emit(ev localapi.EnrollEvent) {
	e.mu.Lock()
	e.events = append(e.events, ev)
	e.mu.Unlock()
	if ev.Event != localapi.EnrollBrowser {
		return
	}
	e.wg.Add(1)
	go func() {
		defer e.wg.Done()
		resp, err := http.Get(ev.VerificationURI)
		if err != nil {
			return
		}
		defer func() { _ = resp.Body.Close() }()
		page, _ := io.ReadAll(resp.Body)
		e.mu.Lock()
		e.pages = append(e.pages, string(page))
		e.mu.Unlock()
	}()
}

func (e *enrollEvents) kinds() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	var out []string
	for _, ev := range e.events {
		out = append(out, ev.Event)
	}
	return out
}

// TestCredEnrollLanes pins the lane a person's enrollment runs: the empty
// lane and device-code by code, browser by the browser sign-in with a nonce
// that names the new key, a server that requires the browser lane refusing
// the code lane before any code, an older server without the browser lane,
// and a lane the service does not know.
func TestCredEnrollLanes(t *testing.T) {
	selfIdP := func(f *fakeStrazad, min string) map[string]any {
		return map[string]any{"issuer": f.srv.URL, "client_id": "", "nhi_issuer": f.srv.URL, "min_sign_in": min}
	}
	cases := []struct {
		name       string
		lane       string
		min        string // idp.json's min_sign_in, no idp.json when empty
		wantKinds  []string
		wantErr    string
		wantStarts int
	}{
		{name: "the empty lane signs in by code", wantKinds: []string{localapi.EnrollCode}, wantStarts: 1},
		{name: "device-code signs in by code", lane: localapi.LaneDeviceCode, min: "code", wantKinds: []string{localapi.EnrollCode}, wantStarts: 1},
		{name: "browser signs in through the browser", lane: localapi.LaneBrowser, min: "code", wantKinds: []string{localapi.EnrollBrowser}},
		{name: "browser under min_sign_in browser", lane: localapi.LaneBrowser, min: "browser", wantKinds: []string{localapi.EnrollBrowser}},
		{
			name: "the code lane under min_sign_in browser", min: "browser",
			wantErr: "Straza refused this sign-in because your Straza admin requires a browser sign-in, and a sign-in code cannot show which machine it was approved for. " +
				"Run `straza enroll --server %[1]s` in a desktop session on this machine.",
		},
		{
			name: "an older server", lane: localapi.LaneBrowser,
			wantErr: "The Straza server at %[1]s does not offer a browser sign-in yet. Run `straza enroll --server %[1]s --use-device-code`, or ask your Straza admin to upgrade the server.",
		},
		{
			name: "a lane the service does not know", lane: "phone",
			wantErr: "Straza cannot sign you in on the lane \"phone\", because it knows browser and device-code only. " +
				"Run `straza enroll --server %[1]s`, or `straza enroll --server %[1]s --use-device-code` to sign in with a code.",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := newCredRig(t)
			if tc.min != "" {
				r.srv.with(func() { r.srv.idp = selfIdP(r.srv, tc.min) })
			}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			var ev enrollEvents
			res, err := r.s.Enroll(ctx, alice, localapi.EnrollRequest{ServerURL: r.srv.serverURL(), Lane: tc.lane}, ev.emit)
			ev.wg.Wait()
			if tc.wantErr != "" {
				want := tc.wantErr
				if strings.Contains(want, "%[1]s") {
					want = fmt.Sprintf(want, r.srv.serverURL())
				}
				if err == nil || err.Error() != want {
					t.Fatalf("err = %v\nwant %q", err, want)
				}
			} else if err != nil || res.Username != "alice" || res.KeyThumbprint == "" {
				t.Fatalf("Enroll: %+v, %v", res, err)
			}
			var kinds []string
			for _, k := range ev.kinds() {
				if k == localapi.EnrollCode || k == localapi.EnrollBrowser {
					kinds = append(kinds, k)
				}
			}
			if strings.Join(kinds, ",") != strings.Join(tc.wantKinds, ",") {
				t.Errorf("sign-in events %v, want %v", kinds, tc.wantKinds)
			}
			r.srv.with(func() {
				if r.srv.deviceStarts != tc.wantStarts {
					t.Errorf("device authorization requests %d, want %d", r.srv.deviceStarts, tc.wantStarts)
				}
			})
		})
	}
}

// TestCredEnrollBrowser runs a browser-lane enrollment end to end against
// the fake strazad: the request asks for a fresh sign-in with PKCE and the
// scopes openid and profile, the nonce strazad sees names the thumbprint of
// the key it enrolls, the browser lands on the callback page, the port
// closes, and no event carries the ID token.
func TestCredEnrollBrowser(t *testing.T) {
	r := newCredRig(t)
	r.srv.with(func() {
		r.srv.idp = map[string]any{"issuer": r.srv.srv.URL, "client_id": "", "nhi_issuer": r.srv.srv.URL, "min_sign_in": "browser"}
	})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var ev enrollEvents
	res, err := r.s.Enroll(ctx, alice, localapi.EnrollRequest{ServerURL: r.srv.serverURL(), Lane: localapi.LaneBrowser}, ev.emit)
	ev.wg.Wait()
	if err != nil {
		t.Fatalf("Enroll: %v", err)
	}
	var asked url.Values
	var nonce string
	r.srv.with(func() { asked, nonce = r.srv.authorized, r.srv.enrolledNonce })
	want := map[string]string{"response_type": "code", "client_id": "straza", "scope": "openid profile",
		"code_challenge_method": "S256", "prompt": "login", "max_age": "600"}
	for k, v := range want {
		if asked.Get(k) != v {
			t.Errorf("authorization request %s = %q, want %q", k, asked.Get(k), v)
		}
	}
	jkt, rnd, ok := strings.Cut(nonce, ".")
	if !ok || jkt != res.KeyThumbprint || len(rnd) < 22 || asked.Get("nonce") != nonce {
		t.Fatalf("nonce %q does not name the enrolled key %s", nonce, res.KeyThumbprint)
	}
	if len(ev.pages) != 1 || !strings.Contains(ev.pages[0], "You are signed in to Straza on this computer.") {
		t.Errorf("browser pages %q", ev.pages)
	}
	callback, _ := url.Parse(asked.Get("redirect_uri"))
	if resp, err := http.Get(callback.String()); err == nil {
		_ = resp.Body.Close()
		t.Fatalf("the callback port %s still answers after the enrollment", callback.Host)
	}
	for _, e := range ev.events {
		if raw, _ := json.Marshal(e); strings.Contains(string(raw), "e30.") {
			t.Fatalf("an enroll event carries the ID token: %s", raw)
		}
	}
	if st := r.s.Status(alice); st.Enrollment == nil || st.Enrollment.DeviceID != res.DeviceID {
		t.Fatalf("status after the browser enrollment: %+v", st.Enrollment)
	}
}
