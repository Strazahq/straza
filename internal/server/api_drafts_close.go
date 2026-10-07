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
	"github.com/strazahq/straza/internal/redact"
	"github.com/strazahq/straza/internal/store"
)

// discardBody is the body of a discard: the revision the caller read and
// its reason, both optional.
type discardBody struct {
	Revision int    `json:"revision"`
	Reason   string `json:"reason"`
}

// revertBody is the body of an undo: the note of the new draft, optional.
type revertBody struct {
	Note *string `json:"note"`
}

// discardReasonMax is the most bytes a discard's reason holds, the cap of a
// decider's reason.
const discardReasonMax = 500

// reasonSecretRefusal is the 400 sentence of a discard reason that the
// note's secret scan refuses.
const reasonSecretRefusal = "The reason holds what looks like a secret, and the draft and its audit record keep the reason for good. Remove it and discard again."

// hiddenRunes are the characters the decider reason rule refuses beside the
// control characters: the direction embeddings, overrides, isolates and
// marks, which reorder the words on every surface that shows them, and two
// invisible spaces.
var hiddenRunes = map[rune]bool{
	0x202A: true, 0x202B: true, 0x202C: true, 0x202D: true, 0x202E: true,
	0x2066: true, 0x2067: true, 0x2068: true, 0x2069: true,
	0x200E: true, 0x200F: true, 0x061C: true, 0x200B: true, 0xFEFF: true,
}

// handleDraftDiscard ends an open draft as discarded, which changes nothing
// live. Its authors, root, and a person with standing over every item
// in it discard it, and an admin API token only the drafts it wrote.
func (a *App) handleDraftDiscard(w http.ResponseWriter, r *http.Request, c draftCaller) {
	var body discardBody
	if !decodeDraftBody(w, r, &body, discardBodyRefusal, true) {
		return
	}
	if msg := reasonRefusal(body.Reason); msg != "" {
		apiError(w, http.StatusBadRequest, msg)
		return
	}
	row, rows, revs, ok := a.readDraft(w, r, c)
	if !ok {
		return
	}
	ctx := r.Context()
	id := strconv.FormatInt(row.ID, 10)
	if row.State != string(drafts.StateOpen) {
		apiError(w, http.StatusConflict, discardedRefusal(row))
		return
	}
	// The authorship rule answers before the revision, so a caller who may
	// not discard is not sent to read the draft again first.
	authors := authorsOf(revs)
	msg, err := a.discardRefusal(ctx, c, draftOf(row, rows, authors))
	switch {
	case err != nil:
		a.fail(w, r, http.StatusServiceUnavailable, discardUnreadRefusal(id, err), err)
		return
	case msg != "":
		apiError(w, http.StatusForbidden, msg)
		return
	case body.Revision != 0 && body.Revision != row.Revision:
		apiError(w, http.StatusConflict, movedRefusal(row, body.Revision))
		return
	}
	table, err := a.cutWorld(ctx)
	if err != nil {
		a.fail(w, r, http.StatusServiceUnavailable, discardUnreadRefusal(id, err), err)
		return
	}
	live, err := a.liveObjects(ctx, table, rows)
	if err != nil {
		a.fail(w, r, http.StatusServiceUnavailable, discardUnreadRefusal(id, err), err)
		return
	}
	closed, err := a.store.Drafts().Close(ctx, row.ID, row.Revision, string(drafts.StateDiscarded), actorOf(c.author), body.Reason, time.Now())
	if err != nil {
		a.storeFailed(w, r, "Straza could not discard draft "+id+", so it is still open. Try again, and read the strazad log if it keeps failing.", err)
		return
	}
	now, rows, err := a.store.Drafts().Get(ctx, row.ID)
	switch {
	case err != nil:
		a.storeFailed(w, r, draftReadRefusal, err)
		return
	case !closed && now.State != string(drafts.StateOpen):
		apiError(w, http.StatusConflict, discardedRefusal(now))
		return
	case !closed && body.Revision == 0:
		apiError(w, http.StatusConflict, fmt.Sprintf("Draft %s moved to revision %d while the discard ran, so it is still open. "+
			"Read it again, and discard it again if you still mean to.", id, now.Revision))
		return
	case !closed:
		apiError(w, http.StatusConflict, movedRefusal(now, row.Revision))
		return
	}
	a.emitEventCtx(context.WithoutCancel(ctx), "straza.audit.admin", withFile(discardData(id, now.Revision, body.Reason), now, rows))
	answer := draftAnswer(c, &table, now, rows, authors)
	markExisted(answer.Items, rows, live)
	writeJSON(w, http.StatusOK, map[string]any{"draft": answer})
}

