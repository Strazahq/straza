package config

import (
	"strings"
	"testing"
	"time"
)

// TestApprovalPushDefaults: push delivery is off out of the box in both
// profiles: no FCM, no UnifiedPush allowlist.
func TestApprovalPushDefaults(t *testing.T) {
	cfg, err := Loader{Getenv: noEnv}.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Approval.Push.FCM.Enabled {
		t.Error("FCM must default disabled")
	}
	if len(cfg.Approval.Push.AllowedPushHosts) != 0 {
		t.Errorf("allowedPushHosts must default empty, got %v", cfg.Approval.Push.AllowedPushHosts)
	}
}

// TestApprovalPreviewKnob: the args preview defaults ON, only an explicit
// false/0 disables it, and any other value keeps it on (default-on fail-open,
// the inverse of the fail-safe push knob).
func TestApprovalPreviewKnob(t *testing.T) {
	cfg, err := Loader{Getenv: noEnv}.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !cfg.Approval.Preview.Enabled {
		t.Error("args preview must default ENABLED")
	}
	for _, v := range []string{"false", "0"} {
		cfg, err := Loader{Getenv: envMap(map[string]string{"STRAZA_APPROVAL_PREVIEW_ENABLED": v})}.Load()
		if err != nil {
			t.Fatalf("Load %q: %v", v, err)
		}
		if cfg.Approval.Preview.Enabled {
			t.Errorf("STRAZA_APPROVAL_PREVIEW_ENABLED=%q must disable the preview", v)
		}
	}
	for _, v := range []string{"true", "1", "yes"} {
		cfg, err := Loader{Getenv: envMap(map[string]string{"STRAZA_APPROVAL_PREVIEW_ENABLED": v})}.Load()
		if err != nil {
			t.Fatalf("Load %q: %v", v, err)
		}
		if !cfg.Approval.Preview.Enabled {
			t.Errorf("STRAZA_APPROVAL_PREVIEW_ENABLED=%q must keep the preview on", v)
		}
	}
}

// TestApprovalPushEnvKnobs covers the four container env overrides and the
// comma-split of the host allowlist. Only "true"/"1" enables FCM (fail-safe,
// mirroring Slack) so an env typo cannot half-enable push.
func TestApprovalPushEnvKnobs(t *testing.T) {
	env := envMap(map[string]string{
		"STRAZA_APPROVAL_PUSH_FCM_ENABLED":              "true",
		"STRAZA_APPROVAL_PUSH_FCM_SERVICE_ACCOUNT_FILE": "/etc/straza/fcm-sa.json",
		"STRAZA_APPROVAL_PUSH_FCM_PROJECT_ID":           "straza-proj",
		"STRAZA_APPROVAL_PUSH_ALLOWED_HOSTS":            "ntfy.sh, push.example.org ,, up.conduit.im",
	})
	cfg, err := Loader{Getenv: env}.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	f := cfg.Approval.Push.FCM
	if !f.Enabled || f.ServiceAccountFile != "/etc/straza/fcm-sa.json" || f.ProjectID != "straza-proj" {
		t.Errorf("fcm config = %+v, want env-provided values", f)
	}
	got := cfg.Approval.Push.AllowedPushHosts
	want := []string{"ntfy.sh", "push.example.org", "up.conduit.im"}
	if len(got) != len(want) {
		t.Fatalf("allowedPushHosts = %v, want %v (trimmed, blanks dropped)", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("allowedPushHosts[%d] = %q, want %q", i, got[i], want[i])
		}
	}

	// A typo'd ENABLED value stays OFF (fail-safe).
	off := envMap(map[string]string{"STRAZA_APPROVAL_PUSH_FCM_ENABLED": "yes-please"})
	cfg, err = Loader{Getenv: off}.Load()
	if err != nil {
		t.Fatalf("Load with bad ENABLED: %v", err)
	}
	if cfg.Approval.Push.FCM.Enabled {
		t.Error("non-true ENABLED value must not enable FCM")
	}
}

