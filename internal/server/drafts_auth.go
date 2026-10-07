package server

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"

	"github.com/strazahq/straza/internal/drafts"
	"github.com/strazahq/straza/internal/store"
)

// The credential lanes an admin request comes in on, in the words of the
// audit actor, and the client a revision records for a login and for an
// admin API token.
const (
	laneLogin      = "login"
	laneSession    = "session"
	laneAdminAPI   = "api-token"
	clientLogin    = "id-token"
	clientAdminAPI = "api-token"
)

// The refusals of a caller who may not use the drafts routes: a
// person short of every grant that opens them, and an agent, which only
// root or a drafts grant opens them to. An admin API token's refusal names
// the grants it holds (tokenAccessRefusal).
const (
	draftsAccessRefusal      = "Drafts need the scope drafts:read to read or drafts:write to change, or a role that may change servers, roles or policy sets. Ask an administrator for one."
	draftsAccessRefusalAgent = "An agent reaches drafts only with the scope drafts:read to read or drafts:write to change, whatever roles it holds. Ask an administrator for that grant."
)

// The refusals of step 1 of a publish: only a person publishes.
const (
	publishRefusalAdminAPI = "An admin API token carries no person and cannot publish a draft. It may create drafts. A person publishes them on the console or with strazactl."
	publishRefusalAgent    = "This identity is an agent, and an agent cannot publish a draft, even with an administrator role. A person with standing over every object in the draft publishes it on the console or with strazactl."
)

// The refusals of admin.secondPerson: an author of a draft, the
// minter of a token that wrote one, a token whose minter is unknown, the
// sponsor of an agent that wrote one, a draft whose authors could not be
// read, and a direct route.
const (
	secondPersonAuthorRefusal  = "You changed this draft, and this deployment needs a second person to publish a change that widens access (admin.secondPerson). Ask another administrator with standing over every object in it to review and publish it."
	secondPersonMinterRefusal  = "You minted the admin API token %s, which changed this draft, and this deployment needs a second person to publish a change that widens access (admin.secondPerson). Ask another administrator with standing over every object in it to review and publish it."
	secondPersonUnknownRefusal = "The admin API token %s changed this draft, and Straza cannot tell who minted it, so it cannot tell whether you are a second person, which this deployment needs to publish a change that widens access (admin.secondPerson). Create a new draft from its documents and ask another administrator to publish it."
	secondPersonSponsorRefusal = "You sponsor %s, which proposed this draft, and this deployment needs a publisher other than the proposer and its sponsor for a change that widens access (admin.secondPerson). Ask another administrator to review and publish it."
	secondPersonUnreadRefusal  = "Straza could not read who wrote this draft, so it cannot tell whether you are a second person, and it refuses the publish. Try again, and check the strazad log if it keeps failing."
	secondPersonDirectRefusal  = "This deployment needs a second person to publish a change that widens access (admin.secondPerson), so this route cannot make it alone. Save it as a draft on the console under Drafts or with strazactl drafts create, and ask another administrator to publish it."
)

// draftCaller is who calls a drafts route: the authenticated principal,
// whether it is a person, the servers it administers, and the author and
// the door that a draft written on the request records.
type draftCaller struct {
	p adminPrincipal
	// person is true for a user whose row reads as a person by userKind,
	// and false for an agent, a service account and an admin API token.
	person bool
	// servers maps the id of every live server whose admin role the caller
	// holds to the server's name. It stays nil for root.
	servers map[string]string
	// disabled and locked say that the user row is not active, or that the
	// lock denylist blocks the user, which a publish refuses.
	disabled, locked bool
	author           drafts.Principal
	door             drafts.Door
	// Change is true on a direct admin route, whose caller changed one
	// object and sent no draft, so a refusal speaks of the change and never
	// of a draft.
	Change bool
}

