package agentguard_test

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/strazahq/straza/internal/agentguard"
)

func checksByName(cs []agentguard.Check) map[string]agentguard.Check {
	m := map[string]agentguard.Check{}
	for _, c := range cs {
		m[c.Name] = c
	}
	return m
}

// TestDoctor drives the doctor diagnostics through their states: unenrolled,
// fully healthy, server unreachable, snapshot tampered. Every non-ok state
// must carry an actionable hint: a diagnosis without a next step is noise.
func TestDoctor(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	// Isolate harness-wiring lookups from the developer machine.
	claudeDir := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", claudeDir)
	t.Setenv("CODEX_HOME", t.TempDir())
	t.Setenv("STRAZA_GEMINI_CONFIG_DIR", t.TempDir())
	t.Setenv("STRAZA_MANAGED_SETTINGS_DIR", t.TempDir())

	// Unenrolled: the enrollment failure leads, loud, with the enroll hint,
	// and the machine-local checks still report after it (their contract; the
	// full un-enrolled matrix is TestDoctorUnenrolledLocalChecks). Nothing
	// server-dependent may appear: there is no server to speak of.
	t.Setenv("STRAZA_HOME", t.TempDir())
	emptyStore, err := agentguard.OpenStore()
	if err != nil {
		t.Fatal(err)
	}
	cs := agentguard.Doctor(ctx, emptyStore)
	if len(cs) == 0 || cs[0].Name != "enrollment" || cs[0].Status != "fail" || !strings.Contains(cs[0].Hint, "straza enroll") {
		t.Fatalf("unenrolled doctor = %+v", cs)
	}
	un := checksByName(cs)
	for _, name := range []string{"wiring", "audit-spool"} {
		if _, ok := un[name]; !ok {
			t.Errorf("un-enrolled doctor dropped local check %s: %+v", name, cs)
		}
	}
	for _, name := range []string{"identity", "server", "session", "snapshot", "killswitch"} {
		if c, ok := un[name]; ok {
			t.Errorf("un-enrolled doctor ran server-dependent check %s: %+v", name, c)
		}
	}

	// Healthy: enrolled against a real strazad, session started, hooks wired.
	base, _ := bootStrazad(t)
	home := t.TempDir()
	t.Setenv("STRAZA_HOME", home)
	agStore, err := agentguard.OpenStore()
	if err != nil {
		t.Fatal(err)
	}
	driveEnroll(t, agStore, base)
	if _, _, err := agentguard.SessionStart(ctx, agStore, "claude-code", "2.1.0"); err != nil {
		t.Fatal(err)
	}
	// The full roster a current `straza install` writes, naming a binary that
	// is actually there. A partial roster is not healthy wiring:
	// governance would work while the events added later (conversation
	// capture's) stay dead, which is the state doctor calls out (see
	// TestMissingHookEvents). A registration naming a binary that ISN'T there
	// is its own finding (see TestHookBinaryCheck).
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
	// A live daemon is the other half of the killswitch claim: doctor
	// reads the heartbeat `straza daemon` refreshes, and a verified lane nobody
	// subscribes to is a warn, not health. Written raw on purpose: this pins
	// the on-disk contract from outside the package.
	if err := os.WriteFile(filepath.Join(home, "state", "daemon-heartbeat.json"),
		[]byte(`{"pid":`+strconv.Itoa(os.Getpid())+`,"at":"`+time.Now().Format(time.RFC3339)+`","intervalSeconds":30}`), 0o600); err != nil {
		t.Fatal(err)
	}

	byName := checksByName(agentguard.Doctor(ctx, agStore))
	for name, wantStatus := range map[string]string{
		"enrollment": "ok", "identity": "ok", "server": "ok",
		"session": "ok", "snapshot": "ok", "wiring": "ok", "killswitch": "ok",
		"audit-spool": "ok", "hook-binary": "ok",
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
	if c := byName["wiring"]; !strings.Contains(c.Detail, "claude-code (user)") {
		t.Errorf("wiring detail = %q, want claude-code (user)", c.Detail)
	}
	if c := byName["hook-binary"]; !strings.Contains(c.Detail, strazaBin) {
		t.Errorf("hook-binary detail = %q, want the resolved binary %s", c.Detail, strazaBin)
	}
	// The kill-switch line is green only because doctor DIALED the lane AND a
	// live daemon's heartbeat says someone is subscribed to it (a verified lane
	// nobody holds delivers nothing sub-second).
	if c := byName["killswitch"]; !strings.Contains(c.Detail, "verified from here") ||
		!strings.Contains(c.Detail, "daemon alive") {
		t.Errorf("killswitch detail = %q, want the lane verified and the daemon alive", c.Detail)
	}
	// No audit was ever dropped, so no audit-dropped line: loss is the
	// exception and healthy output must not carry a routine row for it.
	if c, ok := byName["audit-dropped"]; ok {
		t.Errorf("audit-dropped check present without a marker: %+v", c)
	}
	// SessionStart's checkin drained the (empty) spool, so the healthy state
	// already carries a last-drain time, not the "never drained" fallback.
	if c := byName["audit-spool"]; !strings.Contains(c.Detail, "no pending audit events") ||
		!strings.Contains(c.Detail, "last successful drain") {
		t.Errorf("healthy audit-spool detail = %q", c.Detail)
	}

	// Server gone: the server check fails with a reachability hint; local
	// checks still report (the operator sees the whole picture).
	cfg, err := agStore.LoadConfig()
	if err != nil {
		t.Fatal(err)
	}
	good := cfg.ServerURL
	cfg.ServerURL = "http://127.0.0.1:9" // discard port: nothing listens
	if err := agStore.SaveConfig(cfg); err != nil {
		t.Fatal(err)
	}
	byName = checksByName(agentguard.Doctor(ctx, agStore))
	if c := byName["server"]; c.Status != "fail" || !strings.Contains(c.Detail, "unreachable") || c.Hint == "" {
		t.Errorf("dead-server check = %+v", c)
	}
	if c := byName["snapshot"]; c.Status != "ok" {
		t.Errorf("snapshot should still verify locally with the server down: %+v", c)
	}
	cfg.ServerURL = good
	if err := agStore.SaveConfig(cfg); err != nil {
		t.Fatal(err)
	}

	// Tampered snapshot: verification failure is a FAIL with a re-enroll
	// hint: hooks are denying and the operator must know why.
	if err := agStore.SaveSnapshot([]byte("garbage, not a signed snapshot")); err != nil {
		t.Fatal(err)
	}
	byName = checksByName(agentguard.Doctor(ctx, agStore))
	if c := byName["snapshot"]; c.Status != "fail" || c.Hint == "" {
		t.Errorf("tampered-snapshot check = %+v", c)
	}

	// Spooled backlog: doctor counts events across the live file AND rotated
	// pending files, says how many files hold them, and still reports the
	// last successful drain.
	spoolDir := filepath.Dir(agStore.SpoolPath())
	if err := os.WriteFile(agStore.SpoolPath(), []byte("{\"e\":1}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(spoolDir, "audit-pending-zzz.jsonl"),
		[]byte("{\"e\":2}\n{\"e\":3}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	byName = checksByName(agentguard.Doctor(ctx, agStore))
	if c := byName["audit-spool"]; c.Status != "warn" ||
		!strings.Contains(c.Detail, "3 audit event(s) in 2 file(s)") ||
		!strings.Contains(c.Detail, "last successful drain") || c.Hint == "" {
		t.Errorf("backlog audit-spool check = %+v", c)
	}

	// Unreadable spool (a pending path spool.ScanRecords cannot scan at all):
	// doctor says so and keeps going, never crashes. A directory stands in for
	// the permission failures this models, because it reproduces identically on
	// every OS and as any user; an oversize LINE does not qualify, since the
	// spool counts those as drops rather than failing the read (spool.go,
	// ScanRecords).
	if err := os.Mkdir(filepath.Join(spoolDir, "audit-pending-corrupt.jsonl"), 0o750); err != nil {
		t.Fatal(err)
	}
	byName = checksByName(agentguard.Doctor(ctx, agStore))
	if c := byName["audit-spool"]; c.Status != "warn" ||
		!strings.Contains(c.Detail, "spool unreadable") || c.Hint == "" {
		t.Errorf("unreadable audit-spool check = %+v", c)
	}
	if _, ok := byName["killswitch"]; !ok {
		t.Error("checks after the spool check missing: spool failure must not stop doctor")
	}

	// Dropped audit: a populated audit-dropped marker (what enforceSpoolCap /
	// the oversize guard write when they discard records) is a loud FAIL with
	// the totals and the last drop time.
	// Deleting the marker is the documented acknowledgement and clears it.
	marker := filepath.Join(spoolDir, "audit-dropped")
	if err := os.WriteFile(marker,
		[]byte("2026-07-29T10:00:00Z dropped 2 parked file(s) over the 33554432-byte spool cap\n"+
			"2026-07-30T04:05:06Z dropped 1 oversize record(s)\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	byName = checksByName(agentguard.Doctor(ctx, agStore))
	if c := byName["audit-dropped"]; c.Status != "fail" ||
		!strings.Contains(c.Detail, "2 parked spool file(s)") ||
		!strings.Contains(c.Detail, "1 oversize record(s)") ||
		!strings.Contains(c.Detail, "2026-07-30T04:05:06Z") ||
		!strings.Contains(c.Hint, marker) {
		t.Errorf("dropped-audit check = %+v", c)
	}
	if err := os.Remove(marker); err != nil {
		t.Fatal(err)
	}
	byName = checksByName(agentguard.Doctor(ctx, agStore))
	if c, ok := byName["audit-dropped"]; ok {
		t.Errorf("audit-dropped check outlived the marker: %+v", c)
	}
}

// TestDoctorUnenrolledLocalChecks pins localChecks' contract from outside the
// package: an un-enrolled box still reports every machine-local finding. The
// wiped-config-live-wiring machine is exactly where a ghost binary, a managed
// lane, or dropped audit matters most, while nothing server-dependent runs and
// nothing reads healthier than it is.
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
	isolate := func(t *testing.T) (claudeDir, codexHome string, store *agentguard.Store) {
		t.Helper()
		claudeDir, codexHome = t.TempDir(), t.TempDir()
		t.Setenv("CLAUDE_CONFIG_DIR", claudeDir)
		t.Setenv("CODEX_HOME", codexHome)
		t.Setenv("STRAZA_GEMINI_CONFIG_DIR", t.TempDir())
		t.Setenv("STRAZA_MANAGED_SETTINGS_DIR", t.TempDir())
		t.Setenv("STRAZA_HOME", t.TempDir())
		store, err := agentguard.OpenStore()
		if err != nil {
			t.Fatal(err)
		}
		return claudeDir, codexHome, store
	}

	t.Run("ghost wiring, managed lane, and dropped audit all surface", func(t *testing.T) {
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
		managedPath, err := agentguard.CodexManagedRequirementsPath()
		if err != nil {
			t.Fatal(err)
		}
		liveBin := filepath.Join(t.TempDir(), "straza")
		if err := os.WriteFile(liveBin, []byte("#!/bin/sh\nexit 0\n"), 0o700); err != nil {
			t.Fatal(err)
		}
		if _, err := agentguard.InstallCodexManagedHooks(managedPath, liveBin); err != nil {
			t.Fatal(err)
		}
		// A spooled backlog and a drop marker: audit evidence in limbo and
		// audit evidence gone, both of which enrollment must not hide.
		spoolDir := filepath.Dir(store.SpoolPath())
		if err := os.MkdirAll(spoolDir, 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(store.SpoolPath(), []byte("{\"e\":1}\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(spoolDir, "audit-dropped"),
			[]byte("2026-07-30T04:05:06Z dropped 1 oversize record(s)\n"), 0o600); err != nil {
			t.Fatal(err)
		}

		cs := agentguard.Doctor(ctx, store)
		if len(cs) == 0 || cs[0].Name != "enrollment" || cs[0].Status != "fail" {
			t.Fatalf("first un-enrolled check = %+v, want the enrollment fail", cs)
		}
		byName := checksByName(cs)
		if c := byName["hook-binary"]; c.Status != "fail" || !strings.Contains(c.Detail, ghost) {
			t.Errorf("un-enrolled hook-binary = %+v, want a fail naming %s", c, ghost)
		}
		if c := byName["wiring"]; !strings.Contains(c.Detail, "codex (managed)") ||
			!strings.Contains(c.Detail, "claude-code (user)") {
			t.Errorf("un-enrolled wiring = %+v, want both lanes listed", c)
		}
		if c := byName["audit-dropped"]; c.Status != "fail" {
			t.Errorf("un-enrolled audit-dropped = %+v, want the loss to stay loud", c)
		}
		if c := byName["audit-spool"]; c.Status != "warn" ||
			!strings.Contains(c.Hint, "nothing can upload until this machine is enrolled") {
			t.Errorf("un-enrolled audit-spool = %+v, want the enroll-first hint variant", c)
		}
		for _, name := range []string{"identity", "server", "session", "snapshot", "killswitch"} {
			if c, ok := byName[name]; ok {
				t.Errorf("server-dependent check %s ran un-enrolled: %+v", name, c)
			}
		}
	})

	t.Run("codex user lane stays trust-honest", func(t *testing.T) {
		// Session{}, false must land codexHooksCheck in its degraded branch:
		// a fully wired user hooks.json with no readable trust record is a
		// warn: un-enrolled there is never a session to serve as evidence,
		// and file presence alone must not green the trust-gated lane.
		_, codexHome, store := isolate(t)
		liveBin := filepath.Join(t.TempDir(), "straza")
		if err := os.WriteFile(liveBin, []byte("#!/bin/sh\nexit 0\n"), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(codexHome, "hooks.json"),
			[]byte(fullRoster("codex", liveBin)), 0o600); err != nil {
			t.Fatal(err)
		}
		byName := checksByName(agentguard.Doctor(ctx, store))
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
		if hint := agentguard.HintFor(reason); hint == "" {
			t.Errorf("no doctor hint for reason %q", reason)
		}
	}
	if agentguard.HintFor("some totally unrelated error") != "" {
		t.Error("catalog matched an unrelated reason: substrings too broad")
	}
}
