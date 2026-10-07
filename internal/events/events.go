// Package events is the event spine: an embedded NATS server with
// JetStream in standalone mode, or a connection to an external NATS
// deployment in enterprise mode. Stream definitions for the Straza
// subject taxonomy are ensured at boot.
package events

import (
	"context"
	"fmt"
	"path/filepath"
	"time"

	"github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"

	"github.com/strazahq/straza/internal/config"
	"github.com/strazahq/straza/internal/redact"
)

// Stream definitions per the subject taxonomy. Audit is its own
// stream: different retention pressure and consumers than control events.
var streamDefs = []jetstream.StreamConfig{
	{
		Name:     "STRAZA_AUDIT",
		Subjects: []string{"straza.audit.>"},
		Storage:  jetstream.FileStorage,
	},
	{
		Name: "STRAZA_EVENTS",
		Subjects: []string{
			"straza.policy.>",
			"straza.revocation.>",
			"straza.apps.>",
			"straza.identity.>",
		},
		Storage: jetstream.FileStorage,
	},
}

// defaultAuditStreamMaxBytes bounds STRAZA_AUDIT when the operator sets no
// explicit cap: small enough to fit a 5 GiB NATS volume, large enough for
// weeks of decision audit.
const defaultAuditStreamMaxBytes = 2 << 30

// streamDefsFor resolves the stream definitions for this deployment: the
// STRAZA_AUDIT stream gets age and size bounds. Unbounded, the stream
// grows until the disk is full, at which point EVERY publish through the
// outbox relay fails (kill-switch revocations included) while health
// probes stay green. Bounded with DiscardOld, it sheds its oldest messages
// instead and publishes keep flowing. The hash chain and events_outbox hold
// the durable copies, so a discarded message that was already consumed costs
// nothing, and one that was not is still recoverable from the outbox.
func streamDefsFor(cfg config.Config) []jetstream.StreamConfig {
	defs := make([]jetstream.StreamConfig, len(streamDefs))
	copy(defs, streamDefs)
	for i := range defs {
		if defs[i].Name != "STRAZA_AUDIT" {
			continue
		}
		maxAge := cfg.Events.AuditStreamMaxAge
		if maxAge == 0 {
			maxAge = 2 * cfg.EffectiveCaptureRetention()
		}
		maxBytes := cfg.Events.AuditStreamMaxBytes
		if maxBytes == 0 {
			maxBytes = defaultAuditStreamMaxBytes
		}
		defs[i].MaxAge = maxAge
		defs[i].MaxBytes = maxBytes
		defs[i].Discard = jetstream.DiscardOld // shed oldest; never refuse a publish
	}
	return defs
}

// Bus is the process-facing handle on the event spine.
type Bus struct {
	nc  *nats.Conn
	js  jetstream.JetStream
	srv *server.Server // non-nil only when embedded
}

// Start boots the bus per cfg: an in-process nats-server (JetStream store
// under <dataDir>/nats) or a client connection to cfg.Events.URL. Stream
// definitions are ensured in both modes.
func Start(ctx context.Context, cfg config.Config) (*Bus, error) {
	b := &Bus{}
	if cfg.Events.Embedded {
		opts := &server.Options{
			ServerName:             "strazad-embedded",
			DontListen:             true, // in-process connections only
			JetStream:              true,
			StoreDir:               filepath.Join(cfg.DataDir, "nats"),
			NoLog:                  true,
			NoSigs:                 true,
			DisableJetStreamBanner: true,
		}
		srv, err := server.NewServer(opts)
		if err != nil {
			return nil, fmt.Errorf("events: init embedded nats: %w", err)
		}
		srv.Start()
		if !srv.ReadyForConnections(10 * time.Second) {
			srv.Shutdown()
			return nil, fmt.Errorf("events: embedded nats not ready within 10s")
		}
		b.srv = srv
		nc, err := nats.Connect("", nats.InProcessServer(srv))
		if err != nil {
			srv.Shutdown()
			return nil, fmt.Errorf("events: connect embedded nats: %w", err)
		}
		b.nc = nc
	} else {
		nc, err := nats.Connect(cfg.Events.URL,
			nats.Name("strazad"),
			nats.MaxReconnects(-1),
		)
		if err != nil {
			// Redacted: a NATS URL can carry user:password@ credentials, and
			// this error reaches the boot log / stderr.
			return nil, fmt.Errorf("events: connect %s: %w", redact.URL(cfg.Events.URL), err)
		}
		b.nc = nc
	}

	js, err := jetstream.New(b.nc)
	if err != nil {
		b.Close()
		return nil, fmt.Errorf("events: init jetstream: %w", err)
	}
	b.js = js

	for _, def := range streamDefsFor(cfg) {
		if _, err := js.CreateOrUpdateStream(ctx, def); err != nil {
			b.Close()
			return nil, fmt.Errorf("events: ensure stream %s: %w", def.Name, err)
		}
	}
	// The dead-letter lane is ensured beside the spine streams but kept out
	// of streamDefs on purpose (deadletter.go): sink filters intersect
	// StreamSubjects(), and parked records must never feed a sink.
	if _, err := js.CreateOrUpdateStream(ctx, deadLetterStreamDef); err != nil {
		b.Close()
		return nil, fmt.Errorf("events: ensure stream %s: %w", deadLetterStreamDef.Name, err)
	}
	return b, nil
}

