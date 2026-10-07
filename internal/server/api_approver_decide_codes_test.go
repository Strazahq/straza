package server

import (
	"context"
	"crypto/ecdsa"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/strazahq/straza/internal/approval"
	"github.com/strazahq/straza/internal/store"
)

// TestApproverDecideError is the single-source-of-truth table for the decide
// path's err → (status, code) mapping. An empty code means the handler writes
// the plain {"error":…} envelope (no code field), byte-identical to the
// pre-code behavior.
func TestApproverDecideError(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name       string
		err        error
		wantStatus int
		wantCode   string
	}{
		{"bad verdict → 400 plain", approval.ErrBadVerdict, http.StatusBadRequest, ""},
		// The deliberately coarse verification family: one code for stale ts,
		// bad/unparseable key, bad signature, and missing/expired/spent challenge.
		{"stale timestamp", approval.ErrStaleTimestamp, http.StatusUnauthorized, codeChallengeRejected},
		{"bad key", approval.ErrBadKey, http.StatusUnauthorized, codeChallengeRejected},
		{"bad signature", approval.ErrBadSignature, http.StatusUnauthorized, codeChallengeRejected},
		{"challenge invalid (missing/expired/replayed)", approval.ErrChallengeInvalid, http.StatusUnauthorized, codeChallengeRejected},
		{"wrapped challenge invalid still classifies", fmt.Errorf("decide: %w", approval.ErrChallengeInvalid), http.StatusUnauthorized, codeChallengeRejected},
		// The one decide-path destroy signal, same const as everywhere else.
		{"device revoked", approval.ErrDeviceRevoked, http.StatusUnauthorized, codeDeviceRevoked},
		// Authorization outcomes: the key stays valid, the app must NOT destroy it.
		{"self approval → 403", approval.ErrSelfApproval, http.StatusForbidden, codeNotAuthorized},
		{"not an approver → 403", approval.ErrNotApprover, http.StatusForbidden, codeNotAuthorized},
		{"not the requester (confirm) → 403", approval.ErrNotRequester, http.StatusForbidden, codeNotAuthorized},
		{"not found → 404 plain", approval.ErrNotFound, http.StatusNotFound, ""},
		{"expired → 410 plain", approval.ErrExpired, http.StatusGone, ""},
		// Reachable only when the handler's final-state Get hit a store error;
		// pre-code parity says plain 409 (resolved elsewhere), never a 500.
		{"conflict Get-failure fallthrough → 409 plain", approval.ErrConflict, http.StatusConflict, ""},
		// ErrUserInactive is NOT reachable on the decide path (requireApprover
		// already fails an inactive user with user_inactive; DecideSigned/Decide
		// never return it; it is a refresh-lane code). So the mapper does not
		// special-case it: it falls to the default 500, and this pins that.
		{"user_inactive is decide-unreachable → default 500", approval.ErrUserInactive, http.StatusInternalServerError, ""},
		// A decider retyped as an AI agent or a service account after its phone
		// enrolled is an authorization outcome, and the key stays valid.
		{"nhi decider → 403", approval.ErrNHIDecider, http.StatusForbidden, codeNotAuthorized},
		{"unexpected store error → default 500", errors.New("boom"), http.StatusInternalServerError, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			status, code := approverDecideError(tc.err)
			if status != tc.wantStatus || code != tc.wantCode {
				t.Errorf("approverDecideError(%v) = (%d, %q), want (%d, %q)",
					tc.err, status, code, tc.wantStatus, tc.wantCode)
			}
		})
	}
}

// enrollApprover mints an enroll token, registers a fresh P-256 device for
// userID, and returns the private key, device id, and a live use=approver token.
func enrollApprover(t *testing.T, app *App, userID string) (priv *ecdsa.PrivateKey, deviceID, token string) {
	t.Helper()
	ctx := context.Background()
	priv, spki := genApproverKey(t)
	grant, err := app.approval.MintEnrollToken(ctx, userID, "")
	if err != nil {
		t.Fatalf("mint enroll token: %v", err)
	}
	res, err := app.approval.Enroll(ctx, approval.EnrollInput{
		EnrollToken: grant.Token, Name: "kim-pixel", Platform: "android",
		KeyAlg: "ecdsa-p256", PublicKeyB64: spki, KeySecurityLevel: "strongbox",
		AttestationKind: "none",
	})
	if err != nil {
		t.Fatalf("enroll: %v", err)
	}
	tok, err := app.tokens.MintApproverToken(res.UserID, res.DeviceID, time.Hour)
	if err != nil {
		t.Fatalf("mint approver token: %v", err)
	}
	return priv, res.DeviceID, tok
}

