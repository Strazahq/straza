package server

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/strazahq/straza/internal/drafts"
	"github.com/strazahq/straza/internal/manager"
	"github.com/strazahq/straza/internal/store"
)

// publishRoute is the pattern of the publish route as the router hands it
// to the request in r.Pattern.
const publishRoute = "POST /v1/admin/drafts/{id}/publish"

// publishRuns is how many runs of steps 3 to 7 a publish takes before it
// answers that live state kept moving.
const publishRuns = 3

// codeSecondPerson is the code of the 409 that admin.secondPerson answers,
// so a client tells that refusal from the route's other 409s. Its
// sentence says who may publish the draft instead.
const codeSecondPerson = "second_person"

// The sentences of the publish route. The cause of every 500 goes to
// the log with the correlation id, never into the body.
const (
	publishBodyRefusal        = "The request body is not a publish: %s. Send revision, risk_digest, ticked and typed as JSON."
	publishDigestRefusal      = "The request body is not a publish: risk_digest must be the verdict's risk digest, 64 hex characters, or empty. Send the digest the check answered."
	publishCutRefusal         = "Publish refused: draft %s holds a risk on something you cannot read, so you cannot type the text that acknowledges it. Ask someone who can read everything the draft names to publish it."
	publishMovedRefusal       = "Publish refused: other publishes kept changing live state while this one was checked, so nothing was published. Publish again."
	publishBusyRefusal        = "Straza could not publish draft %s, because the database turned the change away three times while other changes were written. Nothing was published. Publish again in a moment."
	publishBuildRefusal       = "Straza could not build the policy snapshot for draft %s, so nothing was published. Read the strazad log for the cause, fix it, and publish again."
	publishFailedRefusal      = "Straza could not publish draft %s, so nothing was published. Try again, and read the strazad log if it keeps failing."
	publishUnconfirmedRefusal = "Straza could not confirm whether draft %s was published, because the database did not answer. Read it with strazactl drafts show %s, which says whether it was, before you publish it again."
)

// publishBody is the body of the publish route: the revision reviewed, the
// risk digest the publisher read, the key of every risk acknowledged, tick
// and typed, and the text typed for each typed risk by key.
type publishBody struct {
	Revision   int               `json:"revision"`
	RiskDigest string            `json:"risk_digest"`
	Ticked     []string          `json:"ticked"`
	Typed      map[string]string `json:"typed"`
}

// publishedPayload is the 200 of the publish route.
type publishedPayload struct {
	Draft    draftPayload    `json:"draft"`
	Snapshot string          `json:"snapshot"`
	Servers  []serverPayload `json:"servers"`
	Next     []string        `json:"next"`
}

// serverPayload is a server the publish created, changed or removed, with
// its status on this replica after the apply and, as its detail, the
// start's error or the health reason.
type serverPayload struct {
	Name   string `json:"name"`
	Change string `json:"change"`
	Status string `json:"status"`
	Detail string `json:"detail,omitempty"`
}

// useRefusal answers the 403 sentence that refuses c the drafts route r
// asks for, or "". On the publish route an admin API token and an agent
// read step 1's sentence before the drafts-grant refusal, so neither learns
// that a drafts grant would open the route to it. Everyone else meets
// mayUse, in accessRefusal's words.
func (c draftCaller) useRefusal(r *http.Request) string {
	if r.Pattern == publishRoute && (c.adminAPI() || !c.person) {
		return c.publisherRefusal()
	}
	if !c.mayUse(r.Method) {
		return c.accessRefusal(r.Method)
	}
	return ""
}

// handleDraftPublish publishes a draft whole in eight steps:
//
//  1. Only a person publishes, judged before the body is read.
//  2. The draft is open and was checked at the revision sent.
//  3. The verdict is computed again over live state read now.
//  4. The publisher holds the standing every item needs.
//  5. With admin.secondPerson on, a risk needs a publisher who wrote no revision.
//  6. Every risk of the verdict is acknowledged.
//  7. The commit writes the plan and closes the saved edits of the sets it writes.
//  8. This replica applies the publish.
//
// Steps 3 to 8 run under a.configMu, three runs at most when live state
// moves. The commit and the apply run on a context no client can cancel.
func (a *App) handleDraftPublish(w http.ResponseWriter, r *http.Request, c draftCaller) {
	if msg := c.publisherRefusal(); msg != "" {
		apiError(w, http.StatusForbidden, msg)
		return
	}
	var body publishBody
	if !decodeDraftBody(w, r, &body, publishBodyRefusal, false) {
		return
	}
	if !riskDigestShape(body.RiskDigest) {
		apiError(w, http.StatusBadRequest, publishDigestRefusal)
		return
	}
	row, rows, revs, ok := a.readDraft(w, r, c)
	if !ok {
		return
	}
	if msg := publishStateRefusal(row, rows, body.Revision); msg != "" {
		apiError(w, http.StatusConflict, msg)
		return
	}
	p := &publishRun{a: a, w: w, r: r, c: c, ctx: context.WithoutCancel(r.Context()), row: row, rows: rows, revs: revs, body: body}
	a.configMu.Lock()
	p.locked = true
	defer p.unlock()
	p.run()
}

