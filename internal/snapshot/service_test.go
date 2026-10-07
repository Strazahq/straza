package snapshot

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/strazahq/straza/internal/authn"
	"github.com/strazahq/straza/internal/config"
	"github.com/strazahq/straza/internal/policy"
	"github.com/strazahq/straza/internal/store"
)

// rmPolicy mirrors the block-rm document the server API tests apply, so the
// engine behavior pinned here matches what the PDP tests observe.
const rmPolicy = `apiVersion: straza.dev/v1beta1
kind: PolicySet
metadata: { name: block-rm }
spec:
  match: { roles: [dev] }
  rules:
    - id: no-rm-rf
      tools: [shell.exec]
      command: { denyPatterns: ["rm -rf *"] }
      effect: deny
      reason: "Destructive delete blocked"
`

const pushPolicy = `apiVersion: straza.dev/v1beta1
kind: PolicySet
metadata: { name: block-push }
spec:
  match: { roles: [dev] }
  rules:
    - id: no-push
      tools: [shell.exec]
      command: { denyPatterns: ["git push *"] }
      effect: deny
      reason: "Push blocked"
`

func testStore(t *testing.T) store.Store {
	t.Helper()
	s, err := store.Open(config.Config{Store: config.Store{
		Driver: config.DriverSQLite,
		DSN:    filepath.Join(t.TempDir(), "t.db"),
	}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	if err := s.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	return s
}

// testSigner is the production Signer (authn.SnapshotKeys) over the test
// store; it creates the active snapshot signing key on first use.
func testSigner(t *testing.T, st store.Store) Signer {
	t.Helper()
	keys, err := authn.NewSnapshotKeys(context.Background(), st.SigningKeys())
	if err != nil {
		t.Fatal(err)
	}
	return keys
}

func createSet(t *testing.T, st store.Store, name, status, yaml string) {
	t.Helper()
	if _, err := st.Policies().Create(context.Background(), store.PolicySet{
		Name: name, Status: status, YAMLSource: yaml,
	}); err != nil {
		t.Fatal(err)
	}
}

func evaluate(t *testing.T, cur *Current, command string) policy.Decision {
	t.Helper()
	if cur == nil {
		t.Fatal("Current() = nil, want live snapshot")
	}
	if cur.Engine == nil {
		t.Fatal("Current().Engine = nil")
	}
	return cur.Engine.Evaluate(
		policy.Event{Kind: policy.EventToolPre, Tool: policy.ToolShellExec, Command: command},
		policy.Subject{User: "kim", Roles: []string{"dev"}},
	)
}

func TestLoadEmptyStoreCompilesEmptySnapshot(t *testing.T) {
	ctx := context.Background()
	st := testStore(t)
	svc := New(st, testSigner(t, st), nil, policy.EffectAllow, 300, nil)
	if svc.Current() != nil {
		t.Fatal("Current() before Load = non-nil, want nil")
	}
	if err := svc.Load(ctx); err != nil {
		t.Fatalf("Load: %v", err)
	}
	cur := svc.Current()
	if cur == nil {
		t.Fatal("Current() after Load = nil")
	}
	if cur.ID == "" || len(cur.Signed) == 0 {
		t.Errorf("empty snapshot not materialized: id=%q signed=%d bytes", cur.ID, len(cur.Signed))
	}
	if cur.MaxAge != 300 {
		t.Errorf("MaxAge = %d, want 300", cur.MaxAge)
	}
	if d := evaluate(t, cur, "rm -rf /tmp/x"); d.Effect != policy.EffectAllow || !d.Default {
		t.Errorf("empty-snapshot decision = %+v, want profile-default allow", d)
	}
	// The empty snapshot is persisted and active, not just in memory.
	act, err := st.Snapshots().GetActive(ctx)
	if err != nil {
		t.Fatalf("GetActive: %v", err)
	}
	if act.ID != cur.ID {
		t.Errorf("active snapshot = %s, want %s", act.ID, cur.ID)
	}
}

func TestRecompileCompilesActiveSetsAndPublishes(t *testing.T) {
	ctx := context.Background()
	st := testStore(t)

	type pubCall struct {
		subject string
		data    map[string]any
	}
	var calls []pubCall
	pub := func(_ context.Context, subject string, data map[string]any) {
		calls = append(calls, pubCall{subject, data})
	}
	svc := New(st, testSigner(t, st), pub, policy.EffectAllow, 300, nil)
	if err := svc.Load(ctx); err != nil {
		t.Fatalf("Load: %v", err)
	}
	emptyID := svc.Current().ID

	createSet(t, st, "block-rm", "active", rmPolicy)
	createSet(t, st, "block-push", "draft", pushPolicy)

	id, err := svc.Recompile(ctx)
	if err != nil {
		t.Fatalf("Recompile: %v", err)
	}
	if id == "" || id == emptyID {
		t.Fatalf("Recompile id = %q, want new content-addressed id (empty snapshot was %q)", id, emptyID)
	}
	if cur := svc.Current(); cur.ID != id {
		t.Errorf("Current().ID = %s, want swapped to %s", cur.ID, id)
	}

	if len(calls) != 2 { // Load's empty compile + this Recompile
		t.Fatalf("publisher fired %d times, want 2", len(calls))
	}
	last := calls[len(calls)-1]
	if last.subject != "straza.policy.updated" {
		t.Errorf("publish subject = %q, want straza.policy.updated", last.subject)
	}
	if last.data["snapshot"] != id {
		t.Errorf("publish snapshot = %v, want %s", last.data["snapshot"], id)
	}
	if last.data["sets"] != 1 {
		t.Errorf("publish sets = %v, want 1 (draft must not compile in)", last.data["sets"])
	}

	if d := evaluate(t, svc.Current(), "rm -rf /tmp/x"); d.Effect != policy.EffectDeny || d.RuleID != "no-rm-rf" {
		t.Errorf("rm decision = %+v, want deny by no-rm-rf", d)
	}
	// The draft set is not in the snapshot: its deny must not fire.
	if d := evaluate(t, svc.Current(), "git push origin main"); d.Effect != policy.EffectAllow || !d.Default {
		t.Errorf("git push decision = %+v, want default allow (draft excluded)", d)
	}
}

func TestRecompileSameStateReusesPersistedSnapshot(t *testing.T) {
	ctx := context.Background()
	st := testStore(t)
	svc := New(st, testSigner(t, st), nil, policy.EffectAllow, 300, nil)
	createSet(t, st, "block-rm", "active", rmPolicy)

	// The id is a content hash over a CreatedUnix-second payload and ed25519
	// is deterministic, so recompiling the same policy state within one
	// second reproduces the persisted row byte for byte: the second Create
	// hits store.ErrConflict and must be reused, not surfaced as an error.
	// Retry on the rare second-boundary crossing so the same-id pin cannot
	// flake.
	var first, second string
	for attempt := 0; attempt < 5; attempt++ {
		sec := time.Now().Unix()
		var err error
		if first, err = svc.Recompile(ctx); err != nil {
			t.Fatalf("first Recompile: %v", err)
		}
		if second, err = svc.Recompile(ctx); err != nil {
			t.Fatalf("second Recompile (existing-row reuse): %v", err)
		}
		if time.Now().Unix() == sec {
			break
		}
	}
	if first != second {
		t.Fatalf("back-to-back Recompile ids differ: %s vs %s", first, second)
	}
	if cur := svc.Current(); cur == nil || cur.ID != second {
		t.Fatalf("Current() not on reused snapshot %s", second)
	}
}

func TestLoadFailsClosedOnCorruptActiveSnapshot(t *testing.T) {
	ctx := context.Background()
	st := testStore(t)
	if _, err := st.Snapshots().Create(ctx, store.Snapshot{
		ID: "corrupt", SignerKeyID: "unknown", Blob: []byte("not a snapshot"),
	}); err != nil {
		t.Fatalf("seed corrupt snapshot: %v", err)
	}
	if err := st.Snapshots().SetActive(ctx, "corrupt"); err != nil {
		t.Fatalf("activate corrupt snapshot: %v", err)
	}

	svc := New(st, testSigner(t, st), nil, policy.EffectAllow, 300, nil)
	if err := svc.Load(ctx); err == nil {
		t.Fatal("Load of an unopenable active snapshot succeeded, want error (fail closed)")
	}
	if svc.Current() != nil {
		t.Error("Current() non-nil after failed Load, want nil")
	}
}

func TestRecompileInvalidDocumentKeepsCurrent(t *testing.T) {
	ctx := context.Background()
	st := testStore(t)
	svc := New(st, testSigner(t, st), nil, policy.EffectAllow, 300, nil)
	if err := svc.Load(ctx); err != nil {
		t.Fatalf("Load: %v", err)
	}
	prevID := svc.Current().ID

	createSet(t, st, "broken", "active", "kind: NotAPolicySet")
	if _, err := svc.Recompile(ctx); err == nil {
		t.Fatal("Recompile with an unparseable active set succeeded, want error")
	}
	if cur := svc.Current(); cur == nil || cur.ID != prevID {
		t.Errorf("Current() changed after failed Recompile, want %s kept live", prevID)
	}
}

// fetchPolicy is a live set whose Rego module calls http.send, which the
// policy compiler refuses.
const fetchPolicy = `apiVersion: straza.dev/v1beta1
kind: PolicySet
metadata: { name: fetcher }
spec:
  rules:
    - { id: r1, tools: [shell.exec], effect: deny, reason: x }
  escape:
    rego: |
      package straza.ext

      deny contains msg if {
        r := http.send({"method": "get", "url": "http://169.254.169.254/"})
        msg := r.raw_body
      }
`

// TestLoadRefusesLiveRefusedBuiltin pins the boot sentence for a live
// policy whose module calls a refused built-in, from a stored snapshot and
// from the stored sets of a store with no snapshot. Nothing goes live.
func TestLoadRefusesLiveRefusedBuiltin(t *testing.T) {
	const want = "snapshot: policy: set fetcher: its Rego module calls http.send on line 4, and Straza refuses that built-in " +
		"because a policy module must not reach the network, the file system or the process environment. " +
		"Remove the call from the module. strazad did not start, because the live policy holds that module. " +
		"Start the release you upgraded from, remove the call or turn the set off and publish, then start this release again"
	tests := []struct {
		name string
		seed func(t *testing.T, st store.Store, signer Signer)
	}{
		{"an active snapshot holds the module", func(t *testing.T, st store.Store, signer Signer) {
			ctx := context.Background()
			snap, err := policy.Compile(policy.CompileInput{
				Documents: [][]byte{[]byte(fetchPolicy)}, LocalDefault: policy.EffectAllow, MaxAge: 300, CreatedUnix: 1700000000,
			})
			if err != nil {
				t.Fatalf("Compile: %v", err)
			}
			kid, priv, err := signer.Active(ctx)
			if err != nil {
				t.Fatalf("signing key: %v", err)
			}
			signed, id, err := snap.Sign(kid, priv)
			if err != nil {
				t.Fatalf("Sign: %v", err)
			}
			if _, err := st.Snapshots().Create(ctx, store.Snapshot{ID: id, SignerKeyID: kid, Blob: signed}); err != nil {
				t.Fatalf("seed snapshot: %v", err)
			}
			if err := st.Snapshots().SetActive(ctx, id); err != nil {
				t.Fatalf("activate snapshot: %v", err)
			}
		}},
		{"no snapshot and an active set holds the module", func(t *testing.T, st store.Store, _ Signer) {
			createSet(t, st, "fetcher", "active", fetchPolicy)
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			st := testStore(t)
			signer := testSigner(t, st)
			tc.seed(t, st, signer)
			svc := New(st, signer, nil, policy.EffectAllow, 300, nil)
			err := svc.Load(context.Background())
			if err == nil || err.Error() != want {
				t.Errorf("Load error\n got %v\nwant %s", err, want)
			}
			if svc.Current() != nil {
				t.Error("Current() non-nil after the refused Load, want nil")
			}
		})
	}
}

func TestLoadOpensPersistedActiveSnapshot(t *testing.T) {
	ctx := context.Background()
	st := testStore(t)
	createSet(t, st, "block-rm", "active", rmPolicy)

	first := New(st, testSigner(t, st), nil, policy.EffectAllow, 300, nil)
	id, err := first.Recompile(ctx)
	if err != nil {
		t.Fatalf("Recompile: %v", err)
	}

	// Fresh service and signer over the same store: Load must open the
	// persisted blob, not recompile; a recompile would publish.
	pub := func(_ context.Context, subject string, _ map[string]any) {
		t.Errorf("unexpected publish %q: Load of a persisted snapshot must not recompile", subject)
	}
	// graceSecs deliberately differs: MaxAge must come from the persisted
	// snapshot, not the new service's config.
	second := New(st, testSigner(t, st), pub, policy.EffectAllow, 999, nil)
	if err := second.Load(ctx); err != nil {
		t.Fatalf("Load: %v", err)
	}
	cur := second.Current()
	if cur == nil || cur.ID != id {
		t.Fatalf("Current() = %+v, want persisted snapshot %s", cur, id)
	}
	if cur.MaxAge != 300 {
		t.Errorf("MaxAge = %d, want 300 from the persisted snapshot", cur.MaxAge)
	}
	if d := evaluate(t, cur, "rm -rf /tmp/x"); d.Effect != policy.EffectDeny || d.RuleID != "no-rm-rf" {
		t.Errorf("rm decision = %+v, want deny by no-rm-rf", d)
	}
}
