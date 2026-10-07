package ctl

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// TestDraftCallsWireShapes pins every drafts call to its wire: the method,
// the escaped path and query, and the exact JSON body. Each call hands the
// answer back as the server sent it.
func TestDraftCallsWireShapes(t *testing.T) {
	rec := &wireRecorder{}
	srv := httptest.NewServer(http.HandlerFunc(rec.handle))
	t.Cleanup(srv.Close)
	c := loggedInClient(t, srv.URL)
	ctx := context.Background()
	empty := ""

	tests := []struct {
		name   string
		call   func() ([]byte, int, error)
		method string
		uri    string
		body   string
	}{
		{
			name: "check sends the documents and the note",
			call: func() ([]byte, int, error) {
				return c.CheckDraft(ctx, DraftBody{Documents: []string{"a", "b"}, Note: "why"})
			},
			method: http.MethodPost, uri: "/v1/admin/drafts/check",
			body: `{"documents":["a","b"],"note":"why"}`,
		},
		{
			name:   "create leaves an empty note out",
			call:   func() ([]byte, int, error) { return c.CreateDraft(ctx, DraftBody{Documents: []string{"a"}}) },
			method: http.MethodPost, uri: "/v1/admin/drafts",
			body: `{"documents":["a"]}`,
		},
		{
			name:   "list asks for the largest page of one state",
			call:   func() ([]byte, int, error) { return c.ListDrafts(ctx, "open", false) },
			method: http.MethodGet, uri: "/v1/admin/drafts?limit=200&state=open",
		},
		{
			name:   "list of mine",
			call:   func() ([]byte, int, error) { return c.ListDrafts(ctx, "all", true) },
			method: http.MethodGet, uri: "/v1/admin/drafts?limit=200&mine=true&state=all",
		},
		{
			name:   "get escapes the id",
			call:   func() ([]byte, int, error) { return c.GetDraft(ctx, "a/b") },
			method: http.MethodGet, uri: "/v1/admin/drafts/a%2Fb",
		},
		{
			name: "update sends a given note, an empty one included",
			call: func() ([]byte, int, error) {
				return c.UpdateDraft(ctx, "41", DraftUpdate{Revision: 2, Documents: []string{"a"}, Note: &empty})
			},
			method: http.MethodPut, uri: "/v1/admin/drafts/41",
			body: `{"revision":2,"documents":["a"],"note":""}`,
		},
		{
			name: "update without a note keeps the old one",
			call: func() ([]byte, int, error) {
				return c.UpdateDraft(ctx, "41", DraftUpdate{Revision: 2, Documents: []string{"a"}})
			},
			method: http.MethodPut, uri: "/v1/admin/drafts/41",
			body: `{"revision":2,"documents":["a"]}`,
		},
		{
			name: "publish sends ticked and typed even when empty",
			call: func() ([]byte, int, error) {
				return c.PublishDraft(ctx, "41", DraftPublish{Revision: 2, RiskDigest: "d1"})
			},
			method: http.MethodPost, uri: "/v1/admin/drafts/41/publish",
			body: `{"revision":2,"risk_digest":"d1","ticked":[],"typed":{}}`,
		},
		{
			name: "publish sends the keys and the typed texts",
			call: func() ([]byte, int, error) {
				return c.PublishDraft(ctx, "41", DraftPublish{Revision: 2, RiskDigest: "d1",
					Ticked: []string{"k1", "k2"}, Typed: map[string]string{"k2": "api.example.com"}})
			},
			method: http.MethodPost, uri: "/v1/admin/drafts/41/publish",
			body: `{"revision":2,"risk_digest":"d1","ticked":["k1","k2"],"typed":{"k2":"api.example.com"}}`,
		},
		{
			name:   "discard without a reason sends an empty object",
			call:   func() ([]byte, int, error) { return c.DiscardDraft(ctx, "41", "") },
			method: http.MethodPost, uri: "/v1/admin/drafts/41/discard",
			body: `{}`,
		},
		{
			name:   "discard sends the reason",
			call:   func() ([]byte, int, error) { return c.DiscardDraft(ctx, "41", "superseded") },
			method: http.MethodPost, uri: "/v1/admin/drafts/41/discard",
			body: `{"reason":"superseded"}`,
		},
		{
			name:   "revert sends the note",
			call:   func() ([]byte, int, error) { return c.RevertDraft(ctx, "41", "undo the rollout") },
			method: http.MethodPost, uri: "/v1/admin/drafts/41/revert",
			body: `{"note":"undo the rollout"}`,
		},
		{
			name:   "contact names the server as an App object",
			call:   func() ([]byte, int, error) { return c.ContactDraftServer(ctx, "41", "github") },
			method: http.MethodPost, uri: "/v1/admin/drafts/41/contact",
			body: `{"object":"App/github"}`,
		},
		{
			name:   "rebase without picks sends the revision alone",
			call:   func() ([]byte, int, error) { return c.RebaseDraft(ctx, "41", DraftRebase{Revision: 2}) },
			method: http.MethodPost, uri: "/v1/admin/drafts/41/rebase",
			body: `{"revision":2}`,
		},
		{
			name: "rebase sends the picks",
			call: func() ([]byte, int, error) {
				return c.RebaseDraft(ctx, "41", DraftRebase{Revision: 2, Picks: map[string]string{"App/demo straza.limits.rps": "live"}})
			},
			method: http.MethodPost, uri: "/v1/admin/drafts/41/rebase",
			body: `{"revision":2,"picks":{"App/demo straza.limits.rps":"live"}}`,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			const answer = `{"draft":{"id":"41"}}`
			rec.script(answer)
			raw, code, err := tc.call()
			if err != nil {
				t.Fatalf("call: %v", err)
			}
			if string(raw) != answer || code != http.StatusOK {
				t.Errorf("answer = %d %s, want 200 %s", code, raw, answer)
			}
			method, uri, ct, body := rec.last()
			if method != tc.method || uri != tc.uri {
				t.Errorf("request = %s %s, want %s %s", method, uri, tc.method, tc.uri)
			}
			if string(body) != tc.body {
				t.Errorf("body = %s, want %s", body, tc.body)
			}
			if tc.body != "" && ct != "application/json" {
				t.Errorf("content type = %q, want application/json", ct)
			}
		})
	}
}

