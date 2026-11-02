package server

import (
	"context"
	"encoding/json"
	"net/http"
	"reflect"
	"testing"

	"github.com/strazahq/straza/internal/config"
	"github.com/strazahq/straza/internal/manager"
	"github.com/strazahq/straza/internal/objects"
	"github.com/strazahq/straza/internal/store"
)

// trackerManifest is tracker, a remote server that pins the invented
// object type ticket: ticket_read names its ticket.
const trackerManifest = `apiVersion: straza.dev/v1beta1
kind: App
metadata: {name: tracker}
server: {name: straza.test/tracker, version: "1.0.0"}
straza:
  runtime:
    kind: remote
    remote: {url: "http://127.0.0.1:1/mcp"}
  objects:
    type: ticket
    list: {tool: ticket_list}
    tools:
      ticket_read: {argument: ticket}
`

// ticketRole is the Role document of the application role name that
// tracker owns, binding ticket_read on the server app with the covers line
// covers, or with none when covers is empty.
func ticketRole(name, app, covers string) string {
	doc := "apiVersion: straza.dev/v1beta1\nkind: Role\nmetadata:\n    name: " + name + "\nspec:\n    kind: application\n    server: tracker\n" +
		"    bindings:\n        - app: " + app + "\n          tools:\n            - ticket_read\n"
	if covers != "" {
		doc += "          covers: " + covers + "\n"
	}
	return doc
}

// TestCoversChanges pins how a draft's role item changes what a role covers
// on one server's tickets, compared with the live covers line by id: a
// line that reaches more tickets or every ticket adds the role, a removed
// role, a narrowed line, a line gone and a binding moved to another server
// drop it, and a line that reaches the same tickets, a value the list does
// not hold, a role of another server and a document that does not read
// change nothing.
func TestCoversChanges(t *testing.T) {
	t.Parallel()
	ids := func(list ...string) coverSet {
		s := coverSet{ids: map[string]bool{}}
		for _, id := range list {
			s.ids[id] = true
		}
		return s
	}
	live := map[string]coverSet{"tracker-triage": ids("101"), "tracker-ops": ids("101", "102"), "tracker-all": {every: true}}
	values := map[string]string{"T-1": "101", "T-2": "102", "T-3": "103"}
	put := func(name, app, covers string) store.DraftItemRow {
		return store.DraftItemRow{Kind: "Role", Name: name, Op: "put", Doc: ticketRole(name, app, covers)}
	}
	on := func(role, change string, tickets ...string) map[string][]objectDraft {
		out := map[string][]objectDraft{}
		for _, id := range tickets {
			out[id] = []objectDraft{{Draft: "7", Role: role, Change: change}}
		}
		return out
	}
	cases := []struct {
		name string
		item store.DraftItemRow
		want map[string][]objectDraft
	}{
		{"a line that reaches one more ticket adds the role there", put("tracker-triage", "tracker", "{objects: [T-1, T-2]}"), on("tracker-triage", "adds", "102")},
		{"a line of every ticket adds the role where it does not reach", put("tracker-triage", "tracker", "every"), on("tracker-triage", "adds", "102", "103")},
		{"a new role adds where its line reaches", put("tracker-new", "tracker", "{objects: [T-3]}"), on("tracker-new", "adds", "103")},
		{"a removed role drops where it reaches", store.DraftItemRow{Kind: "Role", Name: "tracker-ops", Op: "remove"}, on("tracker-ops", "drops", "101", "102")},
		{"a narrowed line drops", put("tracker-ops", "tracker", "{objects: [T-2]}"), on("tracker-ops", "drops", "101")},
		{"a line from every to some drops", put("tracker-all", "tracker", "{objects: [T-1]}"), on("tracker-all", "drops", "102", "103")},
		{"a line gone drops", put("tracker-ops", "tracker", ""), on("tracker-ops", "drops", "101", "102")},
		{"a binding moved to another server drops", put("tracker-ops", "helpdesk", "every"), on("tracker-ops", "drops", "101", "102")},
		{"the same tickets change nothing", put("tracker-ops", "tracker", "{objects: [T-2, T-1]}"), map[string][]objectDraft{}},
		{"a value the list does not hold changes nothing", put("tracker-new", "tracker", "{objects: [T-9]}"), map[string][]objectDraft{}},
		{"a role of another server changes nothing", put("helpdesk-agents", "helpdesk", "every"), map[string][]objectDraft{}},
		{"a document that does not read changes nothing", store.DraftItemRow{Kind: "Role", Name: "tracker-ops", Op: "put", Doc: "spec: ["}, map[string][]objectDraft{}},
	}
	for _, tc := range cases {
		changes := coversChanges("tracker", live, values, []store.DraftRow{{ID: 7}}, map[int64][]store.DraftItemRow{7: {tc.item}})
		got := map[string][]objectDraft{}
		for _, id := range []string{"101", "102", "103"} {
			if ds := draftsOn(changes, id); ds != nil {
				got[id] = ds
			}
		}
		if !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%s: %v, want %v", tc.name, got, tc.want)
		}
	}
	// Drafts sort by their number and then by role, so draft 9 comes
	// before draft 10.
	changes := coversChanges("tracker", live, values, []store.DraftRow{{ID: 10}, {ID: 9}}, map[int64][]store.DraftItemRow{
		10: {put("tracker-triage", "tracker", "every")},
		9:  {put("tracker-triage", "tracker", "every"), put("tracker-new", "tracker", "every")},
	})
	want := []objectDraft{{Draft: "9", Role: "tracker-new", Change: "adds"}, {Draft: "9", Role: "tracker-triage", Change: "adds"}, {Draft: "10", Role: "tracker-triage", Change: "adds"}}
	if got := draftsOn(changes, "103"); !reflect.DeepEqual(got, want) {
		t.Errorf("the order on T-3 = %v, want %v", got, want)
	}
}

