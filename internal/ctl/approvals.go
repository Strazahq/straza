package ctl

import (
	"context"
	"net/http"
	"net/url"
)

// ApprovalInfo is the admin API view of one approval record (openapi.yaml
// Approval schema / server recordPayload).
type ApprovalInfo struct {
	ID            string   `json:"id"`
	State         string   `json:"state"`
	CreatedAt     string   `json:"createdAt"`
	ExpiresAt     string   `json:"expiresAt"`
	DecidedAt     *string  `json:"decidedAt"`
	User          string   `json:"user"`
	Username      string   `json:"username"`
	Session       string   `json:"session"`
	Rule          string   `json:"rule"`
	Set           string   `json:"set"`
	Lane          string   `json:"lane"`
	Summary       string   `json:"summary"`
	Justification string   `json:"justification"`
	ApproverRoles []string `json:"approverRoles"`
	// ApproverUsers: the user-scoped decide pool (policyset revision 13
	// approve.deciders, 0.72.0): usernames resolved at request time.
	ApproverUsers []string `json:"approverUsers,omitempty"`
	SelfApproval  bool     `json:"selfApproval"`
	DecidedBy     string   `json:"decidedBy"`
	DecidedByName string   `json:"decidedByName"`
	Channel       string   `json:"channel"`
	// Args preview. Shown only in the CLI detail/--wide view; the five fields
	// travel together.
	ArgsPreview    string `json:"argsPreview"`
	ArgsTruncated  bool   `json:"argsTruncated"`
	ArgsBytes      int    `json:"argsBytes"`
	ArgvHashPrefix string `json:"argvHashPrefix"`
	BindingScope   string `json:"bindingScope"`
}

// Approvals lists approval records. state is "pending" or "all" (empty ⇒
// server default, pending).
func (c *Client) Approvals(ctx context.Context, state string) ([]ApprovalInfo, error) {
	var out struct {
		Approvals []ApprovalInfo `json:"approvals"`
	}
	path := "/v1/admin/approvals"
	if state != "" {
		path += "?state=" + url.QueryEscape(state)
	}
	return out.Approvals, c.Do(ctx, http.MethodGet, path, nil, &out)
}

// ApproveApproval approves one pending record by id. The server validates the
// caller's approver roles (any active session may attempt it). reason is the
// decider's optional own words (0.63.0, ≤500 bytes); "" sends no body, the
// pre-0.63.0 wire shape.
func (c *Client) ApproveApproval(ctx context.Context, id, reason string) (ApprovalInfo, error) {
	return c.decideApproval(ctx, id, "approve", reason)
}

// DenyApproval denies one pending record by id; reason as on ApproveApproval.
func (c *Client) DenyApproval(ctx context.Context, id, reason string) (ApprovalInfo, error) {
	return c.decideApproval(ctx, id, "deny", reason)
}

func (c *Client) decideApproval(ctx context.Context, id, verdict, reason string) (ApprovalInfo, error) {
	var out struct {
		Approval ApprovalInfo `json:"approval"`
	}
	var body any
	if reason != "" {
		body = map[string]string{"reason": reason}
	}
	return out.Approval, c.Do(ctx, http.MethodPost, "/v1/admin/approvals/"+url.PathEscape(id)+"/"+verdict, body, &out)
}

// ApproverDeviceInfo is the admin API view of one enrolled mobile approver
// device (GET /v1/admin/approvers, 0.41.0): owner, posture, liveness, and the
// push-registration count that says whether the phone actually rings.
type ApproverDeviceInfo struct {
	ID               string `json:"id"` // apd_-prefixed; what the revoke takes
	UserID           string `json:"user_id"`
	Username         string `json:"username"`
	Name             string `json:"name"`
	Platform         string `json:"platform"`
	KeySecurityLevel string `json:"key_security_level"`
	Attestation      string `json:"attestation"`
	EnrolledAt       string `json:"enrolled_at"`
	LastSeen         string `json:"last_seen"`
	PushRoutes       int    `json:"push_routes"`
}

// ApproverDevices lists enrolled mobile approver devices; user (username or
// id, "" = all) filters to one owner's phones.
func (c *Client) ApproverDevices(ctx context.Context, user string) ([]ApproverDeviceInfo, error) {
	path := "/v1/admin/approvers"
	if user != "" {
		path += "?user=" + url.QueryEscape(user)
	}
	var out []ApproverDeviceInfo
	return out, c.Do(ctx, http.MethodGet, path, nil, &out)
}

// RevokeApproverDevice deletes an enrolled approver device by its apd_ id
// (DELETE /v1/admin/approvers/{id}): its use=approver token fails on the very
// next call (row-backed), and its push registrations stop routing.
func (c *Client) RevokeApproverDevice(ctx context.Context, id string) error {
	return c.Do(ctx, http.MethodDelete, "/v1/admin/approvers/"+url.PathEscape(id), nil, nil)
}
