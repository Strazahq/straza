package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"

	"github.com/strazahq/straza/internal/config"
)

// TestApproverSurfaceOnly pins the dedicated listener's exposure contract:
// ONLY the phone surface (/v1/approver/*) and /readyz pass through; admin,
// console, gateway, and OIDC answer 404 so the extra https port never widens
// the exposed surface.
func TestApproverSurfaceOnly(t *testing.T) {
	t.Parallel()
	inner := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusTeapot) })
	h := approverSurfaceOnly(inner)
	cases := []struct {
		path string
		pass bool
	}{
		{"/v1/approver/enroll", true},
		{"/v1/approver/pending", true},
		{"/v1/approver/decide/abc", true},
		{"/readyz", true},
		{"/v1/admin/users", false},
		{"/v1/admin/approvers/enroll-token", false},
		{"/console/", false},
		{"/mcp", false},
		{"/oidc/token", false},
		{"/", false},
	}
	for _, tc := range cases {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, tc.path, nil))
		got := rec.Code == http.StatusTeapot
		if got != tc.pass {
			t.Errorf("%s: pass=%v, want %v (code %d)", tc.path, got, tc.pass, rec.Code)
		}
	}
}

// TestEnrollServersApproverOverride: with the dedicated approver listener
// configured, the enroll QR names ITS PublicURL (what the phone dials), not
// the possibly-plaintext main publicUrl.
func TestEnrollServersApproverOverride(t *testing.T) {
	t.Parallel()
	app := &App{cfg: config.Config{}}
	app.cfg.Server.PublicURL = "http://localhost:8420"
	if got := app.enrollServers(); len(got) != 1 || got[0] != "http://localhost:8420" {
		t.Fatalf("base servers = %v", got)
	}
	app.cfg.Server.ApproverTLS = config.ApproverTLS{
		Listen: "0.0.0.0:8443", CertFile: "c", KeyFile: "k", PublicURL: "https://127.0.0.1:8443/",
	}
	if got := app.enrollServers(); len(got) != 1 || got[0] != "https://127.0.0.1:8443" {
		t.Fatalf("approver-override servers = %v (want https://127.0.0.1:8443, trailing slash trimmed)", got)
	}
}

// TestEnrollServersPrecedence pins the three tiers the enroll QR chooses
// between: the dedicated approver listener (it also carries the pin), then
// server.approverPublicUrl (a proxy/ingress terminates TLS for the approver
// surface on its own public host), then plain server.publicUrl. Every tier is
// trailing-slash trimmed.
func TestEnrollServersPrecedence(t *testing.T) {
	t.Parallel()
	const (
		listenerURL = "https://phone.example:8443"
		ingressURL  = "https://approve.example.com"
		publicURL   = "http://localhost:8420"
	)
	cases := []struct {
		name                      string
		listener, ingress, public string
		want                      []string
		wantTier                  enrollTier
	}{
		{"nothing configured", "", "", "", nil, enrollTierNone},
		{"publicUrl only", "", "", publicURL, []string{publicURL}, enrollTierPublic},
		{"ingress only", "", ingressURL, "", []string{ingressURL}, enrollTierApproverPublic},
		{"ingress beats publicUrl", "", ingressURL, publicURL, []string{ingressURL}, enrollTierApproverPublic},
		{"listener beats publicUrl", listenerURL, "", publicURL, []string{listenerURL}, enrollTierApproverTLS},
		{"listener beats ingress", listenerURL, ingressURL, publicURL, []string{listenerURL}, enrollTierApproverTLS},
		{"listener slash trimmed", listenerURL + "/", ingressURL, publicURL, []string{listenerURL}, enrollTierApproverTLS},
		{"ingress slash trimmed", "", ingressURL + "/", publicURL, []string{ingressURL}, enrollTierApproverPublic},
		{"publicUrl slash trimmed", "", "", publicURL + "/", []string{publicURL}, enrollTierPublic},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			app := &App{cfg: config.Config{}}
			app.cfg.Server.PublicURL = tc.public
			app.cfg.Server.ApproverPublicURL = tc.ingress
			app.cfg.Server.ApproverTLS.PublicURL = tc.listener
			if got := app.enrollServers(); !slices.Equal(got, tc.want) {
				t.Fatalf("enrollServers() = %v, want %v", got, tc.want)
			}
			got, gotTier := app.enrollServersTier()
			if !slices.Equal(got, tc.want) || gotTier != tc.wantTier {
				t.Fatalf("enrollServersTier() = %v, tier %d; want %v, tier %d", got, gotTier, tc.want, tc.wantTier)
			}
		})
	}
}

