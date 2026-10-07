package main

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/strazahq/straza/internal/ctl"
	policyengine "github.com/strazahq/straza/internal/policy"
)

// policySimulateCmd is `strazactl policy simulate`: the console's what-if on
// the terminal, with the same endpoint, the same engine and the same why-view
// words. The policy command adds it as a subcommand.
func policySimulateCmd(client func() *ctl.Client) *cobra.Command {
	var (
		user, event, tool, command, app, toolName string
		attestation, eventJSON, draftFile         string
		roles, paths                              []string
	)
	cmd := &cobra.Command{
		Use:   "simulate",
		Short: "Ask what a call would do for a subject, against the live policies",
		Long: `Evaluate one event for one subject against the policies live right now,
optionally with a PolicySet from a local file overlaid in place of its
same-named stored set (a preview of what publishing would produce).

Exactly one of --user (real roles, resolved server-side) or --roles (a
hypothetical) is required. --event defaults to tool.pre. Any other event
field goes in --event-json, a file that holds the whole event object. The
verdict is the answer, not an enforcement result: simulate exits 0 on every
verdict.`,
		Example: `  strazactl policy simulate --user dana --tool shell.exec --command "rm -rf /home/dana/work/build"
  strazactl policy simulate --roles local-tools --tool shell.exec --command "kubectl apply -f deploy/app.yaml"
  strazactl policy simulate --user dana --event-json recorded-event.json`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if (user == "") == (len(roles) == 0) {
				return fmt.Errorf("exactly one of --user or --roles is required")
			}
			switch attestation {
			case "", "none", "advisory", "managed":
			default:
				return fmt.Errorf("--attestation must be none, advisory, or managed")
			}
			var ev policyengine.Event
			if eventJSON != "" {
				if cmd.Flags().Changed("event") || tool != "" || command != "" ||
					len(paths) > 0 || app != "" || toolName != "" {
					return fmt.Errorf("--event-json cannot be combined with the per-field event flags (--event, --tool, --command, --path, --app, --tool-name)")
				}
				raw, err := os.ReadFile(eventJSON) // #nosec G304 -- user-supplied file is the feature
				if err != nil {
					return err
				}
				if err := json.Unmarshal(raw, &ev); err != nil {
					return fmt.Errorf("%s: %w", eventJSON, err)
				}
			} else {
				ev = policyengine.Event{
					Kind: event, Tool: tool, Command: command,
					Paths: paths, App: app, ToolName: toolName,
				}
			}
			req := ctl.SimulateRequest{
				Event: ev,
				Subject: ctl.SimulateSubject{
					User: user, Roles: roles, Attestation: attestation,
				},
			}
			if draftFile != "" {
				raw, err := os.ReadFile(draftFile) // #nosec G304 -- user-supplied -f is the feature
				if err != nil {
					return err
				}
				req.Draft = string(raw)
			}
			res, err := client().SimulatePolicy(cmd.Context(), req)
			if err != nil {
				return err
			}
			// Now-truth evidence for the profile wording and the (live)
			// marker; unreachable overview degrades to no claim, never fails
			// the simulate.
			var now ctl.SimContext
			if ov, err := client().OverviewLite(cmd.Context()); err == nil {
				now.Profile, now.LiveSnapshot = ov.Profile, ov.SnapshotID
			}
			return ctl.RenderSimulation(cmd.OutOrStdout(), res, now)
		},
	}
	f := cmd.Flags()
	f.StringVar(&user, "user", "", "simulate this user (real roles, resolved server-side)")
	f.StringSliceVar(&roles, "roles", nil, "simulate a hypothetical subject holding these roles (comma-separated)")
	f.StringVar(&event, "event", "tool.pre", "event kind")
	f.StringVar(&tool, "tool", "", "tool id (e.g. shell.exec, mcp.call, file.write)")
	f.StringVar(&command, "command", "", "shell.exec: the raw command line")
	f.StringArrayVar(&paths, "path", nil, "file.*: affected path (repeatable)")
	f.StringVar(&app, "app", "", "mcp.call: the MCP server's name")
	f.StringVar(&toolName, "tool-name", "", "mcp.call: upstream tool name")
	f.StringVar(&attestation, "attestation", "", "subject attestation: none, advisory, or managed (default none)")
	f.StringVar(&eventJSON, "event-json", "", "read the full event object from a JSON file (excludes the per-field event flags)")
	f.StringVarP(&draftFile, "file", "f", "", "PolicySet YAML to overlay on the live policies")
	return cmd
}
