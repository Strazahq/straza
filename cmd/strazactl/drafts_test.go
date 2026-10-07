package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/strazahq/straza/internal/ctl"
)

// Fixture answers of the drafts routes, in the shapes strazad answers.
const (
	fixRefused = `{"key":"k-pool","code":"policy.pool","class":"refused","object":"PolicySet/github-writers-access",` +
		`"sentence":"Rule github-pr-hold of github-writers-access names release-approvers, which is not a role in Straza.",` +
		`"fix":"Create it first, or fix the name."}`
	fixTyped = `{"key":"k-host","code":"server.credential-host","class":"risk","ack":"typed","object":"App/github",` +
		`"sentence":"Straza will send every caller's own token to api.githubcopilot.com, a host github has not used before.",` +
		`"before":"","after":"token api.githubcopilot.com","typed":"api.githubcopilot.com"}`
	fixTick = `{"key":"k-impl","code":"access.implication","class":"risk","ack":"tick","object":"Role/developer",` +
		`"sentence":"Holders of developer will also hold github-readers and reach what it reaches.","after":"github-readers"}`
	fixCutTyped = `{"key":"k-cut","code":"server.runs-code","class":"risk","ack":"typed",` +
		`"sentence":"This line names a tool on a server you cannot read, so this view leaves its words out. Ask an administrator for the scope apps:read to see it."}`
	fixWarning = `{"key":"k-conn","code":"ready.nobody-connected","class":"warning","object":"App/github",` +
		`"sentence":"github runs on each caller's own token, and nobody who holds its roles has connected yet.",` +
		`"fix":"Each person runs straza connect github after publishing."}`
	fixUnchecked = `{"key":"k-tools","code":"unchecked.tools","class":"unchecked","object":"App/github",` +
		`"sentence":"Straza has not contacted api.githubcopilot.com, because an address in a draft is contacted only when a person asks, so the tool names of github are unknown."}`
	fixInfo = `{"key":"k-nobody","code":"info.nobody-holds","class":"info","object":"Role/github-readers",` +
		`"sentence":"Nobody holds github-readers yet, so it reaches nothing until someone is assigned it."}`
	fixPassed = `{"key":"k-secrets","code":"passed.secrets","class":"passed","sentence":"No document holds a secret or the shape of one."}`
	fixGain   = `{"role":"developer","server":"github","tool":"list_issues","holders_count":3,"before":"not-reachable",` +
		`"after":"needs-approval","after_words":"a hold, up to 2 minutes, decided by sec-approvers"}`
	fixNeed  = `{"object":"App/github","standing":"the scope apps:write or the role straza-global-mcp-admin"}`
	fixItems = `[{"kind":"App","name":"github","op":"put","doc":"kind: App\nmetadata:\n  name: github\n","existed":false},` +
		`{"kind":"Role","name":"developer","op":"put","doc":"kind: Role\nmetadata:\n  name: developer\nspec:\n  implies: [github-readers]\n","existed":true},` +
		`{"kind":"PolicySet","name":"old-set","op":"off","doc":"kind: PolicySet\n","base":"fp-old","existed":true},` +
		`{"kind":"Role","name":"legacy","op":"remove","base":"fp-legacy","existed":true}]`
)

// fixVerdict is a verdict holding the refused and risk lines given, one line
// of every other class, a gain row and a need.
func fixVerdict(refused, risks string) string {
	return `{"draft":"41","revision":2,"snapshot":"3be0a1","checked_at":"2026-09-24T10:20:11Z",` +
		`"refused":[` + refused + `],"risks":[` + risks + `],"warnings":[` + fixWarning + `],` +
		`"unchecked":[` + fixUnchecked + `],"passed":[` + fixPassed + `],"info":[` + fixInfo + `],` +
		`"gains":[` + fixGain + `],"needs":[` + fixNeed + `],"risk_digest":"rd-1"}`
}

// fixDraft is draft id in state and door, at revision rev, holding items.
func fixDraft(id string, rev int, state, door, items string) string {
	b, _ := json.Marshal(rev)
	return `{"id":"` + id + `","revision":` + string(b) + `,"state":"` + state + `","door":"` + door + `",` +
		`"note":"The platform team asked for read access to GitHub.",` +
		`"authors":[{"user_id":"u9","username":"joe-java-developer-agent","agent":true,"via":"session",` +
		`"client":"claude-code","sponsor_id":"u1","sponsor":"alice"}],"items":` + items + `,` +
		`"title":"Add server github and then change role developer",` +
		`"created_at":"2026-09-24T10:14:03Z","updated_at":"2026-09-24T10:20:11Z"}`
}

// fixAnswer is what create, update and revert answer.
func fixAnswer(id string, rev int, refused, risks string) string {
	return `{"draft":` + fixDraft(id, rev, "open", "strazactl", fixItems) + `,"verdict":` + fixVerdict(refused, risks) + `}`
}

// fixDetail is what GET /v1/admin/drafts/41 answers for an open draft.
func fixDetail(state, refused, risks string, mayPublish bool, refusal string) string {
	items := `[{"kind":"App","name":"github","op":"put","doc":"kind: App\nmetadata:\n  name: github\n","existed":false},` +
		`{"kind":"Role","name":"developer","op":"put","doc":"kind: Role\nmetadata:\n  name: developer\nspec:\n  implies: [github-readers]\n","existed":true}]`
	may, _ := json.Marshal(mayPublish)
	detail := `{"draft":` + fixDraft("41", 2, state, "straza-app", items) + `,"verdict":` + fixVerdict(refused, risks) + `,` +
		`"revisions":[{"revision":1,"author":{"user_id":"u9","username":"joe-java-developer-agent","agent":true,"via":"session","client":"claude-code","sponsor":"alice"},"door":"straza-app","digest":"d1","created_at":"2026-09-24T10:14:03Z"}],` +
		`"live":{"App/github":{"op":"remove","doc":""},"Role/developer":{"op":"put","doc":"kind: Role\nmetadata:\n  name: developer\nspec:\n  implies: []\n"}},` +
		`"may_publish":` + string(may)
	if refusal != "" {
		b, _ := json.Marshal(refusal)
		detail += `,"publish_refusal":` + string(b)
	}
	return detail + `}`
}

// draftReply is one scripted answer: a status, 200 when zero, and a body.
type draftReply struct {
	code int
	body string
}

// draftRequest is one request the drafts server saw.
type draftRequest struct {
	method, uri, auth, body string
}

// draftsServer answers the drafts routes from scripted replies, keyed by
// method and path, and records every request, the check-in included, so a
// refusal can be held to sending nothing at all.
type draftsServer struct {
	mu   sync.Mutex
	seen []draftRequest
}

