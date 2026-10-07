package server

import (
	"strings"
)

// The enroll-QR contract: the exact payload the mobile approver
// app scans, the precedence ladder that picks the endpoint it names, and the
// rule deciding whether that payload may carry a TLS pin. Kept together and
// apart from the approver handlers because these three are one decision: a QR
// whose host and pin disagree bricks every phone that scans it.

// qrPayload is the exact object the enroll QR encodes: a
// compact, deterministically-ordered {v, servers, token, pin, project, fcm,
// webpush}. Pin is omitted entirely, key and all, whenever no pin applies to
// Servers (public-CA trust assumed): plaintext, or an endpoint whose key
// strazad did not mint. Project lets the app confirm "Enrolling to: <name>"
// pre-pair and key multi-backend enrollments; name is a label, id is the key.
// Fcm (a pointer, so an unconfigured deployment drops the key rather than
// emitting an empty object) is the BYO-Firebase public app config, see
// fcmAppConfig. Webpush (same pointer idiom) is the deployment's VAPID public
// key. Both are additive, so v stays 1.
//
// Field order is fixed by the struct declaration, so json.Marshal is
// byte-stable; new fields append LAST so payloads without them keep their
// exact prior bytes.
type qrPayload struct {
	V       int           `json:"v"`
	Servers []string      `json:"servers"`
	Token   string        `json:"token"`
	Pin     string        `json:"pin,omitempty"`
	Project projectRef    `json:"project"`
	FCM     *fcmAppConfig `json:"fcm,omitempty"`
	Webpush *webpushQR    `json:"webpush,omitempty"`
}

// fcmAppConfig is the deployment's PUBLIC Firebase app config the enroll
// payload hands the phone (BYO-Firebase: the play-flavor app ships NO
// google-services.json and initializes the default FirebaseApp at runtime
// from these values). All four values are public identifiers, not secrets
// (config.FCMPush documents why); the FCM service-account credential never
// rides any payload. Absent fcm ⇒ the app
// stays on the UnifiedPush/ntfy lane; FCM is per-deployment opt-in, never a
// default.
type fcmAppConfig struct {
	ProjectID string `json:"project_id"`
	AppID     string `json:"app_id"`
	APIKey    string `json:"api_key"`
	SenderID  string `json:"sender_id"`
}

// enrollFCM returns the fcm object the enroll payload/QR advertises, or nil
// when the deployment carries no full public app config. Presence-gated on
// the config set, deliberately NOT on fcm.enabled (the server-side sender):
// a deployment can stage the app config for enrolling phones before the
// sender goes live. config validation enforces all-or-nothing, and this
// helper re-checks the full set so a partial config can never emit a half
// object (fail closed).
func (a *App) enrollFCM() *fcmAppConfig {
	f := a.cfg.Approval.Push.FCM
	if !f.AppConfigured() {
		return nil
	}
	return &fcmAppConfig{ProjectID: f.ProjectID, AppID: f.AppID, APIKey: f.APIKey, SenderID: f.SenderID}
}

// webpushQR is the WebPush advert the enroll payload hands the phone: the
// deployment's VAPID public key (RFC 8292; public by construction, it rides
// every push Authorization header). The app hands it to its UnifiedPush
// distributor at REGISTER (spec 3); a browser passes it as
// pushManager.subscribe's applicationServerKey.
type webpushQR struct {
	VAPIDPublicKey string `json:"vapid_public_key"`
}

// enrollWebpush returns the webpush object the enroll payload/QR advertises,
// or nil when the WebPush lane is unconfigured (no vapidKeyFile ⇒ the key is
// absent entirely, the app's signal that only the legacy keyless lane
// exists, never null or an empty object). Single source with the enroll 201:
// both surfaces read the sender's own key through this helper, so QR and
// response cannot disagree.
func (a *App) enrollWebpush() *webpushQR {
	k := a.approval.WebPushVAPIDPublicKey()
	if k == "" {
		return nil
	}
	return &webpushQR{VAPIDPublicKey: k}
}

