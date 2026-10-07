// build.go holds the strazad assembly sequence: build() is the ordered
// wiring and each buildXxx below constructs one subsystem. The call order in
// build() is load-bearing; the comments at each call site say why.

package server

import (
	"context"
	"crypto/tls"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/strazahq/straza/internal/approval"
	"github.com/strazahq/straza/internal/authn"
	"github.com/strazahq/straza/internal/automint"
	"github.com/strazahq/straza/internal/bodystore"
	"github.com/strazahq/straza/internal/classifier"
	"github.com/strazahq/straza/internal/config"
	"github.com/strazahq/straza/internal/events"
	"github.com/strazahq/straza/internal/identity"
	"github.com/strazahq/straza/internal/manager"
	"github.com/strazahq/straza/internal/secrets"
	"github.com/strazahq/straza/internal/sentinel"
	"github.com/strazahq/straza/internal/server/metrics"
	"github.com/strazahq/straza/internal/server/ratelimit"
	"github.com/strazahq/straza/internal/sinks"
	"github.com/strazahq/straza/internal/snapshot"
	"github.com/strazahq/straza/internal/spine"
	"github.com/strazahq/straza/internal/store"
)

// build assembles an App over an already-opened store (the seam tests use to
// instrument request-path DB access).
func build(ctx context.Context, cfg config.Config, log *slog.Logger, st store.Store) (*App, error) {
	a := &App{cfg: cfg, log: log, instance: "strazad/" + uuid.NewString(),
		ingestLimiter: ratelimit.New(), lifetime: ctx, draftsWake: make(chan struct{}, 1)}
	a.store = st
	// Config notices (removed or ignored keys the operator should delete)
	// are the first lines of the boot log, before any subsystem starts.
	for _, n := range cfg.Notices {
		log.Warn(n)
	}

	roleAreaScopes, err := buildRoleAreaScopes(cfg.Admin, log)
	if err != nil {
		a.close()
		return nil, err
	}
	a.roleAreaScopes = roleAreaScopes

	// Approver TLS auto-mint: fills the unset half of server.approverTLS
	// (persisted first-boot pair, listen :8443, best-effort LAN publicUrl)
	// BEFORE anything reads it: the listener wiring, the enroll QR builder,
	// and the boot log all see the completed config. Explicit fields win.
	urlConfigured := a.cfg.Server.ApproverTLS.PublicURL != ""
	autoMinted, err := automint.EnsureApproverTLS(&a.cfg, log)
	if err != nil {
		a.close()
		return nil, fmt.Errorf("server.approverTLS auto-mint: %w", err)
	}
	a.approverURLDerived = !urlConfigured && a.cfg.Server.ApproverTLS.PublicURL != ""
	cfg = a.cfg // keep the local copy the rest of build reads in sync

	fail := func(err error) (*App, error) {
		a.close()
		return nil, err
	}

	if err := a.buildStore(ctx, cfg, log, st); err != nil {
		return fail(err)
	}
	if err := a.buildIdentity(ctx, cfg, log, st); err != nil {
		return fail(err)
	}
	if err := a.buildStandaloneAdmin(ctx, cfg); err != nil {
		return fail(err)
	}
	// The break-glass admin exists on every profile, fresh and upgraded
	// stores alike (unified role model: the lockout guarantee must not
	// depend on how the deployment was born). AFTER bootstrapAdmin: the
	// fresh-store check there keys on an empty users table.
	if err := a.ensureBreakGlass(ctx); err != nil {
		return fail(fmt.Errorf("break-glass admin: %w", err))
	}
	// The reserved product roles, the self-enrollment pair and the global
	// MCP admin, exist on every profile so the IdM (or an admin) can assign
	// them; the gates check them by name. Every server then gets its own
	// admin role, minted for the rows that predate the field.
	if err := a.ensureProductRoles(ctx); err != nil {
		return fail(fmt.Errorf("product roles: %w", err))
	}
	if err := a.ensureAppAdminRoles(ctx); err != nil {
		return fail(fmt.Errorf("server admin roles: %w", err))
	}
	if err := a.buildBus(ctx, cfg, log); err != nil {
		return fail(err)
	}
	if err := a.buildPolicySnapshots(ctx, cfg, log, st); err != nil {
		return fail(err)
	}
	if err := a.buildHarnessConfigs(ctx); err != nil {
		return fail(fmt.Errorf("harness config: %w", err))
	}
	a.buildPDP(cfg, st)
	if err := a.buildApproval(cfg, log, st, a.bus); err != nil {
		return fail(err)
	}
	a.buildPushHub(log, a.bus)
	// No close on this error path, like the body-store credential-file reads,
	// which return without tearing down.
	if err := a.buildCaptureAndConsumers(cfg, log, st, a.bus); err != nil {
		return nil, err
	}
	if err := a.buildSinksAndSentinel(cfg, log, a.bus); err != nil {
		return fail(err)
	}
	// Rebuild the denylist from persisted revocations so a freshly started
	// pod is not briefly permissive before the stream replays. Control
	// plane: a DB read at boot, never on a request path. A failed read is
	// fatal (fail closed): serving with an empty denylist is exactly the
	// permissive window this rebuild exists to prevent.
	if err := a.rebuildDenylist(ctx, st); err != nil {
		return fail(err)
	}
	if err := a.buildSecrets(ctx, cfg, log, st); err != nil {
		return fail(err)
	}
	if err := a.buildManagerAndGateway(ctx, cfg, log, st); err != nil {
		return fail(err)
	}
	// Multi-pod convergence: a config change made through another pod
	// reaches this pod through the event spine and runs the one apply of
	// drafts_apply.go. The consumer subscribes here and then applies live
	// state once, so this pod serves one consistent read from its first
	// request, and a change after that read arrives as an event.
	a.buildConvergence(log, a.bus)
	if err := a.convCons.Subscribe(ctx); err != nil {
		return fail(fmt.Errorf("config apply at boot: %w, so strazad did not start. Check that its database is reachable, then start it again", err))
	}
	if err := a.buildAppsWatcher(cfg, log); err != nil {
		return fail(err)
	}
	if err := a.buildMainListener(cfg); err != nil {
		return fail(err)
	}
	if err := a.buildApproverListener(cfg, autoMinted); err != nil {
		return fail(err)
	}
	return a, nil
}

