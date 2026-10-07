package approval

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/strazahq/straza/internal/config"
	"github.com/strazahq/straza/internal/events"
	"github.com/strazahq/straza/internal/store"
)

// auditSubject is the CloudEvent type + NATS subject for approval audit events.
const auditSubject = "straza.audit.approval"

// defaultTicketTTLSeconds is the fallback day-scale decision window for a ticket
// whose spec arrived un-normalized (24 h; the compiler normalizes zero to the
// same value; this only guards a direct/legacy caller). spec/policyset rev 6.
const defaultTicketTTLSeconds = 24 * 60 * 60

// defaultGrantTTLSeconds is the fallback post-approval consume window for a
// ticket whose spec arrived un-normalized (1 h; mirrors the compiler's
// policy.defaultGrantTTLSeconds; this only guards a direct/legacy caller). It
// is applied both when persisting the ticket (Request) and when materializing
// the grant deadline (Decide), so the two never disagree. spec/policyset rev 6.
const defaultGrantTTLSeconds = 60 * 60

// defaultRetryTTLSeconds is the fallback use window of a hold's approval for
// a row that carries no retryTTLSeconds (a legacy or directly inserted row);
// Request persists the same default on every hold it opens.
const defaultRetryTTLSeconds = 60

// resolvedSubjectPrefix is the core-NATS resolution broadcast subject prefix;
// the full subject is resolvedSubjectPrefix + "<approvalId>".
const resolvedSubjectPrefix = "straza.approval.resolved."

// coreBus is the subset of *events.Bus the service uses (an interface so tests
// can substitute a loopback bus). *events.Bus satisfies it.
type coreBus interface {
	PublishCore(subject string, data []byte) error
	SubscribeCore(subject string, h func(subject string, data []byte)) (func(), error)
}

// Service is the approval workflow engine. One instance per pod, always
// constructed (the console channel needs no config). Run owns the NATS
// subscription, the expiry sweep, and the retention janitor.
type Service struct {
	st       store.Store
	bus      coreBus
	resolver RoleResolver
	cfg      config.Approval
	log      *slog.Logger
	tokenKey []byte
	// slack/push keep their concrete handles for the channel-SPECIFIC surfaces
	// (Slack HTTP callback, push registration validation); all lifecycle
	// dispatch goes through the notifiers registry (notifier.go).
	slack     *slackChannel
	push      *pushDelivery
	notifiers []notifier
	now       func() time.Time

	// UserBlocked, when set, reports whether a user sits on the server's
	// in-memory revocation denylist: the Straza-lane kill switch, which
	// deliberately never touches users.status (the IdM masters that field).
	// Enroll, RefreshSigned and the Slack lane consult it beside their
	// users.status checks so a locked user cannot mint approver capability. Wired once at
	// construction, before any request is served; nil means no extra gate.
	UserBlocked func(userID string) bool

	mu      sync.Mutex
	waiters map[string][]chan struct{} // approval id → Await signal channels

	// dmu guards lastDelivery, the per-channel most-recent delivery attempt
	// surfaced on the admin channel-status card (status.go).
	dmu          sync.Mutex
	lastDelivery map[string]DeliveryRecord
}

// userBlocked is the nil-safe UserBlocked read.
func (s *Service) userBlocked(userID string) bool {
	return s.UserBlocked != nil && s.UserBlocked(userID)
}

// New builds the service, loading (or creating) the shared decision-token key
// from the settings table. The Slack channel is wired only when configured.
func New(st store.Store, bus *events.Bus, resolver RoleResolver, cfg config.Approval, log *slog.Logger) (*Service, error) {
	key, err := loadOrCreateTokenKey(st)
	if err != nil {
		return nil, err
	}
	s := &Service{
		st: st, bus: bus, resolver: resolver, cfg: cfg, log: log, tokenKey: key,
		now:     time.Now,
		waiters: map[string][]chan struct{}{},
	}
	if cfg.Channels.Slack.Enabled {
		sl, err := newSlackChannel(cfg.Channels.Slack, st, s, log)
		if err != nil {
			return nil, err
		}
		s.slack = sl
		s.register(sl)
	}
	// Push delivery is constructed when any backend has something to do: FCM
	// enabled, an UnifiedPush allowlist present (UnifiedPush-only stack), the
	// WebPush sender configured (allowlist still required to register), or
	// the APNs sender configured, or the hosted relay client enabled.
	if cfg.Push.FCM.Enabled || len(cfg.Push.AllowedPushHosts) > 0 || cfg.Push.WebPush.Enabled() || cfg.Push.APNS.Enabled() || cfg.Push.Relay.Enabled {
		pd, err := newPushDelivery(s, cfg.Push, log)
		if err != nil {
			return nil, err
		}
		s.push = pd
		s.register(pd)
	}
	return s, nil
}