// TestAppObjectsNameTheirOpenDrafts pins the drafts of each entry of the
// object route on tracker, a server of the invented type ticket: root reads
// a draft that adds a role on one ticket, a draft that adds a role on
// every ticket, and a removal that drops a role, and so does an admin API
// token with drafts:read, while tracker's own admin, who may not read the
// draft that also holds a policy set, reads the other two and never that
// one, and a token without a drafts grant reads none, as the drafts queue
// lists them. An entry no draft changes carries no drafts, and every
// answer matches ObjectList.
func TestAppObjectsNameTheirOpenDrafts(t *testing.T) {
	t.Parallel()
	f := newDraftsFixture(t, nil, func(c *config.Config) { c.Apps.Objects.Enabled = false })
	ctx := context.Background()
	mf, err := manager.Parse([]byte(trackerManifest))
	if err != nil {
		t.Fatal(err)
	}
	tracker := putServer(t, f.app, trackerManifest)
	seedList(t, f.app, tracker.ID, "ticket", objects.DeclHash(*mf.Straza.Objects), [][2]string{{"T-1", "101"}, {"T-2", "102"}, {"T-3", "103"}, {"T-4", "104"}})
	if err := f.app.objects.Load(ctx); err != nil {
		t.Fatal(err)
	}
	live := f.create(t, f.root, ticketRole("tracker-triage", "tracker", "{objects: [T-1]}"), ticketRole("tracker-ops", "tracker", "{objects: [T-1, T-2]}")).Draft.ID
	if code, a := f.publishAll(t, f.root, live); code != http.StatusOK {
		t.Fatalf("publish the live roles = %d %q", code, a.Error)
	}
	adds := f.create(t, f.root, ticketRole("tracker-triage", "tracker", "{objects: [T-1, T-2]}")).Draft.ID
	every := f.create(t, f.root, ticketRole("tracker-wide", "tracker", "every"), draftSet("side")).Draft.ID
	drops := f.create(t, f.root, removalDoc("Role", "tracker-ops")).Draft.ID
	mkHuman(t, f.app, "tara", f.adminRole(t, tracker))

	read := func(bearer string) map[string][]objectDraft {
		t.Helper()
		code, raw, _ := adminBytes(t, http.MethodGet, f.base+"/v1/admin/apps/tracker/objects", bearer, "", nil)
		if code != http.StatusOK {
			t.Fatalf("GET objects = %d %s", code, raw)
		}
		assertSchema(t, "ObjectList", raw)
		var out struct {
			Plural  string `json:"plural"`
			Objects []struct {
				Value  string        `json:"value"`
				Drafts []objectDraft `json:"drafts"`
			} `json:"objects"`
		}
		if err := json.Unmarshal(raw, &out); err != nil {
			t.Fatal(err)
		}
		if out.Plural != "tickets" || len(out.Objects) != 4 {
			t.Fatalf("GET objects = %s, want the four tickets", raw)
		}
		got := map[string][]objectDraft{}
		for _, o := range out.Objects {
			if o.Drafts != nil {
				got[o.Value] = o.Drafts
			}
		}
		return got
	}
	add := func(id, role string) objectDraft { return objectDraft{Draft: id, Role: role, Change: "adds"} }
	drop := objectDraft{Draft: drops, Role: "tracker-ops", Change: "drops"}
	wide := add(every, "tracker-wide")
	all := map[string][]objectDraft{"T-1": {wide, drop}, "T-2": {add(adds, "tracker-triage"), wide, drop}, "T-3": {wide}, "T-4": {wide}}
	if got := read(f.root); !reflect.DeepEqual(got, all) {
		t.Errorf("root reads %v, want %v", got, all)
	}
	if got := read(f.token); !reflect.DeepEqual(got, all) {
		t.Errorf("an admin API token with drafts:read reads %v, want %v", got, all)
	}
	if lister, _ := f.mintToken(t, "lister", "apps:read"); len(read(lister)) != 0 {
		t.Errorf("an admin API token without a drafts grant reads %v, want no draft", read(lister))
	}
	if got, want := read(f.login(t, "tara")), map[string][]objectDraft{
		"T-1": {drop}, "T-2": {add(adds, "tracker-triage"), drop},
	}; !reflect.DeepEqual(got, want) {
		t.Errorf("tracker's admin reads %v, want %v without draft %s", got, want, every)
	}
}

