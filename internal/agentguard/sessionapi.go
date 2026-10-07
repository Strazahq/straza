package agentguard

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"

	"github.com/strazahq/straza/internal/sameorigin"
)

// SessionAPI calls strazad as the enrolled caller. Every request carries the
// caller's session token: a live session on disk is adopted and never
// re-minted, a missing one is minted through the enrolled identity, and a 401
// spends one refresh and one retry, exactly as the MCP proxy does.
type SessionAPI struct {
	client *Client
}

// NewSessionAPI returns the enrolled caller's API client, or an error that
// names `straza enroll` when this machine is not enrolled.
func NewSessionAPI(store *Store) (*SessionAPI, error) {
	cfg, err := store.LoadConfig()
	if err != nil {
		return nil, fmt.Errorf("not enrolled (run `straza enroll`): %w", err)
	}
	return &SessionAPI{client: &Client{
		Base: cfg.ServerURL,
		HTTP: &http.Client{
			Timeout:       30 * time.Second,
			CheckRedirect: sameorigin.Check,
			Transport: &sessionAuthTransport{
				base: http.DefaultTransport, store: store,
				client: NewClient(cfg.ServerURL), harness: sessionHarness(""),
			},
		},
	}}, nil
}

// Do sends one JSON request and decodes the answer into out. A refusal comes
// back as the server's own sentence, the way strazactl prints it.
func (a *SessionAPI) Do(ctx context.Context, method, path string, body, out any) error {
	var rd io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rd = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, a.client.Base+path, rd)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	err = a.client.do(req, out)
	var refused *StatusError
	if errors.As(err, &refused) {
		return errors.New(refused.Msg)
	}
	return err
}

// sessionHarness names the harness a session is minted under when none is
// live: the override, then the STRAZA_HARNESS environment variable, then
// claude-code. The proxy and the caller's API client share it, so a session
// either of them mints carries the same label and the same attestation.
func sessionHarness(override string) string {
	if override != "" {
		return override
	}
	if h := os.Getenv("STRAZA_HARNESS"); h != "" {
		return h
	}
	return "claude-code"
}
