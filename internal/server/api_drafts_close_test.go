package server

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/strazahq/straza/internal/redact"
	"github.com/strazahq/straza/internal/store"
)

// TestDiscardDraft pins discard: an author discards with a
// reason, the reason rule refuses in plain words, a token discards only its
// own drafts, and a draft already decided or moved on is refused with its
// pinned sentences.
func TestDiscardDraft(t *testing.T) {
	t.Parallel()
	f := newDraftsFixture(t, nil)
	kims := f.create(t, f.root, draftRole("helpers", "Helpers.")).Draft.ID
	erins := f.create(t, f.erin, draftApp("github", "https://api.github.example/mcp?region=eu", "Ours.")).Draft.ID
	discard := func(bearer, id string, body any) (int, wireAnswer) {
		return f.call(t, http.MethodPost, "/v1/admin/drafts/"+id+"/discard", bearer, body)
	}
	for _, tc := range []struct {
		name, reason, says string
	}{
		{"a reason over 500 bytes", strings.Repeat("r", 501), "The reason holds 501 bytes, and a reason holds at most 500. Shorten it and discard again."},
		{"a direction override", "done \u202e", "The reason holds a control or invisible character, U+202E, that can make the text read differently from what is stored. Remove it and discard again."},
		{"a control character", "done \x01", "The reason holds a control or invisible character, U+0001, that can make the text read differently from what is stored. Remove it and discard again."},
	} {
		if code, a := discard(f.erin, erins, map[string]any{"reason": tc.reason}); code != http.StatusBadRequest || a.Error != tc.says {
			t.Errorf("%s = %d %q, want 400 %q", tc.name, code, a.Error, tc.says)
		}
	}
	code, out, _ := adminBytes(t, http.MethodPost, f.base+"/v1/admin/drafts/"+erins+"/discard", f.erin, "application/json", []byte("{"))
	var bad struct {
		Error string `json:"error"`
	}
	_ = json.Unmarshal(out, &bad)
	if code != http.StatusBadRequest || bad.Error != "The request body is not a discard: unexpected EOF. Send revision and reason as JSON, or no body." {
		t.Errorf("a body that is not JSON = %d %q", code, bad.Error)
	}
	k28 := func(id string) string {
		return "Discarding draft " + id + " needs being one of its authors, the root role, or standing over every object in it. Ask one of its authors or an administrator."
	}
	if code, a := discard(f.token, kims, nil); code != http.StatusForbidden || a.Error != k28(kims) {
		t.Errorf("a token discarding kim's draft = %d %q", code, a.Error)
	}
	if code, a := discard(f.token, kims, map[string]any{"revision": 99}); code != http.StatusForbidden || a.Error != k28(kims) {
		t.Errorf("a token discarding kim's draft at a revision it never read = %d %q, want the authorship 403 before any 409", code, a.Error)
	}
	runtime := f.create(t, f.root, draftApp("github", "https://api.github.example/mcp", "A new address.")).Draft.ID
	if code, a := discard(f.nell, runtime, nil); code != http.StatusForbidden || a.Error != k28(runtime) {
		t.Errorf("a person without the standing of a runtime change discarding it = %d %q", code, a.Error)
	}
	mine := f.create(t, f.erin, draftApp("github", "https://api.github.example/mcp?region=eu", "Mine.")).Draft.ID
	if code, a := f.call(t, http.MethodPut, "/v1/admin/drafts/"+mine, f.root, map[string]any{"revision": 1,
		"documents": []string{draftApp("github", "https://api.github.example/mcp?region=eu", "Mine."), draftApp("jira", "https://jira.example/mcp", "Kim's.")}}); code != http.StatusOK {
		t.Fatalf("root's revision = %d %q", code, a.Error)
	}
	code, answer := discard(f.erin, mine, nil)
	if code != http.StatusOK {
		t.Fatalf("erin's discard of the draft root extended = %d %q", code, answer.Error)
	}
	for _, it := range answer.Draft.Items {
		if it.Name == "jira" && (it.Doc != "" || it.Withheld == "") || it.Name == "github" && it.Doc == "" {
			t.Errorf("erin's discard answer reads %s/%s as %+v, want jira withheld and github whole", it.Kind, it.Name, it)
		}
	}
	code, a := discard(f.erin, erins, map[string]any{"revision": 1, "reason": "Superseded."})
	if code != http.StatusOK || a.Draft.State != "discarded" || a.Draft.DecidedReason != "Superseded." || a.Draft.DecidedBy == nil || a.Draft.DecidedBy.Username != "erin" {
		t.Fatalf("erin's discard = %d %+v", code, a.Draft)
	}
	if code, a := discard(f.erin, erins, nil); code != http.StatusConflict || a.Error != "Draft "+erins+" is discarded already, so there is nothing to discard." {
		t.Errorf("a second discard = %d %q", code, a.Error)
	}
	if code, a := f.call(t, http.MethodPut, "/v1/admin/drafts/"+kims, f.root, map[string]any{"revision": 1, "documents": []string{draftRole("helpers", "More.")}}); code != http.StatusOK {
		t.Fatalf("update = %d %q", code, a.Error)
	}
	if code, a := discard(f.root, kims, map[string]any{"revision": 1}); code != http.StatusConflict ||
		a.Error != "Draft "+kims+" changed after you read it: it is at revision 2, and you sent revision 1. Read it again and make your change on top of revision 2." {
		t.Errorf("a discard of a moved draft = %d %q", code, a.Error)
	}
	if code, a := discard(f.root, kims, nil); code != http.StatusOK || a.Draft.State != "discarded" {
		t.Errorf("a discard with no body = %d %+v", code, a.Draft)
	}
}