// buildStore migrates the already-opened store.
func (a *App) buildStore(ctx context.Context, cfg config.Config, log *slog.Logger, st store.Store) error {
	if err := st.Migrate(ctx); err != nil {
		return err
	}
	log.Info("store ready", "driver", cfg.Store.Driver)
	return nil
}

// buildIdentity wires the token service, the role resolver, the persistent
// project identity and the optional external OIDC verifier.
func (a *App) buildIdentity(ctx context.Context, cfg config.Config, log *slog.Logger, st store.Store) error {
	tokens, err := authn.NewTokenService(ctx, st.SigningKeys(), cfg.Server.PublicURL, authn.DefaultTokenTTL)
	if err != nil {
		return err
	}
	a.tokens = tokens
	a.resolver = identity.NewResolver(st)

	pid, err := resolveProjectID(ctx, st)
	if err != nil {
		return fmt.Errorf("project identity: %w", err)
	}
	a.projectID = pid
	log.Info("project identity", "id", pid, "name", a.projectName())

	if cfg.OIDC.Issuer != "" {
		external, err := authn.NewExternalVerifier(ctx, cfg.OIDC.Issuer, cfg.OIDC.DiscoveryURL, cfg.OIDC.ClientID, st.Users(), cfg.OIDC.JITProvision)
		if err != nil {
			return err
		}
		external.BootstrapUsername = cfg.OIDC.BootstrapAdmin
		external.ProtectedUsername = BreakGlassUsername
		a.external = external
		log.Info("external OIDC configured", "issuer", cfg.OIDC.Issuer, "discoveryUrl", cfg.OIDC.DiscoveryURL,
			"jit", cfg.OIDC.JITProvision, "bootstrapAdmin", cfg.OIDC.BootstrapAdmin != "")
	}
	return nil
}

// buildStandaloneAdmin seeds the first admin (and, on a fresh store, the
// starter guardrail policy) in the standalone profile.
func (a *App) buildStandaloneAdmin(ctx context.Context, cfg config.Config) error {
	if cfg.Profile == config.ProfileStandalone {
		fresh, err := a.bootstrapAdmin(ctx)
		if err != nil {
			return fmt.Errorf("bootstrap admin: %w", err)
		}
		if fresh {
			// Seeded before the snapshot service loads, so the very first
			// compiled snapshot already carries the starter guardrail.
			if err := a.bootstrapStarterPolicy(ctx); err != nil {
				return fmt.Errorf("bootstrap starter policy: %w", err)
			}
		}
	}
	return nil
}

