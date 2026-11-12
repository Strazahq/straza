package server

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lestrrat-go/jwx/v3/jwa"
	"github.com/lestrrat-go/jwx/v3/jws"
	"github.com/lestrrat-go/jwx/v3/jwt"

	"github.com/strazahq/straza/internal/authn"
	"github.com/strazahq/straza/internal/config"
	"github.com/strazahq/straza/internal/store"
)

// The lab-shaped ids of the Entra exchange test.
const (
	entraTenant    = "aaaaaaaa-1111-4111-8111-aaaaaaaaaaaa"
	entraBlueprint = "bbbbbbbb-2222-4222-8222-bbbbbbbbbbbb"
	entraResource  = "api://ffffffff-6666-4666-8666-ffffffffffff/.default"
	entraJoe       = "cccccccc-3333-4333-8333-cccccccccccc"
	entraJoeMoved  = "99999999-7777-4777-8777-999999999999"
	entraForged    = "12345678-1234-4234-8234-123456789abc"
)

// entraIdP is a double of Entra's token endpoint for the agent identity
// exchange. The first step verifies the blueprint's certificate assertion
// against the certificates uploaded to the blueprint, found by the
// assertion's x5t#S256 header as Entra finds them, and answers AADSTS700027
// while none matches. The second step accepts only an exchange token the
// first step issued for the agent identity that asks. It records the
// fmi_path of every first step.
type entraIdP struct {
	*httptest.Server
	t      *testing.T
	mu     sync.Mutex
	certs  map[string]*x509.Certificate // x5t#S256 to the uploaded certificate
	issued map[string]string            // exchange token to its fmi_path
	fmis   []string
	n      int
}

func startEntraIdP(t *testing.T) *entraIdP {
	t.Helper()
	p := &entraIdP{t: t, certs: map[string]*x509.Certificate{}, issued: map[string]string{}}
	refused, err := os.ReadFile("../clientcredentials/testdata/entra-700027.json")
	if err != nil {
		t.Fatal(err)
	}
	p.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		f := r.PostForm
		p.mu.Lock()
		defer p.mu.Unlock()
		p.n++
		w.Header().Set("Content-Type", "application/json")
		if f.Get("scope") == "api://AzureADTokenExchange/.default" {
			p.fmis = append(p.fmis, f.Get("fmi_path"))
			if !p.verify(f.Get("client_assertion"), f.Get("client_id")) {
				w.WriteHeader(http.StatusUnauthorized)
				_, _ = w.Write(refused)
				return
			}
			t1 := fmt.Sprintf("eyJ0MSJ9.t1-%s-%d.c2ln", f.Get("fmi_path"), p.n)
			p.issued[t1] = f.Get("fmi_path")
			_, _ = fmt.Fprintf(w, `{"access_token":%q,"token_type":"Bearer","expires_in":3599}`, t1)
			return
		}
		if fmi, ok := p.issued[f.Get("client_assertion")]; !ok || fmi != f.Get("client_id") || f.Get("scope") != entraResource {
			t.Errorf("the second step = %v, want an issued exchange token of the asking identity and the resource scope", f)
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":"invalid_client","error_codes":[7000215]}`))
			return
		}
		_, _ = fmt.Fprintf(w, `{"access_token":"entra-tok-%s-%d","token_type":"Bearer","expires_in":3599}`, f.Get("client_id"), p.n)
	}))
	t.Cleanup(p.Close)
	return p
}

func (p *entraIdP) tokenURL() string { return p.URL + "/" + entraTenant + "/oauth2/v2.0/token" }

// verify checks a certificate assertion as Entra does. The caller holds mu.
func (p *entraIdP) verify(assertion, client string) bool {
	msg, err := jws.Parse([]byte(assertion))
	if err != nil || len(msg.Signatures()) != 1 {
		return false
	}
	thumb, ok := msg.Signatures()[0].ProtectedHeaders().X509CertThumbprintS256()
	cert := p.certs[thumb]
	if !ok || cert == nil {
		return false
	}
	tok, err := jwt.Parse([]byte(assertion), jwt.WithKey(jwa.RS256(), cert.PublicKey), jwt.WithAudience(p.tokenURL()))
	if err != nil {
		p.t.Errorf("the blueprint assertion does not verify under its certificate: %v", err)
		return false
	}
	iss, _ := tok.Issuer()
	sub, _ := tok.Subject()
	return iss == entraBlueprint && sub == entraBlueprint && client == entraBlueprint
}

