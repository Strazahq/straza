// Command recordfixtures turns harness-matrix sentinel captures into
// normalization fixtures for spec/conformance/hooks/, the anti-transcription
// lane: the sentinel records what a REAL harness binary handed the hook
// boundary (argv plus raw stdin, test/harness-matrix/sentinel), and this tool
// replays each payload through the REAL embedded adapter, writing the payload
// and canonical event as a pair stamped with how it was captured, so a minted
// fixture can never claim more than the lane that produced it.
//
// Identifier values are SANITIZED to stable placeholders (session and turn
// ids, cwd, transcript paths) so fixtures do not churn per run and do not
// publish box-local paths; every other byte of the payload is verbatim. The
// expected event is computed AFTER sanitization, so the corpus test replays
// exactly what this tool verified. Review the diff before committing minted
// fixtures: the adapter's answer is the recorded truth, so a wrong adapter
// pins the wrong truth, and that review is the human step.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	agentguard "github.com/strazahq/straza/internal/agentguard"
)

// sanitize maps payload keys to stable placeholder values. Only keys that
// carry per-run identifiers or box-local paths are rewritten; absence is
// preserved (a key not in the payload is never added).
var sanitize = map[string]string{
	"session_id":      "abc",
	"turn_id":         "turn-1",
	"cwd":             "/work/proj",
	"transcript_path": "/home/u/.codex/sessions/2026/07/31/rollout-x.jsonl",
}

// fixtureName maps a harness-native event name to the corpus file basename.
var fixtureName = map[string]string{
	"SessionStart":      "session-start",
	"UserPromptSubmit":  "prompt-submit",
	"Stop":              "stop",
	"SessionEnd":        "session-end",
	"PreToolUse":        "tool-pre",
	"PostToolUse":       "tool-post",
	"PermissionRequest": "permission-request",
	"SubagentStart":     "subagent-start",
	"SubagentStop":      "subagent-stop",
}

type record struct {
	Stdin string `json:"stdin"`
}

func main() {
	records := flag.String("records", "", "sentinel records.jsonl (required)")
	harness := flag.String("harness", "", "adapter dialect the payloads speak (required)")
	events := flag.String("events", "", "comma-separated native event names to mint (required)")
	provenance := flag.String("provenance", "", `provenance stamp; must start "live " (a sentinel capture IS live) (required)`)
	out := flag.String("out", "", "output fixture directory (required)")
	flag.Parse()
	if *records == "" || *harness == "" || *events == "" || *provenance == "" || *out == "" {
		flag.Usage()
		os.Exit(2)
	}
	if !strings.HasPrefix(*provenance, "live ") {
		fatal("provenance must start with \"live \": a sentinel record is a live capture; doc-derived fixtures are hand-authored, not minted")
	}
	wanted := map[string]bool{}
	for _, e := range strings.Split(*events, ",") {
		wanted[strings.TrimSpace(e)] = true
	}

	adapters, err := agentguard.LoadAdapters()
	if err != nil {
		fatal("%v", err)
	}
	adapter := adapters[*harness]
	if adapter == nil {
		fatal("no adapter for harness %q", *harness)
	}

	raw, err := os.ReadFile(*records)
	if err != nil {
		fatal("%v", err)
	}
	if err := os.MkdirAll(*out, 0o750); err != nil {
		fatal("%v", err)
	}

	minted := 0
	for _, line := range strings.Split(string(raw), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var rec record
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			fatal("bad record line: %v", err)
		}
		var payload map[string]any
		if err := json.Unmarshal([]byte(rec.Stdin), &payload); err != nil {
			continue // sentinel also records non-JSON probes; skip
		}
		event, _ := payload["hook_event_name"].(string)
		if !wanted[event] {
			continue
		}
		name, ok := fixtureName[event]
		if !ok {
			fatal("no fixture name mapping for event %q", event)
		}
		wanted[event] = false // first capture per event wins; later dupes skip

		for k, v := range sanitize {
			if _, present := payload[k]; present {
				payload[k] = v
			}
		}
		n, err := agentguard.Normalize(adapter, payload)
		if err != nil {
			fatal("%s: the real adapter refuses this payload: %v", event, err)
		}
		fx := map[string]any{
			"provenance": *provenance,
			"harness":    *harness,
			"payload":    payload,
			"expect":     n.Event,
		}
		buf, err := json.Marshal(fx)
		if err != nil {
			fatal("%v", err)
		}
		path := filepath.Join(*out, name+".json")
		if err := os.WriteFile(path, append(buf, '\n'), 0o600); err != nil {
			fatal("%v", err)
		}
		fmt.Printf("minted %s (%s → %s)\n", path, event, n.Event.Kind)
		minted++
	}
	for e, still := range wanted {
		if still {
			fmt.Fprintf(os.Stderr, "recordfixtures: WARNING, no %s record in %s (not minted)\n", e, *records)
		}
	}
	if minted == 0 {
		fatal("nothing minted")
	}
}

func fatal(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "recordfixtures: "+format+"\n", args...)
	os.Exit(1)
}
