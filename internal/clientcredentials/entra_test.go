package clientcredentials

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/strazahq/straza/internal/authn"
)

// TestEntraExchangeIsTheTwoGrants pins both requests of the Entra agent
// identity exchange field by field: the blueprint signs in with Straza's
// certificate assertion for the token endpoint and asks for an exchange token
// for the agent identity, and the agent identity presents that token and
// asks for the configured resource. Only the second answer is returned.
func TestEntraExchangeIsTheTwoGrants(t *testing.T) {
	r := newEntraRig(t)
	got, err := r.tokens.Token(bg, entraSam())
	if err != nil {
		t.Fatal(err)
	}
	forms := r.entra.asked()
	if len(forms) != 2 {
		t.Fatalf("the exchange made %d requests, want 2", len(forms))
	}
	t1 := forms[1].Get("client_assertion")
	if !strings.HasPrefix(t1, "eyJ0MSJ9.t1-"+testAgentID) {
		t.Fatalf("the second step presented %q, want the exchange token of the first", t1)
	}
	want := []map[string]string{{
		"grant_type":            "client_credentials",
		"client_id":             testBlueprint,
		"client_assertion_type": "urn:ietf:params:oauth:client-assertion-type:jwt-bearer",
		"client_assertion":      testCertAssertion + "1",
		"scope":                 "api://AzureADTokenExchange/.default",
		"fmi_path":              testAgentID,
	}, {
		"grant_type":            "client_credentials",
		"client_id":             testAgentID,
		"client_assertion_type": "urn:ietf:params:oauth:client-assertion-type:jwt-bearer",
		"client_assertion":      t1,
		"scope":                 testResource,
	}}
	for i, w := range want {
		if len(forms[i]) != len(w) {
			t.Errorf("step %d form = %v, want exactly the fields %v", i+1, forms[i], w)
		}
		for k, v := range w {
			if forms[i].Get(k) != v {
				t.Errorf("step %d form[%s] = %q, want %q", i+1, k, forms[i].Get(k), v)
			}
		}
	}
	if got != "entra-tok-"+testAgentID+"-2" {
		t.Fatalf("Token = %q, want the agent identity's token and never the exchange token", got)
	}
	if r.signer.certCalls != 1 || r.signer.calls != 0 || r.signer.clientID != testBlueprint || r.signer.audience != r.entra.tokenURL() {
		t.Errorf("signed %d certificate and %d plain assertions for %q at %q, want one certificate assertion for the blueprint at the token endpoint",
			r.signer.certCalls, r.signer.calls, r.signer.clientID, r.signer.audience)
	}
}

// TestEntraTokenIsKeptByTheAgentIdentity: the token is served from memory
// until its renewal, and a request that names another Entra agent identity
// for the same agent, as after a changed mapping, fetches anew and never gets
// the token of the identity before.
func TestEntraTokenIsKeptByTheAgentIdentity(t *testing.T) {
	r := newEntraRig(t)
	first, err := r.tokens.Token(bg, entraSam())
	if err != nil {
		t.Fatal(err)
	}
	if again, err := r.tokens.Token(bg, entraSam()); err != nil || again != first {
		t.Fatalf("the second call = %q, %v, want %q from memory", again, err, first)
	}
	if n := len(r.entra.asked()); n != 2 {
		t.Fatalf("two calls made %d requests, want the two of one exchange", n)
	}
	moved := entraSam()
	moved.EntraAgentID = otherAgentID
	got, err := r.tokens.Token(bg, moved)
	if err != nil {
		t.Fatal(err)
	}
	if got == first || got != "entra-tok-"+otherAgentID+"-4" {
		t.Fatalf("after the mapping changed Token = %q, want a token of %s", got, otherAgentID)
	}
	if fmi := r.entra.asked()[2].Get("fmi_path"); fmi != otherAgentID {
		t.Fatalf("the new exchange asked for %q, want %s", fmi, otherAgentID)
	}
}

// TestEntraWithoutAnAgentIdentityAsksNobody: an agent whose record holds no
// Entra agent identity is refused before anything is signed or sent, and the
// refusal pauses nothing, so the first call after the mapping is written runs.
func TestEntraWithoutAnAgentIdentityAsksNobody(t *testing.T) {
	r := newEntraRig(t)
	q := entraSam()
	q.EntraAgentID = ""
	const want = "agent sam-sre-agent could not get a crm token. The agent has no Entra agent identity on its Straza record. " +
		"Ask the identity team to set entraAgentId for sam-sre-agent, or set it with strazactl users entra-agent-id set sam-sre-agent <client id>, then call again."
	for range 2 {
		if _, err := r.tokens.Token(bg, q); err == nil || err.Error() != want {
			t.Fatalf("err = %v\nwant  %s", err, want)
		}
	}
	if n := len(r.entra.asked()); n != 0 || r.signer.certCalls != 0 {
		t.Fatalf("an unmapped agent caused %d requests and %d signatures", n, r.signer.certCalls)
	}
	if _, err := r.tokens.Token(bg, entraSam()); err != nil {
		t.Fatalf("the first call with a mapping was refused: %v", err)
	}
}

