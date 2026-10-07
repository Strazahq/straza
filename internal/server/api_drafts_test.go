package server

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/strazahq/straza/internal/config"
	"github.com/strazahq/straza/internal/drafts"
	"github.com/strazahq/straza/internal/redact"
	"github.com/strazahq/straza/internal/store"
)

// wireFinding is a finding as the drafts routes answer it.
type wireFinding struct {
	Code     string `json:"code"`
	Class    string `json:"class"`
	Object   string `json:"object"`
	Sentence string `json:"sentence"`
	Fix      string `json:"fix"`
	Key      string `json:"key"`
}

// wireVerdict is a verdict as the drafts routes answer it.
type wireVerdict struct {
	Draft     string        `json:"draft"`
	Revision  int           `json:"revision"`
	Snapshot  string        `json:"snapshot"`
	CheckedAt string        `json:"checked_at"`
	Refused   []wireFinding `json:"refused"`
	Risks     []wireFinding `json:"risks"`
	Warnings  []wireFinding `json:"warnings"`
	Unchecked []wireFinding `json:"unchecked"`
	Passed    []wireFinding `json:"passed"`
	Info      []wireFinding `json:"info"`
	Gains     []drafts.Gain `json:"gains"`
	Needs     []drafts.Need `json:"needs"`
}

// wireItem is an item as the drafts routes answer it.
type wireItem struct {
	Kind     string `json:"kind"`
	Name     string `json:"name"`
	Op       string `json:"op"`
	Doc      string `json:"doc"`
	Base     string `json:"base"`
	Withheld string `json:"withheld"`
	Existed  bool   `json:"existed"`
}

// wireDraft is a draft as the drafts routes answer it.
type wireDraft struct {
	ID            string             `json:"id"`
	Revision      int                `json:"revision"`
	State         string             `json:"state"`
	Door          string             `json:"door"`
	Note          string             `json:"note"`
	Authors       []drafts.Principal `json:"authors"`
	Items         []wireItem         `json:"items"`
	Reverts       string             `json:"reverts"`
	Title         string             `json:"title"`
	Working       bool               `json:"working"`
	CreatedAt     string             `json:"created_at"`
	UpdatedAt     string             `json:"updated_at"`
	ExpiresAt     string             `json:"expires_at"`
	DecidedAt     string             `json:"decided_at"`
	DecidedBy     *drafts.Principal  `json:"decided_by"`
	DecidedReason string             `json:"decided_reason"`
}

// wireAnswer is what a drafts route that writes or checks answers: a draft
// with its verdict, the items of a check, or a refusal with its findings.
type wireAnswer struct {
	Draft    wireDraft     `json:"draft"`
	Verdict  wireVerdict   `json:"verdict"`
	Items    []wireItem    `json:"items"`
	Error    string        `json:"error"`
	Findings []wireFinding `json:"findings"`
}

// draftsFixture is a test app for the drafts routes. kim holds the root
// role, and the remote servers github and jira are live. erin administers
// github only, ada's role maps to drafts:read and drafts:write alone, and
// nell's to every read grant, identity:write and drafts:read. bot is an
// agent sponsored by kim with the drafts grants, and token is an admin API
// token that may draft any object.
type draftsFixture struct {
	app                         *App
	base                        string
	kim                         store.User
	root, strazactl             string
	erin, ada, nell, bot, token string
	tokenID                     string
	github, jira                store.App
}

// newDraftsFixture builds the drafts fixture, running preRun on the app
// before it serves.
func newDraftsFixture(t *testing.T, preRun []func(*App), mutators ...func(*config.Config)) *draftsFixture {
	t.Helper()
	app, base := testAppPreRun(t, preRun, append([]func(*config.Config){func(c *config.Config) {
		c.Admin.RoleAreas = map[string][]string{
			"draft-reviewers": {"drafts:read", "drafts:write"},
			"id-admins":       {"identity:read", "identity:write", "apps:read", "policy:read", "drafts:read"},
		}
	}}, mutators...)...)
	ctx := context.Background()
	f := &draftsFixture{app: app, base: base}
	f.kim = seedIdentity(t, app)
	grantAdmin(t, app, f.kim.ID)
	f.github = putServer(t, app, draftApp("github", "https://api.github.example/mcp?region=eu", "The GitHub server."))
	f.jira = putServer(t, app, draftApp("jira", "https://jira.example/mcp", "The Jira server."))
	for _, name := range []string{"draft-reviewers", "id-admins"} {
		if _, err := app.store.Roles().Create(ctx, store.Role{Name: name}); err != nil {
			t.Fatal(err)
		}
	}
	mkHuman(t, app, "erin", f.adminRole(t, f.github))
	mkHuman(t, app, "ada", "draft-reviewers")
	mkHuman(t, app, "nell", "id-admins")
	seedAgent(t, app, "bot", "kim", "draft-reviewers")
	login := func(name string) string { return loginDeviceFlow(t, base, name, "hunter2!") }
	f.root, f.erin, f.ada, f.nell, f.bot = login("kim"), login("erin"), login("ada"), login("nell"), login("bot")
	f.strazactl, _ = checkinTokenAs(t, base, "strazactl")
	var minted struct {
		ID    string `json:"id"`
		Token string `json:"token"`
	}
	if code := adminReq(t, http.MethodPost, base+"/v1/admin/api-tokens", f.root,
		map[string]any{"name": "drafter", "scope": "drafts:read,drafts:write,apps:read,identity:read,policy:read"}, &minted); code != http.StatusCreated {
		t.Fatalf("mint = %d", code)
	}
	f.token, f.tokenID = minted.Token, minted.ID
	return f
}

// adminRole names the admin role of server.
func (f *draftsFixture) adminRole(t *testing.T, server store.App) string {
	t.Helper()
	role, err := f.app.store.Roles().GetByID(context.Background(), server.AdminRoleID)
	if err != nil {
		t.Fatal(err)
	}
	return role.Name
}

// call sends body as JSON to the drafts route path as bearer and answers
// the status and the decoded answer.
func (f *draftsFixture) call(t *testing.T, method, path, bearer string, body any) (int, wireAnswer) {
	t.Helper()
	var raw []byte
	if body != nil {
		raw, _ = json.Marshal(body)
	}
	code, out, _ := adminBytes(t, method, f.base+path, bearer, "application/json", raw)
	var a wireAnswer
	if len(out) > 0 {
		if err := json.Unmarshal(out, &a); err != nil {
			t.Fatalf("%s %s: %v in %s", method, path, err, out)
		}
	}
	return code, a
}

// create stores a draft of documents as bearer and fails unless it is 201.
func (f *draftsFixture) create(t *testing.T, bearer string, documents ...string) wireAnswer {
	t.Helper()
	code, a := f.call(t, http.MethodPost, "/v1/admin/drafts", bearer, map[string]any{"documents": documents})
	if code != http.StatusCreated {
		t.Fatalf("create = %d %q %v", code, a.Error, a.Findings)
	}
	return a
}

// drafts counts every draft the store holds.
func (f *draftsFixture) drafts(t *testing.T) int {
	t.Helper()
	rows, err := f.app.store.Drafts().List(context.Background(), store.DraftFilter{}, 0, 200)
	if err != nil {
		t.Fatal(err)
	}
	return len(rows)
}

// stored reads the draft id from the store with its items.
func (f *draftsFixture) stored(t *testing.T, id string) (store.DraftRow, []store.DraftItemRow) {
	t.Helper()
	n, err := strconv.ParseInt(id, 10, 64)
	if err != nil {
		t.Fatalf("draft id %q: %v", id, err)
	}
	row, items, err := f.app.store.Drafts().Get(context.Background(), n)
	if err != nil {
		t.Fatal(err)
	}
	return row, items
}

// draftApp is the App document of the remote server name at url, with one
// header value in its server block.
func draftApp(name, url, description string) string {
	return fmt.Sprintf(`apiVersion: straza.dev/v1beta1
kind: App
metadata:
  name: %s
  description: %q
server:
  name: example.com/%s
  version: "1.0.0"
  remotes:
    - url: %s
      headers:
        - {name: X-Team, value: team-a}
straza:
  runtime:
    kind: remote
    remote:
      url: %s
`, name, description, name, url, url)
}

// draftRole is the Role document of the global business role name.
func draftRole(name, description string) string {
	return fmt.Sprintf("apiVersion: straza.dev/v1beta1\nkind: Role\nmetadata:\n    name: %s\nspec:\n    kind: business\n    description: %s\n", name, description)
}

// draftSet is the PolicySet document name that denies shell calls to dev.
func draftSet(name string) string {
	return fmt.Sprintf(`apiVersion: straza.dev/v1beta1
kind: PolicySet
metadata: {name: %s}
spec:
  priority: 10
  match: {roles: [dev]}
  rules:
    - id: no-shell
      events: [tool.pre]
      tools: [shell.exec]
      effect: deny
      reason: "Straza: no shell for dev"
`, name)
}

