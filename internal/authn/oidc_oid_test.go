package authn

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/lestrrat-go/jwx/v3/jwa"
	"github.com/lestrrat-go/jwx/v3/jwt"

	"github.com/strazahq/straza/internal/store"
)

// The values of an Entra lab sign-in. The tenant is the lab's and the rest is
// synthetic.
const (
	entraLabIssuer = "https://login.microsoftonline.com/3c6bff24-ce9b-405c-8251-fe86232353b6/v2.0"
	labOID         = "0b5c5a1e-2f47-4c1a-9d1e-6a7f3e2b9c40"
	labSub         = "pairwise-subject-of-this-app"
	labUser        = "ada@lab.example"
)

// signAs signs an ID token for the audience straza with iss, sub, labUser
// as preferred_username and the claims in extra, using the split IdP's
// active key.
func (p *splitIDP) signAs(t *testing.T, iss, sub string, extra map[string]any) string {
	t.Helper()
	now := time.Now()
	b := jwt.NewBuilder().Issuer(iss).Subject(sub).Audience([]string{"straza"}).
		IssuedAt(now).Expiration(now.Add(time.Minute)).Claim("preferred_username", labUser)
	for name, value := range extra {
		b = b.Claim(name, value)
	}
	tok, err := b.Build()
	if err != nil {
		t.Fatal(err)
	}
	p.tokens.mu.RLock()
	key := p.tokens.active
	p.tokens.mu.RUnlock()
	signed, err := jwt.Sign(tok, jwt.WithKey(jwa.EdDSA(), key))
	if err != nil {
		t.Fatal(err)
	}
	return string(signed)
}

