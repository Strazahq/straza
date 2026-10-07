package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/strazahq/straza/internal/drafts"
	"github.com/strazahq/straza/internal/manager"
	"github.com/strazahq/straza/internal/redact"
	"github.com/strazahq/straza/internal/store"
)

// The sentences of Check again.
const (
	rebaseBodyRefusal    = "The request body is not a check again: %s. Send revision and picks as JSON."
	rebasePickRefusal    = "picks must map Kind/Name and a field, such as App/demo-tools straza.limits.rps, to draft or live."
	rebaseNothingRefusal = "Nothing that draft %s holds changed on live state since it was checked, so there is nothing to check again."
	rebaseEmptyRefusal   = "Every change of draft %s was already made on live state, so there is nothing left to check again. Discard the draft with strazactl drafts discard %s."
	rebaseConflict       = "Draft %s and live state both changed %s %s since the draft was checked. Pick which value to keep, then check again."
	rebaseSecretRefusal  = "%s holds a secret at %s on live state, and a draft never keeps a secret, so Check again cannot take live's value there. " +
		"Keep the draft's value, or write the server's manifest from your own file and store the secret with strazactl apps secret set %s."
	rebaseUnreadApp = "Straza cannot read the manifest %s was checked against field by field, so Check again cannot merge it. Send the server's manifest again as a new draft."
	rebaseUnread    = "Straza cannot read %s field by field, so Check again cannot merge it. Send its document again as a new draft."
)

// rebaseBody is the body of Check again: the revision read, and for each
// field both sides changed, the value to keep, keyed "Kind/Name field".
type rebaseBody struct {
	Revision int               `json:"revision"`
	Picks    map[string]string `json:"picks"`
}

// handleDraftRebase is POST /v1/admin/drafts/{id}/rebase, Check again:
// each item whose object moved on live state since its base is
// merged field by field against its base document (drafts.Merge), keeping
// what the draft changed and taking every other field from live, and its
// base becomes live's. A field both sides changed apart answers 409 with
// the conflicts and nothing written, until the body picks a value for it.
// With every field settled the merged items meet intake, waived against
// live state (drafts.Waive), and the check, and the new revision is stored
// stamped, as the one route besides revert that moves a base. A revision no pick decided is mechanical and makes no author
// under admin.secondPerson.
func (a *App) handleDraftRebase(w http.ResponseWriter, r *http.Request, c draftCaller) {
	var body rebaseBody
	if !decodeDraftBody(w, r, &body, rebaseBodyRefusal, false) {
		return
	}
	picks, ok := picksOf(body.Picks)
	if !ok {
		apiError(w, http.StatusBadRequest, rebasePickRefusal)
		return
	}
	row, rows, revs, ok := a.readDraft(w, r, c)
	if !ok {
		return
	}
	id, authors := strconv.FormatInt(row.ID, 10), authorsOf(revs)
	if msg := c.reviseRefusal(id, authors); msg != "" {
		apiError(w, http.StatusForbidden, msg)
		return
	}
	if row.State != string(drafts.StateOpen) || body.Revision != row.Revision {
		apiError(w, http.StatusConflict, movedRefusal(row, body.Revision))
		return
	}
	authors = withAuthor(authors, c.author)
	d := draftOf(row, rows, authors)
	world, in, ok := a.readLive(w, r, d)
	if !ok {
		return
	}
	if msg := c.readRefusal(d, world); msg != "" {
		apiError(w, http.StatusForbidden, msg)
		return
	}
	m, err := a.mergeRows(r.Context(), world, rows, picks)
	if msg := mergeRefusal(err); msg != "" {
		apiError(w, http.StatusConflict, msg)
		return
	}
	switch {
	case err != nil:
		a.fail(w, r, http.StatusServiceUnavailable, worldReadRefusal(err), err)
		return
	case m.moved == 0:
		apiError(w, http.StatusConflict, fmt.Sprintf(rebaseNothingRefusal, id))
		return
	case len(m.conflicts) > 0:
		first := m.conflicts[0]
		writeJSON(w, http.StatusConflict, map[string]any{"error": fmt.Sprintf(rebaseConflict, id, first.Object, first.Field), "conflicts": m.conflicts})
		return
	case len(m.rows) == 0:
		// Every item left the draft with an object live state removed, and
		// a draft with no item can only be discarded.
		apiError(w, http.StatusConflict, fmt.Sprintf(rebaseEmptyRefusal, id, id))
		return
	}
	d = draftOf(row, m.rows, authors)
	refused, waived := c.readerWaived(world, d, intakeOf(d, c.author))
	if len(refused) > 0 {
		refuseIntake(w, refused)
		return
	}
	in.Apps = a.itemFacts(d)
	cd, ok := a.stampAndCheck(w, r, world, in, d, m.rows)
	if !ok {
		return
	}
	cd.v = c.readerVerdict(cd.w, cd.d, cd.v, waived)
	a.storeRebase(w, r, c, row, cd, m.picked == 0)
}

