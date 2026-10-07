package server

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/strazahq/straza/internal/config"
	"github.com/strazahq/straza/internal/drafts"
	"github.com/strazahq/straza/internal/policy"
	"github.com/strazahq/straza/internal/store"
)

// maxPolicyBody caps uploaded PolicySet YAML.
const maxPolicyBody = 1 << 20

type policyPayload struct {
	ID        string         `json:"id"`
	Name      string         `json:"name"`
	Priority  int            `json:"priority"`
	Status    string         `json:"status"`
	YAML      string         `json:"yaml,omitempty"`
	UpdatedAt string         `json:"updated_at,omitempty"` // RFC3339
	Summary   *policySummary `json:"summary,omitempty"`
	// Drift is true while the set has an open saved edit, whose text,
	// priority, summary and time the payload then carries in place of the
	// row's. The saved edit is not live until activate publishes it.
	Drift bool `json:"drift,omitempty"`
}

// policySummary is the server-computed decomposition of a stored set, from
// the SAME parse the server trusts (validate uses it too), so the console
// list can render truth even where its client-side mini-parser cannot
// decompose the YAML. Absent (nil) when the stored source no longer parses:
// absence is the honest signal, never an invented shape.
type policySummary struct {
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"` // metadata.description (0.78.0: the list is summary-only, so it rides here)
	Priority    int            `json:"priority"`
	Rules       int            `json:"rules"`
	Postures    map[string]int `json:"postures,omitempty"`   // deny|allow|confirm|hold|ticket|serverCheck|classify -> count
	MatchRoles  []string       `json:"matchRoles,omitempty"` // spec.match.roles
	MatchOther  bool           `json:"matchOther,omitempty"` // users/identity selectors present
	Capture     string         `json:"capture,omitempty"`    // verbatim|redact when conversations are captured
	// Lanes maps each governed surface (mcp|shell|files|net|other) to its
	// posture counts (0.78.0). A tool-unscoped rule
	// governs every lane, so lane sums can exceed the rule count by design.
	Lanes map[string]map[string]int `json:"lanes,omitempty"`
}

// rulePosture names a rule's decision posture in the Builder's vocabulary:
// mode wins (a confirm/approve/serverCheck/classify rule is that posture,
// with approve split by class), a mode-less rule is its effect.
func rulePosture(r policy.Rule) string {
	switch r.Mode {
	case policy.ModeConfirm:
		return "confirm"
	case policy.ModeServerCheck:
		return "serverCheck"
	case policy.ModeClassify:
		return "classify"
	case policy.ModeApprove:
		if r.Approve != nil && r.Approve.Class == policy.ClassTicket {
			return "ticket"
		}
		return "hold"
	}
	if r.Effect == policy.EffectDeny {
		return "deny"
	}
	return "allow"
}

func summarizePolicy(doc policy.Document) *policySummary {
	s := &policySummary{
		Name:        doc.Metadata.Name,
		Description: doc.Metadata.Description,
		Priority:    doc.Spec.Priority,
		Rules:       len(doc.Spec.Rules),
		Lanes:       setLanes(doc),
	}
	if len(doc.Spec.Rules) > 0 {
		s.Postures = map[string]int{}
		for _, r := range doc.Spec.Rules {
			s.Postures[rulePosture(r)]++
		}
	}
	if len(doc.Spec.Match.Roles) > 0 {
		s.MatchRoles = doc.Spec.Match.Roles
	}
	s.MatchOther = len(doc.Spec.Match.Users) > 0 || doc.Spec.Match.Identity != nil
	if doc.Spec.Capture != nil && doc.Spec.Capture.Conversations {
		s.Capture = doc.Spec.Capture.Mode
		if s.Capture == "" {
			s.Capture = policy.CaptureModeVerbatim
		}
	}
	return s
}

// policyLookupFailed is the 500 sentence of every policy route whose read
// of live state or of a row failed.
const policyLookupFailed = "lookup failed"

// policyUpdateFailed is the 500 sentence of a save of a live set whose
// write of its saved edit failed.
const policyUpdateFailed = "update failed"

