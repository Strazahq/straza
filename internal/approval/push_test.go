package approval

import (
	"context"
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/strazahq/straza/internal/policy"
	"github.com/strazahq/straza/internal/store"
)

// --- recorder transport (routing tests where HTTP is not the point) ---

type pushRecord struct {
	kind, ref, device, user, token string
}

type pushRecorder struct{ ch chan pushRecord }

func newPushRecorder() *pushRecorder { return &pushRecorder{ch: make(chan pushRecord, 64)} }

func (r *pushRecorder) transport(reg store.ApproverPushTarget, kind, ref string, _ time.Time) {
	r.ch <- pushRecord{kind: kind, ref: ref, device: reg.DeviceID, user: reg.UserID, token: reg.TokenOrEndpoint}
}

// attachPush installs a recorder-backed pushDelivery on the harness service and
// returns the recorder. allowedHosts seeds the UnifiedPush allowlist.
func attachPush(t *testing.T, h *harness, allowedHosts ...string) *pushRecorder {
	t.Helper()
	rec := newPushRecorder()
	pd := &pushDelivery{
		svc:          h.svc,
		st:           h.st,
		log:          slog.Default(),
		httpc:        &http.Client{Timeout: pushTimeout},
		allowedHosts: map[string]struct{}{},
	}
	for _, host := range allowedHosts {
		pd.allowedHosts[host] = struct{}{}
	}
	pd.transport = rec.transport
	h.svc.push = pd
	h.svc.register(pd)
	return rec
}

// seedPushDevice registers a device + one push registration for a user and
// returns the device id.
func (h *harness) seedPushDevice(t *testing.T, userID, kind, tok string) string {
	t.Helper()
	ctx := context.Background()
	d, err := h.st.Approvers().InsertDevice(ctx, store.ApproverDevice{UserID: userID, Name: "dev"})
	if err != nil {
		t.Fatalf("InsertDevice: %v", err)
	}
	if err := h.st.Approvers().UpsertPush(ctx, store.ApproverPush{DeviceID: d.ID, Kind: kind, TokenOrEndpoint: tok}); err != nil {
		t.Fatalf("UpsertPush: %v", err)
	}
	return d.ID
}

// drainNow returns every currently-buffered record (decide/status called
// synchronously fills the buffer before returning).
func drainNow(rec *pushRecorder) []pushRecord {
	var out []pushRecord
	for {
		select {
		case r := <-rec.ch:
			out = append(out, r)
		default:
			return out
		}
	}
}

func waitPush(t *testing.T, rec *pushRecorder, n int, timeout time.Duration) []pushRecord {
	t.Helper()
	var out []pushRecord
	deadline := time.After(timeout)
	for len(out) < n {
		select {
		case r := <-rec.ch:
			out = append(out, r)
		case <-deadline:
			t.Fatalf("waited for %d push(es), got %d: %+v", n, len(out), out)
		}
	}
	return out
}

func assertNoPush(t *testing.T, rec *pushRecorder, grace time.Duration) {
	t.Helper()
	select {
	case r := <-rec.ch:
		t.Fatalf("unexpected extra push: %+v", r)
	case <-time.After(grace):
	}
}

func userSet(recs []pushRecord) []string {
	seen := map[string]bool{}
	for _, r := range recs {
		seen[r.user] = true
	}
	out := make([]string, 0, len(seen))
	for u := range seen {
		out = append(out, u)
	}
	sort.Strings(out)
	return out
}

// --- payload privacy pin ---

// TestPushPayloadKeysExact locks the wire body to exactly three opaque keys:
// v/ref/kind, with v==1. No command text, arguments, or usernames may ever
// appear (privacy rule).
func TestPushPayloadKeysExact(t *testing.T) {
	b, err := json.Marshal(pushPayload{V: 1, Ref: "apr-xyz", Kind: pushKindDecide})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(m) != 3 {
		t.Fatalf("payload has %d keys, want exactly 3: %s", len(m), b)
	}
	for _, k := range []string{"v", "ref", "kind"} {
		if _, ok := m[k]; !ok {
			t.Errorf("payload missing key %q: %s", k, b)
		}
	}
	if string(m["v"]) != "1" {
		t.Errorf("v = %s, want 1", m["v"])
	}
	if string(m["ref"]) != `"apr-xyz"` || string(m["kind"]) != `"decide"` {
		t.Errorf("ref/kind = %s/%s", m["ref"], m["kind"])
	}
}

// --- decide routing eligibility ---

