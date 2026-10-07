package agentguard

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// gatedRefusal stands in for the enterprise server's 401 sentence.
const gatedRefusal = "The enterprise profile serves the policy snapshot only to a checked-in session."

// tokenGatedServer fakes an enterprise strazad for the snapshot fetch lanes.
// Every check-in mints a new session token and retires the one before it,
// and /v1/snapshot answers only the bearer of the live token, refusing any
// other with a JSON sentence before the 304 the way the real gate does.
type tokenGatedServer struct {
	blob          []byte
	id            string
	refreshStatus int    // a session-token refresh answers this status when set
	refuseAll     bool   // /v1/snapshot refuses every request
	failStatus    int    // /v1/snapshot answers every request with this status when set
	dropSnapshot  bool   // /v1/snapshot closes the connection without an answer
	redirectTo    string // /v1/snapshot answers a 302 to this address when set

	mu      sync.Mutex
	live    string
	minted  int
	served  int
	refused int
}

func (g *tokenGatedServer) handler(t *testing.T) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v1/checkin":
			var body struct {
				SessionToken string `json:"session_token"`
				DeviceToken  string `json:"device_token"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			if body.SessionToken == "" && body.DeviceToken == "" {
				t.Errorf("checkin with neither session_token nor device_token")
			}
			g.mu.Lock()
			refreshStatus := g.refreshStatus
			g.mu.Unlock()
			if body.SessionToken != "" && refreshStatus != 0 {
				w.WriteHeader(refreshStatus)
				_, _ = w.Write([]byte(`{"error":"session token rejected"}`))
				return
			}
			g.mu.Lock()
			g.minted++
			g.live = fmt.Sprintf("tok-%d", g.minted)
			tok := g.live
			g.mu.Unlock()
			_, _ = fmt.Fprintf(w, `{"session_id":"s-%s","session_token":%q,"expires_in":300,"user":"kim","roles":["dev"],"snapshot_id":%q}`,
				tok, tok, g.id)
		case "/v1/snapshot":
			g.mu.Lock()
			failStatus, drop, redirectTo := g.failStatus, g.dropSnapshot, g.redirectTo
			g.mu.Unlock()
			if redirectTo != "" {
				http.Redirect(w, r, redirectTo, http.StatusFound)
				return
			}
			if drop {
				if conn, _, err := w.(http.Hijacker).Hijack(); err == nil {
					_ = conn.Close()
				}
				return
			}
			if failStatus != 0 {
				// A 5xx carries a reason and anything below answers bare.
				w.WriteHeader(failStatus)
				if failStatus >= http.StatusInternalServerError {
					_, _ = w.Write([]byte(`{"error":"snapshot store unavailable"}`))
				}
				return
			}
			g.mu.Lock()
			ok := !g.refuseAll && g.live != "" && r.Header.Get("Authorization") == "Bearer "+g.live
			if ok {
				g.served++
			} else {
				g.refused++
			}
			g.mu.Unlock()
			if !ok {
				w.WriteHeader(http.StatusUnauthorized)
				_, _ = w.Write([]byte(`{"error":"` + gatedRefusal + `"}`))
				return
			}
			if strings.Trim(r.Header.Get("If-None-Match"), `"`) == g.id {
				w.WriteHeader(http.StatusNotModified)
				return
			}
			w.Header().Set("Content-Type", "application/cbor")
			w.Header().Set("X-Straza-Snapshot-Id", g.id)
			_, _ = w.Write(g.blob)
		case "/v1/audit/batch":
			_, _ = w.Write([]byte(`{"accepted":0}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}
}

// snapshotLane is one place the client fetches the policy snapshot.
type snapshotLane struct {
	name string
	// seed is the lifetime left on the session.json the lane starts from;
	// zero means the lane starts with no session at all.
	seed          time.Duration
	refreshStatus int
	// says is where a refusal's sentence reaches a person: "error" for the
	// returned error, "log" for the daemon's output, "" for a silent lane.
	says string
	run  func(ctx context.Context, t *testing.T, store *Store, serverURL string, out io.Writer) error
}

// hookRefresh runs the hook lane's renewal the way liveDecider does.
func hookRefresh(ctx context.Context, t *testing.T, store *Store, _ string, _ io.Writer) error {
	t.Helper()
	ses, err := store.LoadSession()
	if err != nil {
		return err
	}
	pdp, err := NewLocalPDP(store, ses.Subject())
	if err != nil {
		return err
	}
	pdp.RefreshIfStale(ctx, store)
	return nil
}

// daemonTick runs one daemon poll tick.
func daemonTick(_ context.Context, t *testing.T, store *Store, _ string, out io.Writer) error {
	t.Helper()
	d := NewDaemon(store, out)
	if gone := d.refreshOnce(t); gone {
		return fmt.Errorf("the daemon dropped the session")
	}
	return nil
}

// TestSnapshotFetchSendsSessionToken pins that every lane that fetches the
// policy snapshot sends the session token it holds at that moment, which the
// enterprise profile requires, so a new client never falls back to the
// policy it cached. Each lane runs against a server that honours only the
// newest token it minted. The refusal rows pin what each lane does when the
// server says no: it keeps only the verified blob it already holds, and a
// lane with a surface shows the server's sentence rather than a bare status.
func TestSnapshotFetchSendsSessionToken(t *testing.T) {
	priv, keys := testSnapshotKey(t)
	blobA, idA := testSignedPolicy(t, priv, pinPolicyA)
	blobB, idB := testSignedPolicy(t, priv, pinPolicyB)

	lanes := []snapshotLane{
		{name: "session start", says: "error",
			run: func(ctx context.Context, _ *testing.T, store *Store, _ string, _ io.Writer) error {
				_, _, err := SessionStart(ctx, store, "claude-code", "2.1.0")
				return err
			}},
		{name: "hook refresh", seed: 30 * time.Second, run: hookRefresh},
		{name: "hook re-acquire", seed: -10 * time.Hour, refreshStatus: http.StatusUnauthorized, run: hookRefresh},
		{name: "daemon poll refresh", seed: time.Hour, says: "log", run: daemonTick},
		{name: "daemon re-acquire", seed: time.Hour, refreshStatus: http.StatusUnauthorized, run: daemonTick},
		{name: "MCP proxy and exec mint",
			run: func(ctx context.Context, _ *testing.T, store *Store, serverURL string, _ io.Writer) error {
				_, err := ensureSession(ctx, store, NewClient(serverURL), "claude-code")
				return err
			}},
		{name: "drain pulse", seed: time.Hour,
			run: func(context.Context, *testing.T, *Store, string, io.Writer) error {
				_, err := DrainOnce(5 * time.Second)
				return err
			}},
	}

	for _, lane := range lanes {
		for _, refuse := range []bool{false, true} {
			name := lane.name + ", server serves the live token"
			if refuse {
				name = lane.name + ", server refuses"
			}
			t.Run(name, func(t *testing.T) {
				g := &tokenGatedServer{blob: blobB, id: idB, refreshStatus: lane.refreshStatus, refuseAll: refuse}
				srv := httptest.NewServer(g.handler(t))
				defer srv.Close()
				store := pinStore(t, srv.URL, keys)
				if err := store.SaveSnapshot(blobA); err != nil {
					t.Fatal(err)
				}
				if lane.seed != 0 {
					// The seeded token is live until the lane's own check-in
					// retires it, which only the drain pulse never does.
					g.mu.Lock()
					g.live = "tok-seed"
					g.mu.Unlock()
					if err := store.SaveSession(Session{
						SessionID: "s-seed", SessionToken: "tok-seed", SnapshotID: idA,
						User: "kim", Roles: []string{"dev"}, Harness: "claude-code/2.1.0",
						IssuedAt: time.Now(), ExpiresAt: time.Now().Add(lane.seed),
					}); err != nil {
						t.Fatal(err)
					}
				}

				ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
				defer cancel()
				var out bytes.Buffer
				err := lane.run(ctx, t, store, srv.URL, &out)

				g.mu.Lock()
				served, refused := g.served, g.refused
				g.mu.Unlock()
				wantPin := idB
				if refuse {
					wantPin = idA
					var said string
					switch lane.says {
					case "error":
						if err != nil {
							said = err.Error()
						}
					case "log":
						said = out.String()
					}
					if lane.says != "" && (!strings.Contains(said, gatedRefusal) || strings.Contains(said, "HTTP 401")) {
						t.Errorf("the refusal reached the person as %q, want the server's sentence", said)
					}
					if refused == 0 {
						t.Errorf("the lane never fetched the snapshot")
					}
				} else {
					if err != nil {
						t.Fatalf("lane failed: %v", err)
					}
					if refused != 0 || served == 0 {
						t.Errorf("snapshot fetches: %d served, %d refused; want every fetch to carry the newest session token", served, refused)
					}
				}

				cfg, _ := store.LoadConfig()
				if diskID, ok := diskSnapshotID(store, cfg); !ok || diskID != wantPin {
					t.Errorf("disk blob = %q (verified %v), want %q", diskID, ok, wantPin)
				}
				// A refused session start saves no session, and every other
				// lane leaves one behind that pins the blob on disk.
				ses, err := store.LoadSession()
				switch {
				case err == nil && ses.SnapshotID != wantPin:
					t.Errorf("session pin = %q, want %q", ses.SnapshotID, wantPin)
				case err != nil && (!refuse || lane.says != "error"):
					t.Errorf("no session after the lane: %v", err)
				}
			})
		}
	}
}
