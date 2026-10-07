package connect

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"golang.org/x/term"
)

// tokenStdinMax bounds a piped token read: the server refuses anything over
// its own cap, so a larger read is a wrong file, not a token.
const tokenStdinMax = 16 << 10

// Options is one run of the connect or the disconnect verb. Tool is the
// command name printed in hints, straza or strazactl. For connect an empty
// Server lists the connections. User is another user a sponsor or an
// administrator acts for, and only strazactl sets it. A nil AllowAgents
// leaves the owner's opt-in alone.
type Options struct {
	Tool        string
	Server      string
	User        string
	Expires     string
	AllowAgents *bool
	In          *os.File
	Out         io.Writer
	Err         io.Writer
}

// Run carries out one connect command: list, change the agents opt-in, sign
// in, or paste a token, in that order of precedence. The token never comes
// from an argument, so it stays out of the shell history and the process
// list.
func Run(ctx context.Context, api API, o Options) error {
	if o.Server == "" {
		return printList(ctx, api, o.User, o.Out)
	}
	if o.AllowAgents != nil {
		if err := SetAllowAgents(ctx, api, o.Server, o.User, *o.AllowAgents); err != nil {
			return err
		}
		fmt.Fprintln(o.Out, agentsLine(o.Server, o.User, *o.AllowAgents))
		return nil
	}
	list, err := List(ctx, api, o.User)
	if err != nil {
		return err
	}
	status := statusOf(list, o.Server)
	if status == nil || status.Kind != "token" {
		if o.User != "" {
			return fmt.Errorf("%s uses sign-in, which needs %s's own browser, so nobody can connect on their behalf", o.Server, o.User)
		}
		return SignIn(ctx, api, o.Server, o.Tool, o.Out)
	}
	expiresAt, err := expiryRFC3339(o.Expires)
	if err != nil {
		return err
	}
	token, err := readToken(o.Server, o.Tool, o.In, o.Err)
	if err != nil {
		return err
	}
	res, err := PasteToken(ctx, api, o.Server, token, expiresAt, o.User)
	if err != nil {
		return err
	}
	fmt.Fprintln(o.Out, tokenLine(res))
	return nil
}

// Disconnect removes the acting user's connection on one server and prints
// the line that says so. With no server named it changes nothing and answers
// an error that names the servers the user is connected to.
func Disconnect(ctx context.Context, api API, o Options) error {
	if o.Server == "" {
		return missingServer(ctx, api, o)
	}
	if err := Remove(ctx, api, o.Server, o.User); err != nil {
		return err
	}
	fmt.Fprintf(o.Out, "disconnected %s\n", o.Server)
	return nil
}

// missingServer is the error for a disconnect with no operand: what is
// missing, the valid values, and the command to run next.
func missingServer(ctx context.Context, api API, o Options) error {
	list, err := List(ctx, api, o.User)
	if err != nil {
		return err
	}
	var connected []string
	for _, s := range list {
		if s.Connected {
			connected = append(connected, s.App)
		}
	}
	who, has := "You are", "You have"
	if o.User != "" {
		who, has = o.User+" is", o.User+" has"
	}
	if len(connected) == 0 {
		return fmt.Errorf("name the MCP server to disconnect from. %s no connections, and `%s connect` lists the servers", has, o.Tool)
	}
	next := o.Tool + " disconnect " + connected[0]
	if o.User != "" {
		next += " --user " + o.User
	}
	return fmt.Errorf("name the MCP server to disconnect from. %s connected to %s. Run `%s`",
		who, strings.Join(connected, ", "), next)
}

// printList renders the connection table for the acting user.
func printList(ctx context.Context, api API, user string, w io.Writer) error {
	list, err := List(ctx, api, user)
	if err != nil {
		return err
	}
	tw := tabwriter.NewWriter(w, 2, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "SERVER\tKIND\tPROVIDER\tCONNECTED\tFINGERPRINT\tEXPIRES\tAGENTS\tSET BY")
	for _, s := range list {
		expires := s.ExpiresAt
		if s.Connected && expires == "" {
			expires = "never"
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%v\t%s\t%s\t%s\t%s\n",
			s.App, s.Kind, dash(s.Provider), s.Connected, dash(s.Fingerprint), dash(expires), agentsCell(s), dash(s.SetBy))
	}
	return tw.Flush()
}

// agentsCell says what an agent without a row gets on the server, and
// whether this owner has allowed their agents on the connection.
func agentsCell(s Status) string {
	if !s.Connected {
		return s.Agents
	}
	if s.AllowAgents {
		return s.Agents + ", allowed"
	}
	return s.Agents + ", not allowed"
}

func dash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

// expiryRFC3339 turns the --expires date into the RFC 3339 instant the
// server stores: the start of that day in UTC, which fails closed on the
// date the provider shows.
func expiryRFC3339(date string) (string, error) {
	if date == "" {
		return "", nil
	}
	t, err := time.Parse("2006-01-02", date)
	if err != nil {
		return "", fmt.Errorf("--expires must be a date as YYYY-MM-DD, got %q", date)
	}
	return t.UTC().Format(time.RFC3339), nil
}

// readToken takes the token from a hidden prompt when stdin is a terminal,
// else from stdin whole.
func readToken(server, tool string, in *os.File, prompt io.Writer) (string, error) {
	var raw []byte
	var err error
	if term.IsTerminal(int(in.Fd())) {
		fmt.Fprintf(prompt, "Paste the token for %s (input hidden): ", server)
		raw, err = term.ReadPassword(int(in.Fd()))
		fmt.Fprintln(prompt)
	} else {
		raw, err = io.ReadAll(io.LimitReader(in, tokenStdinMax))
	}
	if err != nil {
		return "", fmt.Errorf("could not read the token: %w", err)
	}
	token := string(bytes.TrimSpace(raw))
	if token == "" {
		return "", fmt.Errorf("no token given. Paste it at the prompt, or pipe it on stdin: printf '%%s' \"$TOKEN\" | %s connect %s", tool, server)
	}
	return token, nil
}

// tokenLine is the one line printed after a paste.
func tokenLine(res TokenResult) string {
	var b strings.Builder
	fmt.Fprintf(&b, "connected %s for %s, token fingerprint %s", res.App, res.User, res.Fingerprint)
	if res.ExpiresAt != "" {
		fmt.Fprintf(&b, ", expires %s", strings.TrimSuffix(res.ExpiresAt, "T00:00:00Z"))
	}
	if res.AllowAgents {
		b.WriteString(", agents allowed")
	}
	return b.String()
}

// agentsLine is the one line printed after the opt-in changed.
func agentsLine(server, user string, allow bool) string {
	whose := "your"
	if user != "" {
		whose = user + "'s"
	}
	if allow {
		return fmt.Sprintf("agents may use %s %s connection where the server permits it", whose, server)
	}
	return fmt.Sprintf("agents may no longer use %s %s connection", whose, server)
}
