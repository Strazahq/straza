package main

import (
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"
	"unicode/utf8"

	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"

	"github.com/strazahq/straza/internal/conformance"
	"github.com/strazahq/straza/internal/ctl"
	"github.com/strazahq/straza/internal/events"
	"github.com/strazahq/straza/internal/manager"
	policyengine "github.com/strazahq/straza/internal/policy"
)

func appsCmd(client func() *ctl.Client) *cobra.Command {
	// "MCP server" is the user-facing noun. `apps` stays the wire and CLI
	// identifier (paths, subjects, manifest kind). Only the labels differ, by design.
	apps := &cobra.Command{Use: "apps", Aliases: []string{"servers"}, Short: "Manage MCP servers"}

	var listJSON bool
	list := &cobra.Command{
		Use:   "list",
		Short: "List MCP servers with live status, the reason behind it and the roles that reach each",
		RunE: func(cmd *cobra.Command, _ []string) error {
			if listJSON {
				raw, err := client().AppsJSON(cmd.Context())
				if err != nil {
					return err
				}
				return printServerJSON(cmd.OutOrStdout(), raw)
			}
			infos, err := client().Apps(cmd.Context())
			if err != nil {
				return err
			}
			rows := [][]string{{"NAME", "VERSION", "RUNTIME", "STATUS", "REASON", "SOURCE", "TOOLS", "REACHED BY", "CHECKED"}}
			for _, a := range infos {
				rows = append(rows, []string{a.Name, a.Version, a.Runtime, a.Status, a.Detail, a.Source,
					strconv.Itoa(len(a.Tools)), joinOrDash(a.ReachedBy), agoOrDash(a.LastProbeAt)})
			}
			width := reasonWidth(isTerminal(cmd.OutOrStdout()), os.Getenv("COLUMNS"), rows)
			tw := tabwriter.NewWriter(cmd.OutOrStdout(), 2, 4, 2, ' ', 0)
			for _, r := range rows {
				r[reasonCol] = trimReason(r[reasonCol], width)
				fmt.Fprintln(tw, strings.Join(r, "\t"))
			}
			return tw.Flush()
		},
	}
	list.Flags().BoolVar(&listJSON, "json", false, jsonFlagUsage)
	apps.AddCommand(list)

	var toolsApp string
	var toolsJSON bool
	tools := &cobra.Command{
		Use:   "tools",
		Short: "List exposed MCP tools with their upstream descriptions, every server or one",
		RunE: func(cmd *cobra.Command, _ []string) error {
			if toolsJSON {
				return printToolsJSON(cmd, client(), toolsApp)
			}
			tools, err := client().Tools(cmd.Context())
			if err != nil {
				return err
			}
			tw := tabwriter.NewWriter(cmd.OutOrStdout(), 2, 4, 2, ' ', 0)
			fmt.Fprintln(tw, "SERVER\tTOOL\tDESCRIPTION")
			shown := 0
			for _, tl := range tools {
				if toolsApp != "" && tl.App != toolsApp {
					continue
				}
				shown++
				fmt.Fprintf(tw, "%s\t%s\t%s\n", tl.App, tl.Name, tl.Description)
			}
			if err := tw.Flush(); err != nil {
				return err
			}
			if toolsApp != "" && shown == 0 {
				fmt.Fprintln(cmd.OutOrStdout(), noToolsFor(toolsApp))
			}
			return nil
		},
	}
	tools.Flags().StringVar(&toolsApp, "app", "", "show only this server's tools")
	tools.Flags().BoolVar(&toolsJSON, "json", false, jsonFlagUsage+" (with --app, that server's entries only)")
	apps.AddCommand(tools)

	apps.AddCommand(&cobra.Command{
		Use:   "recheck <server>",
		Short: "Trigger an immediate health probe and print the refreshed state",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := client().RecheckApp(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			detail := ""
			if a.Detail != "" {
				detail = " (" + a.Detail + ")"
			}
			fmt.Printf("%s: %s%s (probed %s, last healthy %s)\n",
				a.Name, a.Status, detail, agoOrDash(a.LastProbeAt), agoOrDash(a.LastHealthyAt))
			return nil
		},
	})

	apps.AddCommand(&cobra.Command{
		Use:   "enable <server>",
		Short: "Clear a server's admin pause and (re)start it",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := client().EnableApp(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			printAppState(a)
			return nil
		},
	})

	apps.AddCommand(&cobra.Command{
		Use:   "disable <server>",
		Short: "Pause a server: stop it and keep it stopped until re-enabled",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := client().DisableApp(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			printAppState(a)
			return nil
		},
	})

	var installFiles []string
	install := &cobra.Command{
		Use:   "install -f <path>...",
		Short: "Install or update MCP servers from app.yaml manifests",
		Long: "Validates each manifest locally, then uploads it. Installing a name that exists\n" +
			"updates that server in place, and a paused server takes the new manifest and stays\n" +
			"paused until strazactl apps enable. Each file prints one line with the runtime, the\n" +
			"health and, when one follows, the next command to run.",
		Example: "  strazactl apps install -f scout-tools.yaml",
		RunE: func(cmd *cobra.Command, _ []string) error {
			if len(installFiles) == 0 {
				return fmt.Errorf("at least one -f file is required")
			}
			for _, f := range installFiles {
				raw, err := os.ReadFile(f) // #nosec G304 -- user-supplied -f is the feature
				if err != nil {
					return err
				}
				// Validate locally first for a fast, offline error.
				if _, err := manager.Parse(raw); err != nil {
					return fmt.Errorf("%s: %w", f, err)
				}
				info, err := client().InstallApp(cmd.Context(), raw)
				if err != nil {
					return fmt.Errorf("%s: %w", f, err)
				}
				fmt.Fprintln(cmd.OutOrStdout(), installedLine(info))
			}
			return nil
		},
	}
	install.Flags().StringArrayVarP(&installFiles, "file", "f", nil, "the `path` of an MCP server's App manifest (repeatable)")
	apps.AddCommand(install)

	var logLimit int
	logs := &cobra.Command{
		Use:   "logs <server>",
		Short: "Show recent runtime log lines for a server",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			lines, err := client().AppLogs(cmd.Context(), args[0], logLimit)
			if err != nil {
				return err
			}
			for _, l := range lines {
				fmt.Println(l)
			}
			return nil
		},
	}
	logs.Flags().IntVar(&logLimit, "limit", 100, "max lines")
	apps.AddCommand(logs)

	apps.AddCommand(&cobra.Command{
		Use:   "remove <server>",
		Short: "Stop and remove a server",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := client().RemoveApp(cmd.Context(), args[0]); err != nil {
				return err
			}
			fmt.Printf("removed %s\n", args[0])
			return nil
		},
	})

	var bindRole string
	var bindTools []string
	bind := &cobra.Command{
		Use:   "bind <server>",
		Short: "Give a role the server owns access to its tools (a tool without access does not exist for the role)",
		Long: "Gives a role that the server owns its one access row on the server. A role made with\n" +
			"strazactl roles create scout-tools-readers --app scout-tools --tools echo has that row\n" +
			"already, so bind is for such a role whose row was removed with strazactl apps unbind.\n" +
			"To change the tools of a row, edit spec.bindings in the output of strazactl roles export\n" +
			"scout-tools-readers and propose that file with strazactl drafts create -f. A role that\n" +
			"belongs to no server gains no access row.",
		Example: "  strazactl apps bind scout-tools --role scout-tools-readers --tools \"echo,get-sum\"\n" +
			"  strazactl apps bind scout-tools --role scout-tools-readers",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if bindRole == "" {
				return fmt.Errorf("--role is required")
			}
			b, err := client().BindAppTools(cmd.Context(), args[0], bindRole, bindTools)
			if err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "gave %s access to %s: %s. They run unless a policy gates them. Assign the role on Users or through your identity manager.\n",
				b.Role, b.App, grantedTools(b.Tools))
			return nil
		},
	}
	bind.Flags().StringVar(&bindRole, "role", "", "the name of a role this server owns, made with strazactl roles create <server>-<word> --app <server>")
	bind.Flags().StringSliceVar(&bindTools, "tools", nil, "tool names or globs. Without it the access row is every tool, including tools added later, which only a global admin may give")
	apps.AddCommand(bind)

	apps.AddCommand(&cobra.Command{
		Use:   "unbind <access-id>",
		Short: "Remove a role's access to a server (the id is in bindings list and apps show)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			rows, err := client().Bindings(cmd.Context())
			if err != nil {
				return err
			}
			var row *ctl.ToolBinding
			for i := range rows {
				if rows[i].ID == args[0] {
					row = &rows[i]
				}
			}
			if row == nil {
				return fmt.Errorf("no access row has the id %q. Find the id with strazactl bindings list, or strazactl apps show <server>", args[0])
			}
			if err := client().UnbindTools(cmd.Context(), args[0]); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "removed %s's access to %s\n", row.Role, row.App)
			return nil
		},
	})

	apps.AddCommand(appsSecretCmd(client))
	apps.AddCommand(appsShowCmd(client))
	apps.AddCommand(appsExportCmd(client))

	var (
		name, runtime, out string
	)
	importCmd := &cobra.Command{
		Use:         "import <server.json>",
		Short:       "Convert an MCP registry server.json into an App manifest, by the registry import rules of the app manifest specification",
		Annotations: local, // file in, YAML out: nothing is uploaded
		Args:        cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			raw, err := os.ReadFile(args[0]) // #nosec G304 -- user-supplied path is the feature
			if err != nil {
				return err
			}
			m, err := manager.Import(raw, manager.ImportOptions{Name: name, Runtime: runtime})
			if err != nil {
				return err
			}
			doc, err := yaml.Marshal(m)
			if err != nil {
				return err
			}
			if out == "" {
				_, err = os.Stdout.Write(doc)
				return err
			}
			if err := os.WriteFile(out, doc, 0o600); err != nil { // #nosec G703 -- user-supplied -o is the feature
				return err
			}
			fmt.Printf("wrote %s (server %q, runtime %s)\n", out, m.Metadata.Name, m.Straza.Runtime.Kind)
			return nil
		},
	}
	importCmd.Flags().StringVar(&name, "name", "", "override the derived server name")
	importCmd.Flags().StringVar(&runtime, "runtime", "", "restrict runtime selection (command|remote|oci)")
	importCmd.Flags().StringVarP(&out, "output", "o", "", "write the manifest to a file instead of stdout")
	apps.AddCommand(importCmd)

	return apps
}

