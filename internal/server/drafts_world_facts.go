package server

import (
	"context"
	"fmt"
	"slices"
	"sort"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/strazahq/straza/internal/config"
	"github.com/strazahq/straza/internal/drafts"
	"github.com/strazahq/straza/internal/manager"
	"github.com/strazahq/straza/internal/store"
)

// manifestFacts fills the facts of app that mf says: the credential kind
// and agents value, the OAuth provider with the scopes a person's sign-in
// asks for and where a secret is injected, the runtime with the names of its
// environment entries and never their values, the exposure globs, and the
// address the verbatim server block's registry record gives. It is the one
// reading of a manifest for live servers and draft items alike.
func manifestFacts(app drafts.App, mf manager.Manifest) drafts.App {
	app.Credential, app.Agents = mf.CredentialKind(), mf.AgentsSource()
	app.Provider, app.InjectAs, app.Scopes = "", "", nil
	if c := mf.Straza.Credential; c != nil {
		if c.OAuth != nil {
			app.Provider, app.Scopes = c.OAuth.Provider, slices.Clone(c.OAuth.Scopes)
		}
		if c.Inject != nil {
			app.InjectAs = c.Inject.As
		}
	}
	rt := mf.Straza.Runtime
	app.Runtime = rt.Kind
	app.URL, app.Auth, app.Exec, app.Workdir, app.Image, app.Sandbox = "", "", "", "", "", ""
	app.Args, app.EnvNames = nil, nil
	switch {
	case rt.Remote != nil:
		app.URL, app.Auth = rt.Remote.URL, rt.Remote.Auth
	case rt.Command != nil:
		app.Exec, app.Workdir = rt.Command.Exec, rt.Command.Workdir
		app.Args, app.EnvNames = slices.Clone(rt.Command.Args), envNames(rt.Command.Env)
	case rt.OCI != nil:
		app.Image, app.Sandbox, app.EnvNames = rt.OCI.Image, rt.OCI.Sandbox, envNames(rt.OCI.Env)
	}
	app.Exposure = slices.Clone(mf.ExposedTools())
	app.RegistryURL = registryURL(mf.Server)
	return app
}

// envNames answers the sorted names of env, nil when there are none.
func envNames(env []manager.EnvVar) []string {
	if len(env) == 0 {
		return nil
	}
	out := make([]string, len(env))
	for i, e := range env {
		out[i] = e.Name
	}
	sort.Strings(out)
	return out
}

// registryURL answers the address of the first streamable-http remote that
// a verbatim registry server block lists, the one the registry import
// picks, or "" when it lists none.
func registryURL(server map[string]any) string {
	remotes, _ := server["remotes"].([]any)
	for _, r := range remotes {
		remote, _ := r.(map[string]any)
		if addr, _ := remote["url"].(string); remote["type"] == "streamable-http" && addr != "" {
			return addr
		}
	}
	return ""
}

// readOnlyTools answers, sorted, the names of the tools whose upstream
// annotations say readOnlyHint.
func readOnlyTools(tools []*mcp.Tool) []string {
	var out []string
	for _, t := range tools {
		if t != nil && t.Annotations != nil && t.Annotations.ReadOnlyHint {
			out = append(out, t.Name)
		}
	}
	sort.Strings(out)
	return out
}

// itemFacts answers the facts of every App d puts, by name, read from the
// item's document with manager.Parse and manifestFacts, with its manifest
// as canonical JSON and the admin role name and role prefix the store
// gives a new server. An item whose manifest does not parse has no facts,
// so Check refuses it, and the reason goes to the log.
func (a *App) itemFacts(d drafts.Draft) map[string]drafts.App {
	out := map[string]drafts.App{}
	for _, it := range d.Items {
		if it.Kind != drafts.KindApp || it.Op != drafts.OpPut {
			continue
		}
		if app, ok := a.appFacts(d.ID, it.Name, it.Doc); ok {
			out[it.Name] = app
		}
	}
	return out
}