// buildBus starts the event bus.
func (a *App) buildBus(ctx context.Context, cfg config.Config, log *slog.Logger) error {
	bus, err := events.Start(ctx, cfg)
	if err != nil {
		return err
	}
	a.bus = bus
	log.Info("event bus ready", "embedded", cfg.Events.Embedded)
	return nil
}

// buildPolicySnapshots loads the snapshot signing keys and the policy snapshot
// service, then loads the current snapshot.
func (a *App) buildPolicySnapshots(ctx context.Context, cfg config.Config, log *slog.Logger, st store.Store) error {
	snapKeys, err := authn.NewSnapshotKeys(ctx, st.SigningKeys())
	if err != nil {
		return err
	}
	a.snapKeys = snapKeys
	// The snapshot service's publisher rides the outbox (durable JetStream
	// path) AND, for policy activations, fires the low-latency core-NATS
	// nudge so idle connected daemons adopt within seconds instead of a poll
	// interval (spec/events §5 rev 7). Only the recompiling pod publishes;
	// converge consumers reload without re-publishing, so one nudge per
	// activation cluster-wide.
	a.snapshots = snapshot.New(st, snapKeys, func(c context.Context, subject string, data map[string]any) {
		a.emitEventCtx(c, subject, data)
		if subject == "straza.policy.updated" {
			a.pushPolicyNudge(data)
			// Approval-gated tools carry an injected `_straza_justification`
			// schema field derived from the active snapshot: an activation
			// can change which tools are gated, so live MCP clients must
			// re-list (the catalog cache key is already snapshot-aware).
			if a.gateway != nil {
				a.gateway.Invalidate()
			}
		}
	}, cfg.Governance.LocalToolDefault, int64(cfg.Governance.OfflineGraceTTL.Seconds()), log)
	if err := a.snapshots.Load(ctx); err != nil {
		return fmt.Errorf("policy snapshot: %w", err)
	}
	log.Info("policy snapshot ready", "id", a.snapshots.Current().ID)
	// A saved edit that a release before drafts stored in its row moves
	// into its set's draft before a route or a boot audit reads the row.
	if err := a.convertPolicies(ctx); err != nil {
		return fmt.Errorf("policy conversion at boot: %w, so strazad did not start. Check that its database is reachable, then start it again", err)
	}
	// Revision 15 honesty line: active sets whose approve pools predate the
	// approver role kind keep governing, and the operator hears it once.
	a.warnLegacyApprovePools(ctx)
	// Revision 16 honesty line: same contract for match.roles selectors
	// that predate the application-only rule.
	a.warnLegacyMatchRoles(ctx)
	// Revision 18 honesty line: same contract for rules that carry the
	// retired obligations list.
	a.warnLegacyObligations(ctx)
	return nil
}

// buildPDP constructs the in-memory decision-path state: subject cache,
// denylist, audit spool, metrics and the classify backend.
func (a *App) buildPDP(cfg config.Config, st store.Store) {
	a.subjects = newSubjectCache()
	a.denylist = newDenylist(a.log)
	a.audit = newAuditSpool(cfg.Governance.AuditBackpressure == config.BackpressureBlock, a.log, func(c context.Context, e store.OutboxEvent) error {
		_, err := st.Outbox().Insert(c, e)
		return err
	})
	a.audit.insertBatch = func(c context.Context, es []store.OutboxEvent) error {
		_, err := st.Outbox().InsertBatch(c, es)
		return err
	}
	a.metrics = metrics.New(func() float64 { return float64(a.audit.dropped.Load()) },
		func() float64 { return float64(a.audit.lost.Load()) })
	// The mode:classify verdict backend: always the embedded heuristic, with
	// no config surface.
	a.classifier = classifier.NewHeuristic()
}

// buildApproval constructs the human-approval service and the outbox relay.
func (a *App) buildApproval(cfg config.Config, log *slog.Logger, st store.Store, bus *events.Bus) error {
	var err error
	// The human-approval service is ALWAYS constructed: the console is
	// channel #0 and always on, so there is no enable knob. It loads or creates
	// the shared decision-token key from the settings table.
	a.approval, err = approval.New(st, bus, a.resolver, cfg.Approval, log)
	if err != nil {
		return fmt.Errorf("approval service: %w", err)
	}
	// The approver surface honors the Straza-lane kill switch through the same
	// in-memory denylist as every other credential: an admin/external
	// lock lands on the revocation lane, never on users.status, so a status
	// check alone would let a locked user's phone keep minting capability.
	a.approval.UserBlocked = a.denylist.userBlocked
	a.relay = spine.NewRelay(st, bus, log)
	return nil
}

