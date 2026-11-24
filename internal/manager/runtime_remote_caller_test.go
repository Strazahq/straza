package manager

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/strazahq/straza/internal/store"
)

// newUpstreamNotes is an in-process streamable-HTTP MCP server that keeps
// one note for each MCP session, as a server that keeps state per session
// does. Its tool remember stores the note of the calling session, and its
// tool recall answers that note, or "none" when the session stored none.
func newUpstreamNotes(t *testing.T) *httptest.Server {
	t.Helper()
	var mu sync.Mutex
	notes := map[string]string{}
	srv := mcp.NewServer(&mcp.Implementation{Name: "upstream-notes", Version: "1.0.0"}, nil)
	type rememberArgs struct {
		Text string `json:"text"`
	}
	mcp.AddTool(srv, &mcp.Tool{Name: "remember", Description: "remember"},
		func(_ context.Context, req *mcp.CallToolRequest, a rememberArgs) (*mcp.CallToolResult, any, error) {
			mu.Lock()
			notes[req.Session.ID()] = a.Text
			mu.Unlock()
			return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "stored"}}}, nil, nil
		})
	mcp.AddTool(srv, &mcp.Tool{Name: "recall", Description: "recall"},
		func(_ context.Context, req *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, any, error) {
			mu.Lock()
			note, ok := notes[req.Session.ID()]
			mu.Unlock()
			if !ok {
				note = "none"
			}
			return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: note}}}, nil, nil
		})
	up := httptest.NewServer(mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return srv }, nil))
	t.Cleanup(up.Close)
	return up
}

// TestRemoteRuntimeKeepsCallersApart pins that a remote runtime gives each
// Straza session, and each labelled MCP connection of it, an upstream
// session of its own, whatever credential the call carries, so a server
// that keeps state per session never shows one caller the state of another.
// One session or label keeps its own state from call to call, and a call
// Straza makes for itself shares none of a caller's.
func TestRemoteRuntimeKeepsCallersApart(t *testing.T) {
	t.Parallel()
	shared := &Secret{ID: "shared", Value: "one-secret"}
	cases := []struct {
		name     string
		inject   bool
		writer   CallInput // remembers the note
		reader   CallInput // recalls
		wantNote string
	}{
		{name: "two sessions on one shared secret", inject: true,
			writer: CallInput{Session: "ses-alice", Secret: shared}, reader: CallInput{Session: "ses-bob", Secret: shared}, wantNote: "none"},
		{name: "two sessions on a server with no credential",
			writer: CallInput{Session: "ses-alice"}, reader: CallInput{Session: "ses-bob"}, wantNote: "none"},
		{name: "an agent's session on its sponsor's credential row", inject: true,
			writer: CallInput{Session: "ses-sponsor", Secret: &Secret{ID: "row-1", Value: "own-token"}},
			reader: CallInput{Session: "ses-agent", Secret: &Secret{ID: "row-1", Value: "own-token"}}, wantNote: "none"},
		{name: "one session reads its own note", inject: true,
			writer: CallInput{Session: "ses-alice", Secret: shared}, reader: CallInput{Session: "ses-alice", Secret: shared}, wantNote: "alice's note"},
		{name: "a call of Straza's own after a caller's", inject: true,
			writer: CallInput{Session: "ses-alice", Secret: shared}, reader: CallInput{Secret: shared}, wantNote: "none"},
		{name: "a caller after a call of Straza's own", inject: true,
			writer: CallInput{Secret: shared}, reader: CallInput{Session: "ses-bob", Secret: shared}, wantNote: "none"},
		{name: "two connections of one session", inject: true,
			writer: CallInput{Session: "ses-alice", Conn: "conn-1", Secret: shared}, reader: CallInput{Session: "ses-alice", Conn: "conn-2", Secret: shared}, wantNote: "none"},
		{name: "a connection of a session and its calls with no label",
			writer: CallInput{Session: "ses-alice", Conn: "conn-1"}, reader: CallInput{Session: "ses-alice"}, wantNote: "none"},
		{name: "one label in two sessions", inject: true,
			writer: CallInput{Session: "ses-alice", Conn: "conn-1", Secret: shared}, reader: CallInput{Session: "ses-bob", Conn: "conn-1", Secret: shared}, wantNote: "none"},
		{name: "one connection reads its own note", inject: true,
			writer: CallInput{Session: "ses-alice", Conn: "conn-1", Secret: shared}, reader: CallInput{Session: "ses-alice", Conn: "conn-1", Secret: shared}, wantNote: "alice's note"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			up := newUpstreamNotes(t)
			spec, inject := RemoteSpec{URL: up.URL}, (*InjectSpec)(nil)
			if tc.inject {
				spec.Auth = AuthInject
				inject = &InjectSpec{As: InjectHeader, Name: "Authorization", Template: "{{secret}}"}
			}
			r := besideRuntime("notes", spec, inject)
			t.Cleanup(r.Stop)
			ctx := context.Background()

			tc.writer.Tool, tc.writer.Args = "remember", mustJSON(t, map[string]string{"text": "alice's note"})
			if _, err := r.Call(ctx, tc.writer); err != nil {
				t.Fatalf("remember: %v", err)
			}
			tc.reader.Tool = "recall"
			res, err := r.Call(ctx, tc.reader)
			if err != nil {
				t.Fatalf("recall: %v", err)
			}
			if got := res.Content[0].(*mcp.TextContent).Text; got != tc.wantNote {
				t.Errorf("recall answered %q, want %q", got, tc.wantNote)
			}
		})
	}
}

