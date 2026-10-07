package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// signingKeysServer is the admin surface `signing-keys` walks: the checkin a
// stored token refreshes through, the list, the two rotate routes and the
// retire route. It counts the writes and can refuse them the way a grant
// check does.
type signingKeysServer struct {
	mu     sync.Mutex
	calls  int
	refuse bool
	// rotateBody and retireBody are the client assertion answers under test.
	rotateBody string
	retireBody string
}

func (s *signingKeysServer) start(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/checkin", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"session_id":"ses-1","session_token":"stok-2"}`))
	})
	write := func(body func() string) http.HandlerFunc {
		return func(w http.ResponseWriter, _ *http.Request) {
			s.mu.Lock()
			s.calls++
			refuse := s.refuse
			s.mu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			if refuse {
				w.WriteHeader(http.StatusForbidden)
				_, _ = w.Write([]byte(`{"error":"token lacks scope sessions:write"}`))
				return
			}
			_, _ = w.Write([]byte(body()))
		}
	}
	mux.HandleFunc("POST /v1/admin/signing-keys/rotate", write(func() string {
		return `{"kid":"k-new","status":"staged","active_in_seconds":60,"previous_key_verifies_for_seconds":2592090}`
	}))
	mux.HandleFunc("POST /v1/admin/signing-keys/client-assertion/rotate", write(func() string { return s.rotateBody }))
	mux.HandleFunc("POST /v1/admin/signing-keys/client-assertion/{kid}/retire", write(func() string { return s.retireBody }))
	mux.HandleFunc("GET /v1/admin/signing-keys", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[
			{"kid":"k-ca","purpose":"client_assertion","status":"active","created_at":"2026-09-21T10:00:00Z","rotated_at":"2026-09-21T10:01:00Z"},
			{"kid":"k-ses","purpose":"session","status":"active","created_at":"2026-09-01T08:00:00Z"}]`))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

// TestSigningKeysRotate pins `strazactl signing-keys rotate <purpose>`: one
// call to the purpose's route and plain sentences that name the staged kid,
// when it starts signing and when the previous key goes, all computed from
// the response. A client assertion key created while none signs also says
// where its key document is and what to do with it.
func TestSigningKeysRotate(t *testing.T) {
	tests := []struct {
		name, purpose, body, want string
	}{
		{"the session key", "session", "",
			"Staged a new session signing key k-new.\n" +
				"It becomes the signing key in about 1 minute, and the previous key stops verifying about 30 days after that.\n"},
		{"a client assertion key while none signs", "client_assertion",
			`{"kid":"k-ca","status":"staged","active_in_seconds":60,"previous_key_verifies_for_seconds":120,"jwks_uri":"https://straza.example/.well-known/straza/client-assertion-jwks.json"}`,
			"Created a client assertion key k-ca. No other key signs client assertions.\n" +
				"It is in the key document now and starts signing in about 1 minute.\n" +
				"The key document is https://straza.example/.well-known/straza/client-assertion-jwks.json. Register it as the JWKS URL of each agent's client at your identity provider.\n"},
		{"the next client assertion key", "client_assertion",
			`{"kid":"k-ca2","status":"staged","previous_kid":"k-ca","active_in_seconds":60,"previous_key_verifies_for_seconds":120,"jwks_uri":"https://straza.example/.well-known/straza/client-assertion-jwks.json"}`,
			"Staged a new client assertion key k-ca2.\n" +
				"It is in the key document now and starts signing in about 1 minute. The previous key k-ca leaves the key document about 1 minute after that.\n"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := &signingKeysServer{rotateBody: tc.body}
			srv := s.start(t)
			t.Setenv("STRAZA_SERVER", "")
			credsPath := writeCreds(t, srv.URL)

			stdout, _, err := runCLI(t, credsPath, "signing-keys", "rotate", tc.purpose)
			if err != nil {
				t.Fatalf("signing-keys rotate %s: %v", tc.purpose, err)
			}
			if stdout != tc.want {
				t.Errorf("stdout:\n%s\nwant:\n%s", stdout, tc.want)
			}
			s.mu.Lock()
			calls := s.calls
			s.refuse = true
			s.mu.Unlock()
			if calls != 1 {
				t.Errorf("server saw %d rotate calls, want 1", calls)
			}
			// A refused rotation surfaces the server's reason and exits non-zero.
			if _, _, err := runCLI(t, credsPath, "signing-keys", "rotate", tc.purpose); err == nil ||
				!strings.Contains(err.Error(), "token lacks scope sessions:write") {
				t.Errorf("refused rotate = %v, want the server's 403 reason", err)
			}
		})
	}
}

// TestSigningKeysRetire pins `strazactl signing-keys retire <kid>`: what the
// operator reads depends on the state the key was in, and only the retirement
// of the signing key tells them that nothing signs until they rotate.
func TestSigningKeysRetire(t *testing.T) {
	const cache = "Your identity provider may keep the retired key in its cache. If the key was copied, clear that cache or remove the JWKS URL from the agents' clients there.\n"
	tests := []struct {
		name, body, want string
	}{
		{"the signing key",
			`{"kid":"k-ca","status":"retired","was":"active","leaves_document_in_seconds":30,"next_key_active_in_seconds":60}`,
			"Retired the client assertion key k-ca. It leaves the key document on every replica within 30 seconds.\n" +
				"No key signs client assertions now. Run `strazactl signing-keys rotate client_assertion`, and the new key starts signing about 1 minute later.\n" + cache},
		{"a key that no longer signs",
			`{"kid":"k-ca","status":"retired","was":"retiring","leaves_document_in_seconds":30,"next_key_active_in_seconds":60}`,
			"Retired the client assertion key k-ca. It leaves the key document on every replica within 30 seconds.\n" + cache},
		{"a key that is retired already",
			`{"kid":"k-ca","status":"retired","was":"retired","leaves_document_in_seconds":30,"next_key_active_in_seconds":60}`,
			"The client assertion key k-ca was already retired. Nothing changed.\n"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := &signingKeysServer{retireBody: tc.body}
			srv := s.start(t)
			t.Setenv("STRAZA_SERVER", "")
			stdout, _, err := runCLI(t, writeCreds(t, srv.URL), "signing-keys", "retire", "k-ca")
			if err != nil {
				t.Fatalf("signing-keys retire: %v", err)
			}
			if stdout != tc.want {
				t.Errorf("stdout:\n%s\nwant:\n%s", stdout, tc.want)
			}
		})
	}
}

// TestSigningKeysList pins the table: every key with its purpose and status,
// and an empty CHANGED cell for a key whose status never changed.
func TestSigningKeysList(t *testing.T) {
	s := &signingKeysServer{}
	srv := s.start(t)
	t.Setenv("STRAZA_SERVER", "")
	stdout, _, err := runCLI(t, writeCreds(t, srv.URL), "signing-keys", "list")
	if err != nil {
		t.Fatalf("signing-keys list: %v", err)
	}
	want := "KID    PURPOSE           STATUS  CREATED               CHANGED\n" +
		"k-ca   client_assertion  active  2026-09-21T10:00:00Z  2026-09-21T10:01:00Z\n" +
		"k-ses  session           active  2026-09-01T08:00:00Z  -\n"
	if stdout != want {
		t.Errorf("stdout:\n%s\nwant:\n%s", stdout, want)
	}
}

// TestSigningKeysOperands: the purpose and the kid are required operands with
// no default, and a wrong invocation says what to type and never reaches the
// server.
func TestSigningKeysOperands(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want string
	}{
		{"rotate without a purpose", []string{"rotate"},
			"signing-keys rotate needs the purpose of the key to rotate: session or client_assertion. Example: strazactl signing-keys rotate session"},
		{"rotate with an unknown purpose", []string{"rotate", "snapshot"},
			`"snapshot" is not a purpose this command rotates. Use session or client_assertion. Example: strazactl signing-keys rotate session`},
		{"rotate with a stray operand", []string{"rotate", "session", "extra"},
			"signing-keys rotate takes one operand, the purpose: session or client_assertion. Example: strazactl signing-keys rotate session"},
		{"retire without a kid", []string{"retire"},
			"signing-keys retire needs the id of one client assertion key. Run `strazactl signing-keys list` to see the ids"},
		{"list with a stray operand", []string{"list", "extra"}, "unknown command"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := &signingKeysServer{}
			srv := s.start(t)
			t.Setenv("STRAZA_SERVER", "")
			_, _, err := runCLI(t, writeCreds(t, srv.URL), append([]string{"signing-keys"}, tc.args...)...)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("err = %v, want %q", err, tc.want)
			}
			s.mu.Lock()
			defer s.mu.Unlock()
			if s.calls != 0 {
				t.Errorf("a rejected invocation reached the server %d times", s.calls)
			}
		})
	}
}

// TestRoughDuration pins the unit and rounding the rotate sentences use.
func TestRoughDuration(t *testing.T) {
	for _, tc := range []struct {
		seconds int
		want    string
	}{
		{45, "45 seconds"},
		{60, "1 minute"},
		{330, "6 minutes"},
		{3600, "1 hour"},
		{7500, "2 hours"},
		{86400, "1 day"},
		{2592030, "30 days"},
	} {
		if got := roughDuration(tc.seconds); got != tc.want {
			t.Errorf("roughDuration(%d) = %q, want %q", tc.seconds, got, tc.want)
		}
	}
}
