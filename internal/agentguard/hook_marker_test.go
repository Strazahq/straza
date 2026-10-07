package agentguard

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/strazahq/straza/internal/policy"
)

// TestHookReasonsCarryOneMarker pins the hook-lane contract on the reasons
// straza writes itself: a fail-closed reason opens with exactly one
// "Straza: " marker. The package errors underneath carry no program prefix
// (the straza command adds the one prefix where it exits), so wrapping one
// into a reason never doubles the marker.
func TestHookReasonsCarryOneMarker(t *testing.T) {
	toolPre := Normalized{HarnessName: "claude-code", HarnessVersion: "2.1",
		Event: policy.Event{Kind: policy.EventToolPre, Tool: policy.ToolShellExec, Command: "git status"}}
	enrolledConfig := func(t *testing.T, store *Store) {
		t.Helper()
		if err := store.SaveConfig(Config{ServerURL: "http://127.0.0.1:1"}); err != nil {
			t.Fatal(err)
		}
	}
	tests := []struct {
		name         string
		seed         func(t *testing.T, store *Store)
		sessionStart bool   // drive session.start through the hook instead of a tool.pre decision
		want         string // what follows the single marker
	}{
		{
			name: "revoked session",
			seed: func(t *testing.T, store *Store) {
				if err := store.MarkRevoked("kill-switch push from the server"); err != nil {
					t.Fatal(err)
				}
			},
			want: "session revoked (kill-switch push from the server). Tool calls stay denied until a new session starts and checks in again. If only this session was revoked, that check-in starts a new session. If the device or user was disabled, the check-in is refused until an administrator re-enables it. Inform the user and stop.",
		},
		{
			name: "no session",
			seed: func(*testing.T, *Store) {},
			want: "no active Straza session. Restart the session so straza can check in",
		},
		{
			name: "session without a cached snapshot",
			seed: func(t *testing.T, store *Store) {
				enrolledConfig(t, store)
				if err := store.SaveSession(Session{SessionID: "s1", SessionToken: "tok", ExpiresAt: time.Now().Add(time.Hour)}); err != nil {
					t.Fatal(err)
				}
			},
			want: "this session is live, but the policy snapshot is missing on this machine",
		},
		{
			name:         "session start without an enrollment",
			seed:         enrolledConfig,
			sessionStart: true,
			want:         "not enrolled (run `straza enroll`): ",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("STRAZA_HOME", t.TempDir())
			store, err := OpenStore()
			if err != nil {
				t.Fatal(err)
			}
			tt.seed(t, store)
			var reason string
			if tt.sessionStart {
				var out, errb bytes.Buffer
				err := runHook(HookIO{
					Stdin:   strings.NewReader(`{"hook_event_name":"SessionStart","cwd":"/w"}`),
					Stdout:  &out,
					Stderr:  &errb,
					Environ: func() []string { return []string{"CLAUDECODE=1"} },
				}, store, liveDecider{store: store})
				if denyCode(t, err) != 2 {
					t.Fatalf("session start must fail closed, got err=%v stdout=%q", err, out.String())
				}
				reason = strings.TrimSpace(errb.String())
			} else {
				reason = liveDecider{store: store}.Decide(toolPre).Reason
			}
			rest, ok := strings.CutPrefix(reason, "Straza: ")
			if !ok {
				t.Fatalf("reason = %q, want it to open with the Straza: marker", reason)
			}
			if strings.HasPrefix(rest, "Straza: ") || strings.HasPrefix(rest, "straza: ") {
				t.Errorf("reason = %q carries the marker twice", reason)
			}
			if !strings.HasPrefix(rest, tt.want) {
				t.Errorf("reason = %q, want %q after the marker", reason, tt.want)
			}
		})
	}
}
