package ctl

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"

	"github.com/strazahq/straza/internal/policy"
)

// Policy simulate is the CLI half of the console's what-if. The rendering
// mirrors the console's why sentence in web/ui/src/lib/audit-words.ts, function
// whyOf: the outcome, then the decided-by sentence ("decided by rule X in
// policy Y: reason"), then the context. The golden tests in policysim_test.go
// pin every sentence, so a wording change here changes the console too, or the
// two surfaces stop telling one story.

// SimulateSubject is the request's subject block: a user to resolve
// server-side, or explicit roles for a hypothetical, plus optional
// attestation (none|advisory|managed).
type SimulateSubject struct {
	User        string   `json:"user,omitempty"`
	Roles       []string `json:"roles,omitempty"`
	Attestation string   `json:"attestation,omitempty"`
}

// SimulateRequest is the wire shape of POST /v1/admin/policies/simulate
// (internal/server simulateRequest): one event, one subject, optionally a
// draft PolicySet YAML overlaid in place of its same-named stored set.
type SimulateRequest struct {
	Event   policy.Event    `json:"event"`
	Subject SimulateSubject `json:"subject"`
	Draft   string          `json:"draft,omitempty"`
}

// SimulateResult is the simulate response: the active-snapshot decision, the
// draft decision when a draft was sent, the resolved subject, and the id of
// the snapshot the evaluation ran against.
type SimulateResult struct {
	Active   policy.Decision  `json:"active"`
	Draft    *policy.Decision `json:"draft,omitempty"`
	Subject  policy.Subject   `json:"subject"`
	Snapshot string           `json:"snapshot"`
}

// SimulatePolicy runs the server-side what-if: the same endpoint, engine, and
// snapshot the console's Simulate drawer uses.
func (c *Client) SimulatePolicy(ctx context.Context, req SimulateRequest) (SimulateResult, error) {
	var out SimulateResult
	return out, c.Do(ctx, http.MethodPost, "/v1/admin/policies/simulate", req, &out)
}

// OverviewLite is the slice of GET /v1/admin/overview the simulate rendering
// needs: the governance profile (for the default-decision wording) and the
// live snapshot id (for the "(live)" marker).
type OverviewLite struct {
	Profile    string `json:"profile"`
	SnapshotID string `json:"snapshot_id"`
}

// OverviewLite reads the admin overview. Callers treat failure as a degrade,
// never fatal: without it the rendering uses the generic profile wording and
// makes no live-ness claim.
func (c *Client) OverviewLite(ctx context.Context) (OverviewLite, error) {
	var out OverviewLite
	return out, c.Do(ctx, http.MethodGet, "/v1/admin/overview", nil, &out)
}

// SimContext is the now-truth evidence behind the rendering. Zero values mean
// "unknown": the profile sentence stays generic and no "(live)" marker is
// printed. No evidence, no claim (the console's record-first rule).
type SimContext struct {
	Profile      string
	LiveSnapshot string
}

// SimContextLine is the now-truth sentence under every simulate answer. It
// calls the -f overlay a file, because a draft is a config draft.
const SimContextLine = "Simulated against the policies live right now, your file included if you passed one. This is today's answer, not a replay of any past decision."

// SimVerdictAgree and SimVerdictDiffer open the verdict line of a simulate
// run with a file. The agent skill's replay script parses them, so
// tools/pluginsgate refuses a script under plugins/ that stops quoting them.
const (
	SimVerdictAgree  = "live and file agree"
	SimVerdictDiffer = "the live policy says"
)

// engineRefusal opens the engine's default deny of a tool outside the
// taxonomy and of an event that names no tool (policy.Engine's
// defaultDecision).
const engineRefusal = "Straza: denied, because "

// simNoteLine is the propagation-honesty footer: simulate proves the
// decision, not that any particular client is enrolled or current.
const simNoteLine = "this verdict applies once this snapshot reaches the client; straza doctor proves a client is governed"

// RenderSimulation writes a simulate result in the terminal layout: single
// mode is the full why-view card, and dual mode (draft present) is the
// console's active/draft rows plus the delta sentence.
func RenderSimulation(w io.Writer, res SimulateResult, now SimContext) error {
	if res.Draft != nil {
		renderSimDual(w, res, now)
	} else {
		renderSimSingle(w, res, now)
	}
	return nil
}

