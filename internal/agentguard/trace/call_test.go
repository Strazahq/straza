package trace

import (
	"context"
	"strings"
	"testing"
)

func TestCallObserveLastWinsAndBounds(t *testing.T) {
	var nilCall *Call
	nilCall.Observe(200, "x") // nil-safe
	if s, id := nilCall.Get(); s != 0 || id != "" {
		t.Fatalf("nil call Get = %d %q", s, id)
	}
	if CallFrom(context.Background()) != nil {
		t.Fatal("CallFrom on a bare context must be nil")
	}
	c := &Call{}
	ctx := WithCall(context.Background(), c)
	if CallFrom(ctx) != c {
		t.Fatal("CallFrom must return the call stored by WithCall")
	}
	if WithCall(context.Background(), nil) == nil {
		t.Fatal("WithCall(nil) must hand the context back")
	}
	CallFrom(ctx).Observe(401, "first")
	CallFrom(ctx).Observe(200, "second")
	if s, id := c.Get(); s != 200 || id != "second" {
		t.Fatalf("last write must win: %d %q", s, id)
	}
	long := strings.Repeat("a", 200)
	c.Observe(503, long)
	if _, id := c.Get(); len(id) != maxRequestID {
		t.Fatalf("request id not bounded: %d bytes", len(id))
	}
}
