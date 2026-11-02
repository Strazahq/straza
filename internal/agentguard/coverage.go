package agentguard

import (
	"sort"

	"github.com/strazahq/straza/internal/policy"
)

// CanonicalEventCoverage maps each canonical event kind to the sorted
// harness names whose embedded adapter maps a native hook onto it. The
// adapters are the runtime twin of spec/hook-profile/mappings (guarded by
// TestAdapterSpecMappingDrift), so this is the one source the server's
// harness support matrix and the events-never-fire advisory read; nothing
// upstream hard-codes per-harness event support.
func CanonicalEventCoverage() (map[string][]string, error) {
	all, err := LoadAdapters()
	if err != nil {
		return nil, err
	}
	cov := map[string][]string{}
	for harness, a := range all {
		seen := map[string]bool{}
		for _, canonical := range a.Events {
			if canonical == "" || seen[canonical] {
				continue
			}
			seen[canonical] = true
			cov[canonical] = append(cov[canonical], harness)
		}
	}
	for _, hs := range cov {
		sort.Strings(hs)
	}
	return cov, nil
}

// ClassifiedTools maps each harness tool name an embedded adapter puts in a
// lane to that lane's kind, across dialects. The by-name policy gate reads
// it, because a rule may allow by name only a tool Straza does not classify.
// When two dialects classify one name differently, the first harness in name
// order wins, and the gate uses the kind only to name it.
func ClassifiedTools() (map[string]string, error) {
	all, err := LoadAdapters()
	if err != nil {
		return nil, err
	}
	harnesses := make([]string, 0, len(all))
	for h := range all {
		harnesses = append(harnesses, h)
	}
	sort.Strings(harnesses)
	out := map[string]string{}
	for _, h := range harnesses {
		for name, kind := range all[h].Tools {
			if _, seen := out[name]; !seen && kind != policy.ToolOther {
				out[name] = kind
			}
		}
	}
	return out, nil
}