// liveOf is the live fingerprint and canonical document of the role name.
func liveOf(t *testing.T, app *App, name string) (string, string) {
	t.Helper()
	w, err := app.readWorld(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	doc, ok := drafts.RoleDocOf(w, name)
	if !ok {
		t.Fatalf("no live role %s", name)
	}
	text, err := doc.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	return store.FingerprintRole(roleConfig(w, name)), string(text)
}

// TestCreateDraftStoresRevisionOneWithItsCheck pins create: revision
// 1 is stored with its check, every item stamped with its object as live
// state holds it, the door taken from the credential, and a verdict whose
// every finding carries its key.
func TestCreateDraftStoresRevisionOneWithItsCheck(t *testing.T) {
	t.Parallel()
	f := newDraftsFixture(t, nil)
	code, a := f.call(t, http.MethodPost, "/v1/admin/drafts", f.strazactl, map[string]any{
		"documents": []string{draftApp("linear", "https://linear.example/mcp", "The Linear server.")},
		"items":     []map[string]string{{"kind": "Role", "name": "dev", "op": "put", "doc": draftRole("dev", "Developers.")}},
		"note":      "Linear for the developers.",
	})
	if code != http.StatusCreated {
		t.Fatalf("create = %d %q %v", code, a.Error, a.Findings)
	}
	d := a.Draft
	if _, err := strconv.ParseInt(d.ID, 10, 64); err != nil || d.Revision != 1 || d.State != "open" || d.Door != "strazactl" {
		t.Fatalf("draft = %q revision %d %s %s, want a decimal id at revision 1, open, through strazactl", d.ID, d.Revision, d.State, d.Door)
	}
	want := drafts.Principal{UserID: f.kim.ID, Username: "kim", Via: laneSession, Client: "strazactl"}
	if len(d.Authors) != 1 || d.Authors[0] != want {
		t.Errorf("authors = %+v, want %+v", d.Authors, want)
	}
	if d.Title != "Add server linear and then change role dev" || d.Note != "Linear for the developers." {
		t.Errorf("title %q note %q", d.Title, d.Note)
	}
	row, items := f.stored(t, d.ID)
	fp, doc := liveOf(t, f.app, "dev")
	byName := map[string]store.DraftItemRow{}
	for _, it := range items {
		byName[it.Kind+"/"+it.Name] = it
	}
	if it := byName["App/linear"]; it.Base != "" || it.BaseOp != "remove" || it.BaseDoc != "" {
		t.Errorf("App/linear is stamped %q %q %q, want a new object", it.Base, it.BaseOp, it.BaseDoc)
	}
	if it := byName["Role/dev"]; it.Base != fp || it.BaseOp != "put" || it.BaseDoc != doc {
		t.Errorf("Role/dev is stamped %q %q, want the live fingerprint %q and the live document", it.Base, it.BaseOp, fp)
	}
	v := a.Verdict
	counts, _ := json.Marshal(map[string]int{"refused": len(v.Refused), "risks": len(v.Risks), "warnings": len(v.Warnings), "unchecked": len(v.Unchecked)})
	if row.CheckedRevision != 1 || row.CheckCounts != string(counts) || row.CheckedAt == nil {
		t.Errorf("stored check = revision %d counts %s, want revision 1 and %s", row.CheckedRevision, row.CheckCounts, counts)
	}
	if v.Draft != d.ID || v.Revision != 1 {
		t.Errorf("verdict names draft %q revision %d, want %s at 1", v.Draft, v.Revision, d.ID)
	}
	for _, list := range [][]wireFinding{v.Refused, v.Risks, v.Warnings, v.Unchecked, v.Passed, v.Info} {
		for _, fnd := range list {
			if len(fnd.Key) != 64 {
				t.Errorf("%s %s carries the key %q", fnd.Code, fnd.Object, fnd.Key)
			}
		}
	}
}

// runnerApp is the App document of the command server runner, with extra
// appended under its command block.
func runnerApp(extra string) string {
	return "apiVersion: straza.dev/v1beta1\nkind: App\nmetadata:\n  name: runner\nserver:\n  name: io.x/runner\n  version: 1.0.0\n" +
		"straza:\n  runtime:\n    kind: command\n    command:\n      exec: /usr/bin/runner\n" + extra
}

// fakeToken is a string in the shape of a GitHub token, built at run time
// so the source holds no credential-shaped literal.
var fakeToken = "gh" + "p_" + strings.Repeat("A1b2C3d4", 5)

// TestStampKeepsNoStoredSecret pins the stamp of an App: a
// server whose stored manifest holds a secret, as a manifest stored before
// the secret scan may, is stamped with that manifest masked, on a change
// and on a removal alike, and keeps the live fingerprint as its base, so the
// undo of the published change is refused. A manifest with no secret is
// stamped as it is stored.
func TestStampKeepsNoStoredSecret(t *testing.T) {
	t.Parallel()
	f := newDraftsFixture(t, nil)
	for _, secret := range []struct{ name, stored, value string }{
		{"a token in an argument and an env value", runnerApp("      args: [--token, " + fakeToken + "]\n      env:\n        - {name: GITHUB_TOKEN, value: " + fakeToken + "}\n"), fakeToken},
		{"a password in the argument after a flag that names it", runnerApp("      args: [--password, Hunter2Hunter2]\n"), "Hunter2Hunter2"},
	} {
		stored := putServer(t, f.app, secret.stored)
		fp, err := store.FingerprintApp(stored.Manifest)
		if err != nil {
			t.Fatal(err)
		}
		change := ""
		for _, tc := range []struct{ name, doc string }{
			{"a change that takes the secret out", runnerApp("")},
			{"a removal", removalDoc("App", "runner")},
		} {
			id := f.create(t, f.root, tc.doc).Draft.ID
			if change == "" {
				change = id
			}
			_, items := f.stored(t, id)
			if it := items[0]; strings.Contains(it.BaseDoc, secret.value) || !strings.Contains(it.BaseDoc, redact.Mark) || it.Base != fp || it.BaseOp != "put" {
				t.Errorf("%s: %s is stamped %q %q with the document %s, want the live fingerprint %q and the manifest masked", secret.name, tc.name, it.Base, it.BaseOp, it.BaseDoc, fp)
			}
		}
		publishStored(t, f, change)
		before := f.drafts(t)
		if code, a := f.call(t, http.MethodPost, "/v1/admin/drafts/"+change+"/revert", f.root, nil); code != http.StatusUnprocessableEntity ||
			!strings.Contains(a.Error, "whose stored manifest held a secret that a draft never keeps") {
			t.Errorf("%s: the undo of the change = %d %q, want 422 with the masked-manifest sentence", secret.name, code, a.Error)
		}
		if after := f.drafts(t); after != before {
			t.Errorf("%s: the store holds %d drafts after the undo, want %d", secret.name, after, before)
		}
	}
	jira, err := f.app.store.Apps().GetByName(context.Background(), "jira")
	if err != nil {
		t.Fatal(err)
	}
	_, items := f.stored(t, f.create(t, f.root, draftApp("jira", "https://jira.example/mcp", "A new description.")).Draft.ID)
	if items[0].BaseDoc != jira.Manifest {
		t.Errorf("jira is stamped with the document %s, want its stored manifest %s", items[0].BaseDoc, jira.Manifest)
	}
}

// TestMaskedArgumentOrPathSentBackIsRefused pins bundle.masked for the
// masks drafts.WithoutSecrets leaves in a route's answer, an argument and a
// segment of an address path reading redact.Mark, on every route that takes
// documents: create and update answer 422, check answers the refusal in its
// verdict, and an undo whose change record carries one is refused with
// nothing stored. The revert's own guard answers first for a literal mark,
// so the undo is fired with the percent-encoded segment it does not see.
func TestMaskedArgumentOrPathSentBackIsRefused(t *testing.T) {
	t.Parallel()
	f := newDraftsFixture(t, nil)
	own := f.create(t, f.root, draftRole("helpers", "Helpers."))
	codes := func(a wireAnswer) []string {
		var out []string
		for _, fnd := range append(a.Findings, a.Verdict.Refused...) {
			out = append(out, fnd.Code)
		}
		return out
	}
	for _, tc := range []struct{ name, stored, masked, clean string }{
		{"a masked argument", runnerApp("      args: [--seed, '[REDACTED]']\n"), runnerApp("      args: [--seed, '[REDACTED]']\n"), runnerApp("")},
		{"a masked segment in an address path", draftApp("hook", "https://hooks.example.com/services/T0AB12CD3/%5BREDACTED%5D", "Hook."),
			draftApp("hook", "https://hooks.example.com/services/T0AB12CD3/[REDACTED]", "Hook."), draftApp("hook", "https://hooks.example.com/v2/mcp", "Hook.")},
	} {
		for _, door := range []struct {
			name, method, path string
			status             int
		}{
			{"create", http.MethodPost, "/v1/admin/drafts", http.StatusUnprocessableEntity},
			{"update", http.MethodPut, "/v1/admin/drafts/" + own.Draft.ID, http.StatusUnprocessableEntity},
			{"check", http.MethodPost, "/v1/admin/drafts/check", http.StatusOK},
		} {
			before := f.drafts(t)
			code, a := f.call(t, door.method, door.path, f.root, map[string]any{"revision": 1, "documents": []string{tc.masked}})
			if code != door.status || !slices.Contains(codes(a), "bundle.masked") {
				t.Errorf("%s sent to %s = %d %q, want %d with bundle.masked", tc.name, door.name, code, codes(a), door.status)
			}
			if after := f.drafts(t); after != before {
				t.Errorf("%s sent to %s: the store holds %d drafts, want %d", tc.name, door.name, after, before)
			}
		}
		putServer(t, f.app, tc.stored)
		change := f.create(t, f.root, tc.clean).Draft.ID
		publishStored(t, f, change)
		before := f.drafts(t)
		code, a := f.call(t, http.MethodPost, "/v1/admin/drafts/"+change+"/revert", f.root, nil)
		refused := slices.Contains(codes(a), "bundle.masked") || strings.Contains(a.Error, "whose stored manifest held a secret that a draft never keeps")
		if code != http.StatusUnprocessableEntity || !refused {
			t.Errorf("%s: the undo = %d %q %q, want 422 refused", tc.name, code, a.Error, codes(a))
		}
		if after := f.drafts(t); after != before {
			t.Errorf("%s: the store holds %d drafts after the undo, want %d", tc.name, after, before)
		}
		if tc.name == "a masked segment in an address path" && !strings.Contains(a.Error, "whose stored manifest held a secret that a draft never keeps") {
			t.Errorf("the undo of an address with a masked segment answered %q %q, want the revert guard's sentence", a.Error, codes(a))
		}
	}
	if row, _ := f.stored(t, own.Draft.ID); row.Revision != 1 {
		t.Errorf("the draft sent the masked documents is at revision %d, want 1", row.Revision)
	}
}

// TestDraftEmptyOnlyWhenNothingWasSent pins draft.empty
// on create, check and update: a body that sends no document draws it, and
// a body whose every document the bundle refuses draws the bundle's refusal
// without it, because a document was sent.
func TestDraftEmptyOnlyWhenNothingWasSent(t *testing.T) {
	t.Parallel()
	f := newDraftsFixture(t, nil)
	own := f.create(t, f.root, draftRole("helpers", "Helpers."))
	codes := func(a wireAnswer) []string {
		var out []string
		for _, fnd := range append(a.Findings, a.Verdict.Refused...) {
			out = append(out, fnd.Code)
		}
		return out
	}
	for _, tc := range []struct{ name, method, path string }{
		{"create", http.MethodPost, "/v1/admin/drafts"},
		{"check", http.MethodPost, "/v1/admin/drafts/check"},
		{"update", http.MethodPut, "/v1/admin/drafts/" + own.Draft.ID},
	} {
		if _, a := f.call(t, tc.method, tc.path, f.root, map[string]any{"revision": 1}); !slices.Contains(codes(a), "draft.empty") {
			t.Errorf("%s with no document answered %q, want draft.empty", tc.name, codes(a))
		}
		_, a := f.call(t, tc.method, tc.path, f.root, map[string]any{"revision": 1, "documents": []string{"kind: [unclosed"}})
		if got := codes(a); !slices.Contains(got, "bundle.yaml") || slices.Contains(got, "draft.empty") {
			t.Errorf("%s with a document that is not YAML answered %q, want bundle.yaml and no draft.empty", tc.name, got)
		}
	}
}

// TestCreateDraftRefusesAtIntakeAndStoresNothing pins the 422 of create:
// every intake refusal answers the first finding's sentence and fix with
// every finding keyed, and nothing is stored.
func TestCreateDraftRefusesAtIntakeAndStoresNothing(t *testing.T) {
	t.Parallel()
	f := newDraftsFixture(t, nil)
	ctx := context.Background()
	for i := range 10 {
		if _, err := f.app.store.Drafts().Create(ctx, store.DraftRow{Door: "api"},
			[]store.DraftItemRow{{Kind: "Role", Name: fmt.Sprintf("gone-%d", i), Op: "remove"}},
			store.DraftRevisionRow{Author: store.DraftActor{ID: f.tokenID, Name: "drafter", Via: laneAdminAPI, Client: clientAdminAPI}}); err != nil {
			t.Fatal(err)
		}
	}
	masked := strings.Replace(draftApp("linear", "https://linear.example/mcp", "x"), "value: team-a", `value: "[REDACTED]"`, 1)
	noURL := "apiVersion: straza.dev/v1beta1\nkind: App\nmetadata:\n  name: broken\nserver:\n  name: example.com/broken\n  version: 1.0.0\nstraza:\n  runtime:\n    kind: remote\n"
	cases := []struct {
		name   string
		bearer string
		body   map[string]any
		code   string
	}{
		{"an address that carries a password", f.root,
			map[string]any{"documents": []string{draftApp("linear", "https://bot:Hunter2Hunter2@linear.example/mcp", "x")}}, "secret.userinfo"},
		{"a kind a draft does not hold", f.root,
			map[string]any{"documents": []string{"apiVersion: straza.dev/v1beta1\nkind: Pack\nmetadata:\n  name: p\n"}}, "bundle.kind"},
		{"a manifest the parser refuses", f.root, map[string]any{"documents": []string{noURL}}, "app.parse"},
		{"a note with a direction override", f.root,
			map[string]any{"documents": []string{draftRole("helpers", "Helpers.")}, "note": "ok \u202e evil"}, "draft.note-characters"},
		{"a document read back masked", f.root, map[string]any{"documents": []string{masked}}, "bundle.masked"},
		{"the eleventh open draft", f.token, map[string]any{"documents": []string{draftRole("helpers", "Helpers.")}}, "draft.open-limit"},
	}
	before := f.drafts(t)
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			code, a := f.call(t, http.MethodPost, "/v1/admin/drafts", tc.bearer, tc.body)
			if code != http.StatusUnprocessableEntity || len(a.Findings) == 0 {
				t.Fatalf("create = %d %q, want 422 with findings", code, a.Error)
			}
			first := a.Findings[0]
			if want := strings.TrimSpace(first.Sentence + " " + first.Fix); a.Error != want {
				t.Errorf("error = %q, want the first finding's sentence and fix %q", a.Error, want)
			}
			found := false
			for _, fnd := range a.Findings {
				found = found || fnd.Code == tc.code
				if len(fnd.Key) != 64 {
					t.Errorf("%s carries the key %q", fnd.Code, fnd.Key)
				}
			}
			if !found {
				t.Errorf("findings %+v hold no %s", a.Findings, tc.code)
			}
		})
	}
	if after := f.drafts(t); after != before {
		t.Errorf("the store holds %d drafts after the refusals, want %d", after, before)
	}
}

