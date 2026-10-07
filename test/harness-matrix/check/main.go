// check asserts one harness-matrix gate from the evidence files a run left
// behind: sentinel JSONL records (what the hook child actually received and,
// in relay mode, what the real hook returned) and the harness's own stderr /
// log surfaces. Assertions never read what the harness config CLAIMS, only
// what happened.
//
// Gates are registered per harness (codex.go, gemini.go, claudecode.go: one
// file per lane, so lanes extend the binary without touching shared files).
// Flags are deliberately generic and reused across harnesses: -req is "the
// primary rendered config under test", -hooks-json the secondary one,
// -records the sentinel JSONL, -harness-stderr whatever stderr/log capture
// the lane made, -hook-bin the expected argv[0], -version the harness's
// version output.
//
// Exit 0 = gate passed; 1 = gate failed (details on stderr); 2 = usage.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"
)

// opts carries every flag; gates read the ones they document.
type opts struct {
	req        string
	hooksJSON  string
	records    string
	harnessErr string
	hookBin    string
	version    string
	want       string
}

// gates is the per-harness registry; each lane file init()-registers its own.
var gates = map[string]func(o *opts) []string{}

func main() {
	gate := flag.String("gate", "", "gate name (per-harness files register theirs)")
	o := &opts{}
	flag.StringVar(&o.req, "req", "", "primary rendered config under test")
	flag.StringVar(&o.hooksJSON, "hooks-json", "", "secondary rendered config (e.g. user-scope hooks.json)")
	flag.StringVar(&o.records, "records", "", "sentinel JSONL")
	flag.StringVar(&o.harnessErr, "harness-stderr", "", "harness stderr/log capture")
	flag.StringVar(&o.hookBin, "hook-bin", "", "expected argv[0] of the spawned hook")
	flag.StringVar(&o.version, "version", "", "harness version output")
	flag.StringVar(&o.want, "want", "", "expected content for gates that assert a value (e.g. the scripted reply)")
	flag.Parse()

	fn, ok := gates[*gate]
	if !ok {
		known := make([]string, 0, len(gates))
		for k := range gates {
			known = append(known, k)
		}
		fmt.Fprintf(os.Stderr, "check: unknown -gate (known: %s)\n", strings.Join(known, " "))
		os.Exit(2)
	}
	if errs := fn(o); len(errs) > 0 {
		for _, e := range errs {
			fmt.Fprintf(os.Stderr, "check[%s]: %s\n", *gate, e)
		}
		os.Exit(1)
	}
	fmt.Printf("check[%s]: ok\n", *gate)
}

// record is one sentinel line: what the harness handed the hook and, in relay
// mode, what the real hook returned.
type record struct {
	Argv       []string `json:"argv"`
	Stdin      string   `json:"stdin"`
	ExecStdout string   `json:"exec_stdout"`
	ExecStderr string   `json:"exec_stderr"`
	ExecExit   *int     `json:"exec_exit"`
}

func readRecords(path string) ([]record, error) {
	raw, err := os.ReadFile(path) // #nosec G304 -- test-lane file
	if err != nil {
		return nil, fmt.Errorf("sentinel records unreadable: %w", err)
	}
	var recs []record
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		if line == "" {
			continue
		}
		var r record
		if err := json.Unmarshal([]byte(line), &r); err != nil {
			return nil, fmt.Errorf("bad sentinel record %q: %w", line, err)
		}
		recs = append(recs, r)
	}
	return recs, nil
}

// eventOf extracts hook_event_name from a record's stdin payload ("" if the
// payload is not JSON or carries none).
func eventOf(r record) string {
	var payload map[string]any
	if err := json.Unmarshal([]byte(r.Stdin), &payload); err != nil {
		return ""
	}
	ev, _ := payload["hook_event_name"].(string)
	return ev
}
