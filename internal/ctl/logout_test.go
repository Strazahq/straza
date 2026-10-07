package ctl

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
)

// logoutMock is the surface Logout touches: the admin session revoke, the
// checkin a stale session token refreshes through on its way there, and
// (when selfSessions is set) the self-scoped POST /v1/session/revoke a
// 0.41.0+ server offers. Without selfSessions the mux answers that path with
// Go's plain 404, which is exactly what a pre-0.41.0 strazad does; the
// existing tables therefore double as the old-server fallback pin. It records
// every session id each route was asked to revoke, in order.
type logoutMock struct {
	mu       sync.Mutex
	revoked  []string // admin-route asks
	selfSeen []string // self-route revocations (by the session the token named)

	revokeStatus int            // status for every revoke; 0 = 200 OK
	statusFor    map[string]int // per-session override of revokeStatus (both routes)
	checkinID    string         // session id a refresh hands back ("" = no checkin expected)
	checkinToken string
	selfSessions map[string]string // bearer -> session id it names; nil = no self route (old server)
}

func (m *logoutMock) server(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/admin/sessions/{id}/revoke", func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		m.mu.Lock()
		m.revoked = append(m.revoked, id)
		m.mu.Unlock()
		status := m.revokeStatus
		if s, ok := m.statusFor[id]; ok {
			status = s
		}
		w.Header().Set("Content-Type", "application/json")
		if status != 0 && status != http.StatusOK {
			w.WriteHeader(status)
			_, _ = w.Write([]byte(`{"error":"requires role straza-admin"}`))
			return
		}
		_, _ = w.Write([]byte(`{"status":"revoked"}`))
	})
	if m.selfSessions != nil {
		mux.HandleFunc("POST /v1/session/revoke", func(w http.ResponseWriter, r *http.Request) {
			bearer := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
			id, ok := m.selfSessions[bearer]
			w.Header().Set("Content-Type", "application/json")
			if !ok {
				w.WriteHeader(http.StatusUnauthorized)
				_, _ = w.Write([]byte(`{"error":"token rejected: only a session token can end its own session"}`))
				return
			}
			if status, scripted := m.statusFor[id]; scripted && status != http.StatusOK {
				w.WriteHeader(status)
				_, _ = w.Write([]byte(`{"error":"revoke failed"}`))
				return
			}
			m.mu.Lock()
			m.selfSeen = append(m.selfSeen, id)
			m.mu.Unlock()
			_, _ = w.Write([]byte(`{"status":"revoked","session":"` + id + `"}`))
		})
	}
	mux.HandleFunc("POST /v1/checkin", func(w http.ResponseWriter, _ *http.Request) {
		if m.checkinID == "" {
			t.Errorf("unexpected /v1/checkin: the stored token was still fresh")
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"session_id":"` + m.checkinID + `","session_token":"` + m.checkinToken + `"}`))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func (m *logoutMock) seen() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]string(nil), m.revoked...)
}

func (m *logoutMock) selfRevoked() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]string(nil), m.selfSeen...)
}

// TestLogout is the table for the whole command against a pre-0.41.0 server.
// The mock registers no self route, so every attempt 404s there and falls
// back to the admin revoke, the compatibility lane. The server half is best
// effort (a refusal, a stale token, a credentials file nobody can parse),
// and the local half is the promise: the credentials never survive a logout.
//
// The stale-token rows are the load-bearing ones. Refreshing a dead session
// token re-establishes through the device credential, which MINTS a session
// mid-logout. EVERY outcome of the first revoke, failure included, has to be
// followed by a look at what the credentials say now, or logout walks away
// from the session it just created.
func TestLogout(t *testing.T) {
	fresh := fakeSessionJWT(t)
	tests := []struct {
		name          string
		body          string // credentials JSON, %s = server URL
		revokeStatus  int
		statusFor     map[string]int
		checkinID     string
		wantAttempted []string // session ids the server was asked about, in order
		wantRevoked   []string
		wantUnrevoked []string
	}{
		{
			name:          "fresh token revokes the stored session",
			body:          `{"server":"%s","session_token":"` + fresh + `","session_id":"ses-1"}`,
			wantAttempted: []string{"ses-1"},
			wantRevoked:   []string{"ses-1"},
		},
		{
			name:          "a refusing server still costs the operator their credentials",
			body:          `{"server":"%s","session_token":"` + fresh + `","session_id":"ses-1"}`,
			revokeStatus:  http.StatusForbidden,
			wantAttempted: []string{"ses-1"},
			wantUnrevoked: []string{"ses-1"},
		},
		{
			name:          "a refresh in place revokes exactly once",
			body:          `{"server":"%s","session_token":"stale","session_id":"ses-1"}`,
			checkinID:     "ses-1",
			wantAttempted: []string{"ses-1"},
			wantRevoked:   []string{"ses-1"},
		},
		{
			name:          "a re-established session is revoked too",
			body:          `{"server":"%s","session_token":"stale","session_id":"ses-1","device_token":"dtok-1"}`,
			checkinID:     "ses-2",
			wantAttempted: []string{"ses-1", "ses-2"},
			wantRevoked:   []string{"ses-1", "ses-2"},
		},
		{
			// The case this table exists for: a stale token (the NORMAL state
			// at a 300 s TTL) plus a server that refuses (every non-admin
			// caller, the route is straza-admin-gated). Returning on the first
			// failure would leave the session the refresh had just minted alive
			// AND unmentioned.
			name:          "a refusal after a re-establish leaves BOTH sessions, and says so",
			body:          `{"server":"%s","session_token":"stale","session_id":"ses-1","device_token":"dtok-1"}`,
			revokeStatus:  http.StatusForbidden,
			checkinID:     "ses-2",
			wantAttempted: []string{"ses-1", "ses-2"},
			wantUnrevoked: []string{"ses-1", "ses-2"},
		},
		{
			name:          "the minted session goes even when the stored one could not",
			body:          `{"server":"%s","session_token":"stale","session_id":"ses-1","device_token":"dtok-1"}`,
			statusFor:     map[string]int{"ses-1": http.StatusInternalServerError},
			checkinID:     "ses-2",
			wantAttempted: []string{"ses-1", "ses-2"},
			wantRevoked:   []string{"ses-2"},
			wantUnrevoked: []string{"ses-1"},
		},
		{
			name:          "the leftover named is the mint, not the session that went",
			body:          `{"server":"%s","session_token":"stale","session_id":"ses-1","device_token":"dtok-1"}`,
			statusFor:     map[string]int{"ses-2": http.StatusForbidden},
			checkinID:     "ses-2",
			wantAttempted: []string{"ses-1", "ses-2"},
			wantRevoked:   []string{"ses-1"},
			wantUnrevoked: []string{"ses-2"},
		},
		{
			name: "a corrupt credentials file is exactly what logout must clear",
			body: `{not json`,
		},
		{
			name: "credentials naming no session leave the server alone",
			body: `{"server":"%s","session_token":"` + fresh + `"}`,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			m := &logoutMock{
				revokeStatus: tc.revokeStatus, statusFor: tc.statusFor,
				checkinID: tc.checkinID, checkinToken: fresh,
			}
			srv := m.server(t)
			c := NewClient(srv.URL)
			c.CredsPath = writeCredsFile(t, tc.body, srv.URL)

			res, err := c.Logout(context.Background())
			if err != nil {
				t.Fatalf("Logout: %v", err)
			}
			if got := m.seen(); !slices.Equal(got, tc.wantAttempted) {
				t.Errorf("server asked about %v, want %v", got, tc.wantAttempted)
			}
			if !slices.Equal(res.Revoked, tc.wantRevoked) {
				t.Errorf("Revoked = %v, want %v", res.Revoked, tc.wantRevoked)
			}
			if !slices.Equal(res.Unrevoked, tc.wantUnrevoked) {
				t.Errorf("Unrevoked = %v, want %v", res.Unrevoked, tc.wantUnrevoked)
			}
			// Every shortfall is explained, and a clean run explains nothing.
			wantErr := len(tc.wantUnrevoked) > 0 || len(tc.wantAttempted) == 0
			if (res.RevokeErr != nil) != wantErr {
				t.Errorf("RevokeErr = %v, want reported = %v", res.RevokeErr, wantErr)
			}
			if _, err := os.Stat(c.CredsPath); !errors.Is(err, os.ErrNotExist) {
				t.Errorf("credentials survived logout: %v", err)
			}
			if res.Path != c.CredsPath {
				t.Errorf("Path = %q, want %q", res.Path, c.CredsPath)
			}
		})
	}
}

// TestLogoutPrefersSelfRoute is the table for a server with the self-scoped
// POST /v1/session/revoke: logout uses IT, so a caller without straza-admin
// gets a real server-side revoke, and touches the admin route only for a
// session the self route structurally cannot name (the stored id after a
// mid-logout re-establish swapped the token). Reporting stays byte-compatible
// with the admin lane: Revoked/Unrevoked name actual ids, the first
// shortfall is the cause, and the file always goes.
func TestLogoutPrefersSelfRoute(t *testing.T) {
	fresh := fakeSessionJWT(t)
	const minted = "minted-token"
	tests := []struct {
		name          string
		body          string // credentials JSON, %s = server URL
		selfSessions  map[string]string
		statusFor     map[string]int
		checkinID     string
		wantSelf      []string // sessions the SELF route revoked, in order
		wantAdmin     []string // sessions the ADMIN route was asked about
		wantRevoked   []string
		wantUnrevoked []string
	}{
		{
			// The headline: a non-admin's fresh token ends its own session with
			// no admin route involved at all.
			name:         "fresh token self-revokes, admin route untouched",
			body:         `{"server":"%s","session_token":"` + fresh + `","session_id":"ses-1"}`,
			selfSessions: map[string]string{fresh: "ses-1"},
			wantSelf:     []string{"ses-1"},
			wantRevoked:  []string{"ses-1"},
		},
		{
			// A refresh in place keeps the session id, so the retried self
			// revoke still names the stored session: one revoke, no admin call.
			name:         "a stale token refreshes in place and still self-revokes",
			body:         `{"server":"%s","session_token":"stale","session_id":"ses-1","device_token":"dtok-1"}`,
			selfSessions: map[string]string{minted: "ses-1"},
			checkinID:    "ses-1",
			wantSelf:     []string{"ses-1"},
			wantRevoked:  []string{"ses-1"},
		},
		{
			// The re-establish swap: the reauth minted ses-2, so the self route
			// ends ses-2 (the session logout itself created, the one that
			// mattered most), and the stored ses-1 goes to the only route that
			// can still name it. A non-admin is refused there, and the report
			// says exactly which id went and which is left.
			name:          "re-establish: minted dies via self, stored refused via admin",
			body:          `{"server":"%s","session_token":"stale","session_id":"ses-1","device_token":"dtok-1"}`,
			selfSessions:  map[string]string{minted: "ses-2"},
			checkinID:     "ses-2",
			statusFor:     map[string]int{"ses-1": http.StatusForbidden},
			wantSelf:      []string{"ses-2"},
			wantAdmin:     []string{"ses-1"},
			wantRevoked:   []string{"ses-2"},
			wantUnrevoked: []string{"ses-1"},
		},
		{
			// Same swap with an admin caller: both sessions go, split across
			// the two routes: behavior parity with the pre-0.41.0 lane.
			name:         "re-establish: an admin still ends both",
			body:         `{"server":"%s","session_token":"stale","session_id":"ses-1","device_token":"dtok-1"}`,
			selfSessions: map[string]string{minted: "ses-2"},
			checkinID:    "ses-2",
			wantSelf:     []string{"ses-2"},
			wantAdmin:    []string{"ses-1"},
			wantRevoked:  []string{"ses-2", "ses-1"},
		},
		{
			// A self-route failure that is NOT a 404 must not detour to the
			// admin route: the fallback is strictly the older-server signal.
			name:          "a 500 from the self route is reported, never rerouted",
			body:          `{"server":"%s","session_token":"` + fresh + `","session_id":"ses-1"}`,
			selfSessions:  map[string]string{fresh: "ses-1"},
			statusFor:     map[string]int{"ses-1": http.StatusInternalServerError},
			wantUnrevoked: []string{"ses-1"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			m := &logoutMock{
				statusFor: tc.statusFor, selfSessions: tc.selfSessions,
				checkinID: tc.checkinID, checkinToken: minted,
			}
			srv := m.server(t)
			c := NewClient(srv.URL)
			c.CredsPath = writeCredsFile(t, tc.body, srv.URL)

			res, err := c.Logout(context.Background())
			if err != nil {
				t.Fatalf("Logout: %v", err)
			}
			if got := m.selfRevoked(); !slices.Equal(got, tc.wantSelf) {
				t.Errorf("self route revoked %v, want %v", got, tc.wantSelf)
			}
			if got := m.seen(); !slices.Equal(got, tc.wantAdmin) {
				t.Errorf("admin route asked about %v, want %v", got, tc.wantAdmin)
			}
			if !slices.Equal(res.Revoked, tc.wantRevoked) {
				t.Errorf("Revoked = %v, want %v", res.Revoked, tc.wantRevoked)
			}
			if !slices.Equal(res.Unrevoked, tc.wantUnrevoked) {
				t.Errorf("Unrevoked = %v, want %v", res.Unrevoked, tc.wantUnrevoked)
			}
			if wantErr := len(tc.wantUnrevoked) > 0; (res.RevokeErr != nil) != wantErr {
				t.Errorf("RevokeErr = %v, want reported = %v", res.RevokeErr, wantErr)
			}
			if _, err := os.Stat(c.CredsPath); !errors.Is(err, os.ErrNotExist) {
				t.Errorf("credentials survived logout: %v", err)
			}
		})
	}
}

// TestLogoutWithoutCredentials: no file means no session and nothing to
// delete: a distinguishable sentinel, and NOT a server call.
func TestLogoutWithoutCredentials(t *testing.T) {
	m := &logoutMock{}
	srv := m.server(t)
	c := NewClient(srv.URL)
	c.CredsPath = filepath.Join(t.TempDir(), "credentials.json")

	_, err := c.Logout(context.Background())
	if !errors.Is(err, ErrNotLoggedIn) {
		t.Fatalf("err = %v, want ErrNotLoggedIn", err)
	}
	if got := m.seen(); len(got) != 0 {
		t.Fatalf("revoked %v with no credentials, want none", got)
	}
}

// TestLogoutUnreachableServerStillDeletes: the whole point of best effort.
// A deployment that is down (or gone) must not strand a credential on disk,
// and the session it could not reach is still named.
func TestLogoutUnreachableServerStillDeletes(t *testing.T) {
	c := NewClient("http://127.0.0.1:1")
	c.CredsPath = writeCredsFile(t, `{"server":"%s","session_token":"`+fakeSessionJWT(t)+`","session_id":"ses-1"}`, "http://127.0.0.1:1")

	res, err := c.Logout(context.Background())
	if err != nil {
		t.Fatalf("Logout: %v", err)
	}
	if res.RevokeErr == nil {
		t.Error("want the unreachable server reported")
	}
	if !slices.Equal(res.Unrevoked, []string{"ses-1"}) {
		t.Errorf("Unrevoked = %v, want [ses-1]", res.Unrevoked)
	}
	if _, err := os.Stat(c.CredsPath); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("credentials survived logout: %v", err)
	}
}

// TestLogoutNoServerRevokesNothing: with no target resolved (credentials that
// name no server), there is nobody to ask; say so instead of dialling "".
func TestLogoutNoServerRevokesNothing(t *testing.T) {
	c := NewClient("")
	c.CredsPath = writeCredsFile(t, `{"session_token":"tok","session_id":"ses-1"}`, "")

	res, err := c.Logout(context.Background())
	if err != nil {
		t.Fatalf("Logout: %v", err)
	}
	if res.RevokeErr == nil {
		t.Error("want the missing server reported")
	}
	if _, err := os.Stat(c.CredsPath); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("credentials survived logout: %v", err)
	}
}

// TestLogoutLocalFailureIsAnError: when the credentials cannot be deleted the
// command has NOT done what it promises, so it fails loudly rather than
// reporting a logout that did not happen.
func TestLogoutLocalFailureIsAnError(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "credentials.json") // a directory in its place
	if err := os.MkdirAll(filepath.Join(dir, "occupied"), 0o700); err != nil {
		t.Fatal(err)
	}
	c := NewClient("")
	c.CredsPath = dir

	if _, err := c.Logout(context.Background()); err == nil {
		t.Fatal("want an error when the credentials cannot be deleted")
	}
	if _, err := os.Stat(dir); err != nil {
		t.Errorf("path should still be there: %v", err)
	}
}

// writeCredsFile lays down a credentials file, substituting the server URL
// for every %s in the body. Returns the path.
func writeCredsFile(t *testing.T, body, server string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "credentials.json")
	if err := os.WriteFile(path, []byte(strings.ReplaceAll(body, "%s", server)), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}
