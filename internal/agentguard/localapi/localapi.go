// Package localapi is the local API between the straza thin clients and
// `straza service`: the routes, the request and answer bodies, the socket
// path rule and a client that checks who owns the socket before it sends.
// It is a leaf that both sides import, so strazad, which links agentguard,
// never links the service itself.
package localapi

import (
	"time"

	"github.com/strazahq/straza/internal/policy"
)

// Routes of the local API. RouteMCP serves /local/v1/mcp and
// /local/v1/mcp/{server}, and RouteConnect passes every path below it to
// strazad's /v1/connect/ with the caller's session.
const (
	Prefix        = "/local/v1"
	RouteStatus   = Prefix + "/status"
	RouteEnroll   = Prefix + "/enroll"
	RouteKeygen   = Prefix + "/keygen"
	RouteSessions = Prefix + "/sessions"
	RouteDecide   = Prefix + "/decide"
	RouteCapture  = Prefix + "/capture"
	RouteMCP      = Prefix + "/mcp"
	RouteConnect  = Prefix + "/connect/"
)

// BaseURL is the URL prefix a client puts before a route. The host is
// never resolved, because the client dials the socket.
const BaseURL = "http://straza-service"

// Header names that pick the caller's session on the proxied routes.
const (
	HeaderHarness        = "Straza-Harness"
	HeaderHarnessVersion = "Straza-Harness-Version"
	HeaderHarnessSession = "Straza-Harness-Session"
)

// HeaderNotSent marks an answer on a proxied route that is the service's own
// refusal, made before it sent the request to strazad. Only the service
// sets it, and it removes the header from every answer strazad sends.
const HeaderNotSent = "Straza-Not-Sent"

// ServiceSID is the SID Windows gives NT SERVICE\straza-service, the
// account of the Windows service straza-service: S-1-5-80 and the SHA-1 of
// the upper-case service name. The thin clients refuse a pipe it does not
// own.
const ServiceSID = "S-1-5-80-4097175862-1602505720-2885049577-3910644125-2155426594"

// Client timeouts. Each sits above the service's worst case for the route,
// so a hook never denies while the service is still deciding.
const (
	DecideTimeout  = 12 * time.Second
	SessionTimeout = 15 * time.Second
	ShortTimeout   = 5 * time.Second
)

// Harness names one harness window of the caller. Session is the harness's
// own session id. An empty Session picks the caller's newest live session
// of that harness, which is what `straza mcp`, `straza exec` and
// `straza connect` send because they do not know their window.
type Harness struct {
	Name    string `json:"name"`
	Version string `json:"version,omitempty"`
	Session string `json:"session,omitempty"`
}

// Refusal is the body of every answer at or above 400. Error is a sentence
// the thin client shows as it is.
type Refusal struct {
	Error string `json:"error"`
}

// SessionRequest starts a session for one harness window, or reuses the
// live one of the same window.
type SessionRequest struct {
	Harness Harness `json:"harness"`
}

// Pack is one knowledge pack delivered at session start.
type Pack struct {
	Name     string `json:"name"`
	Version  string `json:"version"`
	Content  string `json:"content"`
	Checksum string `json:"checksum"`
}

// SessionAnswer is the governed session as the harness banner shows it. It
// never carries a token.
type SessionAnswer struct {
	SessionID   string                  `json:"session_id"`
	User        string                  `json:"user"`
	Roles       []string                `json:"roles"`
	Attestation string                  `json:"attestation"`
	SnapshotID  string                  `json:"snapshot_id"`
	ServerURL   string                  `json:"server_url"`
	Capture     policy.CaptureDirective `json:"capture"`
	Packs       []Pack                  `json:"packs,omitempty"`
	Reused      bool                    `json:"reused,omitempty"`
	// StartText is the governance paragraph strazad wrote at check-in, nil
	// when the server or the service is older and sent none.
	StartText *string `json:"start_text,omitempty"`
}