// TestCreateDraftAnswersAMalformedBody pins the 400 of a body that is not
// JSON, and the 413 of a body over server.maxBodyBytes.
func TestCreateDraftAnswersAMalformedBody(t *testing.T) {
	t.Parallel()
	app, base := testApp(t, func(c *config.Config) { c.Server.MaxBodyBytes = 4096 })
	kim := seedIdentity(t, app)
	grantAdmin(t, app, kim.ID)
	root := loginDeviceFlow(t, base, "kim", "hunter2!")
	cases := []struct {
		name string
		body string
		code int
		says string
	}{
		{"a body that is not JSON", "{", http.StatusBadRequest,
			"The request body is not a draft: unexpected EOF. Send documents, items or both as JSON."},
		{"a body over the cap", `{"note": "` + strings.Repeat("x", 5000) + `"}`, http.StatusRequestEntityTooLarge,
			"The request body holds more than 4096 bytes, the most strazad reads in one request (server.maxBodyBytes). " +
				"Split the draft into smaller drafts, or raise server.maxBodyBytes."},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			code, out, _ := adminBytes(t, http.MethodPost, base+"/v1/admin/drafts", root, "application/json", []byte(tc.body))
			var got struct {
				Error string `json:"error"`
			}
			_ = json.Unmarshal(out, &got)
			if code != tc.code || got.Error != tc.says {
				t.Errorf("create = %d %q, want %d %q", code, got.Error, tc.code, tc.says)
			}
		})
	}
}

// TestCreateDraftStoresItemsFormRolesCanonical pins that a Role put of the
// items form is stored in the export's form, and that a base the client
// sends is ignored, because the server stamps every item it stores.
func TestCreateDraftStoresItemsFormRolesCanonical(t *testing.T) {
	t.Parallel()
	f := newDraftsFixture(t, nil)
	sent := "apiVersion: straza.dev/v1beta1\nkind: Role\nmetadata: {name: helpers}  # our helpers\nspec: {kind: business, description: Helpers.}\n"
	code, a := f.call(t, http.MethodPost, "/v1/admin/drafts", f.root, map[string]any{
		"items": []map[string]string{{"kind": "Role", "name": "helpers", "op": "put", "doc": sent, "base": "fp-from-the-client"}},
	})
	if code != http.StatusCreated {
		t.Fatalf("create = %d %q %v", code, a.Error, a.Findings)
	}
	doc, err := drafts.ParseRole(sent)
	if err != nil {
		t.Fatal(err)
	}
	canonical, err := doc.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	_, items := f.stored(t, a.Draft.ID)
	if len(items) != 1 || items[0].Doc != string(canonical) || items[0].Base != "" || items[0].BaseOp != "remove" {
		t.Errorf("stored %+v, want the canonical document %q stamped as a new role", items, canonical)
	}
}

