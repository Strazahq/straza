package approval

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/strazahq/straza/internal/config"
)

// fcmTestServer stands in for Google's token endpoint and the FCM send
// endpoint, recording what it receives so the test can assert the JWT and the
// message body.
type fcmTestServer struct {
	srv         *httptest.Server
	tokenHits   int
	sendHits    int
	lastAuth    string
	lastMessage map[string]any
	lastJWT     string
}

func newFCMTestServer(t *testing.T) *fcmTestServer {
	f := &fcmTestServer{}
	mux := http.NewServeMux()
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		f.tokenHits++
		_ = r.ParseForm()
		f.lastJWT = r.Form.Get("assertion")
		if r.Form.Get("grant_type") != "urn:ietf:params:oauth:grant-type:jwt-bearer" {
			t.Errorf("token grant_type = %q", r.Form.Get("grant_type"))
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token": "ya29.test-bearer", "expires_in": 3600, "token_type": "Bearer",
		})
	})
	mux.HandleFunc("/send", func(w http.ResponseWriter, r *http.Request) {
		f.sendHits++
		f.lastAuth = r.Header.Get("Authorization")
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		if m, ok := body["message"].(map[string]any); ok {
			f.lastMessage = m
		}
		w.WriteHeader(http.StatusOK)
	})
	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	return f
}

// writeServiceAccount builds a Google-shaped service-account JSON pointing its
// token_uri at tokenURI, and returns the file path plus the private key the
// test verifies the JWT against.
func writeServiceAccount(t *testing.T, tokenURI string) (string, *rsa.PrivateKey) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	pemKey := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})
	sa := map[string]string{
		"type":         "service_account",
		"project_id":   "straza-proj",
		"client_email": "fcm@straza-proj.iam.gserviceaccount.com",
		"private_key":  string(pemKey),
		"token_uri":    tokenURI,
	}
	b, _ := json.Marshal(sa)
	path := filepath.Join(t.TempDir(), "sa.json")
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}
	return path, key
}

// TestFCMSendSignsExchangesAndPosts drives the full FCM lane: self-signed
// RS256 JWT → bearer exchange → messages:send, asserting the JWT verifies
// against the account key, the bearer rides the send, the body carries exactly
// the opaque envelope (string values), and the bearer is cached across sends.
func TestFCMSendSignsExchangesAndPosts(t *testing.T) {
	server := newFCMTestServer(t)
	saPath, key := writeServiceAccount(t, server.srv.URL+"/token")

	sender, err := newFCMSender(
		config.FCMPush{Enabled: true, ServiceAccountFile: saPath, ProjectID: "straza-proj"},
		&http.Client{Timeout: 5 * time.Second},
	)
	if err != nil {
		t.Fatalf("newFCMSender: %v", err)
	}
	sender.sendEndpoint = server.srv.URL + "/send"

	status, err := sender.send(context.Background(), "reg-token-abc", pushKindDecide, "apr-77")
	if err != nil {
		t.Fatalf("send: %v", err)
	}
	if status != http.StatusOK {
		t.Fatalf("send status = %d, want 200", status)
	}

	// The token endpoint received a verifiable RS256 JWT with the right claims.
	verifyFCMJWT(t, server.lastJWT, &key.PublicKey, server.srv.URL+"/token")

	// The send carried the exchanged bearer.
	if server.lastAuth != "Bearer ya29.test-bearer" {
		t.Errorf("send Authorization = %q, want the exchanged bearer", server.lastAuth)
	}
	// The message body is {"token":..,"data":{"v":"1","ref":"apr-77","kind":"decide"}}.
	if server.lastMessage["token"] != "reg-token-abc" {
		t.Errorf("message token = %v", server.lastMessage["token"])
	}
	data, ok := server.lastMessage["data"].(map[string]any)
	if !ok {
		t.Fatalf("message data missing: %+v", server.lastMessage)
	}
	if len(data) != 3 || data["v"] != "1" || data["ref"] != "apr-77" || data["kind"] != "decide" {
		t.Errorf("fcm data = %+v, want exactly v=1 ref=apr-77 kind=decide (string values)", data)
	}

	// A second send reuses the cached bearer: no re-exchange.
	if _, err := sender.send(context.Background(), "reg-token-abc", pushKindStatus, "apr-78"); err != nil {
		t.Fatalf("second send: %v", err)
	}
	if server.tokenHits != 1 {
		t.Errorf("token endpoint hit %d times, want 1 (bearer must be cached)", server.tokenHits)
	}
	if server.sendHits != 2 {
		t.Errorf("send endpoint hit %d times, want 2", server.sendHits)
	}
}

