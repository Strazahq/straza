package main

import (
	"errors"

	"github.com/spf13/cobra"
)

// errNoPublish answers the retired drafts publish.
var errNoPublish = errors.New("strazactl no longer publishes drafts, because it runs on an admin API token, which is not a person, " +
	"and a person publishes every draft. Publish it in the console under Drafts")

// draftsPublishCmd is the hidden drafts publish. strazad refuses every
// publish on an admin API token, so it takes whatever the old verb took,
// sends nothing and exits 2 as a retired verb does. Hidden keeps it off the
// help, the generated pages and the skill.
func draftsPublishCmd() *cobra.Command {
	return &cobra.Command{
		Use:                "publish",
		Hidden:             true,
		DisableFlagParsing: true,
		Annotations:        local,
		RunE: func(*cobra.Command, []string) error {
			return exitCodeErr{code: 2, err: errNoPublish}
		},
	}
}

// publishNext is the way to the publish of draft id at server: its page in
// the console, where a person publishes it, because an admin API token never
// does.
func publishNext(server, id string) string {
	return "Publish draft " + id + " in the console under Drafts: " + server + "/console/drafts/" + id
}
