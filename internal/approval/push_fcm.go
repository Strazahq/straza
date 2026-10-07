package approval

import (
	"bytes"
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/strazahq/straza/internal/config"
)

// fcmSender delivers to Firebase Cloud Messaging via the HTTP v1 API using ONLY
// the standard library: it self-signs an RS256 JWT from the service-account
// private key, exchanges it at the account's token_uri for a short-lived OAuth2
// bearer (cached until ~5 min before expiry), and POSTs the message. No Google
// SDK, no new Go dependency (invariant: single static binary, stdlib-first).
type fcmSender struct {
	projectID   string
	clientEmail string
	privateKey  *rsa.PrivateKey
	tokenURI    string

	httpc *http.Client
	now   func() time.Time

	// Overridable for httptest; default to the real Google/FCM endpoints.
	sendEndpoint string // .../v1/projects/{projectId}/messages:send

	mu     sync.Mutex
	token  string
	expiry time.Time // cache horizon (real expiry minus a safety margin)
}

// fcmServiceAccount is the subset of a Google service-account JSON we consume.
type fcmServiceAccount struct {
	ClientEmail string `json:"client_email"`
	PrivateKey  string `json:"private_key"` // PEM PKCS#8 RSA
	TokenURI    string `json:"token_uri"`
	ProjectID   string `json:"project_id"`
}

// bearerCacheMargin is how long before real expiry the cached token is retired.
const bearerCacheMargin = 5 * time.Minute

// newFCMSender parses the service-account file and prepares the sender. A bad
// file, key, or missing field fails boot (config.validate already guaranteed
// serviceAccountFile + projectId are set when FCM is enabled).
func newFCMSender(cfg config.FCMPush, httpc *http.Client) (*fcmSender, error) {
	raw, err := os.ReadFile(cfg.ServiceAccountFile) // #nosec G304 -- operator-configured credential path
	if err != nil {
		return nil, fmt.Errorf("approval.push.fcm.serviceAccountFile: %w", err)
	}
	var sa fcmServiceAccount
	if err := json.Unmarshal(raw, &sa); err != nil {
		return nil, fmt.Errorf("approval.push.fcm: parse service account: %w", err)
	}
	if sa.ClientEmail == "" || sa.PrivateKey == "" {
		return nil, errors.New("approval.push.fcm: service account missing client_email or private_key")
	}
	key, err := parseRSAPrivateKey(sa.PrivateKey)
	if err != nil {
		return nil, fmt.Errorf("approval.push.fcm: %w", err)
	}
	tokenURI := sa.TokenURI
	if tokenURI == "" {
		tokenURI = "https://oauth2.googleapis.com/token" // #nosec G101 -- public Google OAuth2 token endpoint URL, not a credential
	}
	return &fcmSender{
		projectID:    cfg.ProjectID,
		clientEmail:  sa.ClientEmail,
		privateKey:   key,
		tokenURI:     tokenURI,
		httpc:        httpc,
		now:          time.Now,
		sendEndpoint: "https://fcm.googleapis.com/v1/projects/" + cfg.ProjectID + "/messages:send",
	}, nil
}

// send POSTs one message and returns the HTTP status (so the caller can prune a
// dead registration on 404/410). FCM data values must be strings, so the
// envelope is rendered as {"v":"1","ref":...,"kind":...}, the same three keys,
// no command text (privacy rule).
func (f *fcmSender) send(ctx context.Context, registration, kind, ref string) (int, error) {
	bearer, err := f.bearer(ctx)
	if err != nil {
		return 0, err
	}
	body, err := json.Marshal(map[string]any{
		"message": map[string]any{
			"token": registration,
			"data":  map[string]string{"v": "1", "ref": ref, "kind": kind},
		},
	})
	if err != nil {
		return 0, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, f.sendEndpoint, bytes.NewReader(body))
	if err != nil {
		return 0, err
	}
	req.Header.Set("Authorization", "Bearer "+bearer)
	req.Header.Set("Content-Type", "application/json")
	resp, err := doRequest(f.httpc, req)
	if err != nil {
		return 0, err
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<16))
	return resp.StatusCode, nil
}

