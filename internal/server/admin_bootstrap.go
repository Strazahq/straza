package server

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/strazahq/straza/internal/authn"
	"github.com/strazahq/straza/internal/store"
)

// BreakGlassUsername is the reserved local admin of the unified role model:
// the account that guarantees a deployment can
// never be locked out by its IdM. Invisible to the SCIM surface end to
// end; refuses deactivation, lock, delete, and de-role on every wire;
// credential rotates on-box only (strazactl users set-password); every
// interactive login as it is alarmed on the audit chain.
const BreakGlassUsername = "break-glass"

// ensureBreakGlass creates the break-glass admin when missing, on fresh
// AND upgraded stores, both profiles. Password printed once at creation,
// never persisted in clear. A row that carries an external link is left as
// it is and warned about on every boot. A row another replica creates in
// the same instant is used as if this boot had found it. A row this boot
// finds without straza-admin gets the role back.
func (a *App) ensureBreakGlass(ctx context.Context) error {
	u, err := a.store.Users().GetByUsername(ctx, BreakGlassUsername)
	if errors.Is(err, store.ErrNotFound) {
		var created bool
		if u, created, err = a.createBreakGlass(ctx); created {
			return nil
		}
	} else if err == nil {
		err = a.repairBreakGlassRole(ctx, u)
	}
	if err != nil {
		return err
	}
	// The verifier refuses such a link, and it stays on the row as the
	// evidence of who held it.
	if u.ExternalID != "" {
		a.log.Warn("BREAK-GLASS: the emergency admin is linked to an external identity. Sign-in through the link is refused, but someone may have signed in through it before this version: "+
			"check the audit chain for actions by break-glass and rotate its password with `strazactl users set-password break-glass`", "externalId", u.ExternalID)
	}
	return nil
}

// createBreakGlass creates the break-glass admin with its straza-admin
// assignment and prints the password once. It answers created false with
// the row when another replica created the admin in the same instant: that
// replica assigns the role and prints the password.
func (a *App) createBreakGlass(ctx context.Context) (store.User, bool, error) {
	password := authn.RandomPassword()
	hash, err := authn.HashPassword(password)
	if err != nil {
		return store.User{}, false, err
	}
	u, peer, err := adoptPeerRow(a.log, "the break-glass admin", func() (store.User, error) {
		return a.store.Users().Create(ctx, store.User{Username: BreakGlassUsername, Display: "Break-glass admin", PasswordHash: hash})
	}, func() (store.User, error) {
		return a.store.Users().GetByUsername(ctx, BreakGlassUsername)
	})
	if err != nil || peer {
		return u, false, err
	}
	if _, err := a.assignBreakGlassAdmin(ctx, u); err != nil {
		return store.User{}, false, err
	}
	a.log.Warn("break-glass admin created. Store this password in your vault now, it will not be shown again; rotate on-box via `strazactl users set-password break-glass`",
		"username", BreakGlassUsername, "password", password)
	return u, true, nil
}

// repairBreakGlassRole assigns straza-admin to the break-glass row u when u
// holds no assignment of it in force now: the row a boot leaves when it
// stops between the user insert and the assignment, or a row whose
// assignment's validity window has ended or not begun, which is removed
// first because the pair is unique. It logs the repair once and announces
// it as an assignment does. It never sets a password: nobody saw the one
// the first boot hashed.
func (a *App) repairBreakGlassRole(ctx context.Context, u store.User) error {
	held, err := a.store.Roles().ListAssignments(ctx, store.SubjectUser, u.ID)
	if err != nil {
		return err
	}
	role, err := a.store.Roles().GetByName(ctx, AdminRole)
	switch {
	case errors.Is(err, store.ErrNotFound):
		held = nil // no role, so no assignment of it
	case err != nil:
		return err
	}
	msg := "the break-glass admin held no straza-admin assignment in force, most likely because an earlier boot stopped before it assigned the role, so this boot assigned it."
	now := time.Now()
	for _, as := range held {
		if as.RoleID != role.ID {
			continue
		}
		if (as.ValidFrom == nil || !now.Before(*as.ValidFrom)) && (as.ValidTo == nil || now.Before(*as.ValidTo)) {
			return nil
		}
		if err := a.store.Roles().Unassign(ctx, as.ID); err != nil && !errors.Is(err, store.ErrNotFound) {
			return err
		}
		msg = "the break-glass admin held no straza-admin assignment in force, because the validity window of its assignment had ended or not yet begun, so this boot replaced it with an assignment that has no window."
	}
	peer, err := a.assignBreakGlassAdmin(ctx, u)
	if err != nil || peer {
		return err
	}
	a.identityChangedCtx(ctx, "straza.identity.updated", u.ID)
	a.log.Info(msg+" If nobody holds the break-glass password, have an admin set a new one with `strazactl users set-password break-glass --password <new password>`. "+
		"When no other admin exists, set oidc.bootstrapAdmin to a person's username and restart strazad, so that person becomes an admin at their next sign-in through the identity provider", "username", BreakGlassUsername)
	return nil
}

