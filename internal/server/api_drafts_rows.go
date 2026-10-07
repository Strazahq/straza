package server

import (
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/strazahq/straza/internal/drafts"
	"github.com/strazahq/straza/internal/manager"
	"github.com/strazahq/straza/internal/redact"
	"github.com/strazahq/straza/internal/store"
)

// bodyItems reads the documents of b, then its items form, into items,
// and answers the bundle refusals of the documents.
func bodyItems(b draftBody) ([]drafts.Item, []drafts.Finding) {
	items, fs := drafts.ParseBundle(b.Documents)
	for _, it := range b.Items {
		items = append(items, drafts.Item{Kind: it.Kind, Name: it.Name, Op: it.Op, Doc: it.Doc})
	}
	return items, fs
}

// intakeOf answers the refusals d meets before it is stored when author
// writes its revision: Intake's, then the manifest parser's for every App
// put Intake did not refuse already, a refusal the waiver may lift not
// counting, so a manifest the parser rejects never reaches the check
// behind a waived finding. An item whose name the scan withholds counts as
// refused, because the parser's words would quote the name.
func intakeOf(d drafts.Draft, author drafts.Principal) []drafts.Finding {
	fs := drafts.Intake(d, author)
	refused := map[string]bool{}
	for _, f := range fs {
		if !drafts.Waivable([]drafts.Finding{f}) {
			refused[f.Object] = true
		}
	}
	for _, it := range d.Items {
		if drafts.NameWithheld(it) {
			refused[it.Object()] = true
		}
	}
	for _, it := range d.Items {
		if it.Kind != drafts.KindApp || it.Op != drafts.OpPut || refused[it.Object()] {
			continue
		}
		if _, err := manager.Parse([]byte(it.Doc)); err != nil {
			fs = append(fs, drafts.AppParse(it.Name, err))
		}
	}
	return fs
}

// bodyIntake answers the refusals of d, read from a body whose documents
// drew the bundle refusals bundle: those, then intakeOf's. draft.empty
// stays out beside a bundle refusal, because the body sent a document.
func bodyIntake(d drafts.Draft, author drafts.Principal, bundle []drafts.Finding) []drafts.Finding {
	fs := intakeOf(d, author)
	if len(bundle) > 0 {
		fs = slices.DeleteFunc(fs, func(f drafts.Finding) bool { return f.Code == "draft.empty" })
	}
	return append(bundle, fs...)
}

// waivedIntake answers the intake findings fs of d over w, the World the
// door read for its check, as the door answers them: the refusals left
// after drafts.Waive, which refuse the draft, and the warnings it made of
// the refusals live state already holds, which join the verdict.
func waivedIntake(w drafts.World, d drafts.Draft, fs []drafts.Finding) (refused, warnings []drafts.Finding) {
	for _, f := range drafts.Waive(w, d, fs) {
		if f.Class == drafts.ClassRefused {
			refused = append(refused, f)
		} else {
			warnings = append(warnings, f)
		}
	}
	return refused, warnings
}

// waiveVerdict answers v, the check's verdict of d over w, with the waiver
// run over its refusals too, because the check's agent rules make the name
// findings again that intake made: a refusal live state holds already is
// downgraded as intake's was (drafts.Waive), and the warnings made there
// and the intake warnings waived join v's warnings, each once.
func waiveVerdict(w drafts.World, d drafts.Draft, v drafts.Verdict, waived []drafts.Finding) drafts.Verdict {
	refused, again := waivedIntake(w, d, v.Refused)
	v.Refused = append([]drafts.Finding{}, refused...)
	for _, f := range append(again, waived...) {
		if !slices.Contains(v.Warnings, f) {
			v.Warnings = append(v.Warnings, f)
		}
	}
	v.Warnings = sortFindings(v.Warnings)
	return v
}