// TestWorkingDraft pins working drafts: only a person keeps one, an
// agent never reaches the route, the first request creates it on its slot
// and a later one revises it, an item naming a held object keeps its base,
// and it never counts toward the open limit.
func TestWorkingDraft(t *testing.T) {
	t.Parallel()
	f := newDraftsFixture(t, nil)
	working := func(bearer string, items ...map[string]string) (int, wireAnswer) {
		return f.call(t, http.MethodPost, "/v1/admin/drafts", bearer, map[string]any{"items": items, "working": true})
	}
	linear := map[string]string{"kind": "App", "name": "linear", "op": "put", "doc": draftApp("linear", "https://linear.example/mcp", "One.")}
	for _, tc := range []struct {
		name, bearer string
		code         int
		says         string
	}{
		{"an admin API token", f.token, http.StatusBadRequest, "working drafts belong to people, and an admin API token is not one. Create a draft without working."},
		{"an agent", f.bot, http.StatusForbidden, fmt.Sprintf(nonPersonAdminRefusal, "bot", "an agent")},
	} {
		if code, a := working(tc.bearer, linear); code != tc.code || a.Error != tc.says {
			t.Errorf("%s: working = %d %q, want %d %q", tc.name, code, a.Error, tc.code, tc.says)
		}
	}
	dev := map[string]string{"kind": "Role", "name": "dev", "op": "put", "doc": draftRole("dev", "Developers.")}
	code, first := working(f.strazactl, linear, dev)
	if code != http.StatusCreated || !first.Draft.Working {
		t.Fatalf("first working = %d %q, want 201 and a working draft", code, first.Error)
	}
	row, _ := f.stored(t, first.Draft.ID)
	if row.Slot != "working:"+f.kim.ID {
		t.Errorf("slot = %q, want working:%s", row.Slot, f.kim.ID)
	}
	stamped, _ := liveOf(t, f.app, "dev")
	devRole, err := f.app.store.Roles().GetByName(context.Background(), "dev")
	if err != nil {
		t.Fatal(err)
	}
	setAccess(t, f.app, devRole.ID, f.github, true, `["*"]`)
	linear["doc"] = draftApp("linear", "https://linear.example/mcp", "Two.")
	jira := map[string]string{"kind": "App", "name": "jira", "op": "put", "doc": draftApp("jira", "https://jira.example/mcp", "Ours.")}
	code, second := working(f.strazactl, linear, dev, jira)
	if code != http.StatusOK || second.Draft.ID != first.Draft.ID || second.Draft.Revision != 2 {
		t.Fatalf("second working = %d draft %s revision %d, want 200 on draft %s at revision 2", code, second.Draft.ID, second.Draft.Revision, first.Draft.ID)
	}
	jiraFP, err := store.FingerprintApp(f.jira.Manifest)
	if err != nil {
		t.Fatal(err)
	}
	_, items := f.stored(t, first.Draft.ID)
	for _, it := range items {
		switch it.Kind + "/" + it.Name {
		case "App/linear":
			if !strings.Contains(it.Doc, "Two.") || it.BaseOp != "remove" {
				t.Errorf("App/linear = %q base op %q, want the new document and its first base", it.Doc, it.BaseOp)
			}
		case "Role/dev":
			if it.Base != stamped {
				t.Errorf("Role/dev is stamped %q, want the base %q it entered the draft with, since live moved since", it.Base, stamped)
			}
		case "App/jira":
			if it.Base != jiraFP || it.BaseOp != "put" {
				t.Errorf("App/jira is stamped %q %q, want its live fingerprint as a new item", it.Base, it.BaseOp)
			}
		}
	}
	stale := false
	for _, fnd := range second.Verdict.Refused {
		stale = stale || fnd.Code == "draft.stale" && fnd.Object == "Role/dev"
	}
	if !stale {
		t.Errorf("refused = %+v, want Role/dev stale on its kept base", second.Verdict.Refused)
	}
	if n, err := f.app.store.Drafts().CountOpen(context.Background(), f.kim.ID); err != nil || n != 0 {
		t.Errorf("CountOpen(kim) = %d %v, want 0: a working draft never counts", n, err)
	}
}

// TestDraftRoutesRefuseAnItemOutsideReadStanding pins readRefusal on
// create, check and update: a server admin who names a role another server
// owns is refused with the grant it lacks, and nothing is stored.
func TestDraftRoutesRefuseAnItemOutsideReadStanding(t *testing.T) {
	t.Parallel()
	f := newDraftsFixture(t, nil)
	jiraWriters := "apiVersion: straza.dev/v1beta1\nkind: Role\nmetadata:\n    name: jira-writers\nspec:\n    kind: application\n    server: jira\n"
	says := "Drafting the role jira-writers needs the scope apps:read or the role " + f.adminRole(t, f.jira) +
		" of the server jira, because a draft shows the live config of the servers it names and what each gives a role. " +
		"Ask an administrator for that grant, or leave the role jira-writers out of the draft."
	own := f.create(t, f.erin, draftApp("github", "https://api.github.example/mcp?region=eu", "Ours."))
	before := f.drafts(t)
	for _, tc := range []struct{ name, method, path string }{
		{"create", http.MethodPost, "/v1/admin/drafts"},
		{"check", http.MethodPost, "/v1/admin/drafts/check"},
		{"update", http.MethodPut, "/v1/admin/drafts/" + own.Draft.ID},
	} {
		code, a := f.call(t, tc.method, tc.path, f.erin, map[string]any{"revision": 1, "documents": []string{jiraWriters}})
		if code != http.StatusForbidden || a.Error != says {
			t.Errorf("%s = %d %q, want 403 %q", tc.name, code, a.Error, says)
		}
	}
	if after := f.drafts(t); after != before {
		t.Errorf("the store holds %d drafts, want %d", after, before)
	}
	if row, _ := f.stored(t, own.Draft.ID); row.Revision != 1 {
		t.Errorf("the refused update moved the draft to revision %d", row.Revision)
	}
}

// TestCheckRefusesAnIntakeRefusedItemOutsideReadStanding pins that the
// check route judges the read standing over every item, the items intake
// refuses included, before it reads whether any exists: such an
// item outside the caller's read standing answers the 403 of create whether
// its object exists or not, and one inside it answers 200 with its refusal.
func TestCheckRefusesAnIntakeRefusedItemOutsideReadStanding(t *testing.T) {
	t.Parallel()
	f := newDraftsFixture(t, nil)
	noKind := func(name string) map[string]string {
		return map[string]string{"kind": "Role", "name": name, "op": "put",
			"doc": "apiVersion: straza.dev/v1beta1\nkind: Role\nmetadata:\n    name: " + name + "\nspec:\n    description: No kind.\n"}
	}
	bogus := func(name string) map[string]string {
		return map[string]string{"kind": "App", "name": name, "op": "put",
			"doc": "apiVersion: straza.dev/v1beta1\nkind: App\nmetadata:\n  name: " + name + "\nstraza:\n  runtime:\n    kind: bogus\n"}
	}
	role := func(name string) string {
		return "Drafting the role " + name + " needs the scope identity:read, because a draft shows the live document of every role it names. " +
			"Ask an administrator for that grant, or leave the role " + name + " out of the draft."
	}
	for _, tc := range []struct {
		name string
		item map[string]string
		says string
	}{
		{"a live global role", noKind("id-admins"), role("id-admins")},
		{"a role that does not exist", noKind("no-such-role"), role("no-such-role")},
		{"a server erin does not administer", bogus("jira"), "Drafting the server jira needs the scope apps:read or the role " + f.adminRole(t, f.jira) +
			" of the server jira, because a draft shows the live config of the servers it names and what each gives a role. " +
			"Ask an administrator for that grant, or leave the server jira out of the draft."},
		{"a policy set", map[string]string{"kind": "PolicySet", "name": "guard", "op": "put", "doc": "[unclosed"},
			"Drafting the policy set guard needs the scope policy:read, because a draft shows the published text of the sets it names and what they do for each role. " +
				"Ask an administrator for that grant, or leave the policy set guard out of the draft."},
	} {
		code, a := f.call(t, http.MethodPost, "/v1/admin/drafts/check", f.erin, map[string]any{"items": []map[string]string{tc.item}})
		if code != http.StatusForbidden || a.Error != tc.says || len(a.Items) != 0 {
			t.Errorf("%s = %d %q with the items %+v, want 403 %q and no item", tc.name, code, a.Error, a.Items, tc.says)
		}
	}
	code, a := f.call(t, http.MethodPost, "/v1/admin/drafts/check", f.erin, map[string]any{"items": []map[string]string{bogus("github")}})
	if code != http.StatusOK || len(a.Items) != 1 || !a.Items[0].Existed || len(a.Verdict.Refused) == 0 {
		t.Errorf("an intake-refused item erin may read = %d %+v refused %+v, want 200 with its refusal and existed", code, a.Items, a.Verdict.Refused)
	}
}

// generationFails is a store whose config generation read fails while
// armed, and which counts those reads.
type generationFails struct {
	store.Store
	armed *atomic.Bool
	reads *atomic.Int32
}

func (s generationFails) Drafts() store.DraftRepo { return generationFailsRepo{s.Store.Drafts(), s} }

type generationFailsRepo struct {
	store.DraftRepo
	s generationFails
}

func (r generationFailsRepo) Generation(ctx context.Context) (int64, error) {
	r.s.reads.Add(1)
	if r.s.armed.Load() {
		return 0, errors.New("injected generation failure")
	}
	return r.DraftRepo.Generation(ctx)
}

// failingGeneration answers a store wrapper whose generation read fails
// while armed, with its switches.
func failingGeneration() (*atomic.Bool, *atomic.Int32, []func(*App)) {
	armed, reads := &atomic.Bool{}, &atomic.Int32{}
	return armed, reads, []func(*App){func(a *App) { a.store = generationFails{a.store, armed, reads} }}
}

