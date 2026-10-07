package server

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strconv"
	"time"

	"github.com/strazahq/straza/internal/drafts"
	"github.com/strazahq/straza/internal/store"
)

// draftBody is the body of the routes that write or check a draft:
// documents are YAML streams, items single objects, note the proposer's
// words, working whether the items join the caller's working draft, and
// revision the revision an update is made on. A nil Note is a note left
// out, which an update reads as keeping the one the draft has.
type draftBody struct {
	Revision  int             `json:"revision"`
	Documents []string        `json:"documents"`
	Items     []draftBodyItem `json:"items"`
	Note      *string         `json:"note"`
	Working   bool            `json:"working"`
}

// draftBodyItem is one object of the items form. It carries no base,
// because the server stamps every item it stores or checks.
type draftBodyItem struct {
	Kind drafts.Kind `json:"kind"`
	Name string      `json:"name"`
	Op   drafts.Op   `json:"op"`
	Doc  string      `json:"doc"`
}

// The sentences of a body that does not decode, one per route family.
const (
	draftBodyRefusal   = "The request body is not a draft: %s. Send documents, items or both as JSON."
	discardBodyRefusal = "The request body is not a discard: %s. Send revision and reason as JSON, or no body."
	revertBodyRefusal  = "The request body is not an undo: %s. Send a note as JSON, or no body."
)

// The sentences of a failed store call on the drafts routes. The cause
// goes to the log with the correlation id.
const (
	draftStoreRefusal = "Straza could not store the draft, so nothing was saved. Try again, and read the strazad log if it keeps failing."
	draftReadRefusal  = "Straza could not read the drafts. Try again, and read the strazad log if it keeps failing."
	workingRefusal    = "Your working draft changed while this request saved it, so nothing was saved. Send the items again."
)

// draftExpiry is how long a draft of the straza-app door stays open after
// its latest revision.
const draftExpiry = 14 * 24 * time.Hour

// decodeDraftBody decodes the request body into v and answers the request
// itself when it cannot: 413 for a body over server.maxBodyBytes, and 400
// in the words of frame otherwise. With optional, a request with no body
// reads as an empty object.
func decodeDraftBody(w http.ResponseWriter, r *http.Request, v any, frame string, optional bool) bool {
	err := json.NewDecoder(r.Body).Decode(v)
	var tooLarge *http.MaxBytesError
	switch {
	case err == nil, optional && errors.Is(err, io.EOF):
		return true
	case errors.As(err, &tooLarge):
		apiError(w, http.StatusRequestEntityTooLarge, fmt.Sprintf("The request body holds more than %d bytes, the most strazad reads in one request (server.maxBodyBytes). "+
			"Split the draft into smaller drafts, or raise server.maxBodyBytes.", tooLarge.Limit))
	default:
		apiError(w, http.StatusBadRequest, fmt.Sprintf(frame, err))
	}
	return false
}

// handleDraftCreate stores a new draft of documents or items, checked
// against live state, or with working adds them to the caller's working
// draft. A refusal at intake stores nothing and answers 422, unless
// live state may waive every finding: those wait for the check's read.
func (a *App) handleDraftCreate(w http.ResponseWriter, r *http.Request, c draftCaller) {
	var body draftBody
	if !decodeDraftBody(w, r, &body, draftBodyRefusal, false) {
		return
	}
	if body.Working {
		a.addToWorking(w, r, c, body)
		return
	}
	items, fs := bodyItems(body)
	d := drafts.Draft{Revision: 1, State: drafts.StateOpen, Door: c.door, Note: noteOf(body.Note, ""), Authors: []drafts.Principal{c.author}, Items: items}
	if fs = bodyIntake(d, c.author, fs); refuseCertain(w, fs) {
		return
	}
	open, err := a.store.Drafts().CountOpen(r.Context(), c.author.UserID)
	if err != nil {
		err = fmt.Errorf("the open drafts of %s cannot be counted: %w", c.author.Username, err)
		a.fail(w, r, http.StatusServiceUnavailable, worldReadRefusal(err), err)
		return
	}
	if limit := drafts.OpenLimit(c.author.Username, open); len(limit) > 0 {
		refuseIntake(w, limit)
		return
	}
	rows := rowsOf(items)
	canonicalRoles(rows, nil)
	a.storeNew(w, r, c, d, rows, "", fs)
}

