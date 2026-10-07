package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// approversServer is the admin surface the approvers family walks: checkin for
// the stored token, the enrolled-phone list, and the existing device delete.
type approversServer struct {
	mu      sync.Mutex
	revoked []string
	lastURI string
}

func (s *approversServer) start(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/checkin", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"session_id":"ses-1","session_token":"stok-2"}`))
	})
	mux.HandleFunc("GET /v1/admin/approvers", func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		s.lastURI = r.URL.RequestURI()
		s.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Query().Get("user") == "ghost" {
			_, _ = w.Write([]byte(`[]`))
			return
		}
		_, _ = w.Write([]byte(`[{"id":"apd_1","user_id":"u-kim","username":"kim","name":"kim-pixel","platform":"android","key_security_level":"strongbox","attestation":"play-integrity","enrolled_at":"2026-07-01T10:00:00Z","last_seen":"2026-07-29T08:30:00Z","push_routes":2},` +
			`{"id":"apd_2","user_id":"u-bob","username":"bob","name":"bob-iphone","platform":"ios","key_security_level":"secure-enclave","attestation":"none","enrolled_at":"2026-07-02T09:00:00Z","push_routes":0}]`))
	})
	mux.HandleFunc("DELETE /v1/admin/approvers/{id}", func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		s.revoked = append(s.revoked, r.PathValue("id"))
		s.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if r.PathValue("id") != "apd_1" {
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"error":"no such approver device"}`))
			return
		}
		_, _ = w.Write([]byte(`{"status":"revoked"}`))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

// TestApproversList pins `strazactl approvers list [username]`: every enrolled
// phone with its owner, posture, and push-route count. The table makes the
// apd_ id and the silent-phone gap visible from a terminal.
func TestApproversList(t *testing.T) {
	s := &approversServer{}
	srv := s.start(t)
	t.Setenv("STRAZA_SERVER", "")
	credsPath := writeCreds(t, srv.URL)

	stdout, _, err := runCLI(t, credsPath, "approvers", "list")
	if err != nil {
		t.Fatalf("approvers list: %v", err)
	}
	for _, want := range []string{
		"ID", "USER", "NAME", "PLATFORM", "KEY", "ATTESTATION", "PUSH", "ENROLLED", "LAST SEEN",
		"apd_1", "kim", "kim-pixel", "android", "strongbox", "play-integrity",
		"apd_2", "bob", "bob-iphone", "ios", "secure-enclave",
	} {
		if !strings.Contains(stdout, want) {
			t.Errorf("stdout missing %q:\n%s", want, stdout)
		}
	}
	// A phone with no push route is flagged inline, not just counted: it can
	// decide but will never ring, and that row is otherwise invisible.
	if !strings.Contains(stdout, "none (never notified)") {
		t.Errorf("stdout must call out the routeless phone:\n%s", stdout)
	}

	// The optional username operand becomes the server-side filter.
	if _, _, err := runCLI(t, credsPath, "approvers", "list", "ghost"); err != nil {
		t.Fatalf("filtered list: %v", err)
	}
	s.mu.Lock()
	lastURI := s.lastURI
	s.mu.Unlock()
	if lastURI != "/v1/admin/approvers?user=ghost" {
		t.Errorf("filtered wire = %q, want /v1/admin/approvers?user=ghost", lastURI)
	}
}

// TestApproversRevoke pins `strazactl approvers revoke <device-id>`: the
// admin DELETE, reachable without opening the database.
func TestApproversRevoke(t *testing.T) {
	s := &approversServer{}
	srv := s.start(t)
	t.Setenv("STRAZA_SERVER", "")
	credsPath := writeCreds(t, srv.URL)

	stdout, _, err := runCLI(t, credsPath, "approvers", "revoke", "apd_1")
	if err != nil {
		t.Fatalf("approvers revoke: %v", err)
	}
	s.mu.Lock()
	revoked := append([]string(nil), s.revoked...)
	s.mu.Unlock()
	if len(revoked) != 1 || revoked[0] != "apd_1" {
		t.Errorf("server saw %v, want exactly apd_1", revoked)
	}
	if !strings.Contains(stdout, "revoked approver device apd_1") {
		t.Errorf("stdout must name the device:\n%s", stdout)
	}

	if _, _, err := runCLI(t, credsPath, "approvers", "revoke", "apd_9"); err == nil ||
		!strings.Contains(err.Error(), "no such approver device") {
		t.Errorf("unknown device = %v, want the server's 404 reason", err)
	}
}

// TestApproversOperandCount: list takes at most one operand, revoke exactly
// one. A stray operand is an error and is never ignored.
func TestApproversOperandCount(t *testing.T) {
	s := &approversServer{}
	srv := s.start(t)
	t.Setenv("STRAZA_SERVER", "")
	credsPath := writeCreds(t, srv.URL)

	for _, args := range [][]string{
		{"approvers", "list", "kim", "extra"},
		{"approvers", "revoke"},
		{"approvers", "revoke", "apd_1", "extra"},
	} {
		if _, _, err := runCLI(t, credsPath, args...); err == nil {
			t.Errorf("%v accepted, want an operand-count error", args)
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.revoked) != 0 {
		t.Errorf("a rejected invocation reached the server: %v", s.revoked)
	}
}
