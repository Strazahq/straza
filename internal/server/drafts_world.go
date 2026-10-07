package server

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"sort"
	"time"

	"github.com/strazahq/straza/internal/config"
	"github.com/strazahq/straza/internal/drafts"
	"github.com/strazahq/straza/internal/identity"
	"github.com/strazahq/straza/internal/manager"
	"github.com/strazahq/straza/internal/policy"
	"github.com/strazahq/straza/internal/store"
)

// draftWorld reads live state for checking d: liveWorld, then worldFor.
// The generation comes first, so a publish that commits during the read
// moves it and sends that publish back. Any failed read fails the
// whole read, because a read that feeds a refusal or a risk fails closed.
func (a *App) draftWorld(ctx context.Context, d drafts.Draft) (drafts.World, drafts.CheckInput, error) {
	w, err := a.liveWorld(ctx)
	if err != nil {
		return drafts.World{}, drafts.CheckInput{}, err
	}
	return a.worldFor(ctx, w, d)
}

// liveWorld reads the live state every check starts from: the config
// generation first, then readWorld, so a direct route can build its one
// item from live state before its draft exists.
func (a *App) liveWorld(ctx context.Context) (drafts.World, error) {
	gen, err := a.store.Drafts().Generation(ctx)
	if err != nil {
		return drafts.World{}, fmt.Errorf("the config generation cannot be read: %w", err)
	}
	w, err := a.readWorld(ctx)
	if err != nil {
		return drafts.World{}, err
	}
	w.Generation = gen
	return w, nil
}

// worldFor completes w, which liveWorld read, for checking d: the live
// fingerprints of d's items and of the objects its removals take with
// them, the last publish of every item that went stale, the facts of every
// App d puts, extracted from the item's manifest by manager.Parse and the
// same extractor readWorld uses for live rows, the credential facts of the
// servers d names and the proposer's user row.
func (a *App) worldFor(ctx context.Context, w drafts.World, d drafts.Draft) (drafts.World, drafts.CheckInput, error) {
	in := drafts.CheckInput{Apps: a.itemFacts(d), Advisories: a.policyAdvisories, Now: time.Now()}
	if err := a.readCredentialFacts(ctx, &w, d); err != nil {
		return drafts.World{}, drafts.CheckInput{}, err
	}
	var err error
	if w.Fingerprints, err = a.liveFingerprints(ctx, w, d); err != nil {
		return drafts.World{}, drafts.CheckInput{}, err
	}
	if in.Changed, err = a.latestChanges(ctx, w, d); err != nil {
		return drafts.World{}, drafts.CheckInput{}, err
	}
	if in.Proposer, err = a.proposerOf(ctx, d); err != nil {
		return drafts.World{}, drafts.CheckInput{}, err
	}
	return w, in, nil
}

// worldReadRefusal is the 503 sentence of a draft route whose read of live
// state failed with err.
func worldReadRefusal(err error) string {
	return fmt.Sprintf("Straza could not read live state to check the draft: %v. Nothing was saved or published. "+
		"Try again, and read the strazad log if it keeps failing.", err)
}

// liveFingerprints answers the live fingerprint of every item of d and of
// every object drafts.Implied names over w, by Item.Object(), with the
// store's fingerprint functions over the rows w read: a server's stored
// manifest, and a role's row, owner, access row and edges. A PolicySet's
// comes from its policy_sets row, read by name here, because w holds only
// the published text. An absent object has no entry, which reads as "".
func (a *App) liveFingerprints(ctx context.Context, w drafts.World, d drafts.Draft) (map[string]drafts.Fingerprint, error) {
	out := map[string]drafts.Fingerprint{}
	seen := map[string]bool{}
	for _, it := range append(slices.Clone(d.Items), drafts.Implied(w, d)...) {
		object := it.Object()
		if seen[object] {
			continue
		}
		seen[object] = true
		var fp string
		switch it.Kind {
		case drafts.KindApp:
			app, ok := w.Apps[it.Name]
			if !ok {
				continue
			}
			var err error
			if fp, err = store.FingerprintApp(app.Manifest); err != nil {
				return nil, fmt.Errorf("the stored manifest of %s cannot be compared: %w", it.Name, err)
			}
		case drafts.KindRole:
			if _, ok := w.Roles[it.Name]; ok {
				fp = store.FingerprintRole(roleConfig(w, it.Name))
			}
		case drafts.KindPolicySet:
			row, err := a.store.Policies().GetByName(ctx, it.Name)
			if errors.Is(err, store.ErrNotFound) {
				continue
			}
			if err != nil {
				return nil, fmt.Errorf("the policy set %s cannot be read: %w", it.Name, err)
			}
			fp = store.FingerprintPolicySet(row)
		}
		if fp != "" {
			out[object] = drafts.Fingerprint(fp)
		}
	}
	return out, nil
}