// sessionAt answers the upstream session pooled under key, or nil when
// there is none. The caller holds r.mu.
func (r *RemoteRuntime) sessionAt(key poolKey) *mcp.ClientSession {
	if p := r.pool[key]; p != nil {
		return p.s
	}
	return nil
}

// TestRemoteRuntimeRotationClosesEverySession pins that a rotated
// credential closes the superseded upstream session of every Straza session
// on it, not only the session whose call carried the new value, so the old
// token leaves the wire for all of them at once.
func TestRemoteRuntimeRotationClosesEverySession(t *testing.T) {
	t.Parallel()
	up := newUpstreamEcho(t)
	inject := &InjectSpec{As: InjectHeader, Name: "Authorization", Template: "Bearer {{secret}}"}
	r := besideRuntime("echo", RemoteSpec{URL: up.URL, Auth: AuthInject}, inject)
	t.Cleanup(r.Stop)
	call := func(session, value string) {
		t.Helper()
		if _, err := r.Call(context.Background(), CallInput{Tool: "echo", Args: mustJSON(t, map[string]string{"text": "hi"}),
			Session: session, Secret: &Secret{ID: "cred-1", Value: value}}); err != nil {
			t.Fatal(err)
		}
	}
	pooled := func() (n int, stale bool) {
		r.mu.Lock()
		defer r.mu.Unlock()
		for k := range r.pool {
			stale = stale || k.fp == sessionKey("", &Secret{ID: "cred-1", Value: "tok-v1"}).fp
		}
		return len(r.pool), stale
	}
	call("ses-alice", "tok-v1")
	call("ses-bob", "tok-v1")
	if n, _ := pooled(); n != 2 {
		t.Fatalf("two sessions on one credential pooled %d upstream sessions, want 2", n)
	}
	call("ses-alice", "tok-v2")
	if n, stale := pooled(); n != 1 || stale {
		t.Fatalf("after the rotation the pool holds %d sessions (one on the old value: %v), want only the session dialed on the new value", n, stale)
	}
	call("ses-bob", "tok-v2")
	if n, stale := pooled(); n != 2 || stale {
		t.Fatalf("after both sessions called on the new value the pool holds %d sessions (one on the old value: %v), want 2 on the new value", n, stale)
	}
}

// TestRemoteRuntimeRotationClosesLabelledSessions pins that a rotated
// credential closes the upstream session of every MCP connection of a
// Straza session on the old value, whichever connection's call carried the
// new value, labelled or not.
func TestRemoteRuntimeRotationClosesLabelledSessions(t *testing.T) {
	t.Parallel()
	for _, rotatedBy := range []string{"conn-1", ""} {
		t.Run("rotated by the label "+strconv.Quote(rotatedBy), func(t *testing.T) {
			t.Parallel()
			up := newUpstreamEcho(t)
			inject := &InjectSpec{As: InjectHeader, Name: "Authorization", Template: "Bearer {{secret}}"}
			r := besideRuntime("echo", RemoteSpec{URL: up.URL, Auth: AuthInject}, inject)
			t.Cleanup(r.Stop)
			call := func(conn, value string) {
				t.Helper()
				if _, err := r.Call(context.Background(), CallInput{Tool: "echo", Args: mustJSON(t, map[string]string{"text": "hi"}),
					Session: "ses-alice", Conn: conn, Secret: &Secret{ID: "cred-1", Value: value}}); err != nil {
					t.Fatal(err)
				}
			}
			for _, conn := range []string{"", "conn-1", "conn-2"} {
				call(conn, "tok-v1")
			}
			r.mu.Lock()
			n := len(r.pool)
			r.mu.Unlock()
			if n != 3 {
				t.Fatalf("three connections of one session pooled %d upstream sessions, want 3", n)
			}
			call(rotatedBy, "tok-v2")
			want := sessionKey("ses-alice", &Secret{ID: "cred-1", Value: "tok-v2"})
			want.conn = rotatedBy
			r.mu.Lock()
			defer r.mu.Unlock()
			if _, ok := r.pool[want]; len(r.pool) != 1 || !ok {
				t.Fatalf("after the rotation the pool holds %d sessions (the new one among them: %v), want only the session dialed on the new value", len(r.pool), ok)
			}
		})
	}
}

