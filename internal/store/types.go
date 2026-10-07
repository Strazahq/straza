package store

import "time"

// UserFilter narrows UserRepo.Page. The zero value narrows nothing. Q is a
// case-insensitive substring over username, email and external_id. RoleIDs
// matches users EFFECTIVELY holding any of the ids at Now: direct
// assignments whose validity window contains the instant (valid_from
// inclusive, valid_to exclusive, mirroring identity.assignmentValidAt).
// Implication closure is the CALLER's job: expand the target role to every
// role that implies it before passing ids, because the store does not read
// the implication graph here.
type UserFilter struct {
	Q       string
	Status  string
	Sponsor string // exact sponsor username; "" = any
	RoleIDs []string
	Now     time.Time // window-evaluation instant for RoleIDs; zero = now
}

// Enum values used across aggregates. Kept as plain strings in the DB; the
// CHECK constraints in the migrations are the source of truth for legality.
const (
	UserActive   = "active"
	UserDisabled = "disabled"

	OriginLocal = "local"
	OriginSCIM  = "scim"
	OriginAdmin = "admin"

	// SubjectUser is the only assignment subject, and the CHECK on the
	// column allows no other.
	SubjectUser = "user"

	// Role kinds on the access plane. business composes application roles
	// (what people request and hold); application binds tools (what agents
	// hold); approver grants decide authority ONLY: it
	// never binds tools or packs, never implies or is implied, and is the
	// only kind (besides straza-admin by name) a policy may name in
	// approve.roles. An access role must never double as decide authority by
	// name coincidence, and "who may approve agent actions" becomes an
	// IdM-masterable, certifiable object of its own.
	RoleKindBusiness    = "business"
	RoleKindApplication = "application"
	RoleKindApprover    = "approver"

	// Role planes: an access-plane role governs what an agent session reaches
	// (or, for the approver kind, who decides for it); a control-plane role
	// governs strazad itself (straza-admin, delegated admins, auditor). The
	// wire spells the control plane as kind "straza"; storage keeps plane
	// orthogonal to kind, in a column of its own.
	RolePlaneAccess  = "access"
	RolePlaneControl = "control"

	SessionActive  = "active"
	SessionRevoked = "revoked"
	SessionClosed  = "closed"

	AttestationManaged  = "managed"
	AttestationAdvisory = "advisory"
	AttestationNone     = "none"

	KeyPurposeSession  = "session"
	KeyPurposeSnapshot = "snapshot"
	// KeyPurposeClientAssertion signs the client assertion an agent's client
	// presents at the customer's identity provider (RFC 7523 section 2.2).
	// The database holds at most one staged and one active key of it.
	KeyPurposeClientAssertion = "client_assertion"

	// Signing key lifecycle: staged, active, retiring, retired. A staged
	// key verifies on every replica before any replica signs with it
	// (authn.TokenService.Advance promotes it after two reload intervals).
	KeyStaged   = "staged"
	KeyActive   = "active"
	KeyRetiring = "retiring"
	KeyRetired  = "retired"

	RevokeUser    = "user"
	RevokeSession = "session"
	RevokeDevice  = "device"
	RevokeJTI     = "jti"

	// Identity typology. userType is the SCIM CORE attribute with
	// a Straza-enforced vocabulary; agencyMode rides the Straza extension.
	// Every value names its engine consumer: userType = policy
	// match + labels + approver-resolution exclusion for service; agencyMode
	// = approval semantics (autonomous is never selfApproval-eligible:
	// engine invariant, spec/policyset revision 9).
	UserTypeHuman   = "human"
	UserTypeAgent   = "agent"
	UserTypeService = "service"

	AgencyInteractive = "interactive"
	AgencySupervised  = "supervised"
	AgencyAutonomous  = "autonomous"

	// Revocation origins: which authority created the row. The SCIM
	// reactivation lift deletes ONLY scim-origin rows. Admin/external rows
	// (locks) survive every IdM write and lift only via an explicit Straza
	// action (admin enable or unlock).
	RevocationOriginSCIM     = "scim"
	RevocationOriginAdmin    = "admin"
	RevocationOriginExternal = "external"

	AppSourceAPI      = "api"
	AppSourceGitops   = "gitops"
	AppSourceRegistry = "registry"

	CredScopeRole = "role"
	CredScopeUser = "user"
	// CredScopeApp is the server's own secret: one static row per app, owned
	// by the app itself (OwnerID is the app id), used for health checks and
	// for calls whose roles hold no per-role row.
	CredScopeApp = "app"
	CredStatic   = "static"
	CredOAuth    = "oauth"
	// CredToken is a caller's own pasted token: one user-scoped row per app
	// and user, the sealed value in EncPayload and the expiry, the setter
	// and the agents opt-in in OAuthMeta.
	CredToken = "token"
)

