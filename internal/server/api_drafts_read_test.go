package server

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/santhosh-tekuri/jsonschema/v6"

	"github.com/strazahq/straza/internal/drafts"
	"github.com/strazahq/straza/internal/manager"
	"github.com/strazahq/straza/internal/store"
)

// wireSummary is a row of the drafts list.
type wireSummary struct {
	ID       string           `json:"id"`
	Title    string           `json:"title"`
	State    string           `json:"state"`
	Door     string           `json:"door"`
	Revision int              `json:"revision"`
	Proposer drafts.Principal `json:"proposer"`
	Items    []wireItem       `json:"items"`
	Checks   *struct {
		Refused   int    `json:"refused"`
		Revision  int    `json:"revision"`
		CheckedAt string `json:"checked_at"`
	} `json:"checks"`
	Working    bool   `json:"working"`
	PolicyEdit string `json:"policy_edit"`
}

// wirePage is a page of the drafts list.
type wirePage struct {
	Items      []wireSummary `json:"items"`
	NextCursor string        `json:"next_cursor"`
	Error      string        `json:"error"`
}

// wireEntry is one object of the live block.
type wireEntry struct {
	Op  string `json:"op"`
	Doc string `json:"doc"`
}

// wireDetail is the answer of GET /v1/admin/drafts/{id}.
type wireDetail struct {
	Draft     wireDraft   `json:"draft"`
	Verdict   wireVerdict `json:"verdict"`
	Revisions []struct {
		Revision  int              `json:"revision"`
		Author    drafts.Principal `json:"author"`
		Door      string           `json:"door"`
		Digest    string           `json:"digest"`
		CreatedAt string           `json:"created_at"`
	} `json:"revisions"`
	Live      map[string]wireEntry `json:"live"`
	Contacted map[string]struct {
		At    string   `json:"at"`
		Tools []string `json:"tools"`
	} `json:"contacted"`
	Changes []struct {
		Kind     string `json:"kind"`
		Name     string `json:"name"`
		BeforeOp string `json:"before_op"`
		AfterOp  string `json:"after_op"`
		AfterDoc string `json:"after_doc"`
	} `json:"changes"`
	Checks         *checksPayload `json:"checks"`
	MayPublish     bool           `json:"may_publish"`
	PublishRefusal string         `json:"publish_refusal"`
	Error          string         `json:"error"`
}

// list reads one page of the drafts list as bearer with the query given.
func (f *draftsFixture) list(t *testing.T, bearer, query string) (int, wirePage) {
	t.Helper()
	code, out, _ := adminBytes(t, http.MethodGet, f.base+"/v1/admin/drafts?"+query, bearer, "", nil)
	var p wirePage
	if err := json.Unmarshal(out, &p); err != nil {
		t.Fatalf("list %s: %v in %s", query, err, out)
	}
	return code, p
}

// get reads the draft id as bearer.
func (f *draftsFixture) get(t *testing.T, bearer, id string) (int, wireDetail) {
	t.Helper()
	code, out, _ := adminBytes(t, http.MethodGet, f.base+"/v1/admin/drafts/"+id, bearer, "", nil)
	var d wireDetail
	if err := json.Unmarshal(out, &d); err != nil {
		t.Fatalf("get %s: %v in %s", id, err, out)
	}
	return code, d
}

// pageIDs lists the ids of a page's rows.
func pageIDs(p wirePage) string {
	var out []string
	for _, row := range p.Items {
		out = append(out, row.ID)
	}
	return strings.Join(out, ",")
}

// removalDoc is the Removal document of the object kind/name.
func removalDoc(kind, name string) string {
	return "apiVersion: straza.dev/v1beta1\nkind: Removal\nmetadata:\n  name: " + name + "\nspec:\n  kind: " + kind + "\n"
}

// publishStored publishes the stored draft id through the store, as the
// publish route does once its checks passed: each item's base as it was
// stamped, and after it the object as the item's document writes it. It
// takes App and Role items only.
func publishStored(t *testing.T, f *draftsFixture, id string) {
	t.Helper()
	ctx := context.Background()
	row, items := f.stored(t, id)
	var plan []store.PlanItem
	for _, it := range items {
		pi := store.PlanItem{Ref: store.ObjectRef{Kind: it.Kind, Name: it.Name}, Op: it.Op, Base: it.Base, BaseOp: it.BaseOp, BaseDoc: it.BaseDoc}
		switch {
		case it.Op == string(drafts.OpRemove):
		case it.Kind == string(drafts.KindApp):
			mf, err := manager.Parse([]byte(it.Doc))
			if err != nil {
				t.Fatal(err)
			}
			raw, err := mf.JSON()
			if err != nil {
				t.Fatal(err)
			}
			pi.After, _ = store.FingerprintApp(raw)
			pi.AfterDoc = raw
			pi.App = &store.App{Name: it.Name, Version: "1.0.0", Manifest: raw, RuntimeKind: mf.Straza.Runtime.Kind}
		case it.Kind == string(drafts.KindRole):
			doc, err := drafts.ParseRole(it.Doc)
			if err != nil {
				t.Fatal(err)
			}
			cfg := store.RoleConfig{Role: store.Role{Name: it.Name, Kind: doc.Spec.Kind, Plane: drafts.PlaneAccess, Description: doc.Spec.Description},
				Owner: doc.Spec.Server, Implies: doc.Spec.Implies}
			pi.After, pi.AfterDoc, pi.Role = store.FingerprintRole(cfg), it.Doc, &cfg
		default:
			t.Fatalf("publishStored takes App and Role items, not %s", it.Kind)
		}
		plan = append(plan, pi)
	}
	gen, err := f.app.store.Drafts().Generation(ctx)
	if err != nil {
		t.Fatal(err)
	}
	active := ""
	if act, err := f.app.store.Snapshots().GetActive(ctx); err == nil {
		active = act.ID
	} else if !errors.Is(err, store.ErrNotFound) {
		t.Fatal(err)
	}
	if _, err := f.app.store.Drafts().Publish(ctx, store.PublishPlan{DraftID: row.ID, Revision: row.Revision, Generation: gen, BaseSnapshot: active,
		Items: plan, Publisher: store.DraftActor{ID: f.kim.ID, Name: "kim", Via: laneLogin, Client: clientLogin}, Acks: "{}",
		Records: func(store.PublishOutcome) ([]store.OutboxEvent, error) {
			return []store.OutboxEvent{{Subject: "straza.audit.admin", CE: `{"data":{"action":"test.publish"}}`}}, nil
		}}); err != nil {
		t.Fatalf("publish draft %s: %v", id, err)
	}
}