// TestDraftCallsKeepTheRefusalBody pins that a refusal comes back whole: the
// status and the body of a 409 or 422, with no error, so a verb can print
// the verdict or the findings the body carries.
func TestDraftCallsKeepTheRefusalBody(t *testing.T) {
	for _, code := range []int{http.StatusConflict, http.StatusUnprocessableEntity} {
		t.Run(http.StatusText(code), func(t *testing.T) {
			const body = `{"error":"Draft 41 cannot be published: a sentence.","verdict":{"refused":[]}}`
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(code)
				_, _ = w.Write([]byte(body))
			}))
			t.Cleanup(srv.Close)
			raw, got, err := loggedInClient(t, srv.URL).CreateDraft(context.Background(), DraftBody{Documents: []string{"a"}})
			if err != nil || got != code || string(raw) != body {
				t.Errorf("answer = %d %s %v, want %d %s and no error", got, raw, err, code, body)
			}
		})
	}
}

// TestPublishDraftWaits pins publish's own timeout: a publish may take longer
// than the client's timeout, because it waits for the servers it starts, and
// a publish that outlasts its own timeout says it may have landed.
func TestPublishDraftWaits(t *testing.T) {
	tests := []struct {
		name          string
		client, delay time.Duration
		publish       time.Duration
		wantErr       string
	}{
		{name: "a publish slower than the client's timeout is answered", client: 50 * time.Millisecond, delay: 300 * time.Millisecond, publish: 5 * time.Second},
		{
			name: "a publish past its own timeout may have landed", client: 5 * time.Second, delay: 2 * time.Second, publish: 100 * time.Millisecond,
			wantErr: "strazad did not answer the publish of draft 41 within 2 minutes, so it may have landed. " +
				"Read it with strazactl drafts show 41 before you publish again",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				// A read body lets the server see the client leave, which
				// ends the wait below.
				_, _ = io.Copy(io.Discard, r.Body)
				select {
				case <-time.After(tc.delay):
				case <-r.Context().Done():
					return
				}
				_, _ = w.Write([]byte(`{"snapshot":"s1"}`))
			}))
			t.Cleanup(srv.Close)
			was := publishTimeout
			publishTimeout = tc.publish
			t.Cleanup(func() { publishTimeout = was })
			c := loggedInClient(t, srv.URL)
			c.HTTP.Timeout = tc.client
			_, code, err := c.PublishDraft(context.Background(), "41", DraftPublish{Revision: 1, RiskDigest: "d"})
			switch {
			case tc.wantErr == "" && (err != nil || code != http.StatusOK):
				t.Errorf("publish = %d %v, want 200", code, err)
			case tc.wantErr != "" && (err == nil || err.Error() != tc.wantErr):
				t.Errorf("err = %v, want %q", err, tc.wantErr)
			}
			if c.HTTP.Timeout != tc.client {
				t.Errorf("the client's timeout moved to %v", c.HTTP.Timeout)
			}
		})
	}
}

