package spine

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/strazahq/straza/internal/redact"
	"github.com/strazahq/straza/internal/sameorigin"
)

// Sinks forward the event spine to external systems. Delivery is
// at-least-once from durable, explicitly-acked JetStream consumers: a sink
// outage NAKs and redelivers with backoff, so events are delayed, never lost
// (the audit path itself stays async, and sinks are
// downstream of the durable stream, not of any request). Consumers of a
// sink's output must dedupe by CloudEvent id, as with any at-least-once
// feed; a 409 from them therefore counts as delivered. Deliveries a
// receiver will never accept park in the dead-letter lane (sink_runner.go,
// deadletter.go): preserved, counted, listed, replayable.

// Sink is one delivery target.
type Sink interface {
	Name() string
	Deliver(ctx context.Context, subject string, ce []byte) error
}

// SinkEvent is one CloudEvent in a batched delivery.
type SinkEvent struct {
	Subject string
	CE      []byte
}

// BatchSink is the optional batched face: one call carries several
// CloudEvents. It is strictly OPT-IN per sink (`batch: N` in config). The
// default stays one Deliver per event, which is the documented webhook
// contract existing receivers depend on.
type BatchSink interface {
	Sink
	DeliverBatch(ctx context.Context, events []SinkEvent) error
}

// --- webhook sink ---

// WebhookSink POSTs each CloudEvent to a SIEM-facing endpoint. When a secret
// is set, the body is signed: X-Straza-Signature: sha256=<hex HMAC-SHA256>.
// Optional operator headers ride every delivery and may override the
// Content-Type default; receivers with their own contracts (Elasticsearch
// wants application/json + Basic auth; Splunk HEC wants `Authorization:
// Splunk <token>`) become direct targets without a shipper in between. The
// Straza headers themselves are never overridable.
type WebhookSink struct {
	name    string
	url     string
	secret  []byte
	headers map[string]string
	client  *http.Client
}

// NewWebhookSink builds a webhook sink; secret may be empty (unsigned) and
// headers nil (defaults only).
func NewWebhookSink(name, url string, secret []byte, headers map[string]string) *WebhookSink {
	return &WebhookSink{name: name, url: url, secret: secret, headers: headers,
		client: &http.Client{Timeout: 10 * time.Second, CheckRedirect: webhookRedirect}}
}

// webhookRedirect is the webhook client's redirect policy, sameorigin.CheckMethod:
// a delivery follows a redirect only within the scheme, host and port of the
// configured url, and only while it stays a POST, as a 307 or 308 keeps it.
// Go's default re-sends the event, the signature and the operator's headers
// to any host, keeps Authorization on another port or scheme of the same host
// name, and turns the POST into a GET on a 301, 302 or 303, whose 2xx would
// count an event the receiver never got. The tenth redirect stops the chain.
func webhookRedirect(req *http.Request, via []*http.Request) error {
	var cross *sameorigin.RedirectError
	var method *sameorigin.MethodError
	err := sameorigin.CheckMethod(req, via)
	switch {
	case errors.As(err, &cross):
		return &redirectError{from: cross.From, to: cross.To}
	case errors.As(err, &method):
		return &redirectError{from: method.Origin, status: method.Status}
	case errors.Is(err, sameorigin.ErrTooManyRedirects):
		return &redirectError{from: via[0].URL.Scheme + "://" + via[0].URL.Host, loop: true}
	}
	return err
}

// redirectError is a redirect the webhook client did not follow. from and to
// are scheme://host[:port] only, so no path, query or credential of either
// address reaches the log. status is set when the redirect stays on the
// origin but would turn the POST into a GET, loop when the chain reached ten
// redirects, and badLocation when the Location header did not parse.
type redirectError struct {
	from, to          string
	status            int
	loop, badLocation bool
}

// sinkRedirectNextStep ends every redirect sentence of the sink.
const sinkRedirectNextStep = "Set the sink's url to the final address of the receiver"

