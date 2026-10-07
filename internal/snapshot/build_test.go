package snapshot

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"github.com/strazahq/straza/internal/policy"
	"github.com/strazahq/straza/internal/store"
)

// TestBuildChangesOnlyTheNamedSets pins what Build compiles: a set mapped
// to text replaces the set of that name or enters the snapshot, a set
// mapped to nil leaves it, and every other set keeps the text it was
// published with, not the edit saved over it. The snapshot is signed and
// opens with the service's keys, and Build stores, activates, swaps and
// emits nothing.
func TestBuildChangesOnlyTheNamedSets(t *testing.T) {
	cases := []struct {
		name    string
		changes map[string][]byte
		want    map[string]string
	}{
		{
			name:    "a changed set and a new one",
			changes: map[string][]byte{"block-push": []byte(pushPolicyEdited), "block-curl": []byte(curlPolicy)},
			want:    map[string]string{"block-rm": rmPolicy, "block-push": pushPolicyEdited, "block-curl": curlPolicy},
		},
		{
			name:    "a set mapped to nil",
			changes: map[string][]byte{"block-push": nil},
			want:    map[string]string{"block-rm": rmPolicy},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			published := 0
			st := testStore(t)
			svc := New(st, testSigner(t, st), func(context.Context, string, map[string]any) { published++ }, policy.EffectAllow, 300, nil)
			createSet(t, st, "block-rm", "active", rmPolicy)
			createSet(t, st, "block-push", "active", pushPolicy)
			if err := svc.Load(ctx); err != nil {
				t.Fatal(err)
			}
			saveOver(t, st, "block-rm", rmPolicyEdited)
			base, emitted := svc.Current().ID, published

			b, err := svc.Build(ctx, base, tc.changes)
			if err != nil {
				t.Fatalf("Build: %v", err)
			}
			if b.Base != base || b.Sets != len(tc.want) || b.MaxAge != 300 || b.Engine == nil || b.SignerKeyID == "" {
				t.Errorf("built base %q sets %d max age %d signer %q, want base %q, %d sets, 300 and a signer",
					b.Base, b.Sets, b.MaxAge, b.SignerKeyID, base, len(tc.want))
			}
			_, snap, err := policy.OpenSnapshot(b.Blob, b.ID, svc.publicLookup(ctx))
			if err != nil {
				t.Fatalf("the built snapshot does not open with the service's keys: %v", err)
			}
			got := map[string]string{}
			for _, raw := range snap.Documents {
				doc, err := policy.Parse(raw)
				if err != nil {
					t.Fatal(err)
				}
				got[doc.Metadata.Name] = string(raw)
			}
			if len(got) != len(tc.want) {
				t.Errorf("built sets %v, want %v", keys(got), keys(tc.want))
			}
			for name, text := range tc.want {
				if got[name] != text {
					t.Errorf("set %s in the built snapshot is %q, want %q", name, got[name], text)
				}
			}
			if act, err := st.Snapshots().GetActive(ctx); err != nil || act.ID != base {
				t.Errorf("active snapshot %q (%v) after Build, want %q unchanged", act.ID, err, base)
			}
			if _, err := st.Snapshots().GetByID(ctx, b.ID); !errors.Is(err, store.ErrNotFound) {
				t.Errorf("Build stored its snapshot (lookup err %v), want nothing stored", err)
			}
			if svc.Current().ID != base || published != emitted {
				t.Errorf("Build swapped to %q or emitted %d events, want %q live and nothing emitted", svc.Current().ID, published-emitted, base)
			}
		})
	}
}

