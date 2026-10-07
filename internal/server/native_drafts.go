package server

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/strazahq/straza/internal/authn"
	"github.com/strazahq/straza/internal/drafts"
	"github.com/strazahq/straza/internal/redact"
	"github.com/strazahq/straza/internal/store"
)

// This file holds the agent door of config drafts:
// the built-in straza app's straza__draft_submit and straza__draft_status,
// which tier one lists only to holders of straza-draft-config. Both run
// from the cached subject and read or write the caller's own draft rows by
// id, and the submit reads live state only to judge a value finding before
// the insert, so no decision and no catalog reads the database. The check
// of a submitted draft runs off the request path, in the drafts checker.

// draftStaleCode is the code of the refusal of a draft whose objects
// changed after it entered the draft.
const draftStaleCode = "draft.stale"

// draftingTools are the built-in straza app's two drafting tools.
var draftingTools = map[string]bool{nativeToolDraftSubmit: true, nativeToolDraftStatus: true}

// The rates of the drafting tools: two calls a second between the
// two tools for each session, and one submit every 10 seconds for each
// user, both on the gateway's limiter. The approval tools keep no rate.
const (
	draftingSessionRPS = 2
	draftSubmitUserRPS = 0.1
)

// The refusals of the drafting tools.
const (
	draftRoleRefusal       = "Straza: %s needs the Straza role %s, and this session does not hold it. Ask an administrator to assign it in your identity manager."
	draftSubmitRateRefusal = "Straza: straza__draft_submit takes one draft every 10 seconds for each agent. Wait, then submit again."
	draftNotSaved          = "Straza: the draft was not saved. "
	draftSubmitShape       = "Straza: straza__draft_submit takes a JSON object with documents, a list of YAML texts, and optionally note and draft. Send the arguments in that shape."
	draftStatusShape       = "Straza: straza__draft_status takes a JSON object with draft, the id straza__draft_submit answered. Send the arguments in that shape."
	draftNoOpenRefusal     = "Straza: no open draft %s of yours. Submit without draft to start a new one."
	draftNoSuchRefusal     = "Straza: no such draft %s"
	draftMovedRefusal      = "Straza: draft %s changed while this revision was saved, so nothing was saved. Read it with straza__draft_status, then submit again with draft %s."
	submitStoreRefusal     = "Straza: the draft was not saved, because Straza could not reach its database. Try again in a minute, and if it keeps failing ask an administrator to read the strazad log."
	statusReadRefusal      = "Straza: the draft could not be read, because Straza could not reach its database. Try again in a minute, and if it keeps failing ask an administrator to read the strazad log."
)

// The next steps a drafting tool names in DraftAgentStatus. A draft
// whose only refusals are draft.stale gets nextStale, because a new
// revision keeps the stale base and would meet the same refusal.
const (
	nextSubmitted = "Straza checks the draft now. Call straza__draft_status with draft %s to read the check. A person publishes it on the console or with strazactl, and you cannot."
	nextUnchecked = "Straza has not checked revision %d yet. Call straza__draft_status again in a few seconds."
	nextRefused   = "Fix the refused lines and submit again with draft %s."
	nextStale     = "Draft %s went stale: live state changed under it. Ask a person to check it again on the console or with strazactl drafts rebase %s, or submit the documents as a new draft."
	nextWaits     = "Draft %s waits for a person to publish it on the console or with strazactl. You cannot publish it."
	nextPublished = "A person published draft %s on %s."
	nextDiscarded = "Draft %s was discarded%s."
	nextExpired   = "Draft %s expired after 14 days without a change."
)

// nativeListed reports whether tier one lists the built-in tool for a
// session holding roles: an approval tool for every session, and a
// drafting tool only for a holder of straza-draft-config.
func nativeListed(tool string, roles []string) bool {
	return !draftingTools[tool] || slices.Contains(roles, DraftConfigRole)
}

// grantedOf answers the granted fact of a tier-one target (spec/policyset
// revisions 17 and 20): true for a proxied tool, whose access row tier one
// matched, and for a drafting tool, which tier one lists only to holders,
// and false for an approval tool, which policy alone gates. The gateway
// call, the overlay probe and simulate's access check all answer it.
func grantedOf(t gwTarget) bool {
	return t.app != nativeAppName || draftingTools[t.tool]
}

// draftingTool reports whether t is one of the two drafting tools.
func draftingTool(t gwTarget) bool {
	return t.app == nativeAppName && draftingTools[t.tool]
}

