package objects

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// TestListsOff pins apps.objects.enabled false: no tick lists a server,
// not on boot and not after a miss, a refresh and a mark answer ErrOff and
// write nothing, and no list row is made.
func TestListsOff(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()
	spec := demoSpec()
	app := installApp(t, st, "objects-demo", &spec, false)
	other := installApp(t, st, "kept-row", &spec, false)
	caller := newCaller()
	caller.serve("objects-demo", map[string]*mcp.CallToolResult{"": page("", item("Strazahq/straza", 1, nil))})
	clk := &clock{t: base}
	l := New(Options{Store: st, Caller: caller, Secrets: fakeSecrets{}, Owner: "pod-a", Record: (&recorder{}).record, Off: true})
	l.now = clk.now
	settle(l)
	clk.add(2 * time.Minute)
	l.Missed(app.ID)
	settle(l)
	if _, _, err := l.Refresh(ctx, app.ID); !errors.Is(err, ErrOff) {
		t.Errorf("refresh while off = %v, want ErrOff", err)
	}
	if err := l.MarkDue(ctx, app.ID); !errors.Is(err, ErrOff) {
		t.Errorf("mark while off = %v, want ErrOff", err)
	}
	settle(l)
	if n := caller.callCount(); n != 0 {
		t.Errorf("the lists are off and %d listings ran", n)
	}
	if got := l.Get(app.ID); got != nil {
		t.Errorf("the lists are off and memory holds %+v", got)
	}
	if rows, err := st.ObjectLists().List(ctx); err != nil || len(rows) != 0 {
		t.Errorf("the lists are off and the store holds %d rows (%v)", len(rows), err)
	}

	// A row made before the lists went off keeps its due time.
	due := clk.now().Add(time.Hour)
	if err := st.ObjectLists().Ensure(ctx, other.ID, "repo", DeclHash(spec), due); err != nil {
		t.Fatal(err)
	}
	if _, _, err := l.Refresh(ctx, other.ID); !errors.Is(err, ErrOff) {
		t.Errorf("refresh of a kept row while off = %v, want ErrOff", err)
	}
	if err := l.MarkDue(ctx, other.ID); !errors.Is(err, ErrOff) {
		t.Errorf("mark of a kept row while off = %v, want ErrOff", err)
	}
	if row, err := st.ObjectLists().Get(ctx, other.ID); err != nil || !row.DueAt.Equal(due) {
		t.Errorf("a refresh while off moved the row's due time to %v (%v), want %v", row.DueAt, err, due)
	}
}

// TestSetPace pins the seam tests use: a stretched tick lists only when a
// mark wakes the loop, and the clock it sets dates the listing.
func TestSetPace(t *testing.T) {
	st := newStore(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	spec := demoSpec()
	app := installApp(t, st, "objects-demo", &spec, false)
	caller := newCaller()
	caller.serve("objects-demo", map[string]*mcp.CallToolResult{"": page("", item("Strazahq/straza", 1, nil))})
	l := New(Options{Store: st, Caller: caller, Secrets: fakeSecrets{}, Owner: "pod-a", Record: (&recorder{}).record})
	at := base.Add(48 * time.Hour)
	l.SetPace(time.Hour, func() time.Time { return at })
	done := make(chan struct{})
	go func() { l.Run(ctx); close(done) }()
	time.Sleep(100 * time.Millisecond)
	if n := caller.callCount(); n != 0 {
		t.Fatalf("a stretched tick listed %d times before any mark", n)
	}
	if err := l.MarkDue(ctx, app.ID); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for got := l.Get(app.ID); got == nil || got.ListedAt == nil; got = l.Get(app.ID) {
		if time.Now().After(deadline) {
			t.Fatal("a mark did not wake the loop")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if got := l.Get(app.ID); !got.ListedAt.Equal(at) {
		t.Errorf("the listing is dated %v, want the paced clock's %v", got.ListedAt, at)
	}
	cancel()
	<-done
}
