package e2ematrix

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// budgets are the performance budgets the README's timers table prints.
// They are printed beside the observed value and never enforced here.
var budgets = map[string]string{
	"cold-start":          "under 1 s",
	"hook-overhead":       "p95 under 25 ms",
	"kill-switch-gateway": "none published (the target is under 5 s)",
	"kill-switch-hook":    "p99 under 2 s",
	"policy-gateway":      "none published",
	"policy-hook":         "none published",
	"catalog-unbind":      "none published",
	"approval-retry":      "none published",
	"session-start":       "none published",
}

// writeReport renders the markdown report: the header with the straza
// version, one row per scenario and dialect, then the timers table.
func (r *run) writeReport(path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return err
	}
	var b strings.Builder
	fmt.Fprintf(&b, "# e2e-matrix report\n\nstraza: `%s`\n\nrun: %s\n\n", r.version, time.Now().UTC().Format(time.RFC3339))
	if r.fatal != "" {
		fmt.Fprintf(&b, "**The run stopped before every scenario ran:** %s\n\n", r.fatal)
	}
	b.WriteString("## Scenarios\n\n| scenario | dialect | result | failing step | reason |\n|---|---|---|---|---|\n")
	for _, row := range r.rows {
		result, stepName, reason := "PASS", "", ""
		if !row.pass {
			result = "FAIL"
			stepName, reason, _ = strings.Cut(row.detail, ": ")
		}
		fmt.Fprintf(&b, "| %s | %s | %s | %s | %s |\n", row.scenario, row.dialect, result, cell(stepName), cell(reason))
	}
	if len(r.rows) == 0 {
		b.WriteString("| (no scenario ran) | | | | |\n")
	}
	b.WriteString("\n## Timers\n\n| timer | dialect | observed | budget |\n|---|---|---|---|\n")
	for _, row := range r.timerRows() {
		fmt.Fprintf(&b, "| %s | %s | %s | %s |\n", row[0], row[1], row[2], row[3])
	}
	return os.WriteFile(path, []byte(b.String()), 0o600)
}

// timerRows builds the timers table: cold-start first as the worst of every
// boot, hook-overhead p50 and p95 per dialect over the deciding hooks, then
// every other timer by name and dialect with the worst observation when it
// fired more than once (session-start is the worst per dialect).
func (r *run) timerRows() [][4]string {
	grouped := map[string][]time.Duration{}
	for _, tr := range r.timers {
		grouped[tr.name+"|"+tr.dialect] = append(grouped[tr.name+"|"+tr.dialect], tr.value)
	}
	var rows [][4]string
	if boots, ok := grouped["cold-start|-"]; ok {
		rows = append(rows, [4]string{"cold-start", "-", worstOf(boots), budgets["cold-start"]})
	}
	for _, d := range sortedKeys(r.hooks) {
		samples := append([]time.Duration(nil), r.hooks[d]...)
		sort.Slice(samples, func(i, j int) bool { return samples[i] < samples[j] })
		rows = append(rows,
			[4]string{"hook-overhead p50", d, ms(percentile(samples, 50)), fmt.Sprintf("(%d hooks) %s", len(samples), budgets["hook-overhead"])},
			[4]string{"hook-overhead p95", d, ms(percentile(samples, 95)), budgets["hook-overhead"]})
	}
	for _, key := range sortedKeys(grouped) {
		name, dialect, _ := strings.Cut(key, "|")
		if name == "cold-start" {
			continue
		}
		budget, ok := budgets[name]
		if !ok {
			budget = "none published"
		}
		rows = append(rows, [4]string{name, dialect, worstOf(grouped[key]), budget})
	}
	return rows
}

// worstOf prints the largest of the samples, with the count when there
// were several.
func worstOf(vals []time.Duration) string {
	worst := vals[0]
	for _, v := range vals {
		if v > worst {
			worst = v
		}
	}
	if len(vals) > 1 {
		return fmt.Sprintf("%s (worst of %d)", ms(worst), len(vals))
	}
	return ms(worst)
}

// ms prints a duration to a tenth of a millisecond.
func ms(d time.Duration) string {
	return d.Round(100 * time.Microsecond).String()
}

// percentile returns the pth percentile of sorted samples.
func percentile(sorted []time.Duration, p int) time.Duration {
	if len(sorted) == 0 {
		return 0
	}
	i := (len(sorted)*p + 99) / 100
	if i > 0 {
		i--
	}
	return sorted[i]
}

// cell makes a value safe inside a markdown table cell.
func cell(s string) string {
	s = strings.ReplaceAll(s, "|", "\\|")
	s = strings.ReplaceAll(s, "\n", " ")
	if len(s) > 400 {
		s = s[:400] + "..."
	}
	return s
}