// Publish writes a message to a Straza subject with JetStream ack.
func (b *Bus) Publish(ctx context.Context, subject string, data []byte) error {
	_, err := b.js.Publish(ctx, subject, data)
	return err
}

// AuditConsumer returns (creating if needed) the durable pull consumer that
// drains the STRAZA_AUDIT stream into the hash chain. Durable + explicit ack
// so a consumer crash redelivers un-acked messages (at-least-once).
// MaxAckPending sizes the in-flight window for BATCHED appends. Chain
// linearity is not this consumer's job: the store's AppendChained
// serializes writers (Postgres advisory lock / SQLite single connection),
// so pods may hold disjoint batches concurrently and share the work.
func (b *Bus) AuditConsumer(ctx context.Context) (jetstream.Consumer, error) {
	return b.js.CreateOrUpdateConsumer(ctx, "STRAZA_AUDIT", jetstream.ConsumerConfig{
		Durable:       "audit-chain",
		AckPolicy:     jetstream.AckExplicitPolicy,
		DeliverPolicy: jetstream.DeliverAllPolicy,
		FilterSubject: "straza.audit.>",
		MaxAckPending: 128, // two 64-message batches in flight across pods
	})
}

// ConvergeConsumer returns an ephemeral consumer over the control-plane
// change subjects for multi-pod HA: policy activations, app/binding
// changes, identity changes. Ephemeral + DeliverNew: a pod loads current
// state from the store at boot, so it only needs changes made AFTER it came
// up. There is no history replay (unlike revocations, where replay rebuilds
// the denylist).
func (b *Bus) ConvergeConsumer(ctx context.Context) (jetstream.Consumer, error) {
	return b.js.CreateOrUpdateConsumer(ctx, "STRAZA_EVENTS", jetstream.ConsumerConfig{
		AckPolicy:      jetstream.AckNonePolicy,
		DeliverPolicy:  jetstream.DeliverNewPolicy,
		FilterSubjects: []string{"straza.policy.updated", "straza.apps.>", "straza.identity.>"},
	})
}

// RevocationConsumer returns an ephemeral consumer over straza.revocation.>
// (kill-switch fan-out). Ephemeral + DeliverAll: every gateway/pod gets
// its own cursor and, on (re)start, replays the full revocation history to
// rebuild its in-memory denylist, so the data plane stays stateless.
func (b *Bus) RevocationConsumer(ctx context.Context) (jetstream.Consumer, error) {
	return b.js.CreateOrUpdateConsumer(ctx, "STRAZA_EVENTS", jetstream.ConsumerConfig{
		AckPolicy:     jetstream.AckNonePolicy,
		DeliverPolicy: jetstream.DeliverAllPolicy,
		FilterSubject: "straza.revocation.>",
	})
}

