package agentguard

import (
	"bytes"
	"context"
	"runtime"
	"strings"
	"testing"

	"github.com/strazahq/straza/internal/agentguard/localapi"
	"github.com/strazahq/straza/internal/policy"
)

// execFixture starts a fake service that decides with policyYAML, the same
// shape the hook flow tests use, so Exec decides through it with no server
// involved.
func execFixture(t *testing.T, policyYAML string) *fakeService {
	t.Helper()
	svc := newFakeService(t)
	svc.engine = compilePolicy(t, policyYAML)
	return svc
}

const execDenyPolicy = `
apiVersion: straza.dev/v1beta1
kind: PolicySet
metadata: {name: exec-guard}
spec:
  rules:
    - id: no-rm-rf
      tools: [shell.exec]
      command: {denyPatterns: ["rm -rf *"]}
      effect: deny
      reason: "Straza: destructive delete blocked"
    - id: allow-the-rest
      tools: [shell.exec]
      effect: allow
      reason: "dev lane"
`

// shellExit returns a portable argv that exits with the given code.
func shellExit(code string) []string {
	if runtime.GOOS == "windows" {
		return []string{"cmd", "/c", "exit", code}
	}
	return []string{"sh", "-c", "exit " + code}
}

func TestExecDenyBlocksAndAudits(t *testing.T) {
	svc := execFixture(t, execDenyPolicy)
	var errOut bytes.Buffer
	code := Exec(context.Background(), []string{"rm", "-rf", "/tmp/x"}, &errOut)
	if code != execDenyExit {
		t.Fatalf("deny exit = %d, want %d", code, execDenyExit)
	}
	if errOut.String() != "Straza: destructive delete blocked\n" {
		t.Errorf("deny line = %q, want the reason behind one Straza marker", errOut.String())
	}
	// The wrapper starts its own session first, then the service decides
	// (and records) one canonical shell.exec event under that harness.
	want := localapi.Harness{Name: execHarness}
	if starts := svc.started(); len(starts) != 1 || starts[0].Harness != want {
		t.Fatalf("session starts = %+v, want one for %+v", starts, want)
	}
	got := svc.decided()
	if len(got) != 1 || got[0].Harness != want || got[0].Event.Tool != policy.ToolShellExec ||
		strings.Join(got[0].Event.Argv, " ") != "rm -rf /tmp/x" {
		t.Fatalf("decide requests = %+v", got)
	}
}

// TestExecDenyLineKeepsOneMarker pins the deny line's marker: a reason that
// already opens with the word Straza, as the seeded starter policy's does,
// is printed as it is, and everything else gets exactly one "Straza: ".
func TestExecDenyLineKeepsOneMarker(t *testing.T) {
	tests := []struct {
		name, reason, want string
	}{
		{"colon marker", "Straza: destructive delete blocked", "Straza: destructive delete blocked"},
		{"starter policy", "Straza starter policy: recursive force-delete is blocked.", "Straza starter policy: recursive force-delete is blocked."},
		{"service sentence", "Straza's background service isn't running on this machine.", "Straza's background service isn't running on this machine."},
		{"bare reason", "recursive force-delete is blocked", "Straza: recursive force-delete is blocked"},
		{"lowercase is not the marker", "straza says no", "Straza: straza says no"},
		{"empty reason", "", "Straza: denied by policy"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := denyLine(tt.reason); got != tt.want {
				t.Errorf("denyLine(%q) = %q, want %q", tt.reason, got, tt.want)
			}
		})
	}
}

func TestExecAllowRunsAndPassesExitCode(t *testing.T) {
	execFixture(t, execDenyPolicy)
	var errOut bytes.Buffer
	if code := Exec(context.Background(), shellExit("0"), &errOut); code != 0 {
		t.Fatalf("allowed command exit = %d, stderr=%q", code, errOut.String())
	}
	// Child exit codes pass through untouched, because scripts depend on them.
	if code := Exec(context.Background(), shellExit("7"), &errOut); code != 7 {
		t.Fatalf("exit-code passthrough = %d, want 7", code)
	}
}

