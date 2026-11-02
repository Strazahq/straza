package main

import (
	"bytes"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"runtime"
	"testing"

	"github.com/strazahq/straza/internal/agentguard"
	"github.com/strazahq/straza/internal/agentguard/localapi"
)

// TestEnrollLaneRule pins the lane `straza enroll` asks the service for:
// the browser when one can be opened, the code lane only when the person
// passes --use-device-code, a stop with the no-display sentence otherwise,
// and no lane for an agent.
func TestEnrollLaneRule(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("the no-display rule applies on Linux")
	}
	t.Setenv("STRAZA_SYSTEM", "")
	if localapi.SystemPresent() || agentguard.ManagedInstalled() {
		t.Skip("this machine has a system service or a managed layout, so enroll would ask the real service")
	}
	cases := []struct {
		name     string
		args     []string
		browser  string
		wantLane string
		wantErr  string
		asked    bool
	}{
		{name: "no display and no flag", wantErr: noDisplay},
		{name: "a browser can be opened", browser: "/bin/true", wantLane: localapi.LaneBrowser, asked: true},
		{name: "the person picks the code lane", args: []string{"--use-device-code"}, wantLane: localapi.LaneDeviceCode, asked: true},
		{name: "an agent has no lane", args: []string{"--headless", "--user", "ci-bot"}, asked: true},
		{name: "an agent cannot pick the code lane", args: []string{"--headless", "--use-device-code"},
			wantErr: "--use-device-code is for a person's sign-in, and --headless enrolls an agent with its key. " +
				"Run `straza enroll --headless` for an agent, or `straza enroll --use-device-code` for a person."},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("DISPLAY", "")
			t.Setenv("WAYLAND_DISPLAY", "")
			t.Setenv("BROWSER", tc.browser)
			t.Setenv("STRAZA_SERVER", "https://straza.example")
			got := fakeEnrollService(t)
			cmd := enrollCmd()
			cmd.SetOut(&bytes.Buffer{})
			cmd.SetErr(&bytes.Buffer{})
			cmd.SetArgs(tc.args)
			cmd.SilenceUsage, cmd.SilenceErrors = true, true
			err := cmd.Execute()
			select {
			case req := <-got:
				if !tc.asked {
					t.Fatalf("the service was asked with %+v", req)
				}
				if req.Lane != tc.wantLane {
					t.Errorf("lane = %q, want %q", req.Lane, tc.wantLane)
				}
			default:
				if tc.asked {
					t.Fatalf("the service was not asked (err = %v)", err)
				}
			}
			if tc.wantErr != "" && (err == nil || err.Error() != tc.wantErr) {
				t.Errorf("err = %v\nwant %q", err, tc.wantErr)
			}
		})
	}
}

// fakeEnrollService answers the enroll route of a user-mode service in a
// fresh STRAZA_HOME with an error event, and hands over each request.
func fakeEnrollService(t *testing.T) <-chan localapi.EnrollRequest {
	t.Helper()
	home, err := os.MkdirTemp("", "straza-svc-") // a socket path must stay short
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(home) })
	t.Setenv("STRAZA_HOME", home)
	socket, _, _ := localapi.Paths(home)
	ln, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	got := make(chan localapi.EnrollRequest, 1)
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req localapi.EnrollRequest
		if r.URL.Path != localapi.RouteEnroll || json.NewDecoder(r.Body).Decode(&req) != nil {
			http.Error(w, "unexpected request", http.StatusBadRequest)
			return
		}
		got <- req
		_ = json.NewEncoder(w).Encode(localapi.EnrollEvent{Event: localapi.EnrollError, Error: "refused by the test"})
	}))
	_ = srv.Listener.Close()
	srv.Listener = ln
	srv.Start()
	t.Cleanup(srv.Close)
	return got
}