// riskDigestShape reports whether digest is empty or has the shape of a
// verdict's risk digest, 64 hex characters. The draft's acks and its
// draft.publish record keep the digest the publisher reviewed, so no other
// text a client sends may reach them.
func riskDigestShape(digest string) bool {
	if digest == "" {
		return true
	}
	if len(digest) != 64 {
		return false
	}
	_, err := hex.DecodeString(digest)
	return err == nil
}

// publishStateRefusal answers the 409 sentence of step 2 for the draft row,
// whose items are rows, published at the revision sent, or "": a published
// draft names who published it and when, whatever revision was sent, a
// discarded or expired draft and another revision read as the update route
// answers them, and an item with no stamp says the revision was not
// checked.
func publishStateRefusal(row store.DraftRow, rows []store.DraftItemRow, sent int) string {
	id := strconv.FormatInt(row.ID, 10)
	switch {
	case row.State == string(drafts.StatePublished):
		at := ""
		if row.DecidedAt != nil {
			at = row.DecidedAt.UTC().Format(time.DateTime)
		}
		return fmt.Sprintf("Draft %s was published by %s at %s UTC, so nothing more was done. Read it with strazactl drafts show %s.", id, row.DecidedBy.Name, at, id)
	case row.State != string(drafts.StateOpen) || sent != row.Revision:
		return movedRefusal(row, sent)
	case slices.ContainsFunc(rows, func(it store.DraftItemRow) bool { return it.BaseOp == "" }):
		return fmt.Sprintf("Draft %s has not been checked at revision %d. Open it, read the check, and publish again.", id, row.Revision)
	}
	return ""
}

// publishRun is one publish from step 3 on: the request and its caller, a
// context no client cancels for the commit and the apply, the draft as
// step 2 read it, whether the run holds a.configMu, and the World and the
// verdict as the caller reads it from the last check.
type publishRun struct {
	a      *App
	w      http.ResponseWriter
	r      *http.Request
	c      draftCaller
	ctx    context.Context
	row    store.DraftRow
	rows   []store.DraftItemRow
	revs   []store.DraftRevisionRow
	body   publishBody
	locked bool
	world  drafts.World
	view   verdictPayload
}

// id is the draft's id as the sentences name it.
func (p *publishRun) id() string { return strconv.FormatInt(p.row.ID, 10) }

// unlock releases a.configMu when the run holds it.
func (p *publishRun) unlock() {
	if p.locked {
		p.a.configMu.Unlock()
		p.locked = false
	}
}

// run takes steps 3 to 7 and commits, and goes back to the World read when
// the commit met live state that moved or a busy database, three runs in
// all. Each run reads the World again, so the verdict, the standing,
// the second person and the acknowledgments all read the new state.
func (p *publishRun) run() {
	var end commitEnd
	var err error
	for range publishRuns {
		if !p.reread() {
			return
		}
		dp, done := p.check()
		if done {
			return
		}
		var res store.PublishResult
		res, end, err = p.a.commit(p.ctx, p.world, dp)
		switch end {
		case commitLanded:
			p.landed(dp, res.Snapshot, &dp.head)
			return
		case commitDraftMoved:
			p.draftMoved(err)
			return
		case commitBuildFailed:
			p.a.fail(p.w, p.r, http.StatusInternalServerError, fmt.Sprintf(publishBuildRefusal, p.id()), err)
			return
		case commitUnconfirmed:
			p.unconfirmed(dp, err)
			return
		}
	}
	if end == commitBusy {
		p.a.fail(p.w, p.r, http.StatusServiceUnavailable, fmt.Sprintf(publishBusyRefusal, p.id()), err)
		return
	}
	apiError(p.w, http.StatusConflict, publishMovedRefusal)
}

