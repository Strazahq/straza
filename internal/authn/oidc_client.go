package authn

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"

	"github.com/coreos/go-oidc/v3/oidc"

	"github.com/strazahq/straza/internal/store"
)

// ErrUnknownIdentity is returned when an IdP-verified identity has no local
// user and JIT provisioning is disabled (the enterprise default: SCIM is
// the source of truth).
var ErrUnknownIdentity = errors.New("authn: identity not provisioned")

// ErrProtectedAccount is returned when an IdP-verified identity resolves to
// the protected local account, which signs in with its own password only.
var ErrProtectedAccount = errors.New("authn: identity resolves to the protected local account")

// DisabledUserError is VerifyLogin's refusal of an IdP-verified identity
// whose local user is not active. User is that row, so the caller can name
// the person in its refusal and its login failure record.
type DisabledUserError struct{ User store.User }

func (e *DisabledUserError) Error() string {
	return fmt.Sprintf("authn: user %s is disabled", e.User.Username)
}

// ExternalVerifier validates ID tokens minted by an external OIDC IdP
// (Keycloak, Okta, Entra, …) and maps their claims onto local users.
//
// Mapping order: external_id == sub, then username == preferred_username,
// then email. A matched user without an external_id gets it backfilled
// (account linking). With JIT enabled a missing user is created.
type ExternalVerifier struct {
	verifier *oidc.IDTokenVerifier
	users    store.UserRepo
	jit      bool

	// BootstrapUsername is the one identity provisioned despite JIT being
	// off (the first-admin bootstrap, `oidc.bootstrapAdmin`). The verifier
	// only creates the user. Granting straza-admin is the server's job.
	BootstrapUsername string

	// ProtectedUsername names the local break-glass account. No external
	// identity resolves to it: not by its external id, not by the username
	// fallback, which would link it, and not by provisioning. Such a login
	// fails with ErrProtectedAccount and writes nothing. "" means no
	// protected account.
	ProtectedUsername string
}

// NewExternalVerifier runs OIDC discovery against issuerURL and returns a
// verifier for tokens with audience clientID. A non-empty discoveryURL is
// where the discovery document is fetched instead (oidc.discoveryUrl); the
// document must name issuerURL byte for byte, and tokens must carry it
// either way. Discovery failure fails the boot: better a loud start than
// silently unverifiable logins.
func NewExternalVerifier(ctx context.Context, issuerURL, discoveryURL, clientID string, users store.UserRepo, jit bool) (*ExternalVerifier, error) {
	var provider *oidc.Provider
	var err error
	if discoveryURL == "" {
		provider, err = oidc.NewProvider(ctx, issuerURL)
		if err != nil {
			return nil, fmt.Errorf("authn: oidc discovery %s: %w", issuerURL, err)
		}
	} else if provider, err = discoverAt(ctx, issuerURL, discoveryURL); err != nil {
		return nil, err
	}
	verifier := provider.Verifier(&oidc.Config{
		ClientID:             clientID,
		SupportedSigningAlgs: []string{oidc.RS256, oidc.ES256, oidc.EdDSA},
	})
	return &ExternalVerifier{verifier: verifier, users: users, jit: jit}, nil
}

// discoverAt fetches the discovery document at discoveryURL and builds the
// provider from it once the document names issuerURL exactly. go-oidc's
// InsecureIssuerURLContext would skip that comparison, so the document is
// decoded and compared here instead: the provider's issuer, which the
// verifier holds every token's iss to, is then the configured one.
func discoverAt(ctx context.Context, issuerURL, discoveryURL string) (*oidc.Provider, error) {
	const fix = "Point oidc.discoveryUrl at your identity provider's /.well-known/openid-configuration address"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, discoveryURL, nil)
	if err != nil {
		return nil, fmt.Errorf("authn: the discovery document address %s, set by oidc.discoveryUrl, is not usable: %w. %s", discoveryURL, err, fix)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("authn: reach the discovery document at %s, set by oidc.discoveryUrl: %w. Check that strazad can reach that address", discoveryURL, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("authn: the discovery document at %s, set by oidc.discoveryUrl, answered HTTP %d. %s", discoveryURL, resp.StatusCode, fix)
	}
	var doc oidc.ProviderConfig
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&doc); err != nil {
		return nil, fmt.Errorf("authn: the discovery document at %s, set by oidc.discoveryUrl, is not OpenID Connect discovery JSON: %w. %s", discoveryURL, err, fix)
	}
	if doc.IssuerURL != issuerURL {
		return nil, fmt.Errorf("authn: the discovery document at %s names issuer %q, but oidc.issuer is %q. Set oidc.issuer to the issuer your identity provider puts in its tokens, or point oidc.discoveryUrl at that provider's own discovery document", discoveryURL, doc.IssuerURL, issuerURL)
	}
	if doc.JWKSURL == "" {
		return nil, fmt.Errorf("authn: the discovery document at %s names no jwks_uri, so strazad cannot check token signatures. %s", discoveryURL, fix)
	}
	return doc.NewProvider(ctx), nil
}

