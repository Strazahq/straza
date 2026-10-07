package spine

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"sync"

	"github.com/nats-io/nats.go/jetstream"

	"github.com/strazahq/straza/internal/events"
)

// Convergence is what one pod refreshes when ANOTHER pod changes the control
// plane (multi-pod HA): every callback re-reads the shared store and swaps
// in-memory state atomically, so control-plane reads happen on events, never
// on request paths. Callbacks must be idempotent and cheap: pods also receive
// their own events, and control-plane change rates are human-scale.
type Convergence struct {
	// SelfSource is this pod's CE source id: events it emitted itself are
	// skipped. The in-process handlers already applied the change, and
	// skipping keeps the gateway's zero-DB window deterministic.
	SelfSource string
	// OnSubscribe applies the persisted config once the consumer has
	// subscribed and before it handles the first event, at every
	// subscription: DeliverNew never delivers what changed before it, and a
	// change after its read arrives as an event.
	OnSubscribe func(context.Context) error
	// OnPolicy applies the persisted config after a policy activation on
	// another pod.
	OnPolicy func(context.Context) error
	// OnApps applies the persisted config after binding CRUD, secret
	// rotation or a server installed, changed, paused or removed on another
	// pod, and after another pod's publish of a draft, whose one
	// straza.apps.updated event with change publish stands for all of its
	// events.
	OnApps func(context.Context) error
	// OnIdentity bumps the role-resolution epoch so the next resolution
	// re-reads the store. Live sessions converge at token refresh (≤ TTL),
	// the same bound single-pod sessions have; immediate cut-off is the
	// revocation consumer's job.
	OnIdentity func(context.Context) error
}

// ConvergeConsumer applies other pods' control-plane changes to this pod's
// in-memory state. Without it, a policy activated (or a binding changed) via
// pod A would be served stale by pod B until B restarted; the multi-pod HA
// test exercises exactly that.
type ConvergeConsumer struct {
	bus  *events.Bus
	conv Convergence
	log  *slog.Logger
	// mu guards next, the subscription Subscribe made for the next Run.
	mu   sync.Mutex
	next jetstream.MessagesContext
}

// NewConvergeConsumer builds the consumer.
func NewConvergeConsumer(bus *events.Bus, conv Convergence, log *slog.Logger) *ConvergeConsumer {
	return &ConvergeConsumer{bus: bus, conv: conv, log: log}
}

// Subscribe subscribes now and then runs OnSubscribe, for a boot that
// applies live state before it serves: the next Run handles the events
// from this subscription on and adds no second catch-up. It answers the
// subscription's error or the catch-up's.
func (c *ConvergeConsumer) Subscribe(ctx context.Context) error {
	iter, err := c.subscribe(ctx)
	if err != nil {
		return err
	}
	c.mu.Lock()
	c.next = iter
	c.mu.Unlock()
	return c.catchUp(ctx)
}

// Run consumes control-plane change events until ctx is done. It reads the
// subscription Subscribe made, or subscribes and then catches up through
// OnSubscribe before the first event, as after a restart, logging a failed
// catch-up, which the apply retries itself.
func (c *ConvergeConsumer) Run(ctx context.Context) error {
	c.mu.Lock()
	iter := c.next
	c.next = nil
	c.mu.Unlock()
	if iter == nil {
		var err error
		if iter, err = c.subscribe(ctx); err != nil {
			return err
		}
		if err := c.catchUp(ctx); err != nil {
			c.log.Error("converge: the catch-up after subscribing failed; this pod may serve stale control-plane state", "err", err)
		}
	}
	for {
		msg, err := iter.Next()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return fmt.Errorf("spine: converge next: %w", err)
		}
		c.Handle(ctx, msg.Subject(), msg.Data())
	}
}

// subscribe creates the consumer and its message stream, which stops when
// ctx is done.
func (c *ConvergeConsumer) subscribe(ctx context.Context) (jetstream.MessagesContext, error) {
	cons, err := c.bus.ConvergeConsumer(ctx)
	if err != nil {
		return nil, fmt.Errorf("spine: converge consumer: %w", err)
	}
	iter, err := cons.Messages()
	if err != nil {
		return nil, fmt.Errorf("spine: converge messages: %w", err)
	}
	go func() {
		<-ctx.Done()
		iter.Stop()
	}()
	return iter, nil
}

// catchUp runs OnSubscribe when it is set.
func (c *ConvergeConsumer) catchUp(ctx context.Context) error {
	if c.conv.OnSubscribe == nil {
		return nil
	}
	return c.conv.OnSubscribe(ctx)
}

// Handle applies one event of the converge stream, as Run does for every
// message: it skips this pod's own events and the events of a publish that
// its publish event stands for, and calls the handler of the subject,
// logging a failure. A test feeds a pod its events through it in the order
// it chooses.
func (c *ConvergeConsumer) Handle(ctx context.Context, subject string, data []byte) {
	var env struct {
		Source string          `json:"source"`
		Data   json.RawMessage `json:"data"`
	}
	if json.Unmarshal(data, &env) == nil && env.Source != "" && env.Source == c.conv.SelfSource {
		return // our own change; in-process handlers already applied it
	}
	if partOfPublish(subject, env.Data) {
		return
	}
	var err error
	handled := true
	switch {
	case subject == "straza.policy.updated":
		err = c.conv.OnPolicy(ctx)
	case strings.HasPrefix(subject, "straza.apps."):
		err = c.conv.OnApps(ctx)
	case strings.HasPrefix(subject, "straza.identity."):
		err = c.conv.OnIdentity(ctx)
	default:
		handled = false
	}
	switch {
	case err != nil:
		// Stale-but-serving beats dead: log loudly, keep consuming. The apply
		// retries itself, and the next change event runs it again.
		c.log.Error("converge: reload failed; this pod may serve stale control-plane state",
			"subject", subject, "err", err)
	case handled:
		// Rare (control-plane changes are human-scale) and worth an
		// operator's eye: this pod converged on another pod's change.
		c.log.Info("converge: reloaded from another pod", "component", "converge",
			"subject", subject, "source", env.Source)
	}
}

// partOfPublish reports whether an event is one of a publish's events that
// its publish event stands for: data carries draft, and the event is not
// straza.apps.updated with change publish. The outbox drain may deliver a
// publish's events in any order, and the one apply the publish event runs
// reads the whole config, so a handler keyed by subject never applies part
// of a publish. Data that does not decode carries no draft.
func partOfPublish(subject string, data json.RawMessage) bool {
	var d struct {
		Draft  any `json:"draft"`
		Change any `json:"change"`
	}
	if len(data) == 0 || json.Unmarshal(data, &d) != nil || d.Draft == nil || d.Draft == "" {
		return false
	}
	return subject != "straza.apps.updated" || d.Change != "publish"
}