// policyRoute is the direct route of a policy route: every
// intake refusal answers 400, and a failed read or publish answers
// "lookup failed".
func policyRoute(prepare func(drafts.World, drafts.Principal) (directChange, bool)) directRoute {
	return directRoute{prepare: prepare, bodyStatus: http.StatusBadRequest, failed: func(error) string { return policyLookupFailed }}
}

// policyRow reads the row of the set name under the caller's lock, and
// answers the request itself when it cannot: 404 for no such set and 500
// for a failed read. With missingOK, no row answers the zero row.
func (a *App) policyRow(w http.ResponseWriter, r *http.Request, name string, missingOK bool) (store.PolicySet, bool) {
	ps, err := a.store.Policies().GetByName(r.Context(), name)
	switch {
	case errors.Is(err, store.ErrNotFound) && missingOK:
		return store.PolicySet{}, true
	case errors.Is(err, store.ErrNotFound):
		apiError(w, http.StatusNotFound, "no such policy set")
		return store.PolicySet{}, false
	case err != nil:
		a.fail(w, r, http.StatusInternalServerError, policyLookupFailed, err)
		return store.PolicySet{}, false
	}
	return ps, true
}

// handlePolicyApply stores a PolicySet from YAML (validated server-side).
// A new name is published off, so it governs nothing until it is
// activated, and a new text of a set that is off is published off too. The
// new text of a set that is on becomes that set's saved edit, its slot
// draft, and the set goes on deciding with the text it was published with
// until activate publishes the edit. Every change after the parse is
// a one-item publish, which the stored text sent again skips.
func (a *App) handlePolicyApply(w http.ResponseWriter, r *http.Request) {
	raw, err := io.ReadAll(io.LimitReader(r.Body, maxPolicyBody))
	if err != nil {
		apiError(w, http.StatusBadRequest, "could not read body")
		return
	}
	// Parse ALL documents and refuse more than one: Parse alone takes the
	// first doc while the WHOLE file would become that set's stored YAML, a
	// policy that looks applied and never enforces. One document per request is
	// the contract; strazactl splits multi-doc files client-side.
	docs, err := policy.ParseAll(raw)
	if err != nil {
		apiError(w, http.StatusBadRequest, err.Error())
		return
	}
	if len(docs) != 1 {
		apiError(w, http.StatusBadRequest, fmt.Sprintf(
			"body contains %d PolicySet documents. Apply exactly one per request (strazactl policy apply splits multi-document files)", len(docs)))
		return
	}
	doc := docs[0]
	name, text := doc.Metadata.Name, string(raw)
	var row store.PolicySet
	res, ok := a.publishOne(w, r, policyRoute(func(live drafts.World, author drafts.Principal) (directChange, bool) {
		var read bool
		if row, read = a.policyRow(w, r, name, true); !read {
			return directChange{}, false
		}
		if _, on := live.Policies[name]; on {
			a.saveEdit(w, r, live, author, row, doc, text)
			return directChange{}, false
		}
		return directChange{Item: drafts.Item{Kind: drafts.KindPolicySet, Name: name, Op: drafts.OpOff, Doc: text}}, true
	}))
	if !ok {
		return
	}
	out := policyPayload{ID: row.ID, Name: name, Priority: doc.Spec.Priority, Status: "draft"}
	status := http.StatusOK
	if res.Item.Created {
		out.ID, status = res.Item.ID, http.StatusCreated
	}
	writeJSON(w, status, out)
}

// handlePolicyDelete removes a stored PolicySet by name through a one-item
// publish. A set that is live, by its stored status or because the
// published snapshot still carries it, is refused with 409: deleting must
// never change enforcement as a side effect, so the deliberate lane is to
// turn it off, which publishes its removal, and then delete it. A saved
// edit left open on a set that is off, which only a replica of the release
// before drafts can leave, is closed with the removal.
func (a *App) handlePolicyDelete(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	_, ok := a.publishOne(w, r, policyRoute(func(live drafts.World, _ drafts.Principal) (directChange, bool) {
		ps, ok := a.policyRow(w, r, name, false)
		if !ok {
			return directChange{}, false
		}
		if ps.Status == "active" {
			apiError(w, http.StatusConflict, "the policy set is live. Turn it off first, then delete")
			return directChange{}, false
		}
		if _, on := live.Policies[name]; on {
			apiError(w, http.StatusConflict, fmt.Sprintf(
				"the policy set %s is still published and deciding, although its stored status says draft. Turn it off first, then delete", name))
			return directChange{}, false
		}
		return directChange{Item: drafts.Item{Kind: drafts.KindPolicySet, Name: name, Op: drafts.OpRemove},
			Close: []store.SlotClose{{Slot: "policy:" + name, Reason: name + " was deleted"}}}, true
	}))
	if ok {
		w.WriteHeader(http.StatusNoContent)
	}
}

