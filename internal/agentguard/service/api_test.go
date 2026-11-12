package service

import (
	"bufio"
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/strazahq/straza/internal/agentguard/localapi"
	"github.com/strazahq/straza/internal/policy"
	"github.com/strazahq/straza/internal/version"
)

// apiDouble is the credential half as the API tests script it. Each wait
// holds its method until the wait passes or the context ends, which is how
// a test spends a budget.
type apiDouble struct {
	mu sync.Mutex

	session     Session
	sessionErr  error // every Session call
	liveErr     error // Session calls with start false
	sessionWait time.Duration
	// sessionWaitErr is what Session answers when the context ends during
	// sessionWait.
	sessionWaitErr error
	sessionCalls   []sessionCall

	engine      *policy.Engine
	maxAge      int64
	snapshot    string // the snapshot Policy names, the session's pin when empty
	policyErr   error
	policyWait  time.Duration
	policyCalls int

	decide      func(ctx context.Context, ev policy.Event) (DecideOutcome, error)
	decideCalls []decideCall

	records   []apiRecord
	recordErr error

	transport http.RoundTripper
	git       gitDouble // scripts the git door's calls
	status    localapi.Status
	callers   []Caller
	enroll    func(emit func(localapi.EnrollEvent)) (localapi.EnrollResult, error)
	keygen    func(req localapi.KeygenRequest) (localapi.KeygenAnswer, error)
}

type sessionCall struct {
	caller  Caller
	harness localapi.Harness
	start   bool
}

type decideCall struct {
	event              policy.Event
	agentType, agentID string
	input              *localapi.ToolInput
}

type apiRecord struct {
	caller Caller
	ceType string
	data   map[string]any
}

// hold waits d or until ctx ends, and reports whether d passed.
func hold(ctx context.Context, d time.Duration) bool {
	if d <= 0 {
		return true
	}
	select {
	case <-ctx.Done():
		return false
	case <-time.After(d):
		return true
	}
}

func (f *apiDouble) Enroll(_ context.Context, c Caller, _ localapi.EnrollRequest, emit func(localapi.EnrollEvent)) (localapi.EnrollResult, error) {
	f.mu.Lock()
	f.callers = append(f.callers, c)
	f.mu.Unlock()
	return f.enroll(emit)
}

func (f *apiDouble) Keygen(_ context.Context, c Caller, req localapi.KeygenRequest) (localapi.KeygenAnswer, error) {
	f.mu.Lock()
	f.callers = append(f.callers, c)
	f.mu.Unlock()
	return f.keygen(req)
}

func (f *apiDouble) Status(c Caller) localapi.Status {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.callers = append(f.callers, c)
	return f.status
}

func (f *apiDouble) Session(ctx context.Context, c Caller, h localapi.Harness, start bool) (Session, error) {
	f.mu.Lock()
	f.sessionCalls = append(f.sessionCalls, sessionCall{caller: c, harness: h, start: start})
	s, err, live := f.session, f.sessionErr, f.liveErr
	f.mu.Unlock()
	if !hold(ctx, f.sessionWait) {
		if f.sessionWaitErr != nil {
			return Session{}, f.sessionWaitErr
		}
		s.Renewal = Renewal{Class: RenewTransport, Detail: ctx.Err().Error()}
	}
	if err == nil && !start {
		err = live
	}
	return s, err
}

func (f *apiDouble) Policy(ctx context.Context, s Session) (*policy.Engine, int64, string, error) {
	f.mu.Lock()
	f.policyCalls++
	snapshot := f.snapshot
	f.mu.Unlock()
	hold(ctx, f.policyWait)
	if snapshot == "" {
		snapshot = s.SnapshotID
	}
	return f.engine, f.maxAge, snapshot, f.policyErr
}

func (f *apiDouble) Decide(ctx context.Context, _ Session, ev policy.Event, agentType, agentID string, input *localapi.ToolInput) (DecideOutcome, error) {
	f.mu.Lock()
	f.decideCalls = append(f.decideCalls, decideCall{event: ev, agentType: agentType, agentID: agentID, input: input})
	f.mu.Unlock()
	if f.decide == nil {
		return DecideOutcome{}, errors.New("dial tcp 127.0.0.1:1: connect: connection refused")
	}
	return f.decide(ctx, ev)
}

