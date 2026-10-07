package policy

import "testing"

// TestDetectInterpreter pins the interpreter-tagging contract
// (spec/hook-profile `interpreter` attribute): the first non-wrapper argv
// token, basename-normalized, must match the known interpreter set, and
// everything else tags "".
func TestDetectInterpreter(t *testing.T) {
	cases := []struct {
		name    string
		command string
		argv    []string
		want    string
	}{
		{"python3 script", "python3 /tmp/x.py", nil, "python3"},
		{"bash dash c", `bash -c "rm -rf /tmp/x"`, nil, "bash"},
		{"plain rm is not an interpreter", "rm -rf /tmp/x", nil, ""},
		{"git is not an interpreter", "git status", nil, ""},
		{"absolute path node", "/usr/local/bin/node app.js", nil, "node"},
		{"env wrapper with assignment", "env FOO=1 python3 x.py", nil, "python3"},
		{"leading var assignment", "FOO=1 sh -c ls", nil, "sh"},
		{"sudo wrapper", "sudo python3 x.py", nil, "python3"},
		{"nohup wrapper", "nohup perl worker.pl", nil, "perl"},
		{"windows exe basename", `python.EXE -c "x"`, nil, "python"},
		{"windows path via argv", "", []string{`C:\Python312\python.EXE`, "-c", "x"}, "python"},
		// Raw-command splitting is POSIX (shellWords, SPEC §4): backslash
		// paths mangle and the tag conservatively under-reports (pinned).
		{"windows path in raw command under-reports", `C:\Python312\python.EXE -c "x"`, nil, ""},
		{"versioned python", "python3.12 -c 'x'", nil, "python3.12"},
		{"pythonic impostor", "python-config --libs", nil, ""},
		{"deno", "deno eval 'fetch(u)'", nil, "deno"},
		{"pwsh", "pwsh -Command Get-ChildItem", nil, "pwsh"},
		{"argv takes precedence", "echo decoy", []string{"bash", "-c", "x"}, "bash"},
		{"empty command", "", nil, ""},
		{"assignment only", "FOO=bar", nil, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := DetectInterpreter(tc.command, tc.argv); got != tc.want {
				t.Fatalf("DetectInterpreter(%q, %v) = %q, want %q", tc.command, tc.argv, got, tc.want)
			}
		})
	}
}
