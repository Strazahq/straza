package config

import (
	"strings"
	"testing"
)

// TestLifetimeNotice pins the boot warning for a device credential lifetime
// too short for the session lifetime: a client presents the credential only
// at a session start and after the janitor closed one, up to about seven
// minutes past the lifetime, and renews it only past half its life, so the
// safe pair has the credential at least twice the session lifetime plus that
// slack. The defaults stay silent; a short pair names both values and the
// least safe credential lifetime.
func TestLifetimeNotice(t *testing.T) {
	cases := []struct {
		name     string
		device   string
		session  string
		contains []string
	}{
		{"defaults are silent", "", "", nil},
		{"a day against the default session is silent", "24h14m", "", nil},
		{"the clock scenario pair warns", "6m45s", "3m45s", []string{"deviceTokenTTL 6m45s", "sessionMaxLifetime 3m45s", "at least 21m30s"}},
		{"twenty hours against twelve warns", "20h", "12h", []string{"deviceTokenTTL 20h0m0s", "at least 24h14m0s", "locked out"}},
		{"just under the bound warns", "24h13m", "12h", []string{"at least 24h14m0s"}},
		{"a credential shorter than one session warns", "8h", "", []string{"deviceTokenTTL 8h0m0s", "sessionMaxLifetime 12h0m0s"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg, err := Loader{Getenv: envMap(map[string]string{
				"STRAZA_DEVICE_TOKEN_TTL":     tc.device,
				"STRAZA_SESSION_MAX_LIFETIME": tc.session,
			})}.Load()
			if err != nil {
				t.Fatal(err)
			}
			if tc.contains == nil {
				if len(cfg.Notices) != 0 {
					t.Fatalf("Notices = %q, want none", cfg.Notices)
				}
				return
			}
			if len(cfg.Notices) != 1 {
				t.Fatalf("Notices = %q, want exactly one lifetime notice", cfg.Notices)
			}
			for _, want := range tc.contains {
				if !strings.Contains(cfg.Notices[0], want) {
					t.Errorf("notice %q does not contain %q", cfg.Notices[0], want)
				}
			}
		})
	}
}
