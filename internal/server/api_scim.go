package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"

	"github.com/strazahq/straza/internal/identity"
	"github.com/strazahq/straza/internal/manager"
	"github.com/strazahq/straza/internal/policy"
	"github.com/strazahq/straza/internal/scim"
	"github.com/strazahq/straza/internal/store"
	"github.com/strazahq/straza/internal/tokenscopes"
)

// scimAuthenticate verifies the bearer a SCIM request presents: an admin API
// token whose scope covers the scim area for the request method, the one
// long-lived credential this plane takes. On success the returned
// context names the token as the audit actor, so every straza.audit.admin
// record the request emits says which token acted. A refusal returns the
// status and the SCIM error detail.
func (a *App) scimAuthenticate(r *http.Request, bearer string) (context.Context, int, string) {
	ctx := r.Context()
	if !strings.HasPrefix(bearer, apiTokenPrefix) {
		return nil, http.StatusUnauthorized, scim.MissingTokenDetail
	}
	meta, ok := a.apiTokenAuthenticate(ctx, bearer)
	if !ok {
		return nil, http.StatusUnauthorized, "admin API token rejected: unknown, expired or revoked (strazactl api-token list)"
	}
	scope, err := tokenscopes.Parse(meta.Scope)
	if err != nil {
		return nil, http.StatusForbidden, "token scope no longer valid: " + err.Error()
	}
	if allowed, needed := scope.AllowsArea("scim", r.Method); !allowed {
		return nil, http.StatusForbidden, "token lacks scope " + needed + " (an identity manager needs scim:read,scim:write; strazactl api-token create --scope)"
	}
	return withActor(ctx, auditActor{Name: meta.Name, ID: meta.ID, Via: "api-token"}), 0, ""
}

// scimHandler serves the SCIM routes under the rule of an admin write:
// runBounded runs a write on a context the identity manager's leaving does
// not cancel, bounded by adminWriteBound, so a deactivation that committed
// still runs its kill switch and chains its records, and a read keeps the
// request's context. An answer a write started after the bound gives way to
// a 503 in SCIM's error shape with one Error record and one count, because
// the SCIM handlers log and count none of their own answers.
func (a *App) scimHandler() http.HandlerFunc {
	sub := http.NewServeMux()
	a.scimServer().Routes(sub)
	return func(w http.ResponseWriter, r *http.Request) {
		// The main mux matched only the mount. The access log, the metrics
		// and the Error record name the SCIM route the sub-mux matches, as
		// they did when the SCIM routes sat on the main mux.
		_, r.Pattern = sub.Handler(r)
		held, r := runBounded(w, r, sub.ServeHTTP)
		if held == nil {
			return
		}
		msg := fmt.Sprintf(adminWriteTimedOut, humanDuration(adminWriteBound), routeLabel(r))
		a.logFailure(r, "503", msg, held.heldBack())
		a.countHTTPError(r, "503")
		w.Header().Set("Content-Type", "application/scim+json")
		w.WriteHeader(http.StatusServiceUnavailable)
		_ = json.NewEncoder(w).Encode(map[string]any{"schemas": []string{scim.SchemaError}, "status": "503", "detail": msg})
	}
}

// scimServer assembles the SCIM subset over the server's enforcement seams.
func (a *App) scimServer() *scim.Server {
	return scim.New(scim.Deps{
		Store:        a.store,
		Authenticate: a.scimAuthenticate,
		Deactivate: func(ctx context.Context, userID, reason string) {
			a.revokeUserCtx(ctx, userID, reason, store.RevocationOriginSCIM)
			a.deprovisionGrants(ctx, userID, reason)
		},
		Reactivate: a.liftUserSCIMCtx,
		IdentityChanged: func(ctx context.Context, subject, id string) {
			a.resolver.Bump()
			a.emitEventCtx(ctx, subject, map[string]any{"id": id, "origin": "scim"})
		},
		MembershipChanged: func(ctx context.Context, ch scim.MembershipChange) {
			action := actionRolesAssign
			if ch.Action == scim.MembershipUnassign {
				action = actionRolesUnassign
			}
			a.auditAssignment(ctx, action, store.RoleAssignment{
				ID: ch.AssignmentID, SubjectKind: store.SubjectUser, SubjectID: ch.UserID,
				RoleID: ch.RoleID, Origin: ch.Origin,
			}, "")
		},
		UserWritten: func(ctx context.Context, action string, u store.User, changed []string) {
			if action == scim.UserCreated {
				a.auditUserCreate(ctx, u, store.OriginSCIM)
				return
			}
			a.auditUserUpdate(ctx, u, changed)
		},
		ProtectedUsername: BreakGlassUsername,
		RoleProjection:    a.groupRoleProjection,
		Log:               a.log,
	})
}

