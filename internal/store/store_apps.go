package store

import "context"

// AppRepo manages MCP app records. Apps are soft-deleted.
type AppRepo interface {
	Create(ctx context.Context, a App) (App, error)
	GetByID(ctx context.Context, id string) (App, error)
	GetByName(ctx context.Context, name string) (App, error)
	List(ctx context.Context) ([]App, error)
	Update(ctx context.Context, a App) (App, error)
	// SetStatus writes the row's runtime status and updated_at and no other
	// column. It answers ErrNotFound when no live row has the id.
	SetStatus(ctx context.Context, id, status string) error
	SoftDelete(ctx context.Context, id string) error
	// Revive brings the soft-deleted row of the same name back with the
	// given manifest and status, keeping its id (apps.name is UNIQUE, so
	// a removed app's name is reinstalled through its old row). It answers
	// ErrNotFound when no soft-deleted row of that name exists.
	Revive(ctx context.Context, a App) (App, error)
	// ListByAdminRoles answers the live rows whose admin role is one of
	// roleIDs; an empty list of ids answers nothing.
	ListByAdminRoles(ctx context.Context, roleIDs []string) ([]App, error)
	// BackfillAdminRoles mints a role for every live app whose admin role
	// is empty or names a role that no longer exists, and answers the rows
	// it changed with their new role id.
	BackfillAdminRoles(ctx context.Context) ([]App, error)
}

// ToolBindingRepo manages role↔app tool exposure rows.
type ToolBindingRepo interface {
	Create(ctx context.Context, b ToolBinding) (ToolBinding, error)
	List(ctx context.Context) ([]ToolBinding, error)
	ListByRole(ctx context.Context, roleID string) ([]ToolBinding, error)
	Delete(ctx context.Context, id string) error
}

// CredentialRepo stores ciphertext only.
type CredentialRepo interface {
	Create(ctx context.Context, c Credential) (Credential, error)
	GetByID(ctx context.Context, id string) (Credential, error)
	ListByApp(ctx context.Context, appID string) ([]Credential, error)
	// ListByOwner returns every row of one scope held by one owner id,
	// oldest first: a user's grants across apps, or a role's statics.
	ListByOwner(ctx context.Context, scope, ownerID string) ([]Credential, error)
	Update(ctx context.Context, c Credential) (Credential, error)
	Delete(ctx context.Context, id string) error
}
