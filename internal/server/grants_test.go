package server

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/strazahq/straza/internal/authn"
	"github.com/strazahq/straza/internal/dpop"
	"github.com/strazahq/straza/internal/store"
)

// payAPI is the git URL of the rig's repo Strazahq/pay-api.
const payAPI = "https://github.com/Strazahq/pay-api.git"

// zeros is the object id git names for a ref a push creates.
var zeros = strings.Repeat("0", 40)

// mainRef is a push that moves refs/heads/main.
var mainRef = []authn.GrantRef{{Ref: "refs/heads/main",
	Old: "3f786850e387550fdab836ed7e6dc881de23001b", New: "89e6c98d92887913cadf06b2adb97f26cde4849b"}}

func intp(v int) *int { return &v }

// grantClaims decodes the header and the claims of a compact grant.
func grantClaims(t *testing.T, grant string) (map[string]any, map[string]any) {
	t.Helper()
	parts := strings.Split(grant, ".")
	if len(parts) != 3 {
		t.Fatalf("grant %q is not a compact JWS", grant)
	}
	var header, claims map[string]any
	for i, out := range []*map[string]any{&header, &claims} {
		raw, err := base64.RawURLEncoding.DecodeString(parts[i])
		if err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(raw, out); err != nil {
			t.Fatal(err)
		}
	}
	return header, claims
}

// TestGrantResolve pins POST /v1/grants/resolve: a listed repo answers the
// server, its canonical name and the audience, also when git spells the
// name in another case, with a trailing slash or without .git. A server
// whose git tools the session cannot see counts as absent, and a URL no
// server's upstream prefixes answers not_listed. The route writes no
// record.
func TestGrantResolve(t *testing.T) {
	t.Parallel()
	r := newGrantRig(t, grantRigOpts{})
	listed := map[string]string{"srv": "ghapp", "repo": "Strazahq/pay-api", "aud": "https://ghapp.example.com"}
	cases := []struct {
		name, user, body string
		status           int
		want             map[string]string
		err              string
	}{
		{"a listed repo", "eve", `{"upstream":"` + payAPI + `"}`, 200, listed, ""},
		{"another case", "eve", `{"upstream":"https://github.com/strazahq/PAY-API"}`, 200, listed, ""},
		{"a trailing slash", "eve", `{"upstream":"https://github.com/Strazahq/pay-api/"}`, 200, listed, ""},
		{"a role that may only fetch", "fen", `{"upstream":"` + payAPI + `"}`, 200, listed, ""},
		{"a role on one repo resolves another", "pam", `{"upstream":"https://github.com/Strazahq/site.git"}`, 200,
			map[string]string{"srv": "ghapp", "repo": "Strazahq/site", "aud": "https://ghapp.example.com"}, ""},
		{"an invisible server is absent", "nob", `{"upstream":"` + payAPI + `"}`, 404, map[string]string{"code": "not_listed"},
			fmt.Sprintf(grantNoServerMsg, payAPI)},
		{"another host", "eve", `{"upstream":"https://gitlab.com/Strazahq/pay-api.git"}`, 404, map[string]string{"code": "not_listed"},
			fmt.Sprintf(grantNoServerMsg, "https://gitlab.com/Strazahq/pay-api.git")},
		{"a credential in the URL is not echoed", "eve", `{"upstream":"https://bob:hunter2@gitlab.com/x/y"}`, 404,
			map[string]string{"code": "not_listed"}, ""},
		{"no repo name", "eve", `{"upstream":"https://github.com/Strazahq"}`, 404, map[string]string{"code": "not_listed"}, ""},
		{"too deep a path", "eve", `{"upstream":"https://github.com/Strazahq/pay-api/tree/main"}`, 404, map[string]string{"code": "not_listed"}, ""},
		{"no upstream", "eve", `{}`, 400, nil, fmt.Sprintf(grantBadRequestMsg, "its body is not a JSON object with the upstream URL")},
		{"not JSON", "eve", `upstream`, 400, nil, fmt.Sprintf(grantBadRequestMsg, "its body is not a JSON object with the upstream URL")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := r.session(t, tc.user)
			status, out := r.post(t, s, "/v1/grants/resolve", tc.body)
			if status != tc.status {
				t.Fatalf("status = %d %v, want %d", status, out, tc.status)
			}
			for k, v := range tc.want {
				if out[k] != v {
					t.Errorf("%s = %v, want %q (answer %v)", k, out[k], v, out)
				}
			}
			if tc.err != "" && out["error"] != tc.err {
				t.Errorf("error = %q\nwant    %q", out["error"], tc.err)
			}
			if e, _ := out["error"].(string); strings.Contains(e, "hunter2") {
				t.Errorf("the refusal echoes the URL's password: %q", e)
			}
			if recs := r.records(t, s); len(recs) != 0 {
				t.Errorf("resolve wrote %d records, want none: %v", len(recs), recs)
			}
		})
	}
}

