package main

import (
	"encoding/json"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"github.com/strazahq/straza/internal/drafts"
)

// fixStale is the refusal of an item whose object changed after the draft
// was checked.
const fixStale = `{"key":"k-stale","code":"draft.stale","class":"refused","object":"App/github",` +
	`"sentence":"App/github changed after this draft was checked.",` +
	`"fix":"Check the draft again with Check again on the console or strazactl drafts rebase 41. Straza keeps what this draft changed, takes every other field from live state, and asks you to pick where both changed."}`

// fixTokenRefusal is the publish refusal the server gives an admin API
// token.
const fixTokenRefusal = "An admin API token carries no person and cannot publish a draft. It may create drafts. " +
	"A person publishes them on the console or with strazactl."

// sameItems is the items of a draft that puts github and developer, both
// of which existed.
const sameItems = `[{"kind":"App","name":"github","op":"put","doc":"kind: App\n","existed":true},` +
	`{"kind":"Role","name":"developer","op":"put","doc":"kind: Role\n","existed":true}]`

// sameVerdict is a verdict with nothing to say but that each of objects
// equals live.
func sameVerdict(objects ...string) string {
	var info []string
	for _, o := range objects {
		info = append(info, `{"key":"k-`+o+`","code":"info.no-change","class":"info","object":"`+o+`",`+
			`"sentence":"`+o+` equals live, so publishing changes nothing for it."}`)
	}
	return `{"draft":"41","revision":1,"snapshot":"3be0a1","checked_at":"2026-09-24T10:20:11Z","refused":[],"risks":[],` +
		`"warnings":[],"unchecked":[],"passed":[],"info":[` + strings.Join(info, ",") + `],"gains":[],"needs":[],"risk_digest":"rd-1"}`
}

// callsOf is every drafts call s saw, as method and URI.
func callsOf(s *draftsServer) []string {
	var calls []string
	for _, r := range s.calls() {
		calls = append(calls, r.method+" "+r.uri)
	}
	return calls
}

// TestDraftsStaleEndsWithCheckAgain pins the last line of a draft whose
// check finds an object changed after the draft was checked. A revision
// keeps the base of every object it holds, so no update can fix it, and
// show, create, update and revert send the person to Check again, never to
// drafts update.
func TestDraftsStaleEndsWithCheckAgain(t *testing.T) {
	file := writeFile(t, t.TempDir(), "github.yaml", "kind: App\n")
	const again = "\nCheck the draft again, which keeps what it changed and takes every other field from live state: strazactl drafts rebase 41\n"
	tests := []struct {
		name     string
		args     []string
		replies  map[string]draftReply
		wantCode int
	}{
		{name: "show", args: []string{"show", "41"},
			replies: map[string]draftReply{"GET /v1/admin/drafts/41": {body: fixDetail("open", fixStale, "", true, "")}}},
		{name: "create", args: []string{"create", "-f", file}, wantCode: 1,
			replies: map[string]draftReply{"POST /v1/admin/drafts": {code: http.StatusCreated, body: fixAnswer("41", 1, fixStale, "")}}},
		{name: "update", args: []string{"update", "41", "-f", file}, wantCode: 1,
			replies: map[string]draftReply{
				"GET /v1/admin/drafts/41": {body: fixDetail("open", fixStale, "", true, "")},
				"PUT /v1/admin/drafts/41": {body: fixAnswer("41", 3, fixStale, "")},
			}},
		{name: "revert", args: []string{"revert", "40"}, wantCode: 1,
			replies: map[string]draftReply{"POST /v1/admin/drafts/40/revert": {code: http.StatusCreated, body: fixAnswer("41", 1, fixStale, "")}}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, srv := newDraftsServer(t, tc.replies)
			t.Setenv("STRAZA_SERVER", "")
			stdout, _, code, err := runDrafts(t, writeCreds(t, srv.URL), "", append([]string{"drafts"}, tc.args...)...)
			if code != tc.wantCode || errText(err) != "" {
				t.Errorf("exit %d, err %v, want exit %d and no second sentence", code, err, tc.wantCode)
			}
			if !strings.HasSuffix(stdout, again) || strings.Contains(stdout, "drafts update") {
				t.Errorf("stdout does not end with the Check again line, or names drafts update:\n%s", stdout)
			}
		})
	}
}

