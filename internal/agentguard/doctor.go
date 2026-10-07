package agentguard

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/strazahq/straza/internal/agentguard/spool"
	"github.com/strazahq/straza/internal/policy"
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

// Doctor runs every diagnostic against local state and the platform. It
// never mutates anything and always returns the full list: a failed early
// check still lets later ones report, because an operator debugging a deny
// wants the whole picture, not the first error.
func Doctor(ctx context.Context, store *Store) []Check {
	var out []Check

	cfg, err := store.LoadConfig()
	if err != nil {
		out = append(out, Check{"enrollment", checkFail,
			"no straza state found",
			"run `straza enroll --server <strazad url>`. The local checks below still ran; identity, server, session, snapshot and killswitch checks resume once enrolled"})
		// Un-enrolled is exactly the box whose local findings need surfacing:
		// a wiped config with live wiring fails every hook, and hiding the
		// wiring, binary, and audit checks behind this return would keep
		// that box dark. See localChecks' contract.
		local := localChecks(store, Session{}, false)
		for i := range local {
			// Un-enrolled, a spool backlog cannot upload at all: pointing at
			// "the server check above" (which did not run) would send the
			// operator past the actual blocker. Fail-soft: if the wording
			// drifted, the generic hint stands.
			if local[i].Name == "audit-spool" {
				local[i].Hint = strings.Replace(local[i].Hint,
					"a persistent backlog means uploads are failing. Check the server check above",
					"nothing can upload until this machine is enrolled", 1)
			}
		}
		return append(out, local...)
	}
	out = append(out, Check{"enrollment", checkOK,
		fmt.Sprintf("server %s, %d snapshot key(s) pinned, straza %s", cfg.ServerURL, len(cfg.SnapshotKeys), version.Version), ""})

	// Identity + the device credential. credentialDead records a verdict
	// the session and kill-switch lines below must not contradict: their
	// automatic-refresh hints assume a device credential that can still
	// start a session.
	credentialDead := false
	id, err := store.LoadIdentity()
	switch {
	case err != nil:
		credentialDead = true
		out = append(out, Check{"identity", checkFail, "no identity persisted",
			"run `straza enroll` again"})
	case id.Headless != "":
		// Deviceless by design: the local credential (NHI key or IdP
		// client secret) mints each session's token, so no device row and no
		// device credential exist to check.
		out = append(out, Check{"identity", checkOK,
			fmt.Sprintf("%s: headless AI agent enrollment (%s lane), sessions are deviceless", id.Username, id.Headless), ""})
	case id.DeviceToken == "":
		out = append(out, Check{"identity", checkWarn,
			fmt.Sprintf("%s (device %s): no device credential (enrolled before device credentials existed)", id.Username, id.DeviceID),
			"re-run `straza enroll`: new sessions currently depend on the short-lived login token"})
	default:
		exp, expErr := tokenExpiry(id.DeviceToken)
		switch {
		case expErr != nil:
			out = append(out, Check{"identity", checkWarn,
				fmt.Sprintf("%s (device %s): device credential unreadable", id.Username, id.DeviceID),
				"re-run `straza enroll`"})
		case time.Now().After(exp):
			credentialDead = true
			out = append(out, Check{"identity", checkFail,
				fmt.Sprintf("%s (device %s): device credential EXPIRED %s", id.Username, id.DeviceID, exp.Format(time.RFC3339)),
				"run `straza enroll` again"})
		case time.Until(exp) < 48*time.Hour:
			out = append(out, Check{"identity", checkWarn,
				fmt.Sprintf("%s (device %s): device credential expires %s", id.Username, id.DeviceID, exp.Format(time.RFC3339)),
				"re-enroll soon (`straza enroll`)"})
		default:
			out = append(out, Check{"identity", checkOK,
				fmt.Sprintf("%s (device %s), device credential valid until %s", id.Username, id.DeviceID, exp.Format("2006-01-02")), ""})
		}
	}

	// Server reachability + clock skew from the response Date header, plus,
	// when the server advertises a pinned approver surface (/version), the
	// facts a phone enrollment stands on.
	srv, approver := serverCheck(ctx, cfg.ServerURL)
	out = append(out, srv)
	if approver != nil {
		out = append(out, approverCheck(ctx, *approver))
	}

	// Session state.
	ses, sesErr := store.LoadSession()
	switch {
	case sesErr != nil:
		out = append(out, Check{"session", checkWarn, "no active session",
			"start a harness session (the SessionStart hook checks in), or pipe a SessionStart payload through `straza hook`"})
	case time.Now().After(ses.ExpiresAt) && credentialDead:
		// The re-acquire lane needs the device credential, so promising an
		// automatic refresh here would send the operator in circles.
		out = append(out, Check{"session", checkFail,
			fmt.Sprintf("%s (roles %v, attestation %s): token expired %s ago and cannot refresh", ses.SessionID, ses.Roles, ses.Attestation, time.Since(ses.ExpiresAt).Round(time.Second)),
			"the automatic refresh needs the device credential the identity check above found dead: run `straza enroll` again, then restart the harness session"})
	case time.Now().After(ses.ExpiresAt):
		out = append(out, Check{"session", checkWarn,
			fmt.Sprintf("%s (roles %v, attestation %s): token expired %s ago", ses.SessionID, ses.Roles, ses.Attestation, time.Since(ses.ExpiresAt).Round(time.Second)),
			"the next hook call refreshes it automatically; if it stays expired, restart the harness session"})
	default:
		out = append(out, Check{"session", checkOK,
			fmt.Sprintf("%s (roles %v, attestation %s, token valid %s)", ses.SessionID, ses.Roles, ses.Attestation, time.Until(ses.ExpiresAt).Round(time.Second)), ""})
	}

	// Cached snapshot: signature against pinned keys + age.
	out = append(out, snapshotCheck(store, cfg, ses, sesErr == nil))
	if c := policyRefusalCheck(store, cfg, ses, time.Now()); sesErr == nil && c != nil {
		out = append(out, *c)
	}

	// Managed wiring CONTENT vs the published artifact:
	// shape checks live in localChecks; this one needs the server + pinned
	// keys, so it runs only enrolled.
	if c := managedContentCheck(ctx, cfg); c != nil {
		out = append(out, *c)
	}

	// Everything that reads only this machine.
	out = append(out, localChecks(store, ses, sesErr == nil)...)

	// Kill-switch path: the transport this session's revocation rides, PROBED
	// rather than printed, and the daemon heartbeat that says someone is
	// actually subscribed to it (a verified lane nobody holds delivers nothing
	// sub-second; see heartbeat.go).
	if sesErr == nil {
		out = append(out, killswitchCheck(ctx, cfg.ServerURL, ses, srv.Status != checkFail, credentialDead,
			readDaemonLiveness(store.heartbeatPath(), time.Now())))
	}
	return out
}

