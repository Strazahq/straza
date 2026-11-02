package objects

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/strazahq/straza/internal/manager"
	"github.com/strazahq/straza/internal/store"
)

var base = time.Date(2026, 10, 18, 9, 0, 0, 0, time.UTC)

// TestOneReplicaLists pins case 18: two replicas on one store tick at the
// same time, exactly one claims the server and calls its listing tool, it
// holds the new version at once, and the other holds it after its next
// tick.
func TestOneReplicaLists(t *testing.T) {
	st := newStore(t)
	spec := demoSpec()
	app := installApp(t, st, "objects-demo", &spec, true)
	caller := newCaller()
	caller.serve("objects-demo", map[string]*mcp.CallToolResult{"": page("", item("Strazahq/straza", 1, "product"), item("Strazahq/site", 2, "web"))})
	caller.release = make(chan struct{})
	secrets := fakeSecrets{app.ID: {ID: "l-1", Value: "tok-listing"}}
	rec, clk := &recorder{}, &clock{t: base}
	a, b := rig(st, caller, secrets, rec, clk, "pod-a"), rig(st, caller, secrets, rec, clk, "pod-b")

	a.tick(context.Background())
	b.tick(context.Background())
	close(caller.release)
	a.wg.Wait()
	b.wg.Wait()
	if n := caller.callCount(); n != 1 {
		t.Fatalf("listing calls = %d, want exactly one across both replicas", n)
	}
	lister, other := a, b
	if a.Get(app.ID) == nil || a.Get(app.ID).Version == 0 {
		lister, other = b, a
	}
	if got := lister.Get(app.ID); got == nil || got.Version != 1 || len(got.Objects) != 2 || got.Objects[0].Value != "Strazahq/site" {
		t.Fatalf("the lister holds %+v, want version 1 with both objects sorted by value", got)
	}
	if got := other.Get(app.ID); got != nil && got.Version != 0 {
		t.Fatalf("the other replica holds version %d before its next tick", got.Version)
	}
	other.tick(context.Background())
	got := other.Get(app.ID)
	if got == nil || got.Version != 1 || len(got.Objects) != 2 {
		t.Fatalf("after one tick the other replica holds %+v, want version 1", got)
	}
	if o, ok := got.ByValue("Strazahq/straza"); !ok || o.ID != "1" || o.Attributes["area"].Values[0] != "product" {
		t.Errorf("ByValue = %+v, %v", o, ok)
	}
	if o, ok := got.ByID("2"); !ok || o.Value != "Strazahq/site" {
		t.Errorf("ByID = %+v, %v", o, ok)
	}
	if _, ok := got.ByValue("strazahq/straza"); ok {
		t.Error("a case variant of a value was found")
	}
	if n := caller.callCount(); n != 1 {
		t.Errorf("the other replica's tick listed again: %d calls", n)
	}
	if recs := rec.all(); len(recs) != 1 {
		t.Errorf("records = %d, want one", len(recs))
	}
}

