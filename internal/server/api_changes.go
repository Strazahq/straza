package server

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// The admin change feed (IGA liveSync): GET /v1/admin/changes pages
// the events outbox as an ordered history so an IGA system (midPoint LiveSync
// via universal-rest-connector's SyncOp) picks up per-object-type deltas
// incrementally instead of full reconciliation. Admin plane: store reads are
// allowed here; the data plane never serves this. Delivery contract is
// at-least-once with a strictly-advancing cursor; consumers dedupe by cursor
// and keep periodic recon as the safety net (multi-pod id ordering is only as
// good as pod clocks; single-writer deployments are exact).

// changeTypes maps feed object types to the outbox subjects that carry them.
// user and role share straza.identity.* (the CE carries a bare object id);
// rows are classified per object after the fetch. The "group" type retired
// with the unified role model (spec/scim-profile rev 12): membership
// changes emit BOTH the user id and the role id, and role events are the
// sync driver for the wire-group render.
var changeTypes = map[string][]string{
	"user":    {"straza.identity.created", "straza.identity.updated", "straza.identity.deactivated", "straza.revocation.lift"},
	"role":    {"straza.identity.created", "straza.identity.updated", "straza.identity.deactivated"},
	"app":     {"straza.apps.deployed", "straza.apps.removed"},
	"tool":    {"straza.apps.drift"},
	"binding": {"straza.apps.updated"},
}

// identityActionNoise are identity.updated actions that do not change the
// IGA-visible object (session churn, gate denials), skipped so a liveSync
// consumer is not told to re-read a user on every checkin.
var identityActionNoise = map[string]bool{
	"session.start": true, "checkin.denied": true, "enroll": true,
	"oauth.connect.refused": true,
}

type changeRecord struct {
	Cursor string `json:"cursor"`
	Type   string `json:"type"`
	Op     string `json:"op"` // create | update | delete
	ID     string `json:"id"` // object id (empty when the event names no object)
	At     string `json:"at"`
}

type changesResponse struct {
	Changes    []changeRecord `json:"changes"`
	NextCursor string         `json:"nextCursor"`
	More       bool           `json:"more"`
	// Head is the newest outbox id at read time: first-time consumers adopt
	// it as their cursor (sync-from-now) instead of paging history.
	Head string `json:"head"`
}

// handleChangesList serves GET /v1/admin/changes?since=<cursor>&types=a,b&limit=n.
// An empty `since` starts at genesis; first-time consumers should prefer a
// full reconciliation and then adopt the cursor from the feed head.
func (a *App) handleChangesList(w http.ResponseWriter, r *http.Request) {
	since := r.URL.Query().Get("since")
	limit := 200
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 1000 {
			limit = n
		}
	}
	requested := map[string]bool{}
	if v := r.URL.Query().Get("types"); v != "" {
		for _, t := range strings.Split(v, ",") {
			t = strings.TrimSpace(t)
			if _, ok := changeTypes[t]; !ok && t != "" {
				apiError(w, http.StatusBadRequest, "unknown type "+t+" (user, role, app, tool, binding)")
				return
			}
			if t != "" {
				requested[t] = true
			}
		}
	}
	if len(requested) == 0 {
		for t := range changeTypes {
			requested[t] = true
		}
	}
	subjectSet := map[string]bool{}
	for t := range requested {
		for _, s := range changeTypes[t] {
			subjectSet[s] = true
		}
	}
	subjects := make([]string, 0, len(subjectSet))
	for s := range subjectSet {
		subjects = append(subjects, s)
	}

	head, err := a.store.Outbox().Head(r.Context())
	if err != nil {
		a.fail(w, r, http.StatusInternalServerError, "list changes failed", err)
		return
	}
	// Fetch one page. `more` is answered by asking for one extra row.
	events, err := a.store.Outbox().ListAfter(r.Context(), since, subjects, limit+1)
	if err != nil {
		a.fail(w, r, http.StatusInternalServerError, "list changes failed", err)
		return
	}
	more := len(events) > limit
	if more {
		events = events[:limit]
	}

	out := changesResponse{Changes: []changeRecord{}, Head: head}
	for _, ev := range events {
		out.NextCursor = ev.ID // every consumed row advances the cursor, even skipped noise
		rec, ok := a.classifyChange(r, ev.Subject, ev.CE)
		if !ok {
			continue
		}
		// "identity" = an id no longer resolvable to user/role (hard
		// delete): valid for any identity consumer, as a delete marker.
		wanted := requested[rec.Type] ||
			(rec.Type == "identity" && (requested["user"] || requested["role"]))
		if !wanted {
			continue
		}
		rec.Cursor = ev.ID
		rec.At = ev.CreatedAt.UTC().Format(time.RFC3339Nano)
		out.Changes = append(out.Changes, rec)
	}
	out.More = more
	writeJSON(w, http.StatusOK, out)
}

// classifyChange maps one outbox CE to a feed record. Identity events carry a
// bare object id (users and roles share the channel), so the object is
// resolved against the store; an id found in neither table was hard-deleted,
// surfaced as type "identity" with op delete, matching both user and role
// requests (a delete delta for an unknown shadow is a no-op downstream).
func (a *App) classifyChange(r *http.Request, subject, ce string) (changeRecord, bool) {
	var env struct {
		Data map[string]any `json:"data"`
	}
	_ = json.Unmarshal([]byte(ce), &env)
	str := func(k string) string {
		v, _ := env.Data[k].(string)
		return v
	}

	switch subject {
	case "straza.identity.created", "straza.identity.updated", "straza.identity.deactivated":
		if identityActionNoise[str("action")] {
			return changeRecord{}, false
		}
		id := str("id")
		if id == "" {
			id = str("user")
		}
		op := "update"
		if subject == "straza.identity.created" {
			op = "create"
		}
		kind := a.identityKind(r, id)
		if kind == "identity" && id != "" {
			op = "delete" // the object is gone; tell consumers to drop the shadow
		}
		return changeRecord{Type: kind, Op: op, ID: id}, true
	case "straza.revocation.lift":
		return changeRecord{Type: "user", Op: "update", ID: str("user")}, true
	case "straza.apps.deployed":
		return changeRecord{Type: "app", Op: "create", ID: str("app")}, true
	case "straza.apps.removed":
		return changeRecord{Type: "app", Op: "delete", ID: str("app")}, true
	case "straza.apps.drift":
		return changeRecord{Type: "tool", Op: "update", ID: str("app")}, true
	case "straza.apps.updated":
		// Advisory event; only binding changes are IGA-model-visible. The
		// consumer re-reads the app's bindings (the event names no binding id).
		if str("change") != "binding" {
			return changeRecord{}, false
		}
		return changeRecord{Type: "binding", Op: "update", ID: str("app")}, true
	}
	return changeRecord{}, false
}

// identityKind resolves an identity-event object id to user | role (role
// CRUD, assignment and membership changes ride the same channel); vanished
// ids (hard deletes) answer "identity", a delete marker valid for any
// identity consumer.
func (a *App) identityKind(r *http.Request, id string) string {
	if id == "" {
		return "identity"
	}
	if _, err := a.store.Users().GetByID(r.Context(), id); err == nil {
		return "user"
	}
	if _, err := a.store.Roles().GetByID(r.Context(), id); err == nil {
		return "role"
	}
	return "identity"
}
