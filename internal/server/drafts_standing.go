package server

import (
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/strazahq/straza/internal/drafts"
	"github.com/strazahq/straza/internal/manager"
	"github.com/strazahq/straza/internal/store"
)

// draftStandings is what a caller may change through each family of
// today's direct config routes: the roles routes and the access row
// routes, which also open to a server's admin role, each on its own area,
// and the policy routes, which open to the policy area's write grant
// alone. The implication routes and the removal of a server take the full
// standing of the identity and the apps area.
type draftStandings struct {
	roles, apps drafts.Standing
	// everyServer is true for a person holding apps:write, whom the roles
	// routes let change the roles of every server, one a draft adds
	// included, and nothing that belongs to no server.
	everyServer bool
	policy      bool
}

// standings answers c's standing over live state w as today's route guards
// derive it: root and an area's write grant are full standing on that
// area, a person's server admin roles open the objects of their servers,
// and a person holding apps:write administers every live server's roles.
// An admin API token never takes a server admin's lane.
func (c draftCaller) standings(w drafts.World) draftStandings {
	g := c.p.scope.Grants
	own := make(map[string]bool, len(c.servers))
	for id := range c.servers {
		own[id] = true
	}
	st := draftStandings{
		roles:       drafts.Standing{Full: c.p.root || g["identity:write"], Servers: own},
		apps:        drafts.Standing{Full: c.p.root || g["apps:write"], Servers: own},
		everyServer: !c.adminAPI() && g["apps:write"],
		policy:      c.p.root || g["policy:write"],
	}
	if st.everyServer && !st.roles.Full {
		st.roles.Servers = make(map[string]bool, len(w.Apps))
		for _, app := range w.Apps {
			st.roles.Servers[app.ID] = true
		}
		st.roles.AreaRefusal = c.areaRefusal("identity")
	}
	return st
}

// areaRefusal is the sentence the admin API's guard refuses c a write on
// area with, in areaAllows' words.
func (c draftCaller) areaRefusal(area string) string {
	switch {
	case c.adminAPI():
		return "token lacks scope " + area + ":write"
	case len(c.p.scope.Grants) == 0:
		return areaRefusalNoGrants
	}
	return "session lacks scope " + area + ":write"
}

// standingNo is why a caller lacks the standing an item needs: the refusal
// today's direct route answers, in its words and with its kind, whether it
// refuses the caller the object's whole area, which a publish words with
// the standing the verdict's needs name, and the words a publish answers
// instead of the route's when those name too little.
type standingNo struct {
	ref     *drafts.Refusal
	area    bool
	publish string
}

// areaNo is the guard's refusal of c on area.
func (c draftCaller) areaNo(area string) *standingNo {
	return &standingNo{ref: &drafts.Refusal{Kind: drafts.RefusalForbidden, Sentence: c.areaRefusal(area)}, area: true}
}

// refusedNo is a route's own refusal, of kind k with the sentence s.
func refusedNo(k drafts.RefusalKind, s string) *standingNo {
	return &standingNo{ref: &drafts.Refusal{Kind: k, Sentence: s}}
}

// standingRefusal answers the 403 sentence that refuses c the publish of
// draft d over live state w and the item facts of in, or "" (step 4 of a
// publish). Each item needs the standing that today's direct routes need for
// the same change, and the first item c lacks it for answers: in the words
// of the rule that refuses it, or, when c lacks the object's whole area,
// with the standing needs names for the item.
func (c draftCaller) standingRefusal(d drafts.Draft, w drafts.World, in drafts.CheckInput, needs []drafts.Need) string {
	i, no := c.firstStanding(d, w, in)
	if no == nil {
		return ""
	}
	msg := no.ref.Sentence
	if no.publish != "" {
		msg = no.publish
	}
	if no.area {
		also := ""
		if i > 0 {
			also = "also "
		}
		msg = fmt.Sprintf("this draft %s%s, which needs %s.", also, itemWords(d.Items[i]), needWords(needs, d.Items[i].Object()))
	}
	if !strings.HasSuffix(msg, ".") {
		msg += "."
	}
	return fmt.Sprintf("You cannot publish draft %s: %s", d.ID, msg)
}

