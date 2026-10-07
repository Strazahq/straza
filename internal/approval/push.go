package approval

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/strazahq/straza/internal/config"
	"github.com/strazahq/straza/internal/policy"
	"github.com/strazahq/straza/internal/redact"
	"github.com/strazahq/straza/internal/store"
)

// Push delivery for the mobile approver surface. A "decide" push nudges
// eligible approvers that a new request awaits;
// a "status" push tells a requester their request resolved. Both carry the
// SAME opaque envelope (an approval id and a kind), never command text,
// arguments, or usernames (privacy rule). The app fetches the redacted detail
// over the authenticated /v1/approver/* API using the reference.
//
// Delivery is best-effort and async: routing runs off the request goroutine
// (fired via `go` at the call sites) and each network send has its own 5s
// timeout; every failure only logs and never affects the approval outcome
// (fail-open for the NOTIFICATION, never for the decision; the decision lane
// is unchanged).

// pushTimeout bounds a single delivery HTTP call.
const pushTimeout = 5 * time.Second

// fcmScope is the OAuth2 scope minted for the FCM HTTP v1 send.
const fcmScope = "https://www.googleapis.com/auth/firebase.messaging"

// ErrPushEndpointNotAllowed is the sentinel for a unifiedpush/webpush
// endpoint that is not https or whose host the allowlist does not admit
// (enforced at registration so an unreachable/forbidden endpoint is never
// stored, and re-checked at delivery). Fail closed: no allowlist ⇒ every
// endpoint-bearing registration is refused. Always returned WRAPPED by
// endpointNotAllowed, which names the refused host and which of the two
// operator-distinct states applies (empty allowlist vs host not listed);
// the API layer maps it to 400 and the phone renders the text verbatim.
var ErrPushEndpointNotAllowed = errors.New("approval: push endpoint not allowed")

// ErrBadPushRegistration is the sentinel every registration-shape refusal
// wraps (missing/invalid subscription keys, keys on a kind that cannot use
// them, keys without a configured WebPush sender). The API layer maps it to
// 400 with the wrapped detail.
var ErrBadPushRegistration = errors.New("approval: invalid push registration")

// badPushReg wraps a registration refusal detail under ErrBadPushRegistration.
func badPushReg(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrBadPushRegistration, fmt.Sprintf(format, args...))
}

// pushPayload is the ONLY notification body. Its JSON is exactly
// {"v":1,"ref":"<approval id>","kind":"decide"|"status"}: three keys, opaque
// reference only. Pinned by TestPushPayloadKeysExact.
type pushPayload struct {
	V    int    `json:"v"`
	Ref  string `json:"ref"`
	Kind string `json:"kind"`
}

// push kinds carried in the envelope (distinct from a verdict; the app maps
// the kind to which API surface to refresh).
const (
	pushKindDecide = "decide"
	pushKindStatus = "status"
)

// pushDelivery routes and delivers approval push notifications. Constructed in
// Service.New only when at least one backend is configured (FCM enabled, an
// UnifiedPush allowlist set, the WebPush sender on, or the APNs sender on).
type pushDelivery struct {
	svc          *Service
	st           store.Store
	log          *slog.Logger
	httpc        *http.Client
	allowedHosts map[string]struct{}
	fcm          *fcmSender     // nil when FCM is disabled
	webpush      *webPushSender // nil when approval.push.webpush is off
	apns         *apnsSender    // nil when approval.push.apns is off

	relay *relaySender // nil when approval.push.relay is off
	// transport delivers one payload to one registration; exp is the
	// record's expiry (zero when unknown), the TTL input. Defaults to
	// deliverOne (kind dispatch → real HTTP); tests substitute a recorder.
	transport func(reg store.ApproverPushTarget, kind, ref string, exp time.Time)
}

