package server

import (
	"context"
	"net/http"
	"testing"

	"github.com/strazahq/straza/internal/approval"
	"github.com/strazahq/straza/internal/policy"
	"github.com/strazahq/straza/internal/store"
)

// TestApproverPendingCarriesSessionIdentity pins that a pending row names
// the SESSION the call would run in
// (session_id) and the harness that session checked in as, resolved from the
// session store, so the approver card can render an execution-context line.
// Both fields are additive and omitted when the record's session is unknown
// to the store (the fields simply absent, never empty strings on the wire).
// Raw-map decoding on purpose: the test proves the JSON keys exist, not that
// a Go struct round-trips its own tags.
func TestApproverPendingCarriesSessionIdentity(t *testing.T) {
	t.Parallel()
	app, base := testApp(t)
	kim := seedIdentity(t, app) // kim holds role dev
	grantAdmin(t, app, kim.ID)
	adminTok, _ := checkinToken(t, app, base)

	var mint struct {
		EnrollToken string `json:"enroll_token"`
	}
	if code := adminReq(t, "POST", base+"/v1/admin/approvers/enroll-token", adminTok,
		map[string]any{"username": "kim"}, &mint); code != http.StatusOK {
		t.Fatalf("mint enroll-token = %d", code)
	}
	_, spki := genApproverKey(t)
	var enr struct {
		DeviceToken string `json:"device_token"`
	}
	if code := adminReq(t, "POST", base+"/v1/approver/enroll", "", map[string]any{
		"enroll_token": mint.EnrollToken,
		"device": map[string]any{"name": "kim-pixel", "platform": "android", "key_alg": "ecdsa-p256",
			"public_key": spki, "key_security_level": "strongbox",
			"attestation": map[string]any{"kind": "none"}},
	}, &enr); code != http.StatusCreated {
		t.Fatalf("enroll = %d", code)
	}

	// A real session row with a harness, and a record raised FROM that session.
	ses, err := app.store.Sessions().Create(context.Background(), store.Session{
		UserID: kim.ID, HarnessName: "claude-code", HarnessVersion: "2.1",
	})
	if err != nil {
		t.Fatalf("seed session: %v", err)
	}
	rec, err := app.approval.Request(context.Background(), approval.RequestInput{
		SessionID: ses.ID, UserID: "u-other", Username: "nova", RuleID: "r-1", SetName: "g",
		ArgvHash: "sha256:ctx-1", Lane: "hook", Summary: "shell.exec: date",
		Spec: policy.ApproveSpec{Roles: []string{"dev"}, TimeoutSeconds: 90},
	})
	if err != nil {
		t.Fatalf("seed approval: %v", err)
	}
	// And one whose session the store has never heard of: the row must omit
	// harness (nothing to resolve) while still naming the session id.
	orphan, err := app.approval.Request(context.Background(), approval.RequestInput{
		SessionID: "s-unknown", UserID: "u-other", Username: "nova", RuleID: "r-1", SetName: "g",
		ArgvHash: "sha256:ctx-2", Lane: "hook", Summary: "shell.exec: date",
		Spec: policy.ApproveSpec{Roles: []string{"dev"}, TimeoutSeconds: 90},
	})
	if err != nil {
		t.Fatalf("seed orphan approval: %v", err)
	}

	var rows []map[string]any
	if code := adminReq(t, "GET", base+"/v1/approver/pending?scope=decidable", enr.DeviceToken, nil, &rows); code != http.StatusOK {
		t.Fatalf("pending decidable = %d", code)
	}
	byID := map[string]map[string]any{}
	for _, r := range rows {
		id, _ := r["id"].(string)
		byID[id] = r
	}
	got, ok := byID[rec.ID]
	if !ok {
		t.Fatalf("no decidable row for %s in %d rows", rec.ID, len(rows))
	}
	if got["session_id"] != ses.ID {
		t.Errorf("session_id = %v, want %q", got["session_id"], ses.ID)
	}
	if got["harness"] != "claude-code" {
		t.Errorf("harness = %v, want claude-code", got["harness"])
	}
	o, ok := byID[orphan.ID]
	if !ok {
		t.Fatalf("no decidable row for the orphan %s", orphan.ID)
	}
	if o["session_id"] != "s-unknown" {
		t.Errorf("orphan session_id = %v, want s-unknown", o["session_id"])
	}
	if v, present := o["harness"]; present {
		t.Errorf("orphan harness = %v, want the key absent (nothing resolved, nothing claimed)", v)
	}
}
