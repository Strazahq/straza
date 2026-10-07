package connect

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// httpAPI is the smallest API a test needs: plain JSON over HTTP, with the
// server's error sentence as the error, the way both real clients answer.
type httpAPI struct{ base string }

func (a httpAPI) Do(ctx context.Context, method, path string, body, out any) error {
	var rd io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rd = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, a.base+path, rd)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	buf, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 400 {
		var apiErr struct {
			Error string `json:"error"`
		}
		_ = json.Unmarshal(buf, &apiErr)
		return errors.New(apiErr.Error)
	}
	if out != nil && len(buf) > 0 {
		return json.Unmarshal(buf, out)
	}
	return nil
}

// serverMock scripts GET /v1/connect responses: each poll pops the next
// status list; the last one repeats. gets counts every GET (the `before`
// snapshot plus each poll) so a test can assert a cancelled context makes no
// request at all. The other verbs record what they were sent.
type serverMock struct {
	mu      sync.Mutex
	lists   [][]Status
	gets    int64 // atomic
	removed string
	patched map[string]any
	pasted  map[string]string
	// pageURL is the Credentials tab a current strazad names in the start
	// answer; empty plays an older strazad.
	pageURL string
}

func (m *serverMock) server(t *testing.T, expiresIn int) httpAPI {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/connect", func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt64(&m.gets, 1)
		m.mu.Lock()
		list := m.lists[0]
		if len(m.lists) > 1 {
			m.lists = m.lists[1:]
		}
		m.mu.Unlock()
		_ = json.NewEncoder(w).Encode(list)
	})
	mux.HandleFunc("POST /v1/connect/{app}", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]string
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body["token"] != "" {
			m.mu.Lock()
			m.pasted = body
			m.mu.Unlock()
			fmt.Fprintf(w, `{"app":%q,"user":"alice","fingerprint":"a1c4"}`, r.PathValue("app"))
			return
		}
		fmt.Fprintf(w, `{"app":%q,"provider":"github","authorize_url":"https://gh.example/authorize?state=s","page_url":%q,"expires_in":%d}`,
			r.PathValue("app"), m.pageURL, expiresIn)
	})
	mux.HandleFunc("PATCH /v1/connect/{app}", func(_ http.ResponseWriter, r *http.Request) {
		m.mu.Lock()
		_ = json.NewDecoder(r.Body).Decode(&m.patched)
		m.mu.Unlock()
	})
	mux.HandleFunc("DELETE /v1/connect/{app}", func(_ http.ResponseWriter, r *http.Request) {
		m.mu.Lock()
		m.removed = r.PathValue("app") + "?" + r.URL.RawQuery
		m.mu.Unlock()
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return httpAPI{base: srv.URL}
}

// TestSignInWaitsForTheSignIn drives the fresh sign-in at its real polling
// cadence: not connected on the first poll, connected on the second. A
// current strazad names the Credentials tab, and the provider's link, which
// any browser could finish, is never printed; an older strazad names no page
// and gets its provider link.
func TestSignInWaitsForTheSignIn(t *testing.T) {
	cases := []struct {
		name, pageURL string
		want, never   []string
	}{
		{"a current strazad", "https://straza.example/self-service/credentials",
			[]string{
				"github uses your own sign-in at github, which you finish in a browser.\nOpen https://straza.example/self-service/credentials\nsign in to Straza there, and press Sign in with github on the github row.\n",
				"waiting for the sign-in…", "Connected: github → github",
			},
			[]string{"gh.example", "state="}},
		{"an older strazad", "",
			[]string{"Open https://gh.example/authorize?state=s\nand authorize Straza with github.\n", "waiting for authorization", "Connected: github → github"},
			nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			m := &serverMock{pageURL: c.pageURL, lists: [][]Status{
				{{App: "github", Provider: "github", Connected: false}},                                   // before
				{{App: "github", Provider: "github", Connected: false}},                                   // poll 1
				{{App: "github", Provider: "github", Connected: true, UpdatedAt: "2026-07-14T10:00:00Z"}}, // poll 2
			}}
			api := m.server(t, 600)

			var out strings.Builder
			if err := SignIn(context.Background(), api, "github", "straza", &out); err != nil {
				t.Fatalf("SignIn: %v", err)
			}
			got := out.String()
			for _, want := range c.want {
				if !strings.Contains(got, want) {
					t.Errorf("output missing %q:\n%s", want, got)
				}
			}
			for _, never := range c.never {
				if strings.Contains(got, never) {
					t.Errorf("output carries %q:\n%s", never, got)
				}
			}
		})
	}
}

