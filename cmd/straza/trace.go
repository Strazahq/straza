package main

import (
	"fmt"
	"io"
	"time"

	"github.com/spf13/cobra"

	"github.com/strazahq/straza/internal/agentguard"
	"github.com/strazahq/straza/internal/agentguard/trace"
)

const traceOperands = "on, off, status, show"

// traceCmd is the client's diagnostic trail: `straza trace on|off|status|show`.
// The decision journal (one content-free line per decision: tool name, effect,
// rule, snapshot, escalation, server status and correlation id, timing; never
// commands, paths, prompts or reason text) is always on. `on` opens a
// time-boxed debug window beside it (default 1h, max 24h), `off` closes it,
// `status` says what is in force and why, `show` prints the records. One
// canonical form: the operand is positional and
// required, the only modifiers are --for on `on` and -n on `show`, and a
// missing or unknown operand errors listing the valid values. The install
// config's `trace:` field (off | journal | debug; the managed file wins) can
// switch the surface off or force debug; the error log (`straza logs`) is
// never affected.
func traceCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "trace <on|off|status|show>",
		Short: "Decision journal and debug trace (content-free): on, off, status, show",
		Args: func(_ *cobra.Command, args []string) error {
			if len(args) == 0 {
				return fmt.Errorf("specify what to do: %s", traceOperands)
			}
			return fmt.Errorf("unknown trace operand %q: specify what to do: %s", args[0], traceOperands)
		},
		RunE: func(*cobra.Command, []string) error {
			return fmt.Errorf("specify what to do: %s", traceOperands)
		},
	}
	cmd.AddCommand(traceOnCmd(), traceOffCmd(), traceStatusCmd(), traceShowCmd())
	return cmd
}

func traceOnCmd() *cobra.Command {
	var window time.Duration
	cmd := &cobra.Command{
		Use:   "on",
		Short: "Open a debug window (default 1h, max 24h); the journal is always on",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if window < time.Second || window > trace.MaxWindow {
				return fmt.Errorf("--for must be between 1s and 24h")
			}
			store, err := agentguard.OpenStore()
			if err != nil {
				return err
			}
			st, err := agentguard.EnableTrace(store, window, time.Now())
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			if st.ConfigLevel == "debug" {
				fmt.Fprintf(out, "debug trace is already forced on by %s; nothing to do\n", st.ConfigPath)
				return nil
			}
			fmt.Fprintf(out, "debug trace on until %s (%s); records in %s\n",
				st.Toggle.Until.UTC().Format(time.RFC3339), trace.DurationString(window), st.Path)
			return nil
		},
	}
	cmd.Flags().DurationVar(&window, "for", trace.DefaultWindow, "how long debug stays on (1s to 24h)")
	return cmd
}

func traceOffCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "off",
		Short: "Close the debug window (the journal stays on)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			store, err := agentguard.OpenStore()
			if err != nil {
				return err
			}
			if err := agentguard.DisableTrace(store); err != nil {
				return err
			}
			fmt.Fprintln(cmd.OutOrStdout(), "debug trace off (the journal stays on)")
			return nil
		},
	}
}

func traceStatusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Report the trace level in force, its source, the window and the file",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			store, err := agentguard.OpenStore()
			if err != nil {
				return err
			}
			printTraceStatus(cmd.OutOrStdout(), agentguard.TraceStatus(store, time.Now()), time.Now())
			return nil
		},
	}
}

// printTraceStatus renders the three status lines: level + source, window,
// file. Pure (no I/O beyond w) so tests pin the wording.
func printTraceStatus(w io.Writer, st agentguard.TraceState, now time.Time) {
	source := "default"
	switch st.ConfigLevel {
	case "off", "debug":
		source = "config " + st.ConfigPath
	case "", "journal":
		if st.Toggle.Active(now) {
			source = "window until " + st.Toggle.Until.UTC().Format(time.RFC3339)
		}
	default:
		source = fmt.Sprintf("config %s says %q, treated as journal", st.ConfigPath, st.ConfigLevel)
	}
	fmt.Fprintf(w, "level: %s (source: %s)\n", st.Level, source)
	switch {
	case st.Toggle.Active(now):
		fmt.Fprintf(w, "window: until %s (%s left)\n",
			st.Toggle.Until.UTC().Format(time.RFC3339), trace.DurationString(st.Toggle.Until.Sub(now)))
	case st.Toggle.Expired(now):
		fmt.Fprintf(w, "window: expired %s ago (treated as off)\n", trace.DurationString(now.Sub(st.Toggle.Until)))
	default:
		fmt.Fprintln(w, "window: none")
	}
	fmt.Fprintf(w, "file: %s (%d bytes, %d record(s), %d unreadable)\n",
		st.Path, st.Summary.Size, st.Summary.Records, st.Summary.Unreadable)
}

func traceShowCmd() *cobra.Command {
	var last int
	cmd := &cobra.Command{
		Use:   "show",
		Short: "Print the journal and debug records, oldest first",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			store, err := agentguard.OpenStore()
			if err != nil {
				return err
			}
			recs, unreadable := agentguard.TraceRecords(store, last)
			out := cmd.OutOrStdout()
			if len(recs) == 0 && unreadable == 0 {
				fmt.Fprintf(out, "no trace records (file: %s)\n", store.TracePath())
				return nil
			}
			for _, r := range recs {
				fmt.Fprintln(out, trace.Render(r))
			}
			if unreadable > 0 {
				fmt.Fprintf(out, "(%d unreadable line(s) skipped)\n", unreadable)
			}
			return nil
		},
	}
	cmd.Flags().IntVarP(&last, "lines", "n", 0, "print only the last N records (0 = all)")
	return cmd
}