// newPushDelivery builds the engine from config. It reads/parses the FCM
// service-account file eagerly when FCM is enabled (a bad file fails boot, like
// the Slack secret files). httpc is shared by both backends; the UnifiedPush
// path additionally requires https + an allowlisted host.
func newPushDelivery(svc *Service, cfg config.ApprovalPush, log *slog.Logger) (*pushDelivery, error) {
	p := &pushDelivery{
		svc:          svc,
		st:           svc.st,
		log:          log,
		httpc:        &http.Client{Timeout: pushTimeout, CheckRedirect: approvalRedirect},
		allowedHosts: make(map[string]struct{}, len(cfg.AllowedPushHosts)),
	}
	for _, h := range cfg.AllowedPushHosts {
		p.allowedHosts[h] = struct{}{}
	}
	if cfg.FCM.Enabled {
		fcm, err := newFCMSender(cfg.FCM, p.httpc)
		if err != nil {
			return nil, err
		}
		p.fcm = fcm
	}
	if cfg.WebPush.Enabled() {
		wp, err := newWebPushSender(cfg.WebPush, log)
		if err != nil {
			return nil, err
		}
		p.webpush = wp
	}
	if cfg.APNS.Enabled() {
		ap, err := newAPNSSender(cfg.APNS)
		if err != nil {
			return nil, err
		}
		p.apns = ap
		// The success line is the operator's positive control that the lane
		// is UP rather than dormant-skip (failure already fails boot). No
		// secrets: key id, team and topic are public identifiers.
		env := cfg.APNS.Environment
		if env == "" {
			env = "production"
		}
		log.Info("apns sender ready", "keyId", cfg.APNS.KeyID, "teamId", cfg.APNS.TeamID, "topic", cfg.APNS.Topic, "environment", env)
	}
	if cfg.Relay.Enabled {
		rl, err := newRelaySender(cfg.Relay, p.httpc, log)
		if err != nil {
			return nil, err
		}
		p.relay = rl
		// The success line is the operator positive control that the hosted
		// lane is UP: apns and fcm registrations route through it whenever
		// no direct sender is configured.
		log.Info("push relay client ready", "url", rl.baseURL, "tokenFile", cfg.Relay.TokenFile)
	}
	p.transport = p.deliverOne
	return p, nil
}

// pushDelivery is a lifecycle + reminder notifier (notifier.go); push
// registration validation is its channel-specific surface.
var (
	_ notifier         = (*pushDelivery)(nil)
	_ reminderNotifier = (*pushDelivery)(nil)
)

// name returns the `approve.notify` routing vocabulary entry for this channel
// (policy.NotifyPush, spec/policyset revision 8).
func (p *pushDelivery) name() string { return policy.NotifyPush }

// reconcile is a no-op: the terminal push already fired exactly once on the
// win pod via resolved; reacting to the every-pod broadcast too would
// duplicate it (a push is a notification, not an idempotent surface).
func (p *pushDelivery) reconcile(Record) {}

// reminder re-announces a near-expiry pending ticket with the SAME opaque
// decide envelope the create push sent; the mobile app posts it under a
// stable notification id, so the reminder REPLACES rather than stacks (no new
// push kind, no payload change).
func (p *pushDelivery) reminder(rec Record) { p.created(rec) }

// created fans a "decide" push to every eligible approver of a freshly
// created record. Eligibility: NOT an NHI, and
// either role-eligible for the rule OR the self-approving requester; a
// non-self record never nudges its own requester. Runs in its own goroutine.
func (p *pushDelivery) created(rec Record) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	targets, err := p.st.Approvers().ListPushTargets(ctx)
	if err != nil {
		p.log.Warn("approval push decide: list targets failed", "id", rec.ID, "err", err)
		return
	}
	byUser := groupByUser(targets)
	notified := 0
	for userID, regs := range byUser {
		if !p.eligibleDecider(ctx, rec, userID) {
			continue
		}
		notified++
		for _, reg := range regs {
			p.transport(reg, pushKindDecide, rec.ID, rec.ExpiresAt)
		}
	}
	// Pool-width honesty (revision 14): role membership drifts via the IdM
	// long after a rule was authored, so the announce is where the real
	// recipient number shows. The builder and validate warn at author time;
	// this is the runtime backstop for the drifted pool.
	if notified >= widePoolWarnRecipients {
		p.log.Warn("approval push decide: wide pool", "id", rec.ID, "rule", rec.RuleID, "recipients", notified)
	}
}

// widePoolWarnRecipients is the announce fan-out above which the decide push
// logs the wide-pool warning (each recipient is one phone rung per request).
const widePoolWarnRecipients = 10