// assignBreakGlassAdmin assigns straza-admin to the break-glass row u and
// creates the role when it is missing. It answers peer true when another
// replica wrote the assignment in the same instant.
func (a *App) assignBreakGlassAdmin(ctx context.Context, u store.User) (bool, error) {
	role, err := a.store.Roles().GetByName(ctx, AdminRole)
	if errors.Is(err, store.ErrNotFound) {
		role, _, err = adoptPeerRow(a.log, "the role "+AdminRole, func() (store.Role, error) {
			return a.store.Roles().Create(ctx, store.Role{Name: AdminRole, Description: "Straza administration",
				Plane: store.RolePlaneControl})
		}, func() (store.Role, error) {
			return a.store.Roles().GetByName(ctx, AdminRole)
		})
	}
	if err != nil {
		return false, err
	}
	_, peer, err := adoptPeerRow(a.log, "the "+AdminRole+" assignment of break-glass", func() (store.RoleAssignment, error) {
		return a.store.Roles().Assign(ctx, store.RoleAssignment{SubjectKind: store.SubjectUser, SubjectID: u.ID, RoleID: role.ID})
	}, func() (store.RoleAssignment, error) {
		held, err := a.store.Roles().ListAssignments(ctx, store.SubjectUser, u.ID)
		if err != nil {
			return store.RoleAssignment{}, err
		}
		for _, as := range held {
			if as.RoleID == role.ID {
				return as, nil
			}
		}
		return store.RoleAssignment{}, store.ErrNotFound
	})
	return peer, err
}

// adoptPeerRow creates a boot row with create and, when that answers
// store.ErrConflict, reads back with read the row that another replica
// booting in the same instant created, logs that, and answers it with peer
// true. With no row to read back the conflict stays the error, so a replica
// never boots without the row (fail closed).
func adoptPeerRow[T any](log *slog.Logger, what string, create, read func() (T, error)) (row T, peer bool, err error) {
	row, err = create()
	if !errors.Is(err, store.ErrConflict) {
		return row, false, err
	}
	theirs, readErr := read()
	if readErr != nil {
		return row, false, err
	}
	log.Info("another replica created the row at the same moment, so this replica uses it", "row", what)
	return theirs, true, nil
}

// formerDraftConfigRoleDescription is the drafting role's description before
// the built-in straza server was called an MCP server, which stores booted
// before then still hold.
const formerDraftConfigRoleDescription = "Lets an agent propose config drafts through the built-in straza app's tools straza__draft_submit and straza__draft_status. " +
	"A person publishes them. It opens no console area, and straza-admin does not include it."

// formerProductDescriptions maps a product role to the description an
// earlier release created it with. It is the only text a boot replaces.
var formerProductDescriptions = map[string]string{
	DraftConfigRole: formerDraftConfigRoleDescription,
}