// ConversationConsumer returns (creating if needed) the durable pull
// consumer that mirrors capture events (spec/events rev 5) into the
// conversation_turns read model. Durable + explicit ack, dedupe in the
// consumer by CE id, the same at-least-once discipline as the audit chain.
func (b *Bus) ConversationConsumer(ctx context.Context) (jetstream.Consumer, error) {
	return b.js.CreateOrUpdateConsumer(ctx, "STRAZA_AUDIT", jetstream.ConsumerConfig{
		Durable:        "conversation-turns",
		AckPolicy:      jetstream.AckExplicitPolicy,
		DeliverPolicy:  jetstream.DeliverAllPolicy,
		FilterSubjects: []string{"straza.audit.prompt", "straza.audit.reply"},
		MaxAckPending:  128, // two batches in flight (batched inserts)
	})
}

// SentinelConsumer returns (creating if needed) the durable pull consumer
// the audit sentinel reads.
// Durable + explicit ack; DeliverNew, NOT DeliverAll: the durable persists
// the cursor across restarts, while a FRESH deployment starts at the stream
// tail; replaying history would re-alert on long-dead sessions. The filter
// deliberately excludes straza.audit.sentinel (its own output) and identity
// events: no feedback loop, no noise.
func (b *Bus) SentinelConsumer(ctx context.Context) (jetstream.Consumer, error) {
	return b.js.CreateOrUpdateConsumer(ctx, "STRAZA_AUDIT", jetstream.ConsumerConfig{
		Durable:       "sentinel",
		AckPolicy:     jetstream.AckExplicitPolicy,
		DeliverPolicy: jetstream.DeliverNewPolicy,
		FilterSubjects: []string{
			"straza.audit.tool", "straza.audit.mcp",
			"straza.audit.prompt", "straza.audit.reply",
		},
		MaxAckPending: 64,
	})
}

// SinkConsumer returns (creating if needed) a durable pull consumer for one
// sink on one stream. Durable + explicit ack: a sink outage NAKs and
// redelivers, at-least-once, with no loss.
func (b *Bus) SinkConsumer(ctx context.Context, stream, durable string, filters []string) (jetstream.Consumer, error) {
	return b.js.CreateOrUpdateConsumer(ctx, stream, jetstream.ConsumerConfig{
		Durable:        durable,
		AckPolicy:      jetstream.AckExplicitPolicy,
		DeliverPolicy:  jetstream.DeliverAllPolicy,
		FilterSubjects: filters,
		MaxAckPending:  64,
	})
}

// StreamSubjects exposes the spine's stream → subject-space map so sink
// filters can be intersected per stream (a JetStream consumer lives on
// exactly one stream).
func StreamSubjects() map[string][]string {
	out := make(map[string][]string, len(streamDefs))
	for _, def := range streamDefs {
		out[def.Name] = append([]string(nil), def.Subjects...)
	}
	return out
}

// PublishCore publishes without JetStream ack (fire-and-forget core NATS),
// for the low-latency client push subject.
func (b *Bus) PublishCore(subject string, data []byte) error {
	return b.nc.Publish(subject, data)
}

// SubscribeCore subscribes to a core-NATS subject (no JetStream, no durable
// cursor): every connected pod's handler fires on each message, and nothing is
// replayed to a pod that was down. It is the notify-only fan-out lane: the
// approval-resolution broadcast (straza.approval.resolved.<id>) rides it so
// any pod's blocked gateway waiter or hook exemption reacts without cross-pod
// DB polling (a missed broadcast simply times the waiter out; fail closed).
// The returned func unsubscribes.
func (b *Bus) SubscribeCore(subject string, h func(subject string, data []byte)) (func(), error) {
	sub, err := b.nc.Subscribe(subject, func(m *nats.Msg) {
		h(m.Subject, m.Data)
	})
	if err != nil {
		return nil, fmt.Errorf("events: subscribe %s: %w", subject, err)
	}
	return func() { _ = sub.Unsubscribe() }, nil
}

// Ping verifies the bus is connected and JetStream answers.
func (b *Bus) Ping(ctx context.Context) error {
	if b.nc == nil || !b.nc.IsConnected() {
		return fmt.Errorf("events: nats not connected")
	}
	if _, err := b.js.Stream(ctx, "STRAZA_EVENTS"); err != nil {
		return fmt.Errorf("events: jetstream unavailable: %w", err)
	}
	return nil
}

// Close drains the client connection and stops the embedded server if any.
func (b *Bus) Close() {
	if b.nc != nil {
		b.nc.Close()
	}
	if b.srv != nil {
		b.srv.Shutdown()
		b.srv.WaitForShutdown()
	}
}
