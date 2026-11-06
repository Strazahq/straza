package agentguard

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/strazahq/straza/internal/agentguard/localapi"
)

func checksByName(cs []Check) map[string]Check {
	m := map[string]Check{}
	for _, c := range cs {
		m[c.Name] = c
	}
	return m
}

// TestDoctor drives the doctor diagnostics through the states the Straza
// background service reports: no service, not enrolled, fully healthy,
// server unreachable, a revoked session, a changed machine and a refused
// status. Every non-ok state must carry an actionable hint: a diagnosis
// without a next step is noise.
func TestDoctor(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	// Isolate harness-wiring lookups from the developer machine.
	claudeDir := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", claudeDir)
	t.Setenv("CODEX_HOME", t.TempDir())
	t.Setenv("STRAZA_GEMINI_CONFIG_DIR", t.TempDir())
	t.Setenv("STRAZA_MANAGED_SETTINGS_DIR", t.TempDir())

	// No service: the service failure leads, loud, with how to start it, and
	// the machine-local checks still report after it (their contract; the
	// full matrix is TestDoctorUnenrolledLocalChecks). Nothing that needs the
	// service may appear.
	noService(t)
	store, err := OpenStore()
	if err != nil {
		t.Fatal(err)
	}
	cs := Doctor(ctx, store, NewServiceClient())
	if len(cs) == 0 || cs[0].Name != "service" || cs[0].Status != checkFail || !strings.Contains(cs[0].Hint, "straza service") {
		t.Fatalf("no-service doctor = %+v", cs)
	}
	assertOnly := func(label string, byName map[string]Check, present, absent []string) {
		t.Helper()
		for _, name := range present {
			if _, ok := byName[name]; !ok {
				t.Errorf("%s: check %s missing: %+v", label, name, byName)
			}
		}
		for _, name := range absent {
			if c, ok := byName[name]; ok {
				t.Errorf("%s: check %s ran: %+v", label, name, c)
			}
		}
	}
	assertOnly("no service", checksByName(cs), []string{"wiring"},
		[]string{"enrollment", "server", "session", "audit-spool", "killswitch"})

	// Not enrolled: the service answers, and the enrollment failure names
	// the enroll command.
	svc := newFakeService(t)
	store = svc.store(t)
	cs = Doctor(ctx, store, NewServiceClient())
	if len(cs) < 2 || cs[0].Name != "service" || cs[0].Status != checkOK ||
		cs[1].Name != "enrollment" || cs[1].Status != checkFail || !strings.Contains(cs[1].Hint, "straza enroll") {
		t.Fatalf("unenrolled doctor = %+v", cs)
	}
	assertOnly("unenrolled", checksByName(cs), []string{"wiring"},
		[]string{"server", "session", "audit-spool", "killswitch"})

	// Healthy: enrolled at a reachable server, one live session holding its
	// push stream, hooks wired.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/version" {
			_, _ = w.Write([]byte(`{"version":"test","profile":"standalone"}`))
		}
	}))
	defer server.Close()
	now := time.Now()
	svc.status.Enrollment = &localapi.Enrollment{ServerURL: server.URL, Kind: localapi.KindPerson,
		Username: "alice", DeviceID: "dev-7f3a", KeyThumbprint: "thumbprint-abcdef", Custody: localapi.CustodyUser}
	svc.status.Sessions = []localapi.SessionStatus{{
		Harness: localapi.Harness{Name: "claude-code", Version: "2.1.0", Session: "w1"}, SessionID: "s1",
		User: "alice", ExpiresAt: now.Add(5 * time.Minute), Push: localapi.PushConnected}}
	svc.status.Spool = localapi.SpoolStatus{LastDrain: now.Add(-10 * time.Second)}
	// The full roster a current `straza install` writes, naming a binary that
	// is actually there. A partial roster is not healthy wiring (see
	// TestMissingHookEvents), and a registration naming a binary that ISN'T
	// there is its own finding (see TestHookBinaryCheck).
	strazaBin := filepath.Join(t.TempDir(), "straza")
	if err := os.WriteFile(strazaBin, []byte("#!/bin/sh\nexit 0\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	hookEntry := `[{"hooks":[{"type":"command","command":` + strconv.Quote(strazaBin+" hook --harness claude-code") + `}]}]`
	if err := os.WriteFile(filepath.Join(claudeDir, "settings.json"),
		[]byte(`{"hooks":{`+
			`"SessionStart":`+hookEntry+`,"PreToolUse":`+hookEntry+`,`+
			`"UserPromptSubmit":`+hookEntry+`,"Stop":`+hookEntry+`,`+
			`"SessionEnd":`+hookEntry+`,"SubagentStart":`+hookEntry+`,`+
			`"SubagentStop":`+hookEntry+`}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	byName := checksByName(Doctor(ctx, store, NewServiceClient()))
	for name, wantStatus := range map[string]string{
		"service": checkOK, "enrollment": checkOK, "server": checkOK, "session": checkOK,
		"wiring": checkOK, "hook-binary": checkOK, "killswitch": checkOK, "audit-spool": checkOK,
	} {
		c, present := byName[name]
		if !present {
			t.Errorf("check %s missing", name)
			continue
		}
		if c.Status != wantStatus {
			t.Errorf("%s = %s (%s / %s), want %s", name, c.Status, c.Detail, c.Hint, wantStatus)
		}
	}
	if c := byName["enrollment"]; !strings.Contains(c.Detail, "alice (device dev-7f3a) at "+server.URL+", key thumbprint-a") {
		t.Errorf("enrollment detail = %q", c.Detail)
	}
	if c := byName["session"]; !strings.Contains(c.Detail, "claude-code/2.1.0 s1 as alice") {
		t.Errorf("session detail = %q", c.Detail)
	}
	if c := byName["hook-binary"]; !strings.Contains(c.Detail, strazaBin) {
		t.Errorf("hook-binary detail = %q, want the resolved binary %s", c.Detail, strazaBin)
	}
	// No audit was ever dropped, so no audit-dropped line: loss is the
	// exception and healthy output must not carry a routine row for it.
	if c, ok := byName["audit-dropped"]; ok {
		t.Errorf("audit-dropped check present with nothing dropped: %+v", c)
	}

	// Server gone and the push stream with it: the server check fails with a
	// reachability hint, and the killswitch line sends the operator there.
	svc.status.Enrollment.ServerURL = "http://127.0.0.1:9" // discard port: nothing listens
	svc.status.Sessions[0].Push = localapi.PushConnecting
	byName = checksByName(Doctor(ctx, store, NewServiceClient()))
	if c := byName["server"]; c.Status != checkFail || !strings.Contains(c.Detail, "unreachable") || c.Hint == "" {
		t.Errorf("dead-server check = %+v", c)
	}
	if c := byName["killswitch"]; c.Status != checkWarn || !strings.Contains(c.Detail, "0 of 1") || !strings.Contains(c.Hint, "fix the server check first") {
		t.Errorf("killswitch with the server down = %+v", c)
	}
	svc.status.Enrollment.ServerURL = server.URL

	// A revoked session is a FAIL that says what to do next.
	svc.status.Sessions[0].Revoked = "an administrator revoked this session"
	byName = checksByName(Doctor(ctx, store, NewServiceClient()))
	if c := byName["session"]; c.Status != checkFail || !strings.Contains(c.Detail, "an administrator revoked this session") || c.Hint == "" {
		t.Errorf("revoked session check = %+v", c)
	}
	svc.status.Sessions[0].Revoked = ""

	// A newer policy the server refused to send is a FAIL on that window's
	// own row, with the server's reason and what ends the denial.
	svc.status.Sessions[0].PolicyRefused = "The server refused it: the snapshot store is unavailable."
	byName = checksByName(Doctor(ctx, store, NewServiceClient()))
	if c := byName["policy"]; c.Status != checkFail ||
		!strings.Contains(c.Detail, "claude-code/2.1.0 s1 as alice: the server holds a newer policy that straza could not fetch. The server refused it: the snapshot store is unavailable.") ||
		!strings.Contains(c.Hint, "the first fetch that succeeds ends the denial") {
		t.Errorf("refused policy check = %+v", c)
	}
	if c, ok := byName["session"]; ok {
		t.Errorf("a refused-policy window also got a session row: %+v", c)
	}
	svc.status.Sessions[0].PolicyRefused = ""

	// A changed machine stops the key: a FAIL naming the re-enroll.
	svc.status.Enrollment.MachineChanged = true
	byName = checksByName(Doctor(ctx, store, NewServiceClient()))
	if c := byName["enrollment"]; c.Status != checkFail || !strings.Contains(c.Detail, "identity changed") || !strings.Contains(c.Hint, "straza enroll") {
		t.Errorf("machine-changed check = %+v", c)
	}
	svc.status.Enrollment.MachineChanged = false

	// A check-in with an unknown outcome holds new sessions: a warn with the
	// time to wait for.
	svc.status.Enrollment.WaitUntil = now.Add(time.Minute)
	byName = checksByName(Doctor(ctx, store, NewServiceClient()))
	if c := byName["enrollment"]; c.Status != checkWarn || !strings.Contains(c.Detail, "waiting for the server") || c.Hint == "" {
		t.Errorf("waiting check = %+v", c)
	}

	// A service that refuses the status request leads with its sentence.
	svc.refuse = "Straza refused this request."
	cs = Doctor(ctx, store, NewServiceClient())
	if cs[0].Name != "service" || cs[0].Status != checkFail || !strings.Contains(cs[0].Detail, "Straza refused this request.") {
		t.Errorf("refused status = %+v", cs[0])
	}
}

// TestDoctorEnrollSteps pins the enrollment hints that name an enroll
// command. A caller with no enrollment reads a person's and an AI agent's
// command, because the service reports an agent key that keygen made as no
// enrollment too. After the machine identity changed, a person reads the
// enroll command at the enrolled server and an agent reads both of its
// lanes. The enroll commands leave out --server when the managed
// configuration names the server.
func TestDoctorEnrollSteps(t *testing.T) {
	const managed = "serverUrl: https://straza.example\n"
	agent := localapi.Enrollment{ServerURL: "https://straza.example", Kind: localapi.KindAgent, Username: "ci-bot", KeyThumbprint: "k1", MachineChanged: true}
	person := localapi.Enrollment{ServerURL: "https://straza.example", Kind: localapi.KindPerson, Username: "alice", DeviceID: "d1", KeyThumbprint: "k1", MachineChanged: true}
	cases := []struct {
		name    string
		managed string // the managed config.yaml, or "" for no managed layout
		e       *localapi.Enrollment
		want    string
	}{
		{"not enrolled", "", nil,
			"A person runs `straza enroll --server <strazad url>` and signs in with their work account. " +
				"An AI agent runs `straza enroll --server <strazad url> --headless` with its registered key or the client secret IT installed for it. " +
				"The local checks below still ran; the server, session and killswitch checks resume once enrolled"},
		{"not enrolled, managed", managed, nil,
			"A person runs `straza enroll` and signs in with their work account. " +
				"An AI agent runs `straza enroll --headless` with its registered key or the client secret IT installed for it. " +
				"The local checks below still ran; the server, session and killswitch checks resume once enrolled"},
		{"a person's machine changed", "", &person, "Run `straza enroll --server https://straza.example` to enroll it again."},
		{"a person's machine changed, managed", managed, &person, "Run `straza enroll` to enroll it again."},
		{"an agent's machine changed", "", &agent,
			"An agent with a key from `straza keygen` runs `straza keygen --force --user ci-bot`, has an admin register the new public key, " +
				"and then runs `straza enroll --server https://straza.example --headless`. " +
				"An agent that signs in with a client secret runs `straza enroll --server https://straza.example --headless --user ci-bot`."},
		{"an agent's machine changed, managed", managed, &agent,
			"An agent with a key from `straza keygen` runs `straza keygen --force --user ci-bot`, has an admin register the new public key, " +
				"and then runs `straza enroll --headless`. " +
				"An agent that signs in with a client secret runs `straza enroll --headless --user ci-bot`."},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sys := t.TempDir()
			t.Setenv("STRAZA_SYSTEM", sys)
			if tc.managed != "" {
				if err := os.WriteFile(filepath.Join(sys, "config.yaml"), []byte(tc.managed), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			var row Check
			if tc.e == nil {
				svc := newFakeService(t)
				for _, c := range Doctor(context.Background(), svc.store(t), NewServiceClient()) {
					if c.Name == "enrollment" {
						row = c
					}
				}
			} else {
				row = enrollmentCheck(*tc.e, time.Now())
			}
			if row.Status != checkFail || row.Hint != tc.want {
				t.Errorf("enrollment row = %+v, want a failure with the hint\n%q", row, tc.want)
			}
		})
	}
}

// TestDoctorUnenrolledLocalChecks pins localChecks' contract: a box whose
// background service does not answer still reports every machine-local
// finding. The live-wiring-nothing-behind-it machine is exactly where a ghost
// binary or a managed lane matters most, while nothing that needs the
// service runs and nothing reads healthier than it is.
func TestDoctorUnenrolledLocalChecks(t *testing.T) {
	ctx := context.Background()

	// fullRoster wires every event a current install registers, so the wiring
	// check has nothing to say and each case isolates its own finding.
	fullRoster := func(harness, bin string) string {
		entry := `[{"hooks":[{"type":"command","command":` + strconv.Quote(bin+" hook --harness "+harness) + `}]}]`
		return `{"hooks":{` +
			`"SessionStart":` + entry + `,"PreToolUse":` + entry + `,` +
			`"UserPromptSubmit":` + entry + `,"Stop":` + entry + `,` +
			`"SessionEnd":` + entry + `,"SubagentStart":` + entry + `,` +
			`"SubagentStop":` + entry + `}}`
	}
	isolate := func(t *testing.T) (claudeDir, codexHome string, store *Store) {
		t.Helper()
		claudeDir, codexHome = t.TempDir(), t.TempDir()
		t.Setenv("CLAUDE_CONFIG_DIR", claudeDir)
		t.Setenv("CODEX_HOME", codexHome)
		t.Setenv("STRAZA_GEMINI_CONFIG_DIR", t.TempDir())
		t.Setenv("STRAZA_MANAGED_SETTINGS_DIR", t.TempDir())
		noService(t)
		store, err := OpenStore()
		if err != nil {
			t.Fatal(err)
		}
		return claudeDir, codexHome, store
	}

	t.Run("ghost wiring and the managed lane surface", func(t *testing.T) {
		claudeDir, _, store := isolate(t)

		// Live wiring, wiped config: claude-code's full roster points at a
		// binary that is gone, so the harness runs a dead command every event.
		ghost := filepath.Join(t.TempDir(), "gone", "straza")
		if err := os.WriteFile(filepath.Join(claudeDir, "settings.json"),
			[]byte(fullRoster("claude-code", ghost)), 0o600); err != nil {
			t.Fatal(err)
		}
		// The codex managed lane, written by the real installer: on the fleet
		// box this is the ONLY live lane, and doctor must see it un-enrolled.
		managedPath, err := CodexManagedRequirementsPath()
		if err != nil {
			t.Fatal(err)
		}
		liveBin := filepath.Join(t.TempDir(), "straza")
		if err := os.WriteFile(liveBin, []byte("#!/bin/sh\nexit 0\n"), 0o700); err != nil {
			t.Fatal(err)
		}
		if _, err := InstallCodexManagedHooks(managedPath, liveBin); err != nil {
			t.Fatal(err)
		}
		cs := Doctor(ctx, store, NewServiceClient())
		if len(cs) == 0 || cs[0].Name != "service" || cs[0].Status != "fail" {
			t.Fatalf("first check = %+v, want the service fail", cs)
		}
		byName := checksByName(cs)
		if c := byName["hook-binary"]; c.Status != "fail" || !strings.Contains(c.Detail, ghost) {
			t.Errorf("un-enrolled hook-binary = %+v, want a fail naming %s", c, ghost)
		}
		if c := byName["wiring"]; !strings.Contains(c.Detail, "codex (managed)") ||
			!strings.Contains(c.Detail, "claude-code (user)") {
			t.Errorf("un-enrolled wiring = %+v, want both lanes listed", c)
		}
		for _, name := range []string{"enrollment", "server", "session", "audit-spool", "killswitch"} {
			if c, ok := byName[name]; ok {
				t.Errorf("check %s ran with no service: %+v", name, c)
			}
		}
	})

	t.Run("codex user lane stays trust-honest", func(t *testing.T) {
		// No sessions must land codexHooksCheck in its degraded branch: a
		// fully wired user hooks.json with no readable trust record is a
		// warn: with no service there is never a session to serve as
		// evidence, and file presence alone must not green the trust-gated
		// lane.
		_, codexHome, store := isolate(t)
		liveBin := filepath.Join(t.TempDir(), "straza")
		if err := os.WriteFile(liveBin, []byte("#!/bin/sh\nexit 0\n"), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(codexHome, "hooks.json"),
			[]byte(fullRoster("codex", liveBin)), 0o600); err != nil {
			t.Fatal(err)
		}
		byName := checksByName(Doctor(ctx, store, NewServiceClient()))
		c, ok := byName["codex-hooks"]
		if !ok {
			t.Fatal("codex-hooks check missing un-enrolled")
		}
		if c.Status != "warn" || !strings.Contains(c.Detail, "trust cannot be verified") {
			t.Errorf("un-enrolled codex-hooks = %+v, want the honest trust warn", c)
		}
	})
}

// TestHintCatalog pins that every deny/failure reason straza emits
// resolves to an actionable hint.
func TestHintCatalog(t *testing.T) {
	// The literal reason strings produced across the kit (hook fail-closed,
	// flows, client, server refusals relayed to the model).
	reasons := []string{
		"Straza: not enrolled (run `straza enroll`): open state",
		"Straza: no active Straza session. Restart the session so straza can check in",
		"Straza: state unavailable (permission denied)",
		"Straza: checkin failed: Post http://x: connection refused",
		"device credential rejected. Run `straza enroll` again",
		"Straza: your session has been revoked. Re-enroll",
		"user is disabled. Contact your administrator",
		`attestation level "advisory" is below the required level "managed"`,
		"Straza: refusing unverified snapshot: signature mismatch",
		"Straza: snapshot fetch failed: connection refused",
		"Straza: blocked by policy",
	}
	for _, reason := range reasons {
		if hint := HintFor(reason); hint == "" {
			t.Errorf("no doctor hint for reason %q", reason)
		}
	}
	if HintFor("some totally unrelated error") != "" {
		t.Error("catalog matched an unrelated reason: substrings too broad")
	}
}

// TestSessionChecksFoldIdleWindows pins the session rows of doctor: a window
// whose token expired at most an hour ago keeps a row of its own, as a
// revoked one does, and windows unused for longer share one row per kind,
// so a machine that kept a week of windows lists two rows for them, not
// hundreds.
func TestSessionChecksFoldIdleWindows(t *testing.T) {
	now := time.Now()
	win := func(id string, expired time.Duration, revoked string) localapi.SessionStatus {
		return localapi.SessionStatus{Harness: localapi.Harness{Name: "claude-code", Session: id}, SessionID: id,
			User: "alice", ExpiresAt: now.Add(-expired), Revoked: revoked}
	}
	rows := []struct {
		name     string
		sessions []localapi.SessionStatus
		want     []string // status and detail prefix of each row, in order
	}{
		{"a live window", []localapi.SessionStatus{win("w1", -time.Minute, "")},
			[]string{"ok claude-code w1 as alice (token valid"}},
		{"a window expired ten minutes ago", []localapi.SessionStatus{win("w1", 10*time.Minute, "")},
			[]string{"warn claude-code w1 as alice: token expired 10m0s ago"}},
		{"a revoked window", []localapi.SessionStatus{win("w1", 10*time.Minute, "by an admin")},
			[]string{"fail claude-code w1 as alice: revoked (by an admin)"}},
		{"a week of idle windows", []localapi.SessionStatus{
			win("w1", -time.Minute, ""), win("w2", 2*time.Hour, ""), win("w3", 50*time.Hour, ""),
			win("w4", 3*time.Hour, "by an admin"), win("w5", 6*24*time.Hour, "")},
			[]string{"ok claude-code w1 as alice (token valid",
				"ok 3 windows unused for over an hour. Each one checks in again at its next call",
				"warn 1 revoked window unused for over an hour. Its tool calls stay denied until its harness starts a new session"}},
	}
	refused := []localapi.SessionStatus{win("w1", 2*time.Hour, ""), win("w2", 3*time.Hour, "")}
	for i := range refused {
		refused[i].PolicyRefused = "the snapshot signature does not verify"
	}
	rows = append(rows, struct {
		name     string
		sessions []localapi.SessionStatus
		want     []string
	}{"idle windows that carry a policy refusal", refused,
		[]string{"ok 2 windows unused for over an hour. Each one checks in again at its next call"}})
	for _, row := range rows {
		t.Run(row.name, func(t *testing.T) {
			got := sessionChecks(row.sessions, now)
			if len(got) != len(row.want) {
				t.Fatalf("%d rows %+v, want %d", len(got), got, len(row.want))
			}
			for i, c := range got {
				if line := c.Status + " " + c.Detail; !strings.HasPrefix(line, row.want[i]) {
					t.Errorf("row %d = %q, want it to start %q", i, line, row.want[i])
				}
			}
		})
	}
}

// TestKillswitchCheckCountsLiveSessions pins that only a live session's
// push stream counts. A revoked or lapsed window holds none by design, and
// the service keeps such windows for days.
func TestKillswitchCheckCountsLiveSessions(t *testing.T) {
	now := time.Now()
	win := func(id string, expired time.Duration, revoked, push string) localapi.SessionStatus {
		return localapi.SessionStatus{Harness: localapi.Harness{Name: "claude-code", Session: id}, SessionID: id,
			User: "alice", ExpiresAt: now.Add(-expired), Revoked: revoked, Push: push}
	}
	rows := []struct {
		name     string
		sessions []localapi.SessionStatus
		want     string // status and detail prefix, "" for no row
	}{
		{"no sessions", nil, ""},
		{"a live window with its stream", []localapi.SessionStatus{win("w1", -time.Minute, "", localapi.PushConnected)},
			"ok push stream connected for all 1 session(s)"},
		{"a live window still connecting", []localapi.SessionStatus{win("w1", -time.Minute, "", localapi.PushConnecting)},
			"warn push stream connected for 0 of 1 session(s) (claude-code w1: connecting)"},
		{"only revoked and lapsed windows", []localapi.SessionStatus{
			win("w1", 2*time.Hour, "by an admin", localapi.PushOff), win("w2", 10*time.Minute, "", localapi.PushOff)}, ""},
		{"a live window beside revoked and lapsed ones", []localapi.SessionStatus{
			win("w1", -time.Minute, "", localapi.PushConnected), win("w2", 3*time.Hour, "by an admin", localapi.PushOff),
			win("w3", 2*24*time.Hour, "", localapi.PushOff), win("w4", -time.Minute, "by an admin", localapi.PushOff)},
			"ok push stream connected for all 1 session(s)"},
	}
	for _, row := range rows {
		t.Run(row.name, func(t *testing.T) {
			c := killswitchCheck(row.sessions, now, true)
			switch {
			case row.want == "" && c != nil:
				t.Fatalf("killswitch row %+v, want none", *c)
			case row.want == "":
			case c == nil:
				t.Fatalf("no killswitch row, want %q", row.want)
			case !strings.HasPrefix(c.Status+" "+c.Detail, row.want):
				t.Errorf("killswitch row = %q, want it to start %q", c.Status+" "+c.Detail, row.want)
			}
		})
	}
}
