package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/strazahq/straza/internal/drafts"
	"github.com/strazahq/straza/internal/manager"
	"github.com/strazahq/straza/internal/store"
)

// rebaseAnswer is what Check again answers: the draft and its verdict, or
// a refusal with the conflicts that need a pick.
type rebaseAnswer struct {
	wireAnswer
	Conflicts []drafts.Conflict `json:"conflicts"`
}

// rebase sends body to Check again of draft id as bearer, a string as it
// is and anything else as JSON.
func (f *draftsFixture) rebase(t *testing.T, id, bearer string, body any) (int, rebaseAnswer, string) {
	t.Helper()
	raw, _ := json.Marshal(body)
	if s, ok := body.(string); ok {
		raw = []byte(s)
	}
	code, raw, _ := adminBytes(t, http.MethodPost, f.base+"/v1/admin/drafts/"+id+"/rebase", bearer, "application/json", raw)
	var a rebaseAnswer
	if err := json.Unmarshal(raw, &a); err != nil {
		t.Fatalf("rebase %s: %v in %s", id, err, raw)
	}
	return code, a, string(raw)
}

// limitedApp is draftApp with the server's rate limit set to rps.
func limitedApp(name, url, description, rps string) string {
	return draftApp(name, url, description) + "  limits:\n    rps: " + rps + "\n"
}

// itemManifest parses the stored document of the item named name of draft
// id.
func (f *draftsFixture) itemManifest(t *testing.T, id, name string) manager.Manifest {
	t.Helper()
	_, items := f.stored(t, id)
	for _, it := range items {
		if it.Name == name && it.Kind == string(drafts.KindApp) {
			mf, err := manager.Parse([]byte(it.Doc))
			if err != nil {
				t.Fatalf("item %s: %v in %s", name, err, it.Doc)
			}
			return mf
		}
	}
	t.Fatalf("draft %s holds no App %s", id, name)
	return manager.Manifest{}
}

const githubURL = "https://api.github.example/mcp?region=eu"

