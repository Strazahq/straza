package config

import (
	"fmt"
	"strings"
	"time"
)

// Approval configures the human-approval workflow. The service itself is
// ALWAYS constructed (the console is channel #0 and always on), so there is
// no top-level enable knob; only the third-party channels opt in. Defaults are
// identical in both profiles.
type Approval struct {
	// Retention is the prune horizon for resolved/expired records (the janitor
	// purges terminal rows older than this). Zero means the built-in default
	// (720h / 30 days); negative fails boot.
	Retention time.Duration `yaml:"retention"`
	// Channels configures the optional third-party notifiers. The console
	// surface needs no configuration.
	Channels ApprovalChannels `yaml:"channels"`
	// Push configures mobile push delivery to the Straza approver app.
	// Both halves are opt-in and default off.
	Push ApprovalPush `yaml:"push"`
	// Preview configures the bounded, redaction-aware args preview shown on
	// every approval surface.
	Preview ApprovalPreview `yaml:"preview"`
	// GatewayHoldSeconds caps how long a mode:approve hold keeps the
	// gateway tools/call SOCKET open before answering the structured
	// pending/retry reason. It never extends the rule's decision
	// window, only how much of it the first request waits through. Zero =
	// the built-in 120s default. Lower it (e.g. 20-30)
	// when your clients are known to time out sooner: the python kit fails
	// closed at 30s, some MCP clients give up at 30-60s, and a socket held
	// past the client's own deadline reads as a network timeout, which
	// models paper over. Negative fails boot.
	GatewayHoldSeconds int `yaml:"gatewayHoldSeconds"`
	// UnsignedOwnDecisions lets a person decide their own request from the
	// console, strazactl and Slack, which carry no device signature. False
	// accepts such a decision only from an enrolled phone or browser, whose
	// key an agent on that person's machine cannot reach. True means that
	// agent can run strazactl and approve its own calls.
	UnsignedOwnDecisions bool `yaml:"unsignedOwnDecisions"`
}

// ApprovalPreview is the single global admin knob for the approval args
// preview. Default ON: the preview is computed and stored at
// request time and rendered on every surface (console, app, Slack, CLI, admin
// API). Turning it off means no preview is computed or stored; the omit-empty
// wire fields degrade every surface to today's behavior with no client change.
// Redaction is non-optional regardless of this knob (off = no preview at all,
// never an unscrubbed one).
type ApprovalPreview struct {
	Enabled bool `yaml:"enabled"`
}

// ApprovalChannels groups the per-channel settings.
type ApprovalChannels struct {
	Slack SlackChannel `yaml:"slack"`
}

// ApprovalPush configures push delivery for the mobile approver surface. The
// delivery engine is constructed only when FCM is enabled OR allowedPushHosts
// is non-empty (an UnifiedPush-only deployment needs no FCM credentials). The
// notification body is an opaque reference (approval id) only, never command
// text or arguments (privacy rule).
type ApprovalPush struct {
	// FCM configures the Firebase Cloud Messaging HTTP v1 backend (Android /
	// Google-routed devices).
	FCM FCMPush `yaml:"fcm"`
	// WebPush configures the RFC 8030/8291/8292 Web Push sender, the one
	// protocol lane behind spec-3 UnifiedPush (encrypted), kind=webpush
	// registrations (browser/PWA subscriptions incl. iOS 16.4+ home-screen
	// PWAs), and any standards-compliant push service (Mozilla autopush,
	// FCM's WebPush endpoint, web.push.apple.com, WNS).
	WebPush WebPush `yaml:"webpush"`
	// APNS configures the Apple Push Notification service sender for the
	// native iOS approver app (kind=apns registrations).
	APNS APNSPush `yaml:"apns"`
	// Relay configures the hosted push relay client (the Straza-operated
	// service). It is the only lane for native
	// iOS push without the Apple publisher key and a convenience lane for
	// Android. Same-lane conflicts with the direct senders fail boot
	// (validate), never a silent preference.
	Relay RelayPush `yaml:"relay"`
	// AllowedPushHosts is the allowlist of hostnames a UnifiedPush (ntfy)
	// endpoint may target. Enforced at registration AND at delivery; an empty
	// list means UnifiedPush registrations are refused (fail-safe).
	AllowedPushHosts []string `yaml:"allowedPushHosts"`
	// TicketReminderBefore is how long before a pending ticket's expiry the
	// single near-expiry reminder push fires (the create push already announced
	// its existence; this nudges the one moment it is about to lapse undecided).
	// Zero means the built-in default (approval.defaultTicketReminderBefore, 2h),
	// resolved in the service (the Retention zero-means-default idiom); negative
	// fails boot. A ticket whose whole decision window is <= this offset gets no
	// reminder (the create push already covered such a short window).
	TicketReminderBefore time.Duration `yaml:"ticketReminderBefore"`
}

