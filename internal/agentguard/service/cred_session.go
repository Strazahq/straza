package service

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/strazahq/straza/internal/agentguard"
	"github.com/strazahq/straza/internal/agentguard/localapi"
)

// maxWindows bounds the windows one account holds, so one user cannot grow
// the session table, its saved copy and the time each save holds a.mu
// without limit. At the cap a new window takes the place of a lapsed one.
const maxWindows = 256

// tooManyWindows is the refusal of a start for a new window when
// maxWindows unexpired windows are held.
const tooManyWindows = "Straza refused to start this session because your user already holds %d live sessions on this machine. " +
	"A session nobody uses stops counting within five minutes, so start this session again in a few minutes, " +
	"and tell your Straza admin if this keeps happening."

// startRefreshAfter is how old a live window's token must be before a start
// that reuses the window refreshes it first. So a loop of starts on held
// windows costs at most one refresh and one save per window in that time,
// while a resumed window, whose token time is unknown, is refreshed at once.
const startRefreshAfter = 10 * time.Second

// sessKey names one harness window of an account: the harness and the
// harness's own session id.
type sessKey struct{ name, id string }

// heldSession is one server session the service holds. The account's mu
// guards s, token, revoked and minted, when this service last got token,
// zero for a session it resumed. lock serializes the renewals of the
// session, and pushMu guards its push stream.
type heldSession struct {
	lock ctxLock

	s       Session
	token   string
	revoked string
	minted  time.Time

	pushMu     sync.Mutex
	push       string
	cancelPush context.CancelFunc
}

// Session returns c's session for h, renewing it when 120 seconds or less
// remain. With start set it checks in when h has no live session, and it
// refreshes a live one whose token is startRefreshAfter old before reusing
// it, so a session the server dropped is found at the start: a 401
// re-acquires it and a 403 revokes it. A revoked window is refused without
// start and checked in again with it.
func (s *credStore) Session(ctx context.Context, c Caller, h localapi.Harness, start bool) (Session, error) {
	a := s.account(c.UID)
	if _, _, err := a.ready(); err != nil {
		return Session{}, err
	}
	hs := a.find(h)
	switch {
	case hs == nil && !start:
		return Session{}, &Refused{Status: http.StatusConflict, Sentence: noSessionSentence}
	case hs != nil:
		rev := a.revocation(hs)
		if rev != "" && !start {
			return Session{}, &Refused{Status: http.StatusForbidden, Sentence: revokedSentence(rev)}
		}
		if rev == "" {
			before := a.copyOf(hs).ID
			ses := s.renew(ctx, a, hs, start && !a.mintedWithin(hs, s.now(), startRefreshAfter))
			rev = a.revocation(hs)
			switch {
			case rev != "" && !start:
				return Session{}, &Refused{Status: http.StatusForbidden, Sentence: revokedSentence(rev)}
			case rev == "" && (!start || s.now().Before(ses.ExpiresAt)):
				ses.Reused = start && ses.ID == before
				return ses, nil
			}
		}
	}
	return s.start(ctx, a, h)
}

// find returns the held session of h: the window h.Session names, or for an
// empty h.Session the newest session of h.Name, one that is not revoked
// first.
func (a *account) find(h localapi.Harness) *heldSession {
	a.mu.Lock()
	defer a.mu.Unlock()
	if h.Session != "" {
		return a.sessions[sessKey{h.Name, h.Session}]
	}
	var best *heldSession
	for k, hs := range a.sessions {
		if k.name == h.Name && (best == nil || preferred(hs, best)) {
			best = hs
		}
	}
	return best
}

// preferred reports whether x is a better pick than y for a caller that
// names no window: a live session over a revoked one, then the newer.
func preferred(x, y *heldSession) bool {
	if (x.revoked == "") != (y.revoked == "") {
		return x.revoked == ""
	}
	return x.s.IssuedAt.After(y.s.IssuedAt)
}

