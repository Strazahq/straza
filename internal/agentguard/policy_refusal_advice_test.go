package agentguard

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/strazahq/straza/internal/policy"
)

// TestRefusalDenyMomentCountsGrace pins that doctor and the daemon log name
// the moment the hooks start to deny a refused policy: the deadline plus the
// cached snapshot's offline grace, which is zero in the enterprise profile
// and 900 s in standalone. The hook itself is the reference. It still allows
// a minute before that moment and denies a second after it.
func TestRefusalDenyMomentCountsGrace(t *testing.T) {
	priv, keys := testSnapshotKey(t)
	cases := []struct {
		name    string
		compile func(*testing.T, ed25519.PrivateKey, string) ([]byte, string)
		grace   time.Duration
	}{
		{"enterprise, no offline grace", testEnterprisePolicy, 0},
		{"standalone, 900 s offline grace", testSignedPolicy, 900 * time.Second},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			blobA, idA := tc.compile(t, priv, pinPolicyA)
			blobB, idB := tc.compile(t, priv, pinPolicyB)
			g := &tokenGatedServer{blob: blobB, id: idB, refuseAll: true}
			srv := httptest.NewServer(g.handler(t))
			defer srv.Close()
			store := pinStore(t, srv.URL, keys)
			if err := store.SaveSnapshot(blobA); err != nil {
				t.Fatal(err)
			}
			held := time.Now().Add(time.Hour).Truncate(time.Second)
			seedSession(t, store, idA, held)
			var log bytes.Buffer
			NewDaemon(store, &log).refreshOnce(t)
			if want := "hooks deny from " + held.Add(tc.grace).Local().Format(time.RFC3339); !strings.Contains(log.String(), want) {
				t.Errorf("daemon log %q, want %q", log.String(), want)
			}
			for _, step := range []struct {
				denyIn time.Duration
				says   string
			}{
				{time.Minute, "Hooks deny every governed call from "},
				{-time.Second, "Hooks have denied every governed call since "},
			} {
				denyAt := time.Now().Add(step.denyIn).Truncate(time.Second)
				ses, _ := store.LoadSession()
				ses.PolicyDeadline = denyAt.Add(-tc.grace)
				if err := store.SaveSession(ses); err != nil {
					t.Fatal(err)
				}
				ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
				var line Check
				for _, c := range Doctor(ctx, store) {
					if c.Name == "policy" {
						line = c
					}
				}
				cancel()
				if want := step.says + denyAt.Local().Format(time.RFC3339); !strings.Contains(line.Detail, want) {
					t.Errorf("doctor policy line %q, want %q", line.Detail, want)
				}
				if d := decideNow(t, store, "git status"); (d.Effect == policy.EffectDeny) != (step.denyIn < 0) {
					t.Errorf("git status %v before the deny moment = %s %q", step.denyIn, d.Effect, d.Reason)
				}
			}
		})
	}
}

// TestUnverifiedPolicyRemedyRestores pins that the remedy the deny names for
// a snapshot the pinned keys cannot verify works on both kinds of install.
// After the server's snapshot key is replaced, `straza enroll` pins the
// current keys on a personal install. On a managed install only the managed
// install run again with --server does, because the managed config wins
// over the one enroll writes. A new session then adopts the server's policy
// and ends the deny.
func TestUnverifiedPolicyRemedyRestores(t *testing.T) {
	for _, managed := range []bool{false, true} {
		t.Run(fmt.Sprintf("managed install %v", managed), func(t *testing.T) {
			oldPriv, oldKeys := testSnapshotKey(t)
			newPriv, newKeys := testSnapshotKey(t)
			blobA, idA := testEnterprisePolicy(t, oldPriv, pinPolicyA)
			blobB, idB := testEnterprisePolicy(t, newPriv, pinPolicyB)
			serve := (&tokenGatedServer{blob: blobB, id: idB}).handler(t)
			// The first managed install pins the old key, and the server
			// serves the new one from the remedy on.
			var servedKey atomic.Value
			servedKey.Store(oldKeys["k1"])
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/.well-known/straza/snapshot-keys.json" {
					_, _ = fmt.Fprintf(w, `{"keys":[{"kid":"k1","key":%q}]}`, servedKey.Load().(string))
					return
				}
				serve(w, r)
			}))
			defer srv.Close()
			store := pinStore(t, srv.URL, oldKeys)
			t.Setenv("STRAZA_SYSTEM", t.TempDir())
			install := ManagedInstallOptions{ServerURL: srv.URL, BinDir: t.TempDir()}
			ctx := context.Background()
			if managed {
				if err := InstallManaged(ctx, install, io.Discard); err != nil {
					t.Fatal(err)
				}
			}
			if err := store.SaveSnapshot(blobA); err != nil {
				t.Fatal(err)
			}
			seedSession(t, store, idA, time.Now().Add(-time.Minute))
			if d := decideAfterRefresh(t, store, shellEvent("git status")); d.Effect != policy.EffectDeny ||
				!strings.Contains(d.Reason, "run `straza enroll` again") || !strings.Contains(d.Reason, "`straza install --managed`") {
				t.Fatalf("git status = %s %q, want the deny that names both remedies", d.Effect, d.Reason)
			}

			// What `straza enroll` writes: the user config with the current keys.
			servedKey.Store(newKeys["k1"])
			if err := store.SaveConfig(Config{ServerURL: srv.URL, SnapshotKeys: newKeys}); err != nil {
				t.Fatal(err)
			}
			if managed {
				if cfg, _ := store.LoadConfig(); cfg.SnapshotKeys["k1"] != oldKeys["k1"] {
					t.Fatal("enroll re-pinned a managed install's keys, so the deny need not name the managed install")
				}
				if err := InstallManaged(ctx, install, io.Discard); err != nil {
					t.Fatal(err)
				}
			}
			if _, _, err := SessionStart(ctx, store, "claude-code", "2.1.0"); err != nil {
				t.Fatalf("session start after the remedy: %v", err)
			}
			if ses, _ := store.LoadSession(); ses.SnapshotID != idB || ses.PolicyRefused != "" {
				t.Fatalf("after the remedy: pin %q, refusal %q; want %q and no refusal", ses.SnapshotID, ses.PolicyRefused, idB)
			}
			if d := decideNow(t, store, "b-cmd /x"); d.Effect != policy.EffectDeny || d.RuleID != "rb" {
				t.Errorf("b-cmd = %s by %q, want the server's policy to deny it", d.Effect, d.RuleID)
			}
		})
	}
}