// DecideRequest is one normalized hook event. It carries no conversation
// text: the thin client records a prompt through the capture route after
// any orphaned reply of the turn before, so the audit order stays
// chronological.
type DecideRequest struct {
	Harness        Harness      `json:"harness"`
	NativeEvent    string       `json:"native_event,omitempty"`
	Event          policy.Event `json:"event"`
	AgentType      string       `json:"agent_type,omitempty"`
	AgentID        string       `json:"agent_id,omitempty"`
	GatewayProxied bool         `json:"gateway_proxied,omitempty"`
	// NativeTool is the harness's own name of a classified local tool. The
	// service puts it on the event it decides and forwards, because the
	// event's JSON leaves it out (policy.Event.NativeTool).
	NativeTool string `json:"native_tool,omitempty"`
	// ToolInput is the input of a classified local tool, nil when the
	// harness sent none. It rides beside the event for the audit record
	// alone: no rule reads it and no approval key binds it.
	ToolInput *ToolInput `json:"tool_input,omitempty"`
}

// ToolInput is what an audit record keeps of a classified local tool's
// input. Text is the harness's tool input as JSON, whole when it fits the
// record's bound and with its longest strings shortened when it does not.
// Hash is "sha256:" and the hex SHA-256 of the whole text, set only when
// Text was shortened.
type ToolInput struct {
	Text string `json:"text"`
	Hash string `json:"hash,omitempty"`
}

// Journal is what the hook's decision journal records about one decision:
// the snapshot that decided, the escalation lane that ran, the server's
// status and correlation id, whether the audit record reached the spool,
// and whether the deny was a fail-closed answer rather than a rule's.
type Journal struct {
	Snapshot    string `json:"snapshot,omitempty"`
	Escalation  string `json:"escalation,omitempty"`
	Status      int    `json:"status,omitempty"`
	Correlation string `json:"correlation,omitempty"`
	Spooled     bool   `json:"spooled,omitempty"`
	SpoolError  string `json:"spool_error,omitempty"`
	FailClosed  bool   `json:"fail_closed,omitempty"`
}

// DecideAnswer is the decision, the session's capture directive and the
// journal facts.
type DecideAnswer struct {
	Decision policy.Decision         `json:"decision"`
	Capture  policy.CaptureDirective `json:"capture"`
	Journal  Journal                 `json:"journal"`
}

// Capture kinds.
const (
	CapturePrompt = "prompt"
	CaptureReply  = "reply"
)

// CaptureRequest hands the service conversation text the thin client read
// from the harness transcript. The service applies the capture mode
// (redaction and the size cap) and records it.
type CaptureRequest struct {
	Harness   Harness `json:"harness"`
	Kind      string  `json:"kind"`
	Text      string  `json:"text"`
	AgentType string  `json:"agent_type,omitempty"`
	AgentID   string  `json:"agent_id,omitempty"`
}

// EnrollRequest enrolls the caller. A person signs in on Lane, LaneBrowser
// or LaneDeviceCode, and the empty value means device code, as older thin
// clients send it. With Headless set, the caller enrolls as the agent
// Username with its key or client secret, and Lane is not read.
type EnrollRequest struct {
	ServerURL string `json:"server_url"`
	Headless  bool   `json:"headless,omitempty"`
	Username  string `json:"username,omitempty"`
	Lane      string `json:"lane,omitempty"`
}

// Sign-in lanes of a person's enrollment.
const (
	LaneBrowser    = "browser"
	LaneDeviceCode = "device-code"
)

// Enrollment event kinds, one JSON object per line of the enroll answer.
// EnrollBrowser carries the authorization link to open in VerificationURI.
const (
	EnrollCode    = "code"
	EnrollBrowser = "browser"
	EnrollNote    = "note"
	EnrollDone    = "done"
	EnrollError   = "error"
)