// requireDrafts guards the drafts routes. It authenticates the
// request as requireAdmin does and refuses a session that a coding harness
// checked in, then reads who the caller is and passes root, a principal
// whose grants hold drafts at the route's verb, and a person whose grants
// hold apps, identity or policy at that verb or who administers a server.
// Anyone else gets useRefusal's 403. A user row that cannot be read fails
// closed, with 503 for a store outage and 403 otherwise, because whether
// the caller is a person decides what it may do. next receives the caller,
// and the request carries no standing, so a helper of the direct routes
// that reads standingFrom meets none and fails closed.
func (a *App) requireDrafts(next func(http.ResponseWriter, *http.Request, draftCaller)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		p, ok := a.authenticateAdmin(w, r)
		if !ok || a.refuseCodingHarness(w, r, p) {
			return
		}
		c, ok := a.draftCallerOf(w, r, p)
		if !ok {
			return
		}
		if msg := c.useRefusal(r); msg != "" {
			apiError(w, http.StatusForbidden, msg)
			return
		}
		next(w, r.WithContext(withStanding(withActor(r.Context(), p.actor), adminStanding{})), c)
	}
}

// accessRefusal is the drafts-route refusal in the words that fit c and the
// request's method.
func (c draftCaller) accessRefusal(method string) string {
	switch {
	case c.adminAPI():
		return tokenAccessRefusal(c.p.scope.Grants, method)
	case !c.person:
		return draftsAccessRefusalAgent
	}
	return draftsAccessRefusal
}

// tokenAccessRefusal is the drafts-route refusal for an admin API token whose
// grants are grants: the drafts scope it holds, the one the method needs,
// and a token that holds both, since a token's scopes are fixed when it is
// minted and strazactl reads a draft before it changes one.
func tokenAccessRefusal(grants map[string]bool, method string) string {
	held := "no drafts scope"
	switch {
	case grants["drafts:read"]:
		held = "drafts:read"
	case grants["drafts:write"]:
		held = "drafts:write"
	}
	need := "writing or checking a draft needs drafts:write"
	if method == http.MethodGet {
		need = "reading drafts needs drafts:read"
	}
	return "This token holds " + held + ", and " + need + ". Mint a token with the scopes drafts:read and drafts:write with strazactl api-token create."
}

// draftCallerOf reads who p is for the drafts routes and answers the
// request itself when a read fails: the user row, whether it reads as a
// person and whether it is disabled or locked, the sponsor an agent's row
// names and whether that sponsor answers for it, and the servers whose
// admin role the user holds. The
// door comes from the credential and never from the request: a session of
// the console is console, a session of strazactl is strazactl, and
// everything else is api. An admin API token reads nothing.
func (a *App) draftCallerOf(w http.ResponseWriter, r *http.Request, p adminPrincipal) (draftCaller, bool) {
	c := draftCaller{p: p, door: drafts.DoorAPI,
		author: drafts.Principal{UserID: p.actor.ID, Username: p.actor.Name, Via: p.actor.Via, Client: clientAdminAPI}}
	if c.adminAPI() {
		return c, true
	}
	ctx := r.Context()
	u, err := a.store.Users().GetByID(ctx, p.actor.ID)
	if err != nil {
		if !a.answerOutage(w, r, "drafts caller", err) {
			a.log.Warn("drafts route: the caller's user row cannot be read, refused (fail closed)", "user", p.actor.ID, "err", err)
			apiError(w, http.StatusForbidden, unreadUserRefusal(r))
		}
		return c, false
	}
	c.person = personUser(u)
	c.disabled, c.locked = u.Status != store.UserActive, a.denylist.userBlocked(u.ID)
	c.author = drafts.Principal{UserID: u.ID, Username: u.Username, Agent: !c.person, Via: p.actor.Via, Client: clientLogin}
	if p.actor.Via == laneSession {
		c.author.Client = p.harness
		switch p.harness {
		case string(drafts.DoorConsole):
			c.door = drafts.DoorConsole
		case string(drafts.DoorStrazactl):
			c.door = drafts.DoorStrazactl
		}
	}
	if !c.person && u.Sponsor != "" {
		c.author.SponsorName = u.Sponsor
		sponsor, ok, err := accountableSponsor(u, func(name string) (store.User, error) { return a.store.Users().GetByUsername(ctx, name) })
		if err != nil {
			if !a.answerOutage(w, r, "drafts caller", err) {
				a.fail(w, r, http.StatusInternalServerError, "Straza could not read the sponsor your user record names, so it refuses the request. "+
					"Try again, and check the strazad log if it keeps failing.", err)
			}
			return c, false
		}
		if ok {
			c.author.SponsorID = sponsor.ID
		}
	}
	if p.root {
		return c, true
	}
	ids := make([]string, len(p.roles))
	for i, role := range p.roles {
		ids[i] = role.ID
	}
	rows, err := a.store.Apps().ListByAdminRoles(ctx, ids)
	if err != nil {
		if !a.answerOutage(w, r, "drafts caller", err) {
			a.fail(w, r, http.StatusInternalServerError, "Straza could not read which servers you administer, so it refuses the request. "+
				"Try again, and check the strazad log if it keeps failing.", err)
		}
		return c, false
	}
	c.servers = make(map[string]string, len(rows))
	for _, row := range rows {
		c.servers[row.ID] = row.Name
	}
	return c, true
}