// readerView answers w as the caller c may read it for the waiver over d,
// with the read refusal c would get for each live server or role of d it
// may not read today, by object. Such an object leaves the view, so no
// finding of it is waived whatever live state holds, and the answer tells
// c nothing its read routes would not show. The rule is
// readRefusal's, so a door that refused the read first sees no change.
func readerView(c draftCaller, w drafts.World, d drafts.Draft) (drafts.World, map[string]string) {
	unread := map[string]string{}
	for _, it := range d.Items {
		_, app := w.Apps[it.Name]
		_, role := w.Roles[it.Name]
		if it.Kind == drafts.KindApp && app || it.Kind == drafts.KindRole && role {
			if msg := c.readRefusal(drafts.Draft{Items: []drafts.Item{it}}, w); msg != "" {
				unread[it.Object()] = msg
			}
		}
	}
	if len(unread) == 0 {
		return w, nil
	}
	view := w
	view.Apps, view.Roles = maps.Clone(w.Apps), maps.Clone(w.Roles)
	for object := range unread {
		kind, name, _ := strings.Cut(object, "/")
		if kind == string(drafts.KindApp) {
			delete(view.Apps, name)
		} else {
			delete(view.Roles, name)
		}
	}
	return view, unread
}

// readerWords answers fs with every refusal the waiver could have lifted
// of an item unread names saying that only a reader of the object makes
// that change, then the read refusal itself. The name finding of a
// document carries no object, so it is matched to its item by the
// document number intake wrote, the bundle's for an item read from one,
// whatever the name's fault is.
func readerWords(d drafts.Draft, fs []drafts.Finding, unread map[string]string) []drafts.Finding {
	if len(unread) == 0 {
		return fs
	}
	out := slices.Clone(fs)
	for i, f := range out {
		object := f.Object
		for n, it := range d.Items {
			if object == "" && strings.HasPrefix(f.Sentence, fmt.Sprintf("The name of document %d ", cmp.Or(it.Number, n+1))) {
				object = it.Object()
			}
		}
		msg, ok := unread[object]
		if !ok || !drafts.Waivable([]drafts.Finding{f}) {
			continue
		}
		kind, _, _ := strings.Cut(object, "/")
		word := "role"
		if kind == string(drafts.KindApp) {
			word = "server"
		}
		reader := fmt.Sprintf("Only someone who can read that %s can change it under that name, because only a reader of the %s may learn whether the stored name already holds that character.", word, word)
		switch f.Code {
		case "secret.value":
			reader = "Only someone who can read that server can send that value, because only a reader of the server may learn whether its stored manifest already holds it."
		case "bundle.masked":
			reader = "Only someone who can read that server can send back a value Straza masked, because only a reader of the server may learn whether its stored manifest holds a value there."
		}
		out[i].Fix = reader + " " + msg
	}
	return out
}

// readerWaived is waivedIntake for the caller c: a finding of a live
// object c may not read is never waived and says that only a reader makes
// that change, and the rest meet the waiver, a value's only for a caller
// valueView lets it read.
func (c draftCaller) readerWaived(w drafts.World, d drafts.Draft, fs []drafts.Finding) (refused, warnings []drafts.Finding) {
	view, unread := readerView(c, w, d)
	refused, warnings = waivedIntake(c.valueView(view), d, fs)
	refused, kept := maskWaived(view, d, refused)
	return readerWords(d, refused, unread), append(warnings, kept...)
}

// valueView is view for the waiver of a value (drafts.Waive): as it is for
// a caller who may change every server, and with no stored manifest for
// anyone else. Every route masks the values the waiver compares, so for a
// caller short of apps:write a value sent in clear draws the scan's
// refusal whether it matches the stored one or not, and no answer tells
// that caller whether a guess was right. The name waiver reads the view's
// servers and still holds.
func (c draftCaller) valueView(view drafts.World) drafts.World {
	if c.standings(view).apps.Full {
		return view
	}
	out := view
	out.Apps = make(map[string]drafts.App, len(view.Apps))
	for name, app := range view.Apps {
		app.Manifest = ""
		out.Apps[name] = app
	}
	return out
}

