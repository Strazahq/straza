package agentguard

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/strazahq/straza/internal/oidcflow"
)

// TestPollTokenWireStates pins PollToken against the issuer's real wire
// behavior (authn TestDeviceFlowWireContract): RFC 6749 protocol states
// arrive as HTTP 400 with an `error` body. authorization_pending and
// slow_down mean "keep waiting", not failure. Every e2e test approves the
// code faster than the first poll, so only this test runs the pending branch
// on the wire.
func TestPollTokenWireStates(t *testing.T) {
	cases := []struct {
		name        string
		status      int
		body        string
		wantToken   string
		wantPending bool
		wantErr     string // substring; empty = no error
	}{
		{"pending is not a failure", http.StatusBadRequest, `{"error":"authorization_pending","error_description":"user has not approved yet"}`, "", true, ""},
		{"slow_down is not a failure", http.StatusBadRequest, `{"error":"slow_down","error_description":"poll slower"}`, "", true, ""},
		{"expired code is terminal", http.StatusBadRequest, `{"error":"expired_token","error_description":"device_code expired; restart login"}`, "", false, "expired_token"},
		{"denied is terminal", http.StatusBadRequest, `{"error":"access_denied"}`, "", false, "access_denied"},
		{"unknown code is terminal", http.StatusBadRequest, `{"error":"invalid_grant"}`, "", false, "invalid_grant"},
		{"approved yields the token", http.StatusOK, `{"id_token":"idt-1","token_type":"Bearer"}`, "idt-1", false, ""},
		{"server failure is an error", http.StatusInternalServerError, `boom`, "", false, "HTTP 500"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer srv.Close()

			// TokenURL is absolute, as oidcflow.Discover always returns it, so
			// the client must NOT prefix its own base onto it.
			flow := oidcflow.Flow{TokenURL: srv.URL + "/oidc/token", ClientID: "straza"}
			tok, pending, err := NewClient("http://strazad.invalid").PollToken(context.Background(), flow, "dc")
			if tc.wantErr == "" && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if tc.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tc.wantErr)) {
				t.Fatalf("error = %v, want substring %q", err, tc.wantErr)
			}
			if pending != tc.wantPending {
				t.Errorf("pending = %v, want %v", pending, tc.wantPending)
			}
			if tok != tc.wantToken {
				t.Errorf("token = %q, want %q", tok, tc.wantToken)
			}
		})
	}
}

// TestStartDeviceFlowWire pins the device-authorization request: it goes to
// the DISCOVERED endpoint (not the strazad base) with the flow's client id
// and scope=openid, because external IdPs only mint an ID token when asked.
func TestStartDeviceFlowWire(t *testing.T) {
	var gotPath, gotClientID, gotScope string
	idp := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		gotPath, gotClientID, gotScope = r.URL.Path, r.PostForm.Get("client_id"), r.PostForm.Get("scope")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"device_code":"dc","user_code":"AAAA-BBBB","verification_uri_complete":"x","expires_in":600,"interval":2}`))
	}))
	defer idp.Close()

	flow := oidcflow.Flow{
		DeviceAuthURL: idp.URL + "/realms/agents/protocol/openid-connect/auth/device",
		TokenURL:      idp.URL + "/realms/agents/protocol/openid-connect/token",
		ClientID:      "straza",
	}
	auth, err := NewClient("http://strazad.invalid").StartDeviceFlow(context.Background(), flow)
	if err != nil {
		t.Fatalf("StartDeviceFlow: %v", err)
	}
	if auth.DeviceCode != "dc" {
		t.Errorf("device code = %q", auth.DeviceCode)
	}
	if gotPath != "/realms/agents/protocol/openid-connect/auth/device" {
		t.Errorf("posted to %q, want the discovered endpoint", gotPath)
	}
	if gotClientID != "straza" || gotScope != "openid" {
		t.Errorf("client_id=%q scope=%q, want straza/openid", gotClientID, gotScope)
	}
}
