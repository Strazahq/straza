package agentguard

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/strazahq/straza/internal/agentguard/trace"
	"github.com/strazahq/straza/internal/policy"
	"github.com/strazahq/straza/internal/sameorigin"
)

// Home returns the straza user-mode state root: $STRAZA_HOME
// or ~/.straza, the user layer. The managed layout is elsewhere.
func Home() (string, error) {
	if v := os.Getenv("STRAZA_HOME"); v != "" {
		return v, nil
	}
	h, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("locate home: %w", err)
	}
	return filepath.Join(h, ".straza"), nil
}

// Config is the persisted straza configuration (~/.straza/config.yaml).
type Config struct {
	ServerURL string `yaml:"serverUrl"`
	// SnapshotKeys are the pinned base64 ed25519 snapshot verification keys
	// (fetched at enroll from /.well-known/straza/snapshot-keys.json).
	SnapshotKeys map[string]string `yaml:"snapshotKeys"`
	// SnapshotLagSeconds bounds how far behind the active policy a busy
	// session may run: the detached post-decision sync checks the server for
	// a newer snapshot at most this often (cheap conditional GET, 304 when
	// unchanged). 0 = default 30. Lower it on standalone for near-instant
	// propagation; raise it in huge fleets to bound 304 traffic.
	SnapshotLagSeconds int `yaml:"snapshotLagSeconds,omitempty"`
	// Trace is the install's diagnostic trace level: "" or "journal" (the
	// default: the always-on content-free decision journal, and the user's
	// `straza trace on` window may raise it to debug), "off" (nothing is
	// written and the window cannot be opened; the error log errors.jsonl is
	// unaffected and always written), or "debug" (debug forced on, no window
	// needed). The managed config wins whole-file, so an operator's value
	// here applies to every user of the install.
	Trace string `yaml:"trace,omitempty"`
}

// Session is the per-session client state (~/.straza/state/session.json).
// It caches the resolved subject so tool.pre evaluates locally with no
// network.
type Session struct {
	SessionID    string   `json:"sessionId"`
	SessionToken string   `json:"sessionToken"`
	SnapshotID   string   `json:"snapshotId"`
	User         string   `json:"user"`
	Roles        []string `json:"roles"`
	Attestation  string   `json:"attestation"`
	Harness      string   `json:"harness"`
	// Identity typology (spec/policyset revision 9), adopted from checkin so
	// the local subject matches identity-scoped sets like the server does.
	UserType   string    `json:"userType,omitempty"`
	AgencyMode string    `json:"agencyMode,omitempty"`
	SwarmID    string    `json:"swarmId,omitempty"`
	IssuedAt   time.Time `json:"issuedAt"`
	ExpiresAt  time.Time `json:"expiresAt"`
	// PolicyRefused is the server's sentence from its refusal to send a
	// policy newer than the one SnapshotID pins, and PolicyDeadline is the
	// session time this client held when that refusal began. Past the
	// deadline the offline gate denies with the sentence until a fetch
	// succeeds (settlePolicy). Both are empty when no refusal is outstanding.
	PolicyRefused  string    `json:"policyRefused,omitempty"`
	PolicyDeadline time.Time `json:"policyDeadline,omitzero"`
}

// Subject builds the policy subject from the cached session.
func (s Session) Subject() policy.Subject {
	return policy.Subject{
		User:        s.User,
		Roles:       s.Roles,
		Attestation: s.Attestation,
		// Honest false until a device-certificate factor exists.
		// The server subject says the same, so a
		// `require: {deviceCert}` rule decides identically on both PEPs.
		DeviceCert: false,
		Harness:    s.Harness,
		UserType:   s.UserType,
		AgencyMode: s.AgencyMode,
		SwarmID:    s.SwarmID,
	}
}

// adoptIdentity copies the server's freshly resolved roles onto the
// session. The server re-resolves them on every /v1/checkin, so this is the
// ONLY way an IdM role grant or removal reaches local hook decisions: the
// subject the PDP evaluates against is built from these fields (Subject).
//
// Roles is the well-formedness signal rather than len(Roles): the server always
// sends a non-nil array (empty for a user who holds no roles). A response
// carrying no roles at all is an identity-less reply (an older server, or a
// proxy that dropped the field) and is ignored on purpose: clearing the role
// set would UNMATCH a restrictive `match.roles` PolicySet and widen access
// rather than fail closed.
func adoptIdentity(ses *Session, resp CheckinResponse) {
	if resp.Roles == nil {
		return
	}
	ses.Roles = resp.Roles
	// Typology rides the same well-formedness gate. Empty fields (whether
	// from an unclassified user, a reclassification, or an older server that
	// predates them) adopt as empty: identity-scoped sets then simply do
	// not match, which IS the unclassified semantic (SPEC.md §1.4). No
	// stale-value retention: the server's resolution is the truth.
	ses.UserType, ses.AgencyMode, ses.SwarmID = resp.UserType, resp.AgencyMode, resp.SwarmID
}