// roleConfig is the role name as w holds it, in the store's words for its
// fingerprint: the row, the name of the live server that owns it, the
// server and tools of its access row, and the roles it implies. An owner or
// an access row whose server is gone reads as no server.
func roleConfig(w drafts.World, name string) store.RoleConfig {
	ro, acc := w.Roles[name], w.Access[name]
	return store.RoleConfig{
		Role:  store.Role{ID: ro.ID, Name: ro.Name, Description: ro.Description, Kind: ro.Kind, Plane: ro.Plane},
		Owner: ro.Owner, Server: acc.Server, Tools: acc.Tools, Implies: w.Implies[name],
	}
}

// latestChanges answers, by Item.Object(), the newest publish that changed
// the object of every item of d whose base is not its live fingerprint in
// w, so draft.stale names who published what. An object no publish changed
// has no entry.
func (a *App) latestChanges(ctx context.Context, w drafts.World, d drafts.Draft) (map[string]drafts.LastChange, error) {
	out := map[string]drafts.LastChange{}
	for _, it := range d.Items {
		object := it.Object()
		if it.Base == w.Fingerprints[object] {
			continue
		}
		row, err := a.store.Drafts().LatestChange(ctx, store.ObjectRef{Kind: string(it.Kind), Name: it.Name})
		if errors.Is(err, store.ErrNotFound) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("the last publish of %s cannot be read: %w", object, err)
		}
		change := drafts.LastChange{Draft: row.ID, Publisher: row.DecidedBy.Name}
		if row.DecidedAt != nil {
			change.At = *row.DecidedAt
		}
		out[object] = change
	}
	return out, nil
}

// proposerOf reads the proposer of d, the author of its first revision, as
// its user row gives it, so a set applies to the proposer as the engine
// matches it, by type, agency mode and swarm, even when it holds no role.
// An admin API token, the apps directory and the upgrade are no user
// and read as no one, so a name they share with a user never lends them
// that user's roles. A user whose row is gone keeps what its revision
// recorded.
func (a *App) proposerOf(ctx context.Context, d drafts.Draft) (drafts.Holder, error) {
	if len(d.Authors) == 0 {
		return drafts.Holder{}, nil
	}
	p := d.Authors[0]
	if p.Via != laneLogin && p.Via != laneSession {
		return drafts.Holder{}, nil
	}
	u, err := a.store.Users().GetByID(ctx, p.UserID)
	if errors.Is(err, store.ErrNotFound) {
		h := drafts.Holder{Username: p.Username, Agent: p.Agent}
		if p.SponsorID != "" {
			h.Sponsor = p.SponsorName
		}
		return h, nil
	}
	if err != nil {
		return drafts.Holder{}, fmt.Errorf("the user record of %s cannot be read: %w", p.Username, err)
	}
	sponsor, ok, err := accountableSponsor(u, func(name string) (store.User, error) { return a.store.Users().GetByUsername(ctx, name) })
	if err != nil {
		return drafts.Holder{}, fmt.Errorf("the sponsor of %s cannot be read: %w", p.Username, err)
	}
	if !ok {
		sponsor = store.User{}
	}
	return holderOf(u, sponsor.Username, 0), nil
}

// accountableSponsor answers the sponsor that u's row names when that
// sponsor answers for u, as the approval lane resolves the sponsor decider:
// a user of that name who is active, a person, and not u. lookup reads a
// user by name, and its store.ErrNotFound means no such user.
func accountableSponsor(u store.User, lookup func(string) (store.User, error)) (store.User, bool, error) {
	if u.Sponsor == "" {
		return store.User{}, false, nil
	}
	s, err := lookup(u.Sponsor)
	if errors.Is(err, store.ErrNotFound) {
		return store.User{}, false, nil
	}
	if err != nil {
		return store.User{}, false, err
	}
	return s, s.Status == store.UserActive && personUser(s) && s.ID != u.ID, nil
}

