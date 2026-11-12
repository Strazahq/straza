package scim

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestWriteRefusedReportsOnce pins when requireToken reports a refusal: once
// per write answered with a 4xx or a 501, carrying the first error the
// handler wrote, the id the route names and the name a handler noted, and
// never for a success, a 5xx failure or a read.
func TestWriteRefusedReportsOnce(t *testing.T) {
	cases := []struct {
		name    string
		method  string
		id      string
		handler http.HandlerFunc
		want    *Refusal
	}{
		{
			name: "a refused create names the requested userName", method: http.MethodPost,
			handler: func(w http.ResponseWriter, _ *http.Request) {
				noteUser(w, "ada")
				writeError(w, http.StatusConflict, "uniqueness", "userName or externalId already exists")
			},
			want: &Refusal{Status: http.StatusConflict, SCIMType: "uniqueness", Detail: "userName or externalId already exists", Username: "ada"},
		},
		{
			name: "a refused PATCH names the route's id and the stored user", method: http.MethodPatch, id: "u-1",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				noteUser(w, "ada")
				writeError(w, http.StatusBadRequest, "mutability", "read-only")
			},
			want: &Refusal{Status: http.StatusBadRequest, SCIMType: "mutability", Detail: "read-only", Target: "u-1", Username: "ada"},
		},
		{
			name: "a 404 names only the route's id", method: http.MethodPut, id: "u-404",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				writeError(w, http.StatusNotFound, "", "no such user")
			},
			want: &Refusal{Status: http.StatusNotFound, Detail: "no such user", Target: "u-404"},
		},
		{
			name: "a refused group write names the role", method: http.MethodPatch, id: "r-1",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				noteRole(w, "deployers")
				writeError(w, http.StatusBadRequest, "invalidValue", "bad member")
			},
			want: &Refusal{Status: http.StatusBadRequest, SCIMType: "invalidValue", Detail: "bad member", Target: "r-1", Role: "deployers"},
		},
		{
			name: "a 501 is a refusal", method: http.MethodDelete, id: "r-1",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				writeError(w, http.StatusNotImplemented, "", "roles are born in Straza")
			},
			want: &Refusal{Status: http.StatusNotImplemented, Detail: "roles are born in Straza", Target: "r-1"},
		},
		{
			name: "only the first error counts", method: http.MethodPost,
			handler: func(w http.ResponseWriter, _ *http.Request) {
				writeError(w, http.StatusConflict, "uniqueness", "first")
				writeError(w, http.StatusBadRequest, "invalidValue", "second")
			},
			want: &Refusal{Status: http.StatusConflict, SCIMType: "uniqueness", Detail: "first"},
		},
		{
			name: "a success reports nothing", method: http.MethodPost,
			handler: func(w http.ResponseWriter, _ *http.Request) {
				noteUser(w, "ada")
				writeSCIM(w, http.StatusCreated, map[string]any{"id": "u-1"})
			},
		},
		{
			name: "a 500 is a failure, not a refusal", method: http.MethodPatch, id: "u-1",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				writeError(w, http.StatusInternalServerError, "", "update failed")
			},
		},
		{
			name: "a refused read reports nothing", method: http.MethodGet,
			handler: func(w http.ResponseWriter, _ *http.Request) {
				writeError(w, http.StatusBadRequest, "invalidFilter", "unsupported filter")
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var got []Refusal
			s := New(Deps{
				Authenticate: func(r *http.Request, _ string) (context.Context, int, string) { return r.Context(), 0, "" },
				WriteRefused: func(_ *http.Request, ref Refusal) { got = append(got, ref) },
			})
			req := httptest.NewRequest(tc.method, "/scim/v2/Users", nil)
			req.Header.Set("Authorization", "Bearer wat_x")
			if tc.id != "" {
				req.SetPathValue("id", tc.id)
			}
			s.requireToken(tc.handler)(httptest.NewRecorder(), req)
			switch {
			case tc.want == nil && len(got) != 0:
				t.Fatalf("reported %+v, want nothing", got)
			case tc.want != nil && (len(got) != 1 || got[0] != *tc.want):
				t.Fatalf("reported %+v, want exactly %+v", got, *tc.want)
			}
		})
	}
}

// TestWriteRefusedWithoutConsumer pins that a server built without the seam
// still answers a refused write.
func TestWriteRefusedWithoutConsumer(t *testing.T) {
	s := New(Deps{
		Authenticate: func(r *http.Request, _ string) (context.Context, int, string) { return r.Context(), 0, "" },
	})
	req := httptest.NewRequest(http.MethodPost, "/scim/v2/Users", nil)
	req.Header.Set("Authorization", "Bearer wat_x")
	rec := httptest.NewRecorder()
	s.requireToken(func(w http.ResponseWriter, _ *http.Request) {
		writeError(w, http.StatusBadRequest, "invalidValue", "userName is required")
	})(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status %d, want 400", rec.Code)
	}
}