// TestExternalVerifierLinksOnOID pins how an ID token finds its account when
// the issuer is an Entra tenant: by oid, then by sub, then by username, where
// a new link stores the oid. Another issuer's oid claim is never read, a
// claim that is not a string or is named oid in another case is no oid, and
// an account linked to another identity is refused with a *LinkMismatchError
// that names it and leaves it as it was.
func TestExternalVerifierLinksOnOID(t *testing.T) {
	oid := map[string]any{"oid": labOID}
	cases := []struct {
		name    string
		issuer  string         // the configured issuer and the token's iss
		claims  map[string]any // the token's claims beside iss, sub, aud and preferred_username
		rows    []store.User   // the platform's accounts before the sign-in
		jit     bool
		protect string
		// want is the index in rows of the account the sign-in resolves to,
		// -1 for one it creates, and wantExt its externalId after the sign-in.
		want    int
		wantExt string
		// wantErr is a refusal errors.Is matches, and mismatch a
		// *LinkMismatchError that names rows[0].
		wantErr  error
		mismatch bool
	}{
		{name: "the oid matches an externalId", issuer: entraLabIssuer, claims: oid,
			rows: []store.User{{Username: "ada-renamed", ExternalID: labOID}}, want: 0, wantExt: labOID},
		{name: "an account linked by sub keeps matching", issuer: entraLabIssuer, claims: oid,
			rows: []store.User{{Username: labUser, ExternalID: labSub}}, want: 0, wantExt: labSub},
		{name: "the oid is asked before the sub", issuer: entraLabIssuer, claims: oid,
			rows: []store.User{{Username: "by-oid", ExternalID: labOID}, {Username: "by-sub", ExternalID: labSub}}, want: 0, wantExt: labOID},
		{name: "an account with no externalId links by username and stores the oid", issuer: entraLabIssuer, claims: oid,
			rows: []store.User{{Username: labUser}}, want: 0, wantExt: labOID},
		{name: "an account with another externalId is refused", issuer: entraLabIssuer, claims: oid,
			rows: []store.User{{Username: labUser, ExternalID: "ada"}}, mismatch: true},
		{name: "the break-glass account linked by oid is refused", issuer: entraLabIssuer, claims: oid,
			rows: []store.User{{Username: "break-glass", ExternalID: labOID}}, protect: "break-glass", wantErr: ErrProtectedAccount},
		{name: "provisioning stores the oid", issuer: entraLabIssuer, claims: oid, jit: true, want: -1, wantExt: labOID},
		{name: "an Entra token without oid links on sub", issuer: entraLabIssuer,
			rows: []store.User{{Username: labUser}}, want: 0, wantExt: labSub},
		{name: "another issuer's oid claim is ignored", issuer: splitCanonical, claims: oid,
			rows: []store.User{{Username: "victim", ExternalID: labOID}}, wantErr: ErrUnknownIdentity},
		{name: "another issuer links on sub as before", issuer: splitCanonical, claims: oid,
			rows: []store.User{{Username: labUser}}, want: 0, wantExt: labSub},
		{name: "another issuer's mismatch is refused as before", issuer: splitCanonical,
			rows: []store.User{{Username: labUser, ExternalID: "elsewhere"}}, mismatch: true},
		{name: "another issuer's oid that is not a string is ignored", issuer: splitCanonical, claims: map[string]any{"oid": 12345},
			rows: []store.User{{Username: labUser}}, want: 0, wantExt: labSub},
		{name: "an Entra oid that is not a string is ignored", issuer: entraLabIssuer, claims: map[string]any{"oid": []string{labOID}},
			rows: []store.User{{Username: labUser}}, want: 0, wantExt: labSub},
		{name: "a claim named oid in another case is not the oid", issuer: entraLabIssuer, claims: map[string]any{"OID": labOID},
			rows: []store.User{{Username: "victim", ExternalID: labOID}, {Username: labUser}}, want: 1, wantExt: labSub},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			idp := newSplitIDP(t, tc.issuer, http.StatusOK)
			users := platformUsers(t)
			before := make([]store.User, len(tc.rows))
			for i, row := range tc.rows {
				var err error
				if before[i], err = users.Create(ctx, row); err != nil {
					t.Fatal(err)
				}
			}
			v, err := NewExternalVerifier(ctx, tc.issuer, idp.discoveryURL(), "straza", users, tc.jit)
			if err != nil {
				t.Fatal(err)
			}
			v.ProtectedUsername = tc.protect

			got, err := v.VerifyLogin(ctx, idp.signAs(t, tc.issuer, labSub, tc.claims))
			unchanged := func(skip int) {
				t.Helper()
				for i, b := range before {
					if i == skip {
						continue
					}
					after, err := users.GetByID(ctx, b.ID)
					if err != nil {
						t.Fatal(err)
					}
					if after.ExternalID != b.ExternalID {
						t.Errorf("the account %s changed its externalId from %q to %q", b.Username, b.ExternalID, after.ExternalID)
					}
				}
			}
			if tc.wantErr != nil || tc.mismatch {
				if got.ID != "" {
					t.Errorf("a refused sign-in returned the user %+v", got)
				}
				if tc.mismatch {
					var mm *LinkMismatchError
					if !errors.As(err, &mm) || mm.User.ID != before[0].ID {
						t.Fatalf("VerifyLogin = %v, want a *LinkMismatchError naming %s", err, before[0].ID)
					}
				} else if !errors.Is(err, tc.wantErr) {
					t.Fatalf("VerifyLogin = %v, want %v", err, tc.wantErr)
				}
				unchanged(-1)
				if all, _ := users.List(ctx); len(all) != len(before) {
					t.Errorf("accounts = %d after a refused sign-in, want %d", len(all), len(before))
				}
				return
			}
			if err != nil {
				t.Fatalf("VerifyLogin: %v", err)
			}
			wantID := ""
			if tc.want >= 0 {
				wantID = before[tc.want].ID
			}
			if (wantID != "" && got.ID != wantID) || (wantID == "" && got.Username != labUser) {
				t.Fatalf("resolved %s (%s), want the account %q", got.Username, got.ID, wantID)
			}
			stored, err := users.GetByID(ctx, got.ID)
			if err != nil {
				t.Fatal(err)
			}
			if stored.ExternalID != tc.wantExt {
				t.Errorf("externalId after the sign-in = %q, want %q", stored.ExternalID, tc.wantExt)
			}
			unchanged(tc.want)
		})
	}
}

// TestEntraIssuer pins the one issuer form whose oid claim links accounts:
// the v2.0 issuer of one Entra tenant on the public cloud, written exactly.
func TestEntraIssuer(t *testing.T) {
	const tenant = "3c6bff24-ce9b-405c-8251-fe86232353b6"
	cases := []struct {
		issuer string
		want   bool
	}{
		{"https://login.microsoftonline.com/" + tenant + "/v2.0", true},
		{"https://login.microsoftonline.com/common/v2.0", false},
		{"https://login.microsoftonline.com/organizations/v2.0", false},
		{"https://login.microsoftonline.com/consumers/v2.0", false},
		{"https://sts.windows.net/" + tenant + "/", false},
		{"https://login.microsoftonline.com/" + tenant + "/v2.0/", false},
		{"https://login.microsoftonline.com/" + tenant + "/oauth2/v2.0", false},
		{"http://login.microsoftonline.com/" + tenant + "/v2.0", false},
		{"https://login.microsoftonline.com.evil.example/" + tenant + "/v2.0", false},
		{"https://evil.example/login.microsoftonline.com/" + tenant + "/v2.0", false},
		{"https://keycloak.example/realms/straza", false},
		{"", false},
	}
	for _, tc := range cases {
		if got := entraIssuer(tc.issuer); got != tc.want {
			t.Errorf("entraIssuer(%q) = %v, want %v", tc.issuer, got, tc.want)
		}
	}
}
