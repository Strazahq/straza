package server

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"maps"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/strazahq/straza/internal/drafts"
	"github.com/strazahq/straza/internal/manager"
	"github.com/strazahq/straza/internal/redact"
	"github.com/strazahq/straza/internal/store"
)

// fileActor is strazad's own account on the apps directory door: it
// proposes every draft of the door, and discards the ones a file replaced,
// with no person.
var fileActor = store.DraftActor{Name: "strazad", Via: "file", Client: "strazad"}

// The reasons the apps directory door closes a draft with.
const (
	fileEqualsLive = "the file now equals live"
	fileReplaced   = "a newer revision of the file replaced it"
	fileWent       = "the file is gone"
)

// removalHash begins the source hash of a removal the door proposes,
// remove:{name}:{link}, where link is the id of the published draft that
// linked the server to its file, or 0 for a link made before the upgrade.
const removalHash = "remove:"

// fileLink is a live server's link to the apps directory: the file
// whose published draft made it, or the directory for a link made before
// the upgrade, and that draft's id, 0 before the upgrade.
type fileLink struct {
	source string
	id     int64
}

// proposeFile turns a new or changed file of the apps directory into a
// draft. A file that does not read becomes a draft with no items. A
// file whose manifest equals live proposes nothing and ends the open
// drafts of its path. Otherwise one stamped draft puts the file's server,
// and removes the server its previous revision named when the file linked
// it and no present file names it, unless a draft of this revision is open,
// was published, or was discarded by a person. The file's older drafts that
// only this door wrote are discarded. Straza never writes, moves or deletes
// the file. Live state is read once, for the waiver of the file's intake
// findings and for the check of its draft.
func (a *App) proposeFile(ctx context.Context, f manager.File) error {
	ctx = noActor(ctx)
	live, err := a.liveWorld(ctx)
	if err != nil {
		return err
	}
	refusal, mf, waived := a.fileRefusal(f, live)
	name := f.Name
	if refusal == "" {
		declared := fmt.Sprintf("the file %s declares %s again", redact.Neutralize(filepath.Base(f.Path)), name)
		if err := a.closeRemovals(ctx, name, declared); err != nil {
			return err
		}
		equal, err := a.equalsLive(ctx, mf)
		if err != nil {
			return err
		}
		if equal {
			return a.closeRevisions(ctx, f.Path, "", fileEqualsLive)
		}
	}
	held, err := a.store.Drafts().BySource(ctx, f.Path)
	if err != nil {
		return err
	}
	if !slices.ContainsFunc(held, func(d store.DraftRow) bool { return d.SourceHash == f.Hash && blocks(d) }) {
		var items []drafts.Item
		if refusal == "" {
			items = []drafts.Item{{Kind: drafts.KindApp, Name: name, Op: drafts.OpPut, Doc: string(f.Raw)}}
			if remove, err := a.renamed(ctx, f); err != nil {
				return err
			} else if remove != "" {
				items = append(items, drafts.Item{Kind: drafts.KindApp, Name: remove, Op: drafts.OpRemove})
			}
		}
		row := store.DraftRow{Door: string(drafts.DoorAppsDir), Source: f.Path, SourceHash: f.Hash, Refusal: refusal}
		if err := a.storeFileDraft(ctx, live, row, items, waived); err != nil {
			return err
		}
	}
	return a.closeRevisions(ctx, f.Path, f.Hash, fileReplaced)
}

// fileRefusal answers why the file f cannot become items, "" when it can,
// with its manifest then and the warnings the waiver made of its intake
// findings over live. The secret scan of its bytes runs first, because
// the parser's sentence can quote part of a value, then the parser, the
// provider check and the whole intake of the one App put, the scan and
// the intake each waived against live (drafts.Waive). A finding keeps
// its fix on a line after its sentence, which file.refused puts after its
// own "Fix the file.", so the draft says what to do next. The text kept
// names places, never a value, and every credential shape redact knows is
// masked in it.
func (a *App) fileRefusal(f manager.File, live drafts.World) (string, manager.Manifest, []drafts.Finding) {
	name := cmp.Or(f.Name, strings.TrimSuffix(filepath.Base(f.Path), filepath.Ext(f.Path)))
	put := drafts.Item{Kind: drafts.KindApp, Name: name, Op: drafts.OpPut, Doc: string(f.Raw)}
	d := drafts.Draft{Door: drafts.DoorAppsDir, Items: []drafts.Item{put}}
	sentence, fix := "", ""
	var waived []drafts.Finding
	mf, err := manager.Parse(f.Raw)
	scan := slices.DeleteFunc(drafts.ScanSecrets(put), func(f drafts.Finding) bool { return f.Class != drafts.ClassRefused })
	switch fs, _ := waivedIntake(live, d, scan); {
	case len(fs) > 0:
		sentence, fix = fs[0].Sentence, fs[0].Fix
	case err != nil:
		sentence = err.Error()
	default:
		var fs []drafts.Finding
		if err := a.manager.CheckProvider(mf); err != nil {
			sentence = err.Error()
		} else if fs, waived = waivedIntake(live, d, intakeOf(d, principalOf(fileActor))); len(fs) > 0 {
			sentence, fix = fs[0].Sentence, fs[0].Fix
		}
	}
	if sentence == "" {
		return "", mf, waived
	}
	clean := func(s string) string { return redact.Neutralize(redact.Redact(strings.Join(strings.Fields(s), " "))) }
	if fix == "" {
		return clean(sentence), manager.Manifest{}, nil
	}
	return clean(sentence) + "\n" + clean(fix), manager.Manifest{}, nil
}

