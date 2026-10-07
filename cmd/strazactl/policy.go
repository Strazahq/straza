package main

import (
	"fmt"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/strazahq/straza/internal/ctl"
	"github.com/strazahq/straza/internal/drafts"
	policyengine "github.com/strazahq/straza/internal/policy"
)

func policyCmd(client func() *ctl.Client) *cobra.Command {
	policy := &cobra.Command{Use: "policy", Short: "Validate and manage PolicySets"}

	var files []string
	validateCmd := &cobra.Command{
		Use:   "validate",
		Short: "Validate PolicySet YAML files locally against the v1beta1 spec",
		Long: "Validates PolicySet YAML files offline against the v1beta1 spec, with no server\n" +
			"and no login. `strazactl drafts check -f` runs the server's checks against live state.",
		Annotations: local, // purely offline: no server, no login needed
		RunE: func(cmd *cobra.Command, _ []string) error {
			if len(files) == 0 {
				return fmt.Errorf("at least one -f file is required")
			}
			namesRoles := false
			for _, f := range files {
				raw, err := os.ReadFile(f) // #nosec G304 -- user-supplied -f is the feature
				if err != nil {
					return err
				}
				docs, err := policyengine.ParseAll(raw)
				if err != nil {
					return fmt.Errorf("%s: %w", f, err)
				}
				// Compile as activation does, so a Rego module it would
				// refuse is refused here.
				if _, err := policyengine.NewEngine(docs, policyengine.EffectAllow); err != nil {
					return fmt.Errorf("%s: %w", f, err)
				}
				for _, d := range docs {
					if viol := strazaRoleViolations(d); len(viol) > 0 {
						return fmt.Errorf("%s: PolicySet %q: %s", f, d.Metadata.Name, strings.Join(viol, "; "))
					}
					namesRoles = namesRoles || drafts.NamesRoles(d)
					fmt.Fprintf(cmd.OutOrStdout(), "%s: PolicySet %q OK (%d rules, priority %d)\n",
						f, d.Metadata.Name, len(d.Spec.Rules), d.Spec.Priority)
				}
			}
			if namesRoles {
				fmt.Fprintf(cmd.OutOrStdout(), "note: validate refuses only the roles the product reserves, whose names start with straza- or mcp-admin-, in match.roles, "+
					"and any of them but straza-admin in approve.roles. "+
					"The server judges every other role a set names, in match.roles and approve.roles, and strazactl drafts check%s runs those checks against live state.\n", fileArgs(files))
			}
			return nil
		},
	}
	validateCmd.Flags().StringArrayVarP(&files, "file", "f", nil, "PolicySet YAML file (repeatable)")
	policy.AddCommand(validateCmd)

	var listJSON bool
	list := &cobra.Command{
		Use:   "list",
		Short: "List stored PolicySets",
		RunE: func(cmd *cobra.Command, _ []string) error {
			if listJSON {
				raw, err := client().PoliciesJSON(cmd.Context())
				if err != nil {
					return err
				}
				return printServerJSON(cmd.OutOrStdout(), raw)
			}
			sets, err := client().Policies(cmd.Context())
			if err != nil {
				return err
			}
			tw := tabwriter.NewWriter(cmd.OutOrStdout(), 2, 4, 2, ' ', 0)
			fmt.Fprintln(tw, "NAME\tPRIORITY\tSTATUS\tID")
			for _, p := range sets {
				status := statusWord(ctl.PolicyDetail{Status: p.Status, Drift: p.Drift})
				fmt.Fprintf(tw, "%s\t%d\t%s\t%s\n", p.Name, p.Priority, status, p.ID)
			}
			return tw.Flush()
		},
	}
	list.Flags().BoolVar(&listJSON, "json", false, jsonFlagUsage)
	policy.AddCommand(list)

	var applyFiles []string
	apply := &cobra.Command{
		Use:   "apply",
		Short: "Upload PolicySet YAML: a new set is stored off, and edits to a live set wait for policy activate",
		Long: "Uploads every PolicySet document in the -f files to the server, which validates\n" +
			"each one. A set the server does not have yet is stored off and governs nothing.\n" +
			"For a set that is already live, the upload is stored as edits and\n" +
			"the published version keeps deciding. `strazactl policy list` shows such a set\n" +
			"as Live, edits not published, until `strazactl policy activate <name>` checks the\n" +
			"edits and publishes them.",
		Example: "  strazactl policy apply -f local-tools-guardrails.yaml\n" +
			"  strazactl policy activate local-tools-guardrails",
		RunE: func(cmd *cobra.Command, _ []string) error {
			if len(applyFiles) == 0 {
				return fmt.Errorf("at least one -f file is required")
			}
			for _, f := range applyFiles {
				raw, err := os.ReadFile(f) // #nosec G304 -- user-supplied -f is the feature
				if err != nil {
					return err
				}
				// Validate locally first for a fast, offline error.
				docs, err := policyengine.ParseAll(raw)
				if err != nil {
					return fmt.Errorf("%s: %w", f, err)
				}
				// The server applies exactly ONE document per request and
				// rejects more. A half-applied multi-doc file would be policy
				// that looks applied and is never enforced, so a multi-doc
				// file is split and each document applied on its own.
				chunks, err := splitPolicyDocs(raw, len(docs))
				if err != nil {
					return fmt.Errorf("%s: %w", f, err)
				}
				for _, chunk := range chunks {
					p, err := client().ApplyPolicy(cmd.Context(), chunk)
					if err != nil {
						return fmt.Errorf("%s: %w", f, err)
					}
					if p.Status == "active" {
						// The server keeps the live set's published text running
						// and stores the upload beside it, so this line must not
						// read as live or published.
						fmt.Fprintf(cmd.OutOrStdout(), "saved edits to the live policy set %s (%s) without publishing them. "+
							"The published version keeps deciding until you run strazactl policy activate %s, which checks the edits and publishes them.\n",
							p.Name, p.ID, p.Name)
						continue
					}
					fmt.Fprintf(cmd.OutOrStdout(), "applied %s (%s, %s)\n", p.Name, liveWord(p.Status), p.ID)
				}
			}
			return nil
		},
	}
	apply.Flags().StringArrayVarP(&applyFiles, "file", "f", nil, "PolicySet YAML file (repeatable)")
	policy.AddCommand(apply)

	activate := &cobra.Command{
		Use:   "activate <name>",
		Short: "Publish one PolicySet's stored text after the server's checks, leaving every other set as published",
		Long: "Runs the server's activation checks on the named set's stored text and, when\n" +
			"they pass, publishes it in a new signed snapshot that every enforcement point\n" +
			"pulls. Only this set changes. Every other live set keeps the text it was last\n" +
			"published with, including edits saved to it and not published yet.\n" +
			"A refused check changes nothing and names what to fix.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			act, err := client().ActivatePolicy(cmd.Context(), args[0], "active")
			if err != nil {
				return err
			}
			if act.Changed != nil && !*act.Changed {
				fmt.Fprintf(cmd.OutOrStdout(), "%s is already live at snapshot %s, so nothing changed.\n", args[0], act.Snapshot)
				return nil
			}
			fmt.Fprintf(cmd.OutOrStdout(), "published %s, it is live now; new snapshot %s\n", args[0], act.Snapshot)
			return nil
		},
	}
	policy.AddCommand(activate)

	deactivate := &cobra.Command{
		Use:   "deactivate <name>",
		Short: "Turn a PolicySet off and recompile and distribute the snapshot",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			act, err := client().ActivatePolicy(cmd.Context(), args[0], "draft")
			if err != nil {
				return err
			}
			if act.Changed != nil && !*act.Changed {
				fmt.Fprintf(cmd.OutOrStdout(), "%s is already off at snapshot %s, so nothing changed.\n", args[0], act.Snapshot)
				return nil
			}
			fmt.Fprintf(cmd.OutOrStdout(), "turned %s off; new snapshot %s\n", args[0], act.Snapshot)
			return nil
		},
	}
	policy.AddCommand(deactivate)

	var deleteYes bool
	del := &cobra.Command{
		Use:   "delete <name>",
		Short: "Delete a PolicySet that is off (turn a live one off first)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if !deleteYes && !confirm(cmd, fmt.Sprintf("delete policy set %s?", args[0])) {
				return errAborted()
			}
			if err := client().DeletePolicy(cmd.Context(), args[0]); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "deleted policy set %s\n", args[0])
			return nil
		},
	}
	del.Flags().BoolVar(&deleteYes, "yes", false, "delete without the interactive confirm")
	policy.AddCommand(del)

	policy.AddCommand(policySimulateCmd(client))
	policy.AddCommand(policyShowCmd(client))
	policy.AddCommand(policyDiffCmd(client))

	return policy
}

