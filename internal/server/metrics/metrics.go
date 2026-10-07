// Package metrics holds strazad's Prometheus series (decision latency, gateway
// throttling, sink lanes, disk gauges) on a private registry served at /metrics.
package metrics

import (
	"net/http"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// Set exports the decision-plane telemetry, such as the decision latency
// histograms. Registered on a private registry so tests can
// run in isolation and multiple apps can coexist in one process.
type Set struct {
	registry        *prometheus.Registry
	latency         *prometheus.HistogramVec
	decisions       *prometheus.CounterVec
	throttled       *prometheus.CounterVec
	catalogOver     prometheus.Counter
	unroutable      prometheus.Counter
	auditRefused    prometheus.Counter
	dropped         prometheus.CounterFunc
	lost            prometheus.CounterFunc
	transcriptBytes prometheus.Gauge
	dataDiskFree    prometheus.Gauge
	sinkParked      *prometheus.CounterVec
	sinkDuplicates  *prometheus.CounterVec
	sinkReplayed    *prometheus.CounterVec
	httpErrors      *prometheus.CounterVec
	failClosed      *prometheus.CounterVec
	httpRequests    *prometheus.CounterVec
	httpLatency     *prometheus.HistogramVec
}

// New builds the set on a private registry; auditDropped and auditLost are
// read on scrape.
func New(auditDropped, auditLost func() float64) *Set {
	reg := prometheus.NewRegistry()
	m := &Set{
		registry: reg,
		latency: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name: "straza_pdp_decision_seconds",
			Help: "PDP decision latency (server /v1/decide).",
			// Buckets tuned to the decision latency budget (p99 < 100µs
			// in-proc; the HTTP path here adds token verify + JSON, so wider
			// tail).
			Buckets: []float64{20e-6, 50e-6, 100e-6, 250e-6, 500e-6, 1e-3, 5e-3, 25e-3, 100e-3},
		}, []string{"effect"}),
		decisions: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "straza_pdp_decisions_total",
			Help: "PDP decisions by effect. On /v1/decide the effect is the answer the client got, so a decision that the full audit queue refused under block counts as a deny. On /mcp it is the policy's verdict, before the approval, credential and audit queue checks that can still refuse the call.",
		}, []string{"effect"}),
		throttled: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "straza_gateway_throttled_total",
			Help: "Gateway tools/call requests rejected by the per-(session,app) rate limit. Rejections are deliberately not audited (a hot loop must not amplify audit work), so this counter is where the abuse volume shows up.",
		}, []string{"app"}),
		catalogOver: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "straza_gateway_catalog_oversize_total",
			Help: "Tier-1 catalog builds whose tool count exceeded apps.catalog.warnSize. A plain counter (no per-role label) to keep cardinality bounded; the accompanying warn log carries the role key.",
		}),
		unroutable: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "straza_approvals_unroutable_total",
			Help: "Approval requests denied at request time because their deciders resolved to nobody (no roles, no usable sponsor). Each one is a routing misconfiguration: the deny reason and the strazad warn log carry the cause, this counter is the alert lane.",
		}),
		auditRefused: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "straza_audit_refused_total",
			Help: "Client-spooled audit records that POST /v1/audit/batch refused because the session they name is not a UUID, is unknown, belongs to another user or another device, or is past the cap of 1000 named sessions per batch. The client deletes them once the upload is answered, so the strazad warn log line is their only trace and this counter is the alert lane.",
		}),
		dropped: prometheus.NewCounterFunc(prometheus.CounterOpts{
			Name: "straza_audit_dropped_total",
			Help: "Audit events dropped when the spool was full (drop-with-counter mode).",
		}, auditDropped),
		lost: prometheus.NewCounterFunc(prometheus.CounterOpts{
			Name: "straza_audit_lost_total",
			Help: "Audit events the spool took in and could not confirm as written to the database: every retry failed, an attempt ended without an answer, or strazad stopped first. Each one leaves one Error log record: its own line with its type and id, or a place in the count of the one line a stop logs. The counter starts at zero in each strazad process, so the log is the only trace of a loss at stop.",
		}, auditLost),
		transcriptBytes: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "straza_transcript_store_bytes",
			Help: "Bytes the transcript recording occupies, measured on the hourly retention-janitor cadence. Dialect-honest, not dialect-identical: Postgres = pg_total_relation_size(conversation_turns); SQLite = the whole database file (page math). Pair with governance.transcriptBytesWatermark: the janitor WARNs while this sits above it.",
		}),
		dataDiskFree: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "straza_data_disk_free_bytes",
			Help: "Free bytes on the filesystem holding server.dataDir, same janitor cadence. -1 = unknown (non-unix build, empty dataDir, or statfs failure); on enterprise the database disk is Postgres's, so watch that side too.",
		}),
		sinkParked: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "straza_sink_deadletter_total",
			Help: "Sink deliveries parked in the dead-letter lane (the receiver refused the event deterministically, or a transient failure outlived its attempt budget). Parked events are preserved and replayable (strazactl sinks replay); a rising counter is an alert, the strazad warn log carries the reason.",
		}, []string{"sink"}),
		sinkDuplicates: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "straza_sink_duplicates_total",
			Help: "Sink deliveries the receiver answered 409 to (already holds the event; acked as delivered under the by-id dedupe contract). Normal after a redelivery; a steady stream means the receiver is not deduping the way its pipeline claims.",
		}, []string{"sink"}),
		sinkReplayed: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "straza_sink_replayed_total",
			Help: "Parked sink events re-delivered by an operator replay and removed from the dead-letter lane.",
		}, []string{"sink"}),
		httpErrors: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "straza_http_errors_total",
			Help: "Server-side failures answered at the HTTP edge: 5xx answers, recovered panics and internal-class JSON-RPC errors on /mcp, by matched route pattern and status (an HTTP status, or rpc<code> for JSON-RPC errors that ride HTTP 200). Each increment has exactly one Error log record carrying the same correlation_id; 4xx answers are client mistakes and are not counted here.",
		}, []string{"route", "status"}),
		failClosed: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "straza_failclosed_total",
			Help: "Internal failures the PEP answered as a fail-closed deny or a tool-level error (approval service, fingerprint, classifier, or an audit record that the full audit queue refused under block), by lane (hook = /v1/decide, gateway = /mcp). Each increment has exactly one Error log record carrying the correlation_id of the request; policy denies, pending/expired/denied approvals and unroutable approvals are decisions and never count here.",
		}, []string{"lane"}),
		httpRequests: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "straza_http_requests_total",
			Help: "Requests completed at the HTTP edge by matched route pattern and HTTP status, every answer 2xx included, on both listeners. The probes /healthz, /readyz and /metrics are excluded so they cannot dominate the series. straza_http_errors_total stays the 5xx and rpc-error lane with one Error log record each; this is the RED total.",
		}, []string{"route", "status"}),
		httpLatency: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "straza_http_request_seconds",
			Help:    "HTTP edge request duration by matched route pattern, handler entry to completion (SSE and long-poll streams observe when they end). Same probe exclusion as straza_http_requests_total.",
			Buckets: []float64{0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 10},
		}, []string{"route"}),
	}
	m.dataDiskFree.Set(-1)
	reg.MustRegister(m.latency, m.decisions, m.throttled, m.catalogOver, m.unroutable, m.auditRefused,
		m.dropped, m.lost, m.transcriptBytes, m.dataDiskFree, m.sinkParked, m.sinkDuplicates, m.sinkReplayed, m.httpErrors,
		m.failClosed, m.httpRequests, m.httpLatency)
	return m
}