// readerVerdict is waiveVerdict for the caller c, under readerWaived's
// rule over the check's refusals.
func (c draftCaller) readerVerdict(w drafts.World, d drafts.Draft, v drafts.Verdict, waived []drafts.Finding) drafts.Verdict {
	view, unread := readerView(c, w, d)
	v = waiveVerdict(c.valueView(view), d, v, waived)
	refused, kept := maskWaived(view, d, v.Refused)
	v.Refused, v.Warnings = readerWords(d, refused, unread), sortFindings(append(v.Warnings, kept...))
	return v
}

// maskWaived answers the refusals fs of d over the view w with every
// bundle.masked refusal of an App put of a live server judged against the
// stored manifest w holds (keptMasks): one whose every mask stands where
// live still masks a value is downgraded to the warning that the publish
// keeps the stored value, and one with a mask that does not fit, or with
// an anchor, an alias or a merge key that could copy a restored value
// elsewhere, stays refused, intake's sentence naming that place or that
// cause in place of the document's first mask, and a door's own sentence,
// the undo's, kept as it is. A server w does not hold, an object the
// caller may not read among them, and a server whose name the scan
// withholds are left as intake wrote them, so the answer tells a caller
// nothing its read routes would not show and quotes no withheld name.
func maskWaived(w drafts.World, d drafts.Draft, fs []drafts.Finding) (refused, warnings []drafts.Finding) {
	for _, f := range fs {
		it, live, ok := maskedItem(w, d, f)
		if !ok {
			refused = append(refused, f)
			continue
		}
		restored, at := keptMasks(it.Doc, live.Manifest)
		intakes := strings.HasPrefix(f.Sentence, f.Object+" holds a value that Straza masked for display at ")
		switch {
		case restored != "":
			warnings = append(warnings, drafts.Finding{Code: f.Code, Class: drafts.ClassWarning, Object: f.Object, Document: f.Document,
				Sentence: redact.Neutralize(fmt.Sprintf("%s holds a value that Straza masked for display at %s, and the server's stored manifest already holds a value there, so the publish keeps the stored value.", f.Object, at)),
				Fix:      "Nothing needs to change. To change that value, write the new value in its place."})
			continue
		case !intakes:
		case holdsAnchor(it.Doc):
			f.Sentence = redact.Neutralize(fmt.Sprintf("%s holds a value that Straza masked for display at %s, and an anchor, an alias or a merge key in the document could copy the stored value to another place, so the mask is not waived.", f.Object, at))
			f.Fix = "Write the document without anchors, aliases and merge keys, and send the draft again."
		default:
			f.Sentence = redact.Neutralize(fmt.Sprintf("%s holds a value that Straza masked for display at %s, and the stored manifest as Straza answers it holds no mask there, so publishing it would store the mask in place of the value.", f.Object, at))
		}
		refused = append(refused, f)
	}
	return refused, warnings
}

// maskedItem answers the App put of d that the bundle.masked finding f
// names and the live server w holds for it, and reports false for any
// other finding, a server w does not hold, and a server whose name the
// scan withholds.
func maskedItem(w drafts.World, d drafts.Draft, f drafts.Finding) (drafts.Item, drafts.App, bool) {
	if f.Code != "bundle.masked" {
		return drafts.Item{}, drafts.App{}, false
	}
	for _, it := range d.Items {
		live, ok := w.Apps[it.Name]
		if it.Kind == drafts.KindApp && it.Op == drafts.OpPut && it.Object() == f.Object && ok && !drafts.NameWithheld(it) {
			return it, live, true
		}
	}
	return drafts.Item{}, drafts.App{}, false
}