// TestDraftCallsGoThroughTheGuard pins that the drafts calls share the one
// authenticated path: inside a coding agent, on a login whose session needs
// renewing, create is refused before anything is sent, the renewal
// included, and check goes out on the login.
func TestDraftCallsGoThroughTheGuard(t *testing.T) {
	s := &bearerServer{}
	c := loggedInClient(t, s.start(t).URL)
	if err := c.saveCreds(credentials{Server: c.Base, SessionToken: "expiring", SessionID: "ses-1"}); err != nil {
		t.Fatal(err)
	}
	c.AgentMarker = "CLAUDECODE"
	if _, _, err := c.CreateDraft(context.Background(), DraftBody{Documents: []string{"a"}}); err == nil ||
		!strings.Contains(err.Error(), "CLAUDECODE is set, so strazactl runs inside a coding agent") {
		t.Fatalf("create inside an agent: %v, want the guard's refusal", err)
	}
	if _, _, err := c.ContactDraftServer(context.Background(), "41", "github"); err == nil || !strings.Contains(err.Error(), "CLAUDECODE is set") {
		t.Fatalf("contact inside an agent: %v, want the guard's refusal", err)
	}
	if _, _, err := c.RebaseDraft(context.Background(), "41", DraftRebase{Revision: 1}); err == nil || !strings.Contains(err.Error(), "CLAUDECODE is set") {
		t.Fatalf("rebase inside an agent: %v, want the guard's refusal", err)
	}
	if got := s.requests(); len(got) != 0 {
		t.Fatalf("the refused calls sent %q", got)
	}
	if _, _, err := c.CheckDraft(context.Background(), DraftBody{Documents: []string{"a"}}); err != nil {
		t.Fatalf("check inside an agent: %v", err)
	}
	if got := s.requests(); len(got) != 2 || got[0] != "POST /v1/checkin " || !strings.HasPrefix(got[1], "POST /v1/admin/drafts/check ") {
		t.Errorf("requests = %q, want the renewal and the one check", got)
	}
}