// HTTPRequest records one completed request at the edge: route is
// the matched mux pattern ("-" when none), status the exact HTTP status.
func (m *Set) HTTPRequest(route, status string, d time.Duration) {
	m.httpRequests.WithLabelValues(route, status).Inc()
	m.httpLatency.WithLabelValues(route).Observe(d.Seconds())
}

// FailClosed counts one internal failure the PEP answered as a fail-closed
// deny or tool-level error on the given lane.
func (m *Set) FailClosed(lane string) { m.failClosed.WithLabelValues(lane).Inc() }

// HTTPError counts one server-side failure answered at the edge:
// route is the matched mux pattern, status the HTTP status or "rpc<code>".
func (m *Set) HTTPError(route, status string) { m.httpErrors.WithLabelValues(route, status).Inc() }

// SinkParked implements spine.SinkMetrics.
func (m *Set) SinkParked(sink string) { m.sinkParked.WithLabelValues(sink).Inc() }

// SinkDuplicate implements spine.SinkMetrics.
func (m *Set) SinkDuplicate(sink string) { m.sinkDuplicates.WithLabelValues(sink).Inc() }

// SinkReplayed implements spine.SinkMetrics.
func (m *Set) SinkReplayed(sink string) { m.sinkReplayed.WithLabelValues(sink).Inc() }

// UnroutableInc records one approval request denied unroutable (revision 14).
func (m *Set) UnroutableInc() { m.unroutable.Inc() }

// AuditRefused records n client-spooled audit records the batch route refused.
func (m *Set) AuditRefused(n int) { m.auditRefused.Add(float64(n)) }

// Observe records one PDP decision: latency by effect and the decision count.
func (m *Set) Observe(effect string, d time.Duration) {
	m.latency.WithLabelValues(effect).Observe(d.Seconds())
	m.decisions.WithLabelValues(effect).Inc()
}

// Throttle records one gateway tools/call rejected by the per-(session,app) limit.
func (m *Set) Throttle(app string) { m.throttled.WithLabelValues(app).Inc() }

// Oversize records one tier-1 catalog build that exceeded apps.catalog.warnSize.
func (m *Set) Oversize() { m.catalogOver.Inc() }

// Handler serves the private registry in the Prometheus text format.
func (m *Set) Handler() http.Handler {
	return promhttp.HandlerFor(m.registry, promhttp.HandlerOpts{})
}

// SetTranscriptBytes sets the straza_transcript_store_bytes gauge (retention janitor cadence).
func (m *Set) SetTranscriptBytes(v float64) { m.transcriptBytes.Set(v) }

// SetDataDiskFree sets the straza_data_disk_free_bytes gauge (-1 = unknown).
func (m *Set) SetDataDiskFree(v float64) { m.dataDiskFree.Set(v) }

// Registry exposes the private registry (tests gather from it directly).
func (m *Set) Registry() *prometheus.Registry { return m.registry }