// TestDiscardRefusesASecretInTheReason pins that a discard reason meets the
// note's secret scan: a reason that holds a token or an address with a
// password answers 400, and the draft stays open with no draft.discard
// record, because the draft row and that record would keep the reason.
func TestDiscardRefusesASecretInTheReason(t *testing.T) {
	t.Parallel()
	f := newDraftsFixture(t, nil)
	id := f.create(t, f.root, draftRole("helpers", "Helpers.")).Draft.ID
	says := "The reason holds what looks like a secret, and the draft and its audit record keep the reason for good. Remove it and discard again."
	for _, tc := range []struct{ name, reason string }{
		{"a token", "Wrong token pasted: " + fakeToken},
		{"an address with a password", "Superseded by https://svc:Hunter2Hunter2@legacy.example/mcp"},
	} {
		code, a := f.call(t, http.MethodPost, "/v1/admin/drafts/"+id+"/discard", f.root, map[string]any{"reason": tc.reason})
		if code != http.StatusBadRequest || a.Error != says {
			t.Errorf("%s = %d %q, want 400 %q", tc.name, code, a.Error, says)
		}
	}
	if row, _ := f.stored(t, id); row.State != "open" || row.DecidedReason != "" {
		t.Errorf("the draft is %s with the reason %q, want it open with none", row.State, row.DecidedReason)
	}
	if records := draftRecords(t, f.app, "draft.discard", id); len(records) != 0 {
		t.Errorf("draft.discard records = %v, want none", records)
	}
}

