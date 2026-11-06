// Package service is `straza service`: the one process on a machine that
// holds each enrolled user's key and every Straza session, and answers the
// thin clients (the hooks, `straza mcp`, the git helper and the CLI) over a
// local socket where the kernel names the caller. It has two halves. The
// credential half owns keys, the check-in counter, the DPoP transport,
// enrollment, check-in, refresh, re-acquire, push, the snapshot cache, the
// audit spool and its drain, the git grants and the git host file. The API
// half owns the listener, the caller's uid, rate limits, decide, capture
// intake, the MCP and connect proxies, the git door and status. The API
// half reaches the credential half only through the credentials interface.
package service

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/strazahq/straza/internal/agentguard"
	"github.com/strazahq/straza/internal/agentguard/localapi"
	"github.com/strazahq/straza/internal/policy"
)

// Options configure one service process.
type Options struct {
	// Socket is the path the service listens on when systemd passes no
	// socket.
	Socket string
	// StateDir holds the directory lock, the machine id and one directory
	// per enrolled uid.
	StateDir string
	// UserMode is set when STRAZA_HOME selected the paths.
	UserMode bool
	// Log receives lifecycle lines and one line per refusal.
	Log *slog.Logger
	// Ready, when set, is called once after the service listens and before
	// it serves. A service that stops before it listens never calls it.
	Ready func()
}

// Caller is the peer of one local API connection, read from the socket.
// UID names the caller's OS account: the decimal uid on Linux and macOS,
// and the account's SID on Windows. It names the caller's folder in the state
// directory, so its Linux spelling never changes.
type Caller struct {
	UID string
	PID int32
}

// RenewalClass is what the last renewal of a session observed. The zero
// value covers both "not attempted" and "succeeded".
type RenewalClass int

// Renewal classes, as the offline gate words them (D11, D30).
const (
	RenewNone      RenewalClass = iota
	RenewTransport              // unreachable or a 5xx: the token's state is unknown
	RenewRejected               // a 401 whose re-acquire could not complete
	RenewTerminal               // a refusal retrying cannot fix
	RenewUnjudged               // an answer below 500 that judged nothing
)

// Renewal is the class and the server's sentence of the last renewal.
type Renewal struct {
	Class  RenewalClass
	Detail string
}

// Session is what the API half sees of one server session. It is a copy,
// and it never carries the session token.
type Session struct {
	UID            string
	Harness        localapi.Harness
	ServerURL      string
	ID             string
	User           string
	Roles          []string
	Attestation    string
	UserType       string
	AgencyMode     string
	SwarmID        string
	SnapshotID     string
	IssuedAt       time.Time
	ExpiresAt      time.Time
	PolicyRefused  string
	PolicyDeadline time.Time
	Renewal        Renewal
	Packs          []localapi.Pack
	Reused         bool
	// StartText is the governance paragraph the server wrote at check-in,
	// nil when the server sent none.
	StartText *string
}

// Subject builds the policy subject of the session, the same way strazad
// builds it, so a rule decides identically on both enforcement points.
func (s Session) Subject() policy.Subject {
	return policy.Subject{
		User:        s.User,
		Roles:       s.Roles,
		Attestation: s.Attestation,
		Harness:     harnessLabel(s.Harness),
		UserType:    s.UserType,
		AgencyMode:  s.AgencyMode,
		SwarmID:     s.SwarmID,
	}
}

// harnessLabel is the harness as a subject and an audit record name it:
// name/version, or the name alone.
func harnessLabel(h localapi.Harness) string {
	if h.Version == "" {
		return h.Name
	}
	return h.Name + "/" + h.Version
}

// Refused is a failure the API half answers with Sentence as it is: the
// caller is not enrolled, the session was revoked, the server refused the
// check-in, the key waits after a lost answer, or the machine changed.
// Status is the local API's answer code.
type Refused struct {
	Status   int
	Sentence string
}

func (r *Refused) Error() string { return r.Sentence }

// DecideOutcome is strazad's answer to one /v1/decide call, with the status
// and correlation id the decision journal records and whether a 401 bounce
// ran.
type DecideOutcome struct {
	Answer      agentguard.DecideResponse
	Status      int
	Correlation string
	Bounced     bool
}

// credentials is the credential half as the API half uses it. Every method
// is safe for concurrent use and acts only on the given caller's
// enrollment, key, sessions and spool.
type credentials interface {
	// Enroll enrolls c, a person by device code or an agent headless, and
	// hands each progress event to emit.
	Enroll(ctx context.Context, c Caller, req localapi.EnrollRequest, emit func(localapi.EnrollEvent)) (localapi.EnrollResult, error)
	// Keygen makes c's agent key and returns its public half.
	Keygen(ctx context.Context, c Caller, req localapi.KeygenRequest) (localapi.KeygenAnswer, error)
	// Status fills c's enrollment, sessions and spool.
	Status(c Caller) localapi.Status
	// Session returns c's session for h. It renews a session with 120
	// seconds or less left, re-acquires once through the enrollment on a
	// 401 (D30), and reports that renewal on Session.Renewal. With start
	// set it checks in when h has no live session. An empty h.Session
	// picks c's newest live session of h.Name. Failures are *Refused.
	Session(ctx context.Context, c Caller, h localapi.Harness, start bool) (Session, error)
	// Policy returns the verified engine of the session's current snapshot
	// pin, the snapshot's maxAge in seconds and the id of the snapshot it
	// used, fetching the snapshot when the service does not hold it. The
	// audit record names that id, never the pin of an older Session copy.
	// A failure's text is the deny sentence.
	Policy(ctx context.Context, s Session) (eng *policy.Engine, maxAge int64, snapshotID string, err error)
	// Decide asks strazad's /v1/decide for s. A 401 bounces once through a
	// renewal.
	Decide(ctx context.Context, s Session, ev policy.Event, agentType, agentID string) (DecideOutcome, error)
	// Record appends one audit record of type ceType to c's spool, and the
	// credential half drains it.
	Record(c Caller, ceType string, data map[string]any) error
	// Transport sends a request for s to strazad with the session token and
	// a fresh proof, renewing the token first when it is near expiry. The
	// API half sets the request's URL to strazad's.
	Transport(s Session) http.RoundTripper
	// GitResolve asks s's strazad whether it serves git for upstream, the
	// URL git gave the helper. Failures are *Refused.
	GitResolve(ctx context.Context, s Session, upstream string) (bool, error)
	// GitGrant asks s's strazad for a git grant for upstream and req, and
	// holds it for s's uid and the helper run opID until it expires. It
	// never returns the grant. While an approval is open it asks again until
	// the approval ends, calling wait on the caller's goroutine with the
	// sentence for the person at once and then at least every 5 seconds.
	// Failures are *Refused.
	GitGrant(ctx context.Context, s Session, opID, upstream string, req localapi.GitGrantRequest, wait func(sentence string)) error
	// GitSend sends in, one smart HTTP request of the helper run opID, to
	// the resource server under the grant that run holds for route, with a
	// fresh proof from the enrollment key, and returns the answer as it
	// came. It streams in's body once and follows no redirect. Failures are
	// *Refused.
	GitSend(s Session, opID string, route gitRoute, in *http.Request) (*http.Response, error)
}
