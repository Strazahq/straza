package authn

import (
	"context"
	"encoding/json"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"

	"github.com/strazahq/straza/internal/config"
	"github.com/strazahq/straza/internal/store"
	"github.com/strazahq/straza/internal/store/storetest"
)

// TestHashPasswordVerifiesAtDefaultCost pins the production hash: the seeded
// test users carry a cheap hash, so this is the check that HashPassword
// stores bcrypt at the default cost and that the login compare accepts its
// password and refuses another.
func TestHashPasswordVerifiesAtDefaultCost(t *testing.T) {
	hash, err := HashPassword("hunter2!")
	if err != nil {
		t.Fatal(err)
	}
	if cost, err := bcrypt.Cost([]byte(hash)); err != nil || cost != bcrypt.DefaultCost {
		t.Fatalf("hash cost = %d, %v, want %d", cost, err, bcrypt.DefaultCost)
	}
	for _, tc := range []struct {
		password string
		accepts  bool
	}{
		{"hunter2!", true},
		{"hunter3!", false},
		{"", false},
	} {
		if got := bcrypt.CompareHashAndPassword([]byte(hash), []byte(tc.password)) == nil; got != tc.accepts {
			t.Errorf("compare %q accepts = %v, want %v", tc.password, got, tc.accepts)
		}
	}
}

type issuerFixture struct {
	srv    *httptest.Server
	issuer *Issuer
	tokens *TokenService
	user   store.User
	users  store.UserRepo
}