// TestListDrafts pins the list: open drafts newest first by default,
// the filters, paging by next_cursor, the check counts of an open checked
// draft, who sees which row, and the refusals of a bad query.
func TestListDrafts(t *testing.T) {
	t.Parallel()
	f := newDraftsFixture(t, nil)
	ctx := context.Background()
	d1 := f.create(t, f.root, draftApp("github", "https://api.github.example/mcp?region=eu", "One.")).Draft.ID
	d2 := f.create(t, f.root, draftApp("jira", "https://jira.example/mcp", "Two.")).Draft.ID
	d3 := f.create(t, f.erin, draftApp("github", "https://api.github.example/mcp?region=eu", "Three.")).Draft.ID
	if code, _ := f.call(t, http.MethodPost, "/v1/admin/drafts/"+d2+"/discard", f.root, nil); code != http.StatusOK {
		t.Fatalf("discard = %d", code)
	}
	code, w := f.call(t, http.MethodPost, "/v1/admin/drafts", f.strazactl, map[string]any{"working": true,
		"items": []map[string]string{{"kind": "App", "name": "linear", "op": "put", "doc": draftApp("linear", "https://linear.example/mcp", "Four.")}}})
	if code != http.StatusCreated {
		t.Fatalf("working = %d %q", code, w.Error)
	}
	d4 := w.Draft.ID
	slot, err := f.app.store.Drafts().Create(ctx, store.DraftRow{Door: "api", Slot: "policy:guard"},
		[]store.DraftItemRow{{Kind: "PolicySet", Name: "guard", Op: "put", Doc: draftSet("guard")}},
		store.DraftRevisionRow{Author: store.DraftActor{ID: f.kim.ID, Name: "kim", Via: laneLogin, Client: clientLogin}})
	if err != nil {
		t.Fatal(err)
	}
	d5 := strconv.FormatInt(slot.ID, 10)
	views := []struct {
		name, bearer, query, want string
	}{
		{"open drafts, newest first", f.root, "", strings.Join([]string{d5, d4, d3, d1}, ",")},
		{"every state", f.root, "state=all", strings.Join([]string{d5, d4, d3, d2, d1}, ",")},
		{"the discarded ones", f.root, "state=discarded", d2},
		{"a server admin's own and its server's", f.erin, "state=all", d3 + "," + d1},
		{"a holder of drafts:read", f.ada, "state=all", strings.Join([]string{d5, d4, d3, d2, d1}, ",")},
		{"mine", f.erin, "mine=true", d3},
		{"one object", f.root, "state=all&object=App/jira", d2},
		{"one door", f.root, "door=strazactl", d4},
		{"the working draft", f.root, "working=true", d4},
		{"a set's saved edit", f.root, "policy_edit=guard", d5},
		{"a token's working draft, which it cannot have", f.token, "working=true", ""},
	}
	for _, tc := range views {
		code, p := f.list(t, tc.bearer, tc.query)
		if code != http.StatusOK || pageIDs(p) != tc.want {
			t.Errorf("%s: list %q = %d [%s], want [%s]", tc.name, tc.query, code, pageIDs(p), tc.want)
		}
	}
	var pages []string
	for cursor, n := "", 0; n < 5; n++ {
		_, p := f.list(t, f.root, "state=all&limit=2&cursor="+cursor)
		pages = append(pages, pageIDs(p))
		if cursor = p.NextCursor; cursor == "" {
			break
		}
	}
	if want := []string{d5 + "," + d4, d3 + "," + d2, d1}; strings.Join(pages, "|") != strings.Join(want, "|") {
		t.Errorf("pages = %v, want %v", pages, want)
	}
	_, all := f.list(t, f.root, "state=all")
	for _, row := range all.Items {
		switch row.ID {
		case d1:
			if row.Checks == nil || row.Checks.Revision != 1 || row.Checks.CheckedAt == "" || row.Proposer.Username != "kim" {
				t.Errorf("draft %s reads checks %+v proposer %+v, want the check of revision 1 by kim", d1, row.Checks, row.Proposer)
			}
		case d2, d5:
			if row.Checks != nil {
				t.Errorf("draft %s reads checks %+v, want none for a closed or unchecked draft", row.ID, row.Checks)
			}
		case d4:
			if !row.Working {
				t.Errorf("draft %s is not marked working", d4)
			}
		}
		if row.ID == d5 && row.PolicyEdit != "guard" {
			t.Errorf("draft %s names the saved edit of %q, want guard", d5, row.PolicyEdit)
		}
	}
	for _, tc := range []struct{ query, says string }{
		{"state=gone", "state must be open, published, discarded, expired or all"},
		{"limit=0", "limit must be a number from 1 to 200"},
		{"limit=201", "limit must be a number from 1 to 200"},
		{"cursor=x", "cursor is not a cursor this list gave out. Start again without it."},
		{"door=email", "door must be console, strazactl, straza-app, apps-directory or api"},
		{"object=github", "object must be Kind/Name, such as App/github"},
		{"mine=yes", "mine must be true or false"},
		{"working=1", "working must be true or false"},
		{"policy_edit=", "policy_edit must name a policy set, such as policy_edit=demo-tools-sandbox-access"},
		{"working=true&policy_edit=guard", "Ask for working or for policy_edit, not both, because each names one draft."},
	} {
		if code, p := f.list(t, f.root, tc.query); code != http.StatusBadRequest || p.Error != tc.says {
			t.Errorf("list %q = %d %q, want 400 %q", tc.query, code, p.Error, tc.says)
		}
	}
}

// TestGetDraft pins GET: the draft with its verdict computed now,
// its revisions, the live state of every item in the words of an item's
// op, and may_publish from steps 1 and 4 of a publish.
func TestGetDraft(t *testing.T) {
	t.Parallel()
	f := newDraftsFixture(t, nil)
	if code, out, _ := adminBytes(t, http.MethodPut, f.base+"/v1/admin/policies", f.root, "application/yaml", []byte(draftSet("parked"))); code != http.StatusCreated {
		t.Fatalf("store parked = %d %s", code, out)
	}
	d := f.create(t, f.root, roleText(t, "dev", "", "Developers.", ""), draftSet("guard"), draftSet("parked"),
		draftApp("jira", "https://jira.example/mcp", "Changed.")).Draft
	code, got := f.get(t, f.root, d.ID)
	if code != http.StatusOK || got.Draft.ID != d.ID || !got.MayPublish || got.PublishRefusal != "" {
		t.Fatalf("get = %d draft %q may %v %q, want 200 and root may publish", code, got.Draft.ID, got.MayPublish, got.PublishRefusal)
	}
	if len(got.Revisions) != 1 || got.Revisions[0].Author.Username != "kim" || got.Revisions[0].Door != "api" || len(got.Revisions[0].Digest) != 64 {
		t.Errorf("revisions = %+v, want kim's revision 1 through the api door with its digest", got.Revisions)
	}
	_, devDoc := liveOf(t, f.app, "dev")
	for object, want := range map[string]wireEntry{
		"Role/dev":         {Op: "put", Doc: devDoc},
		"PolicySet/guard":  {Op: "remove"},
		"PolicySet/parked": {Op: "off", Doc: draftSet("parked")},
	} {
		if got.Live[object] != want {
			t.Errorf("live %s = %+v, want %+v", object, got.Live[object], want)
		}
	}
	if e := got.Live["App/jira"]; e.Op != "put" || !strings.Contains(e.Doc, "The Jira server.") {
		t.Errorf("live App/jira = %+v, want the live manifest", e)
	}
	existed := map[string]bool{"Role/dev": true, "PolicySet/guard": false, "PolicySet/parked": true, "App/jira": true}
	for _, it := range got.Draft.Items {
		if want, ok := existed[it.Kind+"/"+it.Name]; !ok || it.Existed != want {
			t.Errorf("%s/%s reads existed %v, want %v: a set that is off exists", it.Kind, it.Name, it.Existed, want)
		}
	}
	for _, list := range [][]wireFinding{got.Verdict.Refused, got.Verdict.Risks, got.Verdict.Warnings, got.Verdict.Info} {
		for _, fnd := range list {
			if len(fnd.Key) != 64 {
				t.Errorf("%s carries the key %q", fnd.Code, fnd.Key)
			}
		}
	}
	short := f.create(t, f.root, roleText(t, "dev", "", "Developers.", ""), draftSet("guard")).Draft
	_, nell := f.get(t, f.nell, short.ID)
	if want := "You cannot publish draft " + short.ID + ": this draft also changes policy sets, which needs the scope policy:write."; nell.MayPublish || nell.PublishRefusal != want {
		t.Errorf("nell reads may_publish %v %q, want false %q", nell.MayPublish, nell.PublishRefusal, want)
	}
}

// TestGetDraftHidesWhatTheCallerMayNotRead pins who reads what of a draft:
// a draft the caller may not read answers 404 as an unknown
// one does, live leaves out every object outside the caller's read
// standing, and a caller who reaches the draft only as an author reads an
// item outside that standing as its kind, name and op with the reason.
func TestGetDraftHidesWhatTheCallerMayNotRead(t *testing.T) {
	t.Parallel()
	f := newDraftsFixture(t, nil)
	ctx := context.Background()
	dev, err := f.app.store.Roles().GetByName(ctx, "dev")
	if err != nil {
		t.Fatal(err)
	}
	setAccess(t, f.app, dev.ID, f.github, true, `["*"]`)
	if _, err := f.app.store.Roles().Create(ctx, store.Role{Name: "github-readers", Kind: store.RoleKindApplication, OwnerAppID: f.github.ID}); err != nil {
		t.Fatal(err)
	}
	kims := f.create(t, f.root, draftApp("jira", "https://jira.example/mcp", "Kim's.")).Draft.ID
	for _, id := range []string{"abc", "999999", kims} {
		if code, got := f.get(t, f.erin, id); code != http.StatusNotFound ||
			got.Error != "There is no draft "+id+". List the drafts with strazactl drafts list, or open Drafts on the console." {
			t.Errorf("erin reads draft %s: %d %q, want the 404", id, code, got.Error)
		}
	}
	removal := f.create(t, f.erin, removalDoc("App", "github")).Draft.ID
	_, got := f.get(t, f.erin, removal)
	var live []string
	for object := range got.Live {
		live = append(live, object)
	}
	sort.Strings(live)
	if strings.Join(live, ",") != "App/github,Role/github-readers" {
		t.Errorf("erin's live block = %v, want github and the role it owns, not the global role dev", live)
	}
	e := f.create(t, f.erin, draftApp("github", "https://api.github.example/mcp?region=eu", "Ours.")).Draft.ID
	if code, a := f.call(t, http.MethodPut, "/v1/admin/drafts/"+e, f.root, map[string]any{"revision": 1,
		"documents": []string{draftApp("github", "https://api.github.example/mcp?region=eu", "Ours."), roleText(t, "dev", "", "Developers.", "")}}); code != http.StatusOK {
		t.Fatalf("root's revision = %d %q", code, a.Error)
	}
	_, got = f.get(t, f.erin, e)
	withheld := "Straza leaves out the document of Role/dev, because reading a role needs the scope identity:read, " +
		"or for a role a server owns, the scope apps:read or that server's admin role. Ask an administrator for that grant."
	for _, it := range got.Draft.Items {
		switch it.Kind + "/" + it.Name {
		case "App/github":
			if it.Doc == "" || it.Withheld != "" {
				t.Errorf("erin reads App/github as %+v, want its document", it)
			}
		case "Role/dev":
			if it.Doc != "" || it.Base != "" || it.Op != "put" || it.Withheld != withheld || !it.Existed {
				t.Errorf("erin reads Role/dev as %+v, want kind, name, op and that it existed, with the reason", it)
			}
		}
	}
	_, ada := f.get(t, f.ada, e)
	for _, it := range ada.Draft.Items {
		if it.Doc == "" || it.Withheld != "" || it.Base != "" || !it.Existed {
			t.Errorf("a holder of drafts:read alone reads %s/%s as %+v, want its document, that it existed, and no base", it.Kind, it.Name, it)
		}
	}
	_, root := f.get(t, f.root, e)
	for _, it := range root.Draft.Items {
		if it.Base == "" || !it.Existed {
			t.Errorf("root reads %s/%s as %+v, want its base", it.Kind, it.Name, it)
		}
	}
	if len(ada.Live) != 0 {
		t.Errorf("a holder of drafts:read alone reads live %v, want nothing it may not read", ada.Live)
	}
}

