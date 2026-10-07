package server

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/strazahq/straza/internal/config"
	"github.com/strazahq/straza/internal/manager"
	"github.com/strazahq/straza/internal/store"
)

// serverFile is the App document of the uncredentialed remote server name
// at url, as an operator writes it into the apps directory.
func serverFile(name, url string) string {
	return fmt.Sprintf("apiVersion: straza.dev/v1beta1\nkind: App\nmetadata: {name: %s}\nserver: {name: straza.test/%s, version: \"1.0.0\"}\n"+
		"straza:\n  runtime:\n    kind: remote\n    remote: {url: %q}\n", name, name, url)
}

func fileHash(text string) string {
	sum := sha256.Sum256([]byte(text))
	return hex.EncodeToString(sum[:])
}

// fileFixture is a replica with a person, kim, who may publish, and its
// apps directory, whose watcher sweeps only when a test says so. logs holds
// what strazad logged.
type fileFixture struct {
	*draftsFixture
	dir  string
	up   string
	logs *lockedBuffer
}

// lockedBuffer is a log sink that goroutines may write at once.
type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func newFileFixture(t *testing.T, mutators ...func(*config.Config)) *fileFixture {
	t.Helper()
	logs := &lockedBuffer{}
	app, base := testAppPreRun(t, []func(*App){func(a *App) { a.log = slog.New(slog.NewTextHandler(logs, nil)) }},
		append([]func(*config.Config){func(c *config.Config) {
			c.Apps.PollInterval = time.Hour
			c.Apps.HealthInterval = time.Hour
		}}, mutators...)...)
	f := &draftsFixture{app: app, base: base}
	f.kim = seedIdentity(t, app)
	grantAdmin(t, app, f.kim.ID)
	f.root = loginDeviceFlow(t, base, "kim", "hunter2!")
	return &fileFixture{draftsFixture: f, dir: app.cfg.AppsDir(), up: startTaggedUpstream(t, "one").URL, logs: logs}
}

func (f *fileFixture) path(name string) string { return filepath.Join(f.dir, name) }