// TestGrantResolveMissRefresh pins that a repo the list does not hold
// answers not_listed with the sentence that says Straza asked the server
// for its list again, that it did ask, and that the repo resolves once the
// new listing holds it.
func TestGrantResolveMissRefresh(t *testing.T) {
	t.Parallel()
	r := newGrantRig(t, grantRigOpts{})
	before := r.up.listings()
	r.up.add("Strazahq/new", "104")
	r.clock.advance(2 * time.Minute)
	s := r.session(t, "eve")
	status, out := r.post(t, s, "/v1/grants/resolve", `{"upstream":"https://github.com/strazahq/NEW.git"}`)
	if want := fmt.Sprintf(grantNotListedMsg, "strazahq/NEW", "ghapp"); status != 404 || out["code"] != "not_listed" || out["error"] != want {
		t.Fatalf("answer = %d %v, want 404 not_listed %q", status, out, want)
	}
	waitFor(t, "a listing after the miss", func() bool { return r.up.listings() > before })
	waitFor(t, "the new repo in the list", func() bool {
		l := r.app.objects.Get(r.appID)
		_, ok := l.ByValue("Strazahq/new")
		return ok
	})
	if status, out := r.post(t, s, "/v1/grants/resolve", `{"upstream":"https://github.com/strazahq/NEW.git"}`); status != 200 || out["repo"] != "Strazahq/new" {
		t.Errorf("after the listing the repo answers %d %v, want 200 Strazahq/new", status, out)
	}
}

// TestGrantSessionRefusals pins the session checks of both routes: a
// session without a key gets sentence 1 with 403, a key-bound token sent
// as a bearer token is refused as a copied token, and no token is refused.
// None of them writes a straza.audit.mcp record.
func TestGrantSessionRefusals(t *testing.T) {
	t.Parallel()
	r := newGrantRig(t, grantRigOpts{})
	for _, path := range []string{"/v1/grants/resolve", "/v1/grants"} {
		s := r.session(t, "eve")
		bearer, _, err := r.app.tokens.Mint(s.claims)
		if err != nil {
			t.Fatal(err)
		}
		body := `{"upstream":"` + payAPI + `","op":"fetch"}`
		cases := []struct {
			name, auth string
			status     int
			err        string
		}{
			{"a session without a key", "Bearer " + bearer, 403, grantUnboundMsg},
			{"a key-bound token as a bearer token", "Bearer " + s.token, 401, proofCopiedTokenMsg},
			{"no token", "", 401, "missing session token"},
		}
		for _, tc := range cases {
			req, _ := http.NewRequest(http.MethodPost, r.base+path, strings.NewReader(body))
			if tc.auth != "" {
				req.Header.Set("Authorization", tc.auth)
			}
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			var out map[string]any
			_ = json.NewDecoder(resp.Body).Decode(&out)
			_ = resp.Body.Close()
			if resp.StatusCode != tc.status || out["error"] != tc.err {
				t.Errorf("%s %s = %d %v, want %d %q", path, tc.name, resp.StatusCode, out, tc.status, tc.err)
			}
		}
		if recs := r.records(t, s); len(recs) != 0 {
			t.Errorf("%s: the refused sessions wrote %d records, want none", path, len(recs))
		}
	}
}

