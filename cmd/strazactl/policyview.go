// strazactl policy show and diff: the stored truth in the console's words.
// show splits streams so `show <name> > file.yaml` is a clean export with the
// story still on screen. diff renders stored-vs-local with diff(1) exit codes
// so CI can gate on drift between git and the server.
package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/strazahq/straza/internal/ctl"
	policyengine "github.com/strazahq/straza/internal/policy"
)

// exitCodeErr carries a process exit code out of a RunE. The contract
// for main: on errors.As, print "strazactl: <err>" only when err
// is non-nil, then os.Exit(code). diff returns 1 for "differs" with a nil
// err (the diff itself already said everything) and 2 for real failures.
type exitCodeErr struct {
	code int
	err  error
}

func (e exitCodeErr) Error() string {
	if e.err != nil {
		return e.err.Error()
	}
	return ""
}

func (e exitCodeErr) Unwrap() error { return e.err }

func policyShowCmd(client func() *ctl.Client) *cobra.Command {
	return &cobra.Command{
		Use:   "show <name>",
		Short: "Print a stored PolicySet: its story on stderr, the YAML alone on stdout",
		Long: "Prints the named stored PolicySet: a header with the server's own facts\n" +
			"(status, drift, the four posture buckets, recording) on stderr, and the\n" +
			"stored YAML byte-exact on stdout, so `show <name> > copy.yaml` is a clean\n" +
			"export with the story still on screen.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			p, err := client().PolicyByName(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			if err := renderPolicyHeader(cmd.ErrOrStderr(), p); err != nil {
				return err
			}
			_, err = io.WriteString(cmd.OutOrStdout(), p.YAML)
			return err
		},
	}
}

func policyDiffCmd(client func() *ctl.Client) *cobra.Command {
	var file string
	diff := &cobra.Command{
		Use:   "diff <name>",
		Short: "Diff the stored PolicySet against a local file",
		Long: "Renders the difference between the named stored PolicySet and a local\n" +
			"file: rule-level added/changed/removed first, then a unified line diff.\n" +
			"Exit codes speak diff(1): 0 identical, 1 differs, 2 error.\n" +
			"There is no rollback: the way back is apply a previous file, then activate.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if file == "" {
				return exitCodeErr{2, fmt.Errorf("a -f file is required")}
			}
			raw, err := os.ReadFile(file) // #nosec G304 -- user-supplied -f is the feature
			if err != nil {
				return exitCodeErr{2, err}
			}
			p, err := client().PolicyByName(cmd.Context(), args[0])
			if err != nil {
				return exitCodeErr{2, err}
			}
			out := cmd.OutOrStdout()
			if p.YAML == string(raw) {
				fmt.Fprintf(out, "identical: stored %q and %s match byte for byte\n", p.Name, file)
				return nil
			}
			if p.UpdatedAt != "" {
				fmt.Fprintf(out, "stored %s (%s, updated %s) vs %s\n", p.Name, liveWord(p.Status), fmtStamp(p.UpdatedAt), file)
			} else {
				fmt.Fprintf(out, "stored %s (%s) vs %s\n", p.Name, liveWord(p.Status), file)
			}
			printRuleDelta(out, p.YAML, raw, file)
			fmt.Fprintf(out, "--- server/%s\n+++ %s\n", p.Name, file)
			writeUnified(out, diffOps(strings.Split(p.YAML, "\n"), strings.Split(string(raw), "\n")))
			return exitCodeErr{code: 1}
		},
	}
	diff.Flags().StringVarP(&file, "file", "f", "", "local PolicySet YAML file to compare")
	return diff
}

// printRuleDelta prints the rule-level added/changed/removed line, parsing
// both sides with the same parser `policy validate` trusts. A side that
// does not parse gets an honest note instead of an invented delta; the line
// diff below still tells the whole story.
func printRuleDelta(w io.Writer, stored string, local []byte, file string) {
	sdocs, serr := policyengine.ParseAll([]byte(stored))
	ldocs, lerr := policyengine.ParseAll(local)
	switch {
	case serr != nil:
		fmt.Fprintln(w, "stored YAML does not parse; rule-level diff skipped")
	case lerr != nil:
		fmt.Fprintf(w, "%s does not parse; rule-level diff skipped\n", file)
	case len(sdocs) != 1:
		fmt.Fprintf(w, "stored YAML carries %d documents; rule-level diff needs exactly one, line diff only\n", len(sdocs))
	case len(ldocs) != 1:
		fmt.Fprintf(w, "%s carries %d documents; rule-level diff needs exactly one, line diff only\n", file, len(ldocs))
	default:
		changed, added, removed := ruleDelta(sdocs[0], ldocs[0])
		fmt.Fprintf(w, "rules: %s · %s · %s\n",
			countIDs(len(changed), "changed", changed),
			countIDs(len(added), "added", added),
			countIDs(len(removed), "removed", removed))
	}
}

// ruleDelta mirrors the console's ruleDiff: rules compared by id, equality
// judged structurally (JSON form), so the CLI and the Builder agree on what
// "changed" means. changed/added follow the local file's order, removed the
// stored set's.
func ruleDelta(stored, local policyengine.Document) (changed, added, removed []string) {
	storedByID := make(map[string]policyengine.Rule, len(stored.Spec.Rules))
	for _, r := range stored.Spec.Rules {
		storedByID[r.ID] = r
	}
	localIDs := make(map[string]bool, len(local.Spec.Rules))
	for _, r := range local.Spec.Rules {
		localIDs[r.ID] = true
		prev, ok := storedByID[r.ID]
		if !ok {
			added = append(added, r.ID)
			continue
		}
		a, _ := json.Marshal(prev)
		b, _ := json.Marshal(r)
		if string(a) != string(b) {
			changed = append(changed, r.ID)
		}
	}
	for _, r := range stored.Spec.Rules {
		if !localIDs[r.ID] {
			removed = append(removed, r.ID)
		}
	}
	return changed, added, removed
}

