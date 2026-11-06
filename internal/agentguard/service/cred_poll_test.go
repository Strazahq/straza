package service

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/strazahq/straza/internal/agentguard"
	"github.com/strazahq/straza/internal/agentguard/localapi"
	"github.com/strazahq/straza/internal/oidcflow"
)

// TestCredDevicePoll pins the device-code poll of a person's enrollment:
// the first poll goes out at once, a pending answer waits one interval, a
// slow_down answer waits 5 seconds more, a cancelled context polls nothing,
// an approval without an ID token and an expired code end with their
// sentences.
func TestCredDevicePoll(t *testing.T) {
	cases := []struct {
		name      string
		answers   []string // an OAuth error per poll, "" for an ID token, "-" for an answer without one
		interval  int
		expiresIn int
		cancelled bool
		timeout   time.Duration // the context's deadline, 30 seconds when zero
		wantPolls int64
		minWait   time.Duration
		maxWait   time.Duration
		wantErr   error
		wantText  string
	}{
		{name: "approved before the first poll", answers: []string{""}, interval: 5, expiresIn: 600, wantPolls: 1, maxWait: 2 * time.Second},
		{name: "pending then approved", answers: []string{"authorization_pending", ""}, interval: 1, expiresIn: 600, wantPolls: 2, minWait: time.Second, maxWait: 5 * time.Second},
		{name: "slow_down waits 5 seconds more", answers: []string{"slow_down", ""}, interval: 1, expiresIn: 600, timeout: 3 * time.Second,
			wantPolls: 1, wantErr: context.DeadlineExceeded},
		{name: "cancelled before it starts", answers: []string{""}, interval: 5, expiresIn: 600, cancelled: true, wantErr: context.Canceled},
		{name: "approved without an ID token", answers: []string{"-"}, interval: 1, expiresIn: 600, wantPolls: 1,
			wantText: "Ask IT to allow the openid scope for the Straza client, then run `straza enroll --server http://127.0.0.1:"},
		{name: "code already expired", answers: []string{""}, interval: 1, expiresIn: 0, wantErr: errDeviceCodeExpired,
			wantText: "Run `straza enroll --server http://127.0.0.1:"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("STRAZA_SYSTEM", t.TempDir()) // no managed configuration
			var polls atomic.Int64
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				n := int(polls.Add(1)) - 1
				answer := tc.answers[min(n, len(tc.answers)-1)]
				w.Header().Set("Content-Type", "application/json")
				switch answer {
				case "":
					fmt.Fprint(w, `{"id_token":"id-token-xyz"}`)
				case "-":
					fmt.Fprint(w, `{}`)
				default:
					w.WriteHeader(http.StatusBadRequest)
					fmt.Fprintf(w, `{"error":%q}`, answer)
				}
			}))
			defer srv.Close()
			timeout := tc.timeout
			if timeout == 0 {
				timeout = 30 * time.Second
			}
			ctx, cancel := context.WithTimeout(context.Background(), timeout)
			defer cancel()
			if tc.cancelled {
				cancel()
			}
			flow := oidcflow.Flow{TokenURL: srv.URL + "/oidc/token", ClientID: "straza"}
			auth := agentguard.DeviceAuth{DeviceCode: "dc-1", UserCode: "WXYZ-2345", Interval: tc.interval, ExpiresIn: tc.expiresIn}
			start := time.Now()
			tok, err := pollDeviceFlow(ctx, agentguard.NewClient(srv.URL), flow, auth, func(localapi.EnrollEvent) {})
			took := time.Since(start)
			switch {
			case tc.wantErr != nil && !errors.Is(err, tc.wantErr):
				t.Fatalf("err %v, want %v", err, tc.wantErr)
			case tc.wantText != "" && (err == nil || !strings.Contains(err.Error(), tc.wantText)):
				t.Fatalf("err %v, want it to say %q", err, tc.wantText)
			case tc.wantErr == nil && tc.wantText == "" && (err != nil || tok != "id-token-xyz"):
				t.Fatalf("poll: %q, %v", tok, err)
			}
			if got := polls.Load(); got != tc.wantPolls {
				t.Fatalf("%d polls, want %d", got, tc.wantPolls)
			}
			if took < tc.minWait || (tc.maxWait > 0 && took > tc.maxWait) {
				t.Fatalf("took %v, want between %v and %v", took, tc.minWait, tc.maxWait)
			}
		})
	}
}

// TestEnrollAgainSentences pins the enroll command the expired-code
// sentence, the code-lane refusal and the refusal of a server error name:
// the bare command when the managed configuration names a server, because
// the service enrolls there then, and the command with --server and the
// server's address otherwise, because the service refuses an enrollment
// that names no server.
func TestEnrollAgainSentences(t *testing.T) {
	const server = "https://straza.example"
	const (
		expired  = "the sign-in code expired before it was approved. Run `%s` again and approve the new code it shows"
		codeLane = "Straza refused this sign-in because your Straza admin requires a browser sign-in, and a sign-in code " +
			"cannot show which machine it was approved for. Run `%s` in a desktop session on this machine."
		serverError = "The server could not enroll this machine and answered HTTP 500: the store is down. " +
			"Run `%s` again in a minute, and run `straza doctor` if it keeps failing."
	)
	cases := []struct {
		name    string
		managed string // the managed config.yaml, "" for none
		command string
	}{
		{"no managed configuration", "", "straza enroll --server https://straza.example"},
		{"managed configuration names the server", "serverUrl: https://straza.example\n", "straza enroll"},
		{"managed configuration without a server", "snapshotKeys: {}\n", "straza enroll --server https://straza.example"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sys := t.TempDir()
			t.Setenv("STRAZA_SYSTEM", sys)
			if tc.managed != "" {
				if err := os.WriteFile(filepath.Join(sys, "config.yaml"), []byte(tc.managed), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			err := deviceCodeExpired(server)
			if want := fmt.Sprintf(expired, tc.command); !errors.Is(err, errDeviceCodeExpired) || err.Error() != want {
				t.Errorf("deviceCodeExpired = %q, want %q wrapping errDeviceCodeExpired", err, want)
			}
			if got, want := codeLaneRefused(server), fmt.Sprintf(codeLane, tc.command); got != want {
				t.Errorf("codeLaneRefused = %q, want %q", got, want)
			}
			err = enrollRefusal(server, answer{status: http.StatusInternalServerError, body: []byte(`{"error":"the store is down"}`)})
			if want := fmt.Sprintf(serverError, tc.command); err == nil || err.Error() != want {
				t.Errorf("enrollRefusal = %v, want %q", err, want)
			}
		})
	}
}
