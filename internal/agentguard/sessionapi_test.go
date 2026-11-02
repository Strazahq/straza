package agentguard

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/strazahq/straza/internal/agentguard/localapi"
)

// TestSessionAPI pins what `straza connect` sends: strazad's /v1/connect
// paths under the service's /local prefix with the query and the escaped
// server name intact, the harness named so the service picks the caller's
// session, and no credential of its own. A refusal comes back as the
// sentence of whoever refused.
func TestSessionAPI(t *testing.T) {
	type seen struct{ method, path, query string }
	cases := []struct {
		name     string
		method   string
		path     string
		refuse   string // strazad's 403 sentence
		want     seen
		wantErr  string
		wantSent bool
	}{
		{name: "the list keeps its query", method: http.MethodGet, path: "/v1/connect?user=joe",
			want: seen{http.MethodGet, "/v1/connect", "user=joe"}, wantSent: true},
		{name: "a server keeps its escaped name", method: http.MethodDelete, path: "/v1/connect/a%20b",
			want: seen{http.MethodDelete, "/v1/connect/a b", ""}, wantSent: true},
		{name: "a refusal is the server's own sentence", method: http.MethodGet, path: "/v1/connect",
			refuse:  "only joe's sponsor or an administrator can manage joe's connections",
			want:    seen{http.MethodGet, "/v1/connect", ""},
			wantErr: "only joe's sponsor or an administrator can manage joe's connections", wantSent: true},
		{name: "a path outside connect is refused before anything is sent", method: http.MethodGet, path: "/v1/self",
			wantErr: "the Straza background service does not pass /v1/self to the server"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			svc := newFakeService(t)
			var got []seen
			svc.connect = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				got = append(got, seen{r.Method, r.URL.Path, r.URL.RawQuery})
				if tc.refuse != "" {
					w.WriteHeader(http.StatusForbidden)
					_ = json.NewEncoder(w).Encode(map[string]string{"error": tc.refuse})
					return
				}
				_, _ = w.Write([]byte(`[{"app":"midpoint","kind":"oauth"}]`))
			})
			var out []map[string]any
			err := NewSessionAPI().Do(context.Background(), tc.method, tc.path, nil, &out)
			if tc.wantErr != "" {
				if err == nil || err.Error() != tc.wantErr {
					t.Fatalf("err = %v, want %q", err, tc.wantErr)
				}
			} else if err != nil || len(out) != 1 {
				t.Fatalf("Do = %v, %v", out, err)
			}
			if !tc.wantSent {
				if len(got) != 0 {
					t.Fatalf("strazad got %+v, want nothing", got)
				}
				return
			}
			if len(got) != 1 || got[0] != tc.want {
				t.Fatalf("strazad got %+v, want one %+v", got, tc.want)
			}
			svc.mu.Lock()
			h := svc.proxied[0]
			svc.mu.Unlock()
			if h.Get(localapi.HeaderHarness) != "claude-code" || h.Get("Authorization") != "" {
				t.Errorf("headers = %v, want the harness named and no credential", h)
			}
		})
	}
}

// TestSessionAPINoService: with no background service, the caller gets the
// sentence that says how to start it, not a bare dial error.
func TestSessionAPINoService(t *testing.T) {
	noService(t)
	err := NewSessionAPI().Do(context.Background(), http.MethodGet, "/v1/connect", nil, nil)
	if err == nil || !strings.Contains(err.Error(), "isn't running") || !strings.Contains(err.Error(), "straza service") {
		t.Fatalf("err = %v, want the not-running sentence", err)
	}
}
