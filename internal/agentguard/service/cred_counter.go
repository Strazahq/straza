package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"path/filepath"
	"time"

	"github.com/strazahq/straza/internal/agentguard"
	"github.com/strazahq/straza/internal/agentguard/localapi"
	"github.com/strazahq/straza/internal/version"
)

// nextCounter is the only place a key check-in gets its counter. It raises
// a's counter by one, records it as in flight with nonceAt, when the nonce
// the attempt carries arrived, writes the record and syncs it, and only
// then returns the number, so no number is sent twice, across a crash too,
// and a stop before the answer leaves a wait for the next service. The
// caller holds a.keyLock.
func (a *account) nextCounter(nonceAt time.Time) (int64, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.rec == nil || a.rec.Kind == "" {
		return 0, &Refused{Status: http.StatusForbidden, Sentence: notEnrolledSentence}
	}
	next := *a.rec
	next.Counter++
	next.InFlight, next.InFlightNonceAt = next.Counter, nonceAt
	if err := writeRecord(filepath.Join(a.dir, recordFile), next); err != nil {
		return 0, &Refused{Status: http.StatusInternalServerError, Sentence: fmt.Sprintf(
			"Straza could not save this machine's check-in counter in %s, so it did not check in: %s. "+
				"Free space there or fix its permissions, then start the session again.", a.dir, err)}
	}
	a.rec = &next
	return next.Counter, nil
}

// waitUntil returns when a's next key check-in may leave.
func (a *account) waitUntil() time.Time {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.rec == nil {
		return time.Time{}
	}
	return a.rec.WaitUntil
}

// settleAttempt clears the in-flight check-in of a and keeps a's next key
// check-in back until until, unless that is zero, on disk, so a restart
// keeps the wait too. A write that fails still keeps the wait in memory.
// The caller holds a.keyLock or owns a alone.
func (a *account) settleAttempt(until time.Time) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.rec == nil || (a.rec.InFlight == 0 && !until.After(a.rec.WaitUntil)) {
		return nil
	}
	next := *a.rec
	next.InFlight, next.InFlightNonceAt = 0, time.Time{}
	if until.After(next.WaitUntil) {
		next.WaitUntil = until
	}
	a.rec = &next
	return writeRecord(filepath.Join(a.dir, recordFile), next)
}

// harnessBody is the harness a check-in names.
type harnessBody struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

// clientStamp names this build on a check-in. The server records it and
// never verifies it.
type clientStamp struct {
	Version string `json:"version"`
	Commit  string `json:"commit"`
}

// keyCheckinBody is the key-lane check-in of /v1/checkin: no token, and
// the device's next counter, which the proof signs as ctr.
type keyCheckinBody struct {
	DeviceID    string                 `json:"device_id"`
	Counter     int64                  `json:"counter"`
	Harness     harnessBody            `json:"harness"`
	Attestation agentguard.Attestation `json:"attestation"`
	Client      clientStamp            `json:"client"`
}

// errStopping ends a key check-in whose answer arrived after the service
// began to stop. The service may no longer own the state directory, so the
// attempt stays recorded as in flight for the next service to wait out.
var errStopping = errors.New("the Straza background service is stopping, so the next start of the service waits out this check-in")

// errNoSessionToken is a check-in answer that carries no session.
var errNoSessionToken = errors.New("the server's check-in answer carried no session token. Check that the configured server URL reaches strazad, and run `straza doctor`")

// keyCheckin starts a session for h on a's key lane. The caller holds
// a.keyLock, which stays held until an answer arrives or the connection is
// dead. The check-in runs to its answer even when ctx ends, bounded by
// checkinTimeout, because a request given up on can still reach the
// server. Each attempt, the retry after the nonce challenge included,
// takes the next counter and is recorded as in flight with its nonce's
// arrival time until its answer. After an attempt that was sent and got no
// answer, the key's next check-in waits until that attempt's nonce has
// expired, and a check-in inside the wait is refused with the waiting
// sentence.
func (s *credStore) keyCheckin(ctx context.Context, a *account, h localapi.Harness) (agentguard.CheckinResponse, error) {
	rec, api, err := a.ready()
	if err != nil {
		return agentguard.CheckinResponse{}, err
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), s.checkinTimeout)
	defer cancel()
	for attempt := 0; ; attempt++ {
		if s.now().Before(a.waitUntil()) {
			return agentguard.CheckinResponse{}, &Refused{Status: http.StatusServiceUnavailable, Sentence: waitingSentence}
		}
		nonce, nonceAt := a.nonces.get()
		if nonce == "" {
			nonceAt = time.Time{}
		}
		n, err := a.nextCounter(nonceAt)
		if err != nil {
			return agentguard.CheckinResponse{}, err
		}
		att := &keyAttempt{counter: n, nonce: nonce, nonceAt: nonceAt}
		ans, err := postJSON(withAttempt(ctx, att), api, "/v1/checkin", keyCheckinBody{
			DeviceID: rec.DeviceID, Counter: n, Harness: harnessBody{Name: h.Name, Version: h.Version},
			Attestation: measureAttestation(h.Name), Client: clientStamp{Version: version.Version, Commit: version.Commit},
		})
		if s.ctx.Err() != nil {
			return agentguard.CheckinResponse{}, errStopping
		}
		if err != nil {
			s.holdAfterLoss(a, att)
			return agentguard.CheckinResponse{}, err
		}
		if ans.challenge() && attempt == 0 {
			continue
		}
		if err := a.settleAttempt(time.Time{}); err != nil {
			s.log.Warn("the answered check-in could not be cleared from the record, so a restart waits two minutes before the next one",
				"uid", a.uid, "err", err)
		}
		if ans.status != http.StatusOK {
			return agentguard.CheckinResponse{}, ans.refusal("/v1/checkin")
		}
		var out agentguard.CheckinResponse
		if err := json.Unmarshal(ans.body, &out); err != nil || out.SessionToken == "" {
			return agentguard.CheckinResponse{}, errNoSessionToken
		}
		return out, nil
	}
}

// holdAfterLoss starts the wait after att, a key check-in that got no
// answer, and clears it from the in-flight record. An attempt that was
// never written, or that carried no nonce, can never move the counter, so
// it starts no wait.
func (s *credStore) holdAfterLoss(a *account, att *keyAttempt) {
	if !att.sent.Load() || att.nonce == "" {
		if err := a.settleAttempt(time.Time{}); err != nil {
			s.log.Warn("the unsent check-in could not be cleared from the record, so a restart waits two minutes before the next one",
				"uid", a.uid, "err", err)
		}
		return
	}
	until := att.nonceAt.Add(lostAnswerWait)
	if err := a.settleAttempt(until); err != nil {
		s.log.Error("the wait after a lost check-in answer could not be saved, so it lasts only until the service restarts",
			"uid", a.uid, "err", err)
	}
	s.log.Warn("a key check-in got no answer, so this key's next check-in waits until its nonce has expired",
		"uid", a.uid, "counter", att.counter, "until", until.UTC().Format(time.RFC3339))
}
