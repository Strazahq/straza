package agentguard

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/strazahq/straza/internal/agentguard/spool"
	"github.com/strazahq/straza/internal/agentguard/trace"
	"github.com/strazahq/straza/internal/policy"
)

// liveDecider adapts a fully-wired straza (state + PDP) to the hook
// dispatch. It is constructed lazily per event kind: session.start does a
// network checkin; tool.pre loads the local snapshot PDP.
type liveDecider struct {
	store *Store
}

// Decide builds the local PDP, fetching the policy snapshot first when a live
// session has no verifiable one on disk, refreshes a stale token, evaluates,
// and spools the decision for async audit. Fail-closed on any error.
// session.end drains the spool. Observational events are audited but always
// allow.
func (d liveDecider) Decide(n Normalized) policy.Decision {
	start := time.Now()
	tl := d.store.Trace()
	ses, err := d.store.LoadSession()
	if err != nil {
		var dec policy.Decision
		if rev, rerr := d.store.LoadRevocation(); rerr == nil && rev.Reason != "" {
			dec = failClosed("session revoked (" + rev.Reason + "). Tool calls stay denied until a new session " +
				"starts and checks in again. If only this session was revoked, that check-in starts a new session. " +
				"If the device or user was disabled, the check-in is refused until an administrator re-enables it. " +
				"Inform the user and stop.")
		} else {
			dec = failClosed("no active Straza session. Restart the session so straza can check in")
		}
		journalDecision(tl, n, dec, decisionFacts{start: start, failClosed: true})
		return dec
	}

	// Capture points are a SET, not a single event: the root turn ends at
	// session.end, a delegate's at subagent.stop (its reply lives in its own
	// transcript, which no other event ever names). Both are capture/audit
	// boundaries, not PEPs: enforcement for delegated work stays at tool.pre
	// on the spawn tool and inside the delegate's own tool calls.
	if n.Event.Kind == policy.EventSessionEnd || n.Event.Kind == policy.EventSubagentStop {
		captured := d.captureReply(ses, n)
		if n.Event.Kind == policy.EventSessionEnd {
			d.drainSpool(ses)
		} else if captured {
			// Mid-session boundary: a detached drain, never a blocking one
			// (the hook budget is for the harness, not for audit I/O).
			drainSpawner()
		}
		dec := policy.Decision{Effect: policy.EffectAllow, Default: true}
		journalDecision(tl, n, dec, decisionFacts{start: start, snapshot: ses.SnapshotID})
		return dec
	}

	pdp, err := NewLocalPDP(d.store, ses.Subject())
	if err != nil {
		pdp, err = d.pdpAfterFetch(ses, err)
	}
	if err != nil {
		dec := failClosed(err.Error())
		journalDecision(tl, n, dec, decisionFacts{start: start, snapshot: ses.SnapshotID, failClosed: true})
		return dec
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	pdp.RefreshIfStale(ctx, d.store)
	cancel()
	if tl.DebugOn() {
		ps := pdp.Session()
		tl.Debug("session",
			slog.String("session", trace.Short(ps.SessionID, 64)),
			slog.String("snapshot", trace.Short(ps.SnapshotID, 96)),
			slog.Int64("expires_in_s", int64(time.Until(ps.ExpiresAt).Seconds())))
	}

	decision := pdp.Decide(n)
	facts := decisionFacts{start: start, snapshot: pdp.Session().SnapshotID}
	// Spool every locally decided governed decision for audit, then kick a
	// detached drain so it reaches the server within seconds even without a
	// daemon; the hook itself never waits on audit I/O. A verdict
	// adopted from /v1/decide is audited by the server that made it, so the
	// client records nothing for it: one decision, one record.
	if n.Event.Kind == policy.EventToolPre || n.Event.Kind == policy.EventPermissionRequest {
		if !pdp.serverRecorded {
			serr := spoolAppend(spool.NewSpool(d.store.SpoolPath()), n, pdp.Session().SessionID, pdp.Session().SnapshotID, decision)
			d.store.logSpoolError(n, serr)
			facts.spooled, facts.spoolErr = true, serr
			if tl.DebugOn() {
				tl.Debug("spool", slog.String("outcome", spoolOutcome(serr)))
			}
		}
		drainSpawner()
	}
	// The journal line for this decision: what the PDP decided, which
	// escalation lane ran and what the server answered (status + correlation
	// id), how long it took, whether the audit record reached the spool.
	// Content-free by construction (journal.go); never on the blocking path.
	pf := pdp.traceFacts()
	facts.escalation, facts.status, facts.correlation, facts.failClosed = pf.branch, pf.status, pf.correlation, pf.failClosed
	journalDecision(tl, n, decision, facts)
	if tl.DebugOn() && pf.branch != "" {
		tl.Debug("escalate",
			slog.String("branch", pf.branch), slog.Int("status", pf.status),
			slog.String("correlation", pf.correlation), slog.Bool("bounced", pf.bounced),
			slog.Bool("classified", pf.classified))
	}
	// prompt.submit: capture the submitted text when the policy directs it
	// (spec/policyset §6). Observational: never affects the decision.
	if n.Event.Kind == policy.EventPromptSubmit {
		dir := pdp.Capture()
		// Pin the reply watermark to "now", before the model answers and
		// whatever the capture directive says: a later capture must never
		// reach back past this prompt, including when capture is flipped ON
		// mid-session. If the PREVIOUS governed turn's session.end never
		// fired (killed terminal), its orphaned reply comes back here and is
		// spooled first, so the audit order stays chronological.
		if late := pinTranscriptWatermark(d.store, n.TranscriptPath, dir.Conversations); late != "" {
			content, truncated, hash := captureContent(late, dir.Mode)
			d.store.logSpoolError(n, spoolAppendCapture(spool.NewSpool(d.store.SpoolPath()), "reply", n,
				pdp.Session().SessionID, content, dir.Mode, truncated, hash))
		}
		if dir.Conversations && n.Prompt != "" {
			content, truncated, hash := captureContent(n.Prompt, dir.Mode)
			d.store.logSpoolError(n, spoolAppendCapture(spool.NewSpool(d.store.SpoolPath()), "prompt", n,
				pdp.Session().SessionID, content, dir.Mode, truncated, hash))
		}
		if dir.Conversations {
			drainSpawner()
		}
	}
	return decision
}

// captureReply captures a reply at a capture-point event and reports whether
// anything was spooled. At session.end a payload-borne reply (gemini
// AfterAgent `prompt_response`) wins when the dialect pins it (exact, no
// file I/O); otherwise the asymmetric half is read as the parent-transcript
// delta since the last capture. At subagent.stop the lanes invert: the
// DELEGATE's own transcript delta wins (it holds the whole governed output)
// and the payload-borne last_assistant_message (only the final message) is
// the fallback for an absent child file (codex: nullable). Best-effort by
// design: any failure leaves the event untouched.
func (d liveDecider) captureReply(ses Session, n Normalized) bool {
	path, payloadReply := n.TranscriptPath, n.Reply
	delegate := n.Event.Kind == policy.EventSubagentStop
	if delegate {
		// The delegate class tag is the entry ticket ("capture, tagged").
		// Harnesses run internal sidechains too: claude-code's input
		// autosuggest generator fires SubagentStop, and capturing it would
		// plant ghost suggestion text in the transcript. Since claude-code
		// 2.1.220 that sidechain carries an agent_id but still no
		// agent_type, so identity alone proves nothing. Real delegate lanes
		// always carry the type. No agent_type, no capture.
		if n.AgentType == "" {
			return false
		}
		path, payloadReply = n.AgentTranscriptPath, n.AgentReply
	}
	if payloadReply == "" && path == "" {
		return false
	}
	pdp, err := NewLocalPDP(d.store, ses.Subject())
	if err != nil {
		return false
	}
	dir := pdp.Capture()
	if !dir.Conversations {
		// The delegate's stop is its ONLY boundary (no prompt.submit ever
		// pins its file), so seal the watermark even when capture is off:
		// flipping capture on later must never reach back past this stop
		// (the subagent analog of pinTranscriptWatermark).
		if delegate && path != "" {
			sealTranscript(d.store, path)
		}
		return false
	}
	var reply string
	if delegate {
		if path != "" {
			reply, _ = transcriptDelta(d.store, path)
		}
		if reply == "" {
			reply = payloadReply
		}
	} else {
		reply = payloadReply
		if reply == "" {
			if reply, err = transcriptDelta(d.store, path); err != nil {
				return false
			}
		}
	}
	if reply == "" {
		return false
	}
	content, truncated, hash := captureContent(reply, dir.Mode)
	d.store.logSpoolError(n, spoolAppendCapture(spool.NewSpool(d.store.SpoolPath()), "reply", n,
		pdp.Session().SessionID, content, dir.Mode, truncated, hash))
	return true
}

// adoptSnapshot downloads, verifies, and caches the policy snapshot behind
// newID when the client does not already hold it, returning the id the session
// should carry from now on. The pin tracks the VERIFIED blob on disk, not the
// caller's session id: a crash between SaveSnapshot and SaveSession can leave
// the pin behind the blob, and the pin must catch up (hooks verify
// blob-against-pin, so a pin naming a blob we don't hold bricks every decision
// fail-closed). The blob is persisted BEFORE the id ever advances; on any fetch
// failure the last good id is kept: stale-but-working beats bricked, and the
// next refresh retries. This is how mid-session policy changes reach a live
// client (daemon tick or token refresh); a new session always fetches at
// checkin.
func adoptSnapshot(ctx context.Context, client *Client, sessionToken string, store *Store, cfg Config, currentID, newID string) (string, error) {
	// Reconcile to what disk actually holds first, so a pin left behind by a
	// crash is corrected even when the server reports no change.
	pin := currentID
	diskID, onDisk := diskSnapshotID(store, cfg)
	if onDisk {
		pin = diskID
	}
	// A report equal to the pin needs no fetch only when its blob is on disk.
	// With none, fetchAndAdopt downloads it in full.
	if newID == "" || (onDisk && newID == pin) {
		return pin, nil
	}
	return fetchAndAdopt(ctx, client, sessionToken, store, cfg, pin)
}

// fetchAndAdopt does the conditional fetch + verify + save and returns the id
// the session should now pin. The conditional request advertises the VERIFIED id
// of the blob on disk, not the caller's pin, which a crash may have left behind
// the blob, and never an id for a blob we don't hold (which would turn a 304
// into a permanent wedge). A 304 therefore means the disk blob is still the
// active one and its id is what we pin, reconciling any pin/blob drift with no
// download; a 200 saves the new blob first, then pins its id. On any failure the
// caller's id is returned unchanged. sessionToken is the token the caller holds
// right now, which the enterprise profile requires on the fetch.
func fetchAndAdopt(ctx context.Context, client *Client, sessionToken string, store *Store, cfg Config, currentID string) (string, error) {
	cachedID, _ := diskSnapshotID(store, cfg) // "" when no verifiable blob ⇒ forced full download
	sid, body, notModified, err := client.FetchSnapshot(ctx, sessionToken, cachedID)
	if err != nil {
		return currentID, err
	}
	if notModified {
		return cachedID, nil // 304 ⟹ cachedID is active and we hold that blob
	}
	lookup, err := keyLookup(cfg.SnapshotKeys)
	if err != nil {
		return currentID, fmt.Errorf("%w: %w", errSnapshotNotKept, err)
	}
	if _, _, err := policy.OpenSnapshot(body, sid, lookup); err != nil {
		return currentID, fmt.Errorf("%w: %w", errSnapshotUnverified, err)
	}
	if err := store.SaveSnapshot(body); err != nil {
		return currentID, fmt.Errorf("%w: %w", errSnapshotNotKept, err)
	}
	return sid, nil
}

// errSnapshotUnverified marks a fetched snapshot that the pinned keys do not
// verify, which settlePolicy counts as a refusal of the newer policy.
var errSnapshotUnverified = errors.New("refusing unverified snapshot")

// errSnapshotNotKept marks a snapshot the server sent with a 200 that this
// machine could not check against its pinned keys or could not store.
var errSnapshotNotKept = errors.New("straza could not check or store the snapshot on this machine")

// adoptForMint resolves the snapshot id a freshly minted session should pin,
// adopting the active blob when it can. It reuses fetchAndAdopt's blob-first
// contract but supplies the fallback a mint needs: a valid blob already on disk
// keeps its verified id, otherwise the checkin's id stands as an inert pin (no
// blob exists, so hooks correctly report "no cached snapshot" until a later
// fetch heals it). A snapshot fetch FAILURE must not fail the mint: the MCP
// proxy and exec wrapper front the gateway PEP and need no local blob to run;
// so the fallback id comes back with the error, which the caller only settles
// into the session's policy state (settlePolicy).
func adoptForMint(ctx context.Context, client *Client, sessionToken string, store *Store, cfg Config, checkinID string) (string, error) {
	fallback := checkinID
	if diskID, ok := diskSnapshotID(store, cfg); ok {
		fallback = diskID
	}
	return fetchAndAdopt(ctx, client, sessionToken, store, cfg, fallback)
}

// diskSnapshotID returns the content id of the snapshot blob currently cached on
// disk (hex sha256 of the signed bytes), but ONLY after the signature verifies
// against the pinned keys. An empty wantID makes policy.OpenSnapshot skip the
// id-equality gate while still proving the signature (snapshot.go), and a
// published snapshot's content address IS the hash of exactly those verified
// bytes. ok is false for a missing, torn, or unverifiable blob; the caller must
// then treat the cache as empty. This is the anchor of the pin invariant: the
// pin may only ever name a blob we can stand behind, so it follows the blob on
// disk, never the id off the wire.
func diskSnapshotID(store *Store, cfg Config) (id string, ok bool) {
	signed, err := store.LoadSnapshot()
	if err != nil {
		return "", false
	}
	lookup, err := keyLookup(cfg.SnapshotKeys)
	if err != nil {
		return "", false
	}
	if _, _, err := policy.OpenSnapshot(signed, "", lookup); err != nil {
		return "", false
	}
	sum := sha256.Sum256(signed)
	return hex.EncodeToString(sum[:]), true
}

// syncSnapshotIfStale is the per-decision policy pulse: fired from the same
// detached helper as the audit drain, it asks the server for a newer
// snapshot at most once per cfg.SnapshotLagSeconds (default 30 s; a marker
// file's mtime is the throttle); an unchanged snapshot costs one 304. This
// is what keeps a BUSY session at most one tool call + lag behind the active
// policy without any daemon; the hook's own decision path stays untouched.
func syncSnapshotIfStale(ctx context.Context, client *Client, store *Store, cfg Config) {
	marker := store.statePath("snapshot-checked")
	if fi, err := os.Stat(marker); err == nil && time.Since(fi.ModTime()) < snapshotLag(cfg) {
		return
	}
	ses, err := store.LoadSession()
	if err != nil {
		return
	}
	// A refusal here starts nothing: the pulse renews no session and cannot
	// tell a newer policy from an expired token. A fetch that succeeds holds
	// the server's current policy, so it ends any refusal (settlePolicy).
	adopted, err := fetchAndAdopt(ctx, client, ses.SessionToken, store, cfg, ses.SnapshotID)
	if err != nil {
		return // keep the current snapshot; the next pulse retries
	}
	if adopted != ses.SnapshotID || ses.PolicyRefused != "" {
		// Re-load right before writing: a hook may have refreshed the token
		// meanwhile. A lost token update is recoverable (the older token is
		// still valid and refreshes again); a lost snapshot id is not, so the
		// id write wins this tiny race by being last.
		if cur, err := store.LoadSession(); err == nil {
			cur.SnapshotID = adopted
			cur.settlePolicy(adopted, nil, cur.ExpiresAt)
			_ = store.SaveSession(cur)
		}
	}
	_ = os.WriteFile(marker, []byte(time.Now().UTC().Format(time.RFC3339)), 0o600)
}

// DrainOnce is the detached post-decision helper (`straza drain`) and a
// manual escape hatch: it pulses the policy snapshot (lag-throttled) and
// uploads spooled audit records. Returns how many records were uploaded.
func DrainOnce(timeout time.Duration) (int, error) {
	store, err := OpenStore()
	if err != nil {
		return 0, err
	}
	ses, err := store.LoadSession()
	if err != nil {
		return 0, err // no session = nothing to authenticate the batch with
	}
	cfg, err := store.LoadConfig()
	if err != nil {
		return 0, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	client := NewClient(cfg.ServerURL)
	syncSnapshotIfStale(ctx, client, store, cfg)
	// Past the session/config gates an upload was genuinely ATTEMPTED, so a
	// failure is a client error worth the log (the gates themselves are normal
	// states on an un-enrolled or idle box, not errors).
	n, err := spool.NewSpool(store.SpoolPath()).Drain(ctx, client, ses.SessionToken)
	store.logDrainError(err)
	return n, err
}

func (d liveDecider) drainSpool(ses Session) {
	cfg, err := d.store.LoadConfig()
	if err != nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, drainErr := spool.NewSpool(d.store.SpoolPath()).Drain(ctx, NewClient(cfg.ServerURL), ses.SessionToken)
	d.store.logDrainError(drainErr)
}

func failClosed(reason string) policy.Decision {
	return policy.Decision{Effect: policy.EffectDeny, Reason: "Straza: " + reason}
}

func fileSHA256(path string) (string, error) {
	b, err := os.ReadFile(path) // #nosec G304 G703 -- hashing our own managed artifacts for attestation; paths come from the managed resolvers (%ProgramData% env on Windows), never from request input
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(b)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

// SessionInfo describes the governed session established at checkin: who the
// agent is operating as and under which policy. Injected as session context so
// the agent knows it is governed (and can relay denials instead of fighting
// them).
type SessionInfo struct {
	User        string
	Roles       []string
	SnapshotID  string
	Attestation string
	ServerURL   string
	// Capture: whether this session's conversations are recorded (spec/
	// policyset §6: the principal is TOLD, never silently surveilled).
	Capture policy.CaptureDirective
}

// SessionStart performs the checkin flow: authenticate with the stored ID
// token, receive a session token + snapshot id + packs, fetch and verify the
// snapshot, persist state, and return the pack text to inject as harness
// context. Offline with a valid cached snapshot inside grace
// ⇒ degraded session; else block, because the client fails closed.
func SessionStart(ctx context.Context, store *Store, harnessName, harnessVersion string) (packs []PackPayload, info SessionInfo, err error) {
	cfg, err := store.LoadConfig()
	if err != nil {
		return nil, info, err
	}
	id, err := store.LoadIdentity()
	if err != nil {
		return nil, info, fmt.Errorf("not enrolled (run `straza enroll`): %w", err)
	}
	client := NewClient(cfg.ServerURL)

	resp, err := checkinWithIdentity(ctx, client, store, cfg, id, harnessName, harnessVersion)
	if err != nil {
		return nil, info, fmt.Errorf("checkin failed: %w", err)
	}

	// Fetch + verify the snapshot before trusting it. The known id is the
	// VERIFIED content id of the blob actually on disk, NOT the previous
	// session's pin, which may already have diverged from the blob. So a steady
	// state wedged by an old bad pin heals here: If-None-Match advertises what we
	// truly hold, and a server whose active id differs forces a full download
	// instead of a 304 that would keep the wrong blob. An empty (or unverifiable)
	// cache always does a full download.
	cachedID, _ := diskSnapshotID(store, cfg)
	sid, body, notModified, err := client.FetchSnapshot(ctx, resp.SessionToken, cachedID)
	if err != nil {
		return nil, info, fmt.Errorf("snapshot fetch failed: %w", err)
	}
	// The pin only ever equals the id of the blob we hold on disk: on 304 that is
	// the cached id we advertised; on 200 it is the id of the blob we just saved
	// (sid), NEVER the checkin's resp.SnapshotID: activation skew can leave that
	// pointing at a blob the snapshot endpoint is not serving yet.
	pinnedID := cachedID
	if !notModified {
		lookup, err := keyLookup(cfg.SnapshotKeys)
		if err != nil {
			return nil, info, err
		}
		if _, _, err := policy.OpenSnapshot(body, sid, lookup); err != nil {
			return nil, info, fmt.Errorf("refusing unverified snapshot: %w", err)
		}
		if err := store.SaveSnapshot(body); err != nil {
			return nil, info, err
		}
		pinnedID = sid
	}

	ses := Session{
		SessionID: resp.SessionID, SessionToken: resp.SessionToken, SnapshotID: pinnedID,
		User: resp.User, Roles: resp.Roles,
		Attestation: resp.Attestation, Harness: harnessLabel(harnessName, harnessVersion),
		IssuedAt: time.Now(), ExpiresAt: time.Now().Add(time.Duration(resp.ExpiresIn) * time.Second),
	}
	if err := store.SaveSession(ses); err != nil {
		return nil, info, err
	}
	// a successful checkin supersedes any previous kill
	_ = store.ClearRevocation()

	// Leftover audit records from earlier sessions (terminal killed before the
	// session.end drain, no daemon running) ride out on the next session
	// start. Best-effort and silent: the caller is a hook whose stdout the
	// harness parses; a failure just leaves the spool for the next opportunity.
	_, _ = spool.NewSpool(store.SpoolPath()).Drain(ctx, client, resp.SessionToken)
	info = SessionInfo{User: resp.User, Roles: resp.Roles, SnapshotID: pinnedID,
		Attestation: resp.Attestation, ServerURL: cfg.ServerURL}
	// Resolve the capture directive so the banner can say it out loud
	// (spec/policyset §6: no silent surveillance). Best-effort: a resolve
	// failure only omits the notice; enforcement happens per event anyway.
	if signed, err := store.LoadSnapshot(); err == nil {
		if lookup, err := keyLookup(cfg.SnapshotKeys); err == nil {
			if eng, _, err := policy.OpenSnapshot(signed, ses.SnapshotID, lookup); err == nil {
				info.Capture = eng.Capture(ses.Subject())
			}
		}
	}
	return resp.Packs, info, nil
}

// checkinWithIdentity performs one identity-lane check-in.
//
// The long-lived device credential starts sessions; the login-time ID
// token (minutes of validity) is only the fallback for state files written
// before device tokens existed. Headless NHIs have no device at all:
// the local key/client secret mints a fresh ID token per check-in and the
// checkin is deviceless. This is the credential path a session START uses,
// shared by the mid-session re-acquire (LocalPDP/daemon) so an expired
// session token never strands a still-enrolled client.
func checkinWithIdentity(ctx context.Context, client *Client, store *Store, cfg Config, id Identity, harnessName, harnessVersion string) (CheckinResponse, error) {
	switch {
	case id.Headless != "":
		tok, err := headlessIDToken(ctx, store, cfg.ServerURL, id)
		if err != nil {
			return CheckinResponse{}, err
		}
		return client.Checkin(ctx, tok, "", harnessName, harnessVersion, measureAttestation(harnessName))
	case id.DeviceToken != "":
		resp, err := client.CheckinDevice(ctx, id.DeviceToken, harnessName, harnessVersion, measureAttestation(harnessName))
		if err == nil && resp.DeviceToken != "" {
			adoptDeviceToken(store, id, resp.DeviceToken)
		}
		return resp, err
	default:
		return client.Checkin(ctx, id.IDToken, id.DeviceID, harnessName, harnessVersion, measureAttestation(harnessName))
	}
}

// adoptDeviceToken persists a renewed device credential so the
// next session start presents the fresh one. Best-effort by design: the
// credential just presented is still valid, so a failed write goes to
// errors.jsonl and the check-in that carried the renewal stands.
func adoptDeviceToken(store *Store, id Identity, token string) {
	id.DeviceToken = token
	if err := store.SaveIdentity(id); err != nil {
		store.logClientError(ClientError{Kind: "identity", Err: "store renewed device credential: " + err.Error()})
	}
}

// PackContext renders knowledge packs as a single additional-context string
// for injection at session start.
func PackContext(packs []PackPayload) string {
	if len(packs) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("# Straza knowledge packs (delivered by role)\n\n")
	for _, p := range packs {
		fmt.Fprintf(&b, "## %s (v%s)\n\n%s\n\n", p.Name, p.Version, p.Content)
	}
	return b.String()
}
