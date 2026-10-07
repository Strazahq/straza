package spine

import (
	"context"
	"errors"
	"fmt"
	"math"
	"math/rand/v2"
	"time"
)

// Delivery outcome classes. Redelivering every non-2xx in 1 s forever would
// loop a receiver that refuses one event deterministically (a 409 from an
// index that already holds it, a 413, a 400 while an ingest pipeline is
// missing) at 1/s, with the spool behind it. The classes below are what the
// runner's retry policy hangs on.

// ErrDuplicate is the receiver saying "already have it" (HTTP 409). Under
// the documented sink contract receivers dedupe by CloudEvent id, so a
// conflict on redelivery IS success: the event is where it should be.
var ErrDuplicate = errors.New("receiver already holds this event")

// StatusError is a non-2xx answer from a webhook receiver, typed so the
// runner can classify it.
type StatusError struct {
	Sink   string
	Status int
}

func (e *StatusError) Error() string {
	return fmt.Sprintf("sink %s: endpoint returned HTTP %d", e.Sink, e.Status)
}

// statusResult maps a receiver status to the sink's Deliver result.
func statusResult(sink string, status int) error {
	switch {
	case status >= 200 && status <= 299:
		return nil
	case status == 409:
		return fmt.Errorf("sink %s: %w (HTTP 409)", sink, ErrDuplicate)
	default:
		return &StatusError{Sink: sink, Status: status}
	}
}

type deliveryClass int

const (
	classDelivered deliveryClass = iota
	// classDuplicate: the receiver holds the event already; ack and count.
	classDuplicate
	// classDeterministic: repeating the same request cannot succeed (4xx
	// other than the transient trio); a short attempt budget, then park.
	classDeterministic
	// classRetryable: transient (5xx, 408/425/429, network, timeout, file
	// write failures); exponential backoff, a long attempt budget, then park.
	classRetryable
)

func (c deliveryClass) String() string {
	switch c {
	case classDelivered:
		return "delivered"
	case classDuplicate:
		return "duplicate"
	case classDeterministic:
		return "deterministic"
	default:
		return "retryable"
	}
}

// classify sorts a Deliver error into its class. Unknown error shapes are
// retryable: the safe default keeps the event in the stream rather than
// parking it on a guess.
func classify(err error) deliveryClass {
	if err == nil {
		return classDelivered
	}
	if errors.Is(err, ErrDuplicate) {
		return classDuplicate
	}
	var se *StatusError
	if errors.As(err, &se) {
		switch {
		case se.Status == 409:
			return classDuplicate
		case se.Status == 408 || se.Status == 425 || se.Status == 429:
			return classRetryable
		case se.Status >= 500:
			return classRetryable
		case se.Status >= 400:
			return classDeterministic
		}
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return classRetryable
	}
	return classRetryable
}

// RetryPolicy is the runner's redelivery schedule and park rule.
type RetryPolicy struct {
	// BaseDelay is the first redelivery delay; each attempt doubles it.
	BaseDelay time.Duration
	// MaxDelay caps the doubling.
	MaxDelay time.Duration
	// DeterministicAttempts is how many deliveries a deterministic failure
	// gets before the event parks (a brief misconfiguration still heals).
	DeterministicAttempts int
	// RetryableAttempts is the budget for transient failures before the
	// event parks. 0 means never park a retryable failure, bounded only by
	// stream retention.
	RetryableAttempts int

	rand func() float64 // jitter source; nil = math/rand
}

// DefaultRetryPolicy is the documented operator contract: 1 s doubling to
// 60 s (plus or minus 20 percent jitter), deterministic failures park after
// 3 attempts, transient ones after 4320 attempts (about three days at the
// 60 s ceiling, so a weekend-long receiver outage still heals itself).
func DefaultRetryPolicy() RetryPolicy {
	return RetryPolicy{BaseDelay: time.Second, MaxDelay: time.Minute,
		DeterministicAttempts: 3, RetryableAttempts: 4320}
}

// delay is the redelivery delay after the given delivery attempt (1-based).
func (p RetryPolicy) delay(attempt uint64) time.Duration {
	base := p.BaseDelay
	if base <= 0 {
		base = time.Second
	}
	maxDelay := p.MaxDelay
	if maxDelay < base {
		maxDelay = base
	}
	if attempt < 1 {
		attempt = 1
	}
	d := base
	for i := uint64(1); i < attempt && d < maxDelay; i++ {
		d *= 2
	}
	if d > maxDelay {
		d = maxDelay
	}
	r := p.rand
	if r == nil {
		r = rand.Float64
	}
	return time.Duration(math.Round(float64(d) * (0.8 + 0.4*r())))
}

// park reports whether a failure of class c, after attempts deliveries,
// leaves the stream for the dead-letter lane.
func (p RetryPolicy) park(c deliveryClass, attempts uint64) bool {
	switch c {
	case classDeterministic:
		budget := p.DeterministicAttempts
		if budget <= 0 {
			budget = 1
		}
		return attempts >= uint64(budget)
	case classRetryable:
		if p.RetryableAttempts <= 0 {
			return false
		}
		return attempts >= uint64(p.RetryableAttempts)
	default:
		return false
	}
}