// newDraftsServer serves replies as ServeMux patterns, as strazad does, so
// a path it has no reply for answers 404 and a method it has none for on a
// path it serves answers 405, each without a sentence.
func newDraftsServer(t *testing.T, replies map[string]draftReply) (*draftsServer, *httptest.Server) {
	t.Helper()
	s := &draftsServer{}
	mux := http.NewServeMux()
	for route, reply := range replies {
		mux.HandleFunc(route, func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			if reply.code != 0 {
				w.WriteHeader(reply.code)
			}
			_, _ = w.Write([]byte(reply.body + "\n"))
		})
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		s.mu.Lock()
		s.seen = append(s.seen, draftRequest{r.Method, r.URL.RequestURI(), r.Header.Get("Authorization"), string(body)})
		s.mu.Unlock()
		if r.URL.Path == "/v1/checkin" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"session_id":"ses-1","session_token":"stok-2"}`))
			return
		}
		mux.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)
	return s, srv
}

// requests is every request the server saw, the check-in included.
func (s *draftsServer) requests() []draftRequest {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]draftRequest(nil), s.seen...)
}

// calls is every request but the check-in: the drafts routes a verb called.
func (s *draftsServer) calls() []draftRequest {
	var out []draftRequest
	for _, r := range s.requests() {
		if r.uri != "/v1/checkin" {
			out = append(out, r)
		}
	}
	return out
}

// runDrafts drives the real cobra tree with stdin and answers stdout, stderr
// and the exit status main gives the run.
func runDrafts(t *testing.T, creds, stdin string, args ...string) (stdout, stderr string, code int, err error) {
	t.Helper()
	var out, errBuf bytes.Buffer
	root := newRootCmd(creds)
	root.SetOut(&out)
	root.SetErr(&errBuf)
	root.SetIn(strings.NewReader(stdin))
	root.SetArgs(args)
	cmd, err := root.ExecuteC()
	return out.String(), errBuf.String(), exitCode(cmd, err), err
}

// wantInOrder fails the test unless every want appears in got, each after
// the one before.
func wantInOrder(t *testing.T, got string, wants ...string) {
	t.Helper()
	rest := got
	for _, w := range wants {
		i := strings.Index(rest, w)
		if i < 0 {
			t.Errorf("output lacks %q after the lines before it:\n%s", w, got)
			return
		}
		rest = rest[i+len(w):]
	}
}

// errText is err's message, or the empty string for no error.
func errText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// assertCalls fails the test unless the server saw n drafts calls, and for
// none nothing at all, the renewal of the login included.
func assertCalls(t *testing.T, s *draftsServer, n int) {
	t.Helper()
	if n == 0 {
		if got := s.requests(); len(got) != 0 {
			t.Errorf("requests = %+v, want nothing sent", got)
		}
		return
	}
	if got := s.calls(); len(got) != n {
		t.Errorf("requests = %+v, want %d", got, n)
	}
}

// writeFile writes body to rel under dir and answers its path.
func writeFile(t *testing.T, dir, rel, body string) string {
	t.Helper()
	path := filepath.Join(dir, rel)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// TestReadBundle pins how -f reads: a file whole, a directory's top-level
// .yaml and .yml files by name, and - as stdin, once, each text named by the
// file it came from, stdin for -.
func TestReadBundle(t *testing.T) {
	dir := t.TempDir()
	one := writeFile(t, dir, "one.yaml", "kind: App\n---\nkind: Role\n")
	writeFile(t, dir, "bundle/b.yml", "B")
	writeFile(t, dir, "bundle/a.yaml", "A")
	writeFile(t, dir, "bundle/notes.txt", "N")
	writeFile(t, dir, "bundle/sub/c.yaml", "C")
	writeFile(t, dir, "empty/readme.md", "R")
	writeFile(t, dir, "dotted/a.yaml", "A")
	writeFile(t, dir, "dotted/.hidden.yaml", "H")
	if err := os.Symlink(filepath.Join(dir, "gone.yaml"), filepath.Join(dir, "dotted", ".#a.yaml")); err != nil {
		t.Fatal(err)
	}
	bundle, empty, missing := filepath.Join(dir, "bundle"), filepath.Join(dir, "empty"), filepath.Join(dir, "missing.yaml")
	tests := []struct {
		name      string
		paths     []string
		stdin     string
		want      []string
		wantNames []string
		wantErr   string
	}{
		{name: "a file is one text, every document in it", paths: []string{one}, want: []string{"kind: App\n---\nkind: Role\n"}, wantNames: []string{one}},
		{name: "a directory gives its yaml files by name", paths: []string{bundle}, want: []string{"A", "B"},
			wantNames: []string{filepath.Join(bundle, "a.yaml"), filepath.Join(bundle, "b.yml")}},
		{name: "a directory skips names that start with a dot, as a shell glob does", paths: []string{filepath.Join(dir, "dotted")}, want: []string{"A"},
			wantNames: []string{filepath.Join(dir, "dotted", "a.yaml")}},
		{name: "- reads stdin in its place", paths: []string{"-", one}, stdin: "S", want: []string{"S", "kind: App\n---\nkind: Role\n"}, wantNames: []string{"stdin", one}},
		{name: "- twice refuses", paths: []string{"-", "-"}, stdin: "S", wantErr: "-f - reads stdin, which can be read once, and it is given twice. Pass -f - once"},
		{name: "a missing path", paths: []string{missing}, wantErr: "cannot read " + missing + ": no such file or directory"},
		{name: "a directory with no yaml file", paths: []string{empty}, wantErr: empty + " holds no .yaml or .yml file"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, names, err := readBundle(tc.paths, strings.NewReader(tc.stdin))
			if errText(err) != tc.wantErr {
				t.Fatalf("err = %v, want %q", err, tc.wantErr)
			}
			if strings.Join(got, "|") != strings.Join(tc.want, "|") {
				t.Errorf("texts = %q, want %q", got, tc.want)
			}
			if strings.Join(names, "|") != strings.Join(tc.wantNames, "|") {
				t.Errorf("names = %q, want %q", names, tc.wantNames)
			}
		})
	}
}

// TestItemMark pins the item marks by the item's op and whether its object
// existed at the draft's last check. base is left out for a reader who may
// not read the object, so a put of an object that existed reads ~ with no
// base.
func TestItemMark(t *testing.T) {
	tests := []struct {
		name string
		item ctl.DraftItem
		want string
	}{
		{"a put of an object that did not exist", ctl.DraftItem{Op: "put"}, "+"},
		{"a put of an object that existed, its base withheld", ctl.DraftItem{Op: "put", Existed: true}, "~"},
		{"a set turned off", ctl.DraftItem{Op: "off", Existed: true}, "off"},
		{"a removal", ctl.DraftItem{Op: "remove", Existed: true}, "-"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := itemMark(tc.item); got != tc.want {
				t.Errorf("itemMark = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestDraftsCheck pins check: the request, the item lines, the verdict lines
// with the info lines, and the last line and exit status by whether the
// documents can be published.
func TestDraftsCheck(t *testing.T) {
	file := writeFile(t, t.TempDir(), "github.yaml", "kind: App\n")
	checkAnswer := func(refused string) string {
		return `{"items":` + fixItems + `,"verdict":` + fixVerdict(refused, fixTyped+","+fixTick) + `}`
	}
	lines := "  +   App/github\n" +
		"  ~   Role/developer\n" +
		"  off PolicySet/old-set\n" +
		"  -   Role/legacy\n" +
		"Checked against live state at 2026-09-24 10:20 UTC.\n"
	verdictLines := "  widens    App/github Straza will send every caller's own token to api.githubcopilot.com, a host github has not used before. Type api.githubcopilot.com to publish.\n" +
		"            after:  token api.githubcopilot.com\n" +
		"  widens    Role/developer Holders of developer will also hold github-readers and reach what it reaches.\n" +
		"            after:  github-readers\n" +
		"  warning   App/github github runs on each caller's own token, and nobody who holds its roles has connected yet. Each person runs straza connect github after publishing.\n" +
		"  unchecked App/github Straza has not contacted api.githubcopilot.com, because an address in a draft is contacted only when a person asks, so the tool names of github are unknown.\n" +
		"  info      Role/github-readers Nobody holds github-readers yet, so it reaches nothing until someone is assigned it.\n"
	tests := []struct {
		name       string
		refused    string
		wantStdout string
		wantCode   int
	}{
		{
			name:       "documents that can be published",
			wantStdout: lines + verdictLines + "Nothing was stored. The documents can be published: strazactl drafts create -f " + file + " -f " + file + "\n",
		},
		{
			name:    "documents the check refuses",
			refused: fixRefused,
			wantStdout: lines + "  refused   PolicySet/github-writers-access Rule github-pr-hold of github-writers-access names release-approvers, which is not a role in Straza. Create it first, or fix the name.\n" +
				verdictLines + "Nothing was stored. The documents cannot be published as they are: fix the refused lines.\n",
			wantCode: 1,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s, srv := newDraftsServer(t, map[string]draftReply{"POST /v1/admin/drafts/check": {body: checkAnswer(tc.refused)}})
			t.Setenv("STRAZA_SERVER", "")
			stdout, _, code, err := runDrafts(t, writeCreds(t, srv.URL), "", "drafts", "check", "-f", file, "-f", file, "--note", "why")
			if stdout != tc.wantStdout || code != tc.wantCode {
				t.Errorf("exit %d (%v), stdout:\n%s\nwant exit %d, stdout:\n%s", code, err, stdout, tc.wantCode, tc.wantStdout)
			}
			if tc.wantCode != 0 && errText(err) != "" {
				t.Errorf("err = %q, want no second sentence", err)
			}
			want := `{"documents":["kind: App\n","kind: App\n"],"note":"why"}`
			if got := s.calls(); len(got) != 1 || got[0].body != want {
				t.Errorf("requests = %+v, want one body %s", got, want)
			}
		})
	}
}

// TestDraftsCreate pins create: the created line, the item, verdict and
// info lines, the hint for the next command, and a refusal at intake, which
// ends with the command to run again and says nothing more on stderr.
func TestDraftsCreate(t *testing.T) {
	file := writeFile(t, t.TempDir(), "github.yaml", "kind: App\n")
	head := "Created draft 41 with 4 documents. Nothing changes until a person publishes it.\n" +
		"  +   App/github\n  ~   Role/developer\n  off PolicySet/old-set\n  -   Role/legacy\n"
	tests := []struct {
		name     string
		reply    draftReply
		want     []string
		wantErr  string
		wantCode int
	}{
		{
			name:  "a draft that can be published",
			reply: draftReply{code: http.StatusCreated, body: fixAnswer("41", 1, "", fixTick)},
			want: []string{head, "  widens    Role/developer Holders", "  info      Role/github-readers Nobody holds",
				"\nThe draft can be published: strazactl drafts publish 41\n"},
		},
		{
			name:     "a draft the check refuses is stored, and exits 1",
			reply:    draftReply{code: http.StatusCreated, body: fixAnswer("41", 1, fixRefused, "")},
			want:     []string{head, "  refused   PolicySet/github-writers-access Rule github-pr-hold", "\nThe draft cannot be published yet. Fix it and send it again: strazactl drafts update 41 -f " + file + "\n"},
			wantCode: 1,
		},
		{
			name: "a refusal at intake stores nothing, and exits 1",
			reply: draftReply{code: http.StatusUnprocessableEntity, body: `{"error":"The draft names App/github twice. Keep one document for each object and send the draft again.",` +
				`"findings":[{"code":"bundle.duplicate","class":"refused","object":"App/github","sentence":"The draft names App/github twice.","fix":"Keep one document for each object and send the draft again."}]}`},
			want: []string{"  refused   App/github The draft names App/github twice. Keep one document for each object and send the draft again.\n" +
				"Nothing was stored. Fix the files and run strazactl drafts create -f " + file + " again.\n"},
			wantCode: 1,
		},
		{
			name:  "a proxy's 504 may have stored the draft",
			reply: draftReply{code: http.StatusGatewayTimeout, body: "<html>504 Gateway Time-out</html>"},
			wantErr: "strazad or a proxy in front of it answered HTTP 504 without a sentence, so the draft may have been stored. " +
				"List your drafts with strazactl drafts list --mine before you send it again",
			wantCode: 2,
		},
		{
			name: "a refusal of the caller prints nothing and exits 2",
			reply: draftReply{code: http.StatusForbidden, body: `{"error":"Drafting the role probe-reach needs the scope identity:read, because a draft shows the live document of every role it names. ` +
				`Ask an administrator for that grant, or leave the role probe-reach out of the draft."}`},
			wantErr: "Drafting the role probe-reach needs the scope identity:read, because a draft shows the live document of every role it names. " +
				"Ask an administrator for that grant, or leave the role probe-reach out of the draft.",
			wantCode: 2,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s, srv := newDraftsServer(t, map[string]draftReply{"POST /v1/admin/drafts": tc.reply})
			t.Setenv("STRAZA_SERVER", "")
			stdout, _, code, err := runDrafts(t, writeCreds(t, srv.URL), "", "drafts", "create", "-f", file)
			if code != tc.wantCode || errText(err) != tc.wantErr {
				t.Errorf("exit %d, err %v, want exit %d, err %q", code, err, tc.wantCode, tc.wantErr)
			}
			wantInOrder(t, stdout, tc.want...)
			if tc.want == nil && stdout != "" {
				t.Errorf("stdout = %q, want nothing", stdout)
			}
			if got := s.calls(); len(got) != 1 || got[0].body != `{"documents":["kind: App\n"]}` {
				t.Errorf("requests = %+v, want one create without a note", got)
			}
		})
	}
}

