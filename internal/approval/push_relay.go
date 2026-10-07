package approval

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/strazahq/straza/internal/config"
)

// Hosted push relay client. The API contract is the push service README, v1.
// The relay holds the platform publisher
// credentials (the Apple .p8, the Firebase service account) so a
// self-hosted deployment rings native phones without ever possessing them;
// strazad sends {platform, route, envelope, expires_at} and nothing else,
// the same content-free envelope every other lane carries. Error-origin
// discipline: a non-2xx is the relay itself talking, a 200 carries the
// downstream verdict, so a platform refusal is never mistaken for a relay
// auth problem.

// defaultRelayURL is the official Straza-operated relay; an empty
// approval.push.relay.url means this (zero-means-default, resolved here).
const defaultRelayURL = "https://push.straza.ai"

// relayResult is the downstream verdict of one completed forward.
type relayResult struct {
	delivered        bool
	downstreamStatus int
	reason           string
	prune            bool
}

// relaySender forwards envelopes to the relay under the anonymous
// deployment token, minted at first boot (persisted 0600 at tokenFile, the
// vapidKeyFile custody idiom) and re-minted once when the relay refuses it,
// so a server-side revocation self-heals instead of going silent until a
// restart.
type relaySender struct {
	baseURL   string
	tokenFile string
	httpc     *http.Client
	log       *slog.Logger

	mu    sync.Mutex
	token string
}

// newRelaySender loads or mints the deployment token. A mint failure is not
// fatal (the lane is best-effort with the poll floor beneath): it warns and
// the first send retries the mint. An existing but unreadable token file
// fails boot like every other credential file.
func newRelaySender(cfg config.RelayPush, httpc *http.Client, log *slog.Logger) (*relaySender, error) {
	base := strings.TrimSuffix(cfg.URL, "/")
	if base == "" {
		base = defaultRelayURL
	}
	r := &relaySender{baseURL: base, tokenFile: cfg.TokenFile, httpc: httpc, log: log}
	raw, err := os.ReadFile(cfg.TokenFile)
	switch {
	case err == nil:
		r.token = strings.TrimSpace(string(raw))
		return r, nil
	case os.IsNotExist(err):
		ctx, cancel := context.WithTimeout(context.Background(), pushTimeout)
		defer cancel()
		if merr := r.mint(ctx, false); merr != nil {
			log.Warn("approval push: relay token mint failed at boot; the first send retries it", "url", base, "err", merr)
		}
		return r, nil
	default:
		return nil, fmt.Errorf("approval.push.relay.tokenFile %s: %w", cfg.TokenFile, err)
	}
}

// mint registers anonymously (POST /v1/register) and persists the token.
// The first mint (replace false) creates the file only when it is absent.
// When another replica on the same data directory created it first, this
// sender adopts the token in the file, so the replicas send with the token
// the file holds. A re-mint after the relay refused the held token (replace
// true) writes over the file.
func (r *relaySender) mint(ctx context.Context, replace bool) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, r.baseURL+"/v1/register", http.NoBody)
	if err != nil {
		return err
	}
	resp, err := doRequest(r.httpc, req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
	if resp.StatusCode != http.StatusCreated {
		return fmt.Errorf("relay register: HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	var out struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(body, &out); err != nil || out.Token == "" {
		return fmt.Errorf("relay register: malformed response")
	}
	token, adopted := out.Token, false
	if replace {
		if err := os.WriteFile(r.tokenFile, []byte(token), 0o600); err != nil {
			return fmt.Errorf("persist relay token: %w", err)
		}
	} else {
		held, err := createRelayTokenFile(r.tokenFile, token)
		if err != nil {
			return err
		}
		token, adopted = held, held != out.Token
	}
	r.mu.Lock()
	r.token = token
	r.mu.Unlock()
	if adopted {
		r.log.Info("approval push: another replica registered first; this replica uses the relay deployment token that replica wrote",
			"url", r.baseURL, "tokenFile", r.tokenFile)
		return nil
	}
	r.log.Info("approval push: relay deployment token minted", "url", r.baseURL, "tokenFile", r.tokenFile)
	return nil
}

// createRelayTokenFile creates path holding a first-minted token, 0600, and
// answers the token the file holds afterwards: token itself, or the token
// another replica wrote first. The token is written to a temporary file in
// the same directory and hard-linked to path, and the link fails when path
// exists, so path never appears without its whole token and a replica that
// loses reads the winner's token complete.
func createRelayTokenFile(path, token string) (string, error) {
	tmp, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".tmp-*")
	if err != nil {
		return "", fmt.Errorf("persist relay token: %w", err)
	}
	defer func() { _ = os.Remove(tmp.Name()) }()
	_, werr := tmp.WriteString(token)
	if cerr := tmp.Close(); werr == nil {
		werr = cerr
	}
	if werr != nil {
		return "", fmt.Errorf("persist relay token: %w", werr)
	}
	err = os.Link(tmp.Name(), path)
	if errors.Is(err, os.ErrExist) {
		held := readRelayTokenFile(path)
		if held == "" {
			return "", fmt.Errorf("another replica created the relay token file %s first, but it cannot be read or is empty", path)
		}
		return held, nil
	}
	if err != nil {
		return "", fmt.Errorf("persist relay token: %w", err)
	}
	return token, nil
}

