package main

import (
	"fmt"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/strazahq/straza/internal/ctl"
)

func rolesCmd(client func() *ctl.Client) *cobra.Command {
	roles := &cobra.Command{Use: "roles", Short: "Manage roles"}
	var listJSON bool
	listCmd := &cobra.Command{
		Use:   "list",
		Short: "List roles",
		RunE: func(cmd *cobra.Command, _ []string) error {
			if listJSON {
				raw, err := client().RolesJSON(cmd.Context())
				if err != nil {
					return err
				}
				return printServerJSON(cmd.OutOrStdout(), raw)
			}
			list, err := client().Roles(cmd.Context())
			if err != nil {
				return err
			}
			// The SERVER column appears only when a role in the answer
			// carries one, so an install with no server-owned role reads
			// exactly as it read before.
			owned := false
			for _, r := range list {
				if r.Server != "" {
					owned = true
					break
				}
			}
			tw := tabwriter.NewWriter(cmd.OutOrStdout(), 2, 4, 2, ' ', 0)
			if owned {
				fmt.Fprintln(tw, "NAME\tKIND\tSERVER\tDESCRIPTION\tID")
			} else {
				fmt.Fprintln(tw, "NAME\tKIND\tDESCRIPTION\tID")
			}
			for _, r := range list {
				if !owned {
					fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", r.Name, r.Kind, r.Description, r.ID)
					continue
				}
				server := r.Server
				if server == "" {
					server = "-"
				}
				fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", r.Name, r.Kind, server, r.Description, r.ID)
			}
			return tw.Flush()
		},
	}
	listCmd.Flags().BoolVar(&listJSON, "json", false, jsonFlagUsage)
	roles.AddCommand(listCmd)
	var description, kind, ownerApp string
	var tools []string
	create := &cobra.Command{
		Use:   "create <name>",
		Short: "Create a role",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			// The two flags travel together or not at all, which is a shape
			// of this command and is checked here. The tool names, the name
			// prefix and the kind stay the server's to judge, so its 400 is
			// what the operator reads. The owning server is --app, because
			// the root already spends --server on the strazad to talk to.
			if ownerApp != "" && len(tools) == 0 {
				return fmt.Errorf("--tools is required with --app, because a role that belongs to an MCP server reaches only the tools it lists. Add the names, for example --tools read_file,list_dir, and run `strazactl apps tools --app %s` to see what the server offers. A global admin may send --tools '*' for every tool, the ones the server adds later included", ownerApp)
			}
			if ownerApp == "" && len(tools) > 0 {
				return fmt.Errorf("--tools needs --app <server>, because only a role that an MCP server owns reaches tools, and it is created on that server. Run `strazactl roles create <server>-%s --app <server> --tools %s`, with a server name from strazactl apps list", args[0], strings.Join(tools, ","))
			}
			// The CLI takes no silent default: a kindless create would be
			// stored as business, which can hold no tools. --app fixes the
			// kind to application, because the server stores a server-owned
			// role as nothing else.
			switch {
			case ownerApp == "" && kind == "":
				return fmt.Errorf("roles create needs --kind or --app, because a role's kind is fixed at create and Straza must know what %s is for. "+
					"Use --app <server> --tools <tool,...> for a role that reaches an MCP server's tools, --kind application for a role that policy sets match and that reaches no MCP server, "+
					"business for a role that bundles application roles for a job, "+
					"approver for a role that decides approval requests, or straza for a role with capabilities in Straza itself", args[0])
			case ownerApp != "" && kind != "" && kind != "application":
				return fmt.Errorf("--kind %s does not fit --app, because a role that belongs to an MCP server is always an application role. Leave --kind out or set it to application", kind)
			}
			// A set kind travels as typed: the server owns the enum, and its
			// 400 names the value sent and the four kinds to set instead, so
			// there is no second copy to drift.
			r, err := client().CreateRole(cmd.Context(), args[0], description, kind, ownerApp, tools)
			if err != nil {
				return err
			}
			if r.Server != "" {
				fmt.Fprintf(cmd.OutOrStdout(), "created role %s (%s) on the server %s\n", r.Name, r.ID, r.Server)
				return nil
			}
			fmt.Fprintf(cmd.OutOrStdout(), "created role %s (%s)\n", r.Name, r.ID)
			return nil
		},
	}
	create.Flags().StringVar(&description, "description", "", "role description")
	create.Flags().StringVar(&kind, "kind", "",
		"role kind, required unless --app: application is matched by policy sets and reaches an MCP server's tools only with --app, business bundles application roles for a job, "+
			"approver decides approval requests and is the only kind a policy may name in approve.roles besides straza-admin, "+
			"straza carries capabilities in Straza itself, the control plane. Approver and straza roles never hold tools")
	create.Flags().StringVar(&ownerApp, "app", "",
		"the MCP server that owns this role, by name. The role is named after that server, reaches it alone and is removed with it")
	create.Flags().StringSliceVar(&tools, "tools", nil,
		"the tools this role reaches on the owning server, comma-separated and named one at a time, or * for every tool, which only a global admin may give. Required with --app")
	roles.AddCommand(create)

	var upDescription string
	update := &cobra.Command{
		Use:   "update <name>",
		Short: "Update a role's description (the name and the kind are fixed at create)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			// The flag must be SET: an unset default would blank the description.
			if !cmd.Flags().Changed("description") {
				return fmt.Errorf("--description is required: it is the one field a role update changes. The name and the kind are fixed at create")
			}
			r, err := client().UpdateRole(cmd.Context(), args[0], upDescription)
			if err != nil {
				return err
			}
			fmt.Printf("updated role %s (%s)\n", r.Name, r.ID)
			return nil
		},
	}
	update.Flags().StringVar(&upDescription, "description", "", "role description")
	roles.AddCommand(update)

	// The bare form lists a role's edges; add and remove are subcommands of it.
	// Cobra resolves the first operand against the subcommand names first, so a
	// role literally NAMED "add" or "remove" cannot be listed through this
	// command. Its edges stay visible in `roles list` and the console.
	implications := &cobra.Command{
		Use:   "implications <name>",
		Short: "List the roles this role composes (holding it also holds these)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			list, err := client().RoleImplications(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			tw := tabwriter.NewWriter(os.Stdout, 2, 4, 2, ' ', 0)
			fmt.Fprintln(tw, "COMPOSES\tROLE_ID")
			for _, imp := range list {
				fmt.Fprintf(tw, "%s\t%s\n", imp.ImpliesName, imp.ImpliesID)
			}
			return tw.Flush()
		},
	}
	implications.AddCommand(&cobra.Command{
		Use:   "add <role> <implied>",
		Short: "Compose one role into another: holding the first also holds the second",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := client().AddRoleImplication(cmd.Context(), args[0], args[1]); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "added: holding %s now also holds %s\n", args[0], args[1])
			return nil
		},
	})
	implications.AddCommand(&cobra.Command{
		Use:   "remove <role> <implied>",
		Short: "Stop composing one role into another (holding the first no longer holds the second)",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := client().RemoveRoleImplication(cmd.Context(), args[0], args[1]); err != nil {
				return err
			}
			fmt.Fprintln(cmd.OutOrStdout(), "removed")
			return nil
		},
	})
	roles.AddCommand(implications)
	roles.AddCommand(&cobra.Command{
		Use:   "export <name>",
		Short: "Print the role's canonical VCS document (spec/objects kind Role) to stdout",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			doc, err := client().RoleExport(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			_, err = os.Stdout.Write(doc)
			return err
		},
	})
	var deleteYes bool
	del := &cobra.Command{
		Use:   "delete <name>",
		Short: "Delete a role; its assignments and access rows go with it",
		Long: "Deletes the role: its assignments and access rows go with it.\n" +
			"A product role cannot be deleted and answers 409, which covers\n" +
			"`straza-admin` and the two `straza-enroll` roles. To take someone's\n" +
			"access away, run strazactl unassign with the role and --user instead.\n" +
			"A live policy set whose match.roles names only this role is turned off\n" +
			"with it, since it would match nobody. The answer names such sets.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if !deleteYes && !confirm(cmd, fmt.Sprintf("delete role %s? Its assignments and access rows go with it.", args[0])) {
				return errAborted()
			}
			res, err := client().DeleteRole(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "deleted role %s\n", args[0])
			// The server names a set only to a caller who may read policy
			// sets, and an older server sends no field, so nothing is printed
			// for an empty or absent list.
			for _, set := range res.SetsOff {
				fmt.Fprintf(cmd.OutOrStdout(), "turned off the policy set %s, which matched only this role. Delete it with strazactl policy delete --yes %s when you no longer need it.\n", set, set)
			}
			return nil
		},
	}
	del.Flags().BoolVar(&deleteYes, "yes", false, "delete without the interactive confirm")
	roles.AddCommand(del)
	return roles
}

