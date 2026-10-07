package snapshot

import (
	"bytes"
	"context"
	"log/slog"
	"strconv"
	"strings"
	"testing"

	"github.com/strazahq/straza/internal/policy"
)

// hasAttr reports whether a slog text capture carries key=value, quoted or
// not (the text handler quotes values with spaces or special characters).
func hasAttr(got, key, value string) bool {
	return strings.Contains(got, key+"="+value) || strings.Contains(got, key+"="+strconv.Quote(value))
}

// TestSwapLogsTieredByVolume pins the swap transition lines: the
// first install at boot is Debug, a real id change is Info carrying the
// previous id and the source, a reload of the same id is Debug again, and a
// nil logger stays silent without changing behaviour.
func TestSwapLogsTieredByVolume(t *testing.T) {
	ctx := context.Background()
	st := testStore(t)
	var buf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	svc := New(st, testSigner(t, st), nil, policy.EffectAllow, 300, log)
	if err := svc.Load(ctx); err != nil {
		t.Fatalf("Load: %v", err)
	}
	first := svc.Current().ID
	got := buf.String()
	if !strings.Contains(got, `level=DEBUG msg="policy snapshot loaded"`) || strings.Contains(got, "level=INFO") {
		t.Fatalf("boot install must be one Debug line, no Info:\n%s", got)
	}
	// An empty store has no active snapshot, so Load compiles one: the boot
	// install's source is "recompile" here ("load" when a persisted snapshot
	// exists, pinned by the same-id reload below).
	if !hasAttr(got, "source", "recompile") || !strings.Contains(got, "component=snapshot") {
		t.Fatalf("boot install record lacks source/component:\n%s", got)
	}

	buf.Reset()
	createSet(t, st, "block-rm", "active", rmPolicy)
	id, err := svc.Recompile(ctx)
	if err != nil {
		t.Fatalf("Recompile: %v", err)
	}
	got = buf.String()
	if !strings.Contains(got, `level=INFO msg="policy snapshot swapped"`) {
		t.Fatalf("id change must be Info:\n%s", got)
	}
	if !hasAttr(got, "id", id) || !hasAttr(got, "previous", first) || !hasAttr(got, "source", "recompile") || !strings.Contains(got, "component=snapshot") {
		t.Fatalf("swap record lacks id/previous/source/component:\n%s", got)
	}

	buf.Reset()
	if err := svc.Load(ctx); err != nil {
		t.Fatalf("Load again: %v", err)
	}
	got = buf.String()
	if !strings.Contains(got, `level=DEBUG msg="policy snapshot unchanged"`) || strings.Contains(got, "level=INFO") {
		t.Fatalf("same-id reload must be Debug only:\n%s", got)
	}
	if !hasAttr(got, "source", "load") {
		t.Fatalf("persisted reload must name source=load:\n%s", got)
	}

	quiet := New(st, testSigner(t, st), nil, policy.EffectAllow, 300, nil)
	if err := quiet.Load(ctx); err != nil {
		t.Fatalf("Load with nil logger: %v", err)
	}
	if quiet.Current().ID != id {
		t.Fatalf("nil logger changed behaviour: current = %s, want %s", quiet.Current().ID, id)
	}
}
