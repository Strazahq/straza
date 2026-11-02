package e2ematrix

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

// TestDeviceTokenWaitsOutTheStartThrottle pins that the runner's device
// sign-in waits out a 429 on the start, as approveDevice does on the
// password submit, so a scenario that signs people in back to back passes.
func TestDeviceTokenWaitsOutTheStartThrottle(t *testing.T) {
	cases := []struct {
		name       string
		throttled  int64 // starts answered 429 before the first 200
		wantStarts int64
	}{
		{name: "a start that is not throttled", throttled: 0, wantStarts: 1},
		{name: "a throttled start is tried again", throttled: 1, wantStarts: 2},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var starts int64
			mux := http.NewServeMux()
			mux.HandleFunc("POST /oidc/device_authorization", func(w http.ResponseWriter, _ *http.Request) {
				if atomic.AddInt64(&starts, 1) <= tc.throttled {
					w.Header().Set("Retry-After", "1")
					w.WriteHeader(http.StatusTooManyRequests)
					_, _ = w.Write([]byte(`{"error":"Too many sign-in starts from this address. Wait a few seconds and sign in again."}`))
					return
				}
				_, _ = w.Write([]byte(`{"device_code":"dc-1","user_code":"WXYZ-2345"}`))
			})
			mux.HandleFunc("POST /oidc/device", func(w http.ResponseWriter, _ *http.Request) {
				_, _ = w.Write([]byte(`{}`))
			})
			mux.HandleFunc("POST /oidc/token", func(w http.ResponseWriter, _ *http.Request) {
				_, _ = w.Write([]byte(`{"id_token":"id-token-xyz"}`))
			})
			srv := httptest.NewServer(mux)
			t.Cleanup(srv.Close)

			s := &strazad{URL: srv.URL}
			token, err := s.deviceToken(context.Background(), "kim", "pw")
			if err != nil || token != "id-token-xyz" {
				t.Fatalf("deviceToken = %q, %v, want the id token", token, err)
			}
			if got := atomic.LoadInt64(&starts); got != tc.wantStarts {
				t.Errorf("device starts = %d, want %d", got, tc.wantStarts)
			}
		})
	}
}
