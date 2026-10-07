package store

import (
	"context"
	"time"
)

// ObjectRef names one config object the way a draft does, by kind (App,
// Role or PolicySet) and name.
type ObjectRef struct{ Kind, Name string }

// DraftActor is who acted on a draft, named as the admin audit actor is.
// ID and Name are the user's or the admin API token's. Via is login,
// session, api-token, file or upgrade. Client is the session's harness
// name, id-token for a login token without a session, api-token for a
// token, and strazad for the apps directory and the upgrade. Agent is true
// for a user who is not a person, and the sponsor fields name an agent's
// sponsor as its cached subject resolved it.
type DraftActor struct {
	ID, Name, Via, Client  string
	Agent                  bool
	SponsorID, SponsorName string
}

// DraftRow is one row of the drafts table. Proposer is the author of
// revision 1. CheckedRevision is the revision the server last checked, and
// CheckCounts the JSON counts of that check. DecidedBy is who published or
// discarded it, empty for an expiry.
type DraftRow struct {
	ID                                      int64
	Revision                                int
	State, Door                             string
	Source, SourceHash, Slot, Note, Refusal string
	Reverts                                 int64
	Proposer                                DraftActor
	CheckedRevision                         int
	CheckedAt                               *time.Time
	CheckedSnapshot, CheckCounts            string
	AgentVerdict                            string
	CreatedAt, UpdatedAt                    time.Time
	ExpiresAt, DecidedAt                    *time.Time
	DecidedBy                               DraftActor
	DecidedReason, PublishedSnapshot, Acks  string
}

// DraftItemRow is one object of a draft's current revision. Base, BaseOp
// and BaseDoc describe the object when the item entered the draft, and
// BaseOp is empty until the item is stamped.
type DraftItemRow struct {
	Seq                   int
	Kind, Name, Op, Doc   string
	Base, BaseOp, BaseDoc string
	Offered               string
}

// DraftRevisionRow is who wrote one revision of a draft, through which
// door, and the digest of that revision's items. Mechanical marks a Check
// again that took live values with no pick, which makes no author.
type DraftRevisionRow struct {
	Revision     int
	Author       DraftActor
	Door, Digest string
	Mechanical   bool
	CreatedAt    time.Time
}

// DraftChangeRow is the published before and after of one object.
type DraftChangeRow struct {
	Seq                           int
	Kind, Name                    string
	Implied                       bool
	BeforeOp, BeforeDoc, BeforeFP string
	AfterOp, AfterDoc, AfterFP    string
}

// DraftFilter narrows List. Every set field must hold, and the zero value
// lists every draft. A SlotPrefix that ends in a colon, such as policy:,
// matches every slot of that kind, and one that names a whole slot, such as
// policy:guard, matches that slot only. Object matches a draft whose current
// items name it, by kind, name or both.
type DraftFilter struct {
	State, Door, ProposerID, AuthorID, Source, SlotPrefix string
	Object                                                ObjectRef
}

// DraftRevise is one new revision of an open draft. Items is the whole item
// list. An item that names an object the stored revision already holds and
// stamped keeps its stored Base, BaseOp and BaseDoc whatever Items says,
// unless Rebase is set, and every other item takes the base columns Items
// gives, BaseOp empty when the caller did not stamp it. An item the stored
// revision held keeps its stored Offered too. Note and ExpiresAt nil keep
// the stored values. Checked, when set, records the check of the new
// revision in the same transaction, and every item must then be stamped.
type DraftRevise struct {
	From      int
	Items     []DraftItemRow
	Rev       DraftRevisionRow
	Note      *string
	ExpiresAt *time.Time
	Rebase    bool
	Checked   *DraftCheck
}

// DraftCheck is the server's check of one revision: the snapshot it read,
// the JSON counts of its verdict, and the verdict an agent reads, empty for
// a draft no agent wrote.
type DraftCheck struct {
	Snapshot, Counts, AgentVerdict string
	At                             time.Time
}

// FileDecision is one decided draft that can move a server's link to an
// apps directory file: a published or discarded draft of the
// apps-directory door, or a published draft of any door that removes a
// server. Items are its App items with Doc left empty, and DecidedBy is who
// published or discarded it.
type FileDecision struct {
	DraftID   int64
	Door      string
	State     string
	Source    string
	DecidedAt time.Time
	DecidedBy DraftActor
	Items     []DraftItemRow
}