// newIssuerFixture boots a real store, token service and issuer behind an
// httptest server, the scripted client target for the RFC 8628 device flow.
func newIssuerFixture(t *testing.T) *issuerFixture {
	t.Helper()
	ctx := context.Background()
	dsn := filepath.Join(t.TempDir(), "t.db")
	storetest.SeedSQLite(t, dsn)
	s, err := store.Open(config.Config{Store: config.Store{
		Driver: config.DriverSQLite,
		DSN:    dsn,
	}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	if err := s.Migrate(ctx); err != nil {
		t.Fatal(err)
	}

	hash, err := storetest.PasswordHash("hunter2!")
	if err != nil {
		t.Fatal(err)
	}
	user, err := s.Users().Create(ctx, store.User{
		Username: "kim", Email: "kim@x.io", PasswordHash: hash,
	})
	if err != nil {
		t.Fatal(err)
	}

	mux := http.NewServeMux()
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	tokens, err := NewTokenService(ctx, s.SigningKeys(), srv.URL, 0)
	if err != nil {
		t.Fatal(err)
	}
	iss := NewIssuer(s.Users(), tokens, srv.URL)
	// A WIDE interval on purpose: the "fast re-poll → slow_down" assertions
	// need two loopback polls to land INSIDE it, and a 5 ms window loses that
	// race under -race on a busy machine. Tests never wait it out.
	// outwaitInterval rewinds the grant's stamp instead of sleeping.
	iss.interval = time.Second
	iss.Routes(mux)

	return &issuerFixture{srv: srv, issuer: iss, tokens: tokens, user: user, users: s.Users()}
}

// outwaitInterval stands in for "wait for the polling interval to pass": it
// rewinds the grant's last-poll stamp instead of sleeping through the real
// thing. It is deterministic at any interval, where a wall-clock sleep is
// not, and the slow_down branch RESETS the stamp on a too-fast poll, so
// after the fact a late sleep is indistinguishable from an early poll. A
// missing grant (consumed one-shot) is a deliberate no-op: the poll after it
// answers from grant absence, not timing.
func (f *issuerFixture) outwaitInterval(deviceCode string) {
	f.issuer.mu.Lock()
	defer f.issuer.mu.Unlock()
	if g := f.issuer.grants[deviceCode]; g != nil {
		g.lastPoll = g.lastPoll.Add(-2 * f.issuer.interval)
	}
}

func (f *issuerFixture) postForm(t *testing.T, path string, form url.Values) (int, map[string]any) {
	t.Helper()
	resp, err := http.PostForm(f.srv.URL+path, form)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	var body map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("non-JSON response from %s: %v", path, err)
	}
	return resp.StatusCode, body
}

func (f *issuerFixture) pollToken(t *testing.T, deviceCode, clientID string) (int, map[string]any) {
	t.Helper()
	return f.postForm(t, "/oidc/token", url.Values{
		"grant_type":  {"urn:ietf:params:oauth:grant-type:device_code"},
		"device_code": {deviceCode},
		"client_id":   {clientID},
	})
}

func (f *issuerFixture) submitLogin(t *testing.T, userCode, username, password string) string {
	t.Helper()
	resp, err := http.PostForm(f.srv.URL+"/oidc/device", url.Values{
		"user_code": {userCode}, "username": {username}, "password": {password},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	page, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return string(page)
}

func TestDeviceFlowEndToEnd(t *testing.T) {
	f := newIssuerFixture(t)

	// Discovery advertises the flow.
	resp, err := http.Get(f.srv.URL + "/.well-known/openid-configuration")
	if err != nil {
		t.Fatal(err)
	}
	var disc map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&disc)
	_ = resp.Body.Close()
	if disc["device_authorization_endpoint"] == "" || disc["token_endpoint"] == "" {
		t.Fatalf("discovery incomplete: %v", disc)
	}

	// 1. Client requests device authorization.
	code, body := f.postForm(t, "/oidc/device_authorization", url.Values{"client_id": {"straza"}})
	if code != http.StatusOK {
		t.Fatalf("device_authorization = %d: %v", code, body)
	}
	deviceCode := body["device_code"].(string)
	userCode := body["user_code"].(string)
	if len(userCode) != 9 || userCode[4] != '-' {
		t.Errorf("user_code format: %q", userCode)
	}

	// 2. Polling before approval → authorization_pending.
	code, body = f.pollToken(t, deviceCode, "straza")
	if code != http.StatusBadRequest || body["error"] != "authorization_pending" {
		t.Fatalf("pre-approval poll = %d %v", code, body)
	}

	// 3. Polling faster than the interval → slow_down.
	code, body = f.pollToken(t, deviceCode, "straza")
	if body["error"] != "slow_down" {
		t.Fatalf("fast re-poll = %d %v", code, body)
	}
	f.outwaitInterval(deviceCode)

	// 4. Wrong password rejected; grant stays pending.
	page := f.submitLogin(t, userCode, "kim", "wrong")
	if !strings.Contains(page, "Invalid username or password") {
		t.Fatalf("wrong password page: %s", page)
	}

	// 5. Correct login approves the device.
	page = f.submitLogin(t, userCode, "kim", "hunter2!")
	if !strings.Contains(page, "Signed in") || !strings.Contains(page, "You can close this tab") {
		t.Fatalf("approval page: %s", page)
	}

	// 6. Next poll returns tokens; ID token verifies with correct claims.
	f.outwaitInterval(deviceCode)
	code, body = f.pollToken(t, deviceCode, "straza")
	if code != http.StatusOK || body["id_token"] == nil {
		t.Fatalf("token exchange = %d %v", code, body)
	}
	claims, err := f.tokens.VerifyIDToken(body["id_token"].(string), "straza")
	if err != nil {
		t.Fatalf("VerifyIDToken: %v", err)
	}
	if claims.Subject != f.user.ID || claims.Username != "kim" || claims.Email != "kim@x.io" {
		t.Errorf("id claims = %+v", claims)
	}

	// 7. The grant is one-shot (the exchange consumed it, so this rewind is
	// the documented no-op: the reply comes from grant absence, not timing).
	f.outwaitInterval(deviceCode)
	code, body = f.pollToken(t, deviceCode, "straza")
	if code != http.StatusBadRequest || body["error"] != "invalid_grant" {
		t.Fatalf("replayed device_code = %d %v", code, body)
	}
}

// TestDeviceFlowWireContract pins the RFC 6749 wire truth every client must
// code against: protocol states arrive as HTTP 400 with an `error` field,
// NOT 200. Three clients (console, straza, strazactl) poll this endpoint, and
// a client that assumes 200 breaks, so the status code is contract, not
// detail.
func TestDeviceFlowWireContract(t *testing.T) {
	f := newIssuerFixture(t)

	_, auth := f.postForm(t, "/oidc/device_authorization", url.Values{"client_id": {"straza"}})
	dc := auth["device_code"].(string)

	// Pending: 400 + authorization_pending while the human is still typing
	// (first poll: no last-poll stamp exists yet, nothing to outwait).
	code, body := f.pollToken(t, dc, "straza")
	if code != http.StatusBadRequest || body["error"] != "authorization_pending" {
		t.Fatalf("pending poll = %d %v, want 400 authorization_pending", code, body)
	}
	// Polling faster than the interval: 400 + slow_down.
	if code, body = f.pollToken(t, dc, "straza"); code != http.StatusBadRequest || body["error"] != "slow_down" {
		t.Fatalf("fast re-poll = %d %v, want 400 slow_down", code, body)
	}

	// Approval flips the next on-interval poll to 200 + id_token.
	_ = f.submitLogin(t, auth["user_code"].(string), "kim", "hunter2!")
	f.outwaitInterval(dc)
	if code, body = f.pollToken(t, dc, "straza"); code != http.StatusOK || body["id_token"] == nil {
		t.Fatalf("approved poll = %d %v, want 200 with id_token", code, body)
	}
}

func TestDeviceFlowNegativePaths(t *testing.T) {
	f := newIssuerFixture(t)

	// client_id required.
	code, body := f.postForm(t, "/oidc/device_authorization", url.Values{})
	if code != http.StatusBadRequest || body["error"] != "invalid_request" {
		t.Fatalf("missing client_id = %d %v", code, body)
	}

	// Unknown grant type.
	code, body = f.postForm(t, "/oidc/token", url.Values{"grant_type": {"password"}})
	if code != http.StatusBadRequest || body["error"] != "unsupported_grant_type" {
		t.Fatalf("bad grant type = %d %v", code, body)
	}

	// Wrong client_id on poll → invalid_grant.
	_, auth := f.postForm(t, "/oidc/device_authorization", url.Values{"client_id": {"straza"}})
	dc := auth["device_code"].(string)
	if _, body := f.pollToken(t, dc, "other-client"); body["error"] != "invalid_grant" {
		t.Fatalf("client mismatch = %v", body)
	}

	// Unknown user code on the login page.
	page := f.submitLogin(t, "XXXX-XXXX", "kim", "hunter2!")
	if !strings.Contains(page, "That code is not valid or has expired") {
		t.Fatalf("unknown code page: %s", page)
	}

	// Expired grant → expired_token and cleanup.
	_, auth = f.postForm(t, "/oidc/device_authorization", url.Values{"client_id": {"straza"}})
	dc = auth["device_code"].(string)
	f.issuer.mu.Lock()
	f.issuer.grants[dc].expiresAt = time.Now().Add(-time.Minute)
	f.issuer.mu.Unlock()
	if _, body := f.pollToken(t, dc, "straza"); body["error"] != "expired_token" {
		t.Fatalf("expired grant = %v", body)
	}

	// Disabled users cannot log in.
	ctxUser := f.user
	ctxUser.Status = store.UserDisabled
	if _, err := f.issuer.users.Update(context.Background(), ctxUser); err != nil {
		t.Fatal(err)
	}
	_, auth = f.postForm(t, "/oidc/device_authorization", url.Values{"client_id": {"straza"}})
	page = f.submitLogin(t, auth["user_code"].(string), "kim", "hunter2!")
	if !strings.Contains(page, "Invalid username or password") {
		t.Fatalf("disabled user login page: %s", page)
	}
}

// TestOnLoginFailed pins the failed-password observation hook (authn events,
// spec/events rev 21): it fires exactly once per refused password submit,
// with the post-zeroing username (an unknown or inactive user reads as "",
// so the emitter can never fabricate an identity), and it stays silent on
// success and on unknown device codes (no credential was judged there).
func TestOnLoginFailed(t *testing.T) {
	cases := []struct {
		name      string
		user      string
		password  string
		userCode  string // "" = use a freshly minted valid code
		wantFires int
		wantUser  string
	}{
		{name: "wrong password, real user", user: "kim", password: "nope", wantFires: 1, wantUser: "kim"},
		{name: "unknown user", user: "ghost", password: "hunter2!", wantFires: 1, wantUser: ""},
		{name: "success", user: "kim", password: "hunter2!", wantFires: 0},
		{name: "unknown device code", user: "kim", password: "hunter2!", userCode: "XXXX-XXXX", wantFires: 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newIssuerFixture(t)
			var fires int
			var gotUser string
			var gotReq *http.Request
			f.issuer.OnLoginFailed = func(username, _ string, r *http.Request) {
				fires++
				gotUser = username
				gotReq = r
			}
			code := tc.userCode
			if code == "" {
				_, auth := f.postForm(t, "/oidc/device_authorization", url.Values{"client_id": {"straza"}})
				code = auth["user_code"].(string)
			}
			f.submitLogin(t, code, tc.user, tc.password)
			if fires != tc.wantFires {
				t.Fatalf("OnLoginFailed fired %d times, want %d", fires, tc.wantFires)
			}
			if tc.wantFires > 0 {
				if gotUser != tc.wantUser {
					t.Errorf("OnLoginFailed username = %q, want %q", gotUser, tc.wantUser)
				}
				if gotReq == nil {
					t.Errorf("OnLoginFailed request = nil, want the submit request (sourceIp/userAgent source)")
				}
			}
		})
	}
}

// TestLoginOnly pins the emergency mount: with LoginOnly set, the page
// names the one account it signs in, that account's password opens it,
// every other username is refused before its credential is judged (a valid
// hash on another row is not a credential here) with OnLoginFailed firing
// on the post-zeroing empty username, and the code stays pending.
func TestLoginOnly(t *testing.T) {
	cases := []struct {
		name       string
		user       string
		password   string
		wantDone   bool
		wantError  string
		wantFires  int
		wantStatus string
	}{
		{name: "the named account signs in", user: "kim", password: "hunter2!", wantDone: true, wantStatus: "approved"},
		{name: "the named account with a wrong password", user: "kim", password: "nope", wantError: "Invalid username or password.", wantFires: 1, wantStatus: "pending"},
		{name: "another user with a valid hash", user: "bob", password: "hunter2!", wantError: "This page signs in the account kim only. Everyone else signs in at your identity provider.", wantFires: 1, wantStatus: "pending"},
		{name: "an unknown user", user: "ghost", password: "hunter2!", wantError: "This page signs in the account kim only.", wantFires: 1, wantStatus: "pending"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newIssuerFixture(t)
			f.issuer.LoginOnly = "kim"
			hash, err := storetest.PasswordHash("hunter2!")
			if err != nil {
				t.Fatal(err)
			}
			if _, err := f.users.Create(context.Background(), store.User{Username: "bob", Email: "bob@x.io", PasswordHash: hash}); err != nil {
				t.Fatal(err)
			}
			var fires int
			var gotUser string
			f.issuer.OnLoginFailed = func(username, _ string, _ *http.Request) {
				fires++
				gotUser = username
			}

			resp, err := http.Get(f.srv.URL + "/oidc/device")
			if err != nil {
				t.Fatal(err)
			}
			page, _ := io.ReadAll(resp.Body)
			_ = resp.Body.Close()
			if !strings.Contains(string(page), "Emergency sign-in to Straza") || !strings.Contains(string(page), "signs in the emergency admin kim and nobody else") {
				t.Fatalf("emergency page does not name its account: %s", page)
			}

			_, auth := f.postForm(t, "/oidc/device_authorization", url.Values{"client_id": {"straza"}})
			code := auth["user_code"].(string)
			deviceCode := auth["device_code"].(string)
			page = []byte(f.submitLogin(t, code, tc.user, tc.password))
			if tc.wantDone != strings.Contains(string(page), "Signed in") {
				t.Fatalf("done = %v, want %v: %s", !tc.wantDone, tc.wantDone, page)
			}
			if tc.wantError != "" && !strings.Contains(string(page), tc.wantError) {
				t.Fatalf("page lacks %q: %s", tc.wantError, page)
			}
			if fires != tc.wantFires {
				t.Fatalf("OnLoginFailed fired %d times, want %d", fires, tc.wantFires)
			}
			if fires > 0 && tc.user != "kim" && gotUser != "" {
				t.Errorf("OnLoginFailed username = %q, want empty (no credential judged)", gotUser)
			}
			f.issuer.mu.Lock()
			status := f.issuer.grants[deviceCode].status
			f.issuer.mu.Unlock()
			if status != tc.wantStatus {
				t.Errorf("grant status = %q, want %q", status, tc.wantStatus)
			}
		})
	}
}