// certain is the findings of fs that no read of live state can waive.
func certain(fs []drafts.Finding) []drafts.Finding {
	return slices.DeleteFunc(slices.Clone(fs), func(f drafts.Finding) bool { return drafts.Waivable([]drafts.Finding{f}) })
}

// certainFirst is fs with the findings Waive never downgrades first, then
// the rest, each part in intake's order, so a refusal answered before live
// state is read leads with one no read could lift.
func certainFirst(fs []drafts.Finding) []drafts.Finding {
	out := certain(fs)
	for _, f := range fs {
		if drafts.Waivable([]drafts.Finding{f}) {
			out = append(out, f)
		}
	}
	return out
}

// refuseCertain answers the 422 of the intake findings fs when one of them
// is a refusal no read of live state can waive, leading with it, and
// reports whether it did. A door asks before it reads live state, so a
// draft refused for its size or its shape never reads it, and the findings
// Waive may downgrade wait for the World the check reads.
func refuseCertain(w http.ResponseWriter, fs []drafts.Finding) bool {
	if drafts.Waivable(fs) {
		return false
	}
	refuseIntake(w, certainFirst(fs))
	return true
}

// refusedObjects answers the objects the findings fs name, which a check
// leaves out of its stamp and its rules.
func refusedObjects(fs []drafts.Finding) map[string]bool {
	out := map[string]bool{}
	for _, f := range fs {
		out[f.Object] = true
	}
	return out
}

// refusedItems is refusedObjects over fs with the object of every item of
// d whose name the scan withholds, which no finding names, so the item
// stays out of the check, whose reading of its manifest would log and
// quote the name.
func refusedItems(d drafts.Draft, fs []drafts.Finding) map[string]bool {
	out := refusedObjects(fs)
	for _, it := range d.Items {
		if drafts.NameWithheld(it) {
			out[it.Object()] = true
		}
	}
	return out
}

// checkedRows is rows without the objects refused names.
func checkedRows(rows []store.DraftItemRow, refused map[string]bool) []store.DraftItemRow {
	var out []store.DraftItemRow
	for _, row := range rows {
		if !refused[row.Kind+"/"+row.Name] {
			out = append(out, row)
		}
	}
	return out
}

// refuseIntake answers the 422 of the intake refusals fs: the first
// finding's sentence and fix, and every finding keyed.
func refuseIntake(w http.ResponseWriter, fs []drafts.Finding) {
	msg := fs[0].Sentence
	if fs[0].Fix != "" {
		msg += " " + fs[0].Fix
	}
	writeJSON(w, http.StatusUnprocessableEntity, map[string]any{"error": msg, "findings": keyed(fs, nil, nil)})
}

// intakeVerdict is the verdict of a draft that intake refused before live
// state was read: those refusals and nothing else.
func intakeVerdict(fs []drafts.Finding) drafts.Verdict {
	return drafts.Verdict{CheckedAt: time.Now().UTC().Format(time.RFC3339), Refused: sortFindings(fs), Risks: []drafts.Finding{},
		Warnings: []drafts.Finding{}, Unchecked: []drafts.Finding{}, Passed: []drafts.Finding{}, Info: []drafts.Finding{},
		Gains: []drafts.Gain{}, Needs: []drafts.Need{}}
}

// sortFindings sorts fs by object, then code, as Check sorts every list.
func sortFindings(fs []drafts.Finding) []drafts.Finding {
	sort.SliceStable(fs, func(i, j int) bool {
		if fs[i].Object != fs[j].Object {
			return fs[i].Object < fs[j].Object
		}
		return fs[i].Code < fs[j].Code
	})
	return fs
}

// canonicalRoles writes the document of every Role put of rows in the
// export's form, as a bundle's Role documents are, leaving out the objects
// refused names. A document that does not read is intake's to refuse.
func canonicalRoles(rows []store.DraftItemRow, refused map[string]bool) {
	for i, row := range rows {
		if row.Kind != string(drafts.KindRole) || row.Op != string(drafts.OpPut) || refused[row.Kind+"/"+row.Name] {
			continue
		}
		doc, err := drafts.ParseRole(row.Doc)
		if err != nil {
			continue
		}
		if text, err := doc.Marshal(); err == nil {
			rows[i].Doc = string(text)
		}
	}
}

