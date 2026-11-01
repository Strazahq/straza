package config

import (
	"strings"
	"testing"
	"time"
)

// TestSignInMaxAge pins the sign-in deadline of key enrollments: 168h in both
// profiles, the same default when the file leaves the key unset or sets it to
// zero, the set value otherwise, and a negative or unparseable env value
// fails boot with a sentence that names the knob.
func TestSignInMaxAge(t *testing.T) {
	for _, profile := range []string{ProfileStandalone, ProfileEnterprise} {
		if cfg := defaults(profile); cfg.Governance.SignInMaxAge != 168*time.Hour {
			t.Errorf("%s: signInMaxAge default = %s, want 168h", profile, cfg.Governance.SignInMaxAge)
		}
	}
	cases := []struct {
		name string
		env  string
		want time.Duration
		err  string
	}{
		{"unset keeps the default", "", 168 * time.Hour, ""},
		{"set value wins", "24h", 24 * time.Hour, ""},
		{"negative fails boot", "-1h", 0, "governance.signInMaxAge must be a positive duration"},
		{"unparseable fails boot", "a week", 0, "governance.signInMaxAge must be a positive duration"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg, err := Loader{Getenv: envMap(map[string]string{"STRAZA_SIGN_IN_MAX_AGE": tc.env})}.Load()
			if tc.err != "" {
				if err == nil || !strings.Contains(err.Error(), tc.err) {
					t.Fatalf("want error containing %q, got %v", tc.err, err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got := cfg.EffectiveSignInMaxAge(); got != tc.want {
				t.Errorf("EffectiveSignInMaxAge = %s, want %s", got, tc.want)
			}
		})
	}
	var zero Config
	if got := zero.EffectiveSignInMaxAge(); got != 168*time.Hour {
		t.Errorf("zero config EffectiveSignInMaxAge = %s, want 168h", got)
	}
}
