package service

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/strazahq/straza/internal/agentguard/localapi"
)

// gitRig is a credential rig with alice enrolled, a fake ghapp the fake
// strazad's grants name, and the API half over the real credential half.
type gitRig struct {
	*credRig
	ghapp   *fakeGhapp
	door    http.Handler
	apiLogs *rigLog
}

// gitRepoURL is the URL git gives the helper in these tests. The fake
// lists it as Acme/pay-api, so the canonical spelling differs from it.
const gitRepoURL = fakeUpstream + "/acme/pay-api.git"

func newGitRig(t *testing.T) *gitRig {
	t.Helper()
	r := newCredRig(t)
	g := &gitRig{credRig: r, ghapp: newFakeGhapp(t, r.srv), apiLogs: &rigLog{}}
	r.srv.with(func() {
		r.srv.git.aud = g.ghapp.srv.URL
		r.srv.git.repos = map[string]string{"acme/pay-api": "Acme/pay-api"}
	})
	r.enroll(alice)
	a := newAPI(r.s, Options{Log: slog.New(slog.NewJSONHandler(g.apiLogs, nil))})
	a.self = "0"
	g.door = a.routes()
	return g
}

// newOpID returns a helper run id as the helper picks one.
func newOpID() string { return randHex(16) }

// gitReq is a git door request of claude-code window, empty for none, in
// the helper run op.
func gitReq(method, route string, body io.Reader, window, op string) *http.Request {
	r := httptest.NewRequest(method, localapi.BaseURL+route, body)
	r.Header.Set(localapi.HeaderHarness, "claude-code")
	r.Header.Set(localapi.HeaderHarnessVersion, "2.1.0")
	if window != "" {
		r.Header.Set(localapi.HeaderHarnessSession, window)
	}
	r.Header.Set(localapi.HeaderGitURL, gitRepoURL)
	r.Header.Set(localapi.HeaderGitOp, op)
	return r
}

// send serves req as c and returns the answer.
func (g *gitRig) send(c Caller, req *http.Request) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	g.door.ServeHTTP(w, asCaller(req, c.UID))
	return w
}

// grant asks the door for a grant and returns its NDJSON lines.
func (g *gitRig) grant(t *testing.T, c Caller, window, op string, req localapi.GitGrantRequest) []localapi.GitGrantLine {
	t.Helper()
	raw, _ := json.Marshal(req)
	w := g.send(c, gitReq(http.MethodPost, localapi.RouteGitGrant, bytes.NewReader(raw), window, op))
	if w.Code != http.StatusOK || w.Header().Get("Content-Type") != "application/x-ndjson" {
		t.Fatalf("grant answer = %d %q %q", w.Code, w.Header().Get("Content-Type"), w.Body.String())
	}
	return ndjsonLines(t, w.Body.Bytes())
}

// ndjsonLines decodes the grant stream.
func ndjsonLines(t *testing.T, raw []byte) []localapi.GitGrantLine {
	t.Helper()
	var out []localapi.GitGrantLine
	sc := bufio.NewScanner(bytes.NewReader(raw))
	for sc.Scan() {
		var l localapi.GitGrantLine
		if err := json.Unmarshal(sc.Bytes(), &l); err != nil {
			t.Fatalf("grant line %q: %v", sc.Text(), err)
		}
		out = append(out, l)
	}
	return out
}

// okLines reports whether lines is the single final ok line.
func okLines(lines []localapi.GitGrantLine) bool {
	return len(lines) == 1 && lines[0] == localapi.GitGrantLine{OK: true}
}

// relay sends one smart HTTP request through the door and returns the
// answer.
func (g *gitRig) relay(c Caller, window, op, method, route string, body string) *httptest.ResponseRecorder {
	req := gitReq(method, route, strings.NewReader(body), window, op)
	if method == http.MethodPost {
		svc := strings.TrimPrefix(route, localapi.RouteGit)
		req.Header.Set("Content-Type", "application/x-"+svc+"-request")
		req.Header.Set("Accept", "application/x-"+svc+"-result")
	}
	req.Header.Set("Git-Protocol", "version=2")
	req.Header.Set("User-Agent", "git/2.47.3")
	req.Header.Set("Cookie", "straza_session=stolen")
	req.Header.Set("Authorization", "Bearer stolen")
	return g.send(c, req)
}

