package conformance

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/strazahq/straza/internal/agentguard"
)

// TestMain doubles as a hook implementation under test: re-executed with
// STRAZA_CONFORMANCE_HELPER set, the test binary behaves like `straza hook
// --conformance-policy <argv[1]>`, so Straza's own hook is checked without
// building the CLI, or like a broken always-allow hook, which proves the
// runner fails non-conformance.
func TestMain(m *testing.M) {
	if role := os.Getenv("STRAZA_CONFORMANCE_HELPER"); role != "" {
		runHelper(role)
	}
	os.Exit(m.Run())
}

func runHelper(role string) {
	switch role {
	case "agentguard-hook":
		policyFile := ""
		if len(os.Args) > 1 {
			policyFile = os.Args[1]
		}
		err := agentguard.RunHookConformance(agentguard.HookIO{
			Stdin: os.Stdin, Stdout: os.Stdout, Stderr: os.Stderr, Environ: os.Environ,
		}, policyFile)
		if err != nil {
			if coder, ok := err.(interface{ Code() int }); ok {
				os.Exit(coder.Code())
			}
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		os.Exit(0)
	case "allow-all":
		fmt.Println(`{"hookSpecificOutput":{"hookEventName":"PreToolUse","permissionDecision":"allow"}}`)
		os.Exit(0)
	default:
		fmt.Fprintln(os.Stderr, "unknown helper role", role)
		os.Exit(1)
	}
}

func selfCmdline(t *testing.T) string {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	return `"` + exe + `" {policy}`
}

// TestAgentguardPassesTier1 replays the published corpus against Straza's
// own hook in conformance mode: every case must pass.
func TestAgentguardPassesTier1(t *testing.T) {
	t.Setenv("STRAZA_CONFORMANCE_HELPER", "agentguard-hook")
	var out bytes.Buffer
	results, err := RunHookProfile(selfCmdline(t), &out)
	if err != nil {
		t.Fatalf("RunHookProfile: %v", err)
	}
	for _, r := range results {
		if !r.Pass {
			t.Errorf("FAIL %s: %s", r.Name, r.Detail)
		}
	}
	if len(results) < 40 {
		t.Errorf("suite has %d cases, want >= 40 (4 dialects incl. python-sdk; per-event ack cases added 2026-07-31)", len(results))
	}
	if !Passed(results) {
		t.Fatalf("agentguard is not Tier-1 conformant:\n%s", out.String())
	}
	if !strings.Contains(out.String(), "Tier-1 conformance PASS") {
		t.Errorf("summary line missing PASS verdict:\n%s", out.String())
	}
}

// TestHelloHookPassesTier1 pins that the external reference
// implementation, built from spec/conformance/tier1/hello-hook, passes
// Tier-1 conformance using only published docs.
func TestHelloHookPassesTier1(t *testing.T) {
	exe := filepath.Join(t.TempDir(), "hello-hook")
	if runtime.GOOS == "windows" {
		exe += ".exe"
	}
	build := exec.Command("go", "build", "-o", exe,
		"github.com/strazahq/straza/spec/conformance/tier1/hello-hook")
	build.Env = append(os.Environ(), "CGO_ENABLED=0")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("go build hello-hook: %v\n%s", err, out)
	}
	var out bytes.Buffer
	results, err := RunHookProfile(`"`+exe+`"`, &out)
	if err != nil {
		t.Fatalf("RunHookProfile: %v", err)
	}
	for _, r := range results {
		if !r.Pass {
			t.Errorf("FAIL %s: %s", r.Name, r.Detail)
		}
	}
	if !Passed(results) {
		t.Fatalf("hello-hook is not Tier-1 conformant:\n%s", out.String())
	}
}

// TestRunnerFlagsNonConformance proves the runner is not a rubber stamp: an
// always-allow hook must fail every deny and fail-closed case.
func TestRunnerFlagsNonConformance(t *testing.T) {
	t.Setenv("STRAZA_CONFORMANCE_HELPER", "allow-all")
	var out bytes.Buffer
	results, err := RunHookProfile(selfCmdline(t), &out)
	if err != nil {
		t.Fatalf("RunHookProfile: %v", err)
	}
	if Passed(results) {
		t.Fatal("an always-allow hook scored Tier-1 conformant: the runner is broken")
	}
	failed := 0
	for _, r := range results {
		if !r.Pass {
			failed++
		}
	}
	// Every deny and fail-closed case must fail, and so must every
	// silent-ack case: an always-allow hook prints a decision document
	// where claude and codex demand empty stdout. The threshold stays under
	// the exact count so adding allow cases never trips it.
	if failed < 25 {
		t.Errorf("always-allow hook failed only %d cases, want >= 25", failed)
	}
	if !strings.Contains(out.String(), "Tier-1 conformance FAIL") {
		t.Errorf("summary line missing FAIL verdict:\n%s", out.String())
	}
}