func (f *apiDouble) Record(c Caller, ceType string, data map[string]any) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.recordErr != nil {
		return f.recordErr
	}
	f.records = append(f.records, apiRecord{caller: c, ceType: ceType, data: data})
	return nil
}

func (f *apiDouble) Transport(Session) http.RoundTripper { return f.transport }

// recorded returns the records so far.
func (f *apiDouble) recorded() []apiRecord {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]apiRecord(nil), f.records...)
}

// apiEngine compiles, signs and opens doc the way strazad and the
// credential half do, with the offline grace maxAge.
func apiEngine(t *testing.T, doc string, maxAge int64) *policy.Engine {
	t.Helper()
	snap, err := policy.Compile(policy.CompileInput{
		Documents: [][]byte{[]byte(doc)}, LocalDefault: "allow", MaxAge: maxAge, CreatedUnix: time.Now().Unix(),
	})
	if err != nil {
		t.Fatal(err)
	}
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	signed, id, err := snap.Sign("k1", priv)
	if err != nil {
		t.Fatal(err)
	}
	eng, _, err := policy.OpenSnapshot(signed, id, func(kid string) (ed25519.PublicKey, bool) { return pub, kid == "k1" })
	if err != nil {
		t.Fatal(err)
	}
	return eng
}

// liveSession is a session of kim with the dev role, an hour from expiry.
func liveSession() Session {
	return Session{
		UID: "1000", Harness: localapi.Harness{Name: "claude-code", Version: "2.1.0", Session: "w1"},
		ServerURL: "https://straza.example", ID: "s-1", User: "kim", Roles: []string{"dev"},
		Attestation: "managed", SnapshotID: "snap-1", IssuedAt: time.Now(), ExpiresAt: time.Now().Add(time.Hour),
	}
}

// testAPI returns the API half over f with its refusal log in logs, the
// service's own uid at 0.
func testAPI(f *apiDouble, userMode bool) (*api, *bytes.Buffer) {
	logs := &bytes.Buffer{}
	a := newAPI(f, Options{UserMode: userMode, Log: slog.New(slog.NewJSONHandler(logs, nil))})
	a.self = "0"
	return a, logs
}

// asCaller is r as the kernel would name uid on the socket.
func asCaller(r *http.Request, uid string) *http.Request {
	return r.WithContext(context.WithValue(r.Context(), peerKey{}, peer{caller: Caller{UID: uid, PID: 4242}}))
}

// serveAs sends one request to a's routes as uid and returns the answer.
func serveAs(a *api, uid string, method, path string, body any) *httptest.ResponseRecorder {
	var rd io.Reader
	switch b := body.(type) {
	case nil:
	case string:
		rd = strings.NewReader(b)
	default:
		raw, _ := json.Marshal(b)
		rd = bytes.NewReader(raw)
	}
	w := httptest.NewRecorder()
	a.routes().ServeHTTP(w, asCaller(httptest.NewRequest(method, localapi.BaseURL+path, rd), uid))
	return w
}

// refusalOf decodes a Refusal answer.
func refusalOf(t *testing.T, w *httptest.ResponseRecorder) string {
	t.Helper()
	var r localapi.Refusal
	if err := json.Unmarshal(w.Body.Bytes(), &r); err != nil || r.Error == "" {
		t.Fatalf("answer %d is not a Refusal with a sentence: %q", w.Code, w.Body.String())
	}
	return r.Error
}

// logLines decodes the refusal log.
func logLines(t *testing.T, logs *bytes.Buffer) []map[string]any {
	t.Helper()
	var out []map[string]any
	sc := bufio.NewScanner(bytes.NewReader(logs.Bytes()))
	for sc.Scan() {
		var m map[string]any
		if err := json.Unmarshal(sc.Bytes(), &m); err != nil {
			t.Fatalf("log line %q: %v", sc.Text(), err)
		}
		if m["msg"] == "local API refusal" {
			out = append(out, m)
		}
	}
	return out
}

