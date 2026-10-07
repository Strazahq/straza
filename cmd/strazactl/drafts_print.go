package main

import (
	"fmt"
	"io"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/strazahq/straza/internal/ctl"
)

// printItems prints one line per item: two spaces, its mark and its
// Kind/Name.
func printItems(w io.Writer, items []ctl.DraftItem) {
	for _, it := range items {
		fmt.Fprintf(w, "  %-3s %s\n", itemMark(it), it.Object())
	}
}

// itemMark is + for a put of an object that did not exist at the check, ~
// for a put of one that did, - for a removal and off for a set turned off.
// It reads existed, which every reader gets, and never base, which the
// server leaves out for a reader who may not read the object.
func itemMark(it ctl.DraftItem) string {
	switch {
	case it.Op == "remove":
		return "-"
	case it.Op == "off":
		return "off"
	case it.Existed:
		return "~"
	}
	return "+"
}

// printVerdict prints the verdict lines: refused, widens, warning,
// unchecked and info, each list in the server's order. places maps the
// number of each sent document to where it sits, as printFindings says.
func printVerdict(w io.Writer, v ctl.DraftVerdict, places map[int]string) {
	printFindings(w, "refused", v.Refused, places)
	printFindings(w, "widens", v.Risks, nil)
	printFindings(w, "warning", v.Warnings, nil)
	printFindings(w, "unchecked", v.Unchecked, nil)
	printFindings(w, "info", v.Info, nil)
}

// printFindings prints one verdict line per finding: two spaces, the class
// word, the object when the line has one, the sentence with its fix, and for
// a typed risk the text a publisher types. Under a line whose document
// number places maps, it says where the document or text the line refuses
// sits, such as the second document of github.yaml, a Role named deploy.
// Under a risk or a warning whose before and after words differ, it prints
// each side the finding has, indented. The server never puts a value in
// those words.
func printFindings(w io.Writer, word string, findings []ctl.DraftFinding, places map[int]string) {
	for _, f := range findings {
		line := fmt.Sprintf("  %-9s ", word)
		// A mask sentence names its object itself, so the object is not
		// printed a second time in front of it.
		if f.Object != "" && !strings.HasPrefix(f.Sentence, f.Object+" ") {
			line += f.Object + " "
		}
		line += lineText(f)
		if f.Ack == "typed" && f.Typed != "" {
			line += " Type " + f.Typed + " to publish."
		}
		fmt.Fprintln(w, line)
		if where := places[f.Document]; where != "" {
			fmt.Fprintf(w, "  in %s\n", where)
		}
		if (f.Class == "risk" || f.Class == "warning") && f.Before != f.After {
			if f.Before != "" {
				fmt.Fprintf(w, "            before: %s\n", f.Before)
			}
			if f.After != "" {
				fmt.Fprintf(w, "            after:  %s\n", f.After)
			}
		}
	}
}

// lineText is a finding's sentence with its fix.
func lineText(f ctl.DraftFinding) string {
	if f.Fix == "" {
		return f.Sentence
	}
	return f.Sentence + " " + f.Fix
}

