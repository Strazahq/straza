// Package server assembles and runs the strazad process: config → logging →
// store → event bus → HTTP listener, which serves the operational endpoints
// (/healthz, /readyz, /version) and every API plane.
package server

import (
	"context"
	"encoding/json"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/strazahq/straza/internal/approval"
	"github.com/strazahq/straza/internal/authn"
	"github.com/strazahq/straza/internal/automint"
	"github.com/strazahq/straza/internal/bodystore"
	"github.com/strazahq/straza/internal/clientcredentials"
	"github.com/strazahq/straza/internal/config"
	"github.com/strazahq/straza/internal/events"
	"github.com/strazahq/straza/internal/identity"
	"github.com/strazahq/straza/internal/manager"
	"github.com/strazahq/straza/internal/policy"
	"github.com/strazahq/straza/internal/secrets"
	"github.com/strazahq/straza/internal/sentinel"
	"github.com/strazahq/straza/internal/server/console"
	"github.com/strazahq/straza/internal/server/metrics"
	"github.com/strazahq/straza/internal/server/ratelimit"
	"github.com/strazahq/straza/internal/snapshot"
	"github.com/strazahq/straza/internal/spine"
	"github.com/strazahq/straza/internal/store"
	"github.com/strazahq/straza/internal/tokenscopes"
	"github.com/strazahq/straza/internal/version"
	"github.com/strazahq/straza/internal/wire"
)

// Pinger is the health probe seam shared by store and bus.
type Pinger interface {
	Ping(ctx context.Context) error
}