// TestGetDraftMasksManifests pins the masking of a draft read: an item's App
// document and the live one read in one app.yaml form with env values,
// server block values and defaults and the address's query masked, so an
// unchanged manifest reads equal on both sides. Such a document sent back
// by a reader is waived for the stored values. Every other place the secret scan flags
// reads masked as well, such as an argument that holds a token, which a
// manifest or a draft stored before the scan may keep.
func TestGetDraftMasksManifests(t *testing.T) {
	t.Parallel()
	f := newDraftsFixture(t, nil)
	withDefault := func(description string) string {
		return strings.Replace(draftApp("github", "https://api.github.example/mcp?region=eu", description), "value: team-a}", "value: team-a, default: team-b}", 1)
	}
	putServer(t, f.app, withDefault("The GitHub server."))
	created := f.create(t, f.root, withDefault("A new description."))
	_, got := f.get(t, f.root, created.Draft.ID)
	item, live := got.Draft.Items[0].Doc, got.Live["App/github"].Doc
	for _, doc := range []string{item, live} {
		mf, err := manager.Parse([]byte(doc))
		if err != nil {
			t.Fatalf("a masked document does not read: %v\n%s", err, doc)
		}
		remotes, _ := mf.Server["remotes"].([]any)
		first, _ := remotes[0].(map[string]any)
		headers, _ := first["headers"].([]any)
		header, _ := headers[0].(map[string]any)
		if mf.Straza.Runtime.Remote.URL != "https://api.github.example/mcp?\u2026" || first["url"] != "https://api.github.example/mcp?\u2026" ||
			header["value"] != "[REDACTED]" || header["default"] != "[REDACTED]" {
			t.Errorf("masked document:\n%s", doc)
		}
	}
	if strings.Replace(item, "A new description.", "The GitHub server.", 1) != live {
		t.Errorf("the item and live documents differ beyond the description:\n%s\n%s", item, live)
	}
	if created.Draft.Items[0].Doc != item || !created.Draft.Items[0].Existed {
		t.Errorf("create answered the item as %+v\nwant it masked as GET answers it, and existed\n%s", created.Draft.Items[0], item)
	}
	oci := "apiVersion: straza.dev/v1beta1\nkind: App\nmetadata:\n  name: runner\nserver:\n  name: example.com/runner\n  version: 1.0.0\n" +
		"straza:\n  runtime:\n    kind: oci\n    oci:\n      image: ghcr.io/example/runner:1\n      env:\n        - {name: LOG, value: debug}\n"
	runner := f.create(t, f.root, oci)
	if mf, err := manager.Parse([]byte(runner.Draft.Items[0].Doc)); err != nil || mf.Straza.Runtime.OCI.Env[0].Value != "[REDACTED]" {
		t.Errorf("a container's env reads %v %v, want its value masked", mf.Straza.Runtime.OCI, err)
	}
	code, a := f.call(t, http.MethodPost, "/v1/admin/drafts", f.root, map[string]any{"documents": []string{item}})
	if code != http.StatusCreated || !slices.ContainsFunc(a.Verdict.Warnings, func(w wireFinding) bool { return w.Code == "bundle.masked" }) {
		t.Errorf("a masked document sent back = %d %+v, want 201 with the mask waived for the stored value", code, a.Verdict.Warnings)
	}
	putServer(t, f.app, draftApp("alpha", "https://alice@alpha.example/mcp", "Alpha."))
	alpha := f.create(t, f.root, draftApp("alpha", "https://alice@alpha.example/mcp", "Alpha, described anew.")).Draft.ID
	_, got = f.get(t, f.root, alpha)
	for _, doc := range []string{got.Draft.Items[0].Doc, got.Live["App/alpha"].Doc} {
		if mf, err := manager.Parse([]byte(doc)); err != nil || mf.Straza.Runtime.Remote.URL != "https://%5BREDACTED%5D@alpha.example/mcp" {
			t.Errorf("an address with a user name reads %v %v, want the user name marked", mf.Straza.Runtime.Remote, err)
		}
	}
	code, a = f.call(t, http.MethodPost, "/v1/admin/drafts", f.root, map[string]any{"documents": []string{got.Draft.Items[0].Doc}})
	if code != http.StatusCreated || !slices.ContainsFunc(a.Verdict.Warnings, func(w wireFinding) bool { return w.Code == "bundle.masked" }) {
		t.Errorf("an address whose user name was masked sent back = %d %+v, want 201 with the mask waived for the stored value", code, a.Verdict.Warnings)
	}
	flow := `{apiVersion: straza.dev/v1beta1, kind: App, metadata: {name: beta}, server: {name: example.com/beta, version: 1.0.0}, ` +
		`straza: {runtime: {kind: remote, remote: {url: "https://beta.example/mcp"}}}}`
	code, a = f.call(t, http.MethodPost, "/v1/admin/drafts", f.root, map[string]any{"items": []map[string]string{{"kind": "App", "name": "beta", "op": "put", "doc": flow}}})
	if code != http.StatusCreated {
		t.Fatalf("a flow-style document = %d %q", code, a.Error)
	}
	if mf, err := manager.Parse([]byte(a.Draft.Items[0].Doc)); err != nil || mf.Straza.Runtime.Remote.URL != "https://beta.example/mcp" || a.Draft.Items[0].Existed {
		t.Errorf("a flow-style document of a new server reads %+v, want it answered as app.yaml and not existed", a.Draft.Items[0])
	}
	putServer(t, f.app, runnerApp("      args: [--token, "+fakeToken+", --password, Hunter2Hunter2]\n"))
	// A draft saved before the scan read the argument after a flag may hold
	// this item, which intake now refuses, so the store takes it directly.
	row, err := f.app.store.Drafts().Create(context.Background(), store.DraftRow{Door: "api"},
		[]store.DraftItemRow{{Kind: "App", Name: "runner", Op: "put", Doc: runnerApp("      args: [--verbose, --password, Hunter2Hunter2]\n")}},
		store.DraftRevisionRow{Author: store.DraftActor{ID: f.kim.ID, Name: "kim", Via: laneLogin, Client: clientLogin}})
	if err != nil {
		t.Fatal(err)
	}
	_, got = f.get(t, f.root, strconv.FormatInt(row.ID, 10))
	for _, side := range []struct{ name, doc, args string }{
		{"item", got.Draft.Items[0].Doc, "--verbose --password [REDACTED]"},
		{"live", got.Live["App/runner"].Doc, "--token [REDACTED] --password [REDACTED]"},
	} {
		mf, err := manager.Parse([]byte(side.doc))
		if err != nil || mf.Straza.Runtime.Command == nil || strings.Join(mf.Straza.Runtime.Command.Args, " ") != side.args {
			t.Errorf("the %s document of a command line that holds secrets reads %v\n%s\nwant the args %q", side.name, err, side.doc, side.args)
		}
	}
	// An address keeps its host and masks only its capability, and a
	// description masked whole is waived when it comes back.
	capability := "q8Lp3Xv9" + "Rt2Wm7Kc" + "4Hb1Nd6Z"
	putServer(t, f.app, strings.Replace(draftApp("hook", "https://hooks.example.com/services/T0AB12CD3/B0EF45GH6/"+capability, "x"),
		`description: "x"`, "description: 'rotate "+fakeToken+" soon'", 1))
	_, got = f.get(t, f.root, f.create(t, f.root, draftApp("hook", "https://hooks.example.com/v2/mcp", "The hook server.")).Draft.ID)
	live = got.Live["App/hook"].Doc
	mf, err := manager.Parse([]byte(live))
	if err != nil {
		t.Fatalf("the live hook does not read: %v\n%s", err, live)
	}
	remotes, _ := mf.Server["remotes"].([]any)
	first, _ := remotes[0].(map[string]any)
	hook := "https://hooks.example.com/services/T0AB12CD3/B0EF45GH6/[REDACTED]"
	if mf.Straza.Runtime.Remote.URL != hook || first["url"] != hook || mf.Metadata.Description != "[REDACTED]" || strings.Contains(live, capability) {
		t.Errorf("the live hook reads\n%s\nwant its addresses %q and its description masked", live, hook)
	}
	code, a = f.call(t, http.MethodPost, "/v1/admin/drafts", f.root, map[string]any{"documents": []string{live}})
	if code != http.StatusCreated || !slices.ContainsFunc(a.Verdict.Warnings, func(w wireFinding) bool { return w.Code == "bundle.masked" }) {
		t.Errorf("the live hook sent back = %d %+v, want 201 with the mask waived for the stored value", code, a.Verdict.Warnings)
	}
}

