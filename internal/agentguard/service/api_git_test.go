package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/strazahq/straza/internal/agentguard/localapi"
)

// gitDouble scripts the git door's calls on apiDouble. A nil func fails
// the call, so a test that expects no call sees one.
type gitDouble struct {
	resolve func(s Session, upstream string) (bool, error)
	grant   func(ctx context.Context, s Session, opID string, req localapi.GitGrantRequest, wait func(string)) error
	send    func(s Session, opID string, route gitRoute, in *http.Request) (*http.Response, error)
}

var errUnscripted = errors.New("the test did not expect this call")

func (f *apiDouble) GitResolve(_ context.Context, s Session, upstream string) (bool, error) {
	if f.git.resolve == nil {
		return false, errUnscripted
	}
	return f.git.resolve(s, upstream)
}

func (f *apiDouble) GitGrant(ctx context.Context, s Session, opID, _ string, req localapi.GitGrantRequest, wait func(string)) error {
	if f.git.grant == nil {
		return errUnscripted
	}
	return f.git.grant(ctx, s, opID, req, wait)
}

func (f *apiDouble) GitSend(s Session, opID string, route gitRoute, in *http.Request) (*http.Response, error) {
	if f.git.send == nil {
		return nil, errUnscripted
	}
	return f.git.send(s, opID, route, in)
}

// doorReq is a well-formed git door request of window w1 that a row then
// changes.
func doorReq(method, path, body string) *http.Request {
	r := httptest.NewRequest(method, localapi.BaseURL+path, strings.NewReader(body))
	r.Header.Set(localapi.HeaderHarness, "claude-code")
	r.Header.Set(localapi.HeaderHarnessSession, "w1")
	r.Header.Set(localapi.HeaderGitURL, gitRepoURL)
	r.Header.Set(localapi.HeaderGitOp, strings.Repeat("ab", 16))
	return r
}

