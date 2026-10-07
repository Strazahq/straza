package agentguard

import "sort"

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