// readRelayTokenFile answers the token in path, or "" when the file cannot
// be read or is empty.
func readRelayTokenFile(path string) string {
	raw, err := os.ReadFile(path) // #nosec G304 -- operator-configured credential path
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(raw))
}

func (r *relaySender) currentToken() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.token
}

// send forwards one envelope and returns the downstream verdict. A 401/403
// retries once with a fresh token: the token file's, when another replica
// already wrote one other than the refused token, else a re-mint. Every other
// relay-origin status is an error.
func (r *relaySender) send(ctx context.Context, platform, route, kind, ref string, exp time.Time) (relayResult, error) {
	sent := r.currentToken()
	res, status, err := r.post(ctx, sent, platform, route, kind, ref, exp)
	if err == nil && (status == http.StatusUnauthorized || status == http.StatusForbidden) {
		if held := readRelayTokenFile(r.tokenFile); held != "" && held != sent {
			r.mu.Lock()
			r.token = held
			r.mu.Unlock()
			r.log.Info("approval push: the relay refused this replica's deployment token; it uses the newer token in the token file",
				"status", status, "tokenFile", r.tokenFile)
		} else {
			r.log.Warn("approval push: relay refused the deployment token; re-minting", "status", status)
			if merr := r.mint(ctx, true); merr != nil {
				return relayResult{}, fmt.Errorf("relay refused the token (HTTP %d) and the re-mint failed: %w", status, merr)
			}
		}
		res, status, err = r.post(ctx, r.currentToken(), platform, route, kind, ref, exp)
	}
	if err != nil {
		return relayResult{}, err
	}
	if status != http.StatusOK {
		return relayResult{}, fmt.Errorf("relay: HTTP %d", status)
	}
	return res, nil
}

// post is one POST /v1/push. The body is exactly the v1 contract; the relay
// rejects unknown fields, which keeps the envelope content-free by
// construction on both ends.
func (r *relaySender) post(ctx context.Context, token, platform, route, kind, ref string, exp time.Time) (relayResult, int, error) {
	var ea int64
	if !exp.IsZero() {
		ea = exp.Unix()
	}
	body, err := json.Marshal(map[string]any{
		"platform":   platform,
		"route":      route,
		"envelope":   pushPayload{V: 1, Ref: ref, Kind: kind},
		"expires_at": ea,
	})
	if err != nil {
		return relayResult{}, 0, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, r.baseURL+"/v1/push", bytes.NewReader(body))
	if err != nil {
		return relayResult{}, 0, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := doRequest(r.httpc, req)
	if err != nil {
		return relayResult{}, 0, err
	}
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
	if resp.StatusCode != http.StatusOK {
		return relayResult{}, resp.StatusCode, nil
	}
	var out struct {
		Delivered        bool   `json:"delivered"`
		DownstreamStatus int    `json:"downstream_status"`
		Reason           string `json:"reason"`
		Prune            bool   `json:"prune"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return relayResult{}, resp.StatusCode, fmt.Errorf("relay: malformed verdict: %w", err)
	}
	return relayResult{delivered: out.Delivered, downstreamStatus: out.DownstreamStatus,
		reason: out.Reason, prune: out.Prune}, resp.StatusCode, nil
}
