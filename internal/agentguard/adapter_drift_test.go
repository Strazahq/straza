package agentguard

import (
	"os"
	"path/filepath"
	"testing"

	"gopkg.in/yaml.v3"
)

// specTable is the slice of a published mapping table the drift guard checks:
// the events and tools maps. (The embedded-adapter side is the full Adapter
// type; a spec table carries the same two maps plus prose the guard ignores.)
type specTable struct {
	Harness string            `yaml:"harness"`
	Events  map[string]string `yaml:"events"`
	Tools   map[string]string `yaml:"tools"`
}

// TestAdapterSpecMappingDrift is the drift tripwire of the hook dialects:
// the embedded adapter (behavior) and the published spec mapping tables
// (contract) must NOT silently diverge.
// Its failure message names the side that is missing a key, or the
// conflicting value, so the fix is obvious.
//
// A harness may publish several version-pinned tables (gemini ships v0.50 +
// antigravity) while ONE embedded adapter normalizes every build, so the
// contract is: the adapter equals the UNION of the harness's published tables,
// exactly: every adapter entry is published in some table, and every
// published entry is real adapter behavior. For a single-table harness the
// union is just that table.
func TestAdapterSpecMappingDrift(t *testing.T) {
	adapters := loadTestAdapters(t)
	// Relative path from internal/agentguard/, as TestFixtureCorpus does.
	root := filepath.Join("..", "..", "spec", "hook-profile", "mappings")
	dirs, err := os.ReadDir(root)
	if err != nil {
		t.Fatalf("read mappings root %s: %v", root, err)
	}
	seen := 0
	for _, d := range dirs {
		if !d.IsDir() {
			continue
		}
		harnessDir := d.Name()
		seen++
		t.Run(harnessDir, func(t *testing.T) {
			tables, err := filepath.Glob(filepath.Join(root, harnessDir, "*.yaml"))
			if err != nil {
				t.Fatalf("glob %s: %v", harnessDir, err)
			}
			if len(tables) == 0 {
				t.Fatalf("no mapping tables under %s", harnessDir)
			}

			unionEvents := map[string]string{}
			unionTools := map[string]string{}
			eventsFrom := map[string]string{} // key -> table file that first published it
			toolsFrom := map[string]string{}
			harness := ""
			for _, path := range tables {
				raw, err := os.ReadFile(path)
				if err != nil {
					t.Fatalf("read %s: %v", path, err)
				}
				var st specTable
				if err := yaml.Unmarshal(raw, &st); err != nil {
					t.Fatalf("parse %s: %v", path, err)
				}
				base := filepath.Base(path)
				if st.Harness == "" {
					t.Fatalf("%s: missing harness name", base)
				}
				if harness == "" {
					harness = st.Harness
				} else if st.Harness != harness {
					t.Fatalf("%s: harness %q disagrees with sibling table %q", base, st.Harness, harness)
				}
				mergeInto(t, "event", base, unionEvents, eventsFrom, st.Events)
				mergeInto(t, "tool", base, unionTools, toolsFrom, st.Tools)
			}

			a := adapters[harness]
			if a == nil {
				t.Fatalf("no embedded adapter for harness %q (dir %s)", harness, harnessDir)
			}
			compareMaps(t, harness, "event", a.Events, unionEvents)
			compareMaps(t, harness, "tool", a.Tools, unionTools)
		})
	}
	if seen < 4 {
		t.Errorf("expected ≥4 harness mapping dirs (claude-code, codex, gemini, python-sdk), saw %d", seen)
	}
}

// mergeInto folds one table's map into the running union, flagging a key that
// two version tables publish with DIFFERENT canonical values, itself a drift
// bug the guard must catch (the union would otherwise be ambiguous).
func mergeInto(t *testing.T, kind, file string, union, from, in map[string]string) {
	t.Helper()
	for k, v := range in {
		if prev, ok := union[k]; ok {
			if prev != v {
				t.Errorf("spec tables disagree on %s %q: %s says %q but %s says %q",
					kind, k, from[k], prev, file, v)
			}
			continue
		}
		union[k] = v
		from[k] = file
	}
}

// compareMaps asserts adapter == union(spec tables), reporting each side's
// missing keys and any value mismatch by name so the tripwire is actionable.
func compareMaps(t *testing.T, harness, kind string, adapter, spec map[string]string) {
	t.Helper()
	for k, av := range adapter {
		sv, ok := spec[k]
		if !ok {
			t.Errorf("DRIFT [%s]: adapter maps %s %q=%q but NO published spec table has it. Add it to spec/hook-profile/mappings/%s/",
				harness, kind, k, av, harness)
			continue
		}
		if sv != av {
			t.Errorf("DRIFT [%s]: %s %q value mismatch (adapter=%q, spec table=%q)",
				harness, kind, k, av, sv)
		}
	}
	for k, sv := range spec {
		if _, ok := adapter[k]; !ok {
			t.Errorf("DRIFT [%s]: spec table publishes %s %q=%q but the adapter lacks it. Add it to adapters/%s.yaml (or remove it from the table)",
				harness, kind, k, sv, harness)
		}
	}
}
