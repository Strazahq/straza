package server

import (
	"crypto/ecdsa"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"net/http"
	"strconv"
	"testing"
	"time"
)

// signApproverReason signs the 5-line string (0.63.0): the legacy 4 lines plus
// hex(sha256(reason)), binding the decider's words to the device key.
func signApproverReason(t *testing.T, priv *ecdsa.PrivateKey, requestID, verdict, challenge string, ts int64, reason string) string {
	t.Helper()
	rh := sha256.Sum256([]byte(reason))
	msg := requestID + "\n" + verdict + "\n" + challenge + "\n" + strconv.FormatInt(ts, 10) + "\n" + hex.EncodeToString(rh[:])
	d := sha256.Sum256([]byte(msg))
	sig, err := ecdsa.SignASN1(rand.Reader, priv, d[:])
	if err != nil {
		t.Fatal(err)
	}
	return base64.StdEncoding.EncodeToString(sig)
}

// TestApproverRowsCarryDecidedAttribution pins the decided-attribution wire
// (0.63.0): every
// row names the origin actor (agent_session; the requester is the SUBJECT),
// and a settled row carries decided_via {surface, device_id} + the decider's
// reason. The deciding surface here is a browser-platform enrollment, the
// approvals page's own shape.
func TestApproverRowsCarryDecidedAttribution(t *testing.T) {
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
	priv, spki := genApproverKey(t)
	var enr struct {
		DeviceToken string `json:"device_token"`
		DeviceID    string `json:"approver_device_id"`
	}
	if code := adminReq(t, "POST", base+"/v1/approver/enroll", "", map[string]any{
		"enroll_token": mint.EnrollToken,
		"device": map[string]any{"name": "kim-firefox", "platform": "browser", "key_alg": "ecdsa-p256",
			"public_key": spki, "key_security_level": "software",
			"attestation": map[string]any{"kind": "none"}},
	}, &enr); code != http.StatusCreated {
		t.Fatalf("enroll = %d", code)
	}

	rec := seedApproval(t, app, "u-other", []string{"dev"})
	var rows []approverRow
	if code := adminReq(t, "GET", base+"/v1/approver/pending?scope=decidable", enr.DeviceToken, nil, &rows); code != http.StatusOK {
		t.Fatalf("pending decidable = %d", code)
	}
	var challenge string
	for _, row := range rows {
		if row.ID != rec.ID {
			continue
		}
		challenge = row.Challenge
		if row.Origin.Actor != "agent_session" {
			t.Errorf("pending origin.actor = %q, want agent_session", row.Origin.Actor)
		}
		if row.DecidedVia != nil || row.DecidedReason != "" {
			t.Errorf("pending row carries decided attribution: %+v", row)
		}
	}
	if challenge == "" {
		t.Fatalf("no decidable row for %s", rec.ID)
	}

	reason := "not during the change freeze"
	ts := time.Now().Unix()
	var dec struct {
		State string `json:"state"`
	}
	if code := adminReq(t, "POST", base+"/v1/approver/decide", enr.DeviceToken, map[string]any{
		"request_id": rec.ID, "verdict": "deny", "challenge": challenge,
		"signature": signApproverReason(t, priv, rec.ID, "deny", challenge, ts, reason),
		"ts":        ts, "reason": reason,
	}, &dec); code != http.StatusOK {
		t.Fatalf("decide with reason = %d", code)
	}

	var hist historyEnvelope
	if code := adminReq(t, "GET", base+"/v1/approver/history", enr.DeviceToken, nil, &hist); code != http.StatusOK {
		t.Fatalf("history = %d", code)
	}
	var found bool
	for _, row := range hist.Items {
		if row.ID != rec.ID {
			continue
		}
		found = true
		if row.DecidedVia == nil {
			t.Fatalf("settled row missing decided_via: %+v", row)
		}
		if row.DecidedVia.Surface != "browser" {
			t.Errorf("surface = %q, want browser", row.DecidedVia.Surface)
		}
		if row.DecidedVia.DeviceID != enr.DeviceID {
			t.Errorf("device_id = %q, want %q", row.DecidedVia.DeviceID, enr.DeviceID)
		}
		if row.DecidedReason != reason {
			t.Errorf("decided_reason = %q, want %q", row.DecidedReason, reason)
		}
		if row.Origin.Actor != "agent_session" {
			t.Errorf("settled origin.actor = %q", row.Origin.Actor)
		}
	}
	if !found {
		t.Fatalf("record %s not in history", rec.ID)
	}

	// A reason that fails validation is a 400, not a silent trim: the signed
	// lane stores exactly the signed words or nothing.
	rec2 := seedApproval(t, app, "u-other2", []string{"dev"})
	if code := adminReq(t, "GET", base+"/v1/approver/pending?scope=decidable", enr.DeviceToken, nil, &rows); code != http.StatusOK {
		t.Fatalf("pending decidable 2 = %d", code)
	}
	challenge = ""
	for _, row := range rows {
		if row.ID == rec2.ID {
			challenge = row.Challenge
		}
	}
	if challenge == "" {
		t.Fatalf("no decidable row for %s", rec2.ID)
	}
	bad := "cleared\x00with legal"
	ts = time.Now().Unix()
	if code := adminReq(t, "POST", base+"/v1/approver/decide", enr.DeviceToken, map[string]any{
		"request_id": rec2.ID, "verdict": "deny", "challenge": challenge,
		"signature": signApproverReason(t, priv, rec2.ID, "deny", challenge, ts, bad),
		"ts":        ts, "reason": bad,
	}, nil); code != http.StatusBadRequest {
		t.Fatalf("control-char reason = %d, want 400", code)
	}
}