// LiveState is the config every replica runs, read in one read-only
// transaction so that its parts belong to one moment: the config
// generation, the active snapshot, every live server row, every access row
// the gateway holds with its role and server names, every role name, and
// every implication edge by role name. RoleIDs holds each role's id by its
// name, so a role removed and created again under its name reads as another
// role. Snapshot.Blob is empty when the active id equals the caller's live
// one, and Snapshot is zero when no snapshot is active. ReadAt is taken
// before the transaction begins, with the monotonic clock reading that
// time.Now gives, so a caller compares it with later times of this process.
type LiveState struct {
	Generation int64
	Snapshot   Snapshot
	Apps       []App
	Access     []LiveAccess
	Roles      []string
	RoleIDs    map[string]string
	Implies    map[string][]string
	ReadAt     time.Time
}

// LiveAccess is one access row as the gateway holds it.
type LiveAccess struct {
	ID, Role, App string
	Matchers      []string
}

// PublishPlan is a checked draft ready to publish: what the check saw and
// what to write. The server builds it after the verdict passed, the
// acknowledgments matched and the snapshot was built. Items holds the
// explicit items in draft order, then the implied ones. Generation and
// BaseSnapshot are what the World read, so that Publish can tell whether
// live state moved since.
type PublishPlan struct {
	DraftID  int64
	Revision int
	// New, when set, is the one-item draft of a direct route: Publish
	// inserts it first inside its transaction and publishes it, so a
	// refused or conflicting publish leaves no open draft behind. DraftID
	// and Revision are then ignored.
	New          *DraftNew
	Generation   int64
	BaseSnapshot string
	Snapshot     *Snapshot
	Items        []PlanItem
	// Close names the slots whose open draft this publish ends, with the
	// reason each draft.discard record gives.
	Close     []SlotClose
	Publisher DraftActor
	Acks      string
	// Checked, when set, is the check the publish ran, which the draft keeps
	// as the check of its revision. Nil leaves the stored check as it is.
	Checked *DraftCheck
	// Records turns what Publish wrote into the outbox rows of the publish.
	// It runs inside the transaction and must be pure: no store call, no
	// I/O. An error rolls everything back, and so does an empty list, since
	// the other replicas apply a publish from its records.
	Records func(PublishOutcome) ([]OutboxEvent, error)
}

// SlotClose is an open draft a publish ends, found by its slot.
type SlotClose struct{ Slot, Reason string }

// PlanItem is one object to write. Base, BaseOp and BaseDoc are the object
// as the check saw it, After and AfterDoc as the publish leaves it. For put
// and off exactly the field of the item's kind is set: App (Name, Version,
// Manifest as JSON, RuntimeKind, Source), Role, or Policy (Name, Priority,
// YAMLSource). An implied item is a Role put or remove that a removal
// causes, and Publish writes an implied put through that removal only.
type PlanItem struct {
	Ref                   ObjectRef
	Op                    string
	Implied               bool
	Base, BaseOp, BaseDoc string
	After, AfterDoc       string
	App                   *App
	Role                  *RoleConfig
	Policy                *PolicySet
}

// DraftNew is a draft Publish creates and publishes in one transaction.
type DraftNew struct {
	Row   DraftRow
	Items []DraftItemRow
	Rev   DraftRevisionRow
}

// PublishOutcome is what Publish wrote, the input of the records. Snapshot
// is the active snapshot after the publish. Items holds one outcome per
// plan item that changed its object, in plan order, and Closed lists the
// drafts the publish ended through plan.Close.
type PublishOutcome struct {
	DraftID  int64
	Snapshot string
	Items    []ItemOutcome
	Closed   []ClosedDraft
}

// ClosedDraft is a draft a publish ended, with its revision then and the
// reason given.
type ClosedDraft struct {
	ID       int64
	Revision int
	Reason   string
}

// ItemOutcome is what one plan item wrote. ID is the object's row id. For
// an App, AdminRole is the server's admin role, minted when Created, and on
// a remove the admin role that went. Ended lists the direct memberships
// that ended with every role a removal deleted, and Removed those roles. An
// App remove also names in LostAccess the roles whose access row on the
// server went. An App remove, and an App put that revives a removed row,
// count the credential rows they deleted. A Role put names the
// access rows it replaced and the implied roles it added and dropped, and
// an implied Role put names the removed roles it no longer implies. A
// PolicySet says whether its stored text changed and whether it was and is
// on.
type ItemOutcome struct {
	Ref                            ObjectRef
	Op                             string
	Implied, Created               bool
	ID                             string
	AdminRole                      Role
	Ended                          []RoleAssignment
	Removed                        []Role
	LostAccess                     []string
	Credentials                    int
	BindingsRemoved, BindingsAdded []ToolBinding
	ImpliesAdded, ImpliesRemoved   []string
	TextChanged, WasOn, On         bool
}