// printDetail prints a draft as show reads it: each revision with its
// author, who discarded a discarded draft, the note, each item against live
// state, when it was checked, or for a draft that is not open the check the
// server stored for its revision, the verdict, info and passed lines, who
// gains what, the standing publishing needs, and for an open draft whether
// the reader may publish it. token says the reader is an admin API token, and
// then the way to a person's publish follows the server's refusal.
func printDetail(w io.Writer, d ctl.DraftDetail, token bool) error {
	draft := d.Draft
	for _, r := range d.Revisions {
		fmt.Fprintf(w, "Revision %d by %s through %s at %s.\n", r.Revision, principalWords(r.Author), doorPhrase(r.Door), utcStamp(r.CreatedAt))
	}
	if draft.State == "discarded" && draft.DecidedBy != nil && draft.DecidedAt != "" {
		fmt.Fprintln(w, discardedLine(draft))
	}
	if draft.Note != "" {
		fmt.Fprintf(w, "The note, which Straza did not check: %s\n", draft.Note)
	}
	for _, it := range draft.Items {
		printItems(w, []ctl.DraftItem{it})
		printItemDiff(w, it, d.Live[it.Object()].Doc)
	}
	if draft.State == "open" {
		fmt.Fprintf(w, "Checked against live state at %s.\n", utcStamp(d.Verdict.CheckedAt))
	} else {
		fmt.Fprintln(w, storedCheckLine(draft, d.Checks))
	}
	printVerdict(w, d.Verdict, nil)
	printFindings(w, "passed", d.Verdict.Passed, nil)
	if err := printGains(w, d.Verdict.Gains); err != nil {
		return err
	}
	for _, n := range d.Verdict.Needs {
		fmt.Fprintf(w, "  Publishing %s needs %s.\n", n.Object, n.Standing)
	}
	if draft.State != "open" {
		return nil
	}
	switch {
	case stale(d.Verdict):
		fmt.Fprintln(w, staleHint(draft.ID))
	case len(d.Verdict.Refused) > 0:
		fmt.Fprintf(w, "Fix the refused lines, then send the documents again: strazactl drafts update %s -f <path>\n", draft.ID)
	case d.MayPublish:
		fmt.Fprintf(w, "You may publish it: strazactl drafts publish %s\n", draft.ID)
	case d.PublishRefusal != "":
		fmt.Fprintln(w, d.PublishRefusal)
		if token {
			fmt.Fprintln(w, tokenNext(draft.ID)+".")
		}
	}
	return nil
}

// storedCheckLine is what show prints for a draft that is not open in place
// of when it was checked: the counts of the check the server stored for its
// revision, or that it stored none, since the server checks it no more.
func storedCheckLine(d ctl.Draft, c *ctl.DraftChecks) string {
	if c == nil {
		return fmt.Sprintf("Straza stored no check of revision %d, and it does not check a draft again once it is %s.", d.Revision, d.State)
	}
	return fmt.Sprintf("Revision %d was checked against live state at %s (%s). Straza does not check a draft again once it is %s.",
		c.Revision, utcStamp(c.CheckedAt), checksWord(ctl.DraftSummary{Revision: d.Revision, Checks: c}), d.State)
}

// principalWords names a principal as the drafts sentences do: the
// username, and for an agent that it is one and who sponsors it.
func principalWords(p ctl.DraftPrincipal) string {
	switch {
	case p.Agent && p.Sponsor != "":
		return p.Username + " (agent, sponsored by " + p.Sponsor + ")"
	case p.Agent:
		return p.Username + " (agent)"
	}
	return p.Username
}

// discardedLine is who discarded the draft d and when, with the reason
// given as a sentence of its own.
func discardedLine(d ctl.Draft) string {
	line := fmt.Sprintf("Discarded by %s at %s", principalWords(*d.DecidedBy), utcStamp(d.DecidedAt))
	reason := d.DecidedReason
	if reason == "" {
		return line + "."
	}
	if strings.TrimRight(reason, ".!?") == reason {
		reason += "."
	}
	return line + ": " + reason
}

// doorWords is a door as the list's DOOR cell reads it: console, strazactl,
// apps directory, straza app or API. A door this strazactl does not know
// reads as the server sent it.
func doorWords(door string) string {
	switch door {
	case "apps-directory":
		return "apps directory"
	case "straza-app":
		return "straza app"
	case "api":
		return "API"
	}
	return door
}

// doorPhrase is a door as a sentence names it, with the article every door
// but strazactl takes.
func doorPhrase(door string) string {
	if door == "strazactl" {
		return door
	}
	return "the " + doorWords(door)
}

// printLeft prints the objects of read, the items of the revision an
// update read, that items, the new revision's, no longer holds, in the
// order read held them.
func printLeft(w io.Writer, read, items []ctl.DraftItem) {
	kept := map[string]bool{}
	for _, it := range items {
		kept[it.Object()] = true
	}
	var left []string
	for _, it := range read {
		if !kept[it.Object()] {
			left = append(left, it.Object())
		}
	}
	if len(left) > 0 {
		fmt.Fprintf(w, "Left the draft: %s.\n", strings.Join(left, ", "))
	}
}