func assignCmd(client func() *ctl.Client) *cobra.Command {
	var user string
	cmd := &cobra.Command{
		Use:   "assign <role>",
		Short: "Assign a role to a user",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if user == "" {
				return fmt.Errorf("--user is required")
			}
			if _, err := client().Assign(cmd.Context(), user, args[0]); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "assigned %s to %s\n", args[0], user)
			return nil
		},
	}
	cmd.Flags().StringVar(&user, "user", "", "username to assign the role to")
	return cmd
}

// unassignCmd is `strazactl unassign <role> --user <username>`, the way
// back from assign in the same form.
func unassignCmd(client func() *ctl.Client) *cobra.Command {
	var user string
	cmd := &cobra.Command{
		Use:   "unassign <role>",
		Short: "Take a role away from a user",
		Long: "Takes the role away from the user by deleting their assignment of it. A role\n" +
			"the user reaches through another role is not held directly and goes with that\n" +
			"role. A membership the identity manager wrote is mastered there, so remove it\n" +
			"there as well.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if user == "" {
				return fmt.Errorf("strazactl unassign needs --user, the username of the person who holds the role, as in strazactl unassign %s --user alice", args[0])
			}
			if err := client().Unassign(cmd.Context(), user, args[0]); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "unassigned %s from %s\n", args[0], user)
			return nil
		},
	}
	cmd.Flags().StringVar(&user, "user", "", "username to take the role away from")
	return cmd
}
