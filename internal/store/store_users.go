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
	// KindCounts counts the live users of each UserKind word under f, with
	// f.Kind ignored, in one query. A kind with no user is absent.
	KindCounts(ctx context.Context, f UserFilter) (map[string]int, error)
	// SponsoredCounts counts the agents each named user sponsors, in one query.
	SponsoredCounts(ctx context.Context, usernames []string) (map[string]int, error)
	Update(ctx context.Context, u User) (User, error)
	// TakeOver stores the Entra object id objectID as the external id of the
	// live user id and sets its origin to entra, and answers the row read
	// back. It is the one write that changes a user's origin.
	TakeOver(ctx context.Context, id, objectID string) (User, error)
	// Rename gives the live user id the username username. Every live user
	// whose sponsor is the old username is pointed at the new one in the
	// same transaction, so the old name is no user's sponsor once another
	// user takes it. It answers the row read back and the ids of the users
	// whose sponsor it changed.
	Rename(ctx context.Context, id, username string) (User, []string, error)
	// UpdateRenaming writes every column of the live user u as Update does
	// and moves the sponsor of the users the old username sponsors as
	// Rename does, in one transaction, so a failed write moves no sponsor.
	UpdateRenaming(ctx context.Context, u User) (User, []string, error)
	// UpdateFields writes only the columns f names on the live user id, with
	// the revocation row and event of a non-nil revoke, in one transaction and
	// answers the row read back. No column and no revoke writes nothing.
	UpdateFields(ctx context.Context, id string, f UserFields, revoke *UserRevocation) (User, error)
	// SetPasswordHash stores hash on the live user id and writes the outbox
	// event ev in one transaction. On any error neither is written.
	SetPasswordHash(ctx context.Context, id, hash string, ev OutboxEvent) error
	// SoftDelete retires the user and ends every role grant it holds, with the
	// revocation row and event of a non-nil revoke, in one transaction, and
	// returns the removed assignment rows. On any error no row changes.
	SoftDelete(ctx context.Context, id string, revoke *UserRevocation) ([]RoleAssignment, error)
	// RevocationCommitted reports whether the user write that carried the
	// revocation event eventID committed, and answers the user's row as
	// stored, for a writer that lost the write's answer.
	RevocationCommitted(ctx context.Context, userID, eventID string) (User, bool, error)
}
