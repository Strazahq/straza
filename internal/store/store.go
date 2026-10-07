// Package store is the persistence seam for strazad.
//
// One Store interface, two database/sql drivers behind it: pure-Go sqlite
// (standalone) and pgx (enterprise). Queries are a single hand-written corpus
// using $n placeholders, and the sqlite dialect rewrites them to ?. Schema
// changes travel exclusively as embedded migrations. Request paths never
// read the DB: this interface exists for the control plane only.
package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/strazahq/straza/internal/config"
)

// Sentinel errors. Driver-specific failures are mapped onto these.
var (
	ErrNotFound = errors.New("store: not found")
	ErrConflict = errors.New("store: conflict (duplicate key)")
	// ErrInvalidData is a row the database refuses on its values (Postgres
	// SQLSTATE class 22, data exception); a retry cannot help.
	ErrInvalidData = errors.New("store: invalid data (the database refuses the row's values)")
)

// Store is the control-plane persistence interface, grouped per aggregate.
type Store interface {
	Ping(ctx context.Context) error
	Migrate(ctx context.Context) error
	Close() error

	Users() UserRepo
	Roles() RoleRepo
	Devices() DeviceRepo
	Sessions() SessionRepo
	Packs() PackRepo
	Apps() AppRepo
	ToolBindings() ToolBindingRepo
	Credentials() CredentialRepo
	Policies() PolicyRepo
	Snapshots() SnapshotRepo
	Outbox() OutboxRepo
	Audit() AuditRepo
	Revocations() RevocationRepo
	SigningKeys() SigningKeyRepo
	Settings() SettingsRepo
	AttestationHashes() AttestationRepo
	Conversations() ConversationRepo
	Approvals() ApprovalRepo
	Approvers() ApproverRepo
	Drafts() DraftRepo
}

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
	List(ctx context.Context) ([]User, error)
	// Page returns limit users after the cursor in the order srt names (the
	// zero Sort is newest first), skipping soft-deleted rows like List,
	// narrowed server-side by f (see UserFilter for semantics).
	Page(ctx context.Context, f UserFilter, srt Sort, c Cursor, limit int) ([]User, error)
	// SponsoredCounts counts the agents each named user sponsors, in one query.
	SponsoredCounts(ctx context.Context, usernames []string) (map[string]int, error)
	Update(ctx context.Context, u User) (User, error)
	// UpdateFields writes only the columns f names on the live user id and
	// answers the row read back, so writes of different columns never undo
	// each other. An f that names none writes nothing and answers the row.
	UpdateFields(ctx context.Context, id string, f UserFields) (User, error)
	// SoftDelete retires the user and ends every role grant it holds in one
	// transaction, returning the removed assignment rows so the caller can
	// audit each grant. ErrNotFound leaves every row untouched.
	SoftDelete(ctx context.Context, id string) ([]RoleAssignment, error)
}