// resolved fans a "status" push to the requester's OWN registrations after a
// resolution (decide win) or expiry (sweep win). Runs in its own goroutine.
// A self-decided record (selfApproval lane, any channel) sends NO receipt:
// the requester made the decision, so a status push would only repeat it back
// and add to push-notification fatigue. DecidedBy is empty on
// expiry, so the sweep's receipt, the only signal anyone gets, still fans out;
// the empty-string check keeps that lane sending.
func (p *pushDelivery) resolved(rec Record) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if rec.UserID == "" {
		return
	}
	if rec.DecidedBy != "" && rec.DecidedBy == rec.UserID {
		return
	}
	targets, err := p.st.Approvers().ListPushTargets(ctx)
	if err != nil {
		p.log.Warn("approval push status: list targets failed", "id", rec.ID, "err", err)
		return
	}
	for _, reg := range targets {
		if reg.UserID == rec.UserID {
			p.transport(reg, pushKindStatus, rec.ID, rec.ExpiresAt)
		}
	}
}

// eligibleDecider applies the decide-push routing rule for one candidate user
// (evaluated once per user, then reused for all their registrations).
func (p *pushDelivery) eligibleDecider(ctx context.Context, rec Record, userID string) bool {
	if nhi, _ := p.svc.isNHI(ctx, userID); userID == "" || nhi {
		return false
	}
	if rec.Mode == policy.ModeConfirm {
		// mode confirm: exactly one decider exists, so exactly one phone
		// rings. Role resolution never enters it.
		return userID == rec.UserID
	}
	if userID == rec.UserID {
		// The requester is a target only on the self-approval lane; a non-self
		// record never pushes decide to its own requester (even if role-eligible).
		return rec.SelfApproval
	}
	// User-scoped pool first (revision 13): a sponsor's phone rings without
	// any role resolution.
	if userEligible(rec, p.svc.deciderUsername(ctx, userID)) {
		return true
	}
	roles, err := p.svc.resolver.ResolveRoles(ctx, userID, p.svc.now())
	if err != nil {
		p.log.Warn("approval push decide: resolve roles failed", "id", rec.ID, "user", userID, "err", err)
		return false
	}
	return roleEligible(rec, roleNameSet(roles))
}

// deliverOne is the real transport: it dispatches on the registration kind
// (and, for unifiedpush, on whether the registration carries subscription
// keys) and fires the network send in its own goroutine (best-effort, 5s
// timeout). Keys-present is the client's opt-in to the encrypted Web Push
// protocol; a keyless unifiedpush registration keeps the legacy plain-JSON
// POST; existing registrations are NEVER flipped to encrypted (the app and
// raw-ntfy consumers parse the plain body).
func (p *pushDelivery) deliverOne(reg store.ApproverPushTarget, kind, ref string, exp time.Time) {
	switch reg.Kind {
	case "fcm":
		switch {
		case p.fcm != nil:
			go func() { _ = p.sendFCM(reg, kind, ref) }() // #nosec G118 -- best-effort; sender logs + records
		case p.relay != nil:
			go func() { _ = p.sendRelay(reg, "fcm", kind, ref, exp) }() // #nosec G118 -- best-effort; sender logs + records
		default:
			p.log.Debug("approval push: fcm registration but no FCM backend or relay is configured", "device", reg.DeviceID)
		}
	case "unifiedpush":
		if _, _, _, keyed := splitKeyedEndpoint(reg.TokenOrEndpoint); keyed {
			go func() { _ = p.sendWebPush(reg, kind, ref, exp) }() // #nosec G118 -- best-effort; sender logs + records
			return
		}
		go func() { _ = p.sendUnifiedPush(reg, kind, ref, exp) }() // #nosec G118 -- best-effort; sender logs + records
	case "webpush":
		go func() { _ = p.sendWebPush(reg, kind, ref, exp) }() // #nosec G118 -- best-effort; sender logs + records
	case "apns":
		switch {
		case p.apns != nil:
			go func() { _ = p.sendAPNS(reg, kind, ref, exp) }() // #nosec G118 -- best-effort; sender logs + records
		case p.relay != nil:
			go func() { _ = p.sendRelay(reg, "apns", kind, ref, exp) }() // #nosec G118 -- best-effort; sender logs + records
		default:
			p.log.Debug("approval push: apns registration but no APNs backend or relay is configured", "device", reg.DeviceID)
		}
	default:
		p.log.Debug("approval push: unknown push kind, skipped", "kind", reg.Kind, "device", reg.DeviceID)
	}
}