// TestGitDoorRefusesBadRequests pins the door's own refusals, each before
// any call to the credential half, and that a URL carrying a token never
// reaches the log.
func TestGitDoorRefusesBadRequests(t *testing.T) {
	ref := func(name, old, new string) string {
		raw, _ := json.Marshal(localapi.GitRef{Ref: name, Old: old, New: new})
		return string(raw)
	}
	zero, one := strings.Repeat("0", 40), strings.Repeat("1", 40)
	many := strings.Repeat(ref("refs/heads/a", zero, one)+",", 1000) + ref("refs/heads/b", zero, one)
	rows := []struct {
		name, method, path, body string
		edit                     func(r *http.Request)
		code                     int
		want                     string
	}{
		{"unknown route", http.MethodPost, localapi.RouteGit + "upload-archive", "", nil, http.StatusNotFound, "has no route"},
		{"wrong method", http.MethodGet, localapi.RouteGitResolve, "", nil, http.StatusMethodNotAllowed, "takes POST"},
		{"encoded path", http.MethodGet, localapi.RouteGit + "info%2Frefs", "", nil, http.StatusBadRequest, "percent-encoded"},
		{"no harness", http.MethodPost, localapi.RouteGitResolve, "", func(r *http.Request) { r.Header.Del(localapi.HeaderHarness) },
			http.StatusBadRequest, "names no harness"},
		{"no run id", http.MethodPost, localapi.RouteGitResolve, "", func(r *http.Request) { r.Header.Del(localapi.HeaderGitOp) },
			http.StatusBadRequest, badGitOp},
		{"upper-case run id", http.MethodPost, localapi.RouteGitResolve, "", func(r *http.Request) {
			r.Header.Set(localapi.HeaderGitOp, strings.Repeat("AB", 16))
		}, http.StatusBadRequest, badGitOp},
		{"token in the URL", http.MethodPost, localapi.RouteGitResolve, "", func(r *http.Request) {
			r.Header.Set(localapi.HeaderGitURL, "https://ghp_secret@github.com/acme/pay-api.git")
		}, http.StatusBadRequest, badGitURL},
		{"query in the URL", http.MethodPost, localapi.RouteGitResolve, "", func(r *http.Request) {
			r.Header.Set(localapi.HeaderGitURL, gitRepoURL+"?token=ghp_secret")
		}, http.StatusBadRequest, badGitURL},
		{"ssh URL", http.MethodPost, localapi.RouteGitResolve, "", func(r *http.Request) {
			r.Header.Set(localapi.HeaderGitURL, "git@github.com:acme/pay-api.git")
		}, http.StatusBadRequest, badGitURL},
		{"archive advertisement", http.MethodGet, localapi.RouteGit + "info/refs?service=git-upload-archive", "", nil,
			http.StatusBadRequest, "takes only service=git-upload-pack or service=git-receive-pack"},
		{"second query parameter", http.MethodGet, localapi.RouteGit + "info/refs?service=git-upload-pack&x=1", "", nil,
			http.StatusBadRequest, "takes only service=git-upload-pack"},
		{"unknown op", http.MethodPost, localapi.RouteGitGrant, `{"op":"clone"}`, nil, http.StatusBadRequest, `operation "clone"`},
		{"refs on a fetch", http.MethodPost, localapi.RouteGitGrant, `{"op":"fetch","refs":[` + ref("refs/heads/a", zero, one) + `]}`, nil,
			http.StatusBadRequest, refsOnFetch},
		{"too many refs", http.MethodPost, localapi.RouteGitGrant, `{"op":"push","refs":[` + many + `]}`, nil,
			http.StatusBadRequest, "names 1001 ref updates"},
		{"upper-case object id", http.MethodPost, localapi.RouteGitGrant, `{"op":"push","refs":[` + ref("refs/heads/a", zero, strings.Repeat("A", 40)) + `]}`, nil,
			http.StatusBadRequest, "not a full ref name"},
		{"mixed hash lengths", http.MethodPost, localapi.RouteGitGrant, `{"op":"push","refs":[` + ref("refs/heads/a", zero, strings.Repeat("1", 64)) + `]}`, nil,
			http.StatusBadRequest, "not a full ref name"},
		{"short ref name", http.MethodPost, localapi.RouteGitGrant, `{"op":"push","refs":[` + ref("main", zero, one) + `]}`, nil,
			http.StatusBadRequest, "not a full ref name"},
		{"escape in a ref name", http.MethodPost, localapi.RouteGitGrant, `{"op":"push","refs":[` + ref("refs/heads/\u001b[31m", zero, one) + `]}`, nil,
			http.StatusBadRequest, "not a full ref name"},
		{"body too large", http.MethodPost, localapi.RouteGitGrant, `{"op":"push","refs":[` + strings.Repeat(" ", gitGrantBodyMax) + `]}`, nil,
			http.StatusRequestEntityTooLarge, "larger than the 1 MiB"},
	}
	for _, row := range rows {
		t.Run(row.name, func(t *testing.T) {
			f := &apiDouble{session: liveSession()}
			a, logs := testAPI(f, false)
			req := doorReq(row.method, row.path, row.body)
			if row.edit != nil {
				row.edit(req)
			}
			w := httptest.NewRecorder()
			a.routes().ServeHTTP(w, asCaller(req, "1000"))
			if w.Code != row.code || !strings.Contains(refusalOf(t, w), row.want) {
				t.Fatalf("answer = %d %q, want %d holding %q", w.Code, w.Body.String(), row.code, row.want)
			}
			if len(f.sessionCalls) != 0 {
				t.Errorf("the door looked up a session for a refused request: %+v", f.sessionCalls)
			}
			if strings.Contains(logs.String(), "ghp_secret") {
				t.Errorf("the token in the URL reached the log: %s", logs.String())
			}
		})
	}
}

// TestGitDoorWindow pins how the door finds the session: a named window
// only among the caller's live sessions, never starting one, and an
// unnamed window as the MCP proxy does, starting one when none is live.
func TestGitDoorWindow(t *testing.T) {
	noSession := &Refused{Status: http.StatusConflict, Sentence: noSessionSentence}
	rows := []struct {
		name    string
		window  string
		liveErr error
		code    int
		calls   []bool
	}{
		{"named and live", "w1", nil, http.StatusOK, []bool{false}},
		{"named and gone", "w1", noSession, http.StatusConflict, []bool{false}},
		{"unnamed and live", "", nil, http.StatusOK, []bool{false}},
		{"unnamed and none live", "", noSession, http.StatusOK, []bool{false, true}},
	}
	for _, row := range rows {
		t.Run(row.name, func(t *testing.T) {
			f := &apiDouble{session: liveSession(), liveErr: row.liveErr}
			f.git.resolve = func(Session, string) (bool, error) { return true, nil }
			a, _ := testAPI(f, false)
			req := doorReq(http.MethodPost, localapi.RouteGitResolve, "")
			req.Header.Set(localapi.HeaderHarnessSession, row.window)
			w := httptest.NewRecorder()
			a.routes().ServeHTTP(w, asCaller(req, "1000"))
			if w.Code != row.code {
				t.Fatalf("answer = %d %q, want %d", w.Code, w.Body.String(), row.code)
			}
			var starts []bool
			for _, c := range f.sessionCalls {
				starts = append(starts, c.start)
				if c.harness.Session != row.window || c.caller.UID != "1000" {
					t.Errorf("Session call %+v, want the caller's window %q", c, row.window)
				}
			}
			if fmt.Sprint(starts) != fmt.Sprint(row.calls) {
				t.Errorf("Session calls with start %v, want %v", starts, row.calls)
			}
		})
	}
}