// The reasons a saved edit is closed with.
const (
	savedEqualReason = "the saved text equals the published text"
	savedStaleReason = "the published text changed after this edit was saved"
)

// saveEdit stores text as the saved edit of the live set of row, the slot
// draft policy:<name> of the api door by author, and answers today's 200.
// Nothing is published. Intake runs first, so a secret is refused
// with 400. A text equal to the published one discards the open saved edit
// and stores nothing. A saved edit whose base another publish replaced is
// discarded and a new one opens, stamped on the published text, which is
// the base of every new saved edit. A write that meets another
// replica's runs again from a new read of the published texts, three runs
// in all, so the rerun judges the saved edit by what is live now. A failed
// read answers "lookup failed" and a failed write "update failed". The
// caller holds a.configMu.
func (a *App) saveEdit(w http.ResponseWriter, r *http.Request, live drafts.World, author drafts.Principal, row store.PolicySet, doc policy.Document, text string) {
	name := doc.Metadata.Name
	item := drafts.Item{Kind: drafts.KindPolicySet, Name: name, Op: drafts.OpPut, Doc: text}
	if !a.directIntake(w, drafts.Draft{Revision: 1, State: drafts.StateOpen, Door: drafts.DoorAPI, Authors: []drafts.Principal{author},
		Items: []drafts.Item{item}}, http.StatusBadRequest) {
		return
	}
	sets := live.Policies
	for run := range publishRuns {
		if run > 0 {
			var fresh drafts.World
			if err := a.readPolicies(r.Context(), &fresh); err != nil {
				a.fail(w, r, http.StatusInternalServerError, policyLookupFailed, err)
				return
			}
			sets = fresh.Policies
		}
		published, on := sets[name]
		if !on {
			// Another replica turned the set off since the route's read, and
			// the change is now a publish, which a new request makes.
			break
		}
		stamped := store.DraftItemRow{Kind: string(item.Kind), Name: name, Op: string(item.Op), Doc: text,
			Base:   store.FingerprintPolicySet(store.PolicySet{Name: name, Status: "active", YAMLSource: published.Text}),
			BaseOp: string(drafts.OpPut), BaseDoc: published.Text}
		saved, failed, err := a.saveOnce(r.Context(), author, stamped, text == published.Text)
		switch {
		case errors.Is(err, store.ErrConflict):
			continue
		case err != nil:
			a.fail(w, r, http.StatusInternalServerError, failed, err)
			return
		}
		if saved != nil {
			a.recordRevision(r.Context(), saved.action, strconv.FormatInt(saved.id, 10), saved.revision, drafts.DoorAPI, []drafts.Item{item}, "")
		}
		writeJSON(w, http.StatusOK, policyPayload{ID: row.ID, Name: name, Priority: doc.Spec.Priority, Status: "active"})
		return
	}
	apiError(w, http.StatusConflict, directMovedRefusal)
}

// savedRevision is the revision a save wrote: its record's action, the
// draft and the revision.
type savedRevision struct {
	action   string
	id       int64
	revision int
}

