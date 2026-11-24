package service

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/strazahq/straza/internal/agentguard/localapi"
)

// checkinBodies returns the body of every /v1/checkin request the running
// service sent, refreshes included, in the order sent.
func (r *credRig) checkinBodies(t *testing.T) []map[string]any {
	t.Helper()
	r.sent.mu.Lock()
	lines := append([]string(nil), r.sent.reqs...)
	r.sent.mu.Unlock()
	var out []map[string]any
	for _, line := range lines {
		i := strings.IndexByte(line, '{')
		if !strings.HasPrefix(line, "POST /v1/checkin ") || i < 0 {
			continue
		}
		var body map[string]any
		if err := json.Unmarshal([]byte(line[i:]), &body); err != nil {
			t.Fatalf("a check-in body that is not JSON: %q", line)
		}
		out = append(out, body)
	}
	return out
}

// startsOf returns the bodies that start a session: every check-in but a
// refresh.
func startsOf(bodies []map[string]any) []map[string]any {
	var out []map[string]any
	for _, b := range bodies {
		if _, refresh := b["session_token"]; !refresh {
			out = append(out, b)
		}
	}
	return out
}

// TestHostName pins the host name a check-in carries: read once, cut to
// hostMax bytes on a rune boundary from its start, and left out when the
// read fails or gives a name the server would refuse.
func TestHostName(t *testing.T) {
	a := func(n int) string { return strings.Repeat("a", n) }
	cases := []struct {
		name, in string
		err      error
		want     string
	}{
		{"a host name", "kims-mac", nil, "kims-mac"},
		{"a read that failed", "kims-mac", errors.New("no host name"), ""},
		{"empty", "", nil, ""},
		{"exactly the bound", a(hostMax), nil, a(hostMax)},
		{"over the bound keeps the start", a(hostMax) + "-tail", nil, a(hostMax)},
		{"a rune on the cut", a(hostMax-1) + "é", nil, a(hostMax - 1)},
		{"a NUL character", "kims\x00mac", nil, ""},
		{"bytes that are not UTF-8", "kims\xffmac", nil, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := hostName(tc.in, tc.err); got != tc.want {
				t.Fatalf("hostName = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestCredCheckinWhere pins that a check-in that starts a session carries
// the service's host name and the start's folder on a person's key lane,
// on an agent's lane and on the agent's retry after strazad refused its
// cached ID token, and leaves each out when it is empty, so a start without
// a folder sends the check-in body with the host and no folder key.
func TestCredCheckinWhere(t *testing.T) {
	sam := Caller{UID: testUID(65532)}
	enrollSam := func(t *testing.T, r *credRig) {
		t.Helper()
		if _, err := r.s.Keygen(context.Background(), sam, localapi.KeygenRequest{Username: "sam"}); err != nil {
			t.Fatal(err)
		}
		if _, err := r.s.Enroll(context.Background(), sam, localapi.EnrollRequest{ServerURL: r.srv.serverURL(), Headless: true}, func(localapi.EnrollEvent) {}); err != nil {
			t.Fatal(err)
		}
	}
	lanes := []struct {
		name   string
		caller Caller
		enroll func(t *testing.T, r *credRig)
		starts int // the check-ins the start sends at least
	}{
		{"a person's key lane", alice, func(t *testing.T, r *credRig) { r.enroll(alice) }, 1},
		{"an agent's lane", sam, enrollSam, 1},
		{"an agent's retry after a refused cached ID token", sam, func(t *testing.T, r *credRig) {
			enrollSam(t, r)
			r.srv.with(func() { r.srv.idTokenRefuse = 1 })
		}, 2},
	}
	values := []struct {
		name, host, folder string
	}{
		{"host and folder", "kims-mac", "~/src/app"},
		{"no folder, as from an older thin client", "kims-mac", ""},
		{"no host, as when the name could not be read", "", "~/src/app"},
		{"neither", "", ""},
	}
	for _, lane := range lanes {
		for _, v := range values {
			t.Run(lane.name+"/"+v.name, func(t *testing.T) {
				r := newCredRig(t)
				if want := hostName(os.Hostname()); r.s.host != want {
					t.Fatalf("the service's host = %q, want os.Hostname as hostName cuts it, %q", r.s.host, want)
				}
				r.s.host = v.host
				lane.enroll(t, r)
				h := localapi.Harness{Name: "claude-code", Version: "2.1.0", Session: "w1"}
				if _, err := r.s.Start(context.Background(), lane.caller, h, v.folder); err != nil {
					t.Fatal(err)
				}
				starts := startsOf(r.checkinBodies(t))
				if len(starts) < lane.starts {
					t.Fatalf("%d check-ins started the session, want at least %d", len(starts), lane.starts)
				}
				for _, body := range starts {
					for key, want := range map[string]string{"host": v.host, "folder": v.folder} {
						got, sent := body[key]
						if sent != (want != "") || (sent && got != want) {
							t.Errorf("%s = %v (sent %v), want %q and left out when empty: %v", key, got, sent, want, body)
						}
					}
					if harness, _ := body["harness"].(map[string]any); harness["name"] != "claude-code" {
						t.Errorf("body = %v, want the harness beside them", body)
					}
				}
			})
		}
	}
}

// TestCredStartFolder pins the folder the service keeps with a session:
// each window keeps the folder of the start that checked it in, a reused
// window keeps its own and checks nothing in, a re-acquire after a refused
// refresh sends the same folder, a revoked window started again without a
// folder keeps its own, and the saved session keeps it across a restart,
// while one saved before folders reads back with none.
func TestCredStartFolder(t *testing.T) {
	ctx := context.Background()
	w := func(id string) localapi.Harness {
		return localapi.Harness{Name: "claude-code", Version: "2.1.0", Session: id}
	}
	lastStartFolder := func(t *testing.T, r *credRig) any {
		t.Helper()
		starts := startsOf(r.checkinBodies(t))
		if len(starts) == 0 {
			t.Fatal("no check-in started a session")
		}
		return starts[len(starts)-1]["folder"]
	}
	start := func(t *testing.T, r *credRig, id, folder string) Session {
		t.Helper()
		ses, err := r.s.Start(ctx, alice, w(id), folder)
		if err != nil {
			t.Fatal(err)
		}
		return ses
	}

	t.Run("two windows of one harness in two folders", func(t *testing.T) {
		r := newCredRig(t)
		r.enroll(alice)
		ids := map[string]bool{}
		for _, win := range []struct{ id, folder string }{{"w1", "~/one"}, {"w2", "~/two"}} {
			ses := start(t, r, win.id, win.folder)
			if ses.Reused || ses.Folder != win.folder || lastStartFolder(t, r) != win.folder {
				t.Fatalf("window %s: reused %v, folder %q, check-in folder %v, want a new session in %q", win.id, ses.Reused, ses.Folder, lastStartFolder(t, r), win.folder)
			}
			ids[ses.ID] = true
		}
		if len(ids) != 2 {
			t.Fatalf("sessions %v, want two", ids)
		}
	})

	t.Run("a second start of one window keeps its folder", func(t *testing.T) {
		r := newCredRig(t)
		r.enroll(alice)
		first := start(t, r, "w1", "~/one")
		before := len(r.checkinBodies(t))
		again := start(t, r, "w1", "~/elsewhere")
		if !again.Reused || again.ID != first.ID || again.Folder != "~/one" {
			t.Fatalf("second start: reused %v, same session %v, folder %q, want the first session in ~/one", again.Reused, again.ID == first.ID, again.Folder)
		}
		if n := len(r.checkinBodies(t)); n != before {
			t.Fatalf("%d check-ins after the second start, want %d", n, before)
		}
	})

	t.Run("a re-acquire after a refused refresh sends the same folder", func(t *testing.T) {
		r := newCredRig(t)
		r.enroll(alice)
		first := start(t, r, "w1", "~/one")
		r.srv.expireSessions()
		r.clock.Advance(tokenLife - renewBefore + 20*time.Second)
		got, err := r.s.Session(ctx, alice, first.Harness, false)
		if err != nil || got.ID == first.ID || got.Folder != "~/one" {
			t.Fatalf("after the refused refresh: %+v, %v, want a new session in ~/one", got, err)
		}
		bodies := r.checkinBodies(t)
		if len(startsOf(bodies)) == len(bodies) {
			t.Fatal("no refresh was sent before the re-acquire")
		}
		if f := lastStartFolder(t, r); f != "~/one" {
			t.Fatalf("the re-acquire's folder = %v, want ~/one", f)
		}
	})

	// The proxy lane, `straza mcp`, git-remote-straza and `straza connect`,
	// starts a session again through Session with start set and names no
	// folder, so a window the account still holds lends its own.
	restarts := []struct {
		name, folder, want string
	}{
		{"a revoked window the proxy lane starts again keeps its folder", "", "~/one"},
		{"a revoked window started again in a named folder takes that folder", "~/two", "~/two"},
	}
	for _, tc := range restarts {
		t.Run(tc.name, func(t *testing.T) {
			r := newCredRig(t)
			r.enroll(alice)
			h := localapi.Harness{Name: "claude-code", Version: "mcp"}
			first, err := r.s.Start(ctx, alice, h, "~/one")
			if err != nil {
				t.Fatal(err)
			}
			a := r.s.account(alice.UID)
			r.s.revoke(a, a.find(h), first.ID, "kill-switch push from the server")
			var got Session
			if tc.folder == "" {
				got, err = r.s.Session(ctx, alice, h, true) // api_proxy.go proxySession
			} else {
				got, err = r.s.Start(ctx, alice, h, tc.folder)
			}
			if err != nil || got.ID == first.ID || got.Folder != tc.want || lastStartFolder(t, r) != tc.want {
				t.Fatalf("the start after the revocation: %+v, %v, check-in folder %v, want a new session in %s", got, err, lastStartFolder(t, r), tc.want)
			}
		})
	}

	t.Run("a start that names no folder and holds no window sends none", func(t *testing.T) {
		r := newCredRig(t)
		r.enroll(alice)
		ses, err := r.s.Session(ctx, alice, w("w1"), true)
		if err != nil || ses.Folder != "" || lastStartFolder(t, r) != nil {
			t.Fatalf("the start: %+v, %v, check-in folder %v, want none", ses, err, lastStartFolder(t, r))
		}
	})

	t.Run("a saved session keeps its folder across a restart", func(t *testing.T) {
		r := newCredRig(t)
		r.enroll(alice)
		start(t, r, "w1", "~/one")
		r.restart()
		got, err := r.s.Session(ctx, alice, w("w1"), false)
		if err != nil || got.Folder != "~/one" {
			t.Fatalf("after the restart: %+v, %v, want the session in ~/one", got, err)
		}
	})

	t.Run("a session saved before folders reads back with none", func(t *testing.T) {
		r := newCredRig(t)
		r.enroll(alice)
		ses := start(t, r, "w1", "~/one")
		r.stop()
		raw, err := os.ReadFile(r.savedPath(alice))
		var saved map[string]any
		if err == nil {
			err = json.Unmarshal(raw, &saved)
		}
		if err != nil {
			t.Fatal(err)
		}
		list, _ := saved["sessions"].([]any)
		for _, s := range list {
			delete(s.(map[string]any), "folder")
		}
		if raw, err = json.Marshal(saved); err == nil {
			err = os.WriteFile(r.savedPath(alice), raw, 0o600)
		}
		if err != nil {
			t.Fatal(err)
		}
		r.open()
		got, err := r.s.Session(ctx, alice, w("w1"), false)
		if err != nil || got.ID != ses.ID || got.Folder != "" {
			t.Fatalf("the older saved session: %+v, %v, want session %s with no folder", got, err, ses.ID)
		}
	})
}
