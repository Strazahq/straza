package main

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"

	"github.com/spf13/cobra"

	"github.com/strazahq/straza/internal/config"
	"github.com/strazahq/straza/internal/store"
)

// schemaMover is the one store method the migrate command needs, declared
// here so that the Store interface does not grow for one command.
type schemaMover interface {
	MigrateTo(ctx context.Context, version uint) (uint, error)
}

// migrateCmd moves the database schema to one migration version without
// serving. A rollback runs it with the newer binary, because only the newer
// binary carries the down steps of its own migrations.
func migrateCmd() *cobra.Command {
	var (
		flags configFlags
		to    uint
	)
	cmd := &cobra.Command{
		Use:   "migrate --to <version>",
		Short: "Move the database schema to one migration version",
		Long: `Move the database schema up or down to one migration version with the
migrations this strazad carries, then exit without serving. A move down goes
one migration per run, because each down step can drop tables and the rows
they hold.

A rollback to an older strazad release runs this command with the newer
binary first, since only the newer binary has the steps that undo its own
migrations, and then starts the older binary on the same database.

Stop every strazad replica first. The command cannot see other replicas,
and a replica that keeps serving writes to the tables the move changes.
Give the command the same --config, --profile, --data-dir and --store-dsn
that strazad serve runs with, so that it finds the same database.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if !cmd.Flags().Changed("to") {
				return errors.New("the command needs --to <version>, the migration to move the database to. " +
					"The upgrade notes of each strazad release name its newest migration")
			}
			cfg, err := flags.load(cmd)
			if err != nil {
				return err
			}
			if cfg.Store.Driver == config.DriverSQLite {
				if err := sqliteExists(cfg.SQLitePath()); err != nil {
					return err
				}
			}
			st, err := store.Open(cfg)
			if err != nil {
				return err
			}
			defer func() { _ = st.Close() }()
			mover, ok := st.(schemaMover)
			if !ok {
				return fmt.Errorf("this strazad build cannot move the schema of its %s store, so nothing changed. "+
					"Report it with the output of strazad version", cfg.Store.Driver)
			}
			n, err := mover.MigrateTo(cmd.Context(), to)
			switch {
			case errors.Is(err, store.ErrSchemaUnchanged):
				fmt.Fprintf(cmd.OutOrStdout(), "The database is at migration %d already. Nothing changed.\n", n)
			case err != nil:
				return err
			default:
				fmt.Fprintf(cmd.OutOrStdout(), "The database is at migration %d now. Start the strazad release whose newest migration is %d.\n", n, n)
			}
			return nil
		},
	}
	flags.bind(cmd)
	cmd.Flags().UintVar(&to, "to", 0, "the migration version to move the database to")
	return cmd
}

// sqliteExists refuses a sqlite database file that is missing or cannot be
// read. The store would create a missing one, and its directories, under a
// path that may be a Postgres DSN read by the sqlite driver, so the command
// refuses first and never names the path, which can carry a password.
func sqliteExists(path string) error {
	const fix = "Give the command the same --config, --profile, --data-dir and --store-dsn that strazad serve runs with"
	_, err := os.Stat(path)
	switch {
	case err == nil:
		return nil
	case errors.Is(err, fs.ErrNotExist):
		return errors.New("the store driver is sqlite, and no database file exists where the store settings point, so nothing moved. " + fix)
	}
	cause := err
	var pathErr *fs.PathError
	if errors.As(err, &pathErr) {
		cause = pathErr.Err
	}
	return fmt.Errorf("the store driver is sqlite, and the database file where the store settings point cannot be read (%v), so nothing moved. "+fix, cause)
}
