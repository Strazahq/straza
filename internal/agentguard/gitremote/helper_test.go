package gitremote

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/strazahq/straza/internal/agentguard/localapi"
)

const testURL = "https://github.com/acme/pay-api.git"

// fakeDoor stands in for the service's git door. It records every request
// and answers from its fields.
type fakeDoor struct {
	mu          sync.Mutex
	listed      bool
	resolveCode int    // 0 answers 200
	resolveBody string // the body of a resolveCode answer
	grantLines  func(req localapi.GitGrantRequest) []localapi.GitGrantLine
	adverts     map[string]string // by git service
	answers     map[string]string // POST answers by git service
	posted      map[string][]string
	postStarted chan string
	grants      []localapi.GitGrantRequest
	headers     []http.Header
}

func newDoor() *fakeDoor {
	return &fakeDoor{
		listed:      true,
		adverts:     map[string]string{},
		answers:     map[string]string{},
		posted:      map[string][]string{},
		postStarted: make(chan string, 16),
		grantLines: func(localapi.GitGrantRequest) []localapi.GitGrantLine {
			return []localapi.GitGrantLine{{OK: true}}
		},
	}
}

func (d *fakeDoor) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	d.mu.Lock()
	d.headers = append(d.headers, r.Header.Clone())
	d.mu.Unlock()
	route := strings.TrimPrefix(r.URL.Path, localapi.RouteGit)
	switch {
	case r.Method == http.MethodPost && route == "resolve":
		if d.resolveCode != 0 {
			w.WriteHeader(d.resolveCode)
			_, _ = io.WriteString(w, d.resolveBody)
			return
		}
		_ = json.NewEncoder(w).Encode(localapi.GitResolveAnswer{Listed: d.listed})
	case r.Method == http.MethodPost && route == "grant":
		var req localapi.GitGrantRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		d.mu.Lock()
		d.grants = append(d.grants, req)
		d.mu.Unlock()
		for _, line := range d.grantLines(req) {
			_ = json.NewEncoder(w).Encode(line)
			w.(http.Flusher).Flush()
		}
	case r.Method == http.MethodGet && route == "info/refs":
		_, _ = io.WriteString(w, d.adverts[r.URL.Query().Get("service")])
	case r.Method == http.MethodPost && (route == uploadPack || route == receivePack):
		d.postStarted <- route
		body, _ := io.ReadAll(r.Body)
		d.mu.Lock()
		d.posted[route] = append(d.posted[route], string(body))
		d.mu.Unlock()
		_, _ = io.WriteString(w, d.answers[route])
	default:
		http.Error(w, "no such route", http.StatusTeapot)
	}
}

type runResult struct {
	code     int
	handOff  bool
	out, err string
}

// runWith runs one helper against door with env and stdin.
func runWith(t *testing.T, door http.Handler, env map[string]string, stdin io.Reader) runResult {
	t.Helper()
	return prepare(t, door, env, stdin)()
}

// prepare builds a helper against door and returns the call that runs it,
// which may run on another goroutine.
func prepare(t *testing.T, door http.Handler, env map[string]string, stdin io.Reader) func() runResult {
	t.Helper()
	srv := httptest.NewServer(door)
	t.Cleanup(srv.Close)
	var out, errw bytes.Buffer
	h, err := newHelper([]string{"origin", testURL}, env, stdin, &out, &errw)
	if err != nil {
		t.Fatal(err)
	}
	h.base, h.http = srv.URL, srv.Client()
	return func() runResult {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		code, handOff := h.run(ctx)
		return runResult{code, handOff, out.String(), errw.String()}
	}
}

// failReader fails the test when the helper reads stdin.
type failReader struct{ t *testing.T }

func (f failReader) Read([]byte) (int, error) {
	f.t.Error("the helper read stdin before it knew whether Straza serves the repo")
	return 0, io.EOF
}

var v2Advert = pl("version 2\n") + pl("agent=git/2.47.3\n") + pl("ls-refs=unborn\n") + pl("fetch=shallow wait-for-done filter\n") + "0000"

func TestHelperHandsOffAnUnlistedRepo(t *testing.T) {
	door := newDoor()
	door.listed = false
	got := runWith(t, door, nil, failReader{t})
	if !got.handOff || got.code != 0 || got.out != "" || got.err != "" {
		t.Fatalf("got %+v, want a hand-off with nothing written", got)
	}
	if len(door.grants) != 0 {
		t.Fatalf("an unlisted repo took a grant: %+v", door.grants)
	}
}