// TestKeycloakIgnoresTheEntraID: on a provider without a blueprint the
// client is the agent's username, whatever Entra agent identity the session
// holds.
func TestKeycloakIgnoresTheEntraID(t *testing.T) {
	r := newEntraRig(t)
	q := sam()
	q.EntraAgentID = testAgentID
	if _, err := r.tokens.Token(bg, q); err != nil {
		t.Fatal(err)
	}
	if got := r.idp.lastForm().Get("client_id"); got != "sam-sre-agent" {
		t.Fatalf("client_id = %q, want the username", got)
	}
	if r.signer.calls != 1 || r.signer.certCalls != 0 || len(r.entra.asked()) != 0 {
		t.Fatal("a provider without a blueprint ran the Entra exchange")
	}
}

// TestEntraRefusalSentences pins every refusal of the exchange whole. Each
// names the step that failed and the fix, which differs: a blueprint problem
// is the identity team's or the config's, an agent identity problem is that
// agent's. None repeats Entra's description.
func TestEntraRefusalSentences(t *testing.T) {
	const (
		open    = "agent sam-sre-agent could not get a crm token. "
		findLog = "An administrator finds the request in Entra's sign-in log by the trace id in the strazad log, then the agent calls again."
	)
	oauthErr := func(status int, body map[string]any) http.HandlerFunc {
		return func(w http.ResponseWriter, _ *http.Request) { answer(w, status, body) }
	}
	cases := []struct {
		name    string
		signer  error
		step    int
		fixture string
		handler http.HandlerFunc
		want    string
		asks    int
	}{
		{name: "certificate not on the blueprint", step: stepBlueprint, fixture: "entra-700027.json", asks: 1,
			want: "Entra refused the blueprint " + testBlueprint + " (AADSTS700027), because it holds no certificate of the client assertion key " + testKID + ". " +
				"An Agent ID Administrator uploads the certificate that strazactl signing-keys certificate client_assertion " + testKID + " prints to that blueprint, then the agent calls again."},
		{name: "blueprint disabled", step: stepBlueprint, fixture: "entra-7000112.json", asks: 1,
			want: "Entra says the blueprint " + testBlueprint + " is disabled (AADSTS7000112), so no agent of it gets a token. Ask the identity team why it was disabled, and call again once it is enabled."},
		{name: "blueprint unknown", step: stepBlueprint, fixture: "entra-700016.json", asks: 1,
			want: "Entra has no blueprint " + testBlueprint + " in tenant " + testTenant + " (AADSTS700016). " +
				"Check oauth.providers.entra-lab.clientCredentials.entraBlueprint in strazad's config against the agent blueprint in the Entra admin centre, then call again."},
		{name: "agent identity unknown", step: stepAgent, fixture: "entra-700016.json", asks: 2,
			want: "Entra has no agent identity " + testAgentID + " in tenant " + testTenant + " (AADSTS700016). " +
				"Check entraAgentId on sam-sre-agent's Straza record against the agent identity in the Entra admin centre, correct it with strazactl users entra-agent-id set, then call again."},
		{name: "agent identity disabled", step: stepAgent, fixture: "entra-7000112.json", asks: 2,
			want: "Entra says the agent identity " + testAgentID + " is disabled (AADSTS7000112). Ask the identity team why it was disabled, and call again once it is enabled."},
		{name: "agent identity of another blueprint", step: stepAgent, fixture: "entra-7002134.json", asks: 2,
			want: "Entra refused the agent identity " + testAgentID + " at the second step (AADSTS7002134), which Entra answers when the agent identity sits under another blueprint than " + testBlueprint + ", the one Straza signs in as. " +
				"Check entraAgentId on sam-sre-agent's Straza record against that blueprint's agent identities in the Entra admin centre, correct it with strazactl users entra-agent-id set, then call again."},
		{name: "another code at the first step", step: stepBlueprint, asks: 1,
			handler: oauthErr(http.StatusBadRequest, map[string]any{"error": "invalid_request", "error_description": "AADSTS90014: missing field", "error_codes": []int{90014}}),
			want:    "Entra refused the blueprint " + testBlueprint + " at the first step of the exchange with invalid_request (AADSTS90014). " + findLog},
		{name: "another code at the second step", step: stepAgent, asks: 2,
			handler: oauthErr(http.StatusBadRequest, map[string]any{"error": "invalid_scope", "error_description": "AADSTS70011: bad scope", "error_codes": []int{70011}}),
			want:    "Entra refused the agent identity " + testAgentID + " at the second step of the exchange with invalid_scope (AADSTS70011). " + findLog},
		{name: "a code that is not a code and no number", step: stepAgent, asks: 2,
			handler: oauthErr(http.StatusBadRequest, map[string]any{"error": "Invalid <b>", "error_codes": []string{"x"}}),
			want:    "Entra refused the agent identity " + testAgentID + " at the second step of the exchange with an error code Straza does not repeat. " + findLog},
		{name: "no OAuth error at the second step", step: stepAgent, asks: 2,
			handler: func(w http.ResponseWriter, _ *http.Request) {
				http.Error(w, "<html>bad gateway</html>", http.StatusBadGateway)
			},
			want: "entra-lab answered the token request at %URL% with HTTP 502 and no OAuth error. Check oauth.providers.entra-lab.tokenUrl in strazad's config and that the provider is healthy, then call again."},
		{name: "no access token at the first step", step: stepBlueprint, asks: 1,
			handler: oauthErr(http.StatusOK, map[string]any{"token_type": "Bearer", "expires_in": 3599}),
			want:    "entra-lab answered the token request without an access token, so the call is refused. Ask the identity team to check the client " + testBlueprint + " at the provider, then call again."},
		{name: "staged key", signer: &authn.StagedKeyError{KID: testKID},
			want: "The client assertion key " + testKID + " is staged and signs nothing yet. An Agent ID Administrator uploads its certificate, which strazactl signing-keys certificate client_assertion " + testKID +
				" prints, to the Entra blueprint " + testBlueprint + ", then an administrator promotes it with strazactl signing-keys promote client_assertion " + testKID + ", and the agent calls again."},
		{name: "expired certificate", signer: &authn.CertificateExpiredError{KID: testKID, NotAfter: time.Date(2028, 11, 6, 12, 0, 0, 0, time.UTC)},
			want: "The certificate of client assertion key " + testKID + " expired on 2028-11-06, so Entra would refuse it. An administrator runs strazactl signing-keys rotate client_assertion, uploads the new key's certificate to the Entra blueprint and promotes that key with strazactl signing-keys promote client_assertion and its id, then the agent calls again."},
		{name: "no key", signer: authn.ErrNoAssertionKey,
			want: "This Straza deployment has no client assertion key that signs: none was created yet, or the last one was retired. An administrator runs strazactl signing-keys rotate client_assertion, then strazactl signing-keys promote client_assertion with the new key's id, and the agent calls again."},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := newEntraRig(t)
			r.signer.err = tc.signer
			switch {
			case tc.fixture != "":
				r.entra.refuse(tc.step, tc.fixture)
			case tc.handler != nil:
				r.entra.answerWith(tc.step, tc.handler)
			}
			got, err := r.tokens.Token(bg, entraSam())
			want := open + strings.ReplaceAll(tc.want, "%URL%", r.entra.tokenURL())
			if err == nil || err.Error() != want {
				t.Fatalf("err = %v\nwant  %s", err, want)
			}
			if got != "" {
				t.Fatalf("a refusal handed out %q", got)
			}
			if n := len(r.entra.asked()); n != tc.asks {
				t.Errorf("the refusal followed %d requests, want %d", n, tc.asks)
			}
		})
	}
}