// TestDraftsOnATokenPointToALogin pins what follows the publish a token
// cannot do. On STRAZA_API_TOKEN, create and update end with the way to a
// person's publish instead of the publish command, show follows the
// server's refusal of the token with that way, and publish adds it to the
// refusal it exits 2 with, before any other call.
func TestDraftsOnATokenPointToALogin(t *testing.T) {
	file := writeFile(t, t.TempDir(), "github.yaml", "kind: App\n")
	const next = "Unset STRAZA_API_TOKEN, run strazactl login, then strazactl drafts publish 41"
	refused := fixDetail("open", "", fixTick, false, fixTokenRefusal)
	tests := []struct {
		name      string
		args      []string
		replies   map[string]draftReply
		wantEnd   string
		wantErr   string
		wantCode  int
		wantCalls []string
	}{
		{name: "create", args: []string{"create", "-f", file},
			replies:   map[string]draftReply{"POST /v1/admin/drafts": {code: http.StatusCreated, body: fixAnswer("41", 1, "", fixTick)}},
			wantEnd:   "\n" + next + ".\n",
			wantCalls: []string{"POST /v1/admin/drafts"}},
		{name: "update", args: []string{"update", "41", "-f", file},
			replies: map[string]draftReply{
				"GET /v1/admin/drafts/41": {body: refused},
				"PUT /v1/admin/drafts/41": {body: fixAnswer("41", 3, "", fixTick)},
			},
			wantEnd:   "\n" + next + ".\n",
			wantCalls: []string{"GET /v1/admin/drafts/41", "PUT /v1/admin/drafts/41"}},
		{name: "show", args: []string{"show", "41"},
			replies:   map[string]draftReply{"GET /v1/admin/drafts/41": {body: refused}},
			wantEnd:   "\n" + fixTokenRefusal + "\n" + next + ".\n",
			wantCalls: []string{"GET /v1/admin/drafts/41"}},
		{name: "publish", args: []string{"publish", "41"},
			replies: map[string]draftReply{
				"GET /v1/admin/drafts/41":          {body: refused},
				"POST /v1/admin/drafts/41/publish": {body: fixPublished},
			},
			wantErr:   fixTokenRefusal + " " + next,
			wantCode:  2,
			wantCalls: []string{"GET /v1/admin/drafts/41"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s, srv := newDraftsServer(t, tc.replies)
			t.Setenv("STRAZA_SERVER", "")
			t.Setenv(apiTokenEnv, "wat_abc")
			stdout, _, code, err := runDrafts(t, writeCreds(t, srv.URL), "y\n", append([]string{"drafts"}, tc.args...)...)
			if code != tc.wantCode || errText(err) != tc.wantErr {
				t.Errorf("exit %d, err %v, want exit %d, err %q", code, err, tc.wantCode, tc.wantErr)
			}
			if !strings.HasSuffix(stdout, tc.wantEnd) {
				t.Errorf("stdout does not end with %q:\n%s", tc.wantEnd, stdout)
			}
			if got := callsOf(s); strings.Join(got, "|") != strings.Join(tc.wantCalls, "|") {
				t.Errorf("requests = %q, want %q", got, tc.wantCalls)
			}
		})
	}
}

// TestDraftsNothingToPublish pins the last line of check, create and update
// when every document equals live: nothing to publish, exit 0. One document
// that changes something ends on the usual line.
func TestDraftsNothingToPublish(t *testing.T) {
	file := writeFile(t, t.TempDir(), "github.yaml", "kind: App\n")
	const nothing = "\nNothing to publish: every document equals live.\n"
	stored := func(rev int, verdict string) string {
		return `{"draft":` + fixDraft("41", rev, "open", "strazactl", sameItems) + `,"verdict":` + verdict + `}`
	}
	tests := []struct {
		name    string
		args    []string
		replies map[string]draftReply
		wantEnd string
	}{
		{name: "check", args: []string{"check", "-f", file}, wantEnd: nothing,
			replies: map[string]draftReply{"POST /v1/admin/drafts/check": {body: `{"items":` + sameItems + `,"verdict":` + sameVerdict("App/github", "Role/developer") + `}`}}},
		{name: "check of a document that changes something", args: []string{"check", "-f", file},
			wantEnd: "\nNothing was stored. The documents can be published: strazactl drafts create -f " + file + "\n",
			replies: map[string]draftReply{"POST /v1/admin/drafts/check": {body: `{"items":` + sameItems + `,"verdict":` + sameVerdict("App/github") + `}`}}},
		{name: "create", args: []string{"create", "-f", file}, wantEnd: nothing,
			replies: map[string]draftReply{"POST /v1/admin/drafts": {code: http.StatusCreated, body: stored(1, sameVerdict("App/github", "Role/developer"))}}},
		{name: "update", args: []string{"update", "41", "-f", file}, wantEnd: nothing,
			replies: map[string]draftReply{
				"GET /v1/admin/drafts/41": {body: fixDetail("open", "", "", true, "")},
				"PUT /v1/admin/drafts/41": {body: stored(3, sameVerdict("App/github", "Role/developer"))},
			}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, srv := newDraftsServer(t, tc.replies)
			t.Setenv("STRAZA_SERVER", "")
			stdout, _, code, err := runDrafts(t, writeCreds(t, srv.URL), "", append([]string{"drafts"}, tc.args...)...)
			if code != 0 {
				t.Errorf("exit %d (%v), want 0", code, err)
			}
			if !strings.HasSuffix(stdout, tc.wantEnd) {
				t.Errorf("stdout does not end with %q:\n%s", tc.wantEnd, stdout)
			}
		})
	}
}