// sendOne synchronously delivers one payload to one registration and returns
// the outcome, the shared body of the async fan-out (deliverOne) and the
// synchronous channel test (status.go). An unknown kind is an
// error here; the async path filters them out BEFORE calling (quiet skip),
// so only real attempts ever stamp lastDelivery.
func (p *pushDelivery) sendOne(reg store.ApproverPushTarget, kind, ref string, exp time.Time) error {
	switch reg.Kind {
	case "fcm":
		if p.fcm != nil {
			return p.sendFCM(reg, kind, ref)
		}
		if p.relay != nil {
			return p.sendRelay(reg, "fcm", kind, ref, exp)
		}
		return errors.New("fcm registration but no FCM backend or relay is configured")
	case "unifiedpush":
		if _, _, _, keyed := splitKeyedEndpoint(reg.TokenOrEndpoint); keyed {
			return p.sendWebPush(reg, kind, ref, exp)
		}
		return p.sendUnifiedPush(reg, kind, ref, exp)
	case "webpush":
		return p.sendWebPush(reg, kind, ref, exp)
	case "apns":
		if p.apns != nil {
			return p.sendAPNS(reg, kind, ref, exp)
		}
		if p.relay != nil {
			return p.sendRelay(reg, "apns", kind, ref, exp)
		}
		return errors.New("apns registration but no APNs backend or relay is configured (set approval.push.apns.keyFile or approval.push.relay)")
	default:
		return fmt.Errorf("push kind %q is unknown: a registration is fcm, unifiedpush, webpush or apns", reg.Kind)
	}
}

// sendAPNS delivers via the APNs backend and prunes a registration reported
// gone, but only behind the registered-at guard: a 410 carries Apple's
// invalidation time, the other dead-token reasons guard on the attempt start,
// and either way a token the app re-confirmed after the horizon keeps
// ringing. Every attempt stamps lastDelivery.
func (p *pushDelivery) sendAPNS(reg store.ApproverPushTarget, kind, ref string, exp time.Time) error {
	attemptStart := p.svc.now()
	ctx, cancel := context.WithTimeout(context.Background(), pushTimeout)
	defer cancel()
	res, err := p.apns.send(ctx, reg.TokenOrEndpoint, kind, ref, exp)
	if err == nil && res.status >= 400 {
		err = fmt.Errorf("apns status %d reason %q", res.status, res.reason)
	}
	p.svc.recordDelivery(policy.NotifyPush, err, "apns send")
	if err == nil {
		return nil
	}
	p.log.Warn("approval push: apns send failed", "device", reg.DeviceID, "apns_id", res.id, "err", err)
	switch classifyAPNS(res.status, res.reason) {
	case apnsPrune:
		notAfter := res.timestamp
		if notAfter.IsZero() {
			notAfter = attemptStart
		}
		p.pruneDeadBefore(reg, notAfter)
	case apnsRemint:
		// send already re-minted and retried once; landing here means the
		// FRESH token was also refused. That is server clock skew or a
		// mis-scoped key, never the device's fault.
		p.log.Warn("approval push: apns provider token refused after re-mint; check the server clock and the key's environment scope", "device", reg.DeviceID)
	}
	return err
}

// pruneDeadBefore removes a registration an upstream reported gone, unless
// the app re-confirmed it after the horizon (the guarded twin of pruneDead;
// see store.ApproverRepo.DeletePushBefore).
func (p *pushDelivery) pruneDeadBefore(reg store.ApproverPushTarget, notAfter time.Time) {
	ctx, cancel := context.WithTimeout(context.Background(), pushTimeout)
	defer cancel()
	deleted, err := p.st.Approvers().DeletePushBefore(ctx, reg.DeviceID, reg.Kind, reg.TokenOrEndpoint, notAfter)
	if err != nil {
		p.log.Warn("approval push: prune dead registration failed", "device", reg.DeviceID, "err", err)
		return
	}
	if deleted {
		p.log.Info("approval push: pruned dead registration", "device", reg.DeviceID, "kind", reg.Kind)
		return
	}
	p.log.Info("approval push: prune skipped, registration re-confirmed after the invalidation", "device", reg.DeviceID, "kind", reg.Kind)
}