// TestRemoteRuntimeClosesIdleCallerSessions pins the idle close of a health
// pass: a caller's session that ran no call for idleAfter is closed and the
// upstream gets its DELETE, while a session used within the bound, a session
// a call still runs on and the app-level session stay. A labelled MCP
// connection's session goes the same way.
func TestRemoteRuntimeClosesIdleCallerSessions(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name      string
		conn      string        // the label of the caller's MCP connection
		idleAfter time.Duration // 0 keeps the default
		running   bool          // a call still runs on the caller's session at the pass
		wantKept  bool
	}{
		{name: "a session unused past the bound is closed", idleAfter: time.Millisecond},
		{name: "a session used within the bound is kept", wantKept: true},
		{name: "a session a call still runs on is kept", idleAfter: time.Millisecond, running: true, wantKept: true},
		{name: "a labelled session unused past the bound is closed", conn: "conn-1", idleAfter: time.Millisecond},
		{name: "a labelled session a call still runs on is kept", conn: "conn-1", idleAfter: time.Millisecond, running: true, wantKept: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			up := newMethodUpstream(t)
			r := besideRuntime("up", RemoteSpec{URL: up.URL}, nil)
			if tc.idleAfter != 0 {
				r.idleAfter = tc.idleAfter
			}
			t.Cleanup(r.Stop)
			ctx := context.Background()
			if err := r.Ping(ctx, nil); err != nil {
				t.Fatalf("warm ping: %v", err)
			}
			if _, err := r.Call(ctx, CallInput{Tool: "echo", Session: "ses-alice", Conn: tc.conn}); err != nil {
				t.Fatalf("warm call: %v", err)
			}
			if tc.running {
				callCtx, cancel := context.WithCancel(ctx)
				done := make(chan struct{})
				go func() {
					defer close(done)
					_, _ = r.Call(callCtx, CallInput{Tool: "wait", Session: "ses-alice", Conn: tc.conn})
				}()
				t.Cleanup(func() { cancel(); <-done })
				<-up.started
			}
			r.mu.Lock()
			app, caller := r.sessionAt(poolKey{}), r.sessionAt(poolKey{session: "ses-alice", conn: tc.conn})
			r.mu.Unlock()
			time.Sleep(20 * time.Millisecond)

			if err := r.Ping(ctx, nil); err != nil {
				t.Fatalf("the health pass: %v", err)
			}
			r.mu.Lock()
			appKept, callerKept := r.sessionAt(poolKey{}) == app, r.sessionAt(poolKey{session: "ses-alice", conn: tc.conn}) == caller
			r.mu.Unlock()
			if !appKept {
				t.Error("the health pass replaced the app-level session, which is never idle")
			}
			if callerKept != tc.wantKept {
				t.Fatalf("the caller's session kept = %v, want %v", callerKept, tc.wantKept)
			}
			wantDeletes := 1
			if tc.wantKept {
				wantDeletes = 0
			}
			for i := 0; i < 100 && up.count(http.MethodDelete) != wantDeletes; i++ {
				time.Sleep(20 * time.Millisecond)
			}
			if n := up.count(http.MethodDelete); n != wantDeletes {
				t.Errorf("session terminations the upstream saw = %d, want %d", n, wantDeletes)
			}
		})
	}
}

// TestCallRefusesAnEmptySession pins that Manager.Call sends no call
// upstream for an empty Straza session, on which the caller would share the
// upstream session Straza keeps for itself, and that its sentence says what
// was not called, why and who acts.
func TestCallRefusesAnEmptySession(t *testing.T) {
	t.Parallel()
	up := newMethodUpstream(t)
	mgr := New(Options{Store: testStore(t), Secrets: &tableSecrets{}, HealthInterval: time.Hour})
	rt := besideRuntime("notes", RemoteSpec{URL: up.URL}, nil)
	t.Cleanup(rt.Stop)
	mgr.byName["notes"] = &instance{app: store.App{ID: "app-1", Name: "notes"}, manifest: callerManifest(CredentialNone, "", RuntimeRemote), runtime: rt}

	_, err := mgr.Call(context.Background(), "", "", "notes", "echo", nil, nil, nil)
	const want = "notes was not called because the call names no Straza session, so Straza could not keep it apart from the calls of other sessions. " +
		"This is a fault in Straza, and an administrator reports it with the strazad log"
	if err == nil || err.Error() != want {
		t.Fatalf("call with no session = %v, want %q", err, want)
	}
	if n := up.count("tools/call") + up.count("initialize"); n != 0 {
		t.Errorf("the upstream saw %d requests of a call with no session, want 0", n)
	}
	if _, err := mgr.Call(context.Background(), "ses-alice", "", "notes", "echo", nil, nil, nil); err != nil {
		t.Fatalf("call with a session: %v", err)
	}
}