// TestPushDecideRouting is the eligibility table: role match, missing role,
// NHI hard-block, requester exclusion on non-self records, requester inclusion
// on self-approval records.
func TestPushDecideRouting(t *testing.T) {
	h := newHarness(t)
	rec := attachPush(t, h)

	nova := h.seedUser(t, "nova", "sec-approvers")  // requester, holds the role
	kim := h.seedUser(t, "kim", "sec-approvers")    // eligible approver
	mallory := h.seedUser(t, "mallory")             // no role
	robot := h.seedNHI(t, "robot", "sec-approvers") // NHI with the role
	h.seedPushDevice(t, nova.ID, "unifiedpush", "https://ntfy.sh/nova")
	h.seedPushDevice(t, kim.ID, "unifiedpush", "https://ntfy.sh/kim")
	h.seedPushDevice(t, mallory.ID, "unifiedpush", "https://ntfy.sh/mallory")
	h.seedPushDevice(t, robot.ID, "unifiedpush", "https://ntfy.sh/robot")

	// Non-self record: only kim (role, not requester, not NHI) is a target.
	nonSelf := Record{ID: "apr-1", UserID: nova.ID, ApproverRoles: []string{"sec-approvers"}, SelfApproval: false}
	h.svc.push.created(nonSelf)
	if got := userSet(drainNow(rec)); len(got) != 1 || got[0] != kim.ID {
		t.Errorf("non-self decide targets = %v, want [kim(%s)] only", got, kim.ID)
	}

	// Self-approval record: kim AND the self-approving requester nova.
	self := Record{ID: "apr-2", UserID: nova.ID, ApproverRoles: []string{"sec-approvers"}, SelfApproval: true}
	h.svc.push.created(self)
	got := userSet(drainNow(rec))
	want := []string{kim.ID, nova.ID}
	sort.Strings(want)
	if len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Errorf("self-approval decide targets = %v, want %v (kim + self requester)", got, want)
	}

	// Every emitted record carries the approval id as the opaque ref and the
	// decide kind, never any user/command detail.
	h.svc.push.created(nonSelf)
	for _, r := range drainNow(rec) {
		if r.kind != pushKindDecide || r.ref != "apr-1" {
			t.Errorf("decide record = %+v, want kind=decide ref=apr-1", r)
		}
	}
}

// TestPushStatusTargetsRequester: status pushes go only to the requester's own
// registrations, never to approvers.
func TestPushStatusTargetsRequester(t *testing.T) {
	h := newHarness(t)
	rec := attachPush(t, h)
	nova := h.seedUser(t, "nova")
	kim := h.seedUser(t, "kim", "sec-approvers")
	h.seedPushDevice(t, nova.ID, "unifiedpush", "https://ntfy.sh/nova")
	h.seedPushDevice(t, kim.ID, "unifiedpush", "https://ntfy.sh/kim")

	h.svc.push.resolved(Record{ID: "apr-9", UserID: nova.ID, State: StateApproved})
	got := drainNow(rec)
	if len(got) != 1 || got[0].user != nova.ID || got[0].kind != pushKindStatus || got[0].ref != "apr-9" {
		t.Fatalf("status targets = %+v, want single status push to requester nova", got)
	}
}

// --- Service wiring (exactly-once) ---

// TestRequestFiresDecideOnceNotOnDedupe: a new Request nudges approvers; the
// dedupe retry fires no second push (the Slack no-spam contract).
func TestRequestFiresDecideOnceNotOnDedupe(t *testing.T) {
	h := newHarness(t)
	rec := attachPush(t, h)
	requester := h.seedUser(t, "nova")
	kim := h.seedUser(t, "kim", "sec-approvers")
	h.seedPushDevice(t, kim.ID, "unifiedpush", "https://ntfy.sh/kim")
	spec := policy.ApproveSpec{Roles: []string{"sec-approvers"}, TimeoutSeconds: 90, RetryTTLSeconds: 60}

	ctx := context.Background()
	if _, err := h.svc.Request(ctx, req("s-1", requester.ID, "nova", spec)); err != nil {
		t.Fatalf("Request: %v", err)
	}
	got := waitPush(t, rec, 1, 2*time.Second)
	if got[0].user != kim.ID || got[0].kind != pushKindDecide {
		t.Fatalf("first decide push = %+v, want to kim", got[0])
	}

	// Dedupe hit returns before the push fire; no second nudge.
	if _, err := h.svc.Request(ctx, req("s-1", requester.ID, "nova", spec)); err != nil {
		t.Fatalf("dedupe Request: %v", err)
	}
	assertNoPush(t, rec, 300*time.Millisecond)
}

