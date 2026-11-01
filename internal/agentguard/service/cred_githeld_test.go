package service

import (
	"fmt"
	"testing"
	"time"
)

// TestGitHeldEvictsOldestRun pins the bound on held runs: a uid's new run
// past maxGitOps takes the place of that uid's run whose newest grant is
// oldest, instead of being refused, and another uid's runs stay.
func TestGitHeldEvictsOldestRun(t *testing.T) {
	g := &gitState{held: map[gitKey]*gitRun{}}
	start := time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC)
	grant := func(n int) heldGrant {
		return heldGrant{grant: fmt.Sprintf("g%d", n), op: gitFetch, exp: start.Add(120 * time.Second)}
	}
	fetch := gitRoute{tail: "git-upload-pack", op: gitFetch}
	g.hold("1001", "other", slotFetch, grant(-1), start)
	for i := range maxGitOps {
		g.hold("1000", fmt.Sprintf("run%d", i), slotFetch, grant(i), start.Add(time.Duration(i)*time.Millisecond))
	}
	// run0 takes a newer grant, so run1 now holds the oldest newest grant.
	g.hold("1000", "run0", slotFetch, grant(1000), start.Add(time.Second))
	g.hold("1000", "fresh", slotFetch, grant(2000), start.Add(2*time.Second))
	now := start.Add(3 * time.Second)
	for op, want := range map[string]bool{"run0": true, "run1": false, "run2": true, "fresh": true} {
		if _, ok := g.take("1000", op, fetch, now); ok != want {
			t.Errorf("run %s held = %v, want %v", op, ok, want)
		}
	}
	if _, ok := g.take("1001", "other", fetch, now); !ok {
		t.Error("another uid's run was evicted")
	}
	n := 0
	for k := range g.held {
		if k.uid == "1000" {
			n++
		}
	}
	if n != maxGitOps {
		t.Errorf("uid 1000 holds %d runs, want %d", n, maxGitOps)
	}
}

// TestGitSlotFor pins which held grant opens each route. A receive-pack
// POST takes only the grant that names the updates, never the one without
// refs that skipped the approval.
func TestGitSlotFor(t *testing.T) {
	rows := []struct {
		route gitRoute
		want  gitSlot
	}{
		{gitRoute{"info/refs", "git-upload-pack", gitFetch}, slotFetch},
		{gitRoute{tail: "git-upload-pack", op: gitFetch}, slotFetch},
		{gitRoute{"info/refs", "git-receive-pack", gitPush}, slotAdvert},
		{gitRoute{tail: "git-receive-pack", op: gitPush}, slotPush},
	}
	for _, row := range rows {
		if got := slotFor(row.route); got != row.want {
			t.Errorf("slotFor(%+v) = %v, want %v", row.route, got, row.want)
		}
	}
}