func (e *redirectError) Error() string {
	switch {
	case e.loop:
		return e.from + " redirected the delivery 10 times in a row, " +
			"so the sink stopped following it as a redirect loop and the event was not delivered. " + sinkRedirectNextStep
	case e.badLocation:
		return e.from + " answered with a redirect whose Location header is not a valid address, " +
			"so the sink could not follow it and the event was not delivered. " + sinkRedirectNextStep
	case e.status != 0:
		return fmt.Sprintf("%s answered with a %d redirect, which turns the POST of the event into a GET without it, "+
			"so the sink did not follow it. A 2xx answer to that GET would count an event the receiver never got. %s",
			e.from, e.status, sinkRedirectNextStep)
	}
	return e.from + " answered with a redirect to " + e.to + ", which is another scheme, host or port, " +
		"so the sink did not follow it and sent neither the event nor its headers there. " +
		"The event and the sink's credentials must not leave the configured origin. " + sinkRedirectNextStep
}

// deliveryError wraps a failed client.Do for the runner, which logs it on
// every redelivery attempt. A redirect the client did not follow is returned
// as its own sentence, without the *url.Error around it, which names the
// redirect target. Any other *url.Error prints the full sink URL, where an
// HEC-style receiver keeps its token in the query, so it is redacted before
// wrapping.
func (s *WebhookSink) deliveryError(err error) error {
	var refused *redirectError
	if errors.As(err, &refused) {
		return fmt.Errorf("sink %s: %w", s.name, refused)
	}
	if origin, ok := sameorigin.BadLocation(err); ok {
		return fmt.Errorf("sink %s: %w", s.name, &redirectError{from: origin, badLocation: true})
	}
	return fmt.Errorf("sink %s: %w", s.name, redact.SanitizeURLError(err, redact.URL))
}

// Name implements Sink.
func (s *WebhookSink) Name() string { return s.name }

// Deliver implements Sink. A non-2xx response is a typed error the runner
// classifies (sink_policy.go): 409 = already delivered, other 4xx =
// deterministic, 5xx/429 = retryable.
func (s *WebhookSink) Deliver(ctx context.Context, subject string, ce []byte) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.url, bytes.NewReader(ce))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/cloudevents+json")
	for k, v := range s.headers {
		req.Header.Set(k, v)
	}
	// The Straza headers win over operator headers: receivers depend on them
	// for routing (subject), origin (sink), and verification (signature).
	req.Header.Set("X-Straza-Subject", subject)
	req.Header.Set("X-Straza-Sink", s.name)
	if len(s.secret) > 0 {
		mac := hmac.New(sha256.New, s.secret)
		mac.Write(ce)
		req.Header.Set("X-Straza-Signature", "sha256="+hex.EncodeToString(mac.Sum(nil)))
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return s.deliveryError(err)
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4<<10))
	_ = resp.Body.Close()
	return statusResult(s.name, resp.StatusCode)
}