// RoleRepo manages roles, implication edges, and assignments.
type RoleRepo interface {
	Create(ctx context.Context, r Role) (Role, error)
	GetByID(ctx context.Context, id string) (Role, error)
	GetByName(ctx context.Context, name string) (Role, error)
	List(ctx context.Context) ([]Role, error)
	Update(ctx context.Context, r Role) (Role, error)
	Delete(ctx context.Context, id string) error
	// CreateOwned creates a server-owned role and its one binding to the
	// owning server in one transaction, so a refused binding leaves no role.
	CreateOwned(ctx context.Context, r Role, toolMatcher string) (Role, ToolBinding, error)
	// ListByOwner lists the roles one server owns, by name.
	ListByOwner(ctx context.Context, appID string) ([]Role, error)
	// HolderCount counts the subjects holding the role through any path:
	// its own assignments and those of every role whose implication closure
	// reaches it, validity windows included, each subject once.
	HolderCount(ctx context.Context, roleID string) (int, error)

	AddImplication(ctx context.Context, roleID, impliesRoleID string) error
	RemoveImplication(ctx context.Context, roleID, impliesRoleID string) error
	ListImplications(ctx context.Context) ([]RoleImplication, error)

	Assign(ctx context.Context, a RoleAssignment) (RoleAssignment, error)
	Unassign(ctx context.Context, assignmentID string) error
	// ApplyMembership changes who holds roleID in one transaction: it
	// deletes the rows of that role named in removeIDs and inserts add, and
	// reports the rows it really deleted and inserted. A row already gone,
	// or a subject that already holds the role, is skipped rather than
	// refused, so a concurrent write never fails the call. Any other
	// failure rolls back all of it.
	ApplyMembership(ctx context.Context, roleID string, add []RoleAssignment, removeIDs []string) (added, removed []RoleAssignment, err error)
	// Assignment resolves one assignment row; the delete handler names the
	// affected SUBJECT in its identity event, not the assignment row id.
	Assignment(ctx context.Context, assignmentID string) (RoleAssignment, error)
	ListAssignments(ctx context.Context, subjectKind, subjectID string) ([]RoleAssignment, error)
	AssignmentsByRole(ctx context.Context, roleID string) ([]RoleAssignment, error)
	ListAllAssignments(ctx context.Context) ([]RoleAssignment, error)
	// AssignmentCountsByRole returns assignment ROWS per role id in one
	// grouped query (the roles list's Assigned column at directory scale).
	// Windows are included: the number mirrors the assignment list an admin
	// sees and can revoke, not effective validity. Zero-assignment roles are
	// absent from the map.
	AssignmentCountsByRole(ctx context.Context) (map[string]int, error)
	// AssignmentPairsInForce returns the (role, subject) pair of every row
	// in force at t (from inclusive, to exclusive), one flat read.
	AssignmentPairsInForce(ctx context.Context, t time.Time) ([]AssignmentPair, error)
}

// DeviceRepo manages enrolled devices.
type DeviceRepo interface {
	Create(ctx context.Context, d Device) (Device, error)
	GetByID(ctx context.Context, id string) (Device, error)
	ListByUser(ctx context.Context, userID string) ([]Device, error)
	Update(ctx context.Context, d Device) (Device, error)
	Delete(ctx context.Context, id string) error
}

// SessionRepo manages sessions (first-class principals).
type SessionRepo interface {
	Create(ctx context.Context, s Session) (Session, error)
	GetByID(ctx context.Context, id string) (Session, error)
	List(ctx context.Context, status string) ([]Session, error) // status "" = all
	ListByUser(ctx context.Context, userID string) ([]Session, error)
	// Page is the keyset window over sessions in the order srt names,
	// optionally narrowed by status and/or user; c is the row it continues after.
	Page(ctx context.Context, status, userID string, srt Sort, c Cursor, limit int) ([]Session, error)
	// LastSeenByUsers returns each listed user's most recent session
	// last_seen in ONE query, never a per-row lookup. Users with no sessions
	// are absent from the map.
	LastSeenByUsers(ctx context.Context, userIDs []string) (map[string]time.Time, error)
	Touch(ctx context.Context, id string, at time.Time) error
	SetStatus(ctx context.Context, id, status string) error
	// SetStatusIfChanged sets the status and reports whether the row actually
	// transitioned; false when it already carried the target status. The
	// idempotent-replay signal: revoke handlers fire their side effects
	// (revocation row, control event, push) only on true, so replaying a
	// revoke with a still-valid token cannot amplify writes. ErrNotFound when
	// no such session exists.
	SetStatusIfChanged(ctx context.Context, id, status string) (bool, error)
	// CloseIdle bulk-closes active sessions not seen since cutoff (their
	// tokens expired and can never refresh); returns the closed (id, user_id)
	// pairs so the janitor can emit one authn session.end event per session
	// (spec/events rev 21) instead of an unattributable count.
	CloseIdle(ctx context.Context, cutoff time.Time) ([]ClosedSession, error)
	// CloseStartedBefore bulk-closes active sessions started before cutoff:
	// the absolute session lifetime, which ends a session that is still
	// refreshing. Returns the same (id, user_id) pairs as CloseIdle so the
	// janitor emits one authn session.end event per session. Revoked rows
	// keep their status.
	CloseStartedBefore(ctx context.Context, cutoff time.Time) ([]ClosedSession, error)
}