// TestDeviceGrantStampsAuthTime pins that the device grant's ID token
// carries auth_time from the moment the password was checked, not from the
// later token poll, and carries no nonce, because RFC 8628 has none.
func TestDeviceGrantStampsAuthTime(t *testing.T) {
	f := newIssuerFixture(t)
	_, auth := f.postForm(t, "/oidc/device_authorization", url.Values{"client_id": {"straza"}})
	deviceCode := auth["device_code"].(string)

	before := time.Now()
	_ = f.submitLogin(t, auth["user_code"].(string), "kim", "hunter2!")
	after := time.Now()
	f.issuer.mu.Lock()
	stamped := f.issuer.grants[deviceCode].authTime
	// Move the stamp back an hour, so a token minted with the poll's own
	// clock cannot pass for the stamp.
	stamped = stamped.Add(-time.Hour)
	f.issuer.grants[deviceCode].authTime = stamped
	f.issuer.mu.Unlock()
	if stamped.Add(time.Hour).Before(before) || stamped.Add(time.Hour).After(after) {
		t.Fatalf("the grant's auth time %s is not the moment of the password check, between %s and %s", stamped.Add(time.Hour), before, after)
	}

	code, body := f.pollToken(t, deviceCode, "straza")
	if code != http.StatusOK {
		t.Fatalf("token poll = %d %v", code, body)
	}
	claims, err := f.tokens.VerifyIDToken(body["id_token"].(string), "straza")
	if err != nil {
		t.Fatal(err)
	}
	if !claims.AuthTime.Equal(stamped.Truncate(time.Second)) {
		t.Errorf("auth_time = %s, want the grant's stamp %s", claims.AuthTime, stamped.Truncate(time.Second))
	}
	if claims.Nonce != "" {
		t.Errorf("nonce = %q, want none on the device grant", claims.Nonce)
	}
}