// App is a fully assembled strazad instance.
type App struct {
	cfg       config.Config
	log       *slog.Logger
	store     store.Store
	bus       *events.Bus
	tokens    *authn.TokenService
	external  *authn.ExternalVerifier // nil unless oidc.issuer is configured
	resolver  *identity.Resolver
	snapKeys  *authn.SnapshotKeys
	snapshots *snapshot.Service
	// assertionKeys signs the client assertions of agents' clients and
	// publishes their key document (admin_keys.go).
	assertionKeys *authn.ClientAssertionKeys
	// clientTokens fetches and caches the upstream tokens of agents' own
	// clients, for servers with credential.agents client_credentials.
	clientTokens *clientcredentials.Tokens
	// harnessCfgs is the boot-rendered signed managed-config matrix
	// (api_harnesscfg.go), keyed harness+"/"+GOOS; read-only after New.
	harnessCfgs map[string]harnessCfgDoc
	// harnessCfgHooks indexes the CURRENT render's hooks.* hashes per
	// harness (any GOOS), for the sessions wiring-status read model;
	// read-only after New.
	harnessCfgHooks map[string]map[string]bool
	subjects        *subjectCache
	denylist        *denylist
	// approverRefused holds the approver tokens whose user_inactive refusal
	// is already on the chain (approver_authn.go).
	approverRefused sync.Map
	audit           *auditSpool
	metrics         *metrics.Set
	classifier      policy.Classifier // mode: classify backend (the embedded heuristic)
	approval        *approval.Service // mode: approve workflow; always constructed (console channel #0)
	relay           *spine.Relay
	pushHub         *pushHub // gateway-edge push fan-out; nil = lane down, endpoint 503s
	auditCons       *spine.AuditConsumer
	turnsCons       *spine.ConversationConsumer
	revCons         *spine.RevocationConsumer
	convCons        *spine.ConvergeConsumer
	instance        string // pod-unique CE source ("strazad/<uuid>"), self-event filter
	// polSummaries caches per-set summaries for the policies list keyed by
	// updated_at (api_policy_list.go); zero value ready, guarded by its own
	// mutex.
	polSummaries policySummaryCache
	// decisions caches the overview's 24-hour decision block for a minute
	// and its last hour by the minute for 5 seconds
	// (api_console_decisions.go); zero value ready, each guarded by its own
	// mutex.
	decisions decisionsCache
	// roleAreaScopes is admin.roleAreas precomputed at construction
	// (delegated admin, admin_rbac.go): role name → per-area scope for the
	// human lanes of requireAdmin. Empty map = straza-admin only.
	roleAreaScopes map[string]tokenscopes.Scope
	sentinel       *sentinel.Sentinel // nil unless governance.sentinel.enabled
	// bodyStore holds transcript turn bodies when capture.bodyStore is
	// configured; nil = inline bodies. Reads resolve transparently in
	// api_transcripts.go.
	bodyStore bodystore.Store
	// Audit-ingest backpressure: per-session token buckets plus a
	// cached outbox-depth gate; see api_audit.go.
	ingestLimiter *ratelimit.Limiter
	// Self-service approver enroll-token cooldown (approver_enroll.go): last
	// mint per user, pruned lazily under the mutex.
	selfEnrollMu   sync.Mutex
	selfEnrollLast map[string]time.Time
	backlogGate    struct {
		mu   sync.Mutex
		at   time.Time
		over bool
	}
	sinks      []*spine.SinkRunner
	sinksClose func()
	manager    *manager.Manager
	runtimes   []string // the app runtimes this host can run, for /version
	watcher    *manager.Watcher
	broker     *secrets.Broker
	gateway    *gateway
	// configMu serializes every apply of config on this replica with every
	// publish and write that reads live state for one (drafts_apply.go).
	// applied is what the last apply applied, and applyRetry and applyWait
	// are the pending retry of a failed apply and its next delay. configMu
	// guards the other three. appliedGen is the config generation of the
	// last apply that reached every step, which a check-in reads without the
	// lock. lifetime is New's context, which ends when the process stops,
	// and the retry runs on it whoever's apply failed. policyMark is the
	// mark of the last policy conversion that settled every set
	// (drafts_upgrade.go), which configMu guards too.
	configMu   sync.Mutex
	applied    appliedConfig
	applyRetry *time.Timer
	applyWait  time.Duration
	appliedGen atomic.Int64
	lifetime   context.Context
	policyMark store.PolicyMark
	// draftsWake wakes the drafts checker at once after a submit on this
	// replica stored a draft (drafts_checker.go). It holds one wake, and a
	// send never blocks.
	draftsWake chan struct{}
	http       *http.Server
	ln         net.Listener
	// Dedicated https-only approver listener (config.ApproverTLS); nil when
	// unset. Serves only /v1/approver/* + /readyz via approverSurfaceOnly.
	approverHTTP *http.Server
	approverLn   net.Listener
	tlsSPKIPin   string             // cached "sha256/<b64>" SPKI pin of the TLS leaf the enroll QR pins (approver listener wins over main; "" = plaintext/ingress)
	approverCert *automint.CertInfo // leaf behind tlsSPKIPin (boot log, /version, doctor); nil when plaintext/ingress
	// approverURLDerived: automint filled approverTLS.publicUrl itself (no
	// operator value); the phone-enroll guard refuses derived-loopback QRs.
	approverURLDerived bool
	projectID          string // persistent deployment identity (settings KV, first-boot generated); approver apps key multi-backend enrollments on it

	// Per-user OAuth connect: resolved provider registrations, the
	// shared exchange client, and the grant refresh worker.
	oauthProviders map[string]secrets.ProviderConfig
	oauthHTTP      *http.Client
	refresher      *secrets.Refresher
}

// New assembles an App: opens and migrates the store, boots the event bus,
// and binds the HTTP listener. On error, everything already started is torn
// down.
func New(ctx context.Context, cfg config.Config, log *slog.Logger) (*App, error) {
	st, err := store.Open(cfg)
	if err != nil {
		return nil, err
	}
	return build(ctx, cfg, log, st)
}

// approverSurfaceOnly restricts the dedicated approver listener to the
// surface the phone app actually dials (plus /readyz for probes). Everything
// else (admin API, console, gateway, OIDC) stays on the main listener and
// answers 404 here, so the extra port never widens the exposed surface.
func approverSurfaceOnly(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/v1/approver/") || r.URL.Path == "/readyz" {
			next.ServeHTTP(w, r)
			return
		}
		http.NotFound(w, r)
	})
}