// upload puts a key's certificate on the blueprint.
func (p *entraIdP) upload(t *testing.T, c authn.KeyCertificate) {
	t.Helper()
	cert, err := x509.ParseCertificate(c.DER)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(c.DER)
	p.mu.Lock()
	p.certs[base64.RawURLEncoding.EncodeToString(sum[:])] = cert
	p.mu.Unlock()
}

// asked returns the fmi_path of every first step so far.
func (p *entraIdP) asked() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.fmis...)
}

func withEntraProvider(idp *entraIdP) func(*config.Config) {
	return func(cfg *config.Config) {
		cfg.Server.PublicURL = "https://straza.example"
		cfg.OAuth.RefreshInterval = time.Hour
		cfg.OAuth.Providers = map[string]config.OAuthProvider{"entra-lab": {
			ClientID: "cf0611b0-e3d2-454b-acfe-4e2c1f40036b", ClientSecret: "s3cret",
			AuthURL: idp.URL + "/" + entraTenant + "/oauth2/v2.0/authorize", TokenURL: idp.tokenURL(),
			ClientCredentials: &config.ClientCredentials{EntraBlueprint: strings.ToUpper(entraBlueprint), Scopes: []string{entraResource}},
		}}
	}
}

// mcpCallWith posts one tools/call with extra request headers and returns
// the raw answer.
func mcpCallWith(t *testing.T, base, tok string, header http.Header, args map[string]any) string {
	t.Helper()
	raw, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/call",
		"params": map[string]any{"name": "crm__echo", "arguments": args}})
	req, _ := http.NewRequest(http.MethodPost, base+"/mcp", bytes.NewReader(raw))
	req.Header = header.Clone()
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+tok)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)
	return string(body)
}

