package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/strazahq/straza/internal/agentguard"
	"github.com/strazahq/straza/internal/policy"
	"github.com/strazahq/straza/internal/sameorigin"
)

// heldSnapshot is the verified policy snapshot an account holds: verified
// once, kept in memory, and kept on disk for a restart. created is the
// snapshot's compile time, which never goes backward.
type heldSnapshot struct {
	id      string
	eng     *policy.Engine
	maxAge  int64
	created int64
}

// Policy returns the verified engine of ses's snapshot pin, its maxAge and
// the id of the snapshot the engine was built from, read under the same
// lock as the pin. When the account does not hold that snapshot, a live
// session fetches it with its own token, and a failed fetch is answered
// again for the snapshot lag without asking the server. The error's text
// is the deny sentence.
func (s *credStore) Policy(ctx context.Context, ses Session) (*policy.Engine, int64, string, error) {
	a := s.account(ses.UID)
	rec, _, err := a.ready()
	if err != nil {
		return nil, 0, "", err
	}
	pin, token, expires, id := ses.SnapshotID, "", ses.ExpiresAt, ses.ID
	a.mu.Lock()
	if hs := a.sessions[sessKey{ses.Harness.Name, ses.Harness.Session}]; hs != nil {
		pin, token, expires, id = hs.s.SnapshotID, hs.token, hs.s.ExpiresAt, hs.s.ID
	}
	snap := a.snap
	a.mu.Unlock()
	if snap != nil && snap.id == pin {
		if token != "" && s.now().Before(expires) {
			s.pulse(a, token)
		}
		return snap.eng, snap.maxAge, snap.id, nil
	}
	if token == "" || !s.now().Before(expires) {
		return nil, 0, "", errors.New("the policy snapshot of this session is not on this machine, and the session has no time left to fetch it. " +
			"Restart the session so straza can check in again, or run `straza doctor`")
	}
	lag, now := snapshotLag(), s.now()
	a.mu.Lock()
	f, failed := a.failed[id]
	a.mu.Unlock()
	if age := now.Sub(f.at); failed && age >= 0 && age < lag {
		return nil, 0, "", errors.New(missingSnapshotReason(rec.ServerURL, age, f, lag-age))
	}
	if _, err := s.fetchAndAdopt(ctx, a, token, pin); err != nil {
		f := fetchOutcome(err)
		f.at = now
		a.mu.Lock()
		a.failed[id] = f
		a.mu.Unlock()
		return nil, 0, "", errors.New(missingSnapshotReason(rec.ServerURL, 0, f, lag))
	}
	a.mu.Lock()
	snap = a.snap
	delete(a.failed, id)
	a.mu.Unlock()
	if snap == nil {
		return nil, 0, "", errors.New("the policy snapshot of this session is missing on this machine, because the enrollment changed while it was fetched. " +
			"Restart the session so straza can check in again, or run `straza doctor`")
	}
	return snap.eng, snap.maxAge, snap.id, nil
}

// pulse starts one background conditional fetch of the active snapshot for
// a, with token and its proof, when a's last check is older than the
// snapshot lag. So a busy session runs at most one call plus the lag behind
// the active policy even with no push stream. It never blocks the caller,
// at most one runs per account, and a newer snapshot is adopted and pins
// every window of a, which ends any refusal. A failed fetch keeps every
// pin and starts no refusal, because the pulse renews nothing and cannot
// tell a newer policy from an expired token.
func (s *credStore) pulse(a *account, token string) {
	lag, now := snapshotLag(), s.now()
	a.mu.Lock()
	if a.pulsing || now.Sub(a.checked) < lag {
		a.mu.Unlock()
		return
	}
	a.pulsing, a.checked = true, now
	a.mu.Unlock()
	go func() {
		defer func() {
			a.mu.Lock()
			a.pulsing = false
			a.mu.Unlock()
		}()
		ctx, cancel := context.WithTimeout(s.ctx, requestTimeout)
		defer cancel()
		_, _ = s.fetchAndAdopt(ctx, a, token, a.heldID())
	}()
}

// loadSnapshot verifies the snapshot kept on disk for a and holds it. A
// missing or unverifiable blob leaves a holding none.
func (s *credStore) loadSnapshot(a *account, rec enrollment) {
	signed, err := os.ReadFile(filepath.Join(a.dir, snapshotFile)) // #nosec G304 -- the service's own state directory
	if err != nil {
		return
	}
	keys, err := snapshotKeys(rec)
	if err != nil {
		s.log.Error("the managed config cannot be read, so no policy snapshot is trusted until it is fixed", "uid", a.uid, "err", err)
		return
	}
	snap, err := openSnapshot(signed, "", keys)
	if err != nil {
		s.log.Warn("the policy snapshot kept on disk does not verify, so it is fetched again", "uid", a.uid, "err", err)
		return
	}
	a.adoptSnap(s, snap)
}