// TestSignInReconnectNeedsRotation pins the reconnect case: an existing grant
// only counts as re-authorized when updated_at moves, so a stale "connected"
// answer must not end the wait early.
func TestSignInReconnectNeedsRotation(t *testing.T) {
	pre := Status{App: "github", Provider: "github", Connected: true, UpdatedAt: "2026-07-14T09:00:00Z"}
	rotated := pre
	rotated.UpdatedAt = "2026-07-14T11:11:11Z"
	m := &serverMock{lists: [][]Status{
		{pre},     // before
		{pre},     // poll 0 (immediate): user has not finished, same grant
		{pre},     // poll 1: still the same grant
		{rotated}, // poll 2: callback landed
	}}
	api := m.server(t, 600)

	var out strings.Builder
	start := time.Now()
	if err := SignIn(context.Background(), api, "github", "straza", &out); err != nil {
		t.Fatalf("SignIn: %v", err)
	}
	// Poll-first fires poll 0 immediately, so a genuine reconnect needs two
	// bottom waits (2 s each) before the rotation shows.
	if time.Since(start) < 3*time.Second {
		t.Error("sign-in returned before the rotation landed (stale grant accepted as re-authorization)")
	}
}

// TestSignInReturnsOnFirstPoll: when the callback has already landed, the
// immediate first poll ends the wait with no sleep before it.
func TestSignInReturnsOnFirstPoll(t *testing.T) {
	m := &serverMock{lists: [][]Status{
		{{App: "github", Provider: "github", Connected: false}},                                   // before
		{{App: "github", Provider: "github", Connected: true, UpdatedAt: "2026-07-14T10:00:00Z"}}, // poll 0
	}}
	api := m.server(t, 600)

	var out strings.Builder
	start := time.Now()
	if err := SignIn(context.Background(), api, "github", "straza", &out); err != nil {
		t.Fatalf("SignIn: %v", err)
	}
	if d := time.Since(start); d >= 1*time.Second {
		t.Fatalf("SignIn took %v; it slept before the first poll (2 s interval)", d)
	}
	if !strings.Contains(out.String(), "Connected: github → github") {
		t.Fatalf("sign-in did not complete: %q", out.String())
	}
}

// TestSignInPreCancelledContextMakesNoRequest pins the fail-closed guard: an
// already-cancelled context returns context.Canceled and hits no endpoint.
func TestSignInPreCancelledContextMakesNoRequest(t *testing.T) {
	m := &serverMock{lists: [][]Status{{{App: "github", Provider: "github", Connected: false}}}}
	api := m.server(t, 600)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var out strings.Builder
	err := SignIn(ctx, api, "github", "straza", &out)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if got := atomic.LoadInt64(&m.gets); got != 0 {
		t.Fatalf("GET /v1/connect hits = %d on a cancelled context, want 0", got)
	}
}

// TestSignInStopsWaiting: a caller with no browser, a headless agent above
// all, gets an error when the wait runs out, and the hint names the tool it
// ran. Against an older strazad the error is the expired link's.
func TestSignInStopsWaiting(t *testing.T) {
	for _, tool := range []string{"straza", "strazactl"} {
		cases := []struct{ name, pageURL, want string }{
			{"a current strazad", "https://straza.example/self-service/credentials",
				"connect stopped waiting after 1s and nothing changed. The sign-in still works on the page any time. Run `" + tool + " connect github` again to wait for it"},
			{"an older strazad", "", "connect timed out: the authorization link expired. Run `" + tool + " connect github` again"},
		}
		for _, c := range cases {
			t.Run(tool+" against "+c.name, func(t *testing.T) {
				m := &serverMock{pageURL: c.pageURL, lists: [][]Status{{{App: "github", Provider: "github", Connected: false}}}}
				api := m.server(t, 1) // the wait runs out after 1 s

				var out strings.Builder
				err := SignIn(context.Background(), api, "github", tool, &out)
				if err == nil || err.Error() != c.want {
					t.Fatalf("err = %v, want %q", err, c.want)
				}
			})
		}
	}
}

// TestWaitWords pins how the give-up line names the wait.
func TestWaitWords(t *testing.T) {
	cases := []struct {
		d    time.Duration
		want string
	}{
		{10 * time.Minute, "10 minutes"},
		{time.Minute, "1 minute"},
		{90 * time.Second, "1m30s"},
		{time.Second, "1s"},
	}
	for _, c := range cases {
		if got := waitWords(c.d); got != c.want {
			t.Errorf("waitWords(%s) = %q, want %q", c.d, got, c.want)
		}
	}
}