// held returns the account and held session of ses, or the refusal when
// the window holds none or its session was revoked.
func (s *credStore) held(ses Session) (*account, *heldSession, error) {
	a := s.account(ses.UID)
	a.mu.Lock()
	defer a.mu.Unlock()
	hs := a.sessions[sessKey{ses.Harness.Name, ses.Harness.Session}]
	switch {
	case hs == nil:
		return nil, nil, &Refused{Status: http.StatusConflict, Sentence: noSessionSentence}
	case hs.revoked != "":
		return nil, nil, &Refused{Status: http.StatusForbidden, Sentence: revokedSentence(hs.revoked)}
	}
	return a, hs, nil
}

func (a *account) revocation(hs *heldSession) string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return hs.revoked
}

// copyOf returns the copy of hs the API half sees.
func (a *account) copyOf(hs *heldSession) Session {
	a.mu.Lock()
	defer a.mu.Unlock()
	return hs.s
}

// tokenOf returns hs's session token, "" for a revoked session.
func (a *account) tokenOf(hs *heldSession) string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return hs.token
}

// underKey takes a's key lock with ctx and runs run with it. Once the lock
// is held, run goes on to its end even when ctx ends, bounded by
// checkinTimeout and the service's life, because a check-in given up on
// can still reach the server. run may release the lock early, and the lock
// is released when run returns. When ctx ends first, while queued for the
// lock or while run goes on, the caller gets ctx's error, and run's outcome
// lands on the account.
func (s *credStore) underKey(ctx context.Context, a *account, run func(ctx context.Context, release func()) (Session, error)) (Session, error) {
	if err := a.keyLock.lock(ctx); err != nil {
		return Session{}, fmt.Errorf("the check-in was still queued behind another one when this request's time ran out: %w", err)
	}
	var once sync.Once
	release := func() { once.Do(a.keyLock.unlock) }
	type result struct {
		ses Session
		err error
	}
	done := make(chan result, 1)
	go func() {
		defer release()
		rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), s.checkinTimeout)
		defer cancel()
		stop := context.AfterFunc(s.ctx, cancel)
		defer stop()
		ses, err := run(rctx, release)
		done <- result{ses, err}
	}()
	select {
	case r := <-done:
		return r.ses, r.err
	case <-ctx.Done():
		return Session{}, fmt.Errorf("the check-in had not finished when this request's time ran out, and it goes on in the background: %w", ctx.Err())
	}
}

// checkin checks in once for h through a's enrollment: the key lane for a
// person, the agent's lane for an agent. The caller holds a.keyLock.
func (s *credStore) checkin(ctx context.Context, a *account, h localapi.Harness) (enrollment, agentguard.CheckinResponse, error) {
	rec, _, err := a.ready()
	if err != nil {
		return rec, agentguard.CheckinResponse{}, err
	}
	if rec.Kind == localapi.KindAgent {
		resp, err := s.agentCheckin(ctx, a, rec, h)
		return rec, resp, err
	}
	resp, err := s.keyCheckin(ctx, a, h)
	return rec, resp, err
}

// start checks in a new session for h, or reuses the live one another
// start made while this one waited for the key. A new window past
// maxWindows is refused before the check-in.
func (s *credStore) start(ctx context.Context, a *account, h localapi.Harness) (Session, error) {
	return s.underKey(ctx, a, func(rctx context.Context, release func()) (Session, error) {
		if hs := a.find(h); hs != nil && a.revocation(hs) == "" {
			if ses := a.copyOf(hs); s.now().Before(ses.ExpiresAt) {
				ses.Reused = true
				return ses, nil
			}
		}
		if err := s.roomFor(a, h); err != nil {
			release()
			return Session{}, err
		}
		rec, resp, err := s.checkin(rctx, a, h)
		if err != nil {
			release()
			return Session{}, checkinRefusal(rec.ServerURL, err)
		}
		hs := s.holdStart(a, rec, h, resp)
		release()
		return s.finishStart(rctx, a, hs, resp), nil
	})
}

