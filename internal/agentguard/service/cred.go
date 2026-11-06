package service

import (
	"context"
	"crypto/ed25519"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/strazahq/straza/internal/agentguard"
	"github.com/strazahq/straza/internal/agentguard/localapi"
	"github.com/strazahq/straza/internal/agentguard/spool"
	"github.com/strazahq/straza/internal/dpop"
)

// Sentences the credential half answers with. The two lower-case ones are
// deny reasons the hook prefixes with "Straza: ".
const (
	notEnrolledSentence = "This machine isn't enrolled for you yet. A person runs `straza enroll --server <strazad url>` and signs in with their work account. " +
		"An AI agent runs `straza enroll --server <strazad url> --headless` with its registered key or the client secret IT installed for it."
	agentKeySentence = "This machine isn't enrolled for you yet. This OS account holds an AI agent's key from `straza keygen`. " +
		"Once an admin has registered its public key, run `straza enroll --server <strazad url> --headless`."
	machineChangedSentence = "This machine's identity changed since it was enrolled, so Straza stopped using its key. Run `straza enroll --server <strazad url>` to enroll it again."
	waitingSentence        = "Straza is waiting for the server to confirm this machine's last check-in, which takes up to two minutes after a lost connection. Start the session again in a minute."
	secondServiceSentence  = "Another Straza background service already uses this state directory, so this one stopped. Stop the other one first, or set a different STRAZA_HOME."
	noSessionSentence      = "no active Straza session. Restart the session so straza can check in"
	agentKeyChangedFormat  = "This machine's identity changed since it was enrolled, so Straza stopped using its key. " +
		"Run `straza keygen --force --user %s`, have an admin register the new public key it prints, and then run `straza enroll --server <strazad url> --headless`."
	agentSecretChangedFormat = "This machine's identity changed since it was enrolled, so Straza stopped using its key. " +
		"Run `straza enroll --server <strazad url> --headless --user %s` to enroll the agent again."
)

// serverFlag is the --server flag of the enroll commands in the sentences
// above, which onThisMachine fits to this machine.
const serverFlag = " --server <strazad url>"

// onThisMachine fits the enroll commands in sentence to this machine. A
// managed configuration that names the server drops the flag, because the
// service enrolls there whatever --server says, as enrollServer does.
// Otherwise server, when known, takes the placeholder's place.
func onThisMachine(sentence, server string) string {
	if c, managed, err := managedConfig(); err == nil && managed && c.ServerURL != "" {
		return strings.ReplaceAll(sentence, serverFlag, "")
	}
	if server != "" {
		return strings.ReplaceAll(sentence, serverFlag, " --server "+server)
	}
	return sentence
}

// changedSentence is the refusal after the machine identity changed under
// rec, in the lane rec enrolled on. A person enrolls again. An agent on the
// key lane needs a new key, because enrollAgent does not reuse a key whose
// machine changed. An agent on the client-secret lane enrolls again with its
// secret. A record with no lane reads as the secret lane, as in agentCheckin.
func changedSentence(rec enrollment) string {
	if rec.Kind != localapi.KindAgent {
		return onThisMachine(machineChangedSentence, rec.ServerURL)
	}
	user := rec.Username
	if user == "" {
		user = "<agent username>"
	}
	format := agentSecretChangedFormat
	if rec.Lane == laneKey {
		format = agentKeyChangedFormat
	}
	return onThisMachine(fmt.Sprintf(format, user), rec.ServerURL)
}

// revokedSentence is the deny reason of a session the server revoked.
func revokedSentence(reason string) string {
	return "session revoked (" + reason + "). Tool calls stay denied until a new session " +
		"starts and checks in again. If only this session was revoked, that check-in starts a new session. " +
		"If the device or user was disabled, the check-in is refused until an administrator re-enables it. " +
		"Inform the user and stop."
}

