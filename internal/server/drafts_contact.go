package server

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/strazahq/straza/internal/drafts"
	"github.com/strazahq/straza/internal/manager"
	"github.com/strazahq/straza/internal/store"
)

// The sentences of Contact that name who may contact and what.
const (
	contactAdminAPIRefusal = "An admin API token carries no person and cannot contact a proposed server. A person contacts it on the console or with strazactl drafts contact."
	contactAgentRefusal    = "This identity is an agent, and an agent cannot contact a proposed server, even with an administrator role. A person contacts it on the console or with strazactl drafts contact."
	contactStandingRefusal = "Contacting a proposed server needs the scope apps:write or the role " + MCPAdminRole + ", because Straza dials the address the draft names."
	contactBodyRefusal     = "The request body is not a contact: %s. Send object as JSON, such as App/github."
	contactObjectRefusal   = "object must name a server of the draft as App/<name>, such as App/github."
	contactMovedRefusal    = "Draft %s changed while Straza contacted %s, so the tool names were not kept. Contact it again."
	contactStoreRefusal    = "Straza could not keep the tool names %s answered with, so nothing was stored. Contact it again, and read the strazad log if it keeps failing."
)

// requireContact guards the contact route: it authenticates the
// request and refuses a session a coding harness checked in, as
// requireDrafts does, and then passes only a person, active and not locked,
// who holds the root role, the scope apps:write or straza-global-mcp-admin.
// No drafts grant opens it, because Straza dials on a person's request.
func (a *App) requireContact(next func(http.ResponseWriter, *http.Request, draftCaller)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		p, ok := a.authenticateAdmin(w, r)
		if !ok || a.refuseCodingHarness(w, r, p) {
			return
		}
		c, ok := a.draftCallerOf(w, r, p)
		if !ok {
			return
		}
		if msg := c.contactRefusal(); msg != "" {
			apiError(w, http.StatusForbidden, msg)
			return
		}
		next(w, r.WithContext(withStanding(withActor(r.Context(), p.actor), adminStanding{})), c)
	}
}

// contactRefusal answers the 403 sentence that refuses c a contact, or "":
// step 1 of a publish in the words of a contact, then the apps standing.
func (c draftCaller) contactRefusal() string {
	name := c.author.Username
	switch {
	case c.adminAPI():
		return contactAdminAPIRefusal
	case !c.person:
		return contactAgentRefusal
	case c.disabled:
		return fmt.Sprintf("The user %s is disabled, so it cannot contact a proposed server. Ask an administrator to enable it again.", name)
	case c.locked:
		return fmt.Sprintf("The user %s is locked, so it cannot contact a proposed server. An administrator lifts the lock with strazactl users unlock %s.", name, name)
	case !c.p.root && !c.p.scope.Grants["apps:write"]:
		return contactStandingRefusal
	}
	return ""
}

// contactedPayload is the 200 of a contact.
type contactedPayload struct {
	Object      string          `json:"object"`
	Host        string          `json:"host"`
	ContactedAt string          `json:"contacted_at"`
	Server      contactedServer `json:"server"`
	Tools       []contactedTool `json:"tools"`
}

// contactedServer is the name and version a server's initialize result
// gives, its own text.
type contactedServer struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

// contactedTool is one tool a server listed, in its own words.
type contactedTool struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	ReadOnly    bool   `json:"read_only"`
}