// TestGrantFetchClaims pins an issued fetch grant: the answer, the header,
// every claim, and the one record whose CloudEvents id is the dec claim.
func TestGrantFetchClaims(t *testing.T) {
	t.Parallel()
	r := newGrantRig(t, grantRigOpts{})
	s := r.session(t, "eve")
	status, out := r.grant(t, s, "https://github.com/strazahq/pay-api", "fetch", nil, nil)
	if status != 200 {
		t.Fatalf("fetch grant = %d %v, want 200", status, out)
	}
	grant, _ := out["grant"].(string)
	header, claims := grantClaims(t, grant)
	if header["typ"] != authn.GrantType || header["alg"] != "EdDSA" || header["kid"] == "" {
		t.Errorf("header = %v", header)
	}
	jkt := dpop.Thumbprint(r.key.Public().(ed25519.PublicKey))
	iat, _ := claims["iat"].(float64)
	exp, _ := claims["exp"].(float64)
	act, _ := claims["act"].(map[string]any)
	cnf, _ := claims["cnf"].(map[string]any)
	obj, _ := claims["obj"].(map[string]any)
	switch {
	case claims["iss"] != r.base, claims["aud"] != "https://ghapp.example.com", claims["sub"] != r.users["eve"].ID:
		t.Errorf("iss, aud or sub wrong: %v", claims)
	case act["ses"] != s.claims.Session || act["hrn"] != "claude-code/2.1.0" || act["kind"] != "human":
		t.Errorf("act = %v", act)
	case cnf["jkt"] != jkt:
		t.Errorf("cnf = %v, want jkt %s", cnf, jkt)
	case claims["srv"] != "ghapp" || claims["op"] != "fetch":
		t.Errorf("srv or op wrong: %v", claims)
	case obj["type"] != "repo" || obj["value"] != "Strazahq/pay-api" || obj["id"] != "101":
		t.Errorf("obj = %v", obj)
	case exp-iat != 120:
		t.Errorf("exp - iat = %v, want 120", exp-iat)
	}
	for _, absent := range []string{"refs", "apr"} {
		if _, ok := claims[absent]; ok {
			t.Errorf("a fetch grant without an approval carries %s: %v", absent, claims[absent])
		}
	}
	wantExp := time.Unix(int64(exp), 0).UTC().Format(time.RFC3339)
	if out["aud"] != "https://ghapp.example.com" || out["repo"] != "Strazahq/pay-api" || out["srv"] != "ghapp" || out["exp"] != wantExp {
		t.Errorf("answer = %v, want exp %s", out, wantExp)
	}
	recs := r.records(t, s)
	if len(recs) != 1 {
		t.Fatalf("%d records, want one: %v", len(recs), recs)
	}
	rec := recs[0]
	g, _ := rec.data["grant"].(map[string]any)
	switch {
	case rec.id != claims["dec"]:
		t.Errorf("record id %s, dec %v", rec.id, claims["dec"])
	case rec.data["event"] != "grant" || rec.data["effect"] != "allow" || rec.data["user"] != r.users["eve"].ID ||
		rec.data["toolName"] != "git_fetch" || rec.data["arguments"] != `{"repo":"Strazahq/pay-api"}`:
		t.Errorf("record = %v", rec.data)
	case g["jti"] != claims["jti"] || g["op"] != "fetch" || g["aud"] != "https://ghapp.example.com" || g["exp"] != wantExp:
		t.Errorf("grant member = %v", g)
	}
	if _, ok := g["approval"]; ok {
		t.Errorf("the grant member names an approval that never ran: %v", g)
	}
}

// TestGrantKind pins act.kind by the rule check-in reads a user's kind
// with: a person with no user type is human, a user typed agent or service
// keeps the type, and a user marked only by attrs.kind nhi is an agent.
func TestGrantKind(t *testing.T) {
	t.Parallel()
	r := newGrantRig(t, grantRigOpts{})
	ctx := context.Background()
	eve := r.checked["eve"]
	cases := []struct {
		name string
		user store.User
		want string
	}{
		{"a person with no type", store.User{Username: "pat"}, "human"},
		{"an agent", store.User{Username: "ada", UserType: store.UserTypeAgent}, "agent"},
		{"a service account", store.User{Username: "svc", UserType: store.UserTypeService}, "service"},
		{"an NHI marked by attrs only", store.User{Username: "nhi", Attrs: `{"kind":"nhi"}`}, "agent"},
	}
	for _, tc := range cases {
		u, err := r.app.store.Users().Create(ctx, tc.user)
		if err != nil {
			t.Fatal(err)
		}
		sub := eve.sub
		sub.User, sub.UserType = u.Username, u.UserType
		s := r.sessionAs(t, u.ID, sub, sessionFacts{person: personUser(u), harness: "claude-code"})
		status, out := r.grant(t, s, payAPI, "fetch", nil, nil)
		if status != 200 {
			t.Fatalf("%s: fetch grant = %d %v", tc.name, status, out)
		}
		_, claims := grantClaims(t, out["grant"].(string))
		if act, _ := claims["act"].(map[string]any); act["kind"] != tc.want {
			t.Errorf("%s: act.kind = %v, want %s", tc.name, act["kind"], tc.want)
		}
	}
}