// TestRemoteRuntimeHealthPassIsNoUse pins that the ping of a health pass
// does not count as a use of a caller's session: a pass halfway through the
// idle bound pings the session and keeps it, and the next pass, past the
// bound since the call and within it since that ping, closes it.
func TestRemoteRuntimeHealthPassIsNoUse(t *testing.T) {
	t.Parallel()
	up := newMethodUpstream(t)
	r := besideRuntime("up", RemoteSpec{URL: up.URL}, nil)
	r.idleAfter = 400 * time.Millisecond
	t.Cleanup(r.Stop)
	ctx := context.Background()
	if _, err := r.Call(ctx, CallInput{Tool: "echo", Session: "ses-alice"}); err != nil {
		t.Fatalf("warm call: %v", err)
	}
	called := time.Now()
	time.Sleep(r.idleAfter / 2)
	if err := r.Ping(ctx, nil); err != nil {
		t.Fatalf("the first health pass: %v", err)
	}
	r.mu.Lock()
	kept := r.sessionAt(poolKey{session: "ses-alice"}) != nil
	r.mu.Unlock()
	if !kept && time.Since(called) < r.idleAfter {
		t.Fatal("a health pass within the idle bound closed the caller's session")
	}
	if n := up.count("ping"); n != 2 {
		t.Fatalf("the first health pass sent %d pings, want one for the app-level session and one for the caller's", n)
	}
	time.Sleep(r.idleAfter - time.Since(called) + 50*time.Millisecond)
	if err := r.Ping(ctx, nil); err != nil {
		t.Fatalf("the second health pass: %v", err)
	}
	r.mu.Lock()
	kept = r.sessionAt(poolKey{session: "ses-alice"}) != nil
	r.mu.Unlock()
	if kept {
		t.Fatal("the caller's session outlived the idle bound, so the ping of a health pass counted as a use")
	}
}

// TestRemoteRuntimeAnsweredCallDoesNotWaitForADial pins that a call whose
// answer arrived returns at once while another Straza session's first call
// still dials its upstream session.
func TestRemoteRuntimeAnsweredCallDoesNotWaitForADial(t *testing.T) {
	t.Parallel()
	up := newMethodUpstream(t)
	r := besideRuntime("up", RemoteSpec{URL: up.URL}, nil)
	t.Cleanup(r.Stop)
	ctx := context.Background()
	if _, err := r.Call(ctx, CallInput{Tool: "echo", Session: "ses-alice"}); err != nil {
		t.Fatalf("warm call: %v", err)
	}
	up.slowDown("tools/call", 300*time.Millisecond)
	up.slowDown("initialize", 2*time.Second)
	var took time.Duration
	done := make(chan error, 1)
	go func() {
		start := time.Now()
		_, err := r.Call(ctx, CallInput{Tool: "echo", Session: "ses-alice"})
		took = time.Since(start)
		done <- err
	}()
	for i := 0; i < 100 && up.count("tools/call") < 2; i++ {
		time.Sleep(5 * time.Millisecond)
	}
	bob := make(chan struct{})
	go func() {
		defer close(bob)
		_, _ = r.Call(ctx, CallInput{Tool: "echo", Session: "ses-bob"})
	}()
	t.Cleanup(func() { <-bob })
	if err := <-done; err != nil {
		t.Fatalf("alice's call: %v", err)
	}
	if took > time.Second {
		t.Fatalf("alice's answered call took %v, so it waited for bob's 2 s connect handshake", took.Round(10*time.Millisecond))
	}
}

// TestRemoteRuntimeStopClosesSessionsTogether pins that Stop closes the
// pooled sessions at the same time, so stopping a server that no longer
// answers takes one connect timeout and not one for each caller's session.
func TestRemoteRuntimeStopClosesSessionsTogether(t *testing.T) {
	t.Parallel()
	up := newMethodUpstream(t)
	r := besideRuntime("up", RemoteSpec{URL: up.URL}, nil)
	r.ConnectTimeout = shortPing
	ctx := context.Background()
	for _, session := range []string{"ses-1", "ses-2", "ses-3", "ses-4", "ses-5", "ses-6"} {
		if _, err := r.Call(ctx, CallInput{Tool: "echo", Session: session}); err != nil {
			t.Fatalf("warm call of %s: %v", session, err)
		}
	}
	up.stall(t)
	took := within(t, 10*time.Second, "Stop", r.Stop)
	if took > 3*shortPing {
		t.Fatalf("Stop of six sessions on a server that does not answer took %v, want about one connect timeout of %v", took.Round(10*time.Millisecond), shortPing)
	}
	if _, err := r.Call(ctx, CallInput{Tool: "echo", Session: "ses-1"}); !errors.Is(err, ErrNotReady) {
		t.Fatalf("a call after Stop = %v, want the not-running refusal", err)
	}
}

