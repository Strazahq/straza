package agentguard

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/strazahq/straza/internal/agentguard/localapi"
)

// Doctor's DIALED checks stand on a connection doctor actually made, kept
// apart from doctor.go's local-state checks because three rules govern them.
//
//  1. Never green on configuration alone. A URL, pin or transport rendered
//     healthy must have been TOUCHED; an unsafe or meaningless probe says
//     "configured (not probed)" and grades itself down. Green means verified.
//  2. Bounded, always: every probe carries probeTimeout of its own, so `straza
//     doctor` finishes even on a box with a black-holed network.
//  3. Unreachable is WARN, never FAIL. A laptop off the approver's network is
//     not a broken deployment, but it verified nothing and must say so. FAIL
//     is for a surface that answered and answered WRONG.
//
// Nothing here mutates: the approver probe is a bare TLS handshake, and the
// broker probe is a TCP connect.

// probeTimeout bounds every dialed check in this file. Short on purpose: these
// surfaces are local, LAN, or one hop away, and doctor is run by a human
// waiting at a terminal.
const probeTimeout = 3 * time.Second

// approverStatus mirrors the /version approver block: the pinned mobile
// surface as strazad itself advertises it. Everything in it is public (URL
// and pin ride the enroll QR; expiry shows in the handshake).
type approverStatus struct {
	PublicURL    string `json:"public_url"`
	TLSSPKIPin   string `json:"tls_spki_pin"`
	CertNotAfter string `json:"cert_not_after"`
	AutoMinted   bool   `json:"auto_minted"`
	CertFile     string `json:"cert_file"`
}

// serverCheck probes /healthz and /version and estimates clock skew from the
// HTTP Date header (a skewed client clock makes every token look expired:
// the classic 'everything is denied and nothing makes sense' cause). The
// second return is the advertised approver surface, nil when the server
// predates it or terminates TLS elsewhere.
func serverCheck(ctx context.Context, base string) (Check, *approverStatus) {
	client := &http.Client{Timeout: 5 * time.Second}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/healthz", nil)
	if err != nil {
		return Check{"server", checkFail, err.Error(), "check the server URL in straza config"}, nil
	}
	resp, err := client.Do(req)
	if err != nil {
		return Check{"server", checkFail, fmt.Sprintf("%s unreachable: %v", base, err),
			"is strazad running? verify the URL and any TLS/proxy in between; hooks fail closed while it is unreachable (grace TTL permitting)"}, nil
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return Check{"server", checkFail, fmt.Sprintf("/healthz returned %d", resp.StatusCode),
			"the server is up but unhealthy. Check strazad logs"}, nil
	}
	detail := base + " healthy"
	var version struct {
		Version  string          `json:"version"`
		Profile  string          `json:"profile"`
		Approver *approverStatus `json:"approver"`
	}
	if vreq, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/version", nil); err == nil {
		if vresp, err := client.Do(vreq); err == nil {
			_ = json.NewDecoder(vresp.Body).Decode(&version)
			_ = vresp.Body.Close()
			if version.Version != "" {
				detail = fmt.Sprintf("%s healthy (strazad %s, %s profile)", base, version.Version, version.Profile)
			}
		}
	}
	if date := resp.Header.Get("Date"); date != "" {
		if serverTime, err := http.ParseTime(date); err == nil {
			skew := time.Since(serverTime)
			if skew < 0 {
				skew = -skew
			}
			if skew > 30*time.Second {
				return Check{"server", checkWarn,
					fmt.Sprintf("%s (clock skew ~%s vs server)", detail, skew.Round(time.Second)),
					"fix this machine's clock (NTP): tokens verify with only 30 s of skew tolerance"}, version.Approver
			}
		}
	}
	return Check{"server", checkOK, detail, ""}, version.Approver
}