// TestEntraLogNamesWhatFindsTheRequest: a refusal's log line carries Entra's
// error, its numeric codes, the trace and correlation ids, the step, the
// blueprint, the agent and its Entra agent identity, so an operator finds the
// request in Entra's sign-in log. Entra's description never reaches the log,
// and an id that is not a GUID is withheld.
func TestEntraLogNamesWhatFindsTheRequest(t *testing.T) {
	r := newEntraRig(t)
	r.entra.refuse(stepBlueprint, "entra-700027.json")
	if _, err := r.tokens.Token(bg, entraSam()); err == nil {
		t.Fatal("the refused blueprint got a token")
	}
	logs := r.logs.String()
	var line map[string]any
	for _, l := range logLines(t, logs) {
		if l["level"] == "WARN" {
			line = l
		}
	}
	if line == nil {
		t.Fatalf("no warning was logged: %s", logs)
	}
	want := map[string]any{
		"provider": "entra-lab", "server": "crm", "agent": "sam-sre-agent", "step": "blueprint",
		"blueprint": testBlueprint, "entra_agent_id": testAgentID, "status": float64(401),
		"error": "invalid_client", "trace_id": testTraceID, "correlation_id": testCorrID,
	}
	for k, v := range want {
		if line[k] != v {
			t.Errorf("log %s = %v, want %v", k, line[k], v)
		}
	}
	if codes := fmt.Sprint(line["error_codes"]); codes != "[700027]" {
		t.Errorf("log error_codes = %s, want [700027]", codes)
	}
	for _, s := range []string{"not registered on application", "AADSTS700027:", "description"} {
		if strings.Contains(logs, s) {
			t.Errorf("the log carries Entra's description (%q): %s", s, logs)
		}
	}

	r = newEntraRig(t)
	r.entra.answerWith(stepAgent, func(w http.ResponseWriter, _ *http.Request) {
		answer(w, http.StatusBadRequest, map[string]any{"error": "invalid_grant", "error_codes": []int{1, 2, 3, 4, 5, 6, 7}, "trace_id": "not a guid", "correlation_id": strings.Repeat("e", 36)})
	})
	if _, err := r.tokens.Token(bg, entraSam()); err == nil {
		t.Fatal("the refused agent identity got a token")
	}
	line = logLines(t, r.logs.String())[0]
	if line["trace_id"] != "withheld" || line["correlation_id"] != "withheld" || line["step"] != "agent identity" {
		t.Errorf("log = %v, want both ids withheld at the agent identity step", line)
	}
	if codes := fmt.Sprint(line["error_codes"]); codes != "[1 2 3 4 5]" {
		t.Errorf("log error_codes = %s, want at most five", codes)
	}
}

