package server

import (
	"context"
	"strings"

	"github.com/strazahq/straza/internal/entragraph"
	"github.com/strazahq/straza/internal/entrasync"
	"github.com/strazahq/straza/internal/store"
)

// Reasons on the audit record of an assignment the Entra sync started or
// ended.
const (
	reasonEntraMember = "a member of the role's group in Entra"
	reasonEntraLeft   = "no longer a member of the role's group in Entra"
)

// entraDeployment is the part of every role group's uniqueName that keeps
// two Straza deployments in one tenant apart: the last 12 hex digits of
// the project id, which never changes.
func entraDeployment(projectID string) string {
	hex := strings.Map(func(r rune) rune {
		if (r >= '0' && r <= '9') || (r >= 'a' && r <= 'f') {
			return r
		}
		return -1
	}, strings.ToLower(strings.TrimPrefix(projectID, "prj_")))
	if len(hex) > 12 {
		hex = hex[len(hex)-12:]
	}
	return hex
}

// entraSyncer builds the Entra sync over graph. Its records name no actor,
// because the sync acts on what it read in Entra and Graph does not say
// who changed a group. An assignment it ends drops the cached subjects of
// the user's sessions, as every ended assignment does.
func (a *App) entraSyncer(graph entrasync.Graph) *entrasync.Syncer {
	return entrasync.New(entrasync.Deps{
		Graph: graph,
		Roles: a.store.Roles(), Users: a.store.Users(), Groups: a.store.EntraGroups(), Agents: a.store.EntraAgents(), Settings: a.store.Settings(),
		Membership: a.entraMembership(reasonEntraMember, reasonEntraLeft, true),
		People:     a.entraPeople(),
		Record: func(ctx context.Context, data map[string]any) {
			a.emitEventCtx(noActor(ctx), "straza.audit.admin", data)
		},
		Config:     a.cfg.Identity.Entra,
		Deployment: entraDeployment(a.projectID),
		Protected: func(ctx context.Context) string {
			u, err := a.store.Users().GetByUsername(ctx, BreakGlassUsername)
			if err != nil {
				return ""
			}
			return u.ID
		},
		Log: a.log,
	})
}

// runEntraSync runs the Entra sync on the one replica that holds its
// lease, one pass every delta interval. A replica that takes the lease
// over starts with a full pass, because the sync keeps its delta links in
// memory.
func (a *App) runEntraSync(ctx context.Context) {
	cfg := a.cfg.Identity.Entra
	graph, err := entragraph.New(entragraph.Options{Tenant: cfg.Tenant, ClientID: cfg.ClientID, Signer: a.assertionKeys})
	if err != nil {
		a.log.Error("the Entra sync did not start, so no role group is written or read. Check identity.entra in the config and restart strazad", "err", err)
		return
	}
	syncer := a.entraSyncer(graph)
	a.runLeased(ctx, leaseEntraSync, cfg.DeltaInterval, syncer.Pass)
}

// reasonEntraEnabled is the reason on the record of a person the sync
// enabled.
const reasonEntraEnabled = "the account is enabled in Entra"

// entraPeople is the server's part in what the Entra sync does to a
// person. A create and a change write the records and the identity event
// of every identity source, with the origin entra and no actor. A disable
// runs the kill switch as a SCIM deactivation does. An enable lifts the
// revocation such a disable wrote and one an identity manager wrote before
// Entra took the person over, so a lock an admin set stays.
func (a *App) entraPeople() entrasync.People {
	changed := func(ctx context.Context, u store.User, fields []string, reason string) {
		a.resolver.Bump()
		a.emitEventCtx(ctx, "straza.identity.updated", map[string]any{"id": u.ID, "origin": store.OriginEntra})
		a.emitIdentityAudit(noActor(ctx), map[string]any{
			"action": actionUserUpdate, "target": u.ID, "user": u.ID, "username": u.Username,
			"changed": fields, "origin": store.OriginEntra, "reason": reason,
		})
	}
	return entrasync.People{
		Created: func(ctx context.Context, u store.User, reason string) {
			a.resolver.Bump()
			a.emitEventCtx(ctx, "straza.identity.created", map[string]any{"id": u.ID, "origin": store.OriginEntra})
			a.auditUserCreateWithReason(noActor(ctx), u, store.OriginEntra, reason)
		},
		Updated: changed,
		Disable: func(ctx context.Context, userID, reason string) (store.User, error) {
			u, err := a.deactivateUser(noActor(ctx), userID, reason, store.RevocationOriginEntra)
			if err == nil {
				changed(ctx, u, []string{"status"}, reason)
			}
			return u, err
		},
		Enable: func(ctx context.Context, userID string) (store.User, error) {
			active := store.UserActive
			u, err := a.store.Users().UpdateFields(ctx, userID, store.UserFields{Status: &active}, nil)
			if err != nil {
				return store.User{}, err
			}
			// Entra owns the person, so the identity manager can no longer
			// lift a deactivation it wrote before, and this enable does.
			_, _ = a.liftUserOf(noActor(ctx), userID, store.RevocationOriginEntra, store.RevocationOriginEntra, store.RevocationOriginSCIM)
			changed(ctx, u, []string{"status"}, reasonEntraEnabled)
			return u, nil
		},
	}
}
