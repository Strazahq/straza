package ctl

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// credsAt writes a credentials file holding server and returns its path. An
// empty server writes a credentials file with no server recorded (the shape a
// pre-server-field login left behind).
func credsAt(t *testing.T, server string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "credentials.json")
	body := `{"server":"` + server + `","session_token":"stok-1","session_id":"ses-1"}`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write creds: %v", err)
	}
	return path
}

// TestResolveTargetOrder pins the resolution order the CLI promises:
// --server > $STRAZA_SERVER > the server `strazactl login` stored > a loud
// error. There is deliberately no localhost fallback: a silent one would aim
// admin commands at the wrong deployment as the wrong identity.
func TestResolveTargetOrder(t *testing.T) {
	const (
		flagSrv  = "https://flag.example"
		envSrv   = "https://env.example"
		loginSrv = "https://login.example"
	)
	tests := []struct {
		name        string
		flag, env   string
		creds       string // "" = no credentials file at all
		noCredsFile bool
		wantServer  string
		wantSource  Source
		wantLogin   string
		wantErr     error
	}{
		{
			name: "flag beats env and credentials",
			flag: flagSrv, env: envSrv, creds: loginSrv,
			wantServer: flagSrv, wantSource: SourceFlag, wantLogin: loginSrv,
		},
		{
			name: "env beats credentials",
			env:  envSrv, creds: loginSrv,
			wantServer: envSrv, wantSource: SourceEnv, wantLogin: loginSrv,
		},
		{
			name:       "credentials used when nothing overrides",
			creds:      loginSrv,
			wantServer: loginSrv, wantSource: SourceCredentials, wantLogin: loginSrv,
		},
		{
			name: "flag alone, never logged in",
			flag: flagSrv, noCredsFile: true,
			wantServer: flagSrv, wantSource: SourceFlag, wantLogin: "",
		},
		{
			name: "env alone, never logged in",
			env:  envSrv, noCredsFile: true,
			wantServer: envSrv, wantSource: SourceEnv, wantLogin: "",
		},
		{
			name:        "nothing at all is a loud error, not localhost",
			noCredsFile: true, wantErr: ErrNoServer,
		},
		{
			name:    "credentials file present but records no server",
			creds:   "",
			wantErr: ErrNoServer,
		},
		{
			name: "trailing slashes are normalized away everywhere",
			flag: "https://flag.example/", creds: "https://login.example/",
			wantServer: flagSrv, wantSource: SourceFlag, wantLogin: loginSrv,
		},
		{
			name: "surrounding whitespace is trimmed",
			env:  "  https://env.example  ", noCredsFile: true,
			wantServer: envSrv, wantSource: SourceEnv,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			credsPath := filepath.Join(t.TempDir(), "absent.json")
			if !tc.noCredsFile {
				credsPath = credsAt(t, tc.creds)
			}
			got, err := ResolveTarget(tc.flag, tc.env, credsPath)
			if tc.wantErr != nil {
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("err = %v, want %v", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("ResolveTarget: %v", err)
			}
			if got.Server != tc.wantServer {
				t.Errorf("Server = %q, want %q", got.Server, tc.wantServer)
			}
			if got.Source != tc.wantSource {
				t.Errorf("Source = %q, want %q", got.Source, tc.wantSource)
			}
			if got.LoginServer != tc.wantLogin {
				t.Errorf("LoginServer = %q, want %q", got.LoginServer, tc.wantLogin)
			}
		})
	}
}

// TestResolveTargetIgnoresBrokenCredentials keeps an unreadable or corrupt
// credentials file from failing a call that supplied its own server: the file
// only ever contributes a default, so "not logged in" is the right reading.
func TestResolveTargetIgnoresBrokenCredentials(t *testing.T) {
	path := filepath.Join(t.TempDir(), "credentials.json")
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatalf("write creds: %v", err)
	}
	got, err := ResolveTarget("https://flag.example", "", path)
	if err != nil {
		t.Fatalf("ResolveTarget: %v", err)
	}
	if got.Server != "https://flag.example" || got.LoginServer != "" {
		t.Fatalf("got %+v, want the flag server and no login server", got)
	}
	if _, err := ResolveTarget("", "", path); !errors.Is(err, ErrNoServer) {
		t.Fatalf("corrupt creds with no override: err = %v, want ErrNoServer", err)
	}
}

// TestTargetOverridden pins when the CLI owes the operator a mismatch warning:
// only when --server or $STRAZA_SERVER aims somewhere other than the server
// the stored credentials belong to.
func TestTargetOverridden(t *testing.T) {
	tests := []struct {
		name   string
		target Target
		want   bool
	}{
		{"flag disagrees with login", Target{Server: "https://a", Source: SourceFlag, LoginServer: "https://b"}, true},
		{"env disagrees with login", Target{Server: "https://a", Source: SourceEnv, LoginServer: "https://b"}, true},
		{"flag agrees with login", Target{Server: "https://a", Source: SourceFlag, LoginServer: "https://a"}, false},
		{"env agrees with login", Target{Server: "https://a", Source: SourceEnv, LoginServer: "https://a"}, false},
		{"flag but never logged in", Target{Server: "https://a", Source: SourceFlag}, false},
		{"credentials are never an override", Target{Server: "https://a", Source: SourceCredentials, LoginServer: "https://a"}, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.target.Overridden(); got != tc.want {
				t.Errorf("Overridden() = %v, want %v", got, tc.want)
			}
		})
	}
}