// Timing of the credential half.
const (
	// renewBefore is how much session time left triggers a refresh.
	renewBefore = 120 * time.Second
	// lostAnswerWait is how long the next key check-in waits after one whose
	// outcome stayed unknown, counted from when that request's nonce
	// arrived. The server accepts a nonce for two 60-second windows at most.
	lostAnswerWait = 2 * time.Minute
	// requestTimeout bounds every other strazad call.
	requestTimeout = 30 * time.Second
)

// credStore is the credential half of one service process. It holds the
// state directory's lock and one account per uid that has used it.
type credStore struct {
	opts    Options
	log     *slog.Logger
	ctx     context.Context
	cancel  context.CancelFunc
	lock    *os.File
	unlock  sync.Once
	machine string
	selfUID string
	base    http.RoundTripper
	now     func() time.Time
	// checkinTimeout bounds one key check-in, which runs to its answer
	// even when the caller stops waiting.
	checkinTimeout time.Duration
	// drainEvery is the period of the spool drain, and pushCheck how often
	// an open push stream checks that its session has not lapsed.
	drainEvery time.Duration
	pushCheck  time.Duration

	mu       sync.Mutex
	accounts map[string]*account
	// git holds the git door's grants and its transport to resource
	// servers. gitState creates it on first use under mu.
	git *gitState
}

var _ credentials = (*credStore)(nil)

// newCredentials opens the credential half on opts.StateDir. It takes the
// directory's file lock, which it holds until ctx ends, and answers the
// second-service sentence when another service holds it.
func newCredentials(ctx context.Context, opts Options) (*credStore, error) {
	if opts.StateDir == "" {
		return nil, errors.New("the Straza background service has no state directory, so it cannot keep keys. Set STRAZA_HOME, or run it as the system service")
	}
	if err := prepareStateDir(opts.StateDir); err != nil {
		return nil, err
	}
	lock, err := lockStateDir(opts.StateDir)
	if err != nil {
		return nil, err
	}
	machine, err := machineIdentity(opts.StateDir)
	if err != nil {
		_ = lock.Close()
		return nil, err
	}
	log := opts.Log
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	run, cancel := context.WithCancel(ctx)
	s := &credStore{
		opts: opts, log: log, ctx: run, cancel: cancel, lock: lock, machine: machine,
		selfUID: ownUID(),
		base:    http.DefaultTransport.(*http.Transport).Clone(),
		now:     time.Now, checkinTimeout: 10 * time.Second, drainEvery: 15 * time.Second, pushCheck: 5 * time.Second,
		accounts: map[string]*account{},
	}
	go func() {
		<-run.Done()
		s.close()
	}()
	return s, nil
}

// close stops every stream and drain and releases the state directory's
// lock. It runs once, when the service's context ends or a test restarts
// the service.
func (s *credStore) close() {
	s.unlock.Do(func() {
		s.cancel()
		_ = s.lock.Close()
	})
}

// account is one uid's enrollment, key, sessions and spool. keyLock is the
// per-key lock every key check-in and every key change holds. mu guards the
// fields below it, and is never held across a network call.
type account struct {
	uid     string
	dir     string
	keyLock ctxLock
	// fetchLock makes the account's snapshot fetches one at a time, so a
	// policy change is downloaded and verified once.
	fetchLock ctxLock
	nonces    *nonceCache
	spool     *spool.Spool
	kick      chan struct{}

	mu       sync.Mutex
	rec      *enrollment
	key      ed25519.PrivateKey
	changed  bool
	api      *agentguard.Client
	proofs   *proofTransport
	snap     *heldSnapshot
	sessions map[sessKey]*heldSession
	agentTok cachedToken
	failed   map[string]fetchFailure
	drainErr string
	// checked is when a's snapshot was last checked with the server, and
	// pulsing is set while a background check runs.
	checked time.Time
	pulsing bool
}

