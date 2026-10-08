package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// The tokens are short, plain words so that no secret scanner reads them as
// credentials.
const (
	testToken        = "workflow"
	testRecordsToken = "records"
	testTextCommit   = "0123456789abcdef0123456789abcdef01234567"
	testTextURL      = "https://github.com/strazahq/straza/blob/" + testTextCommit + "/CLA.md"
	testText         = "Straza Contributor License Agreement\nVersion 1.2\n"
	headA            = "c0ffee00c0ffee00c0ffee00c0ffee00c0ffee00"
	headB            = "beef0000beef0000beef0000beef0000beef0000"
	prPath           = "v1.2/events/strazahq__straza/123/"
	pageSize         = 2
)

var testNow = time.Date(2026, 10, 5, 13, 0, 0, 0, time.UTC)

type fakeStatus struct{ SHA, State, Description, Target string }

// fakeGitHub serves the calls the check makes, from the fixtures in testdata,
// and keeps what the check writes. It pages every list two at a time, the
// authors of a commit included, so that every run follows the pages. An edit
// keeps the revisions that GitHub's edit history shows, newest first.
type fakeGitHub struct {
	t         *testing.T
	mu        sync.Mutex
	srv       *httptest.Server
	pr        map[string]any
	tmpl      map[string]any
	lists     map[string][]map[string]any
	history   map[string][]map[string]any
	commits   []commit
	openPRs   []map[string]any
	diff      string
	diffCode  int
	published string
	records   map[string][]byte
	statuses  []fakeStatus
	log       []string
	nextID    int64
	clock     time.Time
	failPut   map[string]int
	fail      map[string]fakeFailure
	during    map[string]func()
	edits     int
	// statusBase is how many statuses a commit held before the test.
	statusBase map[string]int
}

// fakeFailure is an answer the fake gives once, for a method and a path.
type fakeFailure struct {
	code    int
	message string
	headers map[string]string
}

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func decodeMap(t *testing.T, data []byte) map[string]any {
	t.Helper()
	m := map[string]any{}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	if err := dec.Decode(&m); err != nil {
		t.Fatal(err)
	}
	return m
}

func newFake(t *testing.T) *fakeGitHub {
	t.Helper()
	commits, _, err := parseCommits(fixture(t, "commits.graphql.json"))
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte(testText))
	f := &fakeGitHub{t: t, pr: decodeMap(t, fixture(t, "pull_request.json")), tmpl: decodeMap(t, fixture(t, "issue_comment.json")),
		lists: map[string][]map[string]any{}, history: map[string][]map[string]any{},
		commits: commits, diff: string(fixture(t, "pull.diff")), published: testText, nextID: 2345678900,
		clock: time.Date(2026, 10, 5, 12, 30, 0, 0, time.UTC), failPut: map[string]int{}, fail: map[string]fakeFailure{}, during: map[string]func(){}, statusBase: map[string]int{},
		records: map[string][]byte{
			"v1.2/text/CLA.md":     []byte(testText),
			"v1.2/text/SHA256SUMS": []byte(hex.EncodeToString(sum[:]) + "  CLA.md\n"),
		}}
	f.openPRs = []map[string]any{f.pr}
	f.srv = httptest.NewServer(f)
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeGitHub) textSHA() string {
	sum := sha256.Sum256([]byte(testText))
	return hex.EncodeToString(sum[:])
}

// setOpener makes a the account that opened the pull request.
func (f *fakeGitHub) setOpener(a account) {
	f.pr["user"] = map[string]any{"login": a.Login, "id": a.ID, "type": "User"}
}

func (f *fakeGitHub) setHead(sha string) {
	f.pr["head"].(map[string]any)["sha"] = sha
}

// addAccount stores an account file, as an acceptance on an earlier pull
// request would have.
func (f *fakeGitHub) addAccount(a account) {
	f.records["v1.2/accounts/"+itoa(a.ID)+".json"] = []byte(fmt.Sprintf(`{"id": %d, "login": %q, "basis": "comment"}`, a.ID, a.Login))
}

// addComment posts a conversation comment as a and returns its id.
func (f *fakeGitHub) addComment(a account, body string) int64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.addLocked(kindComment, a, body)
}

// addReviewComment posts a comment on a line of the diff as a.
func (f *fakeGitHub) addReviewComment(a account, body string) int64 {
	return f.addLocked(kindReviewComment, a, body)
}

