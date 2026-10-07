package agentguard

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// TestDoctorSessionHintDefersToDeadCredential pins the session line against
// the identity line: the automatic-refresh hint is honest only while a device
// credential can still start a session. With the credential expired or gone,
// the session line fails and sends the operator to enroll.
func TestDoctorSessionHintDefersToDeadCredential(t *testing.T) {
	t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())
	t.Setenv("CODEX_HOME", t.TempDir())
	t.Setenv("STRAZA_GEMINI_CONFIG_DIR", t.TempDir())
	t.Setenv("STRAZA_MANAGED_SETTINGS_DIR", t.TempDir())
	fakeToken := func(exp time.Time) string {
		payload, _ := json.Marshal(map[string]any{"exp": exp.Unix()})
		return "h." + base64.RawURLEncoding.EncodeToString(payload) + ".s"
	}
	now := time.Now()
	cases := []struct {
		name         string
		identity     *Identity // nil = no identity.json at all
		wantIdentity string
		wantSession  string
		wantHint     []string
		wantAbsent   []string
	}{
		{
			name:         "valid credential, expired session: the refresh hint stands",
			identity:     &Identity{DeviceID: "d1", Username: "kim", DeviceToken: fakeToken(now.Add(20 * 24 * time.Hour))},
			wantIdentity: checkOK, wantSession: checkWarn,
			wantHint:   []string{"refreshes it automatically"},
			wantAbsent: []string{"straza enroll"},
		},
		{
			name:         "expired credential, expired session: enroll, not a refresh",
			identity:     &Identity{DeviceID: "d1", Username: "kim", DeviceToken: fakeToken(now.Add(-time.Hour))},
			wantIdentity: checkFail, wantSession: checkFail,
			wantHint:   []string{"straza enroll", "identity check above"},
			wantAbsent: []string{"refreshes it automatically"},
		},
		{
			name:         "no identity, expired session: enroll, not a refresh",
			identity:     nil,
			wantIdentity: checkFail, wantSession: checkFail,
			wantHint:   []string{"straza enroll"},
			wantAbsent: []string{"refreshes it automatically"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store := daemonStore(t)
			if err := store.SaveConfig(Config{ServerURL: "http://127.0.0.1:9", SnapshotKeys: map[string]string{"k1": "AA=="}}); err != nil {
				t.Fatal(err)
			}
			if tc.identity != nil {
				if err := store.SaveIdentity(*tc.identity); err != nil {
					t.Fatal(err)
				}
			}
			if err := store.SaveSession(Session{SessionID: "s1", SessionToken: "tok", Roles: []string{"dev"}, ExpiresAt: now.Add(-10 * time.Minute)}); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			byName := map[string]Check{}
			for _, c := range Doctor(ctx, store) {
				byName[c.Name] = c
			}
			if got := byName["identity"].Status; got != tc.wantIdentity {
				t.Errorf("identity = %s (%+v), want %s", got, byName["identity"], tc.wantIdentity)
			}
			ses, ok := byName["session"]
			if !ok || ses.Status != tc.wantSession {
				t.Fatalf("session = %+v, want status %s", ses, tc.wantSession)
			}
			for _, w := range tc.wantHint {
				if !strings.Contains(ses.Hint, w) {
					t.Errorf("session hint %q lacks %q", ses.Hint, w)
				}
			}
			for _, w := range tc.wantAbsent {
				if strings.Contains(ses.Hint, w) {
					t.Errorf("session hint %q still says %q", ses.Hint, w)
				}
			}
		})
	}
}
