package agentguard

import "testing"

// FuzzHookPayload hammers payload parsing + normalization across every
// harness dialect. Hook payloads arrive from harness processes the
// user controls; malformed ones must fail closed with an error, never crash
// the hook (a panicking hook is an enforcement bypass on harnesses that
// treat a dead hook as allow).
func FuzzHookPayload(f *testing.F) {
	adapters, err := LoadAdapters()
	if err != nil {
		f.Fatal(err)
	}
	for _, seed := range []string{
		`{"hook_event_name":"SessionStart","session_id":"s1","cwd":"/work"}`,
		`{"hook_event_name":"PreToolUse","session_id":"s1","cwd":"/w","tool_name":"Bash","tool_input":{"command":"git status"}}`,
		`{"hook_event_name":"PreToolUse","tool_name":"Write","tool_input":{"file_path":"/etc/passwd","content":"x"}}`,
		`{"hookEventName":"BeforeTool","toolCall":{"name":"run_shell_command","args":{"command":"ls"}}}`,
		`{"hook_event_name":"Stop","session_id":"s1"}`,
		`{"tool_input":{"command":123}}`,
		`{}`,
		`[]`,
		`{"hook_event_name":`,
	} {
		f.Add([]byte(seed))
	}
	f.Fuzz(func(_ *testing.T, raw []byte) {
		payload, err := ParsePayload(raw)
		if err != nil {
			return
		}
		for _, a := range adapters {
			_, _ = Normalize(a, payload)
		}
		_ = DetectHarness("", map[string]string{}, payload, adapters)
	})
}
