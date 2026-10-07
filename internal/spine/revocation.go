package spine

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/strazahq/straza/internal/events"
)

// DenylistSink applies revocations to an in-memory denylist. The gateway's
// denylist implements it, and the consumer feeds it from the event spine so
// any stateless pod converges on the same revocation set.
type DenylistSink interface {
	RevokeUser(id string)
	RevokeSession(id string)
	RevokeDevice(id string)
	// AllowUser lifts a user-level entry (reactivation). Sessions
	// already revoked stay revoked.
	AllowUser(id string)
}

// RevocationConsumer subscribes to straza.revocation.> and applies each
// event to the denylist (the kill switch). Ephemeral + DeliverAll means a
// fresh pod replays history and rebuilds its denylist at startup.
type RevocationConsumer struct {
	bus  *events.Bus
	sink DenylistSink
	log  *slog.Logger
}

// NewRevocationConsumer builds the consumer.
func NewRevocationConsumer(bus *events.Bus, sink DenylistSink, log *slog.Logger) *RevocationConsumer {
	return &RevocationConsumer{bus: bus, sink: sink, log: log}
}

// Run consumes the revocation stream until ctx is done.
func (c *RevocationConsumer) Run(ctx context.Context) error {
	cons, err := c.bus.RevocationConsumer(ctx)
	if err != nil {
		return fmt.Errorf("spine: revocation consumer: %w", err)
	}
	iter, err := cons.Messages()
	if err != nil {
		return fmt.Errorf("spine: revocation messages: %w", err)
	}
	go func() {
		<-ctx.Done()
		iter.Stop()
	}()
	for {
		msg, err := iter.Next()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return fmt.Errorf("spine: revocation next: %w", err)
		}
		c.apply(msg.Subject(), msg.Data())
	}
}

// apply maps a revocation CloudEvent onto the denylist. Stream order is
// preserved per subject-space, so a replayed [revoke user U, lift user U]
// history converges on "allowed" (spec/events §revocation).
func (c *RevocationConsumer) apply(subject string, data []byte) {
	var env struct {
		Data struct {
			User     string   `json:"user"`
			Session  string   `json:"session"`
			Sessions []string `json:"sessions"`
			Device   string   `json:"device"`
		} `json:"data"`
	}
	if err := json.Unmarshal(data, &env); err != nil {
		c.log.Warn("revocation: bad payload", "subject", subject, "err", err)
		return
	}
	if subject == "straza.revocation.lift" {
		if env.Data.User != "" {
			c.sink.AllowUser(env.Data.User)
		}
		return
	}
	// Bulk stand-down (rev 17): one control event, the whole set. Each id
	// applies exactly like a straza.revocation.session event (re-applied ids
	// are no-ops; the denylist is a set).
	if subject == "straza.revocation.sessions" {
		for _, id := range env.Data.Sessions {
			if id != "" {
				c.sink.RevokeSession(id)
			}
		}
		return
	}
	switch {
	case env.Data.User != "":
		c.sink.RevokeUser(env.Data.User)
	case env.Data.Session != "":
		c.sink.RevokeSession(env.Data.Session)
	case env.Data.Device != "":
		c.sink.RevokeDevice(env.Data.Device)
	}
}
