package main

import (
	"errors"

	"github.com/spf13/cobra"
)

// errNoContact answers the retired drafts contact.
var errNoContact = errors.New("strazactl no longer contacts proposed servers, because it runs on an admin API token, which is not a person, " +
	"and a person contacts a proposed server. Contact it in the console under Drafts")

// draftsContactCmd is the hidden drafts contact. strazad refuses every
// contact on an admin API token, so it takes whatever the old verb took,
// sends nothing and exits 2 as a retired verb does. Hidden keeps it off the
// help, the generated pages and the skill.
func draftsContactCmd() *cobra.Command {
	return &cobra.Command{
		Use:                "contact",
		Hidden:             true,
		DisableFlagParsing: true,
		Annotations:        local,
		RunE: func(*cobra.Command, []string) error {
			return exitCodeErr{code: 2, err: errNoContact}
		},
	}
}