// TestListingRecords pins the record and the version through a server's
// life: the first listing writes one record, an unchanged listing writes
// no version and no record, a change raises the version and writes one
// with the joined and left counts, a failure keeps the objects and writes
// one, a second failure writes none, and a good listing after it writes
// one. No record carries an actor, an attribute or a value.
func TestListingRecords(t *testing.T) {
	st := newStore(t)
	spec := demoSpec()
	app := installApp(t, st, "objects-demo", &spec, false)
	caller := newCaller()
	first := map[string]*mcp.CallToolResult{"": page("", item("Strazahq/straza", 1, "product"), item("Strazahq/site", 2, "web"))}
	changed := map[string]*mcp.CallToolResult{"": page("", item("Strazahq/straza", 1, "product"), item("Strazahq/pay", 3, "product"))}
	rec, clk := &recorder{}, &clock{t: base}
	l := rig(st, caller, fakeSecrets{}, rec, clk, "pod-a")
	step := func(pages map[string]*mcp.CallToolResult, fail error) {
		t.Helper()
		caller.serve("objects-demo", pages)
		caller.errs["objects-demo"] = fail
		settle(l)
		clk.add(10 * time.Minute)
	}
	broken := errors.New("dial tcp: refused")
	const unreachable = "Straza could not reach objects-demo to call repo_list. Check objects-demo's logs on its Activity tab, then refresh."

	step(first, nil)
	step(first, nil)
	step(changed, nil)
	step(changed, broken)
	step(changed, broken)
	step(changed, nil)

	want := []map[string]any{
		{"outcome": "ok", "count": 2, "version": int64(1), "joined": 2, "left": 0},
		{"outcome": "ok", "count": 2, "version": int64(2), "joined": 1, "left": 1},
		{"outcome": "failed", "count": 2, "version": int64(2), "reason": unreachable},
		{"outcome": "ok", "count": 2, "version": int64(2), "joined": 0, "left": 0},
	}
	recs := rec.all()
	if len(recs) != len(want) {
		t.Fatalf("records = %+v, want %d", recs, len(want))
	}
	for i, w := range want {
		w["action"], w["target"], w["app"], w["tool"], w["type"] = "apps.objects.list", app.ID, "objects-demo", "repo_list", "repo"
		if !reflect.DeepEqual(recs[i], w) {
			t.Errorf("record %d = %v\nwant %v", i, recs[i], w)
		}
	}
	got := l.Get(app.ID)
	if got == nil || got.Version != 2 || got.Outcome != store.ObjectsOK || got.Joined != 1 || got.Left != 1 {
		t.Fatalf("held list = %+v", got)
	}
	values := []string{}
	for _, o := range got.Objects {
		values = append(values, o.Value)
		if o.Value == "Strazahq/pay" && o.AddedVersion != 2 {
			t.Errorf("Strazahq/pay joined at %d, want 2", o.AddedVersion)
		}
	}
	if !reflect.DeepEqual(values, []string{"Strazahq/pay", "Strazahq/straza"}) {
		t.Errorf("values = %v", values)
	}
}

// TestFailedListingKeepsTheList pins that a failure keeps the last good
// list in memory and in the store, with the reason in its status.
func TestFailedListingKeepsTheList(t *testing.T) {
	st := newStore(t)
	spec := demoSpec()
	app := installApp(t, st, "objects-demo", &spec, true)
	caller := newCaller()
	caller.serve("objects-demo", map[string]*mcp.CallToolResult{"": page("", item("Strazahq/straza", 1, nil))})
	secrets := fakeSecrets{app.ID: {ID: "l-1", Value: "tok"}}
	rec, clk := &recorder{}, &clock{t: base}
	l := rig(st, caller, secrets, rec, clk, "pod-a")
	settle(l)
	delete(secrets, app.ID)
	clk.add(10 * time.Minute)
	settle(l)
	got := l.Get(app.ID)
	if got == nil || got.Version != 1 || len(got.Objects) != 1 || got.Outcome != store.ObjectsFailed ||
		got.Reason != reasonNoCredential("objects-demo", "repos") {
		t.Fatalf("held list after the failure = %+v", got)
	}
	if n := caller.callCount(); n != 1 {
		t.Errorf("calls = %d, want the first listing only (no call without the listing credential)", n)
	}
}

