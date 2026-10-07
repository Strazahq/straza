package main

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/spf13/cobra"

	"github.com/strazahq/straza/internal/ctl"
)

func draftsPublishCmd(client func() *ctl.Client) *cobra.Command {
	var acks []string
	var yes, asJSON bool
	cmd := &cobra.Command{
		Use:   "publish <id>",
		Short: "Publish a draft whole, after acknowledging every line that widens access",
		Long: "Checks the draft against live state now and prints each line that widens access.\n" +
			"A line that names a text to type is acknowledged by typing that text. The last\n" +
			"question acknowledges every other line. The server checks the draft again as it\n" +
			"publishes, and an object that changed since refuses the publish, so nothing is\n" +
			"written. Only a person publishes, so an admin API token and an agent cannot.",
		Args: oneDraft("publish"),
		RunE: func(cmd *cobra.Command, args []string) error {
			id := args[0]
			if asJSON && !yes {
				return errJSONNeedsYes("publish", "Pass --yes, and --ack with the text of each typed risk")
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
			if err := draftAnswer(w, false, raw, code, 2, &d); err != nil {
				return err
			}
			if err := publishRefusal(w, id, d, asJSON, c.APIToken != ""); err != nil {
				return err
			}
			if err := c.CheckPublishRoute(cmd.Context(), id); err != nil {
				return err
			}
			body, err := acknowledge(cmd, id, d, acks, yes, asJSON)
			if err != nil {
				return err
			}
			raw, code, err = c.PublishDraft(cmd.Context(), id, body)
			if err != nil {
				return err
			}
			var p ctl.DraftPublished
			if err := draftAnswer(w, asJSON, raw, code, 1, &p); err != nil || asJSON {
				return err
			}
			printPublished(w, id, p)
			return nil
		},
	}
	cmd.Flags().BoolVar(&yes, "yes", false, "publish without the questions: every risk shown is acknowledged, and each typed risk needs its --ack")
	cmd.Flags().StringArrayVar(&acks, "ack", nil, "the text a typed risk asks for, as its line shows it (repeatable)")
	cmd.Flags().BoolVar(&asJSON, "json", false, jsonFlagUsage)
	return cmd
}

// publishRefusal answers why the draft id, as the GET answered it in d,
// cannot be published, before any question is asked: a draft that is not
// open, a verdict that refuses, whose lines it prints on w, and a publisher
// the server said may not publish. When token says the caller is an admin
// API token, the server's refusal goes on with the way to a person's
// publish.
func publishRefusal(w io.Writer, id string, d ctl.DraftDetail, asJSON, token bool) error {
	v := d.Verdict
	switch {
	case d.Draft.State != "open":
		return exitCodeErr{1, fmt.Errorf("draft %s is %s, so it cannot be published. Read it with strazactl drafts show %s", id, d.Draft.State, id)}
	case len(v.Refused) > 0:
		if !asJSON {
			printFindings(w, "refused", v.Refused, nil)
		}
		return exitCodeErr{1, fmt.Errorf("draft %s cannot be published: %s", id, lineText(v.Refused[0]))}
	case !d.MayPublish && d.PublishRefusal != "" && token:
		return errors.New(d.PublishRefusal + " " + tokenNext(id))
	case !d.MayPublish && d.PublishRefusal != "":
		return errors.New(d.PublishRefusal)
	}
	return nil
}

// acknowledge runs publish's questions on the draft d as the GET answered
// it, once publishRefusal found nothing, and answers the body of the
// publish: the revision and risk digest read, the key of every risk shown
// in ticked, and the text typed for each typed risk by key. A typed risk
// cut for this reader carries no text, so it is neither asked for nor
// typed, and the server's refusal answers it.
func acknowledge(cmd *cobra.Command, id string, d ctl.DraftDetail, acks []string, yes, asJSON bool) (ctl.DraftPublish, error) {
	v := d.Verdict
	body := ctl.DraftPublish{Revision: d.Draft.Revision, RiskDigest: v.RiskDigest, Ticked: []string{}, Typed: map[string]string{}}
	w := cmd.OutOrStdout()
	if !asJSON {
		fmt.Fprintf(w, "Publish draft %s: %s.\n", id, d.Draft.Title)
		if len(v.Risks) > 0 {
			fmt.Fprintln(w, "It widens access:")
			printFindings(w, "widens", v.Risks, nil)
		}
	}
	// Every question reads from one reader: a second reader over the same
	// stdin would lose what the first buffered from a pipe.
	in := bufio.NewReader(cmd.InOrStdin())
	for _, r := range v.Risks {
		body.Ticked = append(body.Ticked, r.Key)
		if r.Ack != "typed" || r.Typed == "" {
			continue
		}
		text, ok := ackFor(acks, r.Typed)
		switch {
		case ok:
		case yes:
			return body, fmt.Errorf("draft %s needs a typed acknowledgment for %s. Pass --ack %s, or run the command without --yes and type it", id, r.Object, r.Typed)
		default:
			text = ask(w, in, "Type "+r.Typed+" to acknowledge that "+r.Object+" widens access: ")
			if text == "" {
				return body, errAborted()
			}
			if !sameText(text, r.Typed) {
				return body, fmt.Errorf("the text typed for %s is not %s, so nothing was published. Run the command again and type %s, or pass --ack %s",
					r.Object, r.Typed, r.Typed, r.Typed)
			}
		}
		body.Typed[r.Key] = text
	}
	if !yes {
		if a := strings.ToLower(ask(w, in, "Publish? [y/N] ")); a != "y" && a != "yes" {
			return body, errAborted()
		}
	}
	return body, nil
}

// ackFor finds the --ack that acknowledges the typed text want and answers
// it as the person gave it, trimmed.
func ackFor(acks []string, want string) (string, bool) {
	for _, a := range acks {
		if sameText(a, want) {
			return strings.TrimSpace(a), true
		}
	}
	return "", false
}

// sameText reports whether got acknowledges want: equal after trimming
// spaces and compared without case, never stricter than the server, which
// compares a host without case.
func sameText(got, want string) bool {
	return strings.EqualFold(strings.TrimSpace(got), strings.TrimSpace(want))
}

// ask prints the question and answers one line read from in, trimmed.
func ask(w io.Writer, in *bufio.Reader, question string) string {
	fmt.Fprint(w, question)
	line, _ := in.ReadString('\n')
	return strings.TrimSpace(line)
}

// printPublished prints what a publish answered: the snapshot, one line per
// server it created, changed or removed with the start's error or health
// reason under it, the next steps, and the way back.
func printPublished(w io.Writer, id string, p ctl.DraftPublished) {
	fmt.Fprintf(w, "Published draft %s. The live policy snapshot is %s.\n", id, p.Snapshot)
	for _, s := range p.Servers {
		if s.Change == "removed" {
			fmt.Fprintf(w, "%s is removed.\n", s.Name)
		} else {
			fmt.Fprintf(w, "%s is %s: strazactl apps show %s\n", s.Name, s.Status, s.Name)
		}
		if s.Detail != "" {
			fmt.Fprintf(w, "  %s\n", s.Detail)
		}
	}
	for _, n := range p.Next {
		fmt.Fprintln(w, n)
	}
	fmt.Fprintf(w, "To undo it: strazactl drafts revert %s\n", id)
}