// ClosedSession is one CloseIdle or CloseStartedBefore casualty: just enough
// identity for the janitor's per-session event, deliberately not a full
// Session row.
type ClosedSession struct {
	ID     string
	UserID string
}

// PackRepo manages knowledge packs and role bindings.
type PackRepo interface {
	Create(ctx context.Context, p KnowledgePack) (KnowledgePack, error)
	GetByID(ctx context.Context, id string) (KnowledgePack, error)
	GetByName(ctx context.Context, name string) (KnowledgePack, error)
	List(ctx context.Context) ([]KnowledgePack, error)
	Update(ctx context.Context, p KnowledgePack) (KnowledgePack, error)
	Delete(ctx context.Context, id string) error
	Bind(ctx context.Context, roleID, packID string) error
	Unbind(ctx context.Context, roleID, packID string) error
	ForRoles(ctx context.Context, roleIDs []string) ([]KnowledgePack, error)
	// ListBindings reads every pack-role binding with the role name joined
	// in, one query for the whole packs list (admin read model).
	ListBindings(ctx context.Context) ([]PackBinding, error)
}

// OutboxRepo is the producer side of the event spine.
type OutboxRepo interface {
	Insert(ctx context.Context, e OutboxEvent) (OutboxEvent, error)
	// InsertBatch stores a whole accepted ingest batch in one transaction,
	// preserving submission order for the drain. All-or-nothing: on error no
	// row landed and the caller answers retryable, never partial-accept.
	InsertBatch(ctx context.Context, events []OutboxEvent) (int, error)
	ListUnpublished(ctx context.Context, limit int) ([]OutboxEvent, error)
	// ListRecent returns the newest rows regardless of published state,
	// newest first: the observation seam (tests, diagnostics). Observing
	// emits through ListUnpublished races the relay, which marks rows
	// published on its own schedule; an emitted event must stay observable
	// after delivery.
	ListRecent(ctx context.Context, limit int) ([]OutboxEvent, error)
	// ListUnpublishedControl returns only unpublished CONTROL-plane events
	// (every subject outside straza.audit.>: revocations, policy, identity,
	// apps), oldest first. The relay drains these ahead of bulk audit and
	// capture so a stalled or full audit stream can never head-of-line block
	// the kill switch.
	ListUnpublishedControl(ctx context.Context, limit int) ([]OutboxEvent, error)
	// DrainClaimed is the relay's drain: claim up to limit rows
	// (FOR UPDATE SKIP LOCKED on Postgres, so pods take disjoint sets and
	// SHARE the drain), publish each, mark the successes, one transaction.
	// Returns the published count and any publish error (already-published
	// rows stay marked either way).
	DrainClaimed(ctx context.Context, controlOnly bool, limit int, publish func(OutboxEvent) error) (int, error)
	// CountUnpublished counts unpublished rows up to ceil (bounded scan:
	// the ingest backpressure gate needs "over the limit?", never the true
	// depth of a huge backlog).
	CountUnpublished(ctx context.Context, ceil int) (int, error)
	// PruneBulkPublished deletes PUBLISHED bulk rows (straza.audit.>) older
	// than cutoff, in batches: the chain and read models hold the records, and
	// control rows stay forever because they are the change feed.
	PruneBulkPublished(ctx context.Context, cutoff time.Time) (int64, error)
	MarkPublished(ctx context.Context, ids []string) error
	IncAttempts(ctx context.Context, id string) error
	// ListAfter pages events with id > after (uuidv7 ids are time-ordered),
	// optionally narrowed to subjects, oldest first: the admin change feed
	// (IGA liveSync) reads the outbox as history; rows are never deleted.
	ListAfter(ctx context.Context, after string, subjects []string, limit int) ([]OutboxEvent, error)
	// Head is the newest outbox id ("" when empty): the feed's fast-forward
	// point for first-time consumers (sync-from-now + one reconciliation).
	Head(ctx context.Context) (string, error)
}

