package manager

import (
	"context"
	"testing"
	"time"
)

// panicDeadline is a context whose Deadline panics, which dial reads on the
// calling goroutine after entry registered the dial.
type panicDeadline struct{ context.Context }

func (panicDeadline) Deadline() (time.Time, bool) { panic("a dial that panics") }

// TestRemoteRuntimeDialThatPanicsLeavesNoDial pins that a dial that panics
// removes its entry, so the next call on the key dials at once and does not
// wait out its own deadline for a dial that will never end.
func TestRemoteRuntimeDialThatPanicsLeavesNoDial(t *testing.T) {
	t.Parallel()
	up := newMethodUpstream(t)
	r := besideRuntime("up", RemoteSpec{URL: up.URL}, nil)
	t.Cleanup(r.Stop)
	in := CallInput{Tool: "echo", Session: "ses-alice"}

	func() {
		defer func() {
			if recover() == nil {
				t.Fatal("the call whose dial panics did not panic")
			}
		}()
		_, _ = r.Call(panicDeadline{context.Background()}, in)
	}()
	r.mu.Lock()
	dials := len(r.dials)
	r.mu.Unlock()
	if dials != 0 {
		t.Fatalf("dials in flight after the panic = %d, want 0", dials)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	start := time.Now()
	if _, err := r.Call(ctx, in); err != nil {
		t.Fatalf("the next call on the key answered %v after %s, want its own dial's session", err, time.Since(start).Round(time.Millisecond))
	}
}
