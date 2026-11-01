package agentguard

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/strazahq/straza/internal/agentguard/localapi"
	"github.com/strazahq/straza/internal/policy"
)

// setTrust sets the two facts serviceHome trusts for one test.
func setTrust(t *testing.T, system, managed bool) {
	t.Helper()
	oldSystem, oldManaged := systemPresent, managedInstalled
	systemPresent = func() bool { return system }
	managedInstalled = func() bool { return managed }
	t.Cleanup(func() { systemPresent, managedInstalled = oldSystem, oldManaged })
}

// TestServiceHomeIgnoresTheEnvironmentOnASystemMachine pins the thin
// clients' trust anchor: a machine with a system service or a managed
// install always uses the system socket, whatever STRAZA_HOME says, because
// whoever launches a harness controls its environment. Only a machine with
// neither honours STRAZA_HOME, which is how the journey matrix runs one
// service per identity.
func TestServiceHomeIgnoresTheEnvironmentOnASystemMachine(t *testing.T) {
	tests := []struct {
		name            string
		system, managed bool
		home            string
		wantHome        string
	}{
		{name: "development machine, home set", home: "/home/kim/.straza-ci", wantHome: "/home/kim/.straza-ci"},
		{name: "development machine, no home"},
		{name: "system service present", system: true, home: "/home/kim/.straza-ci"},
		{name: "managed install", managed: true, home: "/home/kim/.straza-ci"},
		{name: "both", system: true, managed: true, home: "/home/kim/.straza-ci"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			setTrust(t, tt.system, tt.managed)
			t.Setenv("STRAZA_HOME", tt.home)
			if got := serviceHome(); got != tt.wantHome {
				t.Fatalf("serviceHome() = %q, want %q", got, tt.wantHome)
			}
			wantSocket, _, _ := localapi.Paths(tt.wantHome)
			if got := NewServiceClient().Socket(); got != wantSocket {
				t.Errorf("NewServiceClient().Socket() = %q, want %q", got, wantSocket)
			}
		})
	}
}

// TestHookNeverDialsAUserSocketOnASystemMachine is the attack the trust
// anchor stops: on a machine with a system service, a socket the caller owns
// under STRAZA_HOME answers allow to everything, and the hook never asks it.
func TestHookNeverDialsAUserSocketOnASystemMachine(t *testing.T) {
	svc := newFakeService(t)
	setTrust(t, true, false)
	// The system socket points at an empty folder, because the machine that
	// runs the test may run a real system service, which would answer.
	oldSocket := localapi.SystemSocket
	localapi.SystemSocket = filepath.Join(t.TempDir(), "service.sock")
	t.Cleanup(func() { localapi.SystemSocket = oldSocket })
	d := serviceDecider{store: svc.store(t), svc: NewServiceClient()}
	dec := d.Decide(Normalized{HarnessName: "claude-code",
		Event: policy.Event{Kind: policy.EventToolPre, Tool: policy.ToolShellExec, Command: "rm -rf /"}})
	if dec.Effect != policy.EffectDeny {
		t.Fatalf("decision = %+v, want the fail-closed deny of the system service", dec)
	}
	if got := svc.decided(); len(got) != 0 {
		t.Fatalf("the socket under STRAZA_HOME was asked %d time(s)", len(got))
	}
}

