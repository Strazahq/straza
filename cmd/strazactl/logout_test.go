package main

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/strazahq/straza/internal/ctl"
)

// logoutServer is the surface `strazactl logout` walks: the refresh its stored
// token needs, and the admin revoke of its own session; plus, when
// selfSessions is set, the self-scoped POST /v1/session/revoke a 0.41.0+
// server offers (without it the mux 404s that path, exactly like an older
// strazad, so the existing tests pin the fallback lane). revokeStatus scripts
// a refusing server.
type logoutServer struct {
	mu           sync.Mutex
	revoked      []string
	revokeStatus int
	statusFor    map[string]int    // per-session override of revokeStatus
	checkinID    string            // session the refresh hands back ("" = ses-1, a refresh in place)
	selfSessions map[string]string // bearer -> session id it names; nil = old server
}

func (s *logoutServer) start(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/checkin", func(w http.ResponseWriter, _ *http.Request) {
		id := s.checkinID
		if id == "" {
			id = "ses-1"
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"session_id":"` + id + `","session_token":"stok-2"}`))
	})
	if s.selfSessions != nil {
		mux.HandleFunc("POST /v1/session/revoke", func(w http.ResponseWriter, r *http.Request) {
			bearer := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
			id, ok := s.selfSessions[bearer]
			w.Header().Set("Content-Type", "application/json")
			if !ok {
				w.WriteHeader(http.StatusUnauthorized)
				_, _ = w.Write([]byte(`{"error":"token rejected"}`))
				return
			}
			s.mu.Lock()
			s.revoked = append(s.revoked, id)
			s.mu.Unlock()
			_, _ = w.Write([]byte(`{"status":"revoked","session":"` + id + `"}`))
		})
	}
	mux.HandleFunc("POST /v1/admin/sessions/{id}/revoke", func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		s.mu.Lock()
		s.revoked = append(s.revoked, id)
		s.mu.Unlock()
		status := s.revokeStatus
		if v, ok := s.statusFor[id]; ok {
			status = v
		}
		w.Header().Set("Content-Type", "application/json")
		if status != 0 && status != http.StatusOK {
			w.WriteHeader(status)
			_, _ = w.Write([]byte(`{"error":"requires role straza-admin"}`))
			return
		}
		_, _ = w.Write([]byte(`{"status":"revoked"}`))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func (s *logoutServer) seen() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.revoked...)
}

// TestLogoutRevokesThenDeletes pins the happy path: both halves reported, one
// line each, and the credentials file is gone.
func TestLogoutRevokesThenDeletes(t *testing.T) {
	s := &logoutServer{}
	srv := s.start(t)
	t.Setenv("STRAZA_SERVER", "")
	credsPath := writeCreds(t, srv.URL)

	stdout, stderr, err := runCLI(t, credsPath, "logout")
	if err != nil {
		t.Fatalf("logout: %v", err)
	}
	if got := s.seen(); len(got) != 1 || got[0] != "ses-1" {
		t.Errorf("server revoked %v, want [ses-1]", got)
	}
	for _, want := range []string{"revoked session ses-1", "deleted " + credsPath} {
		if !strings.Contains(stdout, want) {
			t.Errorf("stdout missing %q:\n%s", want, stdout)
		}
	}
	if strings.Contains(stderr, "warning:") {
		t.Errorf("a clean logout must not warn:\n%s", stderr)
	}
	if _, err := os.Stat(credsPath); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("credentials survived logout: %v", err)
	}
}

// TestLogoutSelfRouteNeedsNoAdmin pins the self-scoped revoke end to end at
// the CLI: against a server with that route, a NON-admin's logout revokes
// server-side (the stale stored token reauths in place and the retried self
// revoke ends the session the fresh token names), stdout reads exactly like
// an admin's, and stderr stays silent, so there is no warning to teach
// operators to ignore.
func TestLogoutSelfRouteNeedsNoAdmin(t *testing.T) {
	// The admin route refuses (this caller holds no straza-admin); with the
	// self route present, logout must never need it.
	s := &logoutServer{
		revokeStatus: http.StatusForbidden,
		selfSessions: map[string]string{"stok-2": "ses-1"},
	}
	srv := s.start(t)
	t.Setenv("STRAZA_SERVER", "")
	credsPath := writeCreds(t, srv.URL)

	stdout, stderr, err := runCLI(t, credsPath, "logout")
	if err != nil {
		t.Fatalf("logout: %v", err)
	}
	if got := s.seen(); len(got) != 1 || got[0] != "ses-1" {
		t.Errorf("server revoked %v, want [ses-1] via the self route", got)
	}
	for _, want := range []string{"revoked session ses-1", "deleted " + credsPath} {
		if !strings.Contains(stdout, want) {
			t.Errorf("stdout missing %q:\n%s", want, stdout)
		}
	}
	if strings.Contains(stderr, "warning:") {
		t.Errorf("a clean self-revoke logout must not warn:\n%s", stderr)
	}
	if _, err := os.Stat(credsPath); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("credentials survived logout: %v", err)
	}
}

// TestLogoutServerRefusalStillDeletes: the local half is the promise. A server
// that refuses (no admin role) or is unreachable costs the operator their
// credentials anyway: loudly, on stderr, and still exit 0, because the thing
// `logout` guarantees (no credential on this machine) DID happen.
func TestLogoutServerRefusalStillDeletes(t *testing.T) {
	s := &logoutServer{revokeStatus: http.StatusForbidden}
	srv := s.start(t)
	t.Setenv("STRAZA_SERVER", "")
	credsPath := writeCreds(t, srv.URL)

	stdout, stderr, err := runCLI(t, credsPath, "logout")
	if err != nil {
		t.Fatalf("logout must not fail on the server half: %v", err)
	}
	if !strings.Contains(stderr, "warning:") || !strings.Contains(stderr, "requires role straza-admin") {
		t.Errorf("stderr must name the failed half:\n%s", stderr)
	}
	if !strings.Contains(stderr, "session ses-1 not revoked") || !strings.Contains(stderr, "until it expires") {
		t.Errorf("stderr must say which session may still be valid:\n%s", stderr)
	}
	if !strings.Contains(stdout, "deleted "+credsPath) {
		t.Errorf("stdout missing the local half:\n%s", stdout)
	}
	if _, err := os.Stat(credsPath); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("credentials survived logout: %v", err)
	}
}

// TestLogoutNamesEverySessionLeftBehind pins the case where the stored token
// is stale by logout time, which a 300 s TTL makes the usual case. The refresh
// re-establishes through the device credential and MINTS a second session,
// and for any caller without straza-admin BOTH revokes are refused. The
// warning names both ids, the minted one especially because logout created
// it, and the credentials still go.
func TestLogoutNamesEverySessionLeftBehind(t *testing.T) {
	s := &logoutServer{revokeStatus: http.StatusForbidden, checkinID: "ses-2"}
	srv := s.start(t)
	t.Setenv("STRAZA_SERVER", "")
	credsPath := writeCreds(t, srv.URL)

	stdout, stderr, err := runCLI(t, credsPath, "logout")
	if err != nil {
		t.Fatalf("logout must not fail on the server half: %v", err)
	}
	if got := s.seen(); len(got) != 2 || got[0] != "ses-1" || got[1] != "ses-2" {
		t.Fatalf("server asked about %v, want [ses-1 ses-2] (the minted session was abandoned)", got)
	}
	if !strings.Contains(stderr, "sessions ses-1, ses-2 not revoked") {
		t.Errorf("stderr must name both leftovers:\n%s", stderr)
	}
	if !strings.Contains(stderr, "they may still be valid") || !strings.Contains(stderr, "until they expire") {
		t.Errorf("stderr must read as plural:\n%s", stderr)
	}
	if strings.Contains(stdout, "revoked session") {
		t.Errorf("nothing was revoked; stdout must not claim otherwise:\n%s", stdout)
	}
	if _, err := os.Stat(credsPath); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("credentials survived logout: %v", err)
	}
}

// TestLogoutReportsEachSessionsFate: one logout, two sessions, two outcomes;
// the operator is told exactly which id went and which id did not, in either
// direction.
func TestLogoutReportsEachSessionsFate(t *testing.T) {
	tests := []struct {
		name          string
		statusFor     map[string]int
		wantRevoked   string // stdout fragment
		wantUnrevoked string // stderr fragment
	}{
		{
			name:          "the stored session resists, the minted one goes",
			statusFor:     map[string]int{"ses-1": http.StatusInternalServerError},
			wantRevoked:   "revoked session ses-2",
			wantUnrevoked: "session ses-1 not revoked",
		},
		{
			name:          "the stored session goes, the minted one resists",
			statusFor:     map[string]int{"ses-2": http.StatusForbidden},
			wantRevoked:   "revoked session ses-1",
			wantUnrevoked: "session ses-2 not revoked",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := &logoutServer{statusFor: tc.statusFor, checkinID: "ses-2"}
			srv := s.start(t)
			t.Setenv("STRAZA_SERVER", "")
			credsPath := writeCreds(t, srv.URL)

			stdout, stderr, err := runCLI(t, credsPath, "logout")
			if err != nil {
				t.Fatalf("logout: %v", err)
			}
			if !strings.Contains(stdout, tc.wantRevoked) {
				t.Errorf("stdout missing %q:\n%s", tc.wantRevoked, stdout)
			}
			if !strings.Contains(stderr, tc.wantUnrevoked) {
				t.Errorf("stderr missing %q:\n%s", tc.wantUnrevoked, stderr)
			}
			if _, err := os.Stat(credsPath); !errors.Is(err, os.ErrNotExist) {
				t.Errorf("credentials survived logout: %v", err)
			}
		})
	}
}

// TestLogoutWithoutCredentialsIsLoud: nothing to log out of is an error naming
// the path, never the generic "not logged in and no server given", whose
// advice (run `login`) is the opposite of what the operator asked for.
func TestLogoutWithoutCredentialsIsLoud(t *testing.T) {
	t.Setenv("STRAZA_SERVER", "")
	_, _, err := runCLI(t, noCreds(t), "logout")
	if err == nil {
		t.Fatal("want an error when there is no login")
	}
	if errors.Is(err, ctl.ErrNoServer) {
		t.Fatalf("logout reported the resolution error: %v", err)
	}
	if !strings.Contains(err.Error(), "not logged in") || !strings.Contains(err.Error(), "credentials.json") {
		t.Fatalf("error = %q, want it to name the missing credentials file", err.Error())
	}
}

// TestLogoutRefusesForeignTarget: the only session logout can end is the one
// in the credentials file, so an override aimed somewhere else is refused
// outright (with the credentials left intact) instead of firing a revoke at
// a deployment the stored token means nothing to.
func TestLogoutRefusesForeignTarget(t *testing.T) {
	tests := []struct {
		name     string
		flagArgs []string
		env      string
		wantRun  bool // logout proceeds
		wantSaid string
	}{
		{
			name: "no override logs out of the stored server", wantRun: true,
		},
		{
			name: "--server naming the login is not foreign",
			// filled in per-test with the live server URL
			flagArgs: []string{"--server", "%s"}, wantRun: true,
		},
		{
			name:     "--server elsewhere is refused",
			flagArgs: []string{"--server", "https://other.example"},
			wantSaid: "--server targets https://other.example",
		},
		{
			name:     "$STRAZA_SERVER elsewhere is refused",
			env:      "https://other.example",
			wantSaid: "$STRAZA_SERVER targets https://other.example",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := &logoutServer{}
			srv := s.start(t)
			t.Setenv("STRAZA_SERVER", tc.env)
			credsPath := writeCreds(t, srv.URL)
			args := make([]string, 0, len(tc.flagArgs)+1)
			for _, a := range tc.flagArgs {
				args = append(args, strings.ReplaceAll(a, "%s", srv.URL))
			}
			args = append(args, "logout")

			_, stderr, err := runCLI(t, credsPath, args...)
			if tc.wantRun {
				if err != nil {
					t.Fatalf("logout: %v", err)
				}
				if got := s.seen(); len(got) != 1 {
					t.Errorf("server revoked %v, want the stored session", got)
				}
				return
			}
			if err == nil {
				t.Fatal("want a refusal")
			}
			if !strings.Contains(err.Error(), tc.wantSaid) {
				t.Errorf("error = %q, want it to name %q", err.Error(), tc.wantSaid)
			}
			if got := s.seen(); len(got) != 0 {
				t.Errorf("a refused logout still called the server: %v", got)
			}
			if _, statErr := os.Stat(credsPath); statErr != nil {
				t.Errorf("a refused logout deleted the credentials: %v", statErr)
			}
			if strings.Contains(stderr, "warning:") {
				t.Errorf("the refusal IS the message; no generic override warning too:\n%s", stderr)
			}
		})
	}
}

// TestLogoutTakesNoOperands: `strazactl logout <anything>` is a typo, not a
// per-session logout. A stray argument is an error and is never ignored.
func TestLogoutTakesNoOperands(t *testing.T) {
	s := &logoutServer{}
	srv := s.start(t)
	t.Setenv("STRAZA_SERVER", "")
	credsPath := writeCreds(t, srv.URL)

	if _, _, err := runCLI(t, credsPath, "logout", "ses-9"); err == nil {
		t.Fatal("want an error for a stray operand")
	}
	if _, err := os.Stat(credsPath); err != nil {
		t.Errorf("a rejected invocation deleted the credentials: %v", err)
	}
}
