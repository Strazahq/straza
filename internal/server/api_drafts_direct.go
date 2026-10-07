package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/strazahq/straza/internal/drafts"
	"github.com/strazahq/straza/internal/store"
)

// The sentences of a direct route whose publish could not land.
// A direct route cannot name its draft, because the draft is minted inside
// the transaction that was turned away.
const (
	directMovedRefusal       = "Straza could not make this change, because other changes kept landing at the same moment. Nothing was changed. Try again."
	directBusyRefusal        = "Straza could not make this change, because the database turned it away three times while other changes were written. Nothing was changed. Try again in a moment."
	directUnconfirmedRefusal = "Straza could not confirm whether this change was made, because the database did not answer. Read the object again, and make the change again only if it is missing."
)

// directRoute is what a direct admin route hands publishOne: its own
// refusals in today's order, its words, and its failure sentence.
type directRoute struct {
	// prepare runs the route's own reads and refusals over live state w,
	// which publishOne read under a.configMu with the generation first, for
	// the caller author. It names the change, or answers the request itself
	// and reports false, on a refusal or on a path that publishes nothing
	// (the save of a live set, a no-op the route answers itself).
	// publishOne calls it again on every run, so it keeps no state.
	prepare func(w drafts.World, author drafts.Principal) (directChange, bool)
	// answer gives the status and words of a Check code the route answered
	// before drafts. ok false leaves the 409 "{sentence} {fix}". A
	// nil answer maps no code.
	answer func(f drafts.Finding) (status int, msg string, ok bool)
	// bodyStatus answers every intake refusal, the secret scan included:
	// 422 on POST /v1/admin/apps and 400 elsewhere.
	bodyStatus int
	// failed is the route's own 500 sentence for a failed read of live
	// state, a refused Build on an unmoved base, or a lost plan, with err
	// its cause when the route's sentence names one.
	failed func(err error) string
}

// directChange is what a direct route publishes: one item as a new draft
// of the api door by the caller, with the items the route publishes
// alongside it, or an open draft as it is (a set's saved edit), and the
// slot drafts the publish ends.
type directChange struct {
	Item drafts.Item
	// Also are the items the route publishes with Item in the same draft
	// and the same transaction, such as the sets a role's deletion turns
	// off. They are empty for a route that changes one object.
	Also  []drafts.Item
	Draft *directDraft
	Close []store.SlotClose
}

// directDraft is an open draft a direct route publishes as it is: its row
// at the revision read, its stamped items and its revisions.
type directDraft struct {
	Row  store.DraftRow
	Rows []store.DraftItemRow
	Revs []store.DraftRevisionRow
}

// directResult is what the publish wrote: the outcome of the item (zero
// for an existing draft with several, or for a change with items
// alongside), the outcome of every item, the active snapshot after it, the
// World the last run checked, and the start error of each server it
// announced that did not start here. Noop is set when a one-item draft
// equals live and nothing was written.
type directResult struct {
	Item     store.ItemOutcome
	Outcome  store.PublishOutcome
	Snapshot string
	World    drafts.World
	Noop     bool
	Started  map[string]error
}

// directRun is one call of publishOne: the request, the route, the caller
// as the draft's author, a context no client cancels for the commit and
// the apply, and whether it holds a.configMu.
type directRun struct {
	a      *App
	w      http.ResponseWriter
	r      *http.Request
	route  directRoute
	author drafts.Principal
	// caller is the author's read standing, under which the waiver runs,
	// because the route's guard asks for the area's write grant alone.
	caller draftCaller
	ctx    context.Context
	locked bool
}

// directCheck is one run's draft checked against live state: the World
// the check read, the draft with its stamped rows, the verdict, when it was
// checked, and the plan.
type directCheck struct {
	w    drafts.World
	d    drafts.Draft
	rows []store.DraftItemRow
	v    drafts.Verdict
	at   time.Time
	dp   draftPlan
}

