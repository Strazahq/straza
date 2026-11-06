package agentguard

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/strazahq/straza/internal/agentguard/localapi"
	"github.com/strazahq/straza/internal/version"
)

// Check is one doctor finding. Status is ok, warn, or fail; Hint says
// what the human does about it and is empty only when nothing is wrong.
type Check struct {
	Name   string
	Status string // "ok" | "warn" | "fail"
	Detail string
	Hint   string
}

const (
	checkOK   = "ok"
	checkWarn = "warn"
	checkFail = "fail"
)

// Doctor runs every diagnostic against the Straza background service, the
// platform and this machine's own files. It never mutates anything and always
// returns the full list: a failed early check still lets later ones report,
// because an operator debugging a deny wants the whole picture, not the first
// error.
func Doctor(ctx context.Context, store *Store, svc *localapi.Client) []Check {
	st, err := svc.Status(ctx)
	if err != nil {
		// No service is exactly the box whose local findings need surfacing:
		// live wiring with nothing behind it fails every hook, so the wiring,
		// binary and error log checks never hide behind this one.
		return append([]Check{serviceDownCheck(err)}, localChecks(store, nil)...)
	}
	out := []Check{{"service", checkOK,
		fmt.Sprintf("straza service %s answers at %s and keeps keys in %s custody%s", st.Version, svc.Socket(), st.Custody, homeIgnored()), ""}}
	e := st.Enrollment
	if e == nil {
		out = append(out, Check{"enrollment", checkFail, "this machine isn't enrolled for you yet",
			NotEnrolledSteps() + " The local checks below still ran; the server, session and killswitch checks resume once enrolled"})
		return append(out, localChecks(store, nil)...)
	}
	out = append(out, enrollmentCheck(*e, time.Now()))

	// Server reachability + clock skew from the response Date header, plus,
	// when the server advertises a pinned approver surface (/version), the
	// facts a phone enrollment stands on.
	srv, approver := serverCheck(ctx, e.ServerURL)
	out = append(out, srv)
	if approver != nil {
		out = append(out, approverCheck(ctx, *approver))
	}

	out = append(out, sessionChecks(st.Sessions, time.Now())...)

	// Managed wiring CONTENT vs the published artifact: shape checks live in
	// localChecks; this one needs the server and the keys the managed
	// install pinned, so it runs only enrolled and only on a managed box.
	if cfg, err := store.LoadConfig(); err == nil {
		if c := managedContentCheck(ctx, cfg, st.Sessions); c != nil {
			out = append(out, *c)
		}
	}

	// Everything that reads only this machine.
	out = append(out, localChecks(store, st.Sessions)...)

	// The audit spool lives in the service, which uploads it.
	out = append(out, spoolCheck(st.Spool, time.Now()))
	if c := droppedAuditCheck(st.Spool); c != nil {
		out = append(out, *c)
	}

	// Kill-switch path: the push stream the service holds for each session.
	if c := killswitchCheck(st.Sessions, time.Now(), srv.Status != checkFail); c != nil {
		out = append(out, *c)
	}
	return out
}

// serviceDownCheck is the first line when the service did not answer: the
// service's own sentence for a refusal, else what failed and how to start it.
func serviceDownCheck(err error) Check {
	var refused *localapi.RefusalError
	if errors.As(err, &refused) {
		return Check{"service", checkFail, "the Straza background service refused the status request: " + refused.Msg, ""}
	}
	hint := "ask IT to reinstall Straza. On Linux, `systemctl status straza-service` shows why"
	switch osName() {
	case "windows":
		hint = "ask IT to reinstall Straza. " + windowsWhy
	case "darwin":
		hint = "ask IT to reinstall Straza. " + macWhy
	}
	if _, _, user := localapi.Paths(serviceHome()); user {
		hint = "start it with `straza service` in a shell that has the same STRAZA_HOME"
	}
	if goos := osName(); goos != "linux" && goos != "darwin" && goos != "windows" {
		hint = "`straza service` runs only on Linux, macOS and Windows in this version of Straza, so this machine cannot hold Straza sessions yet"
	}
	return Check{"service", checkFail, err.Error() + homeIgnored(),
		hint + ". The checks below that read only this machine still ran; the enrollment, server, session and killswitch checks resume once the service answers"}
}