func decideBodyFor(recID, verdict, challenge, sig string, ts int64) map[string]any {
	return map[string]any{
		"request_id": recID, "verdict": verdict, "challenge": challenge,
		"signature": sig, "ts": ts,
	}
}

// decidableChallenge fetches the fresh single-use challenge the decidable feed
// minted for recID (the normal in-app path to a valid challenge).
func decidableChallenge(t *testing.T, base, tok, recID string) string {
	t.Helper()
	var rows []approverRow
	if code := adminReq(t, "GET", base+"/v1/approver/pending?scope=decidable", tok, nil, &rows); code != http.StatusOK {
		t.Fatalf("pending decidable = %d", code)
	}
	for _, row := range rows {
		if row.ID == recID {
			if row.Challenge == "" {
				t.Fatalf("decidable row %s carries no challenge", recID)
			}
			return row.Challenge
		}
	}
	t.Fatalf("no decidable row for %s in %+v", recID, rows)
	return ""
}

// insertChallenge binds a synthetic single-use challenge to (deviceID, recID),
// used to drive the decide path to authorization/lookup outcomes the decidable
// feed would never hand out (a role the bound user lacks; an unknown id).
func insertChallenge(t *testing.T, app *App, deviceID, recID, challenge string) {
	t.Helper()
	now := time.Now().UTC()
	if err := app.store.Approvers().InsertChallenge(context.Background(), store.ApproverChallenge{
		Challenge: challenge, DeviceID: deviceID, ApprovalID: recID,
		CreatedAt: now, ExpiresAt: now.Add(5 * time.Minute),
	}); err != nil {
		t.Fatalf("insert challenge: %v", err)
	}
}

type decideErrBody struct {
	Error string `json:"error"`
	Code  string `json:"code"`
}

// TestApproverDecideWireCodes drives real /v1/approver/decide requests and
// asserts the coded 401/403 JSON body the mobile app branches on.
func TestApproverDecideWireCodes(t *testing.T) {
	t.Parallel()
	t.Run("stale timestamp → 401 challenge_rejected", func(t *testing.T) {
		app, base := testApp(t)
		kim := seedIdentity(t, app)
		priv, _, tok := enrollApprover(t, app, kim.ID)
		rec := seedApproval(t, app, "u-other", []string{"dev"})
		ch := decidableChallenge(t, base, tok, rec.ID)
		ts := time.Now().Add(-10 * time.Minute).Unix() // outside the ±5m window
		body := decideBodyFor(rec.ID, "approve", ch, signApprover(t, priv, rec.ID, "approve", ch, ts), ts)

		var eb decideErrBody
		if code := adminReq(t, "POST", base+"/v1/approver/decide", tok, body, &eb); code != http.StatusUnauthorized {
			t.Fatalf("decide = %d, want 401", code)
		}
		if eb.Code != codeChallengeRejected {
			t.Errorf("code = %q, want %q", eb.Code, codeChallengeRejected)
		}
	})

	t.Run("bad signature → 401 challenge_rejected", func(t *testing.T) {
		app, base := testApp(t)
		kim := seedIdentity(t, app)
		_, _, tok := enrollApprover(t, app, kim.ID)
		rec := seedApproval(t, app, "u-other", []string{"dev"})
		ch := decidableChallenge(t, base, tok, rec.ID)
		ts := time.Now().Unix()
		// Sign with a DIFFERENT key than the one enrolled ⇒ verification fails.
		wrong, _ := genApproverKey(t)
		body := decideBodyFor(rec.ID, "approve", ch, signApprover(t, wrong, rec.ID, "approve", ch, ts), ts)

		var eb decideErrBody
		if code := adminReq(t, "POST", base+"/v1/approver/decide", tok, body, &eb); code != http.StatusUnauthorized {
			t.Fatalf("decide = %d, want 401", code)
		}
		if eb.Code != codeChallengeRejected {
			t.Errorf("code = %q, want %q", eb.Code, codeChallengeRejected)
		}
	})

	t.Run("spent/replayed challenge → 401 challenge_rejected", func(t *testing.T) {
		app, base := testApp(t)
		kim := seedIdentity(t, app)
		priv, _, tok := enrollApprover(t, app, kim.ID)
		rec := seedApproval(t, app, "u-other", []string{"dev"})
		ch := decidableChallenge(t, base, tok, rec.ID)
		ts := time.Now().Unix()
		body := decideBodyFor(rec.ID, "approve", ch, signApprover(t, priv, rec.ID, "approve", ch, ts), ts)

		// First decide wins; the challenge is now consumed.
		if code := adminReq(t, "POST", base+"/v1/approver/decide", tok, body, nil); code != http.StatusOK {
			t.Fatalf("first decide = %d, want 200", code)
		}
		var eb decideErrBody
		if code := adminReq(t, "POST", base+"/v1/approver/decide", tok, body, &eb); code != http.StatusUnauthorized {
			t.Fatalf("replay decide = %d, want 401", code)
		}
		if eb.Code != codeChallengeRejected {
			t.Errorf("replay code = %q, want %q", eb.Code, codeChallengeRejected)
		}
	})

	t.Run("revoked device → 401 device_revoked", func(t *testing.T) {
		app, base := testApp(t)
		kim := seedIdentity(t, app)
		priv, deviceID, tok := enrollApprover(t, app, kim.ID)
		rec := seedApproval(t, app, "u-other", []string{"dev"})
		ch := decidableChallenge(t, base, tok, rec.ID)
		ts := time.Now().Unix()
		body := decideBodyFor(rec.ID, "approve", ch, signApprover(t, priv, rec.ID, "approve", ch, ts), ts)
		// Delete the device row mid-flow (row-backed revocation).
		if err := app.store.Approvers().DeleteDevice(context.Background(), deviceID); err != nil {
			t.Fatalf("delete device: %v", err)
		}
		var eb decideErrBody
		if code := adminReq(t, "POST", base+"/v1/approver/decide", tok, body, &eb); code != http.StatusUnauthorized {
			t.Fatalf("decide = %d, want 401", code)
		}
		if eb.Code != codeDeviceRevoked {
			t.Errorf("code = %q, want %q", eb.Code, codeDeviceRevoked)
		}
	})

	t.Run("non-approver decide → 403 not_authorized", func(t *testing.T) {
		app, base := testApp(t)
		kim := seedIdentity(t, app) // kim holds dev/reader, NOT auditor
		priv, deviceID, tok := enrollApprover(t, app, kim.ID)
		rec := seedApproval(t, app, "u-other", []string{"auditor"})
		// The decidable feed would never offer this row to kim; bind a challenge
		// directly so the request reaches the fresh approve-role check in Decide.
		ch := "not-approver-challenge-nonce"
		insertChallenge(t, app, deviceID, rec.ID, ch)
		ts := time.Now().Unix()
		body := decideBodyFor(rec.ID, "approve", ch, signApprover(t, priv, rec.ID, "approve", ch, ts), ts)

		var eb decideErrBody
		if code := adminReq(t, "POST", base+"/v1/approver/decide", tok, body, &eb); code != http.StatusForbidden {
			t.Fatalf("decide = %d, want 403", code)
		}
		if eb.Code != codeNotAuthorized {
			t.Errorf("code = %q, want %q", eb.Code, codeNotAuthorized)
		}
	})
}