// saveOnce is one run of saveEdit over the open saved edit of the set of
// item as it reads now. It answers the revision it wrote, nil when equal
// asks it to write none, and store.ErrConflict when another replica moved
// the saved edit meanwhile, after which nothing of this run was written.
// Any other error comes with the 500 sentence of the read or the write
// that failed.
func (a *App) saveOnce(ctx context.Context, author drafts.Principal, item store.DraftItemRow, equal bool) (*savedRevision, string, error) {
	slot := "policy:" + item.Name
	open, held, err := a.store.Drafts().BySlot(ctx, slot)
	found := err == nil
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return nil, policyLookupFailed, err
	}
	stale := found && (len(held) != 1 || held[0].Base != item.Base)
	if found && (equal || stale) {
		reason := savedEqualReason
		if !equal {
			reason = savedStaleReason
		}
		closed, err := a.store.Drafts().Close(ctx, open.ID, open.Revision, "discarded", actorOf(author), reason, time.Now())
		if err != nil {
			return nil, policyUpdateFailed, err
		}
		if !closed {
			return nil, "", store.ErrConflict
		}
		a.recordDiscard(ctx, strconv.FormatInt(open.ID, 10), open.Revision, reason)
		found = false
	}
	if equal {
		return nil, "", nil
	}
	rev := store.DraftRevisionRow{Author: actorOf(author), Door: string(drafts.DoorAPI),
		Digest: revisionDigest([]drafts.Item{{Kind: drafts.Kind(item.Kind), Name: item.Name, Op: drafts.Op(item.Op), Doc: item.Doc}})}
	if found {
		updated, err := a.store.Drafts().Revise(ctx, open.ID, store.DraftRevise{From: open.Revision, Items: []store.DraftItemRow{item}, Rev: rev})
		if err != nil {
			return nil, policyUpdateFailed, err
		}
		return &savedRevision{"draft.update", updated.ID, updated.Revision}, "", nil
	}
	created, err := a.store.Drafts().Create(ctx, store.DraftRow{Door: string(drafts.DoorAPI), Slot: slot}, []store.DraftItemRow{item}, rev)
	if err != nil {
		return nil, policyUpdateFailed, err
	}
	return &savedRevision{"draft.create", created.ID, 1}, "", nil
}

// closedSavedEditRefusal is the 409 of an activate whose saved edit another
// replica closed after this activate read it.
const closedSavedEditRefusal = "The saved edit of %s was closed while it was being activated: %s. Nothing was published. " +
	"Read the set with strazactl policy show %s, save your text again if you still need it, then activate it."

// revisedSavedEditRefusal is the 409 of an activate whose saved edit
// another replica saved again after this activate read it.
const revisedSavedEditRefusal = "The saved edit of %s was saved again while it was being activated, and it is now at revision %d. " +
	"Nothing was published. Read the set with strazactl policy show %s, check the text, then activate it again."

// staleSavedEditRefusal is the 409 of an activate whose saved edit was
// saved on a text another publish has replaced since (draft.stale).
const staleSavedEditRefusal = "The saved edit of %s was made on a text that another publish has replaced since, " +
	"so activating it would undo that change. Open the set, save your text again, then activate it."

// policyActivation is the 200 answer of the activate route: the status
// asked for, the snapshot running after the call, and whether the call
// changed the live state. Changed is false when the set was already in
// the asked state and nothing was written.
type policyActivation struct {
	Status   string `json:"status"`
	Snapshot string `json:"snapshot"`
	Changed  bool   `json:"changed"`
}