// put writes text into the file name and sweeps.
func (f *fileFixture) put(t *testing.T, name, text string) {
	t.Helper()
	if err := os.WriteFile(f.path(name), []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
	f.app.watcher.Sweep(context.Background())
}

// drop removes the file name and sweeps.
func (f *fileFixture) drop(t *testing.T, name string) {
	t.Helper()
	if err := os.Remove(f.path(name)); err != nil {
		t.Fatal(err)
	}
	f.app.watcher.Sweep(context.Background())
}

// restart does what a boot of strazad does to the file door: a new
// watcher, with nothing seen, sweeps the directory, and the boot pass runs
// over it. The running watcher is left in place, so the test's later sweeps
// read the directory as before.
func (f *fileFixture) restart(t *testing.T) {
	t.Helper()
	ctx := context.Background()
	w := manager.NewWatcher(f.dir, time.Hour, nil)
	w.SetPropose(f.app.proposeFile)
	w.SetGone(f.app.fileGone)
	w.Sweep(ctx)
	f.app.bootRemovals(ctx, w)
}

// removals answers the open removals the apps directory proposed.
func (f *fileFixture) removals(t *testing.T) []store.DraftRow {
	t.Helper()
	var out []store.DraftRow
	for _, row := range f.open(t) {
		if strings.HasPrefix(row.SourceHash, "remove:") {
			out = append(out, row)
		}
	}
	return out
}

// bySource answers every draft of the source path, newest first.
func (f *fileFixture) bySource(t *testing.T, path string) []store.DraftRow {
	t.Helper()
	rows, err := f.app.store.Drafts().BySource(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	return rows
}

// open answers the open drafts of the apps directory, newest first.
func (f *fileFixture) open(t *testing.T) []store.DraftRow {
	t.Helper()
	rows, err := f.app.store.Drafts().List(context.Background(), store.DraftFilter{State: "open", Door: "apps-directory"}, 0, 200)
	if err != nil {
		t.Fatal(err)
	}
	return rows
}

// items answers the items of draft row as kind/name/op.
func (f *fileFixture) items(t *testing.T, row store.DraftRow) []string {
	t.Helper()
	_, items := f.stored(t, strconv.FormatInt(row.ID, 10))
	out := []string{}
	for _, it := range items {
		out = append(out, it.Kind+"/"+it.Name+"/"+it.Op)
	}
	return out
}

// publishNewest publishes the newest open draft of source path as kim and
// answers its id.
func (f *fileFixture) publishNewest(t *testing.T, path string) int64 {
	t.Helper()
	for _, row := range f.bySource(t, path) {
		if row.State != "open" {
			continue
		}
		id := strconv.FormatInt(row.ID, 10)
		if code, a := f.publishAll(t, f.root, id); code != http.StatusOK {
			t.Fatalf("publish %s = %d %q %+v", id, code, a.Error, a.Verdict.Refused)
		}
		return row.ID
	}
	t.Fatalf("no open draft of %s", path)
	return 0
}

// discard discards draft id as kim through the route and answers its
// draft.discard record.
func (f *fileFixture) discard(t *testing.T, id int64) map[string]any {
	t.Helper()
	sid := strconv.FormatInt(id, 10)
	if code, a := f.call(t, http.MethodPost, "/v1/admin/drafts/"+sid+"/discard", f.root, map[string]any{}); code != http.StatusOK {
		t.Fatalf("discard %s = %d %q", sid, code, a.Error)
	}
	return f.record(t, sid, "draft.discard")
}

// record answers the one record of action that names draft id.
func (f *fileFixture) record(t *testing.T, id, action string) map[string]any {
	t.Helper()
	var found []map[string]any
	for _, ev := range outboxOf(t, f.app, id) {
		if ev.data["action"] == action {
			found = append(found, ev.data)
		}
	}
	if len(found) != 1 {
		t.Fatalf("draft %s has %d %s records, want one: %v", id, len(found), action, found)
	}
	return found[0]
}

// noActorIn fails when a record names an actor.
func noActorIn(t *testing.T, rec map[string]any) {
	t.Helper()
	for _, key := range []string{"actor", "actorId", "actorVia"} {
		if _, ok := rec[key]; ok {
			t.Errorf("%s record carries %s: %v", rec["action"], key, rec)
		}
	}
}

// TestFileDoorProposesANewFile pins the file door for a new file: one open
// draft of the apps directory, proposed by strazad through the file,
// stamped, whose one App put holds the file's text, with draft.create and draft.check
// written once each, with no actor, and draft.create naming the file and
// its hash.
func TestFileDoorProposesANewFile(t *testing.T) {
	t.Parallel()
	f := newFileFixture(t)
	text := serverFile("tagged", f.up)
	f.put(t, "tagged.yaml", text)
	rows := f.bySource(t, f.path("tagged.yaml"))
	if len(rows) != 1 {
		t.Fatalf("drafts of the file = %d, want one", len(rows))
	}
	d := rows[0]
	if d.State != "open" || d.Door != "apps-directory" || d.SourceHash != fileHash(text) || d.CheckedRevision != 1 ||
		d.Proposer != (store.DraftActor{Name: "strazad", Via: "file", Client: "strazad"}) || d.Refusal != "" {
		t.Errorf("draft = %+v, want an open stamped draft of the file door proposed by strazad", d)
	}
	_, items := f.stored(t, strconv.FormatInt(d.ID, 10))
	if len(items) != 1 || items[0].Kind != "App" || items[0].Name != "tagged" || items[0].Op != "put" || items[0].Doc != text || items[0].BaseOp != "remove" {
		t.Errorf("items = %+v, want one App put of tagged with the file's text, stamped as new", items)
	}
	id := strconv.FormatInt(d.ID, 10)
	created := f.record(t, id, "draft.create")
	noActorIn(t, created)
	if created["door"] != "apps-directory" || created["source"] != f.path("tagged.yaml") || created["sourceHash"] != fileHash(text) {
		t.Errorf("draft.create = %v, want the door, the file and its hash", created)
	}
	noActorIn(t, f.record(t, id, "draft.check"))
	f.app.watcher.Sweep(context.Background())
	if n := len(f.bySource(t, f.path("tagged.yaml"))); n != 1 {
		t.Errorf("drafts after another sweep = %d, want one", n)
	}
}

// TestFileDoorReplacesAnOlderRevision pins that a changed file
// discards the older draft only this door wrote, with its reason and a
// draft.discard naming the file and no actor, and keeps a draft a person
// revised.
func TestFileDoorReplacesAnOlderRevision(t *testing.T) {
	t.Parallel()
	f := newFileFixture(t)
	ctx := context.Background()
	f.put(t, "tagged.yaml", serverFile("tagged", f.up+"/one"))
	first := f.bySource(t, f.path("tagged.yaml"))[0]
	f.put(t, "tagged.yaml", serverFile("tagged", f.up+"/two"))
	rows := f.bySource(t, f.path("tagged.yaml"))
	if len(rows) != 2 || rows[1].ID != first.ID || rows[1].State != "discarded" || rows[1].DecidedReason != "a newer revision of the file replaced it" {
		t.Fatalf("drafts = %+v, want the first discarded as replaced", rows)
	}
	discarded := f.record(t, strconv.FormatInt(first.ID, 10), "draft.discard")
	noActorIn(t, discarded)
	if discarded["source"] != f.path("tagged.yaml") || discarded["sourceHash"] != first.SourceHash || discarded["reason"] != "a newer revision of the file replaced it" {
		t.Errorf("draft.discard = %v, want the file, its hash and the reason", discarded)
	}

	second, items := f.stored(t, strconv.FormatInt(rows[0].ID, 10))
	if _, err := f.app.store.Drafts().Revise(ctx, second.ID, store.DraftRevise{From: 1, Items: items,
		Rev: store.DraftRevisionRow{Author: store.DraftActor{ID: f.kim.ID, Name: "kim", Via: "login", Client: "id-token"}, Door: "console", Digest: "d"}}); err != nil {
		t.Fatal(err)
	}
	f.put(t, "tagged.yaml", serverFile("tagged", f.up+"/three"))
	rows = f.bySource(t, f.path("tagged.yaml"))
	if len(rows) != 3 || rows[1].ID != second.ID || rows[1].State != "open" {
		t.Errorf("drafts = %+v, want the person's revision still open", rows)
	}
}

// TestFileDoorEqualToLiveProposesNothing pins that a file whose
// manifest equals live, whatever its bytes, proposes nothing and discards
// the open drafts of its path.
func TestFileDoorEqualToLiveProposesNothing(t *testing.T) {
	t.Parallel()
	f := newFileFixture(t)
	text := serverFile("tagged", f.up)
	f.put(t, "tagged.yaml", text)
	f.publishNewest(t, f.path("tagged.yaml"))
	f.put(t, "tagged.yaml", serverFile("tagged", f.up+"/two"))
	pending := f.bySource(t, f.path("tagged.yaml"))[0]
	f.put(t, "tagged.yaml", "# the same server\n"+text)
	rows := f.bySource(t, f.path("tagged.yaml"))
	if len(rows) != 2 || rows[0].ID != pending.ID || rows[0].State != "discarded" || rows[0].DecidedReason != "the file now equals live" {
		t.Errorf("drafts = %+v, want no new draft and the pending one discarded because the file equals live", rows)
	}
}

// TestFileDoorRefusesAFileThatDoesNotRead pins that a file that
// does not parse, holds a secret or names a provider this server lacks
// becomes a draft with no items whose verdict holds file.refused, a
// finding's fix follows file.refused's own "Fix the file.", a file still
// empty a sweep later is refused too, and the secret is in no drafts column
// and no record.
func TestFileDoorRefusesAFileThatDoesNotRead(t *testing.T) {
	t.Parallel()
	// The token is made at run time, so no scanner of the repository reads
	// a secret in this file.
	token := "ghp_" + fileHash("a token for the file door")[:36]
	f := newFileFixture(t)
	oauth := serverFile("oauthapp", f.up) +
		"  credential:\n    kind: oauth\n    oauth: {provider: github}\n    inject: {as: header, name: Authorization, template: \"Bearer {{secret}}\"}\n"
	cases := []struct {
		file, text, refusal string
	}{
		{"broken.yaml", "kind: Nope\n", "manifest:"},
		{"secret.yaml", strings.Replace(serverFile("secretapp", f.up), "metadata: {name: secretapp}",
			"metadata: {name: secretapp, description: \"token "+token+"\"}", 1), "metadata.description"},
		{"oauth.yaml", oauth, `credential.oauth.provider "github" is not configured on this server`},
	}
	for _, tc := range cases {
		f.put(t, tc.file, tc.text)
		rows := f.bySource(t, f.path(tc.file))
		if len(rows) != 1 || rows[0].Refusal == "" || !strings.Contains(rows[0].Refusal, tc.refusal) {
			t.Fatalf("%s: drafts = %+v, want one with a refusal naming %q", tc.file, rows, tc.refusal)
		}
		if items := f.items(t, rows[0]); len(items) != 0 {
			t.Errorf("%s: items = %v, want none", tc.file, items)
		}
		_, v := f.read(t, f.root, strconv.FormatInt(rows[0].ID, 10))
		if len(v.Refused) != 1 || v.Refused[0].Code != "file.refused" ||
			!strings.HasPrefix(v.Refused[0].Sentence, tc.file+" does not read as an MCP server manifest: ") {
			t.Errorf("%s: refused = %+v, want file.refused", tc.file, v.Refused)
		}
		if code, a, _ := f.publish(t, f.root, strconv.FormatInt(rows[0].ID, 10), acksFor(1, v)); code == http.StatusOK {
			t.Errorf("%s: the publish of a refused file answered %d %+v", tc.file, code, a.Draft)
		}
	}
	if rows := f.bySource(t, f.path("secret.yaml")); !strings.Contains(rows[0].Refusal, "strazactl apps secret set") {
		t.Errorf("the secret-holding file's refusal %q does not say to store the secret with strazactl apps secret set", rows[0].Refusal)
	}
	secret := f.bySource(t, f.path("secret.yaml"))[0]
	if _, v := f.read(t, f.root, strconv.FormatInt(secret.ID, 10)); len(v.Refused) != 1 ||
		!strings.HasPrefix(v.Refused[0].Fix, "Fix the file. ") || !strings.Contains(v.Refused[0].Fix, "strazactl apps secret set") ||
		!strings.HasSuffix(v.Refused[0].Fix, " Straza proposes it again once it is saved.") || strings.Contains(v.Refused[0].Sentence, "secret set") {
		t.Errorf("the secret-holding file's refusal reads %+v, want the scan's fix after \"Fix the file.\" and not in the sentence", v.Refused)
	}
	if err := os.WriteFile(f.path("empty.yaml"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	f.app.watcher.Sweep(context.Background())
	if rows := f.bySource(t, f.path("empty.yaml")); len(rows) != 0 {
		t.Errorf("an empty file was proposed at its first sweep: %+v", rows)
	}
	f.app.watcher.Sweep(context.Background())
	if rows := f.bySource(t, f.path("empty.yaml")); len(rows) != 1 || rows[0].Refusal == "" {
		t.Errorf("a file still empty a sweep later became %+v, want one refused draft", rows)
	}
	for _, row := range f.bySource(t, f.path("secret.yaml")) {
		raw, _ := json.Marshal(row)
		if strings.Contains(string(raw), token[:16]) {
			t.Errorf("the draft row holds the secret: %s", raw)
		}
		revs, err := f.app.store.Drafts().Revisions(context.Background(), row.ID)
		if err != nil {
			t.Fatal(err)
		}
		if raw, _ := json.Marshal(revs); strings.Contains(string(raw), token[:16]) {
			t.Errorf("the revisions hold the secret: %s", raw)
		}
	}
	events, err := f.app.store.Outbox().ListRecent(context.Background(), 1000)
	if err != nil {
		t.Fatal(err)
	}
	for _, ev := range events {
		if strings.Contains(ev.CE, token[:16]) {
			t.Errorf("a record holds the secret: %s", ev.CE)
		}
	}
}

// TestFileDoorRenames pins the file door's renames: a file moved to
// another path proposes no removal and ends the drafts of the old path, and
// a new metadata.name in one file proposes the new server and the removal
// of the old one in one draft.
func TestFileDoorRenames(t *testing.T) {
	t.Parallel()
	t.Run("a new path", func(t *testing.T) {
		t.Parallel()
		f := newFileFixture(t)
		text := serverFile("tagged", f.up)
		f.put(t, "a.yaml", text)
		f.publishNewest(t, f.path("a.yaml"))
		f.put(t, "a.yaml", serverFile("tagged", f.up+"/two"))
		pending := f.bySource(t, f.path("a.yaml"))[0]
		if err := os.WriteFile(f.path("b.yaml"), []byte(text), 0o600); err != nil {
			t.Fatal(err)
		}
		f.drop(t, "a.yaml")
		if open := f.open(t); len(open) != 0 {
			t.Errorf("open drafts = %+v, want none", open)
		}
		if row, _ := f.stored(t, strconv.FormatInt(pending.ID, 10)); row.DecidedReason != "the file is gone" {
			t.Errorf("the old path's draft reads %s %q, want discarded as gone", row.State, row.DecidedReason)
		}
		if _, err := f.app.store.Apps().GetByName(context.Background(), "tagged"); err != nil {
			t.Errorf("tagged after the move: %v", err)
		}
	})
	t.Run("a new metadata.name", func(t *testing.T) {
		t.Parallel()
		f := newFileFixture(t)
		f.put(t, "a.yaml", serverFile("tagged", f.up))
		f.publishNewest(t, f.path("a.yaml"))
		f.put(t, "a.yaml", serverFile("tagged2", f.up))
		open := f.open(t)
		if len(open) != 1 || !reflect.DeepEqual(f.items(t, open[0]), []string{"App/tagged2/put", "App/tagged/remove"}) {
			t.Errorf("open drafts = %+v, want one that adds tagged2 and removes tagged", open)
		}
	})
	t.Run("a second file naming the server", func(t *testing.T) {
		t.Parallel()
		f := newFileFixture(t)
		f.put(t, "a.yaml", serverFile("tagged", f.up))
		f.publishNewest(t, f.path("a.yaml"))
		f.put(t, "b.yaml", serverFile("tagged", f.up+"/two"))
		f.drop(t, "a.yaml")
		for _, row := range f.open(t) {
			if strings.HasPrefix(row.SourceHash, "remove:") {
				t.Errorf("a removal was proposed while b.yaml names the server: %+v", row)
			}
		}
	})
}

// TestFileDoorProposesRemovals pins the file door's removals: a gone file
// whose server it linked proposes one removal, whose hash names the link,
// and ends the open drafts of its path; a restart proposes it once; a discard by a
// person unlinks the server, which no restart proposes again; a file that
// comes back ends the removal, and going again proposes it again; and a
// server whose file went while strazad was down is proposed at the boot
// pass.
func TestFileDoorProposesRemovals(t *testing.T) {
	t.Parallel()
	t.Run("a gone file", func(t *testing.T) {
		t.Parallel()
		f := newFileFixture(t)
		f.put(t, "a.yaml", serverFile("tagged", f.up))
		link := f.publishNewest(t, f.path("a.yaml"))
		f.put(t, "a.yaml", serverFile("tagged", f.up+"/two"))
		f.put(t, "a.yaml", serverFile("tagged", f.up+"/three"))
		f.drop(t, "a.yaml")
		open := f.open(t)
		if len(open) != 1 || open[0].Source != f.path("a.yaml") || open[0].SourceHash != fmt.Sprintf("remove:tagged:%d", link) ||
			open[0].CheckedRevision != 1 || !reflect.DeepEqual(f.items(t, open[0]), []string{"App/tagged/remove"}) {
			t.Fatalf("open drafts = %+v, want one stamped removal of tagged that names link %d", open, link)
		}
		removal := open[0]
		f.restart(t)
		if open := f.open(t); len(open) != 1 || open[0].ID != removal.ID {
			t.Errorf("open drafts after a restart = %+v, want the one removal", open)
		}

		unlinked := f.discard(t, removal.ID)
		if unlinked["unlinked"] != "tagged" || unlinked["source"] != f.path("a.yaml") || unlinked["actor"] != "kim" {
			t.Errorf("draft.discard = %v, want kim unlinking tagged from the file", unlinked)
		}
		f.restart(t)
		if open := f.open(t); len(open) != 0 {
			t.Errorf("open drafts after the discard and a restart = %+v, want none", open)
		}
		if _, err := f.app.store.Apps().GetByName(context.Background(), "tagged"); err != nil {
			t.Errorf("tagged after its removal was discarded: %v", err)
		}
	})
	t.Run("a file gone while strazad was down", func(t *testing.T) {
		t.Parallel()
		f := newFileFixture(t)
		ctx := context.Background()
		elsewhere := filepath.Join(t.TempDir(), "tagged.yaml")
		text := serverFile("tagged", f.up)
		if err := f.app.proposeFile(ctx, manager.File{Path: elsewhere, Hash: fileHash(text), Raw: []byte(text), Name: "tagged"}); err != nil {
			t.Fatal(err)
		}
		link := f.publishNewest(t, elsewhere)
		f.app.proposeUnfiled(ctx)
		open := f.open(t)
		if len(open) != 1 || open[0].Source != elsewhere || open[0].SourceHash != fmt.Sprintf("remove:tagged:%d", link) {
			t.Errorf("open drafts = %+v, want the removal of tagged from its file", open)
		}
	})
	t.Run("a file that comes back ends its removal", func(t *testing.T) {
		t.Parallel()
		f := newFileFixture(t)
		text := serverFile("tagged", f.up)
		f.put(t, "a.yaml", text)
		f.publishNewest(t, f.path("a.yaml"))
		f.drop(t, "a.yaml")
		removal := f.open(t)[0]
		f.put(t, "a.yaml", text)
		if row, _ := f.stored(t, strconv.FormatInt(removal.ID, 10)); row.State != "discarded" || row.DecidedReason != "the file a.yaml declares tagged again" {
			t.Errorf("the removal reads %s %q, want discarded because the file names tagged again", row.State, row.DecidedReason)
		}
		f.drop(t, "a.yaml")
		if open := f.open(t); len(open) != 1 || open[0].ID == removal.ID || open[0].SourceHash != removal.SourceHash {
			t.Errorf("open drafts = %+v, want the removal proposed again when the file goes again", open)
		}
	})
	t.Run("a person's discard is not proposed again", func(t *testing.T) {
		t.Parallel()
		f := newFileFixture(t)
		f.put(t, "a.yaml", serverFile("tagged", f.up))
		f.discard(t, f.bySource(t, f.path("a.yaml"))[0].ID)
		f.restart(t)
		if open := f.open(t); len(open) != 0 {
			t.Errorf("open drafts after a restart = %+v, want the discarded revision not proposed again", open)
		}
	})
	t.Run("a revision this door discarded is proposed again", func(t *testing.T) {
		t.Parallel()
		f := newFileFixture(t)
		two := serverFile("tagged", f.up+"/two")
		f.put(t, "a.yaml", two)
		f.put(t, "a.yaml", serverFile("tagged", f.up+"/three"))
		f.put(t, "a.yaml", two)
		open := f.open(t)
		if len(open) != 1 || open[0].SourceHash != fileHash(two) {
			t.Errorf("open drafts = %+v, want the revision back in the file proposed again", open)
		}
	})
}

// TestFileDoorLinks pins what the link reads: a server installed
// from a file before the upgrade is linked through its source and proposed
// from the directory, a published removal through any door ends a link,
// and a server whose file is present but does not read is not proposed
// for removal at the boot pass.
func TestFileDoorLinks(t *testing.T) {
	t.Parallel()
	t.Run("a link made before the upgrade", func(t *testing.T) {
		t.Parallel()
		f := newFileFixture(t)
		row := putServer(t, f.app, serverFile("tagged", f.up))
		row.Source = store.AppSourceGitops
		if _, err := f.app.store.Apps().Update(context.Background(), row); err != nil {
			t.Fatal(err)
		}
		f.app.proposeUnfiled(context.Background())
		open := f.open(t)
		if len(open) != 1 || open[0].Source != f.dir || open[0].SourceHash != "remove:tagged:0" {
			t.Errorf("open drafts = %+v, want the removal of tagged from the directory", open)
		}
	})
	t.Run("a removal through the API ends the link", func(t *testing.T) {
		t.Parallel()
		f := newFileFixture(t)
		f.put(t, "a.yaml", serverFile("tagged", f.up))
		f.publishNewest(t, f.path("a.yaml"))
		if code := adminReq(t, http.MethodDelete, f.base+"/v1/admin/apps/tagged", f.root, nil, nil); code != http.StatusOK {
			t.Fatalf("delete = %d", code)
		}
		if code := rawReq(t, http.MethodPost, f.base+"/v1/admin/apps", f.root, "application/yaml", []byte(serverFile("tagged", f.up+"/api")), nil); code != http.StatusCreated {
			t.Fatalf("install = %d", code)
		}
		f.drop(t, "a.yaml")
		f.restart(t)
		if open := f.open(t); len(open) != 0 {
			t.Errorf("open drafts = %+v, want no removal of a server the API installed again", open)
		}
	})
	t.Run("a file that does not read keeps its server", func(t *testing.T) {
		t.Parallel()
		f := newFileFixture(t)
		ctx := context.Background()
		text := serverFile("tagged", f.up)
		if err := f.app.proposeFile(ctx, manager.File{Path: f.path("a.yaml"), Hash: fileHash(text), Raw: []byte(text), Name: "tagged"}); err != nil {
			t.Fatal(err)
		}
		f.publishNewest(t, f.path("a.yaml"))
		f.put(t, "a.yaml", "kind: Nope\n")
		f.app.proposeUnfiled(ctx)
		for _, row := range f.open(t) {
			if strings.HasPrefix(row.SourceHash, "remove:") {
				t.Errorf("a removal was proposed while the server's file does not read: %+v", row)
			}
		}
	})
}

// TestFileDoorProposesOneDraftPerHashAcrossReplicas pins one draft per file
// revision across replicas: watchers sweeping one directory at once over
// one store store one draft of a revision and write one draft.create.
func TestFileDoorProposesOneDraftPerHashAcrossReplicas(t *testing.T) {
	t.Parallel()
	t.Run("sqlite", func(t *testing.T) {
		t.Parallel()
		f := newFileFixture(t)
		ctx := context.Background()
		text := serverFile("tagged", f.up)
		if err := os.WriteFile(f.path("tagged.yaml"), []byte(text), 0o600); err != nil {
			t.Fatal(err)
		}
		var wg sync.WaitGroup
		for range 3 {
			w := manager.NewWatcher(f.dir, time.Hour, nil)
			w.SetPropose(f.app.proposeFile)
			w.SetGone(f.app.fileGone)
			wg.Go(func() { w.Sweep(ctx) })
		}
		wg.Wait()
		f.app.watcher.Sweep(ctx)
		rows := f.bySource(t, f.path("tagged.yaml"))
		if len(rows) != 1 {
			t.Fatalf("drafts of one revision = %d, want one", len(rows))
		}
		f.record(t, strconv.FormatInt(rows[0].ID, 10), "draft.create")
	})
	t.Run("postgres", func(t *testing.T) {
		t.Parallel()
		dsn := freshPostgresDSN(t)
		dir := t.TempDir()
		up := startTaggedUpstream(t, "one")
		shared := func(c *config.Config) {
			c.Store = config.Store{Driver: config.DriverPostgres, DSN: dsn}
			c.Apps.Dir = dir
			c.Apps.PollInterval = 50 * time.Millisecond
			c.Apps.HealthInterval = time.Hour
		}
		one, _ := testApp(t, shared)
		two, _ := testApp(t, shared)
		text := serverFile("tagged", up.URL)
		if err := os.WriteFile(filepath.Join(dir, "tagged.yaml"), []byte(text), 0o600); err != nil {
			t.Fatal(err)
		}
		deadline := time.Now().Add(10 * time.Second)
		for {
			rows, err := one.store.Drafts().BySource(context.Background(), filepath.Join(dir, "tagged.yaml"))
			if err != nil {
				t.Fatal(err)
			}
			if len(rows) > 0 || time.Now().After(deadline) {
				break
			}
			time.Sleep(50 * time.Millisecond)
		}
		time.Sleep(500 * time.Millisecond)
		rows, err := two.store.Drafts().BySource(context.Background(), filepath.Join(dir, "tagged.yaml"))
		if err != nil {
			t.Fatal(err)
		}
		if len(rows) != 1 {
			t.Fatalf("drafts of one revision on two replicas = %d, want one", len(rows))
		}
		// The two replicas share one outbox, so one read counts both.
		creates := 0
		for _, ev := range outboxOf(t, one, strconv.FormatInt(rows[0].ID, 10)) {
			if ev.data["action"] == "draft.create" {
				creates++
			}
		}
		if creates != 1 {
			t.Errorf("draft.create records = %d, want one", creates)
		}
	})
}

// TestFilePublishRecordsTheInstall pins what a file's publish writes: one
// apps.install with the draft and the publisher, and the source gitops,
// which a later install through the API sets back to api.
func TestFilePublishRecordsTheInstall(t *testing.T) {
	t.Parallel()
	f := newFileFixture(t)
	ctx := context.Background()
	f.put(t, "tagged.yaml", serverFile("tagged", f.up))
	id := strconv.FormatInt(f.publishNewest(t, f.path("tagged.yaml")), 10)
	install := f.record(t, id, "apps.install")
	if install["app"] != "tagged" || install["actor"] != "kim" || install["draft"] != id {
		t.Errorf("apps.install = %v, want tagged by kim naming draft %s", install, id)
	}
	row, err := f.app.store.Apps().GetByName(ctx, "tagged")
	if err != nil || row.Source != store.AppSourceGitops {
		t.Fatalf("tagged = %+v, %v, want source gitops", row, err)
	}
	if code := rawReq(t, http.MethodPost, f.base+"/v1/admin/apps", f.root, "application/yaml", []byte(serverFile("tagged", f.up+"/api")), nil); code != http.StatusCreated {
		t.Fatalf("install = %d", code)
	}
	if row, err = f.app.store.Apps().GetByName(ctx, "tagged"); err != nil || row.Source != store.AppSourceAPI {
		t.Errorf("tagged after an API install = %+v, %v, want source api", row, err)
	}
}

// TestFileChangeToAPausedServerStaysStopped pins that a file's change to a
// paused server proposes like any other, and its publish stores the
// manifest while the server stays stopped and paused.
func TestFileChangeToAPausedServerStaysStopped(t *testing.T) {
	t.Parallel()
	f := newFileFixture(t)
	f.put(t, "tagged.yaml", serverFile("tagged", f.up))
	f.publishNewest(t, f.path("tagged.yaml"))
	if code := adminReq(t, http.MethodPost, f.base+"/v1/admin/apps/tagged/disable", f.root, nil, nil); code != http.StatusOK {
		t.Fatalf("disable = %d", code)
	}
	f.put(t, "tagged.yaml", serverFile("tagged", f.up+"/two"))
	f.publishNewest(t, f.path("tagged.yaml"))
	row, err := f.app.store.Apps().GetByName(context.Background(), "tagged")
	if err != nil || !strings.Contains(row.Manifest, f.up+"/two") {
		t.Fatalf("tagged = %+v, %v, want the file's new address stored", row, err)
	}
	if _, running := f.app.manager.View("tagged"); running || !f.app.manager.IsPaused("tagged") {
		t.Errorf("tagged runs %v and is paused %v, want stopped and paused", running, f.app.manager.IsPaused("tagged"))
	}
}

// TestAppsListNamesTheFile pins file and file_differs on the list: the
// path of the present file that names a server, file_differs when that
// file's manifest differs from live or does not read, and neither for a
// server no file names.
func TestAppsListNamesTheFile(t *testing.T) {
	t.Parallel()
	f := newFileFixture(t)
	for _, name := range []string{"same", "other", "broken"} {
		f.put(t, name+".yaml", serverFile(name, f.up))
		f.publishNewest(t, f.path(name+".yaml"))
	}
	putServer(t, f.app, serverFile("nofile", f.up))
	f.put(t, "other.yaml", serverFile("other", f.up+"/two"))
	f.put(t, "broken.yaml", "kind: Nope\n")
	var apps []struct {
		Name        string `json:"name"`
		File        string `json:"file"`
		FileDiffers *bool  `json:"file_differs"`
	}
	if code := adminReq(t, http.MethodGet, f.base+"/v1/admin/apps", f.root, nil, &apps); code != http.StatusOK {
		t.Fatalf("list = %d", code)
	}
	want := map[string]struct {
		file    string
		differs bool
	}{
		"same": {f.path("same.yaml"), false}, "other": {f.path("other.yaml"), true},
		"broken": {f.path("broken.yaml"), true}, "nofile": {"", false},
	}
	for _, a := range apps {
		w, ok := want[a.Name]
		if !ok {
			continue
		}
		differs := a.FileDiffers != nil && *a.FileDiffers
		if a.File != w.file || differs != w.differs || (a.FileDiffers != nil && !*a.FileDiffers) {
			t.Errorf("%s answers file %q and file_differs %v, want %q and %v, omitted when false", a.Name, a.File, a.FileDiffers, w.file, w.differs)
		}
		delete(want, a.Name)
	}
	if len(want) != 0 {
		t.Errorf("the list lacks %v", want)
	}
}

// TestFileDoorBootPassLeavesServersWhoseFilesDoNotRead pins the boot pass
// against a new watcher, as a boot builds it: while the directory cannot be
// read, or any present file cannot be read, is empty or does not parse,
// no linked server is proposed for removal, whichever file names it, and
// one log line names the files. A file deleted while strazad was down is
// proposed once, the positive control.
func TestFileDoorBootPassLeavesServersWhoseFilesDoNotRead(t *testing.T) {
	t.Parallel()
	root := os.Geteuid() == 0
	cases := []struct {
		name     string
		needUser bool
		breakIt  func(t *testing.T, f *fileFixture)
	}{
		{"a file that cannot be read", true, func(t *testing.T, f *fileFixture) {
			if err := os.Chmod(f.path("a.yaml"), 0); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = os.Chmod(f.path("a.yaml"), 0o600) })
		}},
		{"a directory that cannot be read", true, func(t *testing.T, f *fileFixture) {
			if err := os.Chmod(f.dir, 0); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = os.Chmod(f.dir, 0o750) })
		}},
		{"an empty file", false, func(t *testing.T, f *fileFixture) {
			if err := os.WriteFile(f.path("a.yaml"), nil, 0o600); err != nil {
				t.Fatal(err)
			}
		}},
		{"the file with a typo", false, func(t *testing.T, f *fileFixture) {
			if err := os.WriteFile(f.path("a.yaml"), []byte("kind: Nope\n"), 0o600); err != nil {
				t.Fatal(err)
			}
		}},
		{"a moved file with a typo", false, func(t *testing.T, f *fileFixture) {
			if err := os.Rename(f.path("a.yaml"), f.path("b.yaml")); err != nil {
				t.Fatal(err)
			}
			f.app.watcher.Sweep(context.Background())
			if err := os.WriteFile(f.path("b.yaml"), []byte("kind: Nope\n"+serverFile("tagged", f.up)), 0o600); err != nil {
				t.Fatal(err)
			}
		}},
		{"a second file with a typo", false, func(t *testing.T, f *fileFixture) {
			f.put(t, "b.yaml", "# b\n"+serverFile("tagged", f.up))
			f.drop(t, "a.yaml")
			if err := os.WriteFile(f.path("b.yaml"), []byte("kind: Nope\n"), 0o600); err != nil {
				t.Fatal(err)
			}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if tc.needUser && root {
				t.Skip("root reads every file")
			}
			f := newFileFixture(t)
			f.put(t, "a.yaml", serverFile("tagged", f.up))
			f.publishNewest(t, f.path("a.yaml"))
			tc.breakIt(t, f)
			f.restart(t)
			if rm := f.removals(t); len(rm) != 0 {
				t.Errorf("the boot pass proposed %+v", rm)
			}
			if n := strings.Count(f.logs.String(), "does not read, so the removal"); tc.name != "a directory that cannot be read" && n != 1 {
				t.Errorf("log lines of the skip = %d, want one: %s", n, f.logs)
			}
		})
	}
	t.Run("a file deleted while strazad was down", func(t *testing.T) {
		t.Parallel()
		f := newFileFixture(t)
		f.put(t, "a.yaml", serverFile("tagged", f.up))
		link := f.publishNewest(t, f.path("a.yaml"))
		if err := os.Remove(f.path("a.yaml")); err != nil {
			t.Fatal(err)
		}
		f.restart(t)
		f.restart(t)
		if rm := f.removals(t); len(rm) != 1 || rm[0].SourceHash != fmt.Sprintf("remove:tagged:%d", link) {
			t.Errorf("removals after two boots = %+v, want one of link %d", rm, link)
		}
	})
}