// recordDiscard writes the draft.discard of the draft id, discarded at
// revision rev with reason, whether or not the request is
// still waiting, since the draft is discarded already.
func (a *App) recordDiscard(ctx context.Context, id string, rev int, reason string) {
	a.emitEventCtx(context.WithoutCancel(ctx), "straza.audit.admin", discardData(id, rev, reason))
}

// discardData is the data of the draft.discard of the draft id at revision
// rev with reason, which a draft of the apps directory completes with its
// file, its hash and the server it unlinks.
func discardData(id string, rev int, reason string) map[string]any {
	data := map[string]any{"action": "draft.discard", "draft": id, "revision": rev}
	if reason != "" {
		data["reason"] = reason
	}
	return data
}

// discardedRefusal is the 409 of a discard of a draft that is decided
// already.
func discardedRefusal(row store.DraftRow) string {
	return fmt.Sprintf("Draft %d is %s already, so there is nothing to discard.", row.ID, row.State)
}

// discardUnreadRefusal is the 503 of a discard of the draft id whose read
// of live state failed with err.
func discardUnreadRefusal(id string, err error) string {
	return fmt.Sprintf("Straza could not read live state to decide the discard of draft %s: %v. The draft is still open. "+
		"Try again, and read the strazad log if it keeps failing.", id, err)
}

// cutWorld reads the roles and servers that the item cut of draftAnswer,
// readRefusal and liveObjects read.
func (a *App) cutWorld(ctx context.Context) (drafts.World, error) {
	w, err := a.readRoleTable(ctx)
	if err != nil {
		return drafts.World{}, fmt.Errorf("the roles and servers cannot be read: %w", err)
	}
	return w, nil
}

// reasonRefusal answers the 400 sentence of a discard reason that the
// decider reason rule refuses, or "": more than 500 bytes, a control
// character other than tab and newline, or a character that reorders or
// hides text. A JSON body carries valid UTF-8 only, so no other check is
// needed. A reason the note's secret scan refuses is refused too, since the
// draft row and its draft.discard record keep the reason.
func reasonRefusal(reason string) string {
	if len(reason) > discardReasonMax {
		return fmt.Sprintf("The reason holds %d bytes, and a reason holds at most %d. Shorten it and discard again.", len(reason), discardReasonMax)
	}
	for _, r := range reason {
		if r < 0x20 && r != '\n' && r != '\t' || r == 0x7f || hiddenRunes[r] {
			return fmt.Sprintf("The reason holds a control or invisible character, U+%04X, that can make the text read differently from what is stored. "+
				"Remove it and discard again.", r)
		}
	}
	if slices.ContainsFunc(drafts.ScanNote(reason), func(f drafts.Finding) bool { return f.Class == drafts.ClassRefused }) {
		return reasonSecretRefusal
	}
	return ""
}

