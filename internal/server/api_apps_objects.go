package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/strazahq/straza/internal/manager"
	"github.com/strazahq/straza/internal/objects"
	"github.com/strazahq/straza/internal/secrets"
	"github.com/strazahq/straza/internal/store"
)

// objectsAnswer is GET /v1/admin/apps/{id}/objects: the pinned declaration,
// what the server's live marks declare, the last listing and one page of
// the object list this replica holds.
type objectsAnswer struct {
	App            string                    `json:"app"`
	Type           string                    `json:"type"`
	Plural         string                    `json:"plural"`
	Attributes     []manager.ObjectAttribute `json:"attributes"`
	Pinned         *manager.ObjectsSpec      `json:"pinned"`
	Declared       *manager.ObjectsSpec      `json:"declared"`
	DeclaredFaults []string                  `json:"declaredFaults"`
	Drift          bool                      `json:"drift"`
	Listing        *objectsListing           `json:"listing"`
	Objects        []objectPayload           `json:"objects"`
	Total          int                       `json:"total"`
	Next           string                    `json:"next,omitempty"`
}

// objectsListing is the status of a server's listing.
type objectsListing struct {
	Tool        string `json:"tool"`
	Credential  string `json:"credential"`
	Outcome     string `json:"outcome"`
	Reason      string `json:"reason,omitempty"`
	Version     int64  `json:"version"`
	Count       int    `json:"count"`
	Joined      int    `json:"joined"`
	Left        int    `json:"left"`
	ListedAt    string `json:"listedAt,omitempty"`
	AttemptedAt string `json:"attemptedAt,omitempty"`
	DueAt       string `json:"dueAt,omitempty"`
}

// objectPayload is one object with the roles that cover it and the open
// drafts that would add or drop one.
type objectPayload struct {
	Value      string                  `json:"value"`
	ID         string                  `json:"id"`
	Title      string                  `json:"title"`
	Attributes map[string]objects.Attr `json:"attributes"`
	New        bool                    `json:"new"`
	CoveredBy  []string                `json:"coveredBy"`
	Drafts     []objectDraft           `json:"drafts,omitempty"`
}

