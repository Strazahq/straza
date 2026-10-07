package agentguard

import (
	"crypto/ed25519"
	"encoding/base64"
	"testing"
	"time"

	"github.com/strazahq/straza/internal/agentguard/spool"
	"github.com/strazahq/straza/internal/policy"
)

// TestDrainSpawnBinaryAllowed pins the self-re-exec gate: the detached drain
// fires only under the client binary's own name, `straza` (± .exe), and
// refuses test binaries and anything else (a unit-test binary re-exec'ing
// itself would run the suite). The pre-rename name is refused too: the gate
// accepts only the exact name, which is what makes it a guard at all.
func TestDrainSpawnBinaryAllowed(t *testing.T) {
	cases := []struct {
		base string
		want bool
	}{
		{"straza", true},
		{"straza.exe", true},
		{"straz", false},
		{"straz.exe", false},
		{"strazactl", false},
		{"strazad", false},
		{"agentguard.test", false},
		{"straza.test", false},
		{"", false},
	}
	for _, tc := range cases {
		if got := drainSpawnBinaryAllowed(tc.base); got != tc.want {
			t.Errorf("drainSpawnBinaryAllowed(%q) = %v, want %v", tc.base, got, tc.want)
		}
	}
}

// TestDecideKicksDetachedDrain pins the near-live audit path: every governed
// decision spools its record AND kicks the drain seam, so audit reaches the
// server within seconds without a daemon (without the kick, a deny stays
// invisible in `strazactl audit tail` until session exit). Observational events must not
// kick, because they are not spooled.
func TestDecideKicksDetachedDrain(t *testing.T) {
	t.Setenv("STRAZA_HOME", t.TempDir())
	store, err := OpenStore()
	if err != nil {
		t.Fatal(err)
	}

	// Minimal-but-real local state: a signed snapshot the LocalPDP verifies
	// against pinned keys, and a session that is nowhere near token refresh.
	doc := []byte(`
apiVersion: straza.dev/v1beta1
kind: PolicySet
metadata: {name: drain-kick}
spec:
  match: {roles: [dev]}
  rules:
    - id: no-rm
      tools: [shell.exec]
      command: {denyPatterns: ["rm -rf *"]}
      effect: deny
      reason: "blocked"
`)
	snap, err := policy.Compile(policy.CompileInput{
		Documents: [][]byte{doc}, LocalDefault: "allow", MaxAge: 900,
		CreatedUnix: time.Now().Unix(),
	})
	if err != nil {
		t.Fatal(err)
	}
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	signed, id, err := snap.Sign("k1", priv)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SaveConfig(Config{
		ServerURL:    "http://127.0.0.1:1",
		SnapshotKeys: map[string]string{"k1": base64.StdEncoding.EncodeToString(pub)},
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveSnapshot(signed); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveSession(Session{
		SessionID: "s1", SessionToken: "tok", SnapshotID: id,
		User: "u1", Roles: []string{"dev"}, Harness: "claude-code/2.1",
		IssuedAt: time.Now(), ExpiresAt: time.Now().Add(time.Hour),
	}); err != nil {
		t.Fatal(err)
	}

	kicked := 0
	old := drainSpawner
	drainSpawner = func() { kicked++ }
	t.Cleanup(func() { drainSpawner = old })

	d := liveDecider{store: store}
	ev := func(kind string, cmd string) Normalized {
		return Normalized{HarnessName: "claude-code", HarnessVersion: "2.1",
			Event: policy.Event{Kind: kind, Tool: policy.ToolShellExec, Command: cmd}}
	}

	if dec := d.Decide(ev(policy.EventToolPre, "rm -rf /x")); dec.Effect != policy.EffectDeny {
		t.Fatalf("decision = %+v, want deny", dec)
	}
	if kicked != 1 {
		t.Fatalf("drain kicked %d times after a governed decision, want 1", kicked)
	}
	if n, _ := spool.NewSpool(store.SpoolPath()).Pending(); n != 1 {
		t.Fatalf("spooled records = %d, want 1", n)
	}

	// Observational event: audited nowhere on this path, no kick.
	if dec := d.Decide(ev(policy.EventToolPost, "ls")); dec.Effect != policy.EffectAllow {
		t.Fatalf("tool.post decision = %+v, want allow", dec)
	}
	if kicked != 1 {
		t.Errorf("drain kicked on an observational event (%d kicks)", kicked)
	}
}