// TestGitDoorRelay pins the relay: each smart HTTP path maps to its route
// and op, the run id and session reach the credential half, and the
// answer comes back with its status and body as they came but only git's
// own headers.
func TestGitDoorRelay(t *testing.T) {
	rows := []struct {
		method, path string
		want         gitRoute
	}{
		{http.MethodGet, localapi.RouteGit + "info/refs?service=git-upload-pack", gitRoute{"info/refs", "git-upload-pack", gitFetch}},
		{http.MethodGet, localapi.RouteGit + "info/refs?service=git-receive-pack", gitRoute{"info/refs", "git-receive-pack", gitPush}},
		{http.MethodPost, localapi.RouteGit + "git-upload-pack", gitRoute{tail: "git-upload-pack", op: gitFetch}},
		{http.MethodPost, localapi.RouteGit + "git-receive-pack", gitRoute{tail: "git-receive-pack", op: gitPush}},
	}
	for _, row := range rows {
		t.Run(row.method+" "+row.path, func(t *testing.T) {
			f := &apiDouble{session: liveSession()}
			var got gitRoute
			var gotOp, gotBody string
			f.git.send = func(_ Session, opID string, route gitRoute, in *http.Request) (*http.Response, error) {
				got, gotOp = route, opID
				raw, _ := io.ReadAll(in.Body)
				gotBody = string(raw)
				h := http.Header{}
				h.Set("Content-Type", "text/plain")
				h.Set("WWW-Authenticate", `DPoP error="invalid_token"`)
				h.Set("Set-Cookie", "ghapp=1")
				return &http.Response{StatusCode: http.StatusForbidden, Header: h, Body: io.NopCloser(strings.NewReader("ghapp says no\n"))}, nil
			}
			a, _ := testAPI(f, false)
			w := httptest.NewRecorder()
			a.routes().ServeHTTP(w, asCaller(doorReq(row.method, row.path, "0000"), "1000"))
			if got != row.want || gotOp != strings.Repeat("ab", 16) {
				t.Errorf("route %+v run %q, want %+v", got, gotOp, row.want)
			}
			if row.method == http.MethodPost && gotBody != "0000" {
				t.Errorf("body %q reached the credential half, want 0000", gotBody)
			}
			if w.Code != http.StatusForbidden || w.Body.String() != "ghapp says no\n" {
				t.Errorf("answer = %d %q, want ghapp's own", w.Code, w.Body.String())
			}
			if len(w.Header()) != 1 || w.Header().Get("Content-Type") != "text/plain" {
				t.Errorf("headers %v reached the helper, want only Content-Type", w.Header())
			}
		})
	}
}

// TestGitDoorGrantStream pins the grant route's NDJSON: each wait sentence
// as a line as it comes, then the ok line, or the error line with the
// refusal's sentence, which is also logged.
func TestGitDoorGrantStream(t *testing.T) {
	rows := []struct {
		name string
		err  error
		want string
	}{
		{"granted", nil, `{"wait":"one"}` + "\n" + `{"wait":"two"}` + "\n" + `{"ok":true}` + "\n"},
		{"refused", &Refused{Status: http.StatusForbidden, Sentence: "Straza refused this push."},
			`{"wait":"one"}` + "\n" + `{"wait":"two"}` + "\n" + `{"error":"Straza refused this push."}` + "\n"},
	}
	for _, row := range rows {
		t.Run(row.name, func(t *testing.T) {
			f := &apiDouble{session: liveSession()}
			var gotReq localapi.GitGrantRequest
			f.git.grant = func(_ context.Context, _ Session, _ string, req localapi.GitGrantRequest, wait func(string)) error {
				gotReq = req
				wait("one")
				wait("two")
				return row.err
			}
			a, logs := testAPI(f, false)
			w := httptest.NewRecorder()
			body := `{"op":"push","refs":[{"ref":"refs/heads/main","old":"` + strings.Repeat("0", 40) + `","new":"` + strings.Repeat("1", 40) + `"}]}`
			a.routes().ServeHTTP(w, asCaller(doorReq(http.MethodPost, localapi.RouteGitGrant, body), "1000"))
			if w.Code != http.StatusOK || w.Header().Get("Content-Type") != "application/x-ndjson" || w.Body.String() != row.want {
				t.Fatalf("answer = %d %q %q, want %q", w.Code, w.Header().Get("Content-Type"), w.Body.String(), row.want)
			}
			if gotReq.Op != gitPush || len(gotReq.Refs) != 1 || gotReq.Refs[0].Ref != "refs/heads/main" {
				t.Errorf("request %+v reached the credential half", gotReq)
			}
			if logged := strings.Contains(logs.String(), "Straza refused this push."); logged != (row.err != nil) {
				t.Errorf("refusal logged = %v, want %v: %s", logged, row.err != nil, logs.String())
			}
		})
	}
}

