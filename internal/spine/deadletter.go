package spine

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/nats-io/nats.go/jetstream"

	"github.com/strazahq/straza/internal/events"
)

// DeadLetterRecord is one parked delivery: self-describing (which sink,
// which stream and subject, how many attempts, why) and carrying the
// CloudEvent verbatim, so a replay needs nothing but the record.
type DeadLetterRecord struct {
	Sink     string          `json:"sink"`
	Stream   string          `json:"stream"`
	Subject  string          `json:"subject"`
	Seq      uint64          `json:"seq"`
	Attempts uint64          `json:"attempts"`
	Reason   string          `json:"reason"`
	ParkedAt time.Time       `json:"parked_at"`
	CE       json.RawMessage `json:"ce"`
}

// newDeadLetterRecord builds the record for one source message. Event
// payloads are CloudEvents JSON; a non-JSON payload (never produced by the
// relay, guarded anyway) is carried as a JSON string.
func newDeadLetterRecord(sink, stream, subject string, seq, attempts uint64, reason string, ce []byte) DeadLetterRecord {
	rec := DeadLetterRecord{Sink: sink, Stream: stream, Subject: subject, Seq: seq,
		Attempts: attempts, Reason: reason, ParkedAt: time.Now().UTC()}
	if json.Valid(ce) {
		rec.CE = json.RawMessage(ce)
	} else {
		quoted, _ := json.Marshal(string(ce))
		rec.CE = quoted
	}
	return rec
}

// Payload returns the event bytes to deliver.
func (r DeadLetterRecord) Payload() []byte {
	if len(r.CE) > 0 && r.CE[0] == '"' {
		var s string
		if err := json.Unmarshal(r.CE, &s); err == nil {
			return []byte(s)
		}
	}
	return []byte(r.CE)
}

// DeadLetterKey is the lane key for a sink name (its durable-safe form).
func DeadLetterKey(sinkName string) string { return sanitizeDurable(sinkName) }

// SinkMetrics is the observer the runner and the replayer report to (the
// server wires Prometheus counters; nil is allowed).
type SinkMetrics interface {
	SinkParked(sink string)
	SinkDuplicate(sink string)
	SinkReplayed(sink string)
}

// ReplayResult is one replay call's outcome.
type ReplayResult struct {
	// Replayed is how many parked events the sink accepted (and which left
	// the lane).
	Replayed int `json:"replayed"`
	// Remaining is how many records the sink still holds in the lane after
	// this call.
	Remaining int `json:"remaining"`
	// Stopped names the failure that ended the replay early; empty when the
	// call ran to its limit or emptied the lane.
	Stopped string `json:"stopped,omitempty"`
}

// replayMaxPerCall bounds one replay call so the admin request that drives
// it stays short; operators loop (the response says what remains).
const replayMaxPerCall = 1000

// ReplaySink re-delivers sink's parked events oldest first, deleting each
// from the lane as the receiver accepts it (a 409 counts: the receiver has
// it), and stops at the first failure, leaving that record and everything
// behind it parked. Nothing is re-parked and nothing is dropped.
func ReplaySink(ctx context.Context, bus *events.Bus, sink Sink, limit int, obs SinkMetrics) (ReplayResult, error) {
	var res ReplayResult
	if limit <= 0 || limit > replayMaxPerCall {
		limit = replayMaxPerCall
	}
	key := DeadLetterKey(sink.Name())
	cons, err := bus.DeadLetterConsumer(ctx, key)
	if err != nil {
		return res, fmt.Errorf("replay %s: %w", sink.Name(), err)
	}
	finish := func() (ReplayResult, error) {
		n, err := bus.DeadLetterCount(ctx, key)
		if err != nil {
			return res, err
		}
		res.Remaining = int(n) // #nosec G115 -- a lane count, never near overflow
		return res, nil
	}
	for res.Replayed < limit {
		want := limit - res.Replayed
		if want > 50 {
			want = 50
		}
		batch, err := cons.Fetch(want, jetstream.FetchMaxWait(time.Second))
		if err != nil {
			return res, fmt.Errorf("replay %s: fetch: %w", sink.Name(), err)
		}
		got := 0
		for m := range batch.Messages() {
			got++
			var rec DeadLetterRecord
			if err := json.Unmarshal(m.Data(), &rec); err != nil {
				res.Stopped = fmt.Sprintf("parked record unreadable: %v", err)
				return finish()
			}
			md, err := m.Metadata()
			if err != nil {
				return res, fmt.Errorf("replay %s: metadata: %w", sink.Name(), err)
			}
			derr := sink.Deliver(ctx, rec.Subject, rec.Payload())
			switch classify(derr) {
			case classDelivered, classDuplicate:
				if err := bus.DeleteDeadLetter(ctx, md.Sequence.Stream); err != nil {
					return res, err
				}
				_ = m.Ack()
				res.Replayed++
				if obs != nil {
					obs.SinkReplayed(sink.Name())
				}
			default:
				res.Stopped = derr.Error()
				return finish()
			}
		}
		if err := batch.Error(); err != nil {
			return res, fmt.Errorf("replay %s: batch: %w", sink.Name(), err)
		}
		if got == 0 {
			break // lane empty
		}
	}
	return finish()
}
