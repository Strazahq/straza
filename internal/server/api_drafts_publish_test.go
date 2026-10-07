package server

import (
	"bytes"
	"context"
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

	"github.com/santhosh-tekuri/jsonschema/v6"
	"gopkg.in/yaml.v3"

	"github.com/strazahq/straza/internal/config"
	"github.com/strazahq/straza/internal/drafts"
	"github.com/strazahq/straza/internal/store"
)

// pubFinding is a verdict line as the drafts routes answer it, with the
// fields an acknowledgment reads.
type pubFinding struct {
	Code     string `json:"code"`
	Class    string `json:"class"`
	Ack      string `json:"ack"`
	Object   string `json:"object"`
	Sentence string `json:"sentence"`
	Fix      string `json:"fix"`
	Typed    string `json:"typed"`
	Key      string `json:"key"`
}

// pubVerdict is a verdict as the drafts routes answer it.
type pubVerdict struct {
	Refused    []pubFinding `json:"refused"`
	Risks      []pubFinding `json:"risks"`
	Warnings   []pubFinding `json:"warnings"`
	RiskDigest string       `json:"risk_digest"`
}

// pubServer is one server of a publish's answer.
type pubServer struct {
	Name   string `json:"name"`
	Change string `json:"change"`
	Status string `json:"status"`
	Detail string `json:"detail"`
}

// pubAnswer is what the publish route answers: the published draft with
// the snapshot, servers and next steps, or a refusal with its verdict.
type pubAnswer struct {
	Draft    wireDraft   `json:"draft"`
	Snapshot string      `json:"snapshot"`
	Servers  []pubServer `json:"servers"`
	Next     []string    `json:"next"`
	Error    string      `json:"error"`
	Verdict  pubVerdict  `json:"verdict"`
}

// read answers the revision of draft id and its verdict as bearer reads
// them.
func (f *draftsFixture) read(t *testing.T, bearer, id string) (int, pubVerdict) {
	t.Helper()
	code, out, _ := adminBytes(t, http.MethodGet, f.base+"/v1/admin/drafts/"+id, bearer, "", nil)
	if code != http.StatusOK {
		t.Fatalf("read draft %s = %d %s", id, code, out)
	}
	var d struct {
		Draft   wireDraft  `json:"draft"`
		Verdict pubVerdict `json:"verdict"`
	}
	if err := json.Unmarshal(out, &d); err != nil {
		t.Fatal(err)
	}
	return d.Draft.Revision, d.Verdict
}

// acksFor is the publish body at revision that acknowledges every risk of
// v as strazactl does: each key in ticked, and a typed risk's text in
// typed.
func acksFor(revision int, v pubVerdict) map[string]any {
	ticked, typed := []string{}, map[string]string{}
	for _, r := range v.Risks {
		ticked = append(ticked, r.Key)
		if r.Ack == "typed" && r.Typed != "" {
			typed[r.Key] = r.Typed
		}
	}
	return map[string]any{"revision": revision, "risk_digest": v.RiskDigest, "ticked": ticked, "typed": typed}
}

// publish sends body to the publish route of draft id as bearer and
// answers the status, the decoded answer and its bytes.
func (f *draftsFixture) publish(t *testing.T, bearer, id string, body any) (int, pubAnswer, []byte) {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	code, out, _ := adminBytes(t, http.MethodPost, f.base+"/v1/admin/drafts/"+id+"/publish", bearer, "application/json", raw)
	var a pubAnswer
	if len(out) > 0 {
		if err := json.Unmarshal(out, &a); err != nil {
			t.Fatalf("publish %s: %v in %s", id, err, out)
		}
	}
	return code, a, out
}

// publishAll reads draft id as bearer and publishes it with every risk
// acknowledged.
func (f *draftsFixture) publishAll(t *testing.T, bearer, id string) (int, pubAnswer) {
	t.Helper()
	rev, v := f.read(t, bearer, id)
	code, a, _ := f.publish(t, bearer, id, acksFor(rev, v))
	return code, a
}