// TestDraftsIntakeRefusalEndsWithTheCommand pins the end of a refusal at
// intake, where nothing was stored: after the refused lines, the command to
// run again once the files are fixed, exit 1 and nothing on stderr. A
// refusal of the note or of the open-draft limit alone is no fault of the
// files, so its end says only that nothing was stored.
func TestDraftsIntakeRefusalEndsWithTheCommand(t *testing.T) {
	file := writeFile(t, t.TempDir(), "github.yaml", "kind: App\n")
	const (
		document = `{"key":"k-dup","code":"bundle.duplicate","class":"refused","object":"App/github","sentence":"The draft names App/github twice.",` +
			`"fix":"Keep one document for each object and send the draft again."}`
		note = `{"key":"k-note","code":"draft.note-size","class":"refused","object":"Note","sentence":"The note holds 2,100 bytes, and a note holds at most 2,000 bytes.",` +
			`"fix":"Shorten it and send the draft again."}`
		limit = `{"key":"k-limit","code":"draft.open-limit","class":"refused","sentence":"admin already has 10 open drafts, and a proposer may keep at most 10.",` +
			`"fix":"Publish, discard or wait for one of them, then send this draft again."}`
	)
	refusal := func(findings ...string) draftReply {
		return draftReply{code: http.StatusUnprocessableEntity, body: `{"error":"a sentence","findings":[` + strings.Join(findings, ",") + `]}`}
	}
	tests := []struct {
		name    string
		args    []string
		replies map[string]draftReply
		wantEnd string
	}{
		{name: "update refused for a document", args: []string{"update", "41", "-f", file},
			replies: map[string]draftReply{"GET /v1/admin/drafts/41": {body: fixDetail("open", "", "", true, "")}, "PUT /v1/admin/drafts/41": refusal(document)},
			wantEnd: "\nNothing was stored. Fix the files and run strazactl drafts update 41 -f " + file + " again.\n"},
		{name: "create refused for the note and a document", args: []string{"create", "-f", file},
			replies: map[string]draftReply{"POST /v1/admin/drafts": refusal(note, document)},
			wantEnd: "\nNothing was stored. Fix the files and run strazactl drafts create -f " + file + " again.\n"},
		{name: "create refused for the note alone", args: []string{"create", "-f", file},
			replies: map[string]draftReply{"POST /v1/admin/drafts": refusal(note)},
			wantEnd: "  refused   Note The note holds 2,100 bytes, and a note holds at most 2,000 bytes. Shorten it and send the draft again.\nNothing was stored.\n"},
		{name: "create refused for the open-draft limit alone", args: []string{"create", "-f", file},
			replies: map[string]draftReply{"POST /v1/admin/drafts": refusal(limit)},
			wantEnd: "Publish, discard or wait for one of them, then send this draft again.\nNothing was stored.\n"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, srv := newDraftsServer(t, tc.replies)
			t.Setenv("STRAZA_SERVER", "")
			stdout, _, code, err := runDrafts(t, writeCreds(t, srv.URL), "", append([]string{"drafts"}, tc.args...)...)
			if code != 1 || errText(err) != "" {
				t.Errorf("exit %d, err %v, want exit 1 and nothing more on stderr", code, err)
			}
			if !strings.HasSuffix(stdout, tc.wantEnd) {
				t.Errorf("stdout does not end with %q:\n%s", tc.wantEnd, stdout)
			}
		})
	}
}