// TestRebaseMovesTheBase pins Check again on a draft that went stale: the
// field the draft changed stays, the field live changed comes from live,
// every base becomes the live fingerprint so the stale refusal goes, the
// revision is mechanical, and one draft.update with rebase: true and one
// draft.check are written.
func TestRebaseMovesTheBase(t *testing.T) {
	t.Parallel()
	f := newDraftsFixture(t, nil)
	id := f.create(t, f.root, draftApp("github", githubURL, "A new description.")).Draft.ID
	putServer(t, f.app, limitedApp("github", githubURL, "The GitHub server.", "10"))
	if _, v := f.read(t, f.root, id); len(v.Refused) == 0 || v.Refused[0].Code != "draft.stale" {
		t.Fatalf("refused = %+v, want the draft stale", v.Refused)
	}
	checks := len(draftRecords(t, f.app, "draft.check", id))
	code, a, raw := f.rebase(t, id, f.root, map[string]any{"revision": 1})
	if code != http.StatusOK || a.Draft.Revision != 2 {
		t.Fatalf("rebase = %d %s", code, raw)
	}
	for _, r := range a.Verdict.Refused {
		if r.Code == "draft.stale" {
			t.Errorf("still stale: %+v", r)
		}
	}
	mf := f.itemManifest(t, id, "github")
	if mf.Metadata.Description != "A new description." || mf.Straza.Limits == nil || mf.Straza.Limits.RPS != 10 {
		t.Errorf("merged manifest: description %q, limits %+v", mf.Metadata.Description, mf.Straza.Limits)
	}
	row, items := f.stored(t, id)
	w, err := f.app.readWorld(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	live, err := store.FingerprintApp(w.Apps["github"].Manifest)
	if err != nil || items[0].Base != live || row.CheckedRevision != 2 {
		t.Errorf("base %s checked %d, want the live %s at revision 2 (%v)", items[0].Base, row.CheckedRevision, live, err)
	}
	revs, err := f.app.store.Drafts().Revisions(context.Background(), row.ID)
	if err != nil || len(revs) != 2 || !revs[1].Mechanical || revs[1].Author.Name != "kim" {
		t.Errorf("revisions = %+v, %v; want a mechanical revision 2 by kim", revs, err)
	}
	updates := draftRecords(t, f.app, "draft.update", id)
	if len(updates) != 1 || updates[0]["rebase"] != true || updates[0]["revision"] != float64(2) || updates[0]["actor"] != "kim" {
		t.Errorf("draft.update = %+v, want one with rebase true", updates)
	}
	if got := len(draftRecords(t, f.app, "draft.check", id)); got != checks+1 {
		t.Errorf("%d draft.check records, want %d", got, checks+1)
	}
}

// TestRebaseConflictsWriteNothing pins a field both sides changed apart: a
// 409 names it, its values masked as GET masks them, and nothing is written
// until a pick settles it, after which the revision is not mechanical.
func TestRebaseConflictsWriteNothing(t *testing.T) {
	t.Parallel()
	f := newDraftsFixture(t, nil)
	id := f.create(t, f.root, draftApp("github", "https://api2.github.example/mcp?region=us", "The GitHub server.")).Draft.ID
	putServer(t, f.app, draftApp("github", "https://api3.github.example/mcp?region=ap", "The GitHub server."))
	updates := len(draftRecords(t, f.app, "draft.update", id))
	code, a, raw := f.rebase(t, id, f.root, map[string]any{"revision": 1})
	want := "Draft " + id + " and live state both changed App/github server since the draft was checked. Pick which value to keep, then check again."
	if code != http.StatusConflict || a.Error != want {
		t.Fatalf("rebase = %d %s\nwant 409 %q", code, raw, want)
	}
	var url drafts.Conflict
	for _, c := range a.Conflicts {
		if c.Field == "straza.runtime.remote.url" {
			url = c
		}
	}
	if url != (drafts.Conflict{Object: "App/github", Field: "straza.runtime.remote.url", Base: "https://api.github.example/mcp?…",
		Draft: "https://api2.github.example/mcp?…", Live: "https://api3.github.example/mcp?…"}) || len(a.Conflicts) != 2 {
		t.Errorf("conflicts = %+v", a.Conflicts)
	}
	if strings.Contains(raw, "region=") {
		t.Errorf("the conflicts show a query GET masks: %s", raw)
	}
	if row, _ := f.stored(t, id); row.Revision != 1 || len(draftRecords(t, f.app, "draft.update", id)) != updates {
		t.Errorf("a conflict wrote: revision %d", row.Revision)
	}
	picks := map[string]string{"App/github server": "live", "App/github straza.runtime.remote.url": "draft"}
	if code, _, raw := f.rebase(t, id, f.root, map[string]any{"revision": 1, "picks": picks}); code != http.StatusOK {
		t.Fatalf("rebase with picks = %d %s", code, raw)
	}
	mf := f.itemManifest(t, id, "github")
	if mf.Straza.Runtime.Remote.URL != "https://api2.github.example/mcp?region=us" || !strings.Contains(fmt.Sprint(mf.Server), "api3") {
		t.Errorf("merged: url %s, server %v", mf.Straza.Runtime.Remote.URL, mf.Server)
	}
	revs, _ := f.app.store.Drafts().Revisions(context.Background(), mustID(t, id))
	if len(revs) != 2 || revs[1].Mechanical {
		t.Errorf("revisions = %+v, want revision 2 not mechanical", revs)
	}
}

// mustID reads a draft id.
func mustID(t *testing.T, id string) int64 {
	t.Helper()
	var n int64
	if _, err := fmt.Sscan(id, &n); err != nil {
		t.Fatal(err)
	}
	return n
}

// TestRebaseMergesARoleAndASet pins the fields of a role and of a set on
// the route: the role's description from live beside the draft's
// implications, and a set's text both sides changed, settled by a pick of
// live.
func TestRebaseMergesARoleAndASet(t *testing.T) {
	t.Parallel()
	f := newDraftsFixture(t, nil)
	role := "apiVersion: straza.dev/v1beta1\nkind: Role\nmetadata:\n    name: helpers\nspec:\n    kind: business\n    description: %s\n%s"
	live := f.create(t, f.root, fmt.Sprintf(role, "Old.", ""), draftSet("guard")).Draft.ID
	if code, a := f.publishAll(t, f.root, live); code != http.StatusOK {
		t.Fatalf("publish the live objects = %d %q", code, a.Error)
	}
	id := f.create(t, f.root, fmt.Sprintf(role, "Old.", "    implies:\n        - dev\n"),
		strings.Replace(draftSet("guard"), "no shell for dev", "no shell for dev at all", 1)).Draft.ID
	moved := f.create(t, f.root, fmt.Sprintf(role, "New.", ""), strings.Replace(draftSet("guard"), "no shell for dev", "never a shell", 1)).Draft.ID
	if code, a := f.publishAll(t, f.root, moved); code != http.StatusOK {
		t.Fatalf("publish the live change = %d %q", code, a.Error)
	}
	code, a, raw := f.rebase(t, id, f.root, map[string]any{"revision": 1})
	if code != http.StatusConflict || len(a.Conflicts) != 1 || a.Conflicts[0].Object != "PolicySet/guard" || a.Conflicts[0].Field != "text" {
		t.Fatalf("rebase = %d %s, want the set's text in conflict alone", code, raw)
	}
	if code, _, raw := f.rebase(t, id, f.root, map[string]any{"revision": 1, "picks": map[string]string{"PolicySet/guard text": "live"}}); code != http.StatusOK {
		t.Fatalf("rebase with a pick = %d %s", code, raw)
	}
	_, items := f.stored(t, id)
	for _, it := range items {
		switch it.Kind {
		case string(drafts.KindRole):
			doc, err := drafts.ParseRole(it.Doc)
			if err != nil || doc.Spec.Description != "New." || strings.Join(doc.Spec.Implies, ",") != "dev" {
				t.Errorf("role = %+v, %v; want live's description and the draft's implication", doc.Spec, err)
			}
		case string(drafts.KindPolicySet):
			if !strings.Contains(it.Doc, "never a shell") {
				t.Errorf("set text = %s, want live's", it.Doc)
			}
		}
	}
}

// TestRebaseMechanicalMakesNoAuthor pins the mechanical-revision rule under
// admin.secondPerson: a
// Check again with no pick leaves its caller free to publish another
// person's risky draft, and one with a pick makes the caller an author.
func TestRebaseMechanicalMakesNoAuthor(t *testing.T) {
	t.Parallel()
	f, lars, _ := secondPersonFixture(t)
	removal := f.create(t, f.root, removalDoc("App", "srv-a")).Draft.ID
	putServer(t, f.app, draftApp("srv-a", "https://srv-a.example/mcp", "Changed live."))
	if code, _, raw := f.rebase(t, removal, lars, map[string]any{"revision": 1}); code != http.StatusOK {
		t.Fatalf("mechanical rebase = %d %s", code, raw)
	}
	if code, a := f.publishAll(t, lars, removal); code != http.StatusOK {
		t.Errorf("publish after a mechanical Check again = %d %q, want 200", code, a.Error)
	}
	picked := f.create(t, f.root, removalDoc("App", "srv-b"), draftApp("srv-c", "https://srv-c.example/mcp", "The draft's words.")).Draft.ID
	putServer(t, f.app, draftApp("srv-c", "https://srv-c.example/mcp", "Live's words."))
	body := map[string]any{"revision": 1, "picks": map[string]string{"App/srv-c metadata.description": "draft"}}
	if code, _, raw := f.rebase(t, picked, lars, body); code != http.StatusOK {
		t.Fatalf("rebase with a pick = %d %s", code, raw)
	}
	if code, a := f.publishAll(t, lars, picked); code != http.StatusConflict || a.Error != secondPersonAuthorRefusal {
		t.Errorf("publish after a picked Check again = %d %q, want 409 %q", code, a.Error, secondPersonAuthorRefusal)
	}
}

// TestRebaseRefusals pins what Check again refuses with nothing written: a
// body that is not a check again, a pick that is not one, no such draft, a
// token revising another's draft, a revision that moved, a draft that
// is not open, an item outside the caller's read standing, a draft nothing
// of which moved, a draft every item of which leaves, and a merge that
// would take a secret from live.
func TestRebaseRefusals(t *testing.T) {
	t.Parallel()
	f := newDraftsFixture(t, nil)
	stale := f.create(t, f.root, draftApp("github", githubURL, "Another description.")).Draft.ID
	putServer(t, f.app, limitedApp("github", githubURL, "The GitHub server.", "5"))
	fresh := f.create(t, f.root, draftApp("jira", "https://jira.example/mcp", "New words.")).Draft.ID
	closed := f.create(t, f.root, draftRole("closers", "Closes.")).Draft.ID
	if code, a := f.call(t, http.MethodPost, "/v1/admin/drafts/"+closed+"/discard", f.root, map[string]any{}); code != http.StatusOK {
		t.Fatalf("discard = %d %q", code, a.Error)
	}
	cases := []struct {
		name, id, bearer string
		body             any
		code             int
		want             string
	}{
		{"a body that is not JSON", stale, f.root, "{", http.StatusBadRequest,
			"The request body is not a check again: unexpected EOF. Send revision and picks as JSON."},
		{"a pick of neither side", stale, f.root, map[string]any{"revision": 1, "picks": map[string]string{"App/github metadata.description": "both"}},
			http.StatusBadRequest, rebasePickRefusal},
		{"a pick with no field", stale, f.root, map[string]any{"revision": 1, "picks": map[string]string{"App/github": "draft"}},
			http.StatusBadRequest, rebasePickRefusal},
		{"no such draft", "999", f.root, map[string]any{"revision": 1}, http.StatusNotFound,
			"There is no draft 999. List the drafts with strazactl drafts list, or open Drafts on the console."},
		{"a token on another's draft", stale, f.token, map[string]any{"revision": 1}, http.StatusForbidden,
			"An admin API token changes only the drafts it wrote, and draft " + stale + " was written by someone else. A person can revise it on the console or with strazactl."},
		{"a revision that moved", stale, f.root, map[string]any{"revision": 3}, http.StatusConflict,
			"Draft " + stale + " changed after you read it: it is at revision 1, and you sent revision 3. Read it again and make your change on top of revision 1."},
		{"a discarded draft", closed, f.root, map[string]any{"revision": 1}, http.StatusConflict,
			"Draft " + closed + " is discarded, so it cannot change. Create a new draft from its documents."},
		{"an item outside the read standing", stale, f.ada, map[string]any{"revision": 1}, http.StatusForbidden,
			"Drafting the server github needs the scope apps:read or the role " + f.adminRole(t, f.github) + " of the server github, " +
				"because a draft shows the live config of the servers it names and what each gives a role. Ask an administrator for that grant, or leave the server github out of the draft."},
		{"nothing moved", fresh, f.root, map[string]any{"revision": 1}, http.StatusConflict,
			"Nothing that draft " + fresh + " holds changed on live state since it was checked, so there is nothing to check again."},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			code, a, raw := f.rebase(t, tc.id, tc.bearer, tc.body)
			if code != tc.code || a.Error != tc.want {
				t.Errorf("%d %s\nwant %d %q", code, raw, tc.code, tc.want)
			}
		})
	}
	gone := f.create(t, f.root, removalDoc("App", "jira")).Draft.ID
	changed := f.create(t, f.root, draftApp("github", githubURL, "A change of a server live removes.")).Draft.ID
	for _, app := range []store.App{f.jira, f.github} {
		if err := f.app.store.Apps().SoftDelete(context.Background(), app.ID); err != nil {
			t.Fatal(err)
		}
	}
	for _, tc := range []struct {
		name, id string
		body     map[string]any
	}{
		{"every item leaves", gone, map[string]any{"revision": 1}},
		{"a pick of live takes the only item out", changed, map[string]any{"revision": 1, "picks": map[string]string{"App/github state": "live"}}},
	} {
		code, a, raw := f.rebase(t, tc.id, f.root, tc.body)
		want := "Every change of draft " + tc.id + " was already made on live state, so there is nothing left to check again. " +
			"Discard the draft with strazactl drafts discard " + tc.id + "."
		if code != http.StatusConflict || a.Error != want {
			t.Errorf("%s = %d %s\nwant 409 %q", tc.name, code, raw, want)
		}
		if row, _ := f.stored(t, tc.id); row.Revision != 1 || len(draftRecords(t, f.app, "draft.update", tc.id)) != 0 {
			t.Errorf("%s wrote revision %d", tc.name, row.Revision)
		}
	}
	putServer(t, f.app, runnerApp(""))
	secret := f.create(t, f.root, runnerApp("      args: [--x]\n")).Draft.ID
	putServer(t, f.app, runnerApp("      env:\n        - {name: GITHUB_TOKEN, value: "+fakeToken+"}\n"))
	code, a, raw := f.rebase(t, secret, f.root, map[string]any{"revision": 1})
	want := "runner holds a secret at straza.runtime.command.env on live state, and a draft never keeps a secret, so Check again cannot take live's value there. " +
		"Keep the draft's value, or write the server's manifest from your own file and store the secret with strazactl apps secret set runner."
	if code != http.StatusConflict || a.Error != want || strings.Contains(raw, fakeToken) {
		t.Errorf("a live secret = %d %s\nwant 409 %q", code, raw, want)
	}
	if row, _ := f.stored(t, secret); row.Revision != 1 {
		t.Errorf("a refused rebase wrote revision %d", row.Revision)
	}
}