// TestDiscoveryAdvertisesCodeFlow pins the discovery document: the
// authorization endpoint, code as the only response type, the
// authorization_code grant, S256 as the only PKCE method and the public
// client auth method none, beside the device grant it already advertised.
func TestDiscoveryAdvertisesCodeFlow(t *testing.T) {
	f := newIssuerFixture(t)
	resp, err := http.Get(f.srv.URL + "/.well-known/openid-configuration")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	var disc map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&disc); err != nil {
		t.Fatal(err)
	}
	has := func(member, want string) bool {
		list, _ := disc[member].([]any)
		for _, v := range list {
			if v == want {
				return true
			}
		}
		return false
	}
	if disc["authorization_endpoint"] != f.srv.URL+"/oidc/authorize" {
		t.Errorf("authorization_endpoint = %v, want %s/oidc/authorize", disc["authorization_endpoint"], f.srv.URL)
	}
	for _, c := range []struct{ member, want string }{
		{"response_types_supported", "code"},
		{"grant_types_supported", "authorization_code"},
		{"grant_types_supported", "urn:ietf:params:oauth:grant-type:device_code"},
		{"grant_types_supported", "client_credentials"},
		{"code_challenge_methods_supported", "S256"},
		{"token_endpoint_auth_methods_supported", "none"},
		{"token_endpoint_auth_methods_supported", "private_key_jwt"},
	} {
		if !has(c.member, c.want) {
			t.Errorf("%s = %v, want it to list %s", c.member, disc[c.member], c.want)
		}
	}
	if methods, _ := disc["code_challenge_methods_supported"].([]any); len(methods) != 1 {
		t.Errorf("code_challenge_methods_supported = %v, want S256 alone", methods)
	}
	if types, _ := disc["response_types_supported"].([]any); len(types) != 1 {
		t.Errorf("response_types_supported = %v, want code alone, the one type /oidc/authorize serves", types)
	}
	if disc["device_authorization_endpoint"] != f.srv.URL+"/oidc/device_authorization" {
		t.Errorf("device_authorization_endpoint = %v", disc["device_authorization_endpoint"])
	}
}

