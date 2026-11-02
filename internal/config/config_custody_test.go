package config

import (
	"testing"
)

// TestMinCustody pins the custody gate of key enrollments: user under
// standalone and account under enterprise, the env face, the order user
// below account with any other kind below both, and a boot failure that
// names the two values for any other setting.
func TestMinCustody(t *testing.T) {
	for profile, want := range map[string]string{ProfileStandalone: CustodyUser, ProfileEnterprise: CustodyAccount} {
		if got := defaults(profile).Governance.MinCustody; got != want {
			t.Errorf("%s: minCustody default = %q, want %q", profile, got, want)
		}
	}
	const refused = `governance.minCustody must be account or user, not "root". ` +
		"Set account to accept only machine keys the Straza background service holds, " +
		"or user to also accept a key kept in the person's own account"
	cases := []struct {
		name string
		env  string
		want string
		err  string
	}{
		{"unset keeps the default", "", CustodyUser, ""},
		{"account", "account", CustodyAccount, ""},
		{"user", "user", CustodyUser, ""},
		{"another value fails boot", "root", "", refused},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg, err := Loader{Getenv: envMap(map[string]string{"STRAZA_MIN_CUSTODY": tc.env})}.Load()
			if tc.err != "" {
				if err == nil || err.Error() != tc.err {
					t.Fatalf("Load error = %v, want %q", err, tc.err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if cfg.Governance.MinCustody != tc.want {
				t.Errorf("minCustody = %q, want %q", cfg.Governance.MinCustody, tc.want)
			}
		})
	}
	if CustodyRank(CustodyUser) >= CustodyRank(CustodyAccount) {
		t.Error("custody ranks must order user below account")
	}
	for _, kind := range []string{"", "root", "Account"} {
		if CustodyRank(kind) >= CustodyRank(CustodyUser) {
			t.Errorf("custody kind %q must rank below user, so a row without a known custody fails every setting", kind)
		}
	}
}