// TestEntraNothingLeaks runs a success and hostile answers at both steps,
// then looks for the certificate assertion, the exchange token, the final
// token and Entra's chosen text in every error and every log line.
func TestEntraNothingLeaks(t *testing.T) {
	secrets := []string{"eyJ", "t1-", "entra-tok-", testCertAssertion, "plain words"}
	check := func(t *testing.T, where, s string) {
		t.Helper()
		for _, secret := range secrets {
			if strings.Contains(s, secret) {
				t.Errorf("the %s carries %q: %s", where, secret, s)
			}
		}
	}
	echo := func(field string) http.HandlerFunc {
		return func(w http.ResponseWriter, req *http.Request) {
			_ = req.ParseForm()
			sent := req.PostForm.Get("client_assertion")
			body := map[string]any{"error": "invalid_client", "error_description": "plain words", "error_codes": []int{700027}}
			switch field {
			case "description":
				body["error_description"] = "you sent " + sent
			case "code":
				body["error"] = sent
			case "trace id":
				body["trace_id"] = sent
			}
			answer(w, http.StatusUnauthorized, body)
		}
	}
	for _, step := range []int{stepBlueprint, stepAgent} {
		for _, field := range []string{"description", "code", "trace id", "plain"} {
			t.Run(fmt.Sprintf("step %d %s", step, field), func(t *testing.T) {
				r := newEntraRig(t)
				r.entra.answerWith(step, echo(field))
				if _, err := r.tokens.Token(bg, entraSam()); err == nil {
					t.Fatal("the refused exchange got a token")
				} else {
					check(t, "error", err.Error())
				}
				check(t, "log", r.logs.String())
			})
		}
	}
	t.Run("success", func(t *testing.T) {
		r := newEntraRig(t)
		got, err := r.tokens.Token(bg, entraSam())
		if err != nil || !strings.HasPrefix(got, "entra-tok-") {
			t.Fatalf("Token = %q, %v", got, err)
		}
		logs := r.logs.String()
		check(t, "log", logs)
		if !strings.Contains(logs, "sam-sre-agent") || !strings.Contains(logs, testAgentID) {
			t.Errorf("the fetch left no log line naming the agent and its identity: %s", logs)
		}
		if s := fmt.Sprintf("%v %+v", r.tokens, r.tokens); strings.Contains(s, "t1-") {
			t.Errorf("printing the cache shows the exchange token: %s", s)
		}
	})
}

// TestEntraLimitCoversBothSteps: the two requests share the one fetch limit,
// so a second step that never answers ends within it.
func TestEntraLimitCoversBothSteps(t *testing.T) {
	r := newEntraRig(t)
	r.tokens.limit = 50 * time.Millisecond
	hold := make(chan struct{})
	t.Cleanup(func() { close(hold) })
	r.entra.answerWith(stepAgent, func(http.ResponseWriter, *http.Request) { <-hold })
	start := time.Now()
	_, err := r.tokens.Token(bg, entraSam())
	want := "agent sam-sre-agent could not get a crm token. entra-lab did not answer at " + r.entra.tokenURL() + " within 3 seconds, so the call is refused. Check that the provider is up and reachable from Straza, then call again."
	if err == nil || err.Error() != want {
		t.Fatalf("err = %v\nwant  %s", err, want)
	}
	if took := time.Since(start); took > 2*time.Second {
		t.Fatalf("the refusal took %v, the limit did not hold across both steps", took)
	}
}
