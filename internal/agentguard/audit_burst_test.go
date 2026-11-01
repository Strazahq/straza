package agentguard_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/strazahq/straza/internal/agentguard"
)

// TestAuditBatchRetriesBackpressure pins which audit-batch refusals the
// client retries in place. A 429 or 503 is the server asking for a retry and
// the spool has no other trigger to unpark the batch, so the upload waits
// and re-sends; anything else is the server rejecting the batch and comes
// straight back, leaving the records spooled for the next trigger.
func TestAuditBatchRetriesBackpressure(t *testing.T) {
	lanes := []struct {
		name     string
		statuses []int
		wantReqs int
		wantErr  bool
	}{
		{"ingest limit clears", []int{429, 200}, 2, false},
		{"audit store recovers", []int{503, 200}, 2, false},
		{"session refused", []int{401}, 1, true},
		{"batch rejected", []int{400}, 1, true},
		{"refused throughout", []int{429, 429, 429, 429, 429, 429, 429}, 6, true},
	}
	for _, lane := range lanes {
		t.Run(lane.name, func(t *testing.T) {
			var reqs atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				i := int(reqs.Add(1)) - 1
				if i >= len(lane.statuses) {
					t.Errorf("request %d past the scripted answers", i+1)
					i = len(lane.statuses) - 1
				}
				status := lane.statuses[i]
				w.WriteHeader(status)
				if status == http.StatusOK {
					_, _ = w.Write([]byte(`{"accepted":1}`))
				} else {
					_, _ = w.Write([]byte(`{"error":"refused"}`))
				}
			}))
			defer srv.Close()

			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			accepted, err := agentguard.NewClient(srv.URL).AuditBatch(ctx, "tok",
				[]json.RawMessage{json.RawMessage(`{"id":"e1"}`)})
			if (err != nil) != lane.wantErr {
				t.Fatalf("AuditBatch error = %v, want error %v", err, lane.wantErr)
			}
			if got := int(reqs.Load()); got != lane.wantReqs {
				t.Errorf("requests = %d, want %d", got, lane.wantReqs)
			}
			if !lane.wantErr && accepted != 1 {
				t.Errorf("accepted = %d, want 1", accepted)
			}
		})
	}
}