// TestPublishDraftLostAnswer pins the words of a publish whose answer does
// not say what happened: after the request went out it may have landed,
// whether the connection dropped or a proxy answered a 502, 503 or 504
// with no sentence of strazad's. A dial that failed sent nothing and keeps
// the transport's own error, and strazad's own sentence comes back as it
// is.
func TestPublishDraftLostAnswer(t *testing.T) {
	const lost = "so it may have landed. Read it with strazactl drafts show 41 before you publish again"
	tests := []struct {
		name     string
		handler  http.HandlerFunc
		gone     bool
		wantErr  string
		wantCode int
	}{
		{name: "a dropped connection may have landed", handler: dropConnection,
			wantErr: "strazad's answer to the publish of draft 41 was lost (EOF), " + lost},
		{name: "a proxy's 502 may have landed", handler: answer(http.StatusBadGateway, "<html>502 Bad Gateway</html>"),
			wantErr: "strazad's answer to the publish of draft 41 was lost (a proxy answered HTTP 502), " + lost},
		{name: "a proxy's 503 may have landed", handler: answer(http.StatusServiceUnavailable, "<html>503</html>"),
			wantErr: "strazad's answer to the publish of draft 41 was lost (a proxy answered HTTP 503), " + lost},
		{name: "a proxy's 504 may have landed", handler: answer(http.StatusGatewayTimeout, "<html>504 Gateway Time-out</html>"),
			wantErr: "strazad's answer to the publish of draft 41 was lost (a proxy answered HTTP 504), " + lost},
		{name: "strazad's own 503 comes back as it is", wantCode: http.StatusServiceUnavailable,
			handler: answer(http.StatusServiceUnavailable, `{"error":"Straza could not publish draft 41, because the database turned the change away three times while other changes were written. Nothing was published. Publish again in a moment."}`)},
		{name: "a 500 with no sentence comes back as it is", handler: answer(http.StatusInternalServerError, "oops"), wantCode: http.StatusInternalServerError},
		{name: "a failed dial sent nothing", gone: true, wantErr: "strazad unreachable at "},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(tc.handler)
			if tc.gone {
				srv.Close()
			} else {
				t.Cleanup(srv.Close)
			}
			_, code, err := loggedInClient(t, srv.URL).PublishDraft(context.Background(), "41", DraftPublish{Revision: 1, RiskDigest: "d"})
			switch {
			case tc.wantErr == "" && (err != nil || code != tc.wantCode):
				t.Errorf("answer = %d %v, want %d and no error", code, err, tc.wantCode)
			case tc.wantErr != "" && (err == nil || !strings.HasPrefix(err.Error(), tc.wantErr)):
				t.Errorf("err = %v, want it to start with %q", err, tc.wantErr)
			}
		})
	}
}