// TestServiceSentence pins what each failure to get an answer says: the
// service's own sentence, a timeout that names the outcome, the not-running
// sentence of the mode and system in use, the sentence of a system that runs
// no service, and a sentence for an answer the client cannot read.
func TestServiceSentence(t *testing.T) {
	const userHome = "/home/kim/.straza-ci"
	tests := []struct {
		name string
		os   string
		home string
		err  error
		want string
	}{
		{"refusal", "linux", "", &localapi.RefusalError{Status: 403, Msg: "This machine isn't enrolled for you yet. Run `straza enroll` and sign in with your work account."},
			"This machine isn't enrolled for you yet. Run `straza enroll` and sign in with your work account."},
		{"timeout", "linux", "", fmt.Errorf("%w: context deadline exceeded", localapi.ErrTimeout),
			"Straza's background service did not answer in time, so Straza denied this call. Run `straza doctor` to see why."},
		{"system service unreachable", "linux", "", fmt.Errorf("%w: no such file", localapi.ErrUnreachable),
			"Straza's background service isn't running on this machine, so this command can't reach your Straza session. Run `straza doctor` to see why, or ask IT to reinstall Straza. On Linux, `systemctl status straza-service` shows why."},
		{"user service unreachable", "linux", userHome, fmt.Errorf("%w: no such file", localapi.ErrUnreachable),
			"Straza's background service for STRAZA_HOME " + userHome + " isn't running, so this command can't reach your Straza session. Start it with `straza service` in a shell that has the same STRAZA_HOME."},
		{"Windows service unreachable", "windows", "", fmt.Errorf("%w: pipe not found", localapi.ErrUnreachable),
			"Straza's background service isn't running on this machine, so this command can't reach your Straza session. Run `straza doctor` to see why, or ask IT to reinstall Straza. " +
				"On Windows, `Get-Service straza-service` shows whether it runs, and the Application event log under straza-service shows why it stopped."},
		{"macOS service unreachable", "darwin", "", fmt.Errorf("%w: no such file", localapi.ErrUnreachable),
			"Straza's background service isn't running on this machine, so this command can't reach your Straza session. Run `straza doctor` to see why, or ask IT to reinstall Straza. " +
				"On macOS, `sudo launchctl print system/ai.straza.service` shows whether it runs, and /var/log/straza-service/service.log shows why it stopped."},
		{"neither Linux, macOS nor Windows", "freebsd", "", fmt.Errorf("%w: refused", localapi.ErrUnreachable),
			"`straza service` runs only on Linux, macOS and Windows in this version of Straza, so this machine cannot hold Straza sessions yet."},
		{"unreadable answer", "linux", "", errors.New("read the service's answer to /local/v1/decide: unexpected EOF"),
			"Straza could not read the answer of its background service (read the service's answer to /local/v1/decide: unexpected EOF). Run `straza doctor` to see why."},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			setOSName(t, tt.os)
			t.Setenv("STRAZA_HOME", tt.home)
			if got := serviceSentence(tt.err, outcomeDenied); got != tt.want {
				t.Errorf("serviceSentence = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestServiceDownHint pins the doctor's hint when no service answers: where
// to see why on Linux, macOS and Windows, how to start a user-mode service,
// and the sentence of a system that runs no service.
func TestServiceDownHint(t *testing.T) {
	const rest = ". The checks below that read only this machine still ran; the enrollment, server, session and killswitch checks resume once the service answers"
	tests := []struct {
		name, os, home, want string
	}{
		{"Linux system service", "linux", "", "ask IT to reinstall Straza. On Linux, `systemctl status straza-service` shows why"},
		{"Linux user service", "linux", "/home/kim/.straza-ci", "start it with `straza service` in a shell that has the same STRAZA_HOME"},
		{"Windows", "windows", "", "ask IT to reinstall Straza. On Windows, `Get-Service straza-service` shows whether it runs, " +
			"and the Application event log under straza-service shows why it stopped"},
		{"macOS", "darwin", "", "ask IT to reinstall Straza. On macOS, `sudo launchctl print system/ai.straza.service` shows whether it runs, " +
			"and /var/log/straza-service/service.log shows why it stopped"},
		{"neither Linux, macOS nor Windows", "freebsd", "/home/kim/.straza-ci",
			"`straza service` runs only on Linux, macOS and Windows in this version of Straza, so this machine cannot hold Straza sessions yet"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			setOSName(t, tt.os)
			setTrust(t, false, false)
			t.Setenv("STRAZA_HOME", tt.home)
			c := serviceDownCheck(fmt.Errorf("%w: no such file", localapi.ErrUnreachable))
			if c.Status != checkFail || c.Hint != tt.want+rest {
				t.Errorf("serviceDownCheck = %+v, want a FAIL with the hint %q", c, tt.want+rest)
			}
		})
	}
}

// TestHookDeniesAnUnresolvedAllow pins the defence against a service fault:
// an allow that still needs a server check, the classifier or an approval is
// denied fail-closed, as the conformance decider does, and journaled so.
func TestHookDeniesAnUnresolvedAllow(t *testing.T) {
	for _, d := range []policy.Decision{
		{Effect: policy.EffectAllow, RuleID: "r1", ServerCheck: true},
		{Effect: policy.EffectAllow, RuleID: "r1", Classify: true},
		{Effect: policy.EffectAllow, RuleID: "r1", Approve: &policy.ApproveSpec{}},
	} {
		svc := newFakeService(t)
		svc.decision = &d
		got := serviceDecider{store: svc.store(t), svc: NewServiceClient()}.Decide(Normalized{HarnessName: "claude-code",
			Event: policy.Event{Kind: policy.EventToolPre, Tool: policy.ToolShellExec, Command: "ls"}})
		if got.Effect != policy.EffectDeny || got.RuleID != "r1" ||
			got.Reason != "Straza: "+unresolvedReason {
			t.Errorf("answer %+v became %+v, want the unresolved deny", d, got)
		}
	}
}

// TestDoctorFlagsOldCredentialFiles pins the row for credential files an
// older client left in the straza home: one FAIL per file, with the path
// and what to do, even when no service answers.
func TestDoctorFlagsOldCredentialFiles(t *testing.T) {
	home := noService(t)
	store, err := OpenStore()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(home, "state"), 0o700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"identity.json", "nhi-key.json"} {
		if err := os.WriteFile(filepath.Join(home, "state", name), []byte("{}"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	var rows []Check
	for _, c := range localChecks(store, nil) {
		if c.Name == "old-credential" {
			rows = append(rows, c)
		}
	}
	if len(rows) != 2 {
		t.Fatalf("old-credential rows = %+v, want 2", rows)
	}
	for i, name := range []string{"identity.json", "nhi-key.json"} {
		want := "this machine still holds a credential file from an older Straza client at " + filepath.Join(home, "state", name)
		if rows[i].Status != checkFail || rows[i].Detail != want ||
			!strings.Contains(rows[i].Hint, "`strazactl users nhi-key set`") {
			t.Errorf("row %d = %+v, want a FAIL saying %q", i, rows[i], want)
		}
	}
}

// TestGatewayProxiedReachesTheService drives a PreToolUse payload captured
// verbatim from codex-cli 0.146.0 on Windows, whose mangled app name would
// produce a false deny in the hook, through Normalize and the decider: the
// gateway-proxied flag the service defers on comes from Normalize.
func TestGatewayProxiedReachesTheService(t *testing.T) {
	adapters, err := LoadAdapters()
	if err != nil {
		t.Fatal(err)
	}
	payload := map[string]any{
		"session_id":      "019fb68c-c89c-7fd0-a953-5f860511ecef",
		"turn_id":         "019fb68c-e400-7170-aa86-f420fb5e832a",
		"transcript_path": `C:\Users\alice\.codex\sessions\2026\07\31\rollout.jsonl`,
		"cwd":             `C:\Users\alice`,
		"hook_event_name": "PreToolUse",
		"model":           "gpt-5.6-terra",
		"permission_mode": "default",
		"tool_name":       "mcp__straza__demo_tools__echo",
		"tool_input": map[string]any{
			"message":               "strazaaa",
			"_straza_justification": "The user requested an echo invocation to return the exact supplied message.",
		},
		"tool_use_id": "exec-8ab67b73-cb25-4291-8036-3581a92bf031",
	}
	n, err := Normalize(adapters["codex"], payload)
	if err != nil {
		t.Fatal(err)
	}
	if !n.GatewayProxied {
		t.Fatal("captured payload must classify as gateway-proxied (mcp__straza__ prefix)")
	}
	svc := newFakeService(t)
	serviceDecider{store: svc.store(t), svc: NewServiceClient()}.Decide(n)
	got := svc.decided()
	if len(got) != 1 || !got[0].GatewayProxied || got[0].Harness.Session != "019fb68c-c89c-7fd0-a953-5f860511ecef" {
		t.Fatalf("decide request = %+v, want the gateway-proxied flag and the window", got)
	}
}

// TestManagedDetectionIgnoresTheEnvironment pins that the managed half of
// the trust anchor reads only the fixed path: pointing STRAZA_SYSTEM at a
// directory with no managed config cannot hide a managed install, and one
// with a config cannot fake one. The machine running the test decides the
// answer, so the test compares against the fixed path itself.
func TestManagedDetectionIgnoresTheEnvironment(t *testing.T) {
	want := managedConfigFixed()
	for _, dir := range []string{t.TempDir(), filepath.Join(t.TempDir(), "absent")} {
		t.Setenv("STRAZA_SYSTEM", dir)
		_ = os.WriteFile(filepath.Join(dir, "config.yaml"), []byte("serverUrl: https://rogue.example\n"), 0o600)
		if got := managedConfigFixed(); got != want {
			t.Errorf("with STRAZA_SYSTEM=%s managedConfigFixed() = %v, want %v", dir, got, want)
		}
	}
}