// User is an identity: a person, or a non-human identity such as an agent or a service.
type User struct {
	ID           string
	ExternalID   string // SCIM externalId; empty for local users
	Username     string
	Email        string
	Display      string
	Title        string // SCIM core title (RFC 7643): job title / agent function; informational, never engine-matched
	Status       string // active|disabled
	Origin       string // local|scim
	PasswordHash string // local users only; never serialized outward
	Attrs        string // JSON object
	// Identity typology (typed columns, never Attrs JSON: the
	// policy engine matches these via the checkin-built subject and must not
	// parse JSON on request paths). Empty = unclassified.
	UserType   string // human|agent|service (SCIM core userType)
	AgencyMode string // interactive|supervised|autonomous (agents)
	Sponsor    string // accountable human for an agent (username/ref)
	SwarmID    string // fleet membership (policy match + fleet kill)
	Ephemeral  bool   // short-lived task agent
	CreatedAt  time.Time
	UpdatedAt  time.Time
	DeletedAt  *time.Time
}

// Role is the unit of entitlement (policy selectors, tool bindings, packs).
type Role struct {
	ID          string
	Name        string
	Description string
	Kind        string // business|application|approver (access plane; ignored on the control plane)
	Plane       string // access|control (RolePlane*)
	// OwnerAppID names the server that owns the role, empty for a global
	// role. Ownership is authorized on this column, never on the name.
	OwnerAppID string
	CreatedAt  time.Time
	UpdatedAt  time.Time
}

// RoleImplication is a midPoint-style inducement edge: holding RoleID implies
// holding ImpliesRoleID. The graph is cycle-checked at write time.
type RoleImplication struct {
	RoleID        string
	ImpliesRoleID string
}

// AssignmentPair is the (role, subject) pair of one assignment row in force
// at a moment: the flat read the roles list folds into effective holders.
type AssignmentPair struct {
	RoleID    string
	SubjectID string
}

// RoleAssignment binds a user to a role, optionally time-boxed. Over the
// SCIM wire this row IS group membership (spec/scim-profile rev 12: the
// Groups surface renders roles; a members add/remove creates/deletes it).
type RoleAssignment struct {
	ID          string
	SubjectKind string // always SubjectUser
	SubjectID   string
	RoleID      string
	ValidFrom   *time.Time
	ValidTo     *time.Time
	Origin      string // scim|admin
	CreatedAt   time.Time
}

// Device client kinds name the client that enrolled a device and holds its
// credential. A row enrolled before the kind was recorded carries "".
const (
	DeviceClientKit   = "kit"   // the enforcement kit, straza
	DeviceClientHuman = "human" // strazactl, the console and the self-service page
)

// Device is an enrolled machine bound to a user (the second factor).
type Device struct {
	ID          string
	UserID      string
	Name        string
	Fingerprint string // pubkey/cert fingerprint
	Platform    string
	Status      string // active|disabled
	ClientKind  string // DeviceClientKit|DeviceClientHuman, "" before the kind was recorded
	EnrolledAt  time.Time
}

