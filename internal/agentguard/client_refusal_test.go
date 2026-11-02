package agentguard

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/strazahq/straza/internal/oidcflow"
)

// TestRefusalCarriesTheReason pins how a refused request reaches the
// person: a strazad refusal by its error sentence, an OAuth refusal by its
// description, else its error code, and a body that says nothing by its
// HTTP status. straza enroll's device sign-in start rides this path.
func TestRefusalCarriesTheReason(t *testing.T) {
	const (
		tooMany       = "Too many sign-in starts from this address. Wait a few seconds and sign in again."
		clientRefused = "client_id must be straza, strazactl or console, the Straza programs this server signs in. Start the sign-in again with straza enroll or strazactl login."
	)
	cases := []struct {
		name    string
		status  int
		body    string
		wantMsg string
	}{
		{name: "a strazad refusal", status: http.StatusTooManyRequests,
			body: `{"error":"` + tooMany + `"}`, wantMsg: tooMany},
		{name: "an OAuth refusal with a description", status: http.StatusBadRequest,
			body: `{"error":"invalid_client","error_description":"` + clientRefused + `"}`, wantMsg: clientRefused},
		{name: "an OAuth refusal without a description", status: http.StatusServiceUnavailable,
			body: `{"error":"temporarily_unavailable"}`, wantMsg: "temporarily_unavailable"},
		{name: "a body that is not JSON", status: http.StatusBadGateway,
			body: `<html>bad gateway</html>`, wantMsg: "HTTP 502"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			t.Cleanup(srv.Close)
			c := NewClient(srv.URL)
			_, err := c.StartDeviceFlow(context.Background(), oidcflow.Builtin(srv.URL, "straza"))
			var refused *StatusError
			if !errors.As(err, &refused) {
				t.Fatalf("StartDeviceFlow error = %v, want a *StatusError", err)
			}
			if refused.Status != tc.status || refused.Msg != tc.wantMsg {
				t.Errorf("StatusError = %d %q, want %d %q", refused.Status, refused.Msg, tc.status, tc.wantMsg)
			}
		})
	}
}
