package server

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/strazahq/straza/internal/approval"
	"github.com/strazahq/straza/internal/store"
)

// TestApproverDecideRefusesAPersonRetypedAsANonPerson pins that a person
// whose user is retyped as an AI agent or a service account after the phone
// fetched a challenge, and who signs a decision inside that challenge's life,
// reads 403 not_authorized with a sentence that says why and what to do next,
// and that the record stays pending. A person who keeps the type decides:
// the positive control.
func TestApproverDecideRefusesAPersonRetypedAsANonPerson(t *testing.T) {
	t.Parallel()
	const next = ", and only a person can decide a request, so this decision was not recorded. " +
		"If kim is a person, ask an administrator to set its user_type back to human with PATCH /v1/admin/users/{id}, " +
		"or in the identity manager when one manages the user."
	cases := []struct {
		name       string
		retype     string
		wantStatus int
		wantCode   string
		wantError  string
		wantState  approval.State
	}{
		{"a person keeps deciding", "", http.StatusOK, "", "", approval.StateApproved},
		{"a person retyped as an AI agent", store.UserTypeAgent, http.StatusForbidden, codeNotAuthorized,
			"the user kim is an agent" + next, approval.StatePending},
		{"a person retyped as a service account", store.UserTypeService, http.StatusForbidden, codeNotAuthorized,
			"the user kim is a service account" + next, approval.StatePending},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			app, base := testApp(t)
			kim := seedIdentity(t, app)
			priv, _, tok := enrollApprover(t, app, kim.ID)
			rec := seedApproval(t, app, "u-other", []string{"dev"})
			ch := decidableChallenge(t, base, tok, rec.ID)
			if tc.retype != "" {
				kim.UserType = tc.retype
				if _, err := app.store.Users().Update(context.Background(), kim); err != nil {
					t.Fatal(err)
				}
			}
			ts := time.Now().Unix()
			body := decideBodyFor(rec.ID, "approve", ch, signApprover(t, priv, rec.ID, "approve", ch, ts), ts)

			var eb decideErrBody
			if code := adminReq(t, "POST", base+"/v1/approver/decide", tok, body, &eb); code != tc.wantStatus {
				t.Fatalf("decide = %d %+v, want %d", code, eb, tc.wantStatus)
			}
			if eb.Code != tc.wantCode || eb.Error != tc.wantError {
				t.Errorf("decide answered code %q error %q, want %q %q", eb.Code, eb.Error, tc.wantCode, tc.wantError)
			}
			got, err := app.approval.Get(context.Background(), rec.ID)
			if err != nil {
				t.Fatal(err)
			}
			if got.State != tc.wantState {
				t.Errorf("the record is %s, want %s", got.State, tc.wantState)
			}
		})
	}
}