// TestDraftsUpdate pins update: the revision the GET read goes into the PUT,
// the note goes only when given, the new revision prints its item lines as
// create does, and a draft that moved exits 1.
func TestDraftsUpdate(t *testing.T) {
	file := writeFile(t, t.TempDir(), "github.yaml", "kind: App\n")
	tests := []struct {
		name     string
		args     []string
		put      draftReply
		wantBody string
		want     []string
		wantErr  string
		wantCode int
	}{
		{
			name:     "a new revision on the one read",
			args:     []string{"--note", ""},
			put:      draftReply{body: fixAnswer("41", 3, "", fixTick)},
			wantBody: `{"revision":2,"documents":["kind: App\n"],"note":""}`,
			want: []string{"Draft 41 is at revision 3. Nothing changes until a person publishes it.\n" +
				"  +   App/github\n  ~   Role/developer\n  off PolicySet/old-set\n  -   Role/legacy\n  widens    Role/developer",
				"\nThe draft can be published: strazactl drafts publish 41\n"},
		},
		{
			name:     "no --note keeps the note",
			put:      draftReply{body: fixAnswer("41", 3, fixRefused, "")},
			wantBody: `{"revision":2,"documents":["kind: App\n"]}`,
			want:     []string{"  refused   PolicySet/github-writers-access", "\nThe draft cannot be published yet. Fix it and send it again: strazactl drafts update 41 -f " + file + "\n"},
			wantCode: 1,
		},
		{
			name: "a draft that moved after the read",
			put: draftReply{code: http.StatusConflict, body: `{"error":"Draft 41 changed after you read it: it is at revision 4, and you sent revision 2. ` +
				`Read it again and make your change on top of revision 4."}`},
			wantBody: `{"revision":2,"documents":["kind: App\n"]}`,
			wantErr:  "Draft 41 changed after you read it: it is at revision 4, and you sent revision 2. Read it again and make your change on top of revision 4.",
			wantCode: 1,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s, srv := newDraftsServer(t, map[string]draftReply{
				"GET /v1/admin/drafts/41": {body: fixDetail("open", "", fixTick, true, "")},
				"PUT /v1/admin/drafts/41": tc.put,
			})
			t.Setenv("STRAZA_SERVER", "")
			args := append([]string{"drafts", "update", "41", "-f", file}, tc.args...)
			stdout, _, code, err := runDrafts(t, writeCreds(t, srv.URL), "", args...)
			if code != tc.wantCode || errText(err) != tc.wantErr {
				t.Errorf("exit %d, err %v, want exit %d, err %q", code, err, tc.wantCode, tc.wantErr)
			}
			wantInOrder(t, stdout, tc.want...)
			got := s.calls()
			if len(got) != 2 || got[0].method+" "+got[0].uri != "GET /v1/admin/drafts/41" || got[1].body != tc.wantBody {
				t.Errorf("requests = %+v, want the GET, then a PUT of %s", got, tc.wantBody)
			}
		})
	}
}

