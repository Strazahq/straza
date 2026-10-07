package main

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
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
		name           string
		flag, env      string
		wantServer     string
		wantSource     enrollSource
		wantAnnounce   string
		wantFailClosed bool
	}{
		{name: "flag only", flag: flagURL, wantServer: flagURL, wantSource: enrollSourceFlag},
		{name: "env only", env: envURL, wantServer: envURL, wantSource: enrollSourceEnv,
			wantAnnounce: "enrolling at " + envURL + " (from $STRAZA_SERVER)"},
		{name: "flag beats env", flag: flagURL, env: envURL, wantServer: flagURL, wantSource: enrollSourceFlag},
		{name: "neither fails closed", wantSource: enrollSourceNone, wantFailClosed: true},
		{name: "blank env is not a server", env: "   ", wantSource: enrollSourceNone, wantFailClosed: true},
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
			if (got.Server == "") != tt.wantFailClosed {
				t.Errorf("fail-closed = %v, want %v", got.Server == "", tt.wantFailClosed)
			}
			if line := got.Announce(); line != tt.wantAnnounce {
				t.Errorf("Announce() = %q, want %q", line, tt.wantAnnounce)
			}
		})
	}
}

// TestEnrollAnnouncesEnvServer drives the real command so the announcement is
// pinned where it has to happen: on stderr, exactly once, BEFORE the device
// flow contacts anything or the store is touched. The flow itself is expected
// to fail here (the fake strazad serves no discovery document), which is
// precisely the case where a silent enrolment left the operator guessing which
// server it had even tried.
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
		t.Fatal("enroll against a server with no discovery document must fail")
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
		t.Fatal("enroll against a server with no discovery document must fail")
	}
	if strings.Contains(stderr.String(), "enrolling at") {
		t.Errorf("--server must not be announced, got %q", stderr.String())
	}
}

// TestEnrollWithoutAServerIsLoud: no --server and no $STRAZA_SERVER must fail
// closed naming both, never guess a localhost.
func TestEnrollWithoutAServerIsLoud(t *testing.T) {
	t.Setenv("STRAZA_HOME", t.TempDir())
	t.Setenv("STRAZA_SERVER", "")

	cmd := enrollCmd()
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs(nil)
	cmd.SilenceUsage, cmd.SilenceErrors = true, true
	err := cmd.Execute()
	if err == nil {
		t.Fatal("enroll with no server anywhere must fail")
	}
	if !strings.Contains(err.Error(), "--server is required") || !strings.Contains(err.Error(), "STRAZA_SERVER") {
		t.Errorf("error = %q, want it to name both ways to set the server", err)
	}
}