// TestDraftRoutesAnswer503WhenLiveStateCannotBeRead pins that a failed
// read of live state answers 503 with the read that failed, and that
// nothing is stored.
func TestDraftRoutesAnswer503WhenLiveStateCannotBeRead(t *testing.T) {
	t.Parallel()
	armed, _, preRun := failingGeneration()
	f := newDraftsFixture(t, preRun)
	d := f.create(t, f.root, draftRole("helpers", "Helpers."))
	armed.Store(true)
	before := f.drafts(t)
	says := "Straza could not read live state to check the draft: the config generation cannot be read: injected generation failure. " +
		"Nothing was saved or published. Try again, and read the strazad log if it keeps failing."
	body := map[string]any{"revision": 1, "documents": []string{draftRole("helpers", "Helpers, again.")}}
	for _, tc := range []struct{ name, method, path string }{
		{"create", http.MethodPost, "/v1/admin/drafts"},
		{"check", http.MethodPost, "/v1/admin/drafts/check"},
		{"get", http.MethodGet, "/v1/admin/drafts/" + d.Draft.ID},
		{"update", http.MethodPut, "/v1/admin/drafts/" + d.Draft.ID},
	} {
		var sent any = body
		if tc.method == http.MethodGet {
			sent = nil
		}
		if code, a := f.call(t, tc.method, tc.path, f.root, sent); code != http.StatusServiceUnavailable || a.Error != says {
			t.Errorf("%s = %d %q, want 503 %q", tc.name, code, a.Error, says)
		}
	}
	if after := f.drafts(t); after != before {
		t.Errorf("the store holds %d drafts, want %d", after, before)
	}
	if row, _ := f.stored(t, d.Draft.ID); row.Revision != 1 {
		t.Errorf("the failed update moved the draft to revision %d", row.Revision)
	}
}

// TestDraftErrorBodiesHoldTheirSentence pins the error body of the drafts
// routes, as strazactl reads it: {"error": sentence} alone, with the
// findings beside it on a 422 and the correlation id on a 5xx.
func TestDraftErrorBodiesHoldTheirSentence(t *testing.T) {
	t.Parallel()
	armed, _, preRun := failingGeneration()
	f := newDraftsFixture(t, preRun)
	id := f.create(t, f.root, draftRole("helpers", "Helpers.")).Draft.ID
	jiraWriters := "apiVersion: straza.dev/v1beta1\nkind: Role\nmetadata:\n    name: jira-writers\nspec:\n    kind: application\n    server: jira\n"
	pack := "apiVersion: straza.dev/v1beta1\nkind: Pack\nmetadata:\n  name: p\n"
	cases := []struct {
		name, method, path, bearer string
		body                       any
		arm                        bool
		code                       int
		fields                     string
	}{
		{"a query the list does not take", http.MethodGet, "/v1/admin/drafts?state=gone", f.root, nil, false, http.StatusBadRequest, "error"},
		{"a body that is not a draft", http.MethodPost, "/v1/admin/drafts", f.root, map[string]any{"documents": "one"}, false, http.StatusBadRequest, "error"},
		{"an object outside the read standing", http.MethodPost, "/v1/admin/drafts", f.erin, map[string]any{"documents": []string{jiraWriters}}, false, http.StatusForbidden, "error"},
		{"no such draft", http.MethodGet, "/v1/admin/drafts/999999", f.root, nil, false, http.StatusNotFound, "error"},
		{"a revision that moved", http.MethodPut, "/v1/admin/drafts/" + id, f.root, map[string]any{"revision": 5, "documents": []string{pack}}, false, http.StatusConflict, "error"},
		{"an intake refusal", http.MethodPost, "/v1/admin/drafts", f.root, map[string]any{"documents": []string{pack}}, false, http.StatusUnprocessableEntity, "error,findings"},
		{"live state that cannot be read", http.MethodGet, "/v1/admin/drafts/" + id, f.root, nil, true, http.StatusServiceUnavailable, "correlation_id,error"},
	}
	for _, tc := range cases {
		armed.Store(tc.arm)
		var raw []byte
		if tc.body != nil {
			raw, _ = json.Marshal(tc.body)
		}
		code, out, _ := adminBytes(t, tc.method, f.base+tc.path, tc.bearer, "application/json", raw)
		var body map[string]any
		if err := json.Unmarshal(out, &body); err != nil {
			t.Fatalf("%s: %v in %s", tc.name, err, out)
		}
		if msg, _ := body["error"].(string); code != tc.code || fieldNames(body) != tc.fields || msg == "" {
			t.Errorf("%s = %d with the fields %s, want %d with %s and a sentence", tc.name, code, fieldNames(body), tc.code, tc.fields)
		}
	}
}

// TestDraftSizeIsAnsweredBeforeLiveStateIsRead pins that a draft over the
// item limit meets draft.size at intake: create answers 422 and check a
// verdict of that refusal, neither reading live state.
func TestDraftSizeIsAnsweredBeforeLiveStateIsRead(t *testing.T) {
	t.Parallel()
	_, reads, preRun := failingGeneration()
	f := newDraftsFixture(t, preRun)
	items := make([]map[string]string, 1001)
	for i := range items {
		items[i] = map[string]string{"kind": "Role", "name": fmt.Sprintf("gone-%04d", i), "op": "remove"}
	}
	reads.Store(0)
	code, a := f.call(t, http.MethodPost, "/v1/admin/drafts", f.root, map[string]any{"items": items})
	if code != http.StatusUnprocessableEntity || a.Error != "The draft holds 1,001 items, and a draft holds at most 1,000. Split it into smaller drafts." {
		t.Errorf("create = %d %q, want 422 draft.size", code, a.Error)
	}
	code, a = f.call(t, http.MethodPost, "/v1/admin/drafts/check", f.root, map[string]any{"items": items})
	if code != http.StatusOK || len(a.Verdict.Refused) != 1 || a.Verdict.Refused[0].Code != "draft.size" || len(a.Items) != 1001 {
		t.Errorf("check = %d refused %+v with %d items, want 200, draft.size alone and every item", code, a.Verdict.Refused, len(a.Items))
	}
	if n := reads.Load(); n != 0 {
		t.Errorf("the routes read the config generation %d times, want none", n)
	}
}

// TestCheckDraftStoresNothing pins the unsaved check: it stores and
// stamps nothing and writes no record, it stamps in memory so a live
// object reads fresh and its item carries the live base, and the intake
// refusals join the verdict's refusals in order.
func TestCheckDraftStoresNothing(t *testing.T) {
	t.Parallel()
	f := newDraftsFixture(t, nil)
	before, records := f.drafts(t), len(adminAuditEvents(t, f.app))
	fp, _ := liveOf(t, f.app, "dev")
	code, a := f.call(t, http.MethodPost, "/v1/admin/drafts/check", f.root, map[string]any{
		"items": []map[string]string{{"kind": "Role", "name": "dev", "op": "put", "doc": roleText(t, "dev", "", "Developers.", "")}},
		"note":  strings.Repeat("n", 2001),
	})
	if code != http.StatusOK {
		t.Fatalf("check = %d %q", code, a.Error)
	}
	if len(a.Items) != 1 || a.Items[0].Base != fp {
		t.Errorf("items = %+v, want Role/dev with the live base %q", a.Items, fp)
	}
	var got []string
	for _, fnd := range a.Verdict.Refused {
		got = append(got, fnd.Code+" "+fnd.Object)
	}
	if strings.Join(got, ",") != "draft.note-size Note" {
		t.Errorf("refused = %v, want the note's refusal alone and no draft.stale", got)
	}
	if a.Verdict.Draft != "" || a.Verdict.Revision != 0 {
		t.Errorf("verdict names draft %q revision %d, want neither", a.Verdict.Draft, a.Verdict.Revision)
	}
	if after := f.drafts(t); after != before {
		t.Errorf("the store holds %d drafts, want %d", after, before)
	}
	if n := len(adminAuditEvents(t, f.app)); n != records {
		t.Errorf("the check wrote %d records", n-records)
	}
}