// TestDecideFiresExactlyOneStatusPush: resolving a request fires a single
// status push to the requester (and none to the requester on the decide lane).
func TestDecideFiresExactlyOneStatusPush(t *testing.T) {
	h := newHarness(t)
	rec := attachPush(t, h)
	requester := h.seedUser(t, "nova")
	approver := h.seedUser(t, "kim", "sec-approvers")
	h.seedPushDevice(t, requester.ID, "unifiedpush", "https://ntfy.sh/nova")
	spec := policy.ApproveSpec{Roles: []string{"sec-approvers"}, TimeoutSeconds: 90, RetryTTLSeconds: 60}

	ctx := context.Background()
	r, err := h.svc.Request(ctx, req("s-1", requester.ID, "nova", spec))
	if err != nil {
		t.Fatalf("Request: %v", err)
	}
	// The requester is NOT a decide target on a non-self record, so Request
	// fires no push here (kim has no registration).
	assertNoPush(t, rec, 200*time.Millisecond)

	if _, err := h.svc.Decide(ctx, r.ID, "approved", approver.ID, "console", "", ""); err != nil {
		t.Fatalf("Decide: %v", err)
	}
	got := waitPush(t, rec, 1, 2*time.Second)
	if got[0].user != requester.ID || got[0].kind != pushKindStatus || got[0].ref != r.ID {
		t.Fatalf("status push = %+v, want single status to requester", got[0])
	}
	assertNoPush(t, rec, 300*time.Millisecond)
}

// TestPushStatusSkipsSelfDecided: a record decided by its own requester sends
// no status receipt at all (the requester made the decision, on whatever
// channel; a receipt would only repeat it back). Other-decided and expired
// records keep the full requester fan-out.
func TestPushStatusSkipsSelfDecided(t *testing.T) {
	h := newHarness(t)
	rec := attachPush(t, h)
	nova := h.seedUser(t, "nova")
	kim := h.seedUser(t, "kim", "sec-approvers")
	h.seedPushDevice(t, nova.ID, "unifiedpush", "https://ntfy.sh/nova-a")
	h.seedPushDevice(t, nova.ID, "unifiedpush", "https://ntfy.sh/nova-b")
	h.seedPushDevice(t, kim.ID, "unifiedpush", "https://ntfy.sh/kim")

	// Self-decided: zero receipts, on every registration the requester holds.
	h.svc.push.resolved(Record{ID: "apr-1", UserID: nova.ID, DecidedBy: nova.ID, State: StateApproved})
	if got := drainNow(rec); len(got) != 0 {
		t.Fatalf("self-decided status pushes = %+v, want none", got)
	}

	// Other-decided: the requester's registrations all get the receipt.
	h.svc.push.resolved(Record{ID: "apr-2", UserID: nova.ID, DecidedBy: kim.ID, State: StateDenied})
	got := drainNow(rec)
	if len(got) != 2 {
		t.Fatalf("other-decided status pushes = %+v, want both nova registrations", got)
	}
	for _, r := range got {
		if r.user != nova.ID || r.kind != pushKindStatus || r.ref != "apr-2" {
			t.Errorf("other-decided push = %+v, want status apr-2 to nova", r)
		}
	}

	// Expiry (no decider): the receipt is the only signal, full fan-out stays.
	h.svc.push.resolved(Record{ID: "apr-3", UserID: nova.ID, State: StateExpired})
	if got := drainNow(rec); len(got) != 2 {
		t.Fatalf("expired status pushes = %+v, want both nova registrations", got)
	}
}

// TestSelfDecideFiresNoStatusPush: the Decide wiring end to end. A
// self-approval requester still gets the decide nudge at Request time but no
// status receipt after deciding their own record.
func TestSelfDecideFiresNoStatusPush(t *testing.T) {
	h := newHarness(t)
	rec := attachPush(t, h)
	requester := h.seedUser(t, "nova", "sec-approvers")
	h.seedPushDevice(t, requester.ID, "unifiedpush", "https://ntfy.sh/nova")
	spec := policy.ApproveSpec{Roles: []string{"sec-approvers"}, SelfApproval: true, TimeoutSeconds: 90, RetryTTLSeconds: 60}

	ctx := context.Background()
	r, err := h.svc.Request(ctx, req("s-1", requester.ID, "nova", spec))
	if err != nil {
		t.Fatalf("Request: %v", err)
	}
	got := waitPush(t, rec, 1, 2*time.Second)
	if got[0].user != requester.ID || got[0].kind != pushKindDecide {
		t.Fatalf("self-approval decide push = %+v, want to requester", got[0])
	}

	if _, err := h.svc.Decide(ctx, r.ID, "approved", requester.ID, "phone", "", "dev-1"); err != nil {
		t.Fatalf("Decide: %v", err)
	}
	assertNoPush(t, rec, 300*time.Millisecond)
}

// --- UnifiedPush transport (real HTTP) ---

func TestUnifiedPushHostAllowlist(t *testing.T) {
	h := newHarness(t)
	pd := &pushDelivery{
		svc: h.svc, st: h.st, log: slog.Default(),
		httpc:        &http.Client{Timeout: pushTimeout},
		allowedHosts: map[string]struct{}{"ntfy.sh": {}, "*.notify.windows.com": {}},
	}
	cases := []struct {
		endpoint string
		want     bool
	}{
		{"https://ntfy.sh/topic", true},
		{"https://ntfy.sh:8443/topic", true}, // port stripped from host match
		{"http://ntfy.sh/topic", false},      // not https
		{"https://evil.example/topic", false},
		{"://broken", false},
		{"", false},
		// Wildcard entries: "*.suffix" admits subdomains, never the bare
		// suffix, never a lookalike that merely ends in the same letters.
		{"https://par02p.notify.windows.com/w/x", true},
		{"https://a.b.notify.windows.com/w/x", true},
		{"https://notify.windows.com/w/x", false},
		{"https://evilnotify.windows.com/w/x", false},
	}
	for _, c := range cases {
		if got := pd.hostAllowed(c.endpoint); got != c.want {
			t.Errorf("hostAllowed(%q) = %v, want %v", c.endpoint, got, c.want)
		}
	}
}

