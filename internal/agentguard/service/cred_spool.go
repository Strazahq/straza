package service

import (
	"context"
	"os"
	"path/filepath"
	"time"

	"github.com/strazahq/straza/internal/agentguard/localapi"
	"github.com/strazahq/straza/internal/agentguard/spool"
)

// drainTimeout bounds one drain of a spool.
const drainTimeout = 20 * time.Second

// forgetAfter is how long past its expiry a held session is kept. Until
// then a window's next call re-acquires it however long it sat unused, and
// a revoked window keeps its refusal until a start. A window nobody used
// for that long is forgotten, revoked or not.
const forgetAfter = 7 * 24 * time.Hour

// Record appends one audit record of type ceType to c's spool and starts a
// drain.
func (s *credStore) Record(c Caller, ceType string, data map[string]any) error {
	a := s.account(c.UID)
	if err := os.MkdirAll(a.dir, 0o700); err != nil {
		return err
	}
	if err := a.spool.AppendCE(ceType, data); err != nil {
		return err
	}
	a.kickDrain()
	return nil
}

// The service's accounts keep the ledger the API half notes refusals in.
var _ refusedLedger = (*credStore)(nil)

// NoteRefused counts one call of c that the API half refused before it
// could be recorded in c's loss ledger, and starts a drain to report it.
func (s *credStore) NoteRefused(c Caller, call string) {
	a := s.account(c.UID)
	if os.MkdirAll(a.dir, 0o700) != nil {
		return
	}
	a.spool.NoteRefused(call)
	a.kickDrain()
}

// kickDrain asks a's drain loop to drain now.
func (a *account) kickDrain() {
	select {
	case a.kick <- struct{}{}:
	default:
	}
}

// drainLoop drains a's spool every drainEvery, after a record and at a
// session start, until the service ends. Each tick also forgets a's
// sessions that lapsed long ago.
func (s *credStore) drainLoop(a *account) {
	t := time.NewTicker(s.drainEvery)
	defer t.Stop()
	for {
		select {
		case <-s.ctx.Done():
			return
		case <-t.C:
			s.forgetLapsed(a)
		case <-a.kick:
		}
		s.drainOnce(a)
	}
}

// forgetLapsed drops a's sessions whose expiry passed more than forgetAfter
// ago, revoked or not, on disk too, and ends any push stream they still
// hold. A forgotten window answers the no-session refusal, and its next
// session start checks in again. A kept session past refreshRefusedAfter
// loses its knowledge packs, because only a start that reuses a live
// window reads them and the re-acquire it is due brings them back, so a
// week of windows does not grow every save.
func (s *credStore) forgetLapsed(a *account) {
	now := s.now()
	var gone []*heldSession
	slimmed := false
	a.mu.Lock()
	for k, hs := range a.sessions {
		switch {
		case now.Sub(hs.s.ExpiresAt) > forgetAfter:
			delete(a.sessions, k)
			gone = append(gone, hs)
		case now.Sub(hs.s.ExpiresAt) > refreshRefusedAfter && hs.s.Packs != nil:
			hs.s.Packs, slimmed = nil, true
		}
	}
	if len(gone) > 0 || slimmed {
		s.saveSessions(a)
	}
	a.mu.Unlock()
	for _, hs := range gone {
		hs.stopPush()
	}
}

// drainOnce uploads a's spooled records with any live session of a. With no
// live session the records wait for the next one. A failed drain keeps
// every record, and the server dedupes a record sent twice.
func (s *credStore) drainOnce(a *account) {
	if !spoolHasRecords(a.dir) {
		return
	}
	_, api, err := a.ready()
	if err != nil {
		return
	}
	token := a.liveToken(s.now())
	if token == "" {
		return
	}
	ctx, cancel := context.WithTimeout(s.ctx, drainTimeout)
	defer cancel()
	n, err := a.spool.Drain(ctx, api, token)
	msg := ""
	if err != nil {
		msg = err.Error()
		s.log.Warn("the audit drain failed, so its records stay in the spool for the next drain", "uid", a.uid, "uploaded", n, "err", err)
	}
	a.mu.Lock()
	a.drainErr = msg
	a.mu.Unlock()
}

// liveToken returns the token of a's session with the most time left, ""
// when none is live.
func (a *account) liveToken(now time.Time) string {
	a.mu.Lock()
	defer a.mu.Unlock()
	var token string
	var until time.Time
	for _, hs := range a.sessions {
		if hs.revoked == "" && hs.token != "" && now.Before(hs.s.ExpiresAt) && hs.s.ExpiresAt.After(until) {
			token, until = hs.token, hs.s.ExpiresAt
		}
	}
	return token
}

// spoolHasRecords reports whether the spool in dir holds a live or parked
// file or a loss ledger to report, so an idle spool costs no drain.
func spoolHasRecords(dir string) bool {
	if fi, err := os.Stat(filepath.Join(dir, spoolFile)); err == nil && fi.Size() > 0 {
		return true
	}
	parked, _ := filepath.Glob(filepath.Join(dir, "audit-pending-*.jsonl"))
	return len(parked) > 0 || spool.Dropped(dir).Events > 0
}

// spoolStatus is a's spool as status shows it.
func (a *account) spoolStatus(drainErr string) localapi.SpoolStatus {
	st := localapi.SpoolStatus{LastError: drainErr}
	st.Pending, _ = a.spool.Pending()
	st.Dropped = spool.Dropped(a.dir).Lost()
	if fi, err := os.Stat(filepath.Join(a.dir, spool.LastDrainMarkerName)); err == nil {
		st.LastDrain = fi.ModTime()
	}
	return st
}
