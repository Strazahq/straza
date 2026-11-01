package agentguard

import (
	"reflect"
	"testing"
)

// TestCanonicalEventCoverage pins the embedded adapters' canonical-event
// coverage map, the runtime twin of spec/hook-profile/mappings (guarded
// against those tables by TestAdapterSpecMappingDrift). The events-honesty
// surfaces (the served support matrix and the events-never-fire advisory)
// key on exactly this data.
func TestCanonicalEventCoverage(t *testing.T) {
	cov, err := CanonicalEventCoverage()
	if err != nil {
		t.Fatalf("CanonicalEventCoverage: %v", err)
	}
	all := []string{"claude-code", "codex", "gemini", "python-sdk"}
	cases := []struct {
		event string
		want  []string
	}{
		{"tool.pre", all},
		{"session.start", all},
		{"session.end", all},
		{"subagent.start", []string{"claude-code", "codex"}},
		{"subagent.stop", []string{"claude-code", "codex"}},
		{"permission.request", []string{"claude-code", "codex"}},
		{"compact.pre", []string{"claude-code"}},
	}
	for _, c := range cases {
		if got := cov[c.event]; !reflect.DeepEqual(got, c.want) {
			t.Errorf("coverage[%s] = %v, want %v", c.event, got, c.want)
		}
	}
	for ev, hs := range cov {
		for i := 1; i < len(hs); i++ {
			if hs[i-1] >= hs[i] {
				t.Errorf("coverage[%s] not sorted: %v", ev, hs)
			}
		}
	}
}

// TestClassifiedTools pins the table the by-name policy gate reads: every
// name an embedded adapter classifies, with its kind, across dialects, and
// never the kit's identity entry for other, which classifies nothing.
func TestClassifiedTools(t *testing.T) {
	tools, err := ClassifiedTools()
	if err != nil {
		t.Fatalf("ClassifiedTools: %v", err)
	}
	for name, want := range map[string]string{
		"Bash": "shell.exec", "SubagentHandback": "task.spawn", "ArtifactCheck": "net.fetch",
		"apply_patch": "file.edit", "run_shell_command": "shell.exec", "file.read": "file.read",
	} {
		if got := tools[name]; got != want {
			t.Errorf("tools[%s] = %q, want %q", name, got, want)
		}
	}
	if kind, ok := tools["other"]; ok {
		t.Errorf("tools[other] = %q, want no entry", kind)
	}
}
