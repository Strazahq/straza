// Package sinks turns the sinks: config into spine sink runners (SIEM feeds),
// one runner per stream a sink's subject filter spans.
package sinks

import (
	"bytes"
	"fmt"
	"io"
	"log/slog"
	"os"

	"github.com/strazahq/straza/internal/config"
	"github.com/strazahq/straza/internal/events"
	"github.com/strazahq/straza/internal/redact"
	"github.com/strazahq/straza/internal/spine"
)

// defaultSinkSubjects is what a sink with no `subjects:` receives: every
// spine subject EXCEPT straza.audit.prompt/reply; captured conversation
// content leaves the box only by explicit opt-in (spec/events §2 is the
// authoritative audit-type list this enumeration mirrors).
var defaultSinkSubjects = []string{
	"straza.audit.tool", "straza.audit.mcp", "straza.audit.admin",
	"straza.audit.authn", "straza.audit.identity", "straza.audit.approval",
	"straza.audit.sentinel",
	"straza.policy.>", "straza.revocation.>", "straza.apps.>", "straza.identity.>",
}

// Build turns the sinks: config into runners, plus a shutdown
// hook that releases sink resources (file handles). obs receives the
// park/duplicate/replay counts (the server's Prometheus registry; nil ok). A sink whose subject
// filter spans both spine streams gets one runner per stream; JetStream
// durables live on exactly one stream. Config that matches no stream is a
// boot error (fail closed beats a silently idle SIEM feed).
func Build(cfg config.Config, bus *events.Bus, log *slog.Logger, obs spine.SinkMetrics) ([]*spine.SinkRunner, func(), error) {
	var out []*spine.SinkRunner
	var closers []io.Closer
	closeAll := func() {
		for _, c := range closers {
			_ = c.Close()
		}
	}
	streams := events.StreamSubjects()
	for _, sc := range cfg.Sinks {
		var sink spine.Sink
		switch sc.Type {
		case config.SinkWebhook:
			secret := []byte(sc.Secret)
			if sc.SecretFile != "" {
				raw, err := os.ReadFile(sc.SecretFile) // #nosec G304 -- operator-configured secret path
				if err != nil {
					closeAll()
					return nil, nil, fmt.Errorf("sink %q: read secretFile: %w", sc.Name, err)
				}
				secret = bytes.TrimSpace(raw)
			}
			sink = spine.NewWebhookSink(sc.Name, sc.URL, secret, sc.Headers)
		case config.SinkFile:
			fs := spine.NewFileSink(sc.Name, sc.Path)
			closers = append(closers, fs)
			sink = fs
		default:
			// Validate() rejects unknown types; defensive for hand-built configs.
			closeAll()
			return nil, nil, fmt.Errorf("sink %q: unknown type %q", sc.Name, sc.Type)
		}

		want := sc.Subjects
		if len(want) == 0 {
			// Default: everything EXCEPT captured conversation content. A
			// default of straza.> would ship verbatim transcripts off-box the
			// moment an operator added a sink, the unsafe direction for a
			// secrets-bearing surface. Forwarding transcripts is one explicit
			// config line:
			//   subjects: ["straza.>"]  (or name the capture subjects).
			// JetStream filters are inclusive-only, so "everything but" is an
			// enumeration. Keep it in step with spec/events §2, where a NEW audit
			// type must be added here to reach default-configured sinks.
			want = defaultSinkSubjects
			log.Info("sink using default subjects (captured conversation excluded)",
				"name", sc.Name, "excluded", "straza.audit.prompt straza.audit.reply")
		}
		matched := false
		for stream, subjects := range streams {
			filters := spine.EffectiveFilters(subjects, want)
			if len(filters) == 0 {
				continue
			}
			matched = true
			runner := spine.NewSinkRunner(bus, sink, stream, filters, log)
			runner.Batch = sc.Batch
			runner.Observe = obs
			out = append(out, runner)
		}
		if !matched {
			closeAll()
			return nil, nil, fmt.Errorf("sink %q: subjects %v overlap no spine stream", sc.Name, want)
		}
		// Announce the delivery target (announce+verify: Validate checked its
		// shape at boot): a SIEM feed pointed at the wrong host should be
		// one boot-log grep away, not a silent no-show three weeks later.
		// Redacted: the URL check deliberately ALLOWS a query string because
		// HEC-style receivers carry their token there, so the announce
		// prints scheme://host/path with the query masked, never the token.
		target := redact.URL(sc.URL)
		if sc.Type == config.SinkFile {
			target = sc.Path
		}
		log.Info("sink configured", "name", sc.Name, "type", sc.Type, "target", target,
			"batch", sc.Batch, "subjects", want)
	}
	return out, closeAll, nil
}