// directStandingRefusal answers why c lacks the standing that draft d needs
// over live state w and the item facts of in, in the words and with the
// kind today's direct admin route answers the same change with, which
// refusalStatus maps to its status, or nil. A direct route's one-item draft
// meets exactly the standing its route meets today.
func (c draftCaller) directStandingRefusal(d drafts.Draft, w drafts.World, in drafts.CheckInput) *drafts.Refusal {
	if _, no := c.firstStanding(d, w, in); no != nil {
		return no.ref
	}
	return nil
}

// firstStanding answers the first item of d whose standing c lacks, by
// index, with why, or -1 and nil.
func (c draftCaller) firstStanding(d drafts.Draft, w drafts.World, in drafts.CheckInput) (int, *standingNo) {
	st := c.standings(w)
	view := withDraftServers(w, in)
	for i, it := range d.Items {
		var no *standingNo
		switch it.Kind {
		case drafts.KindApp:
			no = c.appStanding(st.apps, w, in, it)
		case drafts.KindRole:
			no = c.roleStanding(st, view, it)
		default:
			if !st.policy {
				no = c.areaNo("policy")
			}
		}
		if no != nil {
			return i, no
		}
	}
	return -1, nil
}

// withDraftServers is w with each server in's facts name that live state
// lacks, under an id no live server has, so a role of a server the draft
// adds meets the rules of a server that exists, as it will once published.
func withDraftServers(w drafts.World, in drafts.CheckInput) drafts.World {
	view := w
	view.Apps = maps.Clone(w.Apps)
	for name, app := range in.Apps {
		if _, live := w.Apps[name]; !live {
			app.ID = "draft:" + name
			view.Apps[name] = app
		}
	}
	return view
}

// itemWords is what an item does, in a standing refusal.
func itemWords(it drafts.Item) string {
	verb := "changes"
	if it.Op == drafts.OpRemove {
		verb = "removes"
	}
	switch it.Kind {
	case drafts.KindApp:
		return verb + " the server " + it.Name
	case drafts.KindRole:
		return verb + " the role " + it.Name
	}
	return "changes policy sets"
}

// needWords is the standing the verdict's needs name for object, or plain
// words for a caller that holds no verdict.
func needWords(needs []drafts.Need, object string) string {
	for _, n := range needs {
		if n.Object == object {
			return n.Standing
		}
	}
	return "standing you do not hold"
}

// appStanding answers the standing of an App item as the install and
// removal routes answer it today with the apps standing st: an area grant
// installs and removes any server, and a server admin changes the servers
// it administers, neither their runtime nor their agents' own tokens, and
// registers and removes none.
func (c draftCaller) appStanding(st drafts.Standing, w drafts.World, in drafts.CheckInput, it drafts.Item) *standingNo {
	switch {
	case st.Full:
		return nil
	case it.Op == drafts.OpRemove || len(st.Servers) == 0:
		return c.areaNo("apps")
	}
	if ref := drafts.RegisterRefusal(w, st, it.Name); ref != nil {
		return &standingNo{ref: ref}
	}
	live := w.Apps[it.Name]
	prev := store.App{Name: live.Name, Manifest: live.Manifest, RuntimeKind: live.Runtime}
	mf, err := manager.FromJSON(in.Apps[it.Name].Manifest)
	hidden := hiddenWrites(it.Doc, live.Manifest)
	runtime := func(prev store.App, mf manager.Manifest) (string, error) {
		return serverAdminRuntimeRefusal(prev, mf, hidden)
	}
	for _, rule := range []func(store.App, manager.Manifest) (string, error){runtime, serverAdminAgentTokensRefusal} {
		msg := ""
		if err == nil {
			msg, err = rule(prev, mf)
		}
		if err != nil {
			return refusedNo(drafts.RefusalUnread, "the change cannot be compared with the stored manifest of "+it.Name+
				", so it is refused. Try again, and check the strazad log if it keeps failing.")
		}
		if msg != "" {
			return refusedNo(drafts.RefusalForbidden, msg)
		}
	}
	return nil
}