// approverCheck names the facts a phone enrollment stands on: the https URL
// the QR advertises, the SPKI pin the app trusts, and the certificate behind
// them (path when auto-minted, expiry always). Expiry warns EARLY because
// rotation is not routine here: a rotated pin strands every enrolled device
// until it re-enrolls.
//
// Every one of those facts arrives as the server's claim on /version. Printed
// as health, an approver listener no phone can reach and a certificate
// re-minted underneath the advertised pin would both read green. So doctor
// performs the phone's own check (a TLS handshake against the advertised URL
// with the pin computed off the leaf it presents), and the line says which of
// the two facts it verified.
func approverCheck(ctx context.Context, st approverStatus) Check {
	facts := []string{}
	if st.TLSSPKIPin != "" {
		facts = append(facts, "pin "+st.TLSSPKIPin)
	} else {
		facts = append(facts, "no pin advertised (TLS is terminated in front of strazad, so phones trust that certificate's own CA)")
	}
	if st.AutoMinted && st.CertFile != "" {
		facts = append(facts, "auto-minted cert "+st.CertFile)
	}
	exp, expErr := time.Parse(time.RFC3339, st.CertNotAfter)
	if expErr == nil {
		facts = append(facts, "expires "+exp.Format("2006-01-02"))
	}

	probe := probeApproverSurface(ctx, st.PublicURL, st.TLSSPKIPin)
	facts = append(facts, probe.note)
	where := st.PublicURL
	if where == "" {
		where = "approver surface"
	}
	detail := where + " (" + strings.Join(facts, ", ") + ")"

	switch {
	case expErr == nil && time.Now().After(exp):
		return Check{"approver", checkFail, detail + ": certificate EXPIRED",
			"approver apps may refuse the handshake. Replace the pair (auto-minted: delete the approver-tls dir on the server and reboot strazad to re-mint), then re-enroll EVERY approver device. Pinned trust does not survive rotation"}
	case probe.status == checkFail:
		return Check{"approver", checkFail, detail, probe.hint}
	case expErr == nil && time.Until(exp) < 30*24*time.Hour:
		return Check{"approver", checkWarn, detail + ": certificate expires soon",
			"plan the rotation now: replacing the pair rotates the SPKI pin, so every enrolled approver device must re-enroll"}
	case probe.status == checkWarn:
		return Check{"approver", checkWarn, detail, probe.hint}
	}
	return Check{"approver", checkOK, detail, ""}
}

// approverProbe is what a handshake against the advertised approver URL proved,
// as a phrase for the check detail plus the grade it argues for.
type approverProbe struct {
	note   string
	status string
	hint   string
}

// probeApproverSurface dials the advertised approver URL exactly as an approver
// app does (TLS handshake, leaf SPKI pin, nothing sent) and reports what it
// saw. It NEVER speaks HTTP over the connection: doctor has no business sending
// a request to the phone's surface, and the handshake alone answers both
// questions (is it there, and is it the key the QR promises).
func probeApproverSurface(ctx context.Context, rawURL, wantPin string) approverProbe {
	if rawURL == "" {
		return approverProbe{"no public URL advertised (enroll QRs carry no server for phones to dial)", checkWarn,
			"set server.approverTLS.publicUrl (or approverPublicUrl behind an ingress) and restart strazad: without it the QR has no address on it"}
	}
	u, err := url.Parse(rawURL)
	if err != nil || u.Host == "" || u.Scheme != "https" {
		return approverProbe{"not probed: only an https URL can be dialed for its pin", checkWarn,
			"the approver surface must be https (phones pin its key). Check server.approverTLS.publicUrl"}
	}
	gotPin, dialErr := dialSPKIPin(ctx, u)
	switch {
	case dialErr != nil:
		return approverProbe{"NOT verified from here: " + dialErr.Error(), checkWarn,
			"phones dial this URL from THEIR network. If this machine is not on it, that is expected and nothing is wrong. If it should be reachable, check the listener, DNS and any firewall between: an approver that cannot reach it cannot approve"}
	case wantPin == "":
		return approverProbe{"reachable from here (TLS handshake ok); no advertised pin to compare", checkOK, ""}
	case gotPin != wantPin:
		return approverProbe{"presents a DIFFERENT key (" + gotPin + ")", checkFail,
			"every device enrolled on the advertised pin will refuse this handshake. Either the certificate was replaced without the pin following it (re-enroll every approver device), or something terminates TLS in front of the listener (from here at least). Compare with what a phone sees before rotating anything"}
	}
	return approverProbe{"verified from here: the surface presents the pinned key", checkOK, ""}
}

