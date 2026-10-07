package manager

import (
	"testing"
	"time"
)

// TestRingEntriesCarryReceiveTimes pins the 0.94.0 shape the console's log
// gutter reads: every entry carries the time the ring received it, the
// times never go backwards, the entry lines equal the plain lines, and a
// full ring still returns oldest first.
func TestRingEntriesCarryReceiveTimes(t *testing.T) {
	ring := NewRing(3)
	before := time.Now()
	ring.Append("one")
	_, _ = ring.Write([]byte("two\nthree\n"))

	entries := ring.Entries(0)
	lines := ring.Last(0)
	if len(entries) != 3 || len(lines) != 3 {
		t.Fatalf("entries = %d lines = %d, want 3 each", len(entries), len(lines))
	}
	for i, e := range entries {
		if e.Line != lines[i] {
			t.Errorf("entry %d line = %q, Last = %q", i, e.Line, lines[i])
		}
		if e.At.Before(before) || e.At.After(time.Now()) {
			t.Errorf("entry %d time %v outside the test window", i, e.At)
		}
		if i > 0 && e.At.Before(entries[i-1].At) {
			t.Errorf("entry %d time %v precedes entry %d time %v", i, e.At, i-1, entries[i-1].At)
		}
	}

	ring.Append("four")
	got := ring.Entries(0)
	want := []string{"two", "three", "four"}
	if len(got) != len(want) {
		t.Fatalf("after overflow entries = %d, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i].Line != want[i] {
			t.Errorf("after overflow entry %d = %q, want %q", i, got[i].Line, want[i])
		}
	}
	if last := ring.Entries(1); len(last) != 1 || last[0].Line != "four" {
		t.Errorf("Entries(1) = %+v, want the newest line", last)
	}
}
