package agentguard

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"

	"github.com/strazahq/straza/internal/clientassertion"
	"github.com/strazahq/straza/internal/oidcflow"
)

// Headless NHI enrollment: no browser, no
// device row, no long-lived bearer token. The durable credential is the
// local Ed25519 key (standalone: the issuer's client_credentials grant with
// a signed assertion) or the IdP client secret (enterprise: the external
// IdP's own client_credentials grant). Every session start mints a fresh
// short ID token non-interactively and checks in DEVICELESS: the session
// binds (nhi, harness, session), which is exactly what a fleet wants.

// Identity.Headless values.
const (
	// HeadlessKey: standalone, signing an RFC 7523 assertion with the local
	// NHI key (state/nhi-key.json) at the built-in issuer.
	HeadlessKey = "nhi-key"
	// HeadlessClientCreds: enterprise, using OAuth2 client_credentials at the
	// external IdP with STRAZA_CLIENT_ID / STRAZA_CLIENT_SECRET.
	HeadlessClientCreds = "client-credentials"
)

// NHIKey is the local private half of the standalone headless credential
// (state/nhi-key.json, 0600). The public half is registered by an admin:
// `strazactl users nhi-key set <username> <publicKey>`.
type NHIKey struct {
	Seed     string `json:"seed"`               // base64 std, ed25519 seed
	Username string `json:"username,omitempty"` // the NHI this key speaks for
}

// LoadNHIKey reads the local NHI key (absent = not a headless enrollment).
func (s *Store) LoadNHIKey() (NHIKey, error) {
	var k NHIKey
	return k, s.readJSON(s.statePath("nhi-key.json"), &k)
}

// SaveNHIKey persists the local NHI key (0600).
func (s *Store) SaveNHIKey(k NHIKey) error {
	return s.writeJSON(s.statePath("nhi-key.json"), k)
}

// Keygen generates the local Ed25519 keypair for headless enrollment and
// prints the public half plus the registration the admin must run. The
// private seed never leaves the straza home.
func Keygen(store *Store, username string, force bool, w io.Writer) error {
	if !force {
		if _, err := store.LoadNHIKey(); err == nil {
			return fmt.Errorf("an NHI key already exists. Pass --force to replace it (the currently registered public key stops working)")
		}
	}
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return fmt.Errorf("generate key: %w", err)
	}
	if err := store.SaveNHIKey(NHIKey{
		Seed:     base64.StdEncoding.EncodeToString(priv.Seed()),
		Username: username,
	}); err != nil {
		return err
	}
	pubB64 := base64.StdEncoding.EncodeToString(pub)
	name := username
	if name == "" {
		name = "<nhi-username>"
	}
	fmt.Fprintf(w, "NHI key generated (private half stays in %s).\n", store.statePath("nhi-key.json"))
	fmt.Fprintf(w, "Public key: %s\n\n", pubB64)
	fmt.Fprintf(w, "An administrator registers it with:\n  strazactl users nhi-key set %s %s\n", name, pubB64)
	fmt.Fprintf(w, "(or: PUT /v1/admin/users/{id}/nhi-key {\"public_key\":\"%s\"})\n", pubB64)
	fmt.Fprintf(w, "Then enroll headless:\n  straza enroll --server <strazad-url> --headless --user %s\n", name)
	return nil
}

// EnrollHeadless is the non-interactive enroll: prove the credential works
// by minting a token, pin the snapshot keys, persist config + identity. No
// /v1/enroll call: headless NHIs have no device row.
func EnrollHeadless(ctx context.Context, store *Store, serverURL, username string, w io.Writer) error {
	client := NewClient(serverURL)
	offer, err := client.DiscoverNHIOffer(ctx)
	if err != nil {
		return err
	}
	hasKey := false
	if _, err := store.LoadNHIKey(); err == nil {
		hasKey = true
	}

	// Lane pick: a local key wins wherever the server offers the key lane
	// (nhi_issuer, or the server being its own issuer). Otherwise the human
	// flow decides: an external issuer means the IdP client-secret lane, the
	// server itself means the key lane is the only headless option and the
	// proof step below says how to mint a key. The offer is judged on
	// idp.json alone, never on whether the offered issuer answers a dial:
	// reachability must not pick the lane.
	var mode string
	var flow oidcflow.Flow
	switch {
	case offer.Offered && hasKey:
		mode = HeadlessKey
	default:
		human, err := client.DiscoverLogin(ctx)
		if err != nil {
			return err
		}
		if offer.Offered && human.Issuer == strings.TrimSuffix(serverURL, "/") {
			mode = HeadlessKey
		} else {
			mode, flow = HeadlessClientCreds, human
			fmt.Fprintf(w, "Headless login at your identity provider: %s (client_credentials)\n", flow.Issuer)
		}
	}
	if mode == HeadlessKey {
		// Dialed only now that the key lane is the picked lane: an unused
		// lane's issuer never blocks enroll, and an unreachable picked lane
		// stays a hard error, never a downgrade to the secret lane.
		flow, err = client.ResolveNHI(ctx, offer)
		if err != nil {
			return err
		}
	}
	if mode == HeadlessKey && username == "" {
		if k, err := store.LoadNHIKey(); err == nil && k.Username != "" {
			username = k.Username
		}
	}
	if mode == HeadlessKey && username == "" {
		return fmt.Errorf("--user is required for headless enroll (the NHI username the key is registered under)")
	}

	// Prove the credential before persisting anything: a failed grant here
	// is an actionable enroll error, not a mystery at the first hook.
	if _, err := fetchHeadlessToken(ctx, client, store, flow, mode, username); err != nil {
		return err
	}

	keys, err := client.SnapshotKeys(ctx)
	if err != nil {
		return fmt.Errorf("fetch snapshot keys: %w", err)
	}
	if err := store.SaveConfig(Config{ServerURL: serverURL, SnapshotKeys: keys}); err != nil {
		return err
	}
	if err := store.SaveIdentity(Identity{Username: username, Headless: mode}); err != nil {
		return err
	}
	fmt.Fprintf(w, "Enrolled headless as %s (%s; sessions are deviceless, and the local credential mints each session's token).\n", username, mode)
	return nil
}