func TestJudge(t *testing.T) {
	deny := Want{Decision: "deny"}
	cases := []struct {
		name    string
		c       Case
		exit    int
		stdout  string
		stderr  string
		pass    bool
		details string
	}{
		{"claude allow via json", Case{Harness: "claude-code", Want: Want{Decision: "allow"}},
			0, `{"hookSpecificOutput":{"permissionDecision":"allow"}}`, "", true, ""},
		{"claude allow via silence", Case{Harness: "claude-code", Want: Want{Decision: "allow"}},
			0, "", "", true, ""},
		{"claude allow rejected on exit 2", Case{Harness: "claude-code", Want: Want{Decision: "allow"}},
			2, "", "boom", false, "exit code"},
		{"claude deny via exit 2 + stderr reason", Case{Harness: "claude-code", Want: deny},
			2, "", "blocked because reasons", true, ""},
		{"claude deny via json only", Case{Harness: "claude-code", Want: deny},
			0, `{"hookSpecificOutput":{"permissionDecision":"deny","permissionDecisionReason":"nope"}}`, "", true, ""},
		{"codex deny via json only fails open (exit 2 required)", Case{Harness: "codex", Want: deny},
			0, `{"hookSpecificOutput":{"permissionDecision":"deny","permissionDecisionReason":"nope"}}`, "", false, "exit code 2"},
		{"codex deny needs a stderr reason", Case{Harness: "codex", Want: deny},
			2, `{"hookSpecificOutput":{"permissionDecisionReason":"stdout only"}}`, "", false, "stderr"},
		{"codex deny via exit 2 + stderr reason", Case{Harness: "codex", Want: deny},
			2, "", "blocked because reasons", true, ""},
		{"deny without reason fails", Case{Harness: "codex", Want: deny},
			2, "", "", false, "stderr"},
		{"silent allow satisfied by empty stdout", Case{Harness: "codex", Want: Want{Decision: "allow", Silent: true}},
			0, "", "", true, ""},
		{"silent allow rejects a decision document", Case{Harness: "claude-code", Want: Want{Decision: "allow", Silent: true}},
			0, `{"hookSpecificOutput":{"hookEventName":"SessionEnd"}}`, "", false, "EMPTY stdout"},
		{"deny reasonContains mismatch", Case{Harness: "claude-code", Want: Want{Decision: "deny", ReasonContains: "environment"}},
			2, "", "destructive commands are blocked", false, "does not mention"},
		{"deny reasonContains case-insensitive", Case{Harness: "claude-code", Want: Want{Decision: "deny", ReasonContains: "Destructive Commands"}},
			2, "", "destructive commands are blocked", true, ""},
		{"gemini deny must be strict json", Case{Harness: "gemini", Want: deny},
			2, "", "denied on stderr", false, "strict JSON"},
		{"gemini deny json passes", Case{Harness: "gemini", Want: deny},
			0, `{"decision":"deny","reason":"nope"}`, "", true, ""},
		{"gemini allow json passes", Case{Harness: "gemini", Want: Want{Decision: "allow"}},
			0, `{"decision":"allow"}`, "", true, ""},
		{"block satisfied by exit 1", Case{Harness: "codex", Want: Want{Decision: "block"}},
			1, "", "bad payload", true, ""},
		{"block satisfied by deny json", Case{Harness: "gemini", Want: Want{Decision: "block"}},
			0, `{"decision":"deny","reason":"bad payload"}`, "", true, ""},
		{"block rejected on clean allow", Case{Harness: "codex", Want: Want{Decision: "block"}},
			0, `{"hookSpecificOutput":{"permissionDecision":"allow"}}`, "", false, "fail closed"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			pass, detail := judge(tc.c, tc.exit, []byte(tc.stdout), []byte(tc.stderr))
			if pass != tc.pass {
				t.Fatalf("pass = %v, want %v (detail: %s)", pass, tc.pass, detail)
			}
			if !tc.pass && tc.details != "" && !strings.Contains(detail, tc.details) {
				t.Errorf("detail %q does not contain %q", detail, tc.details)
			}
		})
	}
}

func TestSplitCommand(t *testing.T) {
	cases := []struct {
		in   string
		want []string
	}{
		{`agentguard hook --conformance-policy {policy}`, []string{"agentguard", "hook", "--conformance-policy", "{policy}"}},
		{`"C:\Program Files\hh.exe" {policy}`, []string{`C:\Program Files\hh.exe`, "{policy}"}},
		{`'/usr/local/bin/hello hook' --x`, []string{"/usr/local/bin/hello hook", "--x"}},
		{`  spaced   out  `, []string{"spaced", "out"}},
		{``, nil},
	}
	for _, tc := range cases {
		got := splitCommand(tc.in)
		if len(got) != len(tc.want) {
			t.Fatalf("splitCommand(%q) = %v, want %v", tc.in, got, tc.want)
		}
		for i := range got {
			if got[i] != tc.want[i] {
				t.Fatalf("splitCommand(%q)[%d] = %q, want %q", tc.in, i, got[i], tc.want[i])
			}
		}
	}
}