// Addr returns the bound listener address (useful when the port was :0).
func (a *App) Addr() string {
	return a.ln.Addr().String()
}

// transcriptWatermarkPass measures capture storage on the retention-janitor
// cadence (never a request path): sets the transcript-store and
// data-disk-free gauges and logs one loud WARN per pass while the store
// sits above governance.transcriptBytesWatermark. The slow failure it
// exists for: match-all verbatim capture fills a disk over weeks, and a
// full disk under Postgres is a fail-closed fleet outage nobody watches
// for. Returns the measured bytes and whether the watermark warned, for
// tests.
func (a *App) transcriptWatermarkPass(ctx context.Context) (int64, bool) {
	bytes, err := a.store.Conversations().StorageBytes(ctx)
	if err != nil {
		a.log.Warn("transcript storage measure failed", "err", err)
		return -1, false
	}
	a.metrics.SetTranscriptBytes(float64(bytes))
	if free, ok := diskFreeBytes(a.cfg.DataDir); ok {
		a.metrics.SetDataDiskFree(float64(free))
	}
	mark := a.cfg.EffectiveTranscriptBytesWatermark()
	if bytes < mark {
		return bytes, false
	}
	a.log.Warn("transcript storage above watermark",
		"bytes", bytes, "watermark", mark,
		"hint", "scope or redact the recording policy, shorten governance.captureRetention, grow the volume, or raise governance.transcriptBytesWatermark",
		"docs", "https://docs.straza.ai/guides/write-policy/capture/#retention-and-where-the-text-lives")
	return bytes, true
}

