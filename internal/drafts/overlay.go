package drafts

import (
	"slices"
	"sort"
)

// statusPending is the status a server the draft adds reads until it runs.
const statusPending = "pending"

// Overlay answers the World as it would be after publishing d. apps holds
// the facts of every App d puts, keyed by name. Role documents are read
// with ParseRole and PolicySet texts are kept as they are. HolderCounts
// keep describing live state, and a new role has none. The puts and offs
// apply in item order, and the removals after them, so a removal takes
// with it whatever it owns in the state the draft leaves. w is not changed.
//
// An App keeps what live state knows of the server and its manifest
// cannot say: its id, status, health detail, pause, file, admin role, role
// prefix and credential rows. It keeps the live tool list, and the tools
// the live list marks read-only, while its runtime kind, address, command,
// arguments and image stay the same, and otherwise takes the list apps
// gives, which Check fills from what a Contact read, or none.
func (w World) Overlay(d Draft, apps map[string]App) World {
	after := w
	after.Apps = cloneMap(w.Apps)
	after.Roles = cloneMap(w.Roles)
	after.Implies = cloneMap(w.Implies)
	after.Access = cloneMap(w.Access)
	after.Policies = cloneMap(w.Policies)
	after.Holders = cloneMap(w.Holders)
	after.roleDocs = map[string]RoleDoc{}
	var removals []Item
	for _, it := range d.Items {
		switch {
		case it.Op == OpRemove:
			removals = append(removals, it)
		case it.Kind == KindApp && it.Op == OpPut:
			after.Apps[it.Name] = overlayApp(w, it.Name, apps[it.Name])
		case it.Kind == KindRole && it.Op == OpPut:
			after.putRole(w, it)
		case it.Kind == KindPolicySet && it.Op == OpPut:
			after.Policies[it.Name] = Policy{Name: it.Name, Text: it.Doc}
		case it.Kind == KindPolicySet && it.Op == OpOff:
			delete(after.Policies, it.Name)
		}
	}
	for _, it := range removals {
		switch it.Kind {
		case KindApp:
			after.removeApp(w, it.Name)
		case KindRole:
			after.removeRole(it.Name)
		case KindPolicySet:
			delete(after.Policies, it.Name)
		}
	}
	return after
}

// overlayApp is the App the server name will be after a put whose facts are
// next.
func overlayApp(w World, name string, next App) App {
	next.Name = name
	live, ok := w.Apps[name]
	if !ok {
		next.Status = statusPending
		return next
	}
	next.ID, next.Status, next.Detail, next.Paused, next.File = live.ID, live.Status, live.Detail, live.Paused, live.File
	next.Source, next.AdminRole, next.RolePrefix = live.Source, live.AdminRole, live.RolePrefix
	next.SharedSecret, next.RoleSecrets, next.UserCredentials = live.SharedSecret, live.RoleSecrets, live.UserCredentials
	if sameRuntime(live, next) {
		next.Offered, next.ReadOnly = live.Offered, live.ReadOnly
	}
	return next
}

// sameRuntime reports whether a and b run the same server: the same runtime
// kind, address, command, arguments and image, so the tools one offers are
// the tools the other offers.
func sameRuntime(a, b App) bool {
	return a.Runtime == b.Runtime && a.URL == b.URL && a.Exec == b.Exec && slices.Equal(a.Args, b.Args) && a.Image == b.Image
}

// putRole sets the role of item it from its document. A document that does
// not read leaves the role as it was, because Check refuses it anyway.
func (after *World) putRole(w World, it Item) {
	doc, err := ParseRole(it.Doc)
	if err != nil {
		return
	}
	after.roleDocs[it.Name] = doc
	live := w.Roles[it.Name]
	ro := Role{ID: live.ID, Name: it.Name, Kind: doc.Spec.Kind, Plane: PlaneAccess, Owner: doc.Spec.Server,
		Owned: doc.Spec.Server != "", Description: doc.Spec.Description, Packs: live.Packs}
	if doc.Spec.Kind == RoleKindStraza {
		ro.Kind, ro.Plane = RoleKindBusiness, PlaneControl
	}
	after.Roles[it.Name] = ro
	delete(after.Access, it.Name)
	if len(doc.Spec.Bindings) > 0 {
		b := doc.Spec.Bindings[0]
		acc := Access{Server: b.App, Tools: slices.Clone(b.Tools)}
		if row, ok := w.Access[it.Name]; ok && row.Server == b.App {
			acc.ID = row.ID
		}
		after.Access[it.Name] = acc
	}
	delete(after.Implies, it.Name)
	if len(doc.Spec.Implies) > 0 {
		after.Implies[it.Name] = sortedCopy(doc.Spec.Implies)
	}
}