// unreadUserRefusal is the 403 sentence of a drafts route whose caller's
// user row could not be read, which names the publish on the publish route.
func unreadUserRefusal(r *http.Request) string {
	what := "the request"
	if strings.HasSuffix(r.URL.Path, "/publish") {
		what = "the publish"
	}
	return "Straza could not read your user record, so it cannot tell whether you are a person, and it refuses " + what +
		". Try again, and check the strazad log if it keeps failing."
}

// adminAPI reports whether c is an admin API token, which carries no user.
func (c draftCaller) adminAPI() bool { return c.p.roles == nil }

// mayUse reports whether c may use a drafts route with method: root,
// a principal whose grants hold drafts at the method's verb, or a person
// whose grants hold apps, identity or policy at that verb or who holds a
// server's admin role. straza-global-mcp-admin passes on the apps grants
// it carries. GET reads and every other method writes, as the admin API's
// grants read a method.
func (c draftCaller) mayUse(method string) bool {
	verb := "write"
	if method == http.MethodGet {
		verb = "read"
	}
	g := c.p.scope.Grants
	switch {
	case c.p.root || g["drafts:"+verb]:
		return true
	case !c.person:
		return false
	}
	return g["apps:"+verb] || g["identity:"+verb] || g["policy:"+verb] || len(c.servers) > 0
}

// mayRead reports whether c may list and read a draft whose current items
// are items, wrote saying whether c wrote a revision of it. Root and
// a holder of drafts:read read every draft. Anyone else reads the drafts
// they wrote a revision of, and the drafts whose every item is an App, or
// a role owned by an App, of a server they administer, so a server admin
// never reads another server's config through a draft.
func (c draftCaller) mayRead(wrote bool, items []store.DraftItemRow) bool {
	if c.p.root || c.p.scope.Grants["drafts:read"] || wrote {
		return true
	}
	for _, it := range items {
		if !c.administers(itemServer(it)) {
			return false
		}
	}
	return len(items) > 0
}

// itemServer names the server whose config the item is: an App's own name,
// and the server that owns a Role, read from the live document the item
// was stamped with, or from the item's own document for a role that did
// not exist then. An item not stamped yet names its owner only in its own
// document, which may claim any server, so it names none, and neither does
// a policy set.
func itemServer(it store.DraftItemRow) string {
	switch it.Kind {
	case string(drafts.KindApp):
		return it.Name
	case string(drafts.KindRole):
	default:
		return ""
	}
	doc := ""
	switch {
	case it.BaseOp == string(drafts.OpPut):
		doc = it.BaseDoc
	case it.BaseOp == string(drafts.OpRemove) && it.Op == string(drafts.OpPut):
		doc = it.Doc
	}
	rd, err := drafts.ParseRole(doc)
	if err != nil {
		return ""
	}
	return rd.Spec.Server
}

