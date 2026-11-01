package main

import (
	"bytes"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/strazahq/straza/internal/agentguard"
	"github.com/strazahq/straza/internal/agentguard/localapi"
)

// TestResolveEnrollServer pins enroll's target resolution and its one
// announcement. Enroll PERSISTS what it resolves, so a $STRAZA_SERVER export
// honoured in silence would enroll a device against the wrong deployment and
// write that server into the agent config, with nothing on screen either
// time. --server stays silent on purpose: it is on the command line the
// operator just typed.
func TestResolveEnrollServer(t *testing.T) {
	const (
		flagURL = "https://flag.example:8420"
		envURL  = "https://env.example:8420"
	)
	tests := []struct {
		name         string
		flag, env    string
		wantServer   string
		wantSource   enrollSource
		wantAnnounce string
	}{
		{name: "flag only", flag: flagURL, wantServer: flagURL, wantSource: enrollSourceFlag},
		{name: "env only", env: envURL, wantServer: envURL, wantSource: enrollSourceEnv,
			wantAnnounce: "enrolling at " + envURL + " (from $STRAZA_SERVER)"},
		{name: "flag beats env", flag: flagURL, env: envURL, wantServer: flagURL, wantSource: enrollSourceFlag},
		{name: "neither names a server", wantSource: enrollSourceNone},
		{name: "blank env is not a server", env: "   ", wantSource: enrollSourceNone},
		{name: "env whitespace trimmed", env: "  " + envURL + "\n", wantServer: envURL, wantSource: enrollSourceEnv,
			wantAnnounce: "enrolling at " + envURL + " (from $STRAZA_SERVER)"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := resolveEnrollServer(tt.flag, tt.env)
			if got.Server != tt.wantServer || got.Source != tt.wantSource {
				t.Fatalf("resolveEnrollServer(%q, %q) = %+v, want {%q %q}",
					tt.flag, tt.env, got, tt.wantServer, tt.wantSource)
			}
			if line := got.Announce(); line != tt.wantAnnounce {
				t.Errorf("Announce() = %q, want %q", line, tt.wantAnnounce)
			}
		})
	}
}

// TestEnrollAnnouncesEnvServer drives the real command so the announcement is
// pinned where it has to happen: on stderr, exactly once, BEFORE anything is
// contacted. The enrollment itself is expected to fail here (no background
// service listens in this STRAZA_HOME), which is precisely the case where a
// silent enrolment left the operator guessing which server it had even tried.
func TestEnrollAnnouncesEnvServer(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "no discovery here", http.StatusNotFound)
	}))
	defer srv.Close()

	t.Setenv("STRAZA_HOME", t.TempDir())
	t.Setenv("STRAZA_SERVER", srv.URL)

	var stdout, stderr bytes.Buffer
	cmd := enrollCmd()
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)
	cmd.SetArgs(nil)
	cmd.SilenceUsage, cmd.SilenceErrors = true, true
	if err := cmd.Execute(); err == nil {
		t.Fatal("enroll with no background service must fail")
	}

	want := "enrolling at " + srv.URL + " (from $STRAZA_SERVER)"
	if got := strings.TrimRight(stderr.String(), "\n"); got != want {
		t.Errorf("stderr = %q, want exactly %q", got, want)
	}
	if n := strings.Count(stderr.String(), "enrolling at"); n != 1 {
		t.Errorf("announced %d times, want exactly 1", n)
	}
}

// TestEnrollFlagServerIsNotAnnounced: the operator typed the URL, so enroll
// adds no line of its own; the announcement is the env fallback's alone.
func TestEnrollFlagServerIsNotAnnounced(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "no discovery here", http.StatusNotFound)
	}))
	defer srv.Close()

	t.Setenv("STRAZA_HOME", t.TempDir())
	t.Setenv("STRAZA_SERVER", "https://stale.example:8420")

	var stdout, stderr bytes.Buffer
	cmd := enrollCmd()
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)
	cmd.SetArgs([]string{"--server", srv.URL})
	cmd.SilenceUsage, cmd.SilenceErrors = true, true
	if err := cmd.Execute(); err == nil {
		t.Fatal("enroll with no background service must fail")
	}
	if strings.Contains(stderr.String(), "enrolling at") {
		t.Errorf("--server must not be announced, got %q", stderr.String())
	}
}

// TestEnrollWithoutAServerAsksTheService: with no --server and no
// $STRAZA_SERVER, enroll still asks the background service, with an empty
// server. A managed machine's service enrolls at the server its managed
// configuration names, and otherwise the service answers why it needs one,
// so the operator sees the service's own sentence.
func TestEnrollWithoutAServerAsksTheService(t *testing.T) {
	t.Setenv("STRAZA_SYSTEM", "")
	if localapi.SystemPresent() || agentguard.ManagedInstalled() {
		t.Skip("this machine has a system service or a managed layout, so enroll would ask the real service")
	}
	home, err := os.MkdirTemp("", "straza-svc-") // a socket path must stay short
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(home) })
	t.Setenv("STRAZA_HOME", home)
	t.Setenv("STRAZA_SERVER", "")
	socket, _, _ := localapi.Paths(home)
	ln, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	const sentence = "Straza needs the address of your Straza server to enroll."
	got := make(chan localapi.EnrollRequest, 1)
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req localapi.EnrollRequest
		if r.URL.Path != localapi.RouteEnroll || json.NewDecoder(r.Body).Decode(&req) != nil {
			http.Error(w, "unexpected request", http.StatusBadRequest)
			return
		}
		got <- req
		_ = json.NewEncoder(w).Encode(localapi.EnrollEvent{Event: localapi.EnrollError, Error: sentence})
	}))
	_ = srv.Listener.Close()
	srv.Listener = ln
	srv.Start()
	t.Cleanup(srv.Close)

	cmd := enrollCmd()
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs([]string{"--use-device-code"})
	cmd.SilenceUsage, cmd.SilenceErrors = true, true
	err = cmd.Execute()
	select {
	case req := <-got:
		if req.ServerURL != "" || req.Lane != localapi.LaneDeviceCode {
			t.Errorf("service request ServerURL = %q, Lane = %q, want empty and device-code", req.ServerURL, req.Lane)
		}
	default:
		t.Fatalf("enroll never asked the service (err = %v)", err)
	}
	if err == nil || err.Error() != sentence {
		t.Errorf("err = %v, want the service's sentence %q", err, sentence)
	}
}