// TestDraftsList pins list: the query, the header on stderr, the table with
// its CHECKS words, when those counts were taken and the door in words, the
// stderr line that says the counts are from the last check, the line about
// a longer list, and no exit 1.
func TestDraftsList(t *testing.T) {
	const counts = "Counts are from each draft's last check. strazactl drafts show checks it against live state now.\n"
	page := `{"items":[` +
		`{"id":"41","title":"Add server github","state":"open","door":"straza-app","revision":2,` +
		`"proposer":{"user_id":"u9","username":"joe-java-developer-agent","agent":true,"via":"session","client":"claude-code"},` +
		`"items":[{"kind":"App","name":"github","op":"put"}],` +
		`"checks":{"refused":1,"risks":3,"warnings":2,"unchecked":0,"revision":2,"checked_at":"2026-09-24T10:20:11Z"},` +
		`"created_at":"2026-09-24T10:14:03Z","updated_at":"2026-09-24T10:20:11Z"},` +
		`{"id":"40","title":"Change role developer","state":"open","door":"strazactl","revision":1,` +
		`"proposer":{"user_id":"u1","username":"alice","agent":false,"via":"session","client":"strazactl"},` +
		`"items":[{"kind":"Role","name":"developer","op":"put"}],` +
		`"checks":{"refused":0,"risks":0,"warnings":0,"unchecked":0,"revision":1,"checked_at":"2026-09-24T09:00:00Z"},` +
		`"created_at":"2026-09-24T09:00:00Z","updated_at":"2026-09-24T09:00:00Z"},` +
		`{"id":"39","title":"Add server jira","state":"open","door":"straza-app","revision":3,` +
		`"proposer":{"user_id":"u9","username":"joe-java-developer-agent","agent":true,"via":"session","client":"claude-code"},` +
		`"items":[{"kind":"App","name":"jira","op":"put"}],` +
		`"checks":{"refused":0,"risks":1,"warnings":0,"unchecked":1,"revision":2,"checked_at":"2026-09-24T08:00:00Z"},` +
		`"created_at":"2026-09-24T08:00:00Z","updated_at":"2026-09-24T08:30:00Z"}` +
		`],"next_cursor":%q}`
	tests := []struct {
		name       string
		args       []string
		cursor     string
		reply      draftReply
		wantURI    string
		wantStderr string
		wantRows   map[string][]string
		wantErr    string
		wantCode   int
	}{
		{
			name:       "the open drafts by default",
			wantURI:    "/v1/admin/drafts?limit=200&state=open",
			wantStderr: "Open drafts, newest first.\n" + counts,
			wantRows: map[string][]string{
				"ID": {"ID", "STATE", "TITLE", "PROPOSER", "DOOR", "CHECKS", "CHECKED", "UPDATED"},
				"41": {"41", "open", "Add server github", "joe-java-developer-agent", "straza app", "1 refused, 3 widen, 2 warn", "2026-09-24 10:20 UTC", "2026-09-24 10:20 UTC"},
				"40": {"40", "Change role developer", "alice", "strazactl", "no findings", "2026-09-24 09:00 UTC", "2026-09-24 09:00 UTC"},
				"39": {"39", "Add server jira", "straza app", " - ", " - ", "2026-09-24 08:30 UTC"},
			},
		},
		{
			name:       "every state of mine, and more than one page",
			args:       []string{"--state", "all", "--mine"},
			cursor:     "c2",
			wantURI:    "/v1/admin/drafts?limit=200&mine=true&state=all",
			wantStderr: "All your drafts, newest first.\n" + counts + "More drafts exist than these 200. Narrow the list with --state or --mine.\n",
		},
		{
			name:       "the published ones",
			args:       []string{"--state", "published"},
			wantURI:    "/v1/admin/drafts?limit=200&state=published",
			wantStderr: "Published drafts, newest first.\n" + counts,
		},
		{
			name:       "my open ones",
			args:       []string{"--mine"},
			wantURI:    "/v1/admin/drafts?limit=200&mine=true&state=open",
			wantStderr: "Your open drafts, newest first.\n" + counts,
		},
		{
			name:       "a list with no counts says nothing about them",
			reply:      draftReply{body: `{"items":[],"next_cursor":""}`},
			wantURI:    "/v1/admin/drafts?limit=200&state=open",
			wantStderr: "Open drafts, newest first.\n",
		},
		{
			name:     "a refusal of the caller exits 2",
			reply:    draftReply{code: http.StatusForbidden, body: `{"error":"Drafts need the scope drafts:read to read or drafts:write to change, or a role that may change servers, roles or policy sets. Ask an administrator for one."}`},
			wantURI:  "/v1/admin/drafts?limit=200&state=open",
			wantErr:  "Drafts need the scope drafts:read to read or drafts:write to change, or a role that may change servers, roles or policy sets. Ask an administrator for one.",
			wantCode: 2,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			reply := tc.reply
			if reply.body == "" {
				reply.body = strings.Replace(page, "%q", `"`+tc.cursor+`"`, 1)
			}
			s, srv := newDraftsServer(t, map[string]draftReply{"GET /v1/admin/drafts": reply})
			t.Setenv("STRAZA_SERVER", "")
			args := append([]string{"drafts", "list"}, tc.args...)
			stdout, stderr, code, err := runDrafts(t, writeCreds(t, srv.URL), "", args...)
			if code != tc.wantCode || errText(err) != tc.wantErr {
				t.Errorf("exit %d, err %v, want exit %d, err %q", code, err, tc.wantCode, tc.wantErr)
			}
			if stderr != tc.wantStderr {
				t.Errorf("stderr = %q, want %q", stderr, tc.wantStderr)
			}
			rows := map[string]string{}
			for _, line := range strings.Split(stdout, "\n") {
				if f := strings.Fields(line); len(f) > 0 {
					rows[f[0]] = line
				}
			}
			for id, cells := range tc.wantRows {
				wantInOrder(t, rows[id], cells...)
			}
			if got := s.calls(); len(got) != 1 || got[0].uri != tc.wantURI {
				t.Errorf("requests = %+v, want GET %s", got, tc.wantURI)
			}
		})
	}
}