// AuditRepo is the append-only hash-chained audit mirror. ceID is the
// CloudEvent id, uniquely indexed so the consumer can dedupe at-least-once
// redeliveries.
type AuditRepo interface {
	Append(ctx context.Context, ceID, ce, prevHash, hash string) (AuditRecord, error)
	ExistsCE(ctx context.Context, ceID string) (bool, error)
	Last(ctx context.Context) (AuditRecord, error)
	// LastHash returns the newest chain row's seq and hash WITHOUT reading
	// the CE body, which Last() drags across the wire on every append.
	LastHash(ctx context.Context) (int64, string, error)
	// AppendChained appends a deduped batch to the chain in one transaction
	// under the store's single-writer guarantee (Postgres advisory
	// lock / SQLite single connection), the batching that lifts the chain
	// ceiling without giving up linearity. genesis and link come from
	// internal/audit.
	AppendChained(ctx context.Context, events []ChainEvent, genesis string, link func(prevHash, ce string) string) (int, error)
	List(ctx context.Context, afterSeq int64, limit int) ([]AuditRecord, error)
	// ListRecent returns the newest rows newest-first: the browsing seam
	// behind `strazactl audit tail` (outbox ListRecent precedent). Chain
	// verification must NOT use it: verify pages List ascending so the
	// hash chain links check in order.
	ListRecent(ctx context.Context, limit int) ([]AuditRecord, error)
	// ListFiltered / ListRecentFiltered are List / ListRecent with an
	// AuditFilter applied in SQL before the limit, so a page is a page of
	// matches and the seq cursor walks matches (the console archaeology
	// seam; a full-chain scan bounded by LIMIT is the accepted cost on
	// this admin surface).
	ListFiltered(ctx context.Context, f AuditFilter, afterSeq int64, limit int) ([]AuditRecord, error)
	ListRecentFiltered(ctx context.Context, f AuditFilter, limit int) ([]AuditRecord, error)
	// ListSince pages rows written at or after since, ascending by seq: the
	// overview decision window. No type column, so the caller filters the CE.
	ListSince(ctx context.Context, since time.Time, afterSeq int64, limit int) ([]AuditRecord, error)
}

// AuditFilter narrows chain reads server-side. Q is a case-insensitive
// substring over the raw CE text (LIKE metacharacters in the needle mean
// themselves); Effect narrows to records whose data.effect equals it
// ("allow"|"deny"), and records carrying no decision effect are excluded
// whenever it is set. The zero value narrows nothing.
type AuditFilter struct {
	Q      string
	Effect string
	// UserID and Username narrow the page to one principal's records: rows
	// whose data.user, data.userId or data.actorId equals UserID, or whose
	// data.user or data.actor equals Username. Both empty means no filter.
	UserID   string
	Username string
}