// deprovisionGrants removes every own row of a user who is leaving, OAuth
// grants and pasted tokens, and writes one straza.audit.admin record per
// row, the row's kind on it. Two seams call
// it: the SCIM Deactivate seam, because an IdM deactivation is
// deprovisioning and a later revive must not return with the old grants
// attached, and the admin API user delete, which ends the identity. An
// admin disable and a lock are reversible and leave the rows alone. A
// failed delete is logged and skipped, never returned, because the
// kill-switch cascade already ran and the caller must see success for it.
func (a *App) deprovisionGrants(ctx context.Context, userID, reason string) {
	rows, err := a.store.Credentials().ListByOwner(ctx, store.CredScopeUser, userID)
	if err != nil {
		a.log.Error("deprovision: grant list failed", "user", userID, "reason", reason, "err", err)
		return
	}
	for _, row := range rows {
		if err := a.broker.DeleteGrant(ctx, row.AppID, userID); err != nil {
			a.log.Error("deprovision: grant delete failed", "user", userID, "reason", reason, "credential", row.ID, "err", err)
			continue
		}
		appName := row.AppID
		if ap, err := a.store.Apps().GetByID(ctx, row.AppID); err == nil {
			appName = ap.Name
		}
		a.emitEventCtx(ctx, "straza.audit.admin", map[string]any{
			"action": "apps.grant.remove", "app": appName, "user": userID,
			"credentialId": row.ID, "kind": row.Kind, "reason": reason,
		})
		a.emitEventCtx(ctx, "straza.apps.updated", map[string]any{"change": "grant", "app": appName})
	}
}

// groupRoleProjection renders the spec/scim-profile §4.1 Group extension
// for the role a mapped group carries. A role id no row names renders no
// block.
func (a *App) groupRoleProjection(ctx context.Context, roleID string) map[string]any {
	role, err := a.store.Roles().GetByID(ctx, roleID)
	if err != nil {
		return nil
	}
	return a.groupRoleBlock(ctx, role)
}

// groupRoleBlock renders the §4.1 block for one role row: the role's
// identity plus its transitive access projection. Control-plane reads only
// (SCIM is polled by IdMs, never consulted by a decision path), computed
// with the SAME helpers the session catalog uses (identity.Closure over
// implications, manager.MatchAnyGlob over running views), so the evidence
// an IdM certifies equals what a session holding the role can actually
// reach. Errors degrade to a smaller (or absent) block, never a failed
// read: one broken policyset must not wedge an IdM reconciliation run.
func (a *App) groupRoleBlock(ctx context.Context, role store.Role) map[string]any {
	// What holding this role effectively grants: closure over implications.
	closure := map[string]bool{role.ID: true}
	if imps, err := a.store.Roles().ListImplications(ctx); err == nil {
		adj := map[string][]string{}
		for _, imp := range imps {
			adj[imp.RoleID] = append(adj[imp.RoleID], imp.ImpliesRoleID)
		}
		closure = identity.Closure(closure, adj)
	}
	closureNames := map[string]bool{}
	if all, err := a.store.Roles().List(ctx); err == nil {
		for _, r := range all {
			if closure[r.ID] {
				closureNames[r.Name] = true
			}
		}
	}

	// apps = the closure's bound apps (binding evidence, like the pull
	// lane's ri:apps); tools = what those bindings reach on the LIVE
	// catalog (running/degraded views only, the fail-closed truth
	// /v1/admin/tools also reports).
	appName := map[string]string{}
	if apps, err := a.store.Apps().List(ctx); err == nil {
		for _, ap := range apps {
			appName[ap.ID] = ap.Name
		}
	}
	matchersByApp := map[string][]string{}
	for id := range closure {
		bindings, err := a.store.ToolBindings().ListByRole(ctx, id)
		if err != nil {
			continue
		}
		for _, b := range bindings {
			var matchers []string
			if json.Unmarshal([]byte(b.ToolMatcher), &matchers) != nil || len(matchers) == 0 {
				continue
			}
			if name := appName[b.AppID]; name != "" {
				matchersByApp[name] = append(matchersByApp[name], matchers...)
			}
		}
	}
	appsOut := make([]string, 0, len(matchersByApp))
	for name := range matchersByApp {
		appsOut = append(appsOut, name)
	}
	sort.Strings(appsOut)
	var toolsOut []string
	for _, v := range a.manager.Views() {
		if v.Status != manager.StatusRunning && v.Status != manager.StatusDegraded {
			continue
		}
		matchers := matchersByApp[v.Name]
		if len(matchers) == 0 {
			continue
		}
		for _, tl := range v.Tools {
			if manager.MatchAnyGlob(matchers, tl.Name) {
				toolsOut = append(toolsOut, v.Name+":"+tl.Name)
			}
		}
	}
	sort.Strings(toolsOut)

	// policies = ACTIVE sets that NAME a projected role. Subject-global
	// sets (empty match) are deliberately absent: the block answers "what
	// does this grant add", not "what applies to everyone".
	var policiesOut []string
	if sets, err := a.store.Policies().List(ctx); err == nil {
		for _, p := range sets {
			if p.Status != "active" {
				continue
			}
			doc, err := policy.Parse([]byte(p.YAMLSource))
			if err != nil {
				a.log.Warn("scim: group projection skips unparsable policyset", "set", p.Name, "err", err)
				continue
			}
			for _, r := range doc.Spec.Match.Roles {
				if closureNames[r] {
					policiesOut = append(policiesOut, p.Name)
					break
				}
			}
		}
	}
	sort.Strings(policiesOut)

	block := map[string]any{
		"role":     role.Name,
		"roleKind": wireRoleKind(role),
		"plane":    role.Plane,
	}
	if role.Description != "" {
		block["description"] = role.Description
	}
	if len(appsOut) > 0 {
		block["apps"] = appsOut
	}
	if len(toolsOut) > 0 {
		block["tools"] = toolsOut
	}
	if len(policiesOut) > 0 {
		block["policies"] = policiesOut
	}
	// server = the owning server's name (revision 19), present only on a
	// server-owned role, so an IdM delineates those roles by one attribute
	// instead of parsing a name prefix.
	if name := appName[role.OwnerAppID]; name != "" {
		block["server"] = name
	}
	// administers = the live servers whose admin role is in the closure
	// (revision 18): a minted role names its one server, a business role
	// that implies minted roles names theirs. straza-global-mcp-admin lists
	// nothing, because its meaning is every server and this block lists
	// facts on rows, not fixed meanings.
	if role.Name != MCPAdminRole {
		ids := make([]string, 0, len(closure))
		for id := range closure {
			ids = append(ids, id)
		}
		if named, err := a.store.Apps().ListByAdminRoles(ctx, ids); err == nil && len(named) > 0 {
			administers := make([]string, 0, len(named))
			for _, row := range named {
				administers = append(administers, row.Name)
			}
			sort.Strings(administers)
			block["administers"] = administers
		}
	}
	return block
}