// openSnapshot verifies signed under keys and, when wantID is set, that it
// is the snapshot wantID names. Its id is the hex SHA-256 of signed.
func openSnapshot(signed []byte, wantID string, keys map[string]string) (*heldSnapshot, error) {
	lookup, err := agentguard.KeyLookup(keys)
	if err != nil {
		return nil, err
	}
	eng, snap, err := policy.OpenSnapshot(signed, wantID, lookup)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(signed)
	return &heldSnapshot{id: hex.EncodeToString(sum[:]), eng: eng, maxAge: snap.MaxAgeSecs, created: snap.CreatedUnix}, nil
}

// heldID returns the id of the verified snapshot a holds, "" for none.
func (a *account) heldID() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.snap == nil {
		return ""
	}
	return a.snap.id
}

// adoptSnap makes snap a's snapshot and pins every held session to it. The
// server serves one active snapshot, so a fetch that landed holds the
// current policy for every session, and it ends any refusal. A session it
// changed is saved.
func (a *account) adoptSnap(s *credStore, snap *heldSnapshot) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.snap = snap
	changed := false
	for _, hs := range a.sessions {
		if hs.s.SnapshotID != snap.id || hs.s.PolicyRefused != "" {
			hs.s.SnapshotID = snap.id
			hs.s.settlePolicy(snap.id, nil, hs.s.ExpiresAt)
			changed = true
		}
	}
	if changed {
		s.saveSessions(a)
	}
}

// adoptSnapshot returns the pin a session should carry after strazad named
// newID. The pin follows the verified snapshot a holds, never an id off
// the wire. A newID a holds, or none, needs no fetch. Otherwise the
// snapshot is fetched, verified and kept before the pin moves, and on any
// failure the pin stays, because stale but working beats bricked.
func (s *credStore) adoptSnapshot(ctx context.Context, a *account, token, currentID, newID string) (string, error) {
	pin := currentID
	held := a.heldID()
	if held != "" {
		pin = held
	}
	if newID == "" || (held != "" && newID == pin) {
		return pin, nil
	}
	return s.fetchAndAdopt(ctx, a, token, pin)
}

// fetchAndAdopt fetches the active snapshot with token, advertising the
// verified id a holds so an unchanged snapshot costs a 304. Fetches of one
// account run one at a time, so the second of two concurrent ones costs a
// 304 too. A new one is verified against the pinned keys and kept on disk
// before a adopts it, and one compiled before the held one is never
// adopted, so an answer that lands late cannot take the policy back. On any
// failure currentID comes back with the error.
func (s *credStore) fetchAndAdopt(ctx context.Context, a *account, token, currentID string) (string, error) {
	rec, api, err := a.ready()
	if err != nil {
		return currentID, err
	}
	if err := a.fetchLock.lock(ctx); err != nil {
		return currentID, err
	}
	defer a.fetchLock.unlock()
	cachedID := a.heldID()
	sid, body, notModified, err := api.FetchSnapshot(ctx, token, cachedID)
	if err != nil {
		return currentID, err
	}
	a.mu.Lock()
	a.checked = s.now()
	a.mu.Unlock()
	if notModified && cachedID != "" {
		return cachedID, nil
	}
	keys, err := snapshotKeys(rec)
	if err != nil {
		err = &managedConfigError{err: err}
		sum := sha256.Sum256(body)
		a.refuseAll(s, hex.EncodeToString(sum[:]), err)
		return currentID, err
	}
	snap, err := openSnapshot(body, sid, keys)
	if err != nil {
		return currentID, fmt.Errorf("%w: %w", errSnapshotUnverified, err)
	}
	a.mu.Lock()
	held := a.snap
	a.mu.Unlock()
	if held != nil && snap.created < held.created {
		return held.id, nil
	}
	if err := writeFileAtomic(filepath.Join(a.dir, snapshotFile), body); err != nil {
		return currentID, fmt.Errorf("%w: %w", errSnapshotNotKept, err)
	}
	a.adoptSnap(s, snap)
	return snap.id, nil
}

// settlePolicy records what a renewal learned about the policy s enforces,
// and it is the one place that rule lives. reported is the snapshot id the
// renewal named, err the result of adopting it, and held the session time
// s had before the renewal. A refusal of a policy other than the one s
// pins starts or continues a refusal, whose deadline stays at the session
// time held when it began, so renewing the token never extends it.
// Adopting the reported policy, or holding it, ends the refusal. A renewal
// that names no policy, a transport failure and a 5xx leave the state as it
// was, so an outage alone never stops work.
func (s *Session) settlePolicy(reported string, err error, held time.Time) {
	switch why := refusalReason(err); {
	case reported == "":
	case err == nil || s.SnapshotID == reported:
		s.PolicyRefused, s.PolicyDeadline = "", time.Time{}
	case why != "":
		if s.PolicyDeadline.IsZero() {
			s.PolicyDeadline = held
		}
		s.PolicyRefused = why
	}
}

// managedConfigError is a managed config that cannot be read or parsed,
// met while checking a fetched snapshot. settlePolicy counts it as a
// refusal, so a policy verified before the config broke stops at the
// session time it held.
type managedConfigError struct{ err error }

