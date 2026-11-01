package config

import (
	"testing"
)

// TestMinSignIn pins the sign-in floor of a person's key enrollments: code
// under standalone and browser under enterprise, the env face, the order
// code below browser with any other kind below both, and a boot failure that
// names the two values for any other setting.
func TestMinSignIn(t *testing.T) {
	for profile, want := range map[string]string{ProfileStandalone: SignInCode, ProfileEnterprise: SignInBrowser} {
		if got := defaults(profile).Governance.MinSignIn; got != want {
			t.Errorf("%s: minSignIn default = %q, want %q", profile, got, want)
		}
	}
	const refused = `governance.minSignIn must be browser or code, not "password". ` +
		"Set browser to accept only a sign-in in a browser on the machine that enrolls, " +
		"or code to also accept a sign-in code approved on another device"
	cases := []struct {
		name    string
		profile string
		env     string
		want    string
		err     string
	}{
		{"standalone unset keeps code", ProfileStandalone, "", SignInCode, ""},
		{"enterprise unset keeps browser", ProfileEnterprise, "", SignInBrowser, ""},
		{"browser on standalone", ProfileStandalone, "browser", SignInBrowser, ""},
		{"code on enterprise", ProfileEnterprise, "code", SignInCode, ""},
		{"another value fails boot", ProfileStandalone, "password", "", refused},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env := map[string]string{"STRAZA_PROFILE": tc.profile, "STRAZA_MIN_SIGN_IN": tc.env}
			if tc.profile == ProfileEnterprise {
				env["STRAZA_STORE_DSN"] = "postgres://straza@db/straza"
				env["STRAZA_OIDC_ISSUER"] = "https://idp.example.com/realms/straza"
			}
			cfg, err := Loader{Getenv: envMap(env)}.Load()
			if tc.err != "" {
				if err == nil || err.Error() != tc.err {
					t.Fatalf("Load error = %v, want %q", err, tc.err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if cfg.Governance.MinSignIn != tc.want {
				t.Errorf("minSignIn = %q, want %q", cfg.Governance.MinSignIn, tc.want)
			}
		})
	}
	if SignInRank(SignInCode) != 1 || SignInRank(SignInBrowser) != 2 {
		t.Errorf("sign-in ranks = code %d, browser %d, want 1 and 2", SignInRank(SignInCode), SignInRank(SignInBrowser))
	}
	for _, kind := range []string{"", "password", "Browser"} {
		if SignInRank(kind) != 0 {
			t.Errorf("sign-in kind %q must rank 0, so an unknown lane fails every setting", kind)
		}
	}
}

// TestOIDCLoopbackPort pins the port of the browser sign-in's loopback
// redirect: 0 for any port by default, the env face, the range 1024 to 65535,
// and a boot failure that says what to set for a port outside it or a value
// that is not a number.
func TestOIDCLoopbackPort(t *testing.T) {
	for _, profile := range []string{ProfileStandalone, ProfileEnterprise} {
		if got := defaults(profile).OIDC.LoopbackPort; got != 0 {
			t.Errorf("%s: loopbackPort default = %d, want 0", profile, got)
		}
	}
	const refused = "oidc.loopbackPort must be 0 or a port from 1024 to 65535. Set it to the port of the redirect " +
		"http://127.0.0.1:<port>/callback registered for Straza at your identity provider, or to 0 to let each sign-in pick a free port"
	cases := []struct {
		env  string
		want int
		err  string
	}{
		{"", 0, ""},
		{"0", 0, ""},
		{"1024", 1024, ""},
		{"8400", 8400, ""},
		{"65535", 65535, ""},
		{"1023", 0, refused},
		{"65536", 0, refused},
		{"-1", 0, refused},
		{"any", 0, refused},
	}
	for _, tc := range cases {
		t.Run("env "+tc.env, func(t *testing.T) {
			cfg, err := Loader{Getenv: envMap(map[string]string{"STRAZA_OIDC_LOOPBACK_PORT": tc.env})}.Load()
			if tc.err != "" {
				if err == nil || err.Error() != tc.err {
					t.Fatalf("Load error = %v, want %q", err, tc.err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if cfg.OIDC.LoopbackPort != tc.want {
				t.Errorf("loopbackPort = %d, want %d", cfg.OIDC.LoopbackPort, tc.want)
			}
		})
	}
}