// addToWorking adds the items of body to the caller's working draft, on the
// slot working:<user id>, or creates it. An item that names an object
// the draft holds replaces its document and keeps its base, and new
// objects join at the end. Only a person keeps a working draft, and it
// never counts toward the open limit.
func (a *App) addToWorking(w http.ResponseWriter, r *http.Request, c draftCaller, body draftBody) {
	switch {
	case c.adminAPI():
		apiError(w, http.StatusBadRequest, "working drafts belong to people, and an admin API token is not one. Create a draft without working.")
		return
	case !c.person:
		apiError(w, http.StatusBadRequest, "working drafts belong to people, and an agent is not one. Create a draft without working.")
		return
	}
	ctx := r.Context()
	slot := "working:" + c.author.UserID
	row, held, err := a.store.Drafts().BySlot(ctx, slot)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		a.storeFailed(w, r, draftReadRefusal, err)
		return
	}
	found := err == nil
	items, fs := bodyItems(body)
	d := drafts.Draft{Revision: 1, State: drafts.StateOpen, Door: c.door, Note: noteOf(body.Note, ""), Authors: []drafts.Principal{c.author}}
	if found {
		revs, err := a.store.Drafts().Revisions(ctx, row.ID)
		if err != nil {
			a.storeFailed(w, r, draftReadRefusal, err)
			return
		}
		d = draftOf(row, held, withAuthor(authorsOf(revs), c.author))
		d.Note = noteOf(body.Note, row.Note)
	}
	rows := mergeItems(held, items)
	d.Items = itemsOf(rows)
	if fs = bodyIntake(d, c.author, fs); refuseCertain(w, fs) {
		return
	}
	canonicalRoles(rows, nil)
	if !found {
		a.storeNew(w, r, c, d, rows, slot, fs)
		return
	}
	a.storeRevision(w, r, c, row, d, rows, body.Note, workingRefusal, fs)
}

// handleDraftCheck checks documents or items against live state and
// answers the verdict, storing, stamping and recording nothing. Each
// item is stamped in memory, so a put of a live object reads as a change,
// and the intake refusals left after the waiver (drafts.Waive) join the
// verdict's refusals, the waived ones its warnings. The read standing is
// judged over every item, the ones intake refused included, since an item
// intake refused reads existed from live state. A draft that intake refuses
// for its size is answered before live state is read.
func (a *App) handleDraftCheck(w http.ResponseWriter, r *http.Request, c draftCaller) {
	var body draftBody
	if !decodeDraftBody(w, r, &body, draftBodyRefusal, false) {
		return
	}
	items, fs := bodyItems(body)
	d := drafts.Draft{State: drafts.StateOpen, Door: c.door, Note: noteOf(body.Note, ""), Authors: []drafts.Principal{c.author}, Items: items}
	fs = bodyIntake(d, c.author, fs)
	rows := rowsOf(items)
	if slices.ContainsFunc(fs, func(f drafts.Finding) bool { return f.Code == "draft.size" }) {
		writeJSON(w, http.StatusOK, map[string]any{"items": itemPayloads(c, nil, rows), "verdict": verdictFor(c, drafts.World{}, d, intakeVerdict(fs))})
		return
	}
	// Live state is read for every item the waiver may keep, so a waived
	// item is stamped with its live base and not read as new.
	read := d
	read.Items = itemsOf(checkedRows(rows, refusedItems(d, certain(fs))))
	world, in, ok := a.readLive(w, r, read)
	if !ok {
		return
	}
	if msg := c.readRefusal(d, world); msg != "" {
		apiError(w, http.StatusForbidden, msg)
		return
	}
	fs, waived := c.readerWaived(world, d, fs)
	refused := refusedItems(d, fs)
	canonicalRoles(rows, refused)
	clean := checkedRows(rows, refused)
	d.Items = itemsOf(clean)
	cd, ok := a.stampAndCheck(w, r, world, in, d, clean)
	if !ok {
		return
	}
	cd.v = c.readerVerdict(cd.w, cd.d, cd.v, waived)
	cd.v.Refused = sortFindings(append(fs, cd.v.Refused...))
	stamped := map[string]store.DraftItemRow{}
	for _, row := range cd.rows {
		stamped[row.Kind+"/"+row.Name] = row
	}
	for i, row := range rows {
		if s, ok := stamped[row.Kind+"/"+row.Name]; ok {
			rows[i] = s
		}
	}
	live, err := a.liveObjects(r.Context(), cd.w, rows)
	if err != nil {
		a.fail(w, r, http.StatusServiceUnavailable, worldReadRefusal(err), err)
		return
	}
	answered := itemPayloads(c, nil, rows)
	markExisted(answered, rows, live)
	writeJSON(w, http.StatusOK, map[string]any{"items": answered, "verdict": verdictFor(c, cd.w, cd.d, cd.v)})
}

