package agentguard

import (
	"errors"
	"os"
	"strings"
	"testing"
)

// The parse-layer blind spot, seen with codex-cli 0.146.0 on Windows: cobra
// rejects mangled argv BEFORE the hook RunE, so the in-command
// RecordHookFailure never runs and the error log would be blind at exactly
// the failing layer. RecordHookArgvFailure is the outermost net.
func TestRecordHookArgvFailure(t *testing.T) {
	cases := []struct {
		name    string
		args    []string
		err     error
		want    int
		wantSub []string
	}{
		{"parse error records argv", []string{"straza", "hook", "--harness codex"},
			errors.New(`unknown flag: --harness codex`), 1,
			[]string{"argv did not parse", `unknown flag`, `"--harness codex"`}},
		{"nil error is a no-op", []string{"straza", "hook"}, nil, 0, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("STRAZA_HOME", t.TempDir())
			RecordHookArgvFailure(tc.args, tc.err)
			store, err := OpenStore()
			if err != nil {
				t.Fatalf("OpenStore: %v", err)
			}
			errs, unreadable := ClientErrors(store)
			if unreadable != 0 {
				t.Fatalf("unreadable lines: %d", unreadable)
			}
			if len(errs) != tc.want {
				t.Fatalf("records = %d, want %d", len(errs), tc.want)
			}
			if tc.want == 0 {
				return
			}
			rec := errs[0]
			if rec.Kind != "hook" || rec.Exit != 1 {
				t.Fatalf("record kind/exit = %q/%d, want hook/1", rec.Kind, rec.Exit)
			}
			for _, sub := range tc.wantSub {
				if !strings.Contains(rec.Err, sub) {
					t.Fatalf("record err %q missing %q", rec.Err, sub)
				}
			}
		})
	}
}

// The stdin payload must never ride into an argv record: argv is the wiring's
// own command line, and this test pins that the recorder never reads stdin.
func TestRecordHookArgvFailureNeverReadsStdin(t *testing.T) {
	t.Setenv("STRAZA_HOME", t.TempDir())
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = r.Close() }()
	canary := `{"payload":"CANARY-not-for-the-log"}`
	if _, err := w.WriteString(canary); err != nil {
		t.Fatal(err)
	}
	_ = w.Close()
	old := os.Stdin
	os.Stdin = r
	defer func() { os.Stdin = old }()

	RecordHookArgvFailure([]string{"straza", "hook", "--bogus"}, errors.New("unknown flag: --bogus"))

	store, err := OpenStore()
	if err != nil {
		t.Fatal(err)
	}
	errs, _ := ClientErrors(store)
	if len(errs) != 1 {
		t.Fatalf("records = %d, want 1", len(errs))
	}
	if strings.Contains(errs[0].Err, "CANARY") {
		t.Fatalf("stdin payload leaked into the argv record: %q", errs[0].Err)
	}
	// The payload must still be sitting unread in the pipe.
	buf := make([]byte, len(canary))
	n, _ := r.Read(buf)
	if got := string(buf[:n]); got != canary {
		t.Fatalf("stdin was consumed by the recorder: read back %q", got)
	}
}