// administers reports whether c administers the server named name: root
// and a holder of apps:write administer every server, and anyone else the
// servers whose admin role they hold.
func (c draftCaller) administers(name string) bool {
	if name != "" && (c.p.root || c.p.scope.Grants["apps:write"]) {
		return true
	}
	return c.holdsAdminRoleOf(name)
}

// holdsAdminRoleOf reports whether c holds the admin role of the live
// server named name.
func (c draftCaller) holdsAdminRoleOf(name string) bool {
	for _, n := range c.servers {
		if n == name && name != "" {
			return true
		}
	}
	return false
}

// readRefusal answers the 403 sentence that refuses c a draft holding an
// item outside what c may read today, or "". A draft shows its
// author the verdict and the live documents of its items, so create,
// update and check refuse an item the author's read routes would not show:
// a server or a role a server owns needs the scope apps:read or that
// server's admin role, a global role needs identity:read, and a policy set
// policy:read. The sentence names every item c may not read and the grant
// each needs, so one answer says all that is missing. The straza-app door
// keeps its own rule, the agent's view.
func (c draftCaller) readRefusal(d drafts.Draft, w drafts.World) string {
	if c.p.root {
		return ""
	}
	g := c.p.scope.Grants
	var misses []readMiss
	lack := func(grant, object, why string) {
		for i := range misses {
			if misses[i].grant == grant {
				misses[i].objects = append(misses[i].objects, object)
				return
			}
		}
		misses = append(misses, readMiss{grant: grant, objects: []string{object}, why: why})
	}
	for _, it := range d.Items {
		switch it.Kind {
		case drafts.KindApp:
			if grant := c.serverGrant(w, it.Name); grant != "" {
				lack(grant, "the server "+it.Name, readWhyServers)
			}
		case drafts.KindRole:
			live, present := w.Roles[it.Name]
			owner, owned := live.Owner, live.Owned
			if !present {
				doc, err := drafts.ParseRole(it.Doc)
				owner, owned = doc.Spec.Server, err == nil && doc.Spec.Server != ""
			}
			switch grant := c.serverGrant(w, owner); {
			case owned && grant != "":
				lack(grant, "the role "+it.Name, readWhyServers)
			case !owned && !g["identity:read"]:
				lack("the scope identity:read", "the role "+it.Name, readWhyRoles)
			}
		default:
			if !g["policy:read"] {
				lack("the scope policy:read", "the policy set "+it.Name, readWhySets)
			}
		}
	}
	return c.readSentence(misses)
}

// serverGrant answers the grant c lacks to read the server named server,
// or "": the scope apps:read reads every server, and a server's admin role
// reads that server, which only a user can hold. A role whose server is
// gone needs apps:read.
func (c draftCaller) serverGrant(w drafts.World, server string) string {
	if c.p.scope.Grants["apps:read"] || c.holdsAdminRoleOf(server) {
		return ""
	}
	grant := "the scope apps:read"
	if app, ok := w.Apps[server]; ok && app.AdminRole != "" && !c.adminAPI() {
		grant += " or the role " + app.AdminRole + " of the server " + server
	}
	return grant
}

// wrote reports whether c wrote one of the revisions whose authors are
// authors.
func (c draftCaller) wrote(authors []drafts.Principal) bool {
	return slices.ContainsFunc(authors, func(p drafts.Principal) bool { return c.isAuthor(p.UserID, p.Via) })
}

