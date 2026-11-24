// run.go holds the strazad supervisor: Run() starts every process-lifetime
// goroutine by name and owns the listen/shutdown ladder; each runXxx below is
// one goroutine body. The names double as the component vocabulary for logs.

package server

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/strazahq/straza/internal/authn"
	"github.com/strazahq/straza/internal/config"
	"github.com/strazahq/straza/internal/spine"
	"github.com/strazahq/straza/internal/version"
)

// Run serves HTTP until ctx is canceled, then shuts down gracefully.
func (a *App) Run(ctx context.Context) error {
	// Event-spine workers: audit spool → outbox, outbox → JetStream, and
	// JetStream → hash-chained audit_log. All use the app-lifecycle
	// context (not request-scoped), which is exactly what a process-lifetime
	// goroutine needs. The audit spool is the exception: its context ends
	// only after the listeners have shut down, so it keeps writing while
	// requests finish, and the store closes only after its drain returns.
	spoolCtx, stopSpool := context.WithCancel(context.WithoutCancel(ctx))
	defer stopSpool()
	spoolDone := make(chan struct{})
	go func() { a.runAuditSpool(spoolCtx); close(spoolDone) }() // #nosec G118
	stopAudit := func() { stopSpool(); <-spoolDone }
	go a.runRelay(ctx)     // #nosec G118
	go a.runManager(ctx)   // #nosec G118
	go a.runRefresher(ctx) // #nosec G118 (OAuth grant rotation)
	// The recorders end with ctx, and Run ends them itself when a listener
	// dies, so both stop paths wait for the pass a recorder is in.
	recCtx, stopRecorders := context.WithCancel(ctx)
	defer stopRecorders()
	a.goRecorder(recCtx, a.runSessionJanitor) // session janitor, control plane
	a.goRecorder(recCtx, a.runKeyRotation)    // signing key reload and rotation, control plane
	a.goRecorder(recCtx, a.runDraftsChecker)  // drafts checker, control plane
	a.goRecorder(recCtx, a.runObjects)        // object lists, control plane
	if a.watcher != nil {
		go a.runAppsWatcher(ctx) // #nosec G118
	}
	if a.cfg.Identity.Entra.Enabled() {
		a.goRecorder(recCtx, a.runEntraSync) // role groups in Entra, control plane
	}
	go a.runAuditConsumer(ctx)        // #nosec G118
	go a.runConversationConsumer(ctx) // #nosec G118 (conversation-turns read model, capture)
	go a.runRetentionJanitor(ctx)     // #nosec G118 (retention janitors, control plane)
	go a.runRevocationConsumer(ctx)   // #nosec G118
	go a.runConvergeConsumer(ctx)     // #nosec G118
	go a.runApprovalService(ctx)      // #nosec G118
	if a.sentinel != nil {
		go a.runSentinel(ctx) // #nosec G118
	}
	for _, runner := range a.sinks {
		go a.runSink(ctx, runner) // #nosec G118
	}

	if a.cfg.Profile == config.ProfileEnterprise && a.http.TLSConfig == nil {
		a.log.Warn("serving PLAINTEXT HTTP in the enterprise profile. Set server.tls.{certFile,keyFile} "+
			"or terminate TLS at your ingress/LB; session tokens and admin API tokens transit this listener. "+
			"The TLS and exposure guide walks both shapes",
			"docs", "https://docs.straza.ai/guides/operate/tls-and-exposure/")
	}
	// Read TLSConfig once, before the serve goroutine starts: net/http's
	// Serve mutates srv.TLSConfig lazily (HTTP/2 setup), so reading it
	// concurrently is a data race (caught by CI -race).
	useTLS := a.http.TLSConfig != nil
	errCh := make(chan error, 2)
	go a.runHTTP(errCh, useTLS)
	serving := 1
	if a.approverHTTP != nil {
		serving++
		go a.runApproverHTTP(errCh)
		// The three facts a phone enrollment stands on, named at boot: the URL
		// the QR advertises, the SPKI pin the app will trust, and the cert
		// behind them (auto-minted or operator-provided).
		a.log.Info("approver surface serving", "addr", a.approverLn.Addr().String(),
			"publicUrl", a.cfg.Server.ApproverTLS.PublicURL,
			"pin", a.tlsSPKIPin,
			"cert", a.cfg.Server.ApproverTLS.CertFile,
			"certExpires", a.approverCert.NotAfter.Format(time.RFC3339),
			"autoMinted", a.approverCert.AutoMinted)
	}
	// The ingress-terminated approver knob, named at boot like its sibling
	// tiers: this URL lands verbatim (slash-trimmed) in enroll payloads.
	if p := a.cfg.Server.ApproverPublicURL; p != "" {
		a.log.Info("approver surface fronted by ingress", "approverPublicUrl", p)
	}
	// publicUrl is announced with the serving line because it is the
	// deployment's identity, not just a link base: the session-token issuer,
	// the discovery base, and the default enroll target all stand on it.
	a.log.Info("strazad serving", "addr", a.Addr(), "profile", a.cfg.Profile,
		"publicUrl", a.cfg.Server.PublicURL, "tls", useTLS, "version", version.Version)

	drain := func() error {
		var first error
		for i := 0; i < serving; i++ {
			if err := <-errCh; err != nil && first == nil {
				first = err
			}
		}
		return first
	}
	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		recorded := a.drainRecorders()
		err := a.http.Shutdown(shutdownCtx)
		if a.approverHTTP != nil {
			if e := a.approverHTTP.Shutdown(shutdownCtx); e != nil && err == nil {
				err = e
			}
		}
		<-recorded
		stopAudit()
		a.close()
		a.log.Info("strazad stopped")
		if serveErr := drain(); serveErr != nil {
			return serveErr
		}
		return err
	case err := <-errCh:
		// One listener died: bring the whole process down (fail closed).
		// Shut the sibling, then surface the original error.
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		stopRecorders()
		recorded := a.drainRecorders()
		_ = a.http.Shutdown(shutdownCtx)
		if a.approverHTTP != nil {
			_ = a.approverHTTP.Shutdown(shutdownCtx)
		}
		for i := 0; i < serving-1; i++ {
			<-errCh //nolint:errcheck // the sibling answers http.ErrServerClosed after the Shutdown above, and the first error is the one returned
		}
		<-recorded
		stopAudit()
		a.close()
		return err
	}
}

