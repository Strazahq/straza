package agentguard

import (
	"bytes"
	"strings"
	"testing"

	"github.com/strazahq/straza/internal/policy"
)

// TestHookReasonsCarryOneMarker pins the hook-lane contract on the reasons
// straza writes itself: a fail-closed reason opens with exactly one Straza
// marker. A sentence that already opens with Straza is the whole reason, and
// any other gets "Straza: " in front, whether the Straza background service
// could not be reached or refused.
func TestHookReasonsCarryOneMarker(t *testing.T) {
	toolPre := Normalized{HarnessName: "claude-code", HarnessVersion: "2.1",
		Event: policy.Event{Kind: policy.EventToolPre, Tool: policy.ToolShellExec, Command: "git status"}}
	const notEnrolled = "This machine isn't enrolled for you yet. Run `straza enroll` and sign in with your work account."
	notRunning := func(home string) string {
		return "Straza's background service for STRAZA_HOME " + home + " isn't running, so this command can't reach your Straza session. " +
			"Start it with `straza service` in a shell that has the same STRAZA_HOME."
	}
	refusing := func(sentence string) func(t *testing.T) string {
		return func(t *testing.T) string {
			svc := newFakeService(t)
			svc.refuse = sentence
			return svc.home
		}
	}
	const noSession = "no active Straza session. Restart the session so straza can check in"
	const waiting = "Straza is waiting for the server to confirm this machine's last check-in, which takes up to two minutes after a lost connection. Start the session again in a minute."
	tests := []struct {
		name         string
		seed         func(t *testing.T) (home string)
		sessionStart bool // drive session.start through the hook instead of a tool.pre decision
		want         func(home string) string
		whole        bool // the sentence opens with Straza and is the whole reason
	}{
		{
			name: "lower-case refusal",
			seed: refusing(noSession),
			want: func(string) string { return noSession },
		},
		{
			name:  "a refusal that names Straza already",
			seed:  refusing(waiting),
			want:  func(string) string { return waiting },
			whole: true,
		},
		{
			name:  "service not running",
			seed:  noService,
			want:  notRunning,
			whole: true,
		},
		{
			name: "service refuses",
			seed: func(t *testing.T) string {
				svc := newFakeService(t)
				svc.refuse = notEnrolled
				return svc.home
			},
			want: func(string) string { return notEnrolled },
		},
		{
			name:         "session start, service not running",
			seed:         noService,
			sessionStart: true,
			want:         notRunning,
			whole:        true,
		},
		{
			name: "session start refused",
			seed: func(t *testing.T) string {
				svc := newFakeService(t)
				svc.refuse = notEnrolled
				return svc.home
			},
			sessionStart: true,
			want:         func(string) string { return notEnrolled },
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			home := tt.seed(t)
			store, err := OpenStore()
			if err != nil {
				t.Fatal(err)
			}
			svc := NewServiceClient()
			var reason string
			if tt.sessionStart {
				var out, errb bytes.Buffer
				err := runHook(HookIO{
					Stdin:   strings.NewReader(`{"hook_event_name":"SessionStart","cwd":"/w"}`),
					Stdout:  &out,
					Stderr:  &errb,
					Environ: func() []string { return []string{"CLAUDECODE=1"} },
				}, store, svc, serviceDecider{store: store, svc: svc})
				if denyCode(t, err) != 2 {
					t.Fatalf("session start must fail closed, got err=%v stdout=%q", err, out.String())
				}
				reason = strings.TrimSpace(errb.String())
			} else {
				reason = serviceDecider{store: store, svc: svc}.Decide(toolPre).Reason
			}
			if strings.HasPrefix(reason, "Straza: Straza") {
				t.Fatalf("reason = %q carries the marker twice", reason)
			}
			if tt.whole {
				if reason != tt.want(home) {
					t.Errorf("reason = %q, want %q as it is", reason, tt.want(home))
				}
				return
			}
			rest, ok := strings.CutPrefix(reason, "Straza: ")
			if !ok {
				t.Fatalf("reason = %q, want it to open with the Straza: marker", reason)
			}
			if strings.HasPrefix(rest, "Straza: ") || strings.HasPrefix(rest, "straza: ") {
				t.Errorf("reason = %q carries the marker twice", reason)
			}
			if want := tt.want(home); rest != want {
				t.Errorf("reason = %q, want %q after the marker", reason, want)
			}
		})
	}
}