// TestAPIStatusComputesCustody pins that status names the caller's custody
// from the uids alone, account when the service runs under its own account
// and user when it runs as the caller, and passes the credential half's
// sessions through as it reports them.
func TestAPIStatusComputesCustody(t *testing.T) {
	for _, row := range []struct {
		name     string
		uid      string
		userMode bool
		want     string
	}{
		{"a person's uid under the service account", "1000", false, localapi.CustodyAccount},
		{"the service's own uid in user mode", "0", true, localapi.CustodyUser},
	} {
		t.Run(row.name, func(t *testing.T) {
			ses := localapi.SessionStatus{Harness: localapi.Harness{Name: "claude-code", Session: "w1"}, SessionID: "s-1",
				Push: localapi.PushConnected, PolicyRefused: "The server refused it: the snapshot is not served to this session."}
			f := &apiDouble{status: localapi.Status{Spool: localapi.SpoolStatus{Pending: 3}, Sessions: []localapi.SessionStatus{ses}}}
			a, _ := testAPI(f, row.userMode)
			w := serveAs(a, row.uid, http.MethodGet, localapi.RouteStatus, nil)
			var st localapi.Status
			if err := json.Unmarshal(w.Body.Bytes(), &st); err != nil || w.Code != http.StatusOK {
				t.Fatalf("status = %d %q", w.Code, w.Body.String())
			}
			if st.Custody != row.want || st.UserMode != row.userMode || st.Version != version.Version || st.Spool.Pending != 3 {
				t.Errorf("status = %+v, want custody %s, user mode %v, version %s and the credential half's spool", st, row.want, row.userMode, version.Version)
			}
			if len(st.Sessions) != 1 || st.Sessions[0].Harness.Session != "w1" || st.Sessions[0].Push != localapi.PushConnected ||
				st.Sessions[0].PolicyRefused != ses.PolicyRefused {
				t.Errorf("sessions = %+v, want the credential half's window as it reports it", st.Sessions)
			}
			if len(f.callers) != 1 || f.callers[0].UID != row.uid {
				t.Errorf("the credential half answered for %+v, want uid %s", f.callers, row.uid)
			}
		})
	}
}

