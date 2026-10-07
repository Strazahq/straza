package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"unicode"

	"github.com/spf13/cobra"

	"github.com/strazahq/straza/internal/ctl"
)

// draftsContactCmd is `strazactl drafts contact <id> <server>`: strazad
// dials the remote server a draft proposes, once and with no credential,
// lists its tools and keeps their names on the draft for its check. A
// contact the server refused or the upstream did not answer exits 1.
func draftsContactCmd(client func() *ctl.Client) *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "contact <id> <server>",
		Short: "Contact a remote server a draft proposes, once and with no credential, and list its tools",
		Long: "Contact a remote server a draft proposes, once and with no credential, and list its tools.\n\n" +
			"strazad dials the address the draft names, follows no redirect, starts nothing, and keeps the\n" +
			"tool names on the draft so its check reads them. The names and descriptions are the server's\n" +
			"own text.",
		Args: func(_ *cobra.Command, args []string) error {
			if len(args) == 2 {
				return nil
			}
			return fmt.Errorf("strazactl drafts contact takes two operands, a draft number and a server name, such as 41 github, and got %d. "+
				"List the drafts with strazactl drafts list", len(args))
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			id, server := args[0], args[1]
			raw, code, err := client().ContactDraftServer(cmd.Context(), id, server)
			if err != nil {
				return err
			}
			w := cmd.OutOrStdout()
			if code == http.StatusBadGateway {
				return unanswered(w, asJSON, raw, code)
			}
			var a ctl.DraftContacted
			if err := draftAnswer(w, asJSON, raw, code, 1, &a); err != nil || asJSON {
				return err
			}
			fmt.Fprintf(w, "%s at %s answered as %s %s with %d tools:\n", server, a.Host, termText(a.Server.Name), termText(a.Server.Version), len(a.Tools))
			for _, t := range a.Tools {
				fmt.Fprintf(w, "  %s  %s\n", termText(t.Name), termText(t.Description))
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, jsonFlagUsage)
	return cmd
}

// unanswered is the end of a contact the upstream did not answer as an MCP
// server: strazad's sentence, which is the server's no and exits 1, and
// under --json its body on w. A 502 with no sentence came from a proxy in
// front of strazad and exits 2.
func unanswered(w io.Writer, asJSON bool, raw []byte, code int) error {
	var r ctl.DraftRefused
	if json.Unmarshal(raw, &r) != nil || r.Error == "" {
		return errors.New(noSentence(code))
	}
	if asJSON {
		if err := printServerJSON(w, raw); err != nil {
			return err
		}
	}
	return exitCodeErr{1, errors.New(r.Error)}
}

// termText is text a server wrote as one terminal line: a line break or a
// tab reads as a space, and any other character that prints nothing, such
// as the escape that starts a terminal sequence, as its code point, so the
// server's own text can neither move the cursor nor recolor the terminal.
func termText(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r == '\n' || r == '\r' || r == '\t':
			b.WriteByte(' ')
		case unicode.IsGraphic(r):
			b.WriteRune(r)
		default:
			fmt.Fprintf(&b, "U+%04X", r)
		}
	}
	return b.String()
}