// sendRelay delivers via the hosted push relay and mirrors its siblings:
// every attempt stamps lastDelivery, and a completed forward whose verdict
// says prune removes the dead route behind the registered-at guard (the
// relay verdict carries no invalidation time, so the attempt start is the
// horizon, like the timestampless APNs reasons).
func (p *pushDelivery) sendRelay(reg store.ApproverPushTarget, platform, kind, ref string, exp time.Time) error {
	attemptStart := p.svc.now()
	ctx, cancel := context.WithTimeout(context.Background(), pushTimeout)
	defer cancel()
	res, err := p.relay.send(ctx, platform, reg.TokenOrEndpoint, kind, ref, exp)
	if err == nil && !res.delivered {
		err = fmt.Errorf("relay: downstream status %d reason %q", res.downstreamStatus, res.reason)
	}
	p.svc.recordDelivery(policy.NotifyPush, err, "relay send ("+platform+")")
	if err == nil {
		return nil
	}
	p.log.Warn("approval push: relay send failed", "device", reg.DeviceID, "platform", platform, "err", err)
	if res.prune {
		p.pruneDeadBefore(reg, attemptStart)
	}
	return err
}

// sendFCM delivers via the FCM HTTP v1 backend and prunes a dead registration
// on a 404/410 (UNREGISTERED). Every attempt stamps lastDelivery.
func (p *pushDelivery) sendFCM(reg store.ApproverPushTarget, kind, ref string) error {
	ctx, cancel := context.WithTimeout(context.Background(), pushTimeout)
	defer cancel()
	status, err := p.fcm.send(ctx, reg.TokenOrEndpoint, kind, ref)
	if err == nil && status >= 400 {
		err = fmt.Errorf("fcm status %d", status)
	}
	p.svc.recordDelivery(policy.NotifyPush, err, "fcm send")
	if err != nil {
		p.log.Warn("approval push: fcm send failed", "device", reg.DeviceID, "err", err)
		if isDead(status) {
			p.pruneDead(reg)
		}
		return err
	}
	return nil
}

// sendUnifiedPush POSTs the raw opaque payload to a registered UnifiedPush
// (e.g. ntfy) endpoint, the LEGACY spec-2 lane: plain JSON, no encryption,
// no VAPID, preserved byte-for-byte for the current mobile app and raw-ntfy
// subscribers. A registration that carries subscription keys never reaches
// here (deliverOne routes it to sendWebPush); keys-present is the client's
// opt-in signal. The only addition over the original lane is the RFC 8030
// §5.2-mandatory TTL header, inert on ntfy (its documented header vocabulary
// has no TTL, verified against docs.ntfy.sh/publish) but required
// by any real RFC 8030 push resource. The endpoint must be https with an
// allowlisted host, re-checked here (defence in depth) so a host removed
// from the allowlist after registration stops receiving. A 404/410 prunes the
// registration. Every attempt stamps lastDelivery.
func (p *pushDelivery) sendUnifiedPush(reg store.ApproverPushTarget, kind, ref string, exp time.Time) error {
	if !p.hostAllowed(reg.TokenOrEndpoint) {
		err := p.endpointNotAllowed(reg.TokenOrEndpoint)
		p.svc.recordDelivery(policy.NotifyPush, err, "unifiedpush send")
		p.log.Warn("approval push: unifiedpush endpoint host not allowed, skipped", "device", reg.DeviceID)
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), pushTimeout)
	defer cancel()
	body, err := json.Marshal(pushPayload{V: 1, Ref: ref, Kind: kind})
	if err != nil {
		return err
	}
	status, err := p.postJSON(ctx, reg.TokenOrEndpoint, body, p.pushTTL(kind, exp))
	if err == nil && status >= 400 {
		err = fmt.Errorf("endpoint status %d", status)
	}
	p.svc.recordDelivery(policy.NotifyPush, err, "unifiedpush send")
	if err != nil {
		p.log.Warn("approval push: unifiedpush send failed", "device", reg.DeviceID, "err", err)
		if isDead(status) {
			p.pruneDead(reg)
		}
		return err
	}
	return nil
}