// TestDraftsShow pins show: the header on stderr, each revision and the
// note, each item against live state, when it was checked, the verdict,
// info and passed lines, who gains what in the policy words, the needs, and
// the line about publishing.
func TestDraftsShow(t *testing.T) {
	tests := []struct {
		name     string
		reply    draftReply
		want     []string
		notWant  string
		wantErr  string
		wantCode int
	}{
		{
			name:  "an open draft the reader may publish",
			reply: draftReply{body: fixDetail("open", "", fixTyped+","+fixTick, true, "")},
			want: []string{
				"Revision 1 by joe-java-developer-agent (agent, sponsored by alice) through the straza app at 2026-09-24 10:14 UTC.\n",
				"The note, which Straza did not check: The platform team asked for read access to GitHub.\n",
				"  +   App/github\n--- live App/github\n+++ draft App/github\n@@ -0,0 +1,3 @@\n+kind: App\n+metadata:\n+  name: github\n",
				"  ~   Role/developer\n--- live Role/developer\n+++ draft Role/developer\n", "-  implies: []\n+  implies: [github-readers]\n",
				"Checked against live state at 2026-09-24 10:20 UTC.\n",
				"  widens    App/github Straza will send", "  widens    Role/developer", "  warning   App/github", "  unchecked App/github",
				"  info      Role/github-readers Nobody holds github-readers yet, so it reaches nothing until someone is assigned it.\n",
				"  passed    No document holds a secret or the shape of one.\n",
				"ROLE", "SERVER", "TOOL", "NOW", "AFTER", "HOLDERS", "developer", "github", "list_issues", "not reachable",
				"a hold, up to 2 minutes, decided by sec-approvers", "3\n",
				"  Publishing App/github needs the scope apps:write or the role straza-global-mcp-admin.\n",
				"You may publish it: strazactl drafts publish 41\n",
			},
			notWant: "Proposed by",
		},
		{
			name: "an open draft the reader may not publish",
			reply: draftReply{body: fixDetail("open", "", fixTick, false,
				"You cannot publish draft 41: this draft also changes policy sets, which needs the scope policy:write.")},
			want:    []string{"\nYou cannot publish draft 41: this draft also changes policy sets, which needs the scope policy:write.\n"},
			notWant: "You may publish it",
		},
		{
			name:    "an open draft the check refuses",
			reply:   draftReply{body: fixDetail("open", fixRefused, "", true, "")},
			want:    []string{"  refused   PolicySet/github-writers-access", "\nFix the refused lines, then send the documents again: strazactl drafts update 41 -f <path>\n"},
			notWant: "You may publish it",
		},
		{
			name:    "an item equal to live prints no diff",
			reply:   draftReply{body: strings.Replace(fixDetail("open", "", "", true, ""), `implies: []\n"}`, `implies: [github-readers]\n"}`, 1)},
			want:    []string{"  ~   Role/developer\nChecked against live state at 2026-09-24 10:20 UTC.\n  warning   App/github"},
			notWant: "--- live Role/developer",
		},
		{
			name:    "a decided draft ends on its needs",
			reply:   draftReply{body: fixDetail("published", "", "", false, "")},
			want:    []string{"  Publishing App/github needs the scope apps:write or the role straza-global-mcp-admin.\n"},
			notWant: "publish it",
		},
		{
			name: "a published draft names the check stored for its revision",
			reply: draftReply{body: strings.Replace(fixDetail("published", "", "", false, ""), `"may_publish":`,
				`"checks":{"refused":0,"risks":1,"warnings":1,"unchecked":0,"revision":2,"checked_at":"2026-09-24T10:20:11Z"},"may_publish":`, 1)},
			want:    []string{"\nRevision 2 was checked against live state at 2026-09-24 10:20 UTC (1 widen, 1 warn). Straza does not check a draft again once it is published.\n"},
			notWant: "Checked against live state at",
		},
		{
			name:    "a published draft with no stored check says so",
			reply:   draftReply{body: strings.Replace(fixDetail("published", "", "", false, ""), `"checked_at":"2026-09-24T10:20:11Z"`, `"checked_at":""`, 1)},
			want:    []string{"\nStraza stored no check of revision 2, and it does not check a draft again once it is published.\n"},
			notWant: "Checked against live state at",
		},
		{
			name:     "a draft that is not there exits 2",
			reply:    draftReply{code: http.StatusNotFound, body: `{"error":"There is no draft 41. List the drafts with strazactl drafts list, or open Drafts on the console."}`},
			wantErr:  "There is no draft 41. List the drafts with strazactl drafts list, or open Drafts on the console.",
			wantCode: 2,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, srv := newDraftsServer(t, map[string]draftReply{"GET /v1/admin/drafts/41": tc.reply})
			t.Setenv("STRAZA_SERVER", "")
			stdout, stderr, code, err := runDrafts(t, writeCreds(t, srv.URL), "", "drafts", "show", "41")
			if code != tc.wantCode || errText(err) != tc.wantErr {
				t.Errorf("exit %d, err %v, want exit %d, err %q", code, err, tc.wantCode, tc.wantErr)
			}
			if tc.wantCode == 0 && !strings.HasPrefix(stderr, "Draft 41, revision 2, ") {
				t.Errorf("stderr = %q, want the draft's header", stderr)
			}
			wantInOrder(t, stdout, tc.want...)
			if tc.notWant != "" && strings.Contains(stdout, tc.notWant) {
				t.Errorf("stdout carries %q:\n%s", tc.notWant, stdout)
			}
		})
	}
}