// rowsOf is items as rows no stamp has touched.
func rowsOf(items []drafts.Item) []store.DraftItemRow {
	out := make([]store.DraftItemRow, len(items))
	for i, it := range items {
		out[i] = store.DraftItemRow{Kind: string(it.Kind), Name: it.Name, Op: string(it.Op), Doc: it.Doc}
	}
	return out
}

// itemsOf is rows as the items Check reads.
func itemsOf(rows []store.DraftItemRow) []drafts.Item {
	out := make([]drafts.Item, len(rows))
	for i, row := range rows {
		out[i] = drafts.Item{Kind: drafts.Kind(row.Kind), Name: row.Name, Op: drafts.Op(row.Op), Doc: row.Doc, Base: drafts.Fingerprint(row.Base)}
	}
	return out
}

// keepHeld is items as the rows of a new revision: an item that names an
// object held names keeps that object's stamp and Offered, and every
// other item waits for its stamp.
func keepHeld(held []store.DraftItemRow, items []drafts.Item) []store.DraftItemRow {
	byObject := make(map[string]store.DraftItemRow, len(held))
	for _, row := range held {
		byObject[row.Kind+"/"+row.Name] = row
	}
	out := rowsOf(items)
	for i := range out {
		if old, ok := byObject[out[i].Kind+"/"+out[i].Name]; ok && old.BaseOp != "" {
			out[i].Base, out[i].BaseOp, out[i].BaseDoc, out[i].Offered = old.Base, old.BaseOp, old.BaseDoc, old.Offered
		}
	}
	return out
}

// mergeItems lays items over the rows held: an item that names a held
// object replaces that row's op and document and keeps its stamp, and every
// other item joins at the end, a second item for one object included, so
// intake still refuses the object named twice.
func mergeItems(held []store.DraftItemRow, items []drafts.Item) []store.DraftItemRow {
	out := slices.Clone(held)
	at := make(map[string]int, len(held))
	for i, row := range held {
		at[row.Kind+"/"+row.Name] = i
	}
	for _, it := range items {
		if i, ok := at[it.Object()]; ok {
			out[i].Op, out[i].Doc = string(it.Op), it.Doc
			delete(at, it.Object())
			continue
		}
		out = append(out, store.DraftItemRow{Kind: string(it.Kind), Name: it.Name, Op: string(it.Op), Doc: it.Doc})
	}
	return out
}

// draftOf is the stored draft row with rows as its items and authors as its
// authors, as Check reads it.
func draftOf(row store.DraftRow, rows []store.DraftItemRow, authors []drafts.Principal) drafts.Draft {
	d := drafts.Draft{ID: strconv.FormatInt(row.ID, 10), Revision: row.Revision, State: drafts.State(row.State), Door: drafts.Door(row.Door),
		Source: row.Source, Refusal: row.Refusal, Note: row.Note, Authors: authors, Items: itemsOf(rows)}
	if row.Reverts != 0 {
		d.Reverts = strconv.FormatInt(row.Reverts, 10)
	}
	return d
}

// authorsOf lists the principal of every revision once, oldest first, so
// the proposer, the author of revision 1, comes first. A user and an admin
// API token never count as one, whatever their ids.
func authorsOf(revs []store.DraftRevisionRow) []drafts.Principal {
	out := []drafts.Principal{}
	for _, rev := range revs {
		out = withAuthor(out, principalOf(rev.Author))
	}
	return out
}

// withAuthor is authors with p at the end, unless p wrote a revision already.
func withAuthor(authors []drafts.Principal, p drafts.Principal) []drafts.Principal {
	for _, au := range authors {
		if au.UserID == p.UserID && (au.Via == laneAdminAPI) == (p.Via == laneAdminAPI) {
			return authors
		}
	}
	return append(authors, p)
}