// ensureProductRoles creates the reserved product roles when missing, the
// self-enrollment pair, the global MCP admin and the drafting role, on fresh
// AND upgraded stores, both profiles. Store-direct on purpose: the API-level
// straza-* name guard never applies to bootstrap. No assignments and no
// implication: WHO holds them is the IdM's call. Each role it creates is
// announced with one straza.identity.updated, so an identity manager's
// LiveSync sees it before its next full reconciliation. A role another
// replica creates in the same instant is used, and that replica announces it.
// A role that exists and still holds the description an earlier release
// created it with (formerProductDescriptions) gets the current one,
// announced the same way with one Info line. Words a person gave it stay,
// and its name, plane and holders never change here.
func (a *App) ensureProductRoles(ctx context.Context) error {
	for name, desc := range map[string]string{
		EnrollMobileRole:  "May enroll their own phone as an approval device.",
		EnrollBrowserRole: "May enroll a signed-in browser as an approval device.",
		MCPAdminRole:      MCPAdminRoleDescription,
		DraftConfigRole:   DraftConfigRoleDescription,
	} {
		if got, err := a.store.Roles().GetByName(ctx, name); err == nil {
			// A person can give a product role their own words through the
			// admin API, a draft or the console, so only the product's own
			// earlier text is replaced.
			if former, ok := formerProductDescriptions[name]; !ok || got.Description != former {
				continue
			}
			got.Description = desc
			if _, err := a.store.Roles().Update(ctx, got); err != nil {
				return err
			}
			a.emitEventCtx(ctx, "straza.identity.updated", map[string]any{"id": got.ID})
			a.log.Info("replaced the description an earlier release gave a product role with the current product text", "role", name)
			continue
		} else if !errors.Is(err, store.ErrNotFound) {
			return err
		}
		role, peer, err := adoptPeerRow(a.log, "the role "+name, func() (store.Role, error) {
			return a.store.Roles().Create(ctx, store.Role{
				Name: name, Description: desc, Plane: store.RolePlaneControl,
			})
		}, func() (store.Role, error) {
			return a.store.Roles().GetByName(ctx, name)
		})
		if err != nil {
			return err
		}
		if !peer {
			a.emitEventCtx(ctx, "straza.identity.updated", map[string]any{"id": role.ID})
		}
	}
	return nil
}

// ensureAppAdminRoles mints an admin role for every server that has none,
// which is every row that predates the column and every row whose role was
// deleted while the server lay removed. Each mint announces itself the way
// a role create does, with an identity event and a log line, and no admin
// record, because boot is nobody's request.
func (a *App) ensureAppAdminRoles(ctx context.Context) error {
	changed, err := a.store.Apps().BackfillAdminRoles(ctx)
	if err != nil {
		return err
	}
	for _, row := range changed {
		role, err := a.store.Roles().GetByID(ctx, row.AdminRoleID)
		if err != nil {
			return err
		}
		a.resolver.Bump()
		a.emitEventCtx(ctx, "straza.identity.updated", map[string]any{"id": role.ID})
		a.log.Info("minted the admin role of a server that had none", "app", row.Name, "role", role.Name)
	}
	return nil
}

// isBreakGlass reports whether userID is the protected break-glass row.
func (a *App) isBreakGlass(ctx context.Context, userID string) bool {
	u, err := a.store.Users().GetByID(ctx, userID)
	return err == nil && u.Username == BreakGlassUsername
}

// bootstrapAdmin creates the first admin on an empty standalone store:
// user `admin` with role straza-admin and a random password printed once.
// It reports whether it bootstrapped, i.e. whether the store was fresh.
func (a *App) bootstrapAdmin(ctx context.Context) (bool, error) {
	users, err := a.store.Users().List(ctx)
	if err != nil {
		return false, err
	}
	if len(users) > 0 {
		return false, nil
	}
	password := authn.RandomPassword()
	hash, err := authn.HashPassword(password)
	if err != nil {
		return false, err
	}
	u, err := a.store.Users().Create(ctx, store.User{Username: "admin", Display: "Bootstrap Admin", PasswordHash: hash})
	if err != nil {
		return false, err
	}
	role, err := a.store.Roles().Create(ctx, store.Role{Name: AdminRole, Description: "Straza administration",
		// Control plane by definition.
		Plane: store.RolePlaneControl})
	if err != nil {
		return false, err
	}
	if _, err := a.store.Roles().Assign(ctx, store.RoleAssignment{
		SubjectKind: store.SubjectUser, SubjectID: u.ID, RoleID: role.ID,
	}); err != nil {
		return false, err
	}
	// Printed once, never persisted in clear. Rotate via
	// `strazactl users set-password admin`.
	a.log.Warn("bootstrap admin created. Store this password now, it will not be shown again",
		"username", "admin", "password", password)
	return true, nil
}