// appFacts reads the facts of the App put named name of the draft id from
// its document doc, and reports false, with the reason in the log, for a
// manifest that does not parse.
func (a *App) appFacts(id, name, doc string) (drafts.App, bool) {
	mf, err := manager.Parse([]byte(doc))
	var manifest string
	if err == nil {
		manifest, err = mf.JSON()
	}
	if err != nil {
		a.log.Warn("draft check: the manifest of an App item cannot be read", "draft", id, "app", name, "err", err)
		return drafts.App{}, false
	}
	return manifestFacts(drafts.App{Name: name, Manifest: manifest, AdminRole: store.AppAdminRoleName(name), RolePrefix: store.OwnedRolePrefix(name)}, mf), true
}

// keptFacts reads again, into apps, the facts of every App put of d whose
// document sends back a mask that stands for a value of the server's
// stored manifest as w holds it (keptMasks), from the document restored,
// so the check reads the live value where the mask stands, as the publish
// writes it. An item whose masks do not all fit keeps the facts of the
// document as sent, and the waiver refuses it.
func (a *App) keptFacts(w drafts.World, d drafts.Draft, apps map[string]drafts.App) {
	for _, it := range d.Items {
		live, ok := w.Apps[it.Name]
		if it.Kind != drafts.KindApp || it.Op != drafts.OpPut || !ok {
			continue
		}
		restored, at := keptMasks(it.Doc, live.Manifest)
		if restored == "" || at == "" {
			continue
		}
		if app, ok := a.appFacts(d.ID, it.Name, restored); ok {
			apps[it.Name] = app
		}
	}
}

// readCredentialFacts fills, in w, the credential facts of every live
// server d names as an App item or as the server of a Role put's access
// row, the servers whose readiness a check reports: whether the server's
// own secret is stored, which roles hold a secret of their own, and how
// many users hold a credential of their own. It reads the rows and never a
// value.
func (a *App) readCredentialFacts(ctx context.Context, w *drafts.World, d drafts.Draft) error {
	names := map[string]bool{}
	for _, it := range d.Items {
		switch {
		case it.Kind == drafts.KindApp:
			names[it.Name] = true
		case it.Kind == drafts.KindRole && it.Op == drafts.OpPut:
			if doc, err := drafts.ParseRole(it.Doc); err == nil {
				for _, b := range doc.Spec.Bindings {
					names[b.App] = true
				}
			}
		}
	}
	roles, _ := idNames(*w)
	for name := range names {
		app, ok := w.Apps[name]
		if !ok {
			continue
		}
		rows, err := a.store.Credentials().ListByApp(ctx, app.ID)
		if err != nil {
			return fmt.Errorf("the credential rows of %s cannot be read: %w", name, err)
		}
		users := map[string]bool{}
		app.SharedSecret, app.RoleSecrets = false, nil
		for _, c := range rows {
			switch c.Scope {
			case store.CredScopeApp:
				app.SharedSecret = true
			case store.CredScopeRole:
				if role, ok := roles[c.OwnerID]; ok {
					app.RoleSecrets = append(app.RoleSecrets, role)
				}
			case store.CredScopeUser:
				users[c.OwnerID] = true
			}
		}
		sort.Strings(app.RoleSecrets)
		app.UserCredentials = len(users)
		w.Apps[name] = app
	}
	return nil
}

// readPacks reads, sorted, the names of the knowledge packs bound to each
// role into w.Roles, which must hold the roles.
func (a *App) readPacks(ctx context.Context, w *drafts.World) error {
	bindings, err := a.store.Packs().ListBindings(ctx)
	if err != nil || len(bindings) == 0 {
		return err
	}
	packs, err := a.store.Packs().List(ctx)
	if err != nil {
		return err
	}
	names := make(map[string]string, len(packs))
	for _, p := range packs {
		names[p.ID] = p.Name
	}
	bound := map[string][]string{}
	for _, b := range bindings {
		if name, ok := names[b.PackID]; ok {
			bound[b.RoleName] = append(bound[b.RoleName], name)
		}
	}
	for role, list := range bound {
		if ro, ok := w.Roles[role]; ok {
			sort.Strings(list)
			ro.Packs = list
			w.Roles[role] = ro
		}
	}
	return nil
}

// pushLaneConfigured reports whether the approval service builds a push
// lane from p, by the service's own test: FCM on, an allowlist of push
// hosts, a WebPush key, an APNs key, or the relay on.
func pushLaneConfigured(p config.ApprovalPush) bool {
	return p.FCM.Enabled || len(p.AllowedPushHosts) > 0 || p.WebPush.Enabled() || p.APNS.Enabled() || p.Relay.Enabled
}
