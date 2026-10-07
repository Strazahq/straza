package agentguard

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/strazahq/straza/internal/agentguard/spool"
)

// Daemon is the long-lived client process: it subscribes to the
// kill-switch push channel so a revocation drops session state
// within ~sub-second, keeps the session token fresh, and drains the audit
// spool. Hooks read the state file the daemon maintains; if the daemon is
// absent, hooks still fail closed on their own (token/snapshot re-verified
// every call); the daemon only makes revocation faster than token TTL.
type Daemon struct {
	store *Store
	// client is built once at Run (config is static for the daemon's
	// lifetime) so poll ticks reuse its transport's keep-alive connections.
	client *Client
	// PollInterval bounds the poll-refresh fallback (default 30 s). On each
	// tick the token is refreshed; a revoked session 401s → state dropped.
	PollInterval time.Duration
	// now is injectable for tests.
	now func() time.Time
	// out receives one-line human status updates (nil = discard).
	out io.Writer
	// beatFailed keeps the heartbeat's log-once contract (heartbeat.go): a
	// write failure is reported the first time, then silently retried.
	beatFailed bool
}

// defaultPollInterval is the poll-refresh cadence when the operator sets none;
// readDaemonLiveness also falls back to it for a heartbeat with no interval.
const defaultPollInterval = 30 * time.Second

// NewDaemon builds a daemon over the straza state store.
func NewDaemon(store *Store, out io.Writer) *Daemon {
	if out == nil {
		out = io.Discard
	}
	return &Daemon{store: store, PollInterval: defaultPollInterval, now: time.Now, out: out}
}

// Run drives the daemon until ctx is done. It returns when the session ends
// (state dropped and no session to refresh) or ctx cancels.
func (d *Daemon) Run(ctx context.Context) error {
	if _, err := d.store.LoadSession(); err != nil {
		return fmt.Errorf("no active session (start a harness session first): %w", err)
	}
	cfg, err := d.store.LoadConfig()
	if err != nil {
		return err
	}
	d.client = NewClient(cfg.ServerURL)

	// Push lane: subscribe over the same HTTPS origin the daemon already talks
	// to. The subscriber re-reads the session token per (re)connect and feeds
	// the revoked and nudged signals. The poll loop below stays the
	// correctness backstop: an older server without /v1/push answers 404 and
	// the subscriber parks itself, which IS plain poll-refresh.
	revoked := make(chan struct{}, 1)
	nudged := make(chan struct{}, 1)
	token := func() (string, bool) {
		ses, err := d.store.LoadSession()
		if err != nil {
			return "", false
		}
		return ses.SessionToken, true
	}
	go newEdgePush(cfg.ServerURL, token, revoked, nudged, d.out).run(ctx)
	fmt.Fprintln(d.out, "daemon: edge push subscribing; poll-refresh remains the backstop")
	return d.loop(ctx, revoked, nudged)
}

// loop runs the refresh/drain ticker, reacting immediately to push signals.
func (d *Daemon) loop(ctx context.Context, revoked, nudged <-chan struct{}) error {
	// The heartbeat rides the poll ticker (the one timer that fires in both
	// push and poll mode) so `straza doctor` can tell a lane with a live
	// subscriber from a lane nobody holds (heartbeat.go). Best-effort: beat
	// never errors and never blocks the real work below.
	d.beat()
	defer d.clearHeartbeat()
	t := time.NewTicker(d.PollInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-revoked:
			if err := d.store.DropSession(); err != nil {
				return fmt.Errorf("drop session: %w", err)
			}
			// leave the WHY behind for the hooks: this was a kill, not a timeout
			_ = d.store.MarkRevoked("kill-switch push from the server")
			fmt.Fprintln(d.out, "daemon: revocation received. Session state dropped, hooks now deny")
			return nil
		case <-nudged:
			// A policy changed server-side: refresh immediately instead of
			// waiting out the poll interval; refresh() adopts the new
			// snapshot blob-first (verified) and drains the spool; a session
			// revoked in the meantime is handled by its refusal path.
			fmt.Fprintln(d.out, "daemon: policy-update nudge, refreshing now")
			if d.refresh(ctx) {
				return nil
			}
		case <-t.C:
			d.beat()
			if d.refresh(ctx) {
				return nil // session ended/revoked via the poll path
			}
		}
	}
}