// ConversationRepo is the conversation-capture read model: captured turns,
// written by the spine consumer, read by the admin transcript/search
// surfaces, purged by the retention janitor. Insert dedupes by CE id
// (ErrConflict).
type ConversationRepo interface {
	Insert(ctx context.Context, t ConversationTurn) (ConversationTurn, error)
	// InsertBatch stores a batch of turns in one transaction: one
	// multi-row insert (already-stored CE ids filtered in-tx) plus the
	// per-session summary upserts that keep the Transcripts inbox O(sessions
	// shown) instead of a GROUP BY over every turn. Returns rows inserted.
	InsertBatch(ctx context.Context, turns []ConversationTurn) (int, error)
	ListBySession(ctx context.Context, sessionID string, limit int) ([]ConversationTurn, error)
	ListRecent(ctx context.Context, userID string, limit int) ([]ConversationTurn, error)
	ListConversations(ctx context.Context, limit int) ([]ConversationSummary, error)
	Search(ctx context.Context, q ConversationSearch) ([]ConversationTurn, error)
	PurgeBefore(ctx context.Context, cutoff time.Time) (int64, error)
	// StorageBytes reports the bytes transcript capture occupies, for the
	// retention janitor's watermark check (never a request path).
	// Dialect-honest rather than dialect-identical: Postgres answers
	// pg_total_relation_size of conversation_turns (heap + indexes +
	// toast, the dominant grower under capture). SQLite answers the whole
	// database file via page math (per-table needs dbstat, and on the
	// single-file standalone store the file IS the signal that the disk is
	// filling).
	StorageBytes(ctx context.Context) (int64, error)
}

// ConversationSearch is the leak-hunt query: substring scan over stored
// content and/or an exact full-content hash match; both empty = no results.
type ConversationSearch struct {
	Substring   string // matches content (driver-native LIKE semantics)
	ContentHash string // exact match against content_hash
	UserID      string // optional narrowing
	Limit       int
}

// ConversationSummary is one captured session's inbox row (the console
// Transcripts landing): who talked, when, how much, and a preview of the
// latest turn.
type ConversationSummary struct {
	SessionID string
	UserID    string
	Turns     int
	FirstAt   time.Time
	LastAt    time.Time
	Preview   string
}