// TestRemoteRuntimeCapsConnectionsOfASession pins the cap of
// maxCallerSessions upstream sessions for one Straza session on one server.
// A new label at the cap closes the least recently used session of that
// Straza session that runs no call, and never another Straza session's or
// Straza's own, both older. A label that has its session reuses it. When
// every session at the cap runs a call, the call is refused with the
// sentence and no request of it reaches the server.
func TestRemoteRuntimeCapsConnectionsOfASession(t *testing.T) {
	t.Parallel()
	label := func(i int) string { return fmt.Sprintf("conn-%d", i) }
	cases := []struct {
		name       string
		running    bool   // every session of ses-alice runs a call when the next call comes
		next       string // the label of the next call
		wantClosed string // the label whose session the next call closes, empty for none
		wantErr    string
	}{
		{name: "a new label closes the least recently used idle session", next: label(maxCallerSessions), wantClosed: label(1)},
		{name: "a label that has its session reuses it", next: label(5)},
		{name: "a new label while every session runs a call is refused", running: true, next: label(maxCallerSessions),
			wantErr: "this session already runs 64 calls at once on the MCP server up, each on its own connection. Wait for one to finish, then call again"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			up := newMethodUpstream(t)
			r := besideRuntime("up", RemoteSpec{URL: up.URL}, nil)
			t.Cleanup(r.Stop)
			ctx := context.Background()
			snapshot := func() map[poolKey]*mcp.ClientSession {
				r.mu.Lock()
				defer r.mu.Unlock()
				out := make(map[poolKey]*mcp.ClientSession, len(r.pool))
				for k, p := range r.pool {
					out[k] = p.s
				}
				return out
			}
			if err := r.Ping(ctx, nil); err != nil {
				t.Fatalf("warm ping: %v", err)
			}
			if _, err := r.Call(ctx, CallInput{Tool: "echo", Session: "ses-bob"}); err != nil {
				t.Fatalf("bob's call: %v", err)
			}
			others := snapshot()
			callCtx, cancel := context.WithCancel(ctx)
			var running sync.WaitGroup
			t.Cleanup(func() { cancel(); running.Wait() })
			for i := range maxCallerSessions {
				if tc.running {
					running.Go(func() { _, _ = r.Call(callCtx, CallInput{Tool: "wait", Session: "ses-alice", Conn: label(i)}) })
					<-up.started
				} else if _, err := r.Call(ctx, CallInput{Tool: "echo", Session: "ses-alice", Conn: label(i)}); err != nil {
					t.Fatalf("the call of %s: %v", label(i), err)
				}
			}
			if !tc.running {
				if _, err := r.Call(ctx, CallInput{Tool: "echo", Session: "ses-alice", Conn: label(0)}); err != nil {
					t.Fatalf("the second call of %s: %v", label(0), err)
				}
			}
			before, inits, calls := snapshot(), up.count("initialize"), up.count("tools/call")

			_, err := r.Call(ctx, CallInput{Tool: "echo", Session: "ses-alice", Conn: tc.next})
			if tc.wantErr != "" {
				var busy *BusyError
				if !errors.As(err, &busy) || err.Error() != tc.wantErr {
					t.Fatalf("the call at the cap = %v, want %q", err, tc.wantErr)
				}
				if n, m := up.count("initialize")-inits, up.count("tools/call")-calls; n != 0 || m != 0 {
					t.Errorf("the refused call sent %d connect handshakes and %d tool calls upstream, want none", n, m)
				}
			} else if err != nil {
				t.Fatalf("the call of %s: %v", tc.next, err)
			}
			after, closed := snapshot(), poolKey{session: "ses-alice", conn: tc.wantClosed}
			for k, s := range before {
				switch kept := after[k] == s; {
				case tc.wantClosed != "" && k == closed && kept:
					t.Errorf("the session of %s, the least recently used, was kept", k.conn)
				case k != closed && !kept:
					t.Errorf("the session of Straza session %q label %q was closed, want it kept", k.session, k.conn)
				}
			}
			for k, s := range others {
				if after[k] != s {
					t.Errorf("the session of Straza session %q was closed while ses-alice opened its own, want it kept", k.session)
				}
			}
			held := 0
			for k := range after {
				if k.session == "ses-alice" {
					held++
				}
			}
			if held != maxCallerSessions {
				t.Errorf("ses-alice holds %d upstream sessions, want %d", held, maxCallerSessions)
			}
			wantDeletes := 0
			if tc.wantClosed != "" {
				wantDeletes = 1
			}
			for i := 0; i < 100 && up.count(http.MethodDelete) != wantDeletes; i++ {
				time.Sleep(20 * time.Millisecond)
			}
			if n := up.count(http.MethodDelete); n != wantDeletes {
				t.Errorf("session terminations the upstream saw = %d, want %d", n, wantDeletes)
			}
		})
	}
}