func TestHelperNamesItsWindowOnEveryRequest(t *testing.T) {
	door := newDoor()
	door.adverts[uploadPack] = "001e# service=git-upload-pack\n0000" + v2Advert
	env := map[string]string{"CLAUDECODE": "1", "CLAUDE_CODE_SESSION_ID": "ses-123"}
	got := runWith(t, door, env, strings.NewReader("capabilities\nconnect git-upload-pack\n0000"))
	if got.code != 0 {
		t.Fatalf("exit %d: %s", got.code, got.err)
	}
	if len(door.headers) != 3 {
		t.Fatalf("%d requests, want resolve, grant and the advertisement", len(door.headers))
	}
	op := door.headers[0].Get(localapi.HeaderGitOp)
	if !regexp.MustCompile(`^[0-9a-f]{32}$`).MatchString(op) {
		t.Fatalf("op id %q is not 32 hex characters", op)
	}
	for i, hd := range door.headers {
		if hd.Get(localapi.HeaderHarness) != "claude-code" || hd.Get(localapi.HeaderHarnessVersion) != harnessVersion ||
			hd.Get(localapi.HeaderHarnessSession) != "ses-123" || hd.Get(localapi.HeaderGitURL) != testURL ||
			hd.Get(localapi.HeaderGitOp) != op {
			t.Fatalf("request %d headers %v", i, hd)
		}
	}
}

func TestWindowOf(t *testing.T) {
	cases := []struct {
		name string
		env  map[string]string
		want localapi.Harness
	}{
		{"claude code", map[string]string{"CLAUDECODE": "1", "CLAUDE_CODE_SESSION_ID": "c1"}, localapi.Harness{Name: "claude-code", Version: "git", Session: "c1"}},
		{"codex thread without markers", map[string]string{"CODEX_THREAD_ID": "t1"}, localapi.Harness{Name: "codex", Version: "git", Session: "t1"}},
		{"codex with its marker", map[string]string{"CODEX_SANDBOX": "seatbelt", "CODEX_THREAD_ID": "t1"}, localapi.Harness{Name: "codex", Version: "git", Session: "t1"}},
		{"claude code session wins", map[string]string{"CLAUDE_CODE_SESSION_ID": "c1", "CODEX_THREAD_ID": "t1"}, localapi.Harness{Name: "claude-code", Version: "git", Session: "c1"}},
		{"override", map[string]string{"STRAZA_HARNESS": "gemini"}, localapi.Harness{Name: "gemini", Version: "git"}},
		{"nothing", nil, localapi.Harness{Name: "claude-code", Version: "git"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := windowOf(tc.env); got != tc.want {
				t.Fatalf("got %+v, want %+v", got, tc.want)
			}
		})
	}
}

func TestHelperFetchRelaysProtocolV2(t *testing.T) {
	door := newDoor()
	door.adverts[uploadPack] = "001e# service=git-upload-pack\n0000" + v2Advert
	door.answers[uploadPack] = "ANSWER\n"
	door.grantLines = func(localapi.GitGrantRequest) []localapi.GitGrantLine {
		return []localapi.GitGrantLine{{Wait: "Straza: waiting for an approver to allow fetch on acme/pay-api (55s left)."}, {OK: true}}
	}
	lsRefs := pl("command=ls-refs\n") + "0001" + pl("peel\n") + "0000"
	fetch := pl("command=fetch\n") + "0001" + pl("want "+oidA+"\n") + pl("done\n") + "0000"
	in := "capabilities\noption progress true\nconnect git-upload-pack\n" + lsRefs + fetch + "0000"
	got := runWith(t, door, nil, strings.NewReader(in))
	if got.code != 0 || got.handOff {
		t.Fatalf("got %+v", got)
	}
	want := "connect\n\nunsupported\n" + "\n" + v2Advert + "ANSWER\n" + "ANSWER\n"
	if got.out != want {
		t.Fatalf("stdout %q\nwant %q", got.out, want)
	}
	if posted := door.posted[uploadPack]; len(posted) != 2 || posted[0] != lsRefs || posted[1] != fetch {
		t.Fatalf("posted %q", posted)
	}
	if len(door.grants) != 1 || door.grants[0].Op != "fetch" || door.grants[0].Refs != nil {
		t.Fatalf("grants %+v, want one fetch grant", door.grants)
	}
	if !strings.Contains(got.err, "waiting for an approver") {
		t.Fatalf("the wait line did not reach stderr: %q", got.err)
	}
	for _, hd := range door.headers[2:] {
		if hd.Get("Git-Protocol") != "version=2" {
			t.Fatalf("an upload-pack request without Git-Protocol: %v", hd)
		}
	}
}