// publishOne publishes the change of a direct admin route. It
// takes a.configMu, reads the World with the generation first, runs
// route.prepare, intake whole, Check (steps 1 to 4 always, 5 to 7 under
// admin.secondPerson), the second-person rule, planFor and commit,
// three runs on a moved or busy commit, then applyLocked and startLive. It
// answers every refusal and failure itself and reports false. On true the
// route writes its own success answer from the result.
func (a *App) publishOne(w http.ResponseWriter, r *http.Request, route directRoute) (directResult, bool) {
	author, err := a.directAuthor(r.Context())
	var caller draftCaller
	if err == nil {
		caller, err = a.proposerCaller(r.Context(), author)
	}
	if err != nil {
		a.fail(w, r, http.StatusInternalServerError, route.failed(err), err)
		return directResult{}, false
	}
	caller.Change = true
	p := &directRun{a: a, w: w, r: r, route: route, author: author, caller: caller, ctx: context.WithoutCancel(r.Context())}
	a.configMu.Lock()
	p.locked = true
	defer p.unlock()
	return p.publish()
}

// unlock releases a.configMu when the run holds it.
func (p *directRun) unlock() {
	if p.locked {
		p.a.configMu.Unlock()
		p.locked = false
	}
}

// publish takes up to three runs: each reads the World again, prepares and
// checks the change, and commits it, and a commit that met moved live
// state, a busy database or a draft that moved goes back to the read.
func (p *directRun) publish() (directResult, bool) {
	var end commitEnd
	var err error
	for range publishRuns {
		live, rerr := p.a.liveWorld(p.r.Context())
		if rerr != nil {
			p.failed(rerr)
			return directResult{}, false
		}
		ch, ok := p.route.prepare(live, p.author)
		if !ok {
			return directResult{}, false
		}
		dc, ok := p.check(live, ch)
		if !ok {
			return directResult{}, false
		}
		if ch.Draft == nil && len(ch.Close) == 0 && !slices.ContainsFunc(dc.dp.plan.Items, func(it store.PlanItem) bool { return it.After != it.Base }) {
			return directResult{Snapshot: dc.w.SnapshotID, World: dc.w, Noop: true}, true
		}
		if !p.fill(&dc, ch) {
			return directResult{}, false
		}
		var res store.PublishResult
		res, end, err = p.a.commit(p.ctx, dc.w, &dc.dp)
		switch end {
		case commitLanded:
			return p.landed(dc, res), true
		case commitBuildFailed:
			p.failed(err)
			return directResult{}, false
		case commitUnconfirmed:
			p.unconfirmed(err)
			return directResult{}, false
		}
	}
	if end == commitBusy {
		p.a.fail(p.w, p.r, http.StatusServiceUnavailable, directBusyRefusal, err)
		return directResult{}, false
	}
	apiError(p.w, http.StatusConflict, directMovedRefusal)
	return directResult{}, false
}

// failed answers 500 with the route's own sentence for err.
func (p *directRun) failed(err error) {
	p.a.fail(p.w, p.r, http.StatusInternalServerError, p.route.failed(err), err)
}

// check reads the change against live, which this run read: intake whole,
// the World completed for the draft, the stamps of a new item, Check with
// the refusals only while admin.secondPerson is off, the
// second-person rule, and the plan. It answers the request itself and
// reports false when a step refuses or a read fails.
func (p *directRun) check(live drafts.World, ch directChange) (directCheck, bool) {
	var d drafts.Draft
	var rows []store.DraftItemRow
	if ch.Draft != nil {
		rows = slices.Clone(ch.Draft.Rows)
		d = draftOf(ch.Draft.Row, rows, authorsOf(ch.Draft.Revs))
	} else {
		rows = rowsOf(append([]drafts.Item{ch.Item}, ch.Also...))
		canonicalRoles(rows, nil)
		d = drafts.Draft{Revision: 1, State: drafts.StateOpen, Door: drafts.DoorAPI, Authors: []drafts.Principal{p.author}, Items: itemsOf(rows)}
	}
	refused, waived := p.caller.readerWaived(live, d, intakeOf(d, p.author))
	if len(refused) > 0 {
		apiError(p.w, p.route.bodyStatus, directWords(refused[0], d))
		return directCheck{}, false
	}
	// worldFor fills the credential facts into the map live holds, so each
	// run reads its own World and hands it to worldFor once.
	cw, in, err := p.a.worldFor(p.r.Context(), live, d)
	if err == nil && ch.Draft == nil {
		err = p.a.stampRows(p.r.Context(), cw, rows)
	}
	if err != nil {
		p.failed(err)
		return directCheck{}, false
	}
	d.Items = itemsOf(rows)
	p.a.keptFacts(cw, d, in.Apps)
	in.RefusalsOnly = !p.a.cfg.Admin.SecondPerson
	in.Contacted = map[string][]string{}
	for object, o := range contactedOf(rows) {
		in.Contacted[object] = o.Tools
	}
	v := p.caller.readerVerdict(cw, d, drafts.Check(cw, d, in), waived)
	if len(v.Refused) > 0 {
		p.refuse(v.Refused[0])
		return directCheck{}, false
	}
	if msg := p.a.directSecondPersonRefusal(p.ownVerdict(cw, d, in, ch, v)); msg != "" {
		if ch.Draft != nil {
			msg = savedEditSecondPersonRefusal(ch.Draft)
		}
		apiError(p.w, http.StatusConflict, msg)
		return directCheck{}, false
	}
	dp, err := planFor(cw, in, d, rows)
	if err != nil {
		p.failed(err)
		return directCheck{}, false
	}
	return directCheck{w: cw, d: d, rows: rows, v: v, at: in.Now, dp: dp}, true
}