// DeliverBatch implements BatchSink: one POST, NDJSON body (one CloudEvent
// per line: Elastic bulk-adjacent, Splunk HEC raw-compatible), signed over
// the whole body. Per-event X-Straza-Subject is meaningless for a mixed
// batch and is replaced by X-Straza-Batch (the line count); receivers route
// on each event's own `type`.
func (s *WebhookSink) DeliverBatch(ctx context.Context, events []SinkEvent) error {
	var body bytes.Buffer
	for _, e := range events {
		body.Write(e.CE)
		body.WriteByte('\n')
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.url, bytes.NewReader(body.Bytes()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-ndjson")
	for k, v := range s.headers {
		req.Header.Set(k, v)
	}
	req.Header.Set("X-Straza-Sink", s.name)
	req.Header.Set("X-Straza-Batch", fmt.Sprintf("%d", len(events)))
	if len(s.secret) > 0 {
		mac := hmac.New(sha256.New, s.secret)
		mac.Write(body.Bytes())
		req.Header.Set("X-Straza-Signature", "sha256="+hex.EncodeToString(mac.Sum(nil)))
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return s.deliveryError(err)
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4<<10))
	_ = resp.Body.Close()
	return statusResult(s.name, resp.StatusCode)
}

// --- file sink ---

// FileSink appends one CloudEvent JSON per line (SIEM-friendly JSONL; ship
// with Filebeat/Elastic Agent or a Splunk file monitor). Each line is synced
// before the message is acked, so an acked event is on disk. Rotation is the
// operator's: use logrotate `copytruncate` (same inode; O_APPEND writes land
// correctly after truncation). Rename-based rotation would keep this handle
// on the old file until restart.
type FileSink struct {
	name string
	path string

	mu sync.Mutex
	f  *os.File
}

// NewFileSink builds a file sink; the file and its directory are created on
// first delivery.
func NewFileSink(name, path string) *FileSink {
	return &FileSink{name: name, path: path}
}

// Name implements Sink.
func (s *FileSink) Name() string { return s.name }

// Deliver implements Sink.
func (s *FileSink) Deliver(_ context.Context, _ string, ce []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.ensureOpen(); err != nil {
		return err
	}
	line := append(append(make([]byte, 0, len(ce)+1), ce...), '\n')
	if _, err := s.f.Write(line); err != nil {
		// The handle may be stale (rotation, deletion): reopen once and retry.
		_ = s.f.Close()
		s.f = nil
		if err := s.ensureOpen(); err != nil {
			return err
		}
		if _, err := s.f.Write(line); err != nil {
			return fmt.Errorf("sink %s: write: %w", s.name, err)
		}
	}
	if err := s.f.Sync(); err != nil {
		return fmt.Errorf("sink %s: sync: %w", s.name, err)
	}
	return nil
}

// DeliverBatch implements BatchSink: all lines in one write set and ONE
// fsync, because the fsync per event is the file sink's real ceiling.
func (s *FileSink) DeliverBatch(_ context.Context, events []SinkEvent) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.ensureOpen(); err != nil {
		return err
	}
	var buf bytes.Buffer
	for _, e := range events {
		buf.Write(e.CE)
		buf.WriteByte('\n')
	}
	if _, err := s.f.Write(buf.Bytes()); err != nil {
		_ = s.f.Close()
		s.f = nil
		if err := s.ensureOpen(); err != nil {
			return err
		}
		if _, err := s.f.Write(buf.Bytes()); err != nil {
			return fmt.Errorf("sink %s: write: %w", s.name, err)
		}
	}
	if err := s.f.Sync(); err != nil {
		return fmt.Errorf("sink %s: sync: %w", s.name, err)
	}
	return nil
}

func (s *FileSink) ensureOpen() error {
	if s.f != nil {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o750); err != nil {
		return fmt.Errorf("sink %s: %w", s.name, err)
	}
	f, err := os.OpenFile(s.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600) // #nosec G304 -- operator-configured sink path
	if err != nil {
		return fmt.Errorf("sink %s: open: %w", s.name, err)
	}
	s.f = f
	return nil
}

// Close releases the file handle (shutdown). The next Deliver reopens.
func (s *FileSink) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.f == nil {
		return nil
	}
	err := s.f.Close()
	s.f = nil
	return err
}

// EffectiveFilters intersects a sink's configured subject filters with one
// stream's subject space. Filters are exact subjects or ".>" prefixes.
// A filter broader than a stream subject narrows to the stream subject; a
// filter inside it passes through; disjoint filters drop.
func EffectiveFilters(streamSubjects, want []string) []string {
	var out []string
	seen := map[string]bool{}
	add := func(s string) {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	for _, ss := range streamSubjects {
		for _, w := range want {
			switch {
			case subjectCovers(w, ss):
				add(ss)
			case subjectCovers(ss, w):
				add(w)
			}
		}
	}
	return out
}

// subjectCovers reports whether pattern a matches everything pattern b does.
func subjectCovers(a, b string) bool {
	if a == b {
		return true
	}
	prefix, ok := strings.CutSuffix(a, ".>")
	if !ok {
		return false
	}
	return b == prefix || strings.HasPrefix(b, prefix+".")
}