func TestUnifiedPushPostAndSkip(t *testing.T) {
	h := newHarness(t)
	bodies := make(chan []byte, 4)
	var gotHdr http.Header
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		gotHdr = r.Header.Clone()
		bodies <- b
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	pd := &pushDelivery{
		svc: h.svc, st: h.st, log: slog.Default(),
		httpc:        srv.Client(),
		allowedHosts: map[string]struct{}{"127.0.0.1": {}},
	}
	reg := store.ApproverPushTarget{
		ApproverPush: store.ApproverPush{DeviceID: "apd_1", Kind: "unifiedpush", TokenOrEndpoint: srv.URL + "/ntfy"},
		UserID:       "u-1",
	}
	_ = pd.sendUnifiedPush(reg, pushKindStatus, "apr-1", time.Time{})

	select {
	case body := <-bodies:
		var p pushPayload
		if err := json.Unmarshal(body, &p); err != nil {
			t.Fatalf("posted body not the payload: %v (%s)", err, body)
		}
		if p.V != 1 || p.Ref != "apr-1" || p.Kind != pushKindStatus {
			t.Errorf("posted payload = %+v, want {1 apr-1 status}", p)
		}
		if ct := gotHdr.Get("Content-Type"); ct != "application/json" {
			t.Errorf("content-type = %q, want application/json", ct)
		}
		// The legacy lane is preserved exactly, plus ONLY the RFC 8030 §5.2
		// mandatory TTL header (inert on ntfy). No encryption headers, no
		// VAPID, no Topic/Urgency; the current app and raw-ntfy subscribers
		// parse this body as-is.
		if ttl, err := strconv.Atoi(gotHdr.Get("TTL")); err != nil || ttl <= 0 {
			t.Errorf("legacy TTL header = %q, want a positive integer", gotHdr.Get("TTL"))
		}
		for _, forbidden := range []string{"Content-Encoding", "Authorization", "Topic", "Urgency", "X-Unifiedpush"} {
			if _, has := gotHdr[forbidden]; has {
				t.Errorf("legacy lane grew a %s header; the plain-JSON contract must stay byte-stable", forbidden)
			}
		}
	case <-time.After(2 * time.Second):
		t.Fatal("expected a POST to the allowlisted endpoint")
	}

	// Endpoint whose host is NOT allowlisted is skipped; no POST.
	bad := store.ApproverPushTarget{
		ApproverPush: store.ApproverPush{DeviceID: "apd_2", Kind: "unifiedpush", TokenOrEndpoint: "https://evil.example/x"},
		UserID:       "u-2",
	}
	_ = pd.sendUnifiedPush(bad, pushKindStatus, "apr-2", time.Time{})
	select {
	case body := <-bodies:
		t.Fatalf("host-disallowed endpoint was posted to: %s", body)
	case <-time.After(200 * time.Millisecond):
	}
}

// TestUnifiedPushPrunesDeadRegistration: a 410 from the endpoint deletes the
// stored registration row so it stops receiving.
func TestUnifiedPushPrunesDeadRegistration(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusGone)
	}))
	defer srv.Close()

	dev, err := h.st.Approvers().InsertDevice(ctx, store.ApproverDevice{UserID: "u-1", Name: "phone"})
	if err != nil {
		t.Fatalf("InsertDevice: %v", err)
	}
	endpoint := srv.URL + "/dead"
	if err := h.st.Approvers().UpsertPush(ctx, store.ApproverPush{DeviceID: dev.ID, Kind: "unifiedpush", TokenOrEndpoint: endpoint}); err != nil {
		t.Fatalf("UpsertPush: %v", err)
	}

	pd := &pushDelivery{
		svc: h.svc, st: h.st, log: slog.Default(),
		httpc:        srv.Client(),
		allowedHosts: map[string]struct{}{"127.0.0.1": {}},
	}
	reg := store.ApproverPushTarget{
		ApproverPush: store.ApproverPush{DeviceID: dev.ID, Kind: "unifiedpush", TokenOrEndpoint: endpoint},
		UserID:       "u-1",
	}
	_ = pd.sendUnifiedPush(reg, pushKindStatus, "apr-410", time.Time{})

	targets, err := h.st.Approvers().ListPushTargets(ctx)
	if err != nil {
		t.Fatalf("ListPushTargets: %v", err)
	}
	if len(targets) != 0 {
		t.Errorf("dead registration not pruned on 410, remaining = %+v", targets)
	}
}