// WebPush configures the Web Push protocol sender (RFC 8030 delivery,
// RFC 8291 aes128gcm encryption, RFC 8292 VAPID). Setting vapidKeyFile is the
// explicit enable act (one knob, no separate bool; presence is the signal,
// the FCM app-config idiom). The file holds the deployment's VAPID P-256
// private key: strazad loads it at boot, and when the path is set but the
// file absent it MINTS the key once and persists it there (0600),
// generate-once semantics mirroring the approverTLS auto-mint. The keypair is
// the deployment's IDENTITY to every push service: rotating or deleting it
// invalidates every existing webpush/unifiedpush(keyed) subscription, which
// must then re-register. Back the file up like a credential; never rotate
// casually.
type WebPush struct {
	// VAPIDKeyFile is the path of the VAPID private key (PEM, PKCS#8 or EC).
	// Empty = the WebPush lane is off (keyed registrations are refused with an
	// actionable error). Set but absent on disk = minted at boot.
	VAPIDKeyFile string `yaml:"vapidKeyFile"`
	// Contact is the RFC 8292 §2.1 `sub` claim: a mailto: or https: URI the
	// push-service operator can use to reach this deployment's operator.
	// Optional but recommended; validated when set.
	Contact string `yaml:"contact"`
}

// Enabled reports whether the WebPush sender lane is configured (the
// vapidKeyFile path is the single enable knob).
func (w WebPush) Enabled() bool { return w.VAPIDKeyFile != "" }

// APNSPush configures the APNs sender for the native iOS approver app.
// Setting keyFile is the explicit enable act (presence is the signal, the
// vapidKeyFile idiom) with one deliberate asymmetry: the file is NEVER
// minted. Apple mints .p8 auth keys; strazad fails boot on an unreadable file
// instead of creating one. The key rides server config only (never a repo,
// never the agent) and is owner-held; back it up like the VAPID key.
type APNSPush struct {
	// KeyFile is the path of the APNs auth key (.p8: PKCS#8, EC P-256).
	// Empty = the APNs lane is off (apns registrations are stored but never
	// rung, and the app's poll floor still covers approvals).
	KeyFile string `yaml:"keyFile"`
	// KeyID is the 10-character id of the auth key (developer portal, shown
	// beside the key and embedded in its filename).
	KeyID string `yaml:"keyId"`
	// TeamID is the Apple Developer Team the key belongs to. APNs tracks
	// provider-token minting globally per team (not per connection or key),
	// which is why the sender keeps one wall-clock JWT bucket (push_apns.go).
	TeamID string `yaml:"teamId"`
	// Topic is the apns-topic header value: the app's bare bundle id for
	// alert pushes (ai.straza.approver for the published Straza approver app).
	Topic string `yaml:"topic"`
	// Environment selects the APNs cluster: "production" (the default; every
	// TestFlight or App Store build yields production-only tokens) or
	// "sandbox" (dev-signed builds only). A cross-environment send fails as
	// BadDeviceToken, so this must match how the app was signed.
	Environment string `yaml:"environment"`
}

// Enabled reports whether the APNs sender lane is configured (the keyFile
// path is the single enable knob).
func (a APNSPush) Enabled() bool { return a.KeyFile != "" }

// RelayPush configures the hosted push relay client. Enabled is the explicit
// act; tokenFile is required and holds the ANONYMOUS deployment token,
// which strazad mints there on first boot
// (0600, the vapidKeyFile custody idiom) and re-mints once if the relay
// refuses it. URL empty means the official relay (zero-means-default,
// resolved in the sender). The relay only ever receives the content-free
// {v, ref, kind} envelope plus the push route; approval content never
// leaves the deployment. Staged faces with enabled: false are legal (the
// FCM stage-before-live idiom).
type RelayPush struct {
	Enabled   bool   `yaml:"enabled"`
	URL       string `yaml:"url"`
	TokenFile string `yaml:"tokenFile"`
}