// TestApprovalPushTicketReminderKnob covers the near-expiry ticket reminder
// offset: it defaults to zero (the service resolves zero to its 2h built-in),
// zero passes validation, a duration env value parses, a negative value fails
// boot (mirroring retention), and an unparseable env value fails boot (never a
// silent fallback).
func TestApprovalPushTicketReminderKnob(t *testing.T) {
	// Default: unset ⇒ zero, and zero validates clean.
	cfg, err := Loader{Getenv: noEnv}.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Approval.Push.TicketReminderBefore != 0 {
		t.Errorf("ticketReminderBefore default = %s, want 0 (service resolves to 2h)", cfg.Approval.Push.TicketReminderBefore)
	}

	// A valid duration env value parses.
	cfg, err = Loader{Getenv: envMap(map[string]string{
		"STRAZA_APPROVAL_PUSH_TICKET_REMINDER_BEFORE": "90m",
	})}.Load()
	if err != nil {
		t.Fatalf("Load 90m: %v", err)
	}
	if cfg.Approval.Push.TicketReminderBefore != 90*time.Minute {
		t.Errorf("ticketReminderBefore = %s, want 90m", cfg.Approval.Push.TicketReminderBefore)
	}

	// A negative value fails boot (positive-duration rule).
	if _, err := (Loader{Getenv: envMap(map[string]string{
		"STRAZA_APPROVAL_PUSH_TICKET_REMINDER_BEFORE": "-1h",
	})}).Load(); err == nil || !strings.Contains(err.Error(), "ticketReminderBefore must be a positive duration") {
		t.Errorf("negative ticketReminderBefore: want positive-duration error, got %v", err)
	}

	// An unparseable value fails boot via the reject sentinel (never a silent fallback).
	if _, err := (Loader{Getenv: envMap(map[string]string{
		"STRAZA_APPROVAL_PUSH_TICKET_REMINDER_BEFORE": "not-a-duration",
	})}).Load(); err == nil || !strings.Contains(err.Error(), "ticketReminderBefore must be a positive duration") {
		t.Errorf("unparseable ticketReminderBefore: want boot error, got %v", err)
	}
}

// TestFCMAppConfigAllOrNothing: the PUBLIC Firebase app-config fields the
// enroll payload advertises (BYO-Firebase) travel as a set: appId, apiKey
// and senderId require each other plus projectId, and a partial set fails boot
// with a clear message instead of enrolling phones with a config Firebase
// rejects at token time. The set is deliberately independent of fcm.enabled:
// a sender-only config (enabled + serviceAccountFile + projectId) stays valid
// unchanged, and a full public set with the sender still disabled is a legal
// staging state. AppConfigured() mirrors exactly the full-set condition.
func TestFCMAppConfigAllOrNothing(t *testing.T) {
	fcmYAML := func(fields string) string {
		return "approval:\n  push:\n    fcm:\n" + fields
	}
	appSet := "      appId: \"1:407:android:ab12\"\n      apiKey: AIzaExample\n      senderId: \"407\"\n"
	cases := []struct {
		name           string
		yaml           string
		wantErr        string // substring of the boot error; "" = must validate
		wantAdvertised bool   // AppConfigured() on the loaded config
	}{
		{"nothing configured", "approval: {}\n", "", false},
		{"sender-only config stays valid, not advertised",
			fcmYAML("      enabled: true\n      serviceAccountFile: /f\n      projectId: p\n"), "", false},
		{"projectId alone is a sender field, not a partial app config",
			fcmYAML("      projectId: p\n"), "", false},
		{"full public set advertises without the sender",
			fcmYAML("      projectId: p\n" + appSet), "", true},
		{"full public set + enabled sender",
			fcmYAML("      enabled: true\n      serviceAccountFile: /f\n      projectId: p\n" + appSet), "", true},
		{"appId alone fails boot",
			fcmYAML("      appId: \"1:407:android:ab12\"\n"), "all-or-nothing", false},
		{"apiKey+senderId without appId fails boot",
			fcmYAML("      apiKey: AIzaExample\n      senderId: \"407\"\n"), "all-or-nothing", false},
		{"public set without projectId fails boot",
			fcmYAML(appSet), "all-or-nothing", false},
		{"public set without senderId fails boot",
			fcmYAML("      projectId: p\n      appId: \"1:407:android:ab12\"\n      apiKey: AIzaExample\n"), "all-or-nothing", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := writeFile(t, tc.yaml)
			cfg, err := (Loader{FilePath: path, ExplicitFile: true, Getenv: noEnv}).Load()
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("want boot error containing %q, got %v", tc.wantErr, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("must validate, got %v", err)
			}
			if got := cfg.Approval.Push.FCM.AppConfigured(); got != tc.wantAdvertised {
				t.Errorf("AppConfigured() = %v, want %v", got, tc.wantAdvertised)
			}
		})
	}
}

