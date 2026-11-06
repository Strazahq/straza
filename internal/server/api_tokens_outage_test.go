package server

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/strazahq/straza/internal/scim"
	"github.com/strazahq/straza/internal/store"
)

// faultSettings fails the read of an admin API token row while
// "api-token-read" is armed. Every other settings key reads as stored.
type faultSettings struct {
	store.SettingsRepo
	f *faultStore
}

func (s faultSettings) Get(ctx context.Context, key string) (string, error) {
	if strings.HasPrefix(key, apiTokenKeyPrefix) {
		if err := s.f.fault("api-token-read"); err != nil {
			return "", err
		}
	}
	return s.SettingsRepo.Get(ctx, key)
}

func (f *faultStore) Settings() store.SettingsRepo { return faultSettings{f.Store.Settings(), f} }

// storeAPIToken writes the row of an admin API token as a mint does, with
// the scope full and the given expiry, and answers the token.
func storeAPIToken(t *testing.T, app *App, token string, expires *time.Time) string {
	t.Helper()
	sum := sha256.Sum256([]byte(token))
	meta, _ := json.Marshal(apiTokenMeta{ID: "tok-" + token, Name: token, Scope: "full", Created: time.Now().UTC(), Expires: expires})
	if err := app.store.Settings().Set(context.Background(), apiTokenKeyPrefix+hex.EncodeToString(sum[:]), string(meta)); err != nil {
		t.Fatal(err)
	}
	return token
}

// tokenGet sends a GET on bearer under the request id id and answers the
// status, the headers and the raw body.
func tokenGet(t *testing.T, url, bearer, id string) (int, http.Header, []byte) {
	t.Helper()
	return tokenDo(t, http.MethodGet, url, bearer, id, "")
}

// tokenDo is tokenGet for any method, with body sent as SCIM JSON when it is
// not empty.
func tokenDo(t *testing.T, method, url, bearer, id, body string) (int, http.Header, []byte) {
	t.Helper()
	var rd io.Reader
	if body != "" {
		rd = strings.NewReader(body)
	}
	req, err := http.NewRequest(method, url, rd)
	if err != nil {
		t.Fatal(err)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/scim+json")
	}
	req.Header.Set("Authorization", "Bearer "+bearer)
	req.Header.Set("X-Request-Id", id)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return resp.StatusCode, resp.Header, raw
}

// auditRecordCount counts the straza.audit records in the outbox.
func auditRecordCount(t *testing.T, app *App) int {
	t.Helper()
	rows, err := app.store.Outbox().ListRecent(context.Background(), 1000)
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, row := range rows {
		if strings.HasPrefix(row.Subject, "straza.audit.") {
			n++
		}
	}
	return n
}