// TestDraftsNamesWhereARefusalSits pins the line under a refusal of the
// bundle reader. The server numbers the documents across every text it is
// sent, and strazactl sends one text per file, so with several files the
// line names the document's place in its file, or in what stdin held, and
// its kind and name when they read. A refusal of a whole text, and any
// refusal when one file was sent, names the file alone. The line follows
// the document number the refusal carries, so two refusals that read alike
// in two files each name their own, and so does the name refusal intake
// makes after a refused document. The fake server answers what the
// server's own bundle reader and intake make of the texts.
func TestDraftsNamesWhereARefusalSits(t *testing.T) {
	dir := t.TempDir()
	bundle := filepath.Join(dir, "bundle")
	texts := map[string]string{
		"a":     "metadata:\n  name: one\n---\nkind: Role\nmetadata:\n  name: two\n",
		"b":     "kind: [Role\n",
		"c":     "kind: Role\n--- # two documents where Straza reads one\nkind: Role\n",
		"one":   "kind: [App\n",
		"stdin": "apiVersion: straza.dev/v1beta1\nkind: Group\nmetadata:\n  name: g\n---\nkind: Removal\nmetadata:\n  name: x\nspec:\n  kind: App\n",
		"p1":    maybeSet("pone"),
		"p2":    "apiVersion: straza.dev/v1beta1\nkind: Removal\nmetadata:\n  name: old\nspec:\n  kind: App\n---\n" + maybeSet("ptwo"),
		"odd":   "apiVersion: straza.dev/v1beta1\nkind: App\nmetadata:\n  name: lf-o1f\n---\napiVersion: straza.dev/v1beta1\nkind: Role\nmetadata:\n  name: dev\u200b\nspec:\n  kind: business\n",
	}
	a, b := writeFile(t, bundle, "a.yaml", texts["a"]), writeFile(t, bundle, "b.yaml", texts["b"])
	c, one := writeFile(t, bundle, "c.yaml", texts["c"]), writeFile(t, dir, "github.yaml", texts["one"])
	p1, p2 := writeFile(t, dir, "p1.yaml", texts["p1"]), writeFile(t, dir, "p2.yaml", texts["p2"])
	odd := writeFile(t, dir, "odd.yaml", texts["odd"])
	tests := []struct {
		name  string
		args  []string
		texts []string
		lines []string
	}{
		{name: "several files", args: []string{"-f", bundle}, texts: []string{texts["a"], texts["b"], texts["c"]}, lines: []string{
			"in the first document of " + a, "in the second document of " + a + ", a Role named two", "in the first document of " + b, "in " + c}},
		{name: "one file", args: []string{"-f", one}, texts: []string{texts["one"]}, lines: []string{"in " + one}},
		{name: "stdin beside a file", args: []string{"-f", "-", "-f", b}, texts: []string{texts["stdin"], texts["b"]}, lines: []string{
			"in the first document read from stdin, a Group named g", "in the second document read from stdin, a Removal named x", "in the first document of " + b}},
		{name: "the same refusal in two files", args: []string{"-f", p1, "-f", p2}, texts: []string{texts["p1"], texts["p2"]}, lines: []string{
			"in the first document of " + p1 + ", a PolicySet named pone", "in the second document of " + p2 + ", a PolicySet named ptwo"}},
		{name: "a name refusal after a refused document", args: []string{"-f", b, "-f", odd}, texts: []string{texts["b"], texts["odd"]}, lines: []string{
			"in the first document of " + b, "in the second document of " + odd + ", a Role named devU+200B"}},
	}
	for _, tc := range tests {
		items, findings := drafts.ParseBundle(tc.texts)
		for _, f := range drafts.Intake(drafts.Draft{Items: items}, drafts.Principal{Username: "alice"}) {
			if f.Code == "bundle.name-characters" {
				findings = append(findings, f)
			}
		}
		if len(findings) != len(tc.lines) {
			t.Fatalf("%s: the bundle reader answered %d refusals, want %d: %+v", tc.name, len(findings), len(tc.lines), findings)
		}
		fs, err := json.Marshal(findings)
		if err != nil {
			t.Fatal(err)
		}
		for _, verb := range []string{"check", "create"} {
			t.Run(tc.name+" "+verb, func(t *testing.T) {
				route, reply := "POST /v1/admin/drafts", draftReply{code: http.StatusUnprocessableEntity, body: `{"error":"a sentence","findings":` + string(fs) + `}`}
				if verb == "check" {
					route, reply = route+"/check", draftReply{body: `{"items":[],"verdict":{"checked_at":"2026-09-24T10:20:11Z","refused":` + string(fs) +
						`,"risks":[],"warnings":[],"unchecked":[],"passed":[],"info":[],"gains":[],"needs":[]}}`}
				}
				_, srv := newDraftsServer(t, map[string]draftReply{route: reply})
				t.Setenv("STRAZA_SERVER", "")
				stdout, _, code, err := runDrafts(t, writeCreds(t, srv.URL), texts["stdin"], append([]string{"drafts", verb}, tc.args...)...)
				if code != 1 {
					t.Errorf("exit %d (%v), want 1", code, err)
				}
				for i, f := range findings {
					line := "  refused   " + strings.TrimSpace(f.Object+" "+f.Sentence+" "+f.Fix)
					if want := line + "\n  " + tc.lines[i] + "\n"; !strings.Contains(stdout, want) {
						t.Errorf("stdout lacks %q:\n%s", want, stdout)
					}
				}
			})
		}
	}
}

