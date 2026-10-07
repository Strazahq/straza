package server

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/strazahq/straza/internal/automint"
	"github.com/strazahq/straza/internal/config"
	"github.com/strazahq/straza/internal/logging"
)

func genApproverKey(t *testing.T) (*ecdsa.PrivateKey, string) {
	t.Helper()
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKIXPublicKey(&priv.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	return priv, base64.StdEncoding.EncodeToString(der)
}

func signApprover(t *testing.T, priv *ecdsa.PrivateKey, requestID, verdict, challenge string, ts int64) string {
	t.Helper()
	msg := requestID + "\n" + verdict + "\n" + challenge + "\n" + strconv.FormatInt(ts, 10)
	d := sha256.Sum256([]byte(msg))
	sig, err := ecdsa.SignASN1(rand.Reader, priv, d[:])
	if err != nil {
		t.Fatal(err)
	}
	return base64.StdEncoding.EncodeToString(sig)
}

// selfSignedCertPEM mints a throwaway self-signed leaf (ECDSA P-256) and returns
// its cert + PKCS#8 key PEM. Used to exercise the enroll-QR TLS SPKI pin path.
func selfSignedCertPEM(t *testing.T) (certPEM, keyPEM []byte) {
	t.Helper()
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "straza.example.test"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames:              []string{"straza.example.test"},
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &priv.PublicKey, priv)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
		pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})
}

// spkiPinOf independently computes the "sha256/<b64>" SPKI pin of a leaf PEM:
// the reference the handler/helper output must match, computed a different way
// (parse the leaf, hash RawSubjectPublicKeyInfo) than production reads it.
func spkiPinOf(t *testing.T, certPEM []byte) string {
	t.Helper()
	block, _ := pem.Decode(certPEM)
	if block == nil {
		t.Fatal("no PEM block")
	}
	leaf, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(leaf.RawSubjectPublicKeyInfo)
	return "sha256/" + base64.StdEncoding.EncodeToString(sum[:])
}

