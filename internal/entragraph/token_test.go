package entragraph

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"
)

// emptyMembers is the script of one member read of an empty role group.
func emptyMembers() step {
	return step{method: http.MethodGet, path: "/v1.0/groups/" + roleGroup + "/members", status: http.StatusOK, body: `{"value":[]}`}
}

func TestNewOptions(t *testing.T) {
	signer := &fakeSigner{}
	tests := []struct {
		name string
		o    Options
		want string
	}{
		{"no tenant", Options{ClientID: testClientID, Signer: signer}, "identity.entra.tenant"},
		{"no client", Options{Tenant: testTenant, Signer: signer}, "identity.entra.clientId"},
		{"no signer", Options{Tenant: testTenant, ClientID: testClientID}, "signer"},
		{"relative Graph endpoint", Options{Tenant: testTenant, ClientID: testClientID, Signer: signer, graphBase: "/graph"}, "not an absolute"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := New(tt.o)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Errorf("New() err = %v, want one holding %q", err, tt.want)
			}
		})
	}

	c, err := New(Options{Tenant: testTenant, ClientID: testClientID, Signer: signer})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if want := "https://login.microsoftonline.com/" + testTenant + "/oauth2/v2.0/token"; c.tokenURL != want {
		t.Errorf("tokenURL = %q, want %q", c.tokenURL, want)
	}
	if c.base != "https://graph.microsoft.com" || c.http.Timeout != 30*time.Second {
		t.Errorf("base = %q, timeout = %v, want the Graph endpoint and 30s", c.base, c.http.Timeout)
	}
}

func TestTokenRequest(t *testing.T) {
	f := newFixture(t, emptyMembers())
	if _, err := f.client.Members(context.Background(), roleGroup); err != nil {
		t.Fatalf("Members: %s", f.show(err))
	}
	f.done()
	tokenURL := f.login.URL + "/" + testTenant + "/oauth2/v2.0/token"
	if f.signer.clientID != testClientID || f.signer.audience != tokenURL {
		t.Errorf("signed for %q with audience %q, want %q and %q", f.signer.clientID, f.signer.audience, testClientID, tokenURL)
	}
	if got := f.tokenRequests(); got != 1 {
		t.Fatalf("token requests = %d, want 1", got)
	}
	form := f.form(0)
	want := map[string]string{
		"grant_type":            "client_credentials",
		"client_id":             testClientID,
		"client_assertion_type": "urn:ietf:params:oauth:client-assertion-type:jwt-bearer",
		"scope":                 "https://graph.microsoft.com/.default",
	}
	for k, v := range want {
		if got := form.Get(k); got != v {
			t.Errorf("form %s = %q, want %q", k, got, v)
		}
	}
	if form.Get("client_assertion") != testAssertion {
		t.Errorf("the form does not carry the signer's assertion")
	}
}

func TestTokenCache(t *testing.T) {
	const lifetime = 3599 * time.Second
	tests := []struct {
		name string
		// gaps is the time that passes before each call, and want the token
		// requests counted after it.
		gaps []time.Duration
		want []int
	}{
		{"two calls share one token", []time.Duration{0, 0}, []int{1, 1}},
		{"kept until 61 s before expiry", []time.Duration{0, lifetime - 61*time.Second}, []int{1, 1}},
		{"renewed 60 s before expiry", []time.Duration{0, lifetime - 60*time.Second}, []int{1, 2}},
		{"renewed after expiry, then kept", []time.Duration{0, 2 * time.Hour, time.Minute}, []int{1, 2, 2}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			steps := make([]step, len(tt.gaps))
			for i := range steps {
				steps[i] = emptyMembers()
			}
			f := newFixture(t, steps...)
			for i, gap := range tt.gaps {
				f.clock.add(gap)
				if _, err := f.client.Members(context.Background(), roleGroup); err != nil {
					t.Fatalf("call %d: %s", i+1, f.show(err))
				}
				if got := f.tokenRequests(); got != tt.want[i] {
					t.Errorf("after call %d token requests = %d, want %d", i+1, got, tt.want[i])
				}
			}
			f.done()
		})
	}
}

func TestTokenCacheConcurrent(t *testing.T) {
	const calls = 8
	steps := make([]step, calls)
	for i := range steps {
		steps[i] = emptyMembers()
	}
	f := newFixture(t, steps...)
	var wg sync.WaitGroup
	for range calls {
		wg.Go(func() {
			if _, err := f.client.Members(context.Background(), roleGroup); err != nil {
				t.Errorf("Members: %s", f.show(err))
			}
		})
	}
	wg.Wait()
	if got := f.tokenRequests(); got != 1 {
		t.Errorf("token requests = %d, want 1", got)
	}
	f.done()
}