// noToolsFor is what apps tools --app says when the list holds no tool of
// that server. The list carries no row for a server that has no tools and
// none for a name that does not exist, so the sentence names both causes and
// sends the reader to apps show, which tells them apart.
func noToolsFor(app string) string {
	return fmt.Sprintf("no tools known for the MCP server %s: it has not answered a health probe yet, or it is stopped, or no server has that name. Run strazactl apps show %s for the reason.", app, app)
}

// installedLine is the sentence apps install prints: the runtime, then the
// health with its reason and the next command when one follows from it. A
// paused app took the manifest and stays stopped, so the line names enable.
func installedLine(info ctl.AppInfo) string {
	s := fmt.Sprintf("installed %s (%s runtime). Health: %s", info.Name, info.Runtime, info.Status)
	switch {
	case info.Paused:
		return fmt.Sprintf("%s. It stays paused: run strazactl apps enable %s to start it with this manifest.", s, info.Name)
	case info.Status == "running":
		return fmt.Sprintf("%s, %d tools.", s, len(info.Tools))
	case info.Detail == "":
		return s + "."
	case strings.Contains(info.Detail, "requires a credential"):
		return fmt.Sprintf("%s, %s. Set one with strazactl apps secret set %s, which asks for the value at a hidden prompt.", s, strings.TrimSuffix(info.Detail, "."), info.Name)
	default:
		return fmt.Sprintf("%s, %s. Fix the cause, then run strazactl apps recheck %s.", s, strings.TrimSuffix(info.Detail, "."), info.Name)
	}
}

