package ctl

import (
	"context"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

// ConversationTurn is one captured prompt/reply (conversation capture).
type ConversationTurn struct {
	At          time.Time `json:"at"`
	Kind        string    `json:"kind"`
	Mode        string    `json:"mode"`
	Content     string    `json:"content"`
	Truncated   bool      `json:"truncated"`
	ContentHash string    `json:"content_hash"`
	SessionID   string    `json:"session_id,omitempty"`
	UserID      string    `json:"user_id,omitempty"`
	Username    string    `json:"username,omitempty"`
}

// SessionTranscript is one session's captured conversation, in order.
type SessionTranscript struct {
	SessionID string             `json:"session_id"`
	Username  string             `json:"username"`
	Turns     []ConversationTurn `json:"turns"`
}

// SessionTranscript fetches a session's transcript.
func (c *Client) SessionTranscript(ctx context.Context, id string) (SessionTranscript, error) {
	var out SessionTranscript
	return out, c.Do(ctx, http.MethodGet, "/v1/admin/sessions/"+url.PathEscape(id)+"/transcript", nil, &out)
}

// SearchTranscripts runs the leak hunt: substring and/or full-content hash.
func (c *Client) SearchTranscripts(ctx context.Context, q, hash, user string, limit int) ([]ConversationTurn, error) {
	params := url.Values{}
	if q != "" {
		params.Set("q", q)
	}
	if hash != "" {
		params.Set("hash", hash)
	}
	if user != "" {
		params.Set("user", user)
	}
	if limit > 0 {
		params.Set("limit", strconv.Itoa(limit))
	}
	var out []ConversationTurn
	return out, c.Do(ctx, http.MethodGet, "/v1/admin/transcripts/search?"+params.Encode(), nil, &out)
}

// AttestationHash is the admin API expected-measurement representation
// (the registry at /v1/admin/attestation-hashes).
type AttestationHash struct {
	ID        string    `json:"id,omitempty"`
	Artifact  string    `json:"artifact"`
	Harness   string    `json:"harness,omitempty"`
	Platform  string    `json:"platform,omitempty"`
	Hash      string    `json:"hash"`
	Note      string    `json:"note,omitempty"`
	CreatedAt time.Time `json:"created_at,omitempty"`
}

// AttestationHashes lists the expected-hash registry.
func (c *Client) AttestationHashes(ctx context.Context) ([]AttestationHash, error) {
	var out []AttestationHash
	return out, c.Do(ctx, http.MethodGet, "/v1/admin/attestation-hashes", nil, &out)
}

// AddAttestationHash registers an expected measurement.
func (c *Client) AddAttestationHash(ctx context.Context, h AttestationHash) (AttestationHash, error) {
	var out AttestationHash
	return out, c.Do(ctx, http.MethodPost, "/v1/admin/attestation-hashes", h, &out)
}

// RemoveAttestationHash removes a measurement from the registry.
func (c *Client) RemoveAttestationHash(ctx context.Context, id string) error {
	return c.Do(ctx, http.MethodDelete, "/v1/admin/attestation-hashes/"+url.PathEscape(id), nil, nil)
}
