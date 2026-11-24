package main

import (
	"context"
	"errors"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/strazahq/straza/internal/config"
	"github.com/strazahq/straza/internal/server"
	"github.com/strazahq/straza/internal/store"
)

// schemaChecker is the one store method the break-glass command needs
// beyond the Store interface, declared here so that the interface does not
// grow for one command.
type schemaChecker interface {
	SchemaCurrent(ctx context.Context) error
}

// errUnreachable is the refusal of a database the command cannot open or
// reach.
var errUnreachable = errors.New("the command could not open or reach the database, so nothing changed. " +
	"Check that the database is running and reachable from this host and that its user and password are right, " +
	"and give the command the same --config, --profile, --data-dir and --store-dsn that strazad serve runs with")

// breakGlassCmd groups the commands an operator on the server host runs on
// the break-glass admin straight in the store.
func breakGlassCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "break-glass",
		Short: "Work on the break-glass admin from the server host",
	}
	cmd.AddCommand(breakGlassSetPasswordCmd())
	return cmd
}

// breakGlassSetPasswordCmd generates a new password for the break-glass
// admin of an enterprise store, stores its hash and prints it once on
// stdout, never through the logger. It refuses a standalone config, a store
// whose schema is not this binary's, and a store with no break-glass admin.
func breakGlassSetPasswordCmd() *cobra.Command {
	var flags configFlags
	cmd := &cobra.Command{
		Use:   "set-password",
		Short: "Give the break-glass admin a new generated password and print it once",
		Long: `Generate a new password for the break-glass admin, store its hash in the
database and print the password once on this terminal. It works on the
enterprise profile, where the break-glass admin starts with no password, and
it replaces a password the admin already has.

Run it on the server host when no admin can sign in to set the password over
the admin API, for example because the identity provider signs nobody in.
strazad serve stops at boot when it cannot reach the identity provider that
oidc.issuer names. In that case take oidc.issuer out of the settings first,
so that strazad starts with only the emergency sign-in, then run this
command. A running strazad reads the new password at the next emergency
sign-in, with no restart. Give the command the same --config, --profile,
--data-dir and --store-dsn that strazad serve runs with, so that it finds the
same database.`,
		Example: "  strazad break-glass set-password --config straza.yaml\n" +
			"  strazad break-glass set-password --profile enterprise",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := flags.load(cmd)
			if err != nil {
				return err
			}
			password, err := setBreakGlassPassword(cmd.Context(), cfg)
			if err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "New break-glass password: %s\n", password)
			fmt.Fprintln(cmd.OutOrStdout(), "It is shown only this once and signs in the account break-glass under Emergency sign-in on the console's sign-in card, so store it in your vault now.")
			return nil
		},
	}
	flags.bind(cmd)
	return cmd
}

// setBreakGlassPassword stores the hash of a new generated password on the
// break-glass admin of the store cfg names and answers the password. It
// changes nothing when it refuses.
func setBreakGlassPassword(ctx context.Context, cfg config.Config) (string, error) {
	if cfg.Profile != config.ProfileEnterprise {
		return "", errors.New("the settings this command read select the standalone profile, and the command works on the enterprise profile only, so nothing changed. " +
			"On standalone the first boot prints the break-glass password in the server log, and an admin sets a new one with `strazactl users set-password break-glass --password <new password>`. " +
			"For an enterprise server, give the command the same --config, --profile, --data-dir and --store-dsn that strazad serve runs with")
	}
	if cfg.Store.Driver == config.DriverSQLite {
		if err := sqliteExists(cfg.SQLitePath()); err != nil {
			return "", err
		}
	}
	st, err := store.Open(cfg)
	if err != nil {
		return "", errUnreachable
	}
	defer func() { _ = st.Close() }()
	// The driver's error is left out on purpose: a DSN that does not parse
	// comes back in it, the password masked but the rest of the DSN whole.
	if err := st.Ping(ctx); err != nil {
		return "", errUnreachable
	}
	checker, ok := st.(schemaChecker)
	if !ok {
		return "", fmt.Errorf("this strazad build cannot read the schema version of its %s store, so nothing changed. "+
			"Report it with the output of strazad version", cfg.Store.Driver)
	}
	if err := checker.SchemaCurrent(ctx); err != nil {
		return "", err
	}
	return server.SetBreakGlassPassword(ctx, st.Users())
}