func (a *App) close() {
	if a.bus != nil {
		a.bus.Close()
	}
	if a.sinksClose != nil {
		a.sinksClose()
	}
	if a.store != nil {
		if err := a.store.Close(); err != nil {
			a.log.Warn("store close", "err", err)
		}
	}
}

// recorderDrainTimeout bounds how long a stop waits for the recorders: the
// two bounded tries of one record insert, and a margin.
const recorderDrainTimeout = 2*auditInsertTimeout + 2*time.Second

// goRecorder starts a worker whose pass changes the store and then writes
// the chained record of that change. A stop waits for these workers in
// drainRecorders, so one that arrives between the two steps does not lose
// the record.
func (a *App) goRecorder(ctx context.Context, run func(context.Context)) {
	a.recorders.Add(1)
	go func() { // #nosec G118 -- a process-lifetime worker that Run waits for
		defer a.recorders.Done()
		run(ctx)
	}()
}

// drainRecorders waits until every recorder has returned, at most
// recorderDrainTimeout, and closes the returned channel then. It runs beside
// the listeners' shutdown, so it adds nothing to a stop while requests
// finish. A recorder's store calls end with its context and a record insert
// gives up after its own bound, so the wait is short unless the store hangs.
// A recorder still running at the bound leaves an Error line, because the
// record of its last change may never be written.
func (a *App) drainRecorders() <-chan struct{} {
	done := make(chan struct{})
	go func() {
		defer close(done)
		returned := make(chan struct{})
		go func() { a.recorders.Wait(); close(returned) }()
		select {
		case <-returned:
		case <-time.After(recorderDrainTimeout):
			a.log.Error("strazad stops while a background worker is still writing, so the audit record of its last change may be missing. "+
				"Check that the database answers, then compare the newest session, signing key and draft changes with the audit log",
				"waited", recorderDrainTimeout.String())
		}
	}()
	return done
}