// TestGetDraftStampsAnUnstampedDraft pins that GET stamps an open draft
// with an item not stamped yet, writes exactly one draft.check with no
// actor for that stamp, and stores the verdict an agent reads.
func TestGetDraftStampsAnUnstampedDraft(t *testing.T) {
	t.Parallel()
	f := newDraftsFixture(t, nil)
	ctx := context.Background()
	bot, err := f.app.store.Users().GetByUsername(ctx, "bot")
	if err != nil {
		t.Fatal(err)
	}
	row, err := f.app.store.Drafts().Create(ctx, store.DraftRow{Door: string(drafts.DoorAgent)},
		[]store.DraftItemRow{{Kind: "Role", Name: "dev", Op: "put", Doc: roleText(t, "dev", "", "Developers.", "")}},
		store.DraftRevisionRow{Author: store.DraftActor{ID: bot.ID, Name: "bot", Agent: true, Via: laneSession, Client: "claude-code", SponsorID: f.kim.ID, SponsorName: "kim"}})
	if err != nil {
		t.Fatal(err)
	}
	id := strconv.FormatInt(row.ID, 10)
	for range 2 {
		if code, got := f.get(t, f.root, id); code != http.StatusOK {
			t.Fatalf("get = %d %q", code, got.Error)
		}
	}
	stamped, items := f.stored(t, id)
	fp, _ := liveOf(t, f.app, "dev")
	if stamped.CheckedRevision != 1 || items[0].Base != fp || items[0].BaseOp != "put" || stamped.AgentVerdict == "" {
		t.Errorf("stored check %d, item %q %q, agent verdict %q, want the stamp of revision 1 with the verdict an agent reads",
			stamped.CheckedRevision, items[0].Base, items[0].BaseOp, stamped.AgentVerdict)
	}
	checks := draftRecords(t, f.app, "draft.check", id)
	if len(checks) != 1 || checks[0]["actor"] != nil {
		t.Errorf("draft.check records = %v, want one with no actor", checks)
	}
}

// TestGetPublishedDraft pins GET of a published draft: its change record,
// masked and cut for its reader, and may_publish false with no refusal.
func TestGetPublishedDraft(t *testing.T) {
	t.Parallel()
	f := newDraftsFixture(t, nil)
	d := f.create(t, f.root, draftApp("linear", "https://linear.example/mcp?team=x", "The Linear server.")).Draft.ID
	publishStored(t, f, d)
	_, got := f.get(t, f.root, d)
	if got.Draft.State != "published" || got.MayPublish || got.PublishRefusal != "" {
		t.Errorf("published draft reads %s may %v %q", got.Draft.State, got.MayPublish, got.PublishRefusal)
	}
	if len(got.Changes) != 1 || got.Changes[0].BeforeOp != "remove" || got.Changes[0].AfterOp != "put" ||
		!strings.Contains(got.Changes[0].AfterDoc, "https://linear.example/mcp?\u2026") {
		t.Errorf("changes = %+v, want App/linear created, its address masked", got.Changes)
	}
	e := f.create(t, f.erin, draftApp("github", "https://api.github.example/mcp?region=eu", "Ours.")).Draft.ID
	if code, a := f.call(t, http.MethodPut, "/v1/admin/drafts/"+e, f.root, map[string]any{"revision": 1,
		"documents": []string{draftApp("github", "https://api.github.example/mcp?region=eu", "Ours."), draftRole("helpers", "Helpers.")}}); code != http.StatusOK {
		t.Fatalf("root's revision = %d %q", code, a.Error)
	}
	publishStored(t, f, e)
	_, erin := f.get(t, f.erin, e)
	if len(erin.Changes) != 1 || erin.Changes[0].Kind != "App" {
		t.Errorf("erin reads the changes %+v, want github's alone", erin.Changes)
	}
}

// TestGetDecidedDraftAnswersItsStoredCheck pins GET of a draft that is not
// open. It runs no check, so an object that its own publish or a
// later change moved never reads as stale. It answers the check the server
// stored for its revision, the counts in checks and no line, with
// may_publish false and no publish_refusal. A revision the server never
// checked answers no time, snapshot or counts. A published draft adds its
// change record, and every decided draft still answers live state.
func TestGetDecidedDraftAnswersItsStoredCheck(t *testing.T) {
	t.Parallel()
	f := newDraftsFixture(t, nil)
	ctx := context.Background()
	at := time.Date(2026, 9, 20, 10, 0, 0, 0, time.UTC)
	kim := store.DraftActor{ID: f.kim.ID, Name: "kim", Via: laneLogin, Client: clientLogin}
	stored := func(name, base, baseOp string) string {
		row, err := f.app.store.Drafts().Create(ctx, store.DraftRow{Door: "api", CheckedRevision: 1, CheckedAt: &at,
			CheckedSnapshot: "snapshot-of-the-check", CheckCounts: `{"refused":0,"risks":2,"warnings":1,"unchecked":3}`},
			[]store.DraftItemRow{{Kind: "App", Name: name, Op: "put", Doc: draftApp(name, "https://"+name+".example/mcp", "Stored."), Base: base, BaseOp: baseOp}},
			store.DraftRevisionRow{Author: kim})
		if err != nil {
			t.Fatal(err)
		}
		return strconv.FormatInt(row.ID, 10)
	}
	checked := stored("linear", "", "remove")
	publishStored(t, f, checked)
	unchecked := stored("notion", "", "remove")
	n, _ := strconv.ParseInt(unchecked, 10, 64)
	if _, err := f.app.store.Drafts().Revise(ctx, n, store.DraftRevise{From: 1, Rev: store.DraftRevisionRow{Author: kim},
		Items: []store.DraftItemRow{{Kind: "App", Name: "notion", Op: "put", Doc: draftApp("notion", "https://notion.example/mcp", "Revised.")}}}); err != nil {
		t.Fatal(err)
	}
	publishStored(t, f, unchecked)
	closed := map[string]string{}
	for state, name := range map[string]string{"discarded": "github", "expired": "jira"} {
		closed[state] = stored(name, "a fingerprint live state no longer holds", "put")
		n, _ := strconv.ParseInt(closed[state], 10, 64)
		if _, err := f.app.store.Drafts().Close(ctx, n, 0, state, kim, "", time.Now()); err != nil {
			t.Fatal(err)
		}
	}
	counts := &checksPayload{Risks: 2, Warnings: 1, Unchecked: 3, Revision: 1, CheckedAt: "2026-09-20T10:00:00Z"}
	for _, tc := range []struct {
		name, id, object string
		revision         int
		checkedAt, snap  string
		checks           *checksPayload
		changes          int
	}{
		{"published at the revision the server checked", checked, "App/linear", 1, counts.CheckedAt, "snapshot-of-the-check", counts, 1},
		{"published at a revision the server never checked", unchecked, "App/notion", 2, "", "", nil, 1},
		{"discarded", closed["discarded"], "App/github", 1, counts.CheckedAt, "snapshot-of-the-check", counts, 0},
		{"expired", closed["expired"], "App/jira", 1, counts.CheckedAt, "snapshot-of-the-check", counts, 0},
	} {
		code, raw, _ := adminBytes(t, http.MethodGet, f.base+"/v1/admin/drafts/"+tc.id, f.root, "", nil)
		var got wireDetail
		if err := json.Unmarshal(raw, &got); err != nil || code != http.StatusOK {
			t.Fatalf("%s: get = %d %s", tc.name, code, raw)
		}
		v := got.Verdict
		if lines := len(v.Refused) + len(v.Risks) + len(v.Warnings) + len(v.Unchecked) + len(v.Passed) + len(v.Info) + len(v.Gains) + len(v.Needs); lines != 0 {
			t.Errorf("%s: the verdict holds %d lines, want none from a check run on the read: %+v", tc.name, lines, v)
		}
		if v.Draft != tc.id || v.Revision != tc.revision || v.CheckedAt != tc.checkedAt || v.Snapshot != tc.snap {
			t.Errorf("%s: verdict of draft %q revision %d checked at %q against %q, want %s, %d, %q and %q",
				tc.name, v.Draft, v.Revision, v.CheckedAt, v.Snapshot, tc.id, tc.revision, tc.checkedAt, tc.snap)
		}
		if !reflect.DeepEqual(got.Checks, tc.checks) || got.MayPublish || got.PublishRefusal != "" || len(got.Changes) != tc.changes {
			t.Errorf("%s: checks %+v, may_publish %v %q, %d changes; want %+v, false, no refusal and %d changes",
				tc.name, got.Checks, got.MayPublish, got.PublishRefusal, len(got.Changes), tc.checks, tc.changes)
		}
		if got.Live[tc.object].Op != "put" {
			t.Errorf("%s: live %s = %+v, want the live server", tc.name, tc.object, got.Live[tc.object])
		}
		inst, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
		if err != nil {
			t.Fatal(err)
		}
		if err := openAPISchema(t, "DraftDetail").Validate(inst); err != nil {
			t.Errorf("%s: DraftDetail does not validate %s: %v", tc.name, raw, err)
		}
	}
}