// homeIgnored says, for the service row, that a set STRAZA_HOME was ignored
// because this machine has a system service or a managed install.
func homeIgnored() string {
	if os.Getenv("STRAZA_HOME") != "" && serviceHome() == "" {
		return ". STRAZA_HOME is ignored here, because this machine has a system service or a managed install"
	}
	return ""
}

// enrollmentCheck reports the caller's enrollment as the service holds it:
// who, on which server, with which key, and whether the key is still usable.
func enrollmentCheck(e localapi.Enrollment, now time.Time) Check {
	who := e.Username + " (device " + e.DeviceID + ")"
	if e.Kind == localapi.KindAgent {
		who = e.Username + " (AI agent, no device)"
	}
	detail := fmt.Sprintf("%s at %s, key %s, straza %s", who, e.ServerURL, shortThumb(e.KeyThumbprint), version.Version)
	switch {
	case e.MachineChanged:
		hint := PersonChangedSteps(e)
		if e.Kind == localapi.KindAgent {
			hint = AgentChangedSteps(e)
		}
		return Check{"enrollment", checkFail, detail + ": this machine's identity changed since it was enrolled, so Straza stopped using its key", hint}
	case now.Before(e.WaitUntil):
		return Check{"enrollment", checkWarn,
			detail + ": Straza is waiting for the server to confirm this machine's last check-in until " + e.WaitUntil.Local().Format(time.RFC3339),
			"new sessions fail until then. Start the session again after that time"}
	}
	return Check{"enrollment", checkOK, detail, ""}
}

// NotEnrolledSteps names how a person and how an AI agent enroll this
// machine, one sentence each. The service reports an agent key that keygen
// made as no enrollment too, so straza doctor and straza status cannot tell
// which one applies. The enroll commands leave out --server when the managed
// configuration names the server, because the service enrolls there then.
func NotEnrolledSteps() string {
	server := enrollServerFlag("")
	return "A person runs `straza enroll" + server + "` and signs in with their work account. " +
		"An AI agent runs `straza enroll" + server + " --headless` with its registered key or the client secret IT installed for it."
}

// PersonChangedSteps says how the person of e enrolls this machine again
// after its identity changed, at the server e names. The enroll command
// leaves out --server when the managed configuration names the server.
func PersonChangedSteps(e localapi.Enrollment) string {
	return "Run `straza enroll" + enrollServerFlag(e.ServerURL) + "` to enroll it again."
}

// AgentChangedSteps says how the AI agent of e enrolls again after this
// machine's identity changed. The service's status does not say which lane
// the agent signs in on, so it names both: a new key from keygen on the key
// lane, because the service does not reuse a key whose machine changed, and
// a headless enrollment alone on the client-secret lane.
func AgentChangedSteps(e localapi.Enrollment) string {
	user := e.Username
	if user == "" {
		user = "<agent username>"
	}
	server := enrollServerFlag(e.ServerURL)
	return fmt.Sprintf("An agent with a key from `straza keygen` runs `straza keygen --force --user %[1]s`, "+
		"has an admin register the new public key, and then runs `straza enroll%[2]s --headless`. "+
		"An agent that signs in with a client secret runs `straza enroll%[2]s --headless --user %[1]s`.", user, server)
}

// enrollServerFlag is the --server flag of an enroll command these steps
// name: none when the managed configuration names the server, else server,
// or a placeholder when server is empty.
func enrollServerFlag(server string) string {
	switch {
	case managedServer() != "":
		return ""
	case server == "":
		return " --server <strazad url>"
	}
	return " --server " + server
}

// shortThumb shortens a key thumbprint for display.
func shortThumb(t string) string {
	if len(t) > 12 {
		return t[:12]
	}
	return t
}