// draftingRate answers the limiter key and the rate the two drafting tools
// share for one session.
func draftingRate(session string) (string, float64) {
	return session + "|straza-drafts", draftingSessionRPS
}

// submitRateRefusal takes one submit from the budget of the user userID,
// one every 10 seconds across all its sessions, and answers the refusal
// when the budget is spent, "" otherwise.
func (a *App) submitRateRefusal(userID string) string {
	if a.gateway.limiter.Allow("draft_submit|"+userID, draftSubmitUserRPS) {
		return ""
	}
	return draftSubmitRateRefusal
}

// draftingSubject answers the session's cache entry for the drafting tool
// tool, or the role refusal when the entry is gone or its roles lack
// straza-draft-config. It answers no entry and no refusal for any other
// tool. The gateway asks before it writes the call's record and each
// handler asks again, so a wrong role check refuses instead of allowing.
func (a *App) draftingSubject(session, tool string) (cachedSubject, string) {
	if !draftingTools[tool] {
		return cachedSubject{}, ""
	}
	e, ok := a.subjects.lookup(session)
	if !ok || !slices.Contains(e.sub.Roles, DraftConfigRole) {
		return cachedSubject{}, fmt.Sprintf(draftRoleRefusal, nativeAppName+"__"+tool, DraftConfigRole)
	}
	return e, ""
}

// argumentsDigest answers the hex sha256 of the verbatim arguments of a
// call and their length in bytes, which stand in for the arguments of
// straza__draft_submit in its record and its approval preview.
func argumentsDigest(args json.RawMessage) (string, int) {
	sum := sha256.Sum256(args)
	return hex.EncodeToString(sum[:]), len(args)
}

// callPreview is the approval preview of an mcp.call of tool on app with
// args: the digest and the size for straza__draft_submit, whose documents
// may hold a secret the handler has not scanned yet, and the arguments
// themselves for every other tool. The preview knob gates both.
func (a *App) callPreview(app, tool string, args json.RawMessage) argsPreview {
	if app == nativeAppName && tool == nativeToolDraftSubmit {
		digest, size := argumentsDigest(args)
		args, _ = json.Marshal(map[string]any{"argumentsDigest": digest, "argumentsSize": size})
	}
	return a.mcpArgsPreview(args)
}

// maskDescribedSubmit answers the arguments of an approval_request call to
// record. When its action describes a straza__draft_submit call, each of
// that call's documents and its note are masked as drafts.WithoutSecrets
// masks a document, because the record is written before the draft's
// secret scan could refuse them. The arguments are then written again
// from what the handler reads, so a field the handler ignores or a key
// sent twice cannot carry a document past the mask. Any other call, and
// arguments the handler cannot read, are answered as sent.
func maskDescribedSubmit(args json.RawMessage) json.RawMessage {
	var in nativeApprovalRequestArgs
	if json.Unmarshal(args, &in) != nil || in.Action.App != nativeAppName || in.Action.ToolName != nativeToolDraftSubmit {
		return args
	}
	var call draftSubmitArgs
	if len(in.Action.Args) > 0 && json.Unmarshal(in.Action.Args, &call) != nil {
		in.Action.Args = json.RawMessage(strconv.Quote(redact.Mark))
	} else if len(in.Action.Args) > 0 {
		for i, doc := range call.Documents {
			call.Documents[i], _ = drafts.WithoutSecrets(doc)
		}
		if call.Note != nil {
			note, _ := drafts.WithoutSecrets(*call.Note)
			call.Note = &note
		}
		in.Action.Args, _ = json.Marshal(call)
	}
	out, err := json.Marshal(in)
	if err != nil {
		return json.RawMessage(strconv.Quote(redact.Mark))
	}
	return out
}

// doorAuthor is the author of a revision through the straza-app door, read
// from the session and its cache entry and never from the store:
// the user, whether check-in found a person, the session lane, the harness
// name, and the sponsor as check-in resolved it.
func doorAuthor(claims authn.Claims, e cachedSubject) drafts.Principal {
	return drafts.Principal{UserID: claims.Subject, Username: e.sub.User, Agent: !e.facts.person, Via: laneSession,
		Client: e.facts.harness, SponsorID: e.sub.SponsorID, SponsorName: e.sub.Sponsor}
}

// draftSubmitArgs is the input of straza__draft_submit, DraftSubmit in the
// openapi document.
type draftSubmitArgs struct {
	Documents []string `json:"documents"`
	Note      *string  `json:"note"`
	Draft     string   `json:"draft"`
}