// adoptCheckin overwrites the session with the state a fresh check-in minted:
// new id, token, expiry, attestation (session-scoped), push wiring, the
// re-resolved identity, and the blob-first adopted snapshot pin. Shared by
// the mid-session re-acquire lanes (LocalPDP hook path and the daemon) so a
// re-established session carries exactly what a session start would.
func adoptCheckin(ctx context.Context, client *Client, store *Store, cfg Config, ses *Session, resp CheckinResponse, now time.Time) {
	held := ses.ExpiresAt
	ses.SessionID = resp.SessionID
	ses.SessionToken = resp.SessionToken
	ses.IssuedAt = now
	ses.ExpiresAt = now.Add(time.Duration(resp.ExpiresIn) * time.Second)
	ses.Attestation = resp.Attestation
	adoptIdentity(ses, resp)
	adopted, err := adoptSnapshot(ctx, client, resp.SessionToken, store, cfg, ses.SnapshotID, resp.SnapshotID)
	ses.SnapshotID = adopted
	ses.settlePolicy(resp.SnapshotID, err, held)
}

// settlePolicy records what a renewal learned about the policy s enforces,
// and it is the one place that rule lives. reported is the snapshot id the
// renewal named, err the result of adopting it, and held the session time s
// had before the renewal. A refusal (refusalReason) of a policy other than
// the one s pins starts or continues a refusal: its reason is kept, and the
// deadline stays at the session time held when the refusal began, so
// renewing the token never extends it. Adopting the reported policy, or
// already holding it, ends the refusal. A renewal that names no policy, as a
// server with no snapshot loaded answers, a transport failure and a 5xx
// leave the state as it was, so an outage alone never stops work. It
// reports whether err was a refusal.
func (s *Session) settlePolicy(reported string, err error, held time.Time) bool {
	switch why := refusalReason(err); {
	case reported == "":
		return false
	case err == nil || s.SnapshotID == reported:
		s.PolicyRefused, s.PolicyDeadline = "", time.Time{}
	case why != "":
		if s.PolicyDeadline.IsZero() {
			s.PolicyDeadline = held
		}
		s.PolicyRefused = why
		return true
	}
	return false
}

