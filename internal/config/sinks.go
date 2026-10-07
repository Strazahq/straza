package config

// Sinks section: event-spine forwarding to external systems.
// Types, validation. Sinks have no env faces by design.

import (
	"fmt"
)

// Sink forwards the event spine to an external system: a
// SIEM-facing webhook (HMAC-signed CloudEvents) or an append-only JSONL
// file. Delivery is at-least-once from durable JetStream consumers, so a
// sink outage delays events but never loses them.
type Sink struct {
	// Name identifies the sink; it becomes the durable consumer name, so
	// renaming resets the sink's cursor to the start of the stream.
	Name string `yaml:"name"`
	// Type is "webhook" or "file".
	Type string `yaml:"type"`
	// URL is the webhook endpoint (type=webhook).
	URL string `yaml:"url"`
	// Secret is the HMAC-SHA256 key for X-Straza-Signature (optional;
	// prefer SecretFile so the key stays out of the config document).
	Secret string `yaml:"secret"`
	// Headers are extra request headers for type=webhook, sent on every
	// delivery; they may override the Content-Type default (Elasticsearch
	// wants application/json + `Authorization: Basic …`; Splunk HEC wants
	// `Authorization: Splunk <token>`) but never the X-Straza-* headers.
	// Header values live in the config document, so treat a config carrying
	// receiver credentials with the same care as one carrying `secret`.
	Headers map[string]string `yaml:"headers"`
	// SecretFile reads the HMAC key from a file at boot.
	SecretFile string `yaml:"secretFile"`
	// Path is the JSONL file to append to (type=file).
	Path string `yaml:"path"`
	// Subjects filters what the sink receives: exact subjects or ".>"
	// prefixes. Default: everything EXCEPT captured conversation content
	// (straza.audit.prompt/reply); forwarding transcripts off-box is an
	// explicit opt-in, e.g. subjects: ["straza.>"].
	Subjects []string `yaml:"subjects"`
	// Batch > 1 opts this sink into batched delivery: up to N
	// CloudEvents per POST as NDJSON (webhook; signature covers the whole
	// body, X-Straza-Batch carries the count) or per fsync (file). The
	// default 0 keeps the documented one-event-per-delivery contract:
	// existing receivers parse a single CloudEvent per request and MUST NOT
	// be flipped silently. Negative fails boot.
	Batch int `yaml:"batch"`
}

// Sink types.
const (
	SinkWebhook = "webhook"
	SinkFile    = "file"
)

// validateSinks holds the sinks section's checks.
func validateSinks(sinks []Sink) error {
	seenSinks := map[string]bool{}
	for i, s := range sinks {
		if s.Name == "" {
			return fmt.Errorf("sinks[%d]: name is required (it becomes the durable consumer name)", i)
		}
		if seenSinks[s.Name] {
			return fmt.Errorf("sinks[%d]: duplicate name %q", i, s.Name)
		}
		seenSinks[s.Name] = true
		if s.Batch < 0 {
			return fmt.Errorf("sink %q: batch must be zero (per-event delivery) or a positive count", s.Name)
		}
		switch s.Type {
		case SinkWebhook:
			if s.URL == "" {
				return fmt.Errorf("sink %q: type webhook requires url", s.Name)
			}
			// Paths and query strings pass (HEC routes, token-in-query
			// receivers); credentials belong in headers:, never the URL.
			if err := checkEndpointURL(fmt.Sprintf("sink %q: url", s.Name), s.URL, true, "; put receiver auth in headers:"); err != nil {
				return err
			}
			if s.Secret != "" && s.SecretFile != "" {
				return fmt.Errorf("sink %q: set secret or secretFile, not both", s.Name)
			}
		case SinkFile:
			if s.Path == "" {
				return fmt.Errorf("sink %q: type file requires path", s.Name)
			}
		default:
			return fmt.Errorf("sink %q: unknown type %q: expected %q or %q", s.Name, s.Type, SinkWebhook, SinkFile)
		}
	}
	return nil
}