// nativeDraftSubmit stores a draft of the straza-app door, or the next
// revision of the caller's own open draft, with no check, and answers
// DraftAgentStatus at once. The gateway already took the rates.
// Intake runs first, and a refusal stores nothing, unless live state may
// waive every finding (drafts.Waive). A finding about a value, a plain
// word at a secret-named place or a mask sent back, is then judged before
// the insert in the author's read view (submitWaived), and what the waiver
// does not lift refuses the submit with nothing stored, in the same words
// for a value the stored manifest holds and for any other, so no submit
// confirms a guess. A draft whose findings are about names alone is stored
// with live state unread, as a clean one is, and the check decides. The
// new revision expires 14 days after it is written, writes its
// draft.create or draft.update with the caller as actor, and wakes the
// checker.
func (a *App) nativeDraftSubmit(ctx context.Context, args json.RawMessage, claims authn.Claims) *mcp.CallToolResult {
	e, msg := a.draftingSubject(claims.Session, nativeToolDraftSubmit)
	if msg != "" {
		return nativeToolError(msg)
	}
	var in draftSubmitArgs
	if err := json.Unmarshal(args, &in); err != nil {
		return nativeToolError(draftSubmitShape)
	}
	author := doorAuthor(claims, e)
	items, bundle := drafts.ParseBundle(in.Documents)
	d := drafts.Draft{Revision: 1, State: drafts.StateOpen, Door: drafts.DoorAgent, Note: noteOf(in.Note, ""),
		Authors: []drafts.Principal{author}, Items: items}
	var own store.DraftRow
	var held []store.DraftItemRow
	if in.Draft != "" {
		var err error
		if own, held, msg, err = a.ownDraft(ctx, in.Draft, author); err != nil {
			a.log.Warn("draft submit: the draft to revise could not be read", "draft", in.Draft, "err", err)
			return nativeToolError(submitStoreRefusal)
		} else if msg != "" {
			return nativeToolError(msg)
		}
		d.Note = noteOf(in.Note, own.Note)
	}
	fs := bodyIntake(d, author, bundle)
	if !drafts.Waivable(fs) {
		return nativeToolError(notSavedRefusal(certainFirst(fs)))
	}
	if slices.ContainsFunc(fs, func(f drafts.Finding) bool { return f.Code == "secret.value" || f.Code == "bundle.masked" }) {
		refused, err := a.submitWaived(ctx, author, d, fs)
		if err != nil {
			a.log.Warn("draft submit: live state could not be read to judge a value", "user", author.UserID, "err", err)
			return nativeToolError(submitStoreRefusal)
		}
		if len(refused) > 0 {
			return nativeToolError(notSavedRefusal(refused))
		}
	}
	if own.ID == 0 {
		n, err := a.store.Drafts().CountOpen(ctx, author.UserID)
		if err != nil {
			a.log.Warn("draft submit: the open drafts could not be counted", "user", author.UserID, "err", err)
			return nativeToolError(submitStoreRefusal)
		}
		if fs := drafts.OpenLimit(author.Username, n); len(fs) > 0 {
			return nativeToolError(notSavedRefusal(fs))
		}
	}
	rows := keepHeld(held, items)
	canonicalRoles(rows, nil)
	stored, err := a.storeDoorDraft(ctx, own, rows, d.Note, in.Note, author)
	var moved *draftMoved
	switch {
	case errors.As(err, &moved):
		return nativeToolError(moved.refusal)
	case err != nil:
		a.log.Warn("draft submit: the draft could not be stored", "user", author.UserID, "err", err)
		return nativeToolError(submitStoreRefusal)
	}
	id := strconv.FormatInt(stored.ID, 10)
	action := "draft.create"
	if stored.Revision > 1 {
		action = "draft.update"
	}
	a.recordDoorRevision(ctx, action, id, stored.Revision, itemsOf(rows), author)
	a.wakeDraftsChecker()
	return nativeToolOK(map[string]any{"draft": id, "revision": stored.Revision, "state": stored.State, "checked": false,
		"publishable": false, "next": fmt.Sprintf(nextSubmitted, id)})
}

// submitWaived answers the intake findings fs of d that the waiver leaves
// refused in the read view of author over live state read now, as the
// checker would answer them later: a value the server's stored manifest
// holds is waived for a reader of the server, and every other finding
// stays refused, the reader's words on a server author may not read.
func (a *App) submitWaived(ctx context.Context, author drafts.Principal, d drafts.Draft, fs []drafts.Finding) ([]drafts.Finding, error) {
	w, err := a.liveWorld(ctx)
	if err != nil {
		return nil, err
	}
	c, err := a.proposerCaller(ctx, author)
	if err != nil {
		return nil, err
	}
	refused, _ := c.readerWaived(w, d, fs)
	return refused, nil
}

