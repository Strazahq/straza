package connect

import (
	"context"
	"os"
	"strings"
	"testing"
)

// TestRun walks the verb's branches against the scripted server: what each
// one sends and the one line it prints.
func TestRun(t *testing.T) {
	yes, no := true, false
	token := Status{App: "github", Kind: "token", Agents: "sponsor"}
	oauth := Status{App: "midpoint", Kind: "oauth", Provider: "keycloak", Agents: "own", Connected: true, UpdatedAt: "2026-09-20T10:00:00Z"}
	cases := []struct {
		name    string
		opts    Options
		stdin   string
		wantOut string
		wantErr string
		check   func(t *testing.T, m *serverMock)
	}{
		{
			name:    "no server lists the connections",
			opts:    Options{},
			wantOut: "midpoint  oauth  keycloak  true       -            never    own, not allowed  -",
		},
		{
			name:    "allow agents changes only the opt-in",
			opts:    Options{Server: "midpoint", AllowAgents: &yes},
			wantOut: "agents may use your midpoint connection where the server permits it",
			check: func(t *testing.T, m *serverMock) {
				if m.patched["allow_agents"] != true || m.pasted != nil || m.removed != "" {
					t.Errorf("patched = %v, pasted = %v, removed = %q", m.patched, m.pasted, m.removed)
				}
			},
		},
		{
			name:    "allow agents false takes it back for a named user",
			opts:    Options{Server: "midpoint", User: "joe", AllowAgents: &no},
			wantOut: "agents may no longer use joe's midpoint connection",
			check: func(t *testing.T, m *serverMock) {
				if m.patched["allow_agents"] != false || m.patched["user"] != "joe" {
					t.Errorf("patched = %v", m.patched)
				}
			},
		},
		{
			name:    "a token server takes the token from the pipe",
			opts:    Options{Server: "github", Expires: "2026-12-31"},
			stdin:   "  tok-NEVER-LEAK\n",
			wantOut: "connected github for alice, token fingerprint a1c4",
			check: func(t *testing.T, m *serverMock) {
				if m.pasted["token"] != "tok-NEVER-LEAK" || m.pasted["expires_at"] != "2026-12-31T00:00:00Z" {
					t.Errorf("pasted = %v", m.pasted)
				}
			},
		},
		{
			name:    "nobody signs in for another user",
			opts:    Options{Server: "midpoint", User: "joe"},
			wantErr: "midpoint uses sign-in, which needs joe's own browser, so nobody can connect on their behalf",
		},
		{
			name:    "a bad expiry is refused before the token is read",
			opts:    Options{Server: "github", Expires: "31.12.2026"},
			wantErr: `--expires must be a date as YYYY-MM-DD, got "31.12.2026"`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := &serverMock{lists: [][]Status{{token, oauth}}}
			api := m.server(t, 600)
			r, w, err := os.Pipe()
			if err != nil {
				t.Fatal(err)
			}
			_, _ = w.WriteString(tc.stdin)
			_ = w.Close()
			defer func() { _ = r.Close() }()

			var out, prompt strings.Builder
			o := tc.opts
			o.Tool, o.In, o.Out, o.Err = "straza", r, &out, &prompt
			err = Run(context.Background(), api, o)
			if tc.wantErr != "" {
				if err == nil || err.Error() != tc.wantErr {
					t.Fatalf("err = %v, want %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("Run: %v", err)
			}
			if !strings.Contains(out.String(), tc.wantOut) {
				t.Errorf("output missing %q:\n%s", tc.wantOut, out.String())
			}
			if strings.Contains(out.String()+prompt.String(), "tok-NEVER-LEAK") {
				t.Error("the token reached the output")
			}
			if tc.check != nil {
				tc.check(t, m)
			}
		})
	}
}

// TestReadTokenFromPipe: a token piped on stdin is read whole and trimmed, an
// empty pipe names the two ways to give one with the tool that was run, and
// nothing is ever taken from an argument.
func TestReadTokenFromPipe(t *testing.T) {
	cases := []struct {
		name    string
		input   string
		want    string
		wantErr string
	}{
		{name: "trimmed", input: "  tok-NEVER-LEAK\n", want: "tok-NEVER-LEAK"},
		{name: "empty", input: "\n", wantErr: `no token given. Paste it at the prompt, or pipe it on stdin: printf '%s' "$TOKEN" | straza connect github`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r, w, err := os.Pipe()
			if err != nil {
				t.Fatal(err)
			}
			if _, err := w.WriteString(tc.input); err != nil {
				t.Fatal(err)
			}
			_ = w.Close()
			var prompt strings.Builder
			got, err := readToken("github", "straza", r, &prompt)
			_ = r.Close()
			if tc.wantErr != "" {
				if err == nil || err.Error() != tc.wantErr {
					t.Fatalf("err = %v, want %q", err, tc.wantErr)
				}
				return
			}
			if err != nil || got != tc.want {
				t.Fatalf("readToken = %q, %v; want %q", got, err, tc.want)
			}
			if prompt.Len() != 0 {
				t.Errorf("a pipe read printed a prompt: %q", prompt.String())
			}
		})
	}
}