// TestApproverSurfaceEndToEnd drives the mobile surface over HTTP: admin mints
// an enroll token, the device enrolls, non-approver tokens are refused, a
// signed decision resolves a record, replay is refused, and admin revocation
// (device-row delete) immediately fails the token.
func TestApproverSurfaceEndToEnd(t *testing.T) {
	t.Parallel()
	app, base := testApp(t)
	kim := seedIdentity(t, app) // kim holds role dev (⇒ reader)
	grantAdmin(t, app, kim.ID)
	adminTok, _ := checkinToken(t, app, base)

	// 1. Admin mints a one-time enroll token for kim (QR carries the same token).
	var mint struct {
		EnrollToken string `json:"enroll_token"`
		ExpiresIn   int    `json:"expires_in"`
		QR          struct {
			V       int      `json:"v"`
			Servers []string `json:"servers"`
			Token   string   `json:"token"`
		} `json:"qr"`
	}
	if code := adminReq(t, "POST", base+"/v1/admin/approvers/enroll-token", adminTok,
		map[string]any{"username": "kim"}, &mint); code != http.StatusOK {
		t.Fatalf("mint enroll-token = %d", code)
	}
	if mint.EnrollToken == "" || mint.QR.Token != mint.EnrollToken || mint.QR.V != 1 {
		t.Fatalf("enroll-token response = %+v", mint)
	}

	// 2. Enroll a hardware key (no auth; the token is the credential).
	priv, spki := genApproverKey(t)
	enrollBody := map[string]any{
		"enroll_token": mint.EnrollToken,
		"device": map[string]any{
			"name": "kim-pixel", "platform": "android", "key_alg": "ecdsa-p256",
			"public_key": spki, "key_security_level": "strongbox",
			"attestation": map[string]any{"kind": "none"},
		},
	}
	var enr struct {
		DeviceID    string `json:"approver_device_id"`
		DeviceToken string `json:"device_token"`
		ExpiresIn   int    `json:"expires_in"`
	}
	if code := adminReq(t, "POST", base+"/v1/approver/enroll", "", enrollBody, &enr); code != http.StatusCreated {
		t.Fatalf("enroll = %d", code)
	}
	if enr.DeviceToken == "" || enr.DeviceID == "" {
		t.Fatalf("enroll response = %+v", enr)
	}

	// 2b. The enroll token is one-time: re-enrolling fails.
	if code := adminReq(t, "POST", base+"/v1/approver/enroll", "", enrollBody, nil); code != http.StatusUnauthorized {
		t.Errorf("re-enroll with a spent token = %d, want 401", code)
	}

	// 3. Only a use=approver token opens the surface; every other credential 401s.
	deviceTok, err := app.tokens.MintDeviceToken(kim.ID, "dev-x", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	for _, bad := range []struct {
		name  string
		token string
	}{
		{"no bearer", ""},
		{"session token", adminTok},
		{"device token", deviceTok},
	} {
		if code := adminReq(t, "GET", base+"/v1/approver/pending", bad.token, nil, nil); code != http.StatusUnauthorized {
			t.Errorf("pending with %s = %d, want 401", bad.name, code)
		}
	}

	// 4. Decidable → sign → decide. kim holds dev, the record needs dev.
	rec := seedApproval(t, app, "u-other", []string{"dev"})
	var rows []approverRow
	if code := adminReq(t, "GET", base+"/v1/approver/pending?scope=decidable", enr.DeviceToken, nil, &rows); code != http.StatusOK {
		t.Fatalf("pending decidable = %d", code)
	}
	var challenge string
	for _, row := range rows {
		if row.ID == rec.ID {
			challenge = row.Challenge
		}
	}
	if challenge == "" {
		t.Fatalf("no decidable row with a challenge for %s: %+v", rec.ID, rows)
	}

	ts := time.Now().Unix()
	decideBody := map[string]any{
		"request_id": rec.ID, "verdict": "approve", "challenge": challenge,
		"signature": signApprover(t, priv, rec.ID, "approve", challenge, ts), "ts": ts,
	}
	var dec struct {
		State string `json:"state"`
	}
	if code := adminReq(t, "POST", base+"/v1/approver/decide", enr.DeviceToken, decideBody, &dec); code != http.StatusOK {
		t.Fatalf("decide = %d", code)
	}
	if dec.State != "approved" {
		t.Errorf("decide state = %q, want approved", dec.State)
	}

	// Replay of the exact signed request: challenge already consumed → 401.
	if code := adminReq(t, "POST", base+"/v1/approver/decide", enr.DeviceToken, decideBody, nil); code != http.StatusUnauthorized {
		t.Errorf("replay decide = %d, want 401", code)
	}

	// 5. History shows the resolved record (kim is eligible by role). The feed is
	// now an {items,next_cursor} envelope; one row fits a page, so next_cursor is
	// empty (no further pages).
	var hist historyEnvelope
	if code := adminReq(t, "GET", base+"/v1/approver/history", enr.DeviceToken, nil, &hist); code != http.StatusOK {
		t.Fatalf("history = %d", code)
	}
	if len(hist.Items) != 1 || hist.Items[0].State != "approved" {
		t.Errorf("history = %+v, want 1 approved", hist)
	}
	if hist.NextCursor != "" {
		t.Errorf("single-page history must have an empty next_cursor, got %q", hist.NextCursor)
	}

	// 6. Admin revokes the device → the token now fails (row-backed).
	if code := adminReq(t, "DELETE", base+"/v1/admin/approvers/"+enr.DeviceID, adminTok, nil, nil); code != http.StatusOK {
		t.Fatalf("revoke device = %d", code)
	}
	if code := adminReq(t, "GET", base+"/v1/approver/pending", enr.DeviceToken, nil, nil); code != http.StatusUnauthorized {
		t.Errorf("pending after revoke = %d, want 401", code)
	}
}

// mintTokenResponse is the enroll-token mint envelope the console/CLI consume.
type mintTokenResponse struct {
	EnrollToken string   `json:"enroll_token"`
	ExpiresIn   int      `json:"expires_in"`
	Servers     []string `json:"servers"`
	TLSSPKIPin  string   `json:"tls_spki_pin"`
	QRPayload   string   `json:"qr_payload"`
	Project     struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	} `json:"project"`
}