// draftMoved is a revision the store turned away because the draft moved
// after it was read, with the refusal the agent reads.
type draftMoved struct{ refusal string }

func (m *draftMoved) Error() string { return m.refusal }

// storeDoorDraft writes rows as a new draft of the straza-app door when own
// is zero, and as the revision after own otherwise, with no check and an
// expiry 14 days out. note is the new draft's note, and sent the note a
// revision sent, nil to keep the stored one. A revision of a draft that
// moved since own was read answers a draftMoved.
func (a *App) storeDoorDraft(ctx context.Context, own store.DraftRow, rows []store.DraftItemRow, note string, sent *string, author drafts.Principal) (store.DraftRow, error) {
	expires := time.Now().Add(draftExpiry)
	rev := store.DraftRevisionRow{Author: actorOf(author), Door: string(drafts.DoorAgent), Digest: revisionDigest(itemsOf(rows))}
	if own.ID == 0 {
		return a.store.Drafts().Create(ctx, store.DraftRow{Door: string(drafts.DoorAgent), Note: note, ExpiresAt: &expires}, rows, rev)
	}
	row, err := a.store.Drafts().Revise(ctx, own.ID, store.DraftRevise{From: own.Revision, Items: rows, Rev: rev, Note: sent, ExpiresAt: &expires})
	if errors.Is(err, store.ErrConflict) {
		id := strconv.FormatInt(own.ID, 10)
		if row.State != string(drafts.StateOpen) {
			return row, &draftMoved{fmt.Sprintf(draftNoOpenRefusal, id)}
		}
		return row, &draftMoved{fmt.Sprintf(draftMovedRefusal, id, id)}
	}
	return row, err
}

// ownDraft reads the draft id that author asks to revise, with its items,
// or answers the submit's refusal. Only an open draft of the straza-app
// door that author proposed is theirs, and an author who is not a person
// revises only a draft whose every revision it wrote. A person's
// console draft stays out, because Check reads the agent rules from the
// row's door and its authors.
func (a *App) ownDraft(ctx context.Context, id string, author drafts.Principal) (store.DraftRow, []store.DraftItemRow, string, error) {
	refusal := fmt.Sprintf(draftNoOpenRefusal, id)
	n, err := strconv.ParseInt(id, 10, 64)
	if err != nil || n <= 0 {
		return store.DraftRow{}, nil, refusal, nil
	}
	row, items, err := a.store.Drafts().Get(ctx, n)
	switch {
	case errors.Is(err, store.ErrNotFound):
		return store.DraftRow{}, nil, refusal, nil
	case err != nil:
		return store.DraftRow{}, nil, "", err
	case row.Door != string(drafts.DoorAgent) || row.Proposer.ID != author.UserID || row.State != string(drafts.StateOpen):
		return store.DraftRow{}, nil, refusal, nil
	}
	if author.Agent {
		revs, err := a.store.Drafts().Revisions(ctx, n)
		if err != nil {
			return store.DraftRow{}, nil, "", err
		}
		for _, rev := range revs {
			if rev.Author.ID != author.UserID || rev.Author.Via == laneAdminAPI {
				return store.DraftRow{}, nil, refusal, nil
			}
		}
	}
	return row, items, "", nil
}

// notSavedRefusal is the tool error of the intake refusals fs: one clause
// per finding, each its object, its sentence and its fix.
func notSavedRefusal(fs []drafts.Finding) string {
	clauses := make([]string, len(fs))
	for i, f := range fs {
		clause := f.Sentence
		if f.Object != "" {
			clause = f.Object + ": " + clause
		}
		if f.Fix != "" {
			clause += " " + f.Fix
		}
		clauses[i] = clause
	}
	return draftNotSaved + strings.Join(clauses, " ")
}

// recordDoorRevision writes the draft.create or draft.update of a revision
// through the straza-app door: the fields every door records, then the
// client and, for an agent with one, its sponsor, with the author as the
// actor on the session lane.
func (a *App) recordDoorRevision(ctx context.Context, action, id string, rev int, items []drafts.Item, author drafts.Principal) {
	data := revisionData(action, id, rev, drafts.DoorAgent, items)
	data["client"] = author.Client
	if author.SponsorID != "" {
		data["sponsor"], data["sponsorId"] = author.SponsorName, author.SponsorID
	}
	actor := auditActor{Name: author.Username, ID: author.UserID, Via: laneSession}
	a.emitEventCtx(withActor(context.WithoutCancel(ctx), actor), "straza.audit.admin", data)
}