// Run subscribes to the resolution fan-out and runs the periodic sweeps until
// ctx is done. It returns nil on shutdown; the caller restarts it with backoff
// (a dead service just means every approve resolution denies; fail closed).
func (s *Service) Run(ctx context.Context) error {
	unsub, err := s.bus.SubscribeCore(resolvedSubjectPrefix+"*", s.handleResolved)
	if err != nil {
		return err
	}
	defer unsub()

	sweep := time.NewTicker(15 * time.Second)
	defer sweep.Stop()
	janitor := time.NewTicker(time.Hour)
	defer janitor.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-sweep.C:
			s.sweepExpired(ctx)
			s.sweepTicketReminders(ctx)
		case <-janitor.C:
			s.prune(ctx)
		}
	}
}

// Get returns one record.
func (s *Service) Get(ctx context.Context, id string) (Record, error) {
	a, err := s.st.Approvals().GetByID(ctx, id)
	if err != nil {
		return Record{}, err
	}
	return recordFromStore(a), nil
}

// List returns records by state ("" = all bounded, "pending" = pending only).
func (s *Service) List(ctx context.Context, state string) ([]Record, error) {
	rows, err := s.st.Approvals().List(ctx, state)
	if err != nil {
		return nil, err
	}
	out := make([]Record, len(rows))
	for i, a := range rows {
		out[i] = recordFromStore(a)
	}
	return out, nil
}

// PageByState is the admin keyset window over records: state "" = all,
// beforeID "" = the newest page. Returns up to limit records newest-first
// plus the next cursor id ("" = last page).
func (s *Service) PageByState(ctx context.Context, state, beforeID string, limit int) ([]Record, string, error) {
	rows, err := s.st.Approvals().PageByState(ctx, state, beforeID, limit+1)
	if err != nil {
		return nil, "", err
	}
	next := ""
	if len(rows) > limit {
		rows = rows[:limit]
		next = rows[limit-1].ID
	}
	out := make([]Record, len(rows))
	for i, a := range rows {
		out[i] = recordFromStore(a)
	}
	return out, next, nil
}

// HandleSlackCallback dispatches an inbound Slack interactive callback to the
// Slack channel (signature-verified inside). 404s when Slack is not configured;
// the route always exists (openapi drift), but only answers when enabled.
func (s *Service) HandleSlackCallback(w http.ResponseWriter, r *http.Request) {
	if s.slack == nil {
		http.Error(w, "slack channel not configured", http.StatusNotFound)
		return
	}
	s.slack.HandleCallback(w, r)
}

// auditPhase emits a straza.audit.approval CloudEvent via the outbox (the same
// shape emitEventCtx uses). Optional fields are omitted when empty; the
// justification rides only the request phase on the gateway lane (privacy).
func (s *Service) auditPhase(ctx context.Context, phase string, rec Record) {
	data := map[string]any{
		"phase":      phase,
		"approvalId": rec.ID,
		"state":      string(rec.State),
	}
	put := func(k, v string) {
		if v != "" {
			data[k] = v
		}
	}
	put("session", rec.SessionID)
	put("user", rec.UserID)
	put("rule", rec.RuleID)
	put("set", rec.SetName)
	put("lane", rec.Lane)
	put("summary", rec.Summary)
	put("decidedBy", rec.DecidedBy)
	put("channel", rec.Channel)
	// Decided attribution (spec/events). The reason is the decider's
	// own words, deliberately on the audit trail: it is the compliance answer
	// to "why was this denied", and unlike the requester justification it is
	// key-bound on the signed lane.
	put("decidedReason", rec.DecidedReason)
	put("decidedDeviceId", rec.DecidedDeviceID)
	if phase == "request" && rec.Lane == "gateway" {
		put("justification", rec.Justification)
	}
	// A ticket grant being cashed: name the consuming session + when (spec/events
	// revision 11 `consumed`). State stays "approved"; consumption is a new fact,
	// not a state change.
	if phase == "consumed" {
		put("consumedBy", rec.ConsumedBy)
		if rec.ConsumedAt != nil {
			data["consumedAt"] = rec.ConsumedAt.UTC().Format(time.RFC3339)
		}
	}
	ce, err := json.Marshal(map[string]any{
		"specversion": "1.0",
		"id":          uuid.NewString(),
		"type":        auditSubject,
		"source":      "strazad",
		"time":        s.now().UTC().Format(time.RFC3339Nano),
		"data":        data,
	})
	if err != nil {
		return
	}
	if _, err := s.st.Outbox().Insert(ctx, store.OutboxEvent{Subject: auditSubject, CE: string(ce)}); err != nil {
		s.log.Warn("approval audit insert failed", "phase", phase, "err", err)
	}
}
