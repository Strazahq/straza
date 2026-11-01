package service

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/strazahq/straza/internal/agentguard"
	"github.com/strazahq/straza/internal/agentguard/localapi"
	"github.com/strazahq/straza/internal/clientassertion"
	"github.com/strazahq/straza/internal/dpop"
	"github.com/strazahq/straza/internal/oidcflow"
)

// The lanes of a headless agent enrollment: a key registered at the Straza
// issuer, or a client secret at the identity provider.
const (
	laneKey    = "nhi-key"
	laneSecret = "client-credentials"
)

// keyExistsSentence refuses a keygen that would replace a key without
// --force.
const keyExistsSentence = "A key for you already exists on this machine. Run `straza keygen --force` to replace it, " +
	"and then have an admin register the new public key, because the old one stops working."

// personKeptSentence refuses an agent's keygen or headless enrollment on a
// uid that holds a person's enrollment, so an agent running as the person
// cannot unenroll the person's machine.
const personKeptSentence = "This OS account holds a person's enrollment on this machine, so an agent's key or enrollment cannot replace it. " +
	"Only `straza enroll` replaces a person's machine key. Run the agent under its own OS account."

// holdsPerson reports whether a holds a person's enrollment.
func (a *account) holdsPerson() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.rec != nil && a.rec.Kind == localapi.KindPerson
}

// cachedToken is an agent's ID token, kept for its life, because the token
// endpoint allows 2 requests per second per address.
type cachedToken struct {
	value string
	until time.Time
}

// agentSecret is the file of the client-secret lane.
type agentSecret struct {
	ClientID     string `json:"client_id"`
	ClientSecret string `json:"client_secret"` // #nosec G117 -- the service reads this credential file, and never writes or returns it
}

// Keygen makes the key of c's agent and returns its public half. The key
// replaces any enrollment of c, which then has to enroll again.
func (s *credStore) Keygen(ctx context.Context, c Caller, req localapi.KeygenRequest) (localapi.KeygenAnswer, error) {
	a := s.account(c.UID)
	if err := a.keyLock.lock(ctx); err != nil {
		return localapi.KeygenAnswer{}, err
	}
	defer a.keyLock.unlock()
	if a.holdsPerson() {
		return localapi.KeygenAnswer{}, &Refused{Status: http.StatusConflict, Sentence: personKeptSentence}
	}
	a.mu.Lock()
	exists := a.key != nil || a.rec != nil
	a.mu.Unlock()
	if exists && !req.Force {
		return localapi.KeygenAnswer{}, &Refused{Status: http.StatusConflict, Sentence: keyExistsSentence}
	}
	pub, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return localapi.KeygenAnswer{}, err
	}
	if err := s.replace(a, enrollment{Username: req.Username, KeyThumbprint: dpop.Thumbprint(pub)}, key); err != nil {
		return localapi.KeygenAnswer{}, err
	}
	s.log.Info("made an agent key", "uid", c.UID, "user", req.Username)
	return localapi.KeygenAnswer{Username: req.Username, PublicKey: base64.StdEncoding.EncodeToString(pub)}, nil
}