// generation reads the config generation.
func (f *draftsFixture) generation(t *testing.T) int64 {
	t.Helper()
	gen, err := f.app.store.Drafts().Generation(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return gen
}

// login signs name in and answers its ID token.
func (f *draftsFixture) login(t *testing.T, name string) string {
	t.Helper()
	return loginDeviceFlow(t, f.base, name, "hunter2!")
}

// consoleSession checks name in on the console and answers the session
// token.
func consoleSession(t *testing.T, base, name string) string {
	t.Helper()
	_, checkin := postJSON(t, base+"/v1/checkin", map[string]any{
		"id_token":    loginDeviceFlow(t, base, name, "hunter2!"),
		"harness":     map[string]string{"name": "console", "version": "1"},
		"attestation": map[string]any{"managed": false, "hashes": map[string]string{"self": "sha256:x"}},
	})
	tok, _ := checkin["session_token"].(string)
	if tok == "" {
		t.Fatalf("no session for %s: %v", name, checkin)
	}
	return tok
}

// mintToken mints an admin API token of scope as root and answers the
// token and its id.
func (f *draftsFixture) mintToken(t *testing.T, name, scope string) (string, string) {
	t.Helper()
	var minted struct {
		ID    string `json:"id"`
		Token string `json:"token"`
	}
	if code := adminReq(t, http.MethodPost, f.base+"/v1/admin/api-tokens", f.root, map[string]any{"name": name, "scope": scope}, &minted); code != http.StatusCreated {
		t.Fatalf("mint %s = %d", name, code)
	}
	return minted.Token, minted.ID
}

// typedRole is the Role document of the application role name that server
// owns, with its one access row there for tools.
func typedRole(name, server string, tools ...string) string {
	return fmt.Sprintf("apiVersion: straza.dev/v1beta1\nkind: Role\nmetadata:\n    name: %s\nspec:\n    kind: application\n    server: %s\n    bindings:\n        - app: %s\n          tools:\n%s",
		name, server, server, "            - '"+strings.Join(tools, "'\n            - '")+"'\n")
}

// setText is the PolicySet name for dev that denies shell calls with the
// reason why.
func setText(name, why string) string {
	return strings.Replace(draftSet(name), "Straza: no shell for dev", why, 1)
}

// TestPublishRefusesAnyoneButAPerson pins step 1 of the publish: an admin API
// token reads step 1's sentence whatever drafts grant it holds, an agent
// reads the admin API's refusal of anyone who is not a person, a disabled
// and a locked person's session reads the refresh's words, a coding
// harness's session reads the admin API's sentence, and the draft stays
// open.
func TestPublishRefusesAnyoneButAPerson(t *testing.T) {
	t.Parallel()
	f := newDraftsFixture(t, nil)
	ctx := context.Background()
	id := f.create(t, f.root, draftRole("helpers", "Helpers.")).Draft.ID
	bare, _ := f.mintToken(t, "reader", "apps:read")
	seedAgent(t, f.app, "bot2", "kim", "dev")
	dora, lars := mkHuman(t, f.app, "dora", AdminRole), mkHuman(t, f.app, "lars", AdminRole)
	doraSession, larsSession := consoleSession(t, f.base, "dora"), consoleSession(t, f.base, "lars")
	dora.Status = store.UserDisabled
	if _, err := f.app.store.Users().Update(ctx, dora); err != nil {
		t.Fatal(err)
	}
	f.app.denylist.RevokeUser(lars.ID)
	harness, _ := checkinTokenAs(t, f.base, "claude-code")
	rev, v := f.read(t, f.root, id)
	body := acksFor(rev, v)
	gen := f.generation(t)
	for _, tc := range []struct{ name, bearer, want string }{
		{"an admin API token with drafts:write", f.token, publishRefusalAdminAPI},
		{"an admin API token without a drafts grant", bare, publishRefusalAdminAPI},
		{"an agent with a drafts grant", f.bot, fmt.Sprintf(nonPersonAdminRefusal, "bot", "an agent")},
		{"an agent without a drafts grant", f.login(t, "bot2"), fmt.Sprintf(nonPersonAdminRefusal, "bot2", "an agent")},
		{"a disabled person", doraSession, "user is disabled. Contact your administrator"},
		{"a locked person", larsSession, revokedIdentityMsg},
		{"a coding harness's session", harness, fmt.Sprintf(codingHarnessRefusal, "claude-code")},
	} {
		if code, a, _ := f.publish(t, tc.bearer, id, body); code != http.StatusForbidden || a.Error != tc.want {
			t.Errorf("%s = %d %q, want 403 %q", tc.name, code, a.Error, tc.want)
		}
	}
	if row, _ := f.stored(t, id); row.State != "open" || f.generation(t) != gen {
		t.Errorf("the draft is %s and the generation moved from %d to %d, want it open and unmoved", row.State, gen, f.generation(t))
	}
}

// TestPublishRouteAnswersAGetWith405 pins what strazactl's route check
// reads: a GET of the publish path through the real router answers 405,
// never 404.
func TestPublishRouteAnswersAGetWith405(t *testing.T) {
	t.Parallel()
	f := newDraftsFixture(t, nil)
	if code, out, _ := adminBytes(t, http.MethodGet, f.base+"/v1/admin/drafts/1/publish", f.root, "", nil); code != http.StatusMethodNotAllowed {
		t.Errorf("GET of the publish path = %d %s, want 405", code, out)
	}
}

// TestPublishAnswersTheDraftsState pins the publish route's read and step 2: 404
// for an id that is no draft or that the caller may not read, and 409 for
// another revision, a draft not checked at its revision, a discarded
// draft and a published one, which names who published it and when.
func TestPublishAnswersTheDraftsState(t *testing.T) {
	t.Parallel()
	f := newDraftsFixture(t, nil)
	ctx := context.Background()
	open := f.create(t, f.root, draftRole("helpers", "Helpers.")).Draft.ID
	discarded := f.create(t, f.root, draftRole("spare", "Spare.")).Draft.ID
	if code, _ := f.call(t, http.MethodPost, "/v1/admin/drafts/"+discarded+"/discard", f.root, nil); code != http.StatusOK {
		t.Fatalf("discard = %d", code)
	}
	unchecked, err := f.app.store.Drafts().Create(ctx, store.DraftRow{Door: "api"},
		[]store.DraftItemRow{{Kind: "Role", Name: "later", Op: "put", Doc: draftRole("later", "Later.")}},
		store.DraftRevisionRow{Author: store.DraftActor{ID: f.kim.ID, Name: "kim", Via: laneLogin, Client: clientLogin}, Door: "api", Digest: "d"})
	if err != nil {
		t.Fatal(err)
	}
	published := f.create(t, f.root, draftRole("done", "Done.")).Draft.ID
	if code, a := f.publishAll(t, f.root, published); code != http.StatusOK {
		t.Fatalf("publish = %d %q", code, a.Error)
	}
	row, _ := f.stored(t, published)
	at := row.DecidedAt.UTC().Format("2006-01-02 15:04:05")
	uncheckedID := strconv.FormatInt(unchecked.ID, 10)
	body := func(rev int) map[string]any {
		return map[string]any{"revision": rev, "risk_digest": "", "ticked": []string{}, "typed": map[string]string{}}
	}
	for _, tc := range []struct {
		name, bearer, id string
		rev, code        int
		want             string
	}{
		{"an id that is no number", f.root, "abc", 1, http.StatusNotFound, "There is no draft abc. List the drafts with strazactl drafts list, or open Drafts on the console."},
		{"an unknown id", f.root, "9999", 1, http.StatusNotFound, "There is no draft 9999. List the drafts with strazactl drafts list, or open Drafts on the console."},
		{"a draft the caller may not read", f.erin, open, 1, http.StatusNotFound, "There is no draft " + open + ". List the drafts with strazactl drafts list, or open Drafts on the console."},
		{"another revision", f.root, open, 2, http.StatusConflict, "Draft " + open + " changed after you read it: it is at revision 1, and you sent revision 2. Read it again and make your change on top of revision 1."},
		{"a draft not checked at its revision", f.root, uncheckedID, 1, http.StatusConflict, "Draft " + uncheckedID + " has not been checked at revision 1. Open it, read the check, and publish again."},
		{"a discarded draft", f.root, discarded, 1, http.StatusConflict, "Draft " + discarded + " is discarded, so it cannot change. Create a new draft from its documents."},
		{"a published draft, whatever revision is sent", f.root, published, 7, http.StatusConflict,
			"Draft " + published + " was published by kim at " + at + " UTC, so nothing more was done. Read it with strazactl drafts show " + published + "."},
	} {
		if code, a, _ := f.publish(t, tc.bearer, tc.id, body(tc.rev)); code != tc.code || a.Error != tc.want {
			t.Errorf("%s = %d %q, want %d %q", tc.name, code, a.Error, tc.code, tc.want)
		}
	}
}

// TestPublishAnswersABodyItCannotRead pins the publish route's 400 and 413: a
// body that is not a
// publish answers 400, and one over server.maxBodyBytes 413.
func TestPublishAnswersABodyItCannotRead(t *testing.T) {
	t.Parallel()
	f := newDraftsFixture(t, nil, func(c *config.Config) { c.Server.MaxBodyBytes = 4096 })
	id := f.create(t, f.root, draftRole("helpers", "Helpers.")).Draft.ID
	path := f.base + "/v1/admin/drafts/" + id + "/publish"
	code, out, _ := adminBytes(t, http.MethodPost, path, f.root, "application/json", []byte(`{"revision": "one"}`))
	var a pubAnswer
	_ = json.Unmarshal(out, &a)
	if code != http.StatusBadRequest || !strings.HasPrefix(a.Error, "The request body is not a publish: ") ||
		!strings.HasSuffix(a.Error, ". Send revision, risk_digest, ticked and typed as JSON.") {
		t.Errorf("a body that is not a publish = %d %q, want 400 with the publish frame", code, a.Error)
	}
	big := []byte(`{"revision": 1, "risk_digest": "` + strings.Repeat("a", 5000) + `"}`)
	code, out, _ = adminBytes(t, http.MethodPost, path, f.root, "application/json", big)
	_ = json.Unmarshal(out, &a)
	want := "The request body holds more than 4096 bytes, the most strazad reads in one request (server.maxBodyBytes). Split the draft into smaller drafts, or raise server.maxBodyBytes."
	if code != http.StatusRequestEntityTooLarge || a.Error != want {
		t.Errorf("a body over the limit = %d %q, want 413 %q", code, a.Error, want)
	}
}

// TestPublishChecksAgain pins step 3: the verdict is computed again under
// the lock, so a draft whose object moved since its check answers 409 with
// draft.stale and the verdict, and a refused verdict its first refusal.
func TestPublishChecksAgain(t *testing.T) {
	t.Parallel()
	f := newDraftsFixture(t, nil)
	stale := f.create(t, f.root, draftRole("helpers", "Helpers.")).Draft.ID
	if code := adminReq(t, http.MethodPost, f.base+"/v1/admin/roles", f.root, map[string]any{"name": "helpers", "kind": "business"}, nil); code != http.StatusCreated {
		t.Fatalf("create helpers = %d", code)
	}
	doc := "apiVersion: straza.dev/v1beta1\nkind: Role\nmetadata:\n    name: team\nspec:\n    kind: business\n    implies:\n        - nosuch\n"
	refused := f.create(t, f.root, doc).Draft.ID
	for _, tc := range []struct{ name, id, code string }{
		{"an object that moved", stale, "draft.stale"},
		{"a refused verdict", refused, "imply.missing"},
	} {
		code, a := f.publishAll(t, f.root, tc.id)
		if code != http.StatusConflict || len(a.Verdict.Refused) == 0 || a.Verdict.Refused[0].Code != tc.code {
			t.Errorf("%s = %d %q %+v, want 409 with %s first", tc.name, code, a.Error, a.Verdict.Refused, tc.code)
			continue
		}
		r := a.Verdict.Refused[0]
		want := strings.TrimSpace("Draft " + tc.id + " cannot be published: " + r.Sentence + " " + r.Fix)
		if a.Error != want {
			t.Errorf("%s says %q, want %q", tc.name, a.Error, want)
		}
		if row, _ := f.stored(t, tc.id); row.State != "open" {
			t.Errorf("%s left the draft %s", tc.name, row.State)
		}
	}
}

// TestPublishNeedsStandingOverEveryItem pins step 4: nell holds
// identity:write and no policy:write, so a draft
// with a role and a set refuses on the set.
func TestPublishNeedsStandingOverEveryItem(t *testing.T) {
	t.Parallel()
	f := newDraftsFixture(t, nil)
	id := f.create(t, f.root, draftRole("helpers", "Helpers."), draftSet("guard")).Draft.ID
	want := "You cannot publish draft " + id + ": this draft also changes policy sets, which needs the scope policy:write."
	if code, a := f.publishAll(t, f.nell, id); code != http.StatusForbidden || a.Error != want {
		t.Errorf("nell's publish = %d %q, want 403 %q", code, a.Error, want)
	}
}

// secondPersonFixture is the drafts fixture with admin.secondPerson on,
// the servers a..e to remove, the person lars holding the root role, and
// the agent rob, sponsored by kim, that holds straza-draft-config.
func secondPersonFixture(t *testing.T) (*draftsFixture, string, store.User) {
	t.Helper()
	f := newDraftsFixture(t, nil, func(c *config.Config) { c.Admin.SecondPerson = true })
	for _, name := range []string{"srv-a", "srv-b", "srv-c", "srv-d", "srv-e"} {
		putServer(t, f.app, draftApp(name, "https://"+name+".example/mcp", "A server."))
	}
	mkHuman(t, f.app, "lars", AdminRole)
	return f, f.login(t, "lars"), seedAgent(t, f.app, "rob", "kim", DraftConfigRole)
}

// agentDraft stores a draft of the built-in straza app's door that agent,
// sponsored by kim, wrote with documents, as straza__draft_submit stores
// one, and answers its id. An agent never reaches the drafts routes.
func (f *draftsFixture) agentDraft(t *testing.T, agent store.User, documents ...string) string {
	t.Helper()
	r := &doorRig{f}
	return strconv.FormatInt(r.storeDraft(t, string(drafts.DoorAgent), r.agentActor(agent), time.Now().Add(time.Hour), documents...).ID, 10)
}

// TestPublishHoldsTheSecondPerson pins step 5, the second-person rule: with
// the setting on
// a risk refuses the publish to an author, the minter of a token that
// wrote a revision, anyone when that token's minter cannot be traced, and
// the sponsor of an agent that wrote one, while another person, the author
// of a mechanical revision, and an author whose draft holds no risk
// publish.
func TestPublishHoldsTheSecondPerson(t *testing.T) {
	t.Parallel()
	f, lars, rob := secondPersonFixture(t)
	ctx := context.Background()
	gone, goneID := f.mintToken(t, "drafter2", "drafts:read,drafts:write,apps:read")
	byKim := f.create(t, f.root, removalDoc("App", "srv-a")).Draft.ID
	byToken := f.create(t, f.token, removalDoc("App", "srv-b")).Draft.ID
	byGone := f.create(t, gone, removalDoc("App", "srv-c")).Draft.ID
	if code := adminReq(t, http.MethodDelete, f.base+"/v1/admin/api-tokens/"+goneID, f.root, nil, nil); code != http.StatusOK && code != http.StatusNoContent {
		t.Fatalf("revoke drafter2 = %d", code)
	}
	byRob := f.agentDraft(t, rob, removalDoc("App", "srv-d"))
	mechanical := f.create(t, f.root, removalDoc("App", "srv-e")).Draft.ID
	row, items := f.stored(t, mechanical)
	larsUser, err := f.app.store.Users().GetByUsername(ctx, "lars")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.app.store.Drafts().Revise(ctx, row.ID, store.DraftRevise{From: 1, Items: items,
		Rev: store.DraftRevisionRow{Author: store.DraftActor{ID: larsUser.ID, Name: "lars", Via: laneLogin, Client: clientLogin}, Door: "api", Digest: "d", Mechanical: true}}); err != nil {
		t.Fatal(err)
	}
	noRisk := f.create(t, f.root, draftRole("helpers", "Helpers.")).Draft.ID
	for _, tc := range []struct {
		name, bearer, id string
		code             int
		want             string
	}{
		{"its author", f.root, byKim, http.StatusConflict, secondPersonAuthorRefusal},
		{"the minter of the token that wrote it", f.root, byToken, http.StatusConflict, fmt.Sprintf(secondPersonMinterRefusal, "drafter")},
		{"anyone, when the token's minter cannot be traced", lars, byGone, http.StatusConflict, fmt.Sprintf(secondPersonUnknownRefusal, "drafter2")},
		{"the sponsor of the agent that wrote it", f.root, byRob, http.StatusConflict, fmt.Sprintf(secondPersonSponsorRefusal, "rob")},
		{"another person", lars, byKim, http.StatusOK, ""},
		{"the author of a mechanical revision", lars, mechanical, http.StatusOK, ""},
		{"an author whose draft holds no risk", f.root, noRisk, http.StatusOK, ""},
	} {
		if code, a := f.publishAll(t, tc.bearer, tc.id); code != tc.code || a.Error != tc.want {
			t.Errorf("%s = %d %q, want %d %q", tc.name, code, a.Error, tc.code, tc.want)
		}
	}
}

// TestPublishNeedsEveryAcknowledgment pins step 6 on a typed risk: a
// missing tick and a missing text answer the risk's sentence, a wrong text
// its own, a text in another case with spaces around it passes, a key of
// no current risk is ignored, and the reviewed digest is kept beside the
// current one with the keys and never the text.
func TestPublishNeedsEveryAcknowledgment(t *testing.T) {
	t.Parallel()
	f := newDraftsFixture(t, nil)
	id := f.create(t, f.root, removalDoc("App", "jira")).Draft.ID
	rev, v := f.read(t, f.root, id)
	var removal pubFinding
	for _, r := range v.Risks {
		if r.Code == "server.removal" {
			removal = r
		}
	}
	if removal.Typed != "jira" {
		t.Fatalf("risks = %+v, want server.removal typed jira", v.Risks)
	}
	body := func(tick bool, text string, extra ...string) map[string]any {
		b := acksFor(rev, v)
		ticked := slices.DeleteFunc(slices.Clone(b["ticked"].([]string)), func(k string) bool { return !tick && k == removal.Key })
		b["ticked"] = append(ticked, extra...)
		typed := b["typed"].(map[string]string)
		delete(typed, removal.Key)
		if text != "" {
			typed[removal.Key] = text
		}
		return b
	}
	missing := "Publish refused: " + removal.Sentence + " Acknowledge it, typing jira, and publish again."
	for _, tc := range []struct {
		name string
		body map[string]any
		want string
	}{
		{"a missing tick", body(false, "jira"), missing},
		{"a missing text", body(true, ""), missing},
		{"a wrong text", body(true, "jora"), "Publish refused: the text typed for App/jira is not jira. Type jira exactly to acknowledge it, then publish again."},
	} {
		code, a, _ := f.publish(t, f.root, id, tc.body)
		if code != http.StatusConflict || a.Error != tc.want || len(a.Verdict.Risks) != len(v.Risks) {
			t.Errorf("%s = %d %q with %d risks, want 409 %q with the verdict", tc.name, code, a.Error, len(a.Verdict.Risks), tc.want)
		}
	}
	pass := body(true, "  JIRA ", "no-such-key")
	reviewed := strings.Repeat("ab", 32)
	pass["risk_digest"] = reviewed
	if code, a, _ := f.publish(t, f.root, id, pass); code != http.StatusOK {
		t.Fatalf("publish with the text in another case = %d %q", code, a.Error)
	}
	row, _ := f.stored(t, id)
	var acks struct {
		ReviewedDigest string   `json:"reviewedDigest"`
		RiskDigest     string   `json:"riskDigest"`
		Ticked         []string `json:"ticked"`
		Typed          []string `json:"typed"`
	}
	if err := json.Unmarshal([]byte(row.Acks), &acks); err != nil {
		t.Fatalf("acks %q: %v", row.Acks, err)
	}
	if acks.ReviewedDigest != reviewed || acks.RiskDigest != v.RiskDigest || !slices.Contains(acks.Typed, removal.Key) ||
		strings.Contains(row.Acks, "JIRA") || strings.Contains(row.Acks, "no-such-key") {
		t.Errorf("acks = %s, want the reviewed digest beside the current one and the typed key without its text", row.Acks)
	}
}

// TestPublishRefusesARiskCutForItsPublisher pins the cut-risk refusal of
// step 6: paul holds apps:write
// and drafts:read without apps:read, so a typed risk on a server reads cut
// for him and refuses whatever he sends, while a cut tick risk is
// acknowledged by its key and its missing tick quotes the cut words.
func TestPublishRefusesARiskCutForItsPublisher(t *testing.T) {
	t.Parallel()
	f := newDraftsFixture(t, nil, func(c *config.Config) { c.Admin.RoleAreas["app-writers"] = []string{"apps:write", "drafts:read"} })
	if _, err := f.app.store.Roles().Create(context.Background(), store.Role{Name: "app-writers"}); err != nil {
		t.Fatal(err)
	}
	mkHuman(t, f.app, "paul", "app-writers")
	paul := f.login(t, "paul")
	removal := f.create(t, f.root, removalDoc("App", "jira")).Draft.ID
	rev, v := f.read(t, paul, removal)
	if len(v.Risks) != 1 || v.Risks[0].Typed != "" {
		t.Fatalf("paul reads the risks %+v, want one typed risk with its text cut", v.Risks)
	}
	body := acksFor(rev, v)
	body["typed"] = map[string]string{v.Risks[0].Key: "jira"}
	cut := "Publish refused: draft " + removal + " holds a risk on something you cannot read, so you cannot type the text that acknowledges it. " +
		"Ask someone who can read everything the draft names to publish it."
	if code, a, _ := f.publish(t, paul, removal, body); code != http.StatusConflict || a.Error != cut || len(a.Verdict.Risks) != 1 {
		t.Errorf("paul's publish of a cut typed risk = %d %q, want 409 %q with the verdict", code, a.Error, cut)
	}
	added := f.create(t, f.root, draftApp("docs", "https://docs.example/mcp", "The docs server.")).Draft.ID
	rev, v = f.read(t, paul, added)
	if len(v.Risks) != 1 || v.Risks[0].Ack != "tick" || v.Risks[0].Sentence != cutLine {
		t.Fatalf("paul reads the risks %+v, want one cut tick risk", v.Risks)
	}
	none := map[string]any{"revision": rev, "risk_digest": v.RiskDigest, "ticked": []string{}, "typed": map[string]string{}}
	if code, a, _ := f.publish(t, paul, added, none); code != http.StatusConflict || a.Error != "Publish refused: "+cutLine+" Acknowledge it and publish again." {
		t.Errorf("paul's publish without the tick = %d %q", code, a.Error)
	}
	if code, a, _ := f.publish(t, paul, added, acksFor(rev, v)); code != http.StatusOK {
		t.Errorf("paul's publish with the key ticked = %d %q, want 200", code, a.Error)
	}
}

// TestPublishWritesTheDraftAndAppliesIt pins the publish route's 200 and
// step 8: a
// new server, its role with an access row and a set are written with the
// draft marked published and its acknowledgments, the generation moves by
// one, and this replica runs the server, serves the row and enforces the
// set. A second publish that drops an implication drops the session that
// held it.
func TestPublishWritesTheDraftAndAppliesIt(t *testing.T) {
	t.Parallel()
	f := newDraftsFixture(t, nil)
	ctx := context.Background()
	up := startTaggedUpstream(t, "from-one")
	id := f.create(t, f.root, string(taggedManifest(up.URL)), typedRole("tagged-whoami", "tagged", "whoami"), draftSet("guard")).Draft.ID
	gen := f.generation(t)
	code, a := f.publishAll(t, f.root, id)
	if code != http.StatusOK {
		t.Fatalf("publish = %d %q %+v", code, a.Error, a.Verdict)
	}
	if a.Draft.State != "published" || a.Draft.DecidedBy == nil || a.Draft.DecidedBy.Username != "kim" {
		t.Errorf("answered draft = %+v, want published by kim", a.Draft)
	}
	if cur := f.app.snapshots.Current(); cur == nil || a.Snapshot == "" || cur.ID != a.Snapshot {
		t.Errorf("answered snapshot %q, want the live one", a.Snapshot)
	}
	if len(a.Servers) != 1 || a.Servers[0].Name != "tagged" || a.Servers[0].Change != "created" || a.Servers[0].Status != "running" || a.Next == nil {
		t.Errorf("servers %+v and next %v, want tagged created and running, and a list of next steps", a.Servers, a.Next)
	}
	row, _ := f.stored(t, id)
	if row.State != "published" || row.PublishedSnapshot != a.Snapshot || !strings.Contains(row.Acks, `"reviewedDigest"`) {
		t.Errorf("stored draft %s on %q with acks %s", row.State, row.PublishedSnapshot, row.Acks)
	}
	if got := f.generation(t); got != gen+1 {
		t.Errorf("generation %d after the publish, want %d", got, gen+1)
	}
	bindings, _ := f.app.gateway.bindings.Load().([]gwBinding)
	if !slices.ContainsFunc(bindings, func(b gwBinding) bool { return b.Role == "tagged-whoami" && b.App == "tagged" }) {
		t.Errorf("gateway rows %+v, want tagged-whoami on tagged", bindings)
	}
	w, err := f.app.readWorld(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := w.Policies["guard"]; !ok || w.SnapshotID != a.Snapshot {
		t.Errorf("live sets %v on %s, want guard in the published snapshot", w.Policies, w.SnapshotID)
	}
	tok := sessionToken(t, f.base, "kim")
	session := sessionOf(t, f.app, tok)
	if _, ok := f.app.subjects.get(session); !ok {
		t.Fatal("kim's session holds no subject after its check-in")
	}
	dev := "apiVersion: straza.dev/v1beta1\nkind: Role\nmetadata:\n    name: dev\nspec:\n    kind: application\n"
	drop := f.create(t, f.root, dev).Draft.ID
	if code, a := f.publishAll(t, f.root, drop); code != http.StatusOK {
		t.Fatalf("publish of dev without reader = %d %q", code, a.Error)
	}
	if _, ok := f.app.subjects.get(session); ok {
		t.Error("kim's session keeps its subject after dev stopped implying reader")
	}
}

// outboxOf answers every event of the outbox that names draft id, oldest
// first, as subject, source and data.
func outboxOf(t *testing.T, app *App, id string) []recordedEvent {
	t.Helper()
	rows, err := app.store.Outbox().ListRecent(context.Background(), 1000)
	if err != nil {
		t.Fatal(err)
	}
	var out []recordedEvent
	for i := len(rows) - 1; i >= 0; i-- {
		var ce struct {
			Source string         `json:"source"`
			Data   map[string]any `json:"data"`
		}
		if err := json.Unmarshal([]byte(rows[i].CE), &ce); err != nil {
			t.Fatal(err)
		}
		if ce.Data["draft"] == id || ce.Data["closedBy"] == id {
			out = append(out, recordedEvent{subject: rows[i].Subject, source: ce.Source, data: ce.Data, raw: rows[i].CE})
		}
	}
	return out
}

// recordedEvent is one outbox event as a test reads it.
type recordedEvent struct {
	subject, source, raw string
	data                 map[string]any
}

// TestPublishRecordsEachChangeOnce pins the records through the route
// against the spec/events examples: every event names the draft and this
// replica as its source, each action once, and draft.publish, apps.install,
// roles.implication.create and the publish event carry the example's
// fields.
func TestPublishRecordsEachChangeOnce(t *testing.T) {
	t.Parallel()
	f := newDraftsFixture(t, nil)
	up := startTaggedUpstream(t, "from-one")
	// A role a server owns composes no other role, so the edge goes out of
	// the live business role reader to the new role.
	reader := "apiVersion: straza.dev/v1beta1\nkind: Role\nmetadata:\n    name: reader\nspec:\n    kind: business\n    implies:\n        - tagged-whoami\n"
	id := f.create(t, f.strazactl, string(taggedManifest(up.URL)), typedRole("tagged-whoami", "tagged", "whoami"), reader, draftSet("guard")).Draft.ID
	if code, a := f.publishAll(t, f.strazactl, id); code != http.StatusOK {
		t.Fatalf("publish = %d %q", code, a.Error)
	}
	events := outboxOf(t, f.app, id)
	seen := map[string]int{}
	byAction := map[string]map[string]any{}
	for _, ev := range events {
		if ev.source != f.app.instance {
			t.Errorf("%s comes from %s, want %s", ev.raw, ev.source, f.app.instance)
		}
		key := ev.subject
		if action, ok := ev.data["action"].(string); ok {
			key = action
		} else if change, ok := ev.data["change"].(string); ok {
			key += " " + change
		}
		seen[key]++
		byAction[key] = ev.data
	}
	for key, n := range seen {
		if n != 1 && key != "straza.identity.updated" {
			t.Errorf("%s written %d times, want once", key, n)
		}
	}
	for _, tc := range []struct{ key, example string }{
		{"draft.publish", "valid-audit-admin-draft-publish.json"},
		{"apps.install", "valid-audit-admin-apps-install-draft.json"},
		{"roles.implication.create", "valid-audit-admin-roles-implication-create.json"},
		{"straza.apps.updated publish", "valid-apps-updated-publish.json"},
	} {
		data, ok := byAction[tc.key]
		if !ok {
			t.Errorf("no %s record among %v", tc.key, seen)
			continue
		}
		if got, want := fieldNames(data), exampleFields(t, tc.example); got != want {
			t.Errorf("%s fields = %s, want the example's %s", tc.key, got, want)
		}
	}
	if p := byAction["draft.publish"]; p["client"] != "strazactl" || p["proposer"] != "kim" || p["actorVia"] != laneSession {
		t.Errorf("draft.publish = %v, want kim's strazactl session", p)
	}
	for _, key := range []string{"roles.create", "apps.binding.create", "policy.create", "policy.activate", "straza.policy.updated", "straza.apps.updated binding"} {
		if seen[key] != 1 {
			t.Errorf("%s written %d times, want once", key, seen[key])
		}
	}
}

// TestPublishRecordsHoldNoText pins that no note, typed text or document
// text enters a record of the publish.
func TestPublishRecordsHoldNoText(t *testing.T) {
	t.Parallel()
	f := newDraftsFixture(t, nil)
	code, a := f.call(t, http.MethodPost, "/v1/admin/drafts", f.root, map[string]any{"note": "night-shift-note-7731",
		"documents": []string{draftApp("wiki", "http://127.0.0.1:9/mcp", "The lantern wiki."), draftRole("helpers", "Helpers of the lantern team.")}})
	if code != http.StatusCreated {
		t.Fatalf("create = %d %q", code, a.Error)
	}
	id := a.Draft.ID
	_, v := f.read(t, f.root, id)
	typed := ""
	for _, r := range v.Risks {
		if r.Typed != "" {
			typed = r.Typed
		}
	}
	if typed == "" {
		t.Fatalf("risks %+v, want a typed one", v.Risks)
	}
	if code, a := f.publishAll(t, f.root, id); code != http.StatusOK {
		t.Fatalf("publish = %d %q", code, a.Error)
	}
	for _, ev := range outboxOf(t, f.app, id) {
		for _, text := range []string{"night-shift-note-7731", "lantern", "team-a", typed} {
			if strings.Contains(ev.raw, text) {
				t.Errorf("%s holds %q", ev.raw, text)
			}
		}
	}
}

// TestPublishClosesTheSetsSlotDrafts pins the saved-edit close on the
// drafts route: a
// publish that writes another text of a set, turns it off or removes it
// discards the set's saved edit with its reason and one draft.discard,
// and a publish of the saved edit itself is not closed.
func TestPublishClosesTheSetsSlotDrafts(t *testing.T) {
	t.Parallel()
	f := newDraftsFixture(t, nil)
	ctx := context.Background()
	kim := store.DraftActor{ID: f.kim.ID, Name: "kim", Via: laneLogin, Client: clientLogin}
	for _, name := range []string{"guard", "gate"} {
		if code, out, _ := adminBytes(t, http.MethodPut, f.base+"/v1/admin/policies", f.root, "application/yaml", []byte(draftSet(name))); code != http.StatusCreated {
			t.Fatalf("store %s = %d %s", name, code, out)
		}
		if code := adminReq(t, http.MethodPost, f.base+"/v1/admin/policies/"+name+"/activate", f.root, map[string]string{"status": "active"}, nil); code != http.StatusOK {
			t.Fatalf("activate %s = %d", name, code)
		}
	}
	slot := func(name, why string) string {
		t.Helper()
		row, err := f.app.store.Drafts().Create(ctx, store.DraftRow{Door: "api", Slot: "policy:" + name},
			[]store.DraftItemRow{{Kind: "PolicySet", Name: name, Op: "put", Doc: setText(name, why)}},
			store.DraftRevisionRow{Author: kim, Door: "api", Digest: "d"})
		if err != nil {
			t.Fatal(err)
		}
		return strconv.FormatInt(row.ID, 10)
	}
	items := func(op, text string) map[string]any {
		return map[string]any{"items": []map[string]any{{"kind": "PolicySet", "name": "guard", "op": op, "doc": text}}}
	}
	for _, tc := range []struct {
		name, reason string
		body         map[string]any
	}{
		{"another text", "draft %s published another text of guard", map[string]any{"documents": []string{setText("guard", "Straza: v3")}}},
		{"turned off", "draft %s turned guard off", items("off", setText("guard", "Straza: v3"))},
		{"removed", "draft %s removed guard", map[string]any{"documents": []string{removalDoc("PolicySet", "guard")}}},
	} {
		saved := slot("guard", "Straza: saved "+tc.name)
		code, a := f.call(t, http.MethodPost, "/v1/admin/drafts", f.root, tc.body)
		if code != http.StatusCreated {
			t.Fatalf("%s: create = %d %q %v", tc.name, code, a.Error, a.Findings)
		}
		if code, p := f.publishAll(t, f.root, a.Draft.ID); code != http.StatusOK {
			t.Fatalf("%s: publish = %d %q", tc.name, code, p.Error)
		}
		row, _ := f.stored(t, saved)
		reason := fmt.Sprintf(tc.reason, a.Draft.ID)
		if row.State != "discarded" || row.DecidedReason != reason || row.DecidedBy.Name != "kim" {
			t.Errorf("%s: the saved edit is %s with %q by %q, want discarded with %q by kim", tc.name, row.State, row.DecidedReason, row.DecidedBy.Name, reason)
		}
		discards := draftRecords(t, f.app, "draft.discard", saved)
		if len(discards) != 1 || discards[0]["closedBy"] != a.Draft.ID || discards[0]["reason"] != reason || discards[0]["revision"] != float64(1) {
			t.Errorf("%s: draft.discard records %v, want one closed by %s", tc.name, discards, a.Draft.ID)
		}
	}
	own := slot("gate", "Straza: saved gate")
	if code, a := f.publishAll(t, f.root, own); code != http.StatusOK {
		t.Fatalf("publish of the saved edit = %d %q", code, a.Error)
	}
	if row, _ := f.stored(t, own); row.State != "published" {
		t.Errorf("the saved edit published itself as %s", row.State)
	}
}

// publishHook wraps a store so a test can act around Publish: before runs
// ahead of the nth call and answers what Publish answers in its place,
// nil to let it run, after turns the answer of a Publish that ran, and a
// set failGet fails every draft read.
type publishHook struct {
	store.Store
	before  atomic.Pointer[func(context.Context, int) error]
	after   atomic.Pointer[func(int, error) error]
	failGet atomic.Bool
	calls   atomic.Int32
	// afterGet runs once, after the next draft read answered.
	afterGet atomic.Pointer[func()]
}

func (s *publishHook) Drafts() store.DraftRepo { return publishHookRepo{s.Store.Drafts(), s} }

type publishHookRepo struct {
	store.DraftRepo
	s *publishHook
}

func (r publishHookRepo) Publish(ctx context.Context, plan store.PublishPlan) (store.PublishResult, error) {
	n := int(r.s.calls.Add(1))
	if before := r.s.before.Load(); before != nil {
		if err := (*before)(ctx, n); err != nil {
			return store.PublishResult{}, err
		}
	}
	res, err := r.DraftRepo.Publish(ctx, plan)
	if after := r.s.after.Load(); after != nil {
		err = (*after)(n, err)
	}
	return res, err
}

func (r publishHookRepo) Get(ctx context.Context, id int64) (store.DraftRow, []store.DraftItemRow, error) {
	if r.s.failGet.Load() {
		return store.DraftRow{}, nil, errors.New("injected read failure")
	}
	row, items, err := r.DraftRepo.Get(ctx, id)
	if then := r.s.afterGet.Swap(nil); then != nil {
		(*then)()
	}
	return row, items, err
}

// hooked builds the drafts fixture on a publishHook and answers both.
func hooked(t *testing.T) (*draftsFixture, *publishHook) {
	t.Helper()
	h := &publishHook{}
	f := newDraftsFixture(t, []func(*App){func(a *App) { h.Store = a.store; a.store = h }})
	return f, h
}

// arm sets the hooks of h for one case, nil for none, and resets its
// count.
func (h *publishHook) arm(before func(context.Context, int) error, after func(int, error) error) {
	h.calls.Store(0)
	h.before.Store(nil)
	h.after.Store(nil)
	if before != nil {
		h.before.Store(&before)
	}
	if after != nil {
		h.after.Store(&after)
	}
}

// disarm clears the hooks of h and keeps its count.
func (h *publishHook) disarm() {
	h.before.Store(nil)
	h.after.Store(nil)
	h.failGet.Store(false)
}

// TestPublishRunsAgainWhenLiveStateMoved pins the rerun rule on the publish
// route: a
// conflict sends the publish back to its World read, so it lands on a run
// that finds live state unmoved, and otherwise the rerun answers what the
// new state holds: draft.stale, a new risk, a lost standing. Three
// conflicts answer 409 and three busy answers 503 with nothing written,
// and a draft conflict answers step 2's sentences.
func TestPublishRunsAgainWhenLiveStateMoved(t *testing.T) {
	t.Parallel()
	f, h := hooked(t)
	ctx := context.Background()
	moved := store.PublishConflict{Generation: true}
	role := func(name string) func(context.Context, int) error {
		return func(ctx context.Context, n int) error {
			if n > 1 {
				return nil
			}
			if _, err := h.Store.Roles().Create(ctx, store.Role{Name: name}); err != nil {
				return err
			}
			return moved
		}
	}
	spare, err := h.Store.Roles().Create(ctx, store.Role{Name: "github-spare", Kind: store.RoleKindApplication, OwnerAppID: f.github.ID})
	if err != nil {
		t.Fatal(err)
	}
	holder := mkHuman(t, f.app, "uma")
	if code := adminReq(t, http.MethodPost, f.base+"/v1/admin/roles", f.root, map[string]any{"name": "github-helpers", "server": "github", "tools": []string{"get_*"}}, nil); code != http.StatusCreated {
		t.Fatalf("create github-helpers = %d", code)
	}
	owned, err := h.Store.Roles().GetByName(ctx, "github-helpers")
	if err != nil {
		t.Fatal(err)
	}
	assign := func(roleID string) func(context.Context, int) error {
		return func(ctx context.Context, n int) error {
			if n > 1 {
				return nil
			}
			if _, err := h.Store.Roles().Assign(ctx, store.RoleAssignment{SubjectKind: store.SubjectUser, SubjectID: holder.ID, RoleID: roleID}); err != nil {
				return err
			}
			f.app.resolver.Bump()
			return moved
		}
	}
	always := func(err error) func(context.Context, int) error {
		return func(context.Context, int) error { return err }
	}
	kim := store.DraftActor{ID: f.kim.ID, Name: "kim", Via: laneLogin, Client: clientLogin}
	for _, tc := range []struct {
		name, bearer, doc string
		before            func(string) func(context.Context, int) error
		code, calls       int
		want              func(id string, a pubAnswer) string
	}{
		{"a run after live state moved elsewhere lands", f.root, draftRole("helpers", "Helpers."),
			func(string) func(context.Context, int) error { return role("bystander") }, http.StatusOK, 2, nil},
		{"a run after the object moved goes stale", f.root, draftRole("late", "Late."),
			func(string) func(context.Context, int) error { return role("late") }, http.StatusConflict, 1,
			func(id string, a pubAnswer) string {
				if len(a.Verdict.Refused) == 0 || a.Verdict.Refused[0].Code != "draft.stale" {
					return "want draft.stale"
				}
				return ""
			}},
		{"a run that meets a new risk refuses on it", f.root, typedRole("github-spare", "github", "*"),
			func(string) func(context.Context, int) error { return assign(spare.ID) }, http.StatusConflict, 1,
			func(id string, a pubAnswer) string {
				if !strings.HasPrefix(a.Error, "Publish refused: ") || len(a.Verdict.Risks) == 0 {
					return "want step 6's refusal with the new verdict"
				}
				return ""
			}},
		{"a run that meets a holder loses the standing", f.erin, removalDoc("Role", "github-helpers"),
			func(string) func(context.Context, int) error { return assign(owned.ID) }, http.StatusForbidden, 1,
			func(id string, a pubAnswer) string {
				want := "You cannot publish draft " + id + ": the role github-helpers has 1 holder. The identity manager removes them first, then delete it."
				if a.Error != want {
					return "want " + want
				}
				return ""
			}},
		{"three conflicts", f.root, draftRole("thrice", "Thrice."),
			func(string) func(context.Context, int) error { return always(moved) }, http.StatusConflict, 3,
			func(id string, a pubAnswer) string {
				if a.Error != "Publish refused: other publishes kept changing live state while this one was checked, so nothing was published. Publish again." {
					return "want the moved sentence"
				}
				return ""
			}},
		{"three busy answers", f.root, draftRole("busy", "Busy."),
			func(string) func(context.Context, int) error { return always(store.PublishConflict{Busy: true}) }, http.StatusServiceUnavailable, 3,
			func(id string, a pubAnswer) string {
				want := "Straza could not publish draft " + id + ", because the database turned the change away three times while other changes were written. " +
					"Nothing was published. Publish again in a moment."
				if a.Error != want {
					return "want " + want
				}
				return ""
			}},
		{"a draft conflict", f.root, draftRole("gone", "Gone."),
			func(id string) func(context.Context, int) error {
				return func(ctx context.Context, n int) error {
					n64, _ := strconv.ParseInt(id, 10, 64)
					if _, err := h.Store.Drafts().Close(ctx, n64, 0, "discarded", kim, "", time.Now()); err != nil {
						return err
					}
					return store.PublishConflict{Draft: true}
				}
			}, http.StatusConflict, 1,
			func(id string, a pubAnswer) string {
				if a.Error != "Draft "+id+" is discarded, so it cannot change. Create a new draft from its documents." {
					return "want step 2's sentence"
				}
				return ""
			}},
	} {
		id := f.create(t, tc.bearer, tc.doc).Draft.ID
		rev, v := f.read(t, tc.bearer, id)
		gen := f.generation(t)
		h.arm(tc.before(id), nil)
		code, a, _ := f.publish(t, tc.bearer, id, acksFor(rev, v))
		h.disarm()
		if code != tc.code {
			t.Errorf("%s = %d %q, want %d", tc.name, code, a.Error, tc.code)
			continue
		}
		if tc.want != nil {
			if why := tc.want(id, a); why != "" {
				t.Errorf("%s answered %q: %s", tc.name, a.Error, why)
			}
		}
		published := len(draftRecords(t, f.app, "draft.publish", id))
		switch {
		case code == http.StatusOK && (published != 1 || f.generation(t) != gen+1):
			t.Errorf("%s wrote %d draft.publish records and moved the generation to %d from %d, want one publish", tc.name, published, f.generation(t), gen)
		case code != http.StatusOK && (published != 0 || f.generation(t) != gen):
			t.Errorf("%s wrote %d draft.publish records and moved the generation, want nothing written", tc.name, published)
		}
		if int(h.calls.Load()) != tc.calls {
			t.Errorf("%s ran Publish %d times, want %d", tc.name, h.calls.Load(), tc.calls)
		}
	}
}

// snapshotMoves wraps a store so the first read of a set by name while
// armed activates another snapshot, as a publish that lands between a
// World read and its Build does.
type snapshotMoves struct {
	store.Store
	armed *atomic.Bool
	move  func(context.Context) error
	err   *atomic.Value
}

func (s snapshotMoves) Policies() store.PolicyRepo { return snapshotMovesRepo{s.Store.Policies(), s} }

type snapshotMovesRepo struct {
	store.PolicyRepo
	s snapshotMoves
}

func (r snapshotMovesRepo) GetByName(ctx context.Context, name string) (store.PolicySet, error) {
	if r.s.armed.Swap(false) {
		if err := r.s.move(ctx); err != nil {
			r.s.err.Store(err.Error())
		}
	}
	return r.PolicyRepo.GetByName(ctx, name)
}

// TestPublishRunsAgainWhenBuildMeetsANewSnapshot pins the rerun rule for
// Build: a
// snapshot that another publish activated between the World read and the
// Build sends the publish back to its World read, and it lands on the new
// snapshot with both sets in it.
func TestPublishRunsAgainWhenBuildMeetsANewSnapshot(t *testing.T) {
	t.Parallel()
	armed, failed := &atomic.Bool{}, &atomic.Value{}
	var app *App
	move := func(ctx context.Context) error {
		act, err := app.store.Snapshots().GetActive(ctx)
		if err != nil {
			return err
		}
		b, err := app.snapshots.Build(ctx, act.ID, map[string][]byte{"other": []byte(draftSet("other"))})
		if err != nil {
			return err
		}
		if _, err := app.store.Snapshots().Create(ctx, store.Snapshot{ID: b.ID, SignerKeyID: b.SignerKeyID, Blob: b.Blob}); err != nil && !errors.Is(err, store.ErrConflict) {
			return err
		}
		return app.store.Snapshots().SetActive(ctx, b.ID)
	}
	f := newDraftsFixture(t, []func(*App){func(a *App) { app = a; a.store = snapshotMoves{a.store, armed, move, failed} }})
	id := f.create(t, f.root, draftSet("fresh")).Draft.ID
	rev, v := f.read(t, f.root, id)
	armed.Store(true)
	code, a, _ := f.publish(t, f.root, id, acksFor(rev, v))
	if msg := failed.Load(); msg != nil {
		t.Fatalf("moving the snapshot failed: %v", msg)
	}
	if code != http.StatusOK {
		t.Fatalf("publish = %d %q", code, a.Error)
	}
	w, err := f.app.readWorld(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if _, fresh := w.Policies["fresh"]; !fresh || w.SnapshotID != a.Snapshot {
		t.Errorf("live sets %v on %s, want fresh in the answered snapshot %s", w.Policies, w.SnapshotID, a.Snapshot)
	}
	if _, other := w.Policies["other"]; !other {
		t.Errorf("live sets %v, want the set of the snapshot that moved kept", w.Policies)
	}
}

// TestPublishAfterAnUnconfirmedCommit pins the publish route's lost-answer
// path: after a Publish error that
// is no conflict the route reads the draft back. Published by the caller,
// it answers 200 after the apply. Still open, it answers that nothing was
// published. Unread, it applies anyway and says to read the draft first.
func TestPublishAfterAnUnconfirmedCommit(t *testing.T) {
	t.Parallel()
	f, h := hooked(t)
	lost := errors.New("the connection dropped")
	for _, tc := range []struct {
		name   string
		before func(context.Context, int) error
		after  func(int, error) error
		code   int
		want   func(id string) string
		state  string
	}{
		{"a commit whose answer was lost", nil, func(int, error) error { return lost }, http.StatusOK, func(string) string { return "" }, "published"},
		{"an error with nothing committed", func(context.Context, int) error { return lost }, nil, http.StatusInternalServerError,
			func(id string) string {
				return "Straza could not publish draft " + id + ", so nothing was published. Try again, and read the strazad log if it keeps failing."
			}, "open"},
		{"a commit whose answer and read back were lost", nil, func(int, error) error { h.failGet.Store(true); return lost }, http.StatusInternalServerError,
			func(id string) string {
				return "Straza could not confirm whether draft " + id + " was published, because the database did not answer. " +
					"Read it with strazactl drafts show " + id + ", which says whether it was, before you publish it again."
			}, "published"},
	} {
		id := f.create(t, f.root, draftRole("r-"+strconv.Itoa(tc.code)+"-"+strconv.Itoa(len(tc.name)), "A role.")).Draft.ID
		rev, v := f.read(t, f.root, id)
		h.arm(tc.before, tc.after)
		code, a, _ := f.publish(t, f.root, id, acksFor(rev, v))
		h.disarm()
		if code != tc.code || a.Error != tc.want(id) {
			t.Errorf("%s = %d %q, want %d %q", tc.name, code, a.Error, tc.code, tc.want(id))
		}
		if row, _ := f.stored(t, id); row.State != tc.state {
			t.Errorf("%s left the draft %s, want %s", tc.name, row.State, tc.state)
		}
		if tc.state == "published" && f.app.appliedGen.Load() != f.generation(t) {
			t.Errorf("%s: this replica applied generation %d, want %d", tc.name, f.app.appliedGen.Load(), f.generation(t))
		}
	}
}

// TestPublishAnswersTheServersItChanged pins the servers block of the
// publish answer: each
// server the publish created, changed or removed in item order with its
// status after the apply, a failed start's error as its detail with the
// draft still published, next from the fixes of the ready warnings, such as
// the secret a static credential waits for, and no detail for a server its
// publisher cannot read.
func TestPublishAnswersTheServersItChanged(t *testing.T) {
	t.Parallel()
	f := newDraftsFixture(t, nil, func(c *config.Config) { c.Admin.RoleAreas["app-writers"] = []string{"apps:write", "drafts:read"} })
	if _, err := f.app.store.Roles().Create(context.Background(), store.Role{Name: "app-writers"}); err != nil {
		t.Fatal(err)
	}
	mkHuman(t, f.app, "paul", "app-writers")
	up := startTaggedUpstream(t, "from-one")
	static := "  credential:\n    kind: static\n    inject: {as: header, name: Authorization, template: \"Bearer {{secret}}\"}\n"
	id := f.create(t, f.root, string(taggedManifest(up.URL)), draftApp("jira", "http://127.0.0.1:9/mcp", "The Jira server.")+static, removalDoc("App", "github")).Draft.ID
	_, v := f.read(t, f.root, id)
	code, a := f.publishAll(t, f.root, id)
	if code != http.StatusOK {
		t.Fatalf("publish = %d %q", code, a.Error)
	}
	got := make([]string, len(a.Servers))
	for i, s := range a.Servers {
		got[i] = s.Name + " " + s.Change + " " + s.Status
	}
	if want := []string{"tagged created running", "jira changed " + a.Servers[1].Status, "github removed removed"}; !slices.Equal(got, want) || a.Servers[1].Status == "" {
		t.Errorf("servers %v, want %v", got, want)
	}
	if a.Servers[1].Detail == "" || a.Servers[0].Detail != "" {
		t.Errorf("details %q and %q, want jira's start error alone", a.Servers[0].Detail, a.Servers[1].Detail)
	}
	var next []string
	for _, w := range v.Warnings {
		if strings.HasPrefix(w.Code, "ready.") && w.Fix != "" && !slices.Contains(next, w.Fix) {
			next = append(next, w.Fix)
		}
	}
	if len(next) == 0 || !slices.Equal(a.Next, next) {
		t.Errorf("next %q, want the ready fixes %q, jira's missing secret among them", a.Next, next)
	}
	if row, _ := f.stored(t, id); row.State != "published" {
		t.Errorf("the draft is %s after a failed start, want published", row.State)
	}
	cut := f.create(t, f.root, draftApp("jira", "http://127.0.0.1:9/other", "The Jira server.")).Draft.ID
	code, a = f.publishAll(t, f.login(t, "paul"), cut)
	if code != http.StatusOK || len(a.Servers) != 1 || a.Servers[0].Detail != "" {
		t.Errorf("paul's publish = %d %q %+v, want jira with no detail", code, a.Error, a.Servers)
	}
}

// planOf reads live state for d, stamps its items and answers the World,
// the stamped draft and planFor's plan.
func planOf(t *testing.T, app *App, d drafts.Draft) (drafts.World, draftPlan) {
	t.Helper()
	ctx := context.Background()
	rows := rowsOf(d.Items)
	canonicalRoles(rows, nil)
	d.Items = itemsOf(rows)
	w, in, err := app.draftWorld(ctx, d)
	if err != nil {
		t.Fatal(err)
	}
	if err := app.stampRows(ctx, w, rows); err != nil {
		t.Fatal(err)
	}
	d.Items = itemsOf(rows)
	dp, err := planFor(w, in, d, rows)
	if err != nil {
		t.Fatal(err)
	}
	return w, dp
}

// TestPublishHeadNamesWhatThePublishChanged pins planFor's head and
// servers: a new server is announced and not stopped, a changed and a
// removed server stop, the removed server's admin role and owned role go,
// a role whose row moves narrows its rows, a removed role goes, the edges
// before and after are the World's and the overlay's, and a draft equal to
// live names nothing.
func TestPublishHeadNamesWhatThePublishChanged(t *testing.T) {
	t.Parallel()
	f := newDraftsFixture(t, nil)
	ctx := context.Background()
	if code := adminReq(t, http.MethodPost, f.base+"/v1/admin/roles", f.root, map[string]any{"name": "github-helpers", "server": "github", "tools": []string{"get_*"}}, nil); code != http.StatusCreated {
		t.Fatalf("create github-helpers = %d", code)
	}
	if _, err := f.app.store.Roles().Create(ctx, store.Role{Name: "old"}); err != nil {
		t.Fatal(err)
	}
	dev := "apiVersion: straza.dev/v1beta1\nkind: Role\nmetadata:\n    name: dev\nspec:\n    kind: application\n    bindings:\n        - app: jira\n          tools:\n            - '*'\n"
	item := func(kind drafts.Kind, name string, op drafts.Op, doc string) drafts.Item {
		return drafts.Item{Kind: kind, Name: name, Op: op, Doc: doc}
	}
	w, dp := planOf(t, f.app, drafts.Draft{ID: "1", Door: drafts.DoorAPI, Items: []drafts.Item{
		item(drafts.KindApp, "fresh", drafts.OpPut, draftApp("fresh", "https://fresh.example/mcp", "Fresh.")),
		item(drafts.KindApp, "jira", drafts.OpPut, draftApp("jira", "https://jira.example/other", "The Jira server.")),
		item(drafts.KindApp, "github", drafts.OpRemove, ""),
		item(drafts.KindRole, "dev", drafts.OpPut, dev),
		item(drafts.KindRole, "old", drafts.OpRemove, ""),
	}})
	servers := make([]string, len(dp.servers))
	for i, s := range dp.servers {
		servers[i] = s.name + " " + s.change
	}
	sorted := func(s []string) []string { s = slices.Clone(s); sort.Strings(s); return s }
	switch {
	case !slices.Equal(servers, []string{"fresh created", "jira changed", "github removed"}):
		t.Errorf("servers %v", servers)
	case !slices.Equal(dp.head.Servers, []string{"jira", "github"}):
		t.Errorf("head servers %v, want the changed and the removed one", dp.head.Servers)
	case !slices.Equal(dp.head.AccessRoles, []string{"dev"}):
		t.Errorf("head access roles %v, want dev", dp.head.AccessRoles)
	case !slices.Equal(sorted(dp.head.Removed), []string{"github-helpers", f.adminRole(t, f.github), "old"}):
		t.Errorf("head removed %v", dp.head.Removed)
	case !slices.Contains(dp.head.Before["dev"], "reader") || slices.Contains(dp.head.After["dev"], "reader"):
		t.Errorf("edges before %v and after %v, want dev implying reader before only", dp.head.Before, dp.head.After)
	}
	for _, it := range dp.plan.Items {
		if it.Ref.Name == "github-helpers" && (!it.Implied || it.Op != "remove" || it.Base != string(w.Fingerprints["Role/github-helpers"])) {
			t.Errorf("the owned role's item %+v, want an implied removal from its live fingerprint", it)
		}
	}
	liveDev, _ := liveOf(t, f.app, "dev")
	_, docDev := liveOf(t, f.app, "dev")
	_, same := planOf(t, f.app, drafts.Draft{ID: "2", Door: drafts.DoorAPI, Items: []drafts.Item{item(drafts.KindRole, "dev", drafts.OpPut, docDev)}})
	if it := same.plan.Items[0]; it.After != it.Base || it.Base != liveDev || len(same.servers) != 0 || len(same.head.Servers)+len(same.head.AccessRoles)+len(same.head.Removed) != 0 {
		t.Errorf("a draft equal to live plans %+v with head %+v", it, same.head)
	}
}

// TestPlanForPublishesAOneItemDraft pins the pieces a direct route calls:
// a one-item draft planned by planFor and committed through
// PublishPlan.New lands published by its author, and its records name the
// minted draft and include no draft record.
func TestPlanForPublishesAOneItemDraft(t *testing.T) {
	t.Parallel()
	f := newDraftsFixture(t, nil)
	kim := drafts.Principal{UserID: f.kim.ID, Username: "kim", Via: laneLogin, Client: clientLogin}
	d := drafts.Draft{Door: drafts.DoorAPI, Authors: []drafts.Principal{kim},
		Items: []drafts.Item{{Kind: drafts.KindRole, Name: "helpers", Op: drafts.OpPut, Doc: draftRole("helpers", "Helpers.")}}}
	w, dp := planOf(t, f.app, d)
	rows := make([]store.DraftItemRow, len(dp.plan.Items))
	for i, it := range dp.plan.Items {
		rows[i] = store.DraftItemRow{Kind: it.Ref.Kind, Name: it.Ref.Name, Op: it.Op, Doc: it.AfterDoc, Base: it.Base, BaseOp: it.BaseOp, BaseDoc: it.BaseDoc}
	}
	dp.plan.New = &store.DraftNew{Row: store.DraftRow{Door: "api"}, Items: rows, Rev: store.DraftRevisionRow{Author: actorOf(kim), Door: "api", Digest: "d"}}
	dp.plan.Publisher, dp.plan.Acks = actorOf(kim), "{}"
	ctx := withActor(context.Background(), auditActor{Name: "kim", ID: f.kim.ID, Via: laneLogin})
	res, end, err := f.app.commit(ctx, w, &dp)
	if end != commitLanded || err != nil {
		t.Fatalf("commit = %v, %v", end, err)
	}
	id := strconv.FormatInt(res.DraftID, 10)
	row, _ := f.stored(t, id)
	if row.State != "published" || row.DecidedBy.Name != "kim" {
		t.Errorf("the minted draft is %s by %q, want published by kim", row.State, row.DecidedBy.Name)
	}
	events := outboxOf(t, f.app, id)
	if len(events) == 0 {
		t.Fatal("no record names the minted draft")
	}
	for _, ev := range events {
		if action, _ := ev.data["action"].(string); strings.HasPrefix(action, "draft.") {
			t.Errorf("a direct publish wrote %s", ev.raw)
		}
	}
}

// openAPISchema compiles one schema of pkg/api/openapi.yaml's components.
func openAPISchema(t *testing.T, name string) *jsonschema.Schema {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "pkg", "api", "openapi.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	var doc any
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	asJSON, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	v, err := jsonschema.UnmarshalJSON(bytes.NewReader(asJSON))
	if err != nil {
		t.Fatal(err)
	}
	// Formats are asserted, as a strict generated client reads them.
	c := jsonschema.NewCompiler()
	c.AssertFormat()
	if err := c.AddResource("openapi.json", v); err != nil {
		t.Fatal(err)
	}
	sch, err := c.Compile("openapi.json#/components/schemas/" + name)
	if err != nil {
		t.Fatalf("compile %s: %v", name, err)
	}
	return sch
}

// TestPublishAnswersMatchTheirSchemas pins the publish wire: a 200
// validates against DraftPublished, a step 6 refusal against DraftRefused,
// and DraftPublish takes a risk_digest of 64 hex characters or none.
func TestPublishAnswersMatchTheirSchemas(t *testing.T) {
	t.Parallel()
	f := newDraftsFixture(t, nil)
	ok := f.create(t, f.root, draftRole("helpers", "Helpers.")).Draft.ID
	rev, v := f.read(t, f.root, ok)
	_, _, published := f.publish(t, f.root, ok, acksFor(rev, v))
	refused := f.create(t, f.root, removalDoc("App", "jira")).Draft.ID
	rev, v = f.read(t, f.root, refused)
	_, _, conflict := f.publish(t, f.root, refused, map[string]any{"revision": rev, "risk_digest": v.RiskDigest, "ticked": []string{}, "typed": map[string]string{}})
	for _, tc := range []struct {
		schema string
		answer []byte
	}{
		{"DraftPublished", published},
		{"DraftRefused", conflict},
	} {
		inst, err := jsonschema.UnmarshalJSON(bytes.NewReader(tc.answer))
		if err != nil {
			t.Fatalf("%s: %v in %s", tc.schema, err, tc.answer)
		}
		if err := openAPISchema(t, tc.schema).Validate(inst); err != nil {
			t.Errorf("%s does not validate %s: %v", tc.schema, tc.answer, err)
		}
	}
	body := openAPISchema(t, "DraftPublish")
	for digest, ok := range map[string]bool{"": true, v.RiskDigest: true, strings.Repeat("A", 64): true, "free text": false, strings.Repeat("a", 63): false} {
		inst, err := jsonschema.UnmarshalJSON(strings.NewReader(`{"revision": 1, "risk_digest": "` + digest + `"}`))
		if err != nil {
			t.Fatal(err)
		}
		if err := body.Validate(inst); (err == nil) != ok {
			t.Errorf("DraftPublish with risk_digest %q validates %v, want %v", digest, err == nil, ok)
		}
	}
}

// TestPublishOfADraftPublishedWhileItWaited pins that each run reads the
// draft again under a.configMu (step 3): a second publish of the same
// draft, sent together with the first as a double click does, passes step
// 2 before the first commits and is then told who published the draft,
// never that it went stale.
func TestPublishOfADraftPublishedWhileItWaited(t *testing.T) {
	t.Parallel()
	f, h := hooked(t)
	for _, doc := range []string{draftRole("twice", "Twice."), removalDoc("App", "jira")} {
		id := f.create(t, f.root, doc).Draft.ID
		rev, v := f.read(t, f.root, id)
		body, err := json.Marshal(acksFor(rev, v))
		if err != nil {
			t.Fatal(err)
		}
		first := make(chan int, 1)
		// The first publish runs whole between the second's read of the
		// draft and its turn at the lock.
		then := func() {
			req, _ := http.NewRequest(http.MethodPost, f.base+"/v1/admin/drafts/"+id+"/publish", bytes.NewReader(body))
			req.Header.Set("Authorization", "Bearer "+f.root)
			req.Header.Set("Content-Type", "application/json")
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				first <- 0
				return
			}
			_ = resp.Body.Close()
			first <- resp.StatusCode
		}
		h.afterGet.Store(&then)
		code, a, _ := f.publish(t, f.root, id, acksFor(rev, v))
		h.afterGet.Store(nil)
		row, _ := f.stored(t, id)
		want := "Draft " + id + " was published by kim at " + row.DecidedAt.UTC().Format("2006-01-02 15:04:05") +
			" UTC, so nothing more was done. Read it with strazactl drafts show " + id + "."
		if got := <-first; got != http.StatusOK || code != http.StatusConflict || a.Error != want {
			t.Errorf("%s: the first publish answered %d and the second %d %q, want 200 and 409 %q", doc, got, code, a.Error, want)
		}
		if n := len(draftRecords(t, f.app, "draft.publish", id)); n != 1 {
			t.Errorf("%s: %d draft.publish records, want 1", doc, n)
		}
	}
}

// TestPublishRefusesARiskDigestThatIsNoDigest pins the publish body rule:
// risk_digest is the verdict's digest, 64 hex characters, or empty, since
// the draft's acks and its draft.publish record keep it, and anything else
// answers 400 with nothing written.
func TestPublishRefusesARiskDigestThatIsNoDigest(t *testing.T) {
	t.Parallel()
	f := newDraftsFixture(t, nil)
	want := "The request body is not a publish: risk_digest must be the verdict's risk digest, 64 hex characters, or empty. Send the digest the check answered."
	id := f.create(t, f.root, removalDoc("App", "jira")).Draft.ID
	rev, v := f.read(t, f.root, id)
	for _, digest := range []string{"free text: gh" + "p_" + strings.Repeat("Qq7Ww6Ee", 5), strings.Repeat("a", 63), strings.Repeat("a", 65), strings.Repeat("g", 64)} {
		body := acksFor(rev, v)
		body["risk_digest"] = digest
		if code, a, _ := f.publish(t, f.root, id, body); code != http.StatusBadRequest || a.Error != want {
			t.Errorf("risk_digest %.20q = %d %q, want 400 %q", digest, code, a.Error, want)
		}
	}
	if row, _ := f.stored(t, id); row.State != "open" || row.Acks != "" || len(draftRecords(t, f.app, "draft.publish", id)) != 0 {
		t.Errorf("a refused digest left the draft %s with acks %q", row.State, row.Acks)
	}
	for _, digest := range []string{"", strings.ToUpper(v.RiskDigest)} {
		id := f.create(t, f.root, draftRole("r"+strconv.Itoa(len(digest)), "A role.")).Draft.ID
		rev, v := f.read(t, f.root, id)
		body := acksFor(rev, v)
		body["risk_digest"] = digest
		if code, a, _ := f.publish(t, f.root, id, body); code != http.StatusOK {
			t.Errorf("risk_digest %q = %d %q, want 200", digest, code, a.Error)
		}
	}
}

// TestPublishStoresTheCheckItRan pins that a publish keeps the check it ran
// as the check of the published revision, so every read of a published
// draft answers the counts, snapshot and time its publish saw. A draft its
// create refused and live state then cleared answers no refusal, and a
// revision the server never checked answers the publish's check. The
// publish waives what live state holds as every door does, so a person's
// draft keeps the waiver's counts and an agent's draft of a live name
// publishes. A saved edit that activate publishes keeps the activate's
// check, which holds refusals alone while admin.secondPerson is off.
func TestPublishStoresTheCheckItRan(t *testing.T) {
	t.Parallel()
	f := newDraftsFixture(t, nil)
	ctx := context.Background()
	team := f.create(t, f.root, "apiVersion: straza.dev/v1beta1\nkind: Role\nmetadata:\n    name: team\nspec:\n    kind: business\n    implies:\n        - nosuch\n")
	if created, _ := f.stored(t, team.Draft.ID); !strings.Contains(created.CheckCounts, `"refused":1`) {
		t.Fatalf("the create of team stored the counts %s, want one refusal for the role nosuch it implies", created.CheckCounts)
	}
	if code := adminReq(t, http.MethodPost, f.base+"/v1/admin/roles", f.root, map[string]any{"name": "nosuch", "kind": "business"}, nil); code != http.StatusCreated {
		t.Fatalf("create the role nosuch = %d", code)
	}
	kim := store.DraftActor{ID: f.kim.ID, Name: "kim", Via: laneLogin, Client: clientLogin}
	stored := func(door string, by store.DraftActor, it store.DraftItemRow) string {
		row, err := f.app.store.Drafts().Create(ctx, store.DraftRow{Door: door}, []store.DraftItemRow{it}, store.DraftRevisionRow{Author: by})
		if err != nil {
			t.Fatal(err)
		}
		return strconv.FormatInt(row.ID, 10)
	}
	unchecked := stored("api", kim, store.DraftItemRow{Kind: "Role", Name: "helpers", Op: "put", Doc: draftRole("helpers", "Helpers."), BaseOp: "remove"})
	putServer(t, f.app, waivedRunner("runner", "The runner."))
	runner := f.create(t, f.root, waivedRunner("runner", "Changed.")).Draft.ID
	if _, err := f.app.store.Roles().Create(ctx, store.Role{Name: "dev\u200bops", Kind: store.RoleKindBusiness}); err != nil {
		t.Fatal(err)
	}
	bot, err := f.app.store.Users().GetByUsername(ctx, "bot")
	if err != nil {
		t.Fatal(err)
	}
	agent := stored(string(drafts.DoorAgent), store.DraftActor{ID: bot.ID, Name: "bot", Agent: true, Via: laneSession, Client: "claude-code",
		SponsorID: f.kim.ID, SponsorName: "kim"}, store.DraftItemRow{Kind: "Role", Name: "dev\u200bops", Op: "put", Doc: draftRole("dev\u200bops", "New words.")})
	policy := func(method, path, ct, body string) (int, string) {
		code, out, _ := adminBytes(t, method, f.base+path, f.root, ct, []byte(body))
		return code, string(out)
	}
	activate := func() (int, string) {
		return policy(http.MethodPost, "/v1/admin/policies/shell-guard/activate", "application/json", `{"status":"active"}`)
	}
	if code, out := policy(http.MethodPut, "/v1/admin/policies", "application/yaml", draftSet("shell-guard")); code != http.StatusCreated {
		t.Fatalf("store shell-guard = %d %s", code, out)
	}
	if code, out := activate(); code != http.StatusOK {
		t.Fatalf("activate shell-guard = %d %s", code, out)
	}
	edit := strings.Replace(draftSet("shell-guard"), "no shell for dev", "no shell at all for dev", 1)
	if code, out := policy(http.MethodPut, "/v1/admin/policies", "application/yaml", edit); code != http.StatusOK {
		t.Fatalf("save the edit of shell-guard = %d %s", code, out)
	}
	saved, _, err := f.app.store.Drafts().BySlot(ctx, "policy:shell-guard")
	if err != nil {
		t.Fatal(err)
	}
	publishAll := func(id string) func() (int, string) {
		return func() (int, string) {
			code, a := f.publishAll(t, f.root, id)
			return code, a.Error
		}
	}
	for _, tc := range []struct {
		name, id     string
		publish      func() (int, string)
		refusalsOnly bool
	}{
		{"refused at create and cleared on live state before the publish", team.Draft.ID, publishAll(team.Draft.ID), false},
		{"a revision the server never checked", unchecked, publishAll(unchecked), false},
		{"a person's draft of a server whose manifest holds the waived value", runner, publishAll(runner), false},
		{"an agent's draft of a role whose live name holds an invisible character", agent, publishAll(agent), false},
		{"a saved edit that activate publishes", strconv.FormatInt(saved.ID, 10), activate, true},
	} {
		_, open := f.get(t, f.root, tc.id)
		active := ""
		if sn, err := f.app.store.Snapshots().GetActive(ctx); err == nil {
			active = sn.ID
		}
		before := time.Now().UTC().Truncate(time.Second)
		if code, msg := tc.publish(); code != http.StatusOK {
			t.Fatalf("%s: publish = %d %s", tc.name, code, msg)
		}
		row, _ := f.stored(t, tc.id)
		_, got := f.get(t, f.root, tc.id)
		_, again := f.get(t, f.root, tc.id)
		v, c := got.Verdict, got.Checks
		if c == nil || c.Refused != 0 || c.Revision != row.Revision || v.Snapshot != active || v.CheckedAt != c.CheckedAt {
			t.Fatalf("%s: checks %+v, checked at %q against %q; want no refusal at revision %d, checked by the publish against %q",
				tc.name, c, v.CheckedAt, v.Snapshot, row.Revision, active)
		}
		if at, err := time.Parse(time.RFC3339, c.CheckedAt); err != nil || at.Before(before) || row.DecidedAt == nil || row.CheckedAt.After(*row.DecidedAt) {
			t.Errorf("%s: the stored check ran at %q and the publish at %v, want the check of this publish", tc.name, c.CheckedAt, row.DecidedAt)
		}
		if !reflect.DeepEqual(again.Checks, c) || again.Verdict.CheckedAt != v.CheckedAt || again.Verdict.Snapshot != v.Snapshot {
			t.Errorf("%s: a second read answers %+v at %q, want the same stored check %+v", tc.name, again.Checks, again.Verdict.CheckedAt, c)
		}
		switch {
		case tc.refusalsOnly && (c.Risks != 0 || c.Unchecked != 0):
			t.Errorf("%s: the stored check counts %d risks and %d unchecked, want a check of refusals alone", tc.name, c.Risks, c.Unchecked)
		case !tc.refusalsOnly && (c.Risks != len(open.Verdict.Risks) || c.Warnings != len(open.Verdict.Warnings)):
			t.Errorf("%s: the publish stored %d risks and %d warnings, and the read before it answered %d and %d",
				tc.name, c.Risks, c.Warnings, len(open.Verdict.Risks), len(open.Verdict.Warnings))
		}
	}
}

// TestDistinctRefusalsOfOneObjectStay pins that two different refusals of
// one code on one object are both answered, although they share a key: a
// Role that implies two missing roles reads two imply.missing lines on
// create, on GET, in the checker's stamp and in the publish's 409 alike.
func TestDistinctRefusalsOfOneObjectStay(t *testing.T) {
	t.Parallel()
	f := newDraftsFixture(t, nil)
	doc := "apiVersion: straza.dev/v1beta1\nkind: Role\nmetadata:\n    name: team\nspec:\n    kind: business\n    implies:\n        - nosuch\n        - other\n"
	created := f.create(t, f.root, doc)
	_, got := f.get(t, f.root, created.Draft.ID)
	_, _, raw := f.publish(t, f.root, created.Draft.ID, map[string]any{"revision": 1, "risk_digest": "", "ticked": []string{}, "typed": map[string]string{}})
	var refusal struct {
		Verdict wireVerdict `json:"verdict"`
	}
	if err := json.Unmarshal(raw, &refusal); err != nil {
		t.Fatal(err)
	}
	stored, _ := f.stored(t, created.Draft.ID)
	for _, tc := range []struct {
		name    string
		refused []wireFinding
	}{
		{"create", created.Verdict.Refused},
		{"GET", got.Verdict.Refused},
		{"the publish's 409", refusal.Verdict.Refused},
	} {
		var lines []string
		for _, fnd := range tc.refused {
			if fnd.Code == "imply.missing" {
				lines = append(lines, fnd.Sentence)
			}
		}
		if len(lines) != 2 || lines[0] == lines[1] {
			t.Errorf("%s answers the imply.missing lines %q, want one for nosuch and one for other", tc.name, lines)
		}
	}
	if !strings.Contains(stored.CheckCounts, `"refused":2`) {
		t.Errorf("the stored check counts %s, want two refusals", stored.CheckCounts)
	}
}