// equalsLive reports whether mf's server is live with the same manifest.
func (a *App) equalsLive(ctx context.Context, mf manager.Manifest) (bool, error) {
	live, err := a.store.Apps().GetByName(ctx, mf.Metadata.Name)
	if errors.Is(err, store.ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	doc, err := mf.JSON()
	if err != nil {
		return false, err
	}
	fileFP, err := store.FingerprintApp(doc)
	if err != nil {
		return false, err
	}
	liveFP, err := store.FingerprintApp(live.Manifest)
	return err == nil && fileFP == liveFP, err
}

// renamed answers the server f's previous revision named when f now names
// another, that server is live and linked, and no present file names it,
// so the draft removes it with the rename. Otherwise it answers "".
func (a *App) renamed(ctx context.Context, f manager.File) (string, error) {
	if f.Was == "" || f.Was == f.Name {
		return "", nil
	}
	if _, ok := a.watcher.FileFor(f.Was); ok {
		return "", nil
	}
	links, err := a.fileLinks(ctx)
	if _, linked := links[f.Was]; err != nil || !linked {
		return "", err
	}
	return f.Was, nil
}

// blocks reports whether a draft of a file revision keeps that revision
// from being proposed again: it is open or published, or a person
// discarded it. A draft this door discarded does not, so a file that comes
// back to an earlier revision is proposed again.
func blocks(d store.DraftRow) bool {
	return d.State != string(drafts.StateDiscarded) || d.DecidedBy.Via != fileActor.Via
}

// fileGone handles a file that left the apps directory: the open
// drafts of its path that only this door wrote are discarded, and every
// server the file door linked that no present file names is proposed for
// removal when it is the server the file's last revision named or its link
// is to this file, which catches a rename that went with its file.
func (a *App) fileGone(ctx context.Context, path, name string) error {
	ctx = noActor(ctx)
	if err := a.closeRevisions(ctx, path, "", fileWent); err != nil {
		return err
	}
	links, err := a.fileLinks(ctx)
	if err != nil {
		return err
	}
	for _, n := range slices.Sorted(maps.Keys(links)) {
		link := links[n]
		if n != name && link.source != path {
			continue
		}
		if _, ok := a.watcher.FileFor(n); ok {
			continue
		}
		if err := a.proposeRemoval(ctx, n, link); err != nil {
			return err
		}
	}
	return nil
}

// proposeUnfiled is the boot pass over the running watcher, which
// strazad runs once after the first sweep that read the directory.
func (a *App) proposeUnfiled(ctx context.Context) {
	a.bootRemovals(ctx, a.watcher)
}

// bootRemovals proposes the removal of every live server the file door
// linked that no present file of w names, which catches a file deleted
// while strazad was down. It proposes none while w's last sweep could not
// read the directory, or while any present file cannot be read, is empty
// or does not parse, since that file may name any server: one log line
// names those files, and a later boot proposes what is due.
func (a *App) bootRemovals(ctx context.Context, w *manager.Watcher) {
	ctx = noActor(ctx)
	files, read := w.Files()
	if !read {
		a.log.Warn("apps directory: the directory could not be read, so no removal was proposed at start. "+
			"Make it readable to strazad, then restart strazad", "dir", a.cfg.AppsDir())
		return
	}
	links, err := a.fileLinks(ctx)
	if err != nil {
		a.log.Error("apps directory: the links of the servers to their files could not be read, so no removal was proposed at start. "+
			"Restart strazad to try again once its database answers", "err", err)
		return
	}
	var due, unread []string
	for _, name := range slices.Sorted(maps.Keys(links)) {
		if _, ok := w.FileFor(name); !ok {
			due = append(due, name)
		}
	}
	for _, f := range files {
		if f.Name == "" {
			unread = append(unread, f.Path)
		}
	}
	if len(due) > 0 && len(unread) > 0 {
		a.log.Warn("apps directory: a file of the directory does not read, so the removal of a server whose file is gone is not proposed. "+
			"Fix the files named, then restart strazad", "files", unread, "apps", due)
		return
	}
	for _, name := range due {
		if err := a.proposeRemoval(ctx, name, links[name]); err != nil {
			a.log.Error("apps directory: the removal of a server whose file is gone could not be proposed. Restart strazad to try again",
				"app", name, "file", links[name].source, "err", err)
		}
	}
}

// proposeRemoval stores the removal draft of the linked server name, with
// the link's file as its source and the link in its hash, unless an open
// draft of this door removes the server already or a draft of that hash
// blocks it.
func (a *App) proposeRemoval(ctx context.Context, name string, link fileLink) error {
	open, err := a.openFileDrafts(ctx, name)
	if err != nil {
		return err
	}
	ids := make([]int64, len(open))
	for i, d := range open {
		ids[i] = d.ID
	}
	items, err := a.store.Drafts().Items(ctx, ids)
	if err != nil {
		return err
	}
	for _, rows := range items {
		if slices.ContainsFunc(rows, func(it store.DraftItemRow) bool {
			return it.Kind == string(drafts.KindApp) && it.Name == name && it.Op == string(drafts.OpRemove)
		}) {
			return nil
		}
	}
	hash := removalHash + name + ":" + strconv.FormatInt(link.id, 10)
	held, err := a.store.Drafts().BySource(ctx, link.source)
	if err != nil {
		return err
	}
	if slices.ContainsFunc(held, func(d store.DraftRow) bool { return d.SourceHash == hash && blocks(d) }) {
		return nil
	}
	live, err := a.liveWorld(ctx)
	if err != nil {
		return err
	}
	return a.storeFileDraft(ctx, live, store.DraftRow{Door: string(drafts.DoorAppsDir), Source: link.source, SourceHash: hash},
		[]drafts.Item{{Kind: drafts.KindApp, Name: name, Op: drafts.OpRemove}}, nil)
}

// fileLinks answers the link of every live server the file door linked,
// read from the store. A published put of a server from a file links
// it to that file. A published removal of it through any door, and a
// person's discard of a removal this door proposed, end the link. A draft
// this door discarded and a discarded put move nothing. A live server no
// such decision names is linked when its row says gitops, the door of an
// install made before the upgrade.
func (a *App) fileLinks(ctx context.Context) (map[string]fileLink, error) {
	rows, err := a.store.Apps().List(ctx)
	if err != nil {
		return nil, err
	}
	decisions, err := a.store.Drafts().FileDecisions(ctx)
	if err != nil {
		return nil, err
	}
	decided := map[string]bool{}
	linked := map[string]fileLink{}
	for _, d := range decisions {
		for _, it := range d.Items {
			switch {
			case d.State == string(drafts.StatePublished) && it.Op == string(drafts.OpPut) && d.Door == string(drafts.DoorAppsDir):
				linked[it.Name] = fileLink{source: d.Source, id: d.DraftID}
			case it.Op == string(drafts.OpRemove) && (d.State == string(drafts.StatePublished) || d.DecidedBy.Via != fileActor.Via):
				delete(linked, it.Name)
			default:
				continue
			}
			decided[it.Name] = true
		}
	}
	dir, err := filepath.Abs(a.cfg.AppsDir())
	if err != nil {
		dir = a.cfg.AppsDir()
	}
	out := map[string]fileLink{}
	for _, row := range rows {
		if link, ok := linked[row.Name]; ok {
			out[row.Name] = link
		} else if !decided[row.Name] && row.Source == store.AppSourceGitops {
			out[row.Name] = fileLink{source: dir}
		}
	}
	return out, nil
}

// storeFileDraft checks a draft of the apps directory against live, which
// the caller read, with the waiver's warnings waived joining its verdict,
// stores it as its revision 1, proposed by strazad, and writes its
// draft.create, with the file and its hash and no actor, and its
// draft.check. A draft of the same file and hash that another replica
// stored first is not an error.
func (a *App) storeFileDraft(ctx context.Context, live drafts.World, row store.DraftRow, items []drafts.Item, waived []drafts.Finding) error {
	d := drafts.Draft{Revision: 1, State: drafts.StateOpen, Door: drafts.DoorAppsDir, Source: row.Source, Refusal: row.Refusal,
		Authors: []drafts.Principal{principalOf(fileActor)}, Items: items}
	world, in, err := a.worldFor(ctx, live, d)
	if err != nil {
		return err
	}
	cd, err := a.checkRows(ctx, world, in, d, rowsOf(items))
	if err != nil {
		return err
	}
	cd.v = waiveVerdict(cd.w, cd.d, cd.v, waived)
	check := stampOf(cd.d, cd.v, cd.in.Now)
	row.CheckedRevision, row.CheckedAt, row.CheckedSnapshot, row.CheckCounts = 1, &check.At, check.Snapshot, check.Counts
	created, err := a.store.Drafts().Create(ctx, row, cd.rows, store.DraftRevisionRow{Author: fileActor, Digest: revisionDigest(cd.d.Items)})
	if errors.Is(err, store.ErrConflict) {
		return nil
	}
	if err != nil {
		return err
	}
	id := strconv.FormatInt(created.ID, 10)
	cd.v.Draft, cd.v.Revision = id, 1
	data := revisionData("draft.create", id, 1, drafts.DoorAppsDir, cd.d.Items)
	data["source"], data["sourceHash"] = row.Source, row.SourceHash
	a.emitEventCtx(context.WithoutCancel(ctx), "straza.audit.admin", data)
	a.recordCheck(ctx, id, cd.v)
	cd.d.ID = id
	a.log.Info("apps directory: proposed a draft, which a person publishes or discards", "draft", id, "title", drafts.Title(cd.d), "file", row.Source)
	return nil
}

// closeRevisions discards the open drafts of the file path that only this
// door wrote, its removals left out, with reason: every one, or with hash
// set, every one of another revision. A draft a person revised stays open.
func (a *App) closeRevisions(ctx context.Context, path, hash, reason string) error {
	held, err := a.store.Drafts().BySource(ctx, path)
	if err != nil {
		return err
	}
	for _, d := range held {
		if d.State != string(drafts.StateOpen) || d.Door != string(drafts.DoorAppsDir) || d.Revision != 1 ||
			d.SourceHash == hash || strings.HasPrefix(d.SourceHash, removalHash) {
			continue
		}
		if err := a.closeFileDraft(ctx, d, reason); err != nil {
			return err
		}
	}
	return nil
}

// closeRemovals discards the open removals of the server name that this
// door proposed, because a file names the server again.
func (a *App) closeRemovals(ctx context.Context, name, reason string) error {
	open, err := a.openFileDrafts(ctx, name)
	if err != nil {
		return err
	}
	for _, d := range open {
		if d.Revision == 1 && strings.HasPrefix(d.SourceHash, removalHash+name+":") {
			if err := a.closeFileDraft(ctx, d, reason); err != nil {
				return err
			}
		}
	}
	return nil
}

// openFileDrafts answers the open drafts of this door whose items name the
// server name.
func (a *App) openFileDrafts(ctx context.Context, name string) ([]store.DraftRow, error) {
	return a.store.Drafts().List(ctx, store.DraftFilter{State: string(drafts.StateOpen), Door: string(drafts.DoorAppsDir),
		Object: store.ObjectRef{Kind: string(drafts.KindApp), Name: name}}, 0, 200)
}

// closeFileDraft discards d as this door with reason and, when this call
// ended it, writes its draft.discard with the file, its hash and no actor.
func (a *App) closeFileDraft(ctx context.Context, d store.DraftRow, reason string) error {
	closed, err := a.store.Drafts().Close(ctx, d.ID, d.Revision, string(drafts.StateDiscarded), fileActor, reason, time.Now())
	if err != nil || !closed {
		return err
	}
	a.emitEventCtx(context.WithoutCancel(ctx), "straza.audit.admin",
		withFile(discardData(strconv.FormatInt(d.ID, 10), d.Revision, reason), d, nil))
	return nil
}

// withFile completes the draft.discard data of d, a draft of the apps
// directory, with its file and hash and, when it removes a server, that
// server under unlinked: a discarded removal leaves the server in place and
// no longer linked to its file. Another door's draft is left as it
// is.
func withFile(data map[string]any, d store.DraftRow, items []store.DraftItemRow) map[string]any {
	if d.Door != string(drafts.DoorAppsDir) {
		return data
	}
	data["source"], data["sourceHash"] = d.Source, d.SourceHash
	for _, it := range items {
		if it.Kind == string(drafts.KindApp) && it.Op == string(drafts.OpRemove) {
			data["unlinked"] = it.Name
			break
		}
	}
	return data
}

// filePath answers the path of the present apps directory file that names
// the server name, "" when none does.
func (a *App) filePath(name string) string {
	if a.watcher == nil {
		return ""
	}
	f, _ := a.watcher.FileFor(name)
	return f.Path
}

// fileOf answers the present apps directory file that names the server of
// row, and whether that file's manifest differs from the live one or does
// not read.
func (a *App) fileOf(row store.App) (string, bool) {
	if a.watcher == nil {
		return "", false
	}
	f, ok := a.watcher.FileFor(row.Name)
	if !ok {
		return "", false
	}
	mf, err := manager.Parse(f.Raw)
	if err != nil {
		return f.Path, true
	}
	doc, err := mf.JSON()
	if err != nil {
		return f.Path, true
	}
	fileFP, err := store.FingerprintApp(doc)
	if err != nil {
		return f.Path, true
	}
	liveFP, err := store.FingerprintApp(row.Manifest)
	return f.Path, err != nil || fileFP != liveFP
}
