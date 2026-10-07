package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/spf13/cobra"

	"github.com/strazahq/straza/internal/ctl"
)

// draftsRebaseCmd is `strazactl drafts rebase <id>`, Check again: each
// object of the draft that changed on live state since the draft was
// checked keeps the fields the draft changed and takes every other field
// from live, and a field both changed needs a --pick. It reads the draft
// first for the revision, so inside a coding agent it refuses before that
// read.
func draftsRebaseCmd(client func() *ctl.Client) *cobra.Command {
	var picks []string
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "rebase <id>",
		Short: "Check a draft again on live state, keeping the fields it changes and taking every other field from live",
		Long: "Check a draft again on live state, keeping the fields it changes and taking every other field from live.\n\n" +
			"A field that the draft and live state both changed to different values needs a pick:\n" +
			"--pick \"App/demo-tools straza.limits.rps=draft\" keeps the draft's value, and =live takes live's.\n" +
			"A Check again with no pick decides nothing, so under admin.secondPerson it does not make you an\n" +
			"author of the draft.",
		Args: oneDraft("rebase"),
		RunE: func(cmd *cobra.Command, args []string) error {
			id := args[0]
			chosen, err := picksOf(picks)
			if err != nil {
				return err
			}
			c := client()
			if err := c.GuardChange(); err != nil {
				return err
			}
			w := cmd.OutOrStdout()
			raw, code, err := c.GetDraft(cmd.Context(), id)
			if err != nil {
				return err
			}
			var d ctl.DraftDetail
			if err := draftAnswer(io.Discard, false, raw, code, 2, &d); err != nil {
				return err
			}
			raw, code, err = c.RebaseDraft(cmd.Context(), id, ctl.DraftRebase{Revision: d.Draft.Revision, Picks: chosen})
			if err != nil {
				return err
			}
			if code == http.StatusConflict && !asJSON {
				if err := conflicted(w, id, raw); err != nil {
					return err
				}
			}
			var a ctl.DraftAnswer
			if err := draftAnswer(w, asJSON, raw, code, 1, &a); err != nil {
				return err
			}
			if !asJSON {
				fmt.Fprintf(w, "Checked draft %s again against live state: it is at revision %d. Nothing changes until a person publishes it.\n", id, a.Draft.Revision)
				printItems(w, a.Draft.Items)
				printVerdict(w, a.Verdict, nil)
			}
			return draftEnd(w, asJSON, a, c.APIToken != "", " -f <path>")
		},
	}
	cmd.Flags().StringArrayVar(&picks, "pick", nil, `the value to keep for a field both sides changed, as "Kind/Name field=draft" or "Kind/Name field=live"; repeatable`)
	cmd.Flags().BoolVar(&asJSON, "json", false, jsonFlagUsage)
	return cmd
}

// picksOf reads each --pick, "Kind/Name field=draft" or "=live", into the
// body's picks, nil when there are none.
func picksOf(flags []string) (map[string]string, error) {
	if len(flags) == 0 {
		return nil, nil
	}
	out := make(map[string]string, len(flags))
	for _, f := range flags {
		key, value, _ := cutLast(f, "=")
		object, field, spaced := strings.Cut(key, " ")
		if !spaced || field == "" || !strings.Contains(object, "/") || (value != "draft" && value != "live") {
			return nil, fmt.Errorf("--pick %q is not Kind/Name field=draft or Kind/Name field=live, such as --pick \"App/demo-tools straza.limits.rps=draft\"", f)
		}
		out[key] = value
	}
	return out, nil
}

// cutLast is strings.Cut at the last sep.
func cutLast(s, sep string) (before, after string, found bool) {
	if i := strings.LastIndex(s, sep); i >= 0 {
		return s[:i], s[i+len(sep):], true
	}
	return s, "", false
}

// conflicted prints the fields of a 409 that need a pick, each with its
// value at the check, in the draft and on live, and the command that picks
// one. A 409 with no conflicts prints nothing and is handled as any other.
func conflicted(w io.Writer, id string, raw []byte) error {
	var r ctl.DraftRefused
	if json.Unmarshal(raw, &r) != nil || len(r.Conflicts) == 0 {
		return nil
	}
	for _, c := range r.Conflicts {
		fmt.Fprintf(w, "  %s %s\n    at the check: %s\n    in the draft: %s\n    on live: %s\n", c.Object, c.Field, orUnset(c.Base), orUnset(c.Draft), orUnset(c.Live))
	}
	first := r.Conflicts[0]
	fmt.Fprintf(w, "Pick each value and check again, as in strazactl drafts rebase %s --pick \"%s %s=draft\", or =live to take live's.\n", id, first.Object, first.Field)
	return exitCodeErr{1, errors.New(r.Error)}
}

// orUnset is a conflict's value, or words for a field that is not set.
func orUnset(v string) string {
	if v == "" {
		return "(not set)"
	}
	return termText(v)
}
