package store

import (
	"context"
	"time"
)

// The outcomes of an object list's last listing.
const (
	ObjectsNever  = "never"
	ObjectsOK     = "ok"
	ObjectsFailed = "failed"
)

// ObjectList is the status row of one server's object list: the pinned
// type and the hash of its declaration, the version of the last good
// listing, the counts of the last change, the last outcome and when the
// next listing is due. ClaimOwner and ClaimUntil name the replica that
// lists it now, empty when none does. Renamed is not stored: only the row
// Replace answers carries it, as the ids whose value that listing changed.
type ObjectList struct {
	AppID        string
	Type         string
	DeclHash     string
	Version      int64
	ListHash     string
	Count        int
	Joined, Left int
	Outcome      string
	Reason       string
	ListedAt     *time.Time
	AttemptedAt  *time.Time
	DueAt        time.Time
	ClaimOwner   string
	ClaimUntil   *time.Time
	Renamed      []string
}

// ListedObject is one object of a server's object list. Attributes is the
// JSON object of the attributes the pinned declaration names, and
// AddedVersion is the version at which its id joined the list.
type ListedObject struct {
	ID           string
	Value        string
	Title        string
	Attributes   string
	AddedVersion int64
}

// Listing is what one listing settles on its row: the outcome, the reason
// of a failure, when it ran and when the next one is due.
type Listing struct {
	Outcome string
	Reason  string
	At      time.Time
	Due     time.Time
}

// ObjectListRepo keeps the object lists: one status row per server that
// pins an object type, and the objects of its last good listing. A
// decision never reads it; a background loop loads it into memory.
type ObjectListRepo interface {
	// List answers every status row, ordered by app id.
	List(ctx context.Context) ([]ObjectList, error)
	// Get answers the status row of appID, or ErrNotFound.
	Get(ctx context.Context, appID string) (ObjectList, error)
	// Ensure inserts a never-listed row for appID due at due, and leaves
	// a row that exists as it is.
	Ensure(ctx context.Context, appID, typ, declHash string, due time.Time) error
	// Reset starts the row of appID over for a new declaration: its
	// objects go, it takes typ and declHash, reads never listed and is due
	// at due. The version stays, so the next good listing raises it. It
	// answers ErrNotFound when the row is gone or already has declHash.
	Reset(ctx context.Context, appID, typ, declHash string, due time.Time) error
	// Delete removes the row of appID and its objects; an absent row is
	// no error.
	Delete(ctx context.Context, appID string) error
	// Claim takes the listing of appID for owner until until, when the
	// row is due at now and no claim holds it past now. It answers the row
	// as claimed and true, or false when another replica holds it or it is
	// not due.
	Claim(ctx context.Context, appID, owner string, now, until time.Time) (ObjectList, bool, error)
	// Replace stores a changed listing under owner's claim on the row
	// with declHash, in one transaction: the objects, the next version,
	// the count, the ids that joined and left, listHash, outcome ok, and
	// the claim cleared. An object keeps the version its id joined at. It
	// answers the row with Renamed, the sorted ids whose value changed,
	// or ErrNotFound when the claim or the declaration moved.
	Replace(ctx context.Context, appID, owner, declHash, listHash string, objs []ListedObject, l Listing) (ObjectList, error)
	// Settle stores an unchanged or failed listing under owner's claim on
	// the row with declHash: outcome, reason, times and the claim cleared,
	// objects and version untouched. It answers ErrNotFound when the
	// claim or the declaration moved.
	Settle(ctx context.Context, appID, owner, declHash string, l Listing) (ObjectList, error)
	// MarkDue makes the row of appID due at due. It answers ErrNotFound
	// when no row exists.
	MarkDue(ctx context.Context, appID string, due time.Time) error
	// MarkDueIfIdle makes the row of appID due at due when it was never
	// attempted or last attempted before attemptedBefore, and answers
	// whether it did. It answers ErrNotFound when no row exists.
	MarkDueIfIdle(ctx context.Context, appID string, due, attemptedBefore time.Time) (bool, error)
	// Objects answers the objects of appID, ordered by value.
	Objects(ctx context.Context, appID string) ([]ListedObject, error)
	// ObjectsByValues answers the id of each of values that the object
	// list of appID holds, by value. A value matches byte for byte, and a
	// value the list does not hold is absent from the map.
	ObjectsByValues(ctx context.Context, appID string, values []string) (map[string]string, error)
	// ObjectsByIDs answers the current value of each of ids that the
	// object list of appID holds, by id. An id the list does not hold is
	// absent from the map.
	ObjectsByIDs(ctx context.Context, appID string, ids []string) (map[string]string, error)
}