// ApprovalRepo manages human-approval records. Writes happen on the
// escalation lane, not the fast PDP path.
type ApprovalRepo interface {
	Insert(ctx context.Context, a Approval) (Approval, error)
	GetByID(ctx context.Context, id string) (Approval, error)
	// List returns records by state ("" = all, bounded newest-first; a concrete
	// state like "pending" narrows to that state).
	List(ctx context.Context, state string) ([]Approval, error)
	// ListBefore is the keyset window backing approver-history pagination: the
	// newest `limit` records with id < beforeID (empty beforeID = newest page),
	// ordered strictly DESC by the UUIDv7 id. It has NO 500-row wall (each page
	// is limit-bounded), so paging can reach arbitrarily far back.
	ListBefore(ctx context.Context, beforeID string, limit int) ([]Approval, error)
	// PageByState is ListBefore with the admin state filter: state ""
	// = all states; same id-keyset window, no row wall.
	PageByState(ctx context.Context, state, beforeID string, limit int) ([]Approval, error)
	// CountByUser counts the approvals a user raised (per-user stats, backed
	// by idx_approvals_user).
	CountByUser(ctx context.Context, userID string) (int, error)
	// FindPendingByKey resolves the live pending record for a dedupe tuple
	// (ErrNotFound when none), the in-pod side of the partial unique index.
	FindPendingByKey(ctx context.Context, sessionID, ruleID, argvHash string) (Approval, error)
	// FindOpenTicketByUser resolves the newest pending TICKET for (userID,
	// ruleID, argvHash) (ErrNotFound when none), the user-scoped dedupe that
	// keeps a fresh session from re-raising a ticket an earlier session already
	// put in front of a human. Tickets are user-scoped like the grants they
	// produce; holds stay session-scoped and are never returned here.
	FindOpenTicketByUser(ctx context.Context, userID, ruleID, argvHash string) (Approval, error)
	// MarkDecided is the one-time cross-pod gate: it transitions exactly one
	// pending row to a terminal verdict and reports whether THIS caller won the
	// race (false = already resolved/expired by someone else). grantExpiresAt,
	// when non-nil (an approve: decidedAt + grantTTLSeconds for a ticket,
	// decidedAt + retryTTLSeconds for a hold), is stamped into grant_expires_at
	// in the same UPDATE so the use window materializes atomically with the
	// flip; nil leaves the column untouched (a denial).
	// reason and deviceID are the decided-attribution pair:
	// the decider's own words and the enrolled device whose key signed the
	// decision, both "" when absent (console/Slack lanes, legacy callers).
	MarkDecided(ctx context.Context, id, state, decidedBy, decidedByName, channel string, at time.Time, grantExpiresAt *time.Time, reason, deviceID string) (bool, error)
	// MarkConsumed is the atomic single-use gate for every approval use, a
	// ticket's grant and a hold's approval alike: it consumes exactly one live
	// approved row (approved, unconsumed, grant_expires_at > asOf) and reports
	// whether THIS caller won the race, on any replica. The use window is
	// judged at asOf, which is now for every caller except the held call, and
	// consumed_at records now. consumedBy records the consuming session. A row
	// with no grant_expires_at is never matched.
	MarkConsumed(ctx context.Context, id, consumedBy string, now, asOf time.Time) (bool, error)
	// FindConsumableHold resolves the approved, unused hold of (userID,
	// sessionID, ruleID, argvHash) whose use deadline is still ahead, the
	// candidate for a retry of the held call. A hold stays session-bound.
	// ErrNotFound when none; the use itself is MarkConsumed by id.
	FindConsumableHold(ctx context.Context, userID, sessionID, ruleID, argvHash string, now time.Time) (Approval, error)
	// FindConsumableGrant resolves the live ticket grant bound to (userID,
	// argvHash) for the retry/plan-gate path: an approved, unconsumed ticket
	// still inside its consume window. Grants follow the requester + fingerprint
	// (session-agnostic), so a fresh session can find one an earlier session
	// raised. ErrNotFound when none.
	FindConsumableGrant(ctx context.Context, userID, argvHash string, now time.Time) (Approval, error)
	// FindLatestTicketByKey returns the most recent ticket-class row for
	// (userID, ruleID, argvHash) regardless of state (ErrNotFound when none);
	// the terminal deny-final lookup: a still-in-window denial blocks a fresh
	// call rather than opening a new ticket.
	FindLatestTicketByKey(ctx context.Context, userID, ruleID, argvHash string) (Approval, error)
	// ListExpirable returns pending records already past ExpiresAt (sweep input).
	ListExpirable(ctx context.Context, now time.Time, limit int) ([]Approval, error)
	// ClaimExpired atomically flips one still-pending, past-expiry row to
	// expired; the bool reports whether this caller claimed it.
	ClaimExpired(ctx context.Context, id string, now time.Time) (bool, error)
	// ListTicketsForReminder returns pending, ticket-class rows whose expiry
	// falls in (now, dueBefore] and that have NOT yet been reminded (the
	// reminder_pushed_at marker is absent from channel_refs), the near-expiry
	// reminder sweep input. Ordered by expiry ascending, and limit <= 0 falls back
	// to 256. Background sweep only, never a request path. The marker-absent
	// filter keeps an already-reminded row out of every subsequent scan.
	ListTicketsForReminder(ctx context.Context, now, dueBefore time.Time, limit int) ([]Approval, error)
	// ClaimTicketReminder atomically records that the single near-expiry reminder
	// push has fired for one pending, not-yet-reminded ticket: it writes refs
	// (the row's channel_refs map extended with reminder_pushed_at) under the
	// still-pending + marker-absent guard. RowsAffected == 1 means THIS caller won
	// the single reminder (the same exactly-once discipline as ClaimExpired).
	ClaimTicketReminder(ctx context.Context, id string, refs map[string]string) (bool, error)
	// PurgeBefore deletes terminal (non-pending) records created before cutoff.
	PurgeBefore(ctx context.Context, cutoff time.Time) (int64, error)
	// SetChannelRefs merges notifier back-references (e.g. slack_ts) onto a
	// record without disturbing its state.
	SetChannelRefs(ctx context.Context, id string, refs map[string]string) error
}

