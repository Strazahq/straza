package server

import (
	"context"
	"fmt"
	"net/http"
	"testing"

	"github.com/strazahq/straza/internal/approval"
)

// TestApproverEnrollRefusesNULCharacter pins the refusal of an approver
// enrollment whose device name, key algorithm, key security level or
// attestation holds a NUL character: 400 with a sentence that names the
// field, before the enroll code is read, so the code still enrolls, no
// device is registered and no record is written, as the malformed body's
// 400 does. A spent code gets the same 400, and a request without a code
// keeps the 401 of a missing code. The test store is SQLite, which would
// keep the character, so the refusal does not depend on the store. A NUL in
// the public key or the platform keeps its earlier answer: the key does not
// decode, and the platform is refused by the code's channel once the code
// is spent.
func TestApproverEnrollRefusesNULCharacter(t *testing.T) {
	t.Parallel()
	app, base := testApp(t)
	kim := seedIdentity(t, app)
	ctx := context.Background()
	_, spki := genApproverKey(t)
	body := func(token string) (map[string]any, map[string]any) {
		device := map[string]any{
			"name": "kim-pixel", "platform": "android", "key_alg": "ecdsa-p256", "public_key": spki,
			"key_security_level": "strongbox", "attestation": map[string]any{"kind": "none", "blob": ""},
		}
		return map[string]any{"enroll_token": token, "device": device}, device
	}
	attestation := func(d map[string]any) map[string]any { return d["attestation"].(map[string]any) }
	nul := func(field, step string) string { return fmt.Sprintf(approverNULMsg, field, step) }

	// The code a refused request carries: the fresh code, none, or the code
	// after a clean enrollment spent it.
	const (
		fresh = iota
		none
		spent
	)
	cases := []struct {
		name      string
		spoil     func(device map[string]any)
		code      int
		wantCode  int
		wantError string
		codeLives bool // whether the enroll code still enrolls after the refusal
	}{
		{"device name", func(d map[string]any) { d["name"] = "kim\x00pixel" }, fresh,
			http.StatusBadRequest, nul("device.name", approverNULStepName), true},
		{"key algorithm", func(d map[string]any) { d["key_alg"] = "ecdsa-p256\x00" }, fresh,
			http.StatusBadRequest, nul("device.key_alg", approverNULStepApp), true},
		{"key security level", func(d map[string]any) { d["key_security_level"] = "\x00strongbox" }, fresh,
			http.StatusBadRequest, nul("device.key_security_level", approverNULStepApp), true},
		{"attestation kind", func(d map[string]any) { attestation(d)["kind"] = "no\x00ne" }, fresh,
			http.StatusBadRequest, nul("device.attestation.kind", approverNULStepApp), true},
		{"attestation blob", func(d map[string]any) { attestation(d)["blob"] = "blob\x00" }, fresh,
			http.StatusBadRequest, nul("device.attestation.blob", approverNULStepApp), true},
		{"device name with a spent code", func(d map[string]any) { d["name"] = "kim\x00pixel" }, spent,
			http.StatusBadRequest, nul("device.name", approverNULStepName), false},
		{"device name without a code", func(d map[string]any) { d["name"] = "kim\x00pixel" }, none,
			http.StatusUnauthorized, approval.ErrEnrollTokenInvalid.Error(), true},
		{"public key", func(d map[string]any) { d["public_key"] = spki + "\x00" }, fresh,
			http.StatusBadRequest, approval.ErrBadKey.Error(), true},
		{"platform", func(d map[string]any) { d["platform"] = "andr\x00oid" }, fresh,
			http.StatusUnauthorized, approval.ErrEnrollTokenInvalid.Error(), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			grant, err := app.approval.MintEnrollToken(ctx, kim.ID, approval.EnrollChannelMobile)
			if err != nil {
				t.Fatal(err)
			}
			if tc.code == spent {
				clean, _ := body(grant.Token)
				if code, out := postJSON(t, base+"/v1/approver/enroll", clean); code != http.StatusCreated {
					t.Fatalf("approver enroll that spends the code = %d %v, want 201", code, out)
				}
			}
			before, err := app.store.Approvers().ListDevices(ctx, kim.ID)
			if err != nil {
				t.Fatal(err)
			}
			head, err := app.store.Outbox().Head(ctx)
			if err != nil {
				t.Fatal(err)
			}
			token := grant.Token
			if tc.code == none {
				token = ""
			}
			refused, device := body(token)
			tc.spoil(device)
			if code, out := postJSON(t, base+"/v1/approver/enroll", refused); code != tc.wantCode || out["error"] != tc.wantError {
				t.Fatalf("approver enroll = %d %v, want %d with %q", code, out, tc.wantCode, tc.wantError)
			}
			if after, err := app.store.Approvers().ListDevices(ctx, kim.ID); err != nil || len(after) != len(before) {
				t.Fatalf("approver devices %d before and %d after the refusal (%v), want no new device", len(before), len(after), err)
			}
			if now, _ := app.store.Outbox().Head(ctx); now != head {
				t.Fatalf("the refused enrollment wrote an outbox row: head %q, was %q", now, head)
			}
			clean, _ := body(grant.Token)
			want := http.StatusCreated
			if !tc.codeLives {
				want = http.StatusUnauthorized
			}
			if code, out := postJSON(t, base+"/v1/approver/enroll", clean); code != want {
				t.Fatalf("approver enroll with the same code and no NUL = %d %v, want %d", code, out, want)
			}
		})
	}
}