// refusalReason words why a fetch of a newer policy failed, in sentences a
// person can act on, when the failure counts as a refusal: a server answer
// below 500, a redirect the client would not follow, a redirect loop, or a
// snapshot the pinned keys cannot verify. It returns "" for a transport
// failure or a 5xx.
func refusalReason(err error) string {
	var refuse *StatusError
	var redirect *sameorigin.RedirectError
	switch {
	case err == nil:
		return ""
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
		// A managed config wins over the one enroll writes (LoadConfig), so
		// there only the managed install run with --server pins new keys.
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

// Identity is the enrolled identity (~/.straza/state/identity.json).
type Identity struct {
	// IDToken is the login-time OIDC token; it expires within minutes and is
	// kept only as the checkin fallback for state files written before device
	// tokens existed.
	IDToken  string `json:"idToken"`
	DeviceID string `json:"deviceId"`
	Username string `json:"username"`
	// DeviceToken is the long-lived enroll credential, what actually
	// starts sessions, so enrolling is once per device.
	DeviceToken string `json:"deviceToken,omitempty"`
	// Headless marks a headless NHI enrollment (headless.go modes): sessions are
	// deviceless and each start mints a fresh ID token from the local
	// credential, and no device token exists.
	Headless string `json:"headless,omitempty"`
}

// Store is the straza on-disk state manager. Always handled by pointer (it
// carries the lazily built trace logger and its sync.Once).
type Store struct {
	root string

	traceOnce sync.Once
	tracer    *trace.Logger
}

// OpenStore returns the state store rooted at Home().
func OpenStore() (*Store, error) {
	root, err := Home()
	if err != nil {
		return nil, err
	}
	return &Store{root: root}, nil
}

func (s *Store) configPath() string        { return filepath.Join(s.root, "config.yaml") }
func (s *Store) statePath(n string) string { return filepath.Join(s.root, "state", n) }
func (s *Store) snapshotPath() string      { return filepath.Join(s.root, "state", "snapshot.cbor") }

// LoadConfig reads the managed system config when present, falling back
// to the user config.yaml. The root-owned config wins so a user-writable file
// cannot re-point enforcement at a rogue server.
func (s *Store) LoadConfig() (Config, error) {
	var c Config
	if raw, err := os.ReadFile(ManagedConfigPath()); err == nil { // #nosec G304 -- fixed managed layout path
		if err := yaml.Unmarshal(raw, &c); err != nil {
			return c, fmt.Errorf("parse managed config: %w", err)
		}
		return c, nil
	}
	raw, err := os.ReadFile(s.configPath())
	if err != nil {
		return c, fmt.Errorf("not enrolled (run `straza enroll`): %w", err)
	}
	if err := yaml.Unmarshal(raw, &c); err != nil {
		return c, fmt.Errorf("parse config: %w", err)
	}
	return c, nil
}

// SaveConfig writes config.yaml (0600), atomically.
func (s *Store) SaveConfig(c Config) error {
	raw, err := yaml.Marshal(c)
	if err != nil {
		return err
	}
	return writeFileAtomic(s.configPath(), raw, 0o600)
}

// LoadSession / SaveSession persist the session token state.
func (s *Store) LoadSession() (Session, error) {
	var ses Session
	return ses, s.readJSON(s.statePath("session.json"), &ses)
}

// SaveSession writes session.json (0600).
func (s *Store) SaveSession(ses Session) error {
	return s.writeJSON(s.statePath("session.json"), ses)
}

// DropSession removes the session state so the next hook fails closed (the
// kill switch). Absent state is not an error.
func (s *Store) DropSession() error {
	err := os.Remove(s.statePath("session.json"))
	if os.IsNotExist(err) {
		return nil
	}
	return err
}

// Revocation is the local marker left behind when the session was killed by
// the server (push or refused refresh), as opposed to merely expiring. Hooks
// use it to tell the agent why tool calls are denied, and the next successful
// check-in clears it.
type Revocation struct {
	Reason string    `json:"reason"`
	At     time.Time `json:"at"`
}

// MarkRevoked records why the session was killed (0600). Best-effort callers
// may ignore the error; the marker only improves messaging.
func (s *Store) MarkRevoked(reason string) error {
	return s.writeJSON(s.statePath("revocation.json"), Revocation{Reason: reason, At: time.Now()})
}

// LoadRevocation returns the revocation marker, if any.
func (s *Store) LoadRevocation() (Revocation, error) {
	var r Revocation
	return r, s.readJSON(s.statePath("revocation.json"), &r)
}

// ClearRevocation removes the marker (on the next successful checkin).
func (s *Store) ClearRevocation() error {
	err := os.Remove(s.statePath("revocation.json"))
	if os.IsNotExist(err) {
		return nil
	}
	return err
}

// LoadIdentity / SaveIdentity persist the enrolled identity.
func (s *Store) LoadIdentity() (Identity, error) {
	var id Identity
	return id, s.readJSON(s.statePath("identity.json"), &id)
}

// SaveIdentity writes identity.json (0600).
func (s *Store) SaveIdentity(id Identity) error {
	return s.writeJSON(s.statePath("identity.json"), id)
}

// SaveSnapshot writes the signed snapshot bytes to disk, atomically: a torn
// snapshot.cbor is just another fail-closed brick, so a reader (or a crash)
// never sees a half-written blob.
func (s *Store) SaveSnapshot(signed []byte) error {
	return writeFileAtomic(s.snapshotPath(), signed, 0o600)
}

// LoadSnapshot reads the cached signed snapshot bytes.
func (s *Store) LoadSnapshot() ([]byte, error) {
	return os.ReadFile(s.snapshotPath())
}

// SpoolPath returns the audit spool file path.
func (s *Store) SpoolPath() string { return s.statePath("audit-spool.jsonl") }

func (s *Store) readJSON(path string, v any) error {
	raw, err := os.ReadFile(path) // #nosec G304 -- our own state files under the straza home
	if err != nil {
		return err
	}
	return json.Unmarshal(raw, v)
}

func (s *Store) writeJSON(path string, v any) error {
	raw, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return writeFileAtomic(path, raw, 0o600)
}

// writeFileAtomic writes data to path via a same-directory temp file that is
// fsync'd and then renamed over the target, so a reader (or a crash) never sees
// a half-written state file. os.Rename replaces the destination atomically on
// every platform straza targets. The temp file is removed best-effort on any
// error, so a failed write leaves no .tmp residue.
func writeFileAtomic(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	if err := f.Chmod(perm); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return err
	}
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}
