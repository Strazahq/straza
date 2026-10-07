package spine

import (
	"context"
	"errors"
	"net/url"
	"strings"
	"testing"
	"time"
)

// A webhook sink's transport failure is logged on every redelivery attempt,
// and an HEC-style receiver keeps its token in the URL query: the delivery
// error must never echo it (the secrets-in-logs rule).
func TestWebhookSinkErrorRedactsURL(t *testing.T) {
	// 192.0.2.0/24 is TEST-NET: connect fails fast without touching a network.
	sink := NewWebhookSink("hec", "https://192.0.2.9/services/collector?token=SECRET-HEC-TOKEN", nil, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	err := sink.Deliver(ctx, "straza.audit.tool", []byte(`{}`))
	if err == nil {
		t.Fatalf("expected a delivery error")
	}
	if s := err.Error(); strings.Contains(s, "SECRET-HEC-TOKEN") {
		t.Fatalf("delivery error leaked the query token: %q", s)
	}
	if s := err.Error(); !strings.Contains(s, "192.0.2.9") {
		t.Fatalf("delivery error lost the target host: %q", s)
	}
	var ue *url.Error
	if !errors.As(err, &ue) {
		t.Fatalf("transport failure no longer unwraps to *url.Error")
	}

	batchErr := sink.DeliverBatch(ctx, []SinkEvent{{CE: []byte(`{}`)}})
	if batchErr == nil {
		t.Fatalf("expected a batch delivery error")
	}
	if s := batchErr.Error(); strings.Contains(s, "SECRET-HEC-TOKEN") {
		t.Fatalf("batch delivery error leaked the query token: %q", s)
	}
}