// TestAPIRefusalsAreSentences pins that every answer at or above 400 is a
// Refusal with a sentence and is logged as one line with the uid, the
// route and the reason.
func TestAPIRefusalsAreSentences(t *testing.T) {
	big := `{"harness":{"name":"claude-code"},"native_event":"` + strings.Repeat("x", textBodyMax) + `"}`
	bigText := `{"harness":{"name":"claude-code"},"kind":"reply","text":"` + strings.Repeat("x", textBodyMax) + `"}`
	rows := []struct {
		name, method, path string
		body               any
		status             int
		says               string
	}{
		{"wrong method", http.MethodGet, localapi.RouteDecide, nil, http.StatusMethodNotAllowed, "takes POST"},
		{"unknown route", http.MethodGet, localapi.Prefix + "/nothing", nil, http.StatusNotFound, "has no route"},
		{"body that is not JSON", http.MethodPost, localapi.RouteDecide, "{", http.StatusBadRequest, "could not read the request"},
		{"tool call over the bound", http.MethodPost, localapi.RouteDecide, big, http.StatusRequestEntityTooLarge,
			"The request for this tool call is larger than the 16 MiB the Straza background service reads, so the service refused it and the call is denied."},
		{"captured text over the bound", http.MethodPost, localapi.RouteCapture, bigText, http.StatusRequestEntityTooLarge,
			"The captured text is larger than the 16 MiB the Straza background service reads, so it was not recorded."},
		{"session start over the bound", http.MethodPost, localapi.RouteSessions, `{"harness":{"name":"` + strings.Repeat("x", smallBodyMax) + `"}}`,
			http.StatusRequestEntityTooLarge, "larger than the 64 KiB the Straza background service reads on /local/v1/sessions"},
		{"no harness", http.MethodPost, localapi.RouteSessions, localapi.SessionRequest{}, http.StatusBadRequest, "names no harness"},
		{"proxy with no harness header", http.MethodPost, localapi.RouteMCP, "{}", http.StatusBadRequest, "names no harness"},
		{"capture of an unknown kind", http.MethodPost, localapi.RouteCapture,
			localapi.CaptureRequest{Harness: localapi.Harness{Name: "claude-code"}, Kind: "tool"}, http.StatusBadRequest, "only prompt and reply"},
		{"not enrolled", http.MethodPost, localapi.RouteDecide,
			localapi.DecideRequest{Harness: localapi.Harness{Name: "claude-code"}}, http.StatusForbidden, "isn't enrolled"},
	}
	for _, row := range rows {
		t.Run(row.name, func(t *testing.T) {
			f := &apiDouble{sessionErr: &Refused{Status: http.StatusForbidden, Sentence: "This machine isn't enrolled for you yet. Run `straza enroll` and sign in with your work account."}}
			a, logs := testAPI(f, false)
			w := serveAs(a, "1000", row.method, row.path, row.body)
			if w.Code != row.status || !strings.Contains(refusalOf(t, w), row.says) {
				t.Fatalf("answer = %d %q, want %d saying %q", w.Code, w.Body.String(), row.status, row.says)
			}
			lines := logLines(t, logs)
			if len(lines) != 1 {
				t.Fatalf("%d refusal log lines, want 1: %s", len(lines), logs)
			}
			if l := lines[0]; l["uid"] != "1000" || !strings.HasPrefix(l["route"].(string), localapi.Prefix) || l["reason"] != refusalOf(t, w) {
				t.Errorf("log line = %v, want uid 1000, the route and the sentence", l)
			}
		})
	}
	t.Run("a caller the kernel did not name", func(t *testing.T) {
		a, logs := testAPI(&apiDouble{}, false)
		w := httptest.NewRecorder()
		a.routes().ServeHTTP(w, httptest.NewRequest(http.MethodGet, localapi.BaseURL+localapi.RouteStatus, nil))
		if w.Code != http.StatusForbidden || !strings.Contains(refusalOf(t, w), "could not tell which") {
			t.Fatalf("answer = %d %q, want the 403 sentence", w.Code, w.Body.String())
		}
		if lines := logLines(t, logs); len(lines) != 1 || lines[0]["uid"] != "unknown" {
			t.Errorf("log = %v, want one line with an unknown uid", lines)
		}
	})
}

