package events

import (
	"context"
	"testing"
	"time"

	"github.com/nats-io/nats.go/jetstream"
)

// TestDeadLetterLane pins the third stream: parked records land on
// straza.deadletter.<key> in STRAZA_DEADLETTER, counts are per key, a record
// can be fetched and then deleted, and the lane is INVISIBLE to sink subject
// intersection (a `straza.>` sink must never consume its own parked events).
func TestDeadLetterLane(t *testing.T) {
	b := startTestBus(t)
	ctx := context.Background()

	if _, ok := StreamSubjects()["STRAZA_DEADLETTER"]; ok {
		t.Fatal("dead-letter stream leaked into StreamSubjects (sinks would consume it)")
	}
	for _, subs := range StreamSubjects() {
		for _, s := range subs {
			if s == "straza.deadletter.>" {
				t.Fatal("dead-letter subject leaked into the spine subject space")
			}
		}
	}

	if n, err := b.DeadLetterCount(ctx, "es"); err != nil || n != 0 {
		t.Fatalf("empty count = %d, %v", n, err)
	}
	if err := b.ParkDeadLetter(ctx, "es", []byte(`{"sink":"es"}`)); err != nil {
		t.Fatalf("ParkDeadLetter: %v", err)
	}
	if err := b.ParkDeadLetter(ctx, "other", []byte(`{"sink":"other"}`)); err != nil {
		t.Fatalf("ParkDeadLetter other: %v", err)
	}
	if n, _ := b.DeadLetterCount(ctx, "es"); n != 1 {
		t.Fatalf("es count = %d, want 1", n)
	}
	if n, _ := b.DeadLetterCount(ctx, "other"); n != 1 {
		t.Fatalf("other count = %d, want 1", n)
	}

	cons, err := b.DeadLetterConsumer(ctx, "es")
	if err != nil {
		t.Fatalf("DeadLetterConsumer: %v", err)
	}
	batch, err := cons.Fetch(10, jetstream.FetchMaxWait(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	var seq uint64
	got := 0
	for m := range batch.Messages() {
		got++
		md, err := m.Metadata()
		if err != nil {
			t.Fatal(err)
		}
		seq = md.Sequence.Stream
		if string(m.Data()) != `{"sink":"es"}` {
			t.Fatalf("data = %s", m.Data())
		}
		_ = m.Ack()
	}
	if got != 1 {
		t.Fatalf("fetched %d, want 1 (filter is per key)", got)
	}
	if err := b.DeleteDeadLetter(ctx, seq); err != nil {
		t.Fatalf("DeleteDeadLetter: %v", err)
	}
	if n, _ := b.DeadLetterCount(ctx, "es"); n != 0 {
		t.Fatalf("es count after delete = %d, want 0", n)
	}
	if n, _ := b.DeadLetterCount(ctx, "other"); n != 1 {
		t.Fatalf("other untouched = %d, want 1", n)
	}
}
