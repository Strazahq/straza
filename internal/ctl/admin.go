package ctl

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"time"

	"github.com/strazahq/straza/internal/audit"
)

// VerifyAudit walks the full chain (paging as needed) and reports the first
// break, or ok with the record count.
func (c *Client) VerifyAudit(ctx context.Context) (ok bool, count int, brokenSeq int64, err error) {
	prev := audit.Genesis
	var after int64
	for {
		// unfiltered on purpose: verification must walk the unbroken chain
		page, err := c.Audit(ctx, after, 1000, "")
		if err != nil {
			return false, count, 0, err
		}
		if len(page) == 0 {
			return true, count, 0, nil
		}
		recs := make([]audit.Record, len(page))
		for i, r := range page {
			recs[i] = audit.Record{Seq: r.Seq, CE: r.CE, PrevHash: r.PrevHash, Hash: r.Hash}
		}
		if good, broken := audit.Verify(recs, prev); !good {
			return false, count, broken, nil
		}
		count += len(recs)
		prev = recs[len(recs)-1].Hash
		after = recs[len(recs)-1].Seq
	}
}

// Payload mirrors of the admin API (pkg/api/openapi.yaml is the contract).

// User is the admin API user representation.
type User struct {
	ID       string `json:"id"`
	Username string `json:"username"`
	Email    string `json:"email"`
	Display  string `json:"display"`
	Status   string `json:"status"`
	Origin   string `json:"origin"`
}

// Role is the admin API role representation.
type Role struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Kind        string `json:"kind"`
	// Server is the name of the MCP server that owns the role. It is empty
	// on a role that belongs to no server.
	Server string `json:"server,omitempty"`
}

// Assignment is the admin API assignment representation.
type Assignment struct {
	ID          string `json:"id"`
	SubjectKind string `json:"subject_kind"`
	SubjectID   string `json:"subject_id"`
	RoleID      string `json:"role_id"`
	Origin      string `json:"origin"`
}

// Pack is the admin API knowledge-pack representation.
type Pack struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Version string `json:"version"`
	Content string `json:"content"`
}

// Session is the admin API session representation.
type Session struct {
	ID            string    `json:"id"`
	UserID        string    `json:"user_id"`
	Username      string    `json:"username"`
	Harness       string    `json:"harness"`
	ClientVersion string    `json:"client_version"`
	Attestation   string    `json:"attestation"`
	Status        string    `json:"status"`
	StartedAt     time.Time `json:"started_at"`
	LastSeen      time.Time `json:"last_seen"`
	// WiringStatus classifies the session's managed-wiring hash against the
	// published render + registry (0.55.0): current | allowed | mismatch |
	// unmeasured; empty on older servers and admin harnesses.
	WiringStatus string `json:"wiring_status"`
	WiringHash   string `json:"wiring_hash"`
}

// Sessions lists sessions, optionally filtered by status.
func (c *Client) Sessions(ctx context.Context, status string) ([]Session, error) {
	path := "/v1/admin/sessions"
	if status != "" {
		path += "?status=" + url.QueryEscape(status)
	}
	var out []Session
	return out, c.Do(ctx, http.MethodGet, path, nil, &out)
}

// RevokeSession revokes a session by id, the kill switch.
func (c *Client) RevokeSession(ctx context.Context, id string) error {
	return c.Do(ctx, http.MethodPost, "/v1/admin/sessions/"+url.PathEscape(id)+"/revoke", nil, nil)
}

// SigningKeyRotation is the admin API answer to a rotation request: the
// staged key and the two timings an operator plans around.
type SigningKeyRotation struct {
	KID                           string `json:"kid"`
	Status                        string `json:"status"`
	ActiveInSeconds               int    `json:"active_in_seconds"`
	PreviousKeyVerifiesForSeconds int    `json:"previous_key_verifies_for_seconds"`
}

// RotateSigningKey stages a new session signing key, phase one of the
// two-phase rotation; the server promotes it on its own schedule.
func (c *Client) RotateSigningKey(ctx context.Context) (SigningKeyRotation, error) {
	var out SigningKeyRotation
	return out, c.Do(ctx, http.MethodPost, "/v1/admin/signing-keys/rotate", nil, &out)
}

// SigningKey is one row of the signing key list: public facts only, the
// server returns no key material on any route.
type SigningKey struct {
	KID       string `json:"kid"`
	Purpose   string `json:"purpose"`
	Status    string `json:"status"`
	CreatedAt string `json:"created_at"`
	RotatedAt string `json:"rotated_at"`
}

// SigningKeys lists every signing key with its purpose and status.
func (c *Client) SigningKeys(ctx context.Context) ([]SigningKey, error) {
	var out []SigningKey
	return out, c.Do(ctx, http.MethodGet, "/v1/admin/signing-keys", nil, &out)
}

// ClientAssertionRotation is the answer to staging a client assertion key.
// PreviousKID is empty when the staged key is the first of its purpose.
type ClientAssertionRotation struct {
	SigningKeyRotation
	PreviousKID string `json:"previous_kid"`
	JWKSURI     string `json:"jwks_uri"`
}

// RotateClientAssertionKey stages a new client assertion key, which the
// server publishes at once and promotes on its own schedule.
func (c *Client) RotateClientAssertionKey(ctx context.Context) (ClientAssertionRotation, error) {
	var out ClientAssertionRotation
	return out, c.Do(ctx, http.MethodPost, "/v1/admin/signing-keys/client-assertion/rotate", nil, &out)
}