// TestRevertDraft pins revert: the undo draft reads the change record
// backwards with every base taken from the row, names what it undoes, and
// says what cannot come back, and the refusals answer their pinned
// sentences, intake and read standing included.
func TestRevertDraft(t *testing.T) {
	t.Parallel()
	f := newDraftsFixture(t, nil)
	revert := func(bearer, id string, body any) (int, wireAnswer) {
		return f.call(t, http.MethodPost, "/v1/admin/drafts/"+id+"/revert", bearer, body)
	}
	removal := f.create(t, f.root, removalDoc("App", "jira")).Draft.ID
	publishStored(t, f, removal)
	code, a := revert(f.root, removal, map[string]any{"note": "Bring jira back."})
	if code != http.StatusCreated {
		t.Fatalf("revert = %d %q %v", code, a.Error, a.Findings)
	}
	d := a.Draft
	if d.Reverts != removal || d.Title != "Undo draft "+removal || d.Note != "Bring jira back." || len(d.Items) != 1 || d.Items[0].Op != "put" {
		t.Errorf("undo draft = %+v, want draft %s undone by a put of jira", d, removal)
	}
	_, items := f.stored(t, d.ID)
	if items[0].Base != "" || items[0].BaseOp != "remove" || !strings.Contains(items[0].Doc, `"name":"jira"`) {
		t.Errorf("stored undo item = %+v, want the stored manifest with the row's after as its base", items[0])
	}
	lossy := false
	for _, w := range a.Verdict.Warnings {
		lossy = lossy || w.Code == "revert.lossy" && w.Object == "App/jira"
	}
	if !lossy {
		t.Errorf("warnings = %+v, want revert.lossy for jira", a.Verdict.Warnings)
	}
	if creates := draftRecords(t, f.app, "draft.create", d.ID); len(creates) != 1 || creates[0]["reverts"] != removal {
		t.Errorf("draft.create records = %v, want one naming the undone draft", creates)
	}
	// A revision of the undo names the undone draft too, as spec/events
	// revision 37 says of draft.update.
	if code, a := f.call(t, http.MethodPut, "/v1/admin/drafts/"+d.ID, f.root, map[string]any{"revision": 1, "note": "Bring jira back, revised.",
		"documents": []string{draftApp("jira", "https://jira.example/mcp", "Brought back.")}}); code != http.StatusOK {
		t.Fatalf("revising the undo = %d %q, want 200", code, a.Error)
	}
	if updates := draftRecords(t, f.app, "draft.update", d.ID); len(updates) != 1 || updates[0]["reverts"] != removal {
		t.Errorf("draft.update records = %v, want one naming the undone draft", updates)
	}
	created := f.create(t, f.root, draftApp("linear", "https://linear.example/mcp", "The Linear server.")).Draft.ID
	publishStored(t, f, created)
	putServer(t, f.app, draftApp("linear", "https://linear.example/mcp", "Changed after the publish."))
	if code, a := revert(f.root, created, nil); code != http.StatusCreated || len(a.Verdict.Refused) == 0 || a.Verdict.Refused[0].Code != "draft.stale" {
		t.Errorf("undoing a draft whose object changed since = %d %+v, want it stale at once", code, a.Verdict.Refused)
	}
	open := f.create(t, f.root, draftRole("helpers", "Helpers.")).Draft.ID
	same := f.create(t, f.root, draftApp("github", "https://api.github.example/mcp?region=eu", "The GitHub server.")).Draft.ID
	publishStored(t, f, same)
	putServer(t, f.app, draftApp("legacy", "https://svc:Hunter2Hunter2@legacy.example/mcp", "Stored before the scan."))
	legacy := f.create(t, f.root, removalDoc("App", "legacy")).Draft.ID
	publishStored(t, f, legacy)
	for _, tc := range []struct {
		name, bearer, id string
		code             int
		says             string
	}{
		{"an open draft", f.root, open, http.StatusConflict, "Draft " + open + " is open, and only a published draft can be undone."},
		{"a draft that changed nothing", f.root, same, http.StatusConflict, "Draft " + same + " changed nothing, so there is nothing to undo."},
		{"a caller who may not read the server", f.ada, removal, http.StatusForbidden,
			"Drafting the server jira needs the scope apps:read, because a draft shows the live config of the servers it names and what each gives a role. " +
				"Ask an administrator for that grant, or leave the server jira out of the draft."},
		{"a caller who may not read a server whose stored manifest intake would refuse", f.ada, legacy, http.StatusForbidden,
			"Drafting the server legacy needs the scope apps:read, because a draft shows the live config of the servers it names and what each gives a role. " +
				"Ask an administrator for that grant, or leave the server legacy out of the draft."},
	} {
		if code, a := revert(tc.bearer, tc.id, nil); code != tc.code || a.Error != tc.says {
			t.Errorf("%s = %d %q, want %d %q", tc.name, code, a.Error, tc.code, tc.says)
		}
	}
	// The stamp masked the password as the user name %5BREDACTED%5D, which
	// keeps the address readable, and the guard reads that form too.
	masked := "Draft " + legacy + " removed or changed legacy, whose stored manifest held a secret that a draft never keeps, so this undo cannot carry it. " +
		"Write the server's manifest from your own file, and after publishing store the secret with strazactl apps secret set legacy and set credential.inject."
	if code, a := revert(f.root, legacy, nil); code != http.StatusUnprocessableEntity || a.Error != masked {
		t.Errorf("undoing the removal of a server stored with a password = %d %q, want 422 %q", code, a.Error, masked)
	}
	code, out, _ := adminBytes(t, http.MethodPost, f.base+"/v1/admin/drafts/"+removal+"/revert", f.root, "application/json", []byte("{"))
	var bad struct {
		Error string `json:"error"`
	}
	_ = json.Unmarshal(out, &bad)
	if code != http.StatusBadRequest || bad.Error != "The request body is not an undo: unexpected EOF. Send a note as JSON, or no body." {
		t.Errorf("a body that is not JSON = %d %q", code, bad.Error)
	}
}