// holderOf is u as a holder of a role: its identity typology, whether it
// is an agent, the username of the sponsor who answers for it, and the
// number of approver devices it enrolled.
func holderOf(u store.User, sponsor string, devices int) drafts.Holder {
	return drafts.Holder{Username: u.Username, Agent: !personUser(u), UserType: u.UserType, AgencyMode: u.AgencyMode,
		SwarmID: u.SwarmID, Sponsor: sponsor, Devices: devices}
}

// readWorld reads live state into one drafts.World: every live server with
// its manifest facts and what the manager knows of it, every role with its
// knowledge packs, the implication edges, the access rows, the direct
// holders in force, the holder count of every role, the OAuth providers of
// the config, the sets of the active policy snapshot with the text each
// was published with, the local tool default, whether a push lane or Slack
// reaches approvers, and whether this host can start containers. The credential
// facts of the servers are left to draftWorld, which reads them for the
// servers a draft names. Any failed read fails the whole read.
func (a *App) readWorld(ctx context.Context) (drafts.World, error) {
	w, err := a.readRoleTable(ctx)
	if err != nil {
		return drafts.World{}, err
	}
	for name, row := range w.Apps {
		app, err := withManifest(row)
		if err != nil {
			return drafts.World{}, fmt.Errorf("the stored manifest of %s cannot be read: %w", name, err)
		}
		if v, ok := a.manager.View(name); ok {
			app.Status, app.Offered, app.Detail, app.ReadOnly = v.Status, v.Offered, v.Detail, readOnlyTools(v.Tools)
		}
		app.Paused = a.manager.IsPaused(name)
		app.File = a.filePath(name)
		w.Apps[name] = app
	}
	for _, read := range []func(context.Context, *drafts.World) error{
		a.readImplications, a.readAccessRows, a.readHolders, a.readHolderCounts, a.readPolicies, a.readPacks,
	} {
		if err := read(ctx, &w); err != nil {
			return drafts.World{}, err
		}
	}
	w.Providers = make(map[string]drafts.Provider, len(a.cfg.OAuth.Providers))
	for name, p := range a.cfg.OAuth.Providers {
		w.Providers[name] = drafts.Provider{Name: name, ClientCredentials: p.ClientCredentials != nil}
	}
	w.LocalToolDefault = a.cfg.Governance.LocalToolDefault
	w.Push = pushLaneConfigured(a.cfg.Approval.Push)
	w.Slack = a.cfg.Approval.Channels.Slack.Enabled
	w.DockerOnPath = manager.DockerOnPath()
	w.RefuseCommand = a.cfg.Profile == config.ProfileEnterprise
	return w, nil
}

// readRoleTable reads every role and every live server into a World, the
// part most rules read: each role with the name of the server that owns it,
// and each server with the name of its admin role. The servers carry no
// manifest facts and nothing the manager knows, so the read also serves at
// boot, before the manager exists.
func (a *App) readRoleTable(ctx context.Context) (drafts.World, error) {
	roles, err := a.store.Roles().List(ctx)
	if err != nil {
		return drafts.World{}, err
	}
	apps, err := a.store.Apps().List(ctx)
	if err != nil {
		return drafts.World{}, err
	}
	roleNames := make(map[string]string, len(roles))
	for _, ro := range roles {
		roleNames[ro.ID] = ro.Name
	}
	w := drafts.World{Apps: make(map[string]drafts.App, len(apps)), Roles: make(map[string]drafts.Role, len(roles))}
	appNames := make(map[string]string, len(apps))
	for _, row := range apps {
		appNames[row.ID] = row.Name
		w.Apps[row.Name] = worldApp(row, roleNames[row.AdminRoleID])
	}
	for _, ro := range roles {
		w.Roles[ro.Name] = worldRole(ro, appNames[ro.OwnerAppID])
	}
	return w, nil
}

// readRoles reads every role, for the rules that read roles alone. Owned is
// set and Owner stays empty, because naming a role's server takes the
// server list, which those rules never read.
func (a *App) readRoles(ctx context.Context) (map[string]drafts.Role, error) {
	roles, err := a.store.Roles().List(ctx)
	if err != nil {
		return nil, err
	}
	out := make(map[string]drafts.Role, len(roles))
	for _, ro := range roles {
		out[ro.Name] = worldRole(ro, "")
	}
	return out, nil
}