// TestApprovalWebPushKnobs: the WebPush lane defaults off, the key-file path
// is the single enable knob (yaml and env faces agree), and the contact claim
// is validated: set without the key file or with a non-mailto/https URI it
// fails boot instead of minting VAPID JWTs push services reject.
func TestApprovalWebPushKnobs(t *testing.T) {
	cfg, err := Loader{Getenv: noEnv}.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Approval.Push.WebPush.Enabled() || cfg.Approval.Push.WebPush.VAPIDKeyFile != "" {
		t.Error("webpush must default disabled (no vapidKeyFile)")
	}

	// Env faces.
	cfg, err = Loader{Getenv: envMap(map[string]string{
		"STRAZA_APPROVAL_PUSH_WEBPUSH_VAPID_KEY_FILE": "/var/lib/straza/vapid.pem",
		"STRAZA_APPROVAL_PUSH_WEBPUSH_CONTACT":        "mailto:ops@example.com",
	})}.Load()
	if err != nil {
		t.Fatalf("Load with webpush env: %v", err)
	}
	w := cfg.Approval.Push.WebPush
	if !w.Enabled() || w.VAPIDKeyFile != "/var/lib/straza/vapid.pem" || w.Contact != "mailto:ops@example.com" {
		t.Errorf("webpush env config = %+v, want env-provided values", w)
	}

	// Yaml face + https contact form.
	path := writeFile(t, "approval:\n  push:\n    webpush:\n      vapidKeyFile: /k.pem\n      contact: https://ops.example.com/push\n")
	cfg, err = (Loader{FilePath: path, ExplicitFile: true, Getenv: noEnv}).Load()
	if err != nil {
		t.Fatalf("Load yaml webpush: %v", err)
	}
	if !cfg.Approval.Push.WebPush.Enabled() || cfg.Approval.Push.WebPush.Contact != "https://ops.example.com/push" {
		t.Errorf("yaml webpush = %+v", cfg.Approval.Push.WebPush)
	}

	// contact without the key file fails boot (the key file is the enable knob).
	orphan := writeFile(t, "approval:\n  push:\n    webpush:\n      contact: mailto:ops@example.com\n")
	if _, err := (Loader{FilePath: orphan, ExplicitFile: true, Getenv: noEnv}).Load(); err == nil ||
		!strings.Contains(err.Error(), "vapidKeyFile") {
		t.Errorf("contact without vapidKeyFile: want enable-knob error, got %v", err)
	}

	// A contact that is neither mailto: nor https: fails boot.
	bad := writeFile(t, "approval:\n  push:\n    webpush:\n      vapidKeyFile: /k.pem\n      contact: ops@example.com\n")
	if _, err := (Loader{FilePath: bad, ExplicitFile: true, Getenv: noEnv}).Load(); err == nil ||
		!strings.Contains(err.Error(), "mailto:") {
		t.Errorf("bare-address contact: want mailto/https error, got %v", err)
	}
}