// TestRebaseKeepsWhatAStoredSecretMasks pins Check again over a server
// stored with a secret: the base masks it, live is compared masked the
// same way, so the draft's own value stands where live still holds the
// secret, the change live made elsewhere is taken, and no secret reaches
// the draft.
func TestRebaseKeepsWhatAStoredSecretMasks(t *testing.T) {
	t.Parallel()
	f := newDraftsFixture(t, nil)
	secretEnv := "      env:\n        - {name: GITHUB_TOKEN, value: " + fakeToken + "}\n"
	putServer(t, f.app, runnerApp(secretEnv))
	id := f.create(t, f.root, runnerApp("      env:\n        - {name: LOG_LEVEL, value: debug}\n")).Draft.ID
	putServer(t, f.app, runnerApp(secretEnv+"      args: [--verbose]\n"))
	code, _, raw := f.rebase(t, id, f.root, map[string]any{"revision": 1})
	if code != http.StatusOK || strings.Contains(raw, fakeToken) {
		t.Fatalf("rebase = %d %s", code, raw)
	}
	mf := f.itemManifest(t, id, "runner")
	cmd := mf.Straza.Runtime.Command
	if len(cmd.Env) != 1 || cmd.Env[0].Name != "LOG_LEVEL" || strings.Join(cmd.Args, " ") != "--verbose" {
		t.Errorf("merged command = %+v, want the draft's env and live's args", cmd)
	}
	_, items := f.stored(t, id)
	if strings.Contains(items[0].Doc, fakeToken) || strings.Contains(items[0].BaseDoc, fakeToken) {
		t.Errorf("the draft keeps the secret: %s / %s", items[0].Doc, items[0].BaseDoc)
	}
}