// hookUsers runs onGet inside GetByUsername, which the password check calls
// between reading the grant and approving it. A test uses it to change the
// grant at that exact moment, as an overlapping submit would.
type hookUsers struct {
	store.UserRepo
	onGet func()
}

func (h hookUsers) GetByUsername(ctx context.Context, username string) (store.User, error) {
	h.onGet()
	return h.UserRepo.GetByUsername(ctx, username)
}

// TestDeviceApprovalRechecksTheGrant pins that a correct password approves
// a device grant only if the grant is still pending and unexpired at the
// moment of approval. A grant another submit approved, or one that expired
// during the password check, keeps its state. OnLogin still fires once,
// because the person did authenticate, so a break-glass password confirmed
// against a lapsing grant still raises the alarm.
func TestDeviceApprovalRechecksTheGrant(t *testing.T) {
	earlier := time.Now().Add(-time.Hour)
	cases := []struct {
		name   string
		change func(g *deviceGrant)
	}{
		{name: "another submit approved it first", change: func(g *deviceGrant) {
			g.status, g.userID, g.authTime = "approved", "user-first", earlier
		}},
		{name: "it expired during the check", change: func(g *deviceGrant) {
			g.expiresAt = time.Now().Add(-time.Second)
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newIssuerFixture(t)
			_, auth := f.postForm(t, "/oidc/device_authorization", url.Values{"client_id": {"straza"}})
			deviceCode := auth["device_code"].(string)
			f.issuer.mu.Lock()
			g := f.issuer.grants[deviceCode]
			f.issuer.mu.Unlock()
			f.issuer.users = hookUsers{UserRepo: f.users, onGet: func() {
				f.issuer.mu.Lock()
				tc.change(g)
				f.issuer.mu.Unlock()
			}}
			var logins int
			f.issuer.OnLogin = func(string, string) { logins++ }
			f.issuer.mu.Lock()
			before := *g
			f.issuer.mu.Unlock()
			tc.change(&before)

			page := f.submitLogin(t, auth["user_code"].(string), "kim", "hunter2!")
			if strings.Contains(page, "Signed in") || !strings.Contains(page, "That code is not valid or has expired") {
				t.Errorf("the submit page = %s, want the code refused", page)
			}
			f.issuer.mu.Lock()
			after := *g
			f.issuer.mu.Unlock()
			if after.status != before.status || after.userID != before.userID || !after.authTime.Equal(before.authTime) {
				t.Errorf("the grant changed to %s %q %s, want %s %q %s", after.status, after.userID, after.authTime, before.status, before.userID, before.authTime)
			}
			if logins != 1 {
				t.Errorf("OnLogin fired %d times, want once for the correct password", logins)
			}
		})
	}
}