// TestApprovalAPNSKnobs: the APNs lane defaults off, keyFile is the single
// enable knob (yaml and env faces agree), the companion identifiers are
// required once the knob is set, and environment only accepts
// production|sandbox (empty resolves to production in the sender; the app has
// no sandbox lane today). Unlike the VAPID key, the .p8 is NEVER minted:
// Apple mints it, so identifiers without the file are a boot error, not a
// generate-once trigger.
func TestApprovalAPNSKnobs(t *testing.T) {
	cfg, err := Loader{Getenv: noEnv}.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Approval.Push.APNS.Enabled() || cfg.Approval.Push.APNS.KeyFile != "" {
		t.Error("apns must default disabled (no keyFile)")
	}

	// Env faces: all five knobs land.
	cfg, err = Loader{Getenv: envMap(map[string]string{
		"STRAZA_APPROVAL_PUSH_APNS_KEY_FILE":    "/var/lib/straza/AuthKey_TEST.p8",
		"STRAZA_APPROVAL_PUSH_APNS_KEY_ID":      "ABC123DEFG",
		"STRAZA_APPROVAL_PUSH_APNS_TEAM_ID":     "TEAM567890",
		"STRAZA_APPROVAL_PUSH_APNS_TOPIC":       "ai.straza.approver",
		"STRAZA_APPROVAL_PUSH_APNS_ENVIRONMENT": "sandbox",
	})}.Load()
	if err != nil {
		t.Fatalf("Load with apns env: %v", err)
	}
	a := cfg.Approval.Push.APNS
	if !a.Enabled() || a.KeyFile != "/var/lib/straza/AuthKey_TEST.p8" || a.KeyID != "ABC123DEFG" ||
		a.TeamID != "TEAM567890" || a.Topic != "ai.straza.approver" || a.Environment != "sandbox" {
		t.Errorf("apns env config = %+v, want env-provided values", a)
	}

	// Yaml face; empty environment validates (the sender resolves it to
	// production, matching how every TestFlight build is signed).
	path := writeFile(t, "approval:\n  push:\n    apns:\n      keyFile: /k.p8\n      keyId: ABC123DEFG\n      teamId: TEAM567890\n      topic: ai.straza.approver\n")
	cfg, err = (Loader{FilePath: path, ExplicitFile: true, Getenv: noEnv}).Load()
	if err != nil {
		t.Fatalf("Load yaml apns: %v", err)
	}
	if !cfg.Approval.Push.APNS.Enabled() || cfg.Approval.Push.APNS.Environment != "" {
		t.Errorf("yaml apns = %+v", cfg.Approval.Push.APNS)
	}

	// keyFile without the companion identifiers fails boot, missing ones named.
	for _, tc := range []struct{ name, yaml, want string }{
		{"no keyId", "approval:\n  push:\n    apns:\n      keyFile: /k.p8\n      teamId: T\n      topic: b\n", "keyId"},
		{"no teamId", "approval:\n  push:\n    apns:\n      keyFile: /k.p8\n      keyId: K\n      topic: b\n", "teamId"},
		{"no topic", "approval:\n  push:\n    apns:\n      keyFile: /k.p8\n      keyId: K\n      teamId: T\n", "topic"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := writeFile(t, tc.yaml)
			if _, err := (Loader{FilePath: p, ExplicitFile: true, Getenv: noEnv}).Load(); err == nil ||
				!strings.Contains(err.Error(), tc.want) {
				t.Errorf("want boot error naming %q, got %v", tc.want, err)
			}
		})
	}

	// Identifiers without keyFile fail boot (the key file is the enable knob).
	orphan := writeFile(t, "approval:\n  push:\n    apns:\n      keyId: ABC123DEFG\n      teamId: TEAM567890\n")
	if _, err := (Loader{FilePath: orphan, ExplicitFile: true, Getenv: noEnv}).Load(); err == nil ||
		!strings.Contains(err.Error(), "keyFile") {
		t.Errorf("identifiers without keyFile: want enable-knob error, got %v", err)
	}

	// An unknown environment fails boot instead of silently picking a cluster
	// (a cross-environment send dies as BadDeviceToken, far from the typo).
	badEnv := writeFile(t, "approval:\n  push:\n    apns:\n      keyFile: /k.p8\n      keyId: K\n      teamId: T\n      topic: b\n      environment: staging\n")
	if _, err := (Loader{FilePath: badEnv, ExplicitFile: true, Getenv: noEnv}).Load(); err == nil ||
		!strings.Contains(err.Error(), "environment") {
		t.Errorf("bad environment: want environment error, got %v", err)
	}
}

// TestFCMAppConfigEnvFaces: the public app-config trio has env faces
// (container deployments), and the all-or-nothing rule sees the merged
// yaml+env result: a partial env set fails boot like a partial yaml set.
func TestFCMAppConfigEnvFaces(t *testing.T) {
	cfg, err := Loader{Getenv: envMap(map[string]string{
		"STRAZA_APPROVAL_PUSH_FCM_PROJECT_ID": "proj",
		"STRAZA_APPROVAL_PUSH_FCM_APP_ID":     "1:407:android:ab12",
		"STRAZA_APPROVAL_PUSH_FCM_API_KEY":    "AIzaExample",
		"STRAZA_APPROVAL_PUSH_FCM_SENDER_ID":  "407",
	})}.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	f := cfg.Approval.Push.FCM
	if !f.AppConfigured() || f.AppID != "1:407:android:ab12" || f.APIKey != "AIzaExample" || f.SenderID != "407" {
		t.Errorf("fcm app config via env = %+v, want the full advertised set", f)
	}
	if _, err := (Loader{Getenv: envMap(map[string]string{
		"STRAZA_APPROVAL_PUSH_FCM_APP_ID": "1:407:android:ab12",
	})}).Load(); err == nil || !strings.Contains(err.Error(), "all-or-nothing") {
		t.Errorf("partial env app config: want all-or-nothing boot error, got %v", err)
	}
}

