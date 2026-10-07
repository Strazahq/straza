package ctl

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// sessionJWT builds a parse-only session token for session id ses that
// expires at exp. Signature checking is the server's job, so the signature
// is a placeholder.
func sessionJWT(t *testing.T, ses string, exp time.Time) string {
	t.Helper()
	claims, err := json.Marshal(map[string]any{"ses": ses, "exp": exp.Unix()})
	if err != nil {
		t.Fatal(err)
	}
	b64 := base64.RawURLEncoding.EncodeToString
	return b64([]byte(`{"alg":"EdDSA"}`)) + "." + b64(claims) + "." + b64([]byte("sig"))
}

// renewalServer refuses every session-token refresh, the way strazad refuses
// an expired or revoked session token at check-in, and re-establishes a
// session for the device token dev-1. Admin routes take only the session it
// minted.
type renewalServer struct {
	minted string

	mu            sync.Mutex
	deviceCheckin int
}

func (s *renewalServer) start(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/checkin", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		w.Header().Set("Content-Type", "application/json")
		if body["device_token"] != "dev-1" {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":"session token rejected"}`))
			return
		}
		s.mu.Lock()
		s.deviceCheckin++
		s.mu.Unlock()
		_ = json.NewEncoder(w).Encode(map[string]string{"session_id": "ses-2", "session_token": s.minted})
	})
	admin := func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Header.Get("Authorization") != "Bearer "+s.minted {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":"session is no longer active"}`))
			return
		}
		_, _ = w.Write([]byte(`{"ok":true}`))
	}
	mux.HandleFunc("GET /v1/admin/roles", admin)
	mux.HandleFunc("PUT /v1/admin/policies", admin)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

// TestEveryWrapperReestablishesWithTheDeviceToken pins that when the stored
// session token can no longer be refreshed, every authenticated wrapper
// re-establishes the session with the device token instead of sending the
// operator to a browser login. That includes the raw-body wrapper that
// policy apply and apps install use.
func TestEveryWrapperReestablishesWithTheDeviceToken(t *testing.T) {
	ctx := context.Background()
	wrappers := []struct {
		name string
		call func(c *Client) error
	}{
		{"Do", func(c *Client) error {
			return c.Do(ctx, http.MethodGet, "/v1/admin/roles", nil, nil)
		}},
		{"DoBytes", func(c *Client) error {
			_, err := c.DoBytes(ctx, http.MethodGet, "/v1/admin/roles")
			return err
		}},
		{"DoRawBody", func(c *Client) error {
			return c.DoRawBody(ctx, http.MethodPut, "/v1/admin/policies", "application/yaml", []byte("kind: PolicySet\n"), nil)
		}},
	}
	stored := []struct {
		name string
		exp  time.Duration
	}{
		{name: "an expired token is renewed before the call", exp: -time.Minute},
		{name: "a token the server refuses is renewed after the call", exp: time.Hour},
	}
	for _, w := range wrappers {
		for _, st := range stored {
			t.Run(w.name+": "+st.name, func(t *testing.T) {
				s := &renewalServer{minted: sessionJWT(t, "ses-2", time.Now().Add(time.Hour))}
				srv := s.start(t)
				c := NewClient(srv.URL)
				c.CredsPath = filepath.Join(t.TempDir(), "credentials.json")
				old := sessionJWT(t, "ses-1", time.Now().Add(st.exp))
				if err := c.saveCreds(credentials{Server: srv.URL, SessionToken: old, SessionID: "ses-1", DeviceToken: "dev-1"}); err != nil {
					t.Fatal(err)
				}
				if err := w.call(c); err != nil {
					t.Fatalf("call failed, the device token was not used: %v", err)
				}
				creds, err := c.loadCreds()
				if err != nil {
					t.Fatal(err)
				}
				if creds.SessionToken != s.minted || creds.SessionID != "ses-2" {
					t.Errorf("the re-established session was not stored: %+v", creds)
				}
				if creds.DeviceToken != "dev-1" {
					t.Errorf("device token = %q, want it kept", creds.DeviceToken)
				}
				s.mu.Lock()
				defer s.mu.Unlock()
				if s.deviceCheckin != 1 {
					t.Errorf("device check-ins = %d, want 1", s.deviceCheckin)
				}
			})
		}
	}
}
