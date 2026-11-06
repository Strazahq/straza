package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// tokensServer is the admin surface api-token revoke walks: the token list
// and the revoke by id. It records each request, so a test sees whether the
// list was read and which id was revoked.
type tokensServer struct {
	list       string // the JSON array GET /v1/admin/api-tokens answers
	listRefuse string // a 403 sentence the list answers instead, when set

	mu       sync.Mutex
	requests []string
}

func (s *tokensServer) start(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/admin/api-tokens", func(w http.ResponseWriter, _ *http.Request) {
		s.record("GET list")
		w.Header().Set("Content-Type", "application/json")
		if s.listRefuse != "" {
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"error":"` + s.listRefuse + `"}`))
			return
		}
		_, _ = w.Write([]byte(s.list))
	})
	mux.HandleFunc("DELETE /v1/admin/api-tokens/{id}", func(w http.ResponseWriter, r *http.Request) {
		s.record("DELETE " + r.PathValue("id"))
		w.Header().Set("Content-Type", "application/json")
		if !strings.Contains(s.list, `"id":"`+r.PathValue("id")+`"`) {
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"error":"no admin API token with that id (strazactl api-token list)"}`))
			return
		}
		_, _ = w.Write([]byte(`{"id":"` + r.PathValue("id") + `","status":"revoked"}`))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func (s *tokensServer) record(req string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.requests = append(s.requests, req)
}

// TestAPITokenRevokeByNameOrID pins strazactl api-token revoke: an id goes to
// the revoke as it stands with no list read, a name is found in the list and
// revoked by its id, the confirmation says which of the two matched, and a
// name that matches no token or more than one revokes nothing.
func TestAPITokenRevokeByNameOrID(t *testing.T) {
	const (
		ciID    = "8026b17c-3568-4018-b817-065b8ccf8e84"
		twinA   = "1316c606-8b9f-4f14-b590-a74f83297674"
		twinB   = "7db165d8-00cb-4cb9-87e3-d19931d1a07f"
		strayID = "1ff50659-4aeb-45f7-b8ed-452840751869"
	)
	list := `[{"id":"` + ciID + `","name":"ci-pull","scope":"audit:read"},` +
		`{"id":"` + twinA + `","name":"twin","scope":"audit:read"},` +
		`{"id":"` + twinB + `","name":"twin","scope":"audit:read"}]`
	for _, tc := range []struct {
		name       string
		arg        string
		listRefuse string
		requests   []string
		stdout     string
		err        string
	}{
		{"an id", ciID, "", []string{"DELETE " + ciID},
			"revoked the admin API token " + ciID + ", matched by its id.\n", ""},
		{"an id, when the token may not read the list", ciID, "token lacks scope tokens:read", []string{"DELETE " + ciID},
			"revoked the admin API token " + ciID + ", matched by its id.\n", ""},
		{"a name", "ci-pull", "", []string{"GET list", "DELETE " + ciID},
			"revoked the admin API token named ci-pull, matched by its name. Its id was " + ciID + ".\n", ""},
		{"a name, when the token may not read the list", "ci-pull", "token lacks scope tokens:read", []string{"GET list"}, "",
			`strazactl reads the token list to find the token named "ci-pull", and reading it failed: token lacks scope tokens:read. ` +
				`If the token in STRAZA_API_TOKEN lacks tokens:read, revoke by the id instead, which needs tokens:write alone, ` +
				`or revoke the token in the console under Settings, API tokens`},
		{"a name, when the list refuses with a closed sentence", "ci-pull", "this admin API token lacks the scope tokens:read.", []string{"GET list"}, "",
			`strazactl reads the token list to find the token named "ci-pull", and reading it failed: this admin API token lacks the scope tokens:read. ` +
				`If the token in STRAZA_API_TOKEN lacks tokens:read, revoke by the id instead, which needs tokens:write alone, ` +
				`or revoke the token in the console under Settings, API tokens`},
		{"a name no token has", "nobody", "", []string{"GET list"}, "",
			`no admin API token is named "nobody". strazactl api-token list prints each token's name and id, and revoke takes either`},
		{"a name two tokens share", "twin", "", []string{"GET list"}, "",
			`2 admin API tokens are named "twin", so the name does not say which one to revoke. Revoke one by its id: ` + twinA + ", " + twinB},
		{"an id no token has", strayID, "", []string{"DELETE " + strayID}, "",
			"no admin API token with that id (strazactl api-token list)"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := &tokensServer{list: list, listRefuse: tc.listRefuse}
			credsPath := onToken(t, s.start(t).URL)
			stdout, _, err := runCLI(t, credsPath, "api-token", "revoke", tc.arg)
			if tc.err == "" && err != nil {
				t.Fatalf("revoke %s: %v", tc.arg, err)
			}
			if tc.err != "" && (err == nil || err.Error() != tc.err) {
				t.Fatalf("revoke %s: err = %v, want %q", tc.arg, err, tc.err)
			}
			if stdout != tc.stdout {
				t.Errorf("stdout = %q, want %q", stdout, tc.stdout)
			}
			if got := strings.Join(s.requests, "|"); got != strings.Join(tc.requests, "|") {
				t.Errorf("requests = %q, want %q", s.requests, tc.requests)
			}
		})
	}
}