// FCMPush configures the Firebase Cloud Messaging HTTP v1 backend. When
// enabled, both the service-account file and the project id are required: the
// project id names the messages:send endpoint, the file self-signs the bearer.
//
// The remaining fields are the deployment's PUBLIC Firebase app config. The
// mobile approver app ships NO google-services.json; the enroll payload hands
// the phone this deployment's own values and the app initializes the default
// FirebaseApp at runtime. AppID, APIKey and SenderID are public identifiers,
// not secrets (Firebase's own docs say so, and every ordinary Android APK
// ships them verbatim inside google-services.json), so emitting them in the
// enroll payload does not violate the rule that credentials never leave the
// server: the service-account file is the credential here. The set is
// all-or-nothing (validate) and deliberately independent of Enabled, so a
// deployment can stage the app config before the sender goes live.
type FCMPush struct {
	Enabled            bool   `yaml:"enabled"`
	ServiceAccountFile string `yaml:"serviceAccountFile"`
	ProjectID          string `yaml:"projectId"`
	// AppID is google-services.json client[].client_info.mobilesdk_app_id
	// ("1:NNN:android:HEX", NOT the package name).
	AppID string `yaml:"appId"`
	// APIKey is google-services.json client[].api_key[0].current_key.
	APIKey string `yaml:"apiKey"`
	// SenderID is google-services.json project_info.project_number. Derivable
	// from AppID, but carrying it makes a mismatch fail loudly at enroll
	// instead of silently at token time.
	SenderID string `yaml:"senderId"`
}

// AppConfigured reports whether the full public Firebase app config is
// present, the condition under which the enroll payload advertises the fcm
// object to enrolling phones. validate enforces all-or-nothing, so after a successful
// boot this is simply "is the BYO-Firebase lane on". Deliberately independent
// of Enabled (the server-side sender): absent ⇒ the app stays on the
// UnifiedPush/ntfy lane.
func (f FCMPush) AppConfigured() bool {
	return f.ProjectID != "" && f.AppID != "" && f.APIKey != "" && f.SenderID != ""
}

// SlackChannel configures the Slack Block Kit approver channel. The
// bot token and signing secret each accept an inline value OR a file
// (file preferred; setting both is rejected). Privacy default: the agent's
// justification is NOT forwarded to Slack unless includeJustification is set;
// the console always shows it.
type SlackChannel struct {
	Enabled              bool   `yaml:"enabled"`
	BotToken             string `yaml:"botToken"`
	BotTokenFile         string `yaml:"botTokenFile"`
	SigningSecret        string `yaml:"signingSecret"`
	SigningSecretFile    string `yaml:"signingSecretFile"`
	Channel              string `yaml:"channel"` // Slack channel ID to post to
	IncludeJustification bool   `yaml:"includeJustification"`
}

// defaultApproval returns the profile-independent approval defaults: a 30-day
// retention horizon, Slack disabled, and push delivery off (no FCM, no
// UnifiedPush allowlist).
func defaultApproval() Approval {
	return Approval{
		Retention: 30 * 24 * time.Hour,
		Channels:  ApprovalChannels{Slack: SlackChannel{Enabled: false, IncludeJustification: false}},
		Push:      ApprovalPush{FCM: FCMPush{Enabled: false}},
		Preview:   ApprovalPreview{Enabled: true},
	}
}

