package automint

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"io"
	"log/slog"
	"math/big"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/strazahq/straza/internal/config"
)

func discardLog() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func ptrBool(v bool) *bool { return &v }

// readLeaf parses the first CERTIFICATE block of a PEM file.
func readLeaf(t *testing.T, path string) *x509.Certificate {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	block, _ := pem.Decode(raw)
	if block == nil || block.Type != "CERTIFICATE" {
		t.Fatalf("%s: no CERTIFICATE block", path)
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	return cert
}

// TestLoadOrMintApproverCert pins the persisted first-boot mint: an empty
// state dir gains a self-signed P-256 pair the approver listener can serve,
// the second boot loads the SAME pair (the phone's pinned SPKI must survive
// restarts), and half-present or corrupt state fails closed with the
// re-enroll caution instead of silently rotating the pin.
func TestLoadOrMintApproverCert(t *testing.T) {
	dir := t.TempDir()
	certFile := filepath.Join(dir, "cert.pem")
	keyFile := filepath.Join(dir, "key.pem")
	hosts := []string{"localhost", "testhost", "192.0.2.7"}

	notAfter, minted, err := LoadOrMintApproverCert(certFile, keyFile, hosts)
	if err != nil {
		t.Fatalf("virgin mint: %v", err)
	}
	if !minted {
		t.Error("virgin mint: minted = false, want true")
	}

	// The pair must actually serve: same loader the listener uses.
	if _, err := tls.LoadX509KeyPair(certFile, keyFile); err != nil {
		t.Fatalf("minted pair does not load: %v", err)
	}

	leaf := readLeaf(t, certFile)
	if leaf.IsCA {
		t.Error("minted leaf claims IsCA")
	}
	// CheckSignatureFrom would demand CA bits; a self-signed LEAF verifies
	// its own signature directly.
	if err := leaf.CheckSignature(leaf.SignatureAlgorithm, leaf.RawTBSCertificate, leaf.Signature); err != nil {
		t.Errorf("not self-signed: %v", err)
	}
	if _, ok := leaf.PublicKey.(*ecdsa.PublicKey); !ok {
		t.Errorf("public key = %T, want *ecdsa.PublicKey (P-256)", leaf.PublicKey)
	}
	wantEKU := false
	for _, eku := range leaf.ExtKeyUsage {
		if eku == x509.ExtKeyUsageServerAuth {
			wantEKU = true
		}
	}
	if !wantEKU {
		t.Error("minted leaf lacks ServerAuth EKU")
	}
	if !slices.Contains(leaf.DNSNames, "localhost") || !slices.Contains(leaf.DNSNames, "testhost") {
		t.Errorf("DNS SANs = %v, want localhost + testhost", leaf.DNSNames)
	}
	foundIP := false
	for _, ip := range leaf.IPAddresses {
		if ip.Equal(net.ParseIP("192.0.2.7")) {
			foundIP = true
		}
	}
	if !foundIP {
		t.Errorf("IP SANs = %v, want 192.0.2.7", leaf.IPAddresses)
	}
	if notAfter != leaf.NotAfter {
		t.Errorf("returned notAfter %v != leaf %v", notAfter, leaf.NotAfter)
	}
	// ~10 years: long on purpose, since rotation forces every pinned device to
	// re-enroll, so short-lived certs would be operator-hostile here.
	wantNotAfter := time.Now().Add(approverCertValidity)
	if d := leaf.NotAfter.Sub(wantNotAfter); d > 48*time.Hour || d < -48*time.Hour {
		t.Errorf("NotAfter %v not ~%v", leaf.NotAfter, wantNotAfter)
	}
	if !leaf.NotBefore.Before(time.Now().Add(-30 * time.Minute)) {
		t.Errorf("NotBefore %v lacks clock-skew backdating", leaf.NotBefore)
	}
	if runtime.GOOS != "windows" {
		fi, err := os.Stat(keyFile)
		if err != nil {
			t.Fatal(err)
		}
		if fi.Mode().Perm() != 0o600 {
			t.Errorf("key.pem mode = %v, want 0600", fi.Mode().Perm())
		}
	}
	pin1, err := SPKIPinFromCertFile(certFile)
	if err != nil || !strings.HasPrefix(pin1, "sha256/") {
		t.Fatalf("pin from minted cert = %q, %v", pin1, err)
	}

	// Second boot: load, never re-mint (pin stability).
	notAfter2, minted2, err := LoadOrMintApproverCert(certFile, keyFile, hosts)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if minted2 {
		t.Error("reload re-minted the pair")
	}
	if !notAfter2.Equal(notAfter) {
		t.Errorf("reload notAfter %v != %v", notAfter2, notAfter)
	}
	pin2, err := SPKIPinFromCertFile(certFile)
	if err != nil || pin2 != pin1 {
		t.Errorf("pin changed across boots: %q → %q (%v)", pin1, pin2, err)
	}

	// Half-present state fails closed with the re-enroll caution; silently
	// re-minting would rotate the pin under every enrolled device.
	if err := os.Remove(keyFile); err != nil {
		t.Fatal(err)
	}
	if _, _, err := LoadOrMintApproverCert(certFile, keyFile, hosts); err == nil ||
		!strings.Contains(err.Error(), "re-enroll") {
		t.Errorf("half pair: err = %v, want re-enroll caution", err)
	}

	// Corrupt state fails closed too.
	if err := os.WriteFile(keyFile, []byte("not a key"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := LoadOrMintApproverCert(certFile, keyFile, hosts); err == nil {
		t.Error("corrupt key: want error, got nil")
	}
}

// TestLoadOrMintExpiredPairStillLoads: an expired persisted pair loads (the
// caller warns; refusing to boot would brick the deployment, and re-minting
// would silently rotate the pin); expiry is surfaced via notAfter.
func TestLoadOrMintExpiredPairStillLoads(t *testing.T) {
	dir := t.TempDir()
	certFile := filepath.Join(dir, "cert.pem")
	keyFile := filepath.Join(dir, "key.pem")
	writeExpiredPair(t, certFile, keyFile)

	notAfter, minted, err := LoadOrMintApproverCert(certFile, keyFile, nil)
	if err != nil {
		t.Fatalf("expired pair refused: %v", err)
	}
	if minted {
		t.Error("expired pair was re-minted (pin rotation!)")
	}
	if !notAfter.Before(time.Now()) {
		t.Errorf("notAfter %v should read as expired", notAfter)
	}
}

func writeExpiredPair(t *testing.T, certFile, keyFile string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "expired"},
		NotBefore:    time.Now().Add(-48 * time.Hour),
		NotAfter:     time.Now().Add(-24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, &tmpl, &tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(certFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyFile, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}), 0o600); err != nil {
		t.Fatal(err)
	}
}

// TestPickAdvertiseIP pins the best-effort LAN-IP choice for the auto
// publicUrl: private IPv4 beats public, loopback and link-local never
// advertise (the caller falls back to 127.0.0.1 with a warning).
func TestPickAdvertiseIP(t *testing.T) {
	mk := func(ss ...string) []net.IP {
		var out []net.IP
		for _, s := range ss {
			out = append(out, net.ParseIP(s))
		}
		return out
	}
	cases := []struct {
		name string
		in   []net.IP
		want string
	}{
		{"private preferred over public", mk("203.0.113.5", "192.168.1.7"), "192.168.1.7"},
		{"first private wins", mk("10.0.0.4", "192.168.1.7"), "10.0.0.4"},
		{"public only", mk("203.0.113.5"), "203.0.113.5"},
		{"loopback never advertises", mk("127.0.0.1"), ""},
		{"link-local never advertises", mk("169.254.1.1"), ""},
		{"ipv6 skipped (v4 LAN story)", mk("2001:db8::1"), ""},
		{"empty", nil, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ""
			if ip := PickAdvertiseIP(tc.in); ip != nil {
				got = ip.String()
			}
			if got != tc.want {
				t.Errorf("PickAdvertiseIP(%v) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// TestEnsureApproverTLS pins the fill semantics: the auto path completes
// whatever the operator left unset, and explicit configuration always wins.
func TestEnsureApproverTLS(t *testing.T) {
	t.Run("auto fills everything", func(t *testing.T) {
		dir := t.TempDir()
		cfg := config.Config{DataDir: dir}
		cfg.Server.ApproverTLS.AutoMint = ptrBool(true)
		autoMinted, err := EnsureApproverTLS(&cfg, discardLog())
		if err != nil {
			t.Fatal(err)
		}
		if !autoMinted {
			t.Error("autoMinted = false")
		}
		at := cfg.Server.ApproverTLS
		if at.Listen != ":8443" {
			t.Errorf("listen = %q, want :8443", at.Listen)
		}
		wantCert := filepath.Join(dir, "approver-tls", "cert.pem")
		if at.CertFile != wantCert {
			t.Errorf("certFile = %q, want %q", at.CertFile, wantCert)
		}
		if _, err := os.Stat(at.CertFile); err != nil {
			t.Errorf("cert not minted: %v", err)
		}
		if _, err := os.Stat(at.KeyFile); err != nil {
			t.Errorf("key not minted: %v", err)
		}
		if !strings.HasPrefix(at.PublicURL, "https://") || !strings.HasSuffix(at.PublicURL, ":8443") {
			t.Errorf("publicUrl = %q, want https://<host>:8443", at.PublicURL)
		}
	})

	t.Run("nil knob is off", func(t *testing.T) {
		dir := t.TempDir()
		cfg := config.Config{DataDir: dir}
		autoMinted, err := EnsureApproverTLS(&cfg, discardLog())
		if err != nil || autoMinted {
			t.Fatalf("nil knob: autoMinted=%v err=%v, want false/nil", autoMinted, err)
		}
		if at := cfg.Server.ApproverTLS; at != (config.ApproverTLS{}) {
			t.Errorf("config touched: %+v", at)
		}
		if _, err := os.Stat(filepath.Join(dir, "approver-tls")); !os.IsNotExist(err) {
			t.Error("state dir created despite knob off")
		}
	})

	t.Run("explicit false is off", func(t *testing.T) {
		dir := t.TempDir()
		cfg := config.Config{DataDir: dir}
		cfg.Server.ApproverTLS.AutoMint = ptrBool(false)
		autoMinted, err := EnsureApproverTLS(&cfg, discardLog())
		if err != nil || autoMinted {
			t.Fatalf("false knob: autoMinted=%v err=%v", autoMinted, err)
		}
		if _, err := os.Stat(filepath.Join(dir, "approver-tls")); !os.IsNotExist(err) {
			t.Error("state dir created despite knob off")
		}
	})

	t.Run("full bring-your-own wins", func(t *testing.T) {
		dir := t.TempDir()
		cfg := config.Config{DataDir: dir}
		cfg.Server.ApproverTLS = config.ApproverTLS{
			Listen: "0.0.0.0:9000", CertFile: "c.pem", KeyFile: "k.pem",
			PublicURL: "https://ops.example:9000", AutoMint: ptrBool(true),
		}
		before := cfg.Server.ApproverTLS
		autoMinted, err := EnsureApproverTLS(&cfg, discardLog())
		if err != nil || autoMinted {
			t.Fatalf("bring-your-own: autoMinted=%v err=%v", autoMinted, err)
		}
		if cfg.Server.ApproverTLS != before {
			t.Errorf("explicit config rewritten: %+v", cfg.Server.ApproverTLS)
		}
		if _, err := os.Stat(filepath.Join(dir, "approver-tls")); !os.IsNotExist(err) {
			t.Error("minted despite full explicit config")
		}
	})

	t.Run("explicit publicUrl preserved", func(t *testing.T) {
		cfg := config.Config{DataDir: t.TempDir()}
		cfg.Server.ApproverTLS.AutoMint = ptrBool(true)
		cfg.Server.ApproverTLS.PublicURL = "https://phone.example:8443"
		if _, err := EnsureApproverTLS(&cfg, discardLog()); err != nil {
			t.Fatal(err)
		}
		if got := cfg.Server.ApproverTLS.PublicURL; got != "https://phone.example:8443" {
			t.Errorf("publicUrl rewritten to %q", got)
		}
		if cfg.Server.ApproverTLS.CertFile == "" || cfg.Server.ApproverTLS.Listen == "" {
			t.Error("rest of the quad not filled")
		}
	})

	t.Run("non-wildcard listen host flows into publicUrl", func(t *testing.T) {
		cfg := config.Config{DataDir: t.TempDir()}
		cfg.Server.ApproverTLS.AutoMint = ptrBool(true)
		cfg.Server.ApproverTLS.Listen = "127.0.0.1:9443"
		if _, err := EnsureApproverTLS(&cfg, discardLog()); err != nil {
			t.Fatal(err)
		}
		if got := cfg.Server.ApproverTLS.PublicURL; got != "https://127.0.0.1:9443" {
			t.Errorf("publicUrl = %q, want https://127.0.0.1:9443", got)
		}
	})

	t.Run("provided cert pair kept, rest filled", func(t *testing.T) {
		dir := t.TempDir()
		certFile := filepath.Join(dir, "own-cert.pem")
		keyFile := filepath.Join(dir, "own-key.pem")
		if _, _, err := LoadOrMintApproverCert(certFile, keyFile, []string{"localhost"}); err != nil {
			t.Fatal(err)
		}
		cfg := config.Config{DataDir: dir}
		cfg.Server.ApproverTLS.AutoMint = ptrBool(true)
		cfg.Server.ApproverTLS.CertFile = certFile
		cfg.Server.ApproverTLS.KeyFile = keyFile
		autoMinted, err := EnsureApproverTLS(&cfg, discardLog())
		if err != nil {
			t.Fatal(err)
		}
		if autoMinted {
			t.Error("autoMinted = true for an operator-provided pair")
		}
		if cfg.Server.ApproverTLS.CertFile != certFile {
			t.Errorf("certFile rewritten to %q", cfg.Server.ApproverTLS.CertFile)
		}
		if cfg.Server.ApproverTLS.Listen != ":8443" || cfg.Server.ApproverTLS.PublicURL == "" {
			t.Errorf("rest not filled: %+v", cfg.Server.ApproverTLS)
		}
		if _, err := os.Stat(filepath.Join(dir, "approver-tls")); !os.IsNotExist(err) {
			t.Error("minted a state pair despite operator-provided files")
		}
	})
}

// TestEnsureApproverTLSAdvertisedHost pins the contract the mobile app
// enforces: the minted pair must NAME the host the enroll QR advertises,
// because the app validates hostname against the pinned leaf (anchored
// trust, fail closed). A pre-existing pair is never rotated over a SAN gap;
// pin stability wins and the boot warning carries the manual re-mint step.
func TestEnsureApproverTLSAdvertisedHost(t *testing.T) {
	t.Run("configured advertise host lands in the SANs", func(t *testing.T) {
		cfg := config.Config{DataDir: t.TempDir()}
		cfg.Server.ApproverTLS.AutoMint = ptrBool(true)
		cfg.Server.ApproverTLS.PublicURL = "https://203.0.113.9:8443"
		if _, err := EnsureApproverTLS(&cfg, discardLog()); err != nil {
			t.Fatal(err)
		}
		leaf := readLeaf(t, cfg.Server.ApproverTLS.CertFile)
		if err := leaf.VerifyHostname("203.0.113.9"); err != nil {
			t.Errorf("minted cert does not name the advertised host: %v", err)
		}
	})

	t.Run("derived advertise host lands in the SANs", func(t *testing.T) {
		cfg := config.Config{DataDir: t.TempDir()}
		cfg.Server.ApproverTLS.AutoMint = ptrBool(true)
		if _, err := EnsureApproverTLS(&cfg, discardLog()); err != nil {
			t.Fatal(err)
		}
		u, err := url.Parse(cfg.Server.ApproverTLS.PublicURL)
		if err != nil {
			t.Fatal(err)
		}
		leaf := readLeaf(t, cfg.Server.ApproverTLS.CertFile)
		if err := leaf.VerifyHostname(u.Hostname()); err != nil {
			t.Errorf("minted cert does not name the derived host %s: %v", u.Hostname(), err)
		}
	})

	t.Run("stale pair warns but never rotates", func(t *testing.T) {
		dir := t.TempDir()
		state := filepath.Join(dir, "approver-tls")
		if err := os.MkdirAll(state, 0o700); err != nil {
			t.Fatal(err)
		}
		certFile := filepath.Join(state, "cert.pem")
		if _, _, err := LoadOrMintApproverCert(certFile, filepath.Join(state, "key.pem"), []string{"localhost"}); err != nil {
			t.Fatal(err)
		}
		before, err := os.ReadFile(certFile)
		if err != nil {
			t.Fatal(err)
		}
		cfg := config.Config{DataDir: dir}
		cfg.Server.ApproverTLS.AutoMint = ptrBool(true)
		cfg.Server.ApproverTLS.PublicURL = "https://203.0.113.9:8443"
		if _, err := EnsureApproverTLS(&cfg, discardLog()); err != nil {
			t.Fatal(err)
		}
		after, err := os.ReadFile(certFile)
		if err != nil {
			t.Fatal(err)
		}
		if !slices.Equal(before, after) {
			t.Error("SAN gap rotated the persisted pair; the pinned SPKI must survive")
		}
	})
}

// TestMintedCertValidityCeiling pins the Apple constraint: iOS trust
// evaluation refuses TLS leaves whose validity exceeds 825 days EVEN when
// anchored to a pinned custom certificate, so a longer-lived certificate
// fails every iPhone enrollment.
func TestMintedCertValidityCeiling(t *testing.T) {
	dir := t.TempDir()
	certFile := filepath.Join(dir, "cert.pem")
	if _, _, err := LoadOrMintApproverCert(certFile, filepath.Join(dir, "key.pem"), []string{"localhost"}); err != nil {
		t.Fatal(err)
	}
	leaf := readLeaf(t, certFile)
	if period := leaf.NotAfter.Sub(leaf.NotBefore); period > 825*24*time.Hour {
		t.Errorf("validity period %v exceeds Apple's 825-day ceiling for TLS leaves; iOS refuses the handshake", period)
	}
}