// TestEnrollTokenQRPayloadPlaintext pins the plaintext/ingress-terminated shape:
// no tls_spki_pin field, servers is exactly [PublicURL], and qr_payload is a
// compact JSON string with NO "pin" key that round-trips to the minted token.
func TestEnrollTokenQRPayloadPlaintext(t *testing.T) {
	t.Parallel()
	app, base := testApp(t)
	kim := seedIdentity(t, app)
	grantAdmin(t, app, kim.ID)
	adminTok, _ := checkinToken(t, app, base)

	var resp mintTokenResponse
	if code := adminReq(t, "POST", base+"/v1/admin/approvers/enroll-token", adminTok,
		map[string]any{"username": "kim"}, &resp); code != http.StatusOK {
		t.Fatalf("mint enroll-token = %d", code)
	}

	if resp.TLSSPKIPin != "" {
		t.Errorf("plaintext deployment must not carry a tls_spki_pin, got %q", resp.TLSSPKIPin)
	}
	if len(resp.Servers) != 1 || resp.Servers[0] != base {
		t.Errorf("servers = %v, want [%s]", resp.Servers, base)
	}

	// The QR payload must omit the "pin" KEY entirely (not emit an empty one).
	var raw map[string]json.RawMessage
	if err := json.Unmarshal([]byte(resp.QRPayload), &raw); err != nil {
		t.Fatalf("qr_payload is not valid JSON: %v (%q)", err, resp.QRPayload)
	}
	if _, ok := raw["pin"]; ok {
		t.Errorf("qr_payload must omit the pin key when there is no TLS pin: %s", resp.QRPayload)
	}

	// And it round-trips to the contract shape, project included.
	var qp struct {
		V       int      `json:"v"`
		Servers []string `json:"servers"`
		Token   string   `json:"token"`
		Project struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"project"`
	}
	if err := json.Unmarshal([]byte(resp.QRPayload), &qp); err != nil {
		t.Fatalf("qr_payload round-trip: %v", err)
	}
	if qp.V != 1 || qp.Token != resp.EnrollToken || len(qp.Servers) != 1 || qp.Servers[0] != base {
		t.Errorf("qr_payload = %+v, want {v:1 servers:[%s] token:%s}", qp, base, resp.EnrollToken)
	}
	if !strings.HasPrefix(qp.Project.ID, "prj_") || qp.Project.Name == "" {
		t.Errorf("qr_payload project = %+v, want prj_-prefixed id + non-empty name", qp.Project)
	}
	if qp.Project != resp.Project {
		t.Errorf("qr project %+v != response project %+v (one composition, two values)", qp.Project, resp.Project)
	}
}

// TestEnrollPayloadCarriesFCM drives the BYO-Firebase enroll delta over HTTP:
// with the
// deployment's public Firebase app config present (server-side sender
// deliberately still disabled), the mint's qr_payload AND the enroll 201
// response both carry the fcm object under the contract's exact names; an
// unconfigured deployment omits the fcm key entirely on both surfaces (the
// app's signal that UnifiedPush/ntfy stays the lane), never an empty object.
func TestEnrollPayloadCarriesFCM(t *testing.T) {
	t.Parallel()
	const (
		projectID = "acme-approver"
		appID     = "1:407:android:ab12cd"
		apiKey    = "AIzaExampleKey"
		senderID  = "407"
	)
	enrollOnce := func(t *testing.T, app *App, base string) (qr, enroll map[string]json.RawMessage) {
		t.Helper()
		kim := seedIdentity(t, app)
		grantAdmin(t, app, kim.ID)
		adminTok, _ := checkinToken(t, app, base)
		var mint mintTokenResponse
		if code := adminReq(t, "POST", base+"/v1/admin/approvers/enroll-token", adminTok,
			map[string]any{"username": "kim"}, &mint); code != http.StatusOK {
			t.Fatalf("mint enroll-token = %d", code)
		}
		if err := json.Unmarshal([]byte(mint.QRPayload), &qr); err != nil {
			t.Fatalf("qr_payload is not valid JSON: %v (%q)", err, mint.QRPayload)
		}
		_, spki := genApproverKey(t)
		if code := adminReq(t, "POST", base+"/v1/approver/enroll", "", map[string]any{
			"enroll_token": mint.EnrollToken,
			"device": map[string]any{
				"name": "kim-pixel", "platform": "android", "key_alg": "ecdsa-p256",
				"public_key": spki, "key_security_level": "strongbox",
				"attestation": map[string]any{"kind": "none"},
			},
		}, &enroll); code != http.StatusCreated {
			t.Fatalf("enroll = %d", code)
		}
		return qr, enroll
	}

	t.Run("configured: both surfaces carry the contract object", func(t *testing.T) {
		app, base := testApp(t, func(cfg *config.Config) {
			cfg.Approval.Push.FCM.ProjectID = projectID
			cfg.Approval.Push.FCM.AppID = appID
			cfg.Approval.Push.FCM.APIKey = apiKey
			cfg.Approval.Push.FCM.SenderID = senderID
		})
		qr, enroll := enrollOnce(t, app, base)
		want := map[string]string{
			"project_id": projectID, "app_id": appID, "api_key": apiKey, "sender_id": senderID,
		}
		for surface, raw := range map[string]json.RawMessage{"qr_payload": qr["fcm"], "enroll 201": enroll["fcm"]} {
			if raw == nil {
				t.Fatalf("%s carries no fcm object", surface)
			}
			var got map[string]string
			if err := json.Unmarshal(raw, &got); err != nil {
				t.Fatalf("%s fcm: %v", surface, err)
			}
			if len(got) != len(want) {
				t.Errorf("%s fcm = %s, want exactly the four contract fields", surface, raw)
			}
			for k, v := range want {
				if got[k] != v {
					t.Errorf("%s fcm.%s = %q, want %q", surface, k, got[k], v)
				}
			}
		}
	})

	t.Run("unconfigured: the fcm key is absent on both surfaces", func(t *testing.T) {
		app, base := testApp(t)
		qr, enroll := enrollOnce(t, app, base)
		if _, ok := qr["fcm"]; ok {
			t.Errorf("qr_payload must omit the fcm key without app config: %s", qr["fcm"])
		}
		if _, ok := enroll["fcm"]; ok {
			t.Errorf("enroll response must omit the fcm key without app config: %s", enroll["fcm"])
		}
	})
}

// TestEnrollPayloadCarriesWebPush drives the 0.42.0 QR-side VAPID advert: with
// the WebPush lane configured, the mint's qr_payload, its structured qr twin,
// AND the enroll 201 all carry the same webpush object as the sender's own key
// (one helper, three surfaces, so they cannot disagree); an unconfigured
// deployment omits the webpush key entirely everywhere (the absent-key
// contract), and the keyless payload's BYTES are pinned identical to the
// pre-0.42.0 form: the advert is additive, v stays 1.
func TestEnrollPayloadCarriesWebPush(t *testing.T) {
	t.Parallel()
	mintAndEnroll := func(t *testing.T, app *App, base string) (mint map[string]json.RawMessage, qr, enroll map[string]json.RawMessage, resp mintTokenResponse) {
		t.Helper()
		kim := seedIdentity(t, app)
		grantAdmin(t, app, kim.ID)
		adminTok, _ := checkinToken(t, app, base)
		if code := adminReq(t, "POST", base+"/v1/admin/approvers/enroll-token", adminTok,
			map[string]any{"username": "kim"}, &mint); code != http.StatusOK {
			t.Fatalf("mint enroll-token = %d", code)
		}
		// Typed view of the same body, field by field from the raw map.
		for field, into := range map[string]any{
			"enroll_token": &resp.EnrollToken, "qr_payload": &resp.QRPayload, "project": &resp.Project,
		} {
			if err := json.Unmarshal(mint[field], into); err != nil {
				t.Fatalf("mint %s: %v", field, err)
			}
		}
		if err := json.Unmarshal([]byte(resp.QRPayload), &qr); err != nil {
			t.Fatalf("qr_payload is not valid JSON: %v (%q)", err, resp.QRPayload)
		}
		_, spki := genApproverKey(t)
		if code := adminReq(t, "POST", base+"/v1/approver/enroll", "", map[string]any{
			"enroll_token": resp.EnrollToken,
			"device": map[string]any{
				"name": "kim-pixel", "platform": "android", "key_alg": "ecdsa-p256",
				"public_key": spki, "key_security_level": "strongbox",
				"attestation": map[string]any{"kind": "none"},
			},
		}, &enroll); code != http.StatusCreated {
			t.Fatalf("enroll = %d", code)
		}
		return mint, qr, enroll, resp
	}

	t.Run("configured: qr_payload, qr twin and enroll 201 carry the sender's key", func(t *testing.T) {
		app, base := testApp(t, func(cfg *config.Config) {
			cfg.Approval.Push.WebPush.VAPIDKeyFile = filepath.Join(t.TempDir(), "vapid.pem")
			cfg.Approval.Push.WebPush.Contact = "mailto:ops@example.com"
		})
		mint, qr, enroll, _ := mintAndEnroll(t, app, base)
		senderKey := app.approval.WebPushVAPIDPublicKey()
		if kb, err := base64.RawURLEncoding.DecodeString(senderKey); err != nil || len(kb) != 65 || kb[0] != 0x04 {
			t.Fatalf("sender key = %q, want base64url of a 65-octet uncompressed point (err=%v)", senderKey, err)
		}
		var qrTwin struct {
			Webpush *struct {
				VAPIDPublicKey string `json:"vapid_public_key"`
			} `json:"webpush"`
		}
		if err := json.Unmarshal(mint["qr"], &qrTwin); err != nil {
			t.Fatalf("qr twin: %v", err)
		}
		if qrTwin.Webpush == nil || qrTwin.Webpush.VAPIDPublicKey != senderKey {
			t.Errorf("qr twin webpush = %+v, want the sender's key %q", qrTwin.Webpush, senderKey)
		}
		for surface, raw := range map[string]json.RawMessage{"qr_payload": qr["webpush"], "enroll 201": enroll["webpush"]} {
			if raw == nil {
				t.Fatalf("%s carries no webpush object", surface)
			}
			var got map[string]string
			if err := json.Unmarshal(raw, &got); err != nil {
				t.Fatalf("%s webpush: %v", surface, err)
			}
			if len(got) != 1 || got["vapid_public_key"] != senderKey {
				t.Errorf("%s webpush = %s, want exactly {vapid_public_key:%q}", surface, raw, senderKey)
			}
		}
	})

	t.Run("unconfigured: the webpush key is absent on every surface", func(t *testing.T) {
		app, base := testApp(t)
		_, qr, enroll, _ := mintAndEnroll(t, app, base)
		if _, ok := qr["webpush"]; ok {
			t.Errorf("qr_payload must omit the webpush key without the lane: %s", qr["webpush"])
		}
		if _, ok := enroll["webpush"]; ok {
			t.Errorf("enroll response must omit the webpush key without the lane: %s", enroll["webpush"])
		}
	})

	t.Run("byte-stability: the keyless payload is the exact pre-0.42.0 bytes", func(t *testing.T) {
		app, base := testApp(t)
		_, _, _, resp := mintAndEnroll(t, app, base)
		want := fmt.Sprintf(`{"v":1,"servers":[%q],"token":%q,"project":{"id":%q,"name":%q}}`,
			base, resp.EnrollToken, resp.Project.ID, resp.Project.Name)
		if resp.QRPayload != want {
			t.Errorf("keyless qr_payload bytes changed:\n got %s\nwant %s", resp.QRPayload, want)
		}
	})
}

// TestEnrollTokenNoPinBehindIngress pins at the wire: with
// server.approverPublicUrl set, the QR names the INGRESS host, so strazad's
// own leaf pin must not ride along, even though strazad serves its own TLS
// and has a pin cached. Shipping it would make every phone enrolled against
// this deployment pin a key the ingress never presents, and they would all
// fail closed together at the first certificate renewal.
func TestEnrollTokenNoPinBehindIngress(t *testing.T) {
	t.Parallel()
	const ingress = "https://approve.example.com"
	// strazad serves its own TLS (so a pin exists and is cached) AND sits
	// behind a proxy that terminates the approver surface on its own host.
	// All three writes ride the preRun hook: the TLS paths are fake (set at
	// construction, New would try to load them), and setting anything after
	// Run starts races its boot-time announce reads.
	app, base := testAppPreRun(t, []func(*App){func(a *App) {
		a.cfg.Server.ApproverPublicURL = ingress
		a.cfg.Server.TLS = config.TLS{CertFile: "cert.pem", KeyFile: "key.pem"}
		a.tlsSPKIPin = "sha256/AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="
	}})

	kim := seedIdentity(t, app)
	grantAdmin(t, app, kim.ID)
	adminTok, _ := checkinToken(t, app, base)

	var resp mintTokenResponse
	if code := adminReq(t, "POST", base+"/v1/admin/approvers/enroll-token", adminTok,
		map[string]any{"username": "kim"}, &resp); code != http.StatusOK {
		t.Fatalf("mint enroll-token = %d", code)
	}

	if len(resp.Servers) != 1 || resp.Servers[0] != ingress {
		t.Fatalf("servers = %v, want [%s]", resp.Servers, ingress)
	}
	if resp.TLSSPKIPin != "" {
		t.Errorf("tls_spki_pin = %q, want none: the pin is strazad's leaf, the QR names the ingress", resp.TLSSPKIPin)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal([]byte(resp.QRPayload), &raw); err != nil {
		t.Fatalf("qr_payload is not valid JSON: %v (%q)", err, resp.QRPayload)
	}
	if _, ok := raw["pin"]; ok {
		t.Errorf("qr_payload must omit the pin key behind an ingress: %s", resp.QRPayload)
	}
}

// TestEnrollTokenTLSPin points config at a self-signed cert fixture, boots
// strazad, and asserts the cached pin, the mint response's tls_spki_pin, and the
// qr_payload's "pin" all equal an independently computed SPKI fingerprint.
func TestEnrollTokenTLSPin(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	certPEM, keyPEM := selfSignedCertPEM(t)
	certFile := filepath.Join(dir, "tls.crt")
	keyFile := filepath.Join(dir, "tls.key")
	if err := os.WriteFile(certFile, certPEM, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyFile, keyPEM, 0o600); err != nil {
		t.Fatal(err)
	}
	wantPin := spkiPinOf(t, certPEM)

	// The bare helper must agree with the reference computation.
	if got, err := automint.SPKIPinFromCertFile(certFile); err != nil || got != wantPin {
		t.Fatalf("spkiPinFromCertFile = %q, %v; want %q", got, err, wantPin)
	}

	const publicURL = "https://straza.example.test:8420"
	cfg := config.Config{
		Profile: config.ProfileStandalone,
		DataDir: dir,
		Server: config.Server{
			Listen:    "127.0.0.1:0",
			PublicURL: publicURL,
			TLS:       config.TLS{CertFile: certFile, KeyFile: keyFile},
		},
		Log:    config.Log{Level: "error", Format: "json"},
		Store:  config.Store{Driver: config.DriverSQLite, DSN: filepath.Join(dir, "straza.db")},
		Events: config.Events{Embedded: true},
		Governance: config.Governance{
			OfflineGraceTTL:   15 * time.Minute,
			LocalToolDefault:  config.EffectAllow,
			AuditBackpressure: config.BackpressureDrop,
		},
	}
	ctx, cancel := context.WithCancel(context.Background())
	app, err := New(ctx, cfg, logging.New(cfg.Log, io.Discard))
	if err != nil {
		cancel()
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() {
		cancel()
		app.close()
		if app.ln != nil {
			_ = app.ln.Close()
		}
	})

	// The pin is computed once at construction and cached (never per request).
	if app.tlsSPKIPin != wantPin {
		t.Fatalf("cached tlsSPKIPin = %q, want %q", app.tlsSPKIPin, wantPin)
	}

	seedIdentity(t, app) // user "kim"

	// Drive the handler directly (this app serves native TLS, so an over-the-wire
	// plaintext client cannot reach it; the response body is what we assert).
	body, _ := json.Marshal(map[string]string{"username": "kim"})
	req := httptest.NewRequest(http.MethodPost, "/v1/admin/approvers/enroll-token", bytes.NewReader(body))
	w := httptest.NewRecorder()
	app.handleApproverEnrollToken(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("mint enroll-token = %d: %s", w.Code, w.Body.String())
	}

	var resp mintTokenResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode mint response: %v", err)
	}
	if resp.TLSSPKIPin != wantPin {
		t.Errorf("tls_spki_pin = %q, want %q", resp.TLSSPKIPin, wantPin)
	}
	if len(resp.Servers) != 1 || resp.Servers[0] != publicURL {
		t.Errorf("servers = %v, want [%s]", resp.Servers, publicURL)
	}

	var qp struct {
		V       int      `json:"v"`
		Servers []string `json:"servers"`
		Token   string   `json:"token"`
		Pin     string   `json:"pin"`
	}
	if err := json.Unmarshal([]byte(resp.QRPayload), &qp); err != nil {
		t.Fatalf("qr_payload round-trip: %v", err)
	}
	if qp.V != 1 || qp.Pin != wantPin || qp.Token != resp.EnrollToken ||
		len(qp.Servers) != 1 || qp.Servers[0] != publicURL {
		t.Errorf("qr_payload = %+v, want {v:1 servers:[%s] token:%s pin:%s}", qp, publicURL, resp.EnrollToken, wantPin)
	}
}

// TestApproverPushWebPushRegistration drives the openapi 0.40.0 registration
// delta over HTTP: with the WebPush lane configured, kind=webpush registers
// with its subscription keys, keyed unifiedpush opts in, keyless unifiedpush
// stays on the legacy lane, malformed keys 400 with the validation detail,
// and the enroll 201 advertises the deployment's VAPID public key (absent
// entirely when the lane is off, the FCM absent-key contract).
func TestApproverPushWebPushRegistration(t *testing.T) {
	t.Parallel()
	app, base := testApp(t, func(cfg *config.Config) {
		cfg.Approval.Push.AllowedPushHosts = []string{"ntfy.example"}
		cfg.Approval.Push.WebPush.VAPIDKeyFile = filepath.Join(t.TempDir(), "vapid.pem")
		cfg.Approval.Push.WebPush.Contact = "mailto:ops@example.com"
	})
	kim := seedIdentity(t, app)
	grantAdmin(t, app, kim.ID)
	adminTok, _ := checkinToken(t, app, base)

	var mint struct {
		EnrollToken string `json:"enroll_token"`
	}
	if code := adminReq(t, "POST", base+"/v1/admin/approvers/enroll-token", adminTok,
		map[string]any{"username": "kim"}, &mint); code != http.StatusOK {
		t.Fatalf("mint enroll-token = %d", code)
	}
	_, spki := genApproverKey(t)
	var enr struct {
		DeviceToken string `json:"device_token"`
		WebPush     *struct {
			VAPIDPublicKey string `json:"vapid_public_key"`
		} `json:"webpush"`
	}
	if code := adminReq(t, "POST", base+"/v1/approver/enroll", "", map[string]any{
		"enroll_token": mint.EnrollToken,
		"device": map[string]any{
			"name": "kim-pixel", "platform": "android", "key_alg": "ecdsa-p256",
			"public_key": spki, "key_security_level": "strongbox",
			"attestation": map[string]any{"kind": "none"},
		},
	}, &enr); code != http.StatusCreated {
		t.Fatalf("enroll = %d", code)
	}

	// The enroll 201 hands the app the VAPID public key (what it gives its
	// distributor at REGISTER / a browser as applicationServerKey).
	if enr.WebPush == nil {
		t.Fatal("enroll response carries no webpush object with the lane configured")
	}
	if kb, err := base64.RawURLEncoding.DecodeString(enr.WebPush.VAPIDPublicKey); err != nil || len(kb) != 65 || kb[0] != 0x04 {
		t.Fatalf("vapid_public_key = %q, want base64url of a 65-octet uncompressed point (err=%v)", enr.WebPush.VAPIDPublicKey, err)
	}

	// Subscription keys for the registration rows.
	subPriv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	subPub, err := subPriv.PublicKey.ECDH()
	if err != nil {
		t.Fatal(err)
	}
	p256dh := base64.RawURLEncoding.EncodeToString(subPub.Bytes())
	authSecret := make([]byte, 16)
	if _, err := rand.Read(authSecret); err != nil {
		t.Fatal(err)
	}
	auth := base64.RawURLEncoding.EncodeToString(authSecret)

	cases := []struct {
		name string
		body map[string]any
		want int
	}{
		{"webpush with keys", map[string]any{
			"kind": "webpush", "token_or_endpoint": "https://ntfy.example/wp/sub",
			"p256dh": p256dh, "auth": auth}, http.StatusOK},
		{"unifiedpush keyed opt-in", map[string]any{
			"kind": "unifiedpush", "token_or_endpoint": "https://ntfy.example/up?up=1",
			"p256dh": p256dh, "auth": auth}, http.StatusOK},
		{"unifiedpush keyless legacy", map[string]any{
			"kind": "unifiedpush", "token_or_endpoint": "https://ntfy.example/topic"}, http.StatusOK},
		{"webpush without keys", map[string]any{
			"kind": "webpush", "token_or_endpoint": "https://ntfy.example/wp/sub2"}, http.StatusBadRequest},
		{"bad auth length", map[string]any{
			"kind": "webpush", "token_or_endpoint": "https://ntfy.example/wp/sub3",
			"p256dh": p256dh, "auth": base64.RawURLEncoding.EncodeToString(authSecret[:8])}, http.StatusBadRequest},
		{"keys on fcm", map[string]any{
			"kind": "fcm", "token_or_endpoint": "tok-1",
			"p256dh": p256dh, "auth": auth}, http.StatusBadRequest},
		{"keyed endpoint off-allowlist", map[string]any{
			"kind": "webpush", "token_or_endpoint": "https://evil.example/wp/sub",
			"p256dh": p256dh, "auth": auth}, http.StatusBadRequest},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if code := adminReq(t, "PUT", base+"/v1/approver/push", enr.DeviceToken, tc.body, nil); code != tc.want {
				t.Errorf("PUT push = %d, want %d", code, tc.want)
			}
		})
	}

	// Delete mirrors registration: same triple (keys included) removes the row.
	if code := adminReq(t, "DELETE", base+"/v1/approver/push", enr.DeviceToken, map[string]any{
		"kind": "webpush", "token_or_endpoint": "https://ntfy.example/wp/sub",
		"p256dh": p256dh, "auth": auth}, nil); code != http.StatusOK {
		t.Errorf("DELETE keyed push = %d, want 200", code)
	}
}

// TestEnrollOmitsWebPushWhenOff: with no vapidKeyFile the enroll 201 has no
// webpush key at all (never null / an empty object) and a keyed registration
// is refused with the fix named: fail closed, nothing stored undeliverable.
func TestEnrollOmitsWebPushWhenOff(t *testing.T) {
	t.Parallel()
	app, base := testApp(t, func(cfg *config.Config) {
		cfg.Approval.Push.AllowedPushHosts = []string{"ntfy.example"}
	})
	kim := seedIdentity(t, app)
	grantAdmin(t, app, kim.ID)
	adminTok, _ := checkinToken(t, app, base)

	var mint struct {
		EnrollToken string `json:"enroll_token"`
	}
	if code := adminReq(t, "POST", base+"/v1/admin/approvers/enroll-token", adminTok,
		map[string]any{"username": "kim"}, &mint); code != http.StatusOK {
		t.Fatalf("mint enroll-token = %d", code)
	}
	_, spki := genApproverKey(t)
	var enroll map[string]json.RawMessage
	var enr struct {
		DeviceToken string `json:"device_token"`
	}
	body := map[string]any{
		"enroll_token": mint.EnrollToken,
		"device": map[string]any{
			"name": "kim-pixel", "platform": "android", "key_alg": "ecdsa-p256",
			"public_key": spki, "key_security_level": "strongbox",
			"attestation": map[string]any{"kind": "none"},
		},
	}
	if code := adminReq(t, "POST", base+"/v1/approver/enroll", "", body, &enroll); code != http.StatusCreated {
		t.Fatalf("enroll = %d", code)
	}
	if raw, ok := enroll["webpush"]; ok {
		t.Errorf("enroll response must omit the webpush key when the lane is off: %s", raw)
	}
	if err := json.Unmarshal(enroll["device_token"], &enr.DeviceToken); err != nil {
		t.Fatalf("device_token: %v", err)
	}

	subPriv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	subPub, err := subPriv.PublicKey.ECDH()
	if err != nil {
		t.Fatal(err)
	}
	if code := adminReq(t, "PUT", base+"/v1/approver/push", enr.DeviceToken, map[string]any{
		"kind": "unifiedpush", "token_or_endpoint": "https://ntfy.example/up",
		"p256dh": base64.RawURLEncoding.EncodeToString(subPub.Bytes()),
		"auth":   base64.RawURLEncoding.EncodeToString(make([]byte, 16)),
	}, nil); code != http.StatusBadRequest {
		t.Errorf("keyed registration without the webpush sender = %d, want 400", code)
	}
}

// TestApproverPendingMineCarriesApproverRoles pins the 0.60.0 field: a
// requester's own (non-decidable) pending row names who may decide it, so the
// approvals page can say "waiting on straza-admin" instead of showing a bare
// row with no verb and no explanation.
func TestApproverPendingMineCarriesApproverRoles(t *testing.T) {
	t.Parallel()
	app, base := testApp(t)
	kim := seedIdentity(t, app)
	grantAdmin(t, app, kim.ID)
	adminTok, _ := checkinToken(t, app, base)

	var mint struct {
		EnrollToken string `json:"enroll_token"`
	}
	if code := adminReq(t, "POST", base+"/v1/admin/approvers/enroll-token", adminTok,
		map[string]any{"username": "kim"}, &mint); code != http.StatusOK {
		t.Fatalf("mint enroll-token = %d", code)
	}
	_, spki := genApproverKey(t)
	var enr struct {
		DeviceToken string `json:"device_token"`
	}
	if code := adminReq(t, "POST", base+"/v1/approver/enroll", "", map[string]any{
		"enroll_token": mint.EnrollToken,
		"device": map[string]any{"name": "kim-pixel", "platform": "android", "key_alg": "ecdsa-p256",
			"public_key": spki, "key_security_level": "strongbox",
			"attestation": map[string]any{"kind": "none"}},
	}, &enr); code != http.StatusCreated {
		t.Fatalf("enroll = %d", code)
	}

	// kim's own request, decidable only by straza-admin: the mine view must
	// SAY so rather than render an unexplained verb-less row.
	seedApproval(t, app, kim.ID, []string{"straza-admin"})
	var rows []approverRow
	if code := adminReq(t, "GET", base+"/v1/approver/pending?scope=mine", enr.DeviceToken, nil, &rows); code != http.StatusOK {
		t.Fatalf("pending mine = %d", code)
	}
	if len(rows) != 1 {
		t.Fatalf("mine rows = %d, want 1", len(rows))
	}
	if rows[0].Challenge != "" {
		t.Fatal("a mine row must carry no challenge")
	}
	if len(rows[0].ApproverRoles) != 1 || rows[0].ApproverRoles[0] != "straza-admin" {
		t.Fatalf("approver_roles = %v, want [straza-admin]", rows[0].ApproverRoles)
	}
}
