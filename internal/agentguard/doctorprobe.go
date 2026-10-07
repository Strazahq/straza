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

	"github.com/strazahq/straza/internal/sameorigin"
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
// Nothing here mutates: the approver probe is a bare TLS handshake, the push
// probe closes its subscription the instant the status is known, and the
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

// killswitchCheck reports the revocation path this session actually has.
// Revocation speed is a security property, so "configured" is not "connected"
// and the transport is probed rather than printed:
//
//   - gateway edge: the subscription the daemon itself opens, closed the moment
//     the status is known. 200 means this session can hold the lane; 404/405 is
//     a server with no lane at all; 503 is a lane that is down or full.
//
// serverReachable comes from the server check above: after that failed, a
// second timeout teaches nothing. daemon is the straza home's heartbeat
// verdict (heartbeat.go): these probes prove the lane accepts a subscriber,
// only a fresh heartbeat proves one is subscribed, so a verified lane nobody
// holds is graded by killswitchVerified, never called green on its own.
// credentialDead comes from the identity check: an expired session whose
// credential is dead cannot refresh, so the hint says enroll, not refresh.
func killswitchCheck(ctx context.Context, serverURL string, ses Session, serverReachable, credentialDead bool, daemon daemonLiveness) Check {
	endpoint := strings.TrimRight(serverURL, "/") + "/v1/push"
	detail := "edge push (SSE via " + endpoint + ")"
	pollFallback := "revocation falls back to poll-refresh, bounded by the poll interval, else token TTL"
	switch {
	case !serverReachable:
		return Check{"killswitch", checkWarn, detail + ". NOT verified from here: the server check above is failing",
			"fix the server check first: with strazad unreachable there is no push lane to hold, and " + pollFallback}
	case time.Now().After(ses.ExpiresAt) && credentialDead:
		return Check{"killswitch", checkWarn, detail + ". NOT verified: this session's token has expired and cannot refresh",
			"run `straza enroll` again (the identity check above says why), start a harness session, then re-run `straza doctor` to verify the push lane"}
	case time.Now().After(ses.ExpiresAt):
		return Check{"killswitch", checkWarn, detail + ". NOT verified: this session's token has expired",
			"the next hook call refreshes the session; re-run `straza doctor` afterwards to verify the push lane"}
	}
	status, err := probeEdgePush(ctx, serverURL, ses.SessionToken)
	switch {
	case err != nil:
		return Check{"killswitch", checkWarn, detail + ". NOT verified from here: " + err.Error(),
			"something between this machine and strazad refused or dropped the stream (a proxy that buffers or times out SSE is the usual cause); until it holds, " + pollFallback}
	case status == http.StatusOK:
		return killswitchVerified(detail+": verified from here (the server accepted a push subscription)", daemon)
	case status == http.StatusNotFound || status == http.StatusMethodNotAllowed:
		return Check{"killswitch", checkWarn, detail + ". This strazad has NO edge push lane (pre-3.1 server)",
			"upgrade strazad to 3.1 or later for sub-second revocation; on this one, " + pollFallback}
	case status == http.StatusServiceUnavailable:
		return Check{"killswitch", checkWarn, detail + ". The server reports the push lane unavailable (down, or at capacity)",
			"check strazad's event bus: its push subscription failed at boot, or this pod is at events.pushEdgeMaxConns. Meanwhile " + pollFallback}
	case status == http.StatusUnauthorized:
		return Check{"killswitch", checkWarn, detail + ". The server rejected this session token, so the lane is NOT verified",
			"the session or its principal may be revoked. See the session check above and start a fresh harness session"}
	}
	return Check{"killswitch", checkWarn, fmt.Sprintf("%s. NOT verified: the server answered %d", detail, status),
		"an unexpected status on the push lane usually means a proxy answered instead of strazad; until it does, " + pollFallback}
}

// killswitchVerified grades a push lane doctor DID verify by the daemon
// heartbeat. Green must mean "revocation will actually arrive sub-second",
// and the lane answering is necessary but not sufficient for that: the probe
// proves the lane accepts a subscriber, the heartbeat proves one is
// subscribed. A verified lane with no live daemon is therefore a WARN naming
// the real bound, the same rule that has the lane itself probed:
// a comfortable claim nobody checked is the failure mode, whichever layer it
// hides in. Warn, never fail: hooks fail closed without a daemon; only the
// revocation SPEED degrades.
func killswitchVerified(detail string, daemon daemonLiveness) Check {
	const bounded = "revocation waits for the next hook call's poll-refresh, else token TTL"
	switch daemon.state {
	case heartbeatFresh:
		return Check{"killswitch", checkOK,
			fmt.Sprintf("%s; daemon alive (pid %d): sub-second revocation", detail, daemon.pid), ""}
	case heartbeatStale:
		return Check{"killswitch", checkWarn,
			fmt.Sprintf("%s, but the daemon looks GONE: its last heartbeat (pid %d) is %s old against a %s write interval",
				detail, daemon.pid, daemon.age.Round(time.Second), daemon.interval),
			"the daemon stopped without cleaning up (crash, kill, closed lid) or is wedged; nothing is subscribed to hear a push, so " + bounded + ". Restart `straza daemon`"}
	case heartbeatCorrupt:
		return Check{"killswitch", checkWarn,
			detail + ", but the daemon heartbeat is unreadable, so a live daemon cannot be confirmed",
			"restart `straza daemon` (it rewrites the heartbeat); until one is confirmed, assume " + bounded}
	case heartbeatFuture:
		return Check{"killswitch", checkWarn,
			fmt.Sprintf("%s, but the daemon heartbeat is dated %s in the FUTURE, so a live daemon cannot be confirmed",
				detail, (-daemon.age).Round(time.Second)),
			"this machine's clock moved backward under the stamp (a restored VM snapshot, or the clock was set back) and the daemon that wrote it may be gone. Restart `straza daemon`. A live one re-stamps with the current clock on its next tick; until then assume " + bounded}
	}
	return Check{"killswitch", checkWarn,
		detail + ", but no daemon heartbeat exists in this straza home: no daemon is subscribed to hear a push",
		"start `straza daemon` (and keep it running) for sub-second revocation; without one " + bounded}
}

// probeEdgePush opens the daemon's own subscription and closes it as soon as
// the status line is in. The body is never read: /v1/push STREAMS, so a client
// with an overall timeout would kill the HEALTHY case, and reading a byte more
// than this would make doctor a push subscriber.
func probeEdgePush(ctx context.Context, base, token string) (int, error) {
	ctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(base, "/")+"/v1/push", nil)
	if err != nil {
		return 0, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "text/event-stream")
	client := &http.Client{CheckRedirect: sameorigin.Check, Transport: &http.Transport{ResponseHeaderTimeout: probeTimeout}}
	resp, err := client.Do(req)
	if err != nil {
		return 0, err
	}
	_ = resp.Body.Close()
	return resp.StatusCode, nil
}