// TestGetDraftWaivesAsItsCreateDid pins that GET of an open draft runs
// intake again for the author of its last revision and waives what live
// state holds, as the create did. So the read answers the warnings the
// create answered, and it refuses a value live state no longer holds. A
// draft that is not open still answers its stored check with no line.
func TestGetDraftWaivesAsItsCreateDid(t *testing.T) {
	t.Parallel()
	f := newDraftsFixture(t, nil)
	putServer(t, f.app, waivedRunner("runner", "The runner."))
	if _, err := f.app.store.Roles().Create(context.Background(), store.Role{Name: "dev\u200bops", Kind: store.RoleKindBusiness}); err != nil {
		t.Fatal(err)
	}
	server := f.create(t, f.root, waivedRunner("runner", "Changed."))
	role := f.create(t, f.root, draftRole("dev\u200bops", "New words."))
	discarded := f.create(t, f.root, waivedRunner("runner", "Discarded."))
	if code, a := f.call(t, http.MethodPost, "/v1/admin/drafts/"+discarded.Draft.ID+"/discard", f.root, map[string]any{"revision": 1}); code != http.StatusOK {
		t.Fatalf("discard = %d %q", code, a.Error)
	}
	moved := f.create(t, f.root, waivedRunner("runner", "Moved."))
	keys := func(fs []wireFinding) string {
		var out []string
		for _, fnd := range fs {
			out = append(out, fnd.Code+" "+fnd.Key)
		}
		sort.Strings(out)
		return strings.Join(out, ", ")
	}
	cases := []struct {
		name    string
		created wireAnswer
		waived  string
	}{
		{"an open draft of a server whose manifest holds the waived value", server, "secret.value"},
		{"an open draft of a role whose live name holds an invisible character", role, "bundle.name-characters"},
	}
	for _, tc := range cases {
		code, got := f.get(t, f.root, tc.created.Draft.ID)
		if code != http.StatusOK || len(got.Verdict.Refused) != 0 || keys(got.Verdict.Warnings) != keys(tc.created.Verdict.Warnings) ||
			!strings.Contains(keys(got.Verdict.Warnings), tc.waived+" ") {
			t.Errorf("%s: %d with refused [%s] and warnings [%s], want 200, none refused and the create's warnings [%s] with %s",
				tc.name, code, keys(got.Verdict.Refused), keys(got.Verdict.Warnings), keys(tc.created.Verdict.Warnings), tc.waived)
		}
	}
	if _, got := f.get(t, f.root, discarded.Draft.ID); got.Checks == nil || got.Checks.Warnings != len(discarded.Verdict.Warnings) ||
		len(got.Verdict.Refused)+len(got.Verdict.Warnings) != 0 {
		t.Errorf("the discarded draft answers checks %+v and the lines %v %v, want its stored %d warnings and no line",
			got.Checks, got.Verdict.Refused, got.Verdict.Warnings, len(discarded.Verdict.Warnings))
	}
	putServer(t, f.app, strings.Replace(waivedRunner("runner", "The runner."), "oauth]", "oauth2]", 1))
	if _, got := f.get(t, f.root, moved.Draft.ID); !strings.Contains(keys(got.Verdict.Refused), "secret.value ") {
		t.Errorf("a draft whose waived value live state no longer holds answers refused [%s], want secret.value", keys(got.Verdict.Refused))
	}
}

// TestGetDraftWaivesInTheReadersView pins that GET waives in the view of
// its reader and stamps in the view of the draft's author, an agent here,
// as the checker does. A reader without apps:read gets the same
// answer for a guess of a live server's value that live state holds and
// for a wrong guess, so a read tells them nothing about a server they may
// not read, while root, who reads the server, reads the right guess
// waived. No refusal is answered twice, and the stamp a read writes counts
// what the checker's stamp of the same draft counts.
func TestGetDraftWaivesInTheReadersView(t *testing.T) {
	t.Parallel()
	f := newDraftsFixture(t, nil)
	ctx := context.Background()
	putServer(t, f.app, waivedRunner("runner", "The runner."))
	bot, err := f.app.store.Users().GetByUsername(ctx, "bot")
	if err != nil {
		t.Fatal(err)
	}
	stored := func(doc string) (store.DraftRow, []store.DraftItemRow) {
		row, err := f.app.store.Drafts().Create(ctx, store.DraftRow{Door: string(drafts.DoorAgent)},
			[]store.DraftItemRow{{Kind: "App", Name: "runner", Op: "put", Doc: doc}},
			store.DraftRevisionRow{Author: store.DraftActor{ID: bot.ID, Name: "bot", Agent: true, Via: laneSession, Client: "claude-code",
				SponsorID: f.kim.ID, SponsorName: "kim"}})
		if err != nil {
			t.Fatal(err)
		}
		return f.stored(t, strconv.FormatInt(row.ID, 10))
	}
	held := waivedRunner("runner", "Changed.")
	wrong := strings.Replace(held, "oauth]", "oauth2]", 1)
	lines := func(fs []wireFinding) string {
		var out []string
		for _, fnd := range fs {
			out = append(out, fnd.Code+" "+fnd.Sentence+" "+fnd.Fix)
		}
		return strings.Join(out, "\n")
	}
	codes := func(fs []wireFinding) string {
		var out []string
		for _, fnd := range fs {
			out = append(out, fnd.Code)
		}
		return strings.Join(out, ",")
	}
	for _, tc := range []struct {
		name, bearer, heldRefused, heldWarned string
	}{
		{"a reader who holds drafts:read alone", f.ada, "agent.runtime,secret.value", ""},
		{"root, who reads the server", f.root, "agent.runtime", "secret.value"},
	} {
		right, _ := stored(held)
		guess, _ := stored(wrong)
		_, a := f.get(t, tc.bearer, strconv.FormatInt(right.ID, 10))
		_, b := f.get(t, tc.bearer, strconv.FormatInt(guess.ID, 10))
		if codes(a.Verdict.Refused) != tc.heldRefused || !strings.Contains(codes(a.Verdict.Warnings), tc.heldWarned) {
			t.Errorf("%s: the right guess answers refused [%s] and warnings [%s], want [%s] and %q among them",
				tc.name, codes(a.Verdict.Refused), codes(a.Verdict.Warnings), tc.heldRefused, tc.heldWarned)
		}
		if tc.heldWarned == "" && (lines(a.Verdict.Refused) != lines(b.Verdict.Refused) || lines(a.Verdict.Warnings) != lines(b.Verdict.Warnings)) {
			t.Errorf("%s: the right guess answers\n%s\n%s\nand the wrong one\n%s\n%s\nwant the same answer",
				tc.name, lines(a.Verdict.Refused), lines(a.Verdict.Warnings), lines(b.Verdict.Refused), lines(b.Verdict.Warnings))
		}
		for _, v := range []wireVerdict{a.Verdict, b.Verdict} {
			seen := map[string]bool{}
			for _, fnd := range v.Refused {
				if line := fnd.Key + " " + fnd.Sentence; seen[line] {
					t.Errorf("%s: %s is answered twice: %s", tc.name, fnd.Code, fnd.Sentence)
				} else {
					seen[line] = true
				}
			}
		}
		stamped, _ := f.stored(t, strconv.FormatInt(right.ID, 10))
		twin, items := stored(held)
		if err := f.app.checkOne(ctx, twin, items); err != nil {
			t.Fatal(err)
		}
		checked, _ := f.stored(t, strconv.FormatInt(twin.ID, 10))
		if stamped.CheckCounts == "" || stamped.CheckCounts != checked.CheckCounts {
			t.Errorf("%s: the read stamped %s, and the checker stamps %s for the same draft", tc.name, stamped.CheckCounts, checked.CheckCounts)
		}
	}
}