// TestEnrollPinEndpointIdentity pins the rule that keeps a fleet of enrolled
// phones alive: the QR's pin may only ride a QR whose servers name the endpoint
// whose certificate minted that pin. Getting this wrong is not a cosmetic bug:
// a phone that pinned the wrong key fail-closes forever, and every approver
// enrolled from the same deployment dies the day that key rotates.
//
// Behind an ingress (server.approverPublicUrl) the QR names the ingress
// host, so carrying strazad's OWN leaf pin would brick every enrolled
// approver at the first ingress certificate renewal.
func TestEnrollPinEndpointIdentity(t *testing.T) {
	t.Parallel()
	const (
		pin         = "sha256/AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="
		listenerURL = "https://phone.example:8443"
		ingressURL  = "https://approve.example.com"
		publicURL   = "https://straza.example:8420"
	)
	cases := []struct {
		name string
		// listen/certFile model which listener minted the cached pin:
		// approverTLS.listen wins over server.tls, exactly as boot does.
		listen, certFile          string
		listener, ingress, public string
		pin                       string
		want                      string
	}{
		{name: "tier 1 dedicated listener carries its own pin",
			listen: "0.0.0.0:8443", listener: listenerURL, public: publicURL, pin: pin, want: pin},
		{name: "tier 1 without a minted pin carries nothing",
			listen: "0.0.0.0:8443", listener: listenerURL, public: publicURL},
		{name: "tier 2 ingress never carries the main listener's pin",
			certFile: "c", ingress: ingressURL, public: publicURL, pin: pin},
		{name: "tier 2 ingress never carries the approver listener's pin",
			listen: "0.0.0.0:8443", ingress: ingressURL, public: publicURL, pin: pin},
		{name: "tier 3 publicUrl carries the main listener's own pin",
			certFile: "c", public: publicURL, pin: pin, want: pin},
		{name: "tier 3 publicUrl never carries the approver listener's pin",
			listen: "0.0.0.0:8443", public: publicURL, pin: pin},
		{name: "tier 3 plaintext carries nothing",
			public: publicURL},
		{name: "nothing configured carries nothing",
			certFile: "c", pin: pin},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			app := &App{cfg: config.Config{}, tlsSPKIPin: tc.pin}
			app.cfg.Server.PublicURL = tc.public
			app.cfg.Server.ApproverPublicURL = tc.ingress
			app.cfg.Server.ApproverTLS.PublicURL = tc.listener
			app.cfg.Server.ApproverTLS.Listen = tc.listen
			if tc.certFile != "" {
				app.cfg.Server.TLS = config.TLS{CertFile: tc.certFile, KeyFile: "k"}
			}
			servers, tier := app.enrollServersTier()
			got := app.enrollPin(tier)
			if got != tc.want {
				t.Fatalf("enrollPin(tier %d) = %q, want %q (servers %v)", tier, got, tc.want, servers)
			}

			// Wire shape: an inapplicable pin drops the KEY, not just the value;
			// the app distinguishes "no pin" from "empty pin".
			raw, err := json.Marshal(qrPayload{V: 1, Servers: servers, Token: "tok", Pin: got})
			if err != nil {
				t.Fatalf("marshal qr payload: %v", err)
			}
			var keys map[string]json.RawMessage
			if err := json.Unmarshal(raw, &keys); err != nil {
				t.Fatalf("qr payload is not a JSON object: %v", err)
			}
			if _, ok := keys["pin"]; ok != (tc.want != "") {
				t.Fatalf("qr payload pin key present = %v, want %v: %s", ok, tc.want != "", raw)
			}
		})
	}
}

// TestEnrollFCMAppConfig pins the BYO-Firebase half of the enroll payload:
// the fcm object rides the QR/enroll payload ONLY when the deployment's full
// public Firebase
// app config is present (presence-gated, deliberately independent of
// fcm.enabled, the server-side sender), and an absent config drops the KEY
// entirely, never an empty object. When present, the wire names are the
// contract's exact snake_case fields.
func TestEnrollFCMAppConfig(t *testing.T) {
	t.Parallel()
	full := config.FCMPush{
		ProjectID: "acme-approver", AppID: "1:407:android:ab12cd",
		APIKey: "AIzaExampleKey", SenderID: "407",
	}
	cases := []struct {
		name string
		fcm  config.FCMPush
		want bool
	}{
		{"unconfigured", config.FCMPush{}, false},
		{"sender-only (enabled, no app config)",
			config.FCMPush{Enabled: true, ServiceAccountFile: "/f", ProjectID: "p"}, false},
		// Public set present, sender still disabled: advertised anyway.
		{"full public set, sender disabled", full, true},
		{"full public set, sender enabled too",
			config.FCMPush{Enabled: true, ServiceAccountFile: "/f",
				ProjectID: full.ProjectID, AppID: full.AppID, APIKey: full.APIKey, SenderID: full.SenderID}, true},
		// validate blocks partial sets at boot; the helper still gates on the
		// full set so a hand-built config can never emit a half object.
		{"partial set never advertises (defense in depth)",
			config.FCMPush{ProjectID: "p", AppID: "1:407:android:ab12cd"}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			app := &App{cfg: config.Config{}}
			app.cfg.Approval.Push.FCM = tc.fcm
			got := app.enrollFCM()
			if (got != nil) != tc.want {
				t.Fatalf("enrollFCM() = %+v, want present=%v", got, tc.want)
			}

			// Wire shape: absent config omits the fcm KEY (the app's signal to
			// stay on the UnifiedPush/ntfy lane); present config carries the
			// contract's exact field names and values.
			raw, err := json.Marshal(qrPayload{V: 1, Servers: []string{"https://x"}, Token: "tok", FCM: got})
			if err != nil {
				t.Fatalf("marshal qr payload: %v", err)
			}
			var keys map[string]json.RawMessage
			if err := json.Unmarshal(raw, &keys); err != nil {
				t.Fatalf("qr payload is not a JSON object: %v", err)
			}
			if _, ok := keys["fcm"]; ok != tc.want {
				t.Fatalf("qr payload fcm key present = %v, want %v: %s", ok, tc.want, raw)
			}
			if !tc.want {
				return
			}
			var fields map[string]string
			if err := json.Unmarshal(keys["fcm"], &fields); err != nil {
				t.Fatalf("fcm object: %v", err)
			}
			want := map[string]string{
				"project_id": tc.fcm.ProjectID, "app_id": tc.fcm.AppID,
				"api_key": tc.fcm.APIKey, "sender_id": tc.fcm.SenderID,
			}
			if len(fields) != len(want) {
				t.Errorf("fcm object = %s, want exactly the four contract fields", keys["fcm"])
			}
			for k, v := range want {
				if fields[k] != v {
					t.Errorf("fcm.%s = %q, want %q", k, fields[k], v)
				}
			}
		})
	}
}