// origin records which authority created the row: the
// SCIM reactivation lift removes scim-origin rows only, so admin/external
// locks survive IdM writes.
func (a *App) revokeUserCtx(ctx context.Context, userID, reason, origin string) {
	// The deactivation this cascade enforces has committed, so each of its
	// writes runs on afterCommit's context: a client that left or a write
	// that ran past its bound must not stop the kill switch halfway.
	wctx, cancel := afterCommit(ctx)
	_, err := a.store.Revocations().Create(wctx, store.Revocation{
		Kind: store.RevokeUser, TargetID: userID, Reason: reason, Origin: origin,
	})
	cancel()
	if err != nil {
		a.log.Error("user revoke: the revocation record could not be written, so the block holds on this replica until it restarts",
			"user", userID, "err", err)
	}
	a.denylist.revokeUser(userID)
	a.pushRevocation("user", userID)
	// sessionsRevoked counts only the rows marked revoked. A session whose
	// write failed stays active in the store, and the user's denylist entry
	// refuses it on the refresh and on the admin plane.
	revokedSessions := 0
	wctx, cancel = afterCommit(ctx)
	sessions, err := a.store.Sessions().ListByUser(wctx, userID)
	cancel()
	if err != nil {
		a.log.Error("user revoke: the user's sessions could not be listed, so none was marked revoked and the denylist refuses them",
			"user", userID, "err", err)
	}
	for _, s := range sessions {
		if s.Status != store.SessionActive {
			continue
		}
		wctx, cancel := afterCommit(ctx)
		err := a.store.Sessions().SetStatus(wctx, s.ID, store.SessionRevoked)
		cancel()
		if err != nil {
			a.log.Error("user revoke: the session could not be marked revoked, so it stays active in the store and the denylist refuses it",
				"user", userID, "session", s.ID, "err", err)
		} else {
			revokedSessions++
		}
		a.denylist.revokeSession(s.ID)
		a.subjects.drop(s.ID)
		a.pushRevocation("session", s.ID)
	}
	a.emitRevokeRecord(ctx, "straza.revocation.user",
		"user revoke: the revocation event could not be written, so the other replicas miss the block until they restart",
		map[string]any{"user": userID, "reason": reason, "origin": origin})
	// The kill switch must leave a chain entry: straza.revocation.* converges
	// enforcement but only straza.audit.> is hash-chained, so without this
	// record the kill would leave no audit trace.
	a.emitRevokeRecord(ctx, "straza.audit.identity",
		"user revoke: the user.killed record could not be written, so the audit chain holds no record of this revoke",
		map[string]any{
			"action": "user.killed", "user": userID, "reason": reason, "origin": origin,
			"sessionsRevoked": revokedSessions,
		})
}

