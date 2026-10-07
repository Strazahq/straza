package main

import (
	"bytes"
	"encoding/json"
	"fmt"

	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"

	"github.com/strazahq/straza/internal/ctl"
	"github.com/strazahq/straza/internal/manager"
)

// appsExportCmd is `strazactl apps export <app>`: the manifest strazad stored
// for a server, printed as the app.yaml document that apps install takes.
func appsExportCmd(client func() *ctl.Client) *cobra.Command {
	return &cobra.Command{
		Use:   "export <server>",
		Short: "Print a server's stored manifest as app.yaml on stdout, the document apps install takes",
		Long: "Prints the manifest strazad stored for the named server, by name or id, as the\n" +
			"app.yaml document that apps install takes, so a change starts from what the\n" +
			"server runs. Every environment value, every value and default in the server\n" +
			"block, and each value the secret scan reads as a secret print as [REDACTED].\n" +
			"In every address, a user part prints as [REDACTED], written %5BREDACTED%5D,\n" +
			"a query and a fragment print as an ellipsis after their marker, and a path\n" +
			"part that reads as a generated secret prints as [REDACTED]. apps install\n" +
			"keeps the stored value wherever a mask still stands at its place.\n" +
			"A file in the apps directory takes no mask, so\n" +
			"write each value there yourself. The document is the only output on stdout,\n" +
			"and it passes strazactl spec validate app as it is.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			apps, err := client().Apps(cmd.Context())
			if err != nil {
				return err
			}
			for _, a := range apps {
				if a.Name != args[0] && a.ID != args[0] {
					continue
				}
				doc, err := manifestYAML(a.Name, a.Manifest)
				if err != nil {
					return err
				}
				_, err = cmd.OutOrStdout().Write(doc)
				return err
			}
			return fmt.Errorf("no MCP server named %q. List them with strazactl apps list", args[0])
		},
	}
}

// manifestYAML renders a stored manifest, the JSON object the apps list
// carries, in the YAML form apps import writes. The decode refuses a field
// this build does not know, so an export from a newer strazad never drops
// one without a word.
func manifestYAML(name string, stored json.RawMessage) ([]byte, error) {
	if len(stored) == 0 || string(stored) == "null" {
		return nil, fmt.Errorf("strazad sent no manifest for the MCP server %s: its stored copy does not decode, or strazad is older than this strazactl. "+
			"Install the server again from its app.yaml, or upgrade strazad, then export again", name)
	}
	dec := json.NewDecoder(bytes.NewReader(stored))
	dec.DisallowUnknownFields()
	var m manager.Manifest
	if err := dec.Decode(&m); err != nil {
		return nil, fmt.Errorf("the stored manifest of the MCP server %s does not fit this strazactl (%v), and an export would drop what does not fit. "+
			"Use a strazactl of the same version as strazad, then export again", name, err)
	}
	return yaml.Marshal(m)
}
