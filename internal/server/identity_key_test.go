package server

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/strazahq/straza/internal/dpop"
	"github.com/strazahq/straza/internal/store"
)

// nonceMsg is the error of the nonce challenge.
const nonceMsg = "Straza needs a fresh proof nonce. Sign the request again with the nonce in the DPoP-Nonce header."

// keyHolder is a test key holder: one Ed25519 key, the last nonce strazad
// sent, the device it enrolled and the counter of its last check-in.
type keyHolder struct {
	t       *testing.T
	base    string
	priv    ed25519.PrivateKey
	nonce   string
	device  string
	counter int64
}

func newKeyHolder(t *testing.T, base string) *keyHolder {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return &keyHolder{t: t, base: base, priv: priv}
}

func (h *keyHolder) thumbprint() string { return dpop.Thumbprint(h.priv.Public().(ed25519.PublicKey)) }

// proof signs a proof for POST path with the cached nonce: a check-in proof
// that signs ctr when ctr is above 0, else a plain proof over token.
func (h *keyHolder) proof(path, token string, ctr int64) string {
	h.t.Helper()
	var p string
	var err error
	if ctr > 0 {
		p, err = dpop.NewCheckinProof(h.priv, h.base+path, h.nonce, ctr, time.Now())
	} else {
		p, err = dpop.NewProof(h.priv, http.MethodPost, h.base+path, h.nonce, token, time.Now())
	}
	if err != nil {
		h.t.Fatal(err)
	}
	return p
}

// proofJTI signs a proof for POST path with the cached nonce as proof does,
// with jti as its proof id, so a test can send an id the client package
// never makes.
func (h *keyHolder) proofJTI(path, jti string, ctr int64) string {
	h.t.Helper()
	pub := h.priv.Public().(ed25519.PublicKey)
	claims := map[string]any{"htm": http.MethodPost, "htu": h.base + path, "iat": time.Now().Unix(), "jti": jti, "nonce": h.nonce}
	if ctr > 0 {
		claims["ctr"] = ctr
	}
	segment := func(v any) string {
		b, err := json.Marshal(v)
		if err != nil {
			h.t.Fatal(err)
		}
		return base64.RawURLEncoding.EncodeToString(b)
	}
	input := segment(map[string]any{"typ": dpop.ProofType, "alg": "EdDSA",
		"jwk": map[string]string{"kty": "OKP", "crv": "Ed25519", "x": base64.RawURLEncoding.EncodeToString(pub)}}) +
		"." + segment(claims)
	return input + "." + base64.RawURLEncoding.EncodeToString(ed25519.Sign(h.priv, []byte(input)))
}

// bodyCounter returns the counter of a test body, 0 when it has none.
func bodyCounter(body any) int64 {
	m, _ := body.(map[string]any)
	switch n := m["counter"].(type) {
	case int:
		return int64(n)
	case int64:
		return n
	}
	return 0
}