// TestDeviceApprovalOnce sends overlapping correct submits for one code and
// checks that exactly one approves it. OnLogin fires for each submit whose
// password was judged, one to four of them: a submit that reaches the code
// after another approved it is refused before its password is judged, and
// TestDeviceApprovalRechecksTheGrant pins the overlap case. Under -race it
// also pins that the grant is read under the lock.
func TestDeviceApprovalOnce(t *testing.T) {
	f := newIssuerFixture(t)
	_, auth := f.postForm(t, "/oidc/device_authorization", url.Values{"client_id": {"straza"}})
	var mu sync.Mutex
	var logins int
	f.issuer.OnLogin = func(string, string) {
		mu.Lock()
		logins++
		mu.Unlock()
	}
	const submits = 4
	pages := make(chan string, submits)
	var wg sync.WaitGroup
	for range submits {
		wg.Add(1)
		go func() {
			defer wg.Done()
			pages <- f.submitLogin(t, auth["user_code"].(string), "kim", "hunter2!")
		}()
	}
	wg.Wait()
	close(pages)
	var signedIn int
	for page := range pages {
		if strings.Contains(page, "Signed in") {
			signedIn++
		}
	}
	if signedIn != 1 || logins < 1 || logins > submits {
		t.Errorf("%d submits signed in and OnLogin fired %d times, want 1 and 1 to %d", signedIn, logins, submits)
	}
}

// The sentences the device authorization endpoint answers a refusal with.
const (
	wantClientRefused = "client_id must be straza, strazactl or console, the Straza programs this server signs in. Start the sign-in again with straza enroll."
	wantGrantsFull    = "This server already holds 4096 sign-ins waiting for a code, the most it keeps. Wait a few minutes and sign in again, or sign in through the browser. If it repeats, look in the access log for a flood of POST /oidc/device_authorization."
)

// grantCount reads how many device grants the issuer holds.
func (f *issuerFixture) grantCount() int {
	f.issuer.mu.Lock()
	defer f.issuer.mu.Unlock()
	return len(f.issuer.grants)
}

// startDevice sends one device authorization request for straza straight
// to the handler, which fills the grant table much faster than a round trip.
func (f *issuerFixture) startDevice(t *testing.T) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(http.MethodPost, "/oidc/device_authorization", strings.NewReader("client_id=straza"))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	f.issuer.handleDeviceAuthorization(rec, r)
	return rec
}

// TestDeviceAuthorizationClientID pins that a device sign-in starts only
// for exactly one client_id that names a Straza program. A refusal holds no
// grant and judges no credential, so it fires no login hook.
func TestDeviceAuthorizationClientID(t *testing.T) {
	cases := []struct {
		name     string
		clientID []string
		wantOK   bool
	}{
		{name: "straza", clientID: []string{"straza"}, wantOK: true},
		{name: "strazactl", clientID: []string{"strazactl"}, wantOK: true},
		{name: "console", clientID: []string{"console"}, wantOK: true},
		{name: "unknown id", clientID: []string{"other-client"}},
		{name: "a known id with different case", clientID: []string{"Straza"}},
		{name: "a repeated client_id", clientID: []string{"straza", "straza"}},
		{name: "a known id beside an unknown one", clientID: []string{"straza", "other-client"}},
		{name: "a 64 KiB id", clientID: []string{strings.Repeat("s", 64<<10)}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newIssuerFixture(t)
			var hooks int
			f.issuer.OnLoginFailed = func(string, string, *http.Request) { hooks++ }
			f.issuer.OnNHIGrantFailed = func(string, string, string, *http.Request) { hooks++ }
			code, body := f.postForm(t, "/oidc/device_authorization", url.Values{"client_id": tc.clientID})
			if tc.wantOK {
				if code != http.StatusOK || body["device_code"] == nil || f.grantCount() != 1 {
					t.Fatalf("start = %d %v with %d grants, want 200 and one grant", code, body, f.grantCount())
				}
				return
			}
			if code != http.StatusBadRequest || body["error"] != "invalid_client" || body["error_description"] != wantClientRefused {
				t.Errorf("start = %d %v, want 400 invalid_client with the sentence", code, body)
			}
			if n := f.grantCount(); n != 0 {
				t.Errorf("a refused start left %d grants, want none", n)
			}
			if hooks != 0 {
				t.Errorf("a refused start fired %d login hooks, want none, because no credential was judged", hooks)
			}
		})
	}
}