// waitingInEntry answers how many goroutines wait in entry of r for a dial
// of their key that another call has in flight. A test that reads it runs
// without t.Parallel, so no other test's goroutines are in its count.
func waitingInEntry(r *RemoteRuntime) int {
	buf := make([]byte, 1<<22)
	buf = buf[:runtime.Stack(buf, true)]
	frame := fmt.Sprintf("manager.(*RemoteRuntime).entry(%p", r)
	n := 0
	for _, g := range strings.Split(string(buf), "\n\n") {
		header, frames, _ := strings.Cut(g, "\n")
		if strings.Contains(header, "[select") && strings.Contains(frames, frame) && !strings.Contains(frames, "manager.(*RemoteRuntime).dial(") {
			n++
		}
	}
	return n
}

// TestRemoteRuntimeDialHoldsUpNoOtherKey pins that a dial runs off the
// runtime lock: while one Straza session's first dial waits at the
// handshake, a call of another Straza session on its pooled session
// returns, and the first call of a third dials and returns.
func TestRemoteRuntimeDialHoldsUpNoOtherKey(t *testing.T) {
	t.Parallel()
	up := newMethodUpstream(t)
	r := besideRuntime("up", RemoteSpec{URL: up.URL}, nil)
	t.Cleanup(r.Stop)
	call := func(session string) error {
		_, err := r.Call(context.Background(), CallInput{Tool: "echo", Session: session})
		return err
	}
	if err := call("ses-bob"); err != nil {
		t.Fatalf("bob's first call: %v", err)
	}
	arrived, release := up.holdNextInit(t)
	alice := make(chan error, 1)
	go func() { alice <- call("ses-alice") }()
	<-arrived
	for _, session := range []string{"ses-bob", "ses-carol"} {
		var err error
		within(t, 5*time.Second, "the call of "+session+" while alice's handshake is held", func() { err = call(session) })
		if err != nil {
			t.Fatalf("the call of %s: %v", session, err)
		}
	}
	release()
	if err := <-alice; err != nil {
		t.Fatalf("alice's call once its handshake went on: %v", err)
	}
}

// TestRemoteRuntimeDialsAKeyOnce pins that two calls on one new key open one
// upstream session: the second waits for the dial the first has in flight
// and takes its session.
func TestRemoteRuntimeDialsAKeyOnce(t *testing.T) {
	up := newMethodUpstream(t)
	r := besideRuntime("up", RemoteSpec{URL: up.URL}, nil)
	t.Cleanup(r.Stop)
	in := CallInput{Tool: "echo", Session: "ses-alice", Conn: "conn-1"}
	arrived, release := up.holdNextInit(t)
	errs := make(chan error, 2)
	go func() { _, err := r.Call(context.Background(), in); errs <- err }()
	<-arrived
	go func() { _, err := r.Call(context.Background(), in); errs <- err }()
	waitFor(t, 5*time.Second, "the second call waiting for the first call's dial", func() bool { return waitingInEntry(r) == 1 })
	release()
	for range 2 {
		if err := <-errs; err != nil {
			t.Fatalf("a call on the key: %v", err)
		}
	}
	r.mu.Lock()
	pooled := len(r.pool)
	r.mu.Unlock()
	if n := up.count("initialize"); n != 1 || pooled != 1 {
		t.Fatalf("two calls on one new key sent %d connect handshakes and pooled %d sessions, want 1 and 1", n, pooled)
	}
}

// TestRemoteRuntimeStopDoesNotWaitForADial pins that Stop returns while a
// dial waits at the handshake, that the call of that dial then answers the
// not-running sentence, and that the session the dial opened late gets its
// DELETE and is never pooled.
func TestRemoteRuntimeStopDoesNotWaitForADial(t *testing.T) {
	t.Parallel()
	up := newMethodUpstream(t)
	r := besideRuntime("up", RemoteSpec{URL: up.URL}, nil)
	t.Cleanup(r.Stop)
	arrived, release := up.holdNextInit(t)
	errs := make(chan error, 1)
	go func() {
		_, err := r.Call(context.Background(), CallInput{Tool: "echo", Session: "ses-alice"})
		errs <- err
	}()
	<-arrived
	within(t, 5*time.Second, "Stop while a handshake is held", r.Stop)
	release()
	if err, want := <-errs, notRunning("up").Error(); err == nil || err.Error() != want {
		t.Fatalf("the call whose dial outlived Stop = %v, want %q", err, want)
	}
	waitFor(t, 5*time.Second, "the DELETE of the session the dial opened late", func() bool { return up.count(http.MethodDelete) == 1 })
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.pool) != 0 || len(r.dials) != 0 {
		t.Fatalf("after Stop the runtime holds %d sessions and %d dials, want none", len(r.pool), len(r.dials))
	}
}