// removeApp takes the server name out, with its access rows, the roles it
// owns and its admin role.
func (after *World) removeApp(w World, name string) {
	delete(after.Apps, name)
	for role, acc := range after.Access {
		if acc.Server == name {
			delete(after.Access, role)
		}
	}
	for role, ro := range after.Roles {
		if ro.Owned && ro.Owner == name {
			after.removeRole(role)
		}
	}
	if admin := w.Apps[name].AdminRole; admin != "" {
		after.removeRole(admin)
	}
}

// removeRole takes the role name out, with its access row, its edges, its
// holders and every edge that implies it.
func (after *World) removeRole(name string) {
	delete(after.Roles, name)
	delete(after.Access, name)
	delete(after.Implies, name)
	delete(after.Holders, name)
	for role, list := range after.Implies {
		if slices.Contains(list, name) {
			after.Implies[role] = slices.DeleteFunc(slices.Clone(list), func(s string) bool { return s == name })
		}
	}
}

// Implied answers the objects that d's removals change without naming
// them: every role that loses its access row with a removed server, every
// role the removed server owns, and every role that loses an implication
// with a removed role. Each comes as the Item that describes its change,
// Implied items being put or remove, never off. A put carries the role's
// whole canonical document after the change. A removed server's admin role
// is part of the server's own item and comes as no item, though a role
// that implies it loses that edge. Roles come sorted by name.
func Implied(w World, d Draft) []Item {
	named := map[string]bool{}
	for _, it := range d.Items {
		named[it.Object()] = true
	}
	servers, gone, owned := map[string]bool{}, map[string]bool{}, map[string]bool{}
	for _, it := range d.Items {
		if it.Op != OpRemove {
			continue
		}
		switch it.Kind {
		case KindApp:
			app, ok := w.Apps[it.Name]
			if !ok {
				continue
			}
			servers[it.Name] = true
			if app.AdminRole != "" {
				gone[app.AdminRole] = true
			}
			for name, ro := range w.Roles {
				if ro.Owned && ro.Owner == it.Name {
					gone[name], owned[name] = true, true
				}
			}
		case KindRole:
			if _, ok := w.Roles[it.Name]; ok {
				gone[it.Name] = true
			}
		}
	}
	if len(gone) == 0 && len(servers) == 0 {
		return nil
	}
	names := make([]string, 0, len(w.Roles))
	for name := range w.Roles {
		names = append(names, name)
	}
	sort.Strings(names)
	var out []Item
	for _, name := range names {
		switch {
		case named[string(KindRole)+"/"+name]:
		case owned[name]:
			out = append(out, Item{Kind: KindRole, Name: name, Op: OpRemove})
		case gone[name]:
		default:
			if doc, changed := impliedDoc(w, name, servers, gone); changed {
				out = append(out, Item{Kind: KindRole, Name: name, Op: OpPut, Doc: roleText(doc)})
			}
		}
	}
	return out
}

// impliedDoc is the document of the role name once the servers and the
// gone roles are removed, and whether it differs from live.
func impliedDoc(w World, name string, servers, gone map[string]bool) (RoleDoc, bool) {
	doc, _ := RoleDocOf(w, name)
	changed := false
	if acc, ok := w.Access[name]; ok && servers[acc.Server] {
		doc.Spec.Bindings, changed = nil, true
	}
	kept := slices.DeleteFunc(slices.Clone(doc.Spec.Implies), func(s string) bool { return gone[s] })
	if len(kept) != len(doc.Spec.Implies) {
		doc.Spec.Implies, changed = kept, true
	}
	return doc, changed
}

// cloneMap answers a shallow copy of m, empty and writable when m is nil.
func cloneMap[K comparable, V any](m map[K]V) map[K]V {
	out := make(map[K]V, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}
