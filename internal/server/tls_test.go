package server

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"io"
	"math/big"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/strazahq/straza/internal/config"
	"github.com/strazahq/straza/internal/logging"
)

// selfSignedCert writes a loopback-valid self-signed cert+key pair and
// returns their paths plus a cert pool trusting it.
func selfSignedCert(t *testing.T) (certFile, keyFile string, pool *x509.CertPool) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "strazad-test"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1")},
	}
	der, err := x509.CreateCertificate(rand.Reader, &tmpl, &tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}

	dir := t.TempDir()
	certFile = filepath.Join(dir, "tls.crt")
	keyFile = filepath.Join(dir, "tls.key")
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	if err := os.WriteFile(certFile, certPEM, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyFile, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}), 0o600); err != nil {
		t.Fatal(err)
	}
	pool = x509.NewCertPool()
	pool.AppendCertsFromPEM(certPEM)
	return certFile, keyFile, pool
}

// TestServeTLS boots strazad with server.tls set and asserts: HTTPS
// serves, the TLS floor is 1.2, and plaintext clients are refused.
func TestServeTLS(t *testing.T) {
	t.Parallel()
	certFile, keyFile, pool := selfSignedCert(t)
	dir := t.TempDir()
	cfg := config.Config{
		Profile: config.ProfileStandalone,
		DataDir: dir,
		Server: config.Server{
			Listen: "127.0.0.1:0", PublicURL: "https://127.0.0.1",
			TLS: config.TLS{CertFile: certFile, KeyFile: keyFile},
		},
		Log:   config.Log{Level: "error", Format: "json"},
		Store: config.Store{Driver: config.DriverSQLite, DSN: filepath.Join(dir, "straza.db")},
		Events: config.Events{
			Embedded: true,
		},
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
	done := make(chan error, 1)
	go func() { done <- app.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(15 * time.Second):
			t.Error("shutdown timed out")
		}
	})

	base := "https://" + app.Addr()
	client := &http.Client{Transport: &http.Transport{
		TLSClientConfig: &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12},
	}}
	resp, err := client.Get(base + "/healthz")
	if err != nil {
		t.Fatalf("HTTPS GET /healthz: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("healthz over TLS = %d", resp.StatusCode)
	}
	if resp.TLS == nil || resp.TLS.Version < tls.VersionTLS12 {
		t.Fatalf("connection not TLS >= 1.2: %+v", resp.TLS)
	}

	// TLS 1.1 client is refused (MinVersion floor).
	oldClient := &http.Client{Transport: &http.Transport{
		TLSClientConfig: &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS11, MaxVersion: tls.VersionTLS11}, // #nosec G402 -- deliberately old client proving the server floor
	}}
	if resp, err := oldClient.Get(base + "/healthz"); err == nil {
		_ = resp.Body.Close()
		t.Fatal("TLS 1.1 handshake must be refused")
	}

	// Plaintext client is refused.
	plain := &http.Client{Timeout: 2 * time.Second}
	if resp, err := plain.Get("http://" + app.Addr() + "/healthz"); err == nil {
		_ = resp.Body.Close()
		if resp.StatusCode == http.StatusOK {
			t.Fatal("plaintext HTTP must not be served on a TLS listener")
		}
	}
}