// TestApprovalPushValidation: enabling FCM requires BOTH the service-account
// file and the project id.
func TestApprovalPushValidation(t *testing.T) {
	noFile := writeFile(t, "approval:\n  push:\n    fcm:\n      enabled: true\n      projectId: p\n")
	if _, err := (Loader{FilePath: noFile, ExplicitFile: true, Getenv: noEnv}).Load(); err == nil ||
		!strings.Contains(err.Error(), "serviceAccountFile is required") {
		t.Errorf("FCM enabled without serviceAccountFile: want file-required error, got %v", err)
	}
	noProj := writeFile(t, "approval:\n  push:\n    fcm:\n      enabled: true\n      serviceAccountFile: /f\n")
	if _, err := (Loader{FilePath: noProj, ExplicitFile: true, Getenv: noEnv}).Load(); err == nil ||
		!strings.Contains(err.Error(), "projectId is required") {
		t.Errorf("FCM enabled without projectId: want projectId-required error, got %v", err)
	}
	// Both present validates clean (file need not exist at config-validate time).
	ok := writeFile(t, "approval:\n  push:\n    fcm:\n      enabled: true\n      serviceAccountFile: /f\n      projectId: p\n")
	if _, err := (Loader{FilePath: ok, ExplicitFile: true, Getenv: noEnv}).Load(); err != nil {
		t.Errorf("fully-configured FCM must validate, got %v", err)
	}
}

// TestApprovalPushRelayKnobs pins the hosted-relay lane's config faces:
// off by default, tokenFile required when
// enabled, env faces wired, and the two same-lane conflicts are boot errors
// naming the choice instead of a silent preference.
func TestApprovalPushRelayKnobs(t *testing.T) {
	t.Run("defaults off", func(t *testing.T) {
		cfg, err := Loader{Getenv: noEnv}.Load()
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		r := cfg.Approval.Push.Relay
		if r.Enabled || r.URL != "" || r.TokenFile != "" {
			t.Errorf("relay must default fully off, got %+v", r)
		}
	})

	t.Run("env faces move the fields", func(t *testing.T) {
		cfg, err := Loader{Getenv: envMap(map[string]string{
			"STRAZA_APPROVAL_PUSH_RELAY_ENABLED":    "true",
			"STRAZA_APPROVAL_PUSH_RELAY_URL":        "https://relay.example.org",
			"STRAZA_APPROVAL_PUSH_RELAY_TOKEN_FILE": "/var/lib/straza/relay-token",
		})}.Load()
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		r := cfg.Approval.Push.Relay
		if !r.Enabled || r.URL != "https://relay.example.org" || r.TokenFile != "/var/lib/straza/relay-token" {
			t.Errorf("relay env faces did not land: %+v", r)
		}
	})

	t.Run("enabled requires tokenFile", func(t *testing.T) {
		_, err := Loader{Getenv: envMap(map[string]string{
			"STRAZA_APPROVAL_PUSH_RELAY_ENABLED": "true",
		})}.Load()
		if err == nil || !strings.Contains(err.Error(), "tokenFile") {
			t.Fatalf("want tokenFile-required boot error, got %v", err)
		}
	})

	t.Run("conflict with the BYO FCM sender is a boot error", func(t *testing.T) {
		_, err := Loader{Getenv: envMap(map[string]string{
			"STRAZA_APPROVAL_PUSH_RELAY_ENABLED":            "true",
			"STRAZA_APPROVAL_PUSH_RELAY_TOKEN_FILE":         "/var/lib/straza/relay-token",
			"STRAZA_APPROVAL_PUSH_FCM_ENABLED":              "true",
			"STRAZA_APPROVAL_PUSH_FCM_SERVICE_ACCOUNT_FILE": "/etc/straza/fcm-sa.json",
			"STRAZA_APPROVAL_PUSH_FCM_PROJECT_ID":           "p",
		})}.Load()
		if err == nil || !strings.Contains(err.Error(), "relay") || !strings.Contains(err.Error(), "fcm") {
			t.Fatalf("want a conflict error naming both lanes, got %v", err)
		}
	})

	t.Run("conflict with the direct APNs sender is a boot error", func(t *testing.T) {
		_, err := Loader{Getenv: envMap(map[string]string{
			"STRAZA_APPROVAL_PUSH_RELAY_ENABLED":    "true",
			"STRAZA_APPROVAL_PUSH_RELAY_TOKEN_FILE": "/var/lib/straza/relay-token",
			"STRAZA_APPROVAL_PUSH_APNS_KEY_FILE":    "/etc/straza/apns.p8",
			"STRAZA_APPROVAL_PUSH_APNS_KEY_ID":      "K",
			"STRAZA_APPROVAL_PUSH_APNS_TEAM_ID":     "T",
			"STRAZA_APPROVAL_PUSH_APNS_TOPIC":       "ai.straza.approver",
		})}.Load()
		if err == nil || !strings.Contains(err.Error(), "relay") || !strings.Contains(err.Error(), "apns") {
			t.Fatalf("want a conflict error naming both lanes, got %v", err)
		}
	})
}