// noSecretsLogged fails when any log line of the rig holds a grant the
// fake strazad minted or a proof ghapp accepted.
func (g *gitRig) noSecretsLogged(t *testing.T) {
	t.Helper()
	g.logs.mu.Lock()
	logs := g.logs.buf.String()
	g.logs.mu.Unlock()
	g.apiLogs.mu.Lock()
	logs += g.apiLogs.buf.String()
	g.apiLogs.mu.Unlock()
	var secrets []string
	for _, s := range g.ghapp.accepted() {
		secrets = append(secrets, s.grant, strings.Split(s.grant, ".")[1])
	}
	for _, p := range g.ghapp.signed() {
		secrets = append(secrets, p, strings.Split(p, ".")[1])
	}
	if len(secrets) == 0 {
		t.Fatal("the test minted no grant, so it proves nothing about the logs")
	}
	for _, s := range secrets {
		if strings.Contains(logs, s) {
			t.Fatalf("a grant or proof reached the log:\n%s", logs)
		}
	}
}

// noKeySentence is strazad's refusal of a session without a key.
const noKeySentence = "Straza gives git grants only to a session bound to this machine's key, and this session has none. " +
	"Run git on a machine where `straza service` runs."

// TestGitResolve pins the resolve route: a repo strazad lists answers
// listed, without case, one it does not list answers not listed, a 403
// passes strazad's sentence, and a server older than the route answers
// the upgrade sentence.
func TestGitResolve(t *testing.T) {
	rows := []struct {
		name    string
		url     string
		setup   func(f *fakeStrazad)
		code    int
		listed  bool
		refusal string
	}{
		{"listed", gitRepoURL, nil, http.StatusOK, true, ""},
		{"listed without case", fakeUpstream + "/ACME/Pay-Api", nil, http.StatusOK, true, ""},
		{"not listed", fakeUpstream + "/acme/site.git", nil, http.StatusOK, false, ""},
		{"refused", gitRepoURL, func(f *fakeStrazad) { f.git.refuse = noKeySentence }, http.StatusForbidden, false, noKeySentence},
		{"older server", gitRepoURL, func(f *fakeStrazad) { f.git.noRoute = true }, http.StatusBadGateway, false,
			"does not give git grants yet. Ask an administrator to upgrade it, then run git again."},
	}
	for _, row := range rows {
		t.Run(row.name, func(t *testing.T) {
			g := newGitRig(t)
			if _, err := g.start(alice, "w1"); err != nil {
				t.Fatal(err)
			}
			if row.setup != nil {
				g.srv.with(func() { row.setup(g.srv) })
			}
			req := gitReq(http.MethodPost, localapi.RouteGitResolve, nil, "w1", newOpID())
			req.Header.Set(localapi.HeaderGitURL, row.url)
			w := g.send(alice, req)
			if w.Code != row.code {
				t.Fatalf("answer = %d %q, want %d", w.Code, w.Body.String(), row.code)
			}
			if row.refusal != "" {
				if got := refusalOf(t, w); !strings.Contains(got, row.refusal) {
					t.Fatalf("refusal = %q, want it to hold %q", got, row.refusal)
				}
				return
			}
			var ans localapi.GitResolveAnswer
			if err := json.Unmarshal(w.Body.Bytes(), &ans); err != nil || ans.Listed != row.listed {
				t.Fatalf("answer = %q (%v), want listed %v", w.Body.String(), err, row.listed)
			}
		})
	}
}