// handleDraftUpdate writes a new revision of an open draft from the whole
// new item list. An item that names an object the draft already
// holds keeps its base, and a new one is stamped now. A person who
// writes a revision becomes an author, and an admin API token or an agent
// revises only its own drafts. The saved edit of a set keeps one
// new text of that set alone, since activate publishes it as it
// is.
func (a *App) handleDraftUpdate(w http.ResponseWriter, r *http.Request, c draftCaller) {
	var body draftBody
	if !decodeDraftBody(w, r, &body, draftBodyRefusal, false) {
		return
	}
	row, held, revs, ok := a.readDraft(w, r, c)
	if !ok {
		return
	}
	authors := authorsOf(revs)
	if msg := c.reviseRefusal(strconv.FormatInt(row.ID, 10), authors); msg != "" {
		apiError(w, http.StatusForbidden, msg)
		return
	}
	if row.State != string(drafts.StateOpen) || body.Revision != row.Revision {
		apiError(w, http.StatusConflict, movedRefusal(row, body.Revision))
		return
	}
	items, fs := bodyItems(body)
	rows := keepHeld(held, items)
	d := draftOf(row, rows, withAuthor(authors, c.author))
	d.Note = noteOf(body.Note, row.Note)
	if fs = bodyIntake(d, c.author, fs); refuseCertain(w, fs) {
		return
	}
	if _, set := slotWords(row.Slot); set != "" && (len(items) != 1 || items[0].Kind != drafts.KindPolicySet || items[0].Name != set || items[0].Op != drafts.OpPut) {
		apiError(w, http.StatusConflict, fmt.Sprintf("Draft %d is the saved edit of the policy set %s and holds a new text of that set alone. "+
			"Save the set's text with strazactl policy apply, or create a draft of your own for any other change.", row.ID, set))
		return
	}
	canonicalRoles(rows, nil)
	a.storeRevision(w, r, c, row, d, rows, body.Note, "", fs)
}

