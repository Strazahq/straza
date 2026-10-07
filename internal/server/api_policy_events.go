package server

import (
	"fmt"
	"net/http"
	"slices"
	"strings"
	"sync"

	"github.com/strazahq/straza/internal/agentguard"
	"github.com/strazahq/straza/internal/policy"
)

// Events honesty: the event taxonomy is not universal. The MCP
// gateway evaluates exactly one kind (tool.pre, gateway_tools.go), and hook
// support varies per harness. The server serves the support matrix (one
// source of truth: the embedded adapters, drift-tested against
// spec/hook-profile/mappings) and validate says when a rule's event set
// cannot fire, so the console never hard-codes coverage.

// eventSupportRow is one served matrix row. Blocking mirrors the hook
// profile's contract: only tool.pre and permission.request may block; the
// rest observe.
type eventSupportRow struct {
	Kind      string   `json:"kind"`
	Blocking  bool     `json:"blocking"`
	Harnesses []string `json:"harnesses"`
}

// canonicalEventOrder is the engine's vocabulary in const order
// (internal/policy/types.go); the served matrix and the builder's chips
// follow it.
var canonicalEventOrder = []string{
	policy.EventSessionStart, policy.EventPromptSubmit, policy.EventToolPre,
	policy.EventToolPost, policy.EventPermissionRequest, policy.EventSubagentStart,
	policy.EventSubagentStop, policy.EventSessionEnd, policy.EventCompactPre,
}

// eventSupport assembles the matrix once: the adapters are embedded data,
// so the answer never changes within a process.
var eventSupport = sync.OnceValues(func() (struct {
	Rows      []eventSupportRow
	Coverage  map[string][]string
	Harnesses []string
}, error) {
	var out struct {
		Rows      []eventSupportRow
		Coverage  map[string][]string
		Harnesses []string
	}
	cov, err := agentguard.CanonicalEventCoverage()
	if err != nil {
		return out, err
	}
	seen := map[string]bool{}
	for _, hs := range cov {
		for _, h := range hs {
			seen[h] = true
		}
	}
	harnesses := make([]string, 0, len(seen))
	for h := range seen {
		harnesses = append(harnesses, h)
	}
	slices.Sort(harnesses)
	rows := make([]eventSupportRow, 0, len(canonicalEventOrder))
	for _, kind := range canonicalEventOrder {
		hs := cov[kind]
		if hs == nil {
			hs = []string{}
		}
		rows = append(rows, eventSupportRow{
			Kind:      kind,
			Blocking:  kind == policy.EventToolPre || kind == policy.EventPermissionRequest,
			Harnesses: hs,
		})
	}
	out.Rows, out.Coverage, out.Harnesses = rows, cov, harnesses
	return out, nil
})

// handlePolicyEventSupport serves the harness support matrix. Admin plane,
// static embedded data, no store access.
func (a *App) handlePolicyEventSupport(w http.ResponseWriter, r *http.Request) {
	sup, err := eventSupport()
	if err != nil {
		a.fail(w, r, http.StatusInternalServerError, "event support unavailable", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"events":    sup.Rows,
		"harnesses": sup.Harnesses,
	})
}

// policyAdvisories is the one advisory list both validate and the
// activate-time log speak: the document's own truths (internal/policy) plus
// the server-known events coverage.
func (a *App) policyAdvisories(doc policy.Document) []policy.Advisory {
	return append(policy.Advisories(doc), a.eventsCoverageAdvisories(doc)...)
}

// eventsCoverageAdvisories emits events-never-fire (class S: believed
// enforced, is not) for rules whose event set excludes the only event the
// MCP gateway evaluates, and for rules whose events cannot fire for some
// mapped harness. Parse has already defaulted absent events to [tool.pre],
// which every harness emits, so untouched rules draw nothing.
func (a *App) eventsCoverageAdvisories(doc policy.Document) []policy.Advisory {
	sup, err := eventSupport()
	if err != nil {
		// Embedded data failing to parse is a build defect, not an authoring
		// truth; advisories stay non-fatal, so say nothing here (the endpoint
		// surfaces the failure loudly).
		a.log.Warn("events coverage unavailable", "err", err)
		return nil
	}
	var out []policy.Advisory
	for _, r := range doc.Spec.Rules {
		if slices.Contains(r.Tools, policy.ToolMCPCall) && !slices.Contains(r.Events, policy.EventToolPre) {
			out = append(out, policy.Advisory{
				Code:     policy.AdvisoryEventsNeverFire,
				Severity: policy.AdvisorySeverityWarn,
				Rule:     r.ID,
				Text: fmt.Sprintf(
					"rule %q: its events exclude tool.pre, the only event the MCP gateway evaluates; this rule never fires for MCP calls",
					r.ID),
			})
		}
		var gaps []string
		for _, h := range sup.Harnesses {
			fires := false
			for _, e := range r.Events {
				if slices.Contains(sup.Coverage[e], h) {
					fires = true
					break
				}
			}
			if !fires {
				gaps = append(gaps, h)
			}
		}
		if len(gaps) > 0 {
			out = append(out, policy.Advisory{
				Code:     policy.AdvisoryEventsNeverFire,
				Severity: policy.AdvisorySeverityWarn,
				Rule:     r.ID,
				Text: fmt.Sprintf(
					"rule %q: its only events (%s) never fire for sessions from %s harnesses; sessions from those harnesses are not covered by this rule",
					r.ID, strings.Join(r.Events, ", "), orJoin(gaps)),
			})
		}
	}
	return out
}

// orJoin renders a name list the way the advisory sentence reads: "x",
// "x or y", "x, y or z".
func orJoin(names []string) string {
	switch len(names) {
	case 0:
		return ""
	case 1:
		return names[0]
	default:
		return strings.Join(names[:len(names)-1], ", ") + " or " + names[len(names)-1]
	}
}
