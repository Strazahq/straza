package drafts

import "time"

// World is live state as the checks see it, every config object keyed by
// name, because a draft's documents name objects and never carry row ids.
// The server reads a World once per check. Overlay lays a draft on top, and
// the rules then read the result without touching the store. A direct admin
// route may read only the parts its rule reads, and each rule says which.
type World struct {
	// Apps are the registered MCP servers.
	Apps map[string]App
	// Roles are every role, the Straza plane included.
	Roles map[string]Role
	// Implies maps a role to the roles it implies directly.
	Implies map[string][]string
	// Access maps an application role to its one access row.
	Access map[string]Access
	// Policies are the sets the active snapshot carries, with the text they
	// were published with.
	Policies map[string]Policy
	// Holders maps a role to the users who hold it directly, from the
	// assignments in force when the World was read.
	Holders map[string][]Holder
	// Windows maps a username to the direct assignments of that user that
	// start or end after the World was read, sorted by role. A role Holders
	// lists the user under and Windows gives no row of is held with no end,
	// because the store keeps one row per user and role. Only the checks of
	// the proposer's own reach and guardrail read it, at each instant the
	// proposer's roles change.
	Windows map[string][]RoleWindow
	// HolderCounts maps a role to the number of subjects that hold it
	// through any path: its own assignments and those of every role whose
	// implication closure reaches it, validity windows included, each
	// subject once. A role missing here was not counted.
	HolderCounts map[string]int
	// Providers are the OAuth providers strazad's config names, with whether
	// each has client credentials settings.
	Providers map[string]Provider
	// SnapshotID is the active policy snapshot the Policies came from.
	SnapshotID string
	// Generation is the config generation, read before anything else, so a
	// publish that lands during the read moves it.
	Generation int64
	// Fingerprints maps Item.Object() of a draft's items and of the objects
	// its removals take with them to the live fingerprint. An object the map
	// lacks does not exist.
	Fingerprints map[string]Fingerprint
	// LocalToolDefault is governance.localToolDefault, allow or deny, which
	// both policy engines of a check compile with.
	LocalToolDefault string
	// Push is true when a push lane for approvals is configured.
	Push bool
	// Slack is true when approval requests also reach approvers in Slack,
	// approval.channels.slack.enabled.
	Slack bool
	// DockerOnPath is true when this host can start container servers.
	DockerOnPath bool
	// RefuseCommand is true when this server refuses command servers, as it
	// does under the enterprise profile.
	RefuseCommand bool
	// ServerAdminsCoverEvery is admin.serverAdminsCoverEvery: true lets a
	// server admin make a role their server owns cover every object of the
	// server's pinned type.
	ServerAdminsCoverEvery bool
	// roleDocs holds, by name, the Role documents Overlay read for the
	// draft it laid on, so a check reads each Role document once.
	roleDocs map[string]RoleDoc
}

// App is one registered MCP server. Manifest is the stored app.yaml as JSON.
// Offered lists the tool names the running server offers, and is nil when
// Straza has not read them.
type App struct {
	ID       string
	Name     string
	Manifest string
	Source   string
	Status   string
	Paused   bool
	Offered  []string
	// File is the apps directory path that defines the server, empty when
	// no file does.
	File string
	// AdminRole names the role that administers the server, empty when
	// that role cannot be found.
	AdminRole string
	// RolePrefix is what the name of every role the server owns begins
	// with: the server's name folded to a-z, 0-9 and single hyphens, then a
	// hyphen.
	RolePrefix string
	// Credential is the credential kind the manifest declares, none when it
	// declares none. Agents is what an agent with no credential of its own
	// uses on a caller kind, own when the manifest says nothing, and empty
	// for the other kinds. Both stay empty until the manifest is read.
	Credential string
	Agents     string
	// Detail is the manager's reason for the server's health status.
	Detail string
	// Runtime is the runtime kind: command, remote or oci. URL and Auth
	// describe a remote server, Exec, Args and Workdir a command, Image and
	// Sandbox a container, and EnvNames are the names of the environment
	// entries of a command or a container, never their values.
	Runtime, URL, Auth, Exec, Workdir, Image, Sandbox string
	Args, EnvNames                                    []string
	// Provider is the OAuth provider the credential names, and InjectAs is
	// where Straza injects a secret, header or env.
	Provider, InjectAs string
	// Scopes are the OAuth scopes the credential asks for in every person's
	// sign-in at Provider.
	Scopes []string
	// Exposure is the manifest's exposure globs, ["*"] by default.
	Exposure []string
	// RegistryURL is the remote URL the verbatim server block names.
	RegistryURL string
	// ReadOnly lists the offered tools whose upstream annotations say
	// readOnlyHint.
	ReadOnly []string
	// Destructive holds each offered tool whose upstream annotations set
	// destructiveHint, with the value they set. A tool it leaves out sets
	// no hint.
	Destructive map[string]bool
	// SharedSecret is true when the server's own secret is stored,
	// RoleSecrets names the roles with a secret of their own, and
	// UserCredentials counts the users with a credential of their own. The
	// values are never read.
	SharedSecret    bool
	RoleSecrets     []string
	UserCredentials int
	// Objects is what the server read of an App item's straza.objects
	// block against its running server, nil when the block is absent.
	Objects *ObjectsReview
	// Pinned is the server's pinned object type after the draft, nil when
	// it pins none.
	Pinned *PinnedObjects
}