// TestApproverDecidePlainEnvelopesUnchanged pins that the non-coded outcomes
// keep the bare {"error":…} shape: no "code" key leaks onto the 400 (bad
// verdict) or 404 (unknown id) responses.
func TestApproverDecidePlainEnvelopesUnchanged(t *testing.T) {
	t.Parallel()
	assertNoCodeKey := func(t *testing.T, base, tok string, body map[string]any, wantStatus int) {
		t.Helper()
		var m map[string]json.RawMessage
		code := adminReq(t, "POST", base+"/v1/approver/decide", tok, body, &m)
		if code != wantStatus {
			t.Fatalf("decide = %d, want %d (body %v)", code, wantStatus, m)
		}
		if _, ok := m["code"]; ok {
			t.Errorf("plain envelope leaked a code key: %v", m)
		}
		if _, ok := m["error"]; !ok {
			t.Errorf("plain envelope missing the error key: %v", m)
		}
		if len(m) != 1 {
			t.Errorf("plain envelope must be exactly {error}, got %v", m)
		}
	}

	t.Run("400 bad verdict", func(t *testing.T) {
		app, base := testApp(t)
		kim := seedIdentity(t, app)
		_, _, tok := enrollApprover(t, app, kim.ID)
		rec := seedApproval(t, app, "u-other", []string{"dev"})
		// Verdict is checked first, before any challenge/signature work.
		body := decideBodyFor(rec.ID, "maybe", "x", "x", time.Now().Unix())
		assertNoCodeKey(t, base, tok, body, http.StatusBadRequest)
	})

	t.Run("404 unknown id", func(t *testing.T) {
		app, base := testApp(t)
		kim := seedIdentity(t, app)
		priv, deviceID, tok := enrollApprover(t, app, kim.ID)
		missing := "apr_does_not_exist"
		ch := "unknown-id-challenge-nonce"
		insertChallenge(t, app, deviceID, missing, ch)
		ts := time.Now().Unix()
		body := decideBodyFor(missing, "approve", ch, signApprover(t, priv, missing, "approve", ch, ts), ts)
		assertNoCodeKey(t, base, tok, body, http.StatusNotFound)
	})
}