// TestNewDraftUnknownOutcome pins the words of a create or a revert whose
// answer does not say whether its new draft was stored: a lost answer, a
// 5xx with or without a sentence, and a wait past the client's timeout each
// add the way to find out, since a second run would store a second draft.
// A refusal and a failed dial come back as they are.
func TestNewDraftUnknownOutcome(t *testing.T) {
	const (
		create = "List your drafts with strazactl drafts list --mine before you send it again"
		revert = "List your drafts with strazactl drafts list --mine before you revert again"
	)
	createDraft := func(c *Client) ([]byte, int, error) {
		return c.CreateDraft(context.Background(), DraftBody{Documents: []string{"a"}})
	}
	revertDraft := func(c *Client) ([]byte, int, error) { return c.RevertDraft(context.Background(), "41", "") }
	tests := []struct {
		name     string
		call     func(c *Client) ([]byte, int, error)
		handler  http.HandlerFunc
		gone     bool
		wantErr  string
		wantCode int
	}{
		{name: "create: a dropped connection", call: createDraft, handler: dropConnection,
			wantErr: "strazad's answer to drafts create was lost (EOF), so the draft may have been stored. " + create},
		{name: "create: a proxy's 502", call: createDraft, handler: answer(http.StatusBadGateway, "<html>502 Bad Gateway</html>"),
			wantErr: "strazad or a proxy in front of it answered HTTP 502 without a sentence, so the draft may have been stored. " + create},
		{name: "create: strazad's own 503", call: createDraft, handler: answer(http.StatusServiceUnavailable, `{"error":"Straza could not read live state. Try again in a moment."}`),
			wantErr: "Straza could not read live state. Try again in a moment. The draft may have been stored. " + create},
		{name: "create: no answer in time", call: createDraft, handler: slowAnswer,
			wantErr: "strazad did not answer drafts create in time, so the draft may have been stored. " + create},
		{name: "create: a refusal at intake comes back as it is", call: createDraft,
			handler: answer(http.StatusUnprocessableEntity, `{"error":"The draft holds no document."}`), wantCode: http.StatusUnprocessableEntity},
		{name: "create: a failed dial sent nothing", call: createDraft, gone: true, wantErr: "strazad unreachable at "},
		{name: "revert: a dropped connection", call: revertDraft, handler: dropConnection,
			wantErr: "strazad's answer to drafts revert was lost (EOF), so the undo draft may have been stored. " + revert},
		{name: "revert: a proxy's 504", call: revertDraft, handler: answer(http.StatusGatewayTimeout, "<html>504 Gateway Time-out</html>"),
			wantErr: "strazad or a proxy in front of it answered HTTP 504 without a sentence, so the undo draft may have been stored. " + revert},
		{name: "revert: strazad's own 503", call: revertDraft, handler: answer(http.StatusServiceUnavailable, `{"error":"Straza could not read live state. Try again in a moment."}`),
			wantErr: "Straza could not read live state. Try again in a moment. The undo draft may have been stored. " + revert},
		{name: "revert: no answer in time", call: revertDraft, handler: slowAnswer,
			wantErr: "strazad did not answer drafts revert in time, so the undo draft may have been stored. " + revert},
		{name: "revert: a draft that changed nothing comes back as it is", call: revertDraft,
			handler: answer(http.StatusConflict, `{"error":"Draft 41 changed nothing, so there is nothing to undo."}`), wantCode: http.StatusConflict},
		{name: "revert: a failed dial sent nothing", call: revertDraft, gone: true, wantErr: "strazad unreachable at "},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(tc.handler)
			if tc.gone {
				srv.Close()
			} else {
				t.Cleanup(srv.Close)
			}
			c := loggedInClient(t, srv.URL)
			c.HTTP.Timeout = 200 * time.Millisecond
			_, code, err := tc.call(c)
			switch {
			case tc.wantErr == "" && (err != nil || code != tc.wantCode):
				t.Errorf("answer = %d %v, want %d and no error", code, err, tc.wantCode)
			case tc.wantErr != "" && (err == nil || !strings.HasPrefix(err.Error(), tc.wantErr)):
				t.Errorf("err = %v, want it to start with %q", err, tc.wantErr)
			}
		})
	}
}

// TestDraftCallsNameAMissingRoute pins the words for a drafts call that
// strazad answers with 404 or 405 and no sentence of its own, which its
// router does for a route it does not serve: the error names the route,
// without its query. A 404 that carries strazad's sentence comes back as it
// is.
func TestDraftCallsNameAMissingRoute(t *testing.T) {
	ctx := context.Background()
	calls := []struct {
		route string
		call  func(c *Client) ([]byte, int, error)
	}{
		{"POST /v1/admin/drafts/check", func(c *Client) ([]byte, int, error) { return c.CheckDraft(ctx, DraftBody{Documents: []string{"a"}}) }},
		{"POST /v1/admin/drafts", func(c *Client) ([]byte, int, error) { return c.CreateDraft(ctx, DraftBody{Documents: []string{"a"}}) }},
		{"GET /v1/admin/drafts", func(c *Client) ([]byte, int, error) { return c.ListDrafts(ctx, "open", false) }},
		{"GET /v1/admin/drafts/41", func(c *Client) ([]byte, int, error) { return c.GetDraft(ctx, "41") }},
		{"PUT /v1/admin/drafts/41", func(c *Client) ([]byte, int, error) { return c.UpdateDraft(ctx, "41", DraftUpdate{Revision: 2}) }},
		{"POST /v1/admin/drafts/41/discard", func(c *Client) ([]byte, int, error) { return c.DiscardDraft(ctx, "41", "") }},
		{"POST /v1/admin/drafts/41/revert", func(c *Client) ([]byte, int, error) { return c.RevertDraft(ctx, "41", "") }},
		{"POST /v1/admin/drafts/41/publish", func(c *Client) ([]byte, int, error) {
			return c.PublishDraft(ctx, "41", DraftPublish{Revision: 2, RiskDigest: "d"})
		}},
	}
	for _, code := range []int{http.StatusNotFound, http.StatusMethodNotAllowed} {
		for _, tc := range calls {
			t.Run(fmt.Sprintf("%d %s", code, tc.route), func(t *testing.T) {
				srv := httptest.NewServer(answer(code, http.StatusText(code)+"\n"))
				t.Cleanup(srv.Close)
				want := fmt.Sprintf("strazad answered HTTP %d with no sentence of its own, which it does when it does not serve %s. "+
					"Upgrade strazad, or check that --server or your login points at strazad", code, tc.route)
				if _, _, err := tc.call(loggedInClient(t, srv.URL)); err == nil || err.Error() != want {
					t.Errorf("err = %v, want %q", err, want)
				}
			})
		}
	}
	t.Run("a 404 with strazad's sentence", func(t *testing.T) {
		const body = `{"error":"There is no draft 41. List the drafts with strazactl drafts list, or open Drafts on the console."}`
		srv := httptest.NewServer(answer(http.StatusNotFound, body))
		t.Cleanup(srv.Close)
		raw, code, err := loggedInClient(t, srv.URL).GetDraft(ctx, "41")
		if err != nil || code != http.StatusNotFound || string(raw) != body {
			t.Errorf("answer = %d %s %v, want 404 with the body and no error", code, raw, err)
		}
	})
}