// TestFailedListingRetry pins when a failed listing runs again: one minute
// after a failure a server that is still starting answers, as when a
// server that should be running is not running on this replica yet or its
// tool list was not read yet, and the full interval after one the server
// itself answered or one of a server an admin stopped or paused. A retry
// that fails the same way writes no second record, and the server lists at
// the first tick after it is up.
func TestFailedListingRetry(t *testing.T) {
	toolErr := map[string]*mcp.CallToolResult{"": {IsError: true, Content: []mcp.Content{&mcp.TextContent{Text: "upstream said no"}}}}
	cases := []struct {
		name   string
		status string
		setup  func(f *fakeCaller)
		after  time.Duration
	}{
		{name: "not running before the first probe", setup: func(f *fakeCaller) { delete(f.tools, "objects-demo") }, after: time.Minute},
		{name: "a server an admin stopped or paused", status: manager.StatusStopped, setup: func(f *fakeCaller) { delete(f.tools, "objects-demo") }, after: 10 * time.Minute},
		{name: "tool list not read yet", setup: func(f *fakeCaller) { f.invErrs["objects-demo"] = manager.ErrToolsUnread }, after: time.Minute},
		{name: "not ready at the call", setup: func(f *fakeCaller) { f.errs["objects-demo"] = fmt.Errorf("gone: %w", manager.ErrNotReady) }, after: time.Minute},
		{name: "unreachable", setup: func(f *fakeCaller) { f.errs["objects-demo"] = errors.New("dial tcp: refused") }, after: 10 * time.Minute},
		{name: "a tool error", setup: func(f *fakeCaller) { f.pages["objects-demo"] = toolErr }, after: 10 * time.Minute},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			st := newStore(t)
			spec := demoSpec()
			app := installApp(t, st, "objects-demo", &spec, false)
			if tc.status != "" {
				if err := st.Apps().SetStatus(ctx, app.ID, tc.status); err != nil {
					t.Fatal(err)
				}
			}
			good := map[string]*mcp.CallToolResult{"": page("", item("Strazahq/straza", 1, nil))}
			caller := newCaller()
			caller.serve("objects-demo", good)
			tc.setup(caller)
			rec, clk := &recorder{}, &clock{t: base}
			l := rig(st, caller, fakeSecrets{}, rec, clk, "pod-a")
			row := func() store.ObjectList {
				t.Helper()
				r, err := st.ObjectLists().Get(ctx, app.ID)
				if err != nil {
					t.Fatal(err)
				}
				return r
			}

			settle(l)
			if r := row(); r.Outcome != store.ObjectsFailed || !r.DueAt.Equal(base.Add(tc.after)) {
				t.Fatalf("after the failure: outcome %s, due %v; want failed, due %v later", r.Outcome, r.DueAt, tc.after)
			}
			clk.add(tc.after - time.Second)
			settle(l)
			if r := row(); r.AttemptedAt == nil || !r.AttemptedAt.Equal(base) {
				t.Fatalf("a tick before the retry was due listed again at %v", r.AttemptedAt)
			}
			clk.add(time.Second)
			settle(l)
			if r := row(); !r.AttemptedAt.Equal(base.Add(tc.after)) || !r.DueAt.Equal(base.Add(2*tc.after)) {
				t.Fatalf("the retry attempted at %v, due %v; want at %v, due %v later", r.AttemptedAt, r.DueAt, tc.after, tc.after)
			}
			if n := len(rec.all()); n != 1 {
				t.Errorf("records after a retry that failed the same way = %d, want 1", n)
			}

			caller.mu.Lock()
			caller.invErrs, caller.errs = map[string]error{}, map[string]error{}
			caller.mu.Unlock()
			caller.serve("objects-demo", good)
			clk.add(tc.after)
			settle(l)
			if got := l.Get(app.ID); got == nil || got.Outcome != store.ObjectsOK || got.Version != 1 {
				t.Fatalf("at the tick after the server is up the list is %+v, want listed", got)
			}
		})
	}
}