// refresh runs one poll-refresh; returns true when the session is gone.
func (d *Daemon) refresh(ctx context.Context) bool {
	ses, err := d.store.LoadSession()
	if err != nil {
		return true // state already gone
	}
	resp, err := d.client.Refresh(ctx, ses.SessionToken,
		harnessName(ses.Harness), harnessVersion(ses.Harness), measureAttestation(harnessName(ses.Harness)))
	if err != nil {
		var refuse *StatusError
		if !errors.As(err, &refuse) || (refuse.Status != http.StatusUnauthorized && refuse.Status != http.StatusForbidden) {
			// Transport failure or server-side trouble: a blip is not a
			// revocation. Keep the state (the server is authoritative on
			// expiry, same as LocalPDP.RefreshIfStale) and retry next tick.
			fmt.Fprintf(d.out, "daemon: refresh failed (%v); keeping session, will retry\n", err)
			return false
		}
		// A 401 means the SESSION died (expired or revoked), but the enrollment
		// may still be alive, so the identity lane gets one try before the
		// state is dropped (parity with LocalPDP.reacquire). A 403 judged
		// the identity itself: terminal, never retried here.
		if refuse.Status == http.StatusUnauthorized && d.reacquireSession(ctx, ses) {
			return false
		}
		// Refused terminally: drop state, hooks fail closed.
		if err := d.store.DropSession(); err != nil {
			fmt.Fprintf(d.out, "daemon: refresh refused but state drop failed (%v); retrying next tick\n", err)
			return false
		}
		_ = d.store.MarkRevoked("session refresh refused by the server")
		fmt.Fprintln(d.out, "daemon: refresh refused. Session dropped")
		return true
	}
	held := ses.ExpiresAt
	ses.SessionToken = resp.SessionToken
	ses.ExpiresAt = d.now().Add(time.Duration(resp.ExpiresIn) * time.Second)
	// Adopt the re-resolved identity alongside the token it was minted for, so
	// an IdM role change reaches hook decisions within a poll interval instead
	// of waiting for the next session start.
	adoptIdentity(&ses, resp)
	// Adopt a changed policy snapshot blob-first. Advancing the id alone would
	// fail every hook on the id-vs-blob check until the session restarts.
	if cfg, err := d.store.LoadConfig(); err == nil {
		adopted, err := adoptSnapshot(ctx, d.client, resp.SessionToken, d.store, cfg, ses.SnapshotID, resp.SnapshotID)
		if err == nil && adopted != ses.SnapshotID {
			fmt.Fprintf(d.out, "daemon: policy snapshot updated → %s\n", adopted)
		}
		ses.SnapshotID = adopted
		switch refused := ses.settlePolicy(resp.SnapshotID, err, held); {
		case refused:
			fmt.Fprintf(d.out, "daemon: the new policy snapshot could not be fetched (%v); hooks deny from %s until a fetch succeeds\n",
				err, policyDenyAt(d.store, cfg, ses).Local().Format(time.RFC3339))
		case err != nil:
			fmt.Fprintf(d.out, "daemon: new policy snapshot not adopted yet (%v); keeping the current one\n", err)
		}
	}
	_ = d.store.SaveSession(ses)

	// Drain spooled hook decisions every healthy tick: this IS the promised
	// near-live audit path. Without a daemon, decisions only reach the server
	// at session end (and never, if the harness dies hard); with one, they
	// land within a poll interval. Best-effort: a failed drain keeps the
	// spool intact for the next tick (zero loss, server dedupes by CE id).
	if n, err := spool.NewSpool(d.store.SpoolPath()).Drain(ctx, d.client, ses.SessionToken); err != nil {
		fmt.Fprintf(d.out, "daemon: audit drain failed (records kept): %v\n", err)
	} else if n > 0 {
		fmt.Fprintf(d.out, "daemon: drained %d audit record(s)\n", n)
	}
	return false
}

// reacquireSession tries to establish a fresh session through the enrolled
// identity after a 401-refused refresh, the same lane a session start uses,
// so the server re-runs its denylist, device/user status, and attestation
// gates. It reports whether the daemon should KEEP running with state intact:
// true on a successful re-acquire (new session saved), on a transport blip
// mid-re-acquire, and on an answer that judged nothing (renewalAnswer's
// renewUnjudged, as the hook classes it), so the next tick retries; false
// when there is no enrolled identity or the server refused it; that refusal
// is terminal and the caller drops state. One deliberate seam: the push
// subscriptions taken at Run still name the OLD session's target subject,
// so a session-scoped revocation of the re-acquired session lands via the
// poll lane (next tick's 401) rather than sub-second push; the user/device
// kill subjects are unchanged and stay live.
func (d *Daemon) reacquireSession(ctx context.Context, ses Session) bool {
	id, err := d.store.LoadIdentity()
	if err != nil {
		return false
	}
	cfg, err := d.store.LoadConfig()
	if err != nil {
		return false
	}
	resp, err := checkinWithIdentity(ctx, d.client, d.store, cfg, id,
		harnessName(ses.Harness), harnessVersion(ses.Harness))
	if err != nil {
		var refuse *StatusError
		if errors.As(err, &refuse) && refuse.Status < 500 {
			class, detail := renewalAnswer(refuse)
			if class == renewUnjudged {
				fmt.Fprintf(d.out, "daemon: session renewal got an answer that does not say whether this session is still accepted: %s "+
					"The daemon keeps the session and tries again at the next poll. If this keeps happening, check that the configured "+
					"server URL reaches strazad with nothing in front of it that limits or blocks straza, and run `straza doctor`.\n", detail)
				return true
			}
			fmt.Fprintf(d.out, "daemon: session renewal refused (%s)\n", detail)
			return false
		}
		fmt.Fprintf(d.out, "daemon: session renewal failed (%v); keeping state, will retry\n", err)
		return true
	}
	adoptCheckin(ctx, d.client, d.store, cfg, &ses, resp, d.now())
	if err := d.store.SaveSession(ses); err != nil {
		fmt.Fprintf(d.out, "daemon: renewed session not saved (%v); retrying next tick\n", err)
		return true
	}
	_ = d.store.ClearRevocation()
	fmt.Fprintln(d.out, "daemon: session renewed via the enrolled identity")
	return true
}
