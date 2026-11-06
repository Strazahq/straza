package approval

import "errors"

// The rate of new approval records. Each new record notifies the people who
// decide it, so a session opens at most five at once and five a minute, and
// a person at most ten at once and ten a minute across their sessions,
// because an agent can start fresh sessions. A request that joins a pending
// record spends nothing. The buckets live in memory on each replica, so a
// deployment of several replicas without sticky routing admits that many
// times as much.
const (
	sessionRequestBurst = 5
	sessionRequestRPS   = 5.0 / 60
	userRequestBurst    = 10
	userRequestRPS      = 10.0 / 60
)

// ErrTooManyRequests reports a new approval record refused because its
// session or its person opened too many in the last minute. Nothing is
// written and nobody is notified. The PEP answers it as a decision with a
// bounded wait, not as a failure.
var ErrTooManyRequests = errors.New("approval request: too many new requests in the last minute")

// allowRequest spends one new record from the buckets of in's session and
// person, and reports whether both had one.
func (s *Service) allowRequest(in RequestInput) bool {
	return s.rate.AllowBurst("session|"+in.SessionID, sessionRequestRPS, sessionRequestBurst) &&
		s.rate.AllowBurst("user|"+in.UserID, userRequestRPS, userRequestBurst)
}

// requestReady reports whether allowRequest would pass in now, and spends
// nothing.
func (s *Service) requestReady(in RequestInput) bool {
	return s.rate.Ready("session|"+in.SessionID, sessionRequestRPS, sessionRequestBurst) &&
		s.rate.Ready("user|"+in.UserID, userRequestRPS, userRequestBurst)
}