// TestGitFetchReusesGrant pins a fetch: one grant from strazad, asked with
// waitSeconds 5, opens the upload-pack advertisement and every upload-pack
// POST of the run, each with a fresh proof, at the canonical repo's URL,
// with only git's headers each way.
func TestGitFetchReusesGrant(t *testing.T) {
	g := newGitRig(t)
	if _, err := g.start(alice, "w1"); err != nil {
		t.Fatal(err)
	}
	op := newOpID()
	if lines := g.grant(t, alice, "w1", op, localapi.GitGrantRequest{Op: "fetch"}); !okLines(lines) {
		t.Fatalf("grant lines = %+v", lines)
	}
	w := g.relay(alice, "w1", op, http.MethodGet, localapi.RouteGit+"info/refs?service=git-upload-pack", "")
	if w.Code != http.StatusOK || w.Header().Get("Content-Type") != "application/x-git-upload-pack-advertisement" {
		t.Fatalf("info/refs = %d %q %q", w.Code, w.Header(), w.Body.String())
	}
	if w.Header().Get("X-Ghapp-Internal") != "" || w.Header().Get("Cache-Control") != "" {
		t.Errorf("headers beyond git's own reached the helper: %v", w.Header())
	}
	for i := range 3 {
		body := fmt.Sprintf("0011command=fetch%04d", i)
		w := g.relay(alice, "w1", op, http.MethodPost, localapi.RouteGit+"git-upload-pack", body)
		want := fmt.Sprintf("ok /git/Acme/pay-api.git/git-upload-pack %d", len(body))
		if w.Code != http.StatusOK || w.Body.String() != want {
			t.Fatalf("upload-pack %d = %d %q, want %q (ghapp refused %v)", i, w.Code, w.Body.String(), want, g.ghapp.refusals())
		}
	}
	seen := g.ghapp.accepted()
	if len(seen) != 4 {
		t.Fatalf("ghapp accepted %d requests (refused %v), want 4", len(seen), g.ghapp.refusals())
	}
	jtis := map[string]bool{}
	for _, s := range seen {
		jtis[s.jti] = true
		if s.grant != seen[0].grant {
			t.Errorf("request %s used another grant", s.path)
		}
		if s.header.Get("Git-Protocol") != "version=2" || s.header.Get("User-Agent") != "git/2.47.3" {
			t.Errorf("git's headers did not reach ghapp: %v", s.header)
		}
	}
	if len(jtis) != 4 {
		t.Errorf("ghapp saw %d distinct proofs for 4 requests", len(jtis))
	}
	g.srv.with(func() {
		if len(g.srv.git.asked) != 1 || !strings.Contains(g.srv.git.asked[0], `"waitSeconds":0`) ||
			!strings.Contains(g.srv.git.asked[0], `"upstream":"`+gitRepoURL+`"`) || strings.Contains(g.srv.git.asked[0], "refs") {
			t.Errorf("grant requests = %q, want one fetch with waitSeconds 0", g.srv.git.asked)
		}
	})
	g.noSecretsLogged(t)
}

