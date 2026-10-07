package main

import (
	"fmt"
	"math"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/strazahq/straza/internal/ctl"
)

const (
	purposeSession         = "session"
	purposeClientAssertion = "client_assertion"
	rotateExample          = "session or client_assertion. Example: strazactl signing-keys rotate session"
)

// signingKeysCmd is the operator lever for the two keys that rotate: the
// session signing key and the client assertion key. rotate stages a new key;
// the server promotes it once every replica holds it and retires the old one
// once everything it signed has expired, so the command reports those timings
// instead of waiting for them. retire takes one client assertion key out of
// service at once.
func signingKeysCmd(client func() *ctl.Client) *cobra.Command {
	keys := &cobra.Command{Use: "signing-keys", Short: "List, rotate and retire signing keys"}
	keys.AddCommand(&cobra.Command{
		Use:   "list",
		Short: "List every signing key with its purpose and status",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			rows, err := client().SigningKeys(cmd.Context())
			if err != nil {
				return err
			}
			tw := tabwriter.NewWriter(cmd.OutOrStdout(), 2, 4, 2, ' ', 0)
			fmt.Fprintln(tw, "KID\tPURPOSE\tSTATUS\tCREATED\tCHANGED")
			for _, k := range rows {
				changed := k.RotatedAt
				if changed == "" {
					changed = "-"
				}
				fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", k.KID, k.Purpose, k.Status, k.CreatedAt, changed)
			}
			return tw.Flush()
		},
	})
	keys.AddCommand(&cobra.Command{
		Use:       "rotate {session|client_assertion}",
		Short:     "Stage a new key of one purpose; the server promotes it once every replica holds it",
		ValidArgs: []string{purposeSession, purposeClientAssertion},
		Args: func(_ *cobra.Command, args []string) error {
			switch {
			case len(args) == 0:
				return fmt.Errorf("signing-keys rotate needs the purpose of the key to rotate: %s", rotateExample)
			case len(args) > 1:
				return fmt.Errorf("signing-keys rotate takes one operand, the purpose: %s", rotateExample)
			case args[0] != purposeSession && args[0] != purposeClientAssertion:
				return fmt.Errorf("%q is not a purpose this command rotates. Use %s", args[0], rotateExample)
			}
			return nil
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			if args[0] == purposeSession {
				return rotateSessionKey(cmd, client())
			}
			return rotateClientAssertionKey(cmd, client())
		},
	})
	keys.AddCommand(&cobra.Command{
		Use:   "retire <kid>",
		Short: "Retire one client assertion key at once, for a key that may have been copied",
		Args: func(_ *cobra.Command, args []string) error {
			if len(args) != 1 {
				return fmt.Errorf("signing-keys retire needs the id of one client assertion key. Run `strazactl signing-keys list` to see the ids")
			}
			return nil
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			return retireClientAssertionKey(cmd, client(), args[0])
		},
	})
	return keys
}

func rotateSessionKey(cmd *cobra.Command, c *ctl.Client) error {
	rot, err := c.RotateSigningKey(cmd.Context())
	if err != nil {
		return err
	}
	fmt.Fprintf(cmd.OutOrStdout(), "Staged a new session signing key %s.\n", rot.KID)
	fmt.Fprintf(cmd.OutOrStdout(),
		"It becomes the signing key in about %s, and the previous key stops verifying about %s after that.\n",
		roughDuration(rot.ActiveInSeconds), roughDuration(rot.PreviousKeyVerifiesForSeconds-rot.ActiveInSeconds))
	return nil
}

func rotateClientAssertionKey(cmd *cobra.Command, c *ctl.Client) error {
	rot, err := c.RotateClientAssertionKey(cmd.Context())
	if err != nil {
		return err
	}
	out := cmd.OutOrStdout()
	activeIn := roughDuration(rot.ActiveInSeconds)
	if rot.PreviousKID == "" {
		fmt.Fprintf(out, "Created a client assertion key %s. No other key signs client assertions.\n", rot.KID)
		fmt.Fprintf(out, "It is in the key document now and starts signing in about %s.\n", activeIn)
		fmt.Fprintf(out, "The key document is %s. Register it as the JWKS URL of each agent's client at your identity provider.\n", rot.JWKSURI)
		return nil
	}
	fmt.Fprintf(out, "Staged a new client assertion key %s.\n", rot.KID)
	fmt.Fprintf(out, "It is in the key document now and starts signing in about %s. The previous key %s leaves the key document about %s after that.\n",
		activeIn, rot.PreviousKID, roughDuration(rot.PreviousKeyVerifiesForSeconds-rot.ActiveInSeconds))
	return nil
}

func retireClientAssertionKey(cmd *cobra.Command, c *ctl.Client, kid string) error {
	ret, err := c.RetireClientAssertionKey(cmd.Context(), kid)
	if err != nil {
		return err
	}
	out := cmd.OutOrStdout()
	if ret.Was == "retired" {
		fmt.Fprintf(out, "The client assertion key %s was already retired. Nothing changed.\n", ret.KID)
		return nil
	}
	fmt.Fprintf(out, "Retired the client assertion key %s. It leaves the key document on every replica within %s.\n",
		ret.KID, roughDuration(ret.LeavesDocumentInSeconds))
	if ret.Was == "active" {
		fmt.Fprintf(out, "No key signs client assertions now. Run `strazactl signing-keys rotate client_assertion`, and the new key starts signing about %s later.\n",
			roughDuration(ret.NextKeyActiveInSeconds))
	}
	fmt.Fprintln(out, "Your identity provider may keep the retired key in its cache. If the key was copied, clear that cache or remove the JWKS URL from the agents' clients there.")
	return nil
}

// roughDuration renders a number of seconds in the largest whole unit an
// operator plans in, rounded: "1 minute", "6 minutes", "30 days".
func roughDuration(seconds int) string {
	d := time.Duration(seconds) * time.Second
	units := []struct {
		name string
		size time.Duration
	}{
		{"day", 24 * time.Hour}, {"hour", time.Hour}, {"minute", time.Minute}, {"second", time.Second},
	}
	for _, u := range units {
		if d < u.size && u.size != time.Second {
			continue
		}
		n := int(math.Round(float64(d) / float64(u.size)))
		if n == 1 {
			return "1 " + u.name
		}
		return fmt.Sprintf("%d %ss", n, u.name)
	}
	return "0 seconds"
}