// PinnedObjects is what a draft check needs of a server's pinned object
// type. Reach maps a tool to argument, credential, all or none, and a tool
// it does not name reads all. Argument maps each argument tool to the
// argument whose value names the object. ListTool is the listing tool.
// Credential is the listing credential's state: set, missing or
// not-needed. IDs maps a value the draft's role puts name to its id in the
// object list, for the values the list holds. LiveType is the type the
// live manifest pins, empty when it pins none.
type PinnedObjects struct {
	Type, Plural string
	Reach        map[string]string
	Argument     map[string]string
	ListTool     string
	Credential   string
	IDs          map[string]string
	LiveType     string
}

// Role is one role. Owner is the name of the server that owns an owned
// application role, empty for a global role. Plane is access or control.
// Owned is true for every server-owned role, also when its server is gone
// and Owner cannot name it. Packs names the knowledge packs bound to it.
type Role struct {
	ID          string
	Name        string
	Kind        string
	Plane       string
	Owner       string
	Owned       bool
	Description string
	Packs       []string
}

// Access is an application role's access row: its id, the server it
// reaches, the tool matchers as stored and the objects it covers on a
// server that pins an object type, nil when the row says none. Server is
// empty when the row's server is gone. Fault says why the stored covers
// line cannot be read, empty when it can: such a row grants nothing, and
// no draft keeps it as it is.
type Access struct {
	ID     string
	Server string
	Tools  []string
	Covers *Scope
	Fault  string
}

// Policy is a set as the active snapshot carries it.
type Policy struct {
	Name string
	Text string
}

// Holder is a user who holds a role directly. Agent is true for a user who
// is not a person. UserType, AgencyMode and SwarmID are the identity
// typology the policy engine matches, Sponsor is the username of the
// person who answers for an agent, and Devices counts the user's enrolled
// approver devices.
type Holder struct {
	Username   string
	Agent      bool
	UserType   string
	AgencyMode string
	SwarmID    string
	Sponsor    string
	Devices    int
}

// RoleWindow is a direct assignment of Role that holds from From,
// inclusive, to To, exclusive, as the identity resolver reads it. A nil
// bound is open.
type RoleWindow struct {
	Role     string
	From, To *time.Time
}

// holds reports whether the window holds at t.
func (r RoleWindow) holds(t time.Time) bool {
	return (r.From == nil || !t.Before(*r.From)) && (r.To == nil || t.Before(*r.To))
}

// Provider is an OAuth provider the server's config names.
type Provider struct {
	Name              string
	ClientCredentials bool
}

// Standing is what the caller of a rule may change, as the admin guard
// resolved it. Full is the root role or an area grant over the object's
// area. Otherwise Servers holds the ids of the servers the caller
// administers, and AreaRefusal, set when that standing comes from the
// apps:write grant, is the sentence for an object that belongs to no
// server.
type Standing struct {
	Full        bool
	Servers     map[string]bool
	AreaRefusal string
}

// The role kinds live state stores, and RoleKindStraza, the word the admin
// API uses for a role on the control plane.
const (
	RoleKindBusiness    = "business"
	RoleKindApplication = "application"
	RoleKindApprover    = "approver"
	RoleKindStraza      = "straza"
)

// The role planes: the Straza roles govern Straza itself, and every other
// role is on the access plane.
const (
	PlaneControl = "control"
	PlaneAccess  = "access"
)

// The credential kinds a manifest declares that the rules read, and
// AgentsShared, the agents value that lets an agent use the server's own
// account.
const (
	CredentialNone  = "none"
	CredentialOAuth = "oauth"
	CredentialToken = "token"
	AgentsShared    = "shared"
)

// The roles and the name prefix the product reserves. AdminRole is full
// control of Straza, MCPAdminRole administers every MCP server,
// DraftConfigRole lets an agent propose drafts through the built-in straza
// app, and every role Straza mints for a server begins with
// AppAdminRolePrefix.
const (
	AdminRole          = "straza-admin"
	MCPAdminRole       = "straza-global-mcp-admin"
	DraftConfigRole    = "straza-draft-config"
	AppAdminRolePrefix = "mcp-admin-"
)