// TestGatewayEntraExchange is the Entra agent identity exchange end to end
// through the gateway. Until the key's certificate is on the blueprint the
// agent is refused with the sentence that names the upload. Then the agent
// runs on the token of its own Entra agent identity, which comes from its
// session's check-in and never from a header or a tool argument, with no
// store read on the way. An agent without a mapping and a person are
// refused, and Entra never hears of them. A changed mapping reaches the next
// call without a restart. Every call leaves exactly one record with the
// actor, the server and tool, the decision and the reason, and no answer and
// no record carries a token.
func TestGatewayEntraExchange(t *testing.T) {
	t.Parallel()
	idp := startEntraIdP(t)
	app, base, cs := testAppCounting(t, withEntraProvider(idp))
	ctx := context.Background()
	key, err := app.assertionKeys.Stage(ctx, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := app.assertionKeys.Promote(ctx, time.Now().Add(2*authn.KeyReloadInterval), key.KID); err != nil {
		t.Fatal(err)
	}
	kim := seedIdentity(t, app)
	grantAdmin(t, app, kim.ID)
	adminTok, _ := checkinToken(t, app, base)
	up := startGatewayUpstream(t)
	seedGatewayUser(t, app, "alice", "dev")
	joe := seedAgent(t, app, "joe", "alice", "dev")
	sam := seedAgent(t, app, "sam", "", "dev")
	setEntra := func(u store.User, id string) {
		t.Helper()
		if code := adminReq(t, http.MethodPatch, base+"/v1/admin/users/"+u.ID, adminTok, map[string]any{"entra_agent_id": id}, nil); code != http.StatusOK {
			t.Fatalf("set the Entra agent id of %s = %d", u.Username, code)
		}
	}
	setEntra(joe, entraJoe)
	row := installCCAppOn(t, app, up, "crm", "entra-lab")
	catRecompile(t, app)

	aliceTok := sessionToken(t, base, "alice")
	joeTok, _ := agentCheckin(t, app, base, "joe")
	samTok := agentSessionToken(t, app, base, "sam")
	var answers []string
	unescape := strings.NewReplacer("\\u003c", "<", "\\u003e", ">", "\\u0026", "&")
	call := func(tok string) string {
		t.Helper()
		raw := echoCall(t, base, tok, "crm")
		answers = append(answers, raw)
		return unescape.Replace(raw)
	}
	once := func(user store.User, effect, reason string) map[string]any {
		t.Helper()
		recs := awaitMCPRecords(t, app, 1, func(d map[string]any) bool {
			return d["app"] == "crm" && d["user"] == user.ID && d["effect"] == effect && (reason == "" || d["reason"] == reason)
		})
		rec := recs[0]
		if rec["session"] == nil || rec["toolName"] != "echo" || rec["reason"] == nil || rec["reason"] == "" {
			t.Errorf("the record lacks its actor, tool or reason: %v", rec)
		}
		return rec
	}

	// Before the certificate is on the blueprint, Entra refuses the first
	// step and the agent reads which certificate to upload.
	const open = "agent joe could not get a crm token. "
	noCert := open + "Entra refused the blueprint " + entraBlueprint + " (AADSTS700027), because it holds no certificate of the client assertion key " + key.KID + ". " +
		"An Agent ID Administrator uploads the certificate that strazactl signing-keys certificate client_assertion " + key.KID + " prints to that blueprint, then the agent calls again."
	if raw := call(joeTok); !strings.Contains(raw, noCert) {
		t.Fatalf("joe before the upload = %s", raw)
	}
	if rec := once(joe, "deny", noCert); rec["credentialSource"] != nil || rec["credentialId"] != nil {
		t.Errorf("the refused record carries credential fields: %v", rec)
	}

	cert, err := app.assertionKeys.Certificate(ctx, time.Now(), key.KID)
	if err != nil {
		t.Fatal(err)
	}
	idp.upload(t, cert)
	app.clientTokens.DropUser(joe.ID)
	before := cs.snapshot()
	for range 2 {
		if raw := call(joeTok); !strings.Contains(raw, "echo: hi") {
			t.Fatalf("joe after the upload = %s", raw)
		}
	}
	assertNoRepoAccess(t, cs, "Entra calls, the exchange and the cached one", before)
	if got := strings.Join(idp.asked(), ","); got != entraJoe+","+entraJoe {
		t.Fatalf("Entra was asked for %q, want joe's identity for the refused call and once for the two allowed ones", got)
	}
	if !up.sawAuth("Bearer entra-tok-" + entraJoe + "-3") {
		t.Fatal("joe's Entra token never reached the upstream")
	}
	allowed := awaitMCPRecords(t, app, 2, func(d map[string]any) bool {
		return d["app"] == "crm" && d["user"] == joe.ID && d["effect"] == "allow"
	})
	for _, rec := range allowed {
		if rec["credentialSource"] != "client_credentials" || rec["credentialId"] != "client_credentials:"+row.ID+":"+joe.ID || rec["credentialOwner"] != nil {
			t.Errorf("joe's record = %v, want the source client_credentials, the stable id and no owner", rec)
		}
	}

	// The Entra agent identity comes from the session and nothing else: a
	// header or a tool argument that names another identity changes nothing.
	for _, tc := range []struct {
		name   string
		header http.Header
		args   map[string]any
	}{
		{"a forged header", http.Header{"X-Straza-Entra-Agent-Id": {entraForged}, "X-Ms-Client-Principal-Id": {entraForged}}, map[string]any{"text": "hi"}},
		{"a forged tool argument", http.Header{}, map[string]any{"text": "hi " + entraForged, "entraAgentId": entraForged, "fmi_path": entraForged, "client_id": entraForged}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			app.clientTokens.DropUser(joe.ID)
			n := len(idp.asked())
			answers = append(answers, mcpCallWith(t, base, joeTok, tc.header, tc.args))
			asked := idp.asked()
			if len(asked) != n+1 || asked[n] != entraJoe {
				t.Fatalf("Entra was asked for %v after the call, want one exchange for joe's own identity", asked[n:])
			}
		})
	}

	// The identity a call runs as is the one its verified token carries, so
	// a cached subject dropped after the gateway authenticated the call
	// neither refuses it as unmapped nor changes the identity.
	claims, err := app.tokens.Verify(joeTok)
	if err != nil {
		t.Fatal(err)
	}
	held, ok := app.subjects.forToken(claims)
	if !ok {
		t.Fatal("joe's session has no cached subject")
	}
	app.subjects.drop(claims.Session)
	app.clientTokens.DropUser(joe.ID)
	n := len(idp.asked())
	params, _ := json.Marshal(map[string]any{"name": "crm__echo", "arguments": map[string]string{"text": "hi"}})
	rec := httptest.NewRecorder()
	app.callTool(rec, httptest.NewRequest(http.MethodPost, "/mcp", nil), rpcRequest{ID: json.RawMessage("1"), Method: "tools/call", Params: params}, claims, held.sub, nil)
	if raw := rec.Body.String(); !strings.Contains(raw, "echo: hi") {
		t.Fatalf("joe's call after his cached subject was dropped = %s", raw)
	}
	if asked := idp.asked(); len(asked) != n+1 || asked[n] != entraJoe {
		t.Fatalf("Entra was asked for %v, want one exchange for joe's own identity", asked[n:])
	}

	// A disable that lands after the gateway authenticated the call bites
	// before a token is fetched: the call gets the gateway's own refusal and
	// one deny record that names the tool, and Entra never hears of it.
	app.denylist.revokeUser(joe.ID)
	n = len(idp.asked())
	rec = httptest.NewRecorder()
	app.callTool(rec, httptest.NewRequest(http.MethodPost, "/mcp", nil), rpcRequest{ID: json.RawMessage("1"), Method: "tools/call", Params: params}, claims, held.sub, nil)
	if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), revokedIdentityMsg) {
		t.Fatalf("joe's call after his disable = %d %s", rec.Code, rec.Body.String())
	}
	if asked := idp.asked(); len(asked) != n {
		t.Fatalf("a disabled agent's call made Entra hear of it: %v", asked[n:])
	}
	awaitMCPRecords(t, app, 1, func(d map[string]any) bool {
		return d["user"] == joe.ID && d["toolName"] == "echo" && d["effect"] == "deny" && d["reason"] == revokedIdentityMsg
	})
	app.denylist.allowUser(joe.ID)

	// An agent without a mapping and a person are refused, and Entra never
	// hears of them.
	n = len(idp.asked())
	noMapping := "agent sam could not get a crm token. The agent has no Entra agent identity on its Straza record. " +
		"Ask the identity team to set entraAgentId for sam, or set it with strazactl users entra-agent-id set sam <client id>, then call again."
	if raw := call(samTok); !strings.Contains(raw, noMapping) {
		t.Fatalf("sam without a mapping = %s", raw)
	}
	once(sam, "deny", noMapping)
	person := "MCP server crm needs your own entra-lab sign-in and you have not connected."
	if raw := call(aliceTok); !strings.Contains(raw, person) {
		t.Fatalf("alice = %s", raw)
	}
	if got := idp.asked(); len(got) != n {
		t.Fatalf("an unmapped agent or a person made Entra hear of them: %v", got[n:])
	}

	// A changed mapping drops joe's cached subject and token, and his next
	// session runs as the new identity with no restart.
	setEntra(joe, entraJoeMoved)
	joeTok, _ = agentCheckin(t, app, base, "joe")
	if raw := call(joeTok); !strings.Contains(raw, "echo: hi") {
		t.Fatalf("joe after the mapping changed = %s", raw)
	}
	if got := idp.asked(); got[len(got)-1] != entraJoeMoved {
		t.Fatalf("after the mapping changed Entra was asked for %q, want %s", got[len(got)-1], entraJoeMoved)
	}

	events := outboxDataFor(t, app, "straza.audit.mcp")
	for _, leak := range []string{"entra-tok-", "t1-", "eyJ"} {
		for _, raw := range answers {
			if strings.Contains(raw, leak) {
				t.Errorf("a gateway answer carries %q: %s", leak, raw)
			}
		}
		if s := fmt.Sprint(events); strings.Contains(s, leak) {
			t.Errorf("an audit record carries %q", leak)
		}
	}
}

// TestOAuthProvidersNameTheEntraBlueprint checks that the providers list
// names the blueprint of a provider whose agents go through Entra, in lower
// case, which is how the console tells that a deployment uses Entra.
func TestOAuthProvidersNameTheEntraBlueprint(t *testing.T) {
	t.Parallel()
	app, base, _ := testAppCounting(t, withEntraProvider(startEntraIdP(t)))
	grantAdmin(t, app, seedIdentity(t, app).ID)
	adminTok, _ := checkinToken(t, app, base)
	var providers []oauthProviderPayload
	if code := adminReq(t, http.MethodGet, base+"/v1/admin/oauth/providers", adminTok, nil, &providers); code != http.StatusOK {
		t.Fatalf("providers = %d", code)
	}
	if len(providers) != 1 || providers[0].Name != "entra-lab" || providers[0].EntraBlueprint != entraBlueprint {
		t.Errorf("providers = %+v, want entra-lab with the blueprint %s", providers, entraBlueprint)
	}
}