// TestDeviceAuthorizationDoesNotPinTheBody pins that a grant keeps only its
// own short client_id. Go's form parser hands out values that share memory
// with the whole body, so a grant that kept that value would hold every
// byte of padding sent beside it for the grant's lifetime.
func TestDeviceAuthorizationDoesNotPinTheBody(t *testing.T) {
	const (
		starts  = 200
		padding = 256 << 10
		budget  = 5 << 20 // starts*padding is 50 MiB when the body stays pinned
	)
	f := newIssuerFixture(t)
	body := "client_id=straza&pad=" + strings.Repeat("x", padding)
	client := f.srv.Client()

	before := liveHeap()
	for i := range starts {
		resp, err := client.Post(f.srv.URL+"/oidc/device_authorization", "application/x-www-form-urlencoded", strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("start %d = %d, want 200", i+1, resp.StatusCode)
		}
	}
	growth := liveHeap() - before
	if n := f.grantCount(); n != starts {
		t.Fatalf("the issuer holds %d grants, want %d, so the heap reading measures nothing", n, starts)
	}
	t.Logf("%d grants grew the live heap by %d bytes", starts, growth)
	if growth >= budget {
		t.Errorf("%d grants grew the live heap by %d bytes, want under %d: a grant pins its request body", starts, growth, budget)
	}
	runtime.KeepAlive(f)
}

// liveHeap reads the bytes the heap holds after two full collections.
func liveHeap() int64 {
	var m runtime.MemStats
	runtime.GC()
	runtime.GC()
	runtime.ReadMemStats(&m)
	return int64(m.HeapAlloc)
}

// TestDeviceAuthorizationGrantCap pins the bound on waiting grants: a full
// table refuses a new start with 503, Retry-After and the sentence, and
// keeps every grant it holds. An expired grant makes room again.
func TestDeviceAuthorizationGrantCap(t *testing.T) {
	f := newIssuerFixture(t)
	var first string
	for i := range maxPendingGrants {
		rec := f.startDevice(t)
		if rec.Code != http.StatusOK {
			t.Fatalf("start %d = %d %s, want 200 below the cap", i+1, rec.Code, rec.Body)
		}
		if i == 0 {
			var auth map[string]any
			if err := json.Unmarshal(rec.Body.Bytes(), &auth); err != nil {
				t.Fatal(err)
			}
			first = auth["device_code"].(string)
		}
	}

	cases := []struct {
		name       string
		expire     int // grants to expire before the start
		wantStatus int
	}{
		{name: "a full table refuses a new start", wantStatus: http.StatusServiceUnavailable},
		{name: "it refuses again rather than evict", wantStatus: http.StatusServiceUnavailable},
		{name: "an expired grant makes room again", expire: 1, wantStatus: http.StatusOK},
		{name: "the table is full again after that start", wantStatus: http.StatusServiceUnavailable},
	}
	for _, tc := range cases {
		f.issuer.mu.Lock()
		expired := 0
		for code, g := range f.issuer.grants {
			if expired == tc.expire {
				break
			}
			if code != first {
				g.expiresAt = time.Now().Add(-time.Second)
				expired++
			}
		}
		if tc.expire > 0 {
			// The next start sweeps, as it would a second later.
			f.issuer.lastSweep = time.Time{}
		}
		f.issuer.mu.Unlock()

		rec := f.startDevice(t)
		if rec.Code != tc.wantStatus {
			t.Fatalf("%s: start = %d %s, want %d", tc.name, rec.Code, rec.Body, tc.wantStatus)
		}
		if n := f.grantCount(); n != maxPendingGrants {
			t.Errorf("%s: the issuer holds %d grants, want %d", tc.name, n, maxPendingGrants)
		}
		if tc.wantStatus != http.StatusServiceUnavailable {
			continue
		}
		var body map[string]string
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		if body["error"] != "temporarily_unavailable" || body["error_description"] != wantGrantsFull {
			t.Errorf("%s: refusal body = %v, want temporarily_unavailable with the sentence", tc.name, body)
		}
		if rec.Header().Get("Retry-After") == "" {
			t.Errorf("%s: refusal carries no Retry-After", tc.name)
		}
	}

	// The oldest grant survived every refusal and still answers its poll.
	if code, body := f.pollToken(t, first, "straza"); code != http.StatusBadRequest || body["error"] != "authorization_pending" {
		t.Fatalf("the first grant's poll = %d %v, want authorization_pending", code, body)
	}
}

