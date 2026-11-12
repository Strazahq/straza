package main

import (
	"crypto/x509"
	"encoding/pem"
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
// session signing key and the client assertion key. rotate stages a new key.
// The server promotes a session key once every replica holds it, while a
// client assertion key waits for promote, so an administrator can first give
// its certificate to an Entra agent blueprint. The server retires the old key
// once everything it signed has expired, so the commands report timings
// instead of waiting for them. retire takes one client assertion key out of
// service at once.
func signingKeysCmd(client func() *ctl.Client) *cobra.Command {
	keys := &cobra.Command{Use: "signing-keys", Short: "List, rotate, promote and retire signing keys, and print a client assertion key's certificate"}
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
			fmt.Fprintln(tw, "KID\tPURPOSE\tSTATUS\tCREATED\tCHANGED\tCERTIFICATE EXPIRES")
			for _, k := range rows {
				fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n", k.KID, k.Purpose, k.Status, k.CreatedAt, orDash(k.RotatedAt), orDash(k.CertificateExpiresAt))
			}
			return tw.Flush()
		},
	})
	keys.AddCommand(&cobra.Command{
		Use:       "rotate {session|client_assertion}",
		Short:     "Stage a new key of one purpose: a session key promotes itself, a client assertion key waits for promote",
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
	keys.AddCommand(&cobra.Command{
		Use:       "promote client_assertion <kid>",
		Short:     "Make a staged client assertion key the signing key",
		ValidArgs: []string{purposeClientAssertion},
		Args: purposeAndKID("promote",
			"A session key promotes itself after strazactl signing-keys rotate session, and only a client_assertion key is promoted by hand."),
		RunE: func(cmd *cobra.Command, args []string) error {
			return promoteClientAssertionKey(cmd, client(), args[1])
		},
	})
	keys.AddCommand(&cobra.Command{
		Use:       "certificate client_assertion <kid>",
		Short:     "Print the certificate of a client assertion key, to upload to an Entra agent blueprint",
		ValidArgs: []string{purposeClientAssertion},
		Args:      purposeAndKID("certificate", "Only a client_assertion key has one."),
		RunE: func(cmd *cobra.Command, args []string) error {
			return printClientAssertionCertificate(cmd, client(), args[1])
		},
	})
	return keys
}

// purposeAndKID checks the two operands of a command that acts on one client
// assertion key. why says why another purpose is refused.
func purposeAndKID(verb, why string) cobra.PositionalArgs {
	example := "Example: strazactl signing-keys " + verb + " client_assertion <kid>"
	return func(_ *cobra.Command, args []string) error {
		switch {
		case len(args) != 2:
			return fmt.Errorf("signing-keys %s needs the purpose and the id of one key. %s. Run `strazactl signing-keys list` to see the ids", verb, example)
		case args[0] != purposeClientAssertion:
			what := map[string]string{"promote": "promotes", "certificate": "serves a certificate for"}[verb]
			return fmt.Errorf("%q is not a purpose this command %s. %s %s", args[0], what, why, example)
		}
		return nil
	}
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
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
	promoteIn := roughDuration(rot.ActiveInSeconds)
	if rot.PreviousKID == "" {
		fmt.Fprintf(out, "Created a client assertion key %s. No other key signs client assertions.\n", rot.KID)
		fmt.Fprintf(out, "It is in the key document now and signs nothing until you promote it, which you can do in about %s.\n", promoteIn)
		fmt.Fprintf(out, "The key document is %s. Register it as the JWKS URL of each agent's client at your identity provider.\n", rot.JWKSURI)
		fmt.Fprintf(out, "If an Entra agent blueprint uses this key, upload its certificate there first, from `strazactl signing-keys certificate client_assertion %s`.\n", rot.KID)
	} else {
		fmt.Fprintf(out, "Staged a new client assertion key %s. The key %s keeps signing until you promote the new one.\n", rot.KID, rot.PreviousKID)
		fmt.Fprintf(out, "It is in the key document now, and you can promote it in about %s.\n", promoteIn)
		fmt.Fprintf(out, "If an Entra agent blueprint uses these keys, upload the new key's certificate there first, from `strazactl signing-keys certificate client_assertion %s`.\n", rot.KID)
	}
	fmt.Fprintf(out, "Then run `strazactl signing-keys promote client_assertion %s`.\n", rot.KID)
	return nil
}

func promoteClientAssertionKey(cmd *cobra.Command, c *ctl.Client, kid string) error {
	p, err := c.PromoteClientAssertionKey(cmd.Context(), kid)
	if err != nil {
		return err
	}
	out := cmd.OutOrStdout()
	if p.Was == "active" {
		fmt.Fprintf(out, "The client assertion key %s signs already. Nothing changed.\n", p.KID)
		return nil
	}
	fmt.Fprintf(out, "Promoted the client assertion key %s. This replica signs with it now, and every replica does within %s.\n",
		p.KID, roughDuration(p.EveryReplicaSignsInSeconds))
	if p.PreviousKID != "" {
		fmt.Fprintf(out, "The previous key %s leaves the key document in about %s. After that you can remove its certificate wherever you uploaded it.\n",
			p.PreviousKID, roughDuration(p.PreviousKeyLeavesDocumentInSeconds))
	}
	return nil
}

// printClientAssertionCertificate writes the PEM alone to stdout, so that a
// redirect saves a file the Entra admin centre takes, and the end of its
// validity to stderr.
func printClientAssertionCertificate(cmd *cobra.Command, c *ctl.Client, kid string) error {
	raw, err := c.ClientAssertionCertificate(cmd.Context(), kid)
	if err != nil {
		return err
	}
	block, _ := pem.Decode(raw)
	var cert *x509.Certificate
	if block != nil && block.Type == "CERTIFICATE" {
		cert, err = x509.ParseCertificate(block.Bytes)
	}
	if cert == nil || err != nil {
		return fmt.Errorf("strazad answered the certificate request for %s with something that is not a PEM certificate. "+
			"Check that strazactl and strazad run the same version, then run the command again", kid)
	}
	if _, err := cmd.OutOrStdout().Write(raw); err != nil {
		return err
	}
	fmt.Fprintf(cmd.ErrOrStderr(), "The certificate of client assertion key %s is valid until %s. Upload it to the Entra agent blueprint under Credentials, Certificates.\n",
		kid, cert.NotAfter.UTC().Format(time.DateOnly))
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
		fmt.Fprintf(out, "No key signs client assertions now. Run `strazactl signing-keys rotate client_assertion`, then about %s later `strazactl signing-keys promote client_assertion` with the new key's id.\n",
			roughDuration(ret.NextKeyActiveInSeconds))
	}
	fmt.Fprintln(out, "Your identity provider may keep the retired key in its cache. If the key was copied, clear that cache or remove the JWKS URL from the agents' clients there.")
	fmt.Fprintln(out, "If an Entra agent blueprint holds this key's certificate, remove it there now. Entra accepts the key until you do.")
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