// TestGitPushFlow pins a push: a grant without refs opens the receive-pack
// advertisement and nothing else, a grant naming the updates opens the
// POST with them, and a route whose grant the run never took is refused
// without reaching ghapp. A refusal ghapp writes reaches the helper as it
// came.
func TestGitPushFlow(t *testing.T) {
	g := newGitRig(t)
	if _, err := g.start(alice, "w1"); err != nil {
		t.Fatal(err)
	}
	op := newOpID()
	if lines := g.grant(t, alice, "w1", op, localapi.GitGrantRequest{Op: "push"}); !okLines(lines) {
		t.Fatalf("advertisement grant lines = %+v", lines)
	}
	if w := g.relay(alice, "w1", op, http.MethodGet, localapi.RouteGit+"info/refs?service=git-receive-pack", ""); w.Code != http.StatusOK {
		t.Fatalf("receive-pack info/refs = %d %q (ghapp refused %v)", w.Code, w.Body.String(), g.ghapp.refusals())
	}
	w := g.relay(alice, "w1", op, http.MethodPost, localapi.RouteGit+"git-upload-pack", "0000")
	if w.Code != http.StatusForbidden || !strings.Contains(refusalOf(t, w), "holds no git grant") {
		t.Fatalf("upload-pack under push grants = %d %q", w.Code, w.Body.String())
	}
	w = g.relay(alice, "w1", op, http.MethodPost, localapi.RouteGit+"git-receive-pack", "0000")
	if w.Code != http.StatusForbidden || !strings.Contains(refusalOf(t, w), "holds no git grant") {
		t.Fatalf("receive-pack under the grant without refs = %d %q", w.Code, w.Body.String())
	}
	if n := len(g.ghapp.accepted()) + len(g.ghapp.refusals()); n != 1 {
		t.Fatalf("ghapp saw %d requests, want only the advertisement", n)
	}
	refs := []localapi.GitRef{{Ref: "refs/heads/feature", Old: strings.Repeat("0", 40), New: strings.Repeat("a", 40)}}
	if lines := g.grant(t, alice, "w1", op, localapi.GitGrantRequest{Op: "push", Refs: refs}); !okLines(lines) {
		t.Fatalf("push grant lines = %+v", lines)
	}
	body := "00960000000000000000000000000000000000000000 aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa refs/heads/feature\x00report-status-v2\n0000PACK"
	w = g.relay(alice, "w1", op, http.MethodPost, localapi.RouteGit+"git-receive-pack", body)
	if w.Code != http.StatusOK || w.Header().Get("Content-Type") != "application/x-git-receive-pack-result" {
		t.Fatalf("receive-pack = %d %q (ghapp refused %v)", w.Code, w.Body.String(), g.ghapp.refusals())
	}
	g.srv.with(func() {
		if len(g.srv.git.asked) != 2 || strings.Contains(g.srv.git.asked[0], "refs") ||
			!strings.Contains(g.srv.git.asked[1], `"refs":[{"ref":"refs/heads/feature"`) {
			t.Errorf("grant requests = %q, want one without refs and one naming the update", g.srv.git.asked)
		}
	})
	g.srv.with(func() { g.srv.git.grants = map[string]fakeGrant{"forgotten": {}} })
	w = g.relay(alice, "w1", op, http.MethodPost, localapi.RouteGit+"git-receive-pack", body)
	if w.Code != http.StatusForbidden || w.Body.String() != "ghapp refused: unknown grant\n" ||
		w.Header().Get("Content-Type") != "text/plain; charset=utf-8" || w.Header().Get("WWW-Authenticate") != "" {
		t.Fatalf("ghapp's refusal reached the helper as %d %v %q", w.Code, w.Header(), w.Body.String())
	}
	g.noSecretsLogged(t)
}

// TestGitGrantWaits pins the approval hold: the first ask holds nothing,
// so the first wait line comes at once, then while strazad answers 202 the
// door writes a wait line at least every wait period, also while a request
// is in flight, asks again with waitSeconds 5 and the same body otherwise,
// and ends with ok when the grant arrives.
func TestGitGrantWaits(t *testing.T) {
	g := newGitRig(t)
	if _, err := g.start(alice, "w1"); err != nil {
		t.Fatal(err)
	}
	every := 100 * time.Millisecond
	g.s.gitState().waitEvery = every
	g.srv.with(func() {
		g.srv.git.pending, g.srv.git.hold, g.srv.git.expires = 3, 3*every, g.clock.Now().Add(90*time.Second)
	})
	refs := []localapi.GitRef{{Ref: "refs/heads/main", Old: strings.Repeat("1", 40), New: strings.Repeat("2", 40)}}
	raw, _ := json.Marshal(localapi.GitGrantRequest{Op: "push", Refs: refs})
	w := &lineClock{header: http.Header{}}
	start := time.Now()
	g.door.ServeHTTP(w, asCaller(gitReq(http.MethodPost, localapi.RouteGitGrant, bytes.NewReader(raw), "w1", newOpID()), alice.UID))
	lines := w.lines()
	if len(lines) < 4 || lines[len(lines)-1].text != `{"ok":true}` {
		t.Fatalf("grant stream = %+v", lines)
	}
	if first := lines[0].at.Sub(start); first > every {
		t.Errorf("the first wait line came after %v, want it at once", first)
	}
	want := `{"wait":"Straza: waiting for an approver to allow refs/heads/main on github.com/acme/pay-api (90s left)."}`
	last := lines[0].at
	for _, l := range lines {
		if gap := l.at.Sub(last); gap > 2*every {
			t.Errorf("a gap of %v between lines, want at most %v: %+v", gap, 2*every, lines)
		}
		last = l.at
		if l.text != want && l.text != `{"ok":true}` {
			t.Errorf("line %q, want %q", l.text, want)
		}
	}
	g.srv.with(func() {
		if len(g.srv.git.asked) != 4 || !strings.Contains(g.srv.git.asked[0], `"waitSeconds":0`) {
			t.Fatalf("grant requests = %q, want 4, the first with waitSeconds 0", g.srv.git.asked)
		}
		again := strings.Replace(g.srv.git.asked[0], `"waitSeconds":0`, `"waitSeconds":5`, 1)
		for _, b := range g.srv.git.asked[1:] {
			if b != again {
				t.Errorf("asked again with %q, want %q", b, again)
			}
		}
	})
}

