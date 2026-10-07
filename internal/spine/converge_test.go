package spine

import (
	"context"
	"io"
	"log/slog"
	"sync/atomic"
	"testing"
	"time"

	natsserver "github.com/nats-io/nats-server/v2/server"

	"github.com/strazahq/straza/internal/config"
	"github.com/strazahq/straza/internal/events"
)

// TestConvergeConsumer pins the multi-pod convergence plumbing: control-plane
// change subjects reach the right reload callback, revocations do not (they
// have their own consumer), and DeliverNew means history published before the
// consumer existed is NOT replayed (boot already loaded current state).
func TestConvergeConsumer(t *testing.T) {
	ns, err := natsserver.NewServer(&natsserver.Options{
		Host: "127.0.0.1", Port: -1,
		JetStream: true, StoreDir: t.TempDir(),
		NoLog: true, NoSigs: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	ns.Start()
	t.Cleanup(ns.Shutdown)
	if !ns.ReadyForConnections(10 * time.Second) {
		t.Fatal("nats not ready")
	}

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	bus, err := events.Start(ctx, config.Config{Events: config.Events{URL: ns.ClientURL()}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(bus.Close)

	// Published BEFORE the consumer exists: must never be delivered.
	if err := bus.Publish(ctx, "straza.policy.updated", []byte(`{"data":{"pre":"history"}}`)); err != nil {
		t.Fatal(err)
	}

	var policy, apps, ident atomic.Int64
	cons := NewConvergeConsumer(bus, Convergence{
		SelfSource: "strazad/self-pod",
		OnPolicy:   func(context.Context) error { policy.Add(1); return nil },
		OnApps:     func(context.Context) error { apps.Add(1); return nil },
		OnIdentity: func(context.Context) error { ident.Add(1); return nil },
	}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	go func() { _ = cons.Run(ctx) }()

	// Give the ephemeral consumer a moment to exist before publishing.
	time.Sleep(300 * time.Millisecond)

	for _, ev := range []struct{ subject, source string }{
		{"straza.policy.updated", "strazad/other-pod"},
		{"straza.policy.updated", "strazad/self-pod"}, // own event: skipped
		{"straza.apps.deployed", "strazad/other-pod"},
		{"straza.apps.drift", "strazad/other-pod"},
		{"straza.identity.updated", "strazad/other-pod"},
		{"straza.revocation.user", "strazad/other-pod"}, // NOT ours
	} {
		if err := bus.Publish(ctx, ev.subject, []byte(`{"source":"`+ev.source+`","data":{}}`)); err != nil {
			t.Fatalf("publish %s: %v", ev.subject, err)
		}
	}

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if policy.Load() == 1 && apps.Load() == 2 && ident.Load() == 1 {
			return // other pods' live events only: no replay, no self, no revocations
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("callback counts: policy=%d (want 1: no replay, no self-event), apps=%d (want 2), identity=%d (want 1)",
		policy.Load(), apps.Load(), ident.Load())
}

// TestConvergeRoutesEvents pins which events reach a handler: another pod's
// event by its subject, none of this pod's own, none that carries draft, and
// a publish's one straza.apps.updated event with change publish to OnApps.
func TestConvergeRoutesEvents(t *testing.T) {
	const self, other = "strazad/self-pod", "strazad/other-pod"
	cases := []struct {
		name, subject, source, data string
		want                        string // the handler reached, empty for none
	}{
		{name: "a policy activation", subject: "straza.policy.updated", source: other, data: `{"snapshot":"s1","sets":2}`, want: "policy"},
		{name: "an access row change", subject: "straza.apps.updated", source: other, data: `{"change":"binding","app":"a1"}`, want: "apps"},
		{name: "an identity change", subject: "straza.identity.updated", source: other, data: `{"id":"r1"}`, want: "identity"},
		{name: "change publish without draft", subject: "straza.apps.updated", source: other, data: `{"change":"publish"}`, want: "apps"},
		{name: "a self-sourced publish event", subject: "straza.apps.updated", source: self, data: `{"change":"publish","draft":"41"}`},
		{name: "a publish's policy event", subject: "straza.policy.updated", source: other, data: `{"snapshot":"s2","sets":3,"draft":"41"}`},
		{name: "a publish's access row event", subject: "straza.apps.updated", source: other, data: `{"change":"binding","app":"a1","draft":"41"}`},
		{name: "a publish's identity event", subject: "straza.identity.updated", source: other, data: `{"id":"r1","draft":"41"}`},
		{name: "a publish's removal event", subject: "straza.apps.removed", source: other, data: `{"app":"a1","name":"github","draft":"41"}`},
		{name: "the publish event", subject: "straza.apps.updated", source: other, data: `{"change":"publish","draft":"41","snapshot":"s2","apps":["github"]}`, want: "apps"},
		{name: "a null draft", subject: "straza.identity.updated", source: other, data: `{"id":"r1","draft":null}`, want: "identity"},
		{name: "an empty draft", subject: "straza.identity.updated", source: other, data: `{"id":"r1","draft":""}`, want: "identity"},
		{name: "data that is not an object", subject: "straza.apps.updated", source: other, data: `"binding"`, want: "apps"},
		{name: "a revocation", subject: "straza.revocation.user", source: other, data: `{"user":"u1"}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var got []string
			reached := func(name string) func(context.Context) error {
				return func(context.Context) error { got = append(got, name); return nil }
			}
			c := NewConvergeConsumer(nil, Convergence{
				SelfSource: self,
				OnPolicy:   reached("policy"),
				OnApps:     reached("apps"),
				OnIdentity: reached("identity"),
			}, slog.New(slog.NewTextHandler(io.Discard, nil)))
			c.Handle(context.Background(), tc.subject, []byte(`{"source":"`+tc.source+`","data":`+tc.data+`}`))
			want := []string{}
			if tc.want != "" {
				want = []string{tc.want}
			}
			if len(got) != len(want) || (len(got) == 1 && got[0] != want[0]) {
				t.Errorf("handlers reached = %v, want %v", got, want)
			}
		})
	}
}

// TestConvergeCatchesUpAfterItSubscribes pins the catch-up's place: the
// consumer calls OnSubscribe only once it subscribed and before any
// handler, so a change that lands while the catch-up reads live state
// arrives as an event. A boot subscribes through Subscribe and its Run adds
// no second catch-up, and a restart's Run subscribes and catches up itself.
func TestConvergeCatchesUpAfterItSubscribes(t *testing.T) {
	cases := []struct {
		name           string
		subscribeFirst bool
		wantApps       int64
	}{
		{name: "a boot, which subscribes before Run", subscribeFirst: true, wantApps: 2},
		{name: "a restart, which Run subscribes", wantApps: 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ns, err := natsserver.NewServer(&natsserver.Options{
				Host: "127.0.0.1", Port: -1, JetStream: true, StoreDir: t.TempDir(), NoLog: true, NoSigs: true,
			})
			if err != nil {
				t.Fatal(err)
			}
			ns.Start()
			t.Cleanup(ns.Shutdown)
			if !ns.ReadyForConnections(10 * time.Second) {
				t.Fatal("nats not ready")
			}
			ctx, cancel := context.WithCancel(context.Background())
			t.Cleanup(cancel)
			bus, err := events.Start(ctx, config.Config{Events: config.Events{URL: ns.ClientURL()}})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(bus.Close)
			change := []byte(`{"source":"strazad/other-pod","data":{"change":"binding"}}`)

			var catchUps, apps atomic.Int64
			cons := NewConvergeConsumer(bus, Convergence{
				SelfSource: "strazad/self-pod",
				OnSubscribe: func(c context.Context) error {
					catchUps.Add(1)
					if apps.Load() != 0 {
						t.Error("a handler ran before the catch-up")
					}
					// A change lands while the catch-up reads live state.
					return bus.Publish(c, "straza.apps.updated", change)
				},
				OnPolicy:   func(context.Context) error { return nil },
				OnApps:     func(context.Context) error { apps.Add(1); return nil },
				OnIdentity: func(context.Context) error { return nil },
			}, slog.New(slog.NewTextHandler(io.Discard, nil)))
			if tc.subscribeFirst {
				if err := cons.Subscribe(ctx); err != nil {
					t.Fatal(err)
				}
				// A change lands between the boot and the first Run.
				if err := bus.Publish(ctx, "straza.apps.updated", change); err != nil {
					t.Fatal(err)
				}
			}
			go func() { _ = cons.Run(ctx) }()

			deadline := time.Now().Add(5 * time.Second)
			for apps.Load() < tc.wantApps && time.Now().Before(deadline) {
				time.Sleep(20 * time.Millisecond)
			}
			if apps.Load() != tc.wantApps || catchUps.Load() != 1 {
				t.Errorf("OnApps ran %d times and OnSubscribe %d, want %d and 1", apps.Load(), catchUps.Load(), tc.wantApps)
			}
		})
	}
}