// PublishResult is the outcome of a committed publish and the number of
// outbox rows it inserted.
type PublishResult struct {
	PublishOutcome
	Records int
}

// PublishConflict says why Publish wrote nothing: the draft is not open at
// the plan's revision, another publish moved the config generation, the
// active snapshot is not the plan's base, an object's fingerprint or what
// goes with a removal moved, or the database turned the transaction away
// as a deadlock or a serialization failure. It wraps ErrConflict. Snapshot
// names the active snapshot when it is not the plan's base, empty when no
// snapshot is active, and Object names the object that moved.
type PublishConflict struct {
	Draft      bool
	Generation bool
	Snapshot   string
	Object     ObjectRef
	Cascade    bool
	Busy       bool
}

// PolicyMark is the config generation and a digest of every policy_sets
// row's id, status, updated_at and text length. A publish moves the
// generation, and any save by any replica moves the digest, whatever that
// replica's clock says. Digest is never empty, so the zero PolicyMark
// matches no store.
type PolicyMark struct {
	Generation int64
	Digest     string
}

// PolicyState is what the policy conversion reads in one read-only
// transaction: its mark, the active snapshot with its blob, zero when none
// is active, and every policy_sets row.
type PolicyState struct {
	Mark     PolicyMark
	Snapshot Snapshot
	Rows     []PolicySet
}

// PolicySettle is one set's step of the upgrade conversion. Generation is
// the config generation its PolicyState read, and Snapshot the id of the
// active snapshot it read, "" when none was active. Was is the row as read,
// nil when the set has no row. Status, Text and Priority are what the row
// must hold, Priority being the one the published text declares. Draft,
// Item and Rev are the saved-edit draft to create, nil Draft when there is
// none. Slot, when not zero, is the open slot draft at SlotRevision that
// takes Item as a new revision instead.
type PolicySettle struct {
	Generation   int64
	Snapshot     string
	Name         string
	Was          *PolicySet
	Status       string
	Text         string
	Priority     int
	Draft        *DraftRow
	Slot         int64
	SlotRevision int
	Item         DraftItemRow
	Rev          DraftRevisionRow
}

