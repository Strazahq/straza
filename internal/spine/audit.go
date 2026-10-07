package spine

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/nats-io/nats.go/jetstream"

	"github.com/strazahq/straza/internal/audit"
	"github.com/strazahq/straza/internal/events"
	"github.com/strazahq/straza/internal/store"
)

// chainBatch bounds one chain-writer batch: fetched from JetStream together,
// deduped together, appended as one transaction. The batch lifts the
// writer's ceiling from one set of DB round trips per event to one per BATCH.
// Linearity is the store's job (advisory lock), not the consumer's.
const chainBatch = 64

// AuditConsumer drains straza.audit.> into the hash-chained audit_log
// in batches. Single-writer linearity is enforced by the STORE
// (AppendChained: Postgres advisory lock / SQLite single connection), not by
// a one-in-flight consumer window, so multiple pods may fetch and append
// concurrently, each batch atomic; duplicates (at-least-once redelivery) are
// filtered inside the same transaction.
type AuditConsumer struct {
	store store.Store
	bus   *events.Bus
	log   *slog.Logger
}

// NewAuditConsumer builds the consumer.
func NewAuditConsumer(st store.Store, bus *events.Bus, log *slog.Logger) *AuditConsumer {
	return &AuditConsumer{store: st, bus: bus, log: log}
}

// Run consumes the audit stream until ctx is done.
func (c *AuditConsumer) Run(ctx context.Context) error {
	cons, err := c.bus.AuditConsumer(ctx)
	if err != nil {
		return fmt.Errorf("spine: audit consumer: %w", err)
	}
	for {
		if ctx.Err() != nil {
			return nil
		}
		batch, err := cons.Fetch(chainBatch, jetstream.FetchMaxWait(2*time.Second))
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return fmt.Errorf("spine: audit fetch: %w", err)
		}
		var msgs []jetstream.Msg
		for m := range batch.Messages() {
			msgs = append(msgs, m)
		}
		if err := batch.Error(); err != nil {
			// Partial delivery (e.g. missed heartbeat): chain what arrived,
			// the rest redelivers.
			c.log.Warn("audit: fetch", "err", err)
		}
		if len(msgs) == 0 {
			continue
		}
		events := make([]store.ChainEvent, len(msgs))
		for i, m := range msgs {
			// An empty id is the defensive untracked path: chained, never
			// deduped (all Straza producers set an id).
			events[i] = store.ChainEvent{CEID: extractCEID(m.Data()), CE: chainForm(m.Data())}
		}
		if _, err := c.store.Audit().AppendChained(ctx, events, audit.Genesis, audit.Link); err != nil {
			c.log.Warn("audit: append batch", "err", err)
			for _, m := range msgs {
				_ = m.Nak() // atomic batch: nothing landed; redeliver, dedupe converges
			}
			continue
		}
		for _, m := range msgs {
			_ = m.Ack()
		}
	}
}

// chainForm returns the bytes the chain stores (spec/events rev 16): capture
// events (straza.audit.prompt/reply) are chained CONTENT-FREE (data.content
// removed, data.contentBytes added beside the client's full-content
// contentHash), so the append-only chain witnesses the conversation by hash
// and size while the text itself lives only in retention-bounded stores.
// Anything unparseable, or any other type, is chained verbatim.
func chainForm(data []byte) string {
	var ce map[string]any
	if json.Unmarshal(data, &ce) != nil {
		return string(data)
	}
	t, _ := ce["type"].(string)
	if t != "straza.audit.prompt" && t != "straza.audit.reply" {
		return string(data)
	}
	d, ok := ce["data"].(map[string]any)
	if !ok {
		return string(data)
	}
	content, ok := d["content"].(string)
	if !ok {
		return string(data)
	}
	delete(d, "content")
	d["contentBytes"] = len(content)
	out, err := json.Marshal(ce)
	if err != nil {
		return string(data)
	}
	return string(out)
}

func extractCEID(data []byte) string {
	var env struct {
		ID string `json:"id"`
	}
	if json.Unmarshal(data, &env) != nil {
		return ""
	}
	return env.ID
}