// worldApp is a server row as a World holds it, with the name of its admin
// role and without manifest facts.
func worldApp(row store.App, adminRole string) drafts.App {
	return drafts.App{ID: row.ID, Name: row.Name, Manifest: row.Manifest, Source: row.Source, Status: row.Status,
		AdminRole: adminRole, RolePrefix: store.OwnedRolePrefix(row.Name)}
}

// worldRole is a role row as a World holds it, owner naming the live server
// that owns it.
func worldRole(ro store.Role, owner string) drafts.Role {
	return drafts.Role{ID: ro.ID, Name: ro.Name, Kind: ro.Kind, Plane: ro.Plane,
		Owner: owner, Owned: ro.OwnerAppID != "", Description: ro.Description}
}

// serverWorld is a World that holds the server row alone, for a rule about
// that one server.
func serverWorld(row store.App) drafts.World {
	return drafts.World{Apps: map[string]drafts.App{row.Name: worldApp(row, "")}}
}

// withManifest fills the facts of app that its stored manifest says, and
// fails when that manifest cannot be decoded.
func withManifest(app drafts.App) (drafts.App, error) {
	mf, err := manager.FromJSON(app.Manifest)
	if err != nil {
		return app, err
	}
	return manifestFacts(app, mf), nil
}

// idNames maps the id of every role and every server in w to its name.
func idNames(w drafts.World) (roles, apps map[string]string) {
	roles = make(map[string]string, len(w.Roles))
	for name, ro := range w.Roles {
		roles[ro.ID] = name
	}
	apps = make(map[string]string, len(w.Apps))
	for name, app := range w.Apps {
		apps[app.ID] = name
	}
	return roles, apps
}

// roleNameOf names the role in w with the id, and reports whether a role has
// it.
func roleNameOf(w drafts.World, id string) (string, bool) {
	for name, ro := range w.Roles {
		if ro.ID == id {
			return name, true
		}
	}
	return "", false
}

// readAccessRows reads every access row into w.Access, keyed by the name of
// its role, which w.Roles must hold. A row whose server is gone keeps an
// empty server.
func (a *App) readAccessRows(ctx context.Context, w *drafts.World) error {
	rows, err := a.store.ToolBindings().List(ctx)
	if err != nil {
		return err
	}
	roles, apps := idNames(*w)
	w.Access = make(map[string]drafts.Access, len(rows))
	for _, b := range rows {
		role, ok := roles[b.RoleID]
		if !ok {
			continue
		}
		var tools []string
		_ = json.Unmarshal([]byte(b.ToolMatcher), &tools)
		w.Access[role] = drafts.Access{ID: b.ID, Server: apps[b.AppID], Tools: tools}
	}
	return nil
}

// readImplications reads every implication edge into w.Implies by role
// name, which w.Roles must hold.
func (a *App) readImplications(ctx context.Context, w *drafts.World) error {
	imps, err := a.store.Roles().ListImplications(ctx)
	if err != nil {
		return err
	}
	roles, _ := idNames(*w)
	w.Implies = map[string][]string{}
	for _, imp := range imps {
		from, ok := roles[imp.RoleID]
		to, known := roles[imp.ImpliesRoleID]
		if ok && known {
			w.Implies[from] = append(w.Implies[from], to)
		}
	}
	return nil
}

// readHolderCounts counts the holders of every role in w.Roles the way the
// store's HolderCount does, in one read: every assignment row, validity
// windows included, of the role or of a role whose implication closure
// reaches it, each subject once. w.Implies must be read first.
func (a *App) readHolderCounts(ctx context.Context, w *drafts.World) error {
	rows, err := a.store.Roles().ListAllAssignments(ctx)
	if err != nil {
		return err
	}
	roles, _ := idNames(*w)
	direct := map[string]map[string]bool{}
	for _, asg := range rows {
		if name, ok := roles[asg.RoleID]; ok {
			if direct[name] == nil {
				direct[name] = map[string]bool{}
			}
			direct[name][asg.SubjectID] = true
		}
	}
	held := map[string]map[string]bool{}
	for from, subjects := range direct {
		for role := range identity.Closure(map[string]bool{from: true}, w.Implies) {
			if held[role] == nil {
				held[role] = map[string]bool{}
			}
			for s := range subjects {
				held[role][s] = true
			}
		}
	}
	w.HolderCounts = make(map[string]int, len(w.Roles))
	for name := range w.Roles {
		w.HolderCounts[name] = len(held[name])
	}
	return nil
}

