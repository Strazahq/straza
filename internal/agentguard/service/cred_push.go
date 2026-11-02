package service

import (
	"bufio"
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/strazahq/straza/internal/agentguard/localapi"
	"github.com/strazahq/straza/internal/dpop"
	"github.com/strazahq/straza/internal/sameorigin"
)

// pushHeaderTimeout bounds the wait for a push stream's answer headers.
const pushHeaderTimeout = 15 * time.Second

// pushRevoked is stream's status for a delivered revocation.
const pushRevoked = -1

func (hs *heldSession) pushState() string {
	hs.pushMu.Lock()
	defer hs.pushMu.Unlock()
	if hs.push == "" {
		return localapi.PushOff
	}
	return hs.push
}

// setPush records the stream state of the stream whose context is ctx. A
// stream another one replaced records nothing.
func (hs *heldSession) setPush(ctx context.Context, state string) {
	hs.pushMu.Lock()
	defer hs.pushMu.Unlock()
	if ctx.Err() == nil {
		hs.push = state
	}
}

// stopPush ends hs's push stream.
func (hs *heldSession) stopPush() {
	hs.pushMu.Lock()
	defer hs.pushMu.Unlock()
	if hs.cancelPush != nil {
		hs.cancelPush()
		hs.cancelPush = nil
	}
	hs.push = localapi.PushOff
}

// startPush runs one /v1/push stream for hs's current server session,
// ending the one it had. The stream is proven once at connect.
func (s *credStore) startPush(a *account, hs *heldSession) {
	ctx, cancel := context.WithCancel(s.ctx)
	hs.pushMu.Lock()
	if hs.cancelPush != nil {
		hs.cancelPush()
	}
	hs.cancelPush, hs.push = cancel, localapi.PushConnecting
	hs.pushMu.Unlock()
	go s.pushLoop(ctx, a, hs, a.copyOf(hs).ID)
}

// pushToken returns hs's token while hs still holds the server session id
// and that session is neither revoked nor past its time.
func (s *credStore) pushToken(a *account, hs *heldSession, id string) (string, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	live := hs.s.ID == id && hs.revoked == "" && hs.token != "" && s.now().Before(hs.s.ExpiresAt)
	return hs.token, live
}

// pushLoop keeps hs's stream for the server session id open until ctx ends,
// the session is revoked, re-acquired or has lapsed, or the server has no
// push lane. The stream is an accelerator: a refresh near expiry catches a
// revocation it missed.
func (s *credStore) pushLoop(ctx context.Context, a *account, hs *heldSession, id string) {
	backoff := time.Second
	for ctx.Err() == nil {
		token, live := s.pushToken(a, hs, id)
		if !live {
			break
		}
		status, connected := s.stream(ctx, a, hs, id, token)
		if _, live := s.pushToken(a, hs, id); !live {
			break
		}
		switch {
		case ctx.Err() != nil, status == pushRevoked:
			return
		case status == http.StatusNotFound || status == http.StatusMethodNotAllowed:
			s.log.Info("the server has no push lane, so revocations reach this session at its next refresh", "uid", a.uid)
			hs.setPush(ctx, localapi.PushOff)
			return
		case status == http.StatusUnauthorized || status == http.StatusForbidden:
			// strazad refuses the stream of a revoked session, user or
			// device, and a revocation published while the stream was down
			// is never sent as an event. So the session is renewed at once,
			// and the renewal's own 401 and 403 rules decide.
			rctx, cancel := context.WithTimeout(s.ctx, requestTimeout)
			s.renew(rctx, a, hs, true)
			cancel()
		}
		if connected {
			backoff = time.Second
		} else {
			backoff = min(backoff*2, 30*time.Second)
		}
		hs.setPush(ctx, localapi.PushConnecting)
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
	}
	hs.setPush(ctx, localapi.PushOff)
}

// stream opens one push connection under token and handles its events
// until the server ends it or the session lapses. It returns the HTTP
// status, 0 for a transport error, or pushRevoked, and whether the
// server's ready event arrived.
func (s *credStore) stream(ctx context.Context, a *account, hs *heldSession, id, token string) (int, bool) {
	rec, _, err := a.ready()
	if err != nil {
		return 0, false
	}
	a.mu.Lock()
	proofs := a.proofs
	a.mu.Unlock()
	rctx, cancel := context.WithCancel(ctx)
	defer cancel()
	req, err := http.NewRequestWithContext(rctx, http.MethodGet, strings.TrimRight(rec.ServerURL, "/")+"/v1/push", nil)
	if err != nil {
		return 0, false
	}
	req.Header.Set("Authorization", dpop.Scheme+" "+token)
	req.Header.Set("Accept", "text/event-stream")
	timer := time.AfterFunc(pushHeaderTimeout, cancel)
	resp, err := (&http.Client{Transport: proofs, CheckRedirect: sameorigin.CheckCredentialed}).Do(req)
	timer.Stop()
	if err != nil {
		return 0, false
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return resp.StatusCode, false
	}
	go func() {
		t := time.NewTicker(s.pushCheck)
		defer t.Stop()
		for {
			select {
			case <-rctx.Done():
				return
			case <-t.C:
				if _, live := s.pushToken(a, hs, id); !live {
					cancel()
					return
				}
			}
		}
	}()
	connected, event := false, ""
	sc := bufio.NewScanner(resp.Body)
	for sc.Scan() {
		line := sc.Text()
		if strings.HasPrefix(line, "event:") {
			event = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
			continue
		}
		if line != "" {
			continue
		}
		switch event {
		case "ready":
			connected = true
			hs.setPush(ctx, localapi.PushConnected)
		case "revocation":
			s.revoke(a, hs, id, "kill-switch push from the server")
			return pushRevoked, true
		case "policy":
			go func() {
				pctx, pcancel := context.WithTimeout(s.ctx, requestTimeout)
				defer pcancel()
				s.renew(pctx, a, hs, true)
			}()
		}
		event = ""
	}
	return 0, connected
}

// revoke marks hs revoked for reason when it still holds the server
// session id, drops its token, saves the mark and ends its stream. The
// service keeps running, and the window's next session start checks in
// again. The mark is written even while the service stops, because it only
// narrows access and a revocation lost there would let the window run on.
func (s *credStore) revoke(a *account, hs *heldSession, id, reason string) {
	a.mu.Lock()
	if hs.s.ID != id {
		a.mu.Unlock()
		return
	}
	hs.revoked, hs.token = reason, ""
	s.writeSessions(a)
	a.mu.Unlock()
	hs.stopPush()
	s.log.Warn("the server revoked a session, so its window is denied until it starts again", "uid", a.uid, "session", id)
}