// TestFileDoorRemovesARenamedServerWhoseFileWent pins the file door for a
// file that renamed its server and went before the rename was published:
// the server the file linked, which no present file names, is proposed for
// removal while strazad runs.
func TestFileDoorRemovesARenamedServerWhoseFileWent(t *testing.T) {
	t.Parallel()
	f := newFileFixture(t)
	f.put(t, "a.yaml", serverFile("tagged", f.up))
	link := f.publishNewest(t, f.path("a.yaml"))
	f.put(t, "a.yaml", serverFile("tagged2", f.up))
	f.drop(t, "a.yaml")
	rm := f.removals(t)
	if len(rm) != 1 || rm[0].SourceHash != fmt.Sprintf("remove:tagged:%d", link) || !reflect.DeepEqual(f.items(t, rm[0]), []string{"App/tagged/remove"}) {
		t.Errorf("removals = %+v, want tagged's removal of link %d", rm, link)
	}
}

// TestFileDoorRunsNothingBeforeAPublish pins that a new file
// starts and fetches nothing and stores no row, and a changed file restarts
// nothing, until a person publishes. Each publish then initializes the
// upstream, the positive controls.
func TestFileDoorRunsNothingBeforeAPublish(t *testing.T) {
	t.Parallel()
	f := newFileFixture(t)
	ctx := context.Background()
	up, hits := countingUpstream(t)
	f.put(t, "n.yaml", serverFile("counted", up.URL+"/one"))
	time.Sleep(time.Second)
	if len(f.bySource(t, f.path("n.yaml"))) != 1 {
		t.Fatal("the file proposed no draft")
	}
	if n := hits.Load(); n != 0 {
		t.Errorf("a new file reached the upstream %d times before a publish", n)
	}
	if _, ok := f.app.manager.View("counted"); ok {
		t.Error("a new file started the server")
	}
	if _, err := f.app.store.Apps().GetByName(ctx, "counted"); err == nil {
		t.Error("a new file stored a row")
	}
	f.publishNewest(t, f.path("n.yaml"))
	deadline := time.Now().Add(10 * time.Second)
	for {
		if v, ok := f.app.manager.View("counted"); ok && v.Status == manager.StatusRunning {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("positive control: the publish did not start the server")
		}
		time.Sleep(50 * time.Millisecond)
	}
	time.Sleep(500 * time.Millisecond)
	started := hits.Load()
	if started == 0 {
		t.Fatal("positive control: the start reached nothing")
	}
	f.put(t, "n.yaml", serverFile("counted", up.URL+"/two"))
	time.Sleep(time.Second)
	if n := hits.Load(); n != started {
		t.Errorf("a changed file reached the upstream: %d requests, then %d", started, n)
	}
	if v, _ := f.app.manager.View("counted"); !strings.HasSuffix(v.Manifest.Straza.Runtime.Remote.URL, "/one") {
		t.Errorf("a changed file changed the running server to %s", v.Manifest.Straza.Runtime.Remote.URL)
	}
	f.publishNewest(t, f.path("n.yaml"))
	deadline = time.Now().Add(10 * time.Second)
	for hits.Load() == started {
		if time.Now().After(deadline) {
			t.Fatal("positive control: the publish of the change reached nothing")
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// TestFileDoorWaivesWhatLiveStateHolds pins the waiver on the apps
// directory: a file that changes the description of a live server whose
// stored manifest holds a plain word after a flag naming a secret is
// proposed as a draft with the waiver's warning counted in its check,
// while the same file with that word one byte off, and the same manifest
// for a new server, are still refused drafts with no item.
func TestFileDoorWaivesWhatLiveStateHolds(t *testing.T) {
	t.Parallel()
	f := newFileFixture(t)
	putServer(t, f.app, waivedRunner("runner", "The runner."))
	f.put(t, "runner.yaml", waivedRunner("runner", "Changed."))
	rows := f.bySource(t, f.path("runner.yaml"))
	if len(rows) != 1 || rows[0].Refusal != "" {
		t.Fatalf("drafts of the file = %+v, want one with no refusal", rows)
	}
	if items := f.items(t, rows[0]); !reflect.DeepEqual(items, []string{"App/runner/put"}) {
		t.Errorf("items = %v, want the put of runner", items)
	}
	var counts map[string]int
	if err := json.Unmarshal([]byte(rows[0].CheckCounts), &counts); err != nil || counts["warnings"] < 1 {
		t.Errorf("check counts = %q (%v), want the waiver's warning counted", rows[0].CheckCounts, err)
	}
	for _, tc := range []struct{ name, file, text string }{
		{"the value one byte off", "runner.yaml", strings.Replace(waivedRunner("runner", "Changed."), "oauth]", "oauth2]", 1)},
		{"the same manifest for a new server", "runner2.yaml", waivedRunner("runner2", "Another.")},
	} {
		f.put(t, tc.file, tc.text)
		rows := f.bySource(t, f.path(tc.file))
		if len(rows) == 0 || !strings.Contains(rows[0].Refusal, "A draft cannot carry a secret") {
			t.Errorf("%s: drafts = %+v, want the newest refused for the secret", tc.name, rows)
			continue
		}
		if items := f.items(t, rows[0]); len(items) != 0 {
			t.Errorf("%s: items = %v, want none", tc.name, items)
		}
	}
}
