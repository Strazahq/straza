package spine

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// TestDeliveryClass pins the classification every retry decision hangs on:
// a 409 is the receiver saying "already have it" (delivered), a
// deterministic 4xx will never succeed by repetition (parks after a few
// tries), and everything transient (5xx, 429, timeouts, network) keeps
// redelivering with backoff. Without it a 400 storm loops at 1/s forever.
func TestDeliveryClass(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want deliveryClass
	}{
		{"nil is delivered", nil, classDelivered},
		{"duplicate sentinel", ErrDuplicate, classDuplicate},
		{"409 status is duplicate", &StatusError{Sink: "s", Status: 409}, classDuplicate},
		{"400 deterministic", &StatusError{Status: 400}, classDeterministic},
		{"401 deterministic", &StatusError{Status: 401}, classDeterministic},
		{"403 deterministic", &StatusError{Status: 403}, classDeterministic},
		{"404 deterministic", &StatusError{Status: 404}, classDeterministic},
		{"413 deterministic", &StatusError{Status: 413}, classDeterministic},
		{"422 deterministic", &StatusError{Status: 422}, classDeterministic},
		{"408 retryable", &StatusError{Status: 408}, classRetryable},
		{"425 retryable", &StatusError{Status: 425}, classRetryable},
		{"429 retryable", &StatusError{Status: 429}, classRetryable},
		{"500 retryable", &StatusError{Status: 500}, classRetryable},
		{"502 retryable", &StatusError{Status: 502}, classRetryable},
		{"503 retryable", &StatusError{Status: 503}, classRetryable},
		{"504 retryable", &StatusError{Status: 504}, classRetryable},
		{"wrapped status unwraps", fmt.Errorf("sink x: %w", &StatusError{Status: 413}), classDeterministic},
		{"wrapped duplicate unwraps", fmt.Errorf("sink x: %w", ErrDuplicate), classDuplicate},
		{"network error retryable", errors.New("dial tcp 127.0.0.1:1: connect: connection refused"), classRetryable},
		{"context deadline retryable", context.DeadlineExceeded, classRetryable},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := classify(tc.err); got != tc.want {
				t.Errorf("classify(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}

// TestWebhookSinkStatusErrors pins that the webhook sink reports the
// receiver's status as a typed error (so the runner can classify) and maps
// 409 to ErrDuplicate on both the single and the batched face.
func TestWebhookSinkStatusErrors(t *testing.T) {
	var status int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(status)
	}))
	defer srv.Close()
	sink := NewWebhookSink("es", srv.URL, nil, nil)
	ctx := context.Background()

	status = http.StatusConflict
	if err := sink.Deliver(ctx, "straza.audit.tool", []byte(`{"id":"e1"}`)); !errors.Is(err, ErrDuplicate) {
		t.Fatalf("409 Deliver = %v, want ErrDuplicate", err)
	}
	if err := sink.DeliverBatch(ctx, []SinkEvent{{Subject: "s", CE: []byte(`{}`)}}); !errors.Is(err, ErrDuplicate) {
		t.Fatalf("409 DeliverBatch = %v, want ErrDuplicate", err)
	}

	status = http.StatusRequestEntityTooLarge
	var se *StatusError
	if err := sink.Deliver(ctx, "straza.audit.tool", []byte(`{}`)); !errors.As(err, &se) || se.Status != 413 || se.Sink != "es" {
		t.Fatalf("413 Deliver = %v, want *StatusError{es,413}", err)
	}
	se = nil
	if err := sink.DeliverBatch(ctx, []SinkEvent{{Subject: "s", CE: []byte(`{}`)}}); !errors.As(err, &se) || se.Status != 413 {
		t.Fatalf("413 DeliverBatch = %v, want *StatusError 413", err)
	}
}

// TestRetryPolicy pins the schedule and the park rule: exponential from
// BaseDelay, jittered plus or minus 20 percent, capped at MaxDelay;
// deterministic failures park after DeterministicAttempts, retryable ones
// after RetryableAttempts; duplicates and successes never park.
func TestRetryPolicy(t *testing.T) {
	p := RetryPolicy{BaseDelay: time.Second, MaxDelay: time.Minute,
		DeterministicAttempts: 3, RetryableAttempts: 10}

	p.rand = func() float64 { return 0.5 } // jitter factor exactly 1.0
	delays := []struct {
		attempt uint64
		want    time.Duration
	}{{1, time.Second}, {2, 2 * time.Second}, {3, 4 * time.Second}, {6, 32 * time.Second},
		{7, time.Minute}, {100, time.Minute}, {0, time.Second}}
	for _, d := range delays {
		if got := p.delay(d.attempt); got != d.want {
			t.Errorf("delay(%d) = %v, want %v", d.attempt, got, d.want)
		}
	}
	p.rand = func() float64 { return 0 }
	if got := p.delay(1); got != 800*time.Millisecond {
		t.Errorf("low jitter delay(1) = %v, want 800ms", got)
	}
	p.rand = func() float64 { return 1 }
	if got := p.delay(1); got != 1200*time.Millisecond {
		t.Errorf("high jitter delay(1) = %v, want 1200ms", got)
	}

	parks := []struct {
		class    deliveryClass
		attempts uint64
		want     bool
	}{
		{classDeterministic, 1, false}, {classDeterministic, 2, false}, {classDeterministic, 3, true},
		{classRetryable, 9, false}, {classRetryable, 10, true}, {classRetryable, 11, true},
		{classDuplicate, 1000, false}, {classDelivered, 1000, false},
	}
	for _, c := range parks {
		if got := p.park(c.class, c.attempts); got != c.want {
			t.Errorf("park(%v, %d) = %v, want %v", c.class, c.attempts, got, c.want)
		}
	}

	// Defaults: the documented operator contract.
	d := DefaultRetryPolicy()
	if d.BaseDelay != time.Second || d.MaxDelay != time.Minute || d.DeterministicAttempts != 3 || d.RetryableAttempts != 4320 {
		t.Errorf("DefaultRetryPolicy = %+v", d)
	}
}
