package approval

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"testing"

	"github.com/strazahq/straza/internal/policy"
)

// Intake sanitation: a raw NUL in
// any client/model-typed display string errors on postgres (SQLSTATE 22021)
// while sqlite stores it silently, and unneutralized text reaches approver
// surfaces verbatim. Request must neutralize summary, justification, and
// preview at intake; sqlite's byte fidelity makes the stored row assertable
// on every box (the escaped form is plain ASCII on both dialects).

func TestRequestNeutralizesHostileIntakeText(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	bidi := string(rune(0x202E)) // right-to-left override
	in := req("s-nul", "u-nul", "tox", policy.ApproveSpec{Roles: []string{"sec-approvers"}})
	in.Lane = "gateway"
	in.Summary = "mcp.call demo:e\x00cho" + bidi
	in.Justification = "edge\x00case"
	in.ArgsPreview = "{\n  \"m\": \"a\x00b\"\n}"

	rec, err := h.svc.Request(ctx, in)
	if err != nil {
		t.Fatalf("Request: %v", err)
	}

	got, err := h.st.Approvals().GetByID(ctx, rec.ID)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if want := "mcp.call demo:e\\u0000cho\\u202E"; got.Summary != want {
		t.Errorf("stored summary = %q, want %q", got.Summary, want)
	}
	if want := "edge\\u0000case"; got.Justification != want {
		t.Errorf("stored justification = %q, want %q", got.Justification, want)
	}
	if !strings.Contains(got.ArgsPreview, "a\\u0000b") {
		t.Errorf("stored preview = %q, want NUL escaped inside", got.ArgsPreview)
	}
	all := got.Summary + got.Justification + got.ArgsPreview
	if strings.ContainsRune(all, 0x00) || strings.ContainsRune(all, 0x202E) {
		t.Errorf("raw hostile runes survived into the store: %q", all)
	}
	// The returned record must match the stored (sanitized) truth, not the
	// caller's raw input.
	if rec.Summary != got.Summary || rec.Justification != got.Justification {
		t.Errorf("returned record differs from stored: summary %q vs %q, justification %q vs %q",
			rec.Summary, got.Summary, rec.Justification, got.Justification)
	}
}

// TestRequestCapsLongIntakeText pins the intake cap on the summary and the
// justification: past displayMax bytes each is stored cut on a rune
// boundary, with its full length after it, and the one request record of
// the ticket carries the same cut text, so a long reason cannot make the
// record large.
func TestRequestCapsLongIntakeText(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	in := req("s-long", "u-long", "tox", policy.ApproveSpec{Roles: []string{"sec-approvers"}})
	in.Lane = "gateway"
	in.Summary = "other: " + strings.Repeat("<", 200<<10)
	// The é takes bytes 4095 and 4096, so the cut falls before it.
	in.Justification = strings.Repeat("a", 4095) + "é" + strings.Repeat("<", 200<<10)
	wantSummary := in.Summary[:4096] + fmt.Sprintf("... (%d bytes)", len(in.Summary))
	wantJustification := strings.Repeat("a", 4095) + fmt.Sprintf("... (%d bytes)", len(in.Justification))

	rec, err := h.svc.Request(ctx, in)
	if err != nil {
		t.Fatalf("Request: %v", err)
	}
	got, err := h.st.Approvals().GetByID(ctx, rec.ID)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if got.Summary != wantSummary || rec.Summary != wantSummary {
		t.Errorf("summary is %d bytes stored and %d returned, want the %d-byte cut", len(got.Summary), len(rec.Summary), len(wantSummary))
	}
	if got.Justification != wantJustification || rec.Justification != wantJustification {
		t.Errorf("justification is %d bytes stored and %d returned, want the %d-byte cut", len(got.Justification), len(rec.Justification), len(wantJustification))
	}

	evs, err := h.st.Outbox().ListUnpublished(ctx, 100)
	if err != nil {
		t.Fatalf("Outbox: %v", err)
	}
	var records []string
	for _, e := range evs {
		if e.Subject == auditSubject && strings.Contains(e.CE, `"phase":"request"`) && strings.Contains(e.CE, rec.ID) {
			records = append(records, e.CE)
		}
	}
	if len(records) != 1 {
		t.Fatalf("%d request records of the ticket, want exactly 1", len(records))
	}
	var ce struct {
		Data map[string]any `json:"data"`
	}
	if err := json.Unmarshal([]byte(records[0]), &ce); err != nil {
		t.Fatal(err)
	}
	if ce.Data["summary"] != wantSummary || ce.Data["justification"] != wantJustification {
		t.Error("the request record does not carry the cut summary and justification")
	}
	if len(records[0]) >= 64<<10 {
		t.Errorf("the request record is %d bytes, want under 64 KiB", len(records[0]))
	}
}

// A store error on any approval-service lookup or insert denies fail-closed at
// the PEP with a generic client string. The service does NOT log it: the PEP
// writes the ONE server-side record (correlation id, lane, rule) and this
// layer names the failing op inside the returned error, so that record's
// cause is specific. Logging at both layers would record every fail-closed
// deny twice.
func TestStoreErrorsWrapTheOpAndStaySilent(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	var buf bytes.Buffer
	h.svc.log = slog.New(slog.NewTextHandler(&buf, nil))
	_ = h.st.Close() // every store call now errors

	steps := []struct {
		name, op string
		call     func() error
	}{
		{"request", "approval request-dedupe: ", func() error {
			_, err := h.svc.Request(ctx, req("s-log", "u-log", "tox", policy.ApproveSpec{}))
			return err
		}},
		{"consume-grant", "approval grant-find: ", func() error {
			_, _, err := h.svc.ConsumeGrant(ctx, "u-log", "sha256:k1", "s-log")
			return err
		}},
		{"denied-ticket", "approval denied-ticket-find: ", func() error {
			_, _, err := h.svc.DeniedTicketWithinWindow(ctx, "u-log", "r-1", "sha256:k1")
			return err
		}},
	}
	for _, s := range steps {
		t.Run(s.name, func(t *testing.T) {
			buf.Reset()
			err := s.call()
			if err == nil {
				t.Fatal("want an error from the closed store")
			}
			if !strings.HasPrefix(err.Error(), s.op) {
				t.Errorf("error = %q, want the op prefix %q", err.Error(), s.op)
			}
			if out := buf.String(); out != "" {
				t.Errorf("service logged %q; the PEP owns the one record", out)
			}
		})
	}
}