// addReview submits a review with body as a.
func (f *fakeGitHub) addReview(a account, body string) int64 {
	return f.addLocked(kindReview, a, body)
}

func (f *fakeGitHub) addLocked(kind string, a account, body string) int64 {
	f.nextID++
	f.clock = f.clock.Add(time.Minute)
	id, ts := f.nextID, f.clock.Format(time.RFC3339)
	c := decodeMap(f.t, mustJSON(f.tmpl))
	c["id"] = id
	user := c["user"].(map[string]any)
	user["id"], user["login"] = a.ID, a.Login
	if a.ID == botID {
		user["type"] = "Bot"
	}
	c["body"] = body
	switch kind {
	case kindReview:
		c["node_id"], c["state"], c["submitted_at"] = fmt.Sprintf("PRR_%d", id), "COMMENTED", ts
		c["html_url"] = fmt.Sprintf("https://github.com/strazahq/straza/pull/123#pullrequestreview-%d", id)
		delete(c, "created_at")
		delete(c, "updated_at")
	case kindReviewComment:
		c["node_id"], c["created_at"], c["updated_at"] = fmt.Sprintf("PRRC_%d", id), ts, ts
		c["html_url"] = fmt.Sprintf("https://github.com/strazahq/straza/pull/123#discussion_r%d", id)
		c["path"] = "internal/audit/sink.go"
	default:
		c["node_id"], c["created_at"], c["updated_at"] = fmt.Sprintf("IC_%d", id), ts, ts
		c["html_url"] = fmt.Sprintf("https://github.com/strazahq/straza/pull/123#issuecomment-%d", id)
		c["url"] = fmt.Sprintf("https://api.github.com/repos/strazahq/straza/issues/comments/%d", id)
	}
	f.lists[kind] = append(f.lists[kind], c)
	return id
}

// find returns the list and the index that hold the item id.
func (f *fakeGitHub) find(id int64) (string, int) {
	for kind, list := range f.lists {
		for i, c := range list {
			if fmt.Sprint(c["id"]) == itoa(id) {
				return kind, i
			}
		}
	}
	f.t.Fatalf("no comment %d", id)
	return "", -1
}

func (f *fakeGitHub) comment(id int64) map[string]any {
	kind, i := f.find(id)
	return f.lists[kind][i]
}

// comments is the conversation, as the tests read it.
func (f *fakeGitHub) comments() []map[string]any { return f.lists[kindComment] }

// editComment changes a comment the way its author would, and GitHub's edit
// history gains the revision.
func (f *fakeGitHub) editComment(id int64, body string) {
	user := f.comment(id)["user"].(map[string]any)
	id64, _ := strconv.ParseInt(fmt.Sprint(user["id"]), 10, 64)
	f.editCommentAs(id, body, account{ID: id64, Login: fmt.Sprint(user["login"])})
}

// editCommentAs changes a comment as the editor, who may be another account
// with write access, such as a maintainer.
func (f *fakeGitHub) editCommentAs(id int64, body string, editor account) {
	f.clock = f.clock.Add(time.Minute)
	c := f.comment(id)
	node := fmt.Sprint(c["node_id"])
	created := fmt.Sprint(c["created_at"])
	if c["created_at"] == nil {
		created = fmt.Sprint(c["submitted_at"])
	}
	author := c["user"].(map[string]any)
	if len(f.history[node]) == 0 {
		f.history[node] = []map[string]any{{"editedAt": created, "diff": c["body"],
			"editor": map[string]any{"login": author["login"], "databaseId": author["id"]}}}
	}
	ts := f.clock.Format(time.RFC3339)
	rev := map[string]any{"editedAt": ts, "diff": body, "editor": map[string]any{"login": editor.Login, "databaseId": editor.ID}}
	f.history[node] = append([]map[string]any{rev}, f.history[node]...)
	c["body"] = body
	if c["updated_at"] != nil {
		c["updated_at"] = ts
	}
}

func (f *fakeGitHub) deleteComment(id int64) {
	kind, i := f.find(id)
	f.lists[kind] = append(f.lists[kind][:i], f.lists[kind][i+1:]...)
}

func mustJSON(v any) []byte {
	data, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return data
}

// run starts one run of the check against the fake with the event payload.
func (f *fakeGitHub) run(t *testing.T, eventName string, payload []byte, edit func(*config)) (string, error) {
	t.Helper()
	return f.runMode(t, eventName, payload, edit, false)
}