// TestGitGrantWaitPeriod pins the real wait period and wait parameter.
func TestGitGrantWaitPeriod(t *testing.T) {
	if gitWaitEvery != 5*time.Second || gitWaitSeconds != 5 {
		t.Errorf("wait period %v and waitSeconds %d, want 5 seconds each", gitWaitEvery, gitWaitSeconds)
	}
}

// TestGitGrantRefusals pins the final error line: an approval window that
// ends, strazad's own 403 sentence, and a repo strazad stopped listing.
// None leaves a grant to relay with.
func TestGitGrantRefusals(t *testing.T) {
	rows := []struct {
		name  string
		setup func(g *gitRig)
		req   localapi.GitGrantRequest
		want  string
	}{
		{"push approval ended", func(g *gitRig) {
			g.srv.git.pending, g.srv.git.expires = 100, g.clock.Now().Add(-time.Second)
		}, localapi.GitGrantRequest{Op: "push", Refs: []localapi.GitRef{{Ref: "refs/heads/main", Old: strings.Repeat("1", 40), New: strings.Repeat("2", 40)}}},
			"Straza: nobody approved this push to github.com/acme/pay-api in time, so it did not run. Ask your approver, then run git push again."},
		{"fetch approval ended", func(g *gitRig) {
			g.srv.git.pending, g.srv.git.expires = 100, g.clock.Now().Add(-time.Second)
		}, localapi.GitGrantRequest{Op: "fetch"},
			"Straza: nobody approved this fetch from github.com/acme/pay-api in time, so it did not run. Ask your approver, then run git again."},
		{"decision refused", func(g *gitRig) {
			g.srv.git.refuse = "Straza refused this push: no role allows git_push on Acme/pay-api."
		}, localapi.GitGrantRequest{Op: "push"}, "Straza refused this push: no role allows git_push on Acme/pay-api."},
		{"not listed any more", func(g *gitRig) { g.srv.git.repos = nil }, localapi.GitGrantRequest{Op: "fetch"},
			"Straza: the repo is not listed yet."},
	}
	for _, row := range rows {
		t.Run(row.name, func(t *testing.T) {
			g := newGitRig(t)
			if _, err := g.start(alice, "w1"); err != nil {
				t.Fatal(err)
			}
			g.srv.with(func() { row.setup(g) })
			op := newOpID()
			lines := g.grant(t, alice, "w1", op, row.req)
			if len(lines) != 1 || lines[0].Error != row.want {
				t.Fatalf("grant lines = %+v, want one error %q", lines, row.want)
			}
			w := g.relay(alice, "w1", op, http.MethodGet, localapi.RouteGit+"info/refs?service=git-upload-pack", "")
			if w.Code != http.StatusForbidden || len(g.ghapp.accepted())+len(g.ghapp.refusals()) != 0 {
				t.Fatalf("relay after a refused grant = %d %q, ghapp saw %d", w.Code, w.Body.String(), len(g.ghapp.accepted()))
			}
		})
	}
}