// TestDraftsDiscard pins discard: the question, the reason on the wire, the
// line for a removal the apps directory proposed, and that a decline or a
// refusal never exits 1.
func TestDraftsDiscard(t *testing.T) {
	removal := `{"kind":"App","name":"demo-tools","op":"remove","base":"fp-demo","existed":true}`
	tests := []struct {
		name      string
		args      []string
		stdin     string
		reply     draftReply
		wantOut   string
		wantBody  string
		wantErr   string
		wantCode  int
		wantCalls int
	}{
		{
			name:      "yes at the question discards",
			args:      []string{"--reason", "superseded"},
			stdin:     "y\n",
			reply:     draftReply{body: `{"draft":` + fixDraft("41", 2, "discarded", "strazactl", fixItems) + `}`},
			wantOut:   "Discard draft 41? Nothing live changes. [y/N] Discarded draft 41.\n",
			wantBody:  `{"reason":"superseded"}`,
			wantCalls: 1,
		},
		{
			name:      "a removal the apps directory proposed keeps its server",
			args:      []string{"--yes"},
			reply:     draftReply{body: `{"draft":` + fixDraft("41", 1, "discarded", "apps-directory", "["+removal+"]") + `}`},
			wantOut:   "Discarded draft 41.\ndemo-tools stays, and its link to the file ends.\n",
			wantBody:  `{}`,
			wantCalls: 1,
		},
		{
			name:     "a decline sends nothing and exits 2",
			stdin:    "n\n",
			wantOut:  "Discard draft 41? Nothing live changes. [y/N] ",
			wantErr:  errAborted().Error(),
			wantCode: 2,
		},
		{
			name:      "a draft decided already exits 2",
			args:      []string{"--yes"},
			reply:     draftReply{code: http.StatusConflict, body: `{"error":"Draft 41 is published already, so there is nothing to discard."}`},
			wantBody:  `{}`,
			wantErr:   "Draft 41 is published already, so there is nothing to discard.",
			wantCode:  2,
			wantCalls: 1,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s, srv := newDraftsServer(t, map[string]draftReply{"POST /v1/admin/drafts/41/discard": tc.reply})
			t.Setenv("STRAZA_SERVER", "")
			args := append([]string{"drafts", "discard", "41"}, tc.args...)
			stdout, _, code, err := runDrafts(t, writeCreds(t, srv.URL), tc.stdin, args...)
			if code != tc.wantCode || errText(err) != tc.wantErr || stdout != tc.wantOut {
				t.Errorf("exit %d, err %v, stdout %q, want exit %d, err %q, stdout %q", code, err, stdout, tc.wantCode, tc.wantErr, tc.wantOut)
			}
			assertCalls(t, s, tc.wantCalls)
			if got := s.calls(); tc.wantCalls == 1 && (len(got) != 1 || got[0].body != tc.wantBody) {
				t.Errorf("requests = %+v, want one with body %s", got, tc.wantBody)
			}
		})
	}
}

// TestDraftsRevert pins revert: the created line, the verdict lines, the
// hint for the next command, and a draft that cannot be undone.
func TestDraftsRevert(t *testing.T) {
	tests := []struct {
		name     string
		args     []string
		reply    draftReply
		want     []string
		wantBody string
		wantErr  string
		wantCode int
	}{
		{
			name:     "an undo that can be published",
			args:     []string{"--note", "undo the rollout"},
			reply:    draftReply{code: http.StatusCreated, body: fixAnswer("42", 1, "", fixTick)},
			want:     []string{"Created draft 42, which undoes draft 41. Nothing changes until a person publishes it.\n  widens    Role/developer", "\nThe draft can be published: strazactl drafts publish 42\n"},
			wantBody: `{"note":"undo the rollout"}`,
		},
		{
			name:     "an undo that went stale at once exits 1",
			reply:    draftReply{code: http.StatusCreated, body: fixAnswer("42", 1, fixRefused, "")},
			want:     []string{"  refused   ", "\nThe draft cannot be published yet. Fix it and send it again: strazactl drafts update 42 -f <path>\n"},
			wantBody: `{}`,
			wantCode: 1,
		},
		{
			name:     "a draft that changed nothing exits 1",
			reply:    draftReply{code: http.StatusConflict, body: `{"error":"Draft 41 changed nothing, so there is nothing to undo."}`},
			wantBody: `{}`,
			wantErr:  "Draft 41 changed nothing, so there is nothing to undo.",
			wantCode: 1,
		},
		{
			name:     "a proxy's 504 may have stored the undo draft",
			reply:    draftReply{code: http.StatusGatewayTimeout, body: "<html>504 Gateway Time-out</html>"},
			wantBody: `{}`,
			wantErr: "strazad or a proxy in front of it answered HTTP 504 without a sentence, so the undo draft may have been stored. " +
				"List your drafts with strazactl drafts list --mine before you revert again",
			wantCode: 2,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s, srv := newDraftsServer(t, map[string]draftReply{"POST /v1/admin/drafts/41/revert": tc.reply})
			t.Setenv("STRAZA_SERVER", "")
			args := append([]string{"drafts", "revert", "41"}, tc.args...)
			stdout, _, code, err := runDrafts(t, writeCreds(t, srv.URL), "", args...)
			if code != tc.wantCode || errText(err) != tc.wantErr {
				t.Errorf("exit %d, err %v, want exit %d, err %q", code, err, tc.wantCode, tc.wantErr)
			}
			wantInOrder(t, stdout, tc.want...)
			if got := s.calls(); len(got) != 1 || got[0].body != tc.wantBody {
				t.Errorf("requests = %+v, want one revert of %s", got, tc.wantBody)
			}
		})
	}
}