func renderSimSingle(w io.Writer, res SimulateResult, now SimContext) {
	d := res.Active
	if d.Effect == "deny" {
		fmt.Fprintln(w, "This call would be denied.")
	} else {
		fmt.Fprintln(w, "This call would be allowed.")
	}
	for _, g := range gateLines(d) {
		writeKeyed(w, "gate", g)
	}
	fmt.Fprintln(w, provenanceLine(d, now.Profile))
	fmt.Fprintln(w, SimContextLine)
	if d.RuleID == "" && d.Effect == "deny" && d.Default {
		if strings.HasPrefix(d.Reason, engineRefusal) {
			// No rule can allow a tool outside the taxonomy, or no tool, so
			// the engine's sentence, which lists the known tools, replaces
			// the remedy.
			r := strings.TrimPrefix(d.Reason, "Straza: ")
			fmt.Fprintln(w, strings.ToUpper(r[:1])+r[1:])
		} else {
			who := "the subject"
			if res.Subject.User != "" {
				who = res.Subject.User
			}
			fmt.Fprintf(w, "To allow an MCP tool, list it in a role of its MCP server that %s holds, or create one with strazactl roles create <server>-<word> --app <server> --tools <tool> and assign it. "+
				"It then runs unless a policy gates it. A local tool needs an allow rule. Test either change with a file: -f policy.yaml\n", who)
		}
	}
	fmt.Fprintln(w)
	writeSimFooters(w, res, now, true)
}

func renderSimDual(w io.Writer, res SimulateResult, now SimContext) {
	rows := []struct {
		label string
		d     policy.Decision
	}{{"live", res.Active}, {"file", *res.Draft}}
	for _, row := range rows {
		fmt.Fprintf(w, "%-6s  %-5s  %s\n", row.label, strings.ToUpper(row.d.Effect), provenanceLine(row.d, now.Profile))
		for _, g := range gateLines(row.d) {
			fmt.Fprintf(w, "%15s%s\n", "", "gate: "+g)
		}
	}
	fmt.Fprintln(w)
	fmt.Fprintln(w, simVerdict(res.Active, *res.Draft))
	fmt.Fprintln(w, SimContextLine)
	fmt.Fprintln(w)
	writeSimFooters(w, res, now, false)
}

// simVerdict is the delta sentence under the live and file rows. The two
// agree only when the effect and every gate line match, so a file that adds,
// drops or changes a hold reads as a change, and the replay script, which
// counts the lines that open with SimVerdictDiffer, counts it. Each side is
// named by its outcome word; when the words match and the gates do not, the
// sentence says which part differs and points at the gate lines.
func simVerdict(live, draft policy.Decision) string {
	lw, dw := outcomeWord(live), outcomeWord(draft)
	switch {
	case lw == dw && slices.Equal(gateLines(live), gateLines(draft)):
		return SimVerdictAgree + ": " + lw
	case lw != dw:
		return fmt.Sprintf("%s %s; this file says %s", SimVerdictDiffer, lw, dw)
	case live.Approve != nil && humanGateLine(live) != humanGateLine(draft):
		return fmt.Sprintf("%s %s; this file says %s after a different approval, see the gate lines above",
			SimVerdictDiffer, lw, strings.ToUpper(draft.Effect))
	default:
		return fmt.Sprintf("%s %s; this file says %s with different checks, see the gate lines above", SimVerdictDiffer, lw, dw)
	}
}

// outcomeWord names what a decision does to the call: its effect, plus
// "after approval" when a human gate holds the call first.
func outcomeWord(d policy.Decision) string {
	w := strings.ToUpper(d.Effect)
	if d.Approve != nil {
		w += " after approval"
	}
	return w
}

// provenanceLine is the decided-by sentence of the console's whyOf,
// sentence for sentence. An empty ruleId means no rule fired (simulate
// always carries setName for a fired rule, so the pre-revision-20 record
// case never arises here). An allow with no rule and a reason is the
// engine's own word for a granted MCP call, so the reason is the sentence.
// A deny that neither a rule nor a profile default made is the engine's
// refusal of an event kind it does not know, which no console form can
// send, so its sentence, the known kinds included, is printed too.
func provenanceLine(d policy.Decision, profile string) string {
	if d.RuleID == "" {
		s := "No policy matched this call. "
		switch {
		case (d.Effect != "deny" || !d.Default) && d.Reason != "":
			r := strings.TrimSuffix(strings.TrimPrefix(d.Reason, "Straza: "), ".")
			s += strings.ToUpper(r[:1]) + r[1:] + "."
		case d.Effect == "deny" && profile == "enterprise":
			s += "Denied by the enterprise profile default: a call nothing allows is denied."
		case d.Effect == "deny":
			s += "Denied by the profile default: no policy allowed this call."
		default:
			s += "Allowed by the profile default: no policy matched."
		}
		return s
	}
	reason := ""
	if d.Reason != "" {
		reason = ": " + strings.TrimSuffix(d.Reason, ".")
	}
	if d.SetName == "" {
		// Defensive only: the engine names the set on every fired rule.
		return fmt.Sprintf("Decided by rule %s%s.", d.RuleID, reason)
	}
	return fmt.Sprintf("Decided by rule %s in policy %s%s.", d.RuleID, d.SetName, reason)
}

// gateLines renders the gate lines in the four-bucket vocabulary,
// only when the decision actually carries a gate. At most one human gate
// (confirm beats the approve classes) plus the auto-checked mechanisms.
func gateLines(d policy.Decision) []string {
	var out []string
	if d.Approve != nil {
		out = append(out, humanGateLine(d))
	}
	if d.Classify {
		out = append(out, "checked: a classifier verdict decides before the call runs")
	}
	if d.ServerCheck {
		out = append(out, "checked: the server re-checks this call at request time")
	}
	return out
}