// localChecks are the diagnostics that read only this machine: harness hook
// wiring and the binary it invokes, codex's trust gate and MCP registration,
// the audit spool, its drop marker, and the client error log.
//
// CONTRACT: every check here MUST run, and be truthful, on an un-enrolled
// box. The wiped-config-live-wiring machine (hooks firing and failing with
// no config to read) is exactly where these findings matter most,
// so none of them may hide behind enrollment. None may claim more without it
// either: ses/haveSession only ever serve as POSITIVE evidence
// (codexHooksCheck), so Session{}, false degrades to the honest warn, never to
// a fabricated green. A new check belongs here only if both directions hold.
func localChecks(store *Store, ses Session, haveSession bool) []Check {
	var out []Check

	// Harness hook wiring (user-mode and managed paths), then the binary those
	// registrations invoke: a path that moved since install leaves wiring that
	// reads perfect and runs nothing.
	out = append(out, wiringCheck())
	if c := hookBinaryCheck(); c != nil {
		out = append(out, *c)
	}

	// codex's hook lane has a trust gate wiringCheck cannot see, and debris
	// from the settings.json straza used to write.
	if c := codexHooksCheck(ses, haveSession); c != nil {
		out = append(out, *c)
	}

	// gemini's hook lane has three vendor-side ways to die silently while the
	// wiring file reads perfect: folder-trust safe mode, the hooksConfig kill
	// switch, the system-settings env redirect (geminitrust.go).
	if cwd, err := os.Getwd(); err == nil {
		if c := geminiHooksCheck(ses, haveSession, cwd); c != nil {
			out = append(out, *c)
		}
	}

	// codex's MCP registration lives in config.toml, outside every settings
	// file wiringCheck reads, so nothing above can see it.
	if c := codexMCPCheck(); c != nil {
		out = append(out, *c)
	}

	// Audit spool: where async audit data is right now.
	out = append(out, spoolCheck(store))

	// Audit the spool had to DROP: the marker enforceSpoolCap / the oversize
	// guard write is the only trace those records existed, and doctor is its
	// only reader (drops happen inside hook-invoked drains, where stderr is
	// harness-interpreted, so no safe drop-time surface exists).
	if c := droppedAuditCheck(store); c != nil {
		out = append(out, *c)
	}

	// Recent client errors (hook/spool/drain failures the error log recorded).
	// The same reasoning as the drop marker: failures happen where stderr is
	// harness-interpreted, so doctor and `straza logs` are the surfaces.
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

// snapshotCheck verifies the cached snapshot against the pinned keys and
// reports its age vs the offline-grace bound.
func snapshotCheck(store *Store, cfg Config, ses Session, haveSession bool) Check {
	signed, err := store.LoadSnapshot()
	if err != nil {
		return Check{"snapshot", checkWarn, "no cached snapshot",
			"start a harness session. SessionStart downloads and verifies the active snapshot"}
	}
	lookup, err := keyLookup(cfg.SnapshotKeys)
	if err != nil {
		return Check{"snapshot", checkFail, err.Error(),
			"run `straza enroll` again to pin the server's snapshot keys, or on a machine set up with " +
				"`straza install --managed` ask an administrator to run that install again with `--server <server-url>`, " +
				"because straza enroll does not change the keys a managed install pinned"}
	}
	wantID := ""
	if haveSession {
		wantID = ses.SnapshotID
	}
	_, snap, err := policy.OpenSnapshot(signed, wantID, lookup)
	if err != nil {
		return Check{"snapshot", checkFail, fmt.Sprintf("cached snapshot fails verification: %v", err),
			"hooks are denying (fail closed). Start a new session of the AI agent, which fetches a fresh snapshot. " +
				"If this line still fails after that, the server's snapshot keys changed: run `straza enroll` again, or on a machine set up with " +
				"`straza install --managed` ask an administrator to run that install again with `--server <server-url>`"}
	}
	age := time.Since(time.Unix(snap.CreatedUnix, 0)).Round(time.Second)
	if snap.MaxAgeSecs > 0 && age > time.Duration(snap.MaxAgeSecs)*time.Second {
		return Check{"snapshot", checkWarn,
			fmt.Sprintf("verified but stale: compiled %s ago, offline grace is %ds", age, snap.MaxAgeSecs),
			"if strazad is reachable the next session refreshes it; offline past grace, hooks deny"}
	}
	return Check{"snapshot", checkOK, fmt.Sprintf("verified against pinned keys (compiled %s ago)", age), ""}
}

// policyRefusalCheck reports an outstanding refusal of a newer policy
// (settlePolicy): its reason and when the hooks start or started to deny
// with it, the deadline plus the offline grace (policyDenyAt). The cached
// snapshot still verifies in that state, so the snapshot line alone would
// read fine. It returns nil when no refusal is outstanding.
func policyRefusalCheck(store *Store, cfg Config, ses Session, now time.Time) *Check {
	if ses.PolicyRefused == "" {
		return nil
	}
	from := policyDenyAt(store, cfg, ses)
	when := "Hooks deny every governed call from " + from.Local().Format(time.RFC3339) + "."
	if now.After(from) {
		when = "Hooks have denied every governed call since " + from.Local().Format(time.RFC3339) + "."
	}
	return &Check{"policy", checkFail,
		"the server holds a newer policy that straza could not fetch. " + ses.PolicyRefused + " " + when,
		"fix the cause the reason names; the first fetch that succeeds ends the denial, at the next renewal or session start"}
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

// spoolCheck is the audit "where is my data" surface: audit is async,
// so "no error" never meant "delivered". It reports the pending backlog (live
// spool + rotated audit-pending-* files) and the last successful drain: the
// mtime of the marker Spool.Drain stamps whenever it leaves the spool empty
// (nothing else persists drain success; the drained files are deleted). It
// degrades, never crashes: no spool is a healthy "no pending audit events",
// an unreadable one is reported with the path.
func spoolCheck(store *Store) Check {
	live := store.SpoolPath()
	dir := filepath.Dir(live)
	lastDrain := "no successful drain recorded yet"
	if fi, err := os.Stat(filepath.Join(dir, spool.LastDrainMarkerName)); err == nil {
		lastDrain = fmt.Sprintf("last successful drain %s ago", time.Since(fi.ModTime()).Round(time.Second))
	}
	parked, _ := filepath.Glob(filepath.Join(dir, "audit-pending-*.jsonl"))
	events, files, oversize := 0, 0, 0
	for _, p := range append([]string{live}, parked...) {
		// spool.ScanRecords, not spool.ReadRecords: the skipped count is records too large
		// for the upload chunk, which the backlog number cannot see. Reporting
		// only what uploads would under-count the spool right up until the next
		// drain discards them.
		recs, skipped, err := spool.ScanRecords(p) // missing file = no records, not an error
		if err != nil {
			return Check{"audit-spool", checkWarn,
				fmt.Sprintf("spool unreadable: %v", err),
				"fix permissions under " + dir + ". Spooled audit events cannot upload until the spool is readable"}
		}
		oversize += skipped
		if len(recs) > 0 {
			events += len(recs)
			files++
		}
	}
	doomed := ""
	if oversize > 0 {
		doomed = fmt.Sprintf(" + %d record(s) too large to upload (the next drain discards them and records it under audit-dropped)", oversize)
	}
	if events == 0 && oversize == 0 {
		return Check{"audit-spool", checkOK, "no pending audit events (" + lastDrain + ")", ""}
	}
	return Check{"audit-spool", checkWarn,
		fmt.Sprintf("%d audit event(s) in %d file(s) awaiting upload%s (%s)", events, files, doomed, lastDrain),
		"they upload on the next drain (after decisions, at session start/end, on daemon ticks, or `straza drain`); a persistent backlog means uploads are failing. Check the server check above. Records too large to upload never block the queue: they are dropped and surface in the audit-dropped check."}
}

// droppedAuditCheck surfaces the audit-dropped marker: records the spool cap
// or the oversize guard discarded before they ever reached the server. They
// are unrecoverable, and nothing else reads the marker.
//
// No marker = no line at all: loss is the exception, and a routine "nothing
// was lost" row would train operators to skim past the day it flips. A
// populated marker is a FAIL (gone audit evidence on a governance box is
// exactly what `doctor`'s exit-1 gate exists for) and stays red until the
// human acknowledges by deleting the marker (doctor itself never mutates).
// A marker that exists but holds nothing readable is a WARN: unknown loss,
// never silence. Parked compaction claims are read and summed alongside.
func droppedAuditCheck(store *Store) *Check {
	path := filepath.Join(filepath.Dir(store.SpoolPath()), spool.DroppedMarkerName)
	// Read the live marker AND any parked compaction claims: a crash between
	// compaction's rename and its summary append leaves the whole ledger in a
	// claim file until the next drop folds it back; doctor summing both
	// closes that window (claims first: their lines are the older half, and
	// lastLine should be the live marker's).
	claims, _ := filepath.Glob(path + spool.DroppedCompactingSuffix + "*")
	var raw []byte
	var sources []string
	for _, p := range append(claims, path) {
		b, err := os.ReadFile(p) // #nosec G304 -- our own marker
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return &Check{"audit-dropped", checkWarn,
				fmt.Sprintf("drop marker unreadable: %v", err),
				"fix " + p + ". It records audit events straza had to drop, and until it is readable the loss cannot be quantified"}
		}
		raw = append(raw, b...)
		raw = append(raw, '\n')
		sources = append(sources, p)
	}
	if len(sources) == 0 {
		return nil // no marker was ever written: nothing was recorded lost
	}
	ack := strings.Join(sources, " and ")
	st := spool.ParseDroppedMarker(raw)
	if st.Events == 0 {
		// The marker EXISTS with no readable drop line. Its only creator is
		// noteDropped, so this means a drop happened and even the note about
		// it failed (a full disk is exactly adjacent to why drops happen).
		// Unknown loss is loss: an empty marker must never read as "nothing
		// lost".
		return &Check{"audit-dropped", checkWarn,
			"drop marker " + path + " exists but holds no readable drop record. Audit was dropped and even the note about it failed (disk full when it was written?)",
			"an unknown amount of audit never reached the server. Investigate the gap, then delete " + ack + " to acknowledge"}
	}
	var what []string
	if st.Files > 0 {
		what = append(what, fmt.Sprintf("%d parked spool file(s)", st.Files))
	}
	if st.Records > 0 {
		what = append(what, fmt.Sprintf("%d oversize record(s)", st.Records))
	}
	if len(what) == 0 {
		what = append(what, "an unknown amount")
	}
	last := "time unknown"
	if !st.Last.IsZero() {
		last = st.Last.Format(time.RFC3339)
	}
	return &Check{"audit-dropped", checkFail,
		fmt.Sprintf("audit evidence DROPPED, unrecoverable: %s across %d drop event(s), last %s (%q)",
			strings.Join(what, " + "), st.Events, last, st.LastLine),
		"these records never reached the server: the agent was offline long enough for the spool cap to evict its oldest backlog, or single records outgrew the upload chunk. Investigate the gap (a failing server check above is the usual cause), then delete " + ack + " to acknowledge. This check stays red until the marker is removed"}
}

// tokenExpiry reads a JWT's exp claim WITHOUT verifying it: purely local,
// informational display; the server is the verifier.
func tokenExpiry(raw string) (time.Time, error) {
	parts := strings.Split(raw, ".")
	if len(parts) != 3 {
		return time.Time{}, fmt.Errorf("not a JWT")
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return time.Time{}, err
	}
	var claims struct {
		Exp float64 `json:"exp"`
	}
	if err := json.Unmarshal(payload, &claims); err != nil || claims.Exp == 0 {
		return time.Time{}, fmt.Errorf("no exp claim")
	}
	return time.Unix(int64(claims.Exp), 0), nil
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