// sendWebPush delivers the opaque envelope over the full Web Push protocol:
// RFC 8291 aes128gcm body, RFC 8292 VAPID authorization, RFC 8030 TTL /
// Urgency / Topic headers. It serves kind=webpush registrations (browser/PWA
// subscriptions) and kind=unifiedpush registrations that opted in by
// registering subscription keys (spec-3 distributors). Fail closed on every
// precondition: a keyed registration NEVER falls back to a plaintext POST.
// A 404/410 prunes the registration. Every attempt stamps lastDelivery.
func (p *pushDelivery) sendWebPush(reg store.ApproverPushTarget, kind, ref string, exp time.Time) error {
	fail := func(err error) error {
		p.svc.recordDelivery(policy.NotifyPush, err, "webpush send")
		p.log.Warn("approval push: webpush send failed", "device", reg.DeviceID, "kind", reg.Kind, "err", err)
		return err
	}
	if p.webpush == nil {
		return fail(errors.New("registration needs the WebPush sender but approval.push.webpush.vapidKeyFile is not configured"))
	}
	endpoint, p256dhB64, authB64, keyed := splitKeyedEndpoint(reg.TokenOrEndpoint)
	if !keyed {
		// Only reachable for kind=webpush rows stored by the pre-0.40 stub
		// ("stored but not deliverable"): they carry no keys and cannot be
		// served; the device must re-register with p256dh+auth.
		return fail(errors.New("webpush registration has no subscription keys. The device must re-register with p256dh and auth"))
	}
	uaPublic, authSecret, err := decodeWebPushKeys(p256dhB64, authB64)
	if err != nil {
		return fail(fmt.Errorf("stored subscription keys are invalid: %w", err))
	}
	if !p.hostAllowed(endpoint) {
		return fail(p.endpointNotAllowed(endpoint))
	}
	payload, err := json.Marshal(pushPayload{V: 1, Ref: ref, Kind: kind})
	if err != nil {
		return fail(err)
	}
	sealed, err := webpushEncrypt(uaPublic, authSecret, payload)
	if err != nil {
		return fail(err)
	}
	authz, err := p.webpush.vapidAuthorization(endpoint)
	if err != nil {
		return fail(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), pushTimeout)
	defer cancel()
	// Endpoint-echoing errors are redacted to scheme://host; the push
	// resource path is a capability token (postJSON's comment argues it).
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(sealed))
	if err != nil {
		return fail(redact.SanitizeURLError(err, redact.Host))
	}
	req.Header.Set("Content-Encoding", "aes128gcm")
	req.Header.Set("Content-Type", "application/octet-stream")
	req.Header.Set("Authorization", authz)
	req.Header.Set("TTL", strconv.Itoa(p.pushTTL(kind, exp)))
	// Approvals are time-sensitive alerts, exactly RFC 8030 §5.3's Urgency:
	// high definition.
	req.Header.Set("Urgency", "high")
	// Same ref ⇒ same topic ⇒ a queued earlier push (create) is REPLACED by a
	// later one (reminder/status) instead of stacking (RFC 8030 §5.4).
	req.Header.Set("Topic", webpushTopic(ref))
	if reg.Kind == "unifiedpush" {
		// ntfy attachment trap: a non-UTF-8 body becomes an attachment file
		// unless the UnifiedPush flag is set, which makes ntfy base64 the
		// binary into the message instead (docs.ntfy.sh/publish). Distributor
		// lane only; RFC 8030 push services ignore the unknown header.
		req.Header.Set("X-UnifiedPush", "1")
	}
	status := 0
	resp, err := doRequest(p.httpc, req)
	err = redact.SanitizeURLError(err, redact.Host)
	if err == nil {
		status = resp.StatusCode
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<16))
		_ = resp.Body.Close()
		if status >= 400 {
			err = fmt.Errorf("endpoint status %d", status)
			if status == http.StatusUnauthorized || status == http.StatusForbidden {
				err = fmt.Errorf("endpoint status %d (VAPID rejected: check clock skew and that the subscription was created against THIS deployment's key)", status)
			}
		}
	}
	p.svc.recordDelivery(policy.NotifyPush, err, "webpush send")
	if err != nil {
		p.log.Warn("approval push: webpush send failed", "device", reg.DeviceID, "kind", reg.Kind, "err", err)
		if isDead(status) {
			p.pruneDead(reg)
		}
		return err
	}
	return nil
}