// handleDraftRevert makes a new draft that undoes a published one:
// its change record read backwards, implied rows included, each item's
// base, base op and base document taken from the row's after, so an object
// changed since the publish reads stale at once. It meets the caller's read
// standing and then intake, as a new draft does, a finding live state may
// waive waiting for the check's read. The standing comes first, because
// the documents come from the change record and intake's words would
// describe objects the caller may not read. A stored manifest's secrets
// were masked by the stamp, so an undo that carries such a manifest
// back sends the masks: the check waives each one for the value the live
// manifest still holds where it stands, and refuses the undo in its own
// words otherwise.
func (a *App) handleDraftRevert(w http.ResponseWriter, r *http.Request, c draftCaller) {
	var body revertBody
	if !decodeDraftBody(w, r, &body, revertBodyRefusal, true) {
		return
	}
	row, _, _, ok := a.readDraft(w, r, c)
	if !ok {
		return
	}
	id := strconv.FormatInt(row.ID, 10)
	if row.State != string(drafts.StatePublished) {
		apiError(w, http.StatusConflict, fmt.Sprintf("Draft %s is %s, and only a published draft can be undone.", id, row.State))
		return
	}
	changes, err := a.store.Drafts().Changes(r.Context(), row.ID)
	if err != nil {
		a.storeFailed(w, r, draftReadRefusal, err)
		return
	}
	if len(changes) == 0 {
		apiError(w, http.StatusConflict, fmt.Sprintf("Draft %s changed nothing, so there is nothing to undo.", id))
		return
	}
	rows := make([]store.DraftItemRow, len(changes))
	for i, ch := range changes {
		rows[i] = store.DraftItemRow{Kind: ch.Kind, Name: ch.Name, Op: ch.BeforeOp, Doc: ch.BeforeDoc, Base: ch.AfterFP, BaseOp: ch.AfterOp, BaseDoc: ch.AfterDoc}
	}
	d := drafts.Draft{Revision: 1, State: drafts.StateOpen, Door: c.door, Note: noteOf(body.Note, ""), Authors: []drafts.Principal{c.author},
		Items: itemsOf(rows), Reverts: id}
	table, err := a.cutWorld(r.Context())
	if err != nil {
		a.fail(w, r, http.StatusServiceUnavailable, worldReadRefusal(err), err)
		return
	}
	if msg := c.readRefusal(d, table); msg != "" {
		apiError(w, http.StatusForbidden, msg)
		return
	}
	fs := intakeOf(d, c.author)
	// A server whose stamped manifest holds the mark answers a refusal of
	// its mask, or of a secret the scan still reads in the masked text, in
	// the undo's words. Inside an address's user information the mark is
	// percent-encoded, because net/url refuses a literal bracket there.
	marked, masked := map[string]bool{}, map[string]bool{}
	for _, it := range rows {
		if it.Kind == string(drafts.KindApp) && (strings.Contains(it.Doc, redact.Mark) || strings.Contains(it.Doc, url.PathEscape(redact.Mark))) {
			marked[it.Kind+"/"+it.Name] = true
		}
	}
	for i, f := range fs {
		if !marked[f.Object] || f.Code != "bundle.masked" && !strings.HasPrefix(f.Code, "secret.") {
			continue
		}
		_, name, _ := strings.Cut(f.Object, "/")
		fs[i].Sentence, fs[i].Fix = fmt.Sprintf(revertMaskedSentence, id, name), fmt.Sprintf(revertMaskedFix, name)
		if f.Code == "bundle.masked" {
			masked[f.Object] = true
		}
	}
	// A mark intake does not read as a mask, one inside a longer string, is
	// refused here, because the undo would publish it as text.
	for _, it := range rows {
		if object := it.Kind + "/" + it.Name; marked[object] && !masked[object] {
			apiError(w, http.StatusUnprocessableEntity, fmt.Sprintf(revertMaskedSentence+" "+revertMaskedFix, id, it.Name, it.Name))
			return
		}
	}
	if refuseCertain(w, fs) {
		return
	}
	a.storeNew(w, r, c, d, rows, "", fs)
}

// The sentence and the fix of an undo that cannot carry a stored manifest
// back, because the stamp masked a secret in it and the mask stands for no
// value the live manifest holds.
const (
	revertMaskedSentence = "Draft %s removed or changed %s, whose stored manifest held a secret that a draft never keeps, so this undo cannot carry it."
	revertMaskedFix      = "Write the server's manifest from your own file, and after publishing store the secret with strazactl apps secret set %s and set credential.inject."
)
