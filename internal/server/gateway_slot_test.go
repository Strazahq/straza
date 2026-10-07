package server

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// TestPersonReason pins the slot's sentence for a person: the "Straza: "
// prefix dropped, the first letter upper-cased, and a long refusal cut to
// 500 characters, counted in characters and not bytes.
func TestPersonReason(t *testing.T) {
	t.Parallel()
	long := "Straza: " + strings.Repeat("é", 700)
	rows := []struct {
		name, in, want string
	}{
		{"prefix dropped", "Straza: approval denied by Kim (ref a1)", "Approval denied by Kim (ref a1)"},
		{"prefix with leading space", "  Straza: env reads are blocked", "Env reads are blocked"},
		{"no prefix", "rate limit exceeded", "Rate limit exceeded"},
		{"empty", "", ""},
		{"long cut with an ellipsis", long, "É" + strings.Repeat("é", slotReasonMax-2) + "…"},
	}
	for _, row := range rows {
		t.Run(row.name, func(t *testing.T) {
			got := personReason(row.in)
			if got != row.want {
				t.Errorf("personReason(%q) = %q, want %q", row.in, got, row.want)
			}
			if n := utf8.RuneCountInString(got); n > slotReasonMax {
				t.Errorf("reason has %d characters, want at most %d", n, slotReasonMax)
			}
		})
	}
}

// TestDecisionSlotWire pins the slot's JSON: v 1, the decision, audited,
// the reason, ref and expiresAt only when set, source Straza, and no other
// field.
func TestDecisionSlotWire(t *testing.T) {
	t.Parallel()
	exp := time.Date(2026, 10, 1, 12, 0, 0, 0, time.FixedZone("x", 3600))
	rows := []struct {
		name string
		slot decisionSlot
		want string
	}{
		{"held", heldSlot("apr-1", exp, true),
			`{"v":1,"decision":"held","audited":true,"reason":"This action waits for a person's approval. Once it is approved, do it again.","ref":"apr-1","expiresAt":"2026-10-01T11:00:00Z","source":"Straza"}`},
		{"denied with a ref", deniedSlot("Straza: approval denied by Kim (ref apr-2)", true, "apr-2"),
			`{"v":1,"decision":"denied","audited":true,"reason":"Approval denied by Kim (ref apr-2)","ref":"apr-2","source":"Straza"}`},
		{"denied, not audited", deniedSlot(auditQueueFullMsg, false, ""),
			`{"v":1,"decision":"denied","audited":false,"reason":"` + personReason(auditQueueFullMsg) + `","source":"Straza"}`},
	}
	for _, row := range rows {
		t.Run(row.name, func(t *testing.T) {
			b, err := json.Marshal(row.slot)
			if err != nil {
				t.Fatal(err)
			}
			if string(b) != row.want {
				t.Errorf("slot = %s\nwant   %s", b, row.want)
			}
		})
	}
}

// TestApproveOutcomeSlot pins which slot a refused approval outcome gets:
// held while the approval is undecided, with its ref and window end, and
// denied otherwise, with the ref when one is known.
func TestApproveOutcomeSlot(t *testing.T) {
	t.Parallel()
	exp := time.Now().Add(time.Minute)
	rows := []struct {
		name         string
		o            approveOutcome
		wantDecision string
		wantRef      string
		wantExpires  bool
	}{
		{"pending hold", approveOutcome{reason: "Straza: approval pending (ref r1)", ref: "r1", pending: true, expiresAt: exp}, "held", "r1", true},
		{"denied approval", approveOutcome{reason: "Straza: approval denied by Kim (ref r2)", ref: "r2"}, "denied", "r2", false},
		{"store down", approveOutcome{reason: approvalStoreDownReason}, "denied", "", false},
	}
	for _, row := range rows {
		t.Run(row.name, func(t *testing.T) {
			s := row.o.slot(true)
			if s.Decision != row.wantDecision || s.Ref != row.wantRef || (s.ExpiresAt != "") != row.wantExpires || !s.Audited {
				t.Errorf("slot = %+v, want decision %s, ref %q, expiresAt set %v", s, row.wantDecision, row.wantRef, row.wantExpires)
			}
			if row.wantDecision == "held" && s.Reason != heldSlotReason {
				t.Errorf("held reason = %q, want %q", s.Reason, heldSlotReason)
			}
			if row.wantDecision == "denied" && s.Reason != personReason(row.o.reason) {
				t.Errorf("denied reason = %q, want the refusal for a person", s.Reason)
			}
		})
	}
}

// TestToolRefusalSlotScope pins where the slot rides: never on /mcp, never
// on a server endpoint whose views are off, and only on one whose views are
// on. The text the model reads is the same on all three.
func TestToolRefusalSlotScope(t *testing.T) {
	t.Parallel()
	const text = "Straza: env reads are blocked"
	rows := []struct {
		name     string
		srv      *serverScope
		wantSlot bool
	}{
		{"combined endpoint", nil, false},
		{"server endpoint, views off", &serverScope{name: "a", cat: &serverCatalog{}}, false},
		{"server endpoint, views on", &serverScope{name: "a", cat: &serverCatalog{viewsOn: true}}, true},
	}
	for _, row := range rows {
		t.Run(row.name, func(t *testing.T) {
			res := toolRefusal(text, row.srv, deniedSlot(text, true, ""))
			if !res.IsError || toolErrText(res) != text {
				t.Fatalf("result = %+v, want an error carrying %q", res, text)
			}
			b, _ := json.Marshal(res)
			if has := strings.Contains(string(b), `"_meta"`); has != row.wantSlot {
				t.Errorf("result %s carries _meta = %v, want %v", b, has, row.wantSlot)
			}
			if row.wantSlot && !strings.Contains(string(b), `"intermediary/decision":{"v":1,"decision":"denied"`) {
				t.Errorf("result %s lacks the denied slot", b)
			}
		})
	}
}

// TestWithoutUpstreamSlotCopies pins that dropping an upstream's own slot
// works on a copy: the upstream's result and its Meta map keep the key, and
// a result without the key is served as it is.
func TestWithoutUpstreamSlotCopies(t *testing.T) {
	t.Parallel()
	meta := mcp.Meta{decisionSlotKey: forgedSlot, "x/kept": "yes"}
	res := &mcp.CallToolResult{Meta: meta}
	got := withoutUpstreamSlot(res)
	if _, ok := got.Meta[decisionSlotKey]; ok || got.Meta["x/kept"] != "yes" {
		t.Errorf("served _meta = %v, want the slot dropped and x/kept kept", got.Meta)
	}
	if _, ok := meta[decisionSlotKey]; !ok || res.Meta[decisionSlotKey] == nil {
		t.Error("withoutUpstreamSlot wrote the upstream's Meta map")
	}
	plain := &mcp.CallToolResult{Meta: mcp.Meta{"x/kept": "yes"}}
	if withoutUpstreamSlot(plain) != plain || withoutUpstreamSlot(nil) != nil {
		t.Error("a result without the slot must be served as it is")
	}
}
