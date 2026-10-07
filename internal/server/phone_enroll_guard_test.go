package server

import (
	"crypto/x509"
	"encoding/pem"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/strazahq/straza/internal/automint"
	"github.com/strazahq/straza/internal/config"
)

// TestPhoneEnrollGuardMessage pins when a phone-destined enroll-token mint
// must be refused: automint DERIVED a loopback advertise URL (its no-LAN
// last resort) while the approver listener accepts non-loopback traffic, so
// the QR would scan fine and dial nothing on any phone. An operator who
// CONFIGURED a loopback publicUrl stays mintable: that is the deliberate
// tunnel lane (ssh tunnel + adb reverse), where the phone really does dial
// 127.0.0.1.
func TestPhoneEnrollGuardMessage(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name        string
		listen, url string
		derived     bool
		wantRefusal bool
	}{
		{"derived loopback on LAN exposure", "0.0.0.0:8443", "https://127.0.0.1:8443", true, true},
		{"derived localhost on empty-host bind", ":8443", "https://localhost:8443", true, true},
		{"configured loopback is the tunnel lane", "0.0.0.0:8443", "https://127.0.0.1:8443", false, false},
		{"derived loopback on loopback bind", "127.0.0.1:8443", "https://127.0.0.1:8443", true, false},
		{"derived dialable address", "0.0.0.0:8443", "https://192.168.1.42:8443", true, false},
		{"no dedicated listener", "", "", true, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			app := &App{cfg: config.Config{}, approverURLDerived: tc.derived}
			app.cfg.Server.PublicURL = "http://localhost:8420"
			if tc.listen != "" {
				app.cfg.Server.ApproverTLS = config.ApproverTLS{
					Listen: tc.listen, CertFile: "c", KeyFile: "k", PublicURL: tc.url,
				}
			}
			msg := app.phoneEnrollGuardMessage()
			if got := msg != ""; got != tc.wantRefusal {
				t.Fatalf("guard = %q, want refusal=%v", msg, tc.wantRefusal)
			}
			if tc.wantRefusal && !strings.Contains(msg, "STRAZA_APPROVER_TLS_PUBLIC_URL") {
				t.Errorf("refusal %q does not name the env var to set", msg)
			}
		})
	}
}

// TestPhoneEnrollGuardEndpoints pins the guard on the two phone-destined
// mint surfaces: the admin "Add mobile approver" mint and the self-service
// mobile mint refuse a derived dud QR with the actionable message, while
// the self-service browser mint (a browser on this machine dials loopback
// just fine) stays open.
func TestPhoneEnrollGuardEndpoints(t *testing.T) {
	t.Parallel()
	app, base := testAppPreRun(t, []func(*App){func(a *App) {
		// Simulate automint's no-LAN last resort: loopback advertise URL,
		// derived, listener accepting non-loopback traffic.
		a.approverURLDerived = true
	}}, func(cfg *config.Config) {
		on := true
		cfg.Server.ApproverTLS = config.ApproverTLS{
			Listen: "0.0.0.0:0", AutoMint: &on, PublicURL: "https://127.0.0.1:8443",
		}
	})
	kim := seedIdentity(t, app)
	grantAdmin(t, app, kim.ID)
	adminTok, _ := checkinToken(t, app, base)

	var refusal struct {
		Error string `json:"error"`
	}
	if code := adminReq(t, http.MethodPost, base+"/v1/admin/approvers/enroll-token", adminTok,
		map[string]any{"user_id": kim.ID}, &refusal); code != http.StatusConflict {
		t.Fatalf("admin mint with derived dud QR = %d, want 409", code)
	}
	if !strings.Contains(refusal.Error, "STRAZA_APPROVER_TLS_PUBLIC_URL") || !strings.Contains(refusal.Error, "127.0.0.1") {
		t.Errorf("admin refusal %q must name the env var and the loopback address", refusal.Error)
	}

	selfURL := base + "/v1/approvals/self/enroll-token"
	loginTok := loginDeviceFlow(t, base, "kim", "hunter2!")
	if code := adminReq(t, http.MethodPost, selfURL, loginTok,
		map[string]any{"channel": "mobile"}, &refusal); code != http.StatusConflict {
		t.Fatalf("self mobile mint with derived dud QR = %d, want 409", code)
	}
	if code := adminReq(t, http.MethodPost, selfURL, loginTok,
		map[string]any{"channel": "browser"}, nil); code != http.StatusOK {
		t.Fatalf("self browser mint = %d, want 200", code)
	}
}