// buildPushHub subscribes the gateway-edge push fan-out.
func (a *App) buildPushHub(log *slog.Logger, bus *events.Bus) {
	// Gateway-edge push: one straza.push.> core subscription per
	// pod, fanned out to this pod's SSE subscribers. A failed subscribe
	// leaves pushHub nil; /v1/push then 503s and daemons ride their poll
	// lane (an accelerator must degrade to slower, never to "connected but
	// deaf").
	hub := newPushHub()
	if _, err := bus.SubscribeCore("straza.push.>", hub.fanout); err != nil {
		log.Warn("edge push lane down (subscribe failed); daemons will poll", "err", err)
	} else {
		a.pushHub = hub
	}
}

// buildCaptureAndConsumers constructs the audit, conversation and revocation
// consumers and the optional transcript body store between them.
func (a *App) buildCaptureAndConsumers(cfg config.Config, log *slog.Logger, st store.Store, bus *events.Bus) error {
	a.auditCons = spine.NewAuditConsumer(st, bus, log)
	if bs := cfg.Capture.BodyStore; bs.Type == "s3" {
		access, secret := bs.AccessKey, bs.SecretKey
		if bs.AccessKeyFile != "" {
			raw, err := os.ReadFile(bs.AccessKeyFile) // #nosec G304 -- operator-configured credential file
			if err != nil {
				return fmt.Errorf("capture.bodyStore.accessKeyFile: %w", err)
			}
			access = strings.TrimSpace(string(raw))
		}
		if bs.SecretKeyFile != "" {
			raw, err := os.ReadFile(bs.SecretKeyFile) // #nosec G304 -- operator-configured credential file
			if err != nil {
				return fmt.Errorf("capture.bodyStore.secretKeyFile: %w", err)
			}
			secret = strings.TrimSpace(string(raw))
		}
		s3, err := bodystore.NewS3(bodystore.S3Opts{
			Endpoint: bs.Endpoint, Bucket: bs.Bucket, Prefix: bs.Prefix,
			Region: bs.Region, AccessKey: access, SecretKey: secret,
			UseSSL: !bs.DisableSSL,
		})
		if err != nil {
			return err
		}
		a.bodyStore = s3
		log.Info("transcript body store configured (D36)",
			"endpoint", bs.Endpoint, "bucket", bs.Bucket, "prefix", bs.Prefix)
	}
	a.turnsCons = spine.NewConversationConsumer(st, bus, log, a.bodyStore)
	a.revCons = spine.NewRevocationConsumer(bus, a.denylist, log)
	return nil
}

// buildSinksAndSentinel constructs the configured SIEM sinks and the optional
// audit sentinel.
func (a *App) buildSinksAndSentinel(cfg config.Config, log *slog.Logger, bus *events.Bus) error {
	var err error
	a.sinks, a.sinksClose, err = sinks.Build(cfg, bus, log, a.metrics)
	if err != nil {
		return err
	}
	if cfg.Governance.Sentinel.Enabled {
		// The audit sentinel: async detection over the audit stream, entirely
		// off the request path. Alert-only; no auto-revoke exists.
		a.sentinel = sentinel.New(bus, cfg.Governance.Sentinel, log)
		log.Info("audit sentinel enabled", "denyBurstWarn", cfg.Governance.Sentinel.DenyBurstWarn,
			"denyBurstCritical", cfg.Governance.Sentinel.DenyBurstCritical)
	}
	return nil
}

// rebuildDenylist replays persisted revocations into the in-memory denylist.
func (a *App) rebuildDenylist(ctx context.Context, st store.Store) error {
	revs, err := st.Revocations().List(ctx)
	if err != nil {
		return fmt.Errorf("denylist rebuild: %w", err)
	}
	for _, rev := range revs {
		switch rev.Kind {
		case store.RevokeUser:
			a.denylist.revokeUser(rev.TargetID)
		case store.RevokeSession:
			a.denylist.revokeSession(rev.TargetID)
		case store.RevokeDevice:
			a.denylist.RevokeDevice(rev.TargetID)
		}
	}
	return nil
}

