package ctl

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// fakeLoginServer is the minimal strazad+issuer surface Login touches: idp
// discovery (a 404 sends oidcflow to the built-in paths), device
// authorization, token polling, checkin and enroll. tokenSeq is the answer
// for each token poll: a non-empty string is an OAuth `error`, "" mints an
// id_token; the last entry repeats. tokenHits counts polls so a test can
// assert the loop never sleeps before the first one.
type fakeLoginServer struct {
	tokenHits int64 // atomic; first field for 64-bit alignment
	idpHits   int64 // atomic; idp.json reads, zero on the emergency door
	interval  int
	expiresIn int
	tokenSeq  []string
	// enrollKind is the client_kind the enrol body carried, "" until then.
	enrollKind string
	// user is the username the check-in answers, left out when empty.
	user string
}

func (f *fakeLoginServer) server(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("GET /.well-known/straza/idp.json", func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt64(&f.idpHits, 1)
		w.WriteHeader(http.StatusNotFound)
	})
	mux.HandleFunc("POST /oidc/device_authorization", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"device_code":"dc-1","user_code":"WXYZ-2345","verification_uri_complete":"https://issuer.example/device?code=WXYZ-2345","expires_in":%d,"interval":%d}`,
			f.expiresIn, f.interval)
	})
	mux.HandleFunc("POST /oidc/token", func(w http.ResponseWriter, _ *http.Request) {
		n := atomic.AddInt64(&f.tokenHits, 1)
		idx := int(n - 1)
		if idx >= len(f.tokenSeq) {
			idx = len(f.tokenSeq) - 1
		}
		w.Header().Set("Content-Type", "application/json")
		if e := f.tokenSeq[idx]; e != "" {
			fmt.Fprintf(w, `{"error":%q}`, e)
			return
		}
		fmt.Fprint(w, `{"id_token":"id-token-xyz"}`)
	})
	mux.HandleFunc("POST /v1/checkin", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if f.user != "" {
			fmt.Fprintf(w, `{"session_id":"ses-1","session_token":"stok-1","user":%q}`, f.user)
			return
		}
		fmt.Fprint(w, `{"session_id":"ses-1","session_token":"stok-1"}`)
	})
	mux.HandleFunc("POST /v1/enroll", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			ClientKind string `json:"client_kind"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		f.enrollKind = body.ClientKind
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"device_token":"dtok-1"}`)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func newLoginClient(t *testing.T, base string) *Client {
	t.Helper()
	c := NewClient(base)
	c.CredsPath = filepath.Join(t.TempDir(), "credentials.json")
	return c
}

// TestLoginReturnsOnFirstPoll pins the poll-first loop of strazactl login:
// with the grant already approved and a large advertised interval, the loop
// finishes well under one interval.
func TestLoginReturnsOnFirstPoll(t *testing.T) {
	f := &fakeLoginServer{interval: 5, expiresIn: 600, tokenSeq: []string{""}}
	srv := f.server(t)
	c := newLoginClient(t, srv.URL)

	var out strings.Builder
	start := time.Now()
	if err := c.Login(context.Background(), &out, false); err != nil {
		t.Fatalf("Login: %v", err)
	}
	if d := time.Since(start); d >= 2*time.Second {
		t.Fatalf("Login took %v; it waited before the first poll (interval 5 s)", d)
	}
	if got := atomic.LoadInt64(&f.tokenHits); got != 1 {
		t.Fatalf("token polls = %d, want exactly 1 (approved before the first poll)", got)
	}
	if !strings.Contains(out.String(), "Logged in.") {
		t.Fatalf("login did not complete: %q", out.String())
	}
	if f.enrollKind != "human" {
		t.Fatalf("enrol client_kind = %q, want human (the CLI enrols a credential for a person)", f.enrollKind)
	}
}

// TestLoginPreCancelledContextPollsNothing pins the fail-closed guard: an
// already-cancelled context returns context.Canceled and makes no token poll.
func TestLoginPreCancelledContextPollsNothing(t *testing.T) {
	f := &fakeLoginServer{interval: 5, expiresIn: 600, tokenSeq: []string{""}}
	srv := f.server(t)
	c := newLoginClient(t, srv.URL)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var out strings.Builder
	err := c.Login(ctx, &out, false)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if got := atomic.LoadInt64(&f.tokenHits); got != 0 {
		t.Fatalf("token polls = %d on a cancelled context, want 0", got)
	}
}

// TestLoginPendingThenApproved keeps the between-polls wait honest: pending on
// the first poll, approved on the second, so the loop sits on its bottom wait
// once (interval 1 s): ≥2 polls, ≥1 interval of wall time.
func TestLoginPendingThenApproved(t *testing.T) {
	f := &fakeLoginServer{interval: 1, expiresIn: 600, tokenSeq: []string{"authorization_pending", ""}}
	srv := f.server(t)
	c := newLoginClient(t, srv.URL)

	var out strings.Builder
	start := time.Now()
	if err := c.Login(context.Background(), &out, false); err != nil {
		t.Fatalf("Login: %v", err)
	}
	if got := atomic.LoadInt64(&f.tokenHits); got < 2 {
		t.Fatalf("token polls = %d, want ≥2 (pending then approved)", got)
	}
	if d := time.Since(start); d < 1*time.Second {
		t.Fatalf("Login took %v; no wait between polls (interval 1 s)", d)
	}
}

// TestLoginRefusedWording pins the error mapping of a token endpoint
// refusal: the provider, the code, the description when the provider sent
// one, and the next step.
func TestLoginRefusedWording(t *testing.T) {
	const issuer = "https://issuer.example/realms/straza"
	tests := []struct {
		name        string
		code        string
		description string
		want        string
	}{
		{
			name: "code alone",
			code: "unknown_error",
			want: "login failed: the identity provider at https://issuer.example/realms/straza refused the device login with the code unknown_error. Run `strazactl login` once more, and if it fails the same way check that provider's logs for the code",
		},
		{
			name:        "code with a description",
			code:        "invalid_grant",
			description: "Device code not valid",
			want:        "login failed: the identity provider at https://issuer.example/realms/straza refused the device login with the code invalid_grant (Device code not valid). Run `strazactl login` once more, and if it fails the same way check that provider's logs for the code",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := loginRefused(issuer, tc.code, tc.description).Error(); got != tc.want {
				t.Errorf("loginRefused = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestLoginRefusalReachesTheOperator runs a refusal through the poll loop:
// the token endpoint's code lands in the error with the next step, after
// exactly one poll.
func TestLoginRefusalReachesTheOperator(t *testing.T) {
	f := &fakeLoginServer{interval: 1, expiresIn: 600, tokenSeq: []string{"access_denied"}}
	srv := f.server(t)
	c := newLoginClient(t, srv.URL)

	var out strings.Builder
	err := c.Login(context.Background(), &out, false)
	if err == nil || !strings.Contains(err.Error(), "the code access_denied") || !strings.Contains(err.Error(), "once more") {
		t.Fatalf("err = %v, want the refusal with the code and the next step", err)
	}
	if got := atomic.LoadInt64(&f.tokenHits); got != 1 {
		t.Fatalf("token polls = %d after a refusal, want 1", got)
	}
}

// TestLoginSlowDownStillCompletes keeps the slow_down interval bump alive: the
// first poll answers slow_down, the loop bumps its wait and polls again to a
// successful login.
func TestLoginSlowDownStillCompletes(t *testing.T) {
	f := &fakeLoginServer{interval: 1, expiresIn: 600, tokenSeq: []string{"slow_down", ""}}
	srv := f.server(t)
	c := newLoginClient(t, srv.URL)

	var out strings.Builder
	if err := c.Login(context.Background(), &out, false); err != nil {
		t.Fatalf("Login: %v", err)
	}
	if got := atomic.LoadInt64(&f.tokenHits); got < 2 {
		t.Fatalf("token polls = %d, want ≥2 (slow_down then approved)", got)
	}
	if !strings.Contains(out.String(), "Logged in.") {
		t.Fatalf("login did not complete after slow_down: %q", out.String())
	}
}

// TestLoginBreakGlassSkipsTheIdentityProvider pins the emergency door: with
// breakGlass set, Login never reads idp.json and runs the device flow at
// the server's own paths, so a dead or misconfigured identity provider
// cannot stop the break-glass admin from signing in.
func TestLoginBreakGlassSkipsTheIdentityProvider(t *testing.T) {
	f := &fakeLoginServer{interval: 1, expiresIn: 600, tokenSeq: []string{""}}
	srv := f.server(t)
	c := newLoginClient(t, srv.URL)

	var out strings.Builder
	if err := c.Login(context.Background(), &out, true); err != nil {
		t.Fatalf("Login: %v", err)
	}
	if got := atomic.LoadInt64(&f.idpHits); got != 0 {
		t.Fatalf("idp.json reads = %d on the emergency door, want 0", got)
	}
	if !strings.Contains(out.String(), "emergency page as break-glass") || !strings.Contains(out.String(), "Logged in.") {
		t.Fatalf("output %q does not name the emergency page and complete", out.String())
	}
}

// TestLoginNamesTheUser pins the last line of a login: it names the user the
// check-in answered, and says Logged in. alone when the answer names none.
func TestLoginNamesTheUser(t *testing.T) {
	tests := []struct {
		name, user, want string
	}{
		{"the check-in names the user", "alice", "Logged in as alice.\n"},
		{"the check-in names nobody", "", "Logged in.\n"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := &fakeLoginServer{interval: 1, expiresIn: 600, tokenSeq: []string{""}, user: tc.user}
			c := newLoginClient(t, f.server(t).URL)
			var out strings.Builder
			if err := c.Login(context.Background(), &out, false); err != nil {
				t.Fatalf("Login: %v", err)
			}
			if !strings.HasSuffix(out.String(), "\n"+tc.want) {
				t.Errorf("output = %q, want it to end with %q", out.String(), tc.want)
			}
		})
	}
}