// TestExpiryRFC3339: the --expires date becomes the start of that day in
// UTC, and anything else is refused with the shape named.
func TestExpiryRFC3339(t *testing.T) {
	if got, err := expiryRFC3339("2026-12-31"); err != nil || got != "2026-12-31T00:00:00Z" {
		t.Errorf("expiryRFC3339 = %q, %v", got, err)
	}
	if got, err := expiryRFC3339(""); err != nil || got != "" {
		t.Errorf("empty expiry = %q, %v", got, err)
	}
	if _, err := expiryRFC3339("31.12.2026"); err == nil || !strings.Contains(err.Error(), "--expires must be a date as YYYY-MM-DD") {
		t.Errorf("bad expiry err = %v", err)
	}
}

// TestLines pins the one-line answers of a paste and an opt-in.
func TestLines(t *testing.T) {
	got := tokenLine(TokenResult{App: "github", User: "alice", Fingerprint: "a1c4", ExpiresAt: "2026-12-31T00:00:00Z", AllowAgents: true})
	if want := "connected github for alice, token fingerprint a1c4, expires 2026-12-31, agents allowed"; got != want {
		t.Errorf("tokenLine = %q, want %q", got, want)
	}
	if got, want := agentsCell(Status{Agents: "sponsor", Connected: true}), "sponsor, not allowed"; got != want {
		t.Errorf("agentsCell = %q, want %q", got, want)
	}
}

// TestDisconnect: the verb removes one named connection, for the caller or
// for the user an administrator or a sponsor names, and with no server named
// it removes nothing and says what to type.
func TestDisconnect(t *testing.T) {
	connected := Status{App: "midpoint", Kind: "oauth", Connected: true}
	idle := Status{App: "github", Kind: "token"}
	cases := []struct {
		name        string
		opts        Options
		list        []Status
		wantOut     string
		wantRemoved string
		wantErr     string
	}{
		{name: "the caller's own", opts: Options{Tool: "straza", Server: "midpoint"}, list: []Status{connected},
			wantOut: "disconnected midpoint\n", wantRemoved: "midpoint?"},
		{name: "another user's, by a sponsor or an administrator", opts: Options{Tool: "strazactl", Server: "midpoint", User: "joe"}, list: []Status{connected},
			wantOut: "disconnected midpoint\n", wantRemoved: "midpoint?user=joe"},
		{name: "no server named lists the connected ones", opts: Options{Tool: "straza"}, list: []Status{idle, connected},
			wantErr: "name the MCP server to disconnect from. You are connected to midpoint. Run `straza disconnect midpoint`"},
		{name: "no server named and nothing connected", opts: Options{Tool: "straza"}, list: []Status{idle},
			wantErr: "name the MCP server to disconnect from. You have no connections, and `straza connect` lists the servers"},
		{name: "no server named for another user", opts: Options{Tool: "strazactl", User: "joe"}, list: []Status{connected},
			wantErr: "name the MCP server to disconnect from. joe is connected to midpoint. Run `strazactl disconnect midpoint --user joe`"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := &serverMock{lists: [][]Status{tc.list}}
			api := m.server(t, 600)
			var out strings.Builder
			o := tc.opts
			o.Out = &out
			err := Disconnect(context.Background(), api, o)
			if tc.wantErr != "" {
				if err == nil || err.Error() != tc.wantErr {
					t.Fatalf("err = %v, want %q", err, tc.wantErr)
				}
			} else if err != nil {
				t.Fatalf("Disconnect: %v", err)
			}
			if out.String() != tc.wantOut {
				t.Errorf("output = %q, want %q", out.String(), tc.wantOut)
			}
			if m.removed != tc.wantRemoved {
				t.Errorf("removed = %q, want %q", m.removed, tc.wantRemoved)
			}
		})
	}
}