// --- WebPush protocol transport (real HTTP, real crypto) ---

type capturedPush struct {
	header http.Header
	body   []byte
}

// newCaptureServer returns a TLS test server recording every POST it receives.
func newCaptureServer(t *testing.T, status int) (*httptest.Server, chan capturedPush) {
	t.Helper()
	ch := make(chan capturedPush, 8)
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		ch <- capturedPush{header: r.Header.Clone(), body: b}
		w.WriteHeader(status)
	}))
	t.Cleanup(srv.Close)
	return srv, ch
}

// TestWebPushDeliveryEncrypted: a keyed unifiedpush registration rides the
// full Web Push protocol: aes128gcm body that the subscription's private key
// decrypts to the exact opaque envelope, VAPID authorization whose aud is the
// endpoint's origin, mandatory TTL bounded by the approval's expiry, Urgency
// high, a ≤32-char Topic, and the ntfy binary-body escape header. A
// kind=webpush registration sends the same protocol WITHOUT the
// distributor-lane header.
func TestWebPushDeliveryEncrypted(t *testing.T) {
	h := newHarness(t)
	srv, got := newCaptureServer(t, http.StatusCreated)

	uaPriv, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("ua key: %v", err)
	}
	authSecret := make([]byte, 16)
	if _, err := rand.Read(authSecret); err != nil {
		t.Fatalf("auth: %v", err)
	}
	p256dhB64 := base64.RawURLEncoding.EncodeToString(uaPriv.PublicKey().Bytes())
	authB64 := base64.RawURLEncoding.EncodeToString(authSecret)

	wp := newTestWebPushSender(t, "mailto:ops@example.com", nil)
	pd := &pushDelivery{
		svc: h.svc, st: h.st, log: slog.Default(),
		httpc:        srv.Client(),
		allowedHosts: map[string]struct{}{"127.0.0.1": {}},
		webpush:      wp,
	}

	exp := h.svc.now().Add(90 * time.Second)
	reg := store.ApproverPushTarget{
		ApproverPush: store.ApproverPush{
			DeviceID: "apd_1", Kind: "unifiedpush",
			TokenOrEndpoint: encodeKeyedEndpoint(srv.URL+"/up?up=1", p256dhB64, authB64),
		},
		UserID: "u-1",
	}
	if err := pd.sendOne(reg, pushKindDecide, "apr-wp1", exp); err != nil {
		t.Fatalf("sendOne: %v", err)
	}

	var cp capturedPush
	select {
	case cp = <-got:
	case <-time.After(2 * time.Second):
		t.Fatal("no POST arrived")
	}
	if ct := cp.header.Get("Content-Type"); ct != "application/octet-stream" {
		t.Errorf("Content-Type = %q, want application/octet-stream", ct)
	}
	if ce := cp.header.Get("Content-Encoding"); ce != "aes128gcm" {
		t.Errorf("Content-Encoding = %q, want aes128gcm (RFC 8291)", ce)
	}
	if u := cp.header.Get("Urgency"); u != "high" {
		t.Errorf("Urgency = %q, want high", u)
	}
	if topic := cp.header.Get("Topic"); topic != webpushTopic("apr-wp1") || len(topic) > 32 {
		t.Errorf("Topic = %q (len %d), want %q", topic, len(topic), webpushTopic("apr-wp1"))
	}
	ttl, err := strconv.Atoi(cp.header.Get("TTL"))
	if err != nil || ttl <= 0 || ttl > 90 {
		t.Errorf("TTL = %q, want an integer in (0, 90] (the approval's remaining window)", cp.header.Get("TTL"))
	}
	if up := cp.header.Get("X-UnifiedPush"); up != "1" {
		t.Errorf("X-UnifiedPush = %q, want 1 on the distributor lane (ntfy binary-body escape)", up)
	}
	jwt, k := parseVAPID(t, cp.header.Get("Authorization"))
	if k != wp.publicKeyB64() {
		t.Errorf("VAPID k = %q, want the sender key", k)
	}
	claims := verifyVAPIDJWT(t, jwt, &wp.key.PublicKey)
	if claims["aud"] != srv.URL {
		t.Errorf("VAPID aud = %v, want the endpoint origin %s", claims["aud"], srv.URL)
	}
	var p pushPayload
	if err := json.Unmarshal(testWebPushDecrypt(t, uaPriv, authSecret, cp.body), &p); err != nil {
		t.Fatalf("decrypted body is not the envelope: %v", err)
	}
	if p.V != 1 || p.Ref != "apr-wp1" || p.Kind != pushKindDecide {
		t.Errorf("decrypted payload = %+v, want {1 apr-wp1 decide}", p)
	}

	// kind=webpush (browser/PWA subscription): same protocol, no
	// distributor-lane header.
	regW := store.ApproverPushTarget{
		ApproverPush: store.ApproverPush{
			DeviceID: "apd_2", Kind: "webpush",
			TokenOrEndpoint: encodeKeyedEndpoint(srv.URL+"/wp/sub", p256dhB64, authB64),
		},
		UserID: "u-1",
	}
	if err := pd.sendOne(regW, pushKindStatus, "apr-wp2", time.Time{}); err != nil {
		t.Fatalf("sendOne webpush: %v", err)
	}
	select {
	case cp = <-got:
	case <-time.After(2 * time.Second):
		t.Fatal("no webpush POST arrived")
	}
	if _, has := cp.header["X-Unifiedpush"]; has {
		t.Error("X-UnifiedPush must not ride the browser push-service lane")
	}
	if ce := cp.header.Get("Content-Encoding"); ce != "aes128gcm" {
		t.Errorf("webpush Content-Encoding = %q, want aes128gcm", ce)
	}
	if got := testWebPushDecrypt(t, uaPriv, authSecret, cp.body); !strings.Contains(string(got), `"apr-wp2"`) {
		t.Errorf("webpush payload = %s", got)
	}
}

