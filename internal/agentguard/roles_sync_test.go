package agentguard

import (
	"context"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
	"time"

	"github.com/strazahq/straza/internal/policy"
)

// rolesSyncPolicy is a PolicySet scoped to the "contractor" role: it only
// applies to subjects that carry the role, so the decision it produces is a
// direct read-out of the roles the local PDP believes the session has.
const rolesSyncPolicy = `
apiVersion: straza.dev/v1beta1
kind: PolicySet
metadata: {name: contractor-lockdown}
spec:
  match:
    roles: [contractor]
  rules:
    - {id: r-contractor, tools: [shell.exec], command: {denyPatterns: ["secret-cmd *"]}, effect: deny, reason: "contractors may not run secret-cmd"}
`

// rolesSyncServer answers /v1/checkin with the identity JSON the test wants to
// hand back, keeping the session's snapshot id stable so the only thing that
// can change a verdict is the role set.
func rolesSyncServer(t *testing.T, signed []byte, snapID, identityJSON string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/checkin":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"session_id":"s1","session_token":"tok2","expires_in":300,` +
				identityJSON + `"snapshot_id":"` + snapID + `"}`))
		case "/v1/snapshot":
			w.Header().Set("X-Straza-Snapshot-Id", snapID)
			_, _ = w.Write(signed)
		case "/v1/audit/batch":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"accepted":0}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

// rolesSyncSeed stores a session whose roles are the pre-change state.
func rolesSyncSeed(t *testing.T, srvURL string, keys map[string]string, signed []byte, snapID string, roles []string, expiresIn time.Duration) *Store {
	t.Helper()
	store := daemonStore(t)
	if err := store.SaveConfig(Config{ServerURL: srvURL, SnapshotKeys: keys}); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveSnapshot(signed); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveSession(Session{
		SessionID: "s1", SessionToken: "tok", SnapshotID: snapID,
		User: "u1", Roles: roles, Harness: "claude-code/2.1.0",
		IssuedAt: time.Now(), ExpiresAt: time.Now().Add(expiresIn),
	}); err != nil {
		t.Fatal(err)
	}
	return store
}

// rolesSyncDecide runs the hook lane exactly as flows.go does: load the
// persisted session, build the PDP from ses.Subject(), decide.
func rolesSyncDecide(t *testing.T, store *Store) policy.Decision {
	t.Helper()
	ses, err := store.LoadSession()
	if err != nil {
		t.Fatal(err)
	}
	pdp, err := NewLocalPDP(store, ses.Subject())
	if err != nil {
		t.Fatal(err)
	}
	return pdp.Decide(Normalized{
		HarnessName: "claude-code", HarnessVersion: "2.1.0",
		Event: policy.Event{Kind: policy.EventToolPre, Tool: policy.ToolShellExec, Command: "secret-cmd --dump"},
	})
}