// headlessIDToken mints a fresh short ID token for a session start using the
// enrolled headless mode. The key lane aims at the NHI issuer, never the
// human IdP: an enterprise agent box needs no route to the IdP at all.
// Fails closed with actionable errors.
func headlessIDToken(ctx context.Context, store *Store, serverURL string, id Identity) (string, error) {
	client := NewClient(serverURL)
	var flow oidcflow.Flow
	switch id.Headless {
	case HeadlessKey:
		f, offered, err := client.DiscoverNHI(ctx)
		if err != nil {
			return "", err
		}
		if !offered {
			return "", fmt.Errorf("the server no longer offers the NHI key lane. Re-run `straza enroll --headless` to pick the current lane")
		}
		flow = f
	default:
		f, err := client.DiscoverLogin(ctx)
		if err != nil {
			return "", err
		}
		flow = f
	}
	return fetchHeadlessToken(ctx, client, store, flow, id.Headless, id.Username)
}

type headlessTokenResponse struct {
	IDToken     string `json:"id_token"`
	AccessToken string `json:"access_token"`
	Error       string `json:"error"`
	ErrorDesc   string `json:"error_description"`
}

func fetchHeadlessToken(ctx context.Context, client *Client, store *Store, flow oidcflow.Flow, mode, username string) (string, error) {
	form := url.Values{"grant_type": {"client_credentials"}}
	switch mode {
	case HeadlessKey:
		k, err := store.LoadNHIKey()
		if err != nil {
			return "", fmt.Errorf("no NHI key. Run `straza keygen` and register the public key: %w", err)
		}
		seed, err := base64.StdEncoding.DecodeString(k.Seed)
		if err != nil || len(seed) != ed25519.SeedSize {
			return "", fmt.Errorf("NHI key file is corrupt. Re-run `straza keygen --force` and re-register")
		}
		assertion, err := clientassertion.Mint(ed25519.NewKeyFromSeed(seed), username, flow.Issuer, 0)
		if err != nil {
			return "", err
		}
		form.Set("client_id", username)
		form.Set("client_assertion_type", "urn:ietf:params:oauth:client-assertion-type:jwt-bearer")
		form.Set("client_assertion", assertion)
	case HeadlessClientCreds:
		cid, secret := os.Getenv("STRAZA_CLIENT_ID"), os.Getenv("STRAZA_CLIENT_SECRET")
		if cid == "" || secret == "" {
			return "", fmt.Errorf("enterprise headless needs STRAZA_CLIENT_ID and STRAZA_CLIENT_SECRET in the environment (the client this AI agent has at your identity provider), or a key of its own instead: run `straza keygen`, have an admin register the public key, and re-enroll")
		}
		form.Set("client_id", cid)
		form.Set("client_secret", secret)
		form.Set("scope", "openid")
	default:
		return "", fmt.Errorf("unknown headless mode %q. Re-run `straza enroll --headless`", mode)
	}

	var out headlessTokenResponse
	if err := client.postFormOAuth(ctx, flow.TokenURL, form, &out); err != nil {
		return "", fmt.Errorf("headless token request failed: %w", err)
	}
	if out.Error != "" {
		msg := fmt.Sprintf("headless login refused (%s): %s. Check the registered key/client and that the identity is an active NHI", out.Error, out.ErrorDesc)
		if out.Error == "invalid_client" || out.Error == "unauthorized_client" {
			// The issuer judged this client, so the refusal carries a status
			// and a renewal counts it as the server's refusal (renewSession),
			// not as a failure to reach it. postFormOAuth keeps no status for
			// an OAuth error body, and the issuer answers these with a 4xx.
			return "", &StatusError{Status: http.StatusBadRequest, Msg: msg}
		}
		return "", errors.New(msg)
	}
	tok := out.IDToken
	if tok == "" {
		tok = out.AccessToken // some IdPs answer client_credentials with a JWT access token only
	}
	if tok == "" {
		return "", fmt.Errorf("identity provider returned no token")
	}
	return tok, nil
}