// roleStanding answers the standing of a Role item as the routes that make
// the same change answer it today: the roles routes for its creation,
// description and removal, the access row routes for its row, and the
// implication routes for its edges, in that order. A put that changes none
// of them calls no route and needs nothing. A document that does not read,
// which Check refuses, asks for the identity and the apps area.
func (c draftCaller) roleStanding(st draftStandings, w drafts.World, it drafts.Item) *standingNo {
	live, present := w.Roles[it.Name]
	if it.Op == drafts.OpRemove {
		if !present {
			return nil
		}
		return c.rolesRoute(st.roles, w, live, true)
	}
	doc, err := drafts.ParseRole(it.Doc)
	switch {
	case err != nil && st.roles.Full && st.apps.Full:
		return nil
	case err != nil:
		return c.areaNo("identity")
	case !present:
		return c.createStanding(st, w, it.Name, doc)
	}
	if doc.Spec.Description != live.Description {
		if no := c.rolesRoute(st.roles, w, live, false); no != nil {
			return no
		}
	}
	if accessChanged(w, it.Name, doc) {
		if no := c.accessRoutes(st.apps, w, it.Name, doc); no != nil {
			return no
		}
	}
	if !slices.Equal(slices.Sorted(slices.Values(doc.Spec.Implies)), slices.Sorted(slices.Values(w.Implies[it.Name]))) && !st.roles.Full {
		return c.areaNo("identity")
	}
	return nil
}

// createStanding answers the standing of a role live state lacks: the
// create route, which also takes the row of a role a server owns, then the
// access row route for a global role's row, and the implication routes for
// its edges.
func (c draftCaller) createStanding(st draftStandings, w drafts.World, name string, doc drafts.RoleDoc) *standingNo {
	spec := drafts.RoleSpec{Name: name, Kind: doc.Spec.Kind, Server: doc.Spec.Server}
	owned := spec.Server != ""
	if owned && len(doc.Spec.Bindings) > 0 {
		spec.Tools = doc.Spec.Bindings[0].Tools
	}
	switch {
	case st.roles.Full || (owned && st.everyServer):
		// The rules the create route runs beside the standing are Check's.
	case len(st.roles.Servers) == 0:
		return c.areaNo("identity")
	default:
		if ref := drafts.RoleCreateRefusal(w, st.roles, spec); ref != nil {
			// A global role meets the identity area's refusal, or the words
			// a server admin reads for a role of no server.
			return &standingNo{ref: ref, area: !owned && (ref.Sentence == st.roles.AreaRefusal || ref.Sentence == drafts.OwnedRoleServerErr)}
		}
	}
	if !owned && len(doc.Spec.Bindings) > 0 {
		ro := drafts.Role{Name: name, Kind: doc.Spec.Kind, Plane: drafts.PlaneAccess}
		if doc.Spec.Kind == drafts.RoleKindStraza {
			ro.Kind, ro.Plane = drafts.RoleKindBusiness, drafts.PlaneControl
		}
		view := w
		view.Roles = maps.Clone(w.Roles)
		view.Roles[name] = ro
		if no := c.accessRoutes(st.apps, view, name, doc); no != nil {
			return no
		}
	}
	if len(doc.Spec.Implies) > 0 && !st.roles.Full {
		return c.areaNo("identity")
	}
	return nil
}