// Session is a first-class principal: user+device+harness binding.
type Session struct {
	ID                string
	UserID            string
	DeviceID          string // empty when no device factor
	HarnessName       string
	HarnessVersion    string
	ClientVersion     string // straza build stamp the client sent at check-in; "" for older clients
	AttestationLevel  string // managed|advisory|none
	AttestationHashes string // JSON object of measured hashes
	Status            string // active|revoked|closed
	StartedAt         time.Time
	LastSeen          time.Time
}

// KnowledgePack is role-bound context delivered at session start.
type KnowledgePack struct {
	ID        string
	Name      string
	Version   string
	Content   string // text v1
	Checksum  string
	CreatedAt time.Time
	UpdatedAt time.Time
}

// PackBinding is one pack-role edge with the role name joined in (admin read
// model). The table's key is composite (role_id, pack_id): there is no row id.
type PackBinding struct {
	PackID   string
	RoleID   string
	RoleName string
}

// App is a deployed MCP app. Manifest is the verbatim app.yaml as JSON.
type App struct {
	ID          string
	Name        string // ns/name
	Version     string
	Manifest    string // JSON
	RuntimeKind string // oci|command|remote
	Status      string // pending|starting|running|degraded|stopped|failed
	Source      string // api|gitops|registry
	// AdminRoleID names the control-plane role that administers this
	// server. Create and Revive mint one when the caller names none, so a
	// live row never reads empty.
	AdminRoleID string
	CreatedAt   time.Time
	UpdatedAt   time.Time
	DeletedAt   *time.Time
}

// ToolBinding exposes a subset of an app's tools to a role. Absence of any
// binding = the app is invisible to that role (default-deny for MCP).
type ToolBinding struct {
	ID          string
	RoleID      string
	AppID       string
	ToolMatcher string // JSON array of glob strings, e.g. ["get_*","list_*"]
	Effect      string // allow (deny arrives via PolicySet rules)
	CreatedAt   time.Time
}

// Credential is an encrypted upstream secret. EncPayload is ciphertext,
// and no API surface may return it to a client.
type Credential struct {
	ID         string
	AppID      string
	Scope      string // role|user|app
	OwnerID    string // role id, user id or app id
	Kind       string // static|oauth|token
	EncPayload []byte
	OAuthMeta  string // JSON (expiry, refresh handle, setter, agents opt-in)
	RotatedAt  *time.Time
	CreatedAt  time.Time
}

