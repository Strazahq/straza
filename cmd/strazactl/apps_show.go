package main

import (
	"context"
	"fmt"
	"io"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/strazahq/straza/internal/ctl"
)

// appsShowCmd is `strazactl apps show <app>`: one read that carries the
// health with its full reason, the tools, every access row with how its
// tools run (read back from the catalog preview), and the stored secrets
// by fingerprint.
func appsShowCmd(client func() *ctl.Client) *cobra.Command {
	return &cobra.Command{
		Use:   "show <server>",
		Short: "Show one server in full: health and its reason, tools, who has access and how each tool runs, the stored secrets",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c := client()
			ctx := cmd.Context()
			apps, err := c.Apps(ctx)
			if err != nil {
				return err
			}
			var app *ctl.AppInfo
			for i := range apps {
				if apps[i].Name == args[0] || apps[i].ID == args[0] {
					app = &apps[i]
				}
			}
			if app == nil {
				return fmt.Errorf("no MCP server named %q. List them with strazactl apps list", args[0])
			}
			rows, err := c.Bindings(ctx)
			if err != nil {
				return err
			}
			var access []ctl.ToolBinding
			for _, b := range rows {
				if b.App == app.Name {
					access = append(access, b)
				}
			}
			out := cmd.OutOrStdout()
			writeAppHead(out, *app)
			writeAppAccess(ctx, out, c, *app, access)
			secrets, err := c.AppSecrets(ctx, app.Name)
			writeAppSecrets(out, secrets, err)
			return nil
		},
	}
}

func writeAppHead(w io.Writer, app ctl.AppInfo) {
	fmt.Fprintf(w, "%s: %s (%s runtime, source %s, version %s)\n", app.Name, app.Status, app.Runtime, app.Source, app.Version)
	if app.Detail != "" {
		fmt.Fprintf(w, "reason:      %s\n", app.Detail)
	}
	fmt.Fprintf(w, "checked:     %s, last healthy %s\n", agoOrDash(app.LastProbeAt), agoOrDash(app.LastHealthyAt))
	if len(app.Tools) == 0 {
		fmt.Fprintln(w, "tools:       none known")
	} else {
		fmt.Fprintf(w, "tools:       %d: %s\n", len(app.Tools), strings.Join(app.Tools, ", "))
	}
	fmt.Fprintf(w, "reached by:  %s\n", joinOrDash(app.ReachedBy))
	fmt.Fprintf(w, "admin role:  %s\n", adminRoleFact(app.AdminRole))
}

// adminRoleFact names the role that administers this server and says what
// holding it means. An empty name is only possible against a strazad older
// than admin API 0.127.0, so the line sends the reader to the upgrade.
func adminRoleFact(role string) string {
	if role == "" {
		return "none reported, so this strazad speaks an admin API older than 0.127.0. " +
			"Upgrade strazad to see the role that administers this server."
	}
	return role + " (its holders administer this server and no other)"
}

// writeAppAccess prints the access rows with the engine's word per tool,
// one preview per role. A preview that fails is reported on its row and
// never fails the read.
func writeAppAccess(ctx context.Context, w io.Writer, c *ctl.Client, app ctl.AppInfo, access []ctl.ToolBinding) {
	fmt.Fprintln(w, "\nACCESS")
	if len(access) == 0 {
		fmt.Fprintf(w, "no role has access to %s. Create one with strazactl roles create %s-<word> --app %s --tools <tool,...>.\n", app.Name, app.Name, app.Name)
		return
	}
	previews := map[string]string{}
	tw := tabwriter.NewWriter(w, 2, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "ID\tROLE\tTOOLS\tHOW THEY RUN")
	for _, b := range access {
		how, seen := previews[b.Role]
		if !seen {
			p, err := c.CatalogPreview(ctx, []string{b.Role}, "", app.Name)
			if err != nil {
				how = "preview unavailable: " + err.Error()
			} else {
				how = howTheyRun(p)
			}
			previews[b.Role] = how
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", b.ID, b.Role, grantedTools(b.Tools), how)
	}
	_ = tw.Flush()
}

// howTheyRun turns one role's preview rows into the words the console
// uses: runs, needs approval, denied with the policy set that denies it,
// and no policy yet when the default deny hid the tool and no rule spoke.
func howTheyRun(p ctl.CatalogPreview) string {
	var parts []string
	for _, e := range p.Entries {
		set := ""
		if e.SetName != "" {
			set = " (" + e.SetName + ")"
		}
		switch e.Status {
		case "visible":
			parts = append(parts, e.Tool+" runs")
		case "approve_gated":
			parts = append(parts, e.Tool+" needs approval"+set)
		case "hidden_policy":
			if e.Default {
				parts = append(parts, e.Tool+" no policy yet")
				continue
			}
			parts = append(parts, e.Tool+" denied"+set)
		case "not_running":
			if e.Hint != "" {
				return e.Hint
			}
			return "the server is not running, so nothing runs yet"
		}
	}
	if len(parts) == 0 {
		return "nothing runs yet: no tool in the access row is known to the server"
	}
	return strings.Join(parts, ", ")
}

func writeAppSecrets(w io.Writer, secrets []ctl.AppSecret, err error) {
	fmt.Fprintln(w, "\nSECRETS")
	switch {
	case err != nil:
		fmt.Fprintf(w, "not readable: %v\n", err)
		return
	case len(secrets) == 0:
		fmt.Fprintln(w, "none stored")
		return
	}
	tw := tabwriter.NewWriter(w, 2, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "SCOPE\tROLE\tKIND\tFINGERPRINT\tSET")
	for _, s := range secrets {
		role := s.Role
		if role == "" {
			role = "-"
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", s.Scope, role, s.Kind, s.Fingerprint, s.SetAt)
	}
	_ = tw.Flush()
}
