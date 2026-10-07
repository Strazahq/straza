package manager

import (
	"context"
	"testing"
	"time"

	"github.com/strazahq/straza/internal/store"
)

// TestStatusSinceMovesOnlyWhenTheWordChanges pins the stamp the console's
// status banner reads as "degraded for 12 minutes": a running app carries
// the time it settled, a new detail under the same status keeps the stamp,
// and a different status word moves it.
func TestStatusSinceMovesOnlyWhenTheWordChanges(t *testing.T) {
	mgr, _ := testManager(t)
	ctx := context.Background()

	mf := helperManifest(t, "sinceapp", []string{"echo"})
	if _, err := mgr.Install(ctx, mf, store.AppSourceAPI); err != nil {
		t.Fatal(err)
	}
	view := waitStatus(t, mgr, "sinceapp", StatusRunning)
	if view.StatusSince.IsZero() {
		t.Fatal("running app carries no StatusSince")
	}
	if view.StatusSince.After(time.Now()) {
		t.Fatalf("StatusSince %v is in the future", view.StatusSince)
	}
	settled := view.StatusSince

	mgr.mu.RLock()
	inst := mgr.byName["sinceapp"]
	mgr.mu.RUnlock()

	mgr.setStatus(ctx, inst, StatusDegraded, "inventory: first failure")
	degraded, _ := mgr.View("sinceapp")
	if !degraded.StatusSince.After(settled) && !degraded.StatusSince.Equal(settled) {
		t.Fatalf("degraded StatusSince %v precedes the running stamp %v", degraded.StatusSince, settled)
	}
	if degraded.StatusSince.Equal(settled) && degraded.Status != StatusDegraded {
		t.Fatalf("status = %q after setStatus degraded", degraded.Status)
	}
	first := degraded.StatusSince

	mgr.setStatus(ctx, inst, StatusDegraded, "inventory: second failure")
	again, _ := mgr.View("sinceapp")
	if !again.StatusSince.Equal(first) {
		t.Errorf("a new detail under the same status moved StatusSince: %v -> %v", first, again.StatusSince)
	}
	if again.Detail != "inventory: second failure" {
		t.Errorf("detail = %q, want the second failure", again.Detail)
	}

	mgr.setStatus(ctx, inst, StatusRunning, "")
	back, _ := mgr.View("sinceapp")
	if back.StatusSince.Before(first) {
		t.Errorf("returning to running left StatusSince at %v, before the degraded stamp %v", back.StatusSince, first)
	}
	if back.Status != StatusRunning {
		t.Errorf("status = %q, want running", back.Status)
	}
}