// TestGitGrantApprovalTimesOut pins a hold that outlives its window: wait
// lines count down, and when expiresAt passes the door ends with the
// timeout sentence without asking strazad again.
func TestGitGrantApprovalTimesOut(t *testing.T) {
	g := newGitRig(t)
	if _, err := g.start(alice, "w1"); err != nil {
		t.Fatal(err)
	}
	every := 50 * time.Millisecond
	g.s.gitState().waitEvery = every
	g.srv.with(func() { g.srv.git.pending, g.srv.git.expires = 1000, g.clock.Now().Add(30*time.Second) })
	w := &lineClock{header: http.Header{}, onLine: func(n int) {
		if n == 2 {
			g.clock.Advance(31 * time.Second)
		}
	}}
	g.door.ServeHTTP(w, asCaller(gitReq(http.MethodPost, localapi.RouteGitGrant, strings.NewReader(`{"op":"fetch"}`), "w1", newOpID()), alice.UID))
	lines := w.lines()
	if len(lines) < 3 || !strings.Contains(lines[0].text, "(30s left)") || !strings.Contains(lines[len(lines)-1].text, "nobody approved this fetch") {
		t.Fatalf("grant stream = %+v", lines)
	}
}

// TestGitOtherUID pins that a second user reaches neither the first one's
// grants nor her window: the run id is keyed under the caller's uid, and
// a named window is looked up among the caller's own sessions.
func TestGitOtherUID(t *testing.T) {
	g := newGitRig(t)
	bob := Caller{UID: testUID(4243), PID: 2}
	g.enroll(bob)
	if _, err := g.start(alice, "w-alice"); err != nil {
		t.Fatal(err)
	}
	if _, err := g.start(bob, "w-bob"); err != nil {
		t.Fatal(err)
	}
	op := newOpID()
	if lines := g.grant(t, alice, "w-alice", op, localapi.GitGrantRequest{Op: "fetch"}); !okLines(lines) {
		t.Fatalf("alice's grant lines = %+v", lines)
	}
	w := g.relay(bob, "w-bob", op, http.MethodGet, localapi.RouteGit+"info/refs?service=git-upload-pack", "")
	if w.Code != http.StatusForbidden || !strings.Contains(refusalOf(t, w), "holds no git grant") {
		t.Fatalf("bob with alice's run id = %d %q", w.Code, w.Body.String())
	}
	w = g.relay(bob, "w-alice", op, http.MethodGet, localapi.RouteGit+"info/refs?service=git-upload-pack", "")
	if w.Code != http.StatusConflict || !strings.Contains(refusalOf(t, w), "no active Straza session") {
		t.Fatalf("bob naming alice's window = %d %q", w.Code, w.Body.String())
	}
	if n := len(g.ghapp.accepted()) + len(g.ghapp.refusals()); n != 0 {
		t.Fatalf("ghapp saw %d of bob's requests", n)
	}
	if w := g.relay(alice, "w-alice", op, http.MethodGet, localapi.RouteGit+"info/refs?service=git-upload-pack", ""); w.Code != http.StatusOK {
		t.Fatalf("alice's own relay = %d %q", w.Code, w.Body.String())
	}
}

// timedLine is one line of a streamed answer and when it arrived.
type timedLine struct {
	text string
	at   time.Time
}

// lineClock is a ResponseWriter that notes when each line arrives, and
// calls onLine with the count of lines so far after each one.
type lineClock struct {
	header http.Header
	onLine func(n int)

	mu    sync.Mutex
	buf   []byte
	timed []timedLine
}

func (l *lineClock) Header() http.Header { return l.header }
func (l *lineClock) WriteHeader(int)     {}
func (l *lineClock) Flush()              {}

func (l *lineClock) Write(p []byte) (int, error) {
	l.mu.Lock()
	l.buf = append(l.buf, p...)
	var n int
	for {
		i := bytes.IndexByte(l.buf, '\n')
		if i < 0 {
			break
		}
		l.timed = append(l.timed, timedLine{text: string(l.buf[:i]), at: time.Now()})
		l.buf = l.buf[i+1:]
		n = len(l.timed)
	}
	on := l.onLine
	l.mu.Unlock()
	if n > 0 && on != nil {
		on(n)
	}
	return len(p), nil
}

func (l *lineClock) lines() []timedLine {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]timedLine(nil), l.timed...)
}