// ApproverRepo manages the mobile approver surface: enrolled approver
// devices, one-time enroll tokens,
// single-use decision challenges, and push registrations. All control plane
// (enroll/decide ride the sanctioned slow lane, never the fast PDP path).
type ApproverRepo interface {
	// InsertDevice registers an approver device (ID is assigned with the apd_
	// prefix when empty).
	InsertDevice(ctx context.Context, d ApproverDevice) (ApproverDevice, error)
	GetDevice(ctx context.Context, id string) (ApproverDevice, error)
	// ListDevices returns enrolled approver devices in enrolment order, each
	// with its push-registration count; userID "" lists every user's. The
	// discoverability half of the device kill switch: DeleteDevice needs the
	// apd_ id, and the enroll response that carried it went to the phone.
	ListDevices(ctx context.Context, userID string) ([]ApproverDeviceInfo, error)
	// DeleteDevice removes a device row (revocation). ErrNotFound when absent.
	DeleteDevice(ctx context.Context, id string) error
	// TouchDevice updates last_seen_at (best-effort liveness; ignores absence).
	TouchDevice(ctx context.Context, id string, at time.Time) error

	InsertEnrollToken(ctx context.Context, t ApproverEnrollToken) (ApproverEnrollToken, error)
	// ConsumeEnrollToken atomically marks an unused, unexpired token used and
	// returns it (ErrNotFound when missing/expired/already used); one-time.
	ConsumeEnrollToken(ctx context.Context, tokenHash string, now time.Time) (ApproverEnrollToken, error)
	// PurgeEnrollTokensBefore deletes tokens whose expiry is before cutoff.
	PurgeEnrollTokensBefore(ctx context.Context, cutoff time.Time) (int64, error)

	InsertChallenge(ctx context.Context, c ApproverChallenge) error
	// ConsumeChallenge atomically single-use-consumes a challenge bound to the
	// given device and approval; the bool reports whether THIS caller claimed a
	// still-valid (unused, unexpired) row.
	ConsumeChallenge(ctx context.Context, challenge, deviceID, approvalID string, now time.Time) (bool, error)
	// PurgeChallengesBefore deletes challenges whose expiry is before cutoff.
	PurgeChallengesBefore(ctx context.Context, cutoff time.Time) (int64, error)

	// UpsertPush stores a push registration, deduping on
	// (device_id, kind, token_or_endpoint). A dedupe hit refreshes the row's
	// RegisteredAt: the timestamp means "the app last confirmed this token"
	// (the guarded-prune input), not first-seen.
	UpsertPush(ctx context.Context, p ApproverPush) error
	// DeletePush removes one registration by its dedupe triple (idempotent).
	DeletePush(ctx context.Context, deviceID, kind, tokenOrEndpoint string) error
	// DeletePushBefore removes one registration by its dedupe triple iff its
	// RegisteredAt is not newer than notAfter (idempotent): the guarded prune
	// for upstream invalidations carrying a timestamp horizon (APNs 410). A
	// device that re-registered after the invalidation must keep ringing. The
	// bool reports whether a row was actually deleted.
	DeletePushBefore(ctx context.Context, deviceID, kind, tokenOrEndpoint string, notAfter time.Time) (bool, error)
	// ListPushTargets returns every push registration joined to the id of the
	// user who owns its device, the routing input for approval push delivery.
	// One row per registration; a registration whose device row is gone is
	// omitted (the inner join drops it, so a revoked device stops receiving).
	ListPushTargets(ctx context.Context) ([]ApproverPushTarget, error)
	// CountDevices reports how many approver devices are enrolled: the
	// channel-status surface's "enrolled devices vs push routes" diagnostic
	// (a device without a push registration decides but never rings).
	CountDevices(ctx context.Context) (int, error)
}

// ApproverPushTarget is one push registration joined to the id of the user who
// owns the device it belongs to, the routing input for approval push delivery
// (ApproverRepo.ListPushTargets). It embeds ApproverPush and adds UserID. Kept
// here beside the interface (not in types.go) so it sits with the query that
// produces it.
type ApproverPushTarget struct {
	ApproverPush
	UserID string
}

