package events

import (
	"encoding/json"
	"fmt"
	"regexp"
	"time"
)

// typeRe is the subject taxonomy (spec/events v1beta1). The NATS subject a
// message is published on equals its CE type.
var typeRe = regexp.MustCompile(`^straza\.(audit\.(tool|mcp|admin|authn|identity|prompt|reply|sentinel|approval)|policy\.updated|revocation\.(user|sessions|session|device|lift)|apps\.(deployed|removed|drift|updated)|identity\.(created|updated|deactivated))$`)

// ValidType reports whether a CE type/subject is in the Straza taxonomy.
func ValidType(t string) bool { return typeRe.MatchString(t) }

// Validate checks one CloudEvent JSON document against the Straza envelope
// profile (spec/events v1beta1). The validator MUST agree with
// events.schema.json on the example corpus, enforced by
// TestEnvelopeAgreesWithSpecExamples.
func Validate(raw []byte) error {
	var ce struct {
		SpecVersion string          `json:"specversion"`
		ID          string          `json:"id"`
		Type        string          `json:"type"`
		Source      string          `json:"source"`
		Time        string          `json:"time"`
		Data        json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(raw, &ce); err != nil {
		return fmt.Errorf("events: parse: %w", err)
	}
	if ce.SpecVersion != "1.0" {
		return fmt.Errorf("events: specversion must be \"1.0\", got %q", ce.SpecVersion)
	}
	if ce.ID == "" {
		return fmt.Errorf("events: id is required (the at-least-once dedupe key)")
	}
	if !ValidType(ce.Type) {
		return fmt.Errorf("events: type %q is not in the straza.* subject taxonomy", ce.Type)
	}
	if ce.Source == "" {
		return fmt.Errorf("events: source is required")
	}
	if _, err := time.Parse(time.RFC3339, ce.Time); err != nil {
		return fmt.Errorf("events: time must be RFC 3339: %w", err)
	}
	var data map[string]any
	if len(ce.Data) == 0 || json.Unmarshal(ce.Data, &data) != nil {
		return fmt.Errorf("events: data must be a JSON object")
	}
	return nil
}