func humanGateLine(d policy.Decision) string {
	a := d.Approve
	if d.Confirm {
		return "needs approval: the requester confirms on their own device"
	}
	deciders := deciderWords(a)
	if a.Class == "ticket" {
		s := "needs approval: "
		if a.TicketTTLSeconds > 0 {
			s += scaleWord(a.TicketTTLSeconds) + " "
		}
		s += "approval ticket"
		if deciders != "" {
			s += ", deciders " + deciders
		}
		if a.GrantTTLSeconds > 0 {
			s += ", the approval is good for " + durShort(a.GrantTTLSeconds)
		}
		return s
	}
	s := "needs approval: "
	if a.TimeoutSeconds > 0 {
		s += scaleWord(a.TimeoutSeconds) + " "
	}
	s += "approval hold"
	if deciders != "" {
		s += ", deciders " + deciders
	}
	return s
}

// deciderWords joins decider kinds (resolved to words) and pool roles the way
// the console speaks them: "sec-approvers or straza-admin".
func deciderWords(a *policy.ApproveSpec) string {
	var names []string
	for _, k := range a.Deciders {
		if k == policy.DeciderSponsor {
			names = append(names, "the person behind the agent")
		} else {
			names = append(names, k)
		}
	}
	names = append(names, a.Roles...)
	return strings.Join(names, " or ")
}

func writeSimFooters(w io.Writer, res SimulateResult, now SimContext, full bool) {
	var subj []string
	if res.Subject.User != "" {
		subj = append(subj, res.Subject.User)
	}
	if len(res.Subject.Roles) > 0 {
		subj = append(subj, "roles "+strings.Join(res.Subject.Roles, "/"))
	} else {
		subj = append(subj, "roles (none)")
	}
	att := res.Subject.Attestation
	if att == "" {
		att = "none"
	}
	subj = append(subj, "attestation "+att)
	fmt.Fprintf(w, "%-8s  %s\n", "subject", strings.Join(subj, " · "))

	marker := ""
	if now.LiveSnapshot != "" && now.LiveSnapshot == res.Snapshot {
		marker = " (live)"
	}
	fmt.Fprintf(w, "%-8s  %s%s\n", "snapshot", res.Snapshot, marker)

	if !full {
		return
	}
	d := res.Active
	orNone := func(s string) string {
		if s == "" {
			return "(none)"
		}
		return s
	}
	snap := res.Snapshot
	if len(snap) > 8 {
		snap = snap[:8]
	}
	fmt.Fprintf(w, "%-8s  effect=%s · ruleId=%s · setName=%s · snapshot=%s\n",
		"wire", orNone(d.Effect), orNone(d.RuleID), orNone(d.SetName), orNone(snap))
	writeKeyed(w, "note", simNoteLine)
}

// scaleWord humanizes a gate window the way the console speaks it. The three
// seed-common windows are pinned words, and everything else derives.
func scaleWord(sec int) string {
	switch sec {
	case 86400:
		return "day-scale"
	case 3600:
		return "hour-scale"
	case 120:
		return "2-minute"
	}
	switch {
	case sec < 60:
		return fmt.Sprintf("%d-second", sec)
	case sec < 3600:
		return fmt.Sprintf("%d-minute", (sec+30)/60)
	case sec < 86400:
		return fmt.Sprintf("%d-hour", (sec+1800)/3600)
	default:
		return fmt.Sprintf("%d-day", (sec+43200)/86400)
	}
}

// durShort renders a compact duration ("1 h", "30 min", "45 s", "2 days")
// for the ticket grant window.
func durShort(sec int) string {
	switch {
	case sec >= 86400 && sec%86400 == 0:
		if sec == 86400 {
			return "1 day"
		}
		return fmt.Sprintf("%d days", sec/86400)
	case sec >= 3600 && sec%3600 == 0:
		return fmt.Sprintf("%d h", sec/3600)
	case sec >= 60 && sec%60 == 0:
		return fmt.Sprintf("%d min", sec/60)
	default:
		return fmt.Sprintf("%d s", sec)
	}
}

// writeKeyed prints a footer-style key/value line, hard-wrapping the value at
// 72 columns with the continuation indented under the value column, the
// shape of the note and gate lines. Values here are ASCII, so byte length
// is column width.
func writeKeyed(w io.Writer, key, val string) {
	const width = 72
	prefix := fmt.Sprintf("%-8s  ", key)
	const indent = "          "
	cur := prefix
	empty := true
	for _, word := range strings.Fields(val) {
		if !empty && len(cur)+1+len(word) > width {
			fmt.Fprintln(w, cur)
			cur = indent
			empty = true
		}
		if empty {
			cur += word
			empty = false
		} else {
			cur += " " + word
		}
	}
	fmt.Fprintln(w, cur)
}