// storeNew checks d for c, its intake findings intake waived against live
// state, stores it as revision 1 with its check on slot, writes its
// draft.create and draft.check, and answers 201 with the draft and its
// verdict.
func (a *App) storeNew(w http.ResponseWriter, r *http.Request, c draftCaller, d drafts.Draft, rows []store.DraftItemRow, slot string, intake []drafts.Finding) {
	cd, ok := a.checkFor(w, r, c, d, rows, intake)
	if !ok {
		return
	}
	ctx := r.Context()
	check := stampOf(cd.d, cd.v, cd.in.Now)
	reverts, _ := strconv.ParseInt(d.Reverts, 10, 64)
	row, err := a.store.Drafts().Create(ctx, store.DraftRow{Door: string(d.Door), Slot: slot, Note: d.Note, Reverts: reverts,
		CheckedRevision: 1, CheckedAt: &check.At, CheckedSnapshot: check.Snapshot, CheckCounts: check.Counts, AgentVerdict: check.AgentVerdict},
		cd.rows, store.DraftRevisionRow{Author: actorOf(c.author), Digest: revisionDigest(cd.d.Items)})
	switch {
	case errors.Is(err, store.ErrConflict) && slot != "":
		apiError(w, http.StatusConflict, workingRefusal)
		return
	case err != nil:
		a.storeFailed(w, r, draftStoreRefusal, err)
		return
	}
	id := strconv.FormatInt(row.ID, 10)
	cd.d.ID, cd.v.Draft, cd.v.Revision = id, id, 1
	a.recordRevision(ctx, "draft.create", id, 1, d.Door, cd.d.Items, d.Reverts)
	a.recordCheck(ctx, id, cd.v)
	writeJSON(w, http.StatusCreated, map[string]any{"draft": draftAnswer(c, &cd.w, row, cd.rows, cd.d.Authors), "verdict": verdictFor(c, cd.w, cd.d, cd.v)})
}

// storeRevision checks d for c, its intake findings intake waived against
// live state, and writes it as the revision after row with its check, then
// writes its draft.update and draft.check and answers 200 with the draft
// and its verdict. A draft of the straza-app door expires 14 days after
// this revision. A draft that moved meanwhile answers 409 with
// conflict, or with the update route's sentences when conflict is empty.
func (a *App) storeRevision(w http.ResponseWriter, r *http.Request, c draftCaller, row store.DraftRow, d drafts.Draft, rows []store.DraftItemRow, note *string, conflict string, intake []drafts.Finding) {
	cd, ok := a.checkFor(w, r, c, d, rows, intake)
	if !ok {
		return
	}
	ctx := r.Context()
	check := stampOf(cd.d, cd.v, cd.in.Now)
	rev := store.DraftRevise{From: row.Revision, Items: cd.rows, Note: note, Checked: &check,
		Rev: store.DraftRevisionRow{Author: actorOf(c.author), Door: string(c.door), Digest: revisionDigest(cd.d.Items)}}
	if row.Door == string(drafts.DoorAgent) {
		expires := cd.in.Now.Add(draftExpiry)
		rev.ExpiresAt = &expires
	}
	updated, err := a.store.Drafts().Revise(ctx, row.ID, rev)
	switch {
	case errors.Is(err, store.ErrConflict) && conflict != "":
		apiError(w, http.StatusConflict, conflict)
		return
	case errors.Is(err, store.ErrConflict):
		apiError(w, http.StatusConflict, movedRefusal(updated, row.Revision))
		return
	case err != nil:
		a.storeFailed(w, r, draftStoreRefusal, err)
		return
	}
	id := strconv.FormatInt(updated.ID, 10)
	cd.d.Revision, cd.v.Draft, cd.v.Revision = updated.Revision, id, updated.Revision
	reverts := ""
	if updated.Reverts != 0 {
		reverts = strconv.FormatInt(updated.Reverts, 10)
	}
	a.recordRevision(ctx, "draft.update", id, updated.Revision, c.door, cd.d.Items, reverts)
	a.recordCheck(ctx, id, cd.v)
	writeJSON(w, http.StatusOK, map[string]any{"draft": draftAnswer(c, &cd.w, updated, cd.rows, cd.d.Authors), "verdict": verdictFor(c, cd.w, cd.d, cd.v)})
}

// checkedDraft is a draft read against live state: the World, the input of
// its check, the draft with its items stamped, those items as rows, and
// the verdict.
type checkedDraft struct {
	w    drafts.World
	in   drafts.CheckInput
	d    drafts.Draft
	rows []store.DraftItemRow
	v    drafts.Verdict
}