// EnrollEvent is one line of the streamed enroll answer: the code and link
// to open, the browser link to open, a progress note, the result, or the
// sentence that ended it.
type EnrollEvent struct {
	Event           string        `json:"event"`
	UserCode        string        `json:"user_code,omitempty"`
	VerificationURI string        `json:"verification_uri,omitempty"`
	Text            string        `json:"text,omitempty"`
	Result          *EnrollResult `json:"result,omitempty"`
	Error           string        `json:"error,omitempty"`
}

// Enrollment kinds.
const (
	KindPerson = "person"
	KindAgent  = "agent"
)

// Custody levels: account when the service runs under its own OS account
// and user when it runs as the caller.
const (
	CustodyAccount = "account"
	CustodyUser    = "user"
)

// EnrollResult describes the enrollment the service now holds.
type EnrollResult struct {
	ServerURL       string `json:"server_url"`
	Kind            string `json:"kind"`
	Username        string `json:"username"`
	UserID          string `json:"user_id,omitempty"`
	DeviceID        string `json:"device_id,omitempty"`
	KeyThumbprint   string `json:"key_thumbprint"`
	Custody         string `json:"custody"`
	SignInExpiresIn int    `json:"sign_in_expires_in,omitempty"`
}

// KeygenRequest makes the agent key of the caller. Force replaces a key
// that exists.
type KeygenRequest struct {
	Username string `json:"username,omitempty"`
	Force    bool   `json:"force,omitempty"`
}

// KeygenAnswer carries the public half as standard base64, the value
// `straza keygen` prints after "Public key: ".
type KeygenAnswer struct {
	Username  string `json:"username,omitempty"`
	PublicKey string `json:"public_key"`
}

// Status is what `straza status` and `straza doctor` show for the caller.
type Status struct {
	Version    string          `json:"version"`
	UserMode   bool            `json:"user_mode,omitempty"`
	Custody    string          `json:"custody"`
	Enrollment *Enrollment     `json:"enrollment,omitempty"`
	Sessions   []SessionStatus `json:"sessions,omitempty"`
	Spool      SpoolStatus     `json:"spool"`
}

// Enrollment is the caller's enrollment. MachineChanged is set when the
// machine identity no longer matches the one the key was enrolled on, and
// WaitUntil when a check-in with an unknown outcome holds the next one.
type Enrollment struct {
	ServerURL      string    `json:"server_url"`
	Kind           string    `json:"kind"`
	Username       string    `json:"username"`
	DeviceID       string    `json:"device_id,omitempty"`
	KeyThumbprint  string    `json:"key_thumbprint"`
	Custody        string    `json:"custody"`
	SignedInAt     time.Time `json:"signed_in_at,omitzero"`
	MachineChanged bool      `json:"machine_changed,omitempty"`
	WaitUntil      time.Time `json:"wait_until,omitzero"`
}

// Push states of a session's revocation stream.
const (
	PushConnected  = "connected"
	PushConnecting = "connecting"
	PushOff        = "off"
)

// SessionStatus is one live server session of the caller. PolicyRefused is
// the server's sentence while it refuses to send a newer policy than the one
// SnapshotID pins. Attestation is the level strazad gave the session at
// the check-in that started it, and AttestedAt is when. Both are empty from
// a service that does not report them.
type SessionStatus struct {
	Harness       Harness   `json:"harness"`
	SessionID     string    `json:"session_id"`
	User          string    `json:"user"`
	SnapshotID    string    `json:"snapshot_id,omitempty"`
	ExpiresAt     time.Time `json:"expires_at"`
	Push          string    `json:"push"`
	Revoked       string    `json:"revoked,omitempty"`
	PolicyRefused string    `json:"policy_refused,omitempty"`
	Attestation   string    `json:"attestation,omitempty"`
	AttestedAt    time.Time `json:"attested_at,omitzero"`
}

// SpoolStatus is the caller's audit spool.
type SpoolStatus struct {
	Pending   int       `json:"pending"`
	Dropped   int       `json:"dropped,omitempty"`
	LastDrain time.Time `json:"last_drain,omitzero"`
	LastError string    `json:"last_error,omitempty"`
}