// TestRemoteRuntimeCapCountsADialInFlight pins that the cap counts a dial in
// flight: with maxCallerSessions-1 sessions pooled and one dial held at the
// handshake, a new label closes the least recently used pooled session,
// never the dial, and the Straza session never holds more than the cap.
func TestRemoteRuntimeCapCountsADialInFlight(t *testing.T) {
	t.Parallel()
	up := newMethodUpstream(t)
	r := besideRuntime("up", RemoteSpec{URL: up.URL}, nil)
	t.Cleanup(r.Stop)
	label := func(i int) string { return fmt.Sprintf("conn-%d", i) }
	call := func(conn string) error {
		_, err := r.Call(context.Background(), CallInput{Tool: "echo", Session: "ses-alice", Conn: conn})
		return err
	}
	for i := range maxCallerSessions - 1 {
		if err := call(label(i)); err != nil {
			t.Fatalf("the call of %s: %v", label(i), err)
		}
	}
	held := func() (pooled int, dialing, oldest bool) {
		r.mu.Lock()
		defer r.mu.Unlock()
		_, dialing = r.dials[poolKey{session: "ses-alice", conn: label(maxCallerSessions - 1)}]
		return len(r.pool), dialing, r.sessionAt(poolKey{session: "ses-alice", conn: label(0)}) != nil
	}
	arrived, release := up.holdNextInit(t)
	dialed := make(chan error, 1)
	go func() { dialed <- call(label(maxCallerSessions - 1)) }()
	<-arrived
	if err := call(label(maxCallerSessions)); err != nil {
		t.Fatalf("the call of a new label beside the held dial: %v", err)
	}
	if pooled, dialing, oldest := held(); pooled != maxCallerSessions-1 || !dialing || oldest {
		t.Fatalf("beside the held dial the pool holds %d sessions (the dial in flight: %v, %s kept: %v), want %d, true and false",
			pooled, dialing, label(0), oldest, maxCallerSessions-1)
	}
	release()
	if err := <-dialed; err != nil {
		t.Fatalf("the held dial's call: %v", err)
	}
	if pooled, dialing, _ := held(); pooled != maxCallerSessions || dialing {
		t.Fatalf("after the held dial the pool holds %d sessions (still dialing: %v), want %d", pooled, dialing, maxCallerSessions)
	}
}

// TestRemoteRuntimeFailedDialLeavesNoWaiterStuck pins the waiters of a dial
// in flight: a waiter whose context ends answers the context's error and
// dials nothing, and when the dial fails its entry is gone, so the next
// waiter dials its own session and gets it.
func TestRemoteRuntimeFailedDialLeavesNoWaiterStuck(t *testing.T) {
	up := newMethodUpstream(t)
	r := besideRuntime("up", RemoteSpec{URL: up.URL}, nil)
	t.Cleanup(r.Stop)
	in := CallInput{Tool: "echo", Session: "ses-alice"}
	callOn := func(ctx context.Context) <-chan error {
		errs := make(chan error, 1)
		go func() { _, err := r.Call(ctx, in); errs <- err }()
		return errs
	}
	arrived, _ := up.holdNextInit(t)
	dialCtx, failDial := context.WithCancel(context.Background())
	defer failDial()
	dialer := callOn(dialCtx)
	<-arrived
	leaveCtx, leave := context.WithCancel(context.Background())
	defer leave()
	patient, leaver := callOn(context.Background()), callOn(leaveCtx)
	waitFor(t, 5*time.Second, "two calls waiting for the held dial", func() bool { return waitingInEntry(r) == 2 })

	leave()
	if err := <-leaver; !errors.Is(err, context.Canceled) {
		t.Fatalf("the waiter whose context ended answered %v, want context.Canceled", err)
	}
	failDial()
	if err := <-dialer; err == nil {
		t.Fatal("the dial whose caller left answered nil")
	}
	if err := <-patient; err != nil {
		t.Fatalf("the waiter behind the failed dial answered %v, want its own dial's session", err)
	}
	r.mu.Lock()
	pooled, dials := len(r.pool), len(r.dials)
	r.mu.Unlock()
	if n := up.count("initialize"); n != 2 || pooled != 1 || dials != 0 {
		t.Fatalf("connect handshakes %d, pooled sessions %d, dials in flight %d; want 2, 1 and 0", n, pooled, dials)
	}
}

