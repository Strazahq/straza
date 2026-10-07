package approval

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/strazahq/straza/internal/store"
)

func statusByName(t *testing.T, list []ChannelStatus, name string) ChannelStatus {
	t.Helper()
	for _, s := range list {
		if s.Name == name {
			return s
		}
	}
	t.Fatalf("channel %q missing from statuses: %+v", name, list)
	return ChannelStatus{}
}

// TestChannelStatusesUnconfigured: a bare service reports the console as
// always-on and both third-party channels as visibly unconfigured, each with
// the config key that would enable it. Silence must be diagnosable.
func TestChannelStatusesUnconfigured(t *testing.T) {
	h := newHarness(t)
	got, err := h.svc.ChannelStatuses(context.Background())
	if err != nil {
		t.Fatalf("ChannelStatuses: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("statuses = %d rows, want console+slack+push", len(got))
	}
	if c := statusByName(t, got, "console"); !c.Configured || c.LastDelivery != nil {
		t.Errorf("console = %+v, want configured, no delivery record", c)
	}
	if s := statusByName(t, got, "slack"); s.Configured || !strings.Contains(s.Detail, "approval.channels.slack") {
		t.Errorf("slack = %+v, want unconfigured naming the config key", s)
	}
	if p := statusByName(t, got, "push"); p.Configured || !strings.Contains(p.Detail, "approval.push") {
		t.Errorf("push = %+v, want unconfigured naming the config key", p)
	}
}

// TestChannelStatusesConfigured: attached channels report configured with the
// enrolled-devices vs push-routes gap (THE diagnostic for a phone that never
// rings) and the
// last delivery attempt.
func TestChannelStatusesConfigured(t *testing.T) {
	h := newHarness(t)
	attachSlack(t, h, "http://unused", "kim@x.io", true)
	attachPush(t, h, "ntfy.sh")
	kim := h.seedUser(t, "kim", "sec-approvers")
	h.seedPushDevice(t, kim.ID, "unifiedpush", "https://ntfy.sh/kim")
	// A second device with NO push registration, the gap the card exposes.
	if _, err := h.st.Approvers().InsertDevice(context.Background(), store.ApproverDevice{UserID: kim.ID, Name: "bare"}); err != nil {
		t.Fatalf("InsertDevice: %v", err)
	}

	h.svc.recordDelivery("push", nil, "unifiedpush send")
	got, err := h.svc.ChannelStatuses(context.Background())
	if err != nil {
		t.Fatalf("ChannelStatuses: %v", err)
	}
	if s := statusByName(t, got, "slack"); !s.Configured || !strings.Contains(s.Detail, "C1") {
		t.Errorf("slack = %+v, want configured with channel id", s)
	}
	p := statusByName(t, got, "push")
	if !p.Configured || p.Devices != 2 || p.Registrations != 1 {
		t.Errorf("push = %+v, want configured, 2 devices, 1 registration", p)
	}
	if !strings.Contains(p.Detail, "ntfy.sh") {
		t.Errorf("push detail %q must name the allowlisted host", p.Detail)
	}
	if p.LastDelivery == nil || !p.LastDelivery.OK || p.LastDelivery.Note != "unifiedpush send" {
		t.Errorf("push lastDelivery = %+v, want the recorded ok attempt", p.LastDelivery)
	}
}

// TestRecordDeliveryFailureNote: a failed attempt records OK=false with the
// error folded into the note.
func TestRecordDeliveryFailureNote(t *testing.T) {
	h := newHarness(t)
	h.svc.recordDelivery("slack", context.DeadlineExceeded, "card posted")
	rec, ok := h.svc.lastDeliveryFor("slack")
	if !ok || rec.OK || !strings.Contains(rec.Note, "deadline") {
		t.Fatalf("lastDelivery = %+v, %v; want failed record naming the error", rec, ok)
	}
}

// TestTestChannelErrors: console is not testable, unknown names 404-shaped,
// unconfigured channels refuse.
func TestTestChannelErrors(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	if _, err := h.svc.TestChannel(ctx, "console"); err != ErrChannelNotTestable {
		t.Errorf("console test err = %v, want ErrChannelNotTestable", err)
	}
	if _, err := h.svc.TestChannel(ctx, "teams"); err != ErrChannelUnknown {
		t.Errorf("unknown test err = %v, want ErrChannelUnknown", err)
	}
	if _, err := h.svc.TestChannel(ctx, "slack"); err != ErrChannelOff {
		t.Errorf("slack-off test err = %v, want ErrChannelOff", err)
	}
	if _, err := h.svc.TestChannel(ctx, "push"); err != ErrChannelOff {
		t.Errorf("push-off test err = %v, want ErrChannelOff", err)
	}
}

// TestTestChannelSlack: the slack test posts one real message through the Web
// API and reports the single target.
func TestTestChannelSlack(t *testing.T) {
	h := newHarness(t)
	fake := newSlackFake(t, "kim@x.io", true)
	attachSlack(t, h, fake.srv.URL, "kim@x.io", true)

	report, err := h.svc.TestChannel(context.Background(), "slack")
	if err != nil {
		t.Fatalf("TestChannel: %v", err)
	}
	if len(report.Targets) != 1 || !report.Targets[0].OK || fake.posted != 1 {
		t.Fatalf("slack test = %+v (posted %d), want one ok target and one post", report, fake.posted)
	}
	if rec, ok := h.svc.lastDeliveryFor("slack"); !ok || !rec.OK || rec.Note != "test message" {
		t.Errorf("lastDelivery = %+v, %v; want ok test-message record", rec, ok)
	}
}

// TestTestChannelPush: the push test rides the REAL senders (allowlist and
// TLS enforced) and reports per-target outcomes: a reachable allowlisted
// endpoint succeeds, a non-allowlisted one fails with the allowlist reason,
// and an apns row with the backend off names the enable knob.
func TestTestChannelPush(t *testing.T) {
	h := newHarness(t)
	attachPush(t, h, "127.0.0.1")

	var gotBody string
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b := make([]byte, 256)
		n, _ := r.Body.Read(b)
		gotBody = string(b[:n])
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)
	h.svc.push.httpc = srv.Client()

	kim := h.seedUser(t, "kim", "sec-approvers")
	h.seedPushDevice(t, kim.ID, "unifiedpush", srv.URL+"/straza-test")
	h.seedPushDevice(t, kim.ID, "unifiedpush", "https://evil.example/x")
	h.seedPushDevice(t, kim.ID, "apns", "token-1")

	report, err := h.svc.TestChannel(context.Background(), "push")
	if err != nil {
		t.Fatalf("TestChannel: %v", err)
	}
	if len(report.Targets) != 3 {
		t.Fatalf("targets = %d, want 3: %+v", len(report.Targets), report.Targets)
	}
	var ok, allowlist, disabled int
	for _, tg := range report.Targets {
		switch {
		case tg.OK:
			ok++
		case strings.Contains(tg.Error, "allowedPushHosts"):
			allowlist++
		case strings.Contains(tg.Error, "approval.push.apns.keyFile"):
			disabled++
		}
	}
	if ok != 1 || allowlist != 1 || disabled != 1 {
		t.Fatalf("outcomes = %+v, want 1 ok / 1 allowlist / 1 backend-disabled", report.Targets)
	}
	if !strings.Contains(gotBody, `"kind":"status"`) || !strings.Contains(gotBody, `"ref":"test:`) {
		t.Errorf("test payload = %q, want the opaque status envelope with a test ref", gotBody)
	}
}

