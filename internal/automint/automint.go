package automint

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/pem"
	"fmt"
	"log/slog"
	"math/big"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"time"

	"github.com/strazahq/straza/internal/config"
)

// Approver TLS auto-mint: a fresh standalone boot serves the https+pin-only
// approver surface with zero TLS configuration (download the app, scan the
// QR, it works). The phone pins the leaf's SPKI from the enroll QR, so a
// self-signed certificate is a full-strength trust anchor and CA trust is
// irrelevant. SAN content is NOT, because the app still validates hostname
// against the pinned leaf (anchored trust, fail closed). Explicit
// server.approverTLS fields always win, and auto-mint only fills the gaps.

const (
	// defaultApproverListen is the auto-mint bind address.
	defaultApproverListen = ":8443"
	// approverCertValidity sits under Apple's 825-day ceiling for TLS leaf
	// certificates: iOS trust evaluation enforces the ceiling even for
	// pinned custom anchors, so a longer-lived certificate fails every iPhone
	// enrollment. The margin absorbs the one-hour NotBefore backdate. Rotation
	// forces re-enrollment, so the cadence stays over two years and the 30-day
	// boot warning covers the tail.
	approverCertValidity = 820 * 24 * time.Hour
	// approverCertDirName under dataDir holds the persisted pair.
	approverCertDirName = "approver-tls"
)

// CertInfo describes the TLS leaf behind the pinned approver surface, for
// the boot log, /version, straza doctor, and the phone-enroll guard.
type CertInfo struct {
	CertFile   string
	NotAfter   time.Time
	AutoMinted bool
	// Leaf is the parsed certificate; nil when unparseable (a display and
	// guard gap, never an error: the pair is already serving).
	Leaf *x509.Certificate
}

// LeafOf returns the parsed leaf of an already-loaded pair. Go's
// LoadX509KeyPair populates Leaf since 1.23; the parse fallback keeps this
// safe against that ever regressing. Nil = unparseable (already serving,
// so purely a display gap, not an error).
func LeafOf(cert tls.Certificate) *x509.Certificate {
	if cert.Leaf != nil {
		return cert.Leaf
	}
	if len(cert.Certificate) > 0 {
		if leaf, err := x509.ParseCertificate(cert.Certificate[0]); err == nil {
			return leaf
		}
	}
	return nil
}

// LeafNotAfter reads the leaf's expiry from an already-loaded pair; zero
// time when unparseable.
func LeafNotAfter(cert tls.Certificate) time.Time {
	if leaf := LeafOf(cert); leaf != nil {
		return leaf.NotAfter
	}
	return time.Time{}
}

// EnsureApproverTLS fills the unset half of server.approverTLS when
// auto-mint is on: first-boot self-signed pair persisted under
// <dataDir>/approver-tls, listen :8443, best-effort LAN publicUrl. The
// returned bool reports whether the auto-managed pair is in service (for the
// boot log and /version).
func EnsureApproverTLS(cfg *config.Config, log *slog.Logger) (bool, error) {
	at := &cfg.Server.ApproverTLS
	if !at.AutoMintEnabled() {
		return false, nil
	}
	if at.Listen != "" && at.CertFile != "" && at.KeyFile != "" && at.PublicURL != "" {
		return false, nil // fully bring-your-own: nothing to fill
	}
	if at.Listen == "" {
		at.Listen = defaultApproverListen
	}
	// The advertised URL resolves BEFORE the mint so the pair can NAME its
	// host: the app validates hostname against the pinned leaf, so a pair
	// that omits the advertised host fails every scan on iOS.
	if at.PublicURL == "" {
		host := advertiseHostFromListen(at.Listen)
		if host == "" {
			if ip := PickAdvertiseIP(localIPs()); ip != nil {
				host = ip.String()
			} else {
				host = "127.0.0.1"
				log.Warn("approver publicUrl autodetect found no LAN IPv4. Advertising 127.0.0.1",
					"fix", "set server.approverTLS.publicUrl to the https URL the phone should dial")
			}
		}
		_, port, err := net.SplitHostPort(at.Listen)
		if err != nil {
			return false, fmt.Errorf("server.approverTLS.listen %q: %w", at.Listen, err)
		}
		at.PublicURL = "https://" + net.JoinHostPort(host, port)
	}
	advHost := ""
	if u, err := url.Parse(at.PublicURL); err == nil {
		advHost = u.Hostname()
	}
	autoMinted := false
	if at.CertFile == "" { // and KeyFile: Validate pins the pair rule
		dir := filepath.Join(cfg.DataDir, approverCertDirName)
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return false, fmt.Errorf("approver TLS state dir: %w", err)
		}
		certFile := filepath.Join(dir, "cert.pem")
		keyFile := filepath.Join(dir, "key.pem")
		notAfter, minted, err := LoadOrMintApproverCert(certFile, keyFile, approverCertHosts(advHost))
		if err != nil {
			return false, err
		}
		at.CertFile, at.KeyFile = certFile, keyFile
		autoMinted = true
		if !minted && advHost != "" && !certFileNamesHost(certFile, advHost) {
			log.Warn("persisted approver TLS certificate does not name the advertised host; the phone app refuses that handshake after scanning the QR",
				"cert", certFile, "host", advHost,
				"fix", "delete the approver-tls dir to re-mint at next boot; re-minting rotates the SPKI pin, so enrolled approver devices must re-enroll")
		}
		switch {
		case minted:
			log.Info("approver TLS pair minted", "cert", certFile, "expires", notAfter.Format(time.RFC3339))
		case time.Now().After(notAfter):
			log.Warn("auto-minted approver TLS certificate has EXPIRED. The app may refuse the handshake",
				"cert", certFile, "expired", notAfter.Format(time.RFC3339),
				"fix", "delete the approver-tls dir to re-mint at next boot; re-minting rotates the SPKI pin, so enrolled approver devices must re-enroll")
		case time.Until(notAfter) < 30*24*time.Hour:
			log.Warn("auto-minted approver TLS certificate expires soon",
				"cert", certFile, "expires", notAfter.Format(time.RFC3339),
				"fix", "delete the approver-tls dir to re-mint at next boot; re-minting rotates the SPKI pin, so enrolled approver devices must re-enroll")
		default:
			log.Info("approver TLS pair loaded", "cert", certFile, "expires", notAfter.Format(time.RFC3339))
		}
	}
	return autoMinted, nil
}