// strazaRoleViolations runs activation's approve-pool and match.roles rules
// offline, in activation's order, for the roles validate can know without
// the role table: the roles the product reserves, whose names start with a
// lowercase straza- or with drafts.AppAdminRolePrefix, a server's admin
// role, and which the product creates on the control plane. The refusal
// reads as activation's. Role lookup is exact, so a name in other capitals
// names no role and passes here. Every other role needs the server's role
// table, so every other approve.roles entry stands in as an approver role,
// which the approve-pool rule passes, and activation judges it.
func strazaRoleViolations(doc policyengine.Document) []string {
	reserved := func(name string) bool {
		return strings.HasPrefix(name, "straza-") || strings.HasPrefix(name, drafts.AppAdminRolePrefix)
	}
	deciders := drafts.World{Roles: map[string]drafts.Role{}}
	for _, r := range doc.Spec.Rules {
		if r.Approve == nil {
			continue
		}
		for _, name := range r.Approve.Roles {
			deciders.Roles[name] = drafts.Role{Name: name, Kind: drafts.RoleKindApprover}
			if reserved(name) {
				deciders.Roles[name] = drafts.Role{Name: name, Plane: drafts.PlaneControl}
			}
		}
	}
	if viol := drafts.ApprovePoolViolations(deciders, doc); len(viol) > 0 {
		return viol
	}
	w := drafts.World{Roles: map[string]drafts.Role{}}
	for _, name := range doc.Spec.Match.Roles {
		if reserved(name) {
			w.Roles[name] = drafts.Role{Name: name, Plane: drafts.PlaneControl}
		}
	}
	return drafts.MatchRoleViolations(w, doc)
}