// TestRefreshAdoptsResolvedIdentity: the server re-resolves roles on EVERY
// checkin, and both refresh paths must write them onto the session. If only
// session start did, a role assigned (or removed) in the IdM would never
// reach local hook decisions for the life of the harness session, while
// server-side lanes (/v1/decide, the MCP gateway) converge at the next
// checkin.
//
// The table drives both refresh paths through the same identity transitions.
func TestRefreshAdoptsResolvedIdentity(t *testing.T) {
	priv, keys := testSnapshotKey(t)
	signed, snapID := testSignedPolicy(t, priv, rolesSyncPolicy)

	cases := []struct {
		name string
		// seed state
		roles []string
		// what /v1/checkin returns for the identity fields
		identityJSON string
		// expected persisted state after the refresh
		wantRoles []string
		// expected hook verdict after the refresh
		wantEffect string
	}{
		{
			name: "role assigned in the IdM is enforced after refresh",
			// The lockdown set does not match yet: the hook allows.
			roles:        []string{"dev"},
			identityJSON: `"user":"u1","roles":["dev","contractor"],`,
			wantRoles:    []string{"dev", "contractor"},
			wantEffect:   policy.EffectDeny,
		},
		{
			name:         "role removed in the IdM stops being enforced after refresh",
			roles:        []string{"dev", "contractor"},
			identityJSON: `"user":"u1","roles":["dev"],`,
			wantRoles:    []string{"dev"},
			wantEffect:   policy.EffectAllow,
		},
		{
			name: "user stripped of every role adopts the empty set",
			// The server sends a non-nil empty array for a user with no roles.
			roles:        []string{"contractor"},
			identityJSON: `"user":"u1","roles":[],`,
			wantRoles:    []string{},
			wantEffect:   policy.EffectAllow,
		},
		{
			name: "identity-less response keeps the last known good",
			// A server that sends no roles field at all (older build, or a
			// proxy that stripped it) must NOT silently clear the role set:
			// that would unmatch the lockdown PolicySet and WIDEN access.
			roles:        []string{"contractor"},
			identityJSON: `"user":"u1",`,
			wantRoles:    []string{"contractor"},
			wantEffect:   policy.EffectDeny,
		},
	}

	// Each refresh path gets the identical table. The daemon path is the
	// common one; RefreshIfStale is the daemonless hook path, which only
	// fires inside the 120 s pre-expiry window.
	paths := []struct {
		name      string
		expiresIn time.Duration
		refresh   func(t *testing.T, store *Store)
	}{
		{
			name:      "daemon poll",
			expiresIn: time.Hour,
			refresh: func(t *testing.T, store *Store) {
				t.Helper()
				d := NewDaemon(store, nil)
				if gone := d.refreshOnce(t); gone {
					t.Fatal("session dropped by a healthy refresh")
				}
			},
		},
		{
			name: "daemonless RefreshIfStale",
			// Inside the 120 s window so the refresh actually fires.
			expiresIn: 30 * time.Second,
			refresh: func(t *testing.T, store *Store) {
				t.Helper()
				ses, err := store.LoadSession()
				if err != nil {
					t.Fatal(err)
				}
				pdp, err := NewLocalPDP(store, ses.Subject())
				if err != nil {
					t.Fatal(err)
				}
				pdp.RefreshIfStale(context.Background(), store)
			},
		},
	}

	for _, path := range paths {
		for _, tc := range cases {
			t.Run(path.name+"/"+tc.name, func(t *testing.T) {
				srv := rolesSyncServer(t, signed, snapID, tc.identityJSON)
				store := rolesSyncSeed(t, srv.URL, keys, signed, snapID, tc.roles, path.expiresIn)

				path.refresh(t, store)

				ses, err := store.LoadSession()
				if err != nil {
					t.Fatal(err)
				}
				if !reflect.DeepEqual(ses.Roles, tc.wantRoles) {
					t.Errorf("persisted Session.Roles = %#v, want %#v", ses.Roles, tc.wantRoles)
				}
				if got := rolesSyncDecide(t, store); got.Effect != tc.wantEffect {
					t.Errorf("hook verdict after refresh = %s (rule %q), want %s",
						got.Effect, got.RuleID, tc.wantEffect)
				}
			})
		}
	}
}

// TestRefreshKeepsTokenAndRolesConsistent pins the reason this is safe to do
// on the refresh path: the token and the roles come from the SAME checkin
// response, so the session can never carry a token minted for one role set
// alongside a different cached role set.
func TestRefreshKeepsTokenAndRolesConsistent(t *testing.T) {
	priv, keys := testSnapshotKey(t)
	signed, snapID := testSignedPolicy(t, priv, rolesSyncPolicy)

	srv := rolesSyncServer(t, signed, snapID, `"user":"u1","roles":["dev","contractor"],`)
	store := rolesSyncSeed(t, srv.URL, keys, signed, snapID, []string{"dev"}, time.Hour)

	d := NewDaemon(store, nil)
	if gone := d.refreshOnce(t); gone {
		t.Fatal("session dropped by a healthy refresh")
	}

	ses, err := store.LoadSession()
	if err != nil {
		t.Fatal(err)
	}
	if ses.SessionToken != "tok2" {
		t.Errorf("session token = %q, want the refreshed token", ses.SessionToken)
	}
	if !reflect.DeepEqual(ses.Roles, []string{"dev", "contractor"}) {
		t.Errorf("roles = %#v, want the set the refreshed token was minted for", ses.Roles)
	}
}