// starterPolicyYAML is the starter seed: one visible, plainly safe guardrail
// so a fresh standalone install demonstrates a deny-with-reason out of the box
// without getting in the way of normal work. Everything else stays on the
// standalone allow default (`git status` keeps working).
// Patterns mirror the pinned conformance semantics in
// spec/conformance/decisions/shell-standalone.yaml (argv-join matching
// normalizes whitespace and quoted flags).
const starterPolicyYAML = `apiVersion: straza.dev/v1beta1
kind: PolicySet
metadata:
  name: standalone-starter
  description: Seeded on first standalone boot. Edit or delete freely. It exists so the deny demo works out of the box.
spec:
  rules:
    - id: block-recursive-delete
      tools: [shell.exec]
      command:
        denyPatterns:
          - "rm -rf *"
          - "rm -fr *"
          - "rm -r -f *"
          - "rm -f -r *"
          - "sudo rm -rf *"
          - "sudo rm -fr *"
      effect: deny
      reason: "Straza starter policy: recursive force-delete is denied. Delete files individually, or an admin can edit the standalone-starter PolicySet."
`

// bootstrapStarterPolicy seeds the starter PolicySet on a fresh standalone
// store. It runs only when bootstrapAdmin just created the store's first
// user, so upgraded installs never get a policy injected on restart, and the
// empty-policies check keeps a partially-failed first boot idempotent.
func (a *App) bootstrapStarterPolicy(ctx context.Context) error {
	existing, err := a.store.Policies().List(ctx)
	if err != nil {
		return err
	}
	if len(existing) > 0 {
		return nil
	}
	if _, err := a.store.Policies().Create(ctx, store.PolicySet{
		Name: "standalone-starter", Status: "active", YAMLSource: starterPolicyYAML,
	}); err != nil {
		return err
	}
	a.log.Info("starter policy seeded. Recursive force-delete (rm -rf) is denied with a reason; " +
		"edit or delete PolicySet standalone-starter to change this")
	return nil
}

// maybeBootstrapAdmin breaks the enterprise first-admin circle:
// with `oidc.bootstrapAdmin` set, an IdP-verified login matching it is
// granted straza-admin, but only while NO straza-admin assignment exists,
// so the knob goes inert the moment real role
// management is in place: revoking the bootstrap admin later never
// resurrects it. Runs on the login path, which already reads the store
// (the no-database-read rule governs tool-call decisions, not
// checkin/enroll). Best-effort: a failed grant logs and leaves the login
// itself intact.
func (a *App) maybeBootstrapAdmin(r *http.Request, u store.User) {
	if a.cfg.OIDC.BootstrapAdmin == "" || u.Username != a.cfg.OIDC.BootstrapAdmin {
		return
	}
	ctx := r.Context()
	role, err := a.store.Roles().GetByName(ctx, AdminRole)
	if errors.Is(err, store.ErrNotFound) {
		role, err = a.store.Roles().Create(ctx, store.Role{
			Name: AdminRole, Description: "Straza administrators (seeded by oidc.bootstrapAdmin)",
			// Control plane by definition.
			Plane: store.RolePlaneControl,
		})
	}
	if err != nil {
		a.log.Error("bootstrap admin: role", "err", err)
		return
	}
	existing, err := a.store.Roles().ListAllAssignments(ctx)
	if err != nil {
		a.log.Error("bootstrap admin: list assignments", "err", err)
		return
	}
	bg, bgErr := a.store.Users().GetByUsername(ctx, BreakGlassUsername)
	for _, as := range existing {
		if as.RoleID != role.ID {
			continue
		}
		// The break-glass account's standing assignment does not count as
		// "an admin exists": the knob's job is seeding the first HUMAN
		// admin, and break-glass exists on every store by construction.
		if bgErr == nil && as.SubjectKind == store.SubjectUser && as.SubjectID == bg.ID {
			continue
		}
		return // a real admin already exists, so the knob is inert
	}
	if _, err := a.store.Roles().Assign(ctx, store.RoleAssignment{
		SubjectKind: store.SubjectUser, SubjectID: u.ID, RoleID: role.ID, Origin: "admin",
	}); err != nil {
		a.log.Error("bootstrap admin: assign", "err", err)
		return
	}
	a.identityChanged(r, "straza.identity.updated", u.ID)
	a.log.Warn("assigned straza-admin via oidc.bootstrapAdmin. Remove the knob once your identity manager manages the role",
		"user", u.Username)
}