// isAuthor reports whether the author with the id userID, who wrote on the
// credential lane via, is c: the same user, or the same admin API token,
// because a token's id never names a user.
func (c draftCaller) isAuthor(userID, via string) bool {
	return userID != "" && userID == c.author.UserID && (via == laneAdminAPI) == (c.author.Via == laneAdminAPI)
}

// publisherRefusal answers the 403 sentence of step 1 of a publish, or
// "": only a person publishes, and only while the user row is
// active and the lock denylist does not block it. requireDrafts has
// already refused the session of a coding harness and a user whose row it
// could not read.
func (c draftCaller) publisherRefusal() string {
	name := c.author.Username
	switch {
	case c.adminAPI():
		return publishRefusalAdminAPI
	case !c.person:
		return publishRefusalAgent
	case c.disabled:
		return fmt.Sprintf("The user %s is disabled, so it cannot publish a draft. Ask an administrator to enable it again.", name)
	case c.locked:
		return fmt.Sprintf("The user %s is locked, so it cannot publish a draft. An administrator lifts the lock with strazactl users unlock %s.", name, name)
	}
	return ""
}

// publishRefusal answers why c may not publish draft d, whose verdict over
// live state w and the item facts of in is v, in the words of the publish
// route: the principal check of step 1, then the standing of every item of
// step 4, or "". GET answers it as may_publish and publish_refusal, which
// are display only, because a publish checks again.
func (c draftCaller) publishRefusal(d drafts.Draft, w drafts.World, in drafts.CheckInput, v drafts.Verdict) string {
	if msg := c.publisherRefusal(); msg != "" {
		return msg
	}
	return c.standingRefusal(d, w, in, v.Needs)
}

// secondPersonRefusal answers why admin.secondPerson refuses c the publish
// of a draft whose revisions are revisions and whose verdict is v, or nil:
// a 409 refusal, or an unread one for a revision list with nothing
// in it, which no draft has. While the setting is on, a verdict with a risk
// is published by no author of a revision that did not take live values
// mechanically, an admin API token counting as the person who minted
// it, and by no sponsor of an agent that wrote one, as the agent's row names
// its sponsor now. A token whose minter cannot be traced refuses every
// publisher. A verdict with no risk publishes by its author. A failed read
// of the tokens or of an agent's row is the error.
func (a *App) secondPersonRefusal(ctx context.Context, c draftCaller, revisions []store.DraftRevisionRow, v drafts.Verdict) (*drafts.Refusal, error) {
	if !a.cfg.Admin.SecondPerson || len(v.Risks) == 0 {
		return nil, nil
	}
	if len(revisions) == 0 {
		return &drafts.Refusal{Kind: drafts.RefusalUnread, Sentence: secondPersonUnreadRefusal}, nil
	}
	conflict := func(s string) *drafts.Refusal { return &drafts.Refusal{Kind: drafts.RefusalConflict, Sentence: s} }
	var tokens map[string]apiTokenMeta
	sponsored := ""
	for _, rev := range revisions {
		au := rev.Author
		switch {
		case rev.Mechanical:
		case c.isAuthor(au.ID, au.Via):
			return conflict(secondPersonAuthorRefusal), nil
		case au.Via == laneAdminAPI:
			if tokens == nil {
				var err error
				if tokens, err = a.apiTokens(ctx); err != nil {
					return nil, fmt.Errorf("the admin API tokens cannot be read: %w", err)
				}
			}
			switch minted, known := mintedBy(tokens, au.ID, c.author.Username); {
			case !known:
				return conflict(fmt.Sprintf(secondPersonUnknownRefusal, au.Name)), nil
			case minted:
				return conflict(fmt.Sprintf(secondPersonMinterRefusal, au.Name)), nil
			}
		case au.Agent && sponsored == "":
			yes, err := a.sponsorsNow(ctx, c, au)
			if err != nil {
				return nil, err
			}
			if yes {
				sponsored = au.Name
			}
		}
	}
	if sponsored != "" {
		return conflict(fmt.Sprintf(secondPersonSponsorRefusal, sponsored)), nil
	}
	return nil, nil
}