// post posts body to path with proof in the DPoP header, none when proof is
// empty, and caches the nonce of the answer.
func (h *keyHolder) post(path, proof string, body any) (int, http.Header, map[string]any) {
	h.t.Helper()
	raw, _ := json.Marshal(body)
	req, _ := http.NewRequest(http.MethodPost, h.base+path, bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	if proof != "" {
		req.Header.Set(dpop.HeaderProof, proof)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		h.t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if n := resp.Header.Get(dpop.HeaderNonce); n != "" {
		h.nonce = n
	}
	var out map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		h.t.Fatalf("non-JSON from %s: %v", path, err)
	}
	return resp.StatusCode, resp.Header, out
}

// send posts body to path once with a fresh proof over token, which signs
// the body's counter when it has one.
func (h *keyHolder) send(path, token string, body any) (int, http.Header, map[string]any) {
	h.t.Helper()
	return h.post(path, h.proof(path, token, bodyCounter(body)), body)
}

// call is send with the one retry RFC 9449 prescribes after a nonce
// challenge.
func (h *keyHolder) call(path, token string, body any) (int, map[string]any) {
	h.t.Helper()
	code, hdr, out := h.send(path, token, body)
	if code == http.StatusUnauthorized && strings.Contains(hdr.Get("WWW-Authenticate"), "use_dpop_nonce") {
		code, _, out = h.send(path, token, body)
	}
	return code, out
}

// keyEnrollBody is the body of a key enrollment of fingerprint fp.
func keyEnrollBody(idToken, fp string) map[string]any {
	return map[string]any{
		"id_token":        idToken,
		"device":          map[string]string{"name": "kim-laptop", "platform": "linux", "fingerprint": fp},
		"custody":         map[string]string{"kind": "account", "platform": "linux/amd64", "version": "0.1.0"},
		"machine_binding": "hmac-of-the-machine-id",
		"counter":         0,
	}
}

// enroll enrolls the holder's key for the ID token's user with fingerprint
// fp and starts its counter over.
func (h *keyHolder) enroll(idToken, fp string) map[string]any {
	h.t.Helper()
	code, out := h.call("/v1/enroll", "", keyEnrollBody(idToken, fp))
	if code != http.StatusOK {
		h.t.Fatalf("key enroll = %d %v", code, out)
	}
	h.device, h.counter = out["device_id"].(string), 0
	return out
}

// checkinBody is the body of a key check-in with counter n.
func (h *keyHolder) checkinBody(n int64) map[string]any {
	return map[string]any{
		"device_id":   h.device,
		"counter":     n,
		"harness":     map[string]string{"name": "claude-code", "version": "2.1.0"},
		"attestation": map[string]any{"managed": false, "hashes": map[string]string{"self": "sha256:x"}},
	}
}

// checkin runs a key check-in with the next counter.
func (h *keyHolder) checkin() (int, map[string]any) {
	h.t.Helper()
	h.counter++
	return h.call("/v1/checkin", "", h.checkinBody(h.counter))
}

// mustCheckin runs a key check-in that must start a session and returns its
// token and session id.
func (h *keyHolder) mustCheckin() (string, string) {
	h.t.Helper()
	code, out := h.checkin()
	if code != http.StatusOK {
		h.t.Fatalf("key checkin = %d %v", code, out)
	}
	return out["session_token"].(string), out["session_id"].(string)
}

// deviceRow reads a device row the test needs to exist.
func deviceRow(t *testing.T, app *App, id string) store.Device {
	t.Helper()
	d, err := app.store.Devices().GetByID(context.Background(), id)
	if err != nil {
		t.Fatalf("device %s: %v", id, err)
	}
	return d
}

// signInAgo moves the device's last sign-in back by ago with a store write.
func signInAgo(t *testing.T, app *App, id string, ago time.Duration) {
	t.Helper()
	d := deviceRow(t, app, id)
	d.SignedInAt = time.Now().Add(-ago).UTC()
	if _, err := app.store.Devices().Update(context.Background(), d); err != nil {
		t.Fatal(err)
	}
}

// matching returns the payloads of the outbox events of subject that
// satisfy pred.
func matching(t *testing.T, app *App, subject string, pred func(map[string]any) bool) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, d := range outboxDataFor(t, app, subject) {
		if pred(d) {
			out = append(out, d)
		}
	}
	return out
}

