package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// AppAdminRolePrefix opens the name of every role the store mints for a
// server. The admin API refuses to create a role with it, so a role that
// wears it is product-minted by definition and goes with its server.
const AppAdminRolePrefix = "mcp-admin-"

// AppAdminRoleName derives a minted role's name from a server name: the
// prefix, the namespace before the last slash, then the short name, each
// lowered and folded to a-z, 0-9 and single hyphens.
func AppAdminRoleName(appName string) string {
	ns, short := "", appName
	if i := strings.LastIndex(appName, "/"); i >= 0 {
		ns, short = appName[:i], appName[i+1:]
	}
	parts := []string{strings.TrimSuffix(AppAdminRolePrefix, "-")}
	for _, p := range []string{ns, short} {
		if f := foldRoleName(p); f != "" {
			parts = append(parts, f)
		}
	}
	return strings.Join(parts, "-")
}

// AppAdminRoleDescription is the sentence a minted role carries, the one
// the console and the identity manager show beside its name.
func AppAdminRoleDescription(appName string) string {
	return "Administers the MCP server " + appName + ": its connection, credentials, settings and health, never its removal. " +
		"It opens the console's MCP servers area for that server only."
}

func foldRoleName(s string) string {
	var b strings.Builder
	dash := true
	for _, c := range strings.ToLower(s) {
		switch {
		case c >= 'a' && c <= 'z', c >= '0' && c <= '9':
			b.WriteRune(c)
			dash = false
		case !dash:
			b.WriteByte('-')
			dash = true
		}
	}
	return strings.TrimSuffix(b.String(), "-")
}

// dbExec is what a mint needs from either the pool or an open transaction.
type dbExec interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// mintAdminRole creates the control-plane role for appName on db, taking
// the first free suffix when the derived name is already a role.
func (r appRepo) mintAdminRole(ctx context.Context, db dbExec, appName string) (Role, error) {
	base := AppAdminRoleName(appName)
	name := base
	for n := 2; ; n++ {
		var one int
		err := db.QueryRowContext(ctx, r.s.q(`SELECT 1 FROM roles WHERE name = $1`), name).Scan(&one)
		if errors.Is(err, sql.ErrNoRows) {
			break
		}
		if err != nil {
			return Role{}, r.s.mapErr(err)
		}
		name = base + "-" + strconv.Itoa(n)
	}
	t := now()
	role := Role{ID: newID(), Name: name, Description: AppAdminRoleDescription(appName),
		Kind: RoleKindBusiness, Plane: RolePlaneControl, CreatedAt: t, UpdatedAt: t}
	_, err := db.ExecContext(ctx, r.s.q(`INSERT INTO roles (id, name, description, kind, plane, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7)`),
		role.ID, role.Name, role.Description, role.Kind, role.Plane, r.s.tArg(role.CreatedAt), r.s.tArg(role.UpdatedAt))
	if err != nil {
		return Role{}, r.s.mapErr(err)
	}
	return role, nil
}

// roleExists answers ErrNotFound for an id no role carries.
func (r appRepo) roleExists(ctx context.Context, db dbExec, roleID string) error {
	var one int
	err := db.QueryRowContext(ctx, r.s.q(`SELECT 1 FROM roles WHERE id = $1`), roleID).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("admin role %s: %w", roleID, ErrNotFound)
	}
	if err != nil {
		return r.s.mapErr(err)
	}
	return nil
}

// ensureAdminRole mints and sets a role for a live row whose field is
// empty or names a role that no longer exists. It answers whether it
// changed the row. When another replica set the row's role first, and the
// row now names a live role, it keeps that role and answers false.
func (r appRepo) ensureAdminRole(ctx context.Context, a App) (bool, error) {
	if a.AdminRoleID != "" {
		if err := r.roleExists(ctx, r.s.db, a.AdminRoleID); err == nil {
			return false, nil
		} else if !errors.Is(err, ErrNotFound) {
			return false, err
		}
	}
	err := r.replaceAdminRole(ctx, a)
	if err == nil {
		return true, nil
	}
	// A peer's mint took the role name first (ErrConflict), or its update
	// moved the field off the value this call read (ErrNotFound). The
	// transaction rolled this mint back, and a live role on the row now is
	// the peer's.
	if errors.Is(err, ErrConflict) || errors.Is(err, ErrNotFound) {
		if cur, getErr := r.GetByID(ctx, a.ID); getErr == nil && cur.AdminRoleID != "" &&
			r.roleExists(ctx, r.s.db, cur.AdminRoleID) == nil {
			return false, nil
		}
	}
	return false, err
}

// replaceAdminRole mints a role for the row a and points the row at it in
// one transaction, only while the row still names the role a carries.
func (r appRepo) replaceAdminRole(ctx context.Context, a App) error {
	tx, err := r.s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	role, err := r.mintAdminRole(ctx, tx, a.Name)
	if err != nil {
		return err
	}
	if err := r.setAdminRole(ctx, tx, a.ID, a.AdminRoleID, role.ID); err != nil {
		return err
	}
	return r.s.mapErr(tx.Commit())
}

// setAdminRole points a live row at roleID while it still names from, the
// empty string standing for no role. A row removed or moved to another role
// since answers ErrNotFound.
func (r appRepo) setAdminRole(ctx context.Context, db dbExec, appID, from, roleID string) error {
	res, err := db.ExecContext(ctx, r.s.q(`UPDATE apps SET admin_role_id = $1, updated_at = $2
		WHERE id = $3 AND deleted_at IS NULL AND COALESCE(admin_role_id, '') = $4`), roleID, r.s.tArg(now()), appID, from)
	return mustAffect(res, r.s.mapErr(err))
}

func (r appRepo) ListByAdminRoles(ctx context.Context, roleIDs []string) ([]App, error) {
	if len(roleIDs) == 0 {
		return nil, nil
	}
	ph := make([]string, len(roleIDs))
	args := make([]any, len(roleIDs))
	for i, id := range roleIDs {
		ph[i] = "$" + strconv.Itoa(i+1)
		args[i] = id
	}
	rows, err := r.s.query(ctx, `SELECT `+appCols+` FROM apps WHERE deleted_at IS NULL
		AND admin_role_id IN (`+strings.Join(ph, ", ")+`) ORDER BY name`, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []App
	for rows.Next() {
		a, err := scanApp(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

func (r appRepo) BackfillAdminRoles(ctx context.Context) ([]App, error) {
	rows, err := r.List(ctx)
	if err != nil {
		return nil, err
	}
	var changed []App
	for _, a := range rows {
		did, err := r.ensureAdminRole(ctx, a)
		if err != nil {
			return changed, err
		}
		if !did {
			continue
		}
		fresh, err := r.GetByID(ctx, a.ID)
		if err != nil {
			return changed, err
		}
		changed = append(changed, fresh)
	}
	return changed, nil
}