// ApproverDeviceInfo is one enrolled approver device joined with the count of
// its push registrations (ApproverRepo.ListDevices): a device without a push
// route decides but never rings, and the count is what attributes the
// channel-status "enrolled vs push routes" diagnostic to a specific phone.
// Kept beside the interface, like ApproverPushTarget, for the same reason.
type ApproverDeviceInfo struct {
	ApproverDevice
	PushRoutes int
}

// RevocationRepo is the denylist source of truth.
type RevocationRepo interface {
	Create(ctx context.Context, r Revocation) (Revocation, error)
	ListSince(ctx context.Context, t time.Time) ([]Revocation, error)
	List(ctx context.Context) ([]Revocation, error)
	ListByTarget(ctx context.Context, kind, targetID string) ([]Revocation, error)
	// ListByTargets batches one kind's rows for a page of targets into a
	// single IN query (the users list must never do per-row lock lookups);
	// targets with no rows are absent from the map.
	ListByTargets(ctx context.Context, kind string, targetIDs []string) (map[string][]Revocation, error)
	// Delete removes every revocation for one target (the admin lift,
	// enable/unlock): the boot-time denylist rebuild must stop re-blocking it.
	Delete(ctx context.Context, kind, targetID string) error
	// DeleteByOrigin removes one lane's rows only (the SCIM lift):
	// admin/external locks survive IdM writes.
	DeleteByOrigin(ctx context.Context, kind, targetID, origin string) error
}

// SigningKeyRepo manages the signing keys and their rotation.
type SigningKeyRepo interface {
	Create(ctx context.Context, k SigningKey) (SigningKey, error)
	Get(ctx context.Context, kid string) (SigningKey, error)
	ListByPurpose(ctx context.Context, purpose string) ([]SigningKey, error)
	SetStatus(ctx context.Context, kid, status string) error
	// Promote makes the staged key kid the active key of its purpose and
	// demotes the key that was active to retiring, in one transaction. It
	// reports whether this call made the change: a kid that is not staged
	// any more, or that a peer promoted first, changes nothing.
	Promote(ctx context.Context, kid string) (bool, error)
	// Retire moves the key kid from the status from to retired and reports
	// whether this call made the change, so that of two replicas retiring
	// the same key exactly one records it.
	Retire(ctx context.Context, kid, from string) (bool, error)
}

// AttestationRepo is the expected-hash registry for managed installs.
// Rows are allowed-set entries: a check-in claiming managed must
// present, for every artifact registered for its harness+platform, a hash
// from that artifact's allowed set.
type AttestationRepo interface {
	Create(ctx context.Context, h AttestationHash) (AttestationHash, error)
	List(ctx context.Context) ([]AttestationHash, error)
	Delete(ctx context.Context, id string) error
}

// SettingsRepo is a small key/value aggregate.
type SettingsRepo interface {
	Set(ctx context.Context, key, value string) error
	// SetIfAbsent writes the pair only when the key does not exist yet; an
	// existing value is never overwritten. Callers that need the winning
	// value (e.g. multi-pod first-boot identity) Get after calling.
	SetIfAbsent(ctx context.Context, key, value string) error
	// Update rewrites the value of a key that exists and answers ErrNotFound
	// for one that does not, so a writer that lost a race with Delete cannot
	// bring the row back.
	Update(ctx context.Context, key, value string) error
	Get(ctx context.Context, key string) (string, error)
	Delete(ctx context.Context, key string) error
	List(ctx context.Context) (map[string]string, error)
}

// Open constructs the Store selected by cfg. It does not run migrations;
// callers decide when to Migrate.
func Open(cfg config.Config) (Store, error) {
	switch cfg.Store.Driver {
	case config.DriverSQLite:
		return openSQLite(cfg.SQLitePath())
	case config.DriverPostgres:
		return openPostgres(cfg.Store.DSN)
	default:
		return nil, fmt.Errorf("store: unknown driver %q", cfg.Store.Driver)
	}
}