// TestKeyEnrollAnswersNonceThenEnrolls pins the key lane of enroll: a proof
// without a nonce gets the nonce challenge and writes nothing, and the retry
// binds the proof key to a new device row and answers its thumbprint and the
// sign-in deadline, with no device token.
func TestKeyEnrollAnswersNonceThenEnrolls(t *testing.T) {
	t.Parallel()
	app, base := testApp(t)
	kim := seedIdentity(t, app)
	idToken := loginDeviceFlow(t, base, "kim", "hunter2!")
	h := newKeyHolder(t, base)

	code, hdr, out := h.send("/v1/enroll", "", keyEnrollBody(idToken, "sha256:k1"))
	if code != http.StatusUnauthorized || out["error"] != nonceMsg ||
		!strings.Contains(hdr.Get("WWW-Authenticate"), `error="use_dpop_nonce"`) || hdr.Get(dpop.HeaderNonce) == "" {
		t.Fatalf("first enroll = %d %v %v, want the nonce challenge", code, hdr, out)
	}
	if devs, _ := app.store.Devices().ListByUser(context.Background(), kim.ID); len(devs) != 0 {
		t.Fatalf("the nonce challenge wrote devices %+v", devs)
	}

	code, _, out = h.send("/v1/enroll", "", keyEnrollBody(idToken, "sha256:k1"))
	if code != http.StatusOK {
		t.Fatalf("enroll with the nonce = %d %v", code, out)
	}
	// The deadline counts from the sign-in, a moment before the enrollment.
	left, _ := out["sign_in_expires_in"].(float64)
	if out["user_id"] != kim.ID || out["username"] != "kim" || out["key_thumbprint"] != h.thumbprint() ||
		left > 7*24*3600 || left < 7*24*3600-60 {
		t.Errorf("enroll answer = %v", out)
	}
	for _, field := range []string{"device_token", "device_token_expires_in"} {
		if _, ok := out[field]; ok {
			t.Errorf("key enroll answered %s: %v", field, out)
		}
	}
	d := deviceRow(t, app, out["device_id"].(string))
	var custody map[string]string
	_ = json.Unmarshal([]byte(d.Custody), &custody)
	if d.UserID != kim.ID || d.ClientKind != store.DeviceClientKit || d.KeyThumbprint != h.thumbprint() ||
		d.KeyJWK != dpop.PublicJWK(h.priv.Public().(ed25519.PublicKey)) || d.MachineBinding != "hmac-of-the-machine-id" ||
		d.KeyCounter != 0 || custody["kind"] != "account" || custody["platform"] != "linux/amd64" || custody["version"] != "0.1.0" {
		t.Errorf("device row = %+v", d)
	}
	if since := time.Since(d.SignedInAt); since < 0 || since > time.Minute {
		t.Errorf("signed_in_at = %s, want now", d.SignedInAt)
	}
	if n := len(outboxDataFor(t, app, "straza.audit.authn")); n != 0 {
		t.Errorf("authn records = %d, want none for an enrollment and its nonce challenge", n)
	}
	if n := len(matching(t, app, "straza.identity.updated", func(e map[string]any) bool { return e["action"] == "enroll" })); n != 1 {
		t.Errorf("enroll events = %d, want 1", n)
	}
}

