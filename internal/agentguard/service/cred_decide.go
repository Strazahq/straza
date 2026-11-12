package service

import (
	"context"
	"errors"
	"io"
	"net/http"
	"time"

	"github.com/strazahq/straza/internal/agentguard"
	"github.com/strazahq/straza/internal/agentguard/localapi"
	"github.com/strazahq/straza/internal/agentguard/trace"
	"github.com/strazahq/straza/internal/policy"
)

// Decide asks strazad's /v1/decide for ses. A 401 means the server
// verified nothing beyond the token, as after a restart, so the session is
// renewed once, through a refresh that keeps the session id and its
// pending approval exemptions, or a re-acquire when the token itself is
// refused, and the decide is asked again. input rides beside the event on
// both asks.
func (s *credStore) Decide(ctx context.Context, ses Session, ev policy.Event, agentType, agentID string, input *localapi.ToolInput) (DecideOutcome, error) {
	a, hs, err := s.held(ses)
	if err != nil {
		return DecideOutcome{}, err
	}
	_, api, err := a.ready()
	if err != nil {
		return DecideOutcome{}, err
	}
	call := trace.CallFrom(ctx)
	if call == nil {
		call = &trace.Call{}
		ctx = trace.WithCall(ctx, call)
	}
	var out DecideOutcome
	dctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	out.Answer, err = api.Decide(dctx, a.tokenOf(hs), ev, agentType, agentID, input)
	cancel()
	var refuse *agentguard.StatusError
	if err != nil && errors.As(err, &refuse) && refuse.Status == http.StatusUnauthorized {
		out.Bounced = true
		if berr := s.bounce(ctx, a, hs, err); berr != nil {
			err = berr
		} else {
			rctx, rcancel := context.WithTimeout(ctx, 2*time.Second)
			out.Answer, err = api.Decide(rctx, a.tokenOf(hs), ev, agentType, agentID, input)
			rcancel()
		}
	}
	out.Status, out.Correlation = call.Get()
	return out, err
}

// bounce renews hs after the decide 401 cause. It returns nil when hs holds
// a fresh token. A refresh refused other than with 401 and a re-acquire the
// server refused return their own error. A re-acquire that failed on this
// machine returns cause, because the server's refusal is what stopped the
// decision. A lapsed window re-acquires with no refresh, and that
// re-acquire follows cause too.
func (s *credStore) bounce(ctx context.Context, a *account, hs *heldSession, cause error) error {
	bctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	if err := hs.lock.lock(bctx); err != nil {
		return cause
	}
	defer hs.lock.unlock()
	lapsed := s.now().Sub(a.copyOf(hs).ExpiresAt) > refreshRefusedAfter
	reacquired, err := s.renewNow(bctx, a, hs)
	reacquired = reacquired || lapsed
	class, detail := renewalClass(reacquired, err)
	s.recordRenewal(a, hs, class, detail)
	var refuse *agentguard.StatusError
	switch {
	case err == nil:
		return nil
	case !reacquired || errors.As(err, &refuse):
		return err
	}
	return cause
}

// Transport sends a request for ses to strazad with the session token and
// a fresh proof, renewing the token first when it is near expiry.
func (s *credStore) Transport(ses Session) http.RoundTripper {
	return sessionTransport{s: s, ses: ses}
}

// sessionTransport is the RoundTripper Transport returns.
type sessionTransport struct {
	s   *credStore
	ses Session
}

// RoundTrip sends req under the session's token with a proof from the
// enrollment's key. A 401 that is not the nonce challenge renews the
// session once, through a refresh or a re-acquire, and the request is sent
// once more with the new token when its body can be sent again. Any other
// answer, a 403 included, comes back as it is.
func (t sessionTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	a, hs, err := t.s.held(t.ses)
	if err == nil {
		t.s.renew(req.Context(), a, hs, false)
		_, _, err = a.ready()
	}
	var getBody func() (io.ReadCloser, error)
	var once io.ReadCloser
	if err == nil {
		getBody, once, err = replayable(req)
	} else if req.Body != nil {
		_ = req.Body.Close()
	}
	if err != nil {
		return nil, err
	}
	resp, token, err := t.send(req, a, hs, getBody, once)
	if err != nil || resp.StatusCode != http.StatusUnauthorized || nonceChallenge(resp) || getBody == nil {
		return resp, err
	}
	t.s.renew(req.Context(), a, hs, true)
	if a.tokenOf(hs) == token {
		return resp, nil
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
	_ = resp.Body.Close()
	resp, _, err = t.send(req, a, hs, getBody, nil)
	return resp, err
}

// send sends one copy of req under hs's current token and returns the
// token it used. The body comes from getBody, or from once for a body that
// cannot be sent twice.
func (t sessionTransport) send(req *http.Request, a *account, hs *heldSession, getBody func() (io.ReadCloser, error), once io.ReadCloser) (*http.Response, string, error) {
	a.mu.Lock()
	token, proofs, revoked := hs.token, a.proofs, hs.revoked
	a.mu.Unlock()
	if token == "" {
		if once != nil {
			_ = once.Close()
		}
		return nil, "", &Refused{Status: http.StatusForbidden, Sentence: revokedSentence(revoked)}
	}
	out := req.Clone(req.Context())
	out.Body, out.GetBody = once, getBody
	if getBody != nil {
		body, err := getBody()
		if err != nil {
			return nil, token, err
		}
		out.Body = body
	}
	out.Header.Del("Cookie")
	out.Header.Set("Authorization", "DPoP "+token)
	resp, err := proofs.RoundTrip(out)
	return resp, token, err
}