// TestGitDoorStreamCap pins the per-uid cap on open git requests: past
// maxGitStreams one uid is refused with the sentence, and another uid is
// still served.
func TestGitDoorStreamCap(t *testing.T) {
	f := &apiDouble{session: liveSession()}
	release := make(chan struct{})
	entered := make(chan struct{}, maxGitStreams)
	f.git.send = func(_ Session, _ string, _ gitRoute, in *http.Request) (*http.Response, error) {
		if callerOf(in).UID == "1000" {
			entered <- struct{}{}
			<-release
		}
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: http.NoBody}, nil
	}
	a, _ := testAPI(f, false)
	door := a.routes()
	serve := func(uid string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		door.ServeHTTP(w, asCaller(doorReq(http.MethodPost, localapi.RouteGit+"git-upload-pack", "0000"), uid))
		return w
	}
	var wg sync.WaitGroup
	codes := make(chan int, maxGitStreams)
	for range maxGitStreams {
		wg.Add(1)
		go func() {
			defer wg.Done()
			codes <- serve("1000").Code
		}()
	}
	for range maxGitStreams {
		select {
		case <-entered:
		case <-time.After(10 * time.Second):
			t.Fatal("the open requests did not all reach the credential half")
		}
	}
	if w := serve("1000"); w.Code != http.StatusTooManyRequests || refusalOf(t, w) != fmt.Sprintf(tooManyGitStreams, maxGitStreams) {
		t.Errorf("request past the cap = %d %q", w.Code, w.Body.String())
	}
	if w := serve("1001"); w.Code != http.StatusOK {
		t.Errorf("another uid's request = %d %q", w.Code, w.Body.String())
	}
	close(release)
	wg.Wait()
	close(codes)
	for c := range codes {
		if c != http.StatusOK {
			t.Errorf("an open request answered %d", c)
		}
	}
	if w := serve("1001"); w.Code != http.StatusOK {
		t.Errorf("a request after the others ended = %d", w.Code)
	}
}

// TestGitDoorResolve pins the resolve answer and that a refusal keeps its
// status and sentence.
func TestGitDoorResolve(t *testing.T) {
	rows := []struct {
		name   string
		listed bool
		err    error
		code   int
		body   string
	}{
		{"listed", true, nil, http.StatusOK, `{"listed":true}`},
		{"not listed", false, nil, http.StatusOK, `{"listed":false}`},
		{"ambiguous", false, &Refused{Status: http.StatusConflict, Sentence: "Straza: two MCP servers list it."}, http.StatusConflict,
			`{"error":"Straza: two MCP servers list it."}`},
	}
	for _, row := range rows {
		t.Run(row.name, func(t *testing.T) {
			f := &apiDouble{session: liveSession()}
			var gotURL string
			f.git.resolve = func(_ Session, upstream string) (bool, error) {
				gotURL = upstream
				return row.listed, row.err
			}
			a, _ := testAPI(f, false)
			w := httptest.NewRecorder()
			a.routes().ServeHTTP(w, asCaller(doorReq(http.MethodPost, localapi.RouteGitResolve, ""), "1000"))
			if w.Code != row.code || strings.TrimSpace(w.Body.String()) != row.body || gotURL != gitRepoURL {
				t.Errorf("answer = %d %q for %q, want %d %q", w.Code, w.Body.String(), gotURL, row.code, row.body)
			}
		})
	}
}
