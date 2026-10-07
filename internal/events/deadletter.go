package events

import (
	"context"
	"fmt"
	"time"

	"github.com/nats-io/nats.go/jetstream"
)

// The dead-letter lane: a sink delivery the receiver will never accept (a
// deterministic 4xx, or a retryable failure past its attempt budget) is
// PARKED here by the sink runner and acked on its source stream, so the
// spool keeps flowing and the event is preserved, counted, listed, and
// replayable. It is its own stream, NOT a spine subject: StreamSubjects()
// never lists it, so a sink filtering `straza.>` cannot consume its own
// parked events.
//
// Bounded and DiscardNew on purpose: a full lane refuses new parks (the
// runner then keeps the message retrying on its source stream, loudly)
// instead of silently shedding the oldest parked event. Unbounded file
// storage can fill the disk, and silent shedding would break the lane's
// promise.
const (
	deadLetterStream          = "STRAZA_DEADLETTER"
	deadLetterPrefix          = "straza.deadletter."
	defaultDeadLetterMaxBytes = 256 << 20
)

var deadLetterStreamDef = jetstream.StreamConfig{
	Name:     deadLetterStream,
	Subjects: []string{deadLetterPrefix + ">"},
	Storage:  jetstream.FileStorage,
	MaxBytes: defaultDeadLetterMaxBytes,
	Discard:  jetstream.DiscardNew,
}

// DeadLetterSubject is the per-sink subject parked records ride; key is the
// sink's durable-safe name (the caller sanitizes).
func DeadLetterSubject(key string) string { return deadLetterPrefix + key }

// ParkDeadLetter publishes one parked record for sink key with JetStream
// ack. A full lane answers an error (DiscardNew) and the caller keeps the
// source message instead of dropping it.
func (b *Bus) ParkDeadLetter(ctx context.Context, key string, data []byte) error {
	if _, err := b.js.Publish(ctx, DeadLetterSubject(key), data); err != nil {
		return fmt.Errorf("events: park dead-letter %s: %w", key, err)
	}
	return nil
}

// DeadLetterCount reports how many records sink key holds in the lane.
func (b *Bus) DeadLetterCount(ctx context.Context, key string) (uint64, error) {
	s, err := b.js.Stream(ctx, deadLetterStream)
	if err != nil {
		return 0, fmt.Errorf("events: dead-letter stream: %w", err)
	}
	subject := DeadLetterSubject(key)
	info, err := s.Info(ctx, jetstream.WithSubjectFilter(subject))
	if err != nil {
		return 0, fmt.Errorf("events: dead-letter info: %w", err)
	}
	return info.State.Subjects[subject], nil
}

// DeadLetterConsumer returns a fresh ephemeral pull consumer over sink key's
// parked records, oldest first (replay order). It expires on its own
// shortly after the caller stops fetching.
func (b *Bus) DeadLetterConsumer(ctx context.Context, key string) (jetstream.Consumer, error) {
	return b.js.CreateConsumer(ctx, deadLetterStream, jetstream.ConsumerConfig{
		AckPolicy:         jetstream.AckExplicitPolicy,
		DeliverPolicy:     jetstream.DeliverAllPolicy,
		FilterSubject:     DeadLetterSubject(key),
		InactiveThreshold: 30 * time.Second,
	})
}

// DeleteDeadLetter removes one parked record by its stream sequence: the
// durable "replayed, done" mark.
func (b *Bus) DeleteDeadLetter(ctx context.Context, seq uint64) error {
	s, err := b.js.Stream(ctx, deadLetterStream)
	if err != nil {
		return fmt.Errorf("events: dead-letter stream: %w", err)
	}
	if err := s.DeleteMsg(ctx, seq); err != nil {
		return fmt.Errorf("events: delete dead-letter %d: %w", seq, err)
	}
	return nil
}