// TestWebPushNeverFallsBackToPlaintext: a keyed registration with the WebPush
// sender unconfigured is an ERROR and no bytes leave the process; the keyed
// row must never degrade to the legacy plaintext POST.
func TestWebPushNeverFallsBackToPlaintext(t *testing.T) {
	h := newHarness(t)
	srv, got := newCaptureServer(t, http.StatusOK)
	uaPub, uaAuth := testSubscriptionKeys(t)
	pd := &pushDelivery{
		svc: h.svc, st: h.st, log: slog.Default(),
		httpc:        srv.Client(),
		allowedHosts: map[string]struct{}{"127.0.0.1": {}},
		// webpush deliberately nil
	}
	reg := store.ApproverPushTarget{
		ApproverPush: store.ApproverPush{
			DeviceID: "apd_1", Kind: "unifiedpush",
			TokenOrEndpoint: encodeKeyedEndpoint(srv.URL+"/up",
				base64.RawURLEncoding.EncodeToString(uaPub),
				base64.RawURLEncoding.EncodeToString(uaAuth)),
		},
		UserID: "u-1",
	}
	if err := pd.sendOne(reg, pushKindDecide, "apr-x", time.Time{}); err == nil {
		t.Fatal("keyed registration without a webpush sender must error")
	}
	select {
	case cp := <-got:
		t.Fatalf("bytes left the process despite the missing sender: %q", cp.body)
	case <-time.After(200 * time.Millisecond):
	}
}

// TestWebPushPrunesDeadRegistration: a 410 from the push service deletes the
// keyed registration row, exactly like the legacy lane.
func TestWebPushPrunesDeadRegistration(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	srv, _ := newCaptureServer(t, http.StatusGone)
	uaPub, uaAuth := testSubscriptionKeys(t)
	stored := encodeKeyedEndpoint(srv.URL+"/dead",
		base64.RawURLEncoding.EncodeToString(uaPub),
		base64.RawURLEncoding.EncodeToString(uaAuth))

	dev, err := h.st.Approvers().InsertDevice(ctx, store.ApproverDevice{UserID: "u-1", Name: "phone"})
	if err != nil {
		t.Fatalf("InsertDevice: %v", err)
	}
	if err := h.st.Approvers().UpsertPush(ctx, store.ApproverPush{DeviceID: dev.ID, Kind: "webpush", TokenOrEndpoint: stored}); err != nil {
		t.Fatalf("UpsertPush: %v", err)
	}
	pd := &pushDelivery{
		svc: h.svc, st: h.st, log: slog.Default(),
		httpc:        srv.Client(),
		allowedHosts: map[string]struct{}{"127.0.0.1": {}},
		webpush:      newTestWebPushSender(t, "", nil),
	}
	reg := store.ApproverPushTarget{
		ApproverPush: store.ApproverPush{DeviceID: dev.ID, Kind: "webpush", TokenOrEndpoint: stored},
		UserID:       "u-1",
	}
	if err := pd.sendOne(reg, pushKindStatus, "apr-410", time.Time{}); err == nil {
		t.Fatal("410 must surface as an error")
	}
	targets, err := h.st.Approvers().ListPushTargets(ctx)
	if err != nil {
		t.Fatalf("ListPushTargets: %v", err)
	}
	if len(targets) != 0 {
		t.Errorf("dead keyed registration not pruned on 410, remaining = %+v", targets)
	}
}

