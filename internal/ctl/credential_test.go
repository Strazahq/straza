package ctl

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// TestCodingAgentMarker pins the marker lookup: each harness marker on its
// own is seen, the list order decides which one a refusal names, and an
// empty value counts as unset.
func TestCodingAgentMarker(t *testing.T) {
	tests := []struct {
		name string
		env  map[string]string
		want string
	}{
		{name: "no marker", env: map[string]string{"HOME": "/home/x"}, want: ""},
		{name: "Claude Code", env: map[string]string{"CLAUDECODE": "1"}, want: "CLAUDECODE"},
		{name: "Claude Code entry point alone", env: map[string]string{"CLAUDE_CODE_ENTRYPOINT": "cli"}, want: "CLAUDE_CODE_ENTRYPOINT"},
		{name: "Codex from npm", env: map[string]string{"CODEX_MANAGED_PACKAGE_ROOT": "/usr/lib/node_modules/@openai/codex"}, want: "CODEX_MANAGED_PACKAGE_ROOT"},
		{name: "Codex from an older npm launcher", env: map[string]string{"CODEX_MANAGED_BY_NPM": "1"}, want: "CODEX_MANAGED_BY_NPM"},
		{name: "Gemini CLI", env: map[string]string{"GEMINI_CLI": "1"}, want: "GEMINI_CLI"},
		{name: "the list order names the first", env: map[string]string{"GEMINI_CLI": "1", "CLAUDECODE": "1"}, want: "CLAUDECODE"},
		{name: "an empty value is unset", env: map[string]string{"CLAUDECODE": "", "CLAUDE_CODE_ENTRYPOINT": ""}, want: ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			getenv := func(k string) string { return tc.env[k] }
			if got := CodingAgentMarker(getenv); got != tc.want {
				t.Errorf("CodingAgentMarker = %q, want %q", got, tc.want)
			}
		})
	}
}

// bearerServer answers every request with 200 and records the method, the
// path and the bearer of each, check-ins included.
type bearerServer struct {
	mu   sync.Mutex
	seen []string
}

