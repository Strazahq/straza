package authn

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/lestrrat-go/jwx/v3/jwa"
	"github.com/lestrrat-go/jwx/v3/jws"
)

// signSessionTyp signs a session claim set with the service's active key
// under a protected header whose typ is typ, or that carries no typ when
// typ is empty.
func signSessionTyp(t *testing.T, svc *TokenService, typ string) string {
	t.Helper()
	now := time.Now().UTC().Truncate(time.Second)
	payload, err := json.Marshal(map[string]any{
		"iss": svc.issuer, "sub": "user-1", "jti": "jti-1", "ses": "ses-1",
		"iat": now.Unix(), "exp": now.Add(DefaultTokenTTL).Unix(),
	})
	if err != nil {
		t.Fatal(err)
	}
	hdr := jws.NewHeaders()
	if typ != "" {
		if err := hdr.Set(jws.TypeKey, typ); err != nil {
			t.Fatal(err)
		}
	}
	svc.mu.RLock()
	key := svc.active
	svc.mu.RUnlock()
	signed, err := jws.Sign(payload, jws.WithKey(jwa.EdDSA(), key, jws.WithProtectedHeaders(hdr)))
	if err != nil {
		t.Fatal(err)
	}
	return string(signed)
}

// TestVerifyRefusesGrantType pins the typ guard in Verify: a token whose
// typ names a git grant is refused even when it carries every session
// claim, the comparison ignores case and an application/ prefix, and every
// other typ still verifies.
func TestVerifyRefusesGrantType(t *testing.T) {
	svc := testService(t, 0)
	cases := []struct {
		name    string
		typ     string
		refused bool
	}{
		{name: "no typ verifies", typ: ""},
		{name: "the session typ JWT verifies", typ: "JWT"},
		{name: "another media type verifies", typ: "at+jwt"},
		{name: "the grant typ is refused", typ: "straza-grant+jwt", refused: true},
		{name: "the grant typ in upper case is refused", typ: "STRAZA-GRANT+JWT", refused: true},
		{name: "the grant typ with application/ is refused", typ: "application/straza-grant+jwt", refused: true},
		{name: "the grant typ with Application/ in mixed case is refused", typ: "Application/Straza-Grant+JWT", refused: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c, err := svc.Verify(signSessionTyp(t, svc, tc.typ))
			if !tc.refused {
				if err != nil {
					t.Fatalf("Verify refused a session token with typ %q: %v", tc.typ, err)
				}
				if c.Session != "ses-1" {
					t.Errorf("Session = %q, want ses-1", c.Session)
				}
				return
			}
			if !errors.Is(err, errGrantTyped) || !strings.HasPrefix(err.Error(), "authn: token rejected: ") {
				t.Fatalf("err = %v, want the git grant refusal", err)
			}
		})
	}
}

// TestVerifyRefusesMintedGrant checks the guard against the real mint: a
// grant from MintGrant fails Verify, and a token from Mint still passes.
func TestVerifyRefusesMintedGrant(t *testing.T) {
	svc := testService(t, 0)
	grant, _, err := svc.MintGrant(sampleGrant())
	if err != nil {
		t.Fatalf("MintGrant: %v", err)
	}
	if _, err := svc.Verify(grant); !errors.Is(err, errGrantTyped) {
		t.Fatalf("Verify(grant) err = %v, want the git grant refusal", err)
	}
	session, _, err := svc.Mint(sampleClaims())
	if err != nil {
		t.Fatalf("Mint: %v", err)
	}
	if _, err := svc.Verify(session); err != nil {
		t.Fatalf("Verify(session token): %v", err)
	}
}

// TestVerifyIDTokenRefusesGrant checks the guard in VerifyIDToken. A grant
// whose aud equals the audience an ID token is checked against, and whose
// sub is set, would otherwise pass as an ID token.
func TestVerifyIDTokenRefusesGrant(t *testing.T) {
	svc := testService(t, 0)
	const audience = "straza-cli"
	g := sampleGrant()
	g.Audience = audience
	grant, _, err := svc.MintGrant(g)
	if err != nil {
		t.Fatalf("MintGrant: %v", err)
	}
	idToken, err := svc.MintIDToken("user-1", audience, time.Minute, "ada", "ada@example.com")
	if err != nil {
		t.Fatalf("MintIDToken: %v", err)
	}
	cases := []struct {
		name    string
		raw     string
		refused bool
	}{
		{name: "an ID token for the audience verifies", raw: idToken},
		{name: "a grant for the same audience is refused", raw: grant, refused: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c, err := svc.VerifyIDToken(tc.raw, audience)
			if !tc.refused {
				if err != nil || c.Subject != "user-1" {
					t.Fatalf("VerifyIDToken = %+v, %v, want subject user-1", c, err)
				}
				return
			}
			if !errors.Is(err, errGrantTyped) || !strings.HasPrefix(err.Error(), "authn: id token rejected: ") {
				t.Fatalf("err = %v, want the git grant refusal", err)
			}
		})
	}
}