// SigningKeyRetirement is the answer to retiring a client assertion key by
// hand. Was is the status the key held before the call.
type SigningKeyRetirement struct {
	KID                     string `json:"kid"`
	Status                  string `json:"status"`
	Was                     string `json:"was"`
	LeavesDocumentInSeconds int    `json:"leaves_document_in_seconds"`
	NextKeyActiveInSeconds  int    `json:"next_key_active_in_seconds"`
}

// RetireClientAssertionKey retires one client assertion key at once, the
// lever for a key that may have been copied.
func (c *Client) RetireClientAssertionKey(ctx context.Context, kid string) (SigningKeyRetirement, error) {
	var out SigningKeyRetirement
	return out, c.Do(ctx, http.MethodPost, "/v1/admin/signing-keys/client-assertion/"+url.PathEscape(kid)+"/retire", nil, &out)
}

// PolicySet is the admin API PolicySet representation. Since openapi
// 0.78.0 list items are summary-only (no YAML); YAML still arrives on the
// apply response and the single-set GET.
type PolicySet struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Priority int    `json:"priority"`
	Status   string `json:"status"`
	YAML     string `json:"yaml"`
	// Drift is the list row's claim that a live set's stored text is not
	// the text it runs. nil makes no claim, as on PolicyDetail.
	Drift *bool `json:"drift"`
}

// Policies lists stored PolicySets. Requires a 0.78.0+ server: the list is
// an envelope {items, total, limit, offset, roles}; unpaged (no limit
// param) returns every set. A pre-0.78.0 strazad answers a bare array,
// which is refused with the skew named so the operator rolls the server
// instead of chasing a JSON decode error.
func (c *Client) Policies(ctx context.Context) ([]PolicySet, error) {
	var out struct {
		Items []PolicySet `json:"items"`
	}
	if err := c.Do(ctx, http.MethodGet, "/v1/admin/policies", nil, &out); err != nil {
		var te *json.UnmarshalTypeError
		if errors.As(err, &te) && te.Value == "array" {
			return nil, fmt.Errorf("this server predates the 0.78.0 policy-list envelope; upgrade strazad or use a matching strazactl: %w", err)
		}
		return nil, err
	}
	return out.Items, nil
}

// PoliciesJSON returns the policy list envelope exactly as the server
// answered it.
func (c *Client) PoliciesJSON(ctx context.Context) ([]byte, error) {
	return c.DoBytes(ctx, http.MethodGet, "/v1/admin/policies")
}

// ApplyPolicy uploads raw PolicySet YAML (server validates).
func (c *Client) ApplyPolicy(ctx context.Context, yaml []byte) (PolicySet, error) {
	var out PolicySet
	return out, c.DoRawBody(ctx, http.MethodPut, "/v1/admin/policies", "application/yaml", yaml, &out)
}

// DeletePolicy deletes a stored PolicySet by name. The server refuses an
// active set with the reason (409), which surfaces verbatim.
func (c *Client) DeletePolicy(ctx context.Context, name string) error {
	return c.Do(ctx, http.MethodDelete, "/v1/admin/policies/"+url.PathEscape(name), nil, nil)
}

// PolicyActivation is the admin API answer to turning a PolicySet on or
// off: the snapshot running after the call, and whether the call changed
// the live state. A nil Changed makes no claim, as from a server that
// predates the field.
type PolicyActivation struct {
	Snapshot string `json:"snapshot"`
	Changed  *bool  `json:"changed"`
}

// ActivatePolicy activates (or drafts) a PolicySet by name and triggers a
// snapshot recompile+distribute. A set already in the asked state answers
// the running snapshot with Changed false.
func (c *Client) ActivatePolicy(ctx context.Context, name, status string) (PolicyActivation, error) {
	var out PolicyActivation
	err := c.Do(ctx, http.MethodPost, "/v1/admin/policies/"+url.PathEscape(name)+"/activate",
		map[string]string{"status": status}, &out)
	return out, err
}

// AuditRecord mirrors audit.Record for the CLI.
type AuditRecord struct {
	Seq      int64  `json:"seq"`
	CE       string `json:"ce"`
	PrevHash string `json:"prevHash"`
	Hash     string `json:"hash"`
	Username string `json:"username,omitempty"`
}

// Audit fetches hash-chained audit records after a sequence number. user
// (id or username) filters to one principal's trail; browsing only, since
// chain verification must fetch unfiltered.
func (c *Client) Audit(ctx context.Context, after int64, limit int, user string) ([]AuditRecord, error) {
	var out []AuditRecord
	path := fmt.Sprintf("/v1/admin/audit?after=%d&limit=%d", after, limit)
	if user != "" {
		path += "&user=" + url.QueryEscape(user)
	}
	return out, c.Do(ctx, http.MethodGet, path, nil, &out)
}

// AuditRecent fetches the newest records newest-first (order=desc), the
// `audit tail` read. user narrows to one principal, ceType to one CloudEvent
// type and q to the records the server's text search matches. Chain
// verification must keep paging Audit ascending.
func (c *Client) AuditRecent(ctx context.Context, limit int, user, ceType, q string) ([]AuditRecord, error) {
	var out []AuditRecord
	path := fmt.Sprintf("/v1/admin/audit?order=desc&limit=%d", limit)
	if user != "" {
		path += "&user=" + url.QueryEscape(user)
	}
	if ceType != "" {
		path += "&type=" + url.QueryEscape(ceType)
	}
	if q != "" {
		path += "&q=" + url.QueryEscape(q)
	}
	return out, c.Do(ctx, http.MethodGet, path, nil, &out)
}
