package agentguard

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// connectAPIStub is strazad as the caller's API client sees it: a check-in
// endpoint that counts mints and refreshes, and GET /v1/connect, which
// accepts one bearer at a time and records the one it served.
type connectAPIStub struct {
	*httptest.Server
	mu        sync.Mutex
	token     string
	refuse    string // a 403 sentence for every authorized call
	served    []string
	checkins  atomic.Int64
	refreshes atomic.Int64
}

func newConnectAPIStub(t *testing.T, token string) *connectAPIStub {
	t.Helper()
	s := &connectAPIStub{token: token}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/checkin", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body["device_token"] != nil {
			s.checkins.Add(1)
		} else {
			s.refreshes.Add(1)
		}
		s.mu.Lock()
		s.token = "tok-renewed"
		s.mu.Unlock()
		_ = json.NewEncoder(w).Encode(map[string]any{
			"session": "s-1", "session_token": "tok-renewed", "expires_in": 300,
			"user": "bob", "roles": []string{"dev"}, "snapshot": "snap-1", "attestation": "advisory",
		})
	})
	mux.HandleFunc("GET /v1/connect", func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		defer s.mu.Unlock()
		got := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		if got != s.token {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		s.served = append(s.served, got)
		if s.refuse != "" {
			w.WriteHeader(http.StatusForbidden)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": s.refuse})
			return
		}
		_, _ = w.Write([]byte(`[{"app":"midpoint","kind":"oauth"}]`))
	})
	s.Server = httptest.NewServer(mux)
	t.Cleanup(s.Close)
	return s
}

// TestSessionAPI pins what the caller's API client does with the session on
// disk. The first row is the one that matters most: a running harness keeps
// its session, because a live one is adopted and nothing is minted.
func TestSessionAPI(t *testing.T) {
	cases := []struct {
		name          string
		onDisk        string // the session token on disk; empty means no session
		serverToken   string
		refuse        string
		wantServed    string
		wantCheckins  int64
		wantRefreshes int64
		wantErr       string
	}{
		{name: "a live session is adopted and nothing is minted", onDisk: "tok-live", serverToken: "tok-live", wantServed: "tok-live"},
		{name: "a refused token spends one refresh and one retry", onDisk: "tok-stale", serverToken: "tok-other", wantServed: "tok-renewed", wantRefreshes: 1},
		{name: "no session mints one through the enrolled identity", serverToken: "tok-other", wantServed: "tok-renewed", wantCheckins: 1},
		{name: "a refusal is the server's own sentence", onDisk: "tok-live", serverToken: "tok-live", wantServed: "tok-live",
			refuse:  "only joe's sponsor or an administrator can manage joe's connections",
			wantErr: "only joe's sponsor or an administrator can manage joe's connections"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := newConnectAPIStub(t, tc.serverToken)
			s.refuse = tc.refuse
			store := proxyTestStore(t, s.URL)
			if tc.onDisk != "" {
				if err := store.SaveSession(Session{
					SessionID: "existing", SessionToken: tc.onDisk, Harness: "claude-code/2.1", User: "bob",
					IssuedAt: time.Now(), ExpiresAt: time.Now().Add(time.Hour),
				}); err != nil {
					t.Fatal(err)
				}
			}
			api, err := NewSessionAPI(store)
			if err != nil {
				t.Fatal(err)
			}
			var out []map[string]any
			err = api.Do(context.Background(), http.MethodGet, "/v1/connect", nil, &out)
			if tc.wantErr != "" {
				if err == nil || err.Error() != tc.wantErr {
					t.Fatalf("err = %v, want %q", err, tc.wantErr)
				}
			} else if err != nil || len(out) != 1 {
				t.Fatalf("Do = %v, %v", out, err)
			}
			if got := s.checkins.Load(); got != tc.wantCheckins {
				t.Errorf("checkins = %d, want %d", got, tc.wantCheckins)
			}
			if got := s.refreshes.Load(); got != tc.wantRefreshes {
				t.Errorf("refreshes = %d, want %d", got, tc.wantRefreshes)
			}
			if len(s.served) != 1 || s.served[0] != tc.wantServed {
				t.Errorf("served bearers = %v, want one %q", s.served, tc.wantServed)
			}
		})
	}
}

// TestSessionAPINotEnrolled: a machine with no enrollment gets the next step
// named, not a bare file error.
func TestSessionAPINotEnrolled(t *testing.T) {
	t.Setenv("STRAZA_HOME", t.TempDir())
	store, err := OpenStore()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := NewSessionAPI(store); err == nil || !strings.Contains(err.Error(), "not enrolled (run `straza enroll`)") {
		t.Fatalf("err = %v", err)
	}
}