// LoadOrMintApproverCert loads the persisted approver pair, minting a
// self-signed ECDSA P-256 leaf on first boot. It NEVER overwrites existing
// state: the phone pins this key's SPKI, so silent rotation would strand
// every enrolled device; unusable or half-present state fails the boot with
// the re-enroll caution instead. Returns the leaf's NotAfter (expiry is the
// caller's warning to raise, not an error: refusing to boot would brick the
// deployment) and whether a fresh pair was minted.
func LoadOrMintApproverCert(certFile, keyFile string, hosts []string) (time.Time, bool, error) {
	_, cErr := os.Stat(certFile)
	_, kErr := os.Stat(keyFile)
	switch {
	case cErr == nil && kErr == nil:
		cert, err := tls.LoadX509KeyPair(certFile, keyFile)
		if err != nil {
			return time.Time{}, false, fmt.Errorf(
				"persisted approver TLS pair is unusable (%w). Delete %s and %s to re-mint at next boot; re-minting rotates the SPKI pin, so enrolled approver devices must re-enroll",
				err, certFile, keyFile)
		}
		leaf := cert.Leaf
		if leaf == nil {
			if leaf, err = x509.ParseCertificate(cert.Certificate[0]); err != nil {
				return time.Time{}, false, fmt.Errorf("parse %s: %w", certFile, err)
			}
		}
		return leaf.NotAfter, false, nil
	case cErr == nil || kErr == nil:
		present, missing := certFile, keyFile
		if kErr == nil {
			present, missing = keyFile, certFile
		}
		return time.Time{}, false, fmt.Errorf(
			"approver TLS state is half-present: %s exists but %s is missing. Restore it, or delete both to re-mint at next boot (re-minting rotates the SPKI pin; enrolled approver devices must re-enroll)",
			present, missing)
	}

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return time.Time{}, false, fmt.Errorf("mint approver key: %w", err)
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return time.Time{}, false, fmt.Errorf("mint approver serial: %w", err)
	}
	tmpl := x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: "straza approver (auto-minted)"},
		// Backdated so a modest clock skew on the phone never rejects a
		// freshly minted certificate.
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(approverCertValidity),
		KeyUsage:              x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
	}
	for _, h := range hosts {
		if ip := net.ParseIP(h); ip != nil {
			tmpl.IPAddresses = append(tmpl.IPAddresses, ip)
		} else {
			tmpl.DNSNames = append(tmpl.DNSNames, h)
		}
	}
	der, err := x509.CreateCertificate(rand.Reader, &tmpl, &tmpl, &key.PublicKey, key)
	if err != nil {
		return time.Time{}, false, fmt.Errorf("mint approver cert: %w", err)
	}
	// Report what the DER actually says (second precision), not the
	// sub-second template value; future boots load the parsed form.
	minted, err := x509.ParseCertificate(der)
	if err != nil {
		return time.Time{}, false, fmt.Errorf("re-parse minted approver cert: %w", err)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return time.Time{}, false, fmt.Errorf("encode approver key: %w", err)
	}
	// Key first: a crash between the writes reads as half-present state next
	// boot (an explicit, actionable error), never as a silently rotated pin.
	if err := os.WriteFile(keyFile, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}), 0o600); err != nil {
		return time.Time{}, false, fmt.Errorf("persist approver key: %w", err)
	}
	if err := os.WriteFile(certFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o644); err != nil { // #nosec G306 -- public certificate, world-readable on purpose
		return time.Time{}, false, fmt.Errorf("persist approver cert: %w", err)
	}
	return minted.NotAfter, true, nil
}