// ownVerdict is the verdict the second-person rule judges: v, the draft's
// own, for a route that changes one object, and the verdict of the
// route's item alone when the route publishes items alongside it, checked
// again over the same World and facts. Those items settle what the change
// leaves behind, such as a set nobody matches once its role is gone, so
// they add no second-person need, and the answer stays the one the change
// alone gets.
func (p *directRun) ownVerdict(cw drafts.World, d drafts.Draft, in drafts.CheckInput, ch directChange, v drafts.Verdict) drafts.Verdict {
	if !p.a.cfg.Admin.SecondPerson || len(ch.Also) == 0 {
		return v
	}
	own := d
	own.Items = d.Items[:1]
	return drafts.Check(cw, own, in)
}

// refuse answers the Check refusal f: in the route's words when it maps
// f's code, through a.fail for a 5xx so its log record is written with the
// finding's sentence as the cause, else 409 "{sentence} {fix}".
func (p *directRun) refuse(f drafts.Finding) {
	if p.route.answer != nil {
		if status, msg, ok := p.route.answer(f); ok {
			if status >= http.StatusInternalServerError {
				p.a.fail(p.w, p.r, status, msg, errors.New(f.Sentence))
				return
			}
			apiError(p.w, status, msg)
			return
		}
	}
	apiError(p.w, http.StatusConflict, findingWords(f))
}

// findingWords is a refusal as a direct route answers it, its sentence and
// its fix.
func findingWords(f drafts.Finding) string {
	return strings.TrimSpace(f.Sentence + " " + f.Fix)
}

// directWords is the intake refusal f of the draft d a direct route made,
// as the route answers it. The caller changed one object and sent no
// draft, so the finding is worded for that change when d holds one item.
func directWords(f drafts.Finding, d drafts.Draft) string {
	if len(d.Items) == 1 {
		f = f.ForChange(d.Items[0])
	}
	return findingWords(f)
}

// fill completes the plan of dc for its commit: a new one-item draft of
// the api door by the caller, checked at revision 1, or the open draft at
// the revision read, which keeps this check as its revision's and records
// its draft.publish with no acknowledgment, since that draft has its
// draft.create. Every direct publish is published by the caller and ends
// the slots the route names.
func (p *directRun) fill(dc *directCheck, ch directChange) bool {
	ackKeys := []string{}
	acks, err := json.Marshal(map[string]any{"reviewedDigest": "", "riskDigest": dc.v.RiskDigest, "ticked": ackKeys, "typed": ackKeys})
	if err != nil {
		p.failed(err)
		return false
	}
	plan := &dc.dp.plan
	plan.Publisher, plan.Acks, plan.Close = actorOf(p.author), string(acks), ch.Close
	stamp := stampOf(dc.d, dc.v, dc.at)
	if dd := ch.Draft; dd != nil {
		plan.DraftID, plan.Revision, plan.Checked = dd.Row.ID, dd.Row.Revision, &stamp
		dc.dp.records.publish = &publishFields{Revision: dd.Row.Revision, Items: dc.d.Items, RiskDigest: dc.v.RiskDigest,
			Acknowledged: ackKeys, Typed: ackKeys, Proposer: dd.Row.Proposer, Client: p.author.Client, Reverts: dc.d.Reverts}
		return true
	}
	plan.New = &store.DraftNew{
		Row: store.DraftRow{Door: string(drafts.DoorAPI), CheckedRevision: 1, CheckedAt: &stamp.At, CheckedSnapshot: stamp.Snapshot,
			CheckCounts: stamp.Counts, AgentVerdict: stamp.AgentVerdict},
		Items: dc.rows,
		Rev:   store.DraftRevisionRow{Author: actorOf(p.author), Door: string(drafts.DoorAPI), Digest: revisionDigest(dc.d.Items)},
	}
	return true
}