// reread reads the draft again under a.configMu and answers step 2's
// sentences when it moved since step 2 read it, so a publish that waited
// for the lock behind another publish of the same draft, as a double click
// sends, is told who published it rather than that its objects moved.
func (p *publishRun) reread() bool {
	row, rows, err := p.a.store.Drafts().Get(p.r.Context(), p.row.ID)
	if err != nil {
		p.a.storeFailed(p.w, p.r, draftReadRefusal, err)
		return false
	}
	if msg := publishStateRefusal(row, rows, p.body.Revision); msg != "" {
		apiError(p.w, http.StatusConflict, msg)
		return false
	}
	p.row, p.rows = row, rows
	return true
}

// check takes steps 3 to 6 over live state read now, the verdict waived in
// the publisher's view as waiveFor says, and plans the publish. It answers
// the request itself and reports done when a step refuses or a read fails.
func (p *publishRun) check() (*draftPlan, bool) {
	a, c := p.a, p.c
	d := draftOf(p.row, p.rows, authorsOf(p.revs))
	world, in, ok := a.readLive(p.w, p.r, d)
	if !ok {
		return nil, true
	}
	cd, ok := a.stampAndCheck(p.w, p.r, world, in, d, slices.Clone(p.rows))
	if !ok {
		return nil, true
	}
	cd = waiveFor(c, cd, lastIntake(cd.d, p.revs))
	view := verdictFor(c, cd.w, cd.d, cd.v)
	if len(view.Refused) > 0 {
		f := view.Refused[0].Finding
		writeJSON(p.w, http.StatusConflict, map[string]any{"error": strings.TrimSpace("Draft " + d.ID + " cannot be published: " + f.Sentence + " " + f.Fix), "verdict": view})
		return nil, true
	}
	if msg := c.standingRefusal(cd.d, cd.w, cd.in, cd.v.Needs); msg != "" {
		apiError(p.w, http.StatusForbidden, msg)
		return nil, true
	}
	if !p.secondPerson(cd.v) {
		return nil, true
	}
	if msg := acksRefusal(d.ID, cd.v, view, p.body); msg != "" {
		writeJSON(p.w, http.StatusConflict, map[string]any{"error": msg, "verdict": view})
		return nil, true
	}
	dp, err := planFor(cd.w, cd.in, cd.d, cd.rows)
	if err != nil {
		a.fail(p.w, p.r, http.StatusInternalServerError, fmt.Sprintf(publishFailedRefusal, d.ID), err)
		return nil, true
	}
	ticked, typed := ackedKeys(cd.v)
	acks, err := json.Marshal(map[string]any{"reviewedDigest": p.body.RiskDigest, "riskDigest": cd.v.RiskDigest, "ticked": ticked, "typed": typed})
	if err != nil {
		a.fail(p.w, p.r, http.StatusInternalServerError, fmt.Sprintf(publishFailedRefusal, d.ID), err)
		return nil, true
	}
	dp.plan.DraftID, dp.plan.Revision, dp.plan.Publisher, dp.plan.Acks = p.row.ID, p.row.Revision, actorOf(c.author), string(acks)
	// The draft keeps this check as its revision's, so a read of the
	// published draft answers what its publish saw.
	check := stampOf(cd.d, cd.v, cd.in.Now)
	dp.plan.Checked = &check
	dp.plan.Close = slotCloses(d.ID, p.row.Slot, dp.plan.Items)
	dp.records.publish = &publishFields{Revision: p.row.Revision, Items: cd.d.Items, RiskDigest: cd.v.RiskDigest, ReviewedDigest: p.body.RiskDigest,
		Acknowledged: ticked, Typed: typed, Proposer: p.row.Proposer, Client: c.author.Client, Reverts: d.Reverts}
	p.world, p.view = cd.w, view
	return &dp, false
}

// secondPerson takes step 5 and reports whether the publish may go
// on, answering the request itself when it may not: 409 with the code
// codeSecondPerson for a conflict, 500 for a revision list with nothing in
// it, and for a failed read of the tokens or an agent's row 503 when the
// store is out and 500 otherwise.
func (p *publishRun) secondPerson(v drafts.Verdict) bool {
	ref, err := p.a.secondPersonRefusal(p.r.Context(), p.c, p.revs, v)
	switch {
	case err != nil:
		if !p.a.answerOutage(p.w, p.r, "drafts publish", err) {
			p.a.fail(p.w, p.r, http.StatusInternalServerError, secondPersonUnreadRefusal, err)
		}
		return false
	case ref == nil:
		return true
	case ref.Kind == drafts.RefusalUnread:
		p.a.fail(p.w, p.r, http.StatusInternalServerError, ref.Sentence, errors.New("the revision list of the draft holds no revision"))
		return false
	}
	apiErrorCode(p.w, http.StatusConflict, codeSecondPerson, ref.Sentence)
	return false
}