// roomFor makes room for a start of a new window of a. A window a holds,
// live or revoked, always has room. At maxWindows the lapsed window that
// expired first is forgotten, one that is not revoked before a revoked
// one, so a revoked window keeps its refusal while another can go, and the
// start is refused only when every held window is unexpired. The start's
// own save writes the change. The caller holds a.keyLock, under which
// every start adds its window.
func (s *credStore) roomFor(a *account, h localapi.Harness) error {
	now := s.now()
	a.mu.Lock()
	if _, held := a.sessions[sessKey{h.Name, h.Session}]; held || len(a.sessions) < maxWindows {
		a.mu.Unlock()
		return nil
	}
	var oldest sessKey
	var gone *heldSession
	for k, hs := range a.sessions {
		if now.Before(hs.s.ExpiresAt) {
			continue
		}
		if gone == nil || forgetsFirst(hs, gone) {
			oldest, gone = k, hs
		}
	}
	if gone == nil {
		a.mu.Unlock()
		return &Refused{Status: http.StatusTooManyRequests, Sentence: fmt.Sprintf(tooManyWindows, maxWindows)}
	}
	delete(a.sessions, oldest)
	a.mu.Unlock()
	gone.stopPush()
	return nil
}

// forgetsFirst reports whether roomFor forgets the lapsed window x before
// y: one that is not revoked first, then the one that expired first. The
// caller holds the account's mu.
func forgetsFirst(x, y *heldSession) bool {
	if (x.revoked == "") != (y.revoked == "") {
		return x.revoked == ""
	}
	return x.s.ExpiresAt.Before(y.s.ExpiresAt)
}

// mintedWithin reports whether this service got hs's token less than d
// before now.
func (a *account) mintedWithin(hs *heldSession, now time.Time, d time.Duration) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return !hs.minted.IsZero() && now.Sub(hs.minted) < d
}

// holdStart holds the session a check-in for h minted, before the key lock
// is released, so a start queued behind this one reuses it. It pins the
// snapshot the account holds, or the reported one until finishStart has
// fetched it. A new window carries the account's outstanding policy
// refusal, so a new session never extends one.
func (s *credStore) holdStart(a *account, rec enrollment, h localapi.Harness, resp agentguard.CheckinResponse) *heldSession {
	now := s.now()
	ses := Session{
		UID: a.uid, Harness: h, ServerURL: rec.ServerURL, ID: resp.SessionID, User: resp.User,
		Attestation: resp.Attestation, IssuedAt: now, ExpiresAt: now.Add(time.Duration(resp.ExpiresIn) * time.Second),
		Packs: packsOf(resp.Packs), SnapshotID: resp.SnapshotID,
	}
	if held := a.heldID(); held != "" {
		ses.SnapshotID = held
	}
	adoptIdentity(&ses, resp)
	ses.PolicyRefused, ses.PolicyDeadline = a.refusal()
	hs := &heldSession{lock: newCtxLock(), s: ses, token: resp.SessionToken, minted: now}
	a.mu.Lock()
	old := a.sessions[sessKey{h.Name, h.Session}]
	a.sessions[sessKey{h.Name, h.Session}] = hs
	a.mu.Unlock()
	if old != nil {
		old.stopPush()
	}
	return hs
}

// finishStart adopts the snapshot a start's check-in named, fetching it
// when the account does not hold it, saves the session, starts its push
// stream, drains the spool and writes the git host file the answer names.
func (s *credStore) finishStart(ctx context.Context, a *account, hs *heldSession, resp agentguard.CheckinResponse) Session {
	cur := a.copyOf(hs)
	pin, err := s.adoptSnapshot(ctx, a, resp.SessionToken, cur.SnapshotID, resp.SnapshotID)
	a.mu.Lock()
	hs.s.SnapshotID = pin
	hs.s.settlePolicy(resp.SnapshotID, err, hs.s.IssuedAt)
	s.saveSessions(a)
	a.mu.Unlock()
	s.startPush(a, hs)
	a.kickDrain()
	s.writeHostFile(resp.GitUpstreams)
	return a.copyOf(hs)
}

