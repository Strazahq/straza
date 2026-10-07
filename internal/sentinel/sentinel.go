// Package sentinel is the audit sentinel (no model): an asynchronous
// JetStream consumer over the audit stream that judges SESSIONS rather than
// events, with rule-based detectors over in-memory sliding windows, and
// emits every finding as a straza.audit.sentinel CloudEvent back onto the
// audit stream, so the judge is judged by the same hash chain.
//
// Posture, deliberately opposite to the inline classifier: the sentinel
// fails open with an alarm. It never sits on a request path (it is a
// consumer of evidence that already exists), it never blocks anything, and
// it revokes nothing: it only alerts, and the manual revoke affordance is
// the console's, over the existing admin revoke endpoint. Detector state is
// in-memory only: a restart loses open windows (an accepted detection gap,
// because the durable consumer cursor resumes the feed).
package sentinel

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/strazahq/straza/internal/config"
	"github.com/strazahq/straza/internal/events"
)

// SubjectVerdict is the CE type and NATS subject sentinel verdicts ride on.
// It lands in STRAZA_AUDIT, where the audit-chain consumer (straza.audit.>)
// hash-chains it like any other audit event. The sentinel's own consumer
// does NOT subscribe to it: no feedback loop.
const SubjectVerdict = "straza.audit.sentinel"

// Sentinel consumes audit events and emits verdicts. One Run goroutine owns
// all engine state; the caller restarts Run with backoff on error.
type Sentinel struct {
	bus *events.Bus
	log *slog.Logger
	eng *engine
}

// New builds a sentinel over the bus with cfg thresholds.
func New(bus *events.Bus, cfg config.Sentinel, log *slog.Logger) *Sentinel {
	return &Sentinel{bus: bus, log: log, eng: newEngine(cfg, time.Now)}
}

// Run consumes the durable sentinel feed until ctx is done. Errors are
// returned for the caller's restart-with-backoff loop, never fatal to
// strazad (fail open-with-alarm).
func (s *Sentinel) Run(ctx context.Context) error {
	cons, err := s.bus.SentinelConsumer(ctx)
	if err != nil {
		return fmt.Errorf("sentinel: consumer: %w", err)
	}
	iter, err := cons.Messages()
	if err != nil {
		return fmt.Errorf("sentinel: messages: %w", err)
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
			return fmt.Errorf("sentinel: next: %w", err)
		}
		s.handle(ctx, msg.Data())
		// Always ack: detector state is in-memory, so a redelivery would
		// double-count windows; and a lost VERDICT is a logged detection gap,
		// never a reason to wedge the feed (fail open-with-alarm). The chain
		// writer preserved the raw event regardless.
		_ = msg.Ack()
	}
}

func (s *Sentinel) handle(ctx context.Context, raw []byte) {
	ev, ok := parseCE(raw, time.Now().UTC())
	if !ok {
		return // malformed or out-of-scope CE: drop, the chain has the bytes
	}
	for _, v := range s.eng.observe(ev) {
		if err := s.emit(ctx, v); err != nil {
			s.log.Error("sentinel: verdict publish failed (detection gap)",
				"detector", v.Detector, "session", v.Session, "err", err)
			continue
		}
		s.log.Warn("sentinel verdict", "detector", v.Detector, "severity", v.Severity,
			"session", v.Session, "user", v.User, "reason", v.Reason)
	}
}

// emit publishes one verdict as a CloudEvent on the audit stream (same
// envelope style as the PDP's audit producer).
func (s *Sentinel) emit(ctx context.Context, v Verdict) error {
	if v.Evidence == nil {
		v.Evidence = []string{}
	}
	data := map[string]any{
		"session":  v.Session,
		"user":     v.User,
		"detector": v.Detector,
		"severity": v.Severity,
		"reason":   v.Reason,
		"evidence": v.Evidence,
	}
	if v.Window != "" {
		data["window"] = v.Window
	}
	ce, err := json.Marshal(map[string]any{
		"specversion": "1.0",
		"id":          uuid.NewString(),
		"type":        SubjectVerdict,
		"source":      "strazad-sentinel",
		"time":        time.Now().UTC().Format(time.RFC3339Nano),
		"data":        data,
	})
	if err != nil {
		return err
	}
	return s.bus.Publish(ctx, SubjectVerdict, ce)
}

// parseCE extracts the sentinel-relevant fields from one audit CE. Only the
// four consumed types pass; everything else (including straza.audit.sentinel
// itself) is dropped. Events without a session cannot be windowed and drop
// too. A missing/bad CE time falls back to fallback (wall clock).
func parseCE(raw []byte, fallback time.Time) (event, bool) {
	var ce struct {
		ID   string `json:"id"`
		Type string `json:"type"`
		Time string `json:"time"`
		Data struct {
			Session  string   `json:"session"`
			User     string   `json:"user"`
			Tool     string   `json:"tool"`
			App      string   `json:"app"`
			ToolName string   `json:"toolName"`
			Command  string   `json:"command"`
			Effect   string   `json:"effect"`
			Content  string   `json:"content"`
			Paths    []string `json:"paths"`
		} `json:"data"`
	}
	if json.Unmarshal(raw, &ce) != nil {
		return event{}, false
	}
	kind := strings.TrimPrefix(ce.Type, "straza.audit.")
	switch kind {
	case "tool", "mcp", "prompt", "reply":
	default:
		return event{}, false
	}
	if ce.Data.Session == "" {
		return event{}, false
	}
	at := fallback
	if t, err := time.Parse(time.RFC3339Nano, ce.Time); err == nil {
		at = t.UTC()
	}
	return event{
		ID: ce.ID, Kind: kind, At: at,
		Session: ce.Data.Session, User: ce.Data.User,
		Tool: ce.Data.Tool, App: ce.Data.App, ToolName: ce.Data.ToolName,
		Command: ce.Data.Command, Effect: ce.Data.Effect,
		Content: ce.Data.Content, Paths: ce.Data.Paths,
	}, true
}