// buildSecrets loads the KEK, the secrets broker and the per-user OAuth
// connect plumbing (providers, exchange client, grant refresher).
func (a *App) buildSecrets(ctx context.Context, cfg config.Config, log *slog.Logger, st store.Store) error {
	provider, err := secrets.LoadOrCreateKEK(cfg.KEKFile())
	if err != nil {
		return err
	}
	// The client assertion key is sealed under the same KEK as the stored
	// credentials, so a replica with another KEK refuses to boot here.
	if a.assertionKeys, err = authn.NewClientAssertionKeys(ctx, st.SigningKeys(), provider, time.Now()); err != nil {
		return err
	}
	a.broker = secrets.NewBroker(st, provider)
	if err := a.broker.Refresh(ctx); err != nil {
		return err
	}
	a.oauthProviders, err = buildOAuthProviders(cfg)
	if err != nil {
		return err
	}
	a.oauthHTTP = newOAuthHTTPClient()
	a.refresher = secrets.NewRefresher(secrets.RefresherOpts{
		Store: st, Broker: a.broker, Providers: a.oauthProviders,
		HTTP: a.oauthHTTP, Log: log,
		Interval: cfg.OAuth.RefreshInterval, Window: cfg.OAuth.RefreshWindow,
	})
	return nil
}

