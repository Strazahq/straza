package store

import "context"

// UserRepo manages the users aggregate. Users are soft-deleted.
type UserRepo interface {
	Create(ctx context.Context, u User) (User, error)
	GetByID(ctx context.Context, id string) (User, error)
	// GetByIDs bulk-fetches by id; missing or deleted ids are simply absent
	// from the result (batch seam for SCIM member display-name resolution).
	GetByIDs(ctx context.Context, ids []string) ([]User, error)
	GetByUsername(ctx context.Context, username string) (User, error)
	// ListByEmail answers every undeleted user with a matching email, of any
	// status or type, oldest first. Emails are not unique, so the Slack lane
	// picks among these rows. An empty email matches nothing, never the many
	// rows without an address.
	ListByEmail(ctx context.Context, email string) ([]User, error)
	GetByExternalID(ctx context.Context, externalID string) (User, error)
	// GetByEntraAgentID answers the live user that holds the normalized
	// Entra agent id. An empty id is ErrNotFound.
	GetByEntraAgentID(ctx context.Context, id string) (User, error)
	List(ctx context.Context) ([]User, error)
	// Page returns limit users after the cursor in the order srt names (the
	// zero Sort is newest first), skipping soft-deleted rows like List,
	// narrowed server-side by f (see UserFilter for semantics).
	Page(ctx context.Context, f UserFilter, srt Sort, c Cursor, limit int) ([]User, error)
	// SponsoredCounts counts the agents each named user sponsors, in one query.
	SponsoredCounts(ctx context.Context, usernames []string) (map[string]int, error)
	Update(ctx context.Context, u User) (User, error)
	// UpdateFields writes only the columns f names on the live user id, with
	// the revocation row and event of a non-nil revoke, in one transaction and
	// answers the row read back. No column and no revoke writes nothing.
	UpdateFields(ctx context.Context, id string, f UserFields, revoke *UserRevocation) (User, error)
	// SoftDelete retires the user and ends every role grant it holds, with the
	// revocation row and event of a non-nil revoke, in one transaction, and
	// returns the removed assignment rows. On any error no row changes.
	SoftDelete(ctx context.Context, id string, revoke *UserRevocation) ([]RoleAssignment, error)
	// RevocationCommitted reports whether the user write that carried the
	// revocation event eventID committed, and answers the user's row as
	// stored, for a writer that lost the write's answer.
	RevocationCommitted(ctx context.Context, userID, eventID string) (User, bool, error)
}
