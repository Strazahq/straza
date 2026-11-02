package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestCreateRoutesWordABadBody pins the 400 answers of three admin create
// routes, which return before any store read: a body that is not valid JSON
// names the route's object and the decoder's complaint, an expires_in sent
// as text asks for seconds, and only a missing name says "name is required".
func TestCreateRoutesWordABadBody(t *testing.T) {
	a := &App{}
	rows := []struct {
		name    string
		handler http.HandlerFunc
		body    string
		want    string
	}{
		{"token expiry as text", a.handleAPITokenCreate, `{"name":"ci","scope":"audit:read","expires_in":"1h"}`,
			"expires_in must be a whole number of seconds, such as 3600 for one hour, or 0 for a token that never expires."},
		{"token name as a number", a.handleAPITokenCreate, `{"name":7}`, "The request body is not valid JSON for an API token: "},
		{"token cut short", a.handleAPITokenCreate, `{"name":`, "The request body is not valid JSON for an API token: "},
		{"token without a name", a.handleAPITokenCreate, `{"scope":"audit:read","expires_in":3600}`, "name is required"},
		{"pack cut short", a.handlePacksCreate, `{"name":`, "The request body is not valid JSON for a pack: "},
		{"pack without a name", a.handlePacksCreate, `{}`, "name is required"},
		{"role cut short", a.handleRolesCreate, `{"name":`, "The request body is not valid JSON for a role: "},
		{"role without a name", a.handleRolesCreate, `{}`, "name is required"},
	}
	for _, row := range rows {
		t.Run(row.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			row.handler(w, httptest.NewRequest(http.MethodPost, "/", strings.NewReader(row.body)))
			var got struct {
				Error string `json:"error"`
			}
			if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil || w.Code != http.StatusBadRequest {
				t.Fatalf("answer = %d %q, want a 400 with an error", w.Code, w.Body.String())
			}
			prefix := strings.HasSuffix(row.want, ": ")
			if got.Error != row.want && (!prefix || !strings.HasPrefix(got.Error, row.want)) {
				t.Errorf("error = %q, want %q", got.Error, row.want)
			}
		})
	}
}
