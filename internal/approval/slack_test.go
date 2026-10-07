package approval

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/strazahq/straza/internal/config"
	"github.com/strazahq/straza/internal/policy"
	"github.com/strazahq/straza/internal/store"
)

const testSigningSecret = "8f742231b10c8538a610bffdff6d825b"

// slackFake records calls and answers the three Web API methods we use.
type slackFake struct {
	srv     *httptest.Server
	posted  int
	mu      sync.Mutex
	updates []slackUpdate // every chat.update body, in arrival order
	email   string        // users.info reply email
	usersOK bool
}

// slackUpdate is the part of a chat.update body the card tests assert on.
type slackUpdate struct {
	Channel string `json:"channel"`
	TS      string `json:"ts"`
	Text    string `json:"text"`
}

// cardUpdates returns a copy of the chat.update calls seen so far; the hooks
// that issue them run on their own goroutines.
func (f *slackFake) cardUpdates() []slackUpdate {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]slackUpdate(nil), f.updates...)
}

func newSlackFake(t *testing.T, email string, usersOK bool) *slackFake {
	f := &slackFake{email: email, usersOK: usersOK}
	mux := http.NewServeMux()
	mux.HandleFunc("/chat.postMessage", func(w http.ResponseWriter, r *http.Request) {
		f.posted++
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true,"ts":"111.222","channel":"C1"}`))
	})
	mux.HandleFunc("/chat.update", func(w http.ResponseWriter, r *http.Request) {
		var body slackUpdate
		_ = json.NewDecoder(r.Body).Decode(&body)
		f.mu.Lock()
		f.updates = append(f.updates, body)
		f.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	})
	mux.HandleFunc("/users.info", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if !f.usersOK {
			_, _ = w.Write([]byte(`{"ok":false,"error":"user_not_found"}`))
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"ok":   true,
			"user": map[string]any{"profile": map[string]any{"email": f.email}},
		})
	})
	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	return f
}

func attachSlack(t *testing.T, h *harness, apiBase, email string, usersOK bool) *slackChannel {
	t.Helper()
	sc, err := newSlackChannel(config.SlackChannel{
		Enabled: true, BotToken: "xoxb-test", SigningSecret: testSigningSecret, Channel: "C1",
	}, h.st, h.svc, slog.Default())
	if err != nil {
		t.Fatalf("newSlackChannel: %v", err)
	}
	sc.apiBase = apiBase
	h.svc.slack = sc
	h.svc.register(sc)
	return sc
}

func slackSign(ts string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(testSigningSecret))
	mac.Write([]byte("v0:" + ts + ":" + string(body)))
	return "v0=" + hex.EncodeToString(mac.Sum(nil))
}

func TestSlackSignatureVerification(t *testing.T) {
	h := newHarness(t)
	sc := attachSlack(t, h, "http://unused", "kim@x.io", true)
	body := []byte("payload=%7B%7D")
	now := time.Unix(1_700_000_000, 0)
	h.svc.now = func() time.Time { return now }
	nowTS := strconv.FormatInt(now.Unix(), 10)

	tests := []struct {
		name string
		ts   string
		sig  string
		want bool
	}{
		{"good", nowTS, slackSign(nowTS, body), true},
		{"bad signature", nowTS, "v0=deadbeef", false},
		{"stale timestamp", strconv.FormatInt(now.Add(-10*time.Minute).Unix(), 10), slackSign(strconv.FormatInt(now.Add(-10*time.Minute).Unix(), 10), body), false},
		{"missing", "", "", false},
		{"replayed body under a fresh ts", nowTS, slackSign(nowTS, []byte("payload=other")), false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := sc.verifySignature(tc.ts, tc.sig, body); got != tc.want {
				t.Errorf("verifySignature = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestSlackNotifyStoresRefs(t *testing.T) {
	h := newHarness(t)
	fake := newSlackFake(t, "kim@x.io", true)
	sc := attachSlack(t, h, fake.srv.URL, "kim@x.io", true)

	now := time.Now().UTC()
	seeded, err := h.st.Approvals().Insert(context.Background(), store.Approval{
		SessionID: "s-1", Username: "nova", Summary: "mcp.call midpoint:disable_user",
		State: "pending", CreatedAt: now, ExpiresAt: now.Add(time.Minute),
	})
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
	rec := recordFromStore(seeded)

	sc.created(rec)
	if fake.posted != 1 {
		t.Errorf("chat.postMessage called %d times, want 1", fake.posted)
	}
	got, _ := h.st.Approvals().GetByID(context.Background(), rec.ID)
	if got.ChannelRefs["slack_ts"] != "111.222" || got.ChannelRefs["slack_channel"] != "C1" {
		t.Errorf("channel refs = %v", got.ChannelRefs)
	}
}

func TestSlackCallbackApproves(t *testing.T) {
	h := newHarness(t)
	requester := h.seedUser(t, "nova")
	approver := h.seedUser(t, "kim", "sec-approvers")
	fake := newSlackFake(t, "kim@x.io", true) // kim@x.io maps to the approver
	attachSlack(t, h, fake.srv.URL, "kim@x.io", true)

	spec := policy.ApproveSpec{Roles: []string{"sec-approvers"}, TimeoutSeconds: 90, RetryTTLSeconds: 60}
	rec, _ := h.svc.Request(context.Background(), req("s-1", requester.ID, "nova", spec))
	_ = approver

	resp := slackCallback(t, h, rec.ID, "approve", "approved", rec.ExpiresAt, "U-KIM")
	if resp.Code != http.StatusOK {
		t.Fatalf("callback status = %d, body %s", resp.Code, resp.Body.String())
	}
	got, _ := h.svc.Get(context.Background(), rec.ID)
	if got.State != StateApproved || got.Channel != "slack" {
		t.Errorf("record after slack approve = %+v", got)
	}
}

func TestSlackCallbackUnmappableUser(t *testing.T) {
	h := newHarness(t)
	requester := h.seedUser(t, "nova")
	fake := newSlackFake(t, "ghost@x.io", true) // email maps to no Straza user
	attachSlack(t, h, fake.srv.URL, "ghost@x.io", true)

	spec := policy.ApproveSpec{Roles: []string{"sec-approvers"}, TimeoutSeconds: 90, RetryTTLSeconds: 60}
	rec, _ := h.svc.Request(context.Background(), req("s-1", requester.ID, "nova", spec))

	resp := slackCallback(t, h, rec.ID, "approve", "approved", rec.ExpiresAt, "U-GHOST")
	if resp.Code != http.StatusOK || !strings.Contains(resp.Body.String(), "not linked") {
		t.Fatalf("unmappable callback = %d %s", resp.Code, resp.Body.String())
	}
	got, _ := h.svc.Get(context.Background(), rec.ID)
	if got.State != StatePending {
		t.Errorf("unmappable tap must not resolve the record, state=%s", got.State)
	}
}

func TestSlackCallbackSelfApproval(t *testing.T) {
	h := newHarness(t)
	requester := h.seedUser(t, "nova") // nova@x.io
	fake := newSlackFake(t, "nova@x.io", true)
	attachSlack(t, h, fake.srv.URL, "nova@x.io", true)

	spec := policy.ApproveSpec{Roles: []string{"sec-approvers"}, TimeoutSeconds: 90, RetryTTLSeconds: 60}
	rec, _ := h.svc.Request(context.Background(), req("s-1", requester.ID, "nova", spec))

	resp := slackCallback(t, h, rec.ID, "approve", "approved", rec.ExpiresAt, "U-NOVA")
	if resp.Code != http.StatusOK || !strings.Contains(resp.Body.String(), "your own request") {
		t.Fatalf("self-approval callback = %d %s", resp.Code, resp.Body.String())
	}
	got, _ := h.svc.Get(context.Background(), rec.ID)
	if got.State != StatePending {
		t.Errorf("self-approval must not resolve the record, state=%s", got.State)
	}
}

func TestSlackCallbackBadSignature(t *testing.T) {
	h := newHarness(t)
	requester := h.seedUser(t, "nova")
	fake := newSlackFake(t, "kim@x.io", true)
	attachSlack(t, h, fake.srv.URL, "kim@x.io", true)
	spec := policy.ApproveSpec{Roles: []string{"sec-approvers"}, TimeoutSeconds: 90, RetryTTLSeconds: 60}
	rec, _ := h.svc.Request(context.Background(), req("s-1", requester.ID, "nova", spec))

	// Craft a request with a wrong signature.
	payload := slackPayloadJSON(rec.ID, "approve", mustToken(t, h, rec.ID, "approved", rec.ExpiresAt), "U-KIM")
	body := "payload=" + url.QueryEscape(payload)
	r := httptest.NewRequest(http.MethodPost, "/v1/approval/callbacks/slack", strings.NewReader(body))
	r.Header.Set("X-Slack-Request-Timestamp", strconv.FormatInt(time.Now().Unix(), 10))
	r.Header.Set("X-Slack-Signature", "v0=bad")
	w := httptest.NewRecorder()
	h.svc.HandleSlackCallback(w, r)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("bad signature status = %d, want 401", w.Code)
	}
}

// TestSlackResolvedRepaintsWithoutDecision pins the resolved hook per record:
// a resolution that carries no human decision (expiry) repaints the card Slack
// posted, a human decision leaves the repaint to reconcile on the broadcast,
// and a record Slack never carded is left alone.
func TestSlackResolvedRepaintsWithoutDecision(t *testing.T) {
	at := time.Date(2026, 9, 7, 15, 4, 0, 0, time.UTC)
	refs := map[string]string{"slack_ts": "111.222", "slack_channel": "C1"}
	cases := []struct {
		name        string
		rec         Record
		wantUpdates int
		wantText    string
	}{
		{"expired record repaints once", Record{
			ID: "a1", Username: "nova", Summary: "mcp.call midpoint:disable_user",
			State: StateExpired, DecidedAt: &at, ChannelRefs: refs,
		}, 1, "Expired · 15:04"},
		{"decided record leaves the repaint to reconcile", Record{
			ID: "a2", Username: "nova", Summary: "mcp.call midpoint:disable_user",
			State: StateApproved, DecidedBy: "u-kim", DecidedByName: "kim", DecidedAt: &at, ChannelRefs: refs,
		}, 0, ""},
		{"expired record Slack never carded is left alone", Record{
			ID: "a3", Username: "nova", Summary: "mcp.call midpoint:disable_user",
			State: StateExpired, DecidedAt: &at,
		}, 0, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			fake := newSlackFake(t, "kim@x.io", true)
			sc := attachSlack(t, h, fake.srv.URL, "kim@x.io", true)

			sc.resolved(tc.rec)
			got := fake.cardUpdates()
			if len(got) != tc.wantUpdates {
				t.Fatalf("chat.update called %d times, want %d: %+v", len(got), tc.wantUpdates, got)
			}
			if tc.wantUpdates == 1 && (got[0].Channel != "C1" || got[0].TS != "111.222" || got[0].Text != tc.wantText) {
				t.Errorf("card update = %+v, want channel C1, ts 111.222, text %q", got[0], tc.wantText)
			}
		})
	}
}

// TestSlackCardRepaintsOncePerResolution drives both resolution lanes through
// the service on the fake bus and asserts one card repaint each: the expiry
// sweep reaches the card through the resolved hook (no broadcast exists for
// it), a human decision through reconcile on the broadcast, never both.
func TestSlackCardRepaintsOncePerResolution(t *testing.T) {
	now := time.Date(2026, 9, 7, 15, 4, 0, 0, time.UTC)
	cases := []struct {
		name       string
		expire     bool
		wantPrefix string
	}{
		{"expiry sweep repaints once", true, "Expired · "},
		{"human decision repaints once", false, "Approved by kim · "},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			h.svc.now = func() time.Time { return now }
			requester := h.seedUser(t, "nova")
			approver := h.seedUser(t, "kim", "sec-approvers")
			fake := newSlackFake(t, "kim@x.io", true)
			attachSlack(t, h, fake.srv.URL, "kim@x.io", true)
			ctx := context.Background()

			expires := now.Add(time.Minute)
			if tc.expire {
				expires = now.Add(-time.Second)
			}
			seeded, err := h.st.Approvals().Insert(ctx, store.Approval{
				SessionID: "s-1", UserID: requester.ID, Username: "nova", RuleID: "r-1",
				Summary: "mcp.call midpoint:disable_user", ApproverRoles: []string{"sec-approvers"},
				State: "pending", RetryTTLSeconds: 60, CreatedAt: now.Add(-time.Minute), ExpiresAt: expires,
				ChannelRefs: map[string]string{"slack_ts": "111.222", "slack_channel": "C1"},
			})
			if err != nil {
				t.Fatalf("seed: %v", err)
			}
			if tc.expire {
				h.svc.sweepExpired(ctx)
			} else if _, err := h.svc.Decide(ctx, seeded.ID, "approved", approver.ID, "console", "", ""); err != nil {
				t.Fatalf("Decide: %v", err)
			}

			got := waitCardUpdates(fake, 1)
			if len(got) != 1 {
				t.Fatalf("chat.update called %d times, want exactly 1: %+v", len(got), got)
			}
			if got[0].Channel != "C1" || got[0].TS != "111.222" || !strings.HasPrefix(got[0].Text, tc.wantPrefix) {
				t.Errorf("card update = %+v, want channel C1, ts 111.222, text starting %q", got[0], tc.wantPrefix)
			}
		})
	}
}

// waitCardUpdates polls the fake until at least want chat.update calls landed
// (the hooks run on their own goroutines), then waits a beat so a duplicate
// repaint arriving right behind the first is counted too.
func waitCardUpdates(f *slackFake, want int) []slackUpdate {
	deadline := time.Now().Add(3 * time.Second)
	for len(f.cardUpdates()) < want && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	time.Sleep(100 * time.Millisecond)
	return f.cardUpdates()
}

// --- callback helpers ---

func slackCallback(t *testing.T, h *harness, id, actionID, verdict string, exp time.Time, slackUser string) *httptest.ResponseRecorder {
	t.Helper()
	payload := slackPayloadJSON(id, actionID, mustToken(t, h, id, verdict, exp), slackUser)
	body := "payload=" + url.QueryEscape(payload)
	ts := strconv.FormatInt(time.Now().Unix(), 10)
	r := httptest.NewRequest(http.MethodPost, "/v1/approval/callbacks/slack", strings.NewReader(body))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.Header.Set("X-Slack-Request-Timestamp", ts)
	r.Header.Set("X-Slack-Signature", slackSign(ts, []byte(body)))
	w := httptest.NewRecorder()
	h.svc.HandleSlackCallback(w, r)
	return w
}

func slackPayloadJSON(id, actionID, token, slackUser string) string {
	b, _ := json.Marshal(map[string]any{
		"type": "block_actions",
		"user": map[string]any{"id": slackUser},
		"actions": []map[string]any{
			{"action_id": actionID, "block_id": id, "value": token},
		},
	})
	return string(b)
}

func mustToken(t *testing.T, h *harness, id, verdict string, exp time.Time) string {
	t.Helper()
	tok, err := h.svc.MintDecisionToken(id, verdict, exp)
	if err != nil {
		t.Fatalf("MintDecisionToken: %v", err)
	}
	return tok
}

// TestSlackRequestBlocksPreview: a record carrying an args preview renders a
// code-block params section ABOVE the justification and a "preview only" honesty
// context line beneath, with the mcp tool-identity wording.
func TestSlackRequestBlocksPreview(t *testing.T) {
	h := newHarness(t)
	sc := attachSlack(t, h, "http://unused", "kim@x.io", true)
	sc.includeJustification = true // exercise the params-above-justification ordering
	now := time.Now().UTC()
	rec := Record{
		ID: "a1", Username: "nova", Summary: "mcp.call midpoint:disable_user",
		ArgvHash: "sha256:0123456789abcdef0123", ExpiresAt: now.Add(time.Minute),
		ArgsPreview: "{\n  \"user\": \"nova\"\n}", Justification: "offboard the leaver",
	}
	raw, err := json.Marshal(sc.requestBlocks(rec))
	if err != nil {
		t.Fatalf("marshal blocks: %v", err)
	}
	s := string(raw)
	if !strings.Contains(s, "Call parameters (preview)") || !strings.Contains(s, "nova") {
		t.Errorf("preview params block missing: %s", s)
	}
	if !strings.Contains(s, "preview only; this approval covers tool identity (sha256:0123456789ab)") {
		t.Errorf("honesty line missing/wrong: %s", s)
	}
	// params must appear before the justification section.
	if strings.Index(s, "Call parameters") > strings.Index(s, "stated reason") {
		t.Error("params must render above the justification")
	}
}

// TestSlackClampPreviewDefusesFence: an embedded triple-backtick cannot close the
// code fence early, and the result is clamped.
func TestSlackClampPreviewDefusesFence(t *testing.T) {
	if got := slackClampPreview("a```b"); strings.Contains(got, "```") {
		t.Errorf("triple backtick not defused: %q", got)
	}
	long := slackClampPreview(strings.Repeat("x", 5000))
	if len([]rune(long)) > slackPreviewMax {
		t.Errorf("clamp exceeded %d runes", slackPreviewMax)
	}
}