// enrollTier names the configured endpoint that won enrollServers' precedence
// ladder. It is what decides whether the enroll QR may carry the TLS pin: a
// pin is a promise about ONE endpoint's key, so it may only ride a servers
// list naming that same endpoint (see enrollPin).
type enrollTier int

const (
	enrollTierNone           enrollTier = iota // nothing configured: no servers, no pin
	enrollTierApproverTLS                      // (1) server.approverTLS.publicUrl: the dedicated listener
	enrollTierApproverPublic                   // (2) server.approverPublicUrl: an ingress fronting the approver surface
	enrollTierPublic                           // (3) server.publicUrl: the main listener
)

// enrollServers is the server-computed base-URL list seeded into the enroll QR,
// in precedence order: (1) the dedicated approver listener's publicUrl, (2)
// server.approverPublicUrl, (3) server.publicUrl. Clients (console/app) may
// append or override alternate URLs on their side (e.g. an internal-VPN URL);
// that augmentation is deliberately not the server's concern.
func (a *App) enrollServers() []string {
	servers, _ := a.enrollServersTier()
	return servers
}

// enrollServersTier is enrollServers plus the tier that produced the list;
// the two are computed together because the caller that mints a QR needs both
// (the pin is only valid for one of the tiers).
func (a *App) enrollServersTier() ([]string, enrollTier) {
	// (1) The dedicated approver listener wins even when approverPublicUrl is
	// also set, because it is the tier that carries the pin: a.tlsSPKIPin is
	// THAT listener's certificate, so QR host and QR pin must name the same
	// endpoint or the phone pins a key the host it dials never presents. The
	// main listener may be plaintext by necessity (eval) or ingress-terminated
	// (no mintable pin).
	if at := a.cfg.Server.ApproverTLS; at.PublicURL != "" {
		return []string{strings.TrimSuffix(at.PublicURL, "/")}, enrollTierApproverTLS
	}
	// (2) Ingress-terminated: a fronting proxy serves the approver surface on
	// its own public host with a public-CA certificate (no pin to carry), while
	// publicUrl may be reachable only from a private network.
	if p := a.cfg.Server.ApproverPublicURL; p != "" {
		return []string{strings.TrimSuffix(p, "/")}, enrollTierApproverPublic
	}
	// (3) Standalone: one listener serves the phone and everything else.
	if a.cfg.Server.PublicURL == "" {
		return []string{}, enrollTierNone
	}
	return []string{strings.TrimSuffix(a.cfg.Server.PublicURL, "/")}, enrollTierPublic
}

// enrollPin returns the SPKI pin the enroll QR may carry for a servers list
// produced by tier, or "" when no pin applies.
//
// The rule is endpoint identity: QR host and QR pin must name the same
// endpoint. It is strict because the failure is silent, simultaneous and
// unrecoverable from the phone's side, and a whole fleet enrolled from the
// same QR source dies together the day that key rotates.
//
// a.tlsSPKIPin caches whichever listener minted it (the dedicated approver
// listener overwrites any main-listener pin at boot), so the tier that may
// carry it follows from which listener that was: with a dedicated approver
// listener configured only tier 1 names it, otherwise only tier 3 does. Tier 2
// is an ingress terminating TLS on its own host with its own certificate, for
// which strazad holds no pin and must never guess one.
func (a *App) enrollPin(tier enrollTier) string {
	if a.tlsSPKIPin == "" {
		return ""
	}
	switch {
	case a.cfg.Server.ApproverTLS.Listen != "":
		if tier == enrollTierApproverTLS {
			return a.tlsSPKIPin
		}
	case a.cfg.TLSEnabled():
		if tier == enrollTierPublic {
			return a.tlsSPKIPin
		}
	}
	return ""
}