// PolicySet is the stored YAML source of one policy document.
type PolicySet struct {
	ID           string
	Name         string
	Priority     int
	YAMLSource   string
	CompiledHash string
	Status       string // draft|active
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

// Snapshot is a compiled, signed policy snapshot (content-addressed).
type Snapshot struct {
	ID          string // content hash
	SignerKeyID string
	Size        int64
	Blob        []byte
	Active      bool
	CreatedAt   time.Time
}

// OutboxEvent is a pending CloudEvent in the transactional outbox.
type OutboxEvent struct {
	ID        string
	Subject   string
	CE        string // CloudEvents JSON
	Published bool
	Attempts  int
	CreatedAt time.Time
}

// AuditRecord is one hash-chained row of the append-only audit mirror.
type AuditRecord struct {
	Seq       int64 // monotonic; assigned by the DB
	CE        string
	PrevHash  string
	Hash      string
	CreatedAt time.Time
}

// ConversationTurn is one captured prompt or reply (spec/events revision 5).
type ConversationTurn struct {
	ID          string
	CEID        string // dedupe key (at-least-once delivery)
	SessionID   string
	UserID      string
	Kind        string // prompt|reply
	Mode        string // verbatim|redact (the mode ACTIVE when captured)
	Content     string
	Truncated   bool
	ContentHash string // SHA-256 of the FULL original content
	// AgentType tags a turn produced by a delegate (subagent capture,
	// spec/events revision 13): the class of agent, e.g. "researcher".
	// Empty = the main agent's own lane.
	AgentType string
	// BodyExternal marks a turn whose body lives in the configured
	// body store, keyed by ContentHash. The row stores empty content (the
	// caller persisted the body BEFORE inserting, and a row must only ever
	// reference a durable body). The read path resolves it transparently.
	BodyExternal bool
	At           time.Time
}

// Approval is one human-approval record for a `mode: approve` rule. Created
// on the /v1/decide escalation lane (State pending), resolved on the admin
// plane, expired by the sweep. ApproverRoles empty means the straza-admin
// fallback applies at decision time. Times are UTC; DecidedAt is nil until
// resolution.
type Approval struct {
	ID            string
	SessionID     string
	UserID        string // requester user id
	Username      string // requester username (display)
	RuleID        string
	SetName       string
	ArgvHash      string   // approval.EventKey(ev)
	Lane          string   // hook|gateway
	Summary       string   // human-facing label, e.g. "mcp.call midpoint:disable_user"
	Justification string   // gateway lane only; may be ""
	ApproverRoles []string // normalized spec.Roles ("" = straza-admin fallback)
	// ApproverUsers (revision 13 approve.deciders): the
	// user-scoped decide pool, USERNAMES resolved at request time (today:
	// the requester's sponsor). Empty = no user-scoped deciders. A record
	// with neither roles nor users is UNROUTED: decide rights stay with
	// straza-admin but no channel announces it.
	ApproverUsers   []string
	SelfApproval    bool
	Mode            string // approve|confirm ("" reads as approve)
	TimeoutSeconds  int
	RetryTTLSeconds int
	State           string // pending|approved|denied|expired
	DecidedBy       string // decider user id
	DecidedByName   string // decider display/username
	Channel         string // console|slack|phone|browser (legacy rows: api)
	ChannelRefs     map[string]string
	// Decided attribution: the decider's own reason
	// (key-bound on the signed lane: its sha256 rides the signed string) and
	// the enrolled device whose key signed the decision. Both "" for legacy
	// rows, console/Slack decisions without a reason, and pending rows.
	DecidedReason   string
	DecidedDeviceID string
	CreatedAt       time.Time
	ExpiresAt       time.Time
	DecidedAt       *time.Time
	// Class and use fields (spec/policyset revisions 6 and 23). Class is
	// 'hold' (default, one run by the held call or one identical retry of its
	// session) or 'ticket' (a single-use, user+fingerprint-bound grant a
	// later session consumes). GrantExpiresAt is the use deadline, set when
	// the row is approved: decidedAt + grantTTL for a ticket, decidedAt +
	// retryTTL for a hold; nil before approval and on a hold approved by an
	// older build.
	// ConsumedAt/ConsumedBy record the single use win (nil/"" until used).
	// GrantTTLSeconds is the configured post-approval consume window persisted at
	// request time so the decision path can materialize GrantExpiresAt =
	// decidedAt + GrantTTLSeconds atomically with the approve flip (0 for a hold).
	Class           string     // hold|ticket
	ConsumedAt      *time.Time // when the approval was used
	ConsumedBy      string     // the session that used it
	GrantExpiresAt  *time.Time // use window deadline
	GrantTTLSeconds int        // ticket: configured consume window (seconds); 0 for a hold
	// Args preview. A redaction-first,
	// 2 KiB-capped glance of the concrete call arguments the fingerprint
	// binds, computed at request time. ArgsPreview is scrubbed BEFORE it is
	// measured/truncated; ArgsBytes is the redacted length before any
	// truncation; ArgsTruncated flags a head+tail elision. All zero for a
	// pre-feature row or when the preview knob is off.
	ArgsPreview   string
	ArgsTruncated bool
	ArgsBytes     int
	// Notify is the winning rule's notification routing (spec/policyset
	// revision 8 `approve.notify`),
	// persisted at request time because the later announcement lanes (terminal
	// status, ticket reminder) run from the stored row. Empty = every
	// configured channel (pre-revision-8 behavior); a record's routing is
	// immutable; a later policy edit never re-routes an existing approval.
	Notify []string
}

// Revocation is a denylist entry pushed to enforcement points.
type Revocation struct {
	ID        string
	Kind      string // user|session|device|jti
	TargetID  string
	Reason    string
	Origin    string // scim|admin|external: who created it
	CreatedAt time.Time
}

// AttestationHash is one expected-measurement row in the managed-install
// registry. Artifact names the measurement key from the check-in
// payload (self, config, hooks.<harness>); empty Harness/Platform means the
// row applies to every harness/platform.
type AttestationHash struct {
	ID        string
	Artifact  string // self | config | hooks.<harness>
	Harness   string // harness this row applies to; "" = all
	Platform  string // GOOS/GOARCH this row applies to; "" = all
	Hash      string // "sha256:<hex>"
	Note      string
	CreatedAt time.Time
}

// SigningKey is one signing keypair. A session or snapshot key is ed25519,
// and PrivateKey is its raw seed. A client assertion key is RSA: PrivateKey
// is the PKCS#8 encoding sealed under the secrets KEK, never the plain key,
// and PublicKey is the PKIX encoding.
type SigningKey struct {
	KID        string
	Purpose    string // session|snapshot|client_assertion
	Status     string // staged|active|retiring|retired
	PrivateKey []byte
	PublicKey  []byte
	CreatedAt  time.Time
	RotatedAt  *time.Time
}

// ApproverDevice is a mobile approver enrolment: a hardware-bound ECDSA
// P-256 signing key registered against a Straza user. The row IS the
// credential: deleting it revokes the device, so use=approver token
// verification is row-backed (no denylist plumbing). PublicKey is base64
// X.509 SubjectPublicKeyInfo DER, and the curve is read from the key
// itself, never from the (advisory) KeyAlg field.
type ApproverDevice struct {
	ID               string // apd_<uuidv7>
	UserID           string
	Name             string
	Platform         string // android|ios
	KeyAlg           string // reported, advisory: the SPKI OID is authoritative
	PublicKey        string // base64 SPKI DER
	KeySecurityLevel string // strongbox|tee|secure-enclave|software
	AttestationKind  string // play-integrity|app-attest|none
	AttestationBlob  string
	CreatedAt        time.Time
	LastSeenAt       *time.Time
}

// ApproverEnrollToken is a one-time, short-TTL enrolment credential minted by
// an admin or by the user themself and rendered as the QR token. Only
// TokenHash (sha256 hex) is stored; UsedAt gates single use. Channel scopes
// what the token may enroll: empty = admin-minted, any platform;
// mobile|browser = self-minted, enforced at consume.
type ApproverEnrollToken struct {
	ID        string
	TokenHash string
	UserID    string
	Channel   string
	CreatedAt time.Time
	ExpiresAt time.Time
	UsedAt    *time.Time
}

// ApproverChallenge is a single-use decision nonce: fresh per decidable
// pending row per fetch, bound to the fetching device and the approval it
// authorizes. Consumed atomically at decide time.
type ApproverChallenge struct {
	Challenge  string // base64 nonce (≥16 bytes)
	DeviceID   string
	ApprovalID string
	CreatedAt  time.Time
	ExpiresAt  time.Time
	UsedAt     *time.Time
}

// ApproverPush is a push registration keyed to an approver device (storage +
// dedupe). Kind ∈ fcm|apns|unifiedpush|webpush. RegisteredAt is the last time
// the app CONFIRMED this registration (every PUT re-upsert refreshes it), not
// first-seen: the APNs 410 prune guard compares it against Apple's
// invalidation timestamp, and a re-registration must outrank an older
// invalidation (ApproverRepo.DeletePushBefore). It maps to the created_at
// column.
type ApproverPush struct {
	ID              string
	DeviceID        string
	Kind            string
	TokenOrEndpoint string
	RegisteredAt    time.Time
}
