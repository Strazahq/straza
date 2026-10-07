package server

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/strazahq/straza/internal/drafts"
	"github.com/strazahq/straza/internal/store"
)

// draftListMax is the most drafts one page of the list holds.
const draftListMax = 200

// draftScanMax is the most store rows one request of the list reads.
// It is a variable only so that a test can lower it.
var draftScanMax = 1000

// draftPayload is a draft as a route answers it. A field with no
// value is left out.
type draftPayload struct {
	ID            string             `json:"id"`
	Revision      int                `json:"revision"`
	State         string             `json:"state"`
	Door          string             `json:"door"`
	Source        string             `json:"source,omitempty"`
	Note          string             `json:"note,omitempty"`
	Authors       []drafts.Principal `json:"authors"`
	Items         []itemPayload      `json:"items"`
	Reverts       string             `json:"reverts,omitempty"`
	Title         string             `json:"title"`
	Working       bool               `json:"working,omitempty"`
	PolicyEdit    string             `json:"policy_edit,omitempty"`
	Refusal       string             `json:"refusal,omitempty"`
	CreatedAt     string             `json:"created_at"`
	UpdatedAt     string             `json:"updated_at"`
	ExpiresAt     string             `json:"expires_at,omitempty"`
	DecidedAt     string             `json:"decided_at,omitempty"`
	DecidedBy     *drafts.Principal  `json:"decided_by,omitempty"`
	DecidedReason string             `json:"decided_reason,omitempty"`
	Snapshot      string             `json:"snapshot,omitempty"`
}

// itemPayload is an item as a route answers it: an App document masked,
// the base only for a reader of the item's object, whether the object
// existed at the draft's last check for every reader, and, for a reader
// who may not read the object, its kind, name and op alone with Withheld
// saying why.
type itemPayload struct {
	Kind     string `json:"kind"`
	Name     string `json:"name"`
	Op       string `json:"op"`
	Doc      string `json:"doc,omitempty"`
	Base     string `json:"base,omitempty"`
	Withheld string `json:"withheld,omitempty"`
	Existed  bool   `json:"existed"`
}

// summaryItem is an item as a list row names it.
type summaryItem struct {
	Kind string `json:"kind"`
	Name string `json:"name"`
	Op   string `json:"op"`
}

// summaryPayload is a row of the drafts list. Checks holds the
// counts of the last check of an open draft the server has checked.
type summaryPayload struct {
	ID         string            `json:"id"`
	Title      string            `json:"title"`
	State      string            `json:"state"`
	Door       string            `json:"door"`
	Source     string            `json:"source,omitempty"`
	Revision   int               `json:"revision"`
	Proposer   drafts.Principal  `json:"proposer"`
	Items      []summaryItem     `json:"items"`
	Checks     *checksPayload    `json:"checks,omitempty"`
	Working    bool              `json:"working,omitempty"`
	PolicyEdit string            `json:"policy_edit,omitempty"`
	CreatedAt  string            `json:"created_at"`
	UpdatedAt  string            `json:"updated_at"`
	DecidedAt  string            `json:"decided_at,omitempty"`
	DecidedBy  *drafts.Principal `json:"decided_by,omitempty"`
}

// checksPayload is the counts a stamp stored, the revision they belong to,
// and when the check ran.
type checksPayload struct {
	Refused   int    `json:"refused"`
	Risks     int    `json:"risks"`
	Warnings  int    `json:"warnings"`
	Unchecked int    `json:"unchecked"`
	Revision  int    `json:"revision"`
	CheckedAt string `json:"checked_at"`
}

// draftDetail is the answer of GET /v1/admin/drafts/{id}. Checks
// is set only on a draft that is not open.
type draftDetail struct {
	Draft          draftPayload              `json:"draft"`
	Verdict        verdictPayload            `json:"verdict"`
	Revisions      []revisionPayload         `json:"revisions"`
	Live           map[string]liveEntry      `json:"live"`
	Contacted      map[string]contactedEntry `json:"contacted,omitempty"`
	Changes        []changePayload           `json:"changes,omitempty"`
	Checks         *checksPayload            `json:"checks,omitempty"`
	MayPublish     bool                      `json:"may_publish"`
	PublishRefusal string                    `json:"publish_refusal,omitempty"`
}

// revisionPayload is who wrote one revision, through which door, and the
// digest of its items.
type revisionPayload struct {
	Revision   int              `json:"revision"`
	Author     drafts.Principal `json:"author"`
	Door       string           `json:"door"`
	Digest     string           `json:"digest"`
	Mechanical bool             `json:"mechanical,omitempty"`
	CreatedAt  string           `json:"created_at"`
}