// maybeSet is a PolicySet document named name whose rule has an effect the
// policy parser refuses, so two of them draw refusals that read alike.
func maybeSet(name string) string {
	return "apiVersion: straza.dev/v1beta1\nkind: PolicySet\nmetadata:\n  name: " + name +
		"\nspec:\n  priority: 10\n  match: { roles: [dev] }\n  rules:\n    - id: r1\n      tools: [mcp.call]\n      effect: maybe\n"
}

// TestPlaceWords pins the words of a place: the ordinal in words to tenth
// and in digits past it, the article before the kind, and the short form
// for a whole text or a bundle of one text.
func TestPlaceWords(t *testing.T) {
	tests := []struct {
		p       drafts.Place
		name    string
		several bool
		want    string
	}{
		{drafts.Place{Doc: 10, Kind: "App", Name: "github"}, "a.yaml", true, "the tenth document of a.yaml, an App named github"},
		{drafts.Place{Doc: 11, Kind: "Role"}, "a.yaml", true, "the 11th document of a.yaml, a Role"},
		{drafts.Place{Doc: 22}, "stdin", true, "the 22nd document read from stdin"},
		{drafts.Place{Doc: 113, Kind: "PolicySet", Name: "p"}, "a.yaml", true, "the 113th document of a.yaml, a PolicySet named p"},
		{drafts.Place{Doc: 3, Kind: "Role", Name: "dev"}, "a.yaml", false, "a.yaml"},
		{drafts.Place{Text: 1}, "b.yaml", true, "b.yaml"},
	}
	for _, tc := range tests {
		if got := placeWords(tc.p, tc.name, tc.several); got != tc.want {
			t.Errorf("placeWords(%+v, %q, %v) = %q, want %q", tc.p, tc.name, tc.several, got, tc.want)
		}
	}
}

// TestDraftsUpdateNamesWhatLeft pins the line update prints under the new
// revision's items: the objects of the revision it read that the new one
// no longer holds, in the order that revision held them.
func TestDraftsUpdateNamesWhatLeft(t *testing.T) {
	file := writeFile(t, t.TempDir(), "github.yaml", "kind: App\n")
	read := `[{"kind":"App","name":"github","op":"put","doc":"kind: App\n","existed":false},` +
		`{"kind":"Role","name":"github-readers","op":"put","doc":"kind: Role\n","existed":false},` +
		`{"kind":"Role","name":"github-writers","op":"put","doc":"kind: Role\n","existed":false},` +
		`{"kind":"PolicySet","name":"github-writers-access","op":"put","doc":"kind: PolicySet\n","existed":false}]`
	kept := `[{"kind":"App","name":"github","op":"put","doc":"kind: App\n","existed":false}]`
	tests := []struct {
		name, items, want, notWant string
	}{
		{name: "three objects left", items: kept,
			want: "  +   App/github\nLeft the draft: Role/github-readers, Role/github-writers, PolicySet/github-writers-access.\n"},
		{name: "every object stayed", items: read, want: "  +   PolicySet/github-writers-access\n", notWant: "Left the draft"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, srv := newDraftsServer(t, map[string]draftReply{
				"GET /v1/admin/drafts/41": {body: `{"draft":` + fixDraft("41", 2, "open", "strazactl", read) + `,"verdict":` + fixVerdict("", "") +
					`,"revisions":[],"live":{},"may_publish":true}`},
				"PUT /v1/admin/drafts/41": {body: `{"draft":` + fixDraft("41", 3, "open", "strazactl", tc.items) + `,"verdict":` + fixVerdict("", "") + `}`},
			})
			t.Setenv("STRAZA_SERVER", "")
			stdout, _, code, err := runDrafts(t, writeCreds(t, srv.URL), "", "drafts", "update", "41", "-f", file)
			if code != 0 {
				t.Errorf("exit %d (%v), want 0", code, err)
			}
			wantInOrder(t, stdout, "Draft 41 is at revision 3.", tc.want)
			if tc.notWant != "" && strings.Contains(stdout, tc.notWant) {
				t.Errorf("stdout carries %q:\n%s", tc.notWant, stdout)
			}
		})
	}
}