// checkFor reads live state for d, whose items rows hold, refuses a draft
// holding an object c may not read today, waives the intake findings
// intake that live state holds already and refuses the rest
// (drafts.Waive), stamps every row that has no base, and checks d, the
// waived warnings joining its verdict. The read standing comes before the
// waiver, so a caller who may not read a server never learns from the
// answer whether a guess at a masked value matches. It answers the request
// itself and reports false when it cannot: 503 when live state cannot be
// read, 403 for an object outside c's read standing, and 422 for an intake
// refusal left.
func (a *App) checkFor(w http.ResponseWriter, r *http.Request, c draftCaller, d drafts.Draft, rows []store.DraftItemRow, intake []drafts.Finding) (checkedDraft, bool) {
	// The waiver reads the items as intake did, because a finding carries
	// the document number the bundle gave its item, which rows never keep.
	sent := d
	d.Items = itemsOf(rows)
	world, in, ok := a.readLive(w, r, d)
	if !ok {
		return checkedDraft{}, false
	}
	if msg := c.readRefusal(d, world); msg != "" {
		apiError(w, http.StatusForbidden, msg)
		return checkedDraft{}, false
	}
	refused, waived := c.readerWaived(world, sent, intake)
	if len(refused) > 0 {
		refuseIntake(w, refused)
		return checkedDraft{}, false
	}
	cd, ok := a.stampAndCheck(w, r, world, in, d, rows)
	if ok {
		cd.v = c.readerVerdict(cd.w, cd.d, cd.v, waived)
	}
	return cd, ok
}

// readLive reads live state for checking d, and answers 503 itself when it
// cannot.
func (a *App) readLive(w http.ResponseWriter, r *http.Request, d drafts.Draft) (drafts.World, drafts.CheckInput, bool) {
	world, in, err := a.draftWorld(r.Context(), d)
	if err != nil {
		a.fail(w, r, http.StatusServiceUnavailable, worldReadRefusal(err), err)
		return drafts.World{}, drafts.CheckInput{}, false
	}
	return world, in, true
}

// stampAndCheck is checkRows for a route, which answers 503 itself when
// the stamp cannot read what it needs.
func (a *App) stampAndCheck(w http.ResponseWriter, r *http.Request, world drafts.World, in drafts.CheckInput, d drafts.Draft, rows []store.DraftItemRow) (checkedDraft, bool) {
	cd, err := a.checkRows(r.Context(), world, in, d, rows)
	if err != nil {
		a.fail(w, r, http.StatusServiceUnavailable, worldReadRefusal(err), err)
		return checkedDraft{}, false
	}
	return cd, true
}

// checkRows stamps every row of d that has no base with its object as
// world holds it, in place, reads the tools a Contact read for each item's
// current document and the facts of a server whose document sends a mask
// back (keptFacts), and checks d. The routes and the off-path checker
// share it.
func (a *App) checkRows(ctx context.Context, world drafts.World, in drafts.CheckInput, d drafts.Draft, rows []store.DraftItemRow) (checkedDraft, error) {
	if err := a.stampRows(ctx, world, rows); err != nil {
		return checkedDraft{}, err
	}
	d.Items = itemsOf(rows)
	a.keptFacts(world, d, in.Apps)
	in.Contacted = map[string][]string{}
	for object, o := range contactedOf(rows) {
		in.Contacted[object] = o.Tools
	}
	return checkedDraft{w: world, in: in, d: d, rows: rows, v: drafts.Check(world, d, in)}, nil
}