// refusal returns the earliest outstanding policy refusal among a's
// sessions.
func (a *account) refusal() (string, time.Time) {
	a.mu.Lock()
	defer a.mu.Unlock()
	var why string
	var deadline time.Time
	for _, hs := range a.sessions {
		if d := hs.s.PolicyDeadline; !d.IsZero() && (deadline.IsZero() || d.Before(deadline)) {
			why, deadline = hs.s.PolicyRefused, d
		}
	}
	return why, deadline
}

// renew refreshes hs when 120 seconds or less remain, or always with
// force, and records the renewal's class on the session. A renewal
// already in flight for hs is not repeated: its outcome lands on hs.
func (s *credStore) renew(ctx context.Context, a *account, hs *heldSession, force bool) Session {
	if err := hs.lock.lock(ctx); err != nil {
		return a.copyOf(hs)
	}
	defer hs.lock.unlock()
	if ses := a.copyOf(hs); !force && ses.ExpiresAt.Sub(s.now()) > renewBefore {
		return ses
	}
	class, detail := renewalClass(s.renewNow(ctx, a, hs))
	s.recordRenewal(a, hs, class, detail)
	return a.copyOf(hs)
}

// recordRenewal keeps the class of hs's last renewal. A terminal one is the
// server's refusal of the session, its user or its device, so hs is marked
// revoked with the server's sentence, as the revocation event does: the
// next decision denies at once, and the window's next start checks in
// again.
func (s *credStore) recordRenewal(a *account, hs *heldSession, class RenewalClass, detail string) {
	a.mu.Lock()
	hs.s.Renewal = Renewal{Class: class, Detail: detail}
	id := hs.s.ID
	a.mu.Unlock()
	if class == RenewTerminal {
		s.revoke(a, hs, id, detail)
	}
}

// refreshRefusedAfter is how long past its expiry a session token is sure
// to be refused by strazad, which accepts one for 30 seconds of clock skew,
// with a margin for the request's own time.
const refreshRefusedAfter = time.Minute

// renewNow refreshes hs. When the server refuses its token with 401 the
// enrollment may still be good, so it re-acquires the window once through
// the enrollment (D30) and reports that it did. A token past
// refreshRefusedAfter is not sent, and the window re-acquires at once. That
// re-acquire followed no refusal, so one that got no answer is not
// reported, and classes as a refresh that got none. It returns the
// refresh's error, or the re-acquire's. The caller holds hs.lock.
func (s *credStore) renewNow(ctx context.Context, a *account, hs *heldSession) (bool, error) {
	_, api, err := a.ready()
	if err != nil {
		return false, err
	}
	token, cur := a.tokenOf(hs), a.copyOf(hs)
	h := cur.Harness
	if token == "" {
		return false, errors.New("this session was revoked")
	}
	if s.now().Sub(cur.ExpiresAt) > refreshRefusedAfter {
		err := s.reacquire(ctx, a, hs, token)
		return err != nil && !unanswered(err), err
	}
	resp, err := api.Refresh(withBodyToken(ctx, token), token, h.Name, h.Version, measureAttestation(h.Name))
	if err == nil {
		s.adoptRefresh(ctx, a, hs, resp)
		return false, nil
	}
	var refuse *agentguard.StatusError
	if !errors.As(err, &refuse) || refuse.Status != http.StatusUnauthorized {
		return false, err
	}
	return true, s.reacquire(ctx, a, hs, token)
}

// reacquire starts a new server session for hs's window through the
// enrollment, the lane a session start uses, after strazad refused the
// token refused. A re-acquire another renewal finished while this one
// waited for the key is kept.
func (s *credStore) reacquire(ctx context.Context, a *account, hs *heldSession, refused string) error {
	_, err := s.underKey(ctx, a, func(rctx context.Context, release func()) (Session, error) {
		if tok := a.tokenOf(hs); tok != "" && tok != refused {
			return Session{}, nil
		}
		_, resp, err := s.checkin(rctx, a, a.copyOf(hs).Harness)
		if err != nil {
			release()
			return Session{}, err
		}
		held := s.holdReacquire(a, hs, resp)
		release()
		s.finishReacquire(rctx, a, hs, resp, held)
		return Session{}, nil
	})
	return err
}