// TestUpdateDraft pins PUT: a new revision keeps the base of every
// object the draft held and stamps the new ones, a person who writes
// it becomes an author, a present note replaces the old one and an absent
// one keeps it, and the refusals answer in their pinned words.
func TestUpdateDraft(t *testing.T) {
	t.Parallel()
	f := newDraftsFixture(t, nil)
	d := f.create(t, f.root, draftApp("github", "https://api.github.example/mcp?region=eu", "First."))
	_, first := f.stored(t, d.Draft.ID)
	putServer(t, f.app, draftApp("github", "https://api.github.example/mcp?region=eu", "Moved on live."))
	path := "/v1/admin/drafts/" + d.Draft.ID
	code, a := f.call(t, http.MethodPut, path, f.nell, map[string]any{"revision": 1, "note": "Second.",
		"documents": []string{draftApp("github", "https://api.github.example/mcp?region=eu", "Second."), draftRole("dev", "Developers.")}})
	if code != http.StatusOK || a.Draft.Revision != 2 || a.Draft.Note != "Second." {
		t.Fatalf("update = %d %q revision %d note %q, want 200 at revision 2 with the new note", code, a.Error, a.Draft.Revision, a.Draft.Note)
	}
	if len(a.Draft.Authors) != 2 || a.Draft.Authors[0].Username != "kim" || a.Draft.Authors[1].Username != "nell" {
		t.Errorf("authors = %+v, want kim then nell", a.Draft.Authors)
	}
	_, items := f.stored(t, d.Draft.ID)
	fp, _ := liveOf(t, f.app, "dev")
	for _, it := range items {
		switch it.Kind + "/" + it.Name {
		case "App/github":
			if it.Base != first[0].Base {
				t.Errorf("App/github took the base %q, want its first %q", it.Base, first[0].Base)
			}
		case "Role/dev":
			if it.Base != fp {
				t.Errorf("Role/dev is stamped %q, want the live fingerprint %q", it.Base, fp)
			}
		}
	}
	stale := false
	for _, fnd := range a.Verdict.Refused {
		stale = stale || fnd.Code == "draft.stale" && fnd.Object == "App/github"
	}
	if !stale {
		t.Errorf("refused = %+v, want App/github stale, since its base was kept", a.Verdict.Refused)
	}
	code, a = f.call(t, http.MethodPut, path, f.root, map[string]any{"revision": 2, "documents": []string{draftRole("dev", "Developers.")}})
	if code != http.StatusOK || a.Draft.Note != "Second." {
		t.Errorf("update without a note = %d note %q, want the note kept", code, a.Draft.Note)
	}
	code, a = f.call(t, http.MethodPut, path, f.root, map[string]any{"revision": 3, "note": "", "documents": []string{draftRole("dev", "Developers.")}})
	if code != http.StatusOK || a.Draft.Note != "" {
		t.Errorf("update with an empty note = %d note %q, want it empty", code, a.Draft.Note)
	}
	id := d.Draft.ID
	refusals := []struct {
		name, bearer string
		body         map[string]any
		code         int
		says         string
	}{
		{"a revision that moved", f.root, map[string]any{"revision": 1, "documents": []string{draftRole("dev", "x")}}, http.StatusConflict,
			"Draft " + id + " changed after you read it: it is at revision 4, and you sent revision 1. Read it again and make your change on top of revision 4."},
		{"a token that did not write it", f.token, map[string]any{"revision": 4, "documents": []string{draftRole("dev", "x")}}, http.StatusForbidden,
			"An admin API token changes only the drafts it wrote, and draft " + id + " was written by someone else. A person can revise it on the console or with strazactl."},
		{"an intake refusal", f.root, map[string]any{"revision": 4, "documents": []string{"apiVersion: straza.dev/v1beta1\nkind: Pack\nmetadata:\n  name: p\n"}},
			http.StatusUnprocessableEntity, "Document 1 has kind Pack, and a draft holds App, Role, PolicySet and Removal documents only. Remove it, or make that change with its own command."},
	}
	for _, tc := range refusals {
		if code, a := f.call(t, http.MethodPut, path, tc.bearer, tc.body); code != tc.code || a.Error != tc.says {
			t.Errorf("%s = %d %q, want %d %q", tc.name, code, a.Error, tc.code, tc.says)
		}
	}
	if code, a := f.call(t, http.MethodPut, "/v1/admin/drafts/999999", f.root, map[string]any{"revision": 1}); code != http.StatusNotFound ||
		a.Error != "There is no draft 999999. List the drafts with strazactl drafts list, or open Drafts on the console." {
		t.Errorf("unknown draft = %d %q", code, a.Error)
	}
	if code, _ := f.call(t, http.MethodPost, path+"/discard", f.root, nil); code != http.StatusOK {
		t.Fatalf("discard = %d", code)
	}
	if code, a := f.call(t, http.MethodPut, path, f.root, map[string]any{"revision": 4, "documents": []string{draftRole("dev", "x")}}); code != http.StatusConflict ||
		a.Error != "Draft "+id+" is discarded, so it cannot change. Create a new draft from its documents." {
		t.Errorf("a discarded draft = %d %q", code, a.Error)
	}
}

// TestUpdateKeepsASavedEditToItsSet pins that a revision of the saved edit
// of a set holds one put of that set alone: any other item list,
// the set's removal and the set turned off included, answers 409 and stores
// nothing.
func TestUpdateKeepsASavedEditToItsSet(t *testing.T) {
	t.Parallel()
	f := newDraftsFixture(t, nil)
	row, err := f.app.store.Drafts().Create(context.Background(), store.DraftRow{Door: "api", Slot: "policy:guard"},
		[]store.DraftItemRow{{Kind: "PolicySet", Name: "guard", Op: "put", Doc: draftSet("guard")}},
		store.DraftRevisionRow{Author: store.DraftActor{ID: f.kim.ID, Name: "kim", Via: laneLogin, Client: clientLogin}})
	if err != nil {
		t.Fatal(err)
	}
	id := strconv.FormatInt(row.ID, 10)
	want := "Draft " + id + " is the saved edit of the policy set guard and holds a new text of that set alone. " +
		"Save the set's text with strazactl policy apply, or create a draft of your own for any other change."
	for name, body := range map[string]map[string]any{
		"the set and a server": {"documents": []string{draftSet("guard"), draftApp("evil", "https://evil.example/mcp", "Evil.")}},
		"another set alone":    {"documents": []string{draftSet("other")}},
		"the set's removal":    {"documents": []string{removalDoc("PolicySet", "guard")}},
		"the set turned off":   {"items": []map[string]string{{"kind": "PolicySet", "name": "guard", "op": "off", "doc": draftSet("guard")}}},
	} {
		body["revision"] = 1
		if code, a := f.call(t, http.MethodPut, "/v1/admin/drafts/"+id, f.root, body); code != http.StatusConflict || a.Error != want {
			t.Errorf("%s = %d %q, want 409 %q", name, code, a.Error, want)
		}
	}
	if stored, items := f.stored(t, id); stored.Revision != 1 || len(items) != 1 || items[0].Name != "guard" {
		t.Errorf("the saved edit reads revision %d with %+v, want revision 1 with guard alone", stored.Revision, items)
	}
	loose := strings.Replace(draftSet("guard"), "effect: deny", "effect: allow", 1)
	if code, a := f.call(t, http.MethodPut, "/v1/admin/drafts/"+id, f.root, map[string]any{"revision": 1, "documents": []string{loose}}); code != http.StatusOK {
		t.Errorf("a new text of guard = %d %q, want 200", code, a.Error)
	}
}

// TestUpdateMovesTheExpiryOfAnAgentsDraft pins the agent-draft expiry on
// PUT: a draft of the
// straza-app door expires 14 days after its latest revision, whoever
// writes it.
func TestUpdateMovesTheExpiryOfAnAgentsDraft(t *testing.T) {
	t.Parallel()
	f := newDraftsFixture(t, nil)
	old := time.Now().Add(-24 * time.Hour)
	row, err := f.app.store.Drafts().Create(context.Background(), store.DraftRow{Door: string(drafts.DoorAgent), ExpiresAt: &old},
		[]store.DraftItemRow{{Kind: "Role", Name: "helpers", Op: "put", Doc: draftRole("helpers", "Helpers.")}},
		store.DraftRevisionRow{Author: store.DraftActor{ID: "u-bot", Name: "bot", Agent: true, Via: laneSession, Client: "claude-code"}})
	if err != nil {
		t.Fatal(err)
	}
	id := strconv.FormatInt(row.ID, 10)
	code, a := f.call(t, http.MethodPut, "/v1/admin/drafts/"+id, f.root, map[string]any{"revision": 1, "documents": []string{draftRole("helpers", "Helpers, reviewed.")}})
	if code != http.StatusOK {
		t.Fatalf("update = %d %q", code, a.Error)
	}
	got, _ := f.stored(t, id)
	if got.ExpiresAt == nil || time.Until(*got.ExpiresAt) < 13*24*time.Hour {
		t.Errorf("expires_at = %v, want 14 days from now", got.ExpiresAt)
	}
}

// draftRecords answers the straza.audit.admin records of action about the
// draft id, oldest first.
func draftRecords(t *testing.T, app *App, action, id string) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, ev := range adminAuditEvents(t, app) {
		if ev["action"] == action && ev["draft"] == id {
			out = append(out, ev)
		}
	}
	return out
}

// fieldNames lists the keys of m, sorted and joined by commas.
func fieldNames(m map[string]any) string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return strings.Join(keys, ",")
}

// exampleFields lists the data fields of the spec/events example name.
func exampleFields(t *testing.T, name string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "spec", "events", "examples", name))
	if err != nil {
		t.Fatal(err)
	}
	var ev struct {
		Data map[string]any `json:"data"`
	}
	if err := json.Unmarshal(raw, &ev); err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	return fieldNames(ev.Data)
}

// codesOf lists the code of every finding, as a draft.check record lists
// them.
func codesOf(fs []wireFinding) []any {
	out := []any{}
	for _, f := range fs {
		out = append(out, f.Code)
	}
	return out
}