// account returns the account of uid, reading it from the state directory
// the first time.
func (s *credStore) account(uid string) *account {
	s.mu.Lock()
	defer s.mu.Unlock()
	if a, ok := s.accounts[uid]; ok {
		return a
	}
	a := &account{
		uid: uid, dir: filepath.Join(s.opts.StateDir, "u", uid),
		keyLock: newCtxLock(), fetchLock: newCtxLock(), nonces: &nonceCache{now: s.now}, kick: make(chan struct{}, 1),
		sessions: map[sessKey]*heldSession{}, failed: map[string]fetchFailure{},
	}
	a.spool = spool.NewSpool(filepath.Join(a.dir, spoolFile))
	s.load(a)
	s.accounts[uid] = a
	go s.drainLoop(a)
	return a
}

// load reads a's record, key, snapshot and saved sessions. A record or key
// that cannot be read leaves a not enrolled and logs why. A record whose
// machine binding no longer holds keeps the record for status, drops the
// key and deletes the saved sessions. The sessions load after the
// snapshot, so adopting it leaves their pins and refusals as saved.
func (s *credStore) load(a *account) {
	rec, err := readRecord(filepath.Join(a.dir, recordFile))
	if err != nil {
		s.log.Error("the enrollment record could not be read, so this user counts as not enrolled", "uid", a.uid, "err", err)
		return
	}
	key, err := readKey(filepath.Join(a.dir, keyFile))
	if err != nil {
		s.log.Error("the key could not be read, so this user counts as not enrolled", "uid", a.uid, "err", err)
		return
	}
	if rec != nil && key != nil && dpop.Thumbprint(key.Public().(ed25519.PublicKey)) != rec.KeyThumbprint {
		s.log.Error("the key does not match the enrollment record, so this user counts as not enrolled", "uid", a.uid)
		return
	}
	a.rec = rec
	if rec != nil && rec.Kind != "" && !bindingHolds(*rec, s.machine) {
		s.log.Warn("the machine identity changed since this user enrolled, so the key is not used", "uid", a.uid)
		a.changed = true
		s.dropSessions(a)
		return
	}
	a.setKey(s, key)
	if rec != nil && rec.Kind != "" {
		s.resumeInFlight(a, *rec)
		s.loadSnapshot(a, *rec)
		s.loadSessions(a, *rec)
	}
}

// resumeInFlight turns a key check-in the last service left in flight into
// an unknown outcome: the next check-in waits until that attempt's nonce
// has expired, because its request may still reach the server. An attempt
// that carried no nonce can never move the counter, so it starts no wait.
func (s *credStore) resumeInFlight(a *account, rec enrollment) {
	if rec.InFlight == 0 {
		return
	}
	var until time.Time
	if !rec.InFlightNonceAt.IsZero() {
		until = rec.InFlightNonceAt.Add(lostAnswerWait)
	}
	s.log.Warn("the last service stopped while a key check-in was in flight, so this key's next check-in waits until its nonce has expired",
		"uid", a.uid, "counter", rec.InFlight, "until", until.UTC().Format(time.RFC3339))
	if err := a.settleAttempt(until); err != nil {
		s.log.Error("the wait after the stopped check-in could not be saved, so it lasts only until the service restarts", "uid", a.uid, "err", err)
	}
}

// setKey makes key a's key and builds the strazad client that signs with
// it. The caller holds a.mu or owns a alone.
func (a *account) setKey(s *credStore, key ed25519.PrivateKey) {
	a.key, a.api, a.proofs = key, nil, nil
	if key == nil || a.rec == nil || a.rec.ServerURL == "" {
		return
	}
	a.proofs = &proofTransport{base: s.base, key: key, origin: originOf(a.rec.ServerURL), nonces: a.nonces}
	a.api = signedClient(a.rec.ServerURL, a.proofs)
}

