package main

import (
	"errors"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/strazahq/straza/internal/ctl"
)

// logoutCmd is the other end of `login`: it revokes the session this machine
// holds and deletes the credentials file that holds it.
//
//   - The server half is best effort. An unreachable deployment, a login that
//     already expired, or (against a pre-0.41.0 server, whose only revoke
//     route is admin-gated) a caller without the admin role must not leave a
//     usable credential on disk, so the file goes either way and the failed
//     half is one loud stderr line naming what stays valid. Exit stays 0: what
//     `logout` promises, no credential on this machine, did happen.
//   - The target is not negotiable. The only session logout can end is the one
//     in the credentials file, so `--server`/`$STRAZA_SERVER` pointing
//     elsewhere is refused rather than firing a revoke at a deployment the
//     stored token means nothing to.
func logoutCmd(client func() *ctl.Client, target *ctl.Target) *cobra.Command {
	return &cobra.Command{
		Use:   "logout",
		Short: "End this machine's strazactl session: revoke it, then delete the stored credentials",
		Long: "Revokes the session recorded in the credentials file and deletes the file.\n\n" +
			"The revoke is self-scoped (the session your own token names), so it needs no\n" +
			"admin role; against an older server without that route it falls back to the\n" +
			"admin revoke. Either way it is best effort: an unreachable server, an expired\n" +
			"login or a fallback refusal is reported on stderr, and the local credentials\n" +
			"are deleted regardless. Logging out only ends this session: an enrolled\n" +
			"device is untouched, so `strazactl login` works again afterwards.\n\n" +
			"Logout never sends the admin API token in STRAZA_API_TOKEN. While that\n" +
			"variable is set, the token keeps working after the logout, and a line on\n" +
			"stderr says so.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			c := client()
			if target.Overridden() {
				return fmt.Errorf(
					"logout ends the session stored in %s, which belongs to %s, but %s targets %s"+
						"; drop the override to log out",
					c.CredsPath, target.LoginServer, target.Source, target.Server)
			}
			if c.APIToken != "" {
				// Logout ends the login alone, and the token keeps working in
				// this shell, so the operator reads that whatever else happens.
				defer fmt.Fprintln(cmd.ErrOrStderr(), logoutTokenNote)
			}

			res, err := c.Logout(cmd.Context())
			switch {
			case errors.Is(err, ctl.ErrNotLoggedIn):
				return fmt.Errorf("not logged in: no credentials at %s; nothing to log out of", res.Path)
			case err != nil:
				return err
			}

			// One logout can end TWO sessions (the stored one, plus whatever a
			// mid-logout re-establish minted), and can succeed for one and fail
			// for the other, so each outcome gets its own line and both name
			// the actual ids.
			if len(res.Revoked) > 0 {
				fmt.Fprintf(cmd.OutOrStdout(), "revoked %s at %s\n", sessionList(res.Revoked), c.Base)
			}
			switch {
			case len(res.Unrevoked) > 0:
				// Consequence first, cause last: the wrapped cause can run long
				// (a refresh chain, a dial error), and what the operator has to
				// act on is which session is still out there. "may still be" is
				// the honest tense: a refused revoke can also mean the row was
				// already closed.
				format := "warning: %s not revoked; it may still be valid at %s until it expires: %v\n"
				if len(res.Unrevoked) > 1 {
					format = "warning: %s not revoked; they may still be valid at %s until they expire: %v\n"
				}
				fmt.Fprintf(cmd.ErrOrStderr(), format, sessionList(res.Unrevoked), c.Base, res.RevokeErr)
			case res.RevokeErr != nil:
				fmt.Fprintf(cmd.ErrOrStderr(), "warning: nothing revoked server-side: %v\n", res.RevokeErr)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "deleted %s\n", res.Path)
			return nil
		},
	}
}

// sessionList renders one or several session ids as a phrase that reads in a
// sentence: "session ses-1", "sessions ses-1, ses-2".
func sessionList(ids []string) string {
	if len(ids) == 1 {
		return "session " + ids[0]
	}
	return "sessions " + strings.Join(ids, ", ")
}
