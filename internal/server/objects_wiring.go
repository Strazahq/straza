package server

import (
	"context"
	"log/slog"
	"slices"
	"strings"

	"github.com/strazahq/straza/internal/identity"
	"github.com/strazahq/straza/internal/manager"
	"github.com/strazahq/straza/internal/objects"
	"github.com/strazahq/straza/internal/store"
)

// buildObjects builds this replica's object lists over the store, the
// manager and the broker, and loads them into memory before the replica
// serves. A listing's record and its rename events are actorless, because
// the background loop acts for no one.
func (a *App) buildObjects(ctx context.Context, log *slog.Logger) error {
	a.objects = objects.New(objects.Options{
		Store: a.store, Caller: a.manager, Secrets: a.broker, Owner: a.instance, Log: log,
		Off: !a.cfg.Apps.Objects.Enabled,
		Record: func(ctx context.Context, data map[string]any) {
			a.emitEventCtx(noActor(ctx), "straza.audit.admin", data)
		},
		OnRenamed: func(ctx context.Context, appID string, ids []string) {
			a.renamedObjects(noActor(ctx), appID, ids)
		},
	})
	return a.objects.Load(ctx)
}

// renamedObjects names in identity events, in name order, each role whose
// covers line on the server with the id appID names one of ids, the
// objects a listing renamed, and then each other role whose implication
// closure reaches one of them. A rename changes what the SCIM group of
// each such role renders under covers with no access row record, so an
// identity event is what makes midPoint's LiveSync read the group again.
// The listing loop's context never cancels, so the reads run under one
// afterCommit bound and a stuck store cannot hold shutdown. A failed read
// logs one Warn line and names no role, and the identity manager reads the
// new names at its next full reconciliation.
func (a *App) renamedObjects(ctx context.Context, appID string, ids []string) {
	rctx, cancel := afterCommit(ctx)
	defer cancel()
	warn := func(err error) {
		a.log.Warn("object lists: the roles that name a renamed object did not reach the change feed, because a read failed. Run a full reconciliation in the identity manager to read the new names now, or wait for its next one",
			"app", appID, "err", err)
	}
	bindings, err := a.store.ToolBindings().List(rctx)
	if err != nil {
		warn(err)
		return
	}
	seed := map[string]bool{}
	for _, b := range bindings {
		if b.AppID != appID || b.Scope == nil {
			continue
		}
		for _, o := range b.Scope.Objects {
			if slices.Contains(ids, o.ID) {
				seed[b.RoleID] = true
			}
		}
	}
	if len(seed) == 0 {
		return
	}
	roles, err := a.store.Roles().List(rctx)
	if err != nil {
		warn(err)
		return
	}
	imps, err := a.store.Roles().ListImplications(rctx)
	if err != nil {
		warn(err)
		return
	}
	impliedBy := map[string][]string{}
	for _, imp := range imps {
		impliedBy[imp.ImpliesRoleID] = append(impliedBy[imp.ImpliesRoleID], imp.RoleID)
	}
	closure := identity.Closure(seed, impliedBy)
	slices.SortFunc(roles, func(x, y store.Role) int { return strings.Compare(x.Name, y.Name) })
	for _, direct := range []bool{true, false} {
		for _, r := range roles {
			if closure[r.ID] && seed[r.ID] == direct {
				a.emitEventCtx(ctx, subjectIdentityUpdated, map[string]any{"id": r.ID})
			}
		}
	}
}

// runObjects keeps the object lists current (control plane).
func (a *App) runObjects(ctx context.Context) { a.objects.Run(ctx) }

// coveringRoles answers, for an object id of the server, the sorted names
// of the roles that cover it, as the gate decides: a role on every object
// that binds an exposed tool whose pinned reach is not none, or a role that
// names the object and binds an exposed tool whose reach is argument and
// that has not drifted, the only tool a partial role can use, while
// apps.objects.enabled is on. A binding with no covers line covers nothing.
func (a *App) coveringRoles(appName string, pinned manager.ObjectsSpec) func(id string) []string {
	view, _ := a.manager.View(appName)
	bindings, _ := a.gateway.bindings.Load().([]gwBinding)
	usable := func(tool string) bool {
		oc, ok := a.manager.ObjectsFor(appName, tool)
		return ok && oc.Reach.Reach == manager.ReachArgument && !oc.Drifted
	}
	binds := func(b gwBinding, reach func(string) bool) bool {
		for _, tl := range view.Tools {
			if reach(tl.Name) && manager.MatchAnyGlob(b.Matchers, tl.Name) {
				return true
			}
		}
		return false
	}
	var every []string
	var some []objectsHolder
	for _, b := range bindings {
		scope := objects.CompileScope(b.Scope)
		switch {
		case b.App != appName || scope == nil:
		case scope.Every() && binds(b, func(tool string) bool { return pinned.ReachOf(tool).Reach != manager.ReachNone }):
			every = append(every, b.Role)
		case !scope.Every() && a.cfg.Apps.Objects.Enabled && binds(b, usable):
			some = append(some, objectsHolder{b.Role, scope})
		}
	}
	return func(id string) []string {
		out := append([]string{}, every...)
		for _, h := range some {
			if h.scope.Covers(id) {
				out = append(out, h.role)
			}
		}
		slices.Sort(out)
		return slices.Compact(out)
	}
}

// objectsHolder is a role whose binding names objects one by one.
type objectsHolder struct {
	role  string
	scope *objects.Scope
}
