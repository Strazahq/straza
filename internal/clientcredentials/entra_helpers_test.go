package clientcredentials

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// The example ids of the fixtures in testdata, and a resource scope.
const (
	testTenant    = "aaaaaaaa-1111-4111-8111-aaaaaaaaaaaa"
	testBlueprint = "bbbbbbbb-2222-4222-8222-bbbbbbbbbbbb"
	testAgentID   = "cccccccc-3333-4333-8333-cccccccccccc"
	testTraceID   = "dddddddd-4444-4444-8444-dddddddddddd"
	testCorrID    = "eeeeeeee-5555-4555-8555-eeeeeeeeeeee"
	testResource  = "api://ffffffff-6666-4666-8666-ffffffffffff/.default"
	otherAgentID  = "99999999-7777-4777-8777-999999999999"
)

// entraStep names the two grants of the exchange in the double.
const (
	stepBlueprint = 1
	stepAgent     = 2
)

// entra is a token endpoint double of the Entra agent identity exchange. The
// first step answers an exchange token for the fmi_path it was asked, shaped
// like a JWT so a leak test finds it. The second step accepts only an
// exchange token the first step issued for the agent identity that asks, as
// Entra does, and answers a token that lives 3599 seconds. A scripted fixture
// or answer refuses a step instead.
type entra struct {
	t      *testing.T
	srv    *httptest.Server
	mu     sync.Mutex
	forms  []url.Values
	issued map[string]string // exchange token to the fmi_path it was issued for
	fail   map[int]http.HandlerFunc
	n      int
}

func newEntra(t *testing.T) *entra {
	t.Helper()
	e := &entra{t: t, issued: map[string]string{}, fail: map[int]http.HandlerFunc{}}
	e.srv = httptest.NewServer(http.HandlerFunc(e.serve))
	t.Cleanup(e.srv.Close)
	return e
}

func (e *entra) tokenURL() string { return e.srv.URL + "/" + testTenant + "/oauth2/v2.0/token" }

func (e *entra) serve(w http.ResponseWriter, r *http.Request) {
	_ = r.ParseForm()
	step := stepAgent
	if r.PostForm.Get("scope") == entraExchangeScope {
		step = stepBlueprint
	}
	e.mu.Lock()
	e.forms = append(e.forms, r.PostForm)
	e.n++
	n := e.n
	h := e.fail[step]
	e.mu.Unlock()
	if r.URL.Path != "/"+testTenant+"/oauth2/v2.0/token" {
		e.t.Errorf("the exchange posted to %s, want the tenant's token endpoint", r.URL.Path)
	}
	if h != nil {
		h(w, r)
		return
	}
	f := r.PostForm
	if step == stepBlueprint {
		fmi := f.Get("fmi_path")
		t1 := fmt.Sprintf("eyJ0MSJ9.t1-%s-%d.c2ln", fmi, n)
		e.mu.Lock()
		e.issued[t1] = fmi
		e.mu.Unlock()
		answer(w, http.StatusOK, map[string]any{"access_token": t1, "token_type": "Bearer", "expires_in": 3599})
		return
	}
	e.mu.Lock()
	fmi, ok := e.issued[f.Get("client_assertion")]
	e.mu.Unlock()
	if !ok || fmi != f.Get("client_id") {
		answer(w, http.StatusUnauthorized, map[string]any{"error": "invalid_client", "error_codes": []int{7000215}})
		return
	}
	answer(w, http.StatusOK, map[string]any{"access_token": fmt.Sprintf("entra-tok-%s-%d", fmi, n), "token_type": "Bearer", "expires_in": 3599})
}

// refuse makes step answer with the fixture file and the status Entra sends
// with its error code: 401 for invalid_client and 400 for every other.
func (e *entra) refuse(step int, fixture string) {
	e.t.Helper()
	body, err := os.ReadFile(filepath.Join("testdata", fixture))
	if err != nil {
		e.t.Fatal(err)
	}
	status := http.StatusBadRequest
	if strings.Contains(string(body), `"invalid_client"`) {
		status = http.StatusUnauthorized
	}
	e.answerWith(step, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write(body)
	})
}

func (e *entra) answerWith(step int, h http.HandlerFunc) {
	e.mu.Lock()
	e.fail[step] = h
	e.mu.Unlock()
}

// asked returns the forms of every request in order.
func (e *entra) asked() []url.Values {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]url.Values(nil), e.forms...)
}

// entraRig is a rig whose provider entra-lab runs the Entra exchange, beside
// the plain keycloak provider of newRig.
type entraRig struct {
	*rig
	entra *entra
}

func newEntraRig(t *testing.T) *entraRig {
	t.Helper()
	r := &entraRig{rig: &rig{signer: &fakeSigner{}, clock: newClock(), idp: newProvider(t), logs: &syncBuffer{}}, entra: newEntra(t)}
	r.tokens = New(Options{
		Providers: map[string]Provider{
			"entra-lab": {TokenURL: r.entra.tokenURL(), Blueprint: testBlueprint, Tenant: testTenant, Scopes: []string{testResource}},
			"keycloak":  {TokenURL: r.idp.srv.URL + "/token", Audience: testAudience},
		},
		Signer:  r.signer,
		KeysURL: testKeysURL,
		Log:     slog.New(slog.NewJSONHandler(r.logs, &slog.HandlerOptions{Level: slog.LevelDebug})),
		Now:     r.clock.now,
	})
	return r
}

// entraSam is sam's request on the Entra provider, with the Entra agent
// identity his check-in read.
func entraSam() Request {
	return Request{Provider: "entra-lab", Server: "crm", ServerID: "app-2", UserID: "u-sam", ClientID: "sam-sre-agent", EntraAgentID: testAgentID, Session: "ses-sam"}
}

// logLines decodes the JSON log lines of the rig.
func logLines(t *testing.T, logs string) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(logs), "\n") {
		if line == "" {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			t.Fatalf("a log line is not JSON: %q", line)
		}
		out = append(out, m)
	}
	return out
}