// approverCertHosts is the SAN set for the minted leaf: the local names
// plus advHost, the host the enroll QR advertises. The app pins the SPKI
// but still validates hostname against the pinned leaf (anchored trust,
// fail closed), so the advertised host is load-bearing, not cosmetic.
func approverCertHosts(advHost string) []string {
	hosts := []string{"localhost", "127.0.0.1", "::1"}
	if hn, err := os.Hostname(); err == nil && hn != "" {
		hosts = append(hosts, hn)
	}
	for _, ip := range localIPs() {
		if v4 := ip.To4(); v4 != nil && !v4.IsLinkLocalUnicast() && !v4.IsLoopback() {
			hosts = append(hosts, v4.String())
		}
	}
	if advHost != "" && !slices.Contains(hosts, advHost) {
		hosts = append(hosts, advHost)
	}
	return hosts
}

// certFileNamesHost reports whether the PEM leaf at path names host. Parse
// failures report true: the boot warning must not cry wolf over state that
// another path already rejects with a real error.
func certFileNamesHost(path, host string) bool {
	raw, err := os.ReadFile(path) // #nosec G304 -- the pair automint itself persisted
	if err != nil {
		return true
	}
	block, _ := pem.Decode(raw)
	if block == nil {
		return true
	}
	leaf, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return true
	}
	return leaf.VerifyHostname(host) == nil
}

// advertiseHostFromListen returns the listen host when it names a concrete
// address the phone could dial; "" for wildcards (autodetect instead).
func advertiseHostFromListen(listen string) string {
	host, _, err := net.SplitHostPort(listen)
	if err != nil || host == "" || host == "0.0.0.0" || host == "::" {
		return ""
	}
	return host
}

// PickAdvertiseIP chooses the best-effort LAN IPv4 for the auto publicUrl:
// private (RFC 1918) first, then any global unicast v4. Loopback,
// link-local, and IPv6 never advertise; nil means the caller falls back to
// 127.0.0.1 with a warning.
func PickAdvertiseIP(ips []net.IP) net.IP {
	var public net.IP
	for _, ip := range ips {
		v4 := ip.To4()
		if v4 == nil || v4.IsLoopback() || v4.IsLinkLocalUnicast() {
			continue
		}
		if v4.IsPrivate() {
			return v4
		}
		if public == nil {
			public = v4
		}
	}
	return public
}

// localIPs enumerates addresses on up, non-loopback interfaces.
func localIPs() []net.IP {
	ifaces, err := net.Interfaces()
	if err != nil {
		return nil
	}
	var out []net.IP
	for _, ifc := range ifaces {
		if ifc.Flags&net.FlagUp == 0 || ifc.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, err := ifc.Addrs()
		if err != nil {
			continue
		}
		for _, a := range addrs {
			if ipn, ok := a.(*net.IPNet); ok {
				out = append(out, ipn.IP)
			}
		}
	}
	return out
}

// SPKIPinFromCertFile reads a PEM certificate file and returns the HPKP-style
// pin "sha256/<base64>" over the leaf certificate's SubjectPublicKeyInfo, the
// value the mobile approver app pins strazad's (often private-PKI) TLS key to
// during QR enrolment. It reads the FIRST CERTIFICATE block (the leaf; a bundle
// may append intermediates).
func SPKIPinFromCertFile(path string) (string, error) {
	raw, err := os.ReadFile(path) // #nosec G304 -- operator-configured TLS cert path (same file server.tls loads)
	if err != nil {
		return "", err
	}
	for {
		var block *pem.Block
		block, raw = pem.Decode(raw)
		if block == nil {
			return "", fmt.Errorf("no CERTIFICATE block in %s", path)
		}
		if block.Type != "CERTIFICATE" {
			continue
		}
		cert, err := x509.ParseCertificate(block.Bytes)
		if err != nil {
			return "", err
		}
		sum := sha256.Sum256(cert.RawSubjectPublicKeyInfo)
		return "sha256/" + base64.StdEncoding.EncodeToString(sum[:]), nil
	}
}
