package main

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"

	"github.com/spf13/cobra"

	"github.com/strazahq/straza/internal/authn"
	"github.com/strazahq/straza/internal/config"
	"github.com/strazahq/straza/internal/secrets"
	"github.com/strazahq/straza/internal/store"
)

// resealCmd moves the signing keys in the database from the key-encryption
// key strazad runs with to a new one, without serving. It is the first step
// of replacing that key, because strazad refuses to start on signing keys it
// cannot open.
func resealCmd() *cobra.Command {
	var (
		flags   configFlags
		newFile string
	)
	cmd := &cobra.Command{
		Use:   "reseal-signing-keys --new-kek-file <path>",
		Short: "Seal the signing keys under a new key-encryption key",
		Long: `Open every signing key that strazad opens at start with the key-encryption
key strazad runs with, secret.key, and seal it with the key in --new-kek-file,
then exit without serving. Each key goes from one seal to the other in a
single write. A retired session or client assertion key stays as it is.

Run it when you replace secret.key. Stop every strazad replica first, write
32 random bytes to the new file, run this command, put the new file in place
of secret.key on every replica, and start strazad. Stored credentials stay
sealed under the old key, so set each one again after the start.

Give the command the same --config, --profile, --data-dir and --store-dsn
that strazad serve runs with, so that it finds the same database and key.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			const makeKey = "Write 32 random bytes to a new file, for example with head -c 32 /dev/urandom > secret.key.new, and name it"
			if newFile == "" {
				return errors.New("the command needs --new-kek-file <path>, the file with the new key-encryption key. " + makeKey)
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
			oldRaw, err := readKEK(cfg.KEKFile(), "the key-encryption key strazad runs with",
				"Give the command the same --config, --profile, --data-dir and --store-dsn that strazad serve runs with")
			if err != nil {
				return err
			}
			newRaw, err := readKEK(newFile, "the new key-encryption key", makeKey)
			if err != nil {
				return err
			}
			if bytes.Equal(oldRaw, newRaw) {
				return fmt.Errorf("the key in %s is the key in use, so nothing changed. %s", newFile, makeKey)
			}
			from, err := secrets.LoadOrCreateKEK(cfg.KEKFile())
			if err != nil {
				return fmt.Errorf("%w, so nothing changed", err)
			}
			to, err := secrets.LoadOrCreateKEK(newFile)
			if err != nil {
				return fmt.Errorf("%w, so nothing changed", err)
			}
			st, err := store.Open(cfg)
			if err != nil {
				return err
			}
			defer func() { _ = st.Close() }()
			n, err := authn.ResealKeys(cmd.Context(), st.SigningKeys(), from, to)
			if err != nil {
				return err
			}
			if n == 0 {
				fmt.Fprintf(cmd.OutOrStdout(), "No signing key in the database is sealed under the key in %s. Nothing changed.\n", cfg.KEKFile())
				return nil
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Sealed %d signing keys under the key in %s.\nPut that file in place of %s on every replica, then start strazad.\n",
				n, newFile, cfg.KEKFile())
			return nil
		},
	}
	flags.bind(cmd)
	cmd.Flags().StringVar(&newFile, "new-kek-file", "", "the file that holds the new 32-byte key-encryption key")
	return cmd
}

// readKEK reads a key-encryption key file for a comparison and refuses a
// missing one with what to do, so that the command never creates a key.
func readKEK(path, what, fix string) ([]byte, error) {
	raw, err := os.ReadFile(path) // #nosec G304 -- operator-named key path
	switch {
	case err == nil:
		return raw, nil
	case errors.Is(err, fs.ErrNotExist):
		return nil, fmt.Errorf("%s is expected in %s, and no file is there, so nothing changed. %s", what, path, fix)
	}
	return nil, fmt.Errorf("%s in %s cannot be read (%v), so nothing changed. %s", what, path, err, fix)
}