// grantedTools names what an access row admits: the tool list, or the glob
// in words.
func grantedTools(tools []string) string {
	if len(tools) == 0 || (len(tools) == 1 && tools[0] == "*") {
		return "every tool, including tools added later"
	}
	return strings.Join(tools, ", ")
}

// reasonMax is the REASON column's width on apps list when the terminal's
// width is unknown. The full text is on apps show.
const reasonMax = 60

// reasonCol is the REASON column's index in the apps list rows.
const reasonCol = 4

// tabPadding is the space the apps list tabwriter adds after every cell
// but the last one.
const tabPadding = 2

// isTerminal reports whether w is a terminal, the one place a line wider
// than the screen wraps.
func isTerminal(w io.Writer) bool {
	f, ok := w.(*os.File)
	if !ok {
		return false
	}
	fi, err := f.Stat()
	return err == nil && fi.Mode()&os.ModeCharDevice != 0
}

// reasonWidth decides the REASON cut on apps list. Zero means no cut, the
// case of a pipe. On a terminal with COLUMNS set the cut is the room left
// beside the other columns as the tabwriter pads them, never under the
// header's width. Otherwise it is reasonMax.
func reasonWidth(terminal bool, columns string, rows [][]string) int {
	if !terminal {
		return 0
	}
	total, err := strconv.Atoi(columns)
	if err != nil || total <= 0 {
		return reasonMax
	}
	used := tabPadding * (len(rows[0]) - 1)
	for col := range rows[0] {
		if col == reasonCol {
			continue
		}
		widest := 0
		for _, r := range rows {
			widest = max(widest, utf8.RuneCountInString(r[col]))
		}
		used += widest
	}
	return max(total-used, len("REASON"))
}