// emitRevokeRecord writes one record of a user revoke to the outbox on
// afterCommit's context, as emitEventCtx writes a record, and logs a failed
// write at Error with msg, because a lost record leaves the other replicas
// or the audit chain without the revoke.
func (a *App) emitRevokeRecord(ctx context.Context, subject, msg string, data map[string]any) {
	ctx, cancel := afterCommit(ctx)
	defer cancel()
	ev, err := a.cloudEvent(ctx, subject, data)
	if err == nil {
		_, err = a.store.Outbox().Insert(ctx, ev)
	}
	if err != nil {
		a.log.Error(msg, "user", data["user"], "err", err)
	}
}

// allowUserCtx is the FULL reactivation lift, the compensating mirror of
// revokeUserCtx: every persisted revocation row for the user goes away
// (so boot-time denylist rebuilds stop re-blocking them), the local denylist
// entry lifts, and straza.revocation.lift converges every other pod and every
// future stream replay. It touches no session row and no device row: the
// sessions the kill revoked stay revoked, and the device enrollment survives,
// so the next check-in from that device opens a new session without a new
// enrollment. Reserved for explicit Straza actions (admin enable, unlock);
// the SCIM lane uses liftUserSCIMCtx.
func (a *App) allowUserCtx(ctx context.Context, userID string) {
	if err := a.store.Revocations().Delete(ctx, store.RevokeUser, userID); err != nil {
		a.log.Error("reactivation: revocation delete failed", "user", userID, "err", err)
	}
	a.denylist.allowUser(userID)
	a.emitEventCtx(ctx, "straza.revocation.lift", map[string]any{"user": userID, "origin": "admin"})
	a.emitEventCtx(ctx, "straza.audit.identity", map[string]any{
		"action": "user.reactivated", "user": userID, "origin": "admin",
	})
}

// liftUserSCIMCtx is the origin-selective lift bound to SCIM reactivation:
// only scim-origin rows are deleted.
// The denylist entry (and the straza.revocation.lift broadcast that would
// clear it on every other pod) happens ONLY when no rows survive: a lift
// event means "this user is no longer denied", and replayed history must
// converge on the same answer (spec/events §revocation). When a lock
// survives, enforcement is unchanged everywhere and the attempt is recorded
// on the audit chain instead.
func (a *App) liftUserSCIMCtx(ctx context.Context, userID string) {
	if err := a.store.Revocations().DeleteByOrigin(ctx, store.RevokeUser, userID, store.RevocationOriginSCIM); err != nil {
		a.log.Error("scim lift: revocation delete failed", "user", userID, "err", err)
		return // unknown state: keep the user denied (fail closed)
	}
	remaining, err := a.store.Revocations().ListByTarget(ctx, store.RevokeUser, userID)
	if err != nil {
		a.log.Error("scim lift: revocation list failed", "user", userID, "err", err)
		return // unknown state: keep the user denied (fail closed)
	}
	if len(remaining) > 0 {
		held := make([]string, 0, len(remaining))
		for _, rv := range remaining {
			held = append(held, rv.Origin)
		}
		a.emitEventCtx(ctx, "straza.audit.identity", map[string]any{
			"action": "user.lift.blocked", "user": userID, "origin": "scim", "heldBy": held,
		})
		return
	}
	a.denylist.allowUser(userID)
	a.emitEventCtx(ctx, "straza.revocation.lift", map[string]any{"user": userID, "origin": "scim"})
	a.emitEventCtx(ctx, "straza.audit.identity", map[string]any{
		"action": "user.reactivated", "user": userID, "origin": "scim",
	})
}

// pushRevocation fires the low-latency, target-scoped client push. It
// is best-effort core NATS: the JetStream event is the durable path, this is
// the sub-second path for connected straza daemons.
func (a *App) pushRevocation(kind, id string) {
	if a.bus == nil {
		return
	}
	_ = a.bus.PublishCore("straza.push."+kind+"."+id, []byte(`{"kind":"`+kind+`","id":"`+id+`"}`))
}

// policyPushSubject is the broadcast policy-update nudge (never a
// revocation; see the daemon's handling and spec/events §5 rev 7).
const policyPushSubject = "straza.push.policy"

// pushPolicyNudge fires the best-effort core-NATS policy nudge after a
// snapshot activation. Snapshot ids are not secrets (served to every
// enrolled client), and daemons verify the blob against pinned keys before
// adopting; the nudge only moves the moment of a fetch that would happen
// anyway.
func (a *App) pushPolicyNudge(data map[string]any) {
	if a.bus == nil {
		return
	}
	id, _ := data["snapshot"].(string)
	payload, err := json.Marshal(map[string]string{"kind": "policy", "snapshot": id})
	if err != nil {
		return
	}
	_ = a.bus.PublishCore(policyPushSubject, payload)
}