// contactHandler is POST /v1/admin/drafts/{id}/contact: it
// opens one MCP connection through d to the address of an App put of a
// remote server, with no credential, lists its tools, closes, stores the
// tool names on the item with SetOffered and writes one draft.contact
// record. Nothing starts and no row is written but the item's.
func (a *App) contactHandler(d contactDialer) func(http.ResponseWriter, *http.Request, draftCaller) {
	return func(w http.ResponseWriter, r *http.Request, c draftCaller) {
		var body struct {
			Object string `json:"object"`
		}
		if !decodeDraftBody(w, r, &body, contactBodyRefusal, false) {
			return
		}
		kind, name, _ := strings.Cut(body.Object, "/")
		if kind != string(drafts.KindApp) || name == "" {
			apiError(w, http.StatusBadRequest, contactObjectRefusal)
			return
		}
		row, rows, _, ok := a.readDraft(w, r, c)
		if !ok {
			return
		}
		id := strconv.FormatInt(row.ID, 10)
		if row.State != string(drafts.StateOpen) {
			apiError(w, http.StatusConflict, movedRefusal(row, row.Revision))
			return
		}
		item, ok := appPutOf(rows, name)
		if !ok {
			apiError(w, http.StatusNotFound, fmt.Sprintf("Draft %s holds no server %s.", id, name))
			return
		}
		mf, err := manager.Parse([]byte(item.Doc))
		if err != nil {
			a.fail(w, r, http.StatusInternalServerError, fmt.Sprintf("Straza could not read the manifest of %s in draft %s, so it contacted nothing. "+
				"Read the strazad log for the cause.", name, id), err)
			return
		}
		if err := a.manager.CheckRuntime(mf); err != nil {
			apiError(w, http.StatusConflict, err.Error())
			return
		}
		if rt := mf.Straza.Runtime; rt.Remote == nil {
			runs := "command"
			if rt.OCI != nil {
				runs = "container"
			}
			apiError(w, http.StatusConflict, fmt.Sprintf("%s runs as a %s, and Straza starts such a server only when a person publishes it.", name, runs))
			return
		}
		got, fault := d.contact(r.Context(), mf.Straza.Runtime.Remote.URL)
		a.recordContact(r.Context(), id, name, got, fault)
		if fault != nil {
			apiError(w, fault.status, fault.sentence)
			return
		}
		a.keepOffered(w, r, row, item, got)
	}
}

// appPutOf answers the App put of rows named name.
func appPutOf(rows []store.DraftItemRow, name string) (store.DraftItemRow, bool) {
	for _, it := range rows {
		if it.Kind == string(drafts.KindApp) && it.Name == name && it.Op == string(drafts.OpPut) {
			return it, true
		}
	}
	return store.DraftItemRow{}, false
}

// keepOffered stores the tool names got read on item with the digest of
// the document they were read for, and answers the contact's 200. A draft
// that moved on while the contact ran keeps nothing and answers 409.
func (a *App) keepOffered(w http.ResponseWriter, r *http.Request, row store.DraftRow, item store.DraftItemRow, got contactAnswer) {
	id := strconv.FormatInt(row.ID, 10)
	sum := sha256.Sum256([]byte(item.Doc))
	names := make([]string, len(got.tools))
	for i, t := range got.tools {
		names[i] = t.Name
	}
	at := rfc3339(got.at)
	raw, _ := json.Marshal(offered{Digest: hex.EncodeToString(sum[:]), Tools: names, At: at})
	err := a.store.Drafts().SetOffered(r.Context(), row.ID, row.Revision, store.ObjectRef{Kind: item.Kind, Name: item.Name}, string(raw))
	switch {
	case errors.Is(err, store.ErrConflict), errors.Is(err, store.ErrNotFound):
		apiError(w, http.StatusConflict, fmt.Sprintf(contactMovedRefusal, id, item.Name))
		return
	case err != nil:
		a.storeFailed(w, r, fmt.Sprintf(contactStoreRefusal, item.Name), err)
		return
	}
	writeJSON(w, http.StatusOK, contactedPayload{Object: string(drafts.KindApp) + "/" + item.Name, Host: got.host, ContactedAt: at,
		Server: got.server, Tools: got.tools})
}

// recordContact writes the one draft.contact of a contact of app in the
// draft id: the host, whether it answered, was refused or
// failed, and how many tools it listed.
func (a *App) recordContact(ctx context.Context, id, app string, got contactAnswer, fault *contactFault) {
	outcome, tools := outcomeAnswered, len(got.tools)
	if fault != nil {
		outcome, tools = fault.outcome, 0
	}
	a.emitEventCtx(context.WithoutCancel(ctx), "straza.audit.admin", map[string]any{"action": "draft.contact", "draft": id, "app": app,
		"host": got.host, "outcome": outcome, "tools": tools})
}