// TestDraftsJSON pins --json on every verb: the server's answer, indented, is
// the only thing on stdout. A 409 or 422 body goes there too, with its
// sentence as the error, and a verb that would ask a question refuses
// without --yes before it sends anything.
func TestDraftsJSON(t *testing.T) {
	file := writeFile(t, t.TempDir(), "github.yaml", "kind: App\n")
	check := `{"items":` + fixItems + `,"verdict":` + fixVerdict("", fixTick) + `}`
	refusedCheck := `{"items":` + fixItems + `,"verdict":` + fixVerdict(fixRefused, "") + `}`
	intake := `{"error":"The draft holds no document. Add at least one App, Role, PolicySet or Removal document.","findings":[]}`
	page := `{"items":[],"next_cursor":"c2"}`
	published := `{"draft":` + fixDraft("41", 2, "published", "strazactl", fixItems) + `,"snapshot":"3be0a1","servers":[],"next":[]}`
	stale := `{"error":"Draft 41 cannot be published: App/github changed after this draft was checked.","verdict":` + fixVerdict(fixRefused, "") + `}`
	discarded := `{"draft":` + fixDraft("41", 2, "discarded", "strazactl", fixItems) + `}`
	tests := []struct {
		name       string
		args       []string
		replies    map[string]draftReply
		wantStdout string
		wantStderr string
		wantErr    string
		wantCode   int
		wantCalls  int
	}{
		{name: "check", args: []string{"check", "-f", file}, replies: map[string]draftReply{"POST /v1/admin/drafts/check": {body: check}},
			wantStdout: check, wantCalls: 1},
		{name: "check that refuses", args: []string{"check", "-f", file}, replies: map[string]draftReply{"POST /v1/admin/drafts/check": {body: refusedCheck}},
			wantStdout: refusedCheck, wantErr: "Nothing was stored. The documents cannot be published as they are: fix the refused lines.", wantCode: 1, wantCalls: 1},
		{name: "create refused at intake", args: []string{"create", "-f", file}, replies: map[string]draftReply{"POST /v1/admin/drafts": {code: 422, body: intake}},
			wantStdout: intake, wantErr: "The draft holds no document. Add at least one App, Role, PolicySet or Removal document.", wantCode: 1, wantCalls: 1},
		{name: "update prints the new revision alone", args: []string{"update", "41", "-f", file},
			replies: map[string]draftReply{
				"GET /v1/admin/drafts/41": {body: fixDetail("open", "", "", true, "")},
				"PUT /v1/admin/drafts/41": {body: fixAnswer("41", 3, "", "")},
			},
			wantStdout: fixAnswer("41", 3, "", ""), wantCalls: 2},
		{name: "list", args: []string{"list"}, replies: map[string]draftReply{"GET /v1/admin/drafts": {body: page}},
			wantStdout: page, wantStderr: "More drafts exist than these 200. Narrow the list with --state or --mine.\n", wantCalls: 1},
		{name: "show", args: []string{"show", "41"}, replies: map[string]draftReply{"GET /v1/admin/drafts/41": {body: fixDetail("open", "", "", true, "")}},
			wantStdout: fixDetail("open", "", "", true, ""), wantCalls: 1},
		{name: "publish", args: []string{"publish", "41", "--yes", "--ack", "api.githubcopilot.com"},
			replies: map[string]draftReply{
				"GET /v1/admin/drafts/41":          {body: fixDetail("open", "", fixTyped, true, "")},
				"POST /v1/admin/drafts/41/publish": {body: published},
			},
			wantStdout: published, wantCalls: 3},
		{name: "publish that went stale", args: []string{"publish", "41", "--yes"},
			replies: map[string]draftReply{
				"GET /v1/admin/drafts/41":          {body: fixDetail("open", "", fixTick, true, "")},
				"POST /v1/admin/drafts/41/publish": {code: 409, body: stale},
			},
			wantStdout: stale, wantErr: "Draft 41 cannot be published: App/github changed after this draft was checked.", wantCode: 1, wantCalls: 3},
		{name: "discard", args: []string{"discard", "41", "--yes"}, replies: map[string]draftReply{"POST /v1/admin/drafts/41/discard": {body: discarded}},
			wantStdout: discarded, wantCalls: 1},
		{name: "revert", args: []string{"revert", "41"}, replies: map[string]draftReply{"POST /v1/admin/drafts/41/revert": {code: 201, body: fixAnswer("42", 1, "", "")}},
			wantStdout: fixAnswer("42", 1, "", ""), wantCalls: 1},
		{name: "publish without --yes asks nothing and sends nothing", args: []string{"publish", "41"},
			wantErr:  "strazactl drafts publish --json needs --yes, because --json keeps stdout for the server's answer and a question cannot share it. Pass --yes, and --ack with the text of each typed risk",
			wantCode: 2},
		{name: "a server outage prints nothing on stdout", args: []string{"check", "-f", file},
			replies:  map[string]draftReply{"POST /v1/admin/drafts/check": {code: 503, body: `{"error":"Straza could not read live state. Try again in a moment."}`}},
			wantErr:  "Straza could not read live state. Try again in a moment.",
			wantCode: 2, wantCalls: 1},
		{name: "discard without --yes asks nothing and sends nothing", args: []string{"discard", "41"},
			wantErr:  "strazactl drafts discard --json needs --yes, because --json keeps stdout for the server's answer and a question cannot share it. Pass --yes to discard without the question",
			wantCode: 2},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s, srv := newDraftsServer(t, tc.replies)
			t.Setenv("STRAZA_SERVER", "")
			args := append(append([]string{"drafts"}, tc.args...), "--json")
			stdout, stderr, code, err := runDrafts(t, writeCreds(t, srv.URL), "", args...)
			if code != tc.wantCode || errText(err) != tc.wantErr {
				t.Errorf("exit %d, err %v, want exit %d, err %q", code, err, tc.wantCode, tc.wantErr)
			}
			want := ""
			if tc.wantStdout != "" {
				want = indented(t, tc.wantStdout)
				if !json.Valid([]byte(stdout)) {
					t.Errorf("stdout is not one JSON document:\n%s", stdout)
				}
			}
			if stdout != want {
				t.Errorf("stdout =\n%s\nwant\n%s", stdout, want)
			}
			if stderr != tc.wantStderr {
				t.Errorf("stderr = %q, want %q", stderr, tc.wantStderr)
			}
			assertCalls(t, s, tc.wantCalls)
		})
	}
}

// TestDraftsInsideACodingAgent pins the guard on every verb: on the login
// inside a coding agent, check, list and show run, and the verbs that change
// a draft are refused before anything is sent, a read or a question
// included, and exit 2. On an admin API token they go out.
func TestDraftsInsideACodingAgent(t *testing.T) {
	file := writeFile(t, t.TempDir(), "github.yaml", "kind: App\n")
	replies := map[string]draftReply{
		"POST /v1/admin/drafts/check":      {body: `{"items":[],"verdict":` + fixVerdict("", "") + `}`},
		"POST /v1/admin/drafts":            {code: 201, body: fixAnswer("41", 1, "", "")},
		"GET /v1/admin/drafts":             {body: `{"items":[],"next_cursor":""}`},
		"GET /v1/admin/drafts/41":          {body: fixDetail("open", "", "", true, "")},
		"POST /v1/admin/drafts/41/discard": {body: `{"draft":` + fixDraft("41", 2, "discarded", "strazactl", fixItems) + `}`},
	}
	const refusal = "CLAUDECODE is set, so strazactl runs inside a coding agent, and this command changes Straza"
	tests := []struct {
		name      string
		token     string
		args      []string
		wantCalls []string
	}{
		{name: "check runs", args: []string{"check", "-f", file}, wantCalls: []string{"POST /v1/admin/drafts/check"}},
		{name: "list runs", args: []string{"list"}, wantCalls: []string{"GET /v1/admin/drafts?limit=200&state=open"}},
		{name: "show runs", args: []string{"show", "41"}, wantCalls: []string{"GET /v1/admin/drafts/41"}},
		{name: "create is refused", args: []string{"create", "-f", file}},
		{name: "update is refused before its read", args: []string{"update", "41", "-f", file}},
		{name: "publish is refused before its read", args: []string{"publish", "41", "--yes"}},
		{name: "discard is refused before its question", args: []string{"discard", "41"}},
		{name: "revert is refused", args: []string{"revert", "41"}},
		{name: "create runs on a token", token: "wat_abc", args: []string{"create", "-f", file}, wantCalls: []string{"POST /v1/admin/drafts"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s, srv := newDraftsServer(t, replies)
			t.Setenv("STRAZA_SERVER", "")
			t.Setenv("CLAUDECODE", "1")
			t.Setenv(apiTokenEnv, tc.token)
			stdout, _, code, err := runDrafts(t, writeCreds(t, srv.URL), "y\n", append([]string{"drafts"}, tc.args...)...)
			var calls []string
			for _, r := range s.calls() {
				calls = append(calls, r.method+" "+r.uri)
			}
			if strings.Join(calls, "|") != strings.Join(tc.wantCalls, "|") {
				t.Errorf("requests = %q, want %q", calls, tc.wantCalls)
			}
			if tc.wantCalls == nil {
				assertCalls(t, s, 0)
			}
			if tc.wantCalls != nil {
				if code != 0 {
					t.Errorf("exit %d (%v), want 0", code, err)
				}
				return
			}
			if code != 2 || !strings.HasPrefix(errText(err), refusal) || stdout != "" {
				t.Errorf("exit %d, err %v, stdout %q, want exit 2 with the guard's refusal and nothing printed", code, err, stdout)
			}
		})
	}
}