// bearer returns a cached OAuth2 access token, refreshing when unset or within
// the safety margin of expiry. Serialized by mu so concurrent sends mint once.
func (f *fcmSender) bearer(ctx context.Context) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.token != "" && f.now().Before(f.expiry) {
		return f.token, nil
	}
	assertion, err := f.signJWT(f.now())
	if err != nil {
		return "", err
	}
	tok, ttl, err := f.exchange(ctx, assertion)
	if err != nil {
		return "", err
	}
	f.token = tok
	horizon := ttl - bearerCacheMargin
	if horizon <= 0 {
		horizon = ttl / 2
	}
	f.expiry = f.now().Add(horizon)
	return tok, nil
}

// signJWT self-signs the RS256 service-account assertion (RFC 7523) requesting
// the FCM scope, audience = token_uri, 1h lifetime.
func (f *fcmSender) signJWT(now time.Time) (string, error) {
	header := map[string]string{"alg": "RS256", "typ": "JWT"}
	claims := map[string]any{
		"iss":   f.clientEmail,
		"scope": fcmScope,
		"aud":   f.tokenURI,
		"iat":   now.Unix(),
		"exp":   now.Add(time.Hour).Unix(),
	}
	hb, err := json.Marshal(header)
	if err != nil {
		return "", err
	}
	cb, err := json.Marshal(claims)
	if err != nil {
		return "", err
	}
	signingInput := b64url(hb) + "." + b64url(cb)
	digest := sha256.Sum256([]byte(signingInput))
	sig, err := rsa.SignPKCS1v15(rand.Reader, f.privateKey, crypto.SHA256, digest[:])
	if err != nil {
		return "", err
	}
	return signingInput + "." + b64url(sig), nil
}

// exchange trades the JWT assertion for a bearer token at token_uri and returns
// the token plus its lifetime.
func (f *fcmSender) exchange(ctx context.Context, assertion string) (string, time.Duration, error) {
	form := url.Values{}
	form.Set("grant_type", "urn:ietf:params:oauth:grant-type:jwt-bearer")
	form.Set("assertion", assertion)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, f.tokenURI, strings.NewReader(form.Encode()))
	if err != nil {
		return "", 0, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := doRequest(f.httpc, req)
	if err != nil {
		return "", 0, err
	}
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
	if resp.StatusCode != http.StatusOK {
		// Parse the OAuth error fields instead of echoing the raw body: this
		// error reaches logs and the channels admin API, and a verbatim
		// third-party body is exactly the shape that ends up carrying
		// something it shouldn't (the secrets-in-logs rule). The
		// fields are the answer; anything unparseable is status-only.
		var oe struct {
			Error string `json:"error"`
			Desc  string `json:"error_description"`
		}
		if json.Unmarshal(raw, &oe) == nil && oe.Error != "" {
			msg := oe.Error
			if oe.Desc != "" {
				msg += ": " + oe.Desc
			}
			return "", 0, fmt.Errorf("fcm token exchange: status %d: %s", resp.StatusCode, msg)
		}
		return "", 0, fmt.Errorf("fcm token exchange: status %d", resp.StatusCode)
	}
	var out struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int    `json:"expires_in"`
		TokenType   string `json:"token_type"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return "", 0, err
	}
	if out.AccessToken == "" {
		return "", 0, errors.New("fcm token exchange: empty access_token")
	}
	ttl := time.Duration(out.ExpiresIn) * time.Second
	if ttl <= 0 {
		ttl = time.Hour
	}
	return out.AccessToken, ttl, nil
}

// parseRSAPrivateKey decodes a PEM RSA private key, accepting PKCS#8 (Google's
// default) or PKCS#1.
func parseRSAPrivateKey(pemStr string) (*rsa.PrivateKey, error) {
	block, _ := pem.Decode([]byte(pemStr))
	if block == nil {
		return nil, errors.New("service account private_key is not PEM")
	}
	if k, err := x509.ParsePKCS1PrivateKey(block.Bytes); err == nil {
		return k, nil
	}
	keyAny, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("parse private_key: %w", err)
	}
	rsaKey, ok := keyAny.(*rsa.PrivateKey)
	if !ok {
		return nil, errors.New("service account private_key is not RSA")
	}
	return rsaKey, nil
}

func b64url(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }
