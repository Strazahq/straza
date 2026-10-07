package server

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

// startConnect starts a sign-in to the github server as the session's user
// and answers the start payload.
func startConnect(t *testing.T, base, sessionTok string) (authorizeURL, pageURL string, raw []byte) {
	t.Helper()
	req, _ := http.NewRequest(http.MethodPost, base+"/v1/connect/github", nil)
	req.Header.Set("Authorization", "Bearer "+sessionTok)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ = io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("connect start = %d: %s", resp.StatusCode, raw)
	}
	var start struct {
		Provider     string `json:"provider"`
		AuthorizeURL string `json:"authorize_url"`
		PageURL      string `json:"page_url"`
	}
	if err := jsonUnmarshal(raw, &start); err != nil || start.Provider != "github" || start.AuthorizeURL == "" {
		t.Fatalf("connect start payload: %s (err %v)", raw, err)
	}
	return start.AuthorizeURL, start.PageURL, raw
}

// browserReturn plays the browser of whoever is signed in at the provider:
// it follows the provider's redirect to strazad's callback and stops at the
// callback's own answer, which it returns with every byte the browser saw.
func browserReturn(t *testing.T, target string, provider *oauthProviderMock, providerUser string) (*http.Response, []byte) {
	t.Helper()
	provider.mu.Lock()
	provider.nextUser = providerUser
	provider.mu.Unlock()
	client := &http.Client{CheckRedirect: func(req *http.Request, _ []*http.Request) error {
		if strings.HasPrefix(req.URL.Path, "/self-service/") {
			return http.ErrUseLastResponse
		}
		return nil
	}}
	resp, err := client.Get(target)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	return resp, body
}

// handedOver reads the code and the state the callback hands to the
// Credentials tab behind the # of its redirect.
func handedOver(t *testing.T, resp *http.Response) url.Values {
	t.Helper()
	loc := resp.Header.Get("Location")
	const lead = testConnectPage + "#connect&"
	if resp.StatusCode != http.StatusSeeOther || !strings.HasPrefix(loc, lead) {
		t.Fatalf("callback = %d with Location %q, want 303 to %s", resp.StatusCode, loc, lead)
	}
	vals, err := url.ParseQuery(strings.TrimPrefix(loc, lead))
	if err != nil {
		t.Fatalf("callback fragment %q: %v", loc, err)
	}
	return vals
}

// finishConnect posts a code and a state to the signed-in finish route the
// way the Credentials tab does. An empty sessionTok sends no bearer.
func finishConnect(t *testing.T, base, sessionTok string, body any) (int, string) {
	t.Helper()
	raw, _ := json.Marshal(body)
	req, _ := http.NewRequest(http.MethodPost, base+"/v1/connect/callback", bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	if sessionTok != "" {
		req.Header.Set("Authorization", "Bearer "+sessionTok)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	out, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(out)
}

// TestConnectFinishRefusesAnotherUsersState pins that a sign-in is stored
// only for the user who started it. ivan starts a sign-in and alice's
// browser, signed in at the provider as alice, follows his link: the callback
// redeems nothing, and alice's session cannot finish ivan's state.
func TestConnectFinishRefusesAnotherUsersState(t *testing.T) {
	t.Parallel()
	gh := startGithubMock(t)
	provider := startOAuthProviderMock(t, 0)
	app, base := testApp(t, withOAuthGithub(provider))
	alice := seedGatewayUser(t, app, "alice", "dev")
	ivan := seedGatewayUser(t, app, "ivan", "dev")
	installOAuthGithubApp(t, app, gh.URL)
	aliceTok := sessionToken(t, base, "alice")
	ivanTok := sessionToken(t, base, "ivan")

	authorize, _, _ := startConnect(t, base, ivanTok)
	resp, _ := browserReturn(t, authorize, provider, "alice")
	if rows := userGrants(t, app, ivan.ID); len(rows) != 0 {
		t.Fatalf("the callback stored %d row(s) for ivan on the state alone", len(rows))
	}
	handed := handedOver(t, resp)
	if provider.tokenCallCount() != 0 {
		t.Fatalf("the callback redeemed the code: %d token endpoint call(s)", provider.tokenCallCount())
	}

	code, body := finishConnect(t, base, aliceTok, map[string]string{"code": handed.Get("code"), "state": handed.Get("state")})
	const want = "ivan started this sign-in to github, and you are signed in as alice, so nothing was stored. If someone sent you the link, tell an administrator. To connect your own account, start the sign-in yourself"
	if code != http.StatusForbidden || !strings.Contains(body, want) {
		t.Errorf("alice finishing ivan's state = %d %s, want 403 with %q", code, body, want)
	}
	if provider.tokenCallCount() != 0 {
		t.Errorf("the refused finish redeemed the code: %d token endpoint call(s)", provider.tokenCallCount())
	}
	for name, id := range map[string]string{"alice": alice.ID, "ivan": ivan.ID} {
		if rows := userGrants(t, app, id); len(rows) != 0 {
			t.Errorf("%s holds %d credential row(s) after the refusal, want none", name, len(rows))
		}
	}
	refused := identityActions(t, app, "oauth.connect.refused")
	if len(refused) != 1 || refused[0]["user"] != ivan.ID || refused[0]["setBy"] != alice.ID ||
		refused[0]["app"] != "github" || refused[0]["provider"] != "github" ||
		refused[0]["reason"] != "the state names another user" {
		t.Errorf("oauth.connect.refused records = %v, want one naming ivan as user and alice as setBy", refused)
	}
	if stored := identityActions(t, app, "oauth.connect"); len(stored) != 0 {
		t.Errorf("oauth.connect records after a refusal = %v, want none", stored)
	}
}

// TestConnectCallbackRedeemsNothing pins the provider's return address: it
// hands whatever the provider sent to the Credentials tab behind the # of a
// fixed same-origin path, and never calls the provider or the store.
func TestConnectCallbackRedeemsNothing(t *testing.T) {
	t.Parallel()
	gh := startGithubMock(t)
	provider := startOAuthProviderMock(t, 0)
	app, base := testApp(t, withOAuthGithub(provider))
	alice := seedGatewayUser(t, app, "alice", "dev")
	installOAuthGithubApp(t, app, gh.URL)
	state, err := app.tokens.MintConnectState(alice.ID, "app-any")
	if err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name  string
		query url.Values
		want  url.Values
	}{
		{"a valid state and a code", url.Values{"code": {"c0de"}, "state": {state}}, url.Values{"code": {"c0de"}, "state": {state}}},
		{"the provider said no", url.Values{"error": {"access_denied"}, "state": {state}}, url.Values{"error": {"access_denied"}}},
		{"hostile values", url.Values{"code": {"x\r\nSet-Cookie: a=b"}, "state": {"//evil.example/#&code=y"}},
			url.Values{"code": {"x\r\nSet-Cookie: a=b"}, "state": {"//evil.example/#&code=y"}}},
		{"an unknown parameter is dropped", url.Values{"code": {"c"}, "state": {"s"}, "next": {"https://evil.example"}}, url.Values{"code": {"c"}, "state": {"s"}}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			resp, _ := browserReturn(t, base+"/v1/connect/callback?"+c.query.Encode(), provider, "alice")
			got := handedOver(t, resp)
			if got.Encode() != c.want.Encode() {
				t.Errorf("handed over %q, want %q", got.Encode(), c.want.Encode())
			}
			if strings.ContainsAny(resp.Header.Get("Location"), "\r\n") || resp.Header.Get("Set-Cookie") != "" {
				t.Errorf("the redirect carries a raw line break or a cookie: %q", resp.Header.Get("Location"))
			}
		})
	}

	resp, body := browserReturn(t, base+"/v1/connect/callback", provider, "alice")
	if resp.StatusCode != http.StatusBadRequest || !strings.Contains(string(body), "Start the sign-in again") {
		t.Errorf("a bare callback = %d %s, want the 400 page", resp.StatusCode, body)
	}
	if provider.tokenCallCount() != 0 || len(userGrants(t, app, alice.ID)) != 0 {
		t.Errorf("the callback reached the provider (%d calls) or the store", provider.tokenCallCount())
	}
}

