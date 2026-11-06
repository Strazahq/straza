package server

import (
	"encoding/json"
	"fmt"
	"maps"
	"net/http"

	"gopkg.in/yaml.v3"

	"github.com/strazahq/straza/internal/drafts"
	"github.com/strazahq/straza/internal/manager"
)

// The answers of the objects draft route that open no draft.
const (
	objectsNoMarksRefusal    = "%s declares no objects: none of its tools carries an ai.straza/object mark. Write straza.objects in its manifest by hand, or ask the server's author to mark its tools."
	objectsNotRunningRefusal = "%s is not running, so Straza cannot read its tool marks. Start it, then draft again."
	objectsFaultFix          = "Fix the marks on the server, or write straza.objects in its manifest by hand."
	objectsUnchanged         = "%s's pinned %s already match its marks. Nothing to draft."
)

// handleAppObjectsDraft is POST /v1/admin/apps/{id}/objects/draft. It reads
// the marks on every tool the running server lists, builds the block they
// declare (manager.DeclaredObjects) and opens one draft of the api door by
// the caller whose App put is the live manifest with that block, keeping
// from the pinned block its plural and every mark at least as strict as the
// server's (mergeObjects). The draft is checked and stored as the drafts
// route stores one, and its own standing rules decide who publishes it. A
// server with no marks, not running or with broken marks answers 409, and
// one whose pinned block already matches answers 200 with no draft.
func (a *App) handleAppObjectsDraft(w http.ResponseWriter, r *http.Request) {
	row, err := a.appByRef(r)
	if err != nil {
		apiError(w, http.StatusNotFound, "unknown server")
		return
	}
	mf, err := manager.FromJSON(row.Manifest)
	if err != nil {
		a.fail(w, r, http.StatusInternalServerError, "stored manifest unreadable (reinstall the server)", err)
		return
	}
	tools, err := a.manager.LiveInventory(row.Name)
	if err != nil {
		apiError(w, http.StatusConflict, fmt.Sprintf(objectsNotRunningRefusal, row.Name))
		return
	}
	declared, marked, faults := manager.DeclaredObjects(row.Name, tools)
	switch {
	case !marked:
		apiError(w, http.StatusConflict, fmt.Sprintf(objectsNoMarksRefusal, row.Name))
		return
	case len(faults) > 0:
		apiError(w, http.StatusConflict, faults[0]+" "+objectsFaultFix)
		return
	}
	merged := mergeObjects(declared, mf.Straza.Objects)
	if mf.Straza.Objects != nil && sameObjects(merged, *mf.Straza.Objects) {
		writeJSON(w, http.StatusOK, map[string]any{"unchanged": true, "sentence": fmt.Sprintf(objectsUnchanged, row.Name, merged.PluralWord())})
		return
	}
	mf.Straza.Objects = &merged
	doc, err := yaml.Marshal(mf)
	if err != nil {
		a.fail(w, r, http.StatusInternalServerError, "Straza could not write the manifest of "+row.Name+" as a draft. Try again, and read the strazad log if it keeps failing.", err)
		return
	}
	a.openObjectsDraft(w, r, drafts.Item{Kind: drafts.KindApp, Name: row.Name, Op: drafts.OpPut, Doc: string(doc)})
}

// openObjectsDraft stores item as a new draft of the api door by the caller,
// as handleDraftCreate stores a draft: intake first, the open limit, then
// the check and the records, answering 201 with the draft and its verdict.
// The draft carries no note, because a note reads as the caller's own words.
func (a *App) openObjectsDraft(w http.ResponseWriter, r *http.Request, item drafts.Item) {
	ctx := r.Context()
	author, err := a.directAuthor(ctx)
	var c draftCaller
	if err == nil {
		c, err = a.proposerCaller(ctx, author)
	}
	if err != nil {
		a.fail(w, r, http.StatusInternalServerError, "Straza could not read who you are to open the draft. Try again, and read the strazad log if it keeps failing.", err)
		return
	}
	c.door = drafts.DoorAPI
	d := drafts.Draft{Revision: 1, State: drafts.StateOpen, Door: drafts.DoorAPI, Authors: []drafts.Principal{author}, Items: []drafts.Item{item}}
	fs := bodyIntake(d, author, nil)
	if refuseCertain(w, fs) {
		return
	}
	open, err := a.store.Drafts().CountOpen(ctx, author.UserID)
	if err != nil {
		err = fmt.Errorf("the open drafts of %s cannot be counted: %w", author.Username, err)
		a.fail(w, r, http.StatusServiceUnavailable, worldReadRefusal(err), err)
		return
	}
	if limit := drafts.OpenLimit(author.Username, open); len(limit) > 0 {
		refuseIntake(w, limit)
		return
	}
	a.storeNew(w, r, c, d, rowsOf(d.Items), "", fs)
}

// mergeObjects answers the declared block with what the pinned block keeps
// over it: the plural, every pinned mark at least as strict as the
// server's own, and the pinned mark of a tool the server leaves unmarked,
// which a person decided. The attributes and the listing tool come from
// the marks.
func mergeObjects(declared manager.ObjectsSpec, pinned *manager.ObjectsSpec) manager.ObjectsSpec {
	out := declared
	out.Tools = maps.Clone(declared.Tools)
	if pinned == nil {
		return out
	}
	out.Plural = pinned.Plural
	for name, r := range pinned.Tools {
		if mark, ok := declared.Tools[name]; !ok || r.AtLeastAsStrict(mark) {
			if out.Tools == nil {
				out.Tools = map[string]manager.ToolReach{}
			}
			out.Tools[name] = r
		}
	}
	return out
}

// sameObjects reports whether two blocks read the same, an absent list and
// an empty one alike.
func sameObjects(a, b manager.ObjectsSpec) bool {
	ja, errA := json.Marshal(a)
	jb, errB := json.Marshal(b)
	return errA == nil && errB == nil && string(ja) == string(jb)
}