// handlePolicyActivate turns one set on or off through a publish of that
// set alone. On publishes the set's saved edit as it is when one is open,
// else the stored text of a set that is off, after the activation gates
// pass on that text. Off publishes the set off with the saved edit's text
// when one is open, else with its published text, and closes the saved
// edit. A set already in the asked state with no saved edit answers
// 200 with the snapshot running and changed false, and writes nothing.
// The status commits with the snapshot, and the answer names the
// snapshot active after the publish.
func (a *App) handlePolicyActivate(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	var req struct {
		Status string `json:"status"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		req.Status = "active"
	}
	if req.Status != "active" && req.Status != "draft" {
		apiError(w, http.StatusBadRequest, "status must be active or draft")
		return
	}
	// tried is the saved edit an earlier run of this request published, at
	// triedRev, so a run that finds it gone, replaced or revised tells the
	// caller what became of it rather than publishing a text it never read.
	var tried int64
	var triedRev int
	var text string
	route := policyRoute(func(live drafts.World, _ drafts.Principal) (directChange, bool) {
		row, ok := a.policyRow(w, r, name, false)
		if !ok {
			return directChange{}, false
		}
		saved, held, err := a.store.Drafts().BySlot(r.Context(), "policy:"+name)
		found := err == nil && len(held) == 1
		if err != nil && !errors.Is(err, store.ErrNotFound) {
			a.fail(w, r, http.StatusInternalServerError, policyLookupFailed, err)
			return directChange{}, false
		}
		_, on := live.Policies[name]
		switch {
		case tried != 0 && (!found || saved.ID != tried || saved.Revision != triedRev):
			a.activatedMeanwhile(w, r, name, tried, req.Status, live.SnapshotID)
			return directChange{}, false
		case !found && on == (req.Status == "active"):
			writeJSON(w, http.StatusOK, policyActivation{Status: req.Status, Snapshot: live.SnapshotID})
			return directChange{}, false
		case req.Status == "draft":
			text = live.Policies[name].Text
			if found {
				text = held[0].Doc
			}
			return directChange{Item: drafts.Item{Kind: drafts.KindPolicySet, Name: name, Op: drafts.OpOff, Doc: text},
				Close: []store.SlotClose{{Slot: "policy:" + name, Reason: name + " was turned off, and it keeps this edit's text while it is off"}}}, true
		}
		text = row.YAMLSource
		if found {
			text = held[0].Doc
		}
		// Revisions 15, 16 and 18: a set starts governing only when its
		// approve pools name approver roles, its match.roles application
		// roles, and no rule carries the retired obligations list. A text
		// that no longer parses is left to Check's compile.
		if doc, perr := policy.Parse([]byte(text)); perr == nil && (!a.roleGates(w, r, doc) || !a.obligationsGate(w, doc)) {
			return directChange{}, false
		}
		if !found {
			return directChange{Item: drafts.Item{Kind: drafts.KindPolicySet, Name: name, Op: drafts.OpPut, Doc: text}}, true
		}
		revs, err := a.store.Drafts().Revisions(r.Context(), saved.ID)
		if err != nil {
			a.fail(w, r, http.StatusInternalServerError, policyLookupFailed, err)
			return directChange{}, false
		}
		tried, triedRev = saved.ID, saved.Revision
		return directChange{Draft: &directDraft{Row: saved, Rows: held, Revs: revs}}, true
	})
	route.answer = func(f drafts.Finding) (int, string, bool) {
		switch f.Code {
		case "policy.compile":
			return http.StatusInternalServerError, "snapshot publish failed: " + f.Sentence, true
		case "draft.stale":
			return http.StatusConflict, fmt.Sprintf(staleSavedEditRefusal, name), true
		}
		return 0, "", false
	}
	res, ok := a.publishOne(w, r, route)
	if !ok {
		return
	}
	if req.Status == "active" {
		// A set can be valid yet carry a predicate no subject satisfies
		// today (require.deviceCert); repeat the validate-time advisory
		// loudly at the moment the set starts governing (spec/policyset
		// rev 10; the Builder shows the same words at validate).
		if doc, perr := policy.Parse([]byte(text)); perr == nil {
			for _, adv := range a.policyAdvisories(doc) {
				a.log.Warn("policy advisory", "set", name, "code", adv.Code, "advisory", adv.Text)
			}
		}
	}
	writeJSON(w, http.StatusOK, policyActivation{Status: req.Status, Snapshot: res.Snapshot, Changed: !res.Noop})
}

// activatedMeanwhile answers an activate whose saved edit tried, which an
// earlier run of it published, another replica moved in between:
// published, the set is in the asked state and the answer is the no-change 200,
// revised, the answer is the 409 that names its new revision, and closed,
// the 409 with the reason it was closed with.
func (a *App) activatedMeanwhile(w http.ResponseWriter, r *http.Request, name string, tried int64, status, snapshot string) {
	d, _, err := a.store.Drafts().Get(r.Context(), tried)
	switch {
	case err != nil:
		a.fail(w, r, http.StatusInternalServerError, policyLookupFailed, err)
	case d.State == string(drafts.StatePublished):
		writeJSON(w, http.StatusOK, policyActivation{Status: status, Snapshot: snapshot})
	case d.State == string(drafts.StateOpen):
		apiError(w, http.StatusConflict, fmt.Sprintf(revisedSavedEditRefusal, name, d.Revision, name))
	default:
		apiError(w, http.StatusConflict, fmt.Sprintf(closedSavedEditRefusal, name, d.DecidedReason, name))
	}
}

// savedEditSecondPersonRefusal is the 409 of admin.secondPerson on
// the activate of the saved edit d whose verdict holds a risk. The edit is
// a draft already, so the answer names it for another administrator to
// publish rather than asking for a draft to be made.
func savedEditSecondPersonRefusal(d *directDraft) string {
	set := ""
	if len(d.Rows) > 0 {
		set = d.Rows[0].Name
	}
	id := strconv.FormatInt(d.Row.ID, 10)
	return "This deployment needs a second person to publish a change that widens access (admin.secondPerson), and the saved edit of " + set +
		" is draft " + id + ". Ask another administrator to review and publish draft " + id + ", with strazactl drafts publish " + id +
		" or on the console under Drafts."
}

// snapshotNoSessionMsg is the enterprise 401 of GET /v1/snapshot for a
// request that carries no bearer at all.
const snapshotNoSessionMsg = "The enterprise profile serves the policy snapshot only to a checked-in session, " +
	"and this request carried no session token. Update straza on this machine, " +
	"then start a new session so it checks in and sends its token. " +
	"If straza is already current, a proxy in front of strazad is dropping the Authorization header."

// snapshotBadSessionMsg is the enterprise 401 of GET /v1/snapshot for a
// bearer that is not a live session token of this server.
const snapshotBadSessionMsg = "The token on this request is not a valid session token from this server, " +
	"because it is expired, is another kind of credential, or was issued elsewhere. " +
	"The enterprise profile serves the policy snapshot only to a checked-in session. " +
	"Start a new session so straza checks in again, and run straza doctor if it keeps failing."

// handleSnapshot serves the active signed snapshot bytes with the id as ETag.
// A matching If-None-Match short-circuits to 304, so clients poll cheaply. No
// DB access. Under the enterprise profile the caller must
// hold a checked-in session, checked before the 304 and the 503 so this route
// tells an anonymous caller neither the active id nor whether a snapshot
// exists. /metrics still counts this route's answers by status unless
// server.metricsToken is set.
func (a *App) handleSnapshot(w http.ResponseWriter, r *http.Request) {
	if a.cfg.Profile == config.ProfileEnterprise && !a.snapshotSession(w, r) {
		return
	}
	cur := a.snapshots.Current()
	if cur == nil {
		a.fail(w, r, http.StatusServiceUnavailable, "no active snapshot", nil)
		return
	}
	etag := `"` + cur.ID + `"`
	if match := r.Header.Get("If-None-Match"); match == etag {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	w.Header().Set("ETag", etag)
	w.Header().Set("Content-Type", "application/cbor")
	w.Header().Set("X-Straza-Snapshot-Id", cur.ID)
	_, _ = w.Write(cur.Signed)
}

// snapshotSession reports whether the request carries a live session token
// whose session, user and device are not revoked, answering 401 itself when
// it does not. The token verifies locally and the denylist is in memory, so
// the check reads no store.
func (a *App) snapshotSession(w http.ResponseWriter, r *http.Request) bool {
	// A refusal writes no audit event, like /v1/push and /v1/decide: an
	// anonymous caller has no actor to record. It is counted on
	// straza_http_requests_total with status 401, and the Debug access log
	// records each one.
	raw := bearerToken(r)
	if raw == "" {
		apiError(w, http.StatusUnauthorized, snapshotNoSessionMsg)
		return false
	}
	claims, err := a.tokens.Verify(raw)
	if err != nil || claims.Session == "" {
		apiError(w, http.StatusUnauthorized, snapshotBadSessionMsg)
		return false
	}
	if a.denylist.blocked(claims) {
		apiError(w, http.StatusUnauthorized, a.denylist.revokedMsg(claims, revokedSessionMsg, revokedIdentityMsg))
		return false
	}
	return true
}

// handleSnapshotKeys publishes the snapshot verification keys so straza
// and gateway pods verify snapshots before swapping.
func (a *App) handleSnapshotKeys(w http.ResponseWriter, _ *http.Request) {
	keys := a.snapKeys.PublicKeys()
	out := make([]map[string]string, 0, len(keys))
	for kid, pub := range keys {
		out = append(out, map[string]string{
			"kid": kid,
			"alg": "ed25519",
			"key": base64.StdEncoding.EncodeToString(pub),
		})
	}
	w.Header().Set("Cache-Control", "max-age=60")
	writeJSON(w, http.StatusOK, map[string]any{"keys": out})
}