// TestFCMBearerRefreshesAfterExpiry: once the cache horizon passes, the sender
// re-exchanges rather than reusing a stale bearer.
func TestFCMBearerRefreshesAfterExpiry(t *testing.T) {
	server := newFCMTestServer(t)
	saPath, _ := writeServiceAccount(t, server.srv.URL+"/token")

	sender, err := newFCMSender(
		config.FCMPush{Enabled: true, ServiceAccountFile: saPath, ProjectID: "straza-proj"},
		&http.Client{Timeout: 5 * time.Second},
	)
	if err != nil {
		t.Fatalf("newFCMSender: %v", err)
	}
	sender.sendEndpoint = server.srv.URL + "/send"

	now := time.Unix(1_700_000_000, 0)
	sender.now = func() time.Time { return now }
	if _, err := sender.send(context.Background(), "reg", pushKindDecide, "a"); err != nil {
		t.Fatal(err)
	}
	// Advance past the cache horizon (expires_in 3600s minus the 5m margin).
	now = now.Add(3600 * time.Second)
	if _, err := sender.send(context.Background(), "reg", pushKindDecide, "b"); err != nil {
		t.Fatal(err)
	}
	if server.tokenHits != 2 {
		t.Errorf("token endpoint hit %d times, want 2 (refresh after expiry)", server.tokenHits)
	}
}

// verifyFCMJWT splits and verifies an RS256 service-account assertion.
func verifyFCMJWT(t *testing.T, jwt string, pub *rsa.PublicKey, wantAud string) {
	t.Helper()
	parts := strings.Split(jwt, ".")
	if len(parts) != 3 {
		t.Fatalf("jwt has %d parts, want 3", len(parts))
	}
	var header struct{ Alg, Typ string }
	decodeSeg(t, parts[0], &header)
	if header.Alg != "RS256" || header.Typ != "JWT" {
		t.Errorf("jwt header = %+v, want RS256/JWT", header)
	}
	var claims struct {
		Iss, Scope, Aud string
		Iat, Exp        int64
	}
	decodeSeg(t, parts[1], &claims)
	if claims.Iss != "fcm@straza-proj.iam.gserviceaccount.com" {
		t.Errorf("jwt iss = %q", claims.Iss)
	}
	if claims.Aud != wantAud {
		t.Errorf("jwt aud = %q, want %q", claims.Aud, wantAud)
	}
	if claims.Scope != fcmScope {
		t.Errorf("jwt scope = %q, want %q", claims.Scope, fcmScope)
	}
	if claims.Exp <= claims.Iat {
		t.Errorf("jwt exp %d not after iat %d", claims.Exp, claims.Iat)
	}
	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		t.Fatalf("decode sig: %v", err)
	}
	digest := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if err := rsa.VerifyPKCS1v15(pub, crypto.SHA256, digest[:], sig); err != nil {
		t.Errorf("jwt signature does not verify: %v", err)
	}
}

func decodeSeg(t *testing.T, seg string, v any) {
	t.Helper()
	raw, err := base64.RawURLEncoding.DecodeString(seg)
	if err != nil {
		t.Fatalf("decode segment: %v", err)
	}
	if err := json.Unmarshal(raw, v); err != nil {
		t.Fatalf("unmarshal segment: %v", err)
	}
}