func (s *bearerServer) start(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		s.seen = append(s.seen, r.Method+" "+r.URL.Path+" "+r.Header.Get("Authorization"))
		s.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{}`))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func (s *bearerServer) requests() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.seen...)
}

// wrapperCall runs one of the three authenticated wrappers.
type wrapperCall struct {
	name string
	call func(c *Client, method, path string) error
}

var wrapperCalls = []wrapperCall{
	{"Do", func(c *Client, method, path string) error {
		return c.Do(context.Background(), method, path, nil, nil)
	}},
	{"DoBytes", func(c *Client, method, path string) error {
		_, err := c.DoBytes(context.Background(), method, path)
		return err
	}},
	{"DoRawBody", func(c *Client, method, path string) error {
		return c.DoRawBody(context.Background(), method, path, "application/yaml", []byte("kind: App\n"), nil)
	}},
}

// TestAgentGuardRefusesWritesOnTheLogin pins the coding-agent guard: inside
// an agent, every call but a GET that would go out on the stored login is
// refused before anything is sent, check-in included, and the refusal names
// the marker, the person the agent would act as and the two ways out. A GET
// goes out on the login as before.
func TestAgentGuardRefusesWritesOnTheLogin(t *testing.T) {
	methods := []struct {
		method  string
		refused bool
	}{
		{http.MethodGet, false},
		{http.MethodPost, true},
		{http.MethodPut, true},
		{http.MethodPatch, true},
		{http.MethodDelete, true},
	}
	for _, w := range wrapperCalls {
		for _, m := range methods {
			t.Run(w.name+" "+m.method, func(t *testing.T) {
				s := &bearerServer{}
				c := loggedInClient(t, s.start(t).URL)
				c.AgentMarker = "CLAUDECODE"
				err := w.call(c, m.method, "/v1/admin/roles")
				if !m.refused {
					if err != nil {
						t.Fatalf("a read inside an agent was refused: %v", err)
					}
					if got := s.requests(); len(got) != 1 || !strings.HasPrefix(got[0], "GET /v1/admin/roles Bearer ") {
						t.Errorf("requests = %q, want the one GET on the login", got)
					}
					return
				}
				if err == nil {
					t.Fatal("a write on the login inside an agent went through")
				}
				for _, want := range []string{
					"CLAUDECODE is set, so strazactl runs inside a coding agent",
					"the agent would act as the person who logged in, so nothing was sent",
					"For automation, use an admin API token in STRAZA_API_TOKEN",
					"An agent proposes config changes through the built-in straza MCP server's drafting tools",
				} {
					if !strings.Contains(err.Error(), want) {
						t.Errorf("refusal lacks %q:\n%v", want, err)
					}
				}
				if got := s.requests(); len(got) != 0 {
					t.Errorf("the refused call reached the server: %q", got)
				}
			})
		}
	}
}

// TestGuardLetsChangeFreeCallsOut pins the change-free list: inside a coding
// agent the listed calls, policy simulate and drafts check, go out on the
// login, and a call that differs from one in method or path is refused
// before it is sent. That covers the apply and activate that store and
// publish a set, and every drafts call that stores, publishes, discards or
// reverts a draft.
func TestGuardLetsChangeFreeCallsOut(t *testing.T) {
	tests := []struct {
		method, path string
		refused      bool
	}{
		{http.MethodPost, "/v1/admin/policies/simulate", false},
		{http.MethodPut, "/v1/admin/policies/simulate", true},
		{http.MethodPost, "/v1/admin/policies/simulate?draft=1", true},
		{http.MethodPost, "/v1/admin/policies/validate", true},
		{http.MethodPut, "/v1/admin/policies", true},
		{http.MethodPost, "/v1/admin/policies/set-a/activate", true},
		{http.MethodPost, "/v1/admin/drafts/check", false},
		{http.MethodPost, "/v1/admin/drafts/check/", true},
		{http.MethodPut, "/v1/admin/drafts/check", true},
		{http.MethodPost, "/v1/admin/drafts", true},
		{http.MethodPut, "/v1/admin/drafts/41", true},
		{http.MethodPost, "/v1/admin/drafts/41/publish", true},
		{http.MethodPost, "/v1/admin/drafts/41/discard", true},
		{http.MethodPost, "/v1/admin/drafts/41/revert", true},
	}
	for _, tc := range tests {
		t.Run(tc.method+" "+tc.path, func(t *testing.T) {
			s := &bearerServer{}
			c := loggedInClient(t, s.start(t).URL)
			c.AgentMarker = "CLAUDECODE"
			err := c.Do(context.Background(), tc.method, tc.path, nil, nil)
			got := s.requests()
			switch {
			case tc.refused && (err == nil || len(got) != 0):
				t.Errorf("err = %v, requests = %q, want a refusal before anything is sent", err, got)
			case !tc.refused && (err != nil || len(got) != 1):
				t.Errorf("err = %v, requests = %q, want the one call on the login", err, got)
			}
		})
	}
}

// TestGuardChange pins the check a command runs before a read that leads
// to a change: on the login inside a coding agent it answers the guard's
// refusal, and on a token inside one, or outside one, it lets the command
// go on.
func TestGuardChange(t *testing.T) {
	tests := []struct {
		name, token, marker string
		refused             bool
	}{
		{name: "the login inside a coding agent", marker: "CLAUDECODE", refused: true},
		{name: "a token inside a coding agent", token: "wat_abc", marker: "CLAUDECODE"},
		{name: "the login outside a coding agent"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c := NewClient("http://127.0.0.1:1")
			c.APIToken, c.AgentMarker = tc.token, tc.marker
			err := c.GuardChange()
			switch {
			case tc.refused && (err == nil || err.Error() != agentGuardError("CLAUDECODE").Error()):
				t.Errorf("err = %v, want the guard's refusal", err)
			case !tc.refused && err != nil:
				t.Errorf("err = %v, want none", err)
			}
		})
	}
}

// TestAPITokenReplacesTheLogin pins the token path: every admin call sends
// the token as the bearer, writes inside a coding agent included, without
// reading the credentials file or checking in. A refused token is final, so
// no renewal is tried, and a call outside the admin API is refused before
// the token leaves the machine.
func TestAPITokenReplacesTheLogin(t *testing.T) {
	for _, w := range wrapperCalls {
		t.Run(w.name+" sends the token inside an agent", func(t *testing.T) {
			s := &bearerServer{}
			c := NewClient(s.start(t).URL)
			c.CredsPath = filepath.Join(t.TempDir(), "no-login.json")
			c.APIToken, c.AgentMarker = "wat_abc", "CLAUDECODE"
			if err := w.call(c, http.MethodPost, "/v1/admin/roles"); err != nil {
				t.Fatalf("token write: %v", err)
			}
			want := []string{"POST /v1/admin/roles Bearer wat_abc"}
			if got := s.requests(); strings.Join(got, "|") != strings.Join(want, "|") {
				t.Errorf("requests = %q, want %q", got, want)
			}
		})
		t.Run(w.name+" stops at a refused token", func(t *testing.T) {
			var calls atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, _ *http.Request) {
				calls.Add(1)
				rw.Header().Set("Content-Type", "application/json")
				rw.WriteHeader(http.StatusUnauthorized)
				_, _ = rw.Write([]byte(`{"error":"admin API token rejected: unknown, expired or revoked (strazactl api-token list)"}`))
			}))
			t.Cleanup(srv.Close)
			c := loggedInClient(t, srv.URL)
			c.APIToken = "wat_dead"
			err := w.call(c, http.MethodGet, "/v1/admin/roles")
			if err == nil || err.Error() != "admin API token rejected: unknown, expired or revoked (strazactl api-token list)" {
				t.Fatalf("err = %v, want the server's sentence", err)
			}
			if n := calls.Load(); n != 1 {
				t.Errorf("requests = %d, want 1 and no renewal", n)
			}
		})
		t.Run(w.name+" keeps the token off person routes", func(t *testing.T) {
			s := &bearerServer{}
			c := loggedInClient(t, s.start(t).URL)
			c.APIToken = "wat_abc"
			err := w.call(c, http.MethodPost, "/v1/connect/github")
			if !errors.Is(err, errTokenOffAdmin) {
				t.Fatalf("err = %v, want errTokenOffAdmin", err)
			}
			if got := s.requests(); len(got) != 0 {
				t.Errorf("the token went out on a person route: %q", got)
			}
		})
	}
}

// TestPolicyByNameSendsTheToken pins the by-name policy read, which keeps
// the status code and so has its own call site, to the same credential as
// every other admin call.
func TestPolicyByNameSendsTheToken(t *testing.T) {
	s := &bearerServer{}
	c := loggedInClient(t, s.start(t).URL)
	c.APIToken = "wat_abc"
	if _, err := c.PolicyByName(context.Background(), "set-a"); err != nil {
		t.Fatalf("policy read on a token: %v", err)
	}
	want := []string{"GET /v1/admin/policies/set-a Bearer wat_abc"}
	if got := s.requests(); strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("requests = %q, want %q", got, want)
	}
}

// TestLogoutEndsTheLoginAlone pins logout under a token and inside a coding
// agent: the revoke goes out on the stored session token, never the admin
// API token, the guard does not hold back the admin fallback that a server
// without the self route needs, and the credentials file goes.
func TestLogoutEndsTheLoginAlone(t *testing.T) {
	for _, selfRoute := range []bool{true, false} {
		name := "self route"
		if !selfRoute {
			name = "admin fallback"
		}
		t.Run(name, func(t *testing.T) {
			var mu sync.Mutex
			var seen []string
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				mu.Lock()
				seen = append(seen, r.Method+" "+r.URL.Path+" "+r.Header.Get("Authorization"))
				mu.Unlock()
				w.Header().Set("Content-Type", "application/json")
				if r.URL.Path == "/v1/session/revoke" && !selfRoute {
					w.WriteHeader(http.StatusNotFound)
					return
				}
				_, _ = w.Write([]byte(`{"session":"ses-1"}`))
			}))
			t.Cleanup(srv.Close)
			c := NewClient(srv.URL)
			c.CredsPath = filepath.Join(t.TempDir(), "credentials.json")
			login := sessionJWT(t, "ses-1", time.Now().Add(time.Hour))
			if err := c.saveCreds(credentials{Server: srv.URL, SessionToken: login, SessionID: "ses-1"}); err != nil {
				t.Fatal(err)
			}
			c.APIToken, c.AgentMarker = "wat_abc", "CLAUDECODE"
			res, err := c.Logout(context.Background())
			if err != nil {
				t.Fatalf("logout: %v", err)
			}
			if len(res.Revoked) != 1 || res.Revoked[0] != "ses-1" || res.RevokeErr != nil {
				t.Errorf("result = %+v, want ses-1 revoked", res)
			}
			mu.Lock()
			defer mu.Unlock()
			if len(seen) == 0 {
				t.Fatal("logout sent nothing")
			}
			for _, req := range seen {
				if !strings.HasSuffix(req, "Bearer "+login) {
					t.Errorf("request %q did not go out on the stored session token", req)
				}
			}
			if _, err := os.Stat(c.CredsPath); !errors.Is(err, os.ErrNotExist) {
				t.Errorf("credentials file still there: %v", err)
			}
		})
	}
}