// TestRevertRefusesAMaskedManifest pins the undo of a change that took a
// secret out of a server's stored manifest: the stamp kept that
// manifest masked, the live manifest holds no value where the mask stands,
// so the undo would carry the mask in place of the value, and it answers
// 422 in the undo's own words with nothing stored, wherever the secret
// stood, in a query parameter of the address included.
func TestRevertRefusesAMaskedManifest(t *testing.T) {
	t.Parallel()
	f := newDraftsFixture(t, nil)
	for _, tc := range []struct{ name, server, stored, change string }{
		{"a secret in an env value", "runner", runnerApp("      env:\n        - {name: GITHUB_TOKEN, value: " + fakeToken + "}\n"), runnerApp("")},
		{"a secret in an argument", "runner", runnerApp("      args: [--token, " + fakeToken + "]\n"), runnerApp("")},
		{"a secret in a query parameter of the address", "qonly", draftApp("qonly", "https://qonly.example/mcp?token=abc123abc123", "Before."), draftApp("qonly", "https://qonly.example/mcp", "After.")},
	} {
		putServer(t, f.app, tc.stored)
		id := f.create(t, f.root, tc.change).Draft.ID
		publishStored(t, f, id)
		before := f.drafts(t)
		says := "Draft " + id + " removed or changed " + tc.server + ", whose stored manifest held a secret that a draft never keeps, so this undo cannot carry it. " +
			"Write the server's manifest from your own file, and after publishing store the secret with strazactl apps secret set " + tc.server + " and set credential.inject."
		if code, a := f.call(t, http.MethodPost, "/v1/admin/drafts/"+id+"/revert", f.root, nil); code != http.StatusUnprocessableEntity || a.Error != says {
			t.Errorf("%s: the undo = %d %q, want 422 %q", tc.name, code, a.Error, says)
		}
		if after := f.drafts(t); after != before {
			t.Errorf("%s: the store holds %d drafts, want %d", tc.name, after, before)
		}
	}
}

// TestDiscardAnswers503WithTheDraftStillOpen pins the 503 of a discard
// whose read of live state failed: it says the draft is still open, and it
// is.
func TestDiscardAnswers503WithTheDraftStillOpen(t *testing.T) {
	t.Parallel()
	armed, _, preRun := failingGeneration()
	f := newDraftsFixture(t, preRun)
	id := f.create(t, f.root, draftRole("helpers", "Helpers.")).Draft.ID
	armed.Store(true)
	code, a := f.call(t, http.MethodPost, "/v1/admin/drafts/"+id+"/discard", f.nell, nil)
	want := "Straza could not read live state to decide the discard of draft " + id + ": the config generation cannot be read: injected generation failure. " +
		"The draft is still open. Try again, and read the strazad log if it keeps failing."
	if code != http.StatusServiceUnavailable || a.Error != want {
		t.Errorf("discard = %d %q, want 503 %q", code, a.Error, want)
	}
	if row, _ := f.stored(t, id); row.State != "open" {
		t.Errorf("the draft is %s, want it open", row.State)
	}
}

// movesBeforeClose is a store whose discard finds the draft moved: while
// armed, Close first writes a revision of the draft, as an update landing
// between a discard's read and its close would.
type movesBeforeClose struct {
	store.Store
	armed *atomic.Bool
}

func (s movesBeforeClose) Drafts() store.DraftRepo {
	return movesBeforeCloseRepo{s.Store.Drafts(), s.armed}
}

type movesBeforeCloseRepo struct {
	store.DraftRepo
	armed *atomic.Bool
}

func (r movesBeforeCloseRepo) Close(ctx context.Context, id int64, revision int, state string, by store.DraftActor, reason string, at time.Time) (bool, error) {
	if r.armed.Swap(false) {
		row, items, err := r.Get(ctx, id)
		if err != nil {
			return false, err
		}
		if _, err := r.Revise(ctx, id, store.DraftRevise{From: row.Revision, Items: items, Rev: store.DraftRevisionRow{Author: by, Door: "api"}}); err != nil {
			return false, err
		}
	}
	return r.DraftRepo.Close(ctx, id, revision, state, by, reason, at)
}

// TestDiscardOfADraftThatMovesMeanwhile pins the 409 of a discard whose
// draft moved while it ran: a discard sent
// with no revision says the draft moved to its new revision, one sent with
// a revision says which it sent, and the draft stays open.
func TestDiscardOfADraftThatMovesMeanwhile(t *testing.T) {
	t.Parallel()
	armed := &atomic.Bool{}
	f := newDraftsFixture(t, []func(*App){func(a *App) { a.store = movesBeforeClose{a.store, armed} }})
	id := f.create(t, f.root, draftRole("helpers", "Helpers.")).Draft.ID
	for _, tc := range []struct {
		name string
		body any
		says string
	}{
		{"no revision sent", nil, "Draft " + id + " moved to revision 2 while the discard ran, so it is still open. " +
			"Read it again, and discard it again if you still mean to."},
		{"revision 2 sent", map[string]any{"revision": 2}, "Draft " + id + " changed after you read it: it is at revision 3, and you sent revision 2. " +
			"Read it again and make your change on top of revision 3."},
	} {
		armed.Store(true)
		if code, a := f.call(t, http.MethodPost, "/v1/admin/drafts/"+id+"/discard", f.root, tc.body); code != http.StatusConflict || a.Error != tc.says {
			t.Errorf("%s = %d %q, want 409 %q", tc.name, code, a.Error, tc.says)
		}
	}
	if row, _ := f.stored(t, id); row.State != "open" || row.Revision != 3 {
		t.Errorf("the draft is %s at revision %d, want open at revision 3", row.State, row.Revision)
	}
}