// typedRisk reports whether the risk f is acknowledged by typing its text.
func typedRisk(f drafts.Finding) bool { return f.Ack == drafts.AckTyped && f.Typed != "" }

// acksRefusal answers the 409 sentence of step 6 for the publish of draft
// id, whose full verdict is v and whose verdict as the publisher reads it
// is view, or "": every risk of v, in verdict order, needs its key in
// ticked, and a typed risk its text in typed, equal to the risk's after
// trimming spaces and compared without case. A typed risk whose text
// verdictFor cut for this publisher refuses whatever the body holds.
// A key in ticked that names no current risk is ignored. The sentences
// quote the view, never the full verdict.
func acksRefusal(id string, v drafts.Verdict, view verdictPayload, body publishBody) string {
	ticked := setOf(body.Ticked)
	lines := make(map[string]drafts.Finding, len(view.Risks))
	for _, f := range view.Risks {
		lines[f.Key] = f.Finding
	}
	for _, f := range v.Risks {
		key, line := f.Key(), lines[f.Key()]
		typed := typedRisk(f)
		sent := strings.TrimSpace(body.Typed[key])
		switch {
		case typed && line.Typed == "":
			return fmt.Sprintf(publishCutRefusal, id)
		case !ticked[key] || typed && sent == "":
			ack := ""
			if typed {
				ack = ", typing " + line.Typed + ","
			}
			return "Publish refused: " + line.Sentence + " Acknowledge it" + ack + " and publish again."
		case typed && !strings.EqualFold(sent, strings.TrimSpace(f.Typed)):
			return fmt.Sprintf("Publish refused: the text typed for %s is not %s. Type %s exactly to acknowledge it, then publish again.", line.Object, line.Typed, line.Typed)
		}
	}
	return ""
}

// ackedKeys answers the keys of the risks of v acknowledged by a tick and
// by typing, which the draft's acks and its draft.publish record keep,
// never the typed texts.
func ackedKeys(v drafts.Verdict) (ticked, typed []string) {
	ticked, typed = []string{}, []string{}
	for _, f := range v.Risks {
		if typedRisk(f) {
			typed = append(typed, f.Key())
		} else {
			ticked = append(ticked, f.Key())
		}
	}
	return ticked, typed
}

// slotCloses names the saved edit of every set the plan writes, the slot
// policy:<set>, which Publish discards with the reason of step 7, unless
// slot, the publishing draft's own, is that slot.
func slotCloses(id, slot string, items []store.PlanItem) []store.SlotClose {
	var out []store.SlotClose
	for _, it := range items {
		name := it.Ref.Name
		if it.Ref.Kind != string(drafts.KindPolicySet) || it.After == it.Base || slot == "policy:"+name {
			continue
		}
		reason := fmt.Sprintf("draft %s published another text of %s", id, name)
		switch drafts.Op(it.Op) {
		case drafts.OpOff:
			reason = fmt.Sprintf("draft %s turned %s off", id, name)
		case drafts.OpRemove:
			reason = fmt.Sprintf("draft %s removed %s", id, name)
		}
		out = append(out, store.SlotClose{Slot: "policy:" + name, Reason: reason})
	}
	return out
}

// landed takes step 8 once the commit landed: the apply with head, or
// with none when the commit's answer was lost and the landed snapshot is
// unknown, under a.configMu, then the starts without it, and the 200. A
// failed apply does not undo the publish: applyLocked logged it and
// armed its retry, and the answer shows the servers as they stand.
func (p *publishRun) landed(dp *draftPlan, snapshot string, head *publishHead) {
	st, _ := p.a.applyLocked(p.ctx, head)
	p.unlock()
	errs := p.a.startLive(p.ctx, st, dp.announce())
	row, rows, err := p.a.store.Drafts().Get(p.ctx, p.row.ID)
	if err != nil {
		// The publish landed, so the answer shows the draft as step 2 read
		// it, marked published, rather than an error.
		p.a.log.Warn("draft publish: the published draft could not be read again", "draft", p.id(), "err", err)
		now := time.Now().UTC()
		row, rows = p.row, p.rows
		row.State, row.PublishedSnapshot, row.DecidedAt, row.DecidedBy, row.UpdatedAt = string(drafts.StatePublished), snapshot, &now, actorOf(p.c.author), now
	}
	out := publishedPayload{Draft: draftAnswer(p.c, &p.world, row, rows, authorsOf(p.revs)), Snapshot: snapshot, Servers: []serverPayload{}, Next: []string{}}
	for _, s := range dp.servers {
		out.Servers = append(out.Servers, p.serverOf(s, st, errs))
	}
	for _, f := range p.view.Warnings {
		if strings.HasPrefix(f.Code, "ready.") && f.Fix != "" && !slices.Contains(out.Next, f.Fix) {
			out.Next = append(out.Next, f.Fix)
		}
	}
	writeJSON(p.w, http.StatusOK, out)
}

