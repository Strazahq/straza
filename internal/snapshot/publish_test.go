package snapshot

import (
	"bytes"
	"context"
	"testing"

	"github.com/strazahq/straza/internal/policy"
	"github.com/strazahq/straza/internal/store"
)

// rmPolicyEdited is block-rm with a new reason: the text an admin saved over
// the live set without publishing it.
const rmPolicyEdited = `apiVersion: straza.dev/v1beta1
kind: PolicySet
metadata: { name: block-rm }
spec:
  match: { roles: [dev] }
  rules:
    - id: no-rm-rf
      tools: [shell.exec]
      command: { denyPatterns: ["rm -rf *"] }
      effect: deny
      reason: "Saved but not published"
`

const pushPolicyEdited = `apiVersion: straza.dev/v1beta1
kind: PolicySet
metadata: { name: block-push }
spec:
  match: { roles: [dev] }
  rules:
    - id: no-push
      tools: [shell.exec]
      command: { denyPatterns: ["git push *"] }
      effect: deny
      reason: "Push blocked, second version"
`

// curlPolicy is a set that is saved as on but never published.
const curlPolicy = `apiVersion: straza.dev/v1beta1
kind: PolicySet
metadata: { name: block-curl }
spec:
  match: { roles: [dev] }
  rules:
    - id: no-curl
      tools: [shell.exec]
      command: { denyPatterns: ["curl *"] }
      effect: deny
      reason: "Curl blocked"
`

// saveOver stores text as the named set's source, keeping its status: the
// state an apply over a live set leaves behind.
func saveOver(t *testing.T, st store.Store, name, yaml string) {
	t.Helper()
	ps, err := st.Policies().GetByName(context.Background(), name)
	if err != nil {
		t.Fatal(err)
	}
	ps.YAMLSource = yaml
	if _, err := st.Policies().Update(context.Background(), ps); err != nil {
		t.Fatal(err)
	}
}

// publishedPair returns a loaded service whose running snapshot carries the
// original block-rm and block-push, both active in the store.
func publishedPair(t *testing.T) (*Service, store.Store) {
	t.Helper()
	st := testStore(t)
	svc := New(st, testSigner(t, st), nil, policy.EffectAllow, 300, nil)
	createSet(t, st, "block-rm", "active", rmPolicy)
	createSet(t, st, "block-push", "active", pushPolicy)
	if err := svc.Load(context.Background()); err != nil {
		t.Fatalf("Load: %v", err)
	}
	return svc, st
}

// TestSnapshotDocumentsCompileToThemselves pins that the text a snapshot
// carries is a valid compile source: compiling a snapshot's own documents
// reproduces them byte for byte, so the running snapshot can stand in for
// the stored sources of the sets a publish does not touch.
func TestSnapshotDocumentsCompileToThemselves(t *testing.T) {
	svc, st := publishedPair(t)
	ctx := context.Background()
	act, err := st.Snapshots().GetActive(ctx)
	if err != nil {
		t.Fatal(err)
	}
	_, snap, err := policy.OpenSnapshot(act.Blob, act.ID, svc.publicLookup(ctx))
	if err != nil {
		t.Fatal(err)
	}
	again, err := policy.Compile(policy.CompileInput{Documents: snap.Documents, LocalDefault: policy.EffectAllow, MaxAge: 300})
	if err != nil {
		t.Fatalf("compile the snapshot's own documents: %v", err)
	}
	if len(again.Documents) != len(snap.Documents) {
		t.Fatalf("recompiled %d documents, want %d", len(again.Documents), len(snap.Documents))
	}
	for i := range again.Documents {
		if !bytes.Equal(again.Documents[i], snap.Documents[i]) {
			t.Errorf("document %d changed on recompile:\n%s\nwant\n%s", i, again.Documents[i], snap.Documents[i])
		}
	}
}

// TestLoadWithoutSnapshotCompilesActiveSetsFromStore pins boot: with no
// persisted snapshot, Load compiles every active set from its stored source,
// because there is no published text to prefer.
func TestLoadWithoutSnapshotCompilesActiveSetsFromStore(t *testing.T) {
	ctx := context.Background()
	st := testStore(t)
	svc := New(st, testSigner(t, st), nil, policy.EffectAllow, 300, nil)
	createSet(t, st, "block-rm", "active", rmPolicyEdited)
	createSet(t, st, "block-push", "draft", pushPolicy)

	if err := svc.Load(ctx); err != nil {
		t.Fatalf("Load: %v", err)
	}
	if d := evaluate(t, svc.Current(), "rm -rf /tmp/x"); d.Reason != "Saved but not published" {
		t.Errorf("boot compile decides with %q, want the stored source", d.Reason)
	}
	if d := evaluate(t, svc.Current(), "git push origin main"); d.Effect != policy.EffectAllow || !d.Default {
		t.Errorf("draft set compiled in at boot: %+v", d)
	}
}