// routeTable enumerates every strazad route. It is data, not just wiring:
// the OpenAPI drift test asserts pkg/api/openapi.yaml documents exactly the
// /v1 surface registered here.
func (a *App) routeTable() []route {
	admin := a.requireAdmin
	// A server's own verbs open to the holder of its admin role as well.
	serverAdmin := a.requireServerAdmin
	return []route{
		{"GET", "/healthz", handleHealthz},
		{"GET", "/readyz", handleReadyz(map[string]Pinger{"store": a.store, "bus": a.bus})},
		{"GET", "/version", a.handleVersion},
		{"GET", "/.well-known/straza/jwks.json", a.handleJWKS},
		{"GET", "/.well-known/straza/snapshot-keys.json", a.handleSnapshotKeys},
		{"GET", clientAssertionJWKSPath, a.handleClientAssertionJWKS},
		{"GET", "/.well-known/straza/idp.json", a.handleIdPDiscovery},

		{"POST", "/v1/enroll", a.handleEnroll},
		{"POST", "/v1/checkin", a.handleCheckin},
		{"POST", "/v1/decide", a.handleDecide},
		{"POST", "/v1/audit/batch", a.handleAuditBatch},
		{"GET", "/v1/push", a.handlePushSubscribe},
		{"GET", "/v1/snapshot", a.handleSnapshot},
		{"GET", "/v1/harness-config", a.handleHarnessConfig},
		// Self-scoped, deliberately outside /v1/admin: any authenticated
		// session may end ITSELF (the token names the only touchable target).
		{"POST", "/v1/session/revoke", a.handleSessionSelfRevoke},

		{"GET", "/v1/connect", a.requireUser(a.handleConnectList)},
		{"GET", "/v1/connect/callback", a.handleConnectCallback},
		{"POST", "/v1/connect/callback", a.requireUser(a.handleConnectFinish)},
		{"POST", "/v1/connect/{app}", a.requireUser(a.handleConnectStart)},
		{"PATCH", "/v1/connect/{app}", a.requireUser(a.handleConnectAgents)},
		{"DELETE", "/v1/connect/{app}", a.requireUser(a.handleConnectDelete)},

		{"GET", "/v1/admin/policies", admin(a.handlePolicyList)},
		{"PUT", "/v1/admin/policies", admin(a.handlePolicyApply)},
		// Literal segment, so it wins over the {name} wildcard below.
		{"GET", "/v1/admin/policies/event-support", admin(a.handlePolicyEventSupport)},
		{"GET", "/v1/admin/policies/{name}", admin(a.handlePolicyGet)},
		{"DELETE", "/v1/admin/policies/{name}", admin(a.handlePolicyDelete)},
		{"POST", "/v1/admin/policies/{name}/activate", admin(a.handlePolicyActivate)},

		{"GET", "/v1/admin/users", admin(a.handleUsersList)},
		{"POST", "/v1/admin/users", admin(a.handleUsersCreate)},
		{"GET", "/v1/admin/users/{id}", admin(a.handleUsersGet)},
		{"PATCH", "/v1/admin/users/{id}", admin(a.handleUsersUpdate)},
		{"DELETE", "/v1/admin/users/{id}", admin(a.handleUsersDelete)},
		{"GET", "/v1/admin/users/{id}/devices", admin(a.handleDevicesList)},
		{"GET", "/v1/admin/revocations", admin(a.handleRevocationsList)},
		{"DELETE", "/v1/admin/users/{id}/devices/{deviceId}", admin(a.handleDeviceRevoke)},
		{"POST", "/v1/admin/users/{id}/lock", admin(a.handleUserLock)},
		{"POST", "/v1/admin/users/{id}/unlock", admin(a.handleUserUnlock)},
		{"GET", "/v1/admin/users/{id}/nhi-key", admin(a.handleNHIKeyGet)},
		{"PUT", "/v1/admin/users/{id}/nhi-key", admin(a.handleNHIKeySet)},
		{"DELETE", "/v1/admin/users/{id}/nhi-key", admin(a.handleNHIKeyDelete)},

		// A server admin reaches the roles their server owns and no other.
		{"GET", "/v1/admin/roles", serverAdmin(a.handleRolesList)},
		{"GET", "/v1/admin/roles/{id}/export", serverAdmin(a.handleRoleExport)},
		{"POST", "/v1/admin/roles", serverAdmin(a.handleRolesCreate)},
		{"PATCH", "/v1/admin/roles/{id}", serverAdmin(a.handleRolesUpdate)},
		{"DELETE", "/v1/admin/roles/{id}", serverAdmin(a.handleRolesDelete)},
		{"GET", "/v1/admin/roles/{id}/implications", admin(a.handleImplicationsList)},
		{"POST", "/v1/admin/roles/{id}/implications", admin(a.handleImplicationCreate)},
		{"DELETE", "/v1/admin/roles/{id}/implications/{implicationId}", admin(a.handleImplicationDelete)},

		{"GET", "/v1/admin/assignments", admin(a.handleAssignmentsList)},
		{"POST", "/v1/admin/assignments", admin(a.handleAssignmentsCreate)},
		{"DELETE", "/v1/admin/assignments/{id}", admin(a.handleAssignmentsDelete)},

		{"GET", "/v1/admin/packs", admin(a.handlePacksList)},
		{"POST", "/v1/admin/packs", admin(a.handlePacksCreate)},
		{"DELETE", "/v1/admin/packs/{id}", admin(a.handlePackDelete)},
		{"POST", "/v1/admin/packs/{id}/bindings", admin(a.handlePackBind)},
		{"DELETE", "/v1/admin/packs/{id}/bindings/{bindingId}", admin(a.handlePackUnbind)},

		{"GET", "/v1/admin/sessions", admin(a.handleSessionsList)},
		{"POST", "/v1/admin/sessions/{id}/revoke", admin(a.handleSessionRevoke)},
		{"POST", "/v1/admin/sessions/revoke", admin(a.handleSessionsBulkRevoke)},
		{"POST", "/v1/admin/signing-keys/rotate", admin(a.handleSigningKeyRotate)},
		{"GET", "/v1/admin/signing-keys", admin(a.handleSigningKeysList)},
		// The client assertion key can sign in as any agent at the identity
		// provider, so no area grant and no server's admin role opens it.
		{"POST", "/v1/admin/signing-keys/client-assertion/rotate", a.requireFullAdmin(a.handleClientAssertionKeyRotate)},
		{"POST", "/v1/admin/signing-keys/client-assertion/{kid}/retire", a.requireFullAdmin(a.handleClientAssertionKeyRetire)},
		{"GET", "/v1/admin/sessions/{id}/transcript", admin(a.handleSessionTranscript)},
		{"GET", "/v1/admin/transcripts", admin(a.handleTranscriptsList)},
		{"GET", "/v1/admin/transcripts/search", admin(a.handleTranscriptSearch)},

		{"GET", "/v1/admin/audit", admin(a.handleAuditList)},
		{"GET", "/v1/admin/changes", admin(a.handleChangesList)},
		{"GET", "/v1/admin/drafts", a.requireDrafts(a.handleDraftsList)},
		{"POST", "/v1/admin/drafts", a.requireDrafts(a.handleDraftCreate)},
		{"POST", "/v1/admin/drafts/check", a.requireDrafts(a.handleDraftCheck)},
		{"GET", "/v1/admin/drafts/{id}", a.requireDrafts(a.handleDraftGet)},
		{"PUT", "/v1/admin/drafts/{id}", a.requireDrafts(a.handleDraftUpdate)},
		{"POST", "/v1/admin/drafts/{id}/discard", a.requireDrafts(a.handleDraftDiscard)},
		{"POST", "/v1/admin/drafts/{id}/revert", a.requireDrafts(a.handleDraftRevert)},
		{"POST", "/v1/admin/drafts/{id}/publish", a.requireDrafts(a.handleDraftPublish)},
		{"POST", "/v1/admin/drafts/{id}/rebase", a.requireDrafts(a.handleDraftRebase)},
		// Contact dials the address a draft names, so only a person with the
		// apps standing reaches it, and no drafts grant opens it.
		{"POST", "/v1/admin/drafts/{id}/contact", a.requireContact(a.contactHandler(newContactDialer()))},
		{"POST", "/v1/admin/policies/validate", admin(a.handlePolicyValidate)},
		{"POST", "/v1/admin/policies/simulate", admin(a.handlePolicySimulate)},

		{"GET", "/v1/admin/apps", serverAdmin(a.handleAppsList)},
		{"POST", "/v1/admin/apps", serverAdmin(a.handleAppsInstall)},
		{"DELETE", "/v1/admin/apps/{id}", admin(a.handleAppsDelete)},
		{"GET", "/v1/admin/apps/{id}/logs", serverAdmin(a.handleAppsLogs)},
		{"POST", "/v1/admin/apps/{id}/health", serverAdmin(a.handleAppRecheck)},
		{"POST", "/v1/admin/apps/{id}/enable", serverAdmin(a.handleAppEnable)},
		{"POST", "/v1/admin/apps/{id}/disable", serverAdmin(a.handleAppDisable)},
		{"GET", "/v1/admin/tools", admin(a.handleToolsList)},
		{"GET", "/v1/admin/catalog/preview", admin(a.handleCatalogPreview)},
		{"POST", "/v1/admin/apps/{id}/secrets", serverAdmin(a.handleAppSecretSet)},
		{"GET", "/v1/admin/apps/{id}/secrets", serverAdmin(a.handleAppSecretsList)},
		{"DELETE", "/v1/admin/apps/{id}/secrets", serverAdmin(a.handleAppSecretDelete)},
		{"DELETE", "/v1/admin/apps/{id}/secrets/{role}", serverAdmin(a.handleAppRoleSecretDelete)},
		{"POST", "/v1/admin/apps/{id}/bindings", serverAdmin(a.handleAppBindingCreate)},
		{"GET", "/v1/admin/bindings", serverAdmin(a.handleBindingsList)},
		{"DELETE", "/v1/admin/bindings/{id}", serverAdmin(a.handleBindingDelete)},
		// Literal segment, so it wins over the {id} wildcard above.
		{"POST", "/v1/admin/apps/import", admin(a.handleAppsImport)},
		{"GET", "/v1/admin/oauth/providers", admin(a.handleOAuthProviders)},
		{"GET", "/v1/admin/access/grant-only", admin(a.handleAccessGrantOnly)},

		{"GET", "/v1/admin/approvals", admin(a.handleApprovalsList)},
		{"GET", "/v1/admin/approvals/{id}", admin(a.handleApprovalGet)},
		{"POST", "/v1/admin/approvals/{id}/approve", a.requirePersonClient(a.handleApprovalApprove)},
		{"POST", "/v1/admin/approvals/{id}/deny", a.requirePersonClient(a.handleApprovalDeny)},
		// Channel status card + test-send. The literal "channels"
		// segment wins over the {id} wildcard above, so no id may collide.
		{"GET", "/v1/admin/approvals/channels", admin(a.handleApprovalChannels)},
		{"POST", "/v1/admin/approvals/channels/{name}/test", admin(a.handleApprovalChannelTest)},
		{"POST", "/v1/approval/callbacks/slack", a.handleSlackCallback},

		// Mobile approver surface. Enrolment is unauthenticated (the one-time
		// enroll token is the credential); the rest require a use=approver
		// device token.
		{"POST", "/v1/approver/enroll", a.handleApproverEnroll},
		// Device-key-signed token refresh, NO bearer (an expired token must not
		// block its own refresh); the device signature over a fetched challenge
		// is the credential.
		{"POST", "/v1/approver/refresh/challenge", a.handleApproverRefreshChallenge},
		{"POST", "/v1/approver/refresh", a.handleApproverRefresh},
		{"GET", "/v1/approver/pending", a.requireApprover(a.handleApproverPending)},
		{"POST", "/v1/approver/decide", a.requireApprover(a.handleApproverDecide)},
		{"PUT", "/v1/approver/push", a.requireApprover(a.handleApproverPushPut)},
		{"DELETE", "/v1/approver/push", a.requireApprover(a.handleApproverPushDelete)},
		{"GET", "/v1/approver/history", a.requireApprover(a.handleApproverHistory)},
		// Self-unenroll (0.66.0): the device's own bearer retires the calling
		// enrollment; same effect as the admin revoke below, attributed via=self.
		{"DELETE", "/v1/approver/enrollment", a.requireApprover(a.handleApproverSelfUnenroll)},
		{"POST", "/v1/admin/approvers/enroll-token", admin(a.handleApproverEnrollToken)},
		{"POST", "/v1/approvals/self/enroll-token", a.requirePersonClient(a.handleSelfEnrollToken)},
		// Whoami for both browser surfaces (0.83.0).
		{"GET", "/v1/self", a.requireIdentified(a.handleSelf)},
		{"GET", "/v1/self/servers", a.requireUser(a.handleSelfServers)},
		{"GET", "/v1/admin/approvers", admin(a.handleApproversList)},
		{"DELETE", "/v1/admin/approvers/{id}", admin(a.handleApproverDeviceDelete)},

		{"GET", "/v1/admin/config", admin(a.handleConfigGet)},
		{"GET", "/v1/admin/overview", admin(a.handleOverviewGet)},
		{"GET", "/v1/admin/sinks", admin(a.handleSinksList)},
		{"POST", "/v1/admin/sinks/{name}/replay", admin(a.handleSinkReplay)},

		{"GET", "/v1/admin/attestation-hashes", admin(a.handleAttestationHashesList)},
		{"POST", "/v1/admin/attestation-hashes", admin(a.handleAttestationHashCreate)},
		{"DELETE", "/v1/admin/attestation-hashes/{id}", admin(a.handleAttestationHashDelete)},

		{"POST", "/v1/admin/api-tokens", admin(a.handleAPITokenCreate)},
		{"GET", "/v1/admin/api-tokens", admin(a.handleAPITokenList)},
		{"DELETE", "/v1/admin/api-tokens/{id}", admin(a.handleAPITokenRevoke)},
	}
}