// mintedBy reports whether the admin API token with the id id was minted by
// the user named publisher, following a token that another token minted to
// the person at the end, and whether its minter can be traced at all: a
// revoked token, a token minted before its minter was recorded, and a loop
// cannot be.
func mintedBy(tokens map[string]apiTokenMeta, id, publisher string) (minted, known bool) {
	byID := make(map[string]apiTokenMeta, len(tokens))
	byName := make(map[string]apiTokenMeta, len(tokens))
	for _, m := range tokens {
		byID[m.ID], byName[m.Name] = m, m
	}
	seen := map[string]bool{}
	for m, ok := byID[id]; ok && !seen[m.ID]; m, ok = byName[m.CreatedBy] {
		seen[m.ID] = true
		switch _, byToken := byName[m.CreatedBy]; {
		case m.CreatedBy == "":
			return false, false
		case m.CreatedBy == publisher:
			return true, true
		case !byToken:
			return false, true
		}
	}
	return false, false
}

// sponsorsNow reports whether c sponsors the agent that wrote as au, as the
// agent's user row names its sponsor now, or as its revision recorded when
// that row is gone.
func (a *App) sponsorsNow(ctx context.Context, c draftCaller, au store.DraftActor) (bool, error) {
	u, err := a.store.Users().GetByID(ctx, au.ID)
	switch {
	case errors.Is(err, store.ErrNotFound):
		return (au.SponsorID != "" && au.SponsorID == c.author.UserID) || (au.SponsorName != "" && au.SponsorName == c.author.Username), nil
	case err != nil:
		return false, fmt.Errorf("the user record of the agent %s cannot be read: %w", au.Name, err)
	}
	return u.Sponsor != "" && u.Sponsor == c.author.Username, nil
}

// directSecondPersonRefusal answers the 409 sentence that refuses a direct
// admin write whose one-item verdict v holds a risk while
// admin.secondPerson is on, whoever calls it, or "".
func (a *App) directSecondPersonRefusal(v drafts.Verdict) string {
	if a.cfg.Admin.SecondPerson && len(v.Risks) > 0 {
		return secondPersonDirectRefusal
	}
	return ""
}

// reviseRefusal answers the 403 sentence that refuses c a new revision of
// draft id, whose revisions authors wrote, or "". A person revises
// any draft, and an admin API token or an agent only a draft whose every
// revision it wrote itself.
func (c draftCaller) reviseRefusal(id string, authors []drafts.Principal) string {
	if c.person || !slices.ContainsFunc(authors, func(p drafts.Principal) bool { return !c.isAuthor(p.UserID, p.Via) }) {
		return ""
	}
	who := "An agent"
	if c.adminAPI() {
		who = "An admin API token"
	}
	return fmt.Sprintf("%s changes only the drafts it wrote, and draft %s was written by someone else. A person can revise it on the console or with strazactl.", who, id)
}

// discardRefusal answers the 403 sentence that refuses c the discard of
// draft d, or "": its authors, a person holding the root role, and a
// person with standing over every item in it discard a draft. An admin API
// token discards only the drafts it wrote, whatever its scope. The
// standing reads live state with draftWorld, and only for a person who is
// neither an author nor root.
func (a *App) discardRefusal(ctx context.Context, c draftCaller, d drafts.Draft) (string, error) {
	if (c.p.root && !c.adminAPI()) || c.wrote(d.Authors) {
		return "", nil
	}
	refusal := fmt.Sprintf("Discarding draft %s needs being one of its authors, the root role, or standing over every object in it. Ask one of its authors or an administrator.", d.ID)
	if !c.person {
		return refusal, nil
	}
	w, in, err := a.draftWorld(ctx, d)
	if err != nil {
		return "", err
	}
	if c.standingRefusal(d, w, in, nil) != "" {
		return refusal, nil
	}
	return "", nil
}