// serverOf is the server s of the answer as this replica runs it after the
// apply: removed for a removed server, and otherwise the manager's status
// and health reason, else the status its row had in the state the apply
// read, with the start's error as the detail when the start failed. A
// server the publisher may not read keeps no detail.
func (p *publishRun) serverOf(s serverChange, st store.LiveState, errs map[string]error) serverPayload {
	out := serverPayload{Name: s.name, Change: s.change, Status: "removed"}
	if s.change != serverRemoved {
		out.Status = manager.StatusPending
		for _, row := range st.Apps {
			if row.Name == s.name {
				out.Status = row.Status
			}
		}
		if v, ok := p.a.manager.View(s.name); ok {
			out.Status, out.Detail = v.Status, v.Detail
		}
		if err := errs[s.name]; err != nil {
			out.Detail = err.Error()
		}
	}
	if !p.c.readsObject(p.world, drafts.Item{Kind: drafts.KindApp, Name: s.name}) {
		out.Detail = ""
	}
	return out
}

// draftMoved answers a commit that found the draft no longer open at the
// plan's revision with step 2's sentences, from the row as it stands. The
// replica whose commit landed applied it, and this one applies it from its
// event.
func (p *publishRun) draftMoved(cause error) {
	row, rows, err := p.a.store.Drafts().Get(p.ctx, p.row.ID)
	if err != nil {
		p.a.storeFailed(p.w, p.r, draftReadRefusal, errors.Join(cause, err))
		return
	}
	msg := publishStateRefusal(row, rows, p.row.Revision)
	if msg == "" {
		msg = movedRefusal(row, p.row.Revision)
	}
	apiError(p.w, http.StatusConflict, msg)
}

// unconfirmed answers a Publish error that is no conflict, after which the
// transaction may have committed or not. It reads the draft again.
// Still open at the plan's revision, nothing was published. Published by
// this caller at that revision, the commit landed, and step 8 runs with no
// head. Decided otherwise, step 2's sentences answer. When the read fails
// too, the outcome is unknown, so the apply runs anyway, since it
// converges either way, and the answer says to read the draft first.
func (p *publishRun) unconfirmed(dp *draftPlan, cause error) {
	id := p.id()
	row, rows, err := p.a.store.Drafts().Get(p.ctx, p.row.ID)
	switch {
	case err != nil:
		st, _ := p.a.applyLocked(p.ctx, nil)
		p.unlock()
		p.a.startLive(p.ctx, st, nil)
		if !p.a.answerOutage(p.w, p.r, "drafts publish", err) {
			p.a.fail(p.w, p.r, http.StatusInternalServerError, fmt.Sprintf(publishUnconfirmedRefusal, id, id), errors.Join(cause, err))
		}
	case row.State == string(drafts.StateOpen) && row.Revision == dp.plan.Revision:
		if !p.a.answerOutage(p.w, p.r, "drafts publish", cause) {
			p.a.fail(p.w, p.r, http.StatusInternalServerError, fmt.Sprintf(publishFailedRefusal, id), cause)
		}
	case row.State == string(drafts.StatePublished) && row.Revision == dp.plan.Revision && p.c.isAuthor(row.DecidedBy.ID, row.DecidedBy.Via):
		p.a.log.Warn("draft publish: the commit landed though its answer was lost", "draft", id, "err", cause)
		p.landed(dp, row.PublishedSnapshot, nil)
	default:
		msg := publishStateRefusal(row, rows, dp.plan.Revision)
		if msg == "" {
			msg = movedRefusal(row, dp.plan.Revision)
		}
		apiError(p.w, http.StatusConflict, msg)
	}
}
