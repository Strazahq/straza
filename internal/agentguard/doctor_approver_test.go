package agentguard

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// approverSurface starts a TLS listener standing in for the approver surface a
// phone dials, and returns its base URL plus the SPKI pin of the certificate it
// presents (the pin an enroll QR would carry).
func approverSurface(t *testing.T) (base, pin string) {
	t.Helper()
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	// The probe hangs up as soon as it has the certificate, which a TLS 1.3
	// server may still be mid-handshake for; that logs one benign line per
	// probe and would otherwise bury the test output.
	srv.Config.ErrorLog = log.New(io.Discard, "", 0)
	t.Cleanup(srv.Close)
	sum := sha256.Sum256(srv.Certificate().RawSubjectPublicKeyInfo)
	return srv.URL, "sha256/" + base64.StdEncoding.EncodeToString(sum[:])
}

// TestServerCheckApprover: `straza doctor` surfaces the approver surface the
// server advertises on /version (the https URL phones dial, the SPKI pin
// enroll QRs carry, the auto-minted cert path, and the expiry), and grades
// expiry with the re-enroll caution, because a rotated pin strands every
// enrolled device.
func TestServerCheckApprover(t *testing.T) {
	surface, pin := approverSurface(t)
	notAfter := time.Now().Add(9 * 365 * 24 * time.Hour).UTC().Format(time.RFC3339)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/healthz":
			w.WriteHeader(http.StatusOK)
		case "/version":
			fmt.Fprintf(w, `{"version":"test","profile":"standalone","approver":{
				"public_url":%q,"tls_spki_pin":%q,
				"cert_not_after":%q,"auto_minted":true,
				"cert_file":"/var/lib/straza/approver-tls/cert.pem"}}`, surface, pin, notAfter)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	check, approver := serverCheck(context.Background(), srv.URL)
	if check.Status != checkOK {
		t.Fatalf("server check = %+v", check)
	}
	if approver == nil {
		t.Fatal("approver block not surfaced from /version")
	}

	c := approverCheck(context.Background(), *approver)
	if c.Status != checkOK {
		t.Errorf("healthy approver check = %+v", c)
	}
	for _, want := range []string{surface, pin, "/var/lib/straza/approver-tls/cert.pem", "expires"} {
		if !strings.Contains(c.Detail, want) {
			t.Errorf("approver detail %q misses %q", c.Detail, want)
		}
	}

	// Expiring soon → warn; expired → fail. Both must spell out the
	// rotation-means-re-enroll reality.
	soon := *approver
	soon.CertNotAfter = time.Now().Add(10 * 24 * time.Hour).UTC().Format(time.RFC3339)
	if c := approverCheck(context.Background(), soon); c.Status != checkWarn || !strings.Contains(c.Hint, "re-enroll") {
		t.Errorf("expiring-soon check = %+v, want warn + re-enroll hint", c)
	}
	expired := *approver
	expired.CertNotAfter = time.Now().Add(-24 * time.Hour).UTC().Format(time.RFC3339)
	if c := approverCheck(context.Background(), expired); c.Status != checkFail || !strings.Contains(c.Hint, "re-enroll") {
		t.Errorf("expired check = %+v, want fail + re-enroll hint", c)
	}
}

// TestApproverCheckDialsTheSurface pins that the approver line verifies the
// three facts a phone enrollment stands on instead of taking them on the
// server's word. On the word alone, an unreachable listener and a rotated
// certificate would both read green. Doctor performs the phone's own check, a
// TLS handshake against the advertised URL with the pin computed off the
// presented leaf, and says which of the two it did.
func TestApproverCheckDialsTheSurface(t *testing.T) {
	surface, pin := approverSurface(t)
	notAfter := time.Now().Add(365 * 24 * time.Hour).UTC().Format(time.RFC3339)

	tests := []struct {
		name       string
		st         approverStatus
		wantStatus string
		wantDetail string
	}{
		{
			name:       "the surface presents the advertised pin",
			st:         approverStatus{PublicURL: surface, TLSSPKIPin: pin, CertNotAfter: notAfter},
			wantStatus: checkOK,
			wantDetail: "verified from here",
		},
		{
			// A re-mint that /version has not caught up with, an ingress
			// terminating TLS in front of the listener, or interception. Every
			// enrolled phone refuses this handshake, so it is loud.
			name:       "the surface presents a different key",
			st:         approverStatus{PublicURL: surface, TLSSPKIPin: "sha256/AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=", CertNotAfter: notAfter},
			wantStatus: checkFail,
			wantDetail: "DIFFERENT key",
		},
		{
			// Doctor runs on a box that is not on the phones' network. That is
			// not a broken deployment and must never fail the exit gate, but
			// it is not verification either.
			name:       "surface unreachable from here",
			st:         approverStatus{PublicURL: "https://127.0.0.1:1", TLSSPKIPin: pin, CertNotAfter: notAfter},
			wantStatus: checkWarn,
			wantDetail: "NOT verified from here",
		},
		{
			// Ingress-terminated (enroll tier 2): strazad holds no pin for a
			// certificate it does not serve, so there is nothing to compare and
			// reachability is all doctor may claim.
			name:       "no pin advertised: reachability only",
			st:         approverStatus{PublicURL: surface, CertNotAfter: notAfter},
			wantStatus: checkOK,
			wantDetail: "no pin advertised",
		},
		{
			name:       "no public URL advertised",
			st:         approverStatus{TLSSPKIPin: pin, CertNotAfter: notAfter},
			wantStatus: checkWarn,
			wantDetail: "no public URL",
		},
		{
			// Expiry is graded off the advertised fact and outranks the probe:
			// a reachable surface serving a dead certificate is still dead.
			name:       "expired beats a successful dial",
			st:         approverStatus{PublicURL: surface, TLSSPKIPin: pin, CertNotAfter: time.Now().Add(-time.Hour).UTC().Format(time.RFC3339)},
			wantStatus: checkFail,
			wantDetail: "EXPIRED",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			start := time.Now()
			got := approverCheck(context.Background(), tc.st)
			if elapsed := time.Since(start); elapsed > 2*probeTimeout {
				t.Errorf("approver probe took %s: it must stay bounded", elapsed)
			}
			if got.Status != tc.wantStatus || !strings.Contains(got.Detail, tc.wantDetail) {
				t.Fatalf("approver check = %+v, want %s mentioning %q", got, tc.wantStatus, tc.wantDetail)
			}
			if got.Status != checkOK && got.Hint == "" {
				t.Error("a non-ok check without a next step is noise")
			}
			// Green is only ever earned by a handshake: no ok line may exist
			// without the probe having said something positive about the
			// surface.
			if got.Status == checkOK && !strings.Contains(got.Detail, "verified from here") &&
				!strings.Contains(got.Detail, "reachable from here") {
				t.Errorf("ok without a dial: %q", got.Detail)
			}
		})
	}
}

// TestServerCheckNoApprover: a server that terminates TLS elsewhere (or
// predates the block) advertises nothing, so the doctor stays silent instead
// of inventing an "approver: missing" row for deployments that never wanted
// one.
func TestServerCheckNoApprover(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/healthz":
			w.WriteHeader(http.StatusOK)
		case "/version":
			fmt.Fprint(w, `{"version":"test","profile":"standalone"}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	check, approver := serverCheck(context.Background(), srv.URL)
	if check.Status != checkOK {
		t.Fatalf("server check = %+v", check)
	}
	if approver != nil {
		t.Errorf("approver = %+v, want nil", approver)
	}
}
