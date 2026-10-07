package main

import (
	"fmt"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/strazahq/straza/internal/ctl"
)

// sinksCmd is the operator surface for SIEM sinks: what each sink has
// behind it, what it refused, and the replay door for parked events. A
// delivery the receiver refuses deterministically parks, is counted, and
// replays from here.
func sinksCmd(client func() *ctl.Client) *cobra.Command {
	sinks := &cobra.Command{Use: "sinks", Short: "Inspect SIEM sinks and replay parked (dead-letter) events"}

	sinks.AddCommand(&cobra.Command{
		Use:   "list",
		Short: "List configured sinks with backlog, parked count, and the last failure",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			rows, err := client().Sinks(cmd.Context())
			if err != nil {
				return err
			}
			tw := tabwriter.NewWriter(cmd.OutOrStdout(), 2, 4, 2, ' ', 0)
			fmt.Fprintln(tw, "NAME\tTYPE\tTARGET\tBACKLOG\tPARKED\tDELIVERED\tLAST ERROR")
			for _, s := range rows {
				var delivered uint64
				for _, st := range s.Streams {
					delivered += st.Delivered
				}
				parked := fmt.Sprintf("%d", s.Parked)
				if s.Parked > 0 {
					// Parked events are preserved but NOT in the SIEM until an
					// operator replays them: say so on the row, not in a footnote.
					parked += " (replay needed)"
				}
				fmt.Fprintf(tw, "%s\t%s\t%s\t%d\t%s\t%d\t%s\n",
					s.Name, s.Type, s.Target, s.Backlog(), parked, delivered, lastSinkError(s.Streams))
			}
			return tw.Flush()
		},
	})

	var limit int
	replay := &cobra.Command{
		Use:   "replay <name>",
		Short: "Re-deliver a sink's parked events to its receiver, oldest first",
		Long: "Replays the dead-letter lane of the named sink (sinks[].name in strazad config):\n" +
			"each parked event is POSTed again and leaves the lane when the receiver accepts it\n" +
			"(a 409 counts: the receiver already holds it). The replay stops at the first\n" +
			"refusal and reports it; nothing is re-parked or dropped. Bounded per call\n" +
			"(--limit, default 1000): run it again while events remain.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			res, err := client().ReplaySink(cmd.Context(), args[0], limit)
			if err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "replayed %d event(s) to sink %s; %d remain parked\n",
				res.Replayed, res.Sink, res.Remaining)
			if res.Stopped != "" {
				return fmt.Errorf("replay stopped: %s (fix the receiver, then run again)", strings.TrimSpace(res.Stopped))
			}
			return nil
		},
	}
	replay.Flags().IntVar(&limit, "limit", 0, "max events to replay in this call (server default 1000)")
	sinks.AddCommand(replay)
	return sinks
}

// lastSinkError is the LAST ERROR cell of a sink row: the newest error of its
// streams as "<time> <text>", the time in RFC3339 to the second in UTC, so
// an error from boot reads as old while deliveries go on after it. An error
// with no time, from an older server, prints its text alone, and a sink with
// no error prints "-".
func lastSinkError(streams []ctl.SinkStreamInfo) string {
	var text string
	var at time.Time
	for _, st := range streams {
		if st.LastError == "" {
			continue
		}
		t, _ := time.Parse(time.RFC3339Nano, st.LastErrorAt)
		if text == "" || t.After(at) {
			text, at = st.LastError, t
		}
	}
	switch {
	case text == "":
		return "-"
	case at.IsZero():
		return text
	}
	return at.UTC().Format(time.RFC3339) + " " + text
}