// TestPushTestLaneMarkers: the test report names each target's encryption lane
// with the SAME predicate the send path dispatches on: keyed (aes128gcm) vs
// legacy (plain JSON) for unifiedpush/webpush rows, no marker for kinds
// without that axis (fcm), so an operator reads which lane a send took from
// the report instead of box archaeology. Outcomes are not asserted here; the
// marker must be present whether or not the send succeeds.
func TestPushTestLaneMarkers(t *testing.T) {
	h := newHarness(t)
	attachPush(t, h, "ntfy.example")
	kim := h.seedUser(t, "kim", "sec-approvers")
	keyedID := h.seedPushDevice(t, kim.ID, "unifiedpush",
		encodeKeyedEndpoint("https://ntfy.example/up1?up=1", "BPub", "AuTh"))
	legacyID := h.seedPushDevice(t, kim.ID, "unifiedpush", "https://ntfy.example/up2")
	fcmID := h.seedPushDevice(t, kim.ID, "fcm", "fcm-token-1")

	report, err := h.svc.TestChannel(context.Background(), "push")
	if err != nil {
		t.Fatalf("TestChannel: %v", err)
	}
	want := map[string]string{ // device id → required Target string
		keyedID:  "unifiedpush · device " + keyedID + " · keyed",
		legacyID: "unifiedpush · device " + legacyID + " · legacy",
		fcmID:    "fcm · device " + fcmID,
	}
	if len(report.Targets) != len(want) {
		t.Fatalf("targets = %d, want %d: %+v", len(report.Targets), len(want), report.Targets)
	}
	got := map[string]bool{}
	for _, tg := range report.Targets {
		got[tg.Target] = true
	}
	for id, target := range want {
		if !got[target] {
			t.Errorf("device %s: no target %q in %+v", id, target, report.Targets)
		}
	}
}
