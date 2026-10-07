package main

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/strazahq/straza/internal/ctl"
)

func transcriptsCmd(client func() *ctl.Client) *cobra.Command {
	cmd := &cobra.Command{Use: "transcripts", Short: "Search recorded conversations by text or by a secret's value"}

	var value, user string
	var limit int
	search := &cobra.Command{
		Use:   "search [pattern]",
		Short: "Find who said what: substring scan and/or exact-value match",
		Long: "Scans recorded turns for a substring, and/or matches a secret VALUE without\n" +
			"sending it to the server: --value hashes locally (SHA-256) and matches turns\n" +
			"whose FULL content equals the value. Output answers who leaked what, when.\n" +
			"Then rotate the credential.",
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			pattern := ""
			if len(args) == 1 {
				pattern = args[0]
			}
			hash := ""
			if value != "" {
				sum := sha256.Sum256([]byte(value))
				hash = "sha256:" + hex.EncodeToString(sum[:])
			}
			if pattern == "" && hash == "" {
				return fmt.Errorf("give a pattern argument, --value, or both")
			}
			hits, err := client().SearchTranscripts(cmd.Context(), pattern, hash, user, limit)
			if err != nil {
				return err
			}
			if len(hits) == 0 {
				fmt.Println("no matches")
				return nil
			}
			tw := tabwriter.NewWriter(os.Stdout, 2, 4, 2, ' ', 0)
			fmt.Fprintln(tw, "TIME\tUSER\tSESSION\tKIND\tCONTENT")
			for _, h := range hits {
				who := h.Username
				if who == "" {
					who = h.UserID
				}
				fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n",
					h.At.Format("2006-01-02 15:04:05"), who, h.SessionID, h.Kind, oneLine(h.Content, 100))
			}
			return tw.Flush()
		},
	}
	search.Flags().StringVar(&value, "value", "", "secret value to match (hashed locally; never sent)")
	search.Flags().StringVar(&user, "user", "", "narrow to a user (id or username)")
	search.Flags().IntVar(&limit, "limit", 0, "max results (server default 100)")
	cmd.AddCommand(search)
	return cmd
}

// oneLine flattens and trims content for table display.
func oneLine(s string, max int) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > max {
		return s[:max] + "…"
	}
	return s
}