func (e *managedConfigError) Error() string { return e.err.Error() }
func (e *managedConfigError) Unwrap() error { return e.err }

// refuseAll starts or continues a refusal of the policy id on every window
// of a, because a could not check it, and saves the refusals. Each
// window's deadline stays at the session time it held, as settlePolicy
// keeps it.
func (a *account) refuseAll(s *credStore, id string, err error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	for _, hs := range a.sessions {
		hs.s.settlePolicy(id, err, hs.s.ExpiresAt)
	}
	s.saveSessions(a)
}

// errSnapshotUnverified marks a fetched snapshot that the pinned keys do not
// verify, which settlePolicy counts as a refusal of the newer policy.
var errSnapshotUnverified = errors.New("refusing unverified snapshot")

// errSnapshotNotKept marks a snapshot the server sent with a 200 that this
// machine could not keep.
var errSnapshotNotKept = errors.New("straza could not check or store the snapshot on this machine")

// refusalReason words why a fetch of a newer policy failed, when the
// failure counts as a refusal: a server answer below 500, a redirect the
// client would not follow, a redirect loop, a snapshot the pinned keys
// cannot verify, or a managed config that cannot be read to check it. It
// returns "" for a transport failure or a 5xx.
func refusalReason(err error) string {
	var refuse *agentguard.StatusError
	var redirect *sameorigin.RedirectError
	var managed *managedConfigError
	switch {
	case err == nil:
		return ""
	case errors.As(err, &managed):
		return "Straza will not enforce the policy the server sent, because " + endSentence(managed.err.Error())
	case errors.As(err, &refuse) && refuse.Status < http.StatusInternalServerError:
		if refuse.Msg == fmt.Sprintf("HTTP %d", refuse.Status) {
			return fmt.Sprintf("The server refused it with HTTP %d and gave no reason.", refuse.Status)
		}
		return "The server refused it: " + endSentence(strings.TrimPrefix(refuse.Msg, "Straza: "))
	case errors.As(err, &redirect):
		return "The fetch was redirected: " + endSentence(redirect.Error())
	case errors.Is(err, sameorigin.ErrTooManyRedirects):
		return "The fetch was redirected 10 times in a row and never reached the policy. " +
			"Check that the configured server URL is the address strazad serves on, and that no proxy in front of strazad redirects."
	case errors.Is(err, errSnapshotUnverified):
		return "The policy the server sent does not verify against the snapshot keys this machine pinned, so straza will not enforce it. " +
			"Either the server's snapshot signing key changed, or something in front of strazad answered in its place. " +
			"After a key change, run `straza enroll` again, or on a machine set up with `straza install --managed` ask an administrator " +
			"to run that install again with `--server <server-url>`, and then restart the session. Otherwise tell an administrator."
	}
	return ""
}

// endSentence closes s with one period.
func endSentence(s string) string {
	return strings.TrimRight(strings.TrimSpace(s), ". ") + "."
}

// managedConfig reads the root-owned managed config when the managed
// layout is installed. It wins over what an enrollment chose, so a
// user-writable choice cannot re-point enforcement at another server. A
// managed config that does not parse is an error, never a fallback.
func managedConfig() (agentguard.Config, bool, error) {
	var c agentguard.Config
	if !agentguard.ManagedInstalled() {
		return c, false, nil
	}
	raw, err := os.ReadFile(agentguard.ManagedConfigPath()) // #nosec G304 -- the fixed managed layout path
	if err != nil {
		return c, false, fmt.Errorf("read the managed config %s: %w", agentguard.ManagedConfigPath(), err)
	}
	if err := yaml.Unmarshal(raw, &c); err != nil {
		return c, false, fmt.Errorf("the managed config %s does not parse, so Straza will not guess the server. Ask IT to reinstall Straza: %w",
			agentguard.ManagedConfigPath(), err)
	}
	return c, true, nil
}

// snapshotKeys returns the snapshot keys that verify rec's policy: the
// managed config's when it pins any, else those rec pinned at enrollment.
// A managed config that cannot be read or parsed returns its error and no
// keys, so no policy verifies until it is fixed.
func snapshotKeys(rec enrollment) (map[string]string, error) {
	c, managed, err := managedConfig()
	switch {
	case err != nil:
		return nil, err
	case managed && len(c.SnapshotKeys) > 0:
		return c.SnapshotKeys, nil
	}
	return rec.SnapshotKeys, nil
}

// snapshotLag is the least time between two fetches of a missing snapshot
// for one session: the managed config's snapshotLagSeconds, or 30 seconds,
// also when the managed config cannot be read, because the lag only paces
// fetches and decides nothing.
func snapshotLag() time.Duration {
	if c, ok, err := managedConfig(); err == nil && ok && c.SnapshotLagSeconds > 0 {
		return time.Duration(c.SnapshotLagSeconds) * time.Second
	}
	return 30 * time.Second
}
