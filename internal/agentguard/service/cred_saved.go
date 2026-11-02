package service

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/strazahq/straza/internal/agentguard/localapi"
)

// savedNotWritten is the log line of a sessions file that could not be
// written.
const savedNotWritten = "the open sessions could not be saved, so a restart of the service would make their windows start again. " +
	"Free space in the state directory or fix its ownership"

// savedUnreadable is the log line of a sessions file that cannot be read
// or parsed, with the file's path and the error.
const savedUnreadable = "Straza could not read the saved sessions in %s (%v), so open windows must start again. " +
	"Check that the straza-service account owns that file and its folder."

// savedSessions is the sessions file of one uid: the sessions its key held,
// so a restarted service resumes every open window without a check-in.
// Key is the thumbprint of the key the sessions are bound to, and Binding
// the machine binding of the enrollment they belong to, which every
// enrollment makes anew, an agent's that keeps its key included.
type savedSessions struct {
	Key      string         `json:"key_thumbprint"`
	Binding  string         `json:"binding"`
	Sessions []savedSession `json:"sessions"`
}

// savedSession is one held session as the sessions file keeps it: the
// window, the server session and its token, the revocation, and what the
// window's next decision and start need. Its push stream and its last
// renewal are not kept.
type savedSession struct {
	Harness        localapi.Harness `json:"harness"`
	ID             string           `json:"session_id"`
	Token          string           `json:"token,omitempty"`
	Revoked        string           `json:"revoked,omitempty"`
	IssuedAt       time.Time        `json:"issued_at"`
	ExpiresAt      time.Time        `json:"expires_at"`
	User           string           `json:"user,omitempty"`
	Roles          []string         `json:"roles"`
	Attestation    string           `json:"attestation,omitempty"`
	UserType       string           `json:"user_type,omitempty"`
	AgencyMode     string           `json:"agency_mode,omitempty"`
	SwarmID        string           `json:"swarm_id,omitempty"`
	SnapshotID     string           `json:"snapshot_id,omitempty"`
	PolicyRefused  string           `json:"policy_refused,omitempty"`
	PolicyDeadline time.Time        `json:"policy_deadline,omitzero"`
	Packs          []localapi.Pack  `json:"packs,omitempty"`
}

// saveSessions writes a's held sessions to its sessions file. Every change
// a restart must see calls it: a start, a renewal, a re-acquire, a new
// pin or policy refusal, the forget sweep and a new key. The caller holds
// a.mu or owns a alone, so the file follows the changes in their order. A
// service that has begun to stop writes nothing, because the next service
// may already own the state directory. Only a revocation writes through,
// with writeSessions.
func (s *credStore) saveSessions(a *account) {
	if s.ctx.Err() == nil {
		s.writeSessions(a)
	}
}

// writeSessions writes a's held sessions, bound to a's key and enrollment,
// whether or not the service is stopping. The caller holds a.mu or owns a
// alone. A failed write is logged and the sessions stay held in memory.
func (s *credStore) writeSessions(a *account) {
	if a.rec == nil {
		return
	}
	saved := savedSessions{Key: a.rec.KeyThumbprint, Binding: a.rec.Binding, Sessions: make([]savedSession, 0, len(a.sessions))}
	for _, hs := range a.sessions {
		v := hs.s
		saved.Sessions = append(saved.Sessions, savedSession{
			Harness: v.Harness, ID: v.ID, Token: hs.token, Revoked: hs.revoked, IssuedAt: v.IssuedAt, ExpiresAt: v.ExpiresAt,
			User: v.User, Roles: v.Roles, Attestation: v.Attestation, UserType: v.UserType, AgencyMode: v.AgencyMode, SwarmID: v.SwarmID,
			SnapshotID: v.SnapshotID, PolicyRefused: v.PolicyRefused, PolicyDeadline: v.PolicyDeadline, Packs: v.Packs,
		})
	}
	raw, err := json.Marshal(saved)
	if err == nil {
		err = writeFileAtomic(filepath.Join(a.dir, sessionsFile), raw)
	}
	if err != nil {
		s.log.Error(savedNotWritten, "uid", a.uid, "err", err)
	}
}

