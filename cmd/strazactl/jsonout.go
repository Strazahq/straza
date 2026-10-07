package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"

	"github.com/spf13/cobra"

	"github.com/strazahq/straza/internal/ctl"
)

// jsonFlagUsage is the help line of every --json flag.
const jsonFlagUsage = "print the server's JSON answer, indented, and nothing else on stdout"

// printServerJSON writes the server's answer to w, indented, with no field
// added or dropped, so pkg/api/openapi.yaml stays the one schema of the
// output. An answer that is not JSON is refused, never printed.
func printServerJSON(w io.Writer, raw []byte) error {
	var buf bytes.Buffer
	if err := json.Indent(&buf, bytes.TrimSpace(raw), "", "  "); err != nil {
		return fmt.Errorf("the server's answer is not JSON (%v), so --json has nothing to print. "+
			"Check that --server or your login points at strazad, or run the command without --json", err)
	}
	buf.WriteByte('\n')
	_, err := w.Write(buf.Bytes())
	return err
}

// printToolsJSON is apps tools --json: the tools list as the server answered
// it, narrowed to one server's entries when app is set. The sentence the
// table prints for a server with no tools goes to stderr, so stdout stays
// JSON.
func printToolsJSON(cmd *cobra.Command, c *ctl.Client, app string) error {
	raw, err := c.ToolsJSON(cmd.Context())
	if err != nil {
		return err
	}
	if app != "" {
		if raw, err = toolsOfApp(raw, app); err != nil {
			return err
		}
		if string(raw) == "[]" {
			fmt.Fprintln(cmd.ErrOrStderr(), noToolsFor(app))
		}
	}
	return printServerJSON(cmd.OutOrStdout(), raw)
}

// toolsOfApp keeps the entries of the tools list whose app is name, each one
// exactly as the server wrote it.
func toolsOfApp(raw []byte, name string) ([]byte, error) {
	var all []json.RawMessage
	if err := json.Unmarshal(raw, &all); err != nil {
		return nil, fmt.Errorf("the server's tools list is not a JSON array (%v). Check that --server or your login points at strazad", err)
	}
	kept := make([][]byte, 0, len(all))
	for _, entry := range all {
		var tool struct {
			App string `json:"app"`
		}
		if err := json.Unmarshal(entry, &tool); err != nil {
			return nil, fmt.Errorf("an entry of the server's tools list is not a JSON object (%v). Check that --server or your login points at strazad", err)
		}
		if tool.App == name {
			kept = append(kept, entry)
		}
	}
	out := append([]byte("["), bytes.Join(kept, []byte(","))...)
	return append(out, ']'), nil
}