// TestStaleFixNamesCheckAgainOnEveryDoor pins draft.stale's fix after Check
// again landed: the verdict of a draft of the strazactl door and of the api
// door, the verdict an agent reads, stored when the checker checks an
// agent's revision of the straza app's door, and the publish route's
// refusal all send the reader to Check again.
func TestStaleFixNamesCheckAgainOnEveryDoor(t *testing.T) {
	t.Parallel()
	f := newDraftsFixture(t, nil)
	ctx := context.Background()
	rob := seedAgent(t, f.app, "rob", "kim", DraftConfigRole)
	fix := func(id string) string {
		return "Check the draft again with Check again on the console or strazactl drafts rebase " + id +
			". Straza keeps what this draft changed, takes every other field from live state, and asks you to pick where both changed."
	}
	byAPI := f.create(t, f.root, draftApp("github", githubURL, "Words of the api door.")).Draft.ID
	byCLI := f.create(t, f.strazactl, draftApp("github", githubURL, "Words of strazactl.")).Draft.ID
	byBot := f.agentDraft(t, rob, draftApp("jira", "https://jira.example/mcp", "Words of the agent."))
	f.read(t, f.root, byBot)
	putServer(t, f.app, limitedApp("github", githubURL, "The GitHub server.", "7"))
	putServer(t, f.app, limitedApp("jira", "https://jira.example/mcp", "The Jira server.", "7"))
	for _, id := range []string{byAPI, byCLI} {
		if _, v := f.read(t, f.root, id); len(v.Refused) != 1 || v.Refused[0].Code != "draft.stale" || v.Refused[0].Fix != fix(id) {
			t.Errorf("draft %s refused = %+v, want draft.stale with the Check again fix", id, v.Refused)
		}
	}
	if code, a := f.publishAll(t, f.root, byAPI); code != http.StatusConflict || !strings.HasSuffix(a.Error, " "+fix(byAPI)) {
		t.Errorf("publish = %d %q, want 409 ending in the fix", code, a.Error)
	}
	row, items := f.stored(t, byBot)
	items[0].Doc = draftApp("jira", "https://jira.example/mcp", "More words of the agent.")
	if _, err := f.app.store.Drafts().Revise(ctx, row.ID, store.DraftRevise{From: 1, Items: items,
		Rev: store.DraftRevisionRow{Author: (&doorRig{f}).agentActor(rob), Door: string(drafts.DoorAgent), Digest: "d"}}); err != nil {
		t.Fatal(err)
	}
	if err := f.app.checkDrafts(ctx); err != nil {
		t.Fatal(err)
	}
	row, _ = f.stored(t, byBot)
	var agent drafts.AgentVerdict
	if err := json.Unmarshal([]byte(row.AgentVerdict), &agent); err != nil {
		t.Fatalf("agent verdict %q: %v", row.AgentVerdict, err)
	}
	if len(agent.Refused) != 1 || agent.Refused[0].Code != "draft.stale" || agent.Refused[0].Fix != fix(byBot) {
		t.Errorf("the agent's refused = %+v, want draft.stale with the Check again fix", agent.Refused)
	}
}

