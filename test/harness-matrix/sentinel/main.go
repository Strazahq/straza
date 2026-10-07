// sentinel is the harness-matrix hook recorder: it stands in for straza under
// the installer's real command string and records argv plus raw stdin, so the
// gates assert the harness-to-hook boundary (G-2) without trusting the harness.
//
//	SENTINEL_OUT   (required) JSONL file records are appended to.
//	SENTINEL_EXEC  (optional) space-separated argv of a real hook command
//	               (workdir paths never contain spaces, run.sh asserts it):
//	               stdin is relayed to it and its stdout, stderr and exit code
//	               are recorded AND propagated, so G-3 output-acceptance runs
//	               the real hook behind the recorder.
//	SENTINEL_ANSI  (optional, "1") ANSI probe: on SessionStart ONLY, print a
//	               codex-shaped SGR-red ack, exit 0; SENTINEL_EXEC wins.
//
// Exit: the relay's exit in exec mode, 0 in record and probe mode, 3 on
// recorder I/O failure (distinct from anything straza or a harness uses).
package main

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"strings"
)

type record struct {
	Argv       []string `json:"argv"`
	Stdin      string   `json:"stdin"`
	ExecArgv   []string `json:"exec_argv,omitempty"`
	ExecStdout string   `json:"exec_stdout,omitempty"`
	ExecStderr string   `json:"exec_stderr,omitempty"`
	ExecExit   *int     `json:"exec_exit,omitempty"`
	// Ack is what the SENTINEL_ANSI probe printed. It is deliberately NOT
	// exec_stdout: nothing here ran a real hook, and an observation must
	// never be mistakable for a relayed straza ack.
	Ack string `json:"ack,omitempty"`
}

// ansiMarker is the token fed to codex's systemMessage renderer, wrapped in
// SGR red with an uncolored tail so the captured harness output separates
// three outcomes: the raw ESC bytes survived, the escapes were stripped but
// the text survived, or nothing was rendered at all. Observation only:
// this file never asserts an answer.
const ansiMarker = "STRAZA-ANSI-RED"

// ansiAck builds the probe document. The shape is codex's OWN SessionStart
// output schema (hookSpecificOutput.hookEventName + additionalContext, plus
// the optional systemMessage), byte-identical in shape to what straza's
// encodeSessionStart writes when a session carries a context doc, so the
// probe measures the real rendering lane rather than a synthetic one.
// json.Marshal escapes the ESC bytes as \u001b, the only legal way to carry a
// control character inside a JSON string.
func ansiAck(event string) []byte {
	out, err := json.Marshal(map[string]any{
		"hookSpecificOutput": map[string]any{
			"hookEventName":     event,
			"additionalContext": "straza ansi probe",
		},
		"systemMessage": "\x1b[31m" + ansiMarker + "\x1b[0m plain-tail",
	})
	if err != nil {
		return nil
	}
	return append(out, '\n')
}

// eventName pulls hook_event_name out of a raw hook payload ("" when the
// payload is not JSON or names no event).
func eventName(stdin []byte) string {
	var payload map[string]any
	if json.Unmarshal(stdin, &payload) != nil {
		return ""
	}
	ev, _ := payload["hook_event_name"].(string)
	return ev
}

func main() {
	out := os.Getenv("SENTINEL_OUT")
	if out == "" {
		os.Exit(3)
	}
	stdin, _ := io.ReadAll(os.Stdin)
	rec := record{Argv: os.Args, Stdin: string(stdin)}

	exitCode := 0
	if cmdline := os.Getenv("SENTINEL_EXEC"); cmdline != "" {
		argv := strings.Fields(cmdline)
		cmd := exec.Command(argv[0], argv[1:]...) // #nosec G204 G702 -- the test lane's own config supplies the command
		cmd.Stdin = bytes.NewReader(stdin)
		var stdout, stderr bytes.Buffer
		cmd.Stdout, cmd.Stderr = &stdout, &stderr
		err := cmd.Run()
		code := 0
		if err != nil {
			code = 1
			if ee, ok := err.(*exec.ExitError); ok {
				code = ee.ExitCode()
			}
		}
		rec.ExecArgv, rec.ExecStdout, rec.ExecStderr, rec.ExecExit = argv, stdout.String(), stderr.String(), &code
		// Propagate byte-for-byte: the harness must judge the REAL hook's
		// output, not the recorder's.
		_, _ = os.Stdout.WriteString(stdout.String())
		_, _ = os.Stderr.WriteString(stderr.String())
		exitCode = code
	} else if os.Getenv("SENTINEL_ANSI") == "1" {
		// SessionStart only: it is the one codex event whose schema carries
		// a systemMessage, so it is the only place the question ("does ANSI
		// survive codex's rendering?") has a well-formed way to be asked.
		if ev := eventName(stdin); ev == "SessionStart" {
			if ack := ansiAck(ev); ack != nil {
				_, _ = os.Stdout.Write(ack)
				rec.Ack = string(ack)
			}
		}
	}

	f, err := os.OpenFile(out, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600) // #nosec G304 G703 -- test-lane record file, path from the lane's own env
	if err != nil {
		os.Exit(3)
	}
	if json.NewEncoder(f).Encode(rec) != nil || f.Close() != nil {
		os.Exit(3)
	}
	os.Exit(exitCode)
}
