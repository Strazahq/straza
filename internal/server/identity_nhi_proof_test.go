package server

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/strazahq/straza/internal/dpop"
	"github.com/strazahq/straza/internal/store"
)

// proofAgent stores an agent and answers it with an ID token for it.
func proofAgent(t *testing.T, app *App, username string) (store.User, string) {
	t.Helper()
	u, err := app.store.Users().Create(context.Background(), store.User{Username: username, UserType: store.UserTypeAgent})
	if err != nil {
		t.Fatal(err)
	}
	idt, err := app.tokens.MintIDToken(u.ID, "straza", 5*time.Minute, username, "")
	if err != nil {
		t.Fatal(err)
	}
	return u, idt
}

// nhiCheckinBody is the body of an agent's id-token check-in.
func nhiCheckinBody(idToken string) map[string]any {
	return map[string]any{
		"id_token":    idToken,
		"harness":     map[string]string{"name": "claude-code", "version": "2.1.0"},
		"attestation": map[string]any{"managed": false, "hashes": map[string]string{"self": "sha256:x"}},
	}
}

// TestNHIProofBindsSession pins the proof on an agent's id-token check-in:
// the first proof without a nonce gets the nonce challenge with no record,
// the retry mints a deviceless session whose token carries the proof key's
// thumbprint as cnf, and the session's refresh needs a proof over the token
// from that key and carries cnf forward. An agent without a DPoP header
// still gets a bearer session.
func TestNHIProofBindsSession(t *testing.T) {
	t.Parallel()
	app, base := testApp(t)
	bot, idt := proofAgent(t, app, "bot")
	h := newKeyHolder(t, base)

	code, hdr, out := h.send("/v1/checkin", "", nhiCheckinBody(idt))
	if code != http.StatusUnauthorized || out["error"] != nonceMsg || !strings.Contains(hdr.Get("WWW-Authenticate"), "use_dpop_nonce") {
		t.Fatalf("first check-in = %d %v, want the nonce challenge", code, out)
	}
	if n := len(outboxDataFor(t, app, "straza.audit.authn")); n != 0 {
		t.Errorf("authn records after the nonce challenge = %d, want none", n)
	}
	code, _, out = h.send("/v1/checkin", "", nhiCheckinBody(idt))
	if code != http.StatusOK {
		t.Fatalf("check-in with the nonce = %d %v", code, out)
	}
	token := out["session_token"].(string)
	claims, err := app.tokens.Verify(token)
	if err != nil {
		t.Fatal(err)
	}
	if claims.CNF != h.thumbprint() || claims.Subject != bot.ID || claims.Device != "" {
		t.Errorf("claims = %+v, want cnf %s on no device", claims, h.thumbprint())
	}

	harness := map[string]string{"name": "claude-code", "version": "2.1.0"}
	code, _, out = h.post("/v1/checkin", "", map[string]any{"session_token": token, "harness": harness})
	if code != http.StatusUnauthorized || out["error"] != copiedTokenMsg {
		t.Errorf("refresh without a proof = %d %v, want the copied-token 401", code, out)
	}
	code, out = h.call("/v1/checkin", token, map[string]any{"session_token": token, "harness": harness})
	if code != http.StatusOK || out["session_id"] != claims.Session {
		t.Fatalf("refresh with the proof = %d %v", code, out)
	}
	if refreshed, err := app.tokens.Verify(out["session_token"].(string)); err != nil || refreshed.CNF != h.thumbprint() {
		t.Errorf("refreshed claims = %+v %v, want cnf %s", refreshed, err, h.thumbprint())
	}

	code, out = postJSON(t, base+"/v1/checkin", nhiCheckinBody(idt))
	if code != http.StatusOK {
		t.Fatalf("check-in without a DPoP header = %d %v", code, out)
	}
	if bearer, err := app.tokens.Verify(out["session_token"].(string)); err != nil || bearer.CNF != "" {
		t.Errorf("claims without a proof = %+v %v, want no cnf", bearer, err)
	}
}