// TestRebaseWaivesWhatLiveStateHolds pins the waiver on Check again: a
// draft that changes the description of a live server whose stored
// manifest holds a plain word after a flag naming a secret takes the
// field live changed, keeps the word it shares with live under the
// waiver's warning, and once live holds that word one byte off the merged
// draft is refused with no revision written and no value in the answer.
func TestRebaseWaivesWhatLiveStateHolds(t *testing.T) {
	t.Parallel()
	f := newDraftsFixture(t, nil)
	putServer(t, f.app, waivedRunner("runner", "The runner."))
	id := f.create(t, f.root, waivedRunner("runner", "Changed.")).Draft.ID
	putServer(t, f.app, waivedRunner("runner", "The runner.")+"  limits:\n    rps: 5\n")
	code, a, raw := f.rebase(t, id, f.root, map[string]any{"revision": 1})
	if code != http.StatusOK || len(a.Verdict.Refused) != 0 {
		t.Fatalf("rebase = %d %s, want 200 with no refusal", code, raw)
	}
	waived := false
	for _, w := range a.Verdict.Warnings {
		waived = waived || w.Code == "secret.value" && strings.Contains(w.Sentence, "already holds the same value there")
	}
	if !waived {
		t.Errorf("warnings = %+v, want the waiver's warning of the stored value", a.Verdict.Warnings)
	}
	mf := f.itemManifest(t, id, "runner")
	if mf.Metadata.Description != "Changed." || mf.Straza.Limits == nil || mf.Straza.Limits.RPS != 5 ||
		mf.Straza.Runtime.Command == nil || !slices.Contains(mf.Straza.Runtime.Command.Args, "oauth") {
		t.Errorf("merged manifest: description %q, limits %+v, command %+v; want the draft's description, live's limit and the shared word", mf.Metadata.Description, mf.Straza.Limits, mf.Straza.Runtime.Command)
	}
	putServer(t, f.app, strings.Replace(waivedRunner("runner", "The runner."), "oauth]", "oauth2]", 1)+"  limits:\n    rps: 7\n")
	row, _ := f.stored(t, id)
	code, a, raw = f.rebase(t, id, f.root, map[string]any{"revision": row.Revision})
	if code != http.StatusUnprocessableEntity || len(a.Findings) == 0 || a.Findings[0].Code != "secret.value" || strings.Contains(raw, "oauth2") {
		t.Errorf("rebase over the value one byte off = %d %s, want 422 with secret.value first and no value in the answer", code, raw)
	}
	if after, _ := f.stored(t, id); after.Revision != row.Revision {
		t.Errorf("the refused rebase wrote revision %d", after.Revision)
	}
}