// TestKeyEnrollRefusals pins the answers to a key enrollment whose body or
// proof is wrong: a 400 for each body field, and a 401 for a proof that
// fails, none of which registers a device or writes an audit record.
func TestKeyEnrollRefusals(t *testing.T) {
	t.Parallel()
	app, base := testApp(t)
	kim := seedIdentity(t, app)
	idToken := loginDeviceFlow(t, base, "kim", "hunter2!")
	long := strings.Repeat("x", 65)

	cases := []struct {
		name    string
		edit    func(body map[string]any)
		proof   func(h *keyHolder) string
		code    int
		errHead string
	}{
		{"a human client", func(b map[string]any) { b["client_kind"] = "human" }, nil, http.StatusBadRequest,
			"a key enrollment is for the Straza client only, so client_kind must be kit or left out"},
		{"an unknown custody kind", func(b map[string]any) { b["custody"] = map[string]string{"kind": "root"} }, nil,
			http.StatusBadRequest, "custody.kind must be account or user"},
		{"a long custody platform", func(b map[string]any) { b["custody"] = map[string]string{"kind": "user", "platform": long} }, nil,
			http.StatusBadRequest, "custody.platform and custody.version must each be at most 64 bytes"},
		{"no machine binding", func(b map[string]any) { delete(b, "machine_binding") }, nil,
			http.StatusBadRequest, "machine_binding is required and must be at most 128 bytes"},
		{"a long machine binding", func(b map[string]any) { b["machine_binding"] = strings.Repeat("x", 129) }, nil,
			http.StatusBadRequest, "machine_binding is required and must be at most 128 bytes"},
		{"a counter above zero", func(b map[string]any) { b["counter"] = 1 }, nil,
			http.StatusBadRequest, "counter must be 0 on a key enrollment, because a new key has not checked in yet"},
		{"a proof for another path", nil, func(h *keyHolder) string { return h.proof("/v1/checkin", "", 0) },
			http.StatusUnauthorized, "the DPoP proof was not accepted: "},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newKeyHolder(t, base)
			h.send("/v1/enroll", "", keyEnrollBody(idToken, "sha256:nonce")) // the nonce challenge
			body := keyEnrollBody(idToken, "sha256:refused")
			if tc.edit != nil {
				tc.edit(body)
			}
			proof := h.proof("/v1/enroll", "", 0)
			if tc.proof != nil {
				proof = tc.proof(h)
			}
			code, _, out := h.post("/v1/enroll", proof, body)
			msg, _ := out["error"].(string)
			if code != tc.code || !strings.HasPrefix(msg, tc.errHead) {
				t.Fatalf("enroll = %d %v, want %d %q", code, out, tc.code, tc.errHead)
			}
			if tc.code == http.StatusUnauthorized && !strings.HasSuffix(msg, ". Sign a fresh proof with the key you are enrolling.") {
				t.Errorf("proof refusal %q does not say what to do", msg)
			}
		})
	}
	if devs, _ := app.store.Devices().ListByUser(context.Background(), kim.ID); len(devs) != 0 {
		t.Errorf("refused enrollments wrote devices %+v", devs)
	}
	if n := len(outboxDataFor(t, app, "straza.audit.authn")); n != 0 {
		t.Errorf("authn records = %d, want none", n)
	}
	if n := len(matching(t, app, "straza.identity.updated", func(e map[string]any) bool { return e["action"] == "enroll" })); n != 0 {
		t.Errorf("enroll events = %d, want none", n)
	}
}

// TestKeyEnrollRefusesNULCharacter pins the refusal of a key enrollment
// whose device name, platform or fingerprint or machine binding holds a NUL
// character: 400 with a sentence that names the field, before the proof is
// checked, so the proof stays unspent, no device row or counter changes and
// no record is written, as the other body refusals do. The test store is
// SQLite, which would keep the character, so the refusal does not depend on
// the store. The custody fields are stored as JSON text, which escapes the
// character, so a NUL there still enrolls.
func TestKeyEnrollRefusesNULCharacter(t *testing.T) {
	t.Parallel()
	app, base := testApp(t)
	kim := seedIdentity(t, app)
	ctx := context.Background()
	idToken := loginDeviceFlow(t, base, "kim", "hunter2!")
	// A device enrolled with the fingerprint the refused bodies carry and
	// checked in once, so a write would enroll it again and reset its counter.
	first := newKeyHolder(t, base)
	first.enroll(idToken, "sha256:nul-same")
	first.mustCheckin()

	device := func(b map[string]any) map[string]string { return b["device"].(map[string]string) }
	cases := []struct {
		name  string
		field string // the field the refusal names, "" when the enrollment passes
		spoil func(map[string]any)
	}{
		{"device name", "device.name", func(b map[string]any) { device(b)["name"] += "\x00" }},
		{"device platform", "device.platform", func(b map[string]any) { device(b)["platform"] = "li\x00nux" }},
		{"device fingerprint", "device.fingerprint", func(b map[string]any) { device(b)["fingerprint"] = "\x00sha256:nul-same" }},
		{"machine binding", "machine_binding", func(b map[string]any) { b["machine_binding"] = "hmac\x00" }},
		{"custody platform", "", func(b map[string]any) { b["custody"].(map[string]string)["platform"] = "linux\x00/amd64" }},
	}
	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newKeyHolder(t, base)
			h.send("/v1/enroll", "", keyEnrollBody(idToken, "sha256:nonce")) // the nonce challenge
			before, err := app.store.Devices().ListByUser(ctx, kim.ID)
			if err != nil {
				t.Fatal(err)
			}
			head, err := app.store.Outbox().Head(ctx)
			if err != nil {
				t.Fatal(err)
			}
			body := keyEnrollBody(idToken, "sha256:nul-same")
			tc.spoil(body)
			proof := h.proof("/v1/enroll", "", 0)
			code, _, out := h.post("/v1/enroll", proof, body)
			if tc.field == "" {
				if code != http.StatusOK {
					t.Fatalf("enroll = %d %v, want 200", code, out)
				}
				if c := storedCustody(deviceRow(t, app, out["device_id"].(string)).Custody); c == nil || c.Platform != "linux\x00/amd64" {
					t.Errorf("stored custody = %+v, want the platform as sent", c)
				}
				return
			}
			if want := fmt.Sprintf(enrollNULMsg, tc.field); code != http.StatusBadRequest || out["error"] != want {
				t.Fatalf("enroll = %d %v, want 400 with %q", code, out, want)
			}
			if after, err := app.store.Devices().ListByUser(ctx, kim.ID); err != nil || !reflect.DeepEqual(after, before) {
				t.Fatalf("devices after the refusal = %+v (%v), want %+v", after, err, before)
			}
			if now, _ := app.store.Outbox().Head(ctx); now != head {
				t.Fatalf("the refused enrollment wrote an outbox row: head %q, was %q", now, head)
			}
			// The refusal came before the proof check, so the proof is
			// unspent and enrolls the same body without the character.
			code, _, out = h.post("/v1/enroll", proof, keyEnrollBody(idToken, fmt.Sprintf("sha256:nul-clean-%d", i)))
			if code != http.StatusOK {
				t.Fatalf("enroll with the same proof and no NUL = %d %v, want 200", code, out)
			}
		})
	}
}