// readHolders reads the users who hold each role directly, from the
// assignments in force now, into w.Holders, each user once per role and
// sorted by username. A holder is an agent when it is not a person, as
// personUser reads both non-person signals, and carries its identity
// typology, the sponsor who answers for it, and its approver devices.
func (a *App) readHolders(ctx context.Context, w *drafts.World) error {
	pairs, err := a.store.Roles().AssignmentPairsInForce(ctx, time.Now())
	if err != nil {
		return err
	}
	users, err := a.store.Users().List(ctx)
	if err != nil {
		return err
	}
	devices, err := a.store.Approvers().ListDevices(ctx, "")
	if err != nil {
		return err
	}
	byID := make(map[string]store.User, len(users))
	byName := make(map[string]store.User, len(users))
	for _, u := range users {
		byID[u.ID], byName[u.Username] = u, u
	}
	enrolled := map[string]int{}
	for _, d := range devices {
		enrolled[d.UserID]++
	}
	lookup := func(name string) (store.User, error) {
		if u, ok := byName[name]; ok {
			return u, nil
		}
		return store.User{}, store.ErrNotFound
	}
	roles, _ := idNames(*w)
	seen := map[[2]string]bool{}
	w.Holders = map[string][]drafts.Holder{}
	for _, p := range pairs {
		role, ok := roles[p.RoleID]
		u, known := byID[p.SubjectID]
		if !ok || !known || seen[[2]string{role, u.ID}] {
			continue
		}
		seen[[2]string{role, u.ID}] = true
		// The lookup reads the users already in hand and never fails.
		sponsor, answers, _ := accountableSponsor(u, lookup)
		if !answers {
			sponsor = store.User{}
		}
		w.Holders[role] = append(w.Holders[role], holderOf(u, sponsor.Username, enrolled[u.ID]))
	}
	for _, hs := range w.Holders {
		sort.Slice(hs, func(i, j int) bool { return hs[i].Username < hs[j].Username })
	}
	return nil
}

// readPolicies reads the sets the active policy snapshot carries, with the
// text each was published with, into w.Policies, and the snapshot's id into
// w.SnapshotID. It reads the store's active snapshot, as a publish does, and
// never the stored source of a set, which can hold a saved edit that is not
// live. With no active snapshot no set is live. A snapshot that cannot be
// opened with the signing keys, or a set in it that does not parse, fails
// the read.
func (a *App) readPolicies(ctx context.Context, w *drafts.World) error {
	w.Policies = map[string]drafts.Policy{}
	act, err := a.store.Snapshots().GetActive(ctx)
	if errors.Is(err, store.ErrNotFound) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read the active policy snapshot: %w", err)
	}
	_, snap, err := policy.OpenSnapshot(act.Blob, act.ID, func(kid string) (ed25519.PublicKey, bool) {
		return a.snapKeys.Public(ctx, kid)
	})
	if err != nil {
		return fmt.Errorf("open the active policy snapshot %s: %w", act.ID, err)
	}
	for _, raw := range snap.Documents {
		doc, err := policy.Parse(raw)
		if err != nil {
			return fmt.Errorf("a set in the active policy snapshot %s does not parse: %w", act.ID, err)
		}
		w.Policies[doc.Metadata.Name] = drafts.Policy{Name: doc.Metadata.Name, Text: string(raw)}
	}
	w.SnapshotID = act.ID
	return nil
}

// draftStanding is the request's standing in the words of the rules.
func draftStanding(st adminStanding) drafts.Standing {
	return drafts.Standing{Full: st.Full, Servers: st.Apps, AreaRefusal: st.AreaRefusal}
}

// refuse answers a rule's refusal with the HTTP status of its kind.
func refuse(w http.ResponseWriter, ref *drafts.Refusal) {
	apiError(w, refusalStatus(ref.Kind), ref.Sentence)
}

// refusalStatus is the HTTP status the direct routes answer a refusal kind
// with, 400 for a kind it does not know. A route reads what an unread
// refusal asks for, so answering one means the route failed to, which is a
// 500.
func refusalStatus(k drafts.RefusalKind) int {
	switch k {
	case drafts.RefusalMissing:
		return http.StatusNotFound
	case drafts.RefusalForbidden:
		return http.StatusForbidden
	case drafts.RefusalConflict, drafts.RefusalExists:
		return http.StatusConflict
	case drafts.RefusalUnread:
		return http.StatusInternalServerError
	}
	return http.StatusBadRequest
}