// adoptRefresh applies a refresh to hs and saves it: the same session with
// a new token, the identity the server resolved again, and the snapshot it
// names, which is fetched and verified before the pin moves. It writes the
// git host file the answer names.
func (s *credStore) adoptRefresh(ctx context.Context, a *account, hs *heldSession, resp agentguard.CheckinResponse) {
	cur := a.copyOf(hs)
	pin, err := s.adoptSnapshot(ctx, a, resp.SessionToken, cur.SnapshotID, resp.SnapshotID)
	a.mu.Lock()
	held := hs.s.ExpiresAt
	hs.token, hs.minted = resp.SessionToken, s.now()
	hs.s.ExpiresAt = s.now().Add(time.Duration(resp.ExpiresIn) * time.Second)
	adoptIdentity(&hs.s, resp)
	hs.s.SnapshotID = pin
	hs.s.settlePolicy(resp.SnapshotID, err, held)
	s.saveSessions(a)
	a.mu.Unlock()
	if hs.pushState() == localapi.PushOff {
		s.startPush(a, hs)
	}
	s.writeHostFile(resp.GitUpstreams)
}

// holdReacquire overwrites hs with the session a re-acquire minted, before
// the key lock is released, so a renewal queued behind this one keeps it.
// It returns the session time hs held before.
func (s *credStore) holdReacquire(a *account, hs *heldSession, resp agentguard.CheckinResponse) time.Time {
	now := s.now()
	a.mu.Lock()
	defer a.mu.Unlock()
	held := hs.s.ExpiresAt
	hs.token, hs.minted = resp.SessionToken, now
	hs.s.ID, hs.s.Attestation, hs.s.IssuedAt = resp.SessionID, resp.Attestation, now
	hs.s.ExpiresAt = now.Add(time.Duration(resp.ExpiresIn) * time.Second)
	adoptIdentity(&hs.s, resp)
	hs.s.Packs = packsOf(resp.Packs)
	return held
}

// finishReacquire adopts the snapshot a re-acquire named, saves the new
// session, restarts its push stream, whose subjects name the old session,
// and writes the git host file the answer names. held is the session time
// hs had before the re-acquire.
func (s *credStore) finishReacquire(ctx context.Context, a *account, hs *heldSession, resp agentguard.CheckinResponse, held time.Time) {
	cur := a.copyOf(hs)
	pin, err := s.adoptSnapshot(ctx, a, resp.SessionToken, cur.SnapshotID, resp.SnapshotID)
	a.mu.Lock()
	hs.s.SnapshotID = pin
	hs.s.settlePolicy(resp.SnapshotID, err, held)
	s.saveSessions(a)
	a.mu.Unlock()
	s.startPush(a, hs)
	s.writeHostFile(resp.GitUpstreams)
}

// adoptIdentity copies the identity a check-in or refresh resolved onto
// ses. A response with no roles at all is an identity-less reply, from an
// older server or a proxy that dropped the field, and is ignored, because
// clearing the roles would unmatch a restrictive set and widen access.
func adoptIdentity(ses *Session, resp agentguard.CheckinResponse) {
	if resp.User != "" {
		ses.User = resp.User
	}
	if resp.Roles == nil {
		return
	}
	ses.Roles = resp.Roles
	ses.UserType, ses.AgencyMode, ses.SwarmID = resp.UserType, resp.AgencyMode, resp.SwarmID
}

// packsOf converts the knowledge packs of a check-in.
func packsOf(in []agentguard.PackPayload) []localapi.Pack {
	var out []localapi.Pack
	for _, p := range in {
		out = append(out, localapi.Pack{Name: p.Name, Version: p.Version, Content: p.Content, Checksum: p.Checksum})
	}
	return out
}