// TestNHIProofRefusals pins each refusal of the proof on an agent's id-token
// check-in: 401 with the proof sentence and invalid_dpop_proof, exactly one
// login failure via id-token naming the agent, the harness and the reason,
// and no session.
func TestNHIProofRefusals(t *testing.T) {
	t.Parallel()
	app, base := testApp(t)
	bot, idt := proofAgent(t, app, "bot")
	h := newKeyHolder(t, base)
	h.send("/v1/checkin", "", nhiCheckinBody(idt)) // the nonce challenge
	replayed := h.proof("/v1/checkin", "", 0)
	if code, _, out := h.post("/v1/checkin", replayed, nhiCheckinBody(idt)); code != http.StatusOK {
		t.Fatalf("the proof before its replay = %d %v", code, out)
	}
	const tail = ". Sign a fresh proof with this agent's key."

	cases := []struct {
		name       string
		proof      func() string
		wantError  string
		wantReason string
	}{
		{"a replayed proof", func() string { return replayed },
			"the DPoP proof was not accepted: jti was already used" + tail, "the DPoP proof was replayed"},
		{"a proof for another path", func() string { return h.proof("/v1/enroll", "", 0) },
			"the DPoP proof was not accepted: htu path does not match the request" + tail, "the DPoP proof did not verify"},
		{"a proof that signs a counter", func() string { return h.proof("/v1/checkin", "", 3) },
			"the DPoP proof was not accepted: ctr is present but the request is not a key check-in" + tail, "the DPoP proof did not verify"},
		{"a proof over an access token", func() string { return h.proof("/v1/checkin", "a-token", 0) },
			"the DPoP proof was not accepted: ath is present but the request carries no access token" + tail, "the DPoP proof did not verify"},
		{"two proofs", nil,
			"the DPoP proof was not accepted: more than one DPoP proof" + tail, "the DPoP proof did not verify"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			pred := func(d map[string]any) bool {
				return d["action"] == "login" && d["outcome"] == "failure" && d["userId"] == bot.ID
			}
			before := len(matching(t, app, "straza.audit.authn", pred))
			sessions, _ := app.store.Sessions().ListByUser(context.Background(), bot.ID)
			var code int
			var hdr http.Header
			var out map[string]any
			if tc.proof == nil {
				code, hdr, out = postTwoProofs(t, h, nhiCheckinBody(idt))
			} else {
				code, hdr, out = h.post("/v1/checkin", tc.proof(), nhiCheckinBody(idt))
			}
			if code != http.StatusUnauthorized || out["error"] != tc.wantError ||
				!strings.Contains(hdr.Get("WWW-Authenticate"), `error="invalid_dpop_proof"`) {
				t.Fatalf("check-in = %d %v %v, want 401 %q", code, hdr, out, tc.wantError)
			}
			failures := matching(t, app, "straza.audit.authn", pred)
			if len(failures) != before+1 {
				t.Fatalf("login failures of bot = %d, want exactly one more than %d", len(failures), before)
			}
			f := failures[len(failures)-1]
			if f["via"] != "id-token" || f["user"] != "bot" || f["harness"] != "claude-code/2.1.0" || f["reason"] != tc.wantReason {
				t.Errorf("login failure = %v, want via id-token naming bot with reason %q", f, tc.wantReason)
			}
			if after, _ := app.store.Sessions().ListByUser(context.Background(), bot.ID); len(after) != len(sessions) {
				t.Errorf("sessions = %d after the refusal, want %d", len(after), len(sessions))
			}
		})
	}
}

// postTwoProofs posts body to /v1/checkin with two DPoP headers.
func postTwoProofs(t *testing.T, h *keyHolder, body any) (int, http.Header, map[string]any) {
	t.Helper()
	req, _ := http.NewRequest(http.MethodPost, h.base+"/v1/checkin", strings.NewReader(string(mustJSON(body))))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Add(dpop.HeaderProof, h.proof("/v1/checkin", "", 0))
	req.Header.Add(dpop.HeaderProof, h.proof("/v1/checkin", "", 0))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	var out map[string]any
	_ = jsonNewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, resp.Header, out
}

// TestNHIProofPersonKeeps400 pins that a person's ID token beside a DPoP
// header stays a malformed check-in that carries two credentials: 400, no
// record, no session, whether or not the proof carries a nonce.
func TestNHIProofPersonKeeps400(t *testing.T) {
	t.Parallel()
	app, base := testApp(t)
	kim := seedIdentity(t, app)
	idToken := loginDeviceFlow(t, base, "kim", "hunter2!")
	h := newKeyHolder(t, base)
	for _, round := range []string{"without a nonce", "with a nonce"} {
		code, _, out := h.send("/v1/checkin", "", nhiCheckinBody(idToken))
		if code != http.StatusBadRequest || out["error"] != oneCredentialMsg {
			t.Fatalf("%s: check-in = %d %v, want 400 %q", round, code, out, oneCredentialMsg)
		}
	}
	if n := len(outboxDataFor(t, app, "straza.audit.authn")); n != 0 {
		t.Errorf("authn records = %d, want none", n)
	}
	if sessions, _ := app.store.Sessions().ListByUser(context.Background(), kim.ID); len(sessions) != 0 {
		t.Errorf("sessions = %+v, want none", sessions)
	}
}