// dialSPKIPin handshakes with an https endpoint and returns the HPKP-style
// "sha256/<base64>" pin over the leaf's SubjectPublicKeyInfo: the same value
// strazad computes for the enroll QR (internal/server/enrollqr.go,
// spkiPinFromCertFile) and the same one the approver app pins.
func dialSPKIPin(ctx context.Context, u *url.URL) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()
	host := u.Host
	if u.Port() == "" {
		host = net.JoinHostPort(u.Hostname(), "443")
	}
	dialer := &tls.Dialer{
		NetDialer: &net.Dialer{Timeout: probeTimeout},
		// Skipping CA verification is the POINT here, not a shortcut: the
		// approver surface is pin-trusted by design (typically a self-signed
		// auto-minted pair no CA vouches for, exactly as the phone sees it), and
		// this probe's whole job is to verify that pin against what /version
		// advertises; a CA check would fail the healthy case and prove nothing
		// about the key phones actually pinned. Nothing is sent over the
		// connection: no request, no credential, no session token, so there is
		// nothing for an impostor on that address to receive. The one claim
		// derived from it is the pin comparison in probeApproverSurface.
		Config: &tls.Config{
			InsecureSkipVerify: true, // #nosec G402 -- pin verification, not CA trust; handshake only, nothing sent
			ServerName:         u.Hostname(),
			MinVersion:         tls.VersionTLS12,
		},
	}
	conn, err := dialer.DialContext(ctx, "tcp", host)
	if err != nil {
		return "", err
	}
	defer func() { _ = conn.Close() }()
	tlsConn, ok := conn.(*tls.Conn)
	if !ok {
		return "", fmt.Errorf("not a TLS connection")
	}
	certs := tlsConn.ConnectionState().PeerCertificates
	if len(certs) == 0 {
		return "", fmt.Errorf("the surface presented no certificate")
	}
	sum := sha256.Sum256(certs[0].RawSubjectPublicKeyInfo)
	return "sha256/" + base64.StdEncoding.EncodeToString(sum[:]), nil
}

// killswitchCheck reports the revocation path of the caller's sessions: the
// push stream the Straza background service holds per server session.
// Revocation speed is a security property, so a session whose stream is not
// connected is a WARN that names the real bound. Warn, never fail: hooks
// fail closed without push; only the revocation SPEED degrades. nil when no
// session is live at now. A revoked or lapsed window holds no stream by
// design, so it is not counted. serverReachable comes from the server check
// above.
func killswitchCheck(sessions []localapi.SessionStatus, now time.Time, serverReachable bool) *Check {
	var live []localapi.SessionStatus
	for _, s := range sessions {
		if s.Revoked == "" && now.Before(s.ExpiresAt) {
			live = append(live, s)
		}
	}
	if len(live) == 0 {
		return nil
	}
	const bounded = "revocation then waits for the session's next renewal, at most its token lifetime"
	var waiting []string
	for _, s := range live {
		if s.Push != localapi.PushConnected {
			waiting = append(waiting, fmt.Sprintf("%s %s: %s", harnessName(s.Harness), s.SessionID, s.Push))
		}
	}
	if len(waiting) == 0 {
		return &Check{"killswitch", checkOK,
			fmt.Sprintf("push stream connected for all %d session(s): a revocation reaches this machine within a second", len(live)), ""}
	}
	detail := fmt.Sprintf("push stream connected for %d of %d session(s) (%s)",
		len(live)-len(waiting), len(live), strings.Join(waiting, ", "))
	if !serverReachable {
		return &Check{"killswitch", checkWarn, detail,
			"fix the server check first: with strazad unreachable there is no push stream to hold, and " + bounded}
	}
	return &Check{"killswitch", checkWarn, detail,
		"the service reconnects on its own. If a stream stays down, something between this machine and strazad refuses or drops it (a proxy that buffers or times out SSE is the usual cause), and " + bounded}
}