func keys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// TestBuildRefuses pins every refusal of Build: each says what failed and
// what to do, and none stores, activates or swaps anything.
func TestBuildRefuses(t *testing.T) {
	cases := []struct {
		name    string
		prepare func(t *testing.T, st store.Store, svc *Service) (base string)
		changes map[string][]byte
		want    []string
	}{
		{
			name: "no active snapshot",
			prepare: func(*testing.T, store.Store, *Service) string {
				return ""
			},
			changes: map[string][]byte{"block-rm": []byte(rmPolicy)},
			want:    []string{"snapshot: publish block-rm: no policy snapshot is active", "Restart strazad so it builds its snapshot, then publish again"},
		},
		{
			name: "an active snapshot the keys cannot open",
			prepare: func(t *testing.T, st store.Store, _ *Service) string {
				if _, err := st.Snapshots().Create(context.Background(), store.Snapshot{ID: "corrupt", SignerKeyID: "unknown", Blob: []byte("not a snapshot")}); err != nil {
					t.Fatal(err)
				}
				if err := st.Snapshots().SetActive(context.Background(), "corrupt"); err != nil {
					t.Fatal(err)
				}
				return "corrupt"
			},
			changes: map[string][]byte{"block-rm": []byte(rmPolicy)},
			want:    []string{"the active snapshot corrupt cannot be opened with the snapshot signing keys", "restore both from a backup, then publish again"},
		},
		{
			name: "a base that is no longer active",
			prepare: func(t *testing.T, _ store.Store, svc *Service) string {
				if err := svc.Load(context.Background()); err != nil {
					t.Fatal(err)
				}
				return "an-older-snapshot"
			},
			changes: map[string][]byte{"block-rm": []byte(rmPolicy), "block-push": nil},
			want: []string{"snapshot: publish block-push, block-rm: the change was checked against the policy snapshot an-older-snapshot",
				"so nothing was built. Check the change again, then publish it"},
		},
		{
			name: "a text that names another set",
			prepare: func(t *testing.T, _ store.Store, svc *Service) string {
				if err := svc.Load(context.Background()); err != nil {
					t.Fatal(err)
				}
				return svc.Current().ID
			},
			changes: map[string][]byte{"block-curl": []byte(pushPolicyEdited)},
			want:    []string{"snapshot: publish block-curl: the text given for the set block-curl names the set block-push, so nothing was built"},
		},
		{
			name: "a text that does not parse",
			prepare: func(t *testing.T, _ store.Store, svc *Service) string {
				if err := svc.Load(context.Background()); err != nil {
					t.Fatal(err)
				}
				return svc.Current().ID
			},
			changes: map[string][]byte{"block-curl": []byte("kind: NotAPolicySet")},
			want:    []string{"snapshot: publish block-curl: the text given for the set block-curl does not parse, so nothing was built"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			st := testStore(t)
			svc := New(st, testSigner(t, st), nil, policy.EffectAllow, 300, nil)
			createSet(t, st, "block-rm", "active", rmPolicy)
			createSet(t, st, "block-push", "active", pushPolicy)
			base := tc.prepare(t, st, svc)
			before := svc.Current()
			activeBefore, _ := st.Snapshots().GetActive(ctx)
			storedBefore, err := st.Snapshots().List(ctx)
			if err != nil {
				t.Fatal(err)
			}

			_, err = svc.Build(ctx, base, tc.changes)
			if err == nil {
				t.Fatal("Build succeeded, want a refusal")
			}
			for _, w := range tc.want {
				if !strings.Contains(err.Error(), w) {
					t.Errorf("refusal %q lacks %q", err, w)
				}
			}
			if svc.Current() != before {
				t.Error("a refused Build swapped the live snapshot")
			}
			activeAfter, _ := st.Snapshots().GetActive(ctx)
			storedAfter, err := st.Snapshots().List(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if activeAfter.ID != activeBefore.ID || len(storedAfter) != len(storedBefore) {
				t.Errorf("a refused Build moved the store: active %q to %q, %d to %d snapshots",
					activeBefore.ID, activeAfter.ID, len(storedBefore), len(storedAfter))
			}
		})
	}
}

// TestAdoptSwapsWithoutEmitting pins Adopt: once the snapshot Build made
// is stored and active, as the publish transaction leaves it, Adopt makes
// it the live snapshot, logs the swap with the source publish, and emits
// nothing, because the transaction carried straza.policy.updated.
func TestAdoptSwapsWithoutEmitting(t *testing.T) {
	ctx := context.Background()
	var logs bytes.Buffer
	published := 0
	st := testStore(t)
	svc := New(st, testSigner(t, st), func(context.Context, string, map[string]any) { published++ },
		policy.EffectAllow, 300, slog.New(slog.NewTextHandler(&logs, nil)))
	createSet(t, st, "block-rm", "active", rmPolicy)
	if err := svc.Load(ctx); err != nil {
		t.Fatal(err)
	}
	base, emitted := svc.Current().ID, published
	b, err := svc.Build(ctx, base, map[string][]byte{"block-rm": []byte(rmPolicyEdited)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.Snapshots().Create(ctx, store.Snapshot{ID: b.ID, SignerKeyID: b.SignerKeyID, Blob: b.Blob}); err != nil {
		t.Fatal(err)
	}
	if err := st.Snapshots().SetActiveFrom(ctx, b.ID, b.Base); err != nil {
		t.Fatal(err)
	}
	logs.Reset()

	svc.Adopt(b)
	cur := svc.Current()
	if cur.ID != b.ID || !bytes.Equal(cur.Signed, b.Blob) || cur.MaxAge != b.MaxAge {
		t.Fatalf("live snapshot %q after Adopt, want the built %q", cur.ID, b.ID)
	}
	if d := evaluate(t, cur, "rm -rf /tmp/x"); d.Reason != "Saved but not published" {
		t.Errorf("the adopted snapshot decides with %q, want the built text", d.Reason)
	}
	if published != emitted {
		t.Errorf("Adopt emitted %d events, want none", published-emitted)
	}
	if got := logs.String(); !strings.Contains(got, `msg="policy snapshot swapped"`) || !hasAttr(got, "source", "publish") || !hasAttr(got, "previous", base) {
		t.Errorf("Adopt logged %q, want the swap from %s with source publish", got, base)
	}
}
