package main

import (
	"encoding/json"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/strazahq/straza/internal/ctl"
)

func auditCmd(client func() *ctl.Client) *cobra.Command {
	audit := &cobra.Command{Use: "audit", Short: "Query and verify the audit log"}

	var limit int
	var user, ceType, app string
	tail := &cobra.Command{
		Use:   "tail",
		Short: "Show the most recent audit records",
		RunE: func(cmd *cobra.Command, _ []string) error {
			// Newest window from the server, printed oldest-to-newest like
			// tail(1), ending at the newest record. The ascending page from seq 0
			// would show the OLDEST records, the chain's genesis, instead.
			// The app filter rides the server's text search and is then
			// matched on data.app, so a record that only mentions the name
			// in its reason does not pass.
			recs, err := client().AuditRecent(cmd.Context(), limit, user, ceType, app)
			if err != nil {
				return err
			}
			for i := len(recs) - 1; i >= 0; i-- {
				r := recs[i]
				if app != "" && auditApp(r.CE) != app {
					continue
				}
				if r.Username != "" {
					fmt.Fprintf(cmd.OutOrStdout(), "#%d [%s] %s\n", r.Seq, r.Username, r.CE)
				} else {
					fmt.Fprintf(cmd.OutOrStdout(), "#%d %s\n", r.Seq, r.CE)
				}
			}
			return nil
		},
	}
	tail.Flags().IntVar(&limit, "limit", 50, "max records the server returns, before the server filter")
	tail.Flags().StringVar(&user, "user", "", "filter to one user (id or username)")
	tail.Flags().StringVar(&ceType, "type", "", "filter to one CloudEvent type, for example straza.audit.mcp")
	tail.Flags().StringVar(&app, "app", "", "filter to the records whose data.app is this MCP server")
	audit.AddCommand(tail)

	audit.AddCommand(&cobra.Command{
		Use:   "verify",
		Short: "Verify the audit hash chain end to end",
		RunE: func(cmd *cobra.Command, _ []string) error {
			ok, count, broken, err := client().VerifyAudit(cmd.Context())
			if err != nil {
				return err
			}
			if !ok {
				return fmt.Errorf("audit chain BROKEN at seq %d (%d records checked)", broken, count)
			}
			fmt.Printf("audit chain intact: %d records verified\n", count)
			return nil
		},
	})
	return audit
}

// auditApp reads data.app out of one CloudEvent, "" when the record has
// none or does not parse.
func auditApp(ce string) string {
	var rec struct {
		Data struct {
			App string `json:"app"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(ce), &rec); err != nil {
		return ""
	}
	return rec.Data.App
}