// enrollAgent enrolls c as the agent username without a browser. A key
// from `straza keygen` wins wherever the server offers the key lane.
// Otherwise an external identity provider means the client-secret lane,
// and a server that is its own issuer means the key lane. The lane is
// judged on idp.json alone, never on whether its issuer answers. The
// credential is proven before anything is kept.
func (s *credStore) enrollAgent(ctx context.Context, c Caller, server string, keys map[string]string, username string, emit func(localapi.EnrollEvent)) (localapi.EnrollResult, error) {
	a := s.account(c.UID)
	if a.holdsPerson() {
		return localapi.EnrollResult{}, &Refused{Status: http.StatusConflict, Sentence: personKeptSentence}
	}
	plain := agentguard.NewClient(server)
	offer, err := plain.DiscoverNHIOffer(ctx)
	if err != nil {
		return localapi.EnrollResult{}, err
	}
	a.mu.Lock()
	var key ed25519.PrivateKey
	if a.rec != nil && a.rec.Kind != localapi.KindPerson && !a.changed {
		key = a.key
		if username == "" {
			username = a.rec.Username
		}
	}
	a.mu.Unlock()
	lane, flow := laneKey, oidcflow.Flow{}
	if !offer.Offered || key == nil {
		human, err := plain.DiscoverLogin(ctx)
		if err != nil {
			return localapi.EnrollResult{}, err
		}
		if !offer.Offered || human.Issuer != server {
			lane, flow = laneSecret, human
			emit(localapi.EnrollEvent{Event: localapi.EnrollNote, Text: "Signing in at your identity provider " + human.Issuer + " with the agent's client secret."})
		}
	}
	var secret agentSecret
	switch {
	case lane == laneKey && key == nil:
		return localapi.EnrollResult{}, &Refused{Status: http.StatusPreconditionFailed, Sentence: "This agent has no key on this machine yet. " +
			"Run `straza keygen`, have an admin register the public key it prints, then run `straza enroll --headless` again."}
	case lane == laneKey && username == "":
		return localapi.EnrollResult{}, &Refused{Status: http.StatusBadRequest, Sentence: "Straza needs the agent's username to enroll it. " +
			"Run `straza enroll --headless --user <username>` with the user its key is registered under."}
	case lane == laneKey:
		if flow, err = plain.ResolveNHI(ctx, offer); err != nil {
			return localapi.EnrollResult{}, err
		}
	default:
		if secret, err = s.agentSecret(c.UID); err != nil {
			return localapi.EnrollResult{}, err
		}
		if username == "" {
			username = secret.ClientID
		}
		if key == nil {
			if _, key, err = ed25519.GenerateKey(rand.Reader); err != nil {
				return localapi.EnrollResult{}, err
			}
		}
	}
	tok, err := fetchAgentToken(ctx, plain, flow, lane, key, username, secret)
	if err != nil {
		return localapi.EnrollResult{}, err
	}
	if len(keys) == 0 {
		if keys, err = plain.SnapshotKeys(ctx); err != nil {
			return localapi.EnrollResult{}, fmt.Errorf("fetch the server's snapshot keys: %w", err)
		}
	}
	bindingKey, binding, err := newBinding(s.machine)
	if err != nil {
		return localapi.EnrollResult{}, err
	}
	thumb := dpop.Thumbprint(key.Public().(ed25519.PublicKey))
	rec := enrollment{
		ServerURL: server, Kind: localapi.KindAgent, Lane: lane, Username: username, KeyThumbprint: thumb,
		Custody: custodyOf(s.selfUID, c.UID), SnapshotKeys: keys, BindingKey: bindingKey, Binding: binding, SignedInAt: s.now().UTC(),
	}
	if err := a.keyLock.lock(ctx); err != nil {
		return localapi.EnrollResult{}, err
	}
	defer a.keyLock.unlock()
	if a.holdsPerson() {
		return localapi.EnrollResult{}, &Refused{Status: http.StatusConflict, Sentence: personKeptSentence}
	}
	if err := s.replace(a, rec, key); err != nil {
		return localapi.EnrollResult{}, err
	}
	a.keepToken(tok, s.now())
	s.log.Info("enrolled an agent", "uid", c.UID, "user", username, "lane", lane)
	return localapi.EnrollResult{ServerURL: server, Kind: localapi.KindAgent, Username: username, KeyThumbprint: thumb, Custody: rec.Custody}, nil
}

// agentSecret reads the client secret of uid's agent from the file
// systemd's LoadCredential passes, or from the state directory's
// credentials directory when the service runs without it. It never comes
// from the caller's environment.
func (s *credStore) agentSecret(uid string) (agentSecret, error) {
	dir := os.Getenv("CREDENTIALS_DIRECTORY")
	if dir == "" {
		dir = filepath.Join(s.opts.StateDir, "credentials")
	}
	path := filepath.Join(dir, "agent-"+uid+".json")
	var sec agentSecret
	raw, err := os.ReadFile(path) // #nosec G304 G703 -- the service's own credentials directory, named by the uid alone
	if err == nil {
		err = json.Unmarshal(raw, &sec)
	}
	if err != nil || sec.ClientID == "" || sec.ClientSecret == "" {
		return agentSecret{}, &Refused{Status: http.StatusPreconditionFailed, Sentence: "Straza has no client secret for this agent. " +
			"The Straza background service reads it from " + path + `, which holds {"client_id": "...", "client_secret": "..."} ` +
			"for the client this agent has at your identity provider. Ask IT to install that file, or give the agent a key instead: " +
			"run `straza keygen`, have an admin register the public key, and run `straza enroll --headless` again."}
	}
	return sec, nil
}

// agentCheckin starts a session for h on the id-token lane with a DPoP
// proof from the agent's key, so the session is bound to it. A cached ID
// token the server refuses is fetched again once.
func (s *credStore) agentCheckin(ctx context.Context, a *account, rec enrollment, h localapi.Harness) (agentguard.CheckinResponse, error) {
	_, api, err := a.ready()
	if err != nil {
		return agentguard.CheckinResponse{}, err
	}
	tok, cached, err := s.agentToken(ctx, a, rec, false)
	if err != nil {
		return agentguard.CheckinResponse{}, err
	}
	resp, err := api.Checkin(ctx, tok, "", h.Name, h.Version, measureAttestation(h.Name))
	var refuse *agentguard.StatusError
	if err != nil && cached && errors.As(err, &refuse) && refuse.Status == http.StatusUnauthorized {
		if tok, _, err = s.agentToken(ctx, a, rec, true); err != nil {
			return agentguard.CheckinResponse{}, err
		}
		resp, err = api.Checkin(ctx, tok, "", h.Name, h.Version, measureAttestation(h.Name))
	}
	if err == nil && resp.SessionToken == "" {
		return resp, errNoSessionToken
	}
	return resp, err
}

