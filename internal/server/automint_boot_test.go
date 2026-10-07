package server

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/strazahq/straza/internal/automint"
	"github.com/strazahq/straza/internal/config"
	"github.com/strazahq/straza/internal/logging"
)

func ptrBool(v bool) *bool { return &v }

// TestApproverAutoMintBootE2E is the acceptance test for the zero-config
// story: a VIRGIN standalone boot with auto-mint on serves the approver
// surface over https, the enroll QR carries that listener's URL + the minted
// leaf's SPKI pin, the wire actually presents the pinned leaf, and /version
// names cert path + pin + expiry for diagnostics.
func TestApproverAutoMintBootE2E(t *testing.T) {
	t.Parallel()
	app, _ := testApp(t, func(cfg *config.Config) {
		cfg.Server.ApproverTLS.AutoMint = ptrBool(true)
		// Ephemeral port: parallel test processes must not fight over :8443.
		cfg.Server.ApproverTLS.Listen = "127.0.0.1:0"
	})

	at := app.cfg.Server.ApproverTLS
	wantCert := filepath.Join(app.cfg.DataDir, "approver-tls", "cert.pem")
	if at.CertFile != wantCert {
		t.Fatalf("certFile = %q, want auto-minted %q", at.CertFile, wantCert)
	}
	wantPin, err := automint.SPKIPinFromCertFile(at.CertFile)
	if err != nil {
		t.Fatal(err)
	}
	if app.tlsSPKIPin != wantPin {
		t.Errorf("cached pin %q != minted cert pin %q", app.tlsSPKIPin, wantPin)
	}
	if !strings.HasPrefix(at.PublicURL, "https://") {
		t.Errorf("publicUrl = %q, want https", at.PublicURL)
	}

	// The QR a phone would scan: names the approver URL and the minted pin.
	seedIdentity(t, app) // user "kim"
	body, _ := json.Marshal(map[string]string{"username": "kim"})
	w := httptest.NewRecorder()
	app.handleApproverEnrollToken(w, httptest.NewRequest(http.MethodPost, "/v1/admin/approvers/enroll-token", bytes.NewReader(body)))
	if w.Code != http.StatusOK {
		t.Fatalf("enroll-token = %d: %s", w.Code, w.Body.String())
	}
	var mint struct {
		Servers []string `json:"servers"`
		QR      struct {
			Pin     string   `json:"pin"`
			Servers []string `json:"servers"`
		} `json:"qr"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &mint); err != nil {
		t.Fatal(err)
	}
	if mint.QR.Pin != wantPin {
		t.Errorf("QR pin = %q, want %q", mint.QR.Pin, wantPin)
	}
	if len(mint.Servers) != 1 || mint.Servers[0] != at.PublicURL {
		t.Errorf("QR servers = %v, want [%s]", mint.Servers, at.PublicURL)
	}

	// The wire agrees with the QR: a client trusting the minted cert (as the
	// pinning app effectively does) completes the handshake against the live
	// listener and sees exactly the pinned SPKI.
	certPEM, err := os.ReadFile(at.CertFile)
	if err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(certPEM) {
		t.Fatal("minted cert not poolable")
	}
	conn, err := tls.Dial("tcp", app.approverLn.Addr().String(), &tls.Config{
		RootCAs: pool, ServerName: "127.0.0.1", MinVersion: tls.VersionTLS12,
	})
	if err != nil {
		t.Fatalf("handshake against auto-minted listener: %v", err)
	}
	leaf := conn.ConnectionState().PeerCertificates[0]
	_ = conn.Close()
	sum := sha256.Sum256(leaf.RawSubjectPublicKeyInfo)
	if got := "sha256/" + base64.StdEncoding.EncodeToString(sum[:]); got != wantPin {
		t.Errorf("wire pin %q != QR pin %q", got, wantPin)
	}

	// /version names the surface for diagnostics (straza doctor reads this).
	vw := httptest.NewRecorder()
	app.handleVersion(vw, httptest.NewRequest(http.MethodGet, "/version", nil))
	var vs struct {
		Approver *struct {
			PublicURL    string `json:"public_url"`
			TLSSPKIPin   string `json:"tls_spki_pin"`
			CertNotAfter string `json:"cert_not_after"`
			AutoMinted   bool   `json:"auto_minted"`
			CertFile     string `json:"cert_file"`
		} `json:"approver"`
	}
	if err := json.Unmarshal(vw.Body.Bytes(), &vs); err != nil {
		t.Fatal(err)
	}
	if vs.Approver == nil {
		t.Fatal("/version carries no approver block")
	}
	if vs.Approver.TLSSPKIPin != wantPin || !vs.Approver.AutoMinted ||
		vs.Approver.CertFile != at.CertFile || vs.Approver.PublicURL != at.PublicURL {
		t.Errorf("/version approver = %+v", vs.Approver)
	}
	// The window is a contract on both sides: far enough out that rotation
	// stays rare, and under Apple's 825-day leaf ceiling, which iOS enforces
	// even for pinned anchors (a longer mint bricks every iPhone enrollment).
	if exp, err := time.Parse(time.RFC3339, vs.Approver.CertNotAfter); err != nil ||
		time.Until(exp) < 700*24*time.Hour || time.Until(exp) > 825*24*time.Hour {
		t.Errorf("cert_not_after = %q (%v), want years out yet under the 825-day ceiling", vs.Approver.CertNotAfter, err)
	}
}

// TestApproverAutoMintPortBusy: when the auto listener cannot bind, the boot
// fails closed with an error naming both ways out (a different port, or the
// autoMint knob), never a silently missing phone surface.
func TestApproverAutoMintPortBusy(t *testing.T) {
	t.Parallel()
	blocker, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer blocker.Close() //nolint:errcheck

	dir := t.TempDir()
	cfg := config.Config{
		Profile: config.ProfileStandalone,
		DataDir: dir,
		Server:  config.Server{Listen: "127.0.0.1:0"},
		Log:     config.Log{Level: "error", Format: "json"},
		Store:   config.Store{Driver: config.DriverSQLite, DSN: filepath.Join(dir, "straza.db")},
		Events:  config.Events{Embedded: true},
		Governance: config.Governance{
			OfflineGraceTTL:   15 * time.Minute,
			LocalToolDefault:  config.EffectAllow,
			AuditBackpressure: config.BackpressureDrop,
		},
	}
	cfg.Server.ApproverTLS.AutoMint = ptrBool(true)
	cfg.Server.ApproverTLS.Listen = blocker.Addr().String()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	app, err := New(ctx, cfg, logging.New(cfg.Log, io.Discard))
	if err == nil {
		app.close()
		t.Fatal("boot succeeded with the approver port taken")
	}
	if !strings.Contains(err.Error(), "autoMint: false") {
		t.Errorf("busy-port error %q does not name the disable knob", err)
	}
}