// DraftRepo stores config drafts, their revisions and their change record,
// and publishes a checked draft in one transaction.
type DraftRepo interface {
	// Create stores an open draft with its items and revision 1 in one
	// transaction and answers the row with its id. The proposer is
	// rev.Author, and a d.Proposer set to anyone else is refused with
	// nothing stored. Revision 1 comes in through d.Door, and the items are
	// stored in the order given. A draft whose items the caller stamped
	// carries CheckedRevision 1 and its CheckCounts, and every item must
	// then be stamped. ErrConflict when its slot, or its source and source
	// hash, already hold an open draft.
	Create(ctx context.Context, d DraftRow, items []DraftItemRow, rev DraftRevisionRow) (DraftRow, error)
	// Get answers the draft and its items in seq order, ErrNotFound for an
	// unknown id.
	Get(ctx context.Context, id int64) (DraftRow, []DraftItemRow, error)
	// Revisions answers who wrote each revision, oldest first.
	Revisions(ctx context.Context, id int64) ([]DraftRevisionRow, error)
	// RevisionsOf answers who wrote each revision of several drafts in one
	// read, by draft id, oldest first, and nothing for an unknown id.
	RevisionsOf(ctx context.Context, ids []int64) (map[int64][]DraftRevisionRow, error)
	// Changes answers a published draft's change record in seq order.
	Changes(ctx context.Context, id int64) ([]DraftChangeRow, error)
	// List answers up to limit drafts with an id below before, 0 meaning
	// from the newest, newest first.
	List(ctx context.Context, f DraftFilter, before int64, limit int) ([]DraftRow, error)
	// Items answers the items of several drafts in one read, by draft id.
	Items(ctx context.Context, ids []int64) (map[int64][]DraftItemRow, error)
	// CountOpen counts the open drafts whose proposer is proposerID and
	// whose slot is empty, so a working draft and a saved policy edit never
	// count against the limit.
	CountOpen(ctx context.Context, proposerID string) (int, error)
	// Revise writes revision From+1 of an open draft at From in one
	// transaction: the items replaced under the base rule of DraftRevise,
	// the revision row added, note, expiry and check updated when given.
	// ErrConflict, with the draft as it stands, when the draft is not open
	// or not at From, ErrNotFound for an unknown id, and nothing is written.
	Revise(ctx context.Context, id int64, r DraftRevise) (DraftRow, error)
	// Stamp records the check of revision: the base columns of the items
	// whose BaseOp is empty, from bases matched by kind and name, and the
	// check fields. Items already stamped keep their bases. It reports false
	// and writes nothing when the draft is not open, is past revision, or a
	// check of revision landed first. A base missing for an item not yet
	// stamped is an error, and nothing is written.
	Stamp(ctx context.Context, id int64, revision int, bases []DraftItemRow, c DraftCheck) (bool, error)
	// Close ends an open draft as discarded or expired and reports whether
	// this call ended it. revision 0 matches any revision.
	Close(ctx context.Context, id int64, revision int, state string, by DraftActor, reason string, at time.Time) (bool, error)
	// BySlot answers the open draft of slot with its items, ErrNotFound
	// when none.
	BySlot(ctx context.Context, slot string) (DraftRow, []DraftItemRow, error)
	// ListUnchecked answers up to limit open drafts whose current revision
	// the server has not checked, oldest first.
	ListUnchecked(ctx context.Context, limit int) ([]DraftRow, error)
	// ListExpirable answers up to limit open drafts whose expiry is before now.
	ListExpirable(ctx context.Context, now time.Time, limit int) ([]DraftRow, error)
	// LatestChange answers the newest published draft whose change record
	// names ref, ErrNotFound when none.
	LatestChange(ctx context.Context, ref ObjectRef) (DraftRow, error)
	// Generation answers the config generation, the one row every publish
	// moves.
	Generation(ctx context.Context) (int64, error)
	// Publish applies plan in one transaction or not at all. It takes the
	// publish lock, checks the generation, the active snapshot, the draft,
	// every item's live fingerprint and what goes with each removal, writes
	// every row and activates plan.Snapshot, reads every object back against
	// its After, ends the slot drafts of plan.Close, keeps the before and
	// after of every change, inserts the outbox rows of plan.Records and
	// marks the draft published with plan.Checked as the check of its
	// revision when set. A PublishConflict says why nothing was
	// written. Publish runs every statement on its own transaction. On sqlite
	// that transaction holds the process's only connection, so a pool call
	// made while it is open waits for it, and one made from inside it waits
	// until its context ends. Records is called inside the transaction and
	// must not touch the store.
	Publish(ctx context.Context, plan PublishPlan) (PublishResult, error)
	// SettlePolicy is one set's step of the upgrade conversion. Under the
	// publish lock, and only while the generation is still s.Generation and
	// the active snapshot still s.Snapshot, it creates or revises the
	// saved-edit draft and writes the row, both or neither. It reports false
	// and writes nothing when the generation, the active snapshot, the row or
	// the slot draft moved since the read, when its insert meets a row of the
	// name, or when the row already holds Status, Text and Priority.
	SettlePolicy(ctx context.Context, s PolicySettle) (bool, error)
	// PolicyMark answers the config generation and a digest of every
	// policy_sets row in one statement, the cheap test of whether the policy
	// conversion has anything to settle.
	PolicyMark(ctx context.Context) (PolicyMark, error)
	// PolicyState reads what the policy conversion needs in one read-only
	// transaction: the mark, the active snapshot with its blob and every
	// policy_sets row.
	PolicyState(ctx context.Context) (PolicyState, error)
	// LiveState reads the config every replica runs in one read-only
	// transaction, the snapshot blob only when the active id is not
	// liveSnapshot.
	LiveState(ctx context.Context, liveSnapshot string) (LiveState, error)
	// SetOffered records on the item ref of the open draft id at revision
	// the tool names a Contact read, as offered, and writes nothing else.
	// ErrConflict when the draft is past revision or not open, and
	// ErrNotFound for an unknown draft or an item it does not hold.
	SetOffered(ctx context.Context, id int64, revision int, ref ObjectRef, offered string) error
	// BySource answers every draft proposed from the apps directory file
	// source, in any state, newest first, and none for an empty source.
	BySource(ctx context.Context, source string) ([]DraftRow, error)
	// FileDecisions answers every published or discarded draft of the
	// apps-directory door that holds an App item, and every published draft
	// of any door that removes an App, oldest decision first.
	FileDecisions(ctx context.Context) ([]FileDecision, error)
}