// storeRebase writes the checked draft cd as the revision after row with
// its stamp, moving the base of every item (DraftRevise.Rebase), then
// writes its draft.update with rebase: true and its draft.check, and
// answers 200 with the draft and its verdict. A draft of the straza-app
// door expires 14 days after this revision.
func (a *App) storeRebase(w http.ResponseWriter, r *http.Request, c draftCaller, row store.DraftRow, cd checkedDraft, mechanical bool) {
	ctx := r.Context()
	check := stampOf(cd.d, cd.v, cd.in.Now)
	rev := store.DraftRevise{From: row.Revision, Items: cd.rows, Rebase: true, Checked: &check,
		Rev: store.DraftRevisionRow{Author: actorOf(c.author), Door: string(c.door), Digest: revisionDigest(cd.d.Items), Mechanical: mechanical}}
	if row.Door == string(drafts.DoorAgent) {
		expires := cd.in.Now.Add(draftExpiry)
		rev.ExpiresAt = &expires
	}
	updated, err := a.store.Drafts().Revise(ctx, row.ID, rev)
	switch {
	case errors.Is(err, store.ErrConflict):
		apiError(w, http.StatusConflict, movedRefusal(updated, row.Revision))
		return
	case err != nil:
		a.storeFailed(w, r, draftStoreRefusal, err)
		return
	}
	id := strconv.FormatInt(updated.ID, 10)
	cd.d.Revision, cd.v.Draft, cd.v.Revision = updated.Revision, id, updated.Revision
	data := revisionData("draft.update", id, updated.Revision, c.door, cd.d.Items)
	data["rebase"] = true
	if updated.Reverts != 0 {
		data["reverts"] = strconv.FormatInt(updated.Reverts, 10)
	}
	a.emitEventCtx(context.WithoutCancel(ctx), "straza.audit.admin", data)
	a.recordCheck(ctx, id, cd.v)
	writeJSON(w, http.StatusOK, map[string]any{"draft": draftAnswer(c, &cd.w, updated, cd.rows, cd.d.Authors), "verdict": verdictFor(c, cd.w, cd.d, cd.v)})
}

// picksOf reads the picks of a body, keyed "Kind/Name field" to draft or
// live, and reports false for any other key or value.
func picksOf(raw map[string]string) (map[string]drafts.Pick, bool) {
	out := make(map[string]drafts.Pick, len(raw))
	for key, v := range raw {
		object, field, ok := strings.Cut(key, " ")
		kind, name, _ := strings.Cut(object, "/")
		p := drafts.Pick(v)
		if !ok || field == "" || kind == "" || name == "" || (p != drafts.PickDraft && p != drafts.PickLive) {
			return nil, false
		}
		out[key] = p
	}
	return out, true
}

// rebaseMerge is Check again over a draft's rows: the new rows, the
// conflicts left, the picks that decided one, and how many items moved.
type rebaseMerge struct {
	rows      []store.DraftItemRow
	conflicts []drafts.Conflict
	picked    int
	moved     int
}

// mergeRows is Check again over rows against w: each item whose object
// moved since its base is merged and waits for a new stamp from w, and
// every other item keeps its row. The conflicts come sorted by object and
// field, a server's values masked as GET masks them.
func (a *App) mergeRows(ctx context.Context, w drafts.World, rows []store.DraftItemRow, picks map[string]drafts.Pick) (rebaseMerge, error) {
	var m rebaseMerge
	for _, row := range rows {
		object, kind := row.Kind+"/"+row.Name, drafts.Kind(row.Kind)
		if row.BaseOp == "" || row.Base == string(w.Fingerprints[object]) {
			m.rows = append(m.rows, row)
			continue
		}
		m.moved++
		live, err := a.liveVersion(ctx, w, row)
		if err != nil {
			return m, err
		}
		draft := drafts.Version{Op: drafts.Op(row.Op), Doc: row.Doc}
		if kind == drafts.KindApp && draft.Op == drafts.OpPut {
			if draft.Doc, err = manifestJSONOf(row.Doc); err != nil {
				return m, &drafts.UnreadError{Object: object, Side: "draft", Err: err}
			}
		}
		merged, err := drafts.Merge(kind, object, drafts.Version{Op: drafts.Op(row.BaseOp), Doc: row.BaseDoc}, draft, live, picks)
		if err != nil {
			return m, err
		}
		m.picked += merged.Picked
		switch {
		case len(merged.Conflicts) > 0:
			m.conflicts = append(m.conflicts, shownConflicts(row, live, merged.Conflicts)...)
			continue
		case merged.Drop:
			continue
		}
		next := store.DraftItemRow{Kind: row.Kind, Name: row.Name, Op: string(merged.Op), Doc: row.Doc, Offered: row.Offered}
		if merged.Changed {
			if next.Doc, err = itemDocOf(kind, merged.Doc); err != nil {
				return m, &drafts.UnreadError{Object: object, Side: "draft", Err: err}
			}
		}
		m.rows = append(m.rows, next)
	}
	sort.SliceStable(m.conflicts, func(i, j int) bool {
		if m.conflicts[i].Object != m.conflicts[j].Object {
			return m.conflicts[i].Object < m.conflicts[j].Object
		}
		return m.conflicts[i].Field < m.conflicts[j].Field
	})
	return m, nil
}

