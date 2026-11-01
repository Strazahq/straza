package authn

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/lestrrat-go/jwx/v3/jwa"
	"github.com/lestrrat-go/jwx/v3/jwt"
)

// payloadOf decodes the claim set of a compact token without verifying it.
func payloadOf(t *testing.T, raw string) map[string]any {
	t.Helper()
	parts := strings.Split(raw, ".")
	if len(parts) != 3 {
		t.Fatalf("token has %d parts", len(parts))
	}
	b, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	return m
}

// TestMintVerifyCNF pins the cnf claim: minted as {"jkt": CNF} only when CNF
// is set, and read back by Verify.
func TestMintVerifyCNF(t *testing.T) {
	svc := testService(t, 0)
	cases := []struct {
		name    string
		cnf     string
		wantRaw any // the cnf member in the payload, nil when absent
	}{
		{name: "a key-bound token carries cnf.jkt", cnf: "kPrK_qmxVWaYVA9wwBF6Iuo3vVzz7TxHCTwXBygrS4k",
			wantRaw: map[string]any{"jkt": "kPrK_qmxVWaYVA9wwBF6Iuo3vVzz7TxHCTwXBygrS4k"}},
		{name: "a bearer token carries no cnf"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			in := sampleClaims()
			in.CNF = tc.cnf
			raw, minted, err := svc.Mint(in)
			if err != nil {
				t.Fatal(err)
			}
			if minted.CNF != tc.cnf {
				t.Errorf("minted CNF = %q, want %q", minted.CNF, tc.cnf)
			}
			got, present := payloadOf(t, raw)["cnf"]
			if (tc.wantRaw == nil) == present {
				t.Fatalf("cnf present = %v in the payload, want %v", present, tc.wantRaw != nil)
			}
			if present {
				b, _ := json.Marshal(got)
				w, _ := json.Marshal(tc.wantRaw)
				if string(b) != string(w) {
					t.Errorf("cnf = %s, want %s", b, w)
				}
			}
			back, err := svc.Verify(raw)
			if err != nil {
				t.Fatalf("Verify: %v", err)
			}
			if back.CNF != tc.cnf || back.Session != in.Session || back.Device != in.Device {
				t.Errorf("verified claims %+v, want CNF %q and the minted session and device", back, tc.cnf)
			}
		})
	}
}

// TestVerifyRefusesMalformedCNF signs tokens with the service's own key, so
// only the cnf claim is wrong, and checks Verify refuses each one.
func TestVerifyRefusesMalformedCNF(t *testing.T) {
	svc := testService(t, 0)
	cases := []struct {
		name string
		cnf  any
	}{
		{"cnf is a string", "kPrK_qmxVWaYVA9wwBF6Iuo3vVzz7TxHCTwXBygrS4k"},
		{"cnf is an array", []string{"jkt"}},
		{"cnf has no jkt", map[string]any{"x5t#S256": "abc"}},
		{"cnf.jkt is empty", map[string]any{"jkt": ""}},
		{"cnf.jkt is a number", map[string]any{"jkt": 7}},
		{"cnf is null", nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			now := time.Now()
			tok, err := jwt.NewBuilder().
				Issuer("https://straza.local").Subject("user-1").JwtID("jti-cnf").
				IssuedAt(now).Expiration(now.Add(time.Minute)).
				Claim("ses", "ses-1").Claim("cnf", tc.cnf).Build()
			if err != nil {
				t.Fatal(err)
			}
			svc.mu.RLock()
			key := svc.active
			svc.mu.RUnlock()
			signed, err := jwt.Sign(tok, jwt.WithKey(jwa.EdDSA(), key))
			if err != nil {
				t.Fatal(err)
			}
			if _, ok := payloadOf(t, string(signed))["cnf"]; !ok {
				t.Fatal("the test token carries no cnf member")
			}
			if c, err := svc.Verify(string(signed)); err == nil {
				t.Fatalf("Verify accepted a malformed cnf and returned CNF %q", c.CNF)
			}
		})
	}
}