func TestTokenRefused(t *testing.T) {
	traced := `{"error":"invalid_request","error_codes":[90002],"trace_id":"DDDDDDDD-4444-4444-8444-DDDDDDDDDDDD"}`
	tests := []struct {
		name   string
		status int
		body   string
		want   []string
	}{
		{"no certificate", http.StatusUnauthorized, "@token-700027.json",
			[]string{"AADSTS700027", testKID, "strazactl signing-keys certificate client_assertion " + testKID}},
		{"no such app", http.StatusBadRequest, `{"error":"unauthorized_client","error_codes":[700016]}`,
			[]string{"AADSTS700016", testClientID, testTenant, "identity.entra.clientId"}},
		{"app disabled", http.StatusBadRequest, `{"error":"invalid_client","error_codes":[7000112]}`,
			[]string{"AADSTS7000112", "disabled"}},
		{"another code", http.StatusBadRequest, traced,
			[]string{"invalid_request (AADSTS90002)", "trace id dddddddd-4444-4444-8444-dddddddddddd"}},
		{"a code Straza does not repeat", http.StatusBadRequest, `{"error":"Bad Thing <b>","trace_id":"not a guid"}`,
			[]string{"an error code Straza does not repeat", "by the time of the request"}},
		{"no OAuth error", http.StatusInternalServerError, "<html>down</html>", []string{"HTTP 500 and no OAuth error"}},
		{"not JSON", http.StatusOK, "<html>ok</html>", []string{"JSON of RFC 6749"}},
		{"no access token", http.StatusOK, `{"token_type":"Bearer","expires_in":3599}`, []string{"without an access token"}},
		{"not a Bearer token", http.StatusOK, `{"access_token":"x.y.z","token_type":"pop","expires_in":3599}`, []string{"not a Bearer token"}},
		{"no lifetime", http.StatusOK, `{"access_token":"x.y.z","token_type":"Bearer","expires_in":0}`, []string{"names no lifetime"}},
		{"redirect", http.StatusFound, "", []string{"redirect"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFixture(t)
			f.answerTokens(func(int) (int, string) { return tt.status, tt.body })
			members, err := f.client.Members(context.Background(), roleGroup)
			f.wantError(err, tt.want...)
			if members != nil || len(f.requests()) != 0 {
				t.Errorf("members = %v and %d Graph requests, want none", members, len(f.requests()))
			}
			f.done()
		})
	}
}

func TestTokenWithoutOID(t *testing.T) {
	tests := []struct {
		name  string
		token string
	}{
		{"no oid claim", jwtOf(map[string]any{"aud": graphHost, "tid": testTenant})},
		{"empty oid claim", jwtOf(map[string]any{"oid": ""})},
		{"not a JWT", "opaque-token-without-claims"},
		{"payload not base64", "eyJhbGciOiJub25lIn0.!!!.c2ln"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFixture(t)
			f.answerTokens(func(int) (int, string) {
				return http.StatusOK, `{"access_token":"` + tt.token + `","token_type":"Bearer","expires_in":3599}`
			}, tt.token)
			_, err := f.client.EnsureGroup(context.Background(), GroupSpec{UniqueName: uniqueName, DisplayName: "Straza: readers"})
			f.wantError(err, "carries no oid claim", testClientID, "identity.entra.tenant")
			if len(f.requests()) != 0 {
				t.Errorf("Graph got %d requests, want none", len(f.requests()))
			}
			f.done()
		})
	}
}

func TestSignerRefused(t *testing.T) {
	staged := errors.New("authn: the client assertion key k1 is staged and waits for its promotion")
	f := newFixture(t)
	f.signer.err = staged
	err := f.client.DeleteGroup(context.Background(), roleGroup)
	f.wantError(err, "could not sign the client assertion", testClientID, "strazactl signing-keys list")
	if !errors.Is(err, staged) {
		t.Errorf("errors.Is(err, the signer's error) = false, want true")
	}
	if f.tokenRequests() != 0 || len(f.requests()) != 0 {
		t.Errorf("token requests = %d, Graph requests = %d, want none", f.tokenRequests(), len(f.requests()))
	}
	f.done()
}
