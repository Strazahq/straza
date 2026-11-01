package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/spf13/cobra"

	"github.com/strazahq/straza/internal/ctl"
)

// appsObjectsCmd is `strazactl apps objects <server>`: the server's object
// list as the answering strazad holds it, one status line about the last
// listing and a table with the roles that cover each entry, and its two
// verbs, refresh and draft.
func appsObjectsCmd(client func() *ctl.Client) *cobra.Command {
	var after string
	var limit int
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "objects <server>",
		Short: "Show what Straza last listed on a server, such as its repos, and the roles that cover each",
		Long: "A server that pins an object type in its manifest (straza.objects) is listed by Straza\n" +
			"every 10 minutes with the server's own listing tool. This command prints how the last\n" +
			"listing went, then one row per entry with its value, id, title, the attributes the\n" +
			"manifest keeps and the roles that cover it. A failed listing changes nothing: Straza\n" +
			"keeps the last good list and the line names the reason.\n\n" +
			"A server that pins no type prints what its tool marks declare, and the command that\n" +
			"opens a draft to pin it. Pages hold 500 entries unless --limit says otherwise, and the\n" +
			"last line names the command for the next page.",
		Example: "  strazactl apps objects objects-demo\n" +
			"  strazactl apps objects objects-demo --limit 100 --after Strazahq/site\n" +
			"  strazactl apps objects refresh objects-demo",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c := client()
			if asJSON {
				raw, err := c.AppObjectsJSON(cmd.Context(), args[0], after, limit)
				if err != nil {
					return err
				}
				return printServerJSON(cmd.OutOrStdout(), raw)
			}
			l, err := c.AppObjects(cmd.Context(), args[0], after, limit)
			if err != nil {
				return err
			}
			return printObjects(cmd.OutOrStdout(), l, after, limit, time.Now())
		},
	}
	cmd.Flags().StringVar(&after, "after", "", "start the page after this value, the last value of the previous page")
	cmd.Flags().IntVar(&limit, "limit", 0, "at most this many entries per page, up to 1000 (default 500)")
	cmd.Flags().BoolVar(&asJSON, "json", false, jsonFlagUsage)
	cmd.AddCommand(appsObjectsRefreshCmd(client), appsObjectsDraftCmd(client))
	return cmd
}

// appsObjectsRefreshCmd is `strazactl apps objects refresh <server>`. It
// reads the server's plural first, so the confirmation speaks of its repos
// or projects, then asks for the listing. Every refusal is strazad's own
// sentence.
func appsObjectsRefreshCmd(client func() *ctl.Client) *cobra.Command {
	return &cobra.Command{
		Use:   "refresh <server>",
		Short: "List a server's objects again now instead of at the next 10-minute listing",
		Long: "Asks Straza to list the server's objects now, for example after a repo was created.\n" +
			"One strazad replica runs the listing within 30 seconds, and every replica holds the\n" +
			"result after that. A server listed less than a minute ago refuses, and the answer\n" +
			"says when to try again. The listing writes its record to the audit log.",
		Example: "  strazactl apps objects refresh objects-demo",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c := client()
			l, err := c.AppObjects(cmd.Context(), args[0], "", 1)
			if err != nil {
				return err
			}
			if _, err := c.RefreshAppObjects(cmd.Context(), args[0]); err != nil {
				return err
			}
			if l.Plural == "" {
				// The type was pinned between the read and the refresh.
				fmt.Fprintf(cmd.OutOrStdout(), "Queued: %s is listed within 30 seconds.\n", args[0])
				return nil
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Queued: %s lists its %s within 30 seconds.\n", l.App, l.Plural)
			return nil
		},
	}
}

// appsObjectsDraftCmd is `strazactl apps objects draft <server>`: a draft
// whose one App item pins what the server's tool marks declare, printed as
// drafts create prints a new draft, with the publish command last.
func appsObjectsDraftCmd(client func() *ctl.Client) *cobra.Command {
	return &cobra.Command{
		Use:   "draft <server>",
		Short: "Open a draft that pins the objects a server declares in its tool marks",
		Long: "Reads the ai.straza/object marks on the server's live tools and opens a draft that\n" +
			"writes them into the manifest's straza.objects block. A mark the manifest already pins\n" +
			"stricter stays as pinned. Nothing changes until a person publishes the draft, and the\n" +
			"draft's check says what still needs a decision. A server that is not running, or\n" +
			"whose tools carry no marks, refuses and says what to do instead.",
		Example: "  strazactl apps objects draft objects-demo",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c := client()
			raw, code, err := c.DraftAppObjects(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			w := cmd.OutOrStdout()
			if code == http.StatusOK {
				var same struct {
					Unchanged bool   `json:"unchanged"`
					Sentence  string `json:"sentence"`
				}
				if err := json.Unmarshal(raw, &same); err != nil || !same.Unchanged || same.Sentence == "" {
					return fmt.Errorf("strazad answered 200 without the sentence of an unchanged pin. Check that --server or STRAZA_SERVER points at strazad, then run the command again")
				}
				fmt.Fprintln(w, same.Sentence)
				return nil
			}
			var a ctl.DraftAnswer
			if err := draftAnswer(w, false, raw, code, 1, &a); err != nil {
				return err
			}
			fmt.Fprintf(w, "Created draft %s with %s. Nothing changes until a person publishes it.\n",
				a.Draft.ID, plural(len(a.Draft.Items), "document", "documents"))
			printItems(w, a.Draft.Items)
			printVerdict(w, a.Verdict, nil)
			return draftEnd(w, false, a, c.Base, "")
		},
	}
}
