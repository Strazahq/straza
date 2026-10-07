package server

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/strazahq/straza/internal/config"
)

// TestSinksEndToEnd pins that a webhook sink outage delays events but
// loses none (durable consumer NAK/redelivers until the endpoint recovers,
// HMAC verified on arrival), and a file sink captures the same events as
// JSONL.
func TestSinksEndToEnd(t *testing.T) {
	t.Parallel()
	secret := "sink-hmac-key"
	var failing atomic.Bool
	failing.Store(true)

	var mu sync.Mutex
	received := map[string]bool{} // CE id → seen
	var badSig atomic.Int32
	hook := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if failing.Load() {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		body, _ := io.ReadAll(r.Body)
		mac := hmac.New(sha256.New, []byte(secret))
		mac.Write(body)
		if r.Header.Get("X-Straza-Signature") != "sha256="+hex.EncodeToString(mac.Sum(nil)) {
			badSig.Add(1)
		}
		var ce struct {
			ID string `json:"id"`
		}
		_ = json.Unmarshal(body, &ce)
		mu.Lock()
		received[ce.ID] = true
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	defer hook.Close()

	jsonlPath := filepath.Join(t.TempDir(), "events.jsonl")
	app, base := testApp(t, func(c *config.Config) {
		c.Sinks = []config.Sink{
			{Name: "siem", Type: config.SinkWebhook, URL: hook.URL, Secret: secret,
				Subjects: []string{"straza.identity.>"}},
			{Name: "archive", Type: config.SinkFile, Path: jsonlPath}, // default: straza.>
		}
	})
	_ = seedIdentity(t, app)
	idToken := loginDeviceFlow(t, base, "kim", "hunter2!")

	// Generate identity events (enroll + session.start) WHILE the webhook is
	// down: they must be redelivered, not dropped.
	_, enroll := postJSON(t, base+"/v1/enroll", map[string]any{
		"id_token": idToken,
		"device":   map[string]string{"name": "kim-laptop", "platform": "linux", "fingerprint": "sha256:sink"},
	})
	code, checkin := postJSON(t, base+"/v1/checkin", map[string]any{
		"id_token": idToken, "device_id": enroll["device_id"],
		"harness":     map[string]string{"name": "claude-code", "version": "2.1.0"},
		"attestation": map[string]any{"managed": false, "hashes": map[string]string{"self": "sha256:abc123"}},
	})
	if code != http.StatusOK {
		t.Fatalf("checkin = %d %v", code, checkin)
	}

	// Give the pipeline time to attempt (and fail) deliveries, then recover.
	time.Sleep(2 * time.Second)
	mu.Lock()
	if len(received) != 0 {
		mu.Unlock()
		t.Fatal("failing endpoint must not have accepted deliveries")
	}
	mu.Unlock()
	failing.Store(false)

	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		n := len(received)
		mu.Unlock()
		if n >= 2 { // enroll + session.start at minimum
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	mu.Lock()
	n := len(received)
	mu.Unlock()
	if n < 2 {
		t.Fatalf("webhook received %d unique events after recovery, want >= 2 (no-loss AC)", n)
	}
	if badSig.Load() != 0 {
		t.Errorf("%d deliveries carried an invalid HMAC signature", badSig.Load())
	}

	// The file sink captured the same identity events as JSONL.
	deadline = time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		raw, err := os.ReadFile(jsonlPath)
		if err == nil && strings.Count(string(raw), "straza.identity.updated") >= 2 {
			for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
				var ce map[string]any
				if err := json.Unmarshal([]byte(line), &ce); err != nil {
					t.Fatalf("file sink line is not valid JSON: %q", line)
				}
			}
			return // both sinks proven
		}
		time.Sleep(200 * time.Millisecond)
	}
	raw, _ := os.ReadFile(jsonlPath)
	t.Fatalf("file sink incomplete after deadline: %q", raw)
}