// TestDeviceStartLimitCannotFillGrants pins the arithmetic between the
// per-address limit on device sign-in starts and the grant cap: one address
// at the default server.loginPerIPRPS, with its full burst, cannot fill the
// table within one grant lifetime. Four IPv4 addresses, or one IPv6 /64
// that rotates its addresses, can, and while they keep starting, new device
// sign-ins are refused, the browser sign-in keeps working and memory stays
// bounded.
func TestDeviceStartLimitCannotFillGrants(t *testing.T) {
	cfg, err := config.Loader{Getenv: func(string) string { return "" }}.Load()
	if err != nil {
		t.Fatal(err)
	}
	rps := cfg.Server.LoginPerIPRPS
	if rps <= 0 {
		t.Fatalf("default loginPerIPRPS = %v, want a limit that is on", rps)
	}
	ttl := NewIssuer(nil, nil, "").grantTTL
	if most := math.Max(rps, 1) + rps*ttl.Seconds(); most >= maxPendingGrants {
		t.Errorf("one address can start %.0f grants in %s, want fewer than %d", most, ttl, maxPendingGrants)
	}
}

// TestExpiredUnsweptIsRefused pins that no reader relies on the sweep: a
// device grant or an authorization code past its expiry, still in memory
// because no sweep ran since, is refused on every path that reads it.
func TestExpiredUnsweptIsRefused(t *testing.T) {
	past := time.Now().Add(-time.Second)
	cases := []struct {
		name string
		read func(t *testing.T, f *issuerFixture)
	}{
		{name: "the device token poll", read: func(t *testing.T, f *issuerFixture) {
			_, auth := f.postForm(t, "/oidc/device_authorization", url.Values{"client_id": {"straza"}})
			dc := auth["device_code"].(string)
			f.expireUnswept(func() { f.issuer.grants[dc].expiresAt = past })
			if code, body := f.pollToken(t, dc, "straza"); code != http.StatusBadRequest || body["error"] != "expired_token" {
				t.Errorf("poll = %d %v, want 400 expired_token", code, body)
			}
		}},
		{name: "the device password submit", read: func(t *testing.T, f *issuerFixture) {
			_, auth := f.postForm(t, "/oidc/device_authorization", url.Values{"client_id": {"straza"}})
			dc := auth["device_code"].(string)
			f.expireUnswept(func() { f.issuer.grants[dc].expiresAt = past })
			page := f.submitLogin(t, auth["user_code"].(string), "kim", "hunter2!")
			f.issuer.mu.Lock()
			status := f.issuer.grants[dc].status
			f.issuer.mu.Unlock()
			if !strings.Contains(page, "That code is not valid or has expired") || status != "pending" {
				t.Errorf("submit page = %s with the grant %s, want the code refused and the grant pending", page, status)
			}
		}},
		{name: "the authorization code redemption", read: func(t *testing.T, f *issuerFixture) {
			verifier, challenge := pkce(t)
			code := f.signIn(t, authzParams(challenge))
			f.expireUnswept(func() { f.issuer.codes[code].expiresAt = past })
			if status, body := f.postForm(t, "/oidc/token", codeForm(code, verifier)); status != http.StatusBadRequest ||
				body["error"] != "invalid_grant" || body["error_description"] != "the code expired. Start the sign-in again" {
				t.Errorf("redeem = %d %v, want 400 invalid_grant for an expired code", status, body)
			}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tc.read(t, newIssuerFixture(t))
		})
	}
}

// expireUnswept runs expire under the issuer's lock and marks a sweep as
// just run, so the expired entry stays in memory for the read that follows.
func (f *issuerFixture) expireUnswept(expire func()) {
	f.issuer.mu.Lock()
	defer f.issuer.mu.Unlock()
	expire()
	f.issuer.lastSweep = time.Now()
}

// TestSweepAtMostOncePerInterval pins that a start sweeps expired grants
// only when sweepEvery has passed since the last sweep, so a flood of
// starts does not walk the whole table under the lock on each one.
func TestSweepAtMostOncePerInterval(t *testing.T) {
	f := newIssuerFixture(t)
	cases := []struct {
		name      string
		rewind    bool // make the last sweep one interval old
		wantCount int  // grants held after the start
	}{
		{name: "the first start sweeps and keeps its own grant", wantCount: 1},
		{name: "a start within the interval leaves the expired grant", wantCount: 2},
		{name: "a start after the interval sweeps both", rewind: true, wantCount: 1},
	}
	for _, tc := range cases {
		f.issuer.mu.Lock()
		for _, g := range f.issuer.grants {
			g.expiresAt = time.Now().Add(-time.Second)
		}
		if tc.rewind {
			f.issuer.lastSweep = f.issuer.lastSweep.Add(-sweepEvery)
		}
		f.issuer.mu.Unlock()
		if rec := f.startDevice(t); rec.Code != http.StatusOK {
			t.Fatalf("%s: start = %d %s", tc.name, rec.Code, rec.Body)
		}
		if n := f.grantCount(); n != tc.wantCount {
			t.Errorf("%s: the issuer holds %d grants, want %d", tc.name, n, tc.wantCount)
		}
	}
}
