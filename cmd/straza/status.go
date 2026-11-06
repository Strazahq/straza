package main

import (
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/spf13/cobra"

	"github.com/strazahq/straza/internal/agentguard"
	"github.com/strazahq/straza/internal/agentguard/localapi"
)

// statusCmd shows the caller's enrollment, live sessions and audit spool as
// the Straza background service holds them.
func statusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show enrollment, sessions and the audit spool the background service holds for you",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			st, err := agentguard.NewServiceClient().Status(cmd.Context())
			if err != nil {
				return errors.New(agentguard.ServiceSentence(err))
			}
			printStatus(cmd.OutOrStdout(), st, time.Now())
			return nil
		},
	}
}

// printStatus renders the status rows: the service, the enrollment, one row
// per live session with its kill-switch push, and the audit spool. Windows
// unused for over agentguard.IdleWindowAfter share one row.
func printStatus(w io.Writer, st localapi.Status, now time.Time) {
	fmt.Fprintf(w, "service    straza service %s (custody %s)\n", st.Version, st.Custody)
	e := st.Enrollment
	if e == nil {
		fmt.Fprintln(w, "identity   not enrolled. "+agentguard.NotEnrolledSteps())
		return
	}
	fmt.Fprintf(w, "server     %s\n", e.ServerURL)
	fmt.Fprintln(w, identityLine(*e))
	switch {
	case e.MachineChanged:
		next := agentguard.PersonChangedSteps(*e)
		if e.Kind == localapi.KindAgent {
			next = agentguard.AgentChangedSteps(*e)
		}
		fmt.Fprintln(w, "warning    This machine's identity changed since it was enrolled, so Straza stopped using its key. "+next)
	case now.Before(e.WaitUntil):
		fmt.Fprintln(w, "warning    Straza is waiting for the server to confirm this machine's last check-in, which takes up to two minutes after a lost connection. Start the session again in a minute.")
	}
	if len(st.Sessions) == 0 {
		fmt.Fprintln(w, "session    none (start a harness session)")
	}
	idle := 0
	for _, s := range st.Sessions {
		if s.Revoked == "" && now.Sub(s.ExpiresAt) > agentguard.IdleWindowAfter {
			idle++
			continue
		}
		fmt.Fprintln(w, sessionLine(s, now, e.MachineChanged))
	}
	switch {
	case idle == 1:
		fmt.Fprintln(w, "session    1 window unused for over an hour (it checks in again at its next call)")
	case idle > 1:
		fmt.Fprintf(w, "session    %d windows unused for over an hour (each one checks in again at its next call)\n", idle)
	}
	fmt.Fprintln(w, spoolLine(st.Spool))
}

// identityLine renders the status row for the enrollment. An agent has no
// device, so its row says so instead of printing an empty device id.
func identityLine(e localapi.Enrollment) string {
	key := e.KeyThumbprint
	if len(key) > 12 {
		key = key[:12]
	}
	if e.Kind == localapi.KindAgent {
		return fmt.Sprintf("identity   %s (AI agent, no device, key %s)", e.Username, key)
	}
	return fmt.Sprintf("identity   %s (device %s, key %s)", e.Username, e.DeviceID, key)
}

// sessionLine renders one live session: its harness window, id, user, how
// long the token lasts or how long ago it lapsed, and whether the
// kill-switch push stream is up. A lapsed token renews at the next call,
// unless keyStopped says the service stopped using the machine's key.
func sessionLine(s localapi.SessionStatus, now time.Time, keyStopped bool) string {
	h := s.Harness.Name
	if s.Harness.Version != "" {
		h += "/" + s.Harness.Version
	}
	state := "expires in " + s.ExpiresAt.Sub(now).Round(time.Second).String() + ", kill switch " + s.Push
	if now.After(s.ExpiresAt) {
		renews := " and renews at the next call"
		if keyStopped {
			renews = ""
		}
		state = "token expired " + now.Sub(s.ExpiresAt).Round(time.Second).String() + " ago" + renews + ", kill switch " + s.Push
	}
	if s.Revoked != "" {
		state = "revoked: " + s.Revoked
	}
	return fmt.Sprintf("session    %s %s as %s (%s)", h, s.SessionID, s.User, state)
}

// spoolLine renders the audit backlog the service still has to upload.
func spoolLine(sp localapi.SpoolStatus) string {
	if sp.Pending == 0 {
		return "audit      nothing waiting to upload"
	}
	return fmt.Sprintf("audit      %d record(s) waiting to upload", sp.Pending)
}