// TestAppObjectsHideARoleTheCallerCannotRead pins the item read rule on the
// object route: tara administers tracker and wrote a draft, so the queue
// lists it to her, and root revised it with night-watch, a role of no
// server that binds tracker on every ticket. The draft's own route
// withholds night-watch's document from tara, so the object route never
// names night-watch to her either, while it still names her own role, and
// root reads both.
func TestAppObjectsHideARoleTheCallerCannotRead(t *testing.T) {
	t.Parallel()
	f := newDraftsFixture(t, nil, func(c *config.Config) { c.Apps.Objects.Enabled = false })
	ctx := context.Background()
	mf, err := manager.Parse([]byte(trackerManifest))
	if err != nil {
		t.Fatal(err)
	}
	tracker := putServer(t, f.app, trackerManifest)
	seedList(t, f.app, tracker.ID, "ticket", objects.DeclHash(*mf.Straza.Objects), [][2]string{{"T-1", "101"}, {"T-2", "102"}})
	if err := f.app.objects.Load(ctx); err != nil {
		t.Fatal(err)
	}
	mkHuman(t, f.app, "tara", f.adminRole(t, tracker))
	tara := f.login(t, "tara")
	own := ticketRole("tracker-triage", "tracker", "{objects: [T-1]}")
	id := f.create(t, tara, own).Draft.ID
	global := "apiVersion: straza.dev/v1beta1\nkind: Role\nmetadata:\n    name: night-watch\nspec:\n    kind: application\n" +
		"    bindings:\n        - app: tracker\n          tools:\n            - ticket_read\n          covers: every\n"
	if code, a := f.call(t, http.MethodPut, "/v1/admin/drafts/"+id, f.root, map[string]any{"revision": 1, "documents": []string{own, global}}); code != http.StatusOK {
		t.Fatalf("root's revision = %d %q", code, a.Error)
	}
	read := func(bearer string) map[string][]objectDraft {
		t.Helper()
		code, raw, _ := adminBytes(t, http.MethodGet, f.base+"/v1/admin/apps/tracker/objects", bearer, "", nil)
		if code != http.StatusOK {
			t.Fatalf("GET objects = %d %s", code, raw)
		}
		var out struct {
			Objects []struct {
				Value  string        `json:"value"`
				Drafts []objectDraft `json:"drafts"`
			} `json:"objects"`
		}
		if err := json.Unmarshal(raw, &out); err != nil {
			t.Fatal(err)
		}
		got := map[string][]objectDraft{}
		for _, o := range out.Objects {
			if o.Drafts != nil {
				got[o.Value] = o.Drafts
			}
		}
		return got
	}
	watch, triage := objectDraft{Draft: id, Role: "night-watch", Change: "adds"}, objectDraft{Draft: id, Role: "tracker-triage", Change: "adds"}
	if got, want := read(f.root), map[string][]objectDraft{"T-1": {watch, triage}, "T-2": {watch}}; !reflect.DeepEqual(got, want) {
		t.Errorf("root reads %v, want %v", got, want)
	}
	if got, want := read(tara), map[string][]objectDraft{"T-1": {triage}}; !reflect.DeepEqual(got, want) {
		t.Errorf("tara reads %v, want %v without night-watch", got, want)
	}
}
