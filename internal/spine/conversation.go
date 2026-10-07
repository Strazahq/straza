package spine

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/nats-io/nats.go/jetstream"

	"github.com/strazahq/straza/internal/bodystore"
	"github.com/strazahq/straza/internal/events"
	"github.com/strazahq/straza/internal/store"
)

// ConversationMaxContent is the server-side ceiling on stored turn content.
// The CLIENT caps first (its knob, default 64 KiB); this is the defensive
// bound against a hostile or misconfigured client: content beyond it is cut
// and the turn is marked truncated (the CE keeps the original contentHash,
// so the chain still witnesses the full text).
const ConversationMaxContent = 64 << 10

// ConversationConsumer mirrors straza.audit.{prompt,reply} into the
// conversation_turns read model.
// The chain writer remains the tamper-evidence surface; this consumer builds
// the READABLE one. Duplicates (at-least-once) are dropped by CE id.
type ConversationConsumer struct {
	store store.Store
	bus   *events.Bus
	log   *slog.Logger
	// bodies, when non-nil, receives every turn's body BEFORE its row
	// lands. Rows then store metadata only and reads resolve transparently.
	bodies bodystore.Store
}

// NewConversationConsumer builds the consumer; bodies may be nil (inline).
func NewConversationConsumer(st store.Store, bus *events.Bus, log *slog.Logger, bodies bodystore.Store) *ConversationConsumer {
	return &ConversationConsumer{store: st, bus: bus, log: log, bodies: bodies}
}

// Run consumes capture events until ctx is done, in batches: fetch up
// to a chain-writer-sized batch, parse each turn, land the batch as ONE
// transaction (multi-row insert + summary upserts). A failed batch NAKs
// whole: nothing landed (atomic), and redelivery converges through the
// in-tx CE id filter.
func (c *ConversationConsumer) Run(ctx context.Context) error {
	cons, err := c.bus.ConversationConsumer(ctx)
	if err != nil {
		return fmt.Errorf("spine: conversation consumer: %w", err)
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
			return fmt.Errorf("spine: conversation fetch: %w", err)
		}
		var msgs []jetstream.Msg
		for m := range batch.Messages() {
			msgs = append(msgs, m)
		}
		if err := batch.Error(); err != nil {
			c.log.Warn("conversation: fetch", "err", err)
		}
		if len(msgs) == 0 {
			continue
		}
		var turns []store.ConversationTurn
		for _, m := range msgs {
			if turn, ok := c.parse(m.Data()); ok {
				turns = append(turns, turn)
			}
		}
		if err := c.externalize(ctx, turns); err != nil {
			c.log.Warn("conversation: body store put", "err", err)
			for _, m := range msgs {
				_ = m.Nak() // body-then-row: nothing landed, redeliver
			}
			continue
		}
		if _, err := c.store.Conversations().InsertBatch(ctx, turns); err != nil {
			if errors.Is(err, store.ErrInvalidData) {
				c.landEach(ctx, msgs)
				continue
			}
			c.log.Warn("conversation: insert batch", "err", err)
			for _, m := range msgs {
				_ = m.Nak()
			}
			continue
		}
		for _, m := range msgs {
			_ = m.Ack()
		}
	}
}

// landEach lands a batch one turn at a time after the database refused the
// batch on a row's values: the refused turn is dropped from the read model
// and terminated so it cannot block every turn behind it. The audit chain
// keeps its bytes (the chain writer consumed the same CE), so the evidence
// surface loses nothing. Any other failure NAKs the turn as the batch path
// does.
func (c *ConversationConsumer) landEach(ctx context.Context, msgs []jetstream.Msg) {
	for _, m := range msgs {
		turn, ok := c.parse(m.Data())
		if !ok {
			_ = m.Ack()
			continue
		}
		one := []store.ConversationTurn{turn}
		err := c.externalize(ctx, one)
		if err == nil {
			_, err = c.store.Conversations().InsertBatch(ctx, one)
		}
		switch {
		case err == nil:
			_ = m.Ack()
		case errors.Is(err, store.ErrInvalidData):
			c.log.Warn("conversation: turn refused by the store and dropped from transcripts; the audit chain keeps it",
				"ce", turn.CEID, "session", turn.SessionID, "err", err)
			_ = m.Term()
		default:
			_ = m.Nak()
		}
	}
}

// externalize persists bodies to the body store and flags the turns:
// body BEFORE row (a row must only ever reference a durable body), with one
// Put per unique hash (content-addressing dedupes within the batch too).
// The key is the FULL-content hash even for a truncated turn (the cap is
// deterministic, so the mapping stays unique). Self-verification by key
// therefore holds for untruncated bodies, and truncated ones carry their
// flag. No-op without a configured store.
func (c *ConversationConsumer) externalize(ctx context.Context, turns []store.ConversationTurn) error {
	if c.bodies == nil {
		return nil
	}
	done := map[string]bool{}
	for i := range turns {
		t := &turns[i]
		if !done[t.ContentHash] {
			if err := c.bodies.Put(ctx, t.ContentHash, []byte(t.Content)); err != nil {
				return err
			}
			done[t.ContentHash] = true
		}
		t.BodyExternal = true
	}
	return nil
}

// parse extracts one stored turn from a capture CE; ok=false drops the
// message (malformed or not a prompt/reply; the chain writer already
// preserved the raw bytes).
func (c *ConversationConsumer) parse(data []byte) (store.ConversationTurn, bool) {
	var ce struct {
		ID   string `json:"id"`
		Type string `json:"type"`
		Time string `json:"time"`
		Data struct {
			Session     string `json:"session"`
			User        string `json:"user"`
			Content     string `json:"content"`
			Mode        string `json:"mode"`
			Truncated   bool   `json:"truncated"`
			ContentHash string `json:"contentHash"`
			AgentType   string `json:"agentType"` // delegate class (spec/events rev 13); "" = main lane
		} `json:"data"`
	}
	if err := json.Unmarshal(data, &ce); err != nil {
		// Malformed capture events are logged and dropped, not redelivered
		// forever: the chain writer already preserved the raw bytes.
		c.log.Warn("conversation: malformed CE, dropped", "err", err)
		return store.ConversationTurn{}, false
	}
	kind := strings.TrimPrefix(ce.Type, "straza.audit.")
	if kind != "prompt" && kind != "reply" {
		return store.ConversationTurn{}, false
	}
	turn := store.ConversationTurn{
		CEID:      ce.ID,
		SessionID: ce.Data.Session,
		UserID:    ce.Data.User,
		Kind:      kind,
		Mode:      ce.Data.Mode,
		Content:   ce.Data.Content,
		Truncated: ce.Data.Truncated,
		AgentType: ce.Data.AgentType,
	}
	if turn.Mode == "" {
		turn.Mode = "verbatim"
	}
	if t, err := time.Parse(time.RFC3339Nano, ce.Time); err == nil {
		turn.At = t.UTC()
	}
	turn.ContentHash = ce.Data.ContentHash
	if turn.ContentHash == "" {
		sum := sha256.Sum256([]byte(turn.Content))
		turn.ContentHash = "sha256:" + hex.EncodeToString(sum[:])
	}
	if len(turn.Content) > ConversationMaxContent {
		// The cut is by byte and may split a multibyte character; the
		// dangling lead bytes are dropped (Postgres refuses invalid UTF-8).
		turn.Content = strings.ToValidUTF8(turn.Content[:ConversationMaxContent], "")
		turn.Truncated = true
	}
	return turn, true
}