// TestRenamedListingCallsOnRenamed pins the rename hook: a listing that
// gives an id the list holds a new value calls OnRenamed once with the
// server and that id, while a first listing, a join, a leave and an
// unchanged listing never call it, and a nil hook is no fault.
func TestRenamedListingCallsOnRenamed(t *testing.T) {
	st := newStore(t)
	spec := demoSpec()
	app := installApp(t, st, "objects-demo", &spec, false)
	caller := newCaller()
	rec, clk := &recorder{}, &clock{t: base}
	l := rig(st, caller, fakeSecrets{}, rec, clk, "pod-a")
	type renamed struct {
		app string
		ids []string
	}
	var calls []renamed
	l.opts.OnRenamed = func(_ context.Context, appID string, ids []string) { calls = append(calls, renamed{appID, ids}) }
	straza, site, pay := item("Strazahq/straza", 1, nil), item("Strazahq/site", 2, nil), item("Strazahq/pay", 3, nil)
	steps := []struct {
		name  string
		items []map[string]any
		want  []renamed
	}{
		{name: "a first listing", items: []map[string]any{straza, site}},
		{name: "a join", items: []map[string]any{straza, site, pay}},
		{name: "a rename and a leave", items: []map[string]any{item("Strazahq/straza-go", 1, nil), pay},
			want: []renamed{{app.ID, []string{"1"}}}},
		{name: "an unchanged listing", items: []map[string]any{item("Strazahq/straza-go", 1, nil), pay},
			want: []renamed{{app.ID, []string{"1"}}}},
	}
	for _, s := range steps {
		caller.serve("objects-demo", map[string]*mcp.CallToolResult{"": page("", s.items...)})
		settle(l)
		clk.add(10 * time.Minute)
		if !reflect.DeepEqual(calls, s.want) {
			t.Fatalf("after %s the hook got %+v, want %+v", s.name, calls, s.want)
		}
	}

	l.opts.OnRenamed = nil
	caller.serve("objects-demo", map[string]*mcp.CallToolResult{"": page("", item("Strazahq/straza-rs", 1, nil), pay)})
	settle(l)
	if got := l.Get(app.ID); got == nil || got.Version != 4 {
		t.Errorf("a rename with no hook settled as %+v, want version 4", got)
	}
}

// TestDeclarationChangeStartsOver pins that a new declaration empties the
// list and lists again at once under a new version, and that a server
// whose manifest no longer pins a type loses its list.
func TestDeclarationChangeStartsOver(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()
	spec := demoSpec()
	app := installApp(t, st, "objects-demo", &spec, false)
	caller := newCaller()
	caller.serve("objects-demo", map[string]*mcp.CallToolResult{"": page("", item("Strazahq/straza", 1, "product"))})
	rec, clk := &recorder{}, &clock{t: base}
	l := rig(st, caller, fakeSecrets{}, rec, clk, "pod-a")
	settle(l)

	spec.List.Attributes = nil
	app.Manifest = manifestJSON(t, "objects-demo", &spec, false)
	if _, err := st.Apps().Update(ctx, app); err != nil {
		t.Fatal(err)
	}
	clk.add(time.Minute)
	settle(l)
	got := l.Get(app.ID)
	if got == nil || got.Version != 2 || got.DeclHash != DeclHash(spec) || len(got.Objects) != 1 || len(got.Objects[0].Attributes) != 0 {
		t.Fatalf("after the declaration change = %+v, want version 2 without the area attribute", got)
	}
	if n := caller.callCount(); n != 2 {
		t.Errorf("calls = %d, want a listing at once after the change", n)
	}

	app.Manifest = manifestJSON(t, "objects-demo", nil, false)
	if _, err := st.Apps().Update(ctx, app); err != nil {
		t.Fatal(err)
	}
	settle(l)
	if got := l.Get(app.ID); got != nil {
		t.Errorf("a server that pins no type still holds %+v", got)
	}
	if _, err := st.ObjectLists().Get(ctx, app.ID); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("its row = %v, want gone", err)
	}
}