// TestDraftRecords pins the draft records against the fields
// of the spec/events examples: create and update each write one
// draft.create or draft.update with the actor, the revision's door, items
// and digest, every stamp one draft.check with the snapshot it read and the
// codes of its refusals and risks and no actor, and a discard one
// draft.discard with its reason.
func TestDraftRecords(t *testing.T) {
	t.Parallel()
	f := newDraftsFixture(t, nil)
	ctx := context.Background()
	if code, out, _ := adminBytes(t, http.MethodPut, f.base+"/v1/admin/policies", f.root, "application/yaml", []byte(draftSet("guard"))); code != http.StatusCreated {
		t.Fatalf("store guard = %d %s", code, out)
	}
	if code := adminReq(t, http.MethodPost, f.base+"/v1/admin/policies/guard/activate", f.root, map[string]string{"status": "active"}, nil); code != http.StatusOK {
		t.Fatalf("activate guard = %d", code)
	}
	active, err := f.app.store.Snapshots().GetActive(ctx)
	if err != nil {
		t.Fatal(err)
	}
	d := f.create(t, f.strazactl, draftRole("helpers", "Helpers."))
	id := d.Draft.ID
	creates := draftRecords(t, f.app, "draft.create", id)
	if len(creates) != 1 {
		t.Fatalf("draft.create records = %d, want 1", len(creates))
	}
	c := creates[0]
	digest := revisionDigest([]drafts.Item{{Kind: drafts.KindRole, Name: "helpers", Op: drafts.OpPut, Doc: d.Draft.Items[0].Doc}})
	if fields := exampleFields(t, "valid-audit-admin-draft-create.json"); fieldNames(c) != fields {
		t.Errorf("draft.create fields = %s, want the example's %s", fieldNames(c), fields)
	}
	if c["revision"] != float64(1) || c["door"] != "strazactl" || c["digest"] != digest || c["actor"] != "kim" || c["actorId"] != f.kim.ID || c["actorVia"] != laneSession {
		t.Errorf("draft.create = %v, want revision 1 through strazactl with the digest %s and kim as actor", c, digest)
	}
	if items, _ := json.Marshal(c["items"]); string(items) != `[{"kind":"Role","name":"helpers","op":"put"}]` {
		t.Errorf("draft.create items = %s", items)
	}
	checks := draftRecords(t, f.app, "draft.check", id)
	if len(checks) != 1 {
		t.Fatalf("draft.check records = %v, want one", checks)
	}
	check := checks[0]
	if fields := exampleFields(t, "valid-audit-admin-draft-check.json"); fieldNames(check) != fields {
		t.Errorf("draft.check fields = %s, want the example's %s, with no actor", fieldNames(check), fields)
	}
	if check["revision"] != float64(1) || check["snapshot"] != active.ID || d.Verdict.Snapshot != active.ID ||
		!reflect.DeepEqual(check["refused"], codesOf(d.Verdict.Refused)) || !reflect.DeepEqual(check["risks"], codesOf(d.Verdict.Risks)) {
		t.Errorf("draft.check = %v, want revision 1 on the snapshot %s with the codes of the verdict %+v", check, active.ID, d.Verdict)
	}
	if code, a := f.call(t, http.MethodPut, "/v1/admin/drafts/"+id, f.root, map[string]any{"revision": 1, "documents": []string{draftRole("helpers", "More.")}}); code != http.StatusOK {
		t.Fatalf("update = %d %q", code, a.Error)
	}
	updates := draftRecords(t, f.app, "draft.update", id)
	if len(updates) != 1 {
		t.Fatalf("draft.update records = %v, want one", updates)
	}
	if u := updates[0]; fieldNames(u) != fieldNames(c) || u["revision"] != float64(2) || u["door"] != "api" || u["actorVia"] != laneLogin {
		t.Errorf("draft.update = %v, want the fields of draft.create at revision 2 through the api door by kim's login", u)
	}
	if n := len(draftRecords(t, f.app, "draft.check", id)); n != 2 {
		t.Errorf("draft.check records = %d after the update, want 2", n)
	}
	if code, _ := f.call(t, http.MethodPost, "/v1/admin/drafts/"+id+"/discard", f.root, map[string]any{"reason": "Superseded."}); code != http.StatusOK {
		t.Fatalf("discard = %d", code)
	}
	discards := draftRecords(t, f.app, "draft.discard", id)
	if len(discards) != 1 {
		t.Fatalf("draft.discard records = %v, want one", discards)
	}
	if dr := discards[0]; fieldNames(dr) != "action,actor,actorId,actorVia,draft,reason,revision" || dr["revision"] != float64(2) || dr["reason"] != "Superseded." || dr["actor"] != "kim" {
		t.Errorf("draft.discard = %v, want revision 2 with its reason and kim as actor", dr)
	}
}

// TestDraftRecordsOutliveTheRequest pins that every draft record reaches
// the outbox after its request was cancelled, because the store write it
// records has landed by then.
func TestDraftRecordsOutliveTheRequest(t *testing.T) {
	t.Parallel()
	f := newDraftsFixture(t, nil)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	f.app.recordRevision(ctx, "draft.create", "900", 1, drafts.DoorAPI, []drafts.Item{{Kind: drafts.KindRole, Name: "helpers", Op: drafts.OpPut, Doc: "d"}}, "")
	f.app.recordCheck(ctx, "900", drafts.Verdict{Revision: 1, Snapshot: "s"})
	f.app.recordDiscard(ctx, "900", 1, "Superseded.")
	for _, action := range []string{"draft.create", "draft.check", "draft.discard"} {
		if n := len(draftRecords(t, f.app, action, "900")); n != 1 {
			t.Errorf("%s records after a cancelled request = %d, want 1", action, n)
		}
	}
}

// TestRevisionDigestCoversItemsWithoutBase pins the revision digest: the
// hex sha256 of the items as JSON, each kind, name, op and document in item
// order, with no base, so a later stamp never moves it.
func TestRevisionDigestCoversItemsWithoutBase(t *testing.T) {
	t.Parallel()
	items := []drafts.Item{{Kind: drafts.KindRole, Name: "dev", Op: drafts.OpPut, Doc: "d"}, {Kind: drafts.KindApp, Name: "x", Op: drafts.OpRemove}}
	sum := sha256.Sum256([]byte(`[{"kind":"Role","name":"dev","op":"put","doc":"d"},{"kind":"App","name":"x","op":"remove","doc":""}]`))
	if got := revisionDigest(items); got != hex.EncodeToString(sum[:]) {
		t.Errorf("digest = %s, want %s", got, hex.EncodeToString(sum[:]))
	}
	stamped := append([]drafts.Item(nil), items...)
	stamped[0].Base = "fp"
	if revisionDigest(stamped) != revisionDigest(items) {
		t.Error("a stamp moved the digest")
	}
	stamped[0].Doc = "e"
	if revisionDigest(stamped) == revisionDigest(items) {
		t.Error("a changed document kept the digest")
	}
}