// trimReason fits a health reason into the apps list column: "-" when
// there is none, the text cut at width with a marker when width is set and
// the text is longer, the full text otherwise.
func trimReason(detail string, width int) string {
	if detail == "" {
		return "-"
	}
	if r := []rune(detail); width > 0 && len(r) > width {
		return string(r[:width-3]) + "..."
	}
	return detail
}

// joinOrDash renders a name list as one cell, "-" when it is empty.
func joinOrDash(names []string) string {
	if len(names) == 0 {
		return "-"
	}
	return strings.Join(names, ",")
}

// printAppState prints the resulting STATUS line after an enable or disable:
// "<name>: <status>[ (detail)] [active|paused]".
func printAppState(a ctl.AppInfo) {
	state := "active"
	if a.Paused {
		state = "paused"
	}
	detail := ""
	if a.Detail != "" {
		detail = " (" + a.Detail + ")"
	}
	fmt.Printf("%s: %s%s [%s]\n", a.Name, a.Status, detail, state)
}

// agoOrDash renders an RFC 3339 health timestamp as a relative age ("42s
// ago"); "-" for never-probed apps or unparseable input.
func agoOrDash(ts string) string {
	if ts == "" {
		return "-"
	}
	at, err := time.Parse(time.RFC3339, ts)
	if err != nil {
		return "-"
	}
	return fmt.Sprintf("%s ago", time.Since(at).Round(time.Second))
}

func specCmd() *cobra.Command {
	// The whole group runs against embedded validators and the embedded
	// corpus; a third-party implementer needs no Straza deployment at all.
	spec := &cobra.Command{Use: "spec", Short: "Work with the Straza spec artifacts", Annotations: local}

	var files []string
	validate := &cobra.Command{
		Use:   "validate <policyset|app|event>",
		Short: "Validate documents against the embedded v1beta1 spec validators",
		Args:  cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			if len(files) == 0 {
				return fmt.Errorf("at least one -f file is required")
			}
			check, err := specValidator(args[0])
			if err != nil {
				return err
			}
			for _, f := range files {
				raw, err := os.ReadFile(f) // #nosec G304 -- user-supplied -f is the feature
				if err != nil {
					return err
				}
				if err := check(raw); err != nil {
					return fmt.Errorf("%s: %w", f, err)
				}
				fmt.Printf("%s: %s OK\n", f, args[0])
			}
			return nil
		},
	}
	validate.Flags().StringArrayVarP(&files, "file", "f", nil, "document to validate (repeatable)")
	spec.AddCommand(validate)

	var suiteName, cmdline string
	conform := &cobra.Command{
		Use:   "conformance",
		Short: "Replay the published Tier-1 corpus against a hook implementation and score it",
		Long: "Replays spec/conformance/tier1 (embedded) against an arbitrary hook implementation\n" +
			"and scores it. Per case the command runs with STRAZA_HARNESS set and the payload on\n" +
			"stdin; {policy} in --cmd is replaced with the conformance PolicySet path.\n" +
			"Straza itself: --cmd \"straza hook --conformance-policy {policy}\"",
		RunE: func(_ *cobra.Command, _ []string) error {
			if suiteName != "hook-profile" {
				return fmt.Errorf("unknown suite %q: hook-profile is the only v1 suite", suiteName)
			}
			if cmdline == "" {
				return fmt.Errorf("--cmd is required (the hook command under test)")
			}
			results, err := conformance.RunHookProfile(cmdline, os.Stdout)
			if err != nil {
				return err
			}
			if !conformance.Passed(results) {
				return fmt.Errorf("Tier-1 conformance FAILED")
			}
			return nil
		},
	}
	conform.Flags().StringVar(&suiteName, "suite", "hook-profile", "conformance suite to run")
	conform.Flags().StringVar(&cmdline, "cmd", "", "hook command under test ({policy} → conformance policy path)")
	spec.AddCommand(conform)
	return spec
}

// specValidator maps a spec kind to its embedded validator. Each validator is
// tested to agree with its JSON Schema on the spec example corpus, so this
// doubles as the third-party implementer's tool.
func specValidator(kind string) (func([]byte) error, error) {
	switch kind {
	case "policyset":
		return func(raw []byte) error { _, err := policyengine.ParseAll(raw); return err }, nil
	case "app", "app-manifest":
		return func(raw []byte) error { _, err := manager.Parse(raw); return err }, nil
	case "event", "events":
		return events.Validate, nil
	default:
		return nil, fmt.Errorf("unknown spec kind %q: expected policyset|app|event", kind)
	}
}