// printItemDiff prints a unified diff of an object's live document against
// the item's own, which the server masks alike, reading an absent side as
// no lines. An item equal to live prints nothing.
func printItemDiff(w io.Writer, it ctl.DraftItem, live string) {
	ops := diffOps(docLines(live), docLines(it.Doc))
	for _, op := range ops {
		if op.t != ' ' {
			fmt.Fprintf(w, "--- live %s\n+++ draft %s\n", it.Object(), it.Object())
			writeUnified(w, ops)
			return
		}
	}
}

// docLines is a document's lines, none for an empty one.
func docLines(doc string) []string {
	if doc == "" {
		return nil
	}
	return strings.Split(strings.TrimSuffix(doc, "\n"), "\n")
}

// printGains prints who gains what: a row per role and tool, the gate in
// words where the server has them, else the outcome in the policy words,
// and the holders by name where the server names them, else counted.
func printGains(w io.Writer, gains []ctl.DraftGain) error {
	if len(gains) == 0 {
		return nil
	}
	tw := tabwriter.NewWriter(w, 2, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "ROLE\tSERVER\tTOOL\tNOW\tAFTER\tHOLDERS")
	for _, g := range gains {
		holders := strconv.Itoa(g.HoldersCount)
		if len(g.Holders) > 0 {
			holders = strings.Join(g.Holders, ", ")
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n", g.Role, g.Server, g.Tool,
			wordsOr(g.BeforeWords, outcomeWords(g.Before)), wordsOr(g.AfterWords, outcomeWords(g.After)), holders)
	}
	return tw.Flush()
}

// outcomeWords is an outcome of who gains what in the words the policy
// pages use. One this strazactl does not know reads as the server sent it.
func outcomeWords(outcome string) string {
	switch outcome {
	case "runs":
		return "runs at once"
	case "needs-approval":
		return "needs approval"
	case "not-reachable":
		return "not reachable"
	}
	return outcome
}

// wordsOr is words, or outcome when there are none.
func wordsOr(words, outcome string) string {
	if words != "" {
		return words
	}
	return outcome
}

// printDraftTable prints the drafts list's table.
func printDraftTable(w io.Writer, rows []ctl.DraftSummary) error {
	tw := tabwriter.NewWriter(w, 2, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "ID\tSTATE\tTITLE\tPROPOSER\tDOOR\tCHECKS\tCHECKED\tUPDATED")
	for _, d := range rows {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n", d.ID, d.State, d.Title, d.Proposer.Username, doorWords(d.Door),
			checksWord(d), checkedWord(d), utcStamp(d.UpdatedAt))
	}
	return tw.Flush()
}

// checksWord is the CHECKS cell: the counts above zero of the server's last
// check, "no findings" when all are zero, and "-" when the draft has no
// check of its current revision.
func checksWord(d ctl.DraftSummary) string {
	c := d.Checks
	if c == nil || c.Revision != d.Revision {
		return "-"
	}
	var parts []string
	for _, p := range []struct {
		n    int
		word string
	}{{c.Refused, "refused"}, {c.Risks, "widen"}, {c.Warnings, "warn"}, {c.Unchecked, "unchecked"}} {
		if p.n > 0 {
			parts = append(parts, fmt.Sprintf("%d %s", p.n, p.word))
		}
	}
	if len(parts) == 0 {
		return "no findings"
	}
	return strings.Join(parts, ", ")
}

// checkedWord is the CHECKED cell: when the check whose counts the CHECKS
// cell shows ran, and "-" when that cell shows none.
func checkedWord(d ctl.DraftSummary) string {
	if checksWord(d) == "-" || d.Checks.CheckedAt == "" {
		return "-"
	}
	return utcStamp(d.Checks.CheckedAt)
}

// utcStamp renders the server's RFC3339 time in UTC as the drafts sentences
// write times, such as 2026-09-24 10:14 UTC. A time that does not parse
// prints as received.
func utcStamp(rfc string) string {
	t, err := time.Parse(time.RFC3339, rfc)
	if err != nil {
		return rfc
	}
	return t.UTC().Format("2006-01-02 15:04 UTC")
}

// plural is n with its noun, the singular for one.
func plural(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return fmt.Sprintf("%d %s", n, many)
}