// TestDraftRoutesWaiveWhatLiveStateHolds pins the waiver on create, check,
// update and the working draft: a live server whose stored manifest holds
// a plain word after a flag naming a secret, a live role whose name holds
// an invisible character, and the manifest a route answered, sent back
// with its masks, each take a new description with the waiver's warning
// in the verdict. The same value one byte off, the same manifest for a new
// server, a mask at a place the live manifest does not hold, the answered
// manifest for a new server and the same character in a new role's name
// are still refused with nothing stored, and a draft that holds a live
// name beside a refusal no read of live state can lift is refused before
// the read, leading with that refusal, while check, which always reads,
// waives the name. A live server named like a token draws no finding that
// quotes the name, the parser's included. A publish of the answered
// manifest stores the live value where the mask stood, never the mark.
func TestDraftRoutesWaiveWhatLiveStateHolds(t *testing.T) {
	t.Parallel()
	f := newDraftsFixture(t, nil)
	runner := putServer(t, f.app, waivedRunner("runner", "The runner."))
	logger := putServer(t, f.app, strings.ReplaceAll(maskedRunner("The runner."), "runner", "logger"))
	const slack = "xoxb-2745010221-2745010221000-abcdefghij0123456789"
	putServer(t, f.app, draftApp(slack, "https://slack.example/mcp", "The Slack server."))
	if _, err := f.app.store.Roles().Create(context.Background(), store.Role{Name: "dev\u200bops", Kind: store.RoleKindBusiness}); err != nil {
		t.Fatal(err)
	}
	held := f.create(t, f.root, draftRole("helpers", "Helpers.")).Draft.ID
	doors := []struct {
		name string
		send func(documents []string) (int, wireAnswer)
	}{
		{"create", func(documents []string) (int, wireAnswer) {
			return f.call(t, http.MethodPost, "/v1/admin/drafts", f.root, map[string]any{"documents": documents})
		}},
		{"check", func(documents []string) (int, wireAnswer) {
			return f.call(t, http.MethodPost, "/v1/admin/drafts/check", f.root, map[string]any{"documents": documents})
		}},
		{"update", func(documents []string) (int, wireAnswer) {
			row, _ := f.stored(t, held)
			return f.call(t, http.MethodPut, "/v1/admin/drafts/"+held, f.root, map[string]any{"revision": row.Revision, "documents": documents})
		}},
		{"working", func(documents []string) (int, wireAnswer) {
			return f.call(t, http.MethodPost, "/v1/admin/drafts", f.strazactl, map[string]any{"documents": documents, "working": true})
		}},
	}
	userinfo := draftApp("linear", "https://bot:Hunter2Hunter2@linear.example/mcp", "The Linear server.")
	cases := []struct {
		name      string
		documents []string
		waived    string
		refused   string
		hides     string
	}{
		{"a new description of the server", []string{waivedRunner("runner", "Changed.")}, "secret.value", "", ""},
		{"a new description of the role", []string{draftRole("dev\u200bops", "New words.")}, "bundle.name-characters", "", ""},
		{"the value one byte off", []string{strings.Replace(waivedRunner("runner", "Changed."), "oauth]", "oauth2]", 1)}, "", "secret.value", ""},
		{"the same manifest for a new server", []string{waivedRunner("runner2", "Another.")}, "", "secret.value", ""},
		{"the same character in a new role's name", []string{draftRole("qa\u200bteam", "New.")}, "", "bundle.name-characters", ""},
		{"a live name beside a refusal no read lifts", []string{draftRole("dev\u200bops", "New words."), userinfo}, "bundle.name-characters", "secret.userinfo", ""},
		{"the waived value beside a field the parser refuses",
			[]string{strings.Replace(waivedRunner("runner", "Changed."), "      exec: /bin/sh\n", "      exec: /bin/sh\n      bogusField: 1\n", 1)}, "", "app.parse", ""},
		{"the answered manifest sent back with a new description", []string{strings.Replace(maskedManifest(runner.Manifest), "The runner.", "Changed.", 1)}, "bundle.masked", "", ""},
		{"the answered manifest of a server with env values sent back", []string{strings.Replace(maskedManifest(logger.Manifest), "The logger.", "Changed.", 1)}, "bundle.masked", "", ""},
		{"a plain env value the answer masked", []string{strings.Replace(strings.ReplaceAll(maskedRunner("Changed."), "runner", "logger"), "value: debug", "value: '[REDACTED]'", 1)}, "bundle.masked", "", ""},
		{"two masks in one document", []string{strings.Replace(strings.Replace(strings.ReplaceAll(maskedRunner("Changed."), "runner", "logger"), "value: debug", "value: '[REDACTED]'", 1), "value: hunter2hunter2", "value: '[REDACTED]'", 1)},
			"bundle.masked", "", ""},
		{"a mask at a place the live manifest does not hold", []string{waivedRunner("runner", "Changed.") + "      env:\n        - {name: EXTRA, value: '[REDACTED]'}\n"}, "", "bundle.masked", ""},
		{"the answered manifest for a new server", []string{strings.ReplaceAll(maskedManifest(runner.Manifest), "runner", "runner3")}, "", "bundle.masked", ""},
		{"a live server named like a token", []string{strings.Replace(draftApp(slack, "https://slack.example/mcp", "Changed."), "kind: remote", "kind: bogus", 1)}, "", "secret.shape", slack},
	}
	codes := func(fs []wireFinding) []string {
		out := []string{}
		for _, fnd := range fs {
			out = append(out, fnd.Code)
		}
		return out
	}
	for _, door := range doors {
		for _, tc := range cases {
			t.Run(door.name+": "+tc.name, func(t *testing.T) {
				before := f.drafts(t)
				code, a := door.send(tc.documents)
				check := door.name == "check"
				switch {
				case check && code != http.StatusOK:
					t.Fatalf("check = %d %q, want 200", code, a.Error)
				case !check && tc.refused == "" && code/100 != 2:
					t.Fatalf("= %d %q %v, want 2xx", code, a.Error, a.Findings)
				case !check && tc.refused != "" && (code != http.StatusUnprocessableEntity || f.drafts(t) != before):
					t.Fatalf("= %d %q with %d drafts stored, want 422 and nothing stored", code, a.Error, f.drafts(t)-before)
				}
				refused, warnings := codes(a.Verdict.Refused), a.Verdict.Warnings
				for _, fnd := range slices.Concat(a.Findings, a.Verdict.Refused, a.Verdict.Warnings, a.Verdict.Risks) {
					if tc.hides != "" && strings.Contains(fnd.Object+" "+fnd.Sentence+" "+fnd.Fix, tc.hides) {
						t.Errorf("the finding %s quotes the withheld name: %+v", fnd.Code, fnd)
					}
				}
				if !check && tc.refused != "" {
					refused, warnings = codes(a.Findings), nil
					if want := strings.TrimSpace(a.Findings[0].Sentence + " " + a.Findings[0].Fix); a.Error != want || a.Findings[0].Code != tc.refused {
						t.Errorf("the refusal leads with %s %q, want %s with its sentence and fix as the error", a.Findings[0].Code, a.Error, tc.refused)
					}
				}
				switch {
				case tc.refused == "" && len(refused) != 0:
					t.Errorf("refused = %v, want none", refused)
				case tc.refused != "" && !slices.Contains(refused, tc.refused):
					t.Errorf("refused = %v, want %s among them", refused, tc.refused)
				}
				if tc.waived == "" || (!check && tc.refused != "") {
					return
				}
				waived := false
				for _, w := range warnings {
					if w.Code == tc.waived && strings.Contains(w.Sentence, "already holds") {
						waived = true
					}
					if w.Code == tc.refused {
						t.Errorf("the refused %s is also a warning: %q", w.Code, w.Sentence)
					}
				}
				if !waived {
					t.Errorf("warnings = %+v, want %s saying live state already holds it", warnings, tc.waived)
				}
			})
		}
	}
	// A publish of the answered manifest reads the stored manifest and finds
	// the live value where each mask stood, never the mark.
	id := f.create(t, f.root, strings.Replace(maskedManifest(logger.Manifest), "The logger.", "Published.", 1)).Draft.ID
	if code, p := f.publishAll(t, f.root, id); code != http.StatusOK {
		t.Fatalf("publishing the answered manifest = %d %q %+v", code, p.Error, p.Verdict.Refused)
	}
	live, err := f.app.store.Apps().GetByName(context.Background(), "logger")
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range []string{`"--auth","oauth"`, `"value":"debug"`, `"value":"hunter2hunter2"`, "Published."} {
		if !strings.Contains(live.Manifest, v) {
			t.Errorf("the published manifest lacks %s: %s", v, live.Manifest)
		}
	}
	if strings.Contains(live.Manifest, redact.Mark) {
		t.Errorf("the published manifest holds the mark: %s", live.Manifest)
	}
}

// TestCheckReadsTheStoredValueWhereAMaskStands pins that the check reads
// the facts of a masked document from its text restored against the live
// manifest: the answered manifest of a live command server, sent back with
// a new description, draws the waiver's warning and no risk, where the
// text as sent would read the mask as a changed argument and draw
// server.runs-code, and the same document with a new command still draws
// that risk.
func TestCheckReadsTheStoredValueWhereAMaskStands(t *testing.T) {
	t.Parallel()
	f := newDraftsFixture(t, nil)
	logger := putServer(t, f.app, strings.ReplaceAll(maskedRunner("The runner."), "runner", "logger"))
	answer := strings.Replace(maskedManifest(logger.Manifest), "The logger.", "Changed.", 1)
	codes := func(fs []wireFinding) []string {
		out := []string{}
		for _, fnd := range fs {
			out = append(out, fnd.Code)
		}
		return out
	}
	for _, tc := range []struct{ name, doc, risk string }{
		{"the answered manifest with a new description", answer, ""},
		{"the answered manifest with a new command", strings.Replace(answer, "exec: /bin/sh", "exec: /bin/bash", 1), "server.runs-code"},
	} {
		code, a := f.call(t, http.MethodPost, "/v1/admin/drafts/check", f.root, map[string]any{"documents": []string{tc.doc}})
		if code != http.StatusOK || len(a.Verdict.Refused) != 0 || !slices.Contains(codes(a.Verdict.Warnings), "bundle.masked") {
			t.Fatalf("%s: check = %d %q, refused %v, warnings %v; want 200 with the mask waived", tc.name, code, a.Error, codes(a.Verdict.Refused), codes(a.Verdict.Warnings))
		}
		switch risks := codes(a.Verdict.Risks); {
		case tc.risk == "" && len(risks) != 0:
			t.Errorf("%s: risks = %v, want none, because the check read the mask as a changed value", tc.name, risks)
		case tc.risk != "" && !slices.Contains(risks, tc.risk):
			t.Errorf("%s: risks = %v, want %s", tc.name, risks, tc.risk)
		}
	}
}

// TestWaiverKeepsTheReadRefusalFirst pins that the waiver never tells a
// caller who may not read a server whether a guess at a masked value
// matches: on create, check, update and the working draft, ada, who holds
// the drafts grants alone, gets the same 403 with the same sentence for the
// stored value and for a wrong guess, and nothing is stored.
func TestWaiverKeepsTheReadRefusalFirst(t *testing.T) {
	t.Parallel()
	f := newDraftsFixture(t, nil)
	putServer(t, f.app, waivedRunner("runner", "The runner."))
	held := f.create(t, f.root, draftRole("helpers", "Helpers.")).Draft.ID
	right := waivedRunner("runner", "Changed.")
	wrong := strings.Replace(right, "oauth]", "oauth2]", 1)
	doors := []struct {
		name string
		send func(document string) (int, wireAnswer)
	}{
		{"create", func(document string) (int, wireAnswer) {
			return f.call(t, http.MethodPost, "/v1/admin/drafts", f.ada, map[string]any{"documents": []string{document}})
		}},
		{"check", func(document string) (int, wireAnswer) {
			return f.call(t, http.MethodPost, "/v1/admin/drafts/check", f.ada, map[string]any{"documents": []string{document}})
		}},
		{"update", func(document string) (int, wireAnswer) {
			row, _ := f.stored(t, held)
			return f.call(t, http.MethodPut, "/v1/admin/drafts/"+held, f.ada, map[string]any{"revision": row.Revision, "documents": []string{document}})
		}},
		{"working", func(document string) (int, wireAnswer) {
			return f.call(t, http.MethodPost, "/v1/admin/drafts", f.ada, map[string]any{"documents": []string{document}, "working": true})
		}},
	}
	for _, door := range doors {
		before := f.drafts(t)
		codeRight, a := door.send(right)
		codeWrong, b := door.send(wrong)
		if codeRight != http.StatusForbidden || codeWrong != http.StatusForbidden || a.Error != b.Error || !strings.Contains(a.Error, "needs the scope apps:read") {
			t.Errorf("%s: the stored value = %d %q, a wrong guess = %d %q; want the same 403 for both", door.name, codeRight, a.Error, codeWrong, b.Error)
		}
		if after := f.drafts(t); after != before {
			t.Errorf("%s stored %d drafts", door.name, after-before)
		}
	}
}