// liveEntry is the live state of one object in the words of an item's op,
// with its canonical live document.
type liveEntry struct {
	Op  string `json:"op"`
	Doc string `json:"doc"`
}

// contactedEntry is what a person's Contact read for an App item's current
// document.
type contactedEntry struct {
	At    string   `json:"at"`
	Tools []string `json:"tools"`
}

// changePayload is the published before and after of one object.
type changePayload struct {
	Kind      string `json:"kind"`
	Name      string `json:"name"`
	Implied   bool   `json:"implied"`
	BeforeOp  string `json:"before_op"`
	BeforeDoc string `json:"before_doc"`
	BeforeFP  string `json:"before_fp"`
	AfterOp   string `json:"after_op"`
	AfterDoc  string `json:"after_doc"`
	AfterFP   string `json:"after_fp"`
}

// handleDraftsList answers one page of drafts, newest first. It
// reads no World and runs no Check: each store page costs a read of its
// rows, of their items and, for a caller without drafts:read, of the
// revisions of the rows neither their proposer nor the server rule opens.
func (a *App) handleDraftsList(w http.ResponseWriter, r *http.Request, c draftCaller) {
	f, before, limit, msg := draftFilterOf(r.URL.Query(), c)
	if msg != "" {
		apiError(w, http.StatusBadRequest, msg)
		return
	}
	page, next, err := a.listPage(r.Context(), c, f, before, limit)
	if err != nil {
		a.storeFailed(w, r, draftReadRefusal, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": page, "next_cursor": next})
}

// listPage answers the drafts that f matches with an id below before and
// that c may list, at most limit, and the cursor of the next page, "" when
// the store holds no more. It reads store pages until it holds limit+1
// such drafts or the store runs out, so that the page and its cursor
// depend on the drafts c may list alone and a filter tells nothing about
// the others. It reads at most draftScanMax rows, for a caller who lists
// only some drafts in store pages of at least a fifth of them, so a full
// scan reads at most five store pages. Past them it answers the drafts it
// found with the cursor at the last row it read.
func (a *App) listPage(ctx context.Context, c draftCaller, f store.DraftFilter, before int64, limit int) ([]summaryPayload, string, error) {
	page := []summaryPayload{}
	chunk := limit + 1
	if !c.p.root && !c.p.scope.Grants["drafts:read"] {
		chunk = max(chunk, draftScanMax/5)
	}
	for scanned := 0; scanned < draftScanMax; {
		asked := min(chunk, draftScanMax-scanned)
		rows, err := a.store.Drafts().List(ctx, f, before, asked)
		if err != nil {
			return nil, "", err
		}
		ids := make([]int64, len(rows))
		for i, row := range rows {
			ids[i] = row.ID
		}
		items, err := a.store.Drafts().Items(ctx, ids)
		if err != nil {
			return nil, "", err
		}
		listed, err := a.listable(ctx, c, rows, items)
		if err != nil {
			return nil, "", err
		}
		for _, row := range rows {
			switch ok := listed[row.ID]; {
			case ok && len(page) == limit:
				return page, page[limit-1].ID, nil
			case ok:
				page = append(page, summaryOf(row, items[row.ID]))
			}
			scanned, before = scanned+1, row.ID
		}
		if len(rows) < asked {
			return page, "", nil
		}
	}
	return page, strconv.FormatInt(before, 10), nil
}

// draftFilterOf reads the list's query for c: the filter, the id a page
// ends before, and the size of a page, or the 400 sentence of a value it
// cannot take. An empty value reads as no value, but for policy_edit,
// which names a set, since "policy:" alone matches every saved edit.
func draftFilterOf(q url.Values, c draftCaller) (store.DraftFilter, int64, int, string) {
	f := store.DraftFilter{State: string(drafts.StateOpen)}
	switch s := q.Get("state"); s {
	case "", string(drafts.StateOpen):
	case string(drafts.StatePublished), string(drafts.StateDiscarded), string(drafts.StateExpired):
		f.State = s
	case "all":
		f.State = ""
	default:
		return f, 0, 0, "state must be open, published, discarded, expired or all"
	}
	switch door := drafts.Door(q.Get("door")); door {
	case "":
	case drafts.DoorConsole, drafts.DoorStrazactl, drafts.DoorAgent, drafts.DoorAppsDir, drafts.DoorAPI:
		f.Door = string(door)
	default:
		return f, 0, 0, "door must be console, strazactl, straza-app, apps-directory or api"
	}
	if object := q.Get("object"); object != "" {
		kind, name, ok := strings.Cut(object, "/")
		if !ok || kind == "" || name == "" {
			return f, 0, 0, "object must be Kind/Name, such as App/github"
		}
		f.Object = store.ObjectRef{Kind: kind, Name: name}
	}
	f.Source = q.Get("source")
	for _, flag := range []struct {
		name string
		set  func()
	}{
		{"mine", func() { f.AuthorID = c.author.UserID }},
		{"working", func() { f.SlotPrefix = "working:" + c.author.UserID }},
	} {
		switch q.Get(flag.name) {
		case "", "false":
		case "true":
			flag.set()
		default:
			return f, 0, 0, flag.name + " must be true or false"
		}
	}
	if q.Has("policy_edit") {
		switch set := q.Get("policy_edit"); {
		case set == "":
			return f, 0, 0, "policy_edit must name a policy set, such as policy_edit=demo-tools-sandbox-access"
		case f.SlotPrefix != "":
			return f, 0, 0, "Ask for working or for policy_edit, not both, because each names one draft."
		default:
			f.SlotPrefix = "policy:" + set
		}
	}
	limit := 50
	if s := q.Get("limit"); s != "" {
		n, err := strconv.Atoi(s)
		if err != nil || n < 1 || n > draftListMax {
			return f, 0, 0, "limit must be a number from 1 to 200"
		}
		limit = n
	}
	var before int64
	if s := q.Get("cursor"); s != "" {
		n, err := strconv.ParseInt(s, 10, 64)
		if err != nil || n <= 0 {
			return f, 0, 0, "cursor is not a cursor this list gave out. Start again without it."
		}
		before = n
	}
	return f, before, limit, ""
}

// listable answers which of rows, whose items by draft id are items, c may
// list, reading in one store call the revisions of the rows that
// neither their proposer nor the server rule opens to c.
func (a *App) listable(ctx context.Context, c draftCaller, rows []store.DraftRow, items map[int64][]store.DraftItemRow) (map[int64]bool, error) {
	out := make(map[int64]bool, len(rows))
	var others []int64
	for _, row := range rows {
		if out[row.ID] = c.mayRead(c.isAuthor(row.Proposer.ID, row.Proposer.Via), items[row.ID]); !out[row.ID] {
			others = append(others, row.ID)
		}
	}
	revs, err := a.store.Drafts().RevisionsOf(ctx, others)
	if err != nil {
		return nil, err
	}
	for id, list := range revs {
		out[id] = c.wrote(authorsOf(list))
	}
	return out, nil
}

// summaryOf is the list row of the draft row whose items are rows.
func summaryOf(row store.DraftRow, rows []store.DraftItemRow) summaryPayload {
	d := draftOf(row, rows, nil)
	s := summaryPayload{ID: d.ID, Title: drafts.Title(d), State: row.State, Door: row.Door, Source: row.Source, Revision: row.Revision,
		Proposer: principalOf(row.Proposer), Items: make([]summaryItem, len(rows)), CreatedAt: rfc3339(row.CreatedAt), UpdatedAt: rfc3339(row.UpdatedAt)}
	for i, it := range rows {
		s.Items[i] = summaryItem{Kind: it.Kind, Name: it.Name, Op: it.Op}
	}
	s.Working, s.PolicyEdit = slotWords(row.Slot)
	if row.State == string(drafts.StateOpen) {
		s.Checks = lastChecks(row)
	}
	if row.DecidedAt != nil {
		s.DecidedAt = rfc3339(*row.DecidedAt)
	}
	s.DecidedBy = decidedBy(row.DecidedBy)
	return s
}

// handleDraftGet answers one draft with its verdict, its revisions, the
// live objects it changes and, once published, its change record.
// An open draft is checked now, as checkOpen says, and may_publish runs
// steps 1 and 4 of a publish only, so a publish can still answer the
// second-person 409. A draft that is not open runs no check, which would
// read what its own publish changed as stale and name fixes it cannot
// take: it answers storedCheck, may_publish false and no publish_refusal.
func (a *App) handleDraftGet(w http.ResponseWriter, r *http.Request, c draftCaller) {
	row, rows, revs, ok := a.readDraft(w, r, c)
	if !ok {
		return
	}
	ctx := r.Context()
	d := draftOf(row, rows, authorsOf(revs))
	open := row.State == string(drafts.StateOpen)
	var cd checkedDraft
	var checks *checksPayload
	if open {
		cd, ok = a.checkOpen(w, r, c, row, d, rows, revs)
	} else {
		cd, checks, ok = a.readDecided(w, r, row, d, rows)
	}
	if !ok {
		return
	}
	live, err := a.liveFor(ctx, c, cd.w, cd.d)
	if err != nil {
		a.fail(w, r, http.StatusServiceUnavailable, worldReadRefusal(err), err)
		return
	}
	out := draftDetail{Draft: draftAnswer(c, &cd.w, row, cd.rows, cd.d.Authors), Verdict: verdictFor(c, cd.w, cd.d, cd.v),
		Revisions: make([]revisionPayload, len(revs)), Live: live, Contacted: map[string]contactedEntry{}, Checks: checks}
	for i, rev := range revs {
		out.Revisions[i] = revisionPayload{Revision: rev.Revision, Author: principalOf(rev.Author), Door: rev.Door, Digest: rev.Digest,
			Mechanical: rev.Mechanical, CreatedAt: rfc3339(rev.CreatedAt)}
	}
	for object, o := range contactedOf(cd.rows) {
		if _, name, _ := strings.Cut(object, "/"); c.readsObject(cd.w, drafts.Item{Kind: drafts.KindApp, Name: name}) {
			out.Contacted[object] = contactedEntry{At: o.At, Tools: o.Tools}
		}
	}
	if row.State == string(drafts.StatePublished) {
		changes, err := a.store.Drafts().Changes(ctx, row.ID)
		if err != nil {
			a.storeFailed(w, r, draftReadRefusal, err)
			return
		}
		out.Changes = changesFor(c, cd.w, changes)
	}
	if open {
		out.PublishRefusal = c.publishRefusal(cd.d, cd.w, cd.in, cd.v)
		out.MayPublish = out.PublishRefusal == ""
	}
	writeJSON(w, http.StatusOK, out)
}

// liveFor answers the live state of every item of d and of every object
// its removals take with them, by Kind/Name, as c may read it. An
// object outside the read standing that readRefusal asks for at create is
// left out. op reads put, off or remove, and doc is the canonical live
// document: a server's stored manifest masked, a role's export, the
// published text of a set that is on, and the stored text of one that is
// off, read by name.
func (a *App) liveFor(ctx context.Context, c draftCaller, w drafts.World, d drafts.Draft) (map[string]liveEntry, error) {
	out := map[string]liveEntry{}
	seen := map[string]bool{}
	for _, it := range append(slices.Clone(d.Items), drafts.Implied(w, d)...) {
		object := it.Object()
		if seen[object] {
			continue
		}
		seen[object] = true
		if !c.readsObject(w, it) {
			continue
		}
		e := liveEntry{Op: string(drafts.OpRemove)}
		switch it.Kind {
		case drafts.KindApp:
			if app, ok := w.Apps[it.Name]; ok {
				e = liveEntry{Op: string(drafts.OpPut), Doc: maskedManifest(app.Manifest)}
			}
		case drafts.KindRole:
			if doc, ok := drafts.RoleDocOf(w, it.Name); ok {
				text, err := doc.Marshal()
				if err != nil {
					return nil, fmt.Errorf("the role %s cannot be written as a document: %w", it.Name, err)
				}
				e = liveEntry{Op: string(drafts.OpPut), Doc: string(text)}
			}
		case drafts.KindPolicySet:
			if p, ok := w.Policies[it.Name]; ok {
				e = liveEntry{Op: string(drafts.OpPut), Doc: p.Text}
			} else if w.Fingerprints[object] != "" {
				row, err := a.store.Policies().GetByName(ctx, it.Name)
				if err != nil {
					return nil, fmt.Errorf("the policy set %s cannot be read: %w", it.Name, err)
				}
				e = liveEntry{Op: string(drafts.OpOff), Doc: row.YAMLSource}
			}
		}
		out[object] = e
	}
	return out, nil
}

// changesFor answers the change record rows as c may read them: each
// server document masked, and a row about an object outside c's read
// standing left out, a role's owner read from the document it had last.
func changesFor(c draftCaller, w drafts.World, rows []store.DraftChangeRow) []changePayload {
	out := []changePayload{}
	for _, ch := range rows {
		doc := ch.AfterDoc
		if ch.AfterOp == string(drafts.OpRemove) {
			doc = ch.BeforeDoc
		}
		if !c.readsObject(w, drafts.Item{Kind: drafts.Kind(ch.Kind), Name: ch.Name, Op: drafts.Op(ch.AfterOp), Doc: doc}) {
			continue
		}
		out = append(out, changePayload{Kind: ch.Kind, Name: ch.Name, Implied: ch.Implied,
			BeforeOp: ch.BeforeOp, BeforeDoc: answerDoc(ch.Kind, ch.BeforeDoc), BeforeFP: ch.BeforeFP,
			AfterOp: ch.AfterOp, AfterDoc: answerDoc(ch.Kind, ch.AfterDoc), AfterFP: ch.AfterFP})
	}
	return out
}

// readsObject reports whether c may read the object of it, by the rule
// readRefusal applies to every item at create.
func (c draftCaller) readsObject(w drafts.World, it drafts.Item) bool {
	return c.readRefusal(drafts.Draft{Items: []drafts.Item{it}}, w) == ""
}

// draftAnswer is the stored draft row with rows as its items and authors
// as its authors, as c reads it. w is live state for the item cut: only a
// reader of an item's object reads its base, and a reader without root or
// drafts:read, who reaches the draft as an author or as a server's admin,
// reads an item outside its read standing as kind, name and op with the
// reason. A nil w cuts nothing, for root and for the items a caller sent
// to the check route.
func draftAnswer(c draftCaller, w *drafts.World, row store.DraftRow, rows []store.DraftItemRow, authors []drafts.Principal) draftPayload {
	d := draftOf(row, rows, authors)
	p := draftPayload{ID: d.ID, Revision: row.Revision, State: row.State, Door: row.Door, Source: row.Source, Note: row.Note,
		Authors: authors, Items: itemPayloads(c, w, rows), Reverts: d.Reverts, Title: drafts.Title(d), Refusal: row.Refusal,
		CreatedAt: rfc3339(row.CreatedAt), UpdatedAt: rfc3339(row.UpdatedAt), DecidedReason: row.DecidedReason, Snapshot: row.PublishedSnapshot}
	if p.Authors == nil {
		p.Authors = []drafts.Principal{}
	}
	p.Working, p.PolicyEdit = slotWords(row.Slot)
	if row.ExpiresAt != nil {
		p.ExpiresAt = rfc3339(*row.ExpiresAt)
	}
	if row.DecidedAt != nil {
		p.DecidedAt = rfc3339(*row.DecidedAt)
	}
	p.DecidedBy = decidedBy(row.DecidedBy)
	return p
}

// itemPayloads answers rows as c reads a draft's items, as draftAnswer
// says, every App document masked. The base is a fingerprint over the
// whole unmasked object, which confirms a guess at a masked value, so it
// goes to a reader of the object alone.
func itemPayloads(c draftCaller, w *drafts.World, rows []store.DraftItemRow) []itemPayload {
	authorOnly := !c.p.root && !c.p.scope.Grants["drafts:read"]
	out := make([]itemPayload, len(rows))
	for i, row := range rows {
		p := itemPayload{Kind: row.Kind, Name: row.Name, Op: row.Op, Existed: row.BaseOp == string(drafts.OpPut) || row.BaseOp == string(drafts.OpOff)}
		switch {
		case w == nil || c.readsObject(*w, drafts.Item{Kind: drafts.Kind(row.Kind), Name: row.Name, Op: drafts.Op(row.Op), Doc: row.Doc}):
			p.Doc, p.Base = answerDoc(row.Kind, row.Doc), row.Base
		case authorOnly:
			p.Withheld = withheldReason(row.Kind + "/" + row.Name)
		default:
			p.Doc = answerDoc(row.Kind, row.Doc)
		}
		out[i] = p
	}
	return out
}

// rfc3339 is t as every route writes a time, RFC3339 in UTC.
func rfc3339(t time.Time) string {
	return t.UTC().Format(time.RFC3339)
}

// withheldReason is why a reader reads the item of object as its kind,
// name and op alone.
func withheldReason(object string) string {
	why := "reading a policy set needs the scope policy:read"
	switch kind, _, _ := strings.Cut(object, "/"); drafts.Kind(kind) {
	case drafts.KindApp:
		why = "reading a server's config needs the scope apps:read or that server's admin role"
	case drafts.KindRole:
		why = "reading a role needs the scope identity:read, or for a role a server owns, the scope apps:read or that server's admin role"
	}
	return "Straza leaves out the document of " + object + ", because " + why + ". Ask an administrator for that grant."
}