// TestDraftsUsageExitsTwo pins the usage refusals: each names the form the
// verb takes, exits 2, and sends nothing.
func TestDraftsUsageExitsTwo(t *testing.T) {
	tests := []struct {
		name    string
		args    []string
		wantErr string
	}{
		{"check without -f", []string{"check"},
			"strazactl drafts check needs its documents with -f: a file, a directory of .yaml and .yml files, or - for stdin, as in strazactl drafts check -f github.yaml"},
		{"update without -f", []string{"update", "41"},
			"strazactl drafts update needs its documents with -f: a file, a directory of .yaml and .yml files, or - for stdin, as in strazactl drafts update 41 -f github.yaml"},
		{"a path without its -f", []string{"create", "github.yaml"},
			"strazactl drafts create reads documents with -f, one -f for each path, as in strazactl drafts create -f github.yaml"},
		{"no draft number", []string{"show"},
			"strazactl drafts show takes one draft number, such as 41, and got 0. List the drafts with strazactl drafts list"},
		{"two draft numbers", []string{"publish", "41", "42"},
			"strazactl drafts publish takes one draft number, such as 41, and got 2. List the drafts with strazactl drafts list"},
		{"stdin twice", []string{"check", "-f", "-", "-f", "-"},
			"-f - reads stdin, which can be read once, and it is given twice. Pass -f - once"},
		{"an unknown flag", []string{"list", "--bogus"}, "unknown flag: --bogus"},
		{"an unknown verb", []string{"chek"}, "strazactl drafts has no verb chek. Run strazactl drafts --help for the verbs"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s, srv := newDraftsServer(t, nil)
			t.Setenv("STRAZA_SERVER", "")
			_, _, code, err := runDrafts(t, writeCreds(t, srv.URL), "", append([]string{"drafts"}, tc.args...)...)
			if code != 2 || errText(err) != tc.wantErr {
				t.Errorf("exit %d, err %v, want exit 2 and %q", code, err, tc.wantErr)
			}
			assertCalls(t, s, 0)
		})
	}
}

// TestDraftsAnswerWithNoSentence pins the words for an answer that carries
// no sentence of its own: the route strazad does not serve for a 404 or
// 405, and a proxy or strazad that said nothing for any other status.
func TestDraftsAnswerWithNoSentence(t *testing.T) {
	const generic = " without a sentence. Check strazad's log, and the log of any proxy in front of it, then run the command again"
	tests := []struct {
		code int
		want string
	}{
		{http.StatusNotFound, "strazad answered HTTP 404 with no sentence of its own, which it does when it does not serve GET /v1/admin/drafts. " +
			"Upgrade strazad, or check that --server or your login points at strazad"},
		{http.StatusMethodNotAllowed, "strazad answered HTTP 405 with no sentence of its own, which it does when it does not serve GET /v1/admin/drafts. " +
			"Upgrade strazad, or check that --server or your login points at strazad"},
		{http.StatusRequestEntityTooLarge, "strazad or a proxy in front of it answered HTTP 413" + generic},
		{http.StatusBadGateway, "strazad or a proxy in front of it answered HTTP 502" + generic},
	}
	for _, tc := range tests {
		t.Run(http.StatusText(tc.code), func(t *testing.T) {
			_, srv := newDraftsServer(t, map[string]draftReply{"GET /v1/admin/drafts": {code: tc.code, body: "<html>" + http.StatusText(tc.code) + "</html>"}})
			t.Setenv("STRAZA_SERVER", "")
			_, _, code, err := runDrafts(t, writeCreds(t, srv.URL), "", "drafts", "list")
			if code != 2 || errText(err) != tc.want {
				t.Errorf("exit %d, err %v, want exit 2 and %q", code, err, tc.want)
			}
		})
	}
}

// TestDraftsGroupPrintsItsHelp pins the drafts group run with no verb: it
// prints its help and exits 0, with no login and no server.
func TestDraftsGroupPrintsItsHelp(t *testing.T) {
	t.Setenv("STRAZA_SERVER", "")
	stdout, _, code, err := runDrafts(t, noCreds(t), "", "drafts")
	if code != 0 || err != nil {
		t.Fatalf("exit %d, err %v, want 0", code, err)
	}
	wantInOrder(t, stdout, "A draft holds App, Role, PolicySet and Removal documents", "Available Commands:", "check", "publish")
}

// TestAPITokenScopeHelpNamesDrafts pins the drafts area in api-token
// create's --scope help.
func TestAPITokenScopeHelpNamesDrafts(t *testing.T) {
	stdout, _, err := runCLI(t, noCreds(t), "api-token", "create", "--help")
	if err != nil {
		t.Fatalf("help: %v", err)
	}
	if want := "tokens, changes, scim, drafts)"; !strings.Contains(stdout, want) {
		t.Errorf("help lacks %q:\n%s", want, stdout)
	}
}

// TestShellWord pins how fileArgs spells a path in both shell forms, called
// directly so the Windows rows run on every platform: as it is when the
// shell neither splits nor expands it, else quoted so that it survives.
func TestShellWord(t *testing.T) {
	for _, tc := range []struct {
		name string
		word func(string) string
		in   string
		want string
	}{
		{"posix", posixWord, "github.yaml", "github.yaml"},
		{"posix", posixWord, "-", "-"},
		{"posix", posixWord, "/tmp/on-board/gh_devs.yaml", "/tmp/on-board/gh_devs.yaml"},
		{"posix", posixWord, "my guard.yaml", "'my guard.yaml'"},
		{"posix", posixWord, "~/policy.yaml", "'~/policy.yaml'"},
		{"posix", posixWord, "$HOME/p.yaml", "'$HOME/p.yaml'"},
		{"posix", posixWord, "dana's.yaml", `'dana'\''s.yaml'`},
		{"posix", posixWord, "", "''"},
		{"windows", cmdWord, `.\guard.yaml`, `.\guard.yaml`},
		{"windows", cmdWord, `C:\Users\dana\guard.yaml`, `C:\Users\dana\guard.yaml`},
		{"windows", cmdWord, "-", "-"},
		{"windows", cmdWord, `C:\Users\dana\my guard.yaml`, `"C:\Users\dana\my guard.yaml"`},
		{"windows", cmdWord, `dana's.yaml`, `"dana's.yaml"`},
		{"windows", cmdWord, "", `""`},
	} {
		if got := tc.word(tc.in); got != tc.want {
			t.Errorf("%s form of %q = %s, want %s", tc.name, tc.in, got, tc.want)
		}
	}
}