type route struct {
	method  string
	pattern string
	handler http.HandlerFunc
}

// consoleNoCache marks console assets must-revalidate. With no validators on
// embedded files, revalidation degrades to a refetch: a few hundred KB per
// console load, the price of never rendering a stale bundle.
func consoleNoCache(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-cache")
		next.ServeHTTP(w, r)
	})
}

func (a *App) routes() http.Handler {
	mux := http.NewServeMux()
	for _, rt := range a.routeTable() {
		mux.HandleFunc(rt.method+" "+rt.pattern, rt.handler)
	}
	mux.Handle("GET /metrics", metricsAuth(a.cfg.Server.MetricsToken, a.metrics.Handler()))
	// The embedded console app. Outside /v1: static assets, not
	// the REST contract; the app talks to /v1 same-origin (no CORS). The
	// handler answers index.html for app routes so a reload deep in the app
	// lands in the app. no-cache: the assets are compiled into the binary and
	// embed.FS carries no modtime, so responses have no validators; anything
	// a browser cached from an OLDER binary would otherwise survive an
	// upgrade indefinitely.
	mux.Handle("GET /console/", consoleNoCache(http.StripPrefix("/console/", console.Handler())))
	mux.Handle("GET /console", http.RedirectHandler("/console/", http.StatusMovedPermanently))
	// The self-service page: the same dist as the console, its own
	// entry page, and its worker served from its own directory so the
	// worker's scope is the page and never the origin root. Same-origin /v1,
	// main listener only: browsers cannot pin SPKI, so the pinned approver
	// listener stays app-only. /approvals/ redirects here for good, since
	// older deny sentences and bookmarks carry that address.
	mux.Handle("GET /self-service/", consoleNoCache(http.StripPrefix("/self-service/", console.SelfServiceHandler())))
	mux.Handle("GET /self-service", http.RedirectHandler("/self-service/", http.StatusMovedPermanently))
	mux.Handle("GET /approvals/", http.RedirectHandler("/self-service/", http.StatusMovedPermanently))
	mux.Handle("GET /approvals", http.RedirectHandler("/self-service/", http.StatusMovedPermanently))
	// The door: / lands on the self-service
	// page, which offers admins the console. 302 on purpose: the door choice
	// must stay revisable without fighting a browser's permanent-redirect
	// cache.
	mux.Handle("GET /{$}", http.RedirectHandler("/self-service/", http.StatusFound))
	// The MCP gateway PEP. Outside /v1: it speaks MCP streamable
	// HTTP, not the admin REST contract. /mcp/{server} is the same handler
	// limited to one server, with its own tool names and its views.
	mux.HandleFunc("/mcp", a.handleMCP)
	mux.HandleFunc("/mcp/{server}", a.handleMCP)
	// SCIM 2.0 provisioning. Outside /v1: its contract is
	// spec/scim-profile, not openapi.yaml. Mounted as one subtree, so its
	// writes run under the rule of an admin write (scimHandler).
	mux.Handle("/scim/v2/", a.scimHandler())
	// Standalone: Straza is its own OIDC issuer. Enterprise mounts the
	// same issuer for two callers: NHIs authenticate here with per-NHI keys
	// on the client_credentials grant (idp.json's nhi_issuer
	// names this mount), and the break-glass admin signs in on the login
	// page, which LoginOnly closes to every other account, so the lockout
	// guarantee holds when the external IdP cannot sign anyone in. Every
	// other human stays at the external IdP. NHIKeys enables the grant;
	// identity policy lives in nhiKeyLookup.
	iss := authn.NewIssuer(a.store.Users(), a.tokens, a.cfg.Server.PublicURL)
	iss.NHIKeys = a.nhiKeyLookup
	// Judged grant outcomes enter the chain (spec/events rev 22): the hooks
	// hand over post-zeroing identity, so an attacker-chosen client_id never
	// becomes chained evidence.
	iss.OnNHIGrant = func(userID, username string, r *http.Request) {
		a.emitAuthnLogin(r, "success", authnFields{
			Via: "client-assertion", User: username, UserID: userID,
		})
	}
	iss.OnNHIGrantFailed = func(userID, username, reason string, r *http.Request) {
		a.emitAuthnLogin(r, "failure", authnFields{
			Via: "client-assertion", User: username, UserID: userID, Reason: reason,
		})
	}
	iss.OnLogin = func(userID, username string) {
		if username != BreakGlassUsername {
			return
		}
		// Every break-glass authentication is an alarm, not noise: the
		// account exists for lockout emergencies only.
		a.log.Warn("BREAK-GLASS LOGIN: the emergency admin authenticated", "user", username)
		a.emitEventCtx(context.Background(), "straza.identity.updated", map[string]any{
			"id": userID, "action": "breakglass.login",
		})
	}
	// Failed password submits enter the chain (spec/events rev 21): the
	// hook hands over the post-zeroing username, so an unknown account
	// emits no user field at all.
	iss.OnLoginFailed = func(username string, r *http.Request) {
		a.emitAuthnLogin(r, "failure", authnFields{
			Via: "password", User: username,
			Reason: "invalid username or password",
		})
	}
	if a.cfg.Profile == config.ProfileEnterprise {
		iss.LoginOnly = BreakGlassUsername
	}
	iss.Routes(mux)
	// Public-surface hardening (hardening.go): body caps on every route
	// without its own bulk contract, and the per-IP login throttle on every
	// unauthenticated credential route: the password submit in both
	// profiles, and the token endpoint in enterprise, where the NHI grant
	// is judged (standalone's token endpoint stays unthrottled: the
	// device-code poll is legitimately frequent; the break-glass poll in
	// enterprise sits under the limit at the advertised interval). All
	// inert at zero.
	var h http.Handler = mux
	h = loginLimit(ratelimit.New(), a.cfg.Server.LoginPerIPRPS, h)
	if a.cfg.Profile == config.ProfileEnterprise {
		h = grantLimit(ratelimit.New(), a.cfg.Server.LoginPerIPRPS, h)
	}
	// Browser hardening baseline: global, both
	// listeners. The full resource CSP is not set here.
	h = securityHeaders(h)
	// Request correlation (middleware.go): requestID outermost so every
	// answer, a recovered panic included, carries X-Request-Id; accessLog
	// (accesslog.go) inside it so the record carries the correlation id and
	// outside recoverPanics so a panic's 500 is measured; the approver
	// listener wraps this same handler and inherits all three layers.
	return a.requestID(a.accessLog(a.recoverPanics(bodyCap(a.cfg.Server.MaxBodyBytes, h))))
}