func TestExecSpawnFailure(t *testing.T) {
	execFixture(t, execDenyPolicy)
	var errOut bytes.Buffer
	code := Exec(context.Background(), []string{"straza-definitely-not-a-binary-xyz"}, &errOut)
	if code != execSpawnExit {
		t.Fatalf("spawn-failure exit = %d, want %d", code, execSpawnExit)
	}
	if errOut.Len() == 0 {
		t.Error("spawn failure printed nothing")
	}
}

func TestExecNoSessionFailsClosed(t *testing.T) {
	// No state directory, no service, or a service that refuses the session:
	// the wrapper must refuse to run ANYTHING (fail closed) with an actionable
	// remedy, and the refusal reads as closed sentences.
	tests := []struct {
		name   string
		seed   func(t *testing.T)
		remedy string
	}{
		{"no state directory", func(t *testing.T) {
			t.Setenv("STRAZA_HOME", "")
			t.Setenv("HOME", "")
			t.Setenv("USERPROFILE", "") // windows UserHomeDir
		}, "Straza: state unavailable (locate home: "},
		{"no service", func(t *testing.T) { noService(t) }, "Start it with `straza service`"},
		{"not enrolled", func(t *testing.T) {
			newFakeService(t).refuse = "This machine isn't enrolled for you yet. Run `straza enroll` and sign in with your work account."
		}, "Run `straza enroll`"},
		{"a disabled user", func(t *testing.T) {
			newFakeService(t).refuse = "this user is disabled, so Straza refuses this request. Ask your IGA owner or a Straza administrator to enable the user."
		}, "Straza: this user is disabled, so Straza refuses this request. Ask your IGA owner or a Straza administrator to enable the user. Refusing to run.\n"},
		{"a refusal without a closing period", func(t *testing.T) {
			newFakeService(t).refuse = "user is disabled. Contact your administrator"
		}, "Straza: user is disabled. Contact your administrator. Refusing to run.\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.seed(t)
			var errOut bytes.Buffer
			code := Exec(context.Background(), shellExit("0"), &errOut)
			if code != execDenyExit {
				t.Fatalf("exit = %d, want %d (fail closed)", code, execDenyExit)
			}
			if !strings.HasPrefix(errOut.String(), "Straza") || strings.HasPrefix(errOut.String(), "Straza: Straza") ||
				!strings.Contains(errOut.String(), tt.remedy) ||
				!strings.HasSuffix(errOut.String(), ". Refusing to run.\n") {
				t.Errorf("refusal = %q, want the marker, the remedy %q and the refusal", errOut.String(), tt.remedy)
			}
		})
	}
}

func TestExecEmptyArgv(t *testing.T) {
	execFixture(t, execDenyPolicy)
	var errOut bytes.Buffer
	if code := Exec(context.Background(), nil, &errOut); code != execDenyExit {
		t.Fatalf("empty argv exit = %d, want %d", code, execDenyExit)
	}
}

// TestExecDecisionParityWithHooks pins the cross-tier promise: the same
// command yields the same decision whether it arrives as a claude-code hook
// payload or through the exec wrapper (the engine sees one canonical event).
func TestExecDecisionParityWithHooks(t *testing.T) {
	svc := execFixture(t, execDenyPolicy)
	d := serviceDecider{store: svc.store(t), svc: NewServiceClient()}
	n := Normalized{
		HarnessName: "claude-code",
		Event: policy.Event{
			Kind: policy.EventToolPre, Tool: "shell.exec",
			Command: "rm -rf /tmp/x", Argv: []string{"rm", "-rf", "/tmp/x"},
		},
	}
	hookDec := d.Decide(n)
	var errOut bytes.Buffer
	execCode := Exec(context.Background(), []string{"rm", "-rf", "/tmp/x"}, &errOut)
	if hookDec.Effect != policy.EffectDeny || execCode != execDenyExit {
		t.Fatalf("parity broken: hook=%+v exec=%d", hookDec, execCode)
	}
}