// TestRevertWaivesALiveName pins the waiver on the undo: the undo of a
// change to a live role whose name holds an invisible character carries
// the stored name with the waiver's warning, and the undo of that role's
// removal, which would bring the name back, is still refused with nothing
// stored. The undo of a change to a server whose stored manifest holds a
// plain word after a flag naming a secret carries that word masked, as the
// stamp kept it, and the mask is waived for the value the live manifest
// still holds there, so the undo publishes with the stored word and never
// the mark.
func TestRevertWaivesALiveName(t *testing.T) {
	t.Parallel()
	f := newDraftsFixture(t, nil)
	revert := func(id string) (int, wireAnswer) {
		return f.call(t, http.MethodPost, "/v1/admin/drafts/"+id+"/revert", f.root, nil)
	}
	name := "dev\u200bops"
	if _, err := f.app.store.Roles().Create(context.Background(), store.Role{Name: name, Kind: store.RoleKindBusiness, Description: "Old words."}); err != nil {
		t.Fatal(err)
	}
	changed := f.create(t, f.root, draftRole(name, "New words.")).Draft.ID
	publishStored(t, f, changed)
	code, a := revert(changed)
	if code != http.StatusCreated || len(a.Verdict.Refused) != 0 {
		t.Fatalf("the undo of a change to the role = %d %q, refused %+v, want 201 with no refusal", code, a.Error, a.Verdict.Refused)
	}
	if len(a.Draft.Items) != 1 || a.Draft.Items[0].Name != name || a.Draft.Items[0].Op != "put" {
		t.Errorf("the undo's items = %+v, want the put of the role under its stored name", a.Draft.Items)
	}
	waived := false
	for _, w := range a.Verdict.Warnings {
		waived = waived || w.Code == "bundle.name-characters" && strings.Contains(w.Sentence, "already holds")
	}
	if !waived {
		t.Errorf("warnings = %+v, want the waiver's warning of the stored name", a.Verdict.Warnings)
	}
	removed := f.create(t, f.root, removalDoc("Role", name)).Draft.ID
	publishStored(t, f, removed)
	before := f.drafts(t)
	if code, a := revert(removed); code != http.StatusUnprocessableEntity || len(a.Findings) == 0 || a.Findings[0].Code != "bundle.name-characters" || f.drafts(t) != before {
		t.Errorf("the undo of the role's removal = %d %q %+v, want 422 with the name refused and nothing stored", code, a.Error, a.Findings)
	}
	putServer(t, f.app, waivedRunner("runner", "The runner."))
	server := f.create(t, f.root, waivedRunner("runner", "Changed.")).Draft.ID
	publishStored(t, f, server)
	code, a = revert(server)
	if code != http.StatusCreated || len(a.Verdict.Refused) != 0 {
		t.Fatalf("the undo of a change to the server = %d %q, refused %+v, want 201 with no refusal", code, a.Error, a.Verdict.Refused)
	}
	kept := false
	for _, w := range a.Verdict.Warnings {
		kept = kept || w.Code == "bundle.masked" && strings.Contains(w.Sentence, "already holds a value there, so the publish keeps the stored value")
	}
	if !kept {
		t.Errorf("warnings = %+v, want the waiver's warning that the publish keeps the stored value", a.Verdict.Warnings)
	}
	if _, items := f.stored(t, a.Draft.ID); !strings.Contains(items[0].Doc, redact.Mark) || strings.Contains(items[0].Doc, "oauth") {
		t.Errorf("the stored undo item reads %s, want the word masked", items[0].Doc)
	}
	if code, p := f.publishAll(t, f.root, a.Draft.ID); code != http.StatusOK {
		t.Fatalf("publishing the undo = %d %q %+v", code, p.Error, p.Verdict.Refused)
	}
	live, err := f.app.store.Apps().GetByName(context.Background(), "runner")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(live.Manifest, `"--auth","oauth"`) || strings.Contains(live.Manifest, redact.Mark) || !strings.Contains(live.Manifest, "The runner.") {
		t.Errorf("the published undo stored the manifest %s, want the stored word kept and the description undone", live.Manifest)
	}
}