// newOAuthHTTPClient is the client of the people's OAuth code exchange and
// refresh. It follows no redirect: the request carries the client secret and
// a code or refresh token, and a redirecting token endpoint would get them
// posted again at whatever address it names. The agents' lane refuses the
// same way (clientcredentials).
func newOAuthHTTPClient() *http.Client {
	return &http.Client{
		Timeout:       30 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}

// buildManagerAndGateway constructs the app manager and the MCP gateway and
// loads apps and bindings.
func (a *App) buildManagerAndGateway(ctx context.Context, cfg config.Config, log *slog.Logger, st store.Store) error {
	providers := make([]string, 0, len(a.oauthProviders))
	for name := range a.oauthProviders {
		providers = append(providers, name)
	}
	withBlock := a.buildClientTokens(cfg, log)
	a.manager = manager.New(manager.Options{
		Store:                      st,
		Emit:                       a.emitEventCtx,
		Log:                        log,
		Secrets:                    a.broker,
		HealthInterval:             cfg.Apps.HealthInterval,
		OAuthProviders:             providers,
		ConnectPageURL:             strings.TrimRight(cfg.Server.PublicURL, "/") + "/self-service/credentials",
		ClientCredentialsProviders: withBlock,
		ClientTokens:               agentTokens{a.clientTokens},
		AllowLoopbackUpstreams:     cfg.Apps.AllowLoopbackUpstreams,
		RefuseCommand:              cfg.Profile == config.ProfileEnterprise,
	})
	a.runtimes = a.manager.Runtimes()
	a.gateway = newGateway()
	a.manager.OnChange(a.gateway.Invalidate)
	if err := a.manager.Load(ctx); err != nil {
		return err
	}
	if err := a.refreshBindings(ctx); err != nil {
		return err
	}
	return nil
}

// buildConvergence subscribes the multi-pod convergence consumer.
func (a *App) buildConvergence(log *slog.Logger, bus *events.Bus) {
	a.convCons = spine.NewConvergeConsumer(bus, spine.Convergence{
		SelfSource: a.instance,
		// A policy change and a server change both run the whole apply, which
		// reads live state once and moves this replica to it in one order, and
		// so does every subscription, to catch up on what came before it.
		OnSubscribe: a.converge,
		OnPolicy:    a.converge,
		OnApps:      a.converge,
		OnIdentity:  func(context.Context) error { a.resolver.Bump(); return nil },
	}, log)
}

// buildAppsWatcher starts the GitOps apps directory watcher when configured.
func (a *App) buildAppsWatcher(cfg config.Config, log *slog.Logger) error {
	if dir := cfg.AppsDir(); dir != "" {
		if err := os.MkdirAll(dir, 0o750); err != nil {
			return fmt.Errorf("apps dir %s: %w", dir, err)
		}
		a.watcher = manager.NewWatcher(dir, cfg.Apps.PollInterval, log)
		a.watcher.SetPropose(a.proposeFile)
		a.watcher.SetGone(a.fileGone)
		log.Info("apps GitOps watcher ready", "dir", dir)
	}
	return nil
}

// buildMainListener binds the main listener and the HTTP server, with TLS when
// configured.
func (a *App) buildMainListener(cfg config.Config) error {
	ln, err := net.Listen("tcp", cfg.Server.Listen)
	if err != nil {
		return fmt.Errorf("listen %s: %w", cfg.Server.Listen, err)
	}
	a.ln = ln
	// No ReadTimeout/WriteTimeout by design: GET /mcp SSE, the
	// gateway approval hold and approval_await are legitimately long-lived.
	// IdleTimeout bounds idle keep-alive connections only; ErrorLog routes
	// Go's own server lines (TLS handshake noise, superfluous WriteHeader)
	// into the structured log at Warn instead of stderr.
	a.http = &http.Server{
		Handler:           a.routes(),
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout:       120 * time.Second,
		ErrorLog:          slog.NewLogLogger(a.log.Handler(), slog.LevelWarn),
	}
	// The event streams end when Shutdown starts, so Shutdown waits only for
	// real requests and the audit spool's drain (run.go) starts in time.
	a.http.RegisterOnShutdown(a.gateway.streams.close)
	if a.pushHub != nil {
		a.http.RegisterOnShutdown(a.pushHub.close)
	}
	if cfg.TLSEnabled() {
		cert, err := tls.LoadX509KeyPair(cfg.Server.TLS.CertFile, cfg.Server.TLS.KeyFile)
		if err != nil {
			return fmt.Errorf("server.tls: %w", err)
		}
		// TLS 1.2 floor; cipher suites and curve preferences are Go's modern
		// defaults. Certificate rotation requires a restart.
		a.http.TLSConfig = &tls.Config{
			MinVersion:   tls.VersionTLS12,
			Certificates: []tls.Certificate{cert},
		}
		// Cache the leaf SPKI pin ONCE (never per request): the mobile approver
		// enroll QR carries it so the app can bootstrap trust in a private-PKI
		// strazad out of band. Same cert that was just loaded.
		pin, err := automint.SPKIPinFromCertFile(cfg.Server.TLS.CertFile)
		if err != nil {
			return fmt.Errorf("server.tls spki pin: %w", err)
		}
		a.tlsSPKIPin = pin
		a.approverCert = &automint.CertInfo{CertFile: cfg.Server.TLS.CertFile, NotAfter: automint.LeafNotAfter(cert), Leaf: automint.LeafOf(cert)}
	}
	return nil
}

// buildApproverListener binds the dedicated https-only approver listener when
// configured.
func (a *App) buildApproverListener(cfg config.Config, autoMinted bool) error {
	if cfg.Server.ApproverTLS.Listen != "" {
		// Dedicated https-only approver listener (config.ApproverTLS): serves
		// ONLY /v1/approver/* (+ /readyz) so a plaintext-by-necessity main
		// listener can still enroll the https+pin-only phone app. Same modern
		// TLS floor as the main listener. The QR pins THIS listener's
		// certificate; it wins over a main-listener pin because
		// ApproverTLS.PublicURL is what the phone dials.
		at := cfg.Server.ApproverTLS
		cert, err := tls.LoadX509KeyPair(at.CertFile, at.KeyFile)
		if err != nil {
			return fmt.Errorf("server.approverTLS: %w", err)
		}
		aln, err := net.Listen("tcp", at.Listen)
		if err != nil {
			return fmt.Errorf("approver listen %s: %w. Set server.approverTLS.listen to a free port, or server.approverTLS.autoMint: false to run without the dedicated approver surface", at.Listen, err)
		}
		a.approverLn = aln
		// Chain, outermost first: per-IP limit (floods and scanners burn
		// their budget before any work happens), then the surface guard,
		// then the shared routes (which already carry the body caps).
		a.approverHTTP = &http.Server{
			Handler:           perIPLimit(ratelimit.New(), at.PerIPRPS, approverSurfaceOnly(a.http.Handler)),
			ReadHeaderTimeout: 5 * time.Second,
			IdleTimeout:       120 * time.Second,
			ErrorLog:          slog.NewLogLogger(a.log.Handler(), slog.LevelWarn),
			TLSConfig: &tls.Config{
				MinVersion:   tls.VersionTLS12,
				Certificates: []tls.Certificate{cert},
			},
		}
		pin, err := automint.SPKIPinFromCertFile(at.CertFile)
		if err != nil {
			return fmt.Errorf("server.approverTLS spki pin: %w", err)
		}
		a.tlsSPKIPin = pin
		a.approverCert = &automint.CertInfo{CertFile: at.CertFile, NotAfter: automint.LeafNotAfter(cert), AutoMinted: autoMinted, Leaf: automint.LeafOf(cert)}
	}
	return nil
}