// TestPushTTL: decide pushes carry the approval's remaining window (ceil,
// clamped to [0, 24h]); status and unknown-expiry pushes carry the 24h
// default.
func TestPushTTL(t *testing.T) {
	h := newHarness(t)
	pd := &pushDelivery{svc: h.svc}
	now := h.svc.now()
	cases := []struct {
		name string
		kind string
		exp  time.Time
		min  int
		max  int
	}{
		{"decide with 90s window", pushKindDecide, now.Add(90 * time.Second), 85, 90},
		{"decide already expired", pushKindDecide, now.Add(-time.Minute), 0, 0},
		{"decide far-future clamps to 24h", pushKindDecide, now.Add(48 * time.Hour), 86400, 86400},
		{"decide without expiry defaults", pushKindDecide, time.Time{}, 86400, 86400},
		{"status ignores expiry", pushKindStatus, now.Add(90 * time.Second), 86400, 86400},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := pd.pushTTL(tc.kind, tc.exp); got < tc.min || got > tc.max {
				t.Errorf("pushTTL = %d, want in [%d, %d]", got, tc.min, tc.max)
			}
		})
	}
}

// --- registration-time validation (the helper RegisterPush must call) ---

func TestValidatePushRegistration(t *testing.T) {
	h := newHarness(t)

	// No push engine configured: endpoint-bearing kinds are refused (fail
	// closed), opaque token kinds pass (nothing to host-check).
	if _, err := h.svc.validatePushRegistration("unifiedpush", "https://ntfy.sh/x", "", ""); !errors.Is(err, ErrPushEndpointNotAllowed) {
		t.Errorf("unifiedpush without engine = %v, want ErrPushEndpointNotAllowed", err)
	}
	if stored, err := h.svc.validatePushRegistration("fcm", "opaque-token", "", ""); err != nil || stored != "opaque-token" {
		t.Errorf("fcm registration = (%q, %v), want the token stored verbatim", stored, err)
	}

	// With an allowlist: only https + allowlisted host passes.
	attachPush(t, h, "ntfy.sh")
	if stored, err := h.svc.validatePushRegistration("unifiedpush", "https://ntfy.sh/topic", "", ""); err != nil || stored != "https://ntfy.sh/topic" {
		t.Errorf("allowlisted unifiedpush = (%q, %v), want the endpoint stored verbatim", stored, err)
	}
	if _, err := h.svc.validatePushRegistration("unifiedpush", "https://evil.example/x", "", ""); !errors.Is(err, ErrPushEndpointNotAllowed) {
		t.Errorf("disallowed host = %v, want ErrPushEndpointNotAllowed", err)
	}
	if _, err := h.svc.validatePushRegistration("unifiedpush", "http://ntfy.sh/x", "", ""); !errors.Is(err, ErrPushEndpointNotAllowed) {
		t.Errorf("non-https = %v, want ErrPushEndpointNotAllowed", err)
	}
}

// TestPushEndpointRefusalMessages pins what the phone actually renders on a
// refused endpoint (the API maps ErrPushEndpointNotAllowed to a 400 whose
// body is err.Error(), shown verbatim): the refused HOST is named, the two
// operator-distinct states read differently (empty allowlist vs host not
// listed), and (the security pin) no variant ever leaks the endpoint's
// path or query, which carry per-subscription capability tokens.
func TestPushEndpointRefusalMessages(t *testing.T) {
	const secret = "SECRET-CAPABILITY-TOKEN"
	endpoint := "https://ntfy.example/up/" + secret + "?auth=" + secret

	cases := []struct {
		name     string
		pd       *pushDelivery // nil = no push engine at all
		endpoint string
		contains []string
		omits    []string
	}{
		{"no engine reads as empty allowlist", nil, endpoint,
			[]string{`host "ntfy.example"`, "allowedPushHosts is empty"},
			[]string{secret, "/up/"}},
		{"empty allowlist names the state", &pushDelivery{allowedHosts: map[string]struct{}{}}, endpoint,
			[]string{`host "ntfy.example"`, "allowedPushHosts is empty"},
			[]string{secret, "/up/"}},
		{"host not listed names the host", &pushDelivery{allowedHosts: map[string]struct{}{"other.example": {}}}, endpoint,
			[]string{`host "ntfy.example"`, "is not in approval.push.allowedPushHosts"},
			[]string{secret, "/up/", "is empty"}},
		{"non-https names the scheme rule, not a host", &pushDelivery{allowedHosts: map[string]struct{}{"ntfy.example": {}}},
			"http://ntfy.example/up/" + secret,
			[]string{"must be a valid https URL"},
			[]string{secret, "/up/", "ntfy.example"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.pd.endpointNotAllowed(tc.endpoint)
			if !errors.Is(err, ErrPushEndpointNotAllowed) {
				t.Fatalf("err = %v, want it to wrap ErrPushEndpointNotAllowed", err)
			}
			msg := err.Error()
			for _, want := range tc.contains {
				if !strings.Contains(msg, want) {
					t.Errorf("message %q should contain %q", msg, want)
				}
			}
			for _, banned := range tc.omits {
				if strings.Contains(msg, banned) {
					t.Errorf("message %q leaks %q", msg, banned)
				}
			}
		})
	}

	// The registration path carries the same detail end to end (the shape the
	// phone sees): engine with a configured allowlist, host off-list.
	h := newHarness(t)
	attachPush(t, h, "ntfy.sh")
	_, err := h.svc.validatePushRegistration("unifiedpush", endpoint, "", "")
	if !errors.Is(err, ErrPushEndpointNotAllowed) {
		t.Fatalf("off-list registration = %v, want ErrPushEndpointNotAllowed", err)
	}
	if msg := err.Error(); !strings.Contains(msg, `host "ntfy.example"`) || strings.Contains(msg, secret) {
		t.Errorf("registration refusal %q must name the host and never the endpoint's path/query", msg)
	}
}