func (f *fakeGitHub) runMode(t *testing.T, eventName string, payload []byte, edit func(*config), pendingOnly bool) (string, error) {
	t.Helper()
	p := filepath.Join(t.TempDir(), "event.json")
	if err := os.WriteFile(p, payload, 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := config{Repo: "strazahq/straza", EventName: eventName, EventPath: p, PRInput: "123", Version: "1.2",
		TextURL: testTextURL, AllowIDs: "4242, 49699333", Token: testToken, RecordsToken: testRecordsToken,
		APIURL: f.srv.URL, GraphQLURL: f.srv.URL + "/graphql", RunURL: "https://github.com/strazahq/straza/actions/runs/1"}
	if edit != nil {
		edit(&cfg)
	}
	var out bytes.Buffer
	r := newRunner(cfg, &out)
	r.now = func() time.Time { return testNow }
	f.handOff()
	var err error
	if pendingOnly {
		err = r.markPending()
	} else {
		err = r.run()
	}
	f.handOff()
	return out.String(), err
}

// handOff orders the test's own reads and writes of the fake's state before
// and after a run with the handler's, for the race detector.
func (f *fakeGitHub) handOff() {
	f.mu.Lock()
	defer f.mu.Unlock()
}

// prEvent is a pull_request_target payload for the fake's pull request.
func (f *fakeGitHub) prEvent(t *testing.T, action string) []byte {
	ev := decodeMap(t, fixture(t, "pull_request_target.json"))
	ev["action"] = action
	pr := ev["pull_request"].(map[string]any)
	pr["head"].(map[string]any)["sha"] = f.pr["head"].(map[string]any)["sha"]
	pr["user"], pr["title"], pr["body"] = f.pr["user"], f.pr["title"], f.pr["body"]
	if s, ok := f.pr["state"]; ok {
		pr["state"] = s
	}
	return mustJSON(ev)
}

// commentEvent is an issue_comment payload for comment id. For an edit,
// before is the body before the change.
func (f *fakeGitHub) commentEvent(t *testing.T, action string, id int64, before string) []byte {
	ev := decodeMap(t, fixture(t, "issue_comment_event.json"))
	ev["action"] = action
	ev["comment"] = f.comment(id)
	if s, ok := f.pr["state"]; ok {
		ev["issue"].(map[string]any)["state"] = s
	}
	if action == "edited" {
		ev["changes"] = map[string]any{"body": map[string]any{"from": before}}
	}
	return mustJSON(ev)
}

func (f *fakeGitHub) lastStatus() fakeStatus {
	if len(f.statuses) == 0 {
		f.t.Fatal("no status was set")
	}
	return f.statuses[len(f.statuses)-1]
}

// recordsUnder lists the records paths with the prefix, sorted.
func (f *fakeGitHub) recordsUnder(prefix string) []string {
	var out []string
	for p := range f.records {
		if strings.HasPrefix(p, prefix) {
			out = append(out, strings.TrimPrefix(p, prefix))
		}
	}
	sort.Strings(out)
	return out
}

// confirmations counts the check's comments that confirm the acceptance key.
func (f *fakeGitHub) confirmations(key string) int {
	n := 0
	for _, c := range f.comments() {
		body := c["body"].(string)
		if fmt.Sprint(c["user"].(map[string]any)["id"]) == itoa(botID) && strings.HasSuffix(body, confirmMarker(key)) {
			n++
		}
	}
	return n
}

// confirmation returns the check's confirmation of the acceptance key.
func (f *fakeGitHub) confirmation(key string) string {
	for _, c := range f.comments() {
		if body := c["body"].(string); strings.HasSuffix(body, confirmMarker(key)) {
			return body
		}
	}
	f.t.Fatalf("no confirmation of %s", key)
	return ""
}

// statusComment returns the body of the check's status comment.
func (f *fakeGitHub) statusComment() string {
	for _, c := range f.comments() {
		if body := c["body"].(string); strings.HasPrefix(body, statusMarker) {
			return body
		}
	}
	f.t.Fatal("no status comment")
	return ""
}

// writesSince lists the requests after mark that change anything other than
// the commit status.
func (f *fakeGitHub) writesSince(mark int) []string {
	var out []string
	for _, l := range f.log[mark:] {
		if strings.HasPrefix(l, "GET ") || strings.Contains(l, "/statuses/") || l == "POST /graphql" {
			continue
		}
		out = append(out, l)
	}
	return out
}