// pushTTL is the RFC 8030 TTL header value in seconds: for a decide push with
// a known expiry it is the approval's remaining decision window (a queued
// nudge for an already-expired approval is noise), capped at 24h; status
// pushes and unknown expiries get the 24h default (the resolution fact stays
// true).
func (p *pushDelivery) pushTTL(kind string, exp time.Time) int {
	const defaultTTL = 24 * time.Hour
	if kind == pushKindDecide && !exp.IsZero() {
		remaining := exp.Sub(p.svc.now())
		if remaining < 0 {
			remaining = 0
		}
		if remaining > defaultTTL {
			remaining = defaultTTL
		}
		return int((remaining + time.Second - 1) / time.Second)
	}
	return int(defaultTTL / time.Second)
}

// postJSON POSTs a JSON body with the mandatory TTL header and returns the
// status code (body drained/closed).
func (p *pushDelivery) postJSON(ctx context.Context, endpoint string, body []byte, ttl int) (int, error) {
	// Both error paths carry the endpoint (NewRequest's parse error and
	// net/http's *url.Error both print the full request URL), and a push
	// resource PATH is a per-subscription capability token, the same rule
	// endpointNotAllowed follows. Redact to scheme://host before the error
	// reaches recordDelivery / logs / the channels admin API.
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return 0, redact.SanitizeURLError(err, redact.Host)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("TTL", strconv.Itoa(ttl))
	resp, err := doRequest(p.httpc, req)
	if err != nil {
		return 0, redact.SanitizeURLError(err, redact.Host)
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<16))
	return resp.StatusCode, nil
}

// pruneDead deletes a registration whose upstream reported it gone (404/410).
func (p *pushDelivery) pruneDead(reg store.ApproverPushTarget) {
	ctx, cancel := context.WithTimeout(context.Background(), pushTimeout)
	defer cancel()
	if err := p.st.Approvers().DeletePush(ctx, reg.DeviceID, reg.Kind, reg.TokenOrEndpoint); err != nil {
		p.log.Warn("approval push: prune dead registration failed", "device", reg.DeviceID, "err", err)
		return
	}
	p.log.Info("approval push: pruned dead registration", "device", reg.DeviceID, "kind", reg.Kind)
}

// endpointNotAllowed builds the ErrPushEndpointNotAllowed refusal for raw. It
// is the explaining half of a pair whose deciding half is hostAllowed: https
// only, allowlist entries matching exactly except "*.suffix", which matches
// any host under that suffix because browser push services mint per-tenant
// subdomains ("*.notify.windows.com") that exact entries cannot express.
// Anything else denies, fail closed.
//
// Every variant names at most the endpoint's HOST, never the endpoint itself:
// push resource paths carry per-subscription capability tokens (secrets), and
// this string travels to the phone as a 400 body, into delivery records, and
// into logs. It distinguishes the two states an operator fixes differently, an
// EMPTY allowlist (approval.push.allowedPushHosts never configured, which is
// also the no-push-engine nil-receiver state) versus a host the configured
// list does not cover. A non-https or unparseable endpoint names that instead,
// where a host would be beside the point.
func (p *pushDelivery) endpointNotAllowed(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" {
		return fmt.Errorf("%w: endpoint must be a valid https URL", ErrPushEndpointNotAllowed)
	}
	if p == nil || len(p.allowedHosts) == 0 {
		return fmt.Errorf("%w: host %q refused. approval.push.allowedPushHosts is empty, and no push host is allowed until it names the push services this deployment may contact", ErrPushEndpointNotAllowed, u.Hostname())
	}
	return fmt.Errorf("%w: host %q is not in approval.push.allowedPushHosts", ErrPushEndpointNotAllowed, u.Hostname())
}

func (p *pushDelivery) hostAllowed(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" {
		return false
	}
	host := u.Hostname()
	if _, ok := p.allowedHosts[host]; ok {
		return true
	}
	for entry := range p.allowedHosts {
		suffix, wild := strings.CutPrefix(entry, "*")
		if wild && strings.HasPrefix(suffix, ".") &&
			len(host) > len(suffix) && strings.HasSuffix(host, suffix) {
			return true
		}
	}
	return false
}

// validatePushRegistration is the registration-time gate the RegisterPush
// wrapper calls before storing a registration; it returns the value to STORE
// as token_or_endpoint (the endpoint alone, or the endpoint+keys canonical
// form for a keyed subscription). The rules, all fail-closed:
//
//   - fcm/apns: opaque device tokens; subscription keys are refused (they
//     have no meaning there and would silently do nothing).
//   - webpush: keys are REQUIRED (a PushSubscription without its keys cannot
//     be encrypted to), endpoint must be https + allowlisted.
//   - unifiedpush: endpoint must be https + allowlisted; keys are OPTIONAL:
//     absent keeps the legacy plain-JSON lane, present opts in to the
//     encrypted Web Push protocol (UnifiedPush spec 3).
//   - any keyed registration additionally needs the WebPush sender configured
//     (approval.push.webpush.vapidKeyFile); otherwise it could be stored but
//     never delivered, so it is refused up front with the fix named.
func (s *Service) validatePushRegistration(kind, endpoint, p256dh, auth string) (string, error) {
	hasKeys := p256dh != "" || auth != ""
	if hasKeys && (p256dh == "" || auth == "") {
		return "", badPushReg("p256dh and auth travel together (got one without the other)")
	}
	switch kind {
	case "fcm", "apns":
		if hasKeys {
			return "", badPushReg("kind %q does not take subscription keys (p256dh/auth belong to webpush/unifiedpush)", kind)
		}
		return endpoint, nil
	case "webpush":
		if !hasKeys {
			return "", badPushReg("kind webpush requires p256dh and auth (the PushSubscription's keys)")
		}
	}
	// unifiedpush and webpush: endpoint-bearing kinds.
	if strings.Contains(endpoint, "#") {
		// Push resource URLs never carry fragments (they are not sent on the
		// wire); the fragment namespace of the stored value is server-owned.
		return "", badPushReg("push endpoint must not contain a fragment")
	}
	if s.push == nil || !s.push.hostAllowed(endpoint) {
		return "", s.push.endpointNotAllowed(endpoint)
	}
	if !hasKeys {
		return endpoint, nil
	}
	if s.push.webpush == nil {
		return "", badPushReg("subscription keys need the WebPush sender. Set approval.push.webpush.vapidKeyFile (or register without keys for the legacy lane)")
	}
	if _, _, err := decodeWebPushKeys(p256dh, auth); err != nil {
		return "", badPushReg("%s", err)
	}
	return encodeKeyedEndpoint(endpoint, p256dh, auth), nil
}

// WebPushVAPIDPublicKey is the deployment's VAPID public key (base64url,
// 65-octet uncompressed P-256 point), what a client passes its push service
// as applicationServerKey when subscribing, advertised in the enroll
// response. Empty when the WebPush lane is off (the client's signal that only
// the legacy keyless lane is available).
func (s *Service) WebPushVAPIDPublicKey() string {
	if s.push == nil || s.push.webpush == nil {
		return ""
	}
	return s.push.webpush.publicKeyB64()
}

// groupByUser buckets registrations by owning user so eligibility is resolved
// once per user rather than once per registration.
func groupByUser(targets []store.ApproverPushTarget) map[string][]store.ApproverPushTarget {
	m := make(map[string][]store.ApproverPushTarget)
	for _, t := range targets {
		m[t.UserID] = append(m[t.UserID], t)
	}
	return m
}

// isDead reports whether an upstream status means the registration is gone.
func isDead(status int) bool {
	return status == http.StatusNotFound || status == http.StatusGone
}
