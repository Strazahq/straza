package ctl

import (
	"errors"
	"testing"
)

// TestResolveServerOrder pins the resolution order the CLI promises:
// --server > $STRAZA_SERVER > a loud error. There is deliberately no
// localhost fallback: a silent one would aim admin commands, and the token
// they carry, at the wrong deployment.
func TestResolveServerOrder(t *testing.T) {
	const (
		flagSrv = "https://flag.example"
		envSrv  = "https://env.example"
	)
	tests := []struct {
		name       string
		flag, env  string
		wantServer string
		wantErr    error
	}{
		{name: "flag beats env", flag: flagSrv, env: envSrv, wantServer: flagSrv},
		{name: "env alone", env: envSrv, wantServer: envSrv},
		{name: "flag alone", flag: flagSrv, wantServer: flagSrv},
		{name: "nothing at all is a loud error, not localhost", wantErr: ErrNoServer},
		{name: "blank values count as unset", flag: "  ", env: "/", wantErr: ErrNoServer},
		{name: "trailing slashes are normalized away", flag: "https://flag.example/", wantServer: flagSrv},
		{name: "surrounding whitespace is trimmed", env: "  https://env.example  ", wantServer: envSrv},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ResolveServer(tc.flag, tc.env)
			if tc.wantErr != nil {
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("err = %v, want %v", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("ResolveServer: %v", err)
			}
			if got != tc.wantServer {
				t.Errorf("server = %q, want %q", got, tc.wantServer)
			}
		})
	}
}