// handleAppObjects answers a server's object list from this replica's
// memory, filtered by q and paged by after and limit, and on each entry
// the open drafts the caller may read that would add or drop a role on it,
// read from the store as objectDrafts says.
func (a *App) handleAppObjects(w http.ResponseWriter, r *http.Request) {
	row, mf, ok := a.objectsApp(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	limit := 500
	if s := q.Get("limit"); s != "" {
		n, err := strconv.Atoi(s)
		if err != nil || n < 1 || n > 1000 {
			apiError(w, http.StatusBadRequest, "limit must be a whole number from 1 to 1000.")
			return
		}
		limit = n
	}
	out := objectsAnswer{App: row.Name, Attributes: []manager.ObjectAttribute{}, DeclaredFaults: []string{}, Objects: []objectPayload{}}
	pinned := pinnedObjects(mf)
	if tools, err := a.manager.LiveInventory(row.Name); err == nil {
		spec, ok, faults := manager.DeclaredObjects(row.Name, tools)
		if ok {
			out.Declared = &spec
		}
		// The block a draft of the marks would pin, or the pinned block of a
		// server with no marks, read against the live tools as the draft
		// check reads it. An argument the schema no longer requires shows
		// here, the tab the gateway's drift refusal sends the admin to.
		switch {
		case ok && len(faults) == 0:
			faults, _, _ = manager.ReviewObjects(row.Name, mergeObjects(spec, pinned), tools)
		case !ok && pinned != nil:
			faults, _, _ = manager.ReviewObjects(row.Name, *pinned, tools)
		}
		out.DeclaredFaults = append(out.DeclaredFaults, faults...)
	}
	if pinned == nil {
		writeJSON(w, http.StatusOK, out)
		return
	}
	out.Type, out.Plural, out.Pinned = pinned.Type, pinned.PluralWord(), pinned
	out.Attributes = append(out.Attributes, pinned.List.Attributes...)
	// Drift is what a draft of the marks would change in the pinned block.
	out.Drift = out.Declared != nil && !sameObjects(mergeObjects(*out.Declared, pinned), *pinned)

	list := a.objects.Get(row.ID)
	if list != nil && list.DeclHash != objects.DeclHash(*pinned) {
		list = nil // the loop has not started the new declaration's list yet
	}
	out.Listing = listingStatus(list, pinned.List.Tool, a.listingCredential(row.ID, mf))
	if list == nil {
		writeJSON(w, http.StatusOK, out)
		return
	}
	covered := a.coveringRoles(row.Name, *pinned)
	match := strings.ToLower(q.Get("q"))
	var shown []objects.Object
	for _, o := range list.Objects {
		if match == "" || strings.Contains(strings.ToLower(o.Value), match) || strings.Contains(strings.ToLower(o.Title), match) {
			shown = append(shown, o)
		}
	}
	out.Total = len(shown)
	after := q.Get("after")
	start := sort.Search(len(shown), func(i int) bool { return shown[i].Value > after })
	end := min(start+limit, len(shown))
	inDrafts, err := a.objectDrafts(r.Context(), row.Name, list)
	if err != nil {
		if !a.answerOutage(w, r, "server objects", err) {
			a.fail(w, r, http.StatusInternalServerError, objectDraftsRefusal, err)
		}
		return
	}
	for _, o := range shown[start:end] {
		out.Objects = append(out.Objects, objectPayload{Value: o.Value, ID: o.ID, Title: o.Title, Attributes: o.Attributes,
			New: list.Version > 1 && o.AddedVersion == list.Version, CoveredBy: covered(o.ID), Drafts: inDrafts(o.ID)})
	}
	if end < len(shown) {
		out.Next = shown[end-1].Value
	}
	writeJSON(w, http.StatusOK, out)
}

// handleAppObjectsRefresh asks for a listing of the server now, unless it
// was listed within the last minute or the lists are off. The listing it
// causes is audited; the request is not.
func (a *App) handleAppObjectsRefresh(w http.ResponseWriter, r *http.Request) {
	row, mf, ok := a.objectsApp(w, r)
	if !ok {
		return
	}
	if pinnedObjects(mf) == nil {
		apiError(w, http.StatusConflict, fmt.Sprintf("%[1]s pins no object type, so there is nothing to list. "+
			"Pin its objects first with strazactl apps objects draft %[1]s, then publish the draft.", row.Name))
		return
	}
	due, since, err := a.objects.Refresh(r.Context(), row.ID)
	if errors.Is(err, objects.ErrOff) {
		apiError(w, http.StatusConflict, fmt.Sprintf("Straza does not list objects on this deployment, because apps.objects.enabled is false. "+
			"Turn it back on to list %s's %s.", row.Name, pinnedObjects(mf).PluralWord()))
		return
	}
	if errors.Is(err, objects.ErrTooSoon) {
		ago := int(since / time.Second)
		wait := max(1, int(math.Ceil((time.Minute - since).Seconds())))
		apiError(w, http.StatusTooManyRequests, fmt.Sprintf("%s was listed %s ago. Refresh again in %s.", row.Name, seconds(ago), seconds(wait)))
		return
	}
	if err != nil {
		a.fail(w, r, http.StatusInternalServerError, "the refresh could not be stored. Try again, and read the strazad log if it keeps failing", err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]string{"app": row.Name, "dueAt": rfc3339(due)})
}

// handleAppListingCredentialSet stores the server's listing credential.
// The answer carries a fingerprint and never the value.
func (a *App) handleAppListingCredentialSet(w http.ResponseWriter, r *http.Request) {
	row, err := a.appByRef(r)
	if err != nil {
		apiError(w, http.StatusNotFound, "unknown server")
		return
	}
	var req struct {
		Value string `json:"value"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Value == "" {
		apiError(w, http.StatusBadRequest, "value is required")
		return
	}
	mf, err := manager.FromJSON(row.Manifest)
	if err != nil {
		a.fail(w, r, http.StatusInternalServerError, "stored manifest unreadable (reinstall the server)", err)
		return
	}
	if msg := listingCredentialRefusal(row.Name, mf); msg != "" {
		apiError(w, http.StatusConflict, msg)
		return
	}
	cred, err := a.broker.SetListing(r.Context(), row.ID, req.Value)
	if err != nil {
		a.fail(w, r, http.StatusInternalServerError, "store the listing credential failed", err)
		return
	}
	a.listingCredentialChanged(r, row, "apps.secret.set")
	setAt := cred.CreatedAt
	if cred.RotatedAt != nil {
		setAt = *cred.RotatedAt
	}
	writeJSON(w, http.StatusCreated, map[string]string{
		"app": row.Name, "fingerprint": secrets.Fingerprint(req.Value), "setAt": rfc3339(setAt),
	})
}

// handleAppListingCredentialDelete removes the server's listing
// credential. It refuses nothing but an absent one, so a credential left
// behind by an earlier pin can always go.
func (a *App) handleAppListingCredentialDelete(w http.ResponseWriter, r *http.Request) {
	row, err := a.appByRef(r)
	if err != nil {
		apiError(w, http.StatusNotFound, "unknown server")
		return
	}
	err = a.broker.DeleteListing(r.Context(), row.ID)
	if errors.Is(err, store.ErrNotFound) {
		apiError(w, http.StatusNotFound, row.Name+" has no listing credential stored, so there is nothing to remove.")
		return
	}
	if err != nil {
		a.fail(w, r, http.StatusInternalServerError, "remove the listing credential failed", err)
		return
	}
	a.listingCredentialChanged(r, row, "apps.secret.remove")
	w.WriteHeader(http.StatusNoContent)
}

// listingCredentialChanged makes the server's listing due at once, records
// the change with the actor, and tells the other replicas to reload their
// broker, with the same event the secret routes send.
func (a *App) listingCredentialChanged(r *http.Request, row store.App, action string) {
	if err := a.objects.MarkDue(r.Context(), row.ID); err != nil && !errors.Is(err, objects.ErrOff) {
		a.log.Warn("object lists: mark a listing due failed", "app", row.Name, "err", err)
	}
	a.emitEvent(r, "straza.audit.admin", map[string]any{
		"action": action, "app": row.Name, "role": "", "scope": store.CredScopeListing,
	})
	a.emitEvent(r, "straza.apps.updated", map[string]any{"change": "secret", "app": row.Name})
}

// objectsApp reads the server the route names and its stored manifest,
// answering the refusal itself when it cannot.
func (a *App) objectsApp(w http.ResponseWriter, r *http.Request) (store.App, manager.Manifest, bool) {
	row, err := a.appByRef(r)
	if err != nil {
		apiError(w, http.StatusNotFound, "unknown server")
		return store.App{}, manager.Manifest{}, false
	}
	mf, err := manager.FromJSON(row.Manifest)
	if err != nil {
		a.fail(w, r, http.StatusInternalServerError, "stored manifest unreadable (reinstall the server)", err)
		return store.App{}, manager.Manifest{}, false
	}
	return row, mf, true
}

// pinnedObjects answers the straza.objects block of mf, nil when it pins
// no type.
func pinnedObjects(mf manager.Manifest) *manager.ObjectsSpec {
	if mf.Straza.Objects == nil || mf.Straza.Objects.Type == "" {
		return nil
	}
	return mf.Straza.Objects
}

// listingCredentialRefusal says why the server of mf keeps no listing
// credential, empty when it takes one.
func listingCredentialRefusal(app string, mf manager.Manifest) string {
	switch {
	case pinnedObjects(mf) == nil:
		return fmt.Sprintf("%[1]s pins no object type, so a listing credential has no use. "+
			"Pin its objects first with strazactl apps objects draft %[1]s, then publish the draft.", app)
	case mf.Straza.Runtime.Kind != manager.RuntimeRemote:
		return app + " runs as one process with one identity, so Straza lists its objects with that identity and keeps no listing credential for it."
	case !manager.ListingNeedsSecret(mf):
		return app + " takes no credential, so Straza lists its objects without one and keeps no listing credential for it."
	}
	return ""
}

// listingCredential is the listing credential state of a server: set,
// missing, or not-needed when its listing carries none.
func (a *App) listingCredential(appID string, mf manager.Manifest) string {
	switch {
	case !manager.ListingNeedsSecret(mf):
		return "not-needed"
	case a.broker.ListingSecret(appID) != nil:
		return "set"
	}
	return "missing"
}

// listingStatus is the listing status of list, a never-listed one when
// this replica holds no list yet.
func listingStatus(list *objects.List, tool, credential string) *objectsListing {
	out := &objectsListing{Tool: tool, Credential: credential, Outcome: store.ObjectsNever}
	if list == nil {
		return out
	}
	out.Outcome, out.Reason, out.Version = list.Outcome, list.Reason, list.Version
	out.Count, out.Joined, out.Left = list.Count, list.Joined, list.Left
	out.ListedAt, out.AttemptedAt = rfc3339Of(list.ListedAt), rfc3339Of(list.AttemptedAt)
	if !list.DueAt.IsZero() {
		out.DueAt = rfc3339(list.DueAt)
	}
	return out
}

// rfc3339Of is rfc3339 of t, empty for nil.
func rfc3339Of(t *time.Time) string {
	if t == nil {
		return ""
	}
	return rfc3339(*t)
}

// seconds is n with its unit, "1 second" or "n seconds".
func seconds(n int) string {
	if n == 1 {
		return "1 second"
	}
	return strconv.Itoa(n) + " seconds"
}