// runAuditSpool writes the audit spool to the outbox until ctx ends, then
// drains what it holds; Run ends ctx after the listeners have shut down.
func (a *App) runAuditSpool(ctx context.Context) {
	a.audit.run(ctx)
}

// runRelay publishes the outbox to JetStream.
func (a *App) runRelay(ctx context.Context) {
	a.relay.Run(ctx)
}

// runManager runs the app manager (health, lifecycle).
func (a *App) runManager(ctx context.Context) {
	a.manager.Run(ctx)
}

// runRefresher rotates per-user OAuth grants on the one replica that holds
// the refresh lease, because two replicas that redeem one refresh token end
// the grant at a provider that revokes a used token.
func (a *App) runRefresher(ctx context.Context) {
	interval := a.cfg.OAuth.RefreshInterval
	if interval <= 0 {
		interval = time.Minute
	}
	a.runLeased(ctx, leaseOAuthRefresh, interval, func(ctx context.Context) {
		if _, err := a.refresher.RefreshDue(ctx); err != nil && ctx.Err() == nil {
			a.log.Warn("oauth refresh pass failed", "err", err)
		}
	})
}

// runSessionJanitor closes idle sessions and sessions past the maximum
// lifetime once a minute (control plane).
func (a *App) runSessionJanitor(ctx context.Context) {
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	for {
		a.closeIdleSessions(ctx)
		a.closeLifetimeSessions(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// runKeyRotation runs the janitor's pass over the client assertion keys and
// over the session signing keys once per reload interval.
func (a *App) runKeyRotation(ctx context.Context) {
	t := time.NewTicker(authn.KeyReloadInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		a.clientAssertionKeyStep(ctx, time.Now())
		a.sessionKeyStep(ctx, time.Now())
	}
}

// runAppsWatcher runs the apps directory door: sweeps propose every file,
// a tick apart until one reads the directory, then every linked server no
// present file declares is proposed for removal, and then the watcher
// polls.
func (a *App) runAppsWatcher(ctx context.Context) {
	if a.watcher.SweepUntilRead(ctx) {
		a.proposeUnfiled(ctx)
	}
	a.watcher.Run(ctx)
}

// runAuditConsumer consumes the audit stream into the hash-chained audit_log.
func (a *App) runAuditConsumer(ctx context.Context) {
	if err := a.auditCons.Run(ctx); err != nil && ctx.Err() == nil {
		a.log.Error("audit consumer stopped", "err", err)
	}
}

// runConversationConsumer maintains the conversation-turns read model (capture).
func (a *App) runConversationConsumer(ctx context.Context) {
	if err := a.turnsCons.Run(ctx); err != nil && ctx.Err() == nil {
		a.log.Error("conversation consumer stopped", "err", err)
	}
}

// runRetentionJanitor runs the hourly retention purges and the transcript
// storage watermark pass (control plane).
func (a *App) runRetentionJanitor(ctx context.Context) {
	retention := a.cfg.EffectiveCaptureRetention()
	outboxRetention := a.cfg.EffectiveOutboxBulkRetention()
	t := time.NewTicker(time.Hour)
	defer t.Stop()
	for {
		if n, err := a.store.Conversations().PurgeBefore(ctx, time.Now().Add(-retention)); err == nil && n > 0 {
			a.log.Info("transcript retention purge", "turns", n)
		}
		a.transcriptWatermarkPass(ctx)
		// Published bulk outbox rows: chain + read models hold
		// the records; control rows stay (the change feed reads them).
		if n, err := a.store.Outbox().PruneBulkPublished(ctx, time.Now().Add(-outboxRetention)); err == nil && n > 0 {
			a.log.Info("outbox bulk prune", "rows", n)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// runRevocationConsumer keeps the kill-switch consumer alive with backoff.
func (a *App) runRevocationConsumer(ctx context.Context) {
	// The kill-switch consumer must not die quietly while the pod keeps
	// serving with a frozen denylist: restart with backoff until
	// shutdown. Each (re)start is an ephemeral DeliverAll consumer, so a
	// restart replays history and re-converges.
	backoff := time.Second
	for {
		err := a.revCons.Run(ctx)
		if ctx.Err() != nil {
			return
		}
		a.log.Error("revocation consumer stopped; restarting", "err", err, "backoff", backoff)
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		if backoff < 30*time.Second {
			backoff *= 2
		}
	}
}

// runConvergeConsumer keeps the multi-pod convergence consumer alive with
// backoff and a full apply after every subscription.
func (a *App) runConvergeConsumer(ctx context.Context) {
	// Convergence restarts like the revocation consumer. DeliverNew means
	// events during the gap were missed, so each Run subscribes again and
	// then applies live state, and no event after that read is lost.
	backoff := time.Second
	for {
		err := a.convCons.Run(ctx)
		if ctx.Err() != nil {
			return
		}
		a.log.Error("converge consumer stopped; restarting", "err", err, "backoff", backoff)
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		if backoff < 30*time.Second {
			backoff *= 2
		}
	}
}

// runApprovalService keeps the human-approval service alive with backoff.
func (a *App) runApprovalService(ctx context.Context) {
	// The approval service is always on. It is fail-CLOSED at the point of
	// use (a dead service just means every approve resolution denies), so a
	// crash is loud but non-fatal: restart with backoff like the other
	// consumers. Each restart re-subscribes to the resolution fan-out.
	backoff := time.Second
	for {
		err := a.approval.Run(ctx)
		if ctx.Err() != nil {
			return
		}
		a.log.Error("approval service stopped; restarting", "err", err, "backoff", backoff)
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		if backoff < 30*time.Second {
			backoff *= 2
		}
	}
}

// runSentinel keeps the audit sentinel alive with backoff (fail open with
// alarm).
func (a *App) runSentinel(ctx context.Context) {
	// Detection fails open with an alarm, the deliberate opposite of the
	// classify lane's fail-closed: a dead sentinel blocks nothing and never
	// touches
	// the request path; it logs the gap loudly and retries. Backoff
	// caps at 1 minute so a recovered bus is re-consumed quickly;
	// the durable cursor means nothing is skipped, only judged late.
	backoff := time.Second
	for {
		err := a.sentinel.Run(ctx)
		if ctx.Err() != nil {
			return
		}
		a.log.Error("sentinel down (detection gap)", "err", err, "backoff", backoff)
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		if backoff < time.Minute {
			backoff *= 2
		}
	}
}

// runSink keeps one SIEM sink runner alive with backoff.
func (a *App) runSink(ctx context.Context, runner *spine.SinkRunner) {
	// Sinks restart like the revocation consumer: the durable cursor
	// means a restart resumes exactly where it stopped (no loss).
	backoff := time.Second
	for {
		err := runner.Run(ctx)
		if ctx.Err() != nil {
			return
		}
		a.log.Error("sink runner stopped; restarting", "sink", runner.Label(), "err", err, "backoff", backoff)
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		if backoff < 30*time.Second {
			backoff *= 2
		}
	}
}

// runHTTP serves the main listener and reports its exit on errCh.
func (a *App) runHTTP(errCh chan<- error, useTLS bool) {
	serve := func() error { return a.http.Serve(a.ln) }
	if useTLS {
		serve = func() error { return a.http.ServeTLS(a.ln, "", "") }
	}
	if err := serve(); !errors.Is(err, http.ErrServerClosed) {
		errCh <- err
		return
	}
	errCh <- nil
}

// runApproverHTTP serves the dedicated approver listener and reports its
// exit on errCh.
func (a *App) runApproverHTTP(errCh chan<- error) {
	if err := a.approverHTTP.ServeTLS(a.approverLn, "", ""); !errors.Is(err, http.ErrServerClosed) {
		errCh <- err
		return
	}
	errCh <- nil
}