// TestAPITokenStoreOutage pins the answer an admin API token gets when
// Straza cannot read the token from its store, on the admin plane, on the
// connect routes and on the SCIM plane. The answer is 503 with the outage
// sentence, and never the 401 that sends the operator to mint a new token.
// It depends only on the failed read: a real token, a forged one and an
// expired one get the same status, headers and body bytes, so the outage
// is no oracle for whether a token exists. Each answer writes exactly one
// Error record with the cause and counts one 503, as the other outage
// answers do, and writes no audit record, as a rejected token writes none.
// Once the store answers again, the real token passes and the forged one
// gets the rejected-token sentence.
func TestAPITokenStoreOutage(t *testing.T) {
	t.Parallel()
	log, buf := captureLogger()
	app, base, fs := testAppFaultLog(t, log)
	seedIdentity(t, app)
	past := time.Now().Add(-time.Hour).UTC()
	realToken := storeAPIToken(t, app, apiTokenPrefix+"real-fixture", nil)
	tokens := []struct{ name, token string }{
		{"a real token", realToken},
		{"a forged token", apiTokenPrefix + "forged-fixture"},
		{"an expired token", storeAPIToken(t, app, apiTokenPrefix+"expired-fixture", &past)},
	}
	planes := []struct {
		name, path, route, contentType string
		retryAfter                     string
		body                           func(id string) map[string]any
	}{
		{"the admin plane", "/v1/admin/users", "GET /v1/admin/users", "application/json", loginOutageRetryAfter, func(id string) map[string]any {
			return map[string]any{"error": fmt.Sprintf(apiTokenOutageMsg, id), "correlation_id": id}
		}},
		{"the connect routes", "/v1/connect?user=kim", "GET /v1/connect", "application/json", loginOutageRetryAfter, func(id string) map[string]any {
			return map[string]any{"error": fmt.Sprintf(apiTokenOutageMsg, id), "correlation_id": id}
		}},
		{"the SCIM plane", "/scim/v2/Users", "GET /scim/v2/Users", "application/scim+json", "", func(id string) map[string]any {
			return map[string]any{"schemas": []any{scim.SchemaError}, "status": "503", "detail": fmt.Sprintf(apiTokenOutageMsg, id)}
		}},
	}
	faults := []struct {
		name string
		err  error
	}{
		{"a Postgres outage", pgDown},
		{"a store error of no known shape while the store pings", errors.New("boom")},
	}
	audit := auditRecordCount(t, app)
	for fi, fault := range faults {
		for pi, plane := range planes {
			t.Run(fault.name+" on "+plane.name, func(t *testing.T) {
				id := fmt.Sprintf("token-outage-%d-%d", fi, pi)
				before := httpErrorCounter(t, app, plane.route, "503")
				fs.arm("api-token-read", fault.err)
				defer fs.disarm()
				var first []byte
				for _, tok := range tokens {
					buf.Reset()
					code, hdr, body := tokenGet(t, base+plane.path, tok.token, id)
					if code != http.StatusServiceUnavailable {
						t.Fatalf("%s: status = %d %s, want 503", tok.name, code, body)
					}
					var got map[string]any
					if err := json.Unmarshal(body, &got); err != nil {
						t.Fatalf("%s: body %s: %v", tok.name, body, err)
					}
					if want := plane.body(id); !reflect.DeepEqual(got, want) {
						t.Errorf("%s: body = %v, want %v", tok.name, got, want)
					}
					for _, leak := range []string{"57P03", "starting up", "boom"} {
						if strings.Contains(string(body), leak) {
							t.Errorf("%s: the body carries the cause %q: %s", tok.name, leak, body)
						}
					}
					if ct := hdr.Get("Content-Type"); ct != plane.contentType {
						t.Errorf("%s: Content-Type = %q, want %q", tok.name, ct, plane.contentType)
					}
					if ra := hdr.Get("Retry-After"); ra != plane.retryAfter {
						t.Errorf("%s: Retry-After = %q, want %q", tok.name, ra, plane.retryAfter)
					}
					rec := assertOneErrorWithCorrelation(t, buf, http.StatusServiceUnavailable, id)
					if !strings.Contains(rec, "cause=") {
						t.Errorf("%s: the Error record names no cause: %s", tok.name, rec)
					}
					if first == nil {
						first = body
					} else if string(body) != string(first) {
						t.Errorf("%s: body = %s, want the bytes the real token got: %s", tok.name, body, first)
					}
				}
				if got := httpErrorCounter(t, app, plane.route, "503") - before; got != float64(len(tokens)) {
					t.Errorf("straza_http_errors_total{%s,503} rose by %v, want %d", plane.route, got, len(tokens))
				}
			})
		}
	}
	if got := auditRecordCount(t, app); got != audit {
		t.Errorf("audit records = %d after the outage answers, want %d: an outage writes none", got, audit)
	}

	for _, plane := range planes {
		t.Run("the store answers again on "+plane.name, func(t *testing.T) {
			if code, _, body := tokenGet(t, base+plane.path, realToken, "token-healthy"); code != http.StatusOK {
				t.Errorf("the real token = %d %s, want 200", code, body)
			}
			code, _, body := tokenGet(t, base+plane.path, tokens[1].token, "token-healthy")
			if code != http.StatusUnauthorized || !strings.Contains(string(body), "admin API token rejected: this token is unknown, expired or revoked.") {
				t.Errorf("the forged token = %d %s, want 401 with the rejected-token sentence", code, body)
			}
		})
	}
}

// TestSCIMTokenReadPastWriteBound pins a SCIM write whose token read ends
// after the write's bound. scimHandler answers the bound's 503, and the
// request leaves exactly one Error record and one 503 count, for a real
// token and a forged one alike, with the same body bytes. It shrinks
// adminWriteBound, so it does not run in parallel.
func TestSCIMTokenReadPastWriteBound(t *testing.T) {
	log, buf := captureLogger()
	app, base, _ := testAppFaultLog(t, log)
	realToken := storeAPIToken(t, app, apiTokenPrefix+"bound-real", nil)
	old := adminWriteBound
	adminWriteBound = time.Nanosecond
	t.Cleanup(func() { adminWriteBound = old })
	const route = "POST /scim/v2/Users"
	var first []byte
	for _, tc := range []struct{ name, token string }{
		{"a real token", realToken},
		{"a forged token", apiTokenPrefix + "bound-forged"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			buf.Reset()
			before := httpErrorCounter(t, app, route, "503")
			code, _, body := tokenDo(t, http.MethodPost, base+"/scim/v2/Users", tc.token, "token-past-bound",
				`{"schemas":["urn:ietf:params:scim:schemas:core:2.0:User"],"userName":"bound@x.io"}`)
			if code != http.StatusServiceUnavailable {
				t.Fatalf("status = %d %s, want 503", code, body)
			}
			var got map[string]any
			if err := json.Unmarshal(body, &got); err != nil {
				t.Fatalf("body %s: %v", body, err)
			}
			if got["status"] != "503" || !strings.Contains(fmt.Sprint(got["detail"]), "did not finish within") {
				t.Errorf("body = %s, want the bound's SCIM 503", body)
			}
			rec := assertOneErrorWithCorrelation(t, buf, http.StatusServiceUnavailable, "token-past-bound")
			if !strings.Contains(rec, "answered 503") {
				t.Errorf("the Error record does not name the held-back 503: %s", rec)
			}
			if got := httpErrorCounter(t, app, route, "503") - before; got != 1 {
				t.Errorf("straza_http_errors_total{%s,503} rose by %v, want 1", route, got)
			}
			if first == nil {
				first = body
			} else if string(body) != string(first) {
				t.Errorf("body = %s, want the bytes the real token got: %s", body, first)
			}
		})
	}
}