// TestKeyReenrollReplacesKey pins a re-enrollment of the same fingerprint: it
// keeps the device id and replaces the key, the custody and the counter and
// restarts the sign-in deadline, so the new key starts at counter 1 and the
// old key is no longer the device's.
func TestKeyReenrollReplacesKey(t *testing.T) {
	t.Parallel()
	app, base := testApp(t)
	_ = seedIdentity(t, app)
	idToken := loginDeviceFlow(t, base, "kim", "hunter2!")

	first := newKeyHolder(t, base)
	first.enroll(idToken, "sha256:same")
	first.mustCheckin()
	first.mustCheckin()
	signInAgo(t, app, first.device, 48*time.Hour)

	second := newKeyHolder(t, base)
	body := keyEnrollBody(idToken, "sha256:same")
	body["custody"] = map[string]string{"kind": "user", "platform": "darwin/arm64", "version": "0.2.0"}
	body["machine_binding"] = "a-new-binding"
	code, out := second.call("/v1/enroll", "", body)
	if code != http.StatusOK || out["device_id"] != first.device || out["key_thumbprint"] != second.thumbprint() {
		t.Fatalf("re-enroll = %d %v, want device %s with the new key", code, out, first.device)
	}
	second.device = first.device
	d := deviceRow(t, app, first.device)
	if d.KeyThumbprint != second.thumbprint() || d.KeyCounter != 0 || d.MachineBinding != "a-new-binding" ||
		!strings.Contains(d.Custody, `"kind":"user"`) || time.Since(d.SignedInAt) > time.Minute {
		t.Errorf("re-enrolled row = %+v", d)
	}
	if code, out := second.checkin(); code != http.StatusOK {
		t.Errorf("new key at counter 1 = %d %v", code, out)
	}
	if code, out := first.checkin(); code != http.StatusForbidden || out["error"] != keyNotEnrolledMsg {
		t.Errorf("old key = %d %v, want 403 %q", code, out, keyNotEnrolledMsg)
	}
}