// mergeRefusal answers the 409 sentence of a merge err refused, or "" for
// no error and for a failed read of live state.
func mergeRefusal(err error) string {
	var secret *drafts.LiveSecretError
	var unread *drafts.UnreadError
	switch {
	case errors.As(err, &secret):
		_, name, _ := strings.Cut(secret.Object, "/")
		return fmt.Sprintf(rebaseSecretRefusal, name, secret.Field, name)
	case errors.As(err, &unread) && unread.Side == "base" && strings.HasPrefix(unread.Object, string(drafts.KindApp)+"/"):
		return fmt.Sprintf(rebaseUnreadApp, strings.TrimPrefix(unread.Object, string(drafts.KindApp)+"/"))
	case errors.As(err, &unread):
		return fmt.Sprintf(rebaseUnread, unread.Object)
	}
	return ""
}

// liveVersion is the object of row as w holds it, unmasked, for a merge:
// a server's stored manifest, a role's export, the published text of a set
// that is on, and the stored text of one that is off, read by name.
func (a *App) liveVersion(ctx context.Context, w drafts.World, row store.DraftItemRow) (drafts.Version, error) {
	gone := drafts.Version{Op: drafts.OpRemove}
	switch drafts.Kind(row.Kind) {
	case drafts.KindApp:
		if app, ok := w.Apps[row.Name]; ok {
			return drafts.Version{Op: drafts.OpPut, Doc: app.Manifest}, nil
		}
	case drafts.KindRole:
		if doc, ok := drafts.RoleDocOf(w, row.Name); ok {
			text, err := doc.Marshal()
			if err != nil {
				return gone, fmt.Errorf("the role %s cannot be written as a document: %w", row.Name, err)
			}
			return drafts.Version{Op: drafts.OpPut, Doc: string(text)}, nil
		}
	case drafts.KindPolicySet:
		if p, ok := w.Policies[row.Name]; ok {
			return drafts.Version{Op: drafts.OpPut, Doc: p.Text}, nil
		}
		if w.Fingerprints[row.Kind+"/"+row.Name] != "" {
			set, err := a.store.Policies().GetByName(ctx, row.Name)
			if err != nil {
				return gone, fmt.Errorf("the policy set %s cannot be read: %w", row.Name, err)
			}
			return drafts.Version{Op: drafts.OpOff, Doc: set.YAMLSource}, nil
		}
	}
	return gone, nil
}

// manifestJSONOf is a server's app.yaml as the manifest JSON a publish
// stores.
func manifestJSONOf(doc string) (string, error) {
	mf, err := manager.Parse([]byte(doc))
	if err != nil {
		return "", err
	}
	return mf.JSON()
}

// itemDocOf is a merged document as an item keeps it: a server's manifest
// JSON in the app.yaml form strazactl apps export prints, and every other
// kind's as it is.
func itemDocOf(kind drafts.Kind, doc string) (string, error) {
	if kind != drafts.KindApp {
		return doc, nil
	}
	mf, err := manager.FromJSON(doc)
	if err != nil {
		return "", err
	}
	out, err := yaml.Marshal(mf)
	return string(out), err
}

// shownConflicts answers the conflicts of the item row as every route shows
// them: a server's values read from its base, draft and live documents
// masked as GET masks them, so a conflict never shows a value GET hides.
func shownConflicts(row store.DraftItemRow, live drafts.Version, cs []drafts.Conflict) []drafts.Conflict {
	if row.Kind != string(drafts.KindApp) {
		return cs
	}
	base, draft, current := maskedFields(row.BaseDoc), maskedFields(row.Doc), maskedFields(live.Doc)
	out := make([]drafts.Conflict, len(cs))
	for i, c := range cs {
		out[i] = c
		if c.Field == drafts.FieldState {
			continue
		}
		out[i].Base, out[i].Draft, out[i].Live = shown(base, c.Field, c.Base), shown(draft, c.Field, c.Draft), shown(current, c.Field, c.Live)
	}
	return out
}

// maskedFields answers the fields of a server's document masked as GET
// masks it, nil when the masked document does not read.
func maskedFields(doc string) map[string]string {
	if doc == "" {
		return map[string]string{}
	}
	var v map[string]any
	if yaml.Unmarshal([]byte(maskedManifest(doc)), &v) != nil || v == nil {
		return nil
	}
	raw, err := json.Marshal(v)
	if err != nil {
		return nil
	}
	fields, err := drafts.AppFieldText(string(raw))
	if err != nil {
		return nil
	}
	return fields
}

// shown is the masked value of field in fields, the mask itself for a
// document that did not read, and "" for a field that is unset.
func shown(fields map[string]string, field, value string) string {
	if value == "" {
		return ""
	}
	if fields == nil {
		return redact.Mark
	}
	return fields[field]
}