// principalOf is an actor the store recorded, as a draft names its author.
func principalOf(a store.DraftActor) drafts.Principal {
	return drafts.Principal{UserID: a.ID, Username: a.Name, Agent: a.Agent, Via: a.Via, Client: a.Client, SponsorID: a.SponsorID, SponsorName: a.SponsorName}
}

// decidedBy is who decided a draft, or nil for an expiry, which names no
// one.
func decidedBy(a store.DraftActor) *drafts.Principal {
	if a.ID == "" && a.Name == "" {
		return nil
	}
	p := principalOf(a)
	return &p
}

// slotWords reads a draft's slot: a working draft, or the name of the set
// whose saved edit it holds.
func slotWords(slot string) (working bool, policyEdit string) {
	kind, name, _ := strings.Cut(slot, ":")
	switch kind {
	case "working":
		return true, ""
	case "policy":
		return false, name
	}
	return false, ""
}

// offered is an item's Offered column: the tool names a person's Contact
// read, the hex sha256 of the document they were read for, and when.
type offered struct {
	Digest string   `json:"digest"`
	Tools  []string `json:"tools"`
	At     string   `json:"at"`
}

// contactedOf answers, by Kind/Name, what the Offered column of each row
// holds for the row's current document. An entry read for another
// document, and one that does not decode, count for nothing.
func contactedOf(rows []store.DraftItemRow) map[string]offered {
	out := map[string]offered{}
	for _, row := range rows {
		var o offered
		if row.Offered == "" || json.Unmarshal([]byte(row.Offered), &o) != nil {
			continue
		}
		if sum := sha256.Sum256([]byte(row.Doc)); o.Digest == hex.EncodeToString(sum[:]) {
			out[row.Kind+"/"+row.Name] = o
		}
	}
	return out
}

// actorOf is a principal as the store records the author of a revision.
func actorOf(p drafts.Principal) store.DraftActor {
	return store.DraftActor{ID: p.UserID, Name: p.Username, Via: p.Via, Client: p.Client, Agent: p.Agent, SponsorID: p.SponsorID, SponsorName: p.SponsorName}
}

// noteOf is the note a request leaves on a draft: the one it sent, empty
// included, or kept when it sent none.
func noteOf(sent *string, kept string) string {
	if sent != nil {
		return *sent
	}
	return kept
}

// liveObjects answers, by Kind/Name, whether the object of each row no
// stamp reached exists in w, the World the route read: a server or a role
// w holds, or a policy set stored on or off, read by name, because w may
// hold only the sets that are on.
func (a *App) liveObjects(ctx context.Context, w drafts.World, rows []store.DraftItemRow) (map[string]bool, error) {
	out := map[string]bool{}
	for _, row := range rows {
		if row.BaseOp != "" {
			continue
		}
		object := row.Kind + "/" + row.Name
		switch drafts.Kind(row.Kind) {
		case drafts.KindApp:
			_, out[object] = w.Apps[row.Name]
		case drafts.KindRole:
			_, out[object] = w.Roles[row.Name]
		case drafts.KindPolicySet:
			_, err := a.store.Policies().GetByName(ctx, row.Name)
			if err != nil && !errors.Is(err, store.ErrNotFound) {
				return nil, fmt.Errorf("the policy set %s cannot be read: %w", row.Name, err)
			}
			out[object] = err == nil
		}
	}
	return out, nil
}

// markExisted sets existed on each item whose row, at the same index, no
// stamp reached, from live as liveObjects answered it.
func markExisted(items []itemPayload, rows []store.DraftItemRow, live map[string]bool) {
	for i, row := range rows {
		if row.BaseOp == "" {
			items[i].Existed = live[row.Kind+"/"+row.Name]
		}
	}
}