func countIDs(n int, word string, ids []string) string {
	if n == 0 {
		return fmt.Sprintf("0 %s", word)
	}
	return fmt.Sprintf("%d %s (%s)", n, word, strings.Join(ids, ", "))
}

// renderPolicyHeader writes show's story block: the console list row's
// facts, in the console's words and key order.
func renderPolicyHeader(w io.Writer, p ctl.PolicyDetail) error {
	tw := tabwriter.NewWriter(w, 2, 4, 2, ' ', 0)
	fmt.Fprintf(tw, "name\t%s\n", p.Name)
	fmt.Fprintf(tw, "status\t%s\n", statusWord(p))
	fmt.Fprintf(tw, "priority\t%d\n", p.Priority)
	if p.UpdatedAt != "" {
		fmt.Fprintf(tw, "updated\t%s\n", fmtStamp(p.UpdatedAt))
	}
	if p.Drift != nil && *p.Drift {
		fmt.Fprintf(tw, "note\tthe stored source below differs from what is enforcing right now; publish to make it live\n")
	}
	if p.Summary == nil {
		// Absent summary is the server's honest signal that the source no
		// longer parses; nothing here invents a shape from it.
		fmt.Fprintf(tw, "summary\tnot decomposable: the stored YAML no longer parses; the bytes below are the record, which is exactly when you need them\n")
		return tw.Flush()
	}
	fmt.Fprintf(tw, "applies to\t%s\n", appliesTo(*p.Summary))
	fmt.Fprintf(tw, "rules\t%s\n", rulesLine(*p.Summary))
	if line := captureLine(p.Summary.Capture); line != "" {
		fmt.Fprintf(tw, "recording\t%s\n", line)
	}
	return tw.Flush()
}

// liveWord is the display word of a policy set's wire status: Live for
// active, Off for draft, and the raw value for anything else. The wire keeps
// draft for a set that is off, and the word draft names a config draft.
func liveWord(status string) string {
	switch status {
	case "active":
		return "Live"
	case "draft":
		return "Off"
	default:
		return status
	}
}

// statusWord speaks the console status column: Live / Live, edits not
// published / Off. Drift is claimed only on an explicit server true;
// an absent flag makes no claim either way.
func statusWord(p ctl.PolicyDetail) string {
	switch p.Status {
	case "active":
		if p.Drift != nil && *p.Drift {
			return "Live, edits not published"
		}
		return "Live"
	case "draft":
		return "Off"
	default:
		return p.Status
	}
}

// fmtStamp renders the server's RFC3339 UTC stamp in local time. An
// unparseable stamp prints as received.
func fmtStamp(rfc string) string {
	t, err := time.Parse(time.RFC3339, rfc)
	if err != nil {
		return rfc
	}
	return t.Local().Format("2006-01-02 15:04:05 -07:00")
}

// appliesTo mirrors the console's Applies-to column: roles, the honest
// "* everyone" for an empty match, "+ users/identity" when non-role
// selectors are present.
func appliesTo(s ctl.PolicySummary) string {
	if len(s.MatchRoles) == 0 {
		if s.MatchOther {
			return "users/identity"
		}
		return "* everyone"
	}
	word := "role"
	if len(s.MatchRoles) > 1 {
		word = "roles"
	}
	line := word + " " + strings.Join(s.MatchRoles, ", ")
	if s.MatchOther {
		line += " + users/identity"
	}
	return line
}

// rulesLine folds the seven engine postures into the four fixed buckets
// (the console's buckets(): deny | hold+ticket+confirm | serverCheck+
// classify | allow), with the approval bucket's split in parentheses.
func rulesLine(s ctl.PolicySummary) string {
	p := s.Postures
	human := p["hold"] + p["ticket"] + p["confirm"]
	line := fmt.Sprintf("%d · denied %d · needs approval %d", s.Rules, p["deny"], human)
	if sub := humanSplit(p); sub != "" {
		line += " (" + sub + ")"
	}
	return line + fmt.Sprintf(" · checked %d · allowed %d", p["serverCheck"]+p["classify"], p["allow"])
}

// humanSplit names the approval bucket's parts in the console hover's order:
// hold, ticket, confirm; only nonzero parts appear.
func humanSplit(p map[string]int) string {
	var parts []string
	if n := p["hold"]; n > 0 {
		parts = append(parts, fmt.Sprintf("%d hold", n))
	}
	if n := p["ticket"]; n > 0 {
		parts = append(parts, fmt.Sprintf("%d ticket", n))
	}
	if n := p["confirm"]; n > 0 {
		parts = append(parts, fmt.Sprintf("%d confirm", n))
	}
	return strings.Join(parts, ", ")
}

// captureLine speaks the recording mode in the console's words.
func captureLine(capture string) string {
	switch capture {
	case "verbatim":
		return "word for word"
	case "redact":
		return "secrets masked"
	case "":
		return ""
	default:
		return capture
	}
}