// ready returns a copy of a's enrollment and its signing client, or the
// refusal that says why a cannot check in.
func (a *account) ready() (enrollment, *agentguard.Client, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	switch {
	case a.changed:
		return enrollment{}, nil, &Refused{Status: http.StatusForbidden, Sentence: changedSentence(*a.rec)}
	case a.rec != nil && a.rec.Kind == "" && a.key != nil:
		// A record without a kind is the key Keygen made, and only an
		// agent's keygen makes one.
		return enrollment{}, nil, &Refused{Status: http.StatusForbidden, Sentence: onThisMachine(agentKeySentence, "")}
	case a.rec == nil || a.rec.Kind == "" || a.key == nil || a.api == nil:
		return enrollment{}, nil, &Refused{Status: http.StatusForbidden, Sentence: onThisMachine(notEnrolledSentence, "")}
	}
	return *a.rec, a.api, nil
}

// replace writes a new key and record for a and drops every session the
// old key held, on disk too. The caller holds a.keyLock.
func (s *credStore) replace(a *account, rec enrollment, key ed25519.PrivateKey) error {
	if err := os.MkdirAll(a.dir, 0o700); err != nil {
		return err
	}
	if err := writeKey(filepath.Join(a.dir, keyFile), key); err != nil {
		return fmt.Errorf("keep the key in %s: %w", a.dir, err)
	}
	if err := writeRecord(filepath.Join(a.dir, recordFile), rec); err != nil {
		return fmt.Errorf("keep the enrollment record in %s: %w", a.dir, err)
	}
	a.mu.Lock()
	old := a.sessions
	a.sessions, a.rec, a.changed, a.snap = map[sessKey]*heldSession{}, &rec, false, nil
	a.agentTok = cachedToken{}
	a.setKey(s, key)
	s.saveSessions(a)
	a.mu.Unlock()
	for _, hs := range old {
		hs.stopPush()
	}
	if rec.Kind != "" {
		s.loadSnapshot(a, rec)
	}
	return nil
}

// Status fills c's enrollment, sessions and spool. The API half sets the
// version, the mode and the caller's custody.
func (s *credStore) Status(c Caller) localapi.Status {
	a := s.account(c.UID)
	var st localapi.Status
	a.mu.Lock()
	if rec := a.rec; rec != nil && rec.Kind != "" {
		e := &localapi.Enrollment{
			ServerURL: rec.ServerURL, Kind: rec.Kind, Username: rec.Username, DeviceID: rec.DeviceID,
			KeyThumbprint: rec.KeyThumbprint, Custody: rec.Custody, SignedInAt: rec.SignedInAt,
			MachineChanged: a.changed,
		}
		if s.now().Before(rec.WaitUntil) {
			e.WaitUntil = rec.WaitUntil
		}
		st.Enrollment = e
	}
	for _, hs := range a.sessions {
		st.Sessions = append(st.Sessions, localapi.SessionStatus{
			Harness: hs.s.Harness, SessionID: hs.s.ID, User: hs.s.User, SnapshotID: hs.s.SnapshotID,
			ExpiresAt: hs.s.ExpiresAt, Push: hs.pushState(), Revoked: hs.revoked, PolicyRefused: hs.s.PolicyRefused,
			Attestation: hs.s.Attestation, AttestedAt: hs.s.IssuedAt,
		})
	}
	drainErr := a.drainErr
	a.mu.Unlock()
	sort.Slice(st.Sessions, func(i, j int) bool { return st.Sessions[i].ExpiresAt.After(st.Sessions[j].ExpiresAt) })
	st.Spool = a.spoolStatus(drainErr)
	return st
}

// ctxLock is a mutex whose lock gives up when the context ends.
type ctxLock chan struct{}

func newCtxLock() ctxLock { return make(ctxLock, 1) }

// lock takes l, or returns ctx's error when ctx ends first.
func (l ctxLock) lock(ctx context.Context) error {
	select {
	case l <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (l ctxLock) unlock() { <-l }