// rotatingRuntime answers a runtime on up that injects its credential, and
// two values of one credential row, the old one and the rotated one.
func rotatingRuntime(t *testing.T, up *methodUpstream) (r *RemoteRuntime, v1, v2 *Secret) {
	t.Helper()
	inject := &InjectSpec{As: InjectHeader, Name: "Authorization", Template: "Bearer {{secret}}"}
	r = besideRuntime("up", RemoteSpec{URL: up.URL, Auth: AuthInject}, inject)
	t.Cleanup(r.Stop)
	return r, &Secret{ID: "row-1", Value: "tok-v1"}, &Secret{ID: "row-1", Value: "tok-v2"}
}

// TestRemoteRuntimeSupersededDialServesOnce pins that on one credential row
// the dial that started last wins. A dial on the old value, held at its
// handshake while a dial on the rotated value starts, dials and answers,
// serves only the use that asked for it once released, and is closed after
// it: it never appears in the pool and the rotated value's session stays.
// Each use took one handshake, so the two dials never take turns again.
func TestRemoteRuntimeSupersededDialServesOnce(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	cases := []struct {
		name    string
		session string // the Straza session of the use, empty for Straza's own
		use     func(r *RemoteRuntime, secret *Secret) error
	}{
		{"a caller's tool call", "ses-alice", func(r *RemoteRuntime, secret *Secret) error {
			_, err := r.Call(ctx, CallInput{Tool: "echo", Session: "ses-alice", Secret: secret})
			return err
		}},
		{"Straza's own health pass", "", func(r *RemoteRuntime, secret *Secret) error { return r.Ping(ctx, secret) }},
		{"Straza's own tool listing", "", func(r *RemoteRuntime, secret *Secret) error {
			_, err := r.Tools(ctx, secret)
			return err
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			up := newMethodUpstream(t)
			r, v1, v2 := rotatingRuntime(t, up)
			arrived, release := up.holdNextInit(t)
			first := make(chan error, 1)
			go func() { first <- tc.use(r, v1) }()
			<-arrived
			if err := tc.use(r, v2); err != nil {
				t.Fatalf("the use on the rotated value: %v", err)
			}
			newKey := sessionKey(tc.session, v2)
			r.mu.Lock()
			newer := r.sessionAt(newKey)
			r.mu.Unlock()
			release()
			if err := <-first; err != nil {
				t.Fatalf("the use whose dial was superseded: %v", err)
			}
			waitFor(t, 5*time.Second, "the DELETE of the superseded session", func() bool { return up.count(http.MethodDelete) == 1 })
			r.mu.Lock()
			defer r.mu.Unlock()
			for k := range r.pool {
				if k.fp == sessionKey("", v1).fp {
					t.Errorf("the pool holds a session on the old value for Straza session %q", k.session)
				}
			}
			if newer == nil || r.sessionAt(newKey) != newer || len(r.pool) != 1 {
				t.Errorf("the pool holds %d sessions, the rotated value's kept: %v; want only that one", len(r.pool), newer != nil && r.sessionAt(newKey) == newer)
			}
			if n := up.count("initialize"); n != 2 {
				t.Errorf("connect handshakes = %d, want one for each use", n)
			}
		})
	}
}

// TestRemoteRuntimeSupersededFailedCallEvictsNothingElse pins that a tool
// call that fails on a superseded session closes that session and leaves
// every pooled session where it was.
func TestRemoteRuntimeSupersededFailedCallEvictsNothingElse(t *testing.T) {
	t.Parallel()
	up := newMethodUpstream(t)
	r, v1, v2 := rotatingRuntime(t, up)
	call := func(session string, secret *Secret) error {
		_, err := r.Call(context.Background(), CallInput{Tool: "echo", Session: session, Secret: secret})
		return err
	}
	arrived, release := up.holdNextInit(t)
	first := make(chan error, 1)
	go func() { first <- call("ses-alice", v1) }()
	<-arrived
	for _, session := range []string{"ses-alice", "ses-bob"} {
		if err := call(session, v2); err != nil {
			t.Fatalf("the call of %s on the rotated value: %v", session, err)
		}
	}
	r.mu.Lock()
	before := make(map[poolKey]*mcp.ClientSession, len(r.pool))
	for k, p := range r.pool {
		before[k] = p.s
	}
	r.mu.Unlock()
	up.failToolCalls()
	release()
	if err := <-first; err == nil {
		t.Fatal("the call on the superseded session answered nil, want the server's failure")
	}
	waitFor(t, 5*time.Second, "the DELETE of the superseded session", func() bool { return up.count(http.MethodDelete) == 1 })
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.pool) != len(before) {
		t.Errorf("the pool holds %d sessions, want the %d it held before", len(r.pool), len(before))
	}
	for k, s := range before {
		if r.sessionAt(k) != s {
			t.Errorf("the session of %s on the rotated value was evicted", k.session)
		}
	}
}