// sessionChecks reports each live session the service holds for the caller,
// one row per harness window. A session whose newer policy the server
// refused to send reports as a policy row, because its hooks deny once the
// session time held at the refusal runs out.
func sessionChecks(sessions []localapi.SessionStatus, now time.Time) []Check {
	if len(sessions) == 0 {
		return []Check{{"session", checkWarn, "no live session",
			"start a harness session (its SessionStart hook starts one through the Straza background service), or pipe a SessionStart payload through `straza hook`"}}
	}
	var out []Check
	var idle, idleRevoked int
	for _, s := range sessions {
		detail := fmt.Sprintf("%s %s as %s", harnessName(s.Harness), s.SessionID, s.User)
		switch {
		case s.Revoked != "" && now.Sub(s.ExpiresAt) > IdleWindowAfter:
			idleRevoked++
		case now.Sub(s.ExpiresAt) > IdleWindowAfter:
			idle++
		case s.Revoked != "":
			out = append(out, Check{"session", checkFail, detail + ": revoked (" + s.Revoked + ")",
				"tool calls in that window stay denied. Start a new session; if that is refused too, your user or this machine is disabled, so contact your administrator"})
		case s.PolicyRefused != "":
			out = append(out, Check{"policy", checkFail,
				detail + ": the server holds a newer policy that straza could not fetch. " + s.PolicyRefused,
				"fix the cause the reason names; the first fetch that succeeds ends the denial, at the next renewal or session start"})
		case now.After(s.ExpiresAt):
			out = append(out, Check{"session", checkWarn,
				fmt.Sprintf("%s: token expired %s ago", detail, now.Sub(s.ExpiresAt).Round(time.Second)),
				"the next hook call renews it automatically; if it stays expired, restart the harness session"})
		default:
			out = append(out, Check{"session", checkOK,
				fmt.Sprintf("%s (token valid %s)", detail, s.ExpiresAt.Sub(now).Round(time.Second)), ""})
		}
	}
	switch {
	case idle == 1:
		out = append(out, Check{"session", checkOK, "1 window unused for over an hour. It checks in again at its next call", ""})
	case idle > 1:
		out = append(out, Check{"session", checkOK,
			fmt.Sprintf("%d windows unused for over an hour. Each one checks in again at its next call", idle), ""})
	}
	revokedHint := "start a new session in that harness; if that is refused too, your user or this machine is disabled, so contact your administrator"
	switch {
	case idleRevoked == 1:
		out = append(out, Check{"session", checkWarn,
			"1 revoked window unused for over an hour. Its tool calls stay denied until its harness starts a new session", revokedHint})
	case idleRevoked > 1:
		out = append(out, Check{"session", checkWarn, fmt.Sprintf("%d revoked windows unused for over an hour. "+
			"Their tool calls stay denied until their harness starts a new session", idleRevoked), revokedHint})
	}
	return out
}

// IdleWindowAfter is how long past its token's expiry a window keeps a row
// of its own in doctor and status. The service keeps a window for days, and
// older ones sit unused, so they share one row.
const IdleWindowAfter = time.Hour

// harnessName renders a harness as name/version, or the bare name.
func harnessName(h localapi.Harness) string {
	if h.Version == "" {
		return h.Name
	}
	return h.Name + "/" + h.Version
}

// checkedIn returns the label of a live session of the named harness, the
// outside evidence that its SessionStart hook ran.
func checkedIn(sessions []localapi.SessionStatus, name string) (string, bool) {
	for _, s := range sessions {
		if s.Harness.Name == name {
			return harnessName(s.Harness), true
		}
	}
	return "", false
}