// draftStatusArgs is the input of straza__draft_status.
type draftStatusArgs struct {
	Draft string `json:"draft"`
}

// nativeDraftStatus answers DraftAgentStatus of one draft of the
// straza-app door the caller proposed, from its row and the verdict an
// agent reads that the check stored with it. Any other id answers
// "no such draft", as approval_status does, so an agent never learns
// whether another's draft exists.
func (a *App) nativeDraftStatus(ctx context.Context, args json.RawMessage, claims authn.Claims) *mcp.CallToolResult {
	if _, msg := a.draftingSubject(claims.Session, nativeToolDraftStatus); msg != "" {
		return nativeToolError(msg)
	}
	var in draftStatusArgs
	if err := json.Unmarshal(args, &in); err != nil || in.Draft == "" {
		return nativeToolError(draftStatusShape)
	}
	notFound := nativeToolError(fmt.Sprintf(draftNoSuchRefusal, in.Draft))
	n, err := strconv.ParseInt(in.Draft, 10, 64)
	if err != nil || n <= 0 {
		return notFound
	}
	row, _, err := a.store.Drafts().Get(ctx, n)
	switch {
	case errors.Is(err, store.ErrNotFound):
		return notFound
	case err != nil:
		a.log.Warn("draft status: the draft could not be read", "draft", in.Draft, "err", err)
		return nativeToolError(statusReadRefusal)
	case row.Door != string(drafts.DoorAgent) || row.Proposer.ID != claims.Subject:
		return notFound
	}
	out, err := agentStatus(row)
	if err != nil {
		a.log.Warn("draft status: the stored check could not be read", "draft", in.Draft, "err", err)
		return nativeToolError(statusReadRefusal)
	}
	return nativeToolOK(out)
}

// agentStatus is DraftAgentStatus of the draft row: its id and revision
// from the row, whether the current revision is checked, the lists of the
// verdict an agent reads that the check stored, each finding keyed as the
// schema asks, and the next step. The stored verdict names draft "" when
// a create stored it, so the id always comes from the row. A checked
// revision whose stored verdict does not read is an error.
func agentStatus(row store.DraftRow) (map[string]any, error) {
	id := strconv.FormatInt(row.ID, 10)
	checked := row.CheckedRevision == row.Revision
	var av drafts.AgentVerdict
	if checked {
		if err := json.Unmarshal([]byte(row.AgentVerdict), &av); err != nil {
			return nil, fmt.Errorf("the stored check of draft %s does not read: %w", id, err)
		}
	}
	return map[string]any{"draft": id, "revision": row.Revision, "state": row.State, "checked": checked,
		"publishable": row.State == string(drafts.StateOpen) && checked && len(av.Refused) == 0,
		"refused":     keyed(av.Refused, nil, nil), "risks": keyed(av.Risks, nil, nil),
		"warnings": keyed(av.Warnings, nil, nil), "unchecked": keyed(av.Unchecked, nil, nil),
		"next": nextStep(row, checked, av.Refused)}, nil
}

// nextStep is the sentence of DraftAgentStatus that says what happens next
// to the draft row, whose check refused the findings refused: its end when
// it is closed, and otherwise whether the server checked it, and what the
// agent or a person does now.
func nextStep(row store.DraftRow, checked bool, refused []drafts.Finding) string {
	id := strconv.FormatInt(row.ID, 10)
	switch drafts.State(row.State) {
	case drafts.StatePublished:
		at := row.UpdatedAt
		if row.DecidedAt != nil {
			at = *row.DecidedAt
		}
		return fmt.Sprintf(nextPublished, id, at.UTC().Format(time.RFC3339))
	case drafts.StateDiscarded:
		reason := ""
		if r := strings.TrimRight(row.DecidedReason, ". "); r != "" {
			reason = ": " + r
		}
		return fmt.Sprintf(nextDiscarded, id, reason)
	case drafts.StateExpired:
		return fmt.Sprintf(nextExpired, id)
	}
	switch {
	case !checked:
		return fmt.Sprintf(nextUnchecked, row.Revision)
	case len(refused) > 0 && !slices.ContainsFunc(refused, func(f drafts.Finding) bool { return f.Code != draftStaleCode }):
		return fmt.Sprintf(nextStale, id, id)
	case len(refused) > 0:
		return fmt.Sprintf(nextRefused, id)
	default:
		return fmt.Sprintf(nextWaits, id)
	}
}
