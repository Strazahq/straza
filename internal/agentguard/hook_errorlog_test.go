package agentguard

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// runFailingHook pipes one payload through RunHook under a fresh STRAZA_HOME
// and returns the store, the hook's streams, and its error.
func runFailingHook(t *testing.T, harness, payload string) (*Store, *bytes.Buffer, *bytes.Buffer, error) {
	t.Helper()
	t.Setenv("STRAZA_HOME", t.TempDir())
	store, err := OpenStore()
	if err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	hookErr := RunHook(HookIO{
		Harness: harness,
		Stdin:   strings.NewReader(payload),
		Stdout:  &stdout,
		Stderr:  &stderr,
		Environ: func() []string { return nil },
	})
	return store, &stdout, &stderr, hookErr
}

// TestHookFailureWritesErrorLog is the in-process positive control: the shape
// `echo {} | straza hook --harness codex` must exit 2 AND leave exactly one
// readable record behind.
func TestHookFailureWritesErrorLog(t *testing.T) {
	store, _, stderr, err := runFailingHook(t, "codex", "{}")

	var deny interface{ Code() int }
	if !errors.As(err, &deny) || deny.Code() != 2 {
		t.Fatalf("want exit-2 deny, got %v", err)
	}
	if !strings.Contains(stderr.String(), "unmapped codex event") {
		t.Fatalf("stderr = %q", stderr.String())
	}

	errs, unreadable := ClientErrors(store)
	if unreadable != 0 || len(errs) != 1 {
		t.Fatalf("got %d records + %d unreadable, want exactly 1 + 0", len(errs), unreadable)
	}
	rec := errs[0]
	if rec.Kind != "hook" || rec.Harness != "codex" || rec.Exit != 2 ||
		!strings.Contains(rec.Err, "unmapped codex event") {
		t.Errorf("record = %+v", rec)
	}
}

// TestHookErrorLogCarriesEventName: when the payload parses far enough to name
// its native event, the record carries it.
func TestHookErrorLogCarriesEventName(t *testing.T) {
	store, _, _, err := runFailingHook(t, "codex", `{"hook_event_name":"NotARealEvent"}`)
	if err == nil {
		t.Fatal("want a fail-closed error")
	}
	errs, _ := ClientErrors(store)
	if len(errs) != 1 || errs[0].Event != "NotARealEvent" {
		t.Fatalf("event not carried: %+v", errs)
	}
}

// TestHookErrorLogNeverHoldsPayload plants a canary in every payload position a
// failing hook sees (valid-JSON fields and a malformed body) and asserts the
// canary never reaches the log file. This is the privacy stance as a test.
func TestHookErrorLogNeverHoldsPayload(t *testing.T) {
	const canary = "CANARY-PAYLOAD-7f3a9"
	payloads := []string{
		// unmapped event, canary in prompt/command fields
		`{"hook_event_name":"NotARealEvent","prompt":"` + canary + `","tool_input":{"command":"` + canary + `"}}`,
		// malformed JSON carrying the canary
		`{"hook_event_name": ` + canary,
	}
	for i, payload := range payloads {
		store, _, _, err := runFailingHook(t, "codex", payload)
		if err == nil {
			t.Fatalf("payload %d: want a fail-closed error", i)
		}
		raw, readErr := os.ReadFile(store.ErrorLogPath())
		if readErr != nil {
			t.Fatalf("payload %d: no error log written: %v", i, readErr)
		}
		if bytes.Contains(raw, []byte(canary)) {
			t.Fatalf("payload %d: canary leaked into the error log: %s", i, raw)
		}
	}
}

// TestHookErrorLogBestEffortIsolation: an unwritable error-log location must
// not change the hook's observable behavior: same stdout bytes, same exit.
func TestHookErrorLogBestEffortIsolation(t *testing.T) {
	const payload = `{"hook_event_name":"NotARealEvent"}`

	_, wantOut, _, wantErr := runFailingHook(t, "codex", payload)

	// Second home: state is a regular file, so the log write path is dead.
	t.Setenv("STRAZA_HOME", t.TempDir())
	home, _ := Home()
	if err := os.MkdirAll(home, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "state"), []byte("not a dir"), 0o600); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	gotErr := RunHook(HookIO{Harness: "codex", Stdin: strings.NewReader(payload),
		Stdout: &stdout, Stderr: &stderr, Environ: func() []string { return nil }})

	if stdout.String() != wantOut.String() {
		t.Errorf("stdout changed when the log was unwritable: %q vs %q", stdout.String(), wantOut.String())
	}
	var a, b interface{ Code() int }
	if !errors.As(wantErr, &a) || !errors.As(gotErr, &b) || a.Code() != b.Code() {
		t.Errorf("exit changed when the log was unwritable: %v vs %v", wantErr, gotErr)
	}
}

// TestRecordHookFailure covers the cmd-layer wrapper that catches the exit-1
// class (errors RunHook returns bare, like a codex Stop encode failure).
func TestRecordHookFailure(t *testing.T) {
	store := testErrorStore(t)

	RecordHookFailure("codex", nil) // nil: no-op
	if errs, _ := ClientErrors(store); len(errs) != 0 {
		t.Fatalf("nil error logged: %+v", errs)
	}

	// A deny-coded error was already recorded at the fail-closed choke point,
	// so the wrapper must not double-log it.
	RecordHookFailure("codex", exitDenyError{code: 2})
	if errs, _ := ClientErrors(store); len(errs) != 0 {
		t.Fatalf("deny-coded error double-logged: %+v", errs)
	}

	RecordHookFailure("codex", fmt.Errorf("write /dev/stdout: broken pipe"))
	errs, _ := ClientErrors(store)
	if len(errs) != 1 || errs[0].Kind != "hook" || errs[0].Harness != "codex" ||
		errs[0].Exit != 1 || !strings.Contains(errs[0].Err, "broken pipe") {
		t.Fatalf("bare error not recorded as exit 1: %+v", errs)
	}
}