// TestAPISessionsAnswer pins POST sessions: a start through the credential
// half, the capture directive the session's policy gives its subject, the
// packs and Reused, and the credential half's refusals passed through.
func TestAPISessionsAnswer(t *testing.T) {
	eng := apiEngine(t, `
apiVersion: straza.dev/v1beta1
kind: PolicySet
metadata: {name: cap-on}
spec:
  match: {roles: [dev]}
  capture: {conversations: true, mode: redact}
  rules: [{id: r1, effect: deny, tools: [shell.exec], command: {denyPatterns: ["rm -rf *"]}}]
`, 900)
	h := localapi.Harness{Name: "claude-code", Version: "2.1.0", Session: "w1"}
	t.Run("a started session", func(t *testing.T) {
		s := liveSession()
		text := "the server's start text"
		s.Packs, s.Reused, s.StartText = []localapi.Pack{{Name: "p", Version: "1", Content: "c", Checksum: "x"}}, true, &text
		f := &apiDouble{session: s, engine: eng}
		a, _ := testAPI(f, false)
		w := serveAs(a, "1000", http.MethodPost, localapi.RouteSessions, localapi.SessionRequest{Harness: h})
		var ans localapi.SessionAnswer
		if err := json.Unmarshal(w.Body.Bytes(), &ans); err != nil || w.Code != http.StatusOK {
			t.Fatalf("answer = %d %q", w.Code, w.Body.String())
		}
		if ans.SessionID != "s-1" || ans.User != "kim" || ans.SnapshotID != "snap-1" || ans.ServerURL != s.ServerURL ||
			!ans.Capture.Conversations || ans.Capture.Mode != policy.CaptureModeRedact || len(ans.Packs) != 1 || !ans.Reused ||
			ans.StartText == nil || *ans.StartText != text {
			t.Errorf("answer = %+v, want the session, its packs, its start text and the redact capture directive", ans)
		}
		if c := f.sessionCalls; len(c) != 1 || !c[0].start || c[0].harness != h || c[0].caller.UID != "1000" {
			t.Errorf("Session calls = %+v, want one start for uid 1000 and the window", c)
		}
	})
	t.Run("the waiting sentence passes through", func(t *testing.T) {
		const waiting = "Straza is waiting for the server to confirm this machine's last check-in, which takes up to two minutes after a lost connection. Start the session again in a minute."
		a, _ := testAPI(&apiDouble{sessionErr: &Refused{Status: http.StatusServiceUnavailable, Sentence: waiting}}, false)
		w := serveAs(a, "1000", http.MethodPost, localapi.RouteSessions, localapi.SessionRequest{Harness: h})
		if w.Code != http.StatusServiceUnavailable || refusalOf(t, w) != waiting {
			t.Errorf("answer = %d %q, want 503 with the sentence as it is", w.Code, w.Body.String())
		}
	})
	t.Run("a policy that cannot be had fails the start closed", func(t *testing.T) {
		a, _ := testAPI(&apiDouble{session: liveSession(), policyErr: errors.New("the policy snapshot is missing on this machine")}, false)
		w := serveAs(a, "1000", http.MethodPost, localapi.RouteSessions, localapi.SessionRequest{Harness: h})
		if w.Code != http.StatusBadGateway || !strings.Contains(refusalOf(t, w), "snapshot is missing") {
			t.Errorf("answer = %d %q, want 502 with the policy sentence", w.Code, w.Body.String())
		}
	})
}

// TestAPIStartLimit pins the per-uid limit on check-ins and enrollments:
// starts that reuse a live session spend nothing, 15 windows starting their
// hook and their bridge at once pass twice over, a burst past 60 is refused
// with the sentence until tokens refill at 10 per second, another uid is
// not affected, and decide is never limited.
func TestAPIStartLimit(t *testing.T) {
	f := &apiDouble{session: liveSession(), engine: apiEngine(t, apiOfflineDoc, 900)}
	a, _ := testAPI(f, false)
	now := time.Now()
	a.starts.now = func() time.Time { return now }
	start := func(uid string) int {
		return serveAs(a, uid, http.MethodPost, localapi.RouteSessions, localapi.SessionRequest{Harness: localapi.Harness{Name: "claude-code"}}).Code
	}
	f.session.Reused = true
	for i := range 10 * startBurst {
		if c := start("1000"); c != http.StatusOK {
			t.Fatalf("reuse %d got %d, want reuses to spend nothing", i, c)
		}
	}
	f.session.Reused = false
	var wg sync.WaitGroup
	codes := make([]int, 30)
	for i := range codes {
		wg.Go(func() { codes[i] = start("1000") })
	}
	wg.Wait()
	for i, c := range codes {
		if c != http.StatusOK {
			t.Fatalf("start %d of 15 windows' hooks and bridges at once got %d", i, c)
		}
	}
	for i := len(codes); i < startBurst; i++ {
		if c := start("1000"); c != http.StatusOK {
			t.Fatalf("check-in %d within the burst got %d", i+1, c)
		}
	}
	w := serveAs(a, "1000", http.MethodPost, localapi.RouteEnroll, localapi.EnrollRequest{ServerURL: "https://straza.example"})
	if w.Code != http.StatusTooManyRequests || refusalOf(t, w) != tooManyStarts {
		t.Fatalf("enroll past the burst = %d %q, want 429 with the sentence", w.Code, w.Body.String())
	}
	if c := start("1001"); c != http.StatusOK {
		t.Errorf("another uid's start = %d, want its own bucket", c)
	}
	dec := localapi.DecideRequest{Harness: localapi.Harness{Name: "claude-code"}, Event: policy.Event{Kind: policy.EventToolPre, Tool: policy.ToolShellExec, Command: "ls"}}
	for i := range 100 {
		if c := serveAs(a, "1000", http.MethodPost, localapi.RouteDecide, dec).Code; c != http.StatusOK {
			t.Fatalf("decide %d past the start burst = %d, want no limit", i, c)
		}
	}
	now = now.Add(time.Second)
	for i := range startRate {
		if c := start("1000"); c != http.StatusOK {
			t.Fatalf("check-in %d after one second of refill = %d", i, c)
		}
	}
	if c := start("1000"); c != http.StatusTooManyRequests {
		t.Errorf("check-in past the refill = %d, want 429", c)
	}
}

