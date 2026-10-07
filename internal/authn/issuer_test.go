package authn

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
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
			f.issuer.OnLoginFailed = func(username string, r *http.Request) {
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
			f.issuer.OnLoginFailed = func(username string, _ *http.Request) {
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