func TestHelperFetchRefusals(t *testing.T) {
	cases := []struct {
		name   string
		advert string
		grant  []localapi.GitGrantLine
		want   string
	}{
		{"grant refused", v2Advert, []localapi.GitGrantLine{{Error: "Straza refused this fetch: no role allows git_fetch."}},
			"Straza refused this fetch: no role allows git_fetch."},
		{"grant answer ended", v2Advert, []localapi.GitGrantLine{{Wait: "Straza: waiting."}}, sentGrantEnded},
		{"protocol v0 advertisement", "001e# service=git-upload-pack\n0000" + pl(oidA+" HEAD\x00multi_ack\n") + "0000", nil,
			"did not answer " + testURL + " in git protocol version 2"},
		{"empty advertisement", "", nil, "the advertisement is empty"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			door := newDoor()
			door.adverts[uploadPack] = tc.advert
			if tc.grant != nil {
				door.grantLines = func(localapi.GitGrantRequest) []localapi.GitGrantLine { return tc.grant }
			}
			got := runWith(t, door, nil, strings.NewReader("capabilities\nconnect git-upload-pack\n"))
			if got.code != 1 || !strings.Contains(got.err, tc.want) || got.out != "connect\n\n" {
				t.Fatalf("got %+v, want exit 1 with %q and no connection", got, tc.want)
			}
		})
	}
}

func TestHelperServiceAnswers(t *testing.T) {
	cases := []struct {
		name string
		code int
		body string
		want string
	}{
		{"older service", http.StatusNotFound, `{"error":"The Straza background service has no route /local/v1/git/resolve. Update Straza so the client and the service are the same version."}`, sentNoRoute},
		{"service sentence", http.StatusConflict, `{"error":"Straza: no session for this window."}`, "Straza: no session for this window."},
		{"text body", http.StatusBadGateway, "upstream down\n", "upstream down"},
		{"no reason", http.StatusInternalServerError, "", "answered HTTP 500 and gave no reason"},
		{"unreadable answer", http.StatusOK, "not json", "gave an answer to the resolve step"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			door := newDoor()
			door.resolveCode, door.resolveBody = tc.code, tc.body
			got := runWith(t, door, nil, failReader{t})
			if got.code != 1 || got.handOff || !strings.Contains(got.err, tc.want) {
				t.Fatalf("got %+v, want %q", got, tc.want)
			}
		})
	}
}

func TestHelperUnreachableService(t *testing.T) {
	var out, errw bytes.Buffer
	h, err := newHelper([]string{"origin", testURL}, nil, failReader{t}, &out, &errw)
	if err != nil {
		t.Fatal(err)
	}
	h.base, h.http = localapi.BaseURL, localapi.NewClient(t.TempDir()).HTTPClient()
	if code, handOff := h.run(context.Background()); code != 1 || handOff || strings.TrimSpace(errw.String()) != sentUnreachable {
		t.Fatalf("exit %d, hand-off %v, stderr %q", code, handOff, errw.String())
	}
}

func TestHelperArgumentsAndCommands(t *testing.T) {
	var out, errw bytes.Buffer
	if _, err := newHelper([]string{"origin"}, nil, failReader{t}, &out, &errw); err == nil || !strings.Contains(err.Error(), "and git gave it 1,") {
		t.Fatalf("one argument: %v", err)
	}
	cases := []struct{ name, in, wantOut, wantErr string }{
		{"archive refused", "capabilities\nconnect git-upload-archive\n", "connect\n\n", sentArchive},
		{"unknown service", "connect git-frob\n", "", "connect to git-frob"},
		{"unknown command", "fetch " + oidA + " HEAD\n", "", "which it does not support"},
		{"empty line ends", "capabilities\n\n", "connect\n\n", ""},
		{"no input", "", "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := runWith(t, newDoor(), nil, strings.NewReader(tc.in))
			wantCode := 0
			if tc.wantErr != "" {
				wantCode = 1
			}
			if got.code != wantCode || got.out != tc.wantOut || !strings.Contains(got.err, tc.wantErr) {
				t.Fatalf("got %+v", got)
			}
		})
	}
}

func TestInvoked(t *testing.T) {
	cases := map[string]bool{
		"git-remote-straza":                      true,
		"/usr/bin/git-remote-straza":             true,
		"git-remote-straza.exe":                  true,
		"GIT-REMOTE-STRAZA.EXE":                  true,
		"/usr/libexec/git-core/git-remote-https": false,
		"straza":                                 false,
		"/usr/bin/straza":                        false,
		"git-remote-straza-old":                  false,
	}
	for argv0, want := range cases {
		if got := Invoked(argv0); got != want {
			t.Errorf("Invoked(%q) = %v, want %v", argv0, got, want)
		}
	}
}