// stampRows stamps every row whose BaseOp is empty with its object as w
// holds it: the live fingerprint, the live state in the words of an item's
// op, and the canonical live document. A server's document is its stored
// manifest with every secret the draft scan finds masked, since a manifest
// stored before the scan may hold one and base_doc keeps no secret,
// while the fingerprint stays the live one. A set that is off is stamped
// with its stored text, read by name and refused when its row moved since w
// was read.
func (a *App) stampRows(ctx context.Context, w drafts.World, rows []store.DraftItemRow) error {
	for i := range rows {
		it := &rows[i]
		if it.BaseOp != "" {
			continue
		}
		it.Base, it.BaseOp, it.BaseDoc = string(w.Fingerprints[it.Kind+"/"+it.Name]), string(drafts.OpRemove), ""
		switch drafts.Kind(it.Kind) {
		case drafts.KindApp:
			if app, ok := w.Apps[it.Name]; ok {
				doc, _ := drafts.WithoutSecrets(app.Manifest)
				it.BaseOp, it.BaseDoc = string(drafts.OpPut), doc
			}
		case drafts.KindRole:
			if doc, ok := drafts.RoleDocOf(w, it.Name); ok {
				text, err := doc.Marshal()
				if err != nil {
					return fmt.Errorf("the role %s cannot be written as a document: %w", it.Name, err)
				}
				it.BaseOp, it.BaseDoc = string(drafts.OpPut), string(text)
			}
		case drafts.KindPolicySet:
			if p, ok := w.Policies[it.Name]; ok {
				it.BaseOp, it.BaseDoc = string(drafts.OpPut), p.Text
			} else if it.Base != "" {
				row, err := a.store.Policies().GetByName(ctx, it.Name)
				if err != nil {
					return fmt.Errorf("the policy set %s cannot be read: %w", it.Name, err)
				}
				if store.FingerprintPolicySet(row) != it.Base {
					return fmt.Errorf("the policy set %s changed while it was read", it.Name)
				}
				it.BaseOp, it.BaseDoc = string(drafts.OpOff), row.YAMLSource
			}
		}
	}
	return nil
}

// readDraft reads the draft the request's id names, with its items and
// revisions, for c. It answers 404 itself for an id that is no draft
// number, for no such draft and for a draft c may not read, so the
// answer does not tell whether a draft c may not read exists.
func (a *App) readDraft(w http.ResponseWriter, r *http.Request, c draftCaller) (store.DraftRow, []store.DraftItemRow, []store.DraftRevisionRow, bool) {
	raw := r.PathValue("id")
	notFound := func() {
		apiError(w, http.StatusNotFound, "There is no draft "+raw+". List the drafts with strazactl drafts list, or open Drafts on the console.")
	}
	id, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || id <= 0 {
		notFound()
		return store.DraftRow{}, nil, nil, false
	}
	ctx := r.Context()
	row, items, err := a.store.Drafts().Get(ctx, id)
	if errors.Is(err, store.ErrNotFound) {
		notFound()
		return store.DraftRow{}, nil, nil, false
	}
	var revs []store.DraftRevisionRow
	if err == nil {
		revs, err = a.store.Drafts().Revisions(ctx, id)
	}
	if err != nil {
		a.storeFailed(w, r, draftReadRefusal, err)
		return store.DraftRow{}, nil, nil, false
	}
	if !c.mayRead(c.wrote(authorsOf(revs)), items) {
		notFound()
		return store.DraftRow{}, nil, nil, false
	}
	return row, items, revs, true
}

// storeFailed answers a failed store call: 503 for an outage of the store,
// and 500 with msg otherwise, the cause going to the log.
func (a *App) storeFailed(w http.ResponseWriter, r *http.Request, msg string, err error) {
	if !a.answerOutage(w, r, "drafts", err) {
		a.fail(w, r, http.StatusInternalServerError, msg, err)
	}
}

// movedRefusal is the 409 of a write on the draft row made at the revision
// sent: the draft is no longer open, or it is at another revision.
func movedRefusal(row store.DraftRow, sent int) string {
	id := strconv.FormatInt(row.ID, 10)
	if row.State != string(drafts.StateOpen) {
		return fmt.Sprintf("Draft %s is %s, so it cannot change. Create a new draft from its documents.", id, row.State)
	}
	return fmt.Sprintf("Draft %s changed after you read it: it is at revision %d, and you sent revision %d. Read it again and make your change on top of revision %d.",
		id, row.Revision, sent, row.Revision)
}