type externalClaims struct {
	Username string `json:"preferred_username"`
	Email    string `json:"email"`
}

// VerifyLogin validates the raw ID token and resolves it to an active local
// user per the mapping rules above. A user who is not active comes back as
// a *DisabledUserError.
func (v *ExternalVerifier) VerifyLogin(ctx context.Context, rawIDToken string) (store.User, error) {
	idToken, err := v.verifier.Verify(ctx, rawIDToken)
	if err != nil {
		return store.User{}, fmt.Errorf("authn: external id token rejected: %w", err)
	}
	var claims externalClaims
	if err := idToken.Claims(&claims); err != nil {
		return store.User{}, fmt.Errorf("authn: parse claims: %w", err)
	}

	u, err := v.mapUser(ctx, idToken.Subject, claims)
	if err != nil {
		return store.User{}, err
	}
	if u.Status != store.UserActive {
		return store.User{}, &DisabledUserError{User: u}
	}
	return u, nil
}

// protects reports whether username is the protected local account.
func (v *ExternalVerifier) protects(username string) bool {
	return v.ProtectedUsername != "" && username == v.ProtectedUsername
}

func (v *ExternalVerifier) mapUser(ctx context.Context, sub string, claims externalClaims) (store.User, error) {
	if u, err := v.users.GetByExternalID(ctx, sub); err == nil {
		// A protected row that already carries this link is refused as well.
		if v.protects(u.Username) {
			return store.User{}, ErrProtectedAccount
		}
		return u, nil
	} else if !errors.Is(err, store.ErrNotFound) {
		return store.User{}, err
	}

	// Fallback matches link the account by backfilling external_id.
	for _, lookup := range []struct {
		key string
		fn  func() (store.User, error)
	}{
		{claims.Username, func() (store.User, error) { return v.users.GetByUsername(ctx, claims.Username) }},
	} {
		if lookup.key == "" {
			continue
		}
		u, err := lookup.fn()
		if errors.Is(err, store.ErrNotFound) {
			continue
		}
		if err != nil {
			return store.User{}, err
		}
		// The row that was found decides, before anything is written to it.
		if v.protects(u.Username) {
			return store.User{}, ErrProtectedAccount
		}
		if u.ExternalID == "" {
			u.ExternalID = sub
			if u, err = v.users.Update(ctx, u); err != nil {
				return store.User{}, fmt.Errorf("authn: link account: %w", err)
			}
		} else if u.ExternalID != sub {
			// Same username, different upstream subject: refuse rather than
			// hijack the existing account.
			return store.User{}, fmt.Errorf("authn: username %q is bound to another identity", u.Username)
		}
		return u, nil
	}

	bootstrap := v.BootstrapUsername != "" && claims.Username == v.BootstrapUsername
	if !v.jit && !bootstrap {
		return store.User{}, fmt.Errorf("%w: subject %s (JIT provisioning is off)", ErrUnknownIdentity, sub)
	}
	username := claims.Username
	if username == "" {
		username = sub
	}
	if v.protects(username) {
		return store.User{}, ErrProtectedAccount
	}
	u, err := v.users.Create(ctx, store.User{
		Username: username, Email: claims.Email, ExternalID: sub, Origin: store.OriginLocal,
	})
	if err != nil {
		return store.User{}, fmt.Errorf("authn: jit provision: %w", err)
	}
	return u, nil
}
