package server

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/strazahq/straza/internal/config"
)

// sinkCounter reads one straza_sink_* counter for a sink off the app's
// private registry (no testutil dependency).
func sinkCounter(t *testing.T, app *App, name, sink string) float64 {
	t.Helper()
	fams, err := app.metrics.Registry().Gather()
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range fams {
		if f.GetName() != name {
			continue
		}
		for _, m := range f.GetMetric() {
			for _, l := range m.GetLabel() {
				if l.GetName() == "sink" && l.GetValue() == sink {
					return m.GetCounter().GetValue()
				}
			}
		}
	}
	return 0
}

// TestSinksAdminSurface pins the operator door onto the dead-letter lane:
// GET /v1/admin/sinks lists every configured sink with its backlog, tallies
// and parked count (the receiver refused the event deterministically, so it
// parked instead of wedging the lane), POST .../replay re-delivers once the
// receiver is healthy again, the counters move, and an unknown sink is a 404.
func TestSinksAdminSurface(t *testing.T) {
	t.Parallel()
	var refuse atomic.Bool
	refuse.Store(true)
	var mu sync.Mutex
	var bodies []string
	hook := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if refuse.Load() {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		mu.Lock()
		bodies = append(bodies, string(body))
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	defer hook.Close()

	// The capture logger rides a.log so the replay's Info record can be
	// read back; subsystems built before the swap
	// keep their own logger, the per-request logger derives from a.log.
	log, logbuf := captureLogger()
	app, base := testAppPreRun(t, []func(*App){func(a *App) { a.log = log }}, func(c *config.Config) {
		c.Sinks = []config.Sink{{Name: "es", Type: config.SinkWebhook, URL: hook.URL + "/straza-events/_doc?pipeline=straza-id",
			Subjects: []string{"straza.audit.tool"}}}
	})
	user := seedIdentity(t, app)
	grantAdmin(t, app, user.ID)
	tok, _ := checkinToken(t, app, base)
	ctx := context.Background()

	if err := app.bus.Publish(ctx, "straza.audit.tool", []byte(`{"id":"sink-test-1","type":"straza.audit.tool"}`)); err != nil {
		t.Fatal(err)
	}

	type streamRow struct {
		Stream     string `json:"stream"`
		Pending    uint64 `json:"pending"`
		Inflight   uint64 `json:"inflight"`
		Delivered  uint64 `json:"delivered"`
		Duplicates uint64 `json:"duplicates"`
		Parked     uint64 `json:"parked"`
		LastError  string `json:"last_error"`
	}
	type sinkRow struct {
		Name     string      `json:"name"`
		Type     string      `json:"type"`
		Target   string      `json:"target"`
		Batch    int         `json:"batch"`
		Subjects []string    `json:"subjects"`
		Parked   uint64      `json:"parked"`
		Streams  []streamRow `json:"streams"`
	}
	var list []sinkRow
	deadline := time.Now().Add(20 * time.Second)
	for {
		list = nil
		if code := adminReq(t, "GET", base+"/v1/admin/sinks", tok, nil, &list); code != http.StatusOK {
			t.Fatalf("GET sinks = %d", code)
		}
		// The runner parks the event before it counts it on the stream row,
		// so the loop waits for both counts in one answer.
		if len(list) == 1 && list[0].Parked == 1 && len(list[0].Streams) == 1 && list[0].Streams[0].Parked == 1 {
			break
		}
		if time.Now().After(deadline) {
			which := "the sink's parked count"
			if len(list) == 1 && list[0].Parked == 1 {
				which = "the stream row's parked count"
			}
			t.Fatalf("%s never reached one for the refused event: %+v", which, list)
		}
		time.Sleep(200 * time.Millisecond)
	}
	row := list[0]
	if row.Name != "es" || row.Type != "webhook" || row.Batch != 0 || len(row.Subjects) != 1 || row.Subjects[0] != "straza.audit.tool" {
		t.Fatalf("row = %+v", row)
	}
	// The target is the boot-announce redaction: host and path, query masked.
	if !strings.HasPrefix(row.Target, hook.URL+"/straza-events/_doc") || strings.Contains(row.Target, "pipeline=straza-id") {
		t.Fatalf("target = %q, want redacted query", row.Target)
	}
	if len(row.Streams) != 1 || row.Streams[0].Stream != "STRAZA_AUDIT" || row.Streams[0].Parked != 1 ||
		!strings.Contains(row.Streams[0].LastError, "HTTP 400") {
		t.Fatalf("streams = %+v", row.Streams)
	}
	if got := sinkCounter(t, app, "straza_sink_deadletter_total", "es"); got != 1 {
		t.Fatalf("straza_sink_deadletter_total{es} = %v, want 1", got)
	}

	if code := adminReq(t, "POST", base+"/v1/admin/sinks/ghost/replay", tok, nil, nil); code != http.StatusNotFound {
		t.Fatalf("replay unknown sink = %d, want 404", code)
	}

	refuse.Store(false)
	var res struct {
		Sink      string `json:"sink"`
		Replayed  int    `json:"replayed"`
		Remaining int    `json:"remaining"`
		Stopped   string `json:"stopped"`
	}
	if code := adminReq(t, "POST", base+"/v1/admin/sinks/es/replay", tok, map[string]any{"limit": 10}, &res); code != http.StatusOK {
		t.Fatalf("replay = %d", code)
	}
	if res.Sink != "es" || res.Replayed != 1 || res.Remaining != 0 || res.Stopped != "" {
		t.Fatalf("replay result = %+v", res)
	}
	// The transition line: exactly one Info "sink replay" with the
	// tallies and the request's correlation id.
	var replayRecs []string
	for _, line := range strings.Split(logbuf.String(), "\n") {
		if strings.Contains(line, `msg="sink replay"`) {
			replayRecs = append(replayRecs, line)
		}
	}
	if len(replayRecs) != 1 {
		t.Fatalf("sink replay records = %d, want 1:\n%s", len(replayRecs), logbuf.String())
	}
	for _, want := range []string{"level=INFO", "component=sink", "sink=es", "replayed=1", "remaining=0", "correlation_id="} {
		if !strings.Contains(replayRecs[0], want) {
			t.Fatalf("sink replay record lacks %s: %s", want, replayRecs[0])
		}
	}
	mu.Lock()
	got := strings.Join(bodies, " ")
	mu.Unlock()
	if !strings.Contains(got, "sink-test-1") {
		t.Fatalf("receiver did not get the replayed event: %q", got)
	}
	if got := sinkCounter(t, app, "straza_sink_replayed_total", "es"); got != 1 {
		t.Fatalf("straza_sink_replayed_total{es} = %v, want 1", got)
	}
	list = nil
	if code := adminReq(t, "GET", base+"/v1/admin/sinks", tok, nil, &list); code != http.StatusOK || len(list) != 1 || list[0].Parked != 0 {
		t.Fatalf("after replay: code %d list %+v", code, list)
	}

	// The replay is an admin action: attributed in the audit admin lane.
	deadline = time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		var audit []map[string]any
		adminReq(t, "GET", base+"/v1/admin/audit?q=sink.replay", tok, nil, &audit)
		for _, rec := range audit {
			if raw, _ := json.Marshal(rec); strings.Contains(string(raw), "sink.replay") {
				return
			}
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatal("no straza.audit.admin sink.replay record landed")
}