// TestConnectFinishRefusals walks the finish route's refusals. None of them
// stores a row.
func TestConnectFinishRefusals(t *testing.T) {
	t.Parallel()
	gh := startGithubMock(t)
	provider := startOAuthProviderMock(t, 0)
	app, base := testApp(t, withOAuthGithub(provider))
	alice := seedGatewayUser(t, app, "alice", "dev")
	installOAuthGithubApp(t, app, gh.URL)
	aliceTok := sessionToken(t, base, "alice")
	authorize, _, _ := startConnect(t, base, aliceTok)
	own := strings.SplitN(strings.SplitN(authorize, "state=", 2)[1], "&", 2)[0]
	own, _ = url.QueryUnescape(own)

	cases := []struct {
		name   string
		tok    string
		body   any
		status int
		want   string
	}{
		{"no session", "", map[string]string{"code": "c", "state": own}, http.StatusUnauthorized, "missing bearer token"},
		{"no code", aliceTok, map[string]string{"state": own}, http.StatusBadRequest, "the body must be a JSON object with code and state"},
		{"no state", aliceTok, map[string]string{"code": "c"}, http.StatusBadRequest, "the body must be a JSON object with code and state"},
		{"a garbage state", aliceTok, map[string]string{"code": "c", "state": "not-a-state"}, http.StatusBadRequest,
			"This sign-in link is invalid or older than 10 minutes, so nothing was stored. Press the Sign in button on the server's row again"},
		{"a session token as the state", aliceTok, map[string]string{"code": "c", "state": aliceTok}, http.StatusBadRequest,
			"This sign-in link is invalid or older than 10 minutes"},
		{"a code the provider rejects", aliceTok, map[string]string{"code": "made-up", "state": own}, http.StatusBadGateway,
			"github rejected the sign-in code, so nothing was stored. A code works once and for a short time. Press Sign in with github again"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			status, body := finishConnect(t, base, c.tok, c.body)
			if status != c.status || !strings.Contains(body, c.want) {
				t.Errorf("finish = %d %s, want %d with %q", status, body, c.status, c.want)
			}
		})
	}
	if rows := userGrants(t, app, alice.ID); len(rows) != 0 {
		t.Errorf("alice holds %d row(s) after refusals only", len(rows))
	}
}

// TestConnectCallbackKeepsTheCodeOutOfTheLog pins that neither value the
// provider hands the browser reaches a log line, at the most verbose level.
func TestConnectCallbackKeepsTheCodeOutOfTheLog(t *testing.T) {
	t.Parallel()
	log, buf := captureLogger()
	_, base, _ := testAppFaultLog(t, log)
	buf.Reset()
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Get(base + "/v1/connect/callback?code=c0de-in-the-address&state=state-in-the-address")
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	rec := waitForAccessRecord(t, buf, `route="GET /v1/connect/callback"`)
	if !strings.Contains(rec, "status=303") {
		t.Errorf("callback record = %s, want status=303", rec)
	}
	for _, leaked := range []string{"c0de-in-the-address", "state-in-the-address"} {
		if strings.Contains(buf.String(), leaked) {
			t.Errorf("%q reached the log: %s", leaked, buf.String())
		}
	}
}

// TestConnectStartRefusesAnAgent pins that an agent cannot start a provider
// sign-in: a sign-in is stored only for the user of the session that
// finishes it in a browser, and no browser is signed in as an agent, so the
// refusal names the ways an agent does get a credential on the server.
func TestConnectStartRefusesAnAgent(t *testing.T) {
	t.Parallel()
	gh := startGithubMock(t)
	provider := startOAuthProviderMock(t, 0)
	app, base := testApp(t, withOAuthGithub(provider))
	seedGatewayUser(t, app, "alice", "dev")
	seedAgent(t, app, "joe", "alice", "dev")
	seedAgent(t, app, "orphan", "", "dev")
	installOAuthGithubApp(t, app, gh.URL)

	const why = " cannot sign in at github, because a sign-in is finished in a browser that is signed in to Straza as the same user, and an agent has no such browser. An administrator sets credential.agents on github to "
	cases := []struct{ agent, want string }{
		{"joe", "agent joe" + why + "sponsor so joe runs on alice's sign-in once alice allows it, or to shared or client_credentials"},
		{"orphan", "agent orphan" + why + "shared or client_credentials"},
	}
	for _, c := range cases {
		t.Run(c.agent, func(t *testing.T) {
			tok, _ := gatewaySession(t, base, c.agent)
			req, _ := http.NewRequest(http.MethodPost, base+"/v1/connect/github", nil)
			req.Header.Set("Authorization", "Bearer "+tok)
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			body, _ := io.ReadAll(resp.Body)
			_ = resp.Body.Close()
			if resp.StatusCode != http.StatusForbidden || !strings.Contains(string(body), c.want) {
				t.Errorf("start as %s = %d %s, want 403 with %q", c.agent, resp.StatusCode, body, c.want)
			}
			if strings.Contains(string(body), "authorize_url") {
				t.Errorf("the refusal carries a provider address: %s", body)
			}
		})
	}
}