// TestCheckPublishRoute pins the check a publish makes before any question:
// one GET of the publish path, which strazad's router answers with 405 when
// it serves the publish route, a POST, and with 404 when it does not, so
// the check can never publish.
func TestCheckPublishRoute(t *testing.T) {
	tests := []struct {
		name    string
		served  bool
		gone    bool
		wantErr string
	}{
		{name: "a strazad that serves the publish route", served: true},
		{name: "a strazad that does not", wantErr: "this strazad serves no publish route yet, so the draft stays open. " +
			"Upgrade strazad to a version with drafts publish, and publish again"},
		{name: "a strazad that cannot be reached", gone: true, wantErr: "strazad unreachable at "},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var mu sync.Mutex
			var seen []string
			mux := http.NewServeMux()
			mux.HandleFunc("GET /v1/admin/drafts/{id}", answer(http.StatusOK, `{}`))
			if tc.served {
				mux.HandleFunc("POST /v1/admin/drafts/{id}/publish", func(http.ResponseWriter, *http.Request) { t.Error("the check published") })
			}
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				mu.Lock()
				seen = append(seen, r.Method+" "+r.URL.RequestURI())
				mu.Unlock()
				mux.ServeHTTP(w, r)
			}))
			if tc.gone {
				srv.Close()
			} else {
				t.Cleanup(srv.Close)
			}
			err := loggedInClient(t, srv.URL).CheckPublishRoute(context.Background(), "41")
			if tc.wantErr == "" && err != nil || tc.wantErr != "" && (err == nil || !strings.HasPrefix(err.Error(), tc.wantErr)) {
				t.Errorf("err = %v, want %q", err, tc.wantErr)
			}
			mu.Lock()
			defer mu.Unlock()
			if !tc.gone && (len(seen) != 1 || seen[0] != "GET /v1/admin/drafts/41/publish") {
				t.Errorf("requests = %q, want one GET of the publish path", seen)
			}
		})
	}
}

// dropConnection closes the connection without an answer, after the
// request went out.
func dropConnection(w http.ResponseWriter, _ *http.Request) {
	if conn, _, err := w.(http.Hijacker).Hijack(); err == nil {
		_ = conn.Close()
	}
}

// answer answers every request with status and body.
func answer(status int, body string) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}
}

// slowAnswer answers after the client gave up, reading the body so the
// server sees the client leave.
func slowAnswer(w http.ResponseWriter, r *http.Request) {
	_, _ = io.Copy(io.Discard, r.Body)
	select {
	case <-time.After(2 * time.Second):
		_, _ = w.Write([]byte(`{}`))
	case <-r.Context().Done():
	}
}