// TestGetDraftContactedComparesTheDigest pins that the tool names a
// Contact read for an App item count only for the document they were read
// for.
func TestGetDraftContactedComparesTheDigest(t *testing.T) {
	t.Parallel()
	f := newDraftsFixture(t, nil)
	doc := draftApp("linear", "https://linear.example/mcp", "One.")
	offered := fmt.Sprintf(`{"digest":"%x","tools":["get_me","list_issues"],"at":"2026-09-25T10:00:00Z"}`, sha256.Sum256([]byte(doc)))
	row, err := f.app.store.Drafts().Create(context.Background(), store.DraftRow{Door: "api"},
		[]store.DraftItemRow{{Kind: "App", Name: "linear", Op: "put", Doc: doc, Offered: offered}},
		store.DraftRevisionRow{Author: store.DraftActor{ID: f.kim.ID, Name: "kim", Via: laneLogin, Client: clientLogin}})
	if err != nil {
		t.Fatal(err)
	}
	id := strconv.FormatInt(row.ID, 10)
	_, got := f.get(t, f.root, id)
	if c := got.Contacted["App/linear"]; c.At != "2026-09-25T10:00:00Z" || strings.Join(c.Tools, ",") != "get_me,list_issues" {
		t.Errorf("contacted = %+v, want the tools read for this document", got.Contacted)
	}
	if code, a := f.call(t, http.MethodPut, "/v1/admin/drafts/"+id, f.root, map[string]any{"revision": 1,
		"documents": []string{draftApp("linear", "https://linear.example/mcp", "Two.")}}); code != http.StatusOK {
		t.Fatalf("update = %d %q", code, a.Error)
	}
	if _, after := f.get(t, f.root, id); len(after.Contacted) != 0 {
		t.Errorf("contacted = %+v after the document changed, want none", after.Contacted)
	}
}

// TestDraftPayloadListsTheProposerFirst pins the draft's authors, each
// principal once and the proposer first, and that every time a route
// answers is RFC3339 in UTC.
func TestDraftPayloadListsTheProposerFirst(t *testing.T) {
	t.Parallel()
	f := newDraftsFixture(t, nil)
	id := f.create(t, f.erin, draftApp("github", "https://api.github.example/mcp?region=eu", "One.")).Draft.ID
	for i, bearer := range []string{f.root, f.erin} {
		if code, a := f.call(t, http.MethodPut, "/v1/admin/drafts/"+id, bearer, map[string]any{"revision": i + 1,
			"documents": []string{draftApp("github", "https://api.github.example/mcp?region=eu", fmt.Sprintf("Take %d.", i+2))}}); code != http.StatusOK {
			t.Fatalf("revision %d = %d %q", i+2, code, a.Error)
		}
	}
	_, got := f.get(t, f.root, id)
	var authors []string
	for _, a := range got.Draft.Authors {
		authors = append(authors, a.Username)
	}
	if strings.Join(authors, ",") != "erin,kim" || len(got.Revisions) != 3 {
		t.Errorf("authors = %v over %d revisions, want erin then kim over 3", authors, len(got.Revisions))
	}
	times := []string{got.Draft.CreatedAt, got.Draft.UpdatedAt, got.Verdict.CheckedAt}
	for _, rev := range got.Revisions {
		times = append(times, rev.CreatedAt)
	}
	for _, s := range times {
		if at, err := time.Parse(time.RFC3339, s); err != nil || !strings.HasSuffix(s, "Z") || at.IsZero() {
			t.Errorf("time %q is not RFC3339 in UTC", s)
		}
	}
}

// TestListDraftsDependsOnVisibleRowsOnly pins the list for a caller without
// root or drafts:read: a page and its cursor come from the rows the
// caller may read alone, so a filter that matches only hidden drafts
// answers as one that matches nothing, and a later author lists a draft
// it did not propose. The drafts lie newest first: two hidden, erin's, two
// hidden, erin's and one hidden.
// So a full page's cursor is its own last row, never a hidden row between
// it and the next row erin may list, and a page followed by hidden rows
// alone carries no cursor.
func TestListDraftsDependsOnVisibleRowsOnly(t *testing.T) {
	t.Parallel()
	f := newDraftsFixture(t, nil)
	ctx := context.Background()
	linear := func() { f.create(t, f.root, draftApp("linear", "https://linear.example/mcp", "Kim's.")) }
	linear()
	own := f.create(t, f.erin, draftApp("github", "https://api.github.example/mcp?region=eu", "Erin's.")).Draft.ID
	linear()
	linear()
	later := f.create(t, f.root, draftApp("github", "https://api.github.example/mcp?region=eu", "Root's.")).Draft.ID
	for i, revision := range []struct {
		bearer string
		docs   []string
	}{
		{f.erin, []string{draftApp("github", "https://api.github.example/mcp?region=eu", "Erin's take.")}},
		{f.root, []string{draftApp("github", "https://api.github.example/mcp?region=eu", "Erin's take."), draftApp("jira", "https://jira.example/mcp", "Kim's.")}},
	} {
		if code, a := f.call(t, http.MethodPut, "/v1/admin/drafts/"+later, revision.bearer, map[string]any{"revision": i + 1, "documents": revision.docs}); code != http.StatusOK {
			t.Fatalf("revision %d = %d %q", i+2, code, a.Error)
		}
	}
	for i := range 2 {
		row, err := f.app.store.Drafts().Create(ctx, store.DraftRow{Door: "api", Slot: "policy:secret-plan"},
			[]store.DraftItemRow{{Kind: "PolicySet", Name: "secret-plan", Op: "put", Doc: draftSet("secret-plan")}},
			store.DraftRevisionRow{Author: store.DraftActor{ID: f.kim.ID, Name: "kim", Via: laneLogin, Client: clientLogin}})
		if err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			if _, err := f.app.store.Drafts().Close(ctx, row.ID, 0, "discarded", store.DraftActor{ID: f.kim.ID, Name: "kim"}, "", row.CreatedAt); err != nil {
				t.Fatal(err)
			}
		}
	}
	for _, pair := range [][2]string{
		{"state=all&object=App/linear&limit=1", "state=all&object=App/nothing-by-this-name&limit=1"},
		{"state=all&object=App/linear&limit=2", "state=all&object=App/nothing-by-this-name&limit=2"},
		{"state=all&policy_edit=secret-plan&limit=1", "state=all&policy_edit=no-such-set&limit=1"},
	} {
		_, hidden := f.list(t, f.erin, pair[0])
		_, nothing := f.list(t, f.erin, pair[1])
		if pageIDs(hidden) != "" || hidden.NextCursor != "" || pageIDs(nothing) != "" || nothing.NextCursor != "" {
			t.Errorf("%s answers [%s] %q and %s answers [%s] %q, want both empty", pair[0], pageIDs(hidden), hidden.NextCursor, pair[1], pageIDs(nothing), nothing.NextCursor)
		}
	}
	if _, p := f.list(t, f.erin, "state=all&object=App/jira"); pageIDs(p) != later {
		t.Errorf("erin's drafts naming jira = [%s], want the one she revised, %s", pageIDs(p), later)
	}
	for _, tc := range []struct {
		name, query, ids, cursor string
	}{
		{"a full page whose next listable row lies below hidden rows", "state=all&limit=1", later, later},
		{"the page after it, followed by hidden rows alone", "state=all&limit=1&cursor=" + later, own, ""},
		{"both of erin's drafts, followed by hidden rows alone", "state=all&limit=2", later + "," + own, ""},
	} {
		if _, p := f.list(t, f.erin, tc.query); pageIDs(p) != tc.ids || p.NextCursor != tc.cursor {
			t.Errorf("%s: [%s] %q, want [%s] %q", tc.name, pageIDs(p), p.NextCursor, tc.ids, tc.cursor)
		}
	}
}