// TestPhoneEnrollConfiguredLoopbackMints is the guard's negative control on
// a booted app: an operator-CONFIGURED loopback publicUrl (the tunnel lane)
// mints exactly as before the guard existed.
func TestPhoneEnrollConfiguredLoopbackMints(t *testing.T) {
	t.Parallel()
	app, base := testApp(t, func(cfg *config.Config) {
		on := true
		cfg.Server.ApproverTLS = config.ApproverTLS{
			Listen: "0.0.0.0:0", AutoMint: &on, PublicURL: "https://127.0.0.1:8443",
		}
	})
	kim := seedIdentity(t, app)
	grantAdmin(t, app, kim.ID)
	adminTok, _ := checkinToken(t, app, base)
	var resp struct {
		Servers []string `json:"servers"`
	}
	if code := adminReq(t, http.MethodPost, base+"/v1/admin/approvers/enroll-token", adminTok,
		map[string]any{"user_id": kim.ID}, &resp); code != http.StatusOK {
		t.Fatalf("configured-loopback mint = %d, want 200 (the tunnel lane)", code)
	}
	if len(resp.Servers) != 1 || resp.Servers[0] != "https://127.0.0.1:8443" {
		t.Errorf("servers = %v", resp.Servers)
	}
}

// TestPhoneEnrollGuardCertName pins the guard's second case: an auto-minted
// pair that does not NAME the advertised host is refused at mint time with
// the re-mint instruction, because the app validates hostname against the
// pinned leaf and would refuse right after the scan, as iOS does.
// Bring-your-own pairs stay mintable: the operator chose that
// certificate.
func TestPhoneEnrollGuardCertName(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	certFile := filepath.Join(dir, "cert.pem")
	if _, _, err := automint.LoadOrMintApproverCert(certFile, filepath.Join(dir, "key.pem"),
		[]string{"localhost", "127.0.0.1"}); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(certFile)
	if err != nil {
		t.Fatal(err)
	}
	block, _ := pem.Decode(raw)
	leaf, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name        string
		url         string
		info        *automint.CertInfo
		wantRefusal bool
	}{
		{"auto-minted pair missing the advertised host", "https://192.0.2.5:8443",
			&automint.CertInfo{AutoMinted: true, Leaf: leaf}, true},
		{"auto-minted pair naming the advertised host", "https://127.0.0.1:8443",
			&automint.CertInfo{AutoMinted: true, Leaf: leaf}, false},
		{"bring-your-own pair is the operator's choice", "https://192.0.2.5:8443",
			&automint.CertInfo{AutoMinted: false, Leaf: leaf}, false},
		{"no leaf loaded", "https://192.0.2.5:8443",
			&automint.CertInfo{AutoMinted: true}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			app := &App{cfg: config.Config{}, approverCert: tc.info}
			app.cfg.Server.PublicURL = "http://localhost:8420"
			app.cfg.Server.ApproverTLS = config.ApproverTLS{
				Listen: "0.0.0.0:8443", CertFile: "c", KeyFile: "k", PublicURL: tc.url,
			}
			msg := app.phoneEnrollGuardMessage()
			if got := msg != ""; got != tc.wantRefusal {
				t.Fatalf("guard = %q, want refusal=%v", msg, tc.wantRefusal)
			}
			if tc.wantRefusal && !strings.Contains(msg, "re-enroll") {
				t.Errorf("refusal %q must carry the re-mint instruction", msg)
			}
		})
	}
}