// TestKeyEnrollNeverConvertsABearerRow pins that a key enrollment never
// reuses a bearer row that strazactl or an older Straza client enrolled for
// the same fingerprint, while a key re-enrollment still reuses its own row.
func TestKeyEnrollNeverConvertsABearerRow(t *testing.T) {
	t.Parallel()
	app, base := testApp(t)
	kim := seedIdentity(t, app)
	idToken := loginDeviceFlow(t, base, "kim", "hunter2!")
	for _, kind := range []string{store.DeviceClientHuman, store.DeviceClientKit} {
		t.Run(kind, func(t *testing.T) {
			fp := "sha256:bearer-" + kind
			bearer, _ := mintDevice(t, app, kim.ID, kind, fp)
			h := newKeyHolder(t, base)
			keyID := h.enroll(idToken, fp)["device_id"].(string)
			if keyID == bearer.ID {
				t.Fatalf("the key enrollment took the bearer row %s", keyID)
			}
			h = newKeyHolder(t, base) // every key enrollment brings a new key
			if again := h.enroll(idToken, fp)["device_id"]; again != keyID {
				t.Errorf("key re-enroll = %v, want %s", again, keyID)
			}
			if d := deviceRow(t, app, bearer.ID); d.KeyThumbprint != "" || d.KeyJWK != "" {
				t.Errorf("bearer row carries a key: %+v", d)
			}
			if d := deviceRow(t, app, keyID); d.KeyThumbprint != h.thumbprint() {
				t.Errorf("keyed row = %+v", d)
			}
		})
	}
}

// TestPeriodWords pins how the deadline sentence renders signInMaxAge.
func TestPeriodWords(t *testing.T) {
	cases := []struct {
		in   time.Duration
		want string
	}{
		{168 * time.Hour, "7 days"},
		{24 * time.Hour, "1 day"},
		{36 * time.Hour, "36 hours"},
		{time.Hour, "1 hour"},
		{90 * time.Minute, "90 minutes"},
		{time.Minute, "1 minute"},
	}
	for _, tc := range cases {
		if got := periodWords(tc.in); got != tc.want {
			t.Errorf("periodWords(%s) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// TestKeyReenrollSameKeyRefused pins that every key enrollment brings a
// new key: enrolling a key this user already holds on any device answers
// 409 and changes nothing, so the device keeps checking in with its next
// counter. TestKeyReenrollReplacesKey pins the
// re-enrollment with a new key.
func TestKeyReenrollSameKeyRefused(t *testing.T) {
	t.Parallel()
	app, base := testApp(t)
	_ = seedIdentity(t, app)
	idToken := loginDeviceFlow(t, base, "kim", "hunter2!")
	const want = "This key is already enrolled for you. Make a new key and enroll again, because each enrollment needs a key of its own."

	for _, fp := range []string{"sha256:same-key", "sha256:another-machine"} {
		name := map[string]string{"sha256:same-key": "the same fingerprint", "sha256:another-machine": "another fingerprint"}[fp]
		t.Run(name, func(t *testing.T) {
			h := newKeyHolder(t, base)
			h.enroll(idToken, "sha256:same-key")
			h.mustCheckin()
			signInAgo(t, app, h.device, 48*time.Hour)
			before := deviceRow(t, app, h.device)
			authnBefore := len(outboxDataFor(t, app, "straza.audit.authn"))
			enrollsBefore := len(matching(t, app, "straza.identity.updated", func(e map[string]any) bool { return e["action"] == "enroll" }))

			code, out := h.call("/v1/enroll", "", keyEnrollBody(idToken, fp))
			if code != http.StatusConflict || out["error"] != want {
				t.Fatalf("same-key enroll = %d %v, want 409 %q", code, out, want)
			}
			if after := deviceRow(t, app, h.device); after.KeyCounter != before.KeyCounter || !after.SignedInAt.Equal(before.SignedInAt) ||
				after.KeyThumbprint != before.KeyThumbprint || after.Custody != before.Custody {
				t.Errorf("row after the refusal = %+v, want %+v", after, before)
			}
			if n := len(outboxDataFor(t, app, "straza.audit.authn")); n != authnBefore {
				t.Errorf("authn records = %d, want %d", n, authnBefore)
			}
			if n := len(matching(t, app, "straza.identity.updated", func(e map[string]any) bool { return e["action"] == "enroll" })); n != enrollsBefore {
				t.Errorf("enroll events = %d, want %d", n, enrollsBefore)
			}
			if code, out := h.checkin(); code != http.StatusOK {
				t.Errorf("check-in with the next counter = %d %v", code, out)
			}
		})
	}
}