// landed applies a publish that landed with its head under a.configMu,
// releases the lock, starts the servers it announced, and answers what it
// wrote.
func (p *directRun) landed(dc directCheck, res store.PublishResult) directResult {
	st, _ := p.a.applyLocked(p.ctx, &dc.dp.head)
	p.unlock()
	out := directResult{Outcome: res.PublishOutcome, Snapshot: res.Snapshot, World: dc.w,
		Started: p.a.startLive(p.ctx, st, dc.dp.announce())}
	if len(dc.d.Items) == 1 {
		it := dc.d.Items[0]
		for _, o := range res.Items {
			if !o.Implied && o.Ref == (store.ObjectRef{Kind: string(it.Kind), Name: it.Name}) {
				out.Item = o
			}
		}
	}
	return out
}

// unconfirmed answers a Publish error that is no conflict, after which the
// transaction may have committed or not. A direct route cannot read its
// draft back, since the draft was minted inside that transaction, so the
// apply runs anyway, because it converges either way, and the answer says
// to read the object before making the change again.
func (p *directRun) unconfirmed(cause error) {
	st, _ := p.a.applyLocked(p.ctx, nil)
	p.unlock()
	p.a.startLive(p.ctx, st, nil)
	if !p.a.answerOutage(p.w, p.r, "direct publish", cause) {
		p.a.fail(p.w, p.r, http.StatusInternalServerError, directUnconfirmedRefusal, cause)
	}
}

// directAuthor reads the caller of a direct route as a drafts.Principal:
// the actor of the context and, on a login or session, the user row, so a
// user who is not a person reads as an agent with its sponsor. The save of
// a live set's text calls it too.
func (a *App) directAuthor(ctx context.Context) (drafts.Principal, error) {
	act, ok := actorFrom(ctx)
	if !ok || act.ID == "" {
		return drafts.Principal{}, errors.New("the request carries no admin actor")
	}
	if act.Via == laneAdminAPI {
		return drafts.Principal{UserID: act.ID, Username: act.Name, Via: act.Via, Client: clientAdminAPI}, nil
	}
	u, err := a.store.Users().GetByID(ctx, act.ID)
	if err != nil {
		return drafts.Principal{}, err
	}
	client := clientLogin
	if act.Via == laneSession {
		client = laneSession
	}
	out := drafts.Principal{UserID: u.ID, Username: u.Username, Agent: !personUser(u), Via: act.Via, Client: client}
	if out.Agent && u.Sponsor != "" {
		out.SponsorName = u.Sponsor
		sponsor, accountable, err := accountableSponsor(u, func(name string) (store.User, error) { return a.store.Users().GetByUsername(ctx, name) })
		if err != nil {
			return drafts.Principal{}, err
		}
		if accountable {
			out.SponsorID = sponsor.ID
		}
	}
	return out, nil
}

// directIntake runs intake whole over d for the save path that stores a
// draft without publishing, answers bodyStatus with the first refusal in
// directWords and reports false. The author of the revision being
// written is the last of d.Authors, so the caller passes d with itself
// last.
func (a *App) directIntake(w http.ResponseWriter, d drafts.Draft, bodyStatus int) bool {
	var author drafts.Principal
	if len(d.Authors) > 0 {
		author = d.Authors[len(d.Authors)-1]
	}
	if fs := intakeOf(d, author); len(fs) > 0 {
		apiError(w, bodyStatus, directWords(fs[0], d))
		return false
	}
	return true
}