// validate rejects approval configurations strazad cannot serve.
func (a Approval) validate() error {
	if a.GatewayHoldSeconds < 0 {
		return fmt.Errorf("approval.gatewayHoldSeconds must be >= 0 (0 = the built-in 120s default)")
	}
	if a.Retention < 0 {
		return fmt.Errorf("approval.retention must be a positive duration (e.g. 720h)")
	}
	s := a.Channels.Slack
	if s.BotToken != "" && s.BotTokenFile != "" {
		return fmt.Errorf("approval.channels.slack: set botToken or botTokenFile, not both")
	}
	if s.SigningSecret != "" && s.SigningSecretFile != "" {
		return fmt.Errorf("approval.channels.slack: set signingSecret or signingSecretFile, not both")
	}
	if s.Enabled {
		if s.Channel == "" {
			return fmt.Errorf("approval.channels.slack: channel is required when the Slack channel is enabled")
		}
		if s.BotToken == "" && s.BotTokenFile == "" {
			return fmt.Errorf("approval.channels.slack: botToken or botTokenFile is required when enabled")
		}
		if s.SigningSecret == "" && s.SigningSecretFile == "" {
			return fmt.Errorf("approval.channels.slack: signingSecret or signingSecretFile is required when enabled")
		}
	}
	if a.Push.TicketReminderBefore < 0 {
		return fmt.Errorf("approval.push.ticketReminderBefore must be a positive duration (e.g. 2h)")
	}
	if w := a.Push.WebPush; w.Contact != "" {
		if w.VAPIDKeyFile == "" {
			return fmt.Errorf("approval.push.webpush.contact is set but vapidKeyFile is not. The key file is the enable knob (set it; strazad mints the key there on first boot)")
		}
		if !strings.HasPrefix(w.Contact, "mailto:") && !strings.HasPrefix(w.Contact, "https://") {
			return fmt.Errorf("approval.push.webpush.contact must be a mailto: or https: URI (RFC 8292 §2.1), got %q", w.Contact)
		}
	}
	if ap := a.Push.APNS; ap.KeyFile == "" {
		if ap.KeyID != "" || ap.TeamID != "" || ap.Topic != "" || ap.Environment != "" {
			return fmt.Errorf("approval.push.apns: keyId/teamId/topic/environment are set but keyFile is not; the key file is the enable knob (Apple mints it in the developer portal; strazad never creates one)")
		}
	} else {
		var missing []string
		if ap.KeyID == "" {
			missing = append(missing, "keyId")
		}
		if ap.TeamID == "" {
			missing = append(missing, "teamId")
		}
		if ap.Topic == "" {
			missing = append(missing, "topic")
		}
		if len(missing) > 0 {
			return fmt.Errorf("approval.push.apns: %s required when keyFile is set (copy them from the developer portal beside the key)", strings.Join(missing, ", "))
		}
		switch ap.Environment {
		case "", "production", "sandbox":
		default:
			return fmt.Errorf("approval.push.apns.environment must be %q or %q (empty = production; every TestFlight/App Store build is production), got %q", "production", "sandbox", ap.Environment)
		}
	}
	if r := a.Push.Relay; r.Enabled {
		if r.TokenFile == "" {
			return fmt.Errorf("approval.push.relay: tokenFile is required when the relay is enabled (strazad mints the anonymous deployment token there on first boot)")
		}
		if a.Push.FCM.Enabled {
			return fmt.Errorf("approval.push.relay and approval.push.fcm are both enabled for the same lane; pick one: BYO Firebase sends direct and needs no relay, the relay needs no Firebase project")
		}
		if a.Push.APNS.Enabled() {
			return fmt.Errorf("approval.push.relay and approval.push.apns are both configured for the same lane; pick one: the direct .p8 sender or the relay (a silent preference is refused on purpose)")
		}
	}
	f := a.Push.FCM
	if f.Enabled {
		if f.ServiceAccountFile == "" {
			return fmt.Errorf("approval.push.fcm: serviceAccountFile is required when FCM is enabled")
		}
		if f.ProjectID == "" {
			return fmt.Errorf("approval.push.fcm: projectId is required when FCM is enabled")
		}
	}
	// The public app config (BYO-Firebase) is all-or-nothing: a partial set
	// would enroll phones with a config Firebase rejects only at token time,
	// silently, on the phone. projectId alone is NOT a partial set (it is also
	// a sender field), but any of the three app-only fields drags in the rest.
	if f.AppID != "" || f.APIKey != "" || f.SenderID != "" {
		if !f.AppConfigured() {
			missing := []string{}
			if f.ProjectID == "" {
				missing = append(missing, "projectId")
			}
			if f.AppID == "" {
				missing = append(missing, "appId")
			}
			if f.APIKey == "" {
				missing = append(missing, "apiKey")
			}
			if f.SenderID == "" {
				missing = append(missing, "senderId")
			}
			return fmt.Errorf("approval.push.fcm: the public Firebase app config is all-or-nothing (appId, apiKey, senderId and projectId travel together; missing: %s); copy all of them from the deployment's google-services.json", strings.Join(missing, ", "))
		}
	}
	return nil
}