// TestValidatePushRegistrationKeys is the subscription-key rule table:
// webpush requires the pair, unifiedpush treats it as the encrypted-lane
// opt-in, fcm/apns refuse it, halves and malformed material are refused, and
// any keyed registration is refused until the WebPush sender is configured.
func TestValidatePushRegistrationKeys(t *testing.T) {
	h := newHarness(t)
	rec := attachPush(t, h, "ntfy.sh", "*.notify.windows.com")
	_ = rec
	uaPub, uaAuth := testSubscriptionKeys(t)
	goodP := base64.RawURLEncoding.EncodeToString(uaPub)
	goodA := base64.RawURLEncoding.EncodeToString(uaAuth)

	// Keys before the WebPush sender exists: refused with the fix named.
	if _, err := h.svc.validatePushRegistration("webpush", "https://ntfy.sh/sub", goodP, goodA); !errors.Is(err, ErrBadPushRegistration) {
		t.Fatalf("keyed registration without webpush sender = %v, want ErrBadPushRegistration", err)
	}

	h.svc.push.webpush = newTestWebPushSender(t, "", nil)
	cases := []struct {
		name       string
		kind, ep   string
		p256dh     string
		auth       string
		wantStored string
		wantErr    error
	}{
		{"webpush with keys stores canonical form", "webpush", "https://ntfy.sh/sub", goodP, goodA,
			"https://ntfy.sh/sub#p256dh=" + goodP + "&auth=" + goodA, nil},
		{"unifiedpush with keys opts in to encryption", "unifiedpush", "https://ntfy.sh/up?up=1", goodP, goodA,
			"https://ntfy.sh/up?up=1#p256dh=" + goodP + "&auth=" + goodA, nil},
		{"unifiedpush without keys stays legacy", "unifiedpush", "https://ntfy.sh/topic", "", "",
			"https://ntfy.sh/topic", nil},
		{"wildcard allowlist admits WNS subdomains", "webpush", "https://par02p.notify.windows.com/w/x", goodP, goodA,
			"https://par02p.notify.windows.com/w/x#p256dh=" + goodP + "&auth=" + goodA, nil},
		{"webpush without keys refused", "webpush", "https://ntfy.sh/sub", "", "", "", ErrBadPushRegistration},
		{"p256dh without auth refused", "unifiedpush", "https://ntfy.sh/t", goodP, "", "", ErrBadPushRegistration},
		{"auth without p256dh refused", "unifiedpush", "https://ntfy.sh/t", "", goodA, "", ErrBadPushRegistration},
		{"fcm with keys refused", "fcm", "opaque-token", goodP, goodA, "", ErrBadPushRegistration},
		{"apns with keys refused", "apns", "opaque-token", goodP, goodA, "", ErrBadPushRegistration},
		{"padded p256dh refused", "webpush", "https://ntfy.sh/sub", goodP + "=", goodA, "", ErrBadPushRegistration},
		{"short auth refused", "webpush", "https://ntfy.sh/sub", goodP,
			base64.RawURLEncoding.EncodeToString(uaAuth[:15]), "", ErrBadPushRegistration},
		{"off-curve p256dh refused", "webpush", "https://ntfy.sh/sub",
			base64.RawURLEncoding.EncodeToString(append([]byte{0x04}, make([]byte, 64)...)), goodA, "", ErrBadPushRegistration},
		{"fragment-bearing endpoint refused", "webpush", "https://ntfy.sh/sub#frag", goodP, goodA, "", ErrBadPushRegistration},
		{"fragment-bearing legacy endpoint refused", "unifiedpush", "https://ntfy.sh/t#frag", "", "", "", ErrBadPushRegistration},
		{"keyed endpoint off-allowlist refused", "webpush", "https://evil.example/sub", goodP, goodA, "", ErrPushEndpointNotAllowed},
		{"wildcard does not admit the bare suffix", "webpush", "https://notify.windows.com/w/x", goodP, goodA, "", ErrPushEndpointNotAllowed},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			stored, err := h.svc.validatePushRegistration(tc.kind, tc.ep, tc.p256dh, tc.auth)
			if tc.wantErr != nil {
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("err = %v, want %v", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("err = %v, want nil", err)
			}
			if stored != tc.wantStored {
				t.Errorf("stored = %q, want %q", stored, tc.wantStored)
			}
		})
	}
}
