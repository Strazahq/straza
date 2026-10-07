package main

import (
	"encoding/json"
	"fmt"
	"os"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"
)

// Probe is one performance budget measurement. Value/Limit are in the probe's natural
// unit (documented in Budget) so CI and humans read the same numbers.
type Probe struct {
	Name     string  `json:"name"`
	Budget   string  `json:"budget"`
	Observed string  `json:"observed"`
	Value    float64 `json:"value"`
	Limit    float64 `json:"limit"`
	Pass     bool    `json:"pass"`
	Skipped  bool    `json:"skipped,omitempty"`
	Detail   string  `json:"detail,omitempty"`
}

// Report is the published perf artifact.
type Report struct {
	Time      string            `json:"time"`
	GoVersion string            `json:"go"`
	OS        string            `json:"os"`
	Arch      string            `json:"arch"`
	CPUs      int               `json:"cpus"`
	Params    map[string]string `json:"params"`
	Probes    []Probe           `json:"probes"`
	Pass      bool              `json:"pass"`
}

func newReport(params map[string]string) *Report {
	return &Report{
		Time:      time.Now().UTC().Format(time.RFC3339),
		GoVersion: runtime.Version(),
		OS:        runtime.GOOS,
		Arch:      runtime.GOARCH,
		CPUs:      runtime.NumCPU(),
		Params:    params,
		Pass:      true,
	}
}

func (r *Report) add(p Probe) {
	r.Probes = append(r.Probes, p)
	if !p.Pass && !p.Skipped {
		r.Pass = false
	}
	status := "PASS"
	if p.Skipped {
		status = "SKIP"
	} else if !p.Pass {
		status = "FAIL"
	}
	fmt.Printf("[%s] %-18s %s (budget %s)\n", status, p.Name, p.Observed, p.Budget)
	if p.Detail != "" {
		fmt.Printf("       %s\n", p.Detail)
	}
}

// write emits the JSON artifact plus a Markdown twin next to it.
func (r *Report) write(path string) error {
	raw, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		return err
	}

	var md strings.Builder
	fmt.Fprintf(&md, "# Straza perf report (perf budgets)\n\n")
	fmt.Fprintf(&md, "%s · %s %s/%s · %d CPUs\n\n", r.Time, r.GoVersion, r.OS, r.Arch, r.CPUs)
	fmt.Fprintf(&md, "| probe | budget | observed | result |\n|---|---|---|---|\n")
	for _, p := range r.Probes {
		res := "✅"
		if p.Skipped {
			res = "⏭ skipped"
		} else if !p.Pass {
			res = "❌ **over budget**"
		}
		fmt.Fprintf(&md, "| %s | %s | %s | %s |\n", p.Name, p.Budget, p.Observed, res)
	}
	fmt.Fprintf(&md, "\nParameters: ")
	keys := make([]string, 0, len(r.Params))
	for k := range r.Params {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for i, k := range keys {
		if i > 0 {
			fmt.Fprint(&md, ", ")
		}
		fmt.Fprintf(&md, "%s=%s", k, r.Params[k])
	}
	fmt.Fprintln(&md)
	mdPath := strings.TrimSuffix(path, ".json") + ".md"
	return os.WriteFile(mdPath, []byte(md.String()), 0o600)
}

// latencies is a concurrency-safe sample recorder sized for the harness
// (≤ a few hundred thousand samples; exact percentiles, no binning).
type latencies struct {
	mu sync.Mutex
	ns []int64
}

func (l *latencies) add(d time.Duration) {
	l.mu.Lock()
	l.ns = append(l.ns, int64(d))
	l.mu.Unlock()
}

func (l *latencies) count() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.ns)
}

// percentile returns the p-th percentile (0 < p ≤ 100) of the samples.
func (l *latencies) percentile(p float64) time.Duration {
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(l.ns) == 0 {
		return 0
	}
	sorted := append([]int64(nil), l.ns...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	idx := int(float64(len(sorted))*p/100+0.5) - 1
	if idx < 0 {
		idx = 0
	}
	if idx >= len(sorted) {
		idx = len(sorted) - 1
	}
	return time.Duration(sorted[idx])
}

func (l *latencies) max() time.Duration {
	l.mu.Lock()
	defer l.mu.Unlock()
	var m int64
	for _, v := range l.ns {
		if v > m {
			m = v
		}
	}
	return time.Duration(m)
}