// TestAPISessionBudget pins that a start queued behind other windows'
// check-ins answers within the service's budget, before the thin client
// gives up, with the sentence that says the service was busy, and not the
// credential half's sentence for a lost answer.
func TestAPISessionBudget(t *testing.T) {
	t.Parallel()
	f := &apiDouble{session: liveSession(), sessionWait: time.Minute,
		sessionWaitErr: &Refused{Status: http.StatusServiceUnavailable, Sentence: "Straza is waiting for the server to confirm this machine's last check-in."}}
	a, logs := testAPI(f, false)
	start := time.Now()
	w := serveAs(a, "1000", http.MethodPost, localapi.RouteSessions, localapi.SessionRequest{Harness: localapi.Harness{Name: "claude-code"}})
	elapsed := time.Since(start)
	if elapsed >= localapi.SessionTimeout || elapsed < sessionBudget-time.Second {
		t.Errorf("the start answered after %v, want about %v and under the client's %v", elapsed, sessionBudget, localapi.SessionTimeout)
	}
	if w.Code != http.StatusServiceUnavailable || refusalOf(t, w) != busyStarting || len(logLines(t, logs)) != 1 {
		t.Errorf("answer = %d %q with log %s, want 503 with the busy sentence and one log line", w.Code, w.Body.String(), logs)
	}
}

// TestAPIEnrollStreams pins the enroll answer: each event on its own line,
// then the done event, an error after the first event as the error event,
// and an error before it as a Refusal with its status.
func TestAPIEnrollStreams(t *testing.T) {
	res := localapi.EnrollResult{ServerURL: "https://straza.example", Kind: localapi.KindPerson, Username: "kim", KeyThumbprint: "tp", Custody: localapi.CustodyAccount}
	rows := []struct {
		name   string
		enroll func(emit func(localapi.EnrollEvent)) (localapi.EnrollResult, error)
		status int
		events []string
	}{
		{"done", func(emit func(localapi.EnrollEvent)) (localapi.EnrollResult, error) {
			emit(localapi.EnrollEvent{Event: localapi.EnrollCode, UserCode: "ABCD", VerificationURI: "https://idp/x"})
			return res, nil
		}, http.StatusOK, []string{localapi.EnrollCode, localapi.EnrollDone}},
		{"refused after the code", func(emit func(localapi.EnrollEvent)) (localapi.EnrollResult, error) {
			emit(localapi.EnrollEvent{Event: localapi.EnrollCode, UserCode: "ABCD"})
			return localapi.EnrollResult{}, errors.New("the sign-in was refused")
		}, http.StatusOK, []string{localapi.EnrollCode, localapi.EnrollError}},
		{"refused before any event", func(func(localapi.EnrollEvent)) (localapi.EnrollResult, error) {
			return localapi.EnrollResult{}, &Refused{Status: http.StatusConflict, Sentence: "Another enrollment runs."}
		}, http.StatusConflict, nil},
	}
	for _, row := range rows {
		t.Run(row.name, func(t *testing.T) {
			a, logs := testAPI(&apiDouble{enroll: row.enroll}, false)
			w := serveAs(a, "1000", http.MethodPost, localapi.RouteEnroll, localapi.EnrollRequest{ServerURL: res.ServerURL})
			if w.Code != row.status {
				t.Fatalf("status = %d, want %d: %s", w.Code, row.status, w.Body.String())
			}
			if row.events == nil {
				if refusalOf(t, w) != "Another enrollment runs." {
					t.Errorf("refusal = %q", w.Body.String())
				}
				return
			}
			var got []string
			for line := range strings.Lines(w.Body.String()) {
				var ev localapi.EnrollEvent
				if err := json.Unmarshal([]byte(line), &ev); err != nil {
					t.Fatalf("line %q: %v", line, err)
				}
				got = append(got, ev.Event)
				if ev.Event == localapi.EnrollDone && (ev.Result == nil || *ev.Result != res) {
					t.Errorf("done event = %+v, want the result", ev)
				}
				if ev.Event == localapi.EnrollError && (ev.Error != "the sign-in was refused" || len(logLines(t, logs)) != 1) {
					t.Errorf("error event = %+v with log %s, want the sentence and one log line", ev, logs)
				}
			}
			if strings.Join(got, ",") != strings.Join(row.events, ",") {
				t.Errorf("events = %v, want %v", got, row.events)
			}
		})
	}
}

