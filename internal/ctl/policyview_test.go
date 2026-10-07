package ctl

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// viewClient builds a client logged into srv with a throwaway credentials
// file, so tests never touch the developer's ~/.straza.
func viewClient(t *testing.T, srv *httptest.Server) *Client {
	t.Helper()
	c := NewClient(srv.URL)
	c.CredsPath = filepath.Join(t.TempDir(), "credentials.json")
	body := fmt.Sprintf(`{"server":%q,"session_token":"stok-1","session_id":"ses-1"}`, srv.URL)
	if err := os.WriteFile(c.CredsPath, []byte(body), 0o600); err != nil {
		t.Fatalf("write creds: %v", err)
	}
	return c
}

// viewMux is a fake strazad with the checkin lane every call walks (the
// test token is opaque, so the client always refreshes first).
func viewMux() *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/checkin", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"session_id":"ses-1","session_token":"stok-2"}`))
	})
	return mux
}

func TestPolicyByNameDecodesDetail(t *testing.T) {
	mux := viewMux()
	mux.HandleFunc("GET /v1/admin/policies/dev-guardrails", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"ps-1","name":"dev-guardrails","priority":150,"status":"active",` +
			`"yaml":"apiVersion: straza.dev/v1beta1\n","updated_at":"2026-08-24T12:02:31Z","drift":true,` +
			`"summary":{"name":"dev-guardrails","priority":150,"rules":12,` +
			`"postures":{"deny":2,"hold":4,"ticket":2,"allow":4},"matchRoles":["dev"],"capture":"verbatim"}}`))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	p, err := viewClient(t, srv).PolicyByName(context.Background(), "dev-guardrails")
	if err != nil {
		t.Fatalf("PolicyByName: %v", err)
	}
	if p.Name != "dev-guardrails" || p.Priority != 150 || p.Status != "active" {
		t.Errorf("detail = %+v", p)
	}
	if p.YAML != "apiVersion: straza.dev/v1beta1\n" {
		t.Errorf("yaml = %q, want the stored bytes verbatim", p.YAML)
	}
	if p.Drift == nil || !*p.Drift {
		t.Errorf("drift = %v, want explicit true", p.Drift)
	}
	if p.Summary == nil || p.Summary.Rules != 12 || p.Summary.Capture != "verbatim" ||
		len(p.Summary.MatchRoles) != 1 || p.Summary.MatchRoles[0] != "dev" {
		t.Errorf("summary = %+v", p.Summary)
	}
}

// Drift is tri-state on the wire: absent makes no claim, and only that
// distinction lets show refuse to render "no drift" it cannot know.
func TestPolicyByNameDriftAbsentVsFalse(t *testing.T) {
	tests := []struct {
		name    string
		body    string
		wantNil bool
		want    bool
	}{
		{"absent", `{"id":"a","name":"s","priority":1,"status":"active","yaml":"x"}`, true, false},
		{"explicit false", `{"id":"a","name":"s","priority":1,"status":"active","yaml":"x","drift":false}`, false, false},
		{"explicit true", `{"id":"a","name":"s","priority":1,"status":"active","yaml":"x","drift":true}`, false, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			mux := viewMux()
			mux.HandleFunc("GET /v1/admin/policies/s", func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(tc.body))
			})
			srv := httptest.NewServer(mux)
			t.Cleanup(srv.Close)
			p, err := viewClient(t, srv).PolicyByName(context.Background(), "s")
			if err != nil {
				t.Fatalf("PolicyByName: %v", err)
			}
			if (p.Drift == nil) != tc.wantNil {
				t.Fatalf("drift nil = %v, want %v", p.Drift == nil, tc.wantNil)
			}
			if p.Drift != nil && *p.Drift != tc.want {
				t.Fatalf("drift = %v, want %v", *p.Drift, tc.want)
			}
		})
	}
}

// A 404 from the by-name read is ambiguous (missing set, or a pre-0.78.0
// server without the route), so the list disambiguates before any claim.
func TestPolicyByNameSkew(t *testing.T) {
	const listWithName = `{"items":[{"id":"ps-1","name":"dev-guardrails","priority":1,"status":"active"}],"total":1,"limit":0,"offset":0,"roles":[]}`
	tests := []struct {
		name       string
		byNameCode int
		list       func(w http.ResponseWriter)
		want       string
	}{
		{"405 names the skew", http.StatusMethodNotAllowed, nil,
			"predates the 0.78.0 policy read by name"},
		{"404 with the name listed names the skew", http.StatusNotFound,
			func(w http.ResponseWriter) { _, _ = w.Write([]byte(listWithName)) },
			"predates the 0.78.0 policy read by name"},
		{"404 with the name absent is an honest missing", http.StatusNotFound,
			func(w http.ResponseWriter) {
				_, _ = w.Write([]byte(`{"items":[],"total":0,"limit":0,"offset":0,"roles":[]}`))
			},
			`no policy set named "dev-guardrails" on this server (the list was checked, so this is not version skew)`},
		{"404 with a bare-array list propagates the envelope skew", http.StatusNotFound,
			func(w http.ResponseWriter) { _, _ = w.Write([]byte(`[]`)) },
			"policy-list envelope"},
		{"404 with a failing list says it could not check", http.StatusNotFound,
			func(w http.ResponseWriter) {
				w.WriteHeader(http.StatusInternalServerError)
				_, _ = w.Write([]byte(`{"error":"boom"}`))
			},
			"the list could not be checked"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			mux := viewMux()
			mux.HandleFunc("GET /v1/admin/policies/dev-guardrails", func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(tc.byNameCode)
				_, _ = w.Write([]byte(`{"error":"no such policy set"}`))
			})
			if tc.list != nil {
				mux.HandleFunc("GET /v1/admin/policies", func(w http.ResponseWriter, _ *http.Request) {
					w.Header().Set("Content-Type", "application/json")
					tc.list(w)
				})
			}
			srv := httptest.NewServer(mux)
			t.Cleanup(srv.Close)
			_, err := viewClient(t, srv).PolicyByName(context.Background(), "dev-guardrails")
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want it to contain %q", err, tc.want)
			}
		})
	}
}

// Other error codes surface the server's own error string, like Do.
func TestPolicyByNameServerError(t *testing.T) {
	mux := viewMux()
	mux.HandleFunc("GET /v1/admin/policies/s", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"error":"insufficient scope"}`))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	_, err := viewClient(t, srv).PolicyByName(context.Background(), "s")
	if err == nil || err.Error() != "insufficient scope" {
		t.Fatalf("err = %v, want the server's own words", err)
	}
}

// Set names are operator input and land in a URL path segment.
func TestPolicyByNameEscapesName(t *testing.T) {
	var gotPath string
	mux := viewMux()
	mux.HandleFunc("GET /v1/admin/policies/", func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.EscapedPath()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"a","name":"my set","priority":1,"status":"draft","yaml":"x"}`))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	if _, err := viewClient(t, srv).PolicyByName(context.Background(), "my set"); err != nil {
		t.Fatalf("PolicyByName: %v", err)
	}
	if gotPath != "/v1/admin/policies/my%20set" {
		t.Fatalf("path = %q, want the escaped name", gotPath)
	}
}
