package agentguard

import (
	"bytes"
	"context"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/strazahq/straza/internal/agentguard/spool"
	"github.com/strazahq/straza/internal/policy"
)

// execFixture boots a local store with a signed snapshot and a live session
// (the same shape the hook flow tests use) so Exec decides offline against
// the verified snapshot, with no server involved.
func execFixture(t *testing.T, policyYAML string) *Store {
	t.Helper()
	priv, keys := testSnapshotKey(t)
	signed, id := testSignedPolicy(t, priv, policyYAML)
	t.Setenv("STRAZA_HOME", t.TempDir())
	store, err := OpenStore()
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SaveConfig(Config{ServerURL: "http://127.0.0.1:0", SnapshotKeys: keys}); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveSnapshot(signed); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveSession(Session{
		SessionID: "s-exec", SessionToken: "tok", SnapshotID: id,
		User: "bob", Roles: []string{"dev"}, Harness: "exec-wrapper/test",
		IssuedAt: time.Now(), ExpiresAt: time.Now().Add(time.Hour),
	}); err != nil {
		t.Fatal(err)
	}
	return store
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
	store := execFixture(t, execDenyPolicy)
	var errOut bytes.Buffer
	code := Exec(context.Background(), []string{"rm", "-rf", "/tmp/x"}, &errOut)
	if code != execDenyExit {
		t.Fatalf("deny exit = %d, want %d", code, execDenyExit)
	}
	if errOut.String() != "Straza: destructive delete blocked\n" {
		t.Errorf("deny line = %q, want the reason behind one Straza marker", errOut.String())
	}
	// The decision is spooled for async audit like any hook decision.
	recs, err := spool.ReadRecords(store.SpoolPath())
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != 1 || !strings.Contains(string(recs[0]), "no-rm-rf") {
		t.Fatalf("audit spool = %v", recs)
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
	// No enrollment, no session, unreachable server: the wrapper must refuse
	// to run ANYTHING (fail closed) with an actionable remedy.
	t.Setenv("STRAZA_HOME", t.TempDir())
	var errOut bytes.Buffer
	code := Exec(context.Background(), shellExit("0"), &errOut)
	if code != execDenyExit {
		t.Fatalf("no-session exit = %d, want %d (fail closed)", code, execDenyExit)
	}
	if !strings.Contains(errOut.String(), "enroll") {
		t.Errorf("no-session error carries no remedy: %q", errOut.String())
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
	store := execFixture(t, execDenyPolicy)
	d := liveDecider{store: store}
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