// TestListDraftsBoundsItsScan pins the bound of the list's scan: a
// caller without root or drafts:read who reads nothing among draftScanMax
// hidden drafts gets an empty page whose cursor is the last row read, and
// the next page goes on from there. The bound is 1,000 rows. The test
// lowers it to 20, which the scan reads in five store pages, so that it
// stores a few dozen drafts, and so it does not run in parallel.
func TestListDraftsBoundsItsScan(t *testing.T) {
	if draftScanMax != 1000 {
		t.Fatalf("draftScanMax = %d, want the 1,000 rows of 7.4", draftScanMax)
	}
	saved := draftScanMax
	draftScanMax = 20
	t.Cleanup(func() { draftScanMax = saved })
	f := newDraftsFixture(t, nil)
	ctx := context.Background()
	own := f.create(t, f.erin, draftApp("github", "https://api.github.example/mcp?region=eu", "Erin's.")).Draft.ID
	var hidden []int64
	for range draftScanMax + 1 {
		row, err := f.app.store.Drafts().Create(ctx, store.DraftRow{Door: "api"}, []store.DraftItemRow{{Kind: "App", Name: "jira", Op: "remove"}},
			store.DraftRevisionRow{Author: store.DraftActor{ID: f.kim.ID, Name: "kim", Via: laneLogin, Client: clientLogin}})
		if err != nil {
			t.Fatal(err)
		}
		hidden = append(hidden, row.ID)
	}
	_, first := f.list(t, f.erin, "state=all&limit=1")
	if want := strconv.FormatInt(hidden[1], 10); pageIDs(first) != "" || first.NextCursor != want {
		t.Fatalf("the first page = [%s] %q, want empty with the cursor %s after %d rows", pageIDs(first), first.NextCursor, want, draftScanMax)
	}
	if _, second := f.list(t, f.erin, "state=all&limit=1&cursor="+first.NextCursor); pageIDs(second) != own || second.NextCursor != "" {
		t.Errorf("the second page = [%s] %q, want erin's draft %s and no cursor", pageIDs(second), second.NextCursor, own)
	}
}

// revisionReads is a store that counts the reads of draft revisions: each
// call of Revisions, and each call of RevisionsOf that names a draft.
type revisionReads struct {
	store.Store
	n *atomic.Int32
}

func (s revisionReads) Drafts() store.DraftRepo { return revisionReadsRepo{s.Store.Drafts(), s.n} }

type revisionReadsRepo struct {
	store.DraftRepo
	n *atomic.Int32
}

func (r revisionReadsRepo) Revisions(ctx context.Context, id int64) ([]store.DraftRevisionRow, error) {
	r.n.Add(1)
	return r.DraftRepo.Revisions(ctx, id)
}

func (r revisionReadsRepo) RevisionsOf(ctx context.Context, ids []int64) (map[int64][]store.DraftRevisionRow, error) {
	if len(ids) > 0 {
		r.n.Add(1)
	}
	return r.DraftRepo.RevisionsOf(ctx, ids)
}

// TestListDraftsReadsRevisionsOncePerStorePage pins the cost of the list
// for a caller without root or drafts:read: one read of revisions per
// store page, for the rows neither their proposer nor the server rule
// opens, and none for root. The rule stays: erin lists her own draft and
// the draft she revised, never ada's. The last case lowers draftScanMax to
// 20, read in five store pages, so the test does not run in parallel.
func TestListDraftsReadsRevisionsOncePerStorePage(t *testing.T) {
	saved := draftScanMax
	t.Cleanup(func() { draftScanMax = saved })
	reads := &atomic.Int32{}
	f := newDraftsFixture(t, []func(*App){func(a *App) { a.store = revisionReads{a.store, reads} }})
	ctx := context.Background()
	at := time.Now()
	ada, err := f.app.store.Users().GetByUsername(ctx, "ada")
	if err != nil {
		t.Fatal(err)
	}
	own := f.create(t, f.erin, draftApp("github", "https://api.github.example/mcp?region=eu", "Erin's.")).Draft.ID
	var hidden []string
	for range 21 {
		row, err := f.app.store.Drafts().Create(ctx, store.DraftRow{Door: "api", CheckedRevision: 1, CheckedAt: &at, CheckCounts: "{}"},
			[]store.DraftItemRow{{Kind: "App", Name: "jira", Op: "remove", Base: "fp", BaseOp: "put"}},
			store.DraftRevisionRow{Author: store.DraftActor{ID: ada.ID, Name: "ada", Via: laneLogin, Client: clientLogin}})
		if err != nil {
			t.Fatal(err)
		}
		hidden = append(hidden, strconv.FormatInt(row.ID, 10))
	}
	later := f.create(t, f.root, draftApp("github", "https://api.github.example/mcp?region=eu", "Root's.")).Draft.ID
	for i, revision := range []struct {
		bearer string
		docs   []string
	}{
		{f.erin, []string{draftApp("github", "https://api.github.example/mcp?region=eu", "Erin's take.")}},
		{f.root, []string{draftApp("github", "https://api.github.example/mcp?region=eu", "Erin's take."), draftApp("jira", "https://jira.example/mcp", "Kim's.")}},
	} {
		if code, a := f.call(t, http.MethodPut, "/v1/admin/drafts/"+later, revision.bearer, map[string]any{"revision": i + 1, "documents": revision.docs}); code != http.StatusOK {
			t.Fatalf("revision %d = %d %q", i+2, code, a.Error)
		}
	}
	for _, tc := range []struct {
		name, bearer, query string
		scanMax             int
		ids                 string
		reads               int32
	}{
		{"root, to whom every draft is open", f.root, "state=all&limit=2", 1000, later + "," + hidden[20], 0},
		{"erin over one store page", f.erin, "state=all&limit=5", 1000, later + "," + own, 1},
		{"erin over five store pages", f.erin, "state=all&limit=1", 20, later, 5},
	} {
		draftScanMax = tc.scanMax
		reads.Store(0)
		code, p := f.list(t, tc.bearer, tc.query)
		if code != http.StatusOK || pageIDs(p) != tc.ids || reads.Load() != tc.reads {
			t.Errorf("%s: %d [%s] after %d reads of revisions, want 200 [%s] after %d", tc.name, code, pageIDs(p), reads.Load(), tc.ids, tc.reads)
		}
	}
}

