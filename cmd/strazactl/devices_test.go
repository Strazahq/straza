package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// devicesServer is the admin surface the devices family walks: the checkin a
// stored token refreshes through, the users list (username → id resolution),
// the per-user device list, and the device revoke. It records every revoke.
type devicesServer struct {
	mu      sync.Mutex
	revoked []string // "userID/deviceID"
}

func (s *devicesServer) start(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/checkin", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"session_id":"ses-1","session_token":"stok-2"}`))
	})
	mux.HandleFunc("GET /v1/admin/users", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[{"id":"u-kim","username":"kim","status":"active"}]`))
	})
	mux.HandleFunc("GET /v1/admin/users/{id}/devices", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.PathValue("id") != "u-kim" {
			_, _ = w.Write([]byte(`[]`))
			return
		}
		_, _ = w.Write([]byte(`[{"ID":"d-1","UserID":"u-kim","Name":"kim-laptop","Platform":"windows","Fingerprint":"cli:kim","Status":"active","EnrolledAt":"2026-07-01T10:00:00Z"}]`))
	})
	mux.HandleFunc("DELETE /v1/admin/users/{id}/devices/{deviceId}", func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		s.revoked = append(s.revoked, r.PathValue("id")+"/"+r.PathValue("deviceId"))
		s.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if r.PathValue("id") != "u-kim" || r.PathValue("deviceId") != "d-1" {
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"error":"no such device for this user"}`))
			return
		}
		_, _ = w.Write([]byte(`{"status":"revoked"}`))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

// TestDevicesList pins `strazactl devices list <username>`: username resolved
// to the user id server-side data comes back as a table naming the device id
// an operator will paste into `devices revoke`.
func TestDevicesList(t *testing.T) {
	s := &devicesServer{}
	srv := s.start(t)
	t.Setenv("STRAZA_SERVER", "")
	credsPath := writeCreds(t, srv.URL)

	stdout, _, err := runCLI(t, credsPath, "devices", "list", "kim")
	if err != nil {
		t.Fatalf("devices list: %v", err)
	}
	for _, want := range []string{"ID", "NAME", "PLATFORM", "STATUS", "ENROLLED", "d-1", "kim-laptop", "windows", "active"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("stdout missing %q:\n%s", want, stdout)
		}
	}

	if _, _, err := runCLI(t, credsPath, "devices", "list", "nobody"); err == nil ||
		!strings.Contains(err.Error(), "nobody") {
		t.Errorf("unknown username = %v, want an error naming it", err)
	}
}

// TestDevicesRevoke pins `strazactl devices revoke <username> <device-id>`:
// the canonical two-operand form, the wire call it makes, and the confirmation
// naming what was revoked and what it means.
func TestDevicesRevoke(t *testing.T) {
	s := &devicesServer{}
	srv := s.start(t)
	t.Setenv("STRAZA_SERVER", "")
	credsPath := writeCreds(t, srv.URL)

	stdout, _, err := runCLI(t, credsPath, "devices", "revoke", "kim", "d-1")
	if err != nil {
		t.Fatalf("devices revoke: %v", err)
	}
	s.mu.Lock()
	revoked := append([]string(nil), s.revoked...)
	s.mu.Unlock()
	if len(revoked) != 1 || revoked[0] != "u-kim/d-1" {
		t.Errorf("server saw %v, want exactly u-kim/d-1", revoked)
	}
	if !strings.Contains(stdout, "revoked device d-1") || !strings.Contains(stdout, "kim") {
		t.Errorf("stdout must name the device and user:\n%s", stdout)
	}

	// A refused revoke surfaces the server's reason and exits non-zero.
	if _, _, err := runCLI(t, credsPath, "devices", "revoke", "kim", "d-9"); err == nil ||
		!strings.Contains(err.Error(), "no such device") {
		t.Errorf("unknown device = %v, want the server's 404 reason", err)
	}
}

// TestDevicesOperandCount: both commands take exact operands. A stray or
// missing argument is an error and is never ignored.
func TestDevicesOperandCount(t *testing.T) {
	s := &devicesServer{}
	srv := s.start(t)
	t.Setenv("STRAZA_SERVER", "")
	credsPath := writeCreds(t, srv.URL)

	for _, args := range [][]string{
		{"devices", "list"},
		{"devices", "list", "kim", "extra"},
		{"devices", "revoke", "kim"},
		{"devices", "revoke", "kim", "d-1", "extra"},
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