// agentToken returns the agent's ID token, from the cache unless fresh is
// set or it has run out, and reports whether it came from the cache.
func (s *credStore) agentToken(ctx context.Context, a *account, rec enrollment, fresh bool) (string, bool, error) {
	a.mu.Lock()
	t, key := a.agentTok, a.key
	a.mu.Unlock()
	if !fresh && t.value != "" && s.now().Before(t.until) {
		return t.value, true, nil
	}
	plain := agentguard.NewClient(rec.ServerURL)
	var flow oidcflow.Flow
	var secret agentSecret
	var err error
	if rec.Lane == laneKey {
		var offered bool
		if flow, offered, err = plain.DiscoverNHI(ctx); err == nil && !offered {
			err = errors.New("the server no longer offers the key lane for agents. Run `straza enroll --headless` again to pick the current lane")
		}
	} else if flow, err = plain.DiscoverLogin(ctx); err == nil {
		secret, err = s.agentSecret(a.uid)
	}
	if err != nil {
		return "", false, err
	}
	tok, err := fetchAgentToken(ctx, plain, flow, rec.Lane, key, rec.Username, secret)
	if err != nil {
		return "", false, err
	}
	a.keepToken(tok, s.now())
	return tok, false, nil
}

// keepToken caches tok until 30 seconds before it expires. A token whose
// expiry cannot be read is not cached.
func (a *account) keepToken(tok string, now time.Time) {
	until := jwtExpiry(tok).Add(-30 * time.Second)
	a.mu.Lock()
	defer a.mu.Unlock()
	if until.After(now) {
		a.agentTok = cachedToken{value: tok, until: until}
		return
	}
	a.agentTok = cachedToken{}
}

// jwtExpiry reads the exp claim of a JWT without verifying it, the zero
// time when there is none.
func jwtExpiry(tok string) time.Time {
	parts := strings.Split(tok, ".")
	if len(parts) != 3 {
		return time.Time{}
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[1])
	var claims struct {
		Exp int64 `json:"exp"`
	}
	if err != nil || json.Unmarshal(raw, &claims) != nil || claims.Exp <= 0 {
		return time.Time{}
	}
	return time.Unix(claims.Exp, 0)
}

// fetchAgentToken runs the client_credentials grant at flow's token
// endpoint: with an assertion signed by key on the key lane, or with the
// client secret on the secret lane.
func fetchAgentToken(ctx context.Context, c *agentguard.Client, flow oidcflow.Flow, lane string, key ed25519.PrivateKey, username string, secret agentSecret) (string, error) {
	form := url.Values{"grant_type": {"client_credentials"}}
	if lane == laneKey {
		assertion, err := clientassertion.Mint(key, username, flow.Issuer, 0)
		if err != nil {
			return "", err
		}
		form.Set("client_id", username)
		form.Set("client_assertion_type", "urn:ietf:params:oauth:client-assertion-type:jwt-bearer")
		form.Set("client_assertion", assertion)
	} else {
		form.Set("client_id", secret.ClientID)
		form.Set("client_secret", secret.ClientSecret)
		form.Set("scope", "openid")
	}
	var out struct {
		IDToken     string `json:"id_token"`
		AccessToken string `json:"access_token"`
		Error       string `json:"error"`
		ErrorDesc   string `json:"error_description"`
	}
	if err := c.PostTokenForm(ctx, flow.TokenURL, form, &out); err != nil {
		return "", fmt.Errorf("the agent's token request failed: %w", err)
	}
	if out.Error != "" {
		msg := fmt.Sprintf("the identity provider refused the agent's sign-in (%s): %s. "+
			"Check the registered key or client, and that the user is an active agent", out.Error, out.ErrorDesc)
		if out.Error == "invalid_client" || out.Error == "unauthorized_client" {
			// The issuer judged this client, so a renewal counts it as the
			// server's refusal, not as a failure to reach it.
			return "", &agentguard.StatusError{Status: http.StatusBadRequest, Msg: msg}
		}
		return "", errors.New(msg)
	}
	if out.IDToken != "" {
		return out.IDToken, nil
	}
	if out.AccessToken != "" {
		return out.AccessToken, nil
	}
	return "", errors.New("the identity provider answered the agent's sign-in with no token. Ask IT to allow the openid scope for the agent's client")
}