// localChecks are the diagnostics that read only this machine: harness hook
// wiring and the binary it invokes, codex's trust gate and MCP registration,
// and the client error log.
//
// CONTRACT: every check here MUST run, and be truthful, with no service and
// no enrollment. The wiped-state-live-wiring machine (hooks firing and
// failing with nothing behind them) is exactly where these findings matter
// most, so none of them may hide behind the service. None may claim more
// without it either: sessions only ever serve as POSITIVE evidence
// (codexHooksCheck), so nil degrades to the honest warn, never to a
// fabricated green. A new check belongs here only if both directions hold.
func localChecks(store *Store, sessions []localapi.SessionStatus) []Check {
	var out []Check

	// Harness hook wiring (user-mode and managed paths), then the binary those
	// registrations invoke: a path that moved since install leaves wiring that
	// reads perfect and runs nothing.
	out = append(out, wiringCheck())
	if c := hookBinaryCheck(); c != nil {
		out = append(out, *c)
	}
	// The service ignores a managed config that non-administrators can
	// change, so only this check tells the admin why.
	if err := ManagedRootUntrusted(); err != nil {
		out = append(out, Check{"managed layout", checkFail, err.Error(), ""})
	}

	// codex's hook lane has a trust gate wiringCheck cannot see, and debris
	// from the settings.json straza used to write.
	if c := codexHooksCheck(sessions); c != nil {
		out = append(out, *c)
	}

	// gemini's hook lane has three vendor-side ways to die silently while the
	// wiring file reads perfect: folder-trust safe mode, the hooksConfig kill
	// switch, the system-settings env redirect (geminitrust.go).
	if cwd, err := os.Getwd(); err == nil {
		if c := geminiHooksCheck(sessions, cwd); c != nil {
			out = append(out, *c)
		}
	}

	// codex's MCP registration lives in config.toml, outside every settings
	// file wiringCheck reads, so nothing above can see it.
	if c := codexMCPCheck(); c != nil {
		out = append(out, *c)
	}

	// Credential files an older client left in the straza home. Nothing
	// reads them, but a copied one still works until it is revoked.
	out = append(out, oldCredentialChecks(store)...)

	// Recent client errors (hook and capture failures the error log
	// recorded). Failures happen where stderr is harness-interpreted, so
	// doctor and `straza logs` are the surfaces.
	if c := clientErrorsCheck(store, time.Now()); c != nil {
		out = append(out, *c)
	}

	// The decision journal and debug trace (tracelog.go): what was decided
	// lately, whether a debug window is open or has expired, or whether the
	// install config switched the surface off. Always one row: the surface
	// exists on every box, enrolled or not, and the row tells a human where
	// `straza trace show` will look.
	out = append(out, traceCheck(store, time.Now()))
	return out
}

// oldCredentialChecks reports each credential file a Straza client before
// the background service wrote to the user's straza home, one FAIL per
// file: an agent key or token in it outlives the client that wrote it.
func oldCredentialChecks(store *Store) []Check {
	var out []Check
	for _, name := range []string{"identity.json", "session.json", "nhi-key.json"} {
		path := store.statePath(name)
		if _, err := os.Lstat(path); err != nil {
			continue
		}
		out = append(out, Check{"old-credential", checkFail,
			"this machine still holds a credential file from an older Straza client at " + path,
			"delete it, and ask your Straza admin to register this agent's new key with `strazactl users nhi-key set` and rotate any client secret it held"})
	}
	return out
}

// codexMCPCheck reports the state of codex's MCP registration: the half of
// the install that hooks cannot show. Older installs only PRINTED this
// registration for the operator to paste, so "wired but no MCP server"
// was both common and invisible: the harness was governed while the agent had
// no route to straza's own tools.
//
// Returns nil (no line at all) for the ordinary case of a machine that simply
// does not use codex: unwired, with nothing registered. A registration the
// user wrote themselves reports ok: nothing is broken and straza deliberately
// yields to it, and a doctor line with no action attached is noise.
func codexMCPCheck() *Check {
	path, err := CodexMCPConfigPath()
	if err != nil {
		return nil
	}
	state, command, err := ReadCodexMCPRegistration(path)
	if err != nil {
		return &Check{"codex-mcp", checkWarn, err.Error(),
			"repair " + path + " by hand, then re-run `straza install codex`"}
	}
	switch state {
	case CodexMCPManaged:
		detail := "straza-managed block in " + path
		if command != "" {
			detail += " runs " + command
			if _, statErr := os.Stat(command); statErr != nil {
				return &Check{"codex-mcp", checkWarn, detail + ", which is not there",
					"the binary moved since install; re-run `straza install codex` to rewrite the block (codex fails the MCP server's startup silently)"}
			}
		}
		return &Check{"codex-mcp", checkOK, detail, ""}
	case CodexMCPUnmanaged:
		return &Check{"codex-mcp", checkOK,
			"an MCP registration straza did not write, in " + path + " (left alone)", ""}
	}
	if layer, _ := wiredLayer("codex"); layer == "" {
		return nil // codex is not in use here
	}
	return &Check{"codex-mcp", checkWarn, "no straza MCP server registered in " + path,
		"codex hooks are wired but the agent has no route to straza's own tools. Re-run `straza install codex`, which writes the registration (older installs only printed it)."}
}