// TestMissedAndRefresh pins the two ways to ask for a listing early: a
// missed object reaches the loop at most once a minute per server and
// lists only when the last listing is over a minute old, and a refresh
// marks the list due unless it was listed within the last minute.
func TestMissedAndRefresh(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()
	spec := demoSpec()
	app := installApp(t, st, "objects-demo", &spec, false)
	caller := newCaller()
	caller.serve("objects-demo", map[string]*mcp.CallToolResult{"": page("", item("Strazahq/straza", 1, nil))})
	rec, clk := &recorder{}, &clock{t: base}
	l := rig(st, caller, fakeSecrets{}, rec, clk, "pod-a")
	settle(l)

	clk.add(30 * time.Second)
	l.Missed(app.ID)
	settle(l)
	if n := caller.callCount(); n != 1 {
		t.Errorf("a miss 30 seconds after a listing listed again: %d calls", n)
	}
	if _, since, err := l.Refresh(ctx, app.ID); !errors.Is(err, ErrTooSoon) || since != 30*time.Second {
		t.Errorf("refresh 30 seconds after a listing = %v, %v; want ErrTooSoon after 30s", since, err)
	}

	clk.add(31 * time.Second)
	l.Missed(app.ID)
	settle(l)
	if n := caller.callCount(); n != 1 {
		t.Errorf("a second miss within the minute reached the loop: %d calls", n)
	}
	clk.add(30 * time.Second)
	l.Missed(app.ID)
	settle(l)
	if n := caller.callCount(); n != 2 {
		t.Errorf("a miss a minute after the last one did not list: %d calls", n)
	}

	clk.add(2 * time.Minute)
	due, _, err := l.Refresh(ctx, app.ID)
	if err != nil || !due.Equal(clk.now()) {
		t.Fatalf("refresh = %v, %v; want due now", due, err)
	}
	settle(l)
	if n := caller.callCount(); n != 3 {
		t.Errorf("a refresh did not list: %d calls", n)
	}
	if due, _, err := l.Refresh(ctx, "no-such-app"); err != nil || due.IsZero() {
		t.Errorf("refresh of a list the loop has not made = %v, %v; want due now", due, err)
	}
}

// TestMarkDueListsAtOnce pins the mark a listing credential change sets: the
// server lists on the next tick whatever its last listing.
func TestMarkDueListsAtOnce(t *testing.T) {
	st := newStore(t)
	spec := demoSpec()
	app := installApp(t, st, "objects-demo", &spec, false)
	caller := newCaller()
	caller.serve("objects-demo", map[string]*mcp.CallToolResult{"": page("", item("a", nil, nil))})
	rec, clk := &recorder{}, &clock{t: base}
	l := rig(st, caller, fakeSecrets{}, rec, clk, "pod-a")
	settle(l)
	clk.add(time.Second)
	if err := l.MarkDue(context.Background(), app.ID); err != nil {
		t.Fatal(err)
	}
	if err := l.MarkDue(context.Background(), "no-such-app"); err != nil {
		t.Errorf("MarkDue of a server without a list = %v", err)
	}
	settle(l)
	if n := caller.callCount(); n != 2 {
		t.Errorf("calls = %d, want a second listing after the mark", n)
	}
}

// TestRunStopsWithItsContext pins that Run lists on its tick, that Load
// fills memory from the store as a booting replica does, and that Run
// returns once its context ends.
func TestRunStopsWithItsContext(t *testing.T) {
	st := newStore(t)
	spec := demoSpec()
	app := installApp(t, st, "objects-demo", &spec, false)
	caller := newCaller()
	caller.serve("objects-demo", map[string]*mcp.CallToolResult{"": page("", item("a", nil, nil))})
	l := New(Options{Store: st, Caller: caller, Secrets: fakeSecrets{}, Owner: "pod-a", Record: (&recorder{}).record})
	l.every = 10 * time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { l.Run(ctx); close(done) }()
	deadline := time.Now().Add(5 * time.Second)
	for l.Get(app.ID) == nil || l.Get(app.ID).Version != 1 {
		if time.Now().After(deadline) {
			t.Fatal("Run did not list within 5 seconds")
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return after its context ended")
	}
	booted := New(Options{Store: st, Caller: caller, Secrets: fakeSecrets{}, Owner: "pod-b"})
	if err := booted.Load(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := booted.Get(app.ID); got == nil || got.Version != 1 || len(got.Objects) != 1 {
		t.Errorf("a booting replica loaded %+v, want version 1", got)
	}
}

// TestAttrJSON pins the attribute's JSON form: a string stays a string and
// a list stays a list.
func TestAttrJSON(t *testing.T) {
	for _, raw := range []string{`"product"`, `["web","docs"]`, `[]`} {
		var a Attr
		if err := a.UnmarshalJSON([]byte(raw)); err != nil {
			t.Fatal(err)
		}
		out, err := a.MarshalJSON()
		if err != nil || string(out) != raw {
			t.Errorf("%s round-tripped as %s, %v", raw, out, err)
		}
	}
}