// TestAPIKeygen pins that keygen answers the public half the credential
// half made for the caller and passes its refusal through.
func TestAPIKeygen(t *testing.T) {
	f := &apiDouble{keygen: func(req localapi.KeygenRequest) (localapi.KeygenAnswer, error) {
		if req.Force {
			return localapi.KeygenAnswer{}, &Refused{Status: http.StatusConflict, Sentence: "A key exists."}
		}
		return localapi.KeygenAnswer{Username: req.Username, PublicKey: "cHVi"}, nil
	}}
	a, _ := testAPI(f, false)
	w := serveAs(a, "1000", http.MethodPost, localapi.RouteKeygen, localapi.KeygenRequest{Username: "bot"})
	var ans localapi.KeygenAnswer
	if err := json.Unmarshal(w.Body.Bytes(), &ans); err != nil || ans.PublicKey != "cHVi" || ans.Username != "bot" {
		t.Errorf("keygen = %d %q", w.Code, w.Body.String())
	}
	w = serveAs(a, "1000", http.MethodPost, localapi.RouteKeygen, localapi.KeygenRequest{Username: "bot", Force: true})
	if w.Code != http.StatusConflict || refusalOf(t, w) != "A key exists." {
		t.Errorf("forced keygen = %d %q, want the refusal", w.Code, w.Body.String())
	}
	if len(f.callers) != 2 || f.callers[0].UID != "1000" {
		t.Errorf("callers = %+v, want uid 1000 twice", f.callers)
	}
}

// TestAPIUserModeRefusesOtherUsers pins that a user-mode service serves
// only the user it runs as, whatever the socket's file modes allow, and
// logs each refusal once.
func TestAPIUserModeRefusesOtherUsers(t *testing.T) {
	f := &apiDouble{}
	a, logs := testAPI(f, true)
	for _, route := range []struct{ method, path string }{
		{http.MethodGet, localapi.RouteStatus},
		{http.MethodPost, localapi.RouteDecide},
		{http.MethodPost, localapi.RouteMCP},
	} {
		logs.Reset()
		w := serveAs(a, "1000", route.method, route.path, "{}")
		if w.Code != http.StatusForbidden || !strings.Contains(refusalOf(t, w), "runs in user mode for another user") {
			t.Errorf("%s as another user = %d %q, want the 403 sentence", route.path, w.Code, w.Body.String())
		}
		if lines := logLines(t, logs); len(lines) != 1 || lines[0]["uid"] != "1000" || lines[0]["route"] != route.path {
			t.Errorf("log = %v, want one line with uid 1000 and the route", lines)
		}
	}
	if w := serveAs(a, "0", http.MethodGet, localapi.RouteStatus, nil); w.Code != http.StatusOK {
		t.Errorf("status as the service's own user = %d %q", w.Code, w.Body.String())
	}
	if len(f.callers) != 1 || len(f.sessionCalls) != 0 {
		t.Errorf("the credential half answered %d times, want once, for the service's own user", len(f.callers)+len(f.sessionCalls))
	}
}