// spoolCheck is the audit "where is my data" surface: audit is async, so "no
// error" never meant "delivered". It reports the backlog the service holds
// for the caller, its last successful upload and the last upload error.
func spoolCheck(sp localapi.SpoolStatus, now time.Time) Check {
	lastDrain := "no successful upload recorded yet"
	if !sp.LastDrain.IsZero() {
		lastDrain = fmt.Sprintf("last successful upload %s ago", now.Sub(sp.LastDrain).Round(time.Second))
	}
	if sp.Pending == 0 {
		return Check{"audit-spool", checkOK, "no pending audit events (" + lastDrain + ")", ""}
	}
	detail := fmt.Sprintf("%d audit event(s) awaiting upload (%s)", sp.Pending, lastDrain)
	if sp.LastError != "" {
		detail += ". The last upload failed: " + sp.LastError
	}
	return Check{"audit-spool", checkWarn, detail,
		"the Straza background service uploads them on its own; a persistent backlog means uploads are failing. Check the server check above"}
}

// droppedAuditCheck surfaces audit records the service's spool cap or
// oversize guard discarded before they ever reached the server. They are
// unrecoverable. No drop = no line at all: loss is the exception, and a
// routine "nothing was lost" row would train operators to skim past the day
// it flips. A drop is a FAIL, because gone audit evidence on a governance box
// is exactly what doctor's exit-1 gate exists for. It stays red until the
// service's next upload reported the loss to strazad.
func droppedAuditCheck(sp localapi.SpoolStatus) *Check {
	if sp.Dropped == 0 {
		return nil
	}
	return &Check{"audit-dropped", checkFail,
		fmt.Sprintf("audit evidence DROPPED, unrecoverable: %d record(s) never reached the server", sp.Dropped),
		"uploads failed long enough for the spool cap to evict the oldest unsent records, single records outgrew the upload chunk, " +
			"or the service refused calls because this account had too many running at once. " +
			"Investigate the gap with your Straza administrator (a failing server check above is the usual cause). " +
			"The Straza background service reports this loss to strazad with its next upload, and this check clears once strazad took it"}
}

// denyHints is the hint catalog: every stable deny/failure reason straza
// can emit, mapped to the action that fixes it. HintFor matches by
// substring; `straza doctor` is the entry point the messages advertise.
var denyHints = []struct{ Reason, Hint string }{
	{"not enrolled", "run `straza enroll --server <strazad url>`"},
	{"no active Straza session", "restart the harness session so straza can check in; `straza doctor` verifies the chain"},
	{"state unavailable", "the straza home is unreadable. Check STRAZA_HOME and permissions; `straza doctor`"},
	{"checkin failed", "run `straza doctor`: server unreachable, dead credential, or revoked principal"},
	{"device credential rejected", "run `straza enroll` again (credential expired or keys rotated)"},
	{"session has been revoked", "an operator revoked this session; start a new one. If that is refused, your user or device is disabled"},
	{"device or user has been revoked", "an administrator revoked this device or your user, so a new session is refused too. Contact your administrator"},
	{"user is disabled", "contact your administrator (identity deactivated in the identity manager)"},
	{"attestation level", "reinstall with `straza install --managed --server <server-url> <harness>` so the wiring matches what the server published"},
	{"refusing unverified snapshot", "snapshot signature does not match pinned keys. Re-enroll; if unexpected, treat as tampering and tell your admin"},
	{"snapshot fetch failed", "strazad unreachable. Hooks deny once offline grace ends; `straza doctor` checks connectivity"},
	{"blocked by policy", "the deny reason names the rule; policy questions go to whoever owns the PolicySet"},
}

// HintFor returns the catalog hint for a deny/failure reason, or "".
func HintFor(reason string) string {
	for _, h := range denyHints {
		if strings.Contains(reason, h.Reason) {
			return h.Hint
		}
	}
	return ""
}