func (a *App) handleJWKS(w http.ResponseWriter, r *http.Request) {
	doc, err := a.tokens.JWKS()
	if err != nil {
		a.fail(w, r, http.StatusInternalServerError, "jwks unavailable", err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "max-age=60")
	_, _ = w.Write(doc)
}

func handleHealthz(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func handleReadyz(components map[string]Pinger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
		defer cancel()

		out := wire.ReadyStatus{Status: "ok", Components: map[string]string{}}
		code := http.StatusOK
		for name, p := range components {
			if err := p.Ping(ctx); err != nil {
				out.Components[name] = err.Error()
				out.Status = "degraded"
				code = http.StatusServiceUnavailable
			} else {
				out.Components[name] = "ok"
			}
		}
		writeJSON(w, code, out)
	}
}

func (a *App) handleVersion(w http.ResponseWriter, _ *http.Request) {
	out := wire.VersionStatus{Info: version.Get(), Profile: a.cfg.Profile, Runtimes: a.runtimes}
	if a.tlsSPKIPin != "" && a.approverCert != nil {
		// Pin follows the host, here too: behind an ingress the
		// advertised URL never presents strazad's own leaf, so pairing them
		// would invite a hand-copied pin that bricks the phone.
		servers, tier := a.enrollServersTier()
		av := &wire.ApproverVersion{TLSSPKIPin: a.enrollPin(tier), AutoMinted: a.approverCert.AutoMinted}
		if len(servers) > 0 {
			av.PublicURL = servers[0]
		}
		if !a.approverCert.NotAfter.IsZero() {
			av.CertNotAfter = a.approverCert.NotAfter.UTC().Format(time.RFC3339)
		}
		if a.approverCert.AutoMinted {
			av.CertFile = a.approverCert.CertFile
		}
		out.Approver = av
	}
	writeJSON(w, http.StatusOK, out)
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}