// rolesRoute answers the guard of the roles routes over the live role ro
// with the roles standing st, as requireServerAdmin answers it, and for a
// removal the rule that a caller short of the identity area removes no
// role anyone holds.
func (c draftCaller) rolesRoute(st drafts.Standing, w drafts.World, ro drafts.Role, remove bool) *standingNo {
	switch {
	case st.Full:
		return nil
	case len(st.Servers) == 0:
		return c.areaNo("identity")
	}
	app, live := w.Apps[ro.Owner]
	switch {
	case !ro.Owned || !live:
		if st.AreaRefusal != "" {
			return &standingNo{ref: &drafts.Refusal{Kind: drafts.RefusalForbidden, Sentence: st.AreaRefusal}, area: true}
		}
		return &standingNo{ref: &drafts.Refusal{Kind: drafts.RefusalForbidden,
			Sentence: "the role " + ro.Name + " belongs to no server, so a server admin cannot change it. Ask an identity administrator."}, area: true}
	case !st.Servers[app.ID]:
		return refusedNo(drafts.RefusalForbidden, drafts.ServerAdminRefusal(app.AdminRole))
	case !remove:
		return nil
	}
	switch n, counted := w.HolderCounts[ro.Name]; {
	case !counted:
		return refusedNo(drafts.RefusalUnread, "Straza did not count who holds "+ro.Name+
			", so it cannot tell whether the role may be removed. Try again, and read the strazad log if it keeps failing.")
	case n > 0:
		return refusedNo(drafts.RefusalConflict, fmt.Sprintf("the role %s has %s. The identity manager removes them first, then delete it",
			ro.Name, drafts.HoldersPhrase(n)))
	}
	return nil
}

// accessChanged reports whether the Role document doc changes the access
// row of the role name from the one live state holds: another server,
// other tools, a row where there was none, or none where there was one.
func accessChanged(w drafts.World, name string, doc drafts.RoleDoc) bool {
	live, had := w.Access[name]
	if len(doc.Spec.Bindings) == 0 {
		return had
	}
	b := doc.Spec.Bindings[0]
	return !had || live.Server != b.App || !slices.Equal(slices.Sorted(slices.Values(live.Tools)), slices.Sorted(slices.Values(b.Tools)))
}

// accessRoutes answers the access row routes with the apps standing st for
// the role name, whose live row the row of doc replaces: the live row goes
// as the removal route takes it, and the new row comes as the create route
// adds it, each route first finding the row's server.
func (c draftCaller) accessRoutes(st drafts.Standing, w drafts.World, name string, doc drafts.RoleDoc) *standingNo {
	switch {
	case st.Full:
		return nil
	case len(st.Servers) == 0:
		return c.areaNo("apps")
	}
	if old, ok := w.Access[name]; ok {
		if no := serverRoute(st, w, name, old.Server); no != nil {
			return no
		}
		if ref := drafts.AccessRemoveRefusal(w, st, name, old.Server); ref != nil {
			return &standingNo{ref: ref}
		}
	}
	if len(doc.Spec.Bindings) == 0 {
		return nil
	}
	b := doc.Spec.Bindings[0]
	if no := serverRoute(st, w, name, b.App); no != nil {
		return no
	}
	view := w
	view.Access = maps.Clone(w.Access)
	delete(view.Access, name)
	if ref := drafts.AccessCreateRefusal(view, st, name, b.App, b.Tools); ref != nil {
		return &standingNo{ref: ref}
	}
	return nil
}

// serverRoute answers the guard of a route that names the server name for
// the access row of role, for a caller short of the apps area, as
// requireServerAdmin answers it: a server that is not live is unknown, and
// one the caller does not administer names the role it would need. A
// publish names the role and the server where the route says only
// "unknown server".
func serverRoute(st drafts.Standing, w drafts.World, role, name string) *standingNo {
	app, ok := w.Apps[name]
	switch {
	case !ok && name == "":
		no := refusedNo(drafts.RefusalMissing, "unknown server")
		no.publish = "the role " + role + " keeps an access row on a server that was removed, and only the scope apps:write or the role " +
			MCPAdminRole + " changes that row. Ask a holder of " + MCPAdminRole + " to publish this draft."
		return no
	case !ok:
		no := refusedNo(drafts.RefusalMissing, "unknown server")
		no.publish = "the role " + role + " reaches the server " + name + ", which is not registered, so a server admin cannot change its access row. " +
			"Register " + name + " in this draft, or fix the name."
		return no
	case !st.Servers[app.ID]:
		return refusedNo(drafts.RefusalForbidden, drafts.ServerAdminRefusal(app.AdminRole))
	}
	return nil
}
