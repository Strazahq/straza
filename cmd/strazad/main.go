// Command strazad is the Straza platform server: identity plane,
// policy plane, MCP manager/gateway, and event spine in one static binary.
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/strazahq/straza/internal/config"
	"github.com/strazahq/straza/internal/logging"
	"github.com/strazahq/straza/internal/server"
	"github.com/strazahq/straza/internal/version"
)

func main() {
	if err := rootCmd().Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "strazad:", err)
		os.Exit(1)
	}
}

// devCommands holds the commands a build-tagged file adds to the root, such
// as gen-docs under the docsgen tag. A plain build leaves it empty.
var devCommands []*cobra.Command

func rootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:           "strazad",
		Short:         "Straza platform server",
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	root.AddCommand(serveCmd(), versionCmd(), migrateCmd())
	root.AddCommand(devCommands...)
	return root
}

// configFlags are the flags a strazad command resolves its config from:
// --config names the file, and every other flag the user set overrides the
// file and the environment. serve and migrate share them, so both commands
// find one store the same way.
type configFlags struct {
	path                                         string
	profile, dataDir, listen, logLevel, storeDSN string
}

// bind adds --config, --profile, --data-dir and --store-dsn to cmd.
func (f *configFlags) bind(cmd *cobra.Command) {
	fl := cmd.Flags()
	fl.StringVar(&f.path, "config", "", "config file path (default: $STRAZA_CONFIG, else ./straza.yaml when it exists)")
	fl.StringVar(&f.profile, "profile", "", "governance profile, standalone or enterprise (default standalone); it decides every other default")
	fl.StringVar(&f.dataDir, "data-dir", "", "local state directory (default data)")
	fl.StringVar(&f.storeDSN, "store-dsn", "", "store DSN: a SQLite file path or a PostgreSQL connection string (default: <data-dir>/straza.db on SQLite; PostgreSQL needs one here, in store.dsn or in STRAZA_STORE_DSN)")
}

// load loads the config: the file named by --config or $STRAZA_CONFIG, else
// ./straza.yaml when present, then the environment, then the flags the user
// set on cmd.
func (f *configFlags) load(cmd *cobra.Command) (config.Config, error) {
	set := func(name string, v *string) *string {
		if cmd.Flags().Changed(name) {
			return v
		}
		return nil
	}
	loader := config.Loader{FilePath: "straza.yaml", Flags: config.Overrides{
		Profile:  set("profile", &f.profile),
		DataDir:  set("data-dir", &f.dataDir),
		Listen:   set("listen", &f.listen),
		LogLevel: set("log-level", &f.logLevel),
		StoreDSN: set("store-dsn", &f.storeDSN),
	}}
	if f.path != "" {
		loader.FilePath, loader.ExplicitFile = f.path, true
	} else if env := os.Getenv("STRAZA_CONFIG"); env != "" {
		loader.FilePath, loader.ExplicitFile = env, true
	}
	return loader.Load()
}

func serveCmd() *cobra.Command {
	var flags configFlags
	cmd := &cobra.Command{
		Use:   "serve",
		Short: "Run the platform server",
		Long: "Reads the config file, then the STRAZA_ environment variables, then these flags,\n" +
			"each overriding the one before. The profile is chosen first, and it decides the\n" +
			"default of every other setting. The Configuration page lists every key, its\n" +
			"default when it has one, and its environment variable when it has one.",
		Example: "  strazad serve --profile standalone\n" +
			"  strazad serve --config straza.yaml",
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := flags.load(cmd)
			if err != nil {
				return err
			}
			log := logging.New(cfg.Log, os.Stderr)

			ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
			defer stop()

			app, err := server.New(ctx, cfg, log)
			if err != nil {
				return err
			}
			return app.Run(ctx)
		},
	}
	flags.bind(cmd)
	cmd.Flags().StringVar(&flags.listen, "listen", "", "HTTP listen address (default 127.0.0.1:8420 standalone, :8420 enterprise)")
	cmd.Flags().StringVar(&flags.logLevel, "log-level", "", "log level: debug, info, warn or error (default info)")
	return cmd
}

func versionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print version information",
		Run: func(*cobra.Command, []string) {
			v := version.Get()
			fmt.Printf("strazad %s (commit %s, %s, %s/%s)\n", v.Version, v.Commit, v.Go, v.OS, v.Arch)
		},
	}
}
