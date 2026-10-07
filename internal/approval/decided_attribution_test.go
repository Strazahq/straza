package approval

import (
	"context"
	"crypto/ecdsa"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/strazahq/straza/internal/policy"
)

// The decided-attribution contract: the decider's
// reason joins the device-signed string as a 5th line hex(sha256(reason)) so
// the words are bound to the same key as the verdict; a reason-less decision
// signs the legacy 4-line string bit-for-bit (deployed phone builds keep
// working); the persisted channel is the honest surface (phone|browser from
// the enrolled device's platform, console for the admin plane) instead of the
// constant "api"; and the signing device persists on every signed decision.

func signDecisionReason(t *testing.T, priv *ecdsa.PrivateKey, requestID, verdict, challenge string, ts int64, reason string) string {
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

// raise creates one pending approval and returns (record, fresh challenge).
func raiseForDecide(t *testing.T, h *harness, requesterID, approverID, deviceID, session string) (Record, string) {
	t.Helper()
	ctx := context.Background()
	spec := policy.ApproveSpec{Roles: []string{"sec-approvers"}, TimeoutSeconds: 90, RetryTTLSeconds: 60}
	rec, err := h.svc.Request(ctx, req(session, requesterID, "nova", spec))
	if err != nil {
		t.Fatalf("Request: %v", err)
	}
	rows, err := h.svc.Decidable(ctx, approverID, deviceID)
	if err != nil {
		t.Fatalf("Decidable: %v", err)
	}
	for _, r := range rows {
		if r.ID == rec.ID {
			return rec, r.Challenge
		}
	}
	t.Fatalf("record %s not decidable", rec.ID)
	return Record{}, ""
}

func TestDecideSignedReasonFifthLine(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	requester := h.seedUser(t, "nova")
	approver := h.seedUser(t, "kim", "sec-approvers")
	priv, spki := genP256(t)
	deviceID := h.enroll(t, approver.ID, spki) // android → surface "phone"

	// Deny with a reason: the 5-line string verifies, and reason + device +
	// honest surface all persist on the record.
	rec, challenge := raiseForDecide(t, h, requester.ID, approver.ID, deviceID, "s-r1")
	ts := time.Now().Unix()
	reason := "deploy window closed; re-raise tomorrow"
	sig := signDecisionReason(t, priv, rec.ID, "deny", challenge, ts, reason)
	out, err := h.svc.DecideSigned(ctx, SignedDecision{
		RequestID: rec.ID, Verdict: "deny", Challenge: challenge, SignatureB64: sig, TS: ts,
		DeviceID: deviceID, DeciderUserID: approver.ID, Reason: reason,
	})
	if err != nil {
		t.Fatalf("DecideSigned deny+reason: %v", err)
	}
	if out.State != StateDenied || out.DecidedReason != reason {
		t.Fatalf("denied record = state %s reason %q, want denied %q", out.State, out.DecidedReason, reason)
	}
	if out.DecidedDeviceID != deviceID {
		t.Errorf("DecidedDeviceID = %q, want %q", out.DecidedDeviceID, deviceID)
	}
	if out.Channel != "phone" {
		t.Errorf("Channel = %q, want phone (android device)", out.Channel)
	}

	// Approve carries a reason the same way: both verdicts accept one.
	rec2, ch2 := raiseForDecide(t, h, requester.ID, approver.ID, deviceID, "s-r2")
	ts = time.Now().Unix()
	sig = signDecisionReason(t, priv, rec2.ID, "approve", ch2, ts, "pairs with ticket OPS-142")
	out, err = h.svc.DecideSigned(ctx, SignedDecision{
		RequestID: rec2.ID, Verdict: "approve", Challenge: ch2, SignatureB64: sig, TS: ts,
		DeviceID: deviceID, DeciderUserID: approver.ID, Reason: "pairs with ticket OPS-142",
	})
	if err != nil {
		t.Fatalf("DecideSigned approve+reason: %v", err)
	}
	if out.State != StateApproved || out.DecidedReason != "pairs with ticket OPS-142" {
		t.Fatalf("approved record = %+v", out)
	}
}

func TestDecideSignedReasonSignatureBinding(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	requester := h.seedUser(t, "nova")
	approver := h.seedUser(t, "kim", "sec-approvers")
	priv, spki := genP256(t)
	deviceID := h.enroll(t, approver.ID, spki)
	rec, challenge := raiseForDecide(t, h, requester.ID, approver.ID, deviceID, "s-b1")
	ts := time.Now().Unix()

	// A reason the key never signed (4-line signature + reason field) must fail:
	// nobody can attach words to a verdict after the fact.
	sig4 := signDecision(t, priv, rec.ID, "deny", challenge, ts)
	if _, err := h.svc.DecideSigned(ctx, SignedDecision{
		RequestID: rec.ID, Verdict: "deny", Challenge: challenge, SignatureB64: sig4, TS: ts,
		DeviceID: deviceID, DeciderUserID: approver.ID, Reason: "unsigned words",
	}); err != ErrBadSignature {
		t.Fatalf("4-line sig + reason = %v, want ErrBadSignature", err)
	}

	// A stripped reason (5-line signature, reason field dropped in transit)
	// must fail: the words cannot be silently removed either.
	sig5 := signDecisionReason(t, priv, rec.ID, "deny", challenge, ts, "these words matter")
	if _, err := h.svc.DecideSigned(ctx, SignedDecision{
		RequestID: rec.ID, Verdict: "deny", Challenge: challenge, SignatureB64: sig5, TS: ts,
		DeviceID: deviceID, DeciderUserID: approver.ID,
	}); err != ErrBadSignature {
		t.Fatalf("5-line sig, no reason = %v, want ErrBadSignature", err)
	}

	// A swapped reason under the old signature must fail.
	if _, err := h.svc.DecideSigned(ctx, SignedDecision{
		RequestID: rec.ID, Verdict: "deny", Challenge: challenge, SignatureB64: sig5, TS: ts,
		DeviceID: deviceID, DeciderUserID: approver.ID, Reason: "different words",
	}); err != ErrBadSignature {
		t.Fatalf("swapped reason = %v, want ErrBadSignature", err)
	}

	// The record is still pending after all three refusals, and the honest
	// 5-line decision still lands (the challenge was never consumed).
	out, err := h.svc.DecideSigned(ctx, SignedDecision{
		RequestID: rec.ID, Verdict: "deny", Challenge: challenge, SignatureB64: sig5, TS: ts,
		DeviceID: deviceID, DeciderUserID: approver.ID, Reason: "these words matter",
	})
	if err != nil {
		t.Fatalf("honest decide after refusals: %v", err)
	}
	if out.DecidedReason != "these words matter" {
		t.Fatalf("DecidedReason = %q", out.DecidedReason)
	}
}

func TestDecideSignedLegacyFourLineStillVerifies(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	requester := h.seedUser(t, "nova")
	approver := h.seedUser(t, "kim", "sec-approvers")
	priv, spki := genP256(t)
	deviceID := h.enroll(t, approver.ID, spki)
	rec, challenge := raiseForDecide(t, h, requester.ID, approver.ID, deviceID, "s-l1")
	ts := time.Now().Unix()

	// A deployed phone build signs the 4-line string and sends no reason:
	// byte-identical verification, empty reason, device still attributed,
	// honest surface still recorded.
	sig := signDecision(t, priv, rec.ID, "approve", challenge, ts)
	out, err := h.svc.DecideSigned(ctx, SignedDecision{
		RequestID: rec.ID, Verdict: "approve", Challenge: challenge, SignatureB64: sig, TS: ts,
		DeviceID: deviceID, DeciderUserID: approver.ID,
	})
	if err != nil {
		t.Fatalf("legacy 4-line: %v", err)
	}
	if out.DecidedReason != "" {
		t.Errorf("DecidedReason = %q, want empty", out.DecidedReason)
	}
	if out.DecidedDeviceID != deviceID {
		t.Errorf("DecidedDeviceID = %q, want %q (device attribution is reason-independent)", out.DecidedDeviceID, deviceID)
	}
	if out.Channel != "phone" {
		t.Errorf("Channel = %q, want phone", out.Channel)
	}
}

func TestDecideReasonValidation(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	requester := h.seedUser(t, "nova")
	approver := h.seedUser(t, "kim", "sec-approvers")
	priv, spki := genP256(t)
	deviceID := h.enroll(t, approver.ID, spki)

	rec, challenge := raiseForDecide(t, h, requester.ID, approver.ID, deviceID, "s-v1")
	ts := time.Now().Unix()
	for name, bad := range map[string]string{
		"over-500-bytes": strings.Repeat("x", 501),
		"nul":            "cleared\x00with legal",
		"bell":           "ok\x07ok",
		"bad-utf8":       "abc\xff",
		// Direction controls can visually reorder the decider's quoted words
		// on every surface, and HTML escaping does not neutralize reordering.
		// Zero-width invisibles cannot reorder but make visually identical
		// reasons differ byte for byte on an audit record. Intake, the single
		// gate, rejects both.
		"rlo-override": "pay \u202egro.evil\u202c today",
		"lre-embed":    "ok \u202aok\u202c ok",
		"rli-isolate":  "hold \u2067evil\u2069 back",
		"lrm-mark":     "ok\u200eok",
		"alm-mark":     "ok\u061cok",
		"zwsp":         "in\u200bvisible gap",
		"zwnbsp-bom":   "\ufeffpasted from a doc",
	} {
		sig := signDecisionReason(t, priv, rec.ID, "deny", challenge, ts, bad)
		if _, err := h.svc.DecideSigned(ctx, SignedDecision{
			RequestID: rec.ID, Verdict: "deny", Challenge: challenge, SignatureB64: sig, TS: ts,
			DeviceID: deviceID, DeciderUserID: approver.ID, Reason: bad,
		}); err != ErrBadReason {
			t.Errorf("%s = %v, want ErrBadReason", name, err)
		}
	}

	// The rejection is surgical, not category Cf: ZWJ emoji sequences and
	// ZWNJ orthography (Persian, Indic) are words a decider legitimately
	// types, and they must keep passing.
	for name, good := range map[string]string{
		"zwj-emoji":    "approved for the family plan \U0001F468\u200d\U0001F469\u200d\U0001F467",
		"zwnj-persian": "تأیید می\u200cشود",
	} {
		grec, gch := raiseForDecide(t, h, requester.ID, approver.ID, deviceID, "s-"+name)
		gsig := signDecisionReason(t, priv, grec.ID, "deny", gch, ts, good)
		if _, err := h.svc.DecideSigned(ctx, SignedDecision{
			RequestID: grec.ID, Verdict: "deny", Challenge: gch, SignatureB64: gsig, TS: ts,
			DeviceID: deviceID, DeciderUserID: approver.ID, Reason: good,
		}); err != nil {
			t.Errorf("%s = %v, want nil (ZWJ/ZWNJ stay legal)", name, err)
		}
	}

	// Newlines and tabs are legitimate prose; exactly 500 bytes is legal.
	fine := "line one\n\tline two " + strings.Repeat("y", 500-20)
	sig := signDecisionReason(t, priv, rec.ID, "deny", challenge, ts, fine)
	if _, err := h.svc.DecideSigned(ctx, SignedDecision{
		RequestID: rec.ID, Verdict: "deny", Challenge: challenge, SignatureB64: sig, TS: ts,
		DeviceID: deviceID, DeciderUserID: approver.ID, Reason: fine,
	}); err != nil {
		t.Fatalf("multiline reason at the cap: %v", err)
	}
}

func TestDecideConsoleLaneReason(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	requester := h.seedUser(t, "nova")
	approver := h.seedUser(t, "kim", "sec-approvers")

	spec := policy.ApproveSpec{Roles: []string{"sec-approvers"}, TimeoutSeconds: 90, RetryTTLSeconds: 60}
	rec, err := h.svc.Request(ctx, req("s-c1", requester.ID, "nova", spec))
	if err != nil {
		t.Fatalf("Request: %v", err)
	}

	// The console lane has no device key: reason persists unsigned, device
	// stays empty, channel stays the admin surface.
	out, err := h.svc.Decide(ctx, rec.ID, "denied", approver.ID, "console", "not during the change freeze", "")
	if err != nil {
		t.Fatalf("Decide console: %v", err)
	}
	if out.DecidedReason != "not during the change freeze" || out.DecidedDeviceID != "" || out.Channel != "console" {
		t.Fatalf("console decide = channel %q reason %q device %q", out.Channel, out.DecidedReason, out.DecidedDeviceID)
	}

	// The same validation gates the unsigned lane.
	rec2, err := h.svc.Request(ctx, req("s-c2", requester.ID, "nova", spec))
	if err != nil {
		t.Fatalf("Request 2: %v", err)
	}
	if _, err := h.svc.Decide(ctx, rec2.ID, "denied", approver.ID, "console", "bad\x00reason", ""); err != ErrBadReason {
		t.Fatalf("console bad reason = %v, want ErrBadReason", err)
	}
}
