package main

import (
	"bufio"
	"fmt"
	"strings"

	"github.com/spf13/cobra"
)

// confirm prints the question and reads one line from the command's stdin,
// true on y or yes. A non-interactive run passes --yes instead of answering.
func confirm(cmd *cobra.Command, question string) bool {
	fmt.Fprint(cmd.OutOrStdout(), question+" [y/N] ")
	line, _ := bufio.NewReader(cmd.InOrStdin()).ReadString('\n')
	s := strings.ToLower(strings.TrimSpace(line))
	return s == "y" || s == "yes"
}

// errAborted is the answer to a declined confirm.
func errAborted() error {
	return fmt.Errorf("aborted (pass --yes for non-interactive runs)")
}