// stampOf is the check a store write records for the verdict v of d: its
// snapshot, its counts, and, for a draft of the straza-app door or one an
// author who is not a person wrote, the verdict an agent reads. A person
// who holds straza-draft-config submits through that door too, and
// straza__draft_status reads that verdict.
func stampOf(d drafts.Draft, v drafts.Verdict, at time.Time) store.DraftCheck {
	counts, _ := json.Marshal(map[string]int{"refused": len(v.Refused), "risks": len(v.Risks), "warnings": len(v.Warnings), "unchecked": len(v.Unchecked)})
	check := store.DraftCheck{Snapshot: v.Snapshot, Counts: string(counts), At: at}
	if d.Door == drafts.DoorAgent || slices.ContainsFunc(d.Authors, func(p drafts.Principal) bool { return p.Agent }) {
		if agent, err := json.Marshal(v.ForAgent()); err == nil {
			check.AgentVerdict = string(agent)
		}
	}
	return check
}

// revisionDigest is the hex sha256 of a revision's items as encoding/json
// writes them, each {kind, name, op, doc} in item order. The base is left
// out, because a stamp may set it after the revision is written.
func revisionDigest(items []drafts.Item) string {
	type digested struct {
		Kind drafts.Kind `json:"kind"`
		Name string      `json:"name"`
		Op   drafts.Op   `json:"op"`
		Doc  string      `json:"doc"`
	}
	list := make([]digested, len(items))
	for i, it := range items {
		list[i] = digested{Kind: it.Kind, Name: it.Name, Op: it.Op, Doc: it.Doc}
	}
	raw, _ := json.Marshal(list)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

// noActor is ctx without the admin actor, for a record strazad writes on
// its own account.
func noActor(ctx context.Context) context.Context {
	return context.WithValue(ctx, actorCtxKey{}, nil)
}

// recordRevision writes the draft.create or draft.update of revision rev of
// the draft id: the door it came through, its items by kind,
// name and op, their digest, and the draft it undoes. No note and no
// document enters a record. It writes whether or not the request is still
// waiting, since the revision is stored already, and so does recordCheck.
func (a *App) recordRevision(ctx context.Context, action, id string, rev int, door drafts.Door, items []drafts.Item, reverts string) {
	data := revisionData(action, id, rev, door, items)
	if reverts != "" {
		data["reverts"] = reverts
	}
	a.emitEventCtx(context.WithoutCancel(ctx), "straza.audit.admin", data)
}

// revisionData is the data of the draft.create or draft.update of revision
// rev of the draft id, which each door completes with its own fields.
func revisionData(action, id string, rev int, door drafts.Door, items []drafts.Item) map[string]any {
	list := make([]map[string]string, len(items))
	for i, it := range items {
		list[i] = map[string]string{"kind": string(it.Kind), "name": it.Name, "op": string(it.Op)}
	}
	return map[string]any{"action": action, "draft": id, "revision": rev, "door": string(door), "items": list, "digest": revisionDigest(items)}
}

// recordCheck writes the one draft.check of a stamp that landed: the
// codes of the verdict's refusals and risks, never a sentence, and no
// actor, because the check is the server's whoever's request caused it.
func (a *App) recordCheck(ctx context.Context, id string, v drafts.Verdict) {
	codes := func(fs []drafts.Finding) []string {
		out := make([]string, len(fs))
		for i, f := range fs {
			out[i] = f.Code
		}
		return out
	}
	a.emitEventCtx(noActor(context.WithoutCancel(ctx)), "straza.audit.admin", map[string]any{"action": "draft.check", "draft": id, "revision": v.Revision,
		"snapshot": v.Snapshot, "refused": codes(v.Refused), "risks": codes(v.Risks)})
}