// loadSessions holds the sessions a's last service saved for rec, and
// starts the push stream of each live one, so a kill switch reaches it
// again. A session past the forget rule is dropped, and so are sessions
// saved under another key or another enrollment, which replace writes
// after the new record, so a crash between the two leaves them to this
// check. A revoked session stays revoked. A session with no policy refusal
// is pinned to the snapshot a holds, as adoptSnap pins it, so a lost save
// cannot leave a pin that fetches on every call. Anything dropped or
// pinned is saved at once, so no forgotten session's token stays on disk.
// A file that cannot be read or parsed is logged with its sentence, and a
// starts with no sessions. The caller owns a alone.
func (s *credStore) loadSessions(a *account, rec enrollment) {
	s.removeSavedTemps(a)
	path := filepath.Join(a.dir, sessionsFile)
	raw, err := os.ReadFile(path) // #nosec G304 -- the service's own state directory
	if errors.Is(err, fs.ErrNotExist) {
		return
	}
	var saved savedSessions
	if err == nil {
		err = json.Unmarshal(raw, &saved)
	}
	if err != nil {
		s.log.Error(fmt.Sprintf(savedUnreadable, path, err), "uid", a.uid)
		return
	}
	changed := saved.Key != rec.KeyThumbprint || saved.Binding != rec.Binding
	if changed {
		s.log.Info("the saved sessions belong to an earlier enrollment of this user, so they are not resumed", "uid", a.uid)
		saved.Sessions = nil
	}
	now := s.now()
	var live []*heldSession
	for _, v := range saved.Sessions {
		if now.Sub(v.ExpiresAt) > forgetAfter {
			changed = true
			continue
		}
		if a.snap != nil && v.PolicyRefused == "" && v.SnapshotID != a.snap.id {
			v.SnapshotID, changed = a.snap.id, true
		}
		hs := &heldSession{lock: newCtxLock(), token: v.Token, revoked: v.Revoked, s: Session{
			UID: a.uid, Harness: v.Harness, ServerURL: rec.ServerURL, ID: v.ID, User: v.User, Roles: v.Roles,
			Attestation: v.Attestation, UserType: v.UserType, AgencyMode: v.AgencyMode, SwarmID: v.SwarmID,
			SnapshotID: v.SnapshotID, IssuedAt: v.IssuedAt, ExpiresAt: v.ExpiresAt,
			PolicyRefused: v.PolicyRefused, PolicyDeadline: v.PolicyDeadline, Packs: v.Packs,
		}}
		a.sessions[sessKey{v.Harness.Name, v.Harness.Session}] = hs
		if hs.revoked == "" && hs.token != "" && now.Before(hs.s.ExpiresAt) {
			live = append(live, hs)
		}
	}
	if changed {
		s.saveSessions(a)
	}
	for _, hs := range live {
		s.startPush(a, hs)
	}
}

// dropSessions deletes a's sessions file and any temp file a crash left
// beside it, when a's machine binding no longer holds, as on a machine
// cloned from an image after people enrolled. Sessions minted on another
// machine are so never resumed here and never come back. The account's
// every call already answers the machine-changed sentence before it looks
// for a session.
func (s *credStore) dropSessions(a *account) {
	s.removeSavedTemps(a)
	err := os.Remove(filepath.Join(a.dir, sessionsFile))
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		s.log.Error("the saved sessions of a machine whose identity changed could not be deleted. They are never used, and the next enrollment replaces them",
			"uid", a.uid, "err", err)
	}
}

// removeSavedTemps removes the temp files of sessions file writes that a
// crash left in a's directory, because they can hold tokens. The caller
// owns a alone.
func (s *credStore) removeSavedTemps(a *account) {
	entries, _ := os.ReadDir(a.dir)
	for _, e := range entries {
		if !strings.HasPrefix(e.Name(), "."+sessionsFile+".tmp-") {
			continue
		}
		if err := os.Remove(filepath.Join(a.dir, e.Name())); err != nil {
			s.log.Warn("a temp file of the saved sessions could not be removed, so it may still hold session tokens. "+
				"Delete it, or fix the ownership of the state directory", "uid", a.uid, "err", err)
		}
	}
}

// resumeAccounts loads every account that saved sessions, before the
// service answers anyone. Each live session's push stream is then open
// again within seconds of a restart, as it was before, instead of from
// its user's next call, so a revocation reaches a window that is quiet.
func (s *credStore) resumeAccounts() {
	users := filepath.Join(s.opts.StateDir, "u")
	entries, err := os.ReadDir(users)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		s.log.Error("the users' folders in the state directory could not be listed, so each user's saved sessions resume at that user's next call",
			"dir", users, "err", err)
	}
	for _, e := range entries {
		if !isUID(e.Name()) || !e.IsDir() {
			continue
		}
		if _, err := os.Lstat(filepath.Join(users, e.Name(), sessionsFile)); err == nil {
			s.account(e.Name())
		}
	}
}