// unanswered reports whether err says a renewal got no answer from
// strazad: a request that failed in transport, ran out of time or was cut
// short, a server error, which a proxy in front of a stopped strazad
// answers, or the wait after a lost answer. Any other failure got an
// answer or failed on this machine.
func unanswered(err error) bool {
	var transport *url.Error
	var status *agentguard.StatusError
	var refused *Refused
	return errors.As(err, &transport) || errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) ||
		errors.Is(err, io.ErrUnexpectedEOF) || (errors.As(err, &status) && status.Status >= http.StatusInternalServerError) ||
		(errors.As(err, &refused) && refused.Sentence == waitingSentence)
}

// renewalClass classes what renewNow reported, as the offline gate words
// it. A refresh that got no answer or a 5xx leaves the token's state
// unknown. A re-acquire the server refused below 500 judged the
// enrollment, and any other re-acquire failure could not complete.
func renewalClass(reacquired bool, err error) (RenewalClass, string) {
	var refuse *agentguard.StatusError
	switch status := errors.As(err, &refuse); {
	case err == nil:
		return RenewNone, ""
	case !reacquired && (!status || refuse.Status >= http.StatusInternalServerError):
		return RenewTransport, err.Error()
	case status && refuse.Status < http.StatusInternalServerError:
		return renewalAnswer(refuse)
	}
	return RenewRejected, err.Error()
}

// renewalAnswer classes a status answer below 500 to a renewal. A 408 or a
// 429 comes from something in front of strazad, and a status with no
// reason says nothing about the session, so both judged nothing. A bare
// 401 or 403 is still a refusal, because strazad always gives a reason, so
// a proxy replaced its answer. Any other answer is the server's refusal,
// with its own sentence as the detail.
func renewalAnswer(refuse *agentguard.StatusError) (RenewalClass, string) {
	msg := strings.TrimPrefix(refuse.Msg, "Straza: ")
	bare := msg == fmt.Sprintf("HTTP %d", refuse.Status)
	switch {
	case bare && (refuse.Status == http.StatusUnauthorized || refuse.Status == http.StatusForbidden):
		return RenewTerminal, fmt.Sprintf("the server answered HTTP %d and gave no reason. "+
			"Ask your administrator whether this user or device is still enabled, "+
			"and check whether a proxy in front of strazad replaces its answers.", refuse.Status)
	case bare:
		return RenewUnjudged, fmt.Sprintf("the server answered HTTP %d and gave no reason.", refuse.Status)
	case refuse.Status == http.StatusRequestTimeout || refuse.Status == http.StatusTooManyRequests:
		return RenewUnjudged, fmt.Sprintf("the server answered HTTP %d: %s", refuse.Status, endSentence(msg))
	}
	return RenewTerminal, msg
}

// checkinRefusal turns a failed session start into the sentence the person
// sees. The server's own refusals pass through unchanged.
func checkinRefusal(server string, err error) error {
	var refused *Refused
	var refuse *agentguard.StatusError
	switch {
	case errors.As(err, &refused):
		return refused
	case errors.As(err, &refuse) && refuse.Msg == fmt.Sprintf("HTTP %d", refuse.Status):
		return &Refused{Status: http.StatusBadGateway, Sentence: fmt.Sprintf("The server answered this machine's check-in with HTTP %d and gave no reason. "+
			"Check that %s reaches strazad with nothing in front of it that blocks Straza, and run `straza doctor`.", refuse.Status, server)}
	case errors.As(err, &refuse) && refuse.Status < http.StatusInternalServerError:
		return &Refused{Status: refuse.Status, Sentence: refuse.Msg}
	case errors.As(err, &refuse):
		return &Refused{Status: http.StatusBadGateway, Sentence: fmt.Sprintf("The server could not start this session and answered HTTP %d: %s "+
			"Start the session again in a minute, and run `straza doctor` if it keeps failing.", refuse.Status, endSentence(refuse.Msg))}
	}
	return &Refused{Status: http.StatusBadGateway, Sentence: fmt.Sprintf("Straza could not check in with the server at %s, so this session cannot start: %s. "+
		"Check that this machine reaches the server, then start the session again, or run `straza doctor`.", server, strings.TrimRight(err.Error(), ". "))}
}