// TestGetDraftCutsContactedForTheReader pins that contacted leaves out a
// server the reader may not read, while root reads it.
func TestGetDraftCutsContactedForTheReader(t *testing.T) {
	t.Parallel()
	f := newDraftsFixture(t, nil)
	ctx := context.Background()
	erin, err := f.app.store.Users().GetByUsername(ctx, "erin")
	if err != nil {
		t.Fatal(err)
	}
	offered := func(doc string) string {
		return fmt.Sprintf(`{"digest":"%x","tools":["get_me"],"at":"2026-09-25T10:00:00Z"}`, sha256.Sum256([]byte(doc)))
	}
	gh := draftApp("github", "https://api.github.example/mcp?region=eu", "Erin's.")
	jira := draftApp("jira", "https://jira.example/mcp", "Kim's jira.")
	row, err := f.app.store.Drafts().Create(ctx, store.DraftRow{Door: "api"},
		[]store.DraftItemRow{{Kind: "App", Name: "github", Op: "put", Doc: gh, Offered: offered(gh)}},
		store.DraftRevisionRow{Author: store.DraftActor{ID: erin.ID, Name: "erin", Via: laneLogin, Client: clientLogin}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.app.store.Drafts().Revise(ctx, row.ID, store.DraftRevise{From: 1,
		Items: []store.DraftItemRow{{Kind: "App", Name: "github", Op: "put", Doc: gh, Offered: offered(gh)}, {Kind: "App", Name: "jira", Op: "put", Doc: jira, Offered: offered(jira)}},
		Rev:   store.DraftRevisionRow{Author: store.DraftActor{ID: f.kim.ID, Name: "kim", Via: laneLogin, Client: clientLogin}}}); err != nil {
		t.Fatal(err)
	}
	id := strconv.FormatInt(row.ID, 10)
	names := func(bearer string) string {
		_, got := f.get(t, bearer, id)
		var out []string
		for object := range got.Contacted {
			out = append(out, object)
		}
		sort.Strings(out)
		return strings.Join(out, ",")
	}
	if got := names(f.erin); got != "App/github" {
		t.Errorf("erin's contacted = %s, want github alone", got)
	}
	if got := names(f.root); got != "App/github,App/jira" {
		t.Errorf("root's contacted = %s, want both", got)
	}
}

// guardLoose is the live set guard with its deny turned into an allow and a
// reason that names what the rule protects.
const guardLoose = `apiVersion: straza.dev/v1beta1
kind: PolicySet
metadata: {name: guard}
spec:
  priority: 10
  match: {roles: [dev]}
  rules:
    - id: no-shell
      events: [tool.pre]
      tools: [shell.exec]
      effect: allow
      reason: "Straza: the payroll box shell is open for dev"
`

// TestGetDraftCutsLinesOnObjectsTheReaderCannotRead pins the line cut
// for an author without root or drafts:read: every line on a set, a role or
// a server it may not read loses its words and keeps its code, class and
// key, while a holder of drafts:read reads the same lines whole.
func TestGetDraftCutsLinesOnObjectsTheReaderCannotRead(t *testing.T) {
	t.Parallel()
	f := newDraftsFixture(t, nil)
	if code, out, _ := adminBytes(t, http.MethodPut, f.base+"/v1/admin/policies", f.root, "application/yaml", []byte(draftSet("guard"))); code != http.StatusCreated {
		t.Fatalf("store guard = %d %s", code, out)
	}
	if code := adminReq(t, http.MethodPost, f.base+"/v1/admin/policies/guard/activate", f.root, map[string]string{"status": "active"}, nil); code != http.StatusOK {
		t.Fatalf("activate guard = %d", code)
	}
	e := f.create(t, f.erin, draftApp("github", "https://api.github.example/mcp?region=eu", "Erin's.")).Draft.ID
	if code, a := f.call(t, http.MethodPut, "/v1/admin/drafts/"+e, f.root, map[string]any{"revision": 1, "documents": []string{
		draftApp("github", "https://api.github.example/mcp?region=eu", "Erin's."), guardLoose,
		roleText(t, "dev", "", "Developers, with payroll access.", ""), draftApp("jira", "https://jira-new.example/mcp", "Jira moved to the payroll host."),
	}}); code != http.StatusOK {
		t.Fatalf("root's revision = %d %q", code, a.Error)
	}
	_, nell := f.get(t, f.nell, e)
	_, erin := f.get(t, f.erin, e)
	byKey := map[string]wireFinding{}
	for _, list := range [][]wireFinding{erin.Verdict.Risks, erin.Verdict.Warnings, erin.Verdict.Unchecked, erin.Verdict.Info} {
		for _, fnd := range list {
			byKey[fnd.Key] = fnd
		}
	}
	onSet := 0
	for _, list := range [][]wireFinding{nell.Verdict.Risks, nell.Verdict.Warnings, nell.Verdict.Unchecked, nell.Verdict.Info} {
		for _, whole := range list {
			if whole.Object != "PolicySet/guard" && whole.Object != "Role/dev" {
				continue
			}
			onSet++
			cut, ok := byKey[whole.Key]
			if !ok || cut.Code != whole.Code || cut.Class != whole.Class || cut.Object != "" || !strings.HasPrefix(cut.Sentence, "This line names ") || cut.Fix != "" {
				t.Errorf("erin reads %+v as %+v, want its words left out and its code, class and key kept", whole, cut)
			}
		}
	}
	if onSet == 0 {
		t.Fatal("the check wrote no line on guard or dev, so the probe proves nothing")
	}
	code, out, _ := adminBytes(t, http.MethodGet, f.base+"/v1/admin/drafts/"+e, f.erin, "", nil)
	for _, word := range []string{"no-shell", "payroll", "jira-new"} {
		if code != http.StatusOK || strings.Contains(string(out), word) {
			t.Errorf("erin's answer (%d) holds %q", code, word)
		}
	}
}

// TestExistedWithoutAStampComesFromLiveState pins existed for an item no
// stamp reached: a row intake refused on /check, and
// every row of a discarded draft nobody checked, read whether the object
// exists in the World the route read, a set that is off included.
func TestExistedWithoutAStampComesFromLiveState(t *testing.T) {
	t.Parallel()
	f := newDraftsFixture(t, nil)
	ctx := context.Background()
	if code, out, _ := adminBytes(t, http.MethodPut, f.base+"/v1/admin/policies", f.root, "application/yaml", []byte(draftSet("parked"))); code != http.StatusCreated {
		t.Fatalf("store parked = %d %s", code, out)
	}
	existed := func(items []wireItem) map[string]bool {
		out := map[string]bool{}
		for _, it := range items {
			out[it.Kind+"/"+it.Name] = it.Existed
		}
		return out
	}
	secret := draftApp("github", "https://svc:Hunter2Hunter2@api.github.example/mcp", "A password in the address.")
	code, a := f.call(t, http.MethodPost, "/v1/admin/drafts/check", f.root, map[string]any{"documents": []string{secret, draftRole("dev", "Developers.")}})
	if got, want := existed(a.Items), map[string]bool{"App/github": true, "Role/dev": true}; code != http.StatusOK || !reflect.DeepEqual(got, want) {
		t.Errorf("/check with an intake refusal = %d, existed %v, want %v", code, got, want)
	}
	row, err := f.app.store.Drafts().Create(ctx, store.DraftRow{Door: "api"},
		[]store.DraftItemRow{{Kind: "Role", Name: "dev", Op: "put", Doc: roleText(t, "dev", "", "Developers.", "")},
			{Kind: "PolicySet", Name: "parked", Op: "put", Doc: draftSet("parked")},
			{Kind: "App", Name: "linear", Op: "put", Doc: draftApp("linear", "https://linear.example/mcp", "New.")}},
		store.DraftRevisionRow{Author: store.DraftActor{ID: f.kim.ID, Name: "kim", Via: laneLogin, Client: clientLogin}})
	if err != nil {
		t.Fatal(err)
	}
	code, a = f.call(t, http.MethodPost, "/v1/admin/drafts/"+strconv.FormatInt(row.ID, 10)+"/discard", f.root, nil)
	if got, want := existed(a.Draft.Items), map[string]bool{"Role/dev": true, "PolicySet/parked": true, "App/linear": false}; code != http.StatusOK || !reflect.DeepEqual(got, want) {
		t.Errorf("the discard of a draft nobody checked = %d, existed %v, want %v", code, got, want)
	}
}

// TestGetDraftCutsGainsOnRolesTheReaderCannotRead pins the gain cut by role:
// github's server admin reads the row of the
// role github owns and not the row of the global role that implies it, and
// the info line counts that row in words that name roles. Root reads both.
func TestGetDraftCutsGainsOnRolesTheReaderCannotRead(t *testing.T) {
	t.Parallel()
	f := newDraftsFixture(t, nil)
	ctx := context.Background()
	readers, err := f.app.store.Roles().Create(ctx, store.Role{Name: "github-readers", Kind: store.RoleKindApplication, OwnerAppID: f.github.ID})
	if err != nil {
		t.Fatal(err)
	}
	managers, err := f.app.store.Roles().Create(ctx, store.Role{Name: "managers", Kind: "business"})
	if err != nil {
		t.Fatal(err)
	}
	if err := f.app.store.Roles().AddImplication(ctx, managers.ID, readers.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.app.store.Roles().Assign(ctx, store.RoleAssignment{SubjectKind: store.SubjectUser, SubjectID: f.kim.ID, RoleID: managers.ID}); err != nil {
		t.Fatal(err)
	}
	f.app.resolver.Bump()
	id := f.create(t, f.erin, roleText(t, "github-readers", "github", "Readers.", "github", "search_code")).Draft.ID
	read := func(bearer string) (string, []string) {
		_, got := f.get(t, bearer, id)
		roles := map[string]bool{}
		for _, g := range got.Verdict.Gains {
			roles[g.Role] = true
		}
		var names, hidden []string
		for role := range roles {
			names = append(names, role)
		}
		sort.Strings(names)
		for _, fnd := range got.Verdict.Info {
			if fnd.Code == "info.gains-hidden" {
				hidden = append(hidden, fnd.Sentence)
			}
		}
		return strings.Join(names, ","), hidden
	}
	want := "1 row of who gains what names a role or a server you cannot read, so this view leaves it out. Ask an administrator for the grant that reads it."
	if roles, hidden := read(f.erin); roles != "github-readers" || !reflect.DeepEqual(hidden, []string{want}) {
		t.Errorf("erin reads the gains of %s and %q, want github-readers alone and %q", roles, hidden, want)
	}
	if roles, hidden := read(f.root); roles != "github-readers,managers" || len(hidden) != 0 {
		t.Errorf("root reads the gains of %s and %q, want both roles and no line", roles, hidden)
	}
}
