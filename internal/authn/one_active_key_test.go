package authn

import (
	"context"
	"crypto/ed25519"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/strazahq/straza/internal/store"
)

// raceRepo is one store that two replicas share. Once armed, it holds the
// first two ListByPurpose calls until both have read, so the two replicas
// act on the same read of the store, as two pods that start at once do.
type raceRepo struct {
	store.SigningKeyRepo

	mu    sync.Mutex
	armed bool
	held  int
	both  chan struct{}
}

func (r *raceRepo) arm() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.armed, r.held, r.both = true, 0, make(chan struct{})
}

func (r *raceRepo) ListByPurpose(ctx context.Context, purpose string) ([]store.SigningKey, error) {
	keys, err := r.SigningKeyRepo.ListByPurpose(ctx, purpose)
	r.mu.Lock()
	if !r.armed {
		r.mu.Unlock()
		return keys, err
	}
	r.held++
	both := r.both
	if r.held == 2 {
		r.armed = false
		close(both)
	}
	r.mu.Unlock()
	<-both
	return keys, err
}

// replica is one strazad's keys of one purpose as these tests see them:
// what it signs, whether it accepts what a peer signed, and the key ids it
// publishes.
type replica struct {
	sign      func(t *testing.T) (kid string, proof []byte)
	accept    func(kid string, proof []byte) error
	published func(t *testing.T) map[string]bool
}

type bootFunc func(ctx context.Context, repo store.SigningKeyRepo) (replica, error)

// keyLoaders boots one replica's keys of each purpose that a simultaneous
// first boot can race on, the way strazad does at start.
var keyLoaders = []struct {
	purpose string
	boot    bootFunc
}{
	{store.KeyPurposeSession, bootSessionReplica},
	{store.KeyPurposeSnapshot, bootSnapshotReplica},
}

func bootSessionReplica(ctx context.Context, repo store.SigningKeyRepo) (replica, error) {
	svc, err := NewTokenService(ctx, repo, "https://straza.local", 0)
	if err != nil {
		return replica{}, err
	}
	return replica{
		sign: func(t *testing.T) (string, []byte) {
			t.Helper()
			raw, _, err := svc.Mint(sampleClaims())
			if err != nil {
				t.Fatal(err)
			}
			return kidOf(t, raw), []byte(raw)
		},
		accept: func(_ string, proof []byte) error {
			_, err := svc.Verify(string(proof))
			return err
		},
		published: func(t *testing.T) map[string]bool { return jwksKIDs(t, svc) },
	}, nil
}

// snapshotMessage is what a snapshot replica signs in these tests.
var snapshotMessage = []byte("policy snapshot")

func bootSnapshotReplica(ctx context.Context, repo store.SigningKeyRepo) (replica, error) {
	keys, err := NewSnapshotKeys(ctx, repo)
	if err != nil {
		return replica{}, err
	}
	return replica{
		sign: func(t *testing.T) (string, []byte) {
			t.Helper()
			kid, priv, err := keys.Active(ctx)
			if err != nil {
				t.Fatal(err)
			}
			return kid, ed25519.Sign(priv, snapshotMessage)
		},
		accept: func(kid string, proof []byte) error {
			pub, ok := keys.Public(ctx, kid)
			if !ok {
				return fmt.Errorf("unknown snapshot key %s", kid)
			}
			if !ed25519.Verify(pub, snapshotMessage, proof) {
				return errors.New("the snapshot signature does not verify")
			}
			return nil
		},
		published: func(*testing.T) map[string]bool {
			out := map[string]bool{}
			for kid := range keys.PublicKeys() {
				out[kid] = true
			}
			return out
		},
	}, nil
}

// bootAtOnce boots two replicas over repo so that both read the store
// before either writes to it.
func bootAtOnce(t *testing.T, repo *raceRepo, boot bootFunc) [2]replica {
	t.Helper()
	repo.arm()
	var out [2]replica
	var errs [2]error
	var wg sync.WaitGroup
	for i := range out {
		wg.Add(1)
		go func() {
			defer wg.Done()
			out[i], errs[i] = boot(context.Background(), repo)
		}()
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("replica %d did not boot: %v", i, err)
		}
	}
	return out
}

// activeKIDs answers the kids of the active keys of purpose in the store.
func activeKIDs(t *testing.T, repo store.SigningKeyRepo, purpose string) []string {
	t.Helper()
	keys, err := repo.ListByPurpose(context.Background(), purpose)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, k := range keys {
		if k.Status == store.KeyActive {
			out = append(out, k.KID)
		}
	}
	return out
}

// TestBootRaceConvergesOnOneKey pins the first boot of two replicas on an
// empty store, for the session and the snapshot key. Both read the store
// before either writes, the database takes one active key, and the replica
// whose write it refused signs with the key its peer wrote. So each replica
// accepts what the other signs.
func TestBootRaceConvergesOnOneKey(t *testing.T) {
	for _, loader := range keyLoaders {
		t.Run(loader.purpose, func(t *testing.T) {
			repo := &raceRepo{SigningKeyRepo: testRepo(t)}
			pair := bootAtOnce(t, repo, loader.boot)
			active := activeKIDs(t, repo, loader.purpose)
			if len(active) != 1 {
				t.Fatalf("the store holds %d active %s keys after the race, want 1", len(active), loader.purpose)
			}
			kidA, proofA := pair[0].sign(t)
			kidB, proofB := pair[1].sign(t)
			if kidA != active[0] || kidB != active[0] {
				t.Errorf("the replicas sign with %s and %s, want both with the stored key %s", kidA, kidB, active[0])
			}
			if err := pair[0].accept(kidB, proofB); err != nil {
				t.Errorf("replica A refuses what replica B signed: %v", err)
			}
			if err := pair[1].accept(kidA, proofA); err != nil {
				t.Errorf("replica B refuses what replica A signed: %v", err)
			}
		})
	}
}

// TestAdvanceRaceOnePromotion pins two replicas that run the rotation step
// in the same tick on the same read of the store. The promotion is one
// compare-and-set, so exactly one replica reports it, and the store holds
// one active session key, the staged one, with the old one retiring.
func TestAdvanceRaceOnePromotion(t *testing.T) {
	ctx := context.Background()
	repo := &raceRepo{SigningKeyRepo: testRepo(t)}
	var svcs [2]*TokenService
	for i := range svcs {
		svc, err := NewTokenService(ctx, repo, "https://straza.local", 0)
		if err != nil {
			t.Fatalf("replica %d: %v", i, err)
		}
		svcs[i] = svc
	}
	old := activeKIDs(t, repo, store.KeyPurposeSession)
	if len(old) != 1 {
		t.Fatalf("%d active session keys before the rotation, want 1", len(old))
	}
	staged, err := svcs[0].Stage(ctx)
	if err != nil {
		t.Fatalf("Stage: %v", err)
	}

	now := time.Now().Add(2*KeyReloadInterval + time.Second)
	repo.arm()
	var steps [2]Advance
	var errs [2]error
	var wg sync.WaitGroup
	for i := range svcs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			steps[i], errs[i] = svcs[i].Advance(ctx, now, time.Hour)
		}()
	}
	wg.Wait()
	promoted := 0
	for i := range steps {
		if errs[i] != nil {
			t.Errorf("replica %d: Advance: %v", i, errs[i])
		}
		switch steps[i].Promoted {
		case staged.KID:
			promoted++
		case "":
		default:
			t.Errorf("replica %d promoted %s, want %s", i, steps[i].Promoted, staged.KID)
		}
	}
	if promoted != 1 {
		t.Errorf("%d replicas report the promotion, want exactly 1", promoted)
	}
	want := map[string]string{old[0]: store.KeyRetiring, staged.KID: store.KeyActive}
	got := keyStatuses(t, repo)
	for kid, status := range want {
		if got[kid] != status {
			t.Errorf("key %s is %q after the race, want %q", kid, got[kid], status)
		}
	}
	if len(got) != len(want) {
		t.Errorf("the store holds %d session keys, want %d: %v", len(got), len(want), got)
	}
}

// vanishedPeerRepo is a store where a peer wrote the active key between the
// loader's read and its write, and the key was no longer active at the
// loader's second read: every list is empty and every create is refused.
type vanishedPeerRepo struct{ store.SigningKeyRepo }

func (vanishedPeerRepo) ListByPurpose(context.Context, string) ([]store.SigningKey, error) {
	return nil, nil
}

func (vanishedPeerRepo) Create(context.Context, store.SigningKey) (store.SigningKey, error) {
	return store.SigningKey{}, fmt.Errorf("%w: UNIQUE constraint failed", store.ErrConflict)
}

// TestLoadersFailClosedWhenThePeerKeyVanished pins that a loader whose write
// the database refused, and that then finds no active key, starts without a
// key to sign with and says so, instead of signing with nothing.
func TestLoadersFailClosedWhenThePeerKeyVanished(t *testing.T) {
	cases := []struct {
		purpose string
		boot    bootFunc
		want    string
	}{
		{store.KeyPurposeSession, bootSessionReplica,
			"authn: another replica created the session signing key at the same moment, and that key is no longer active, " +
				"so this replica has no key to sign with. Start this replica again, and it loads the current key when it starts"},
		{store.KeyPurposeSnapshot, bootSnapshotReplica,
			"authn: another replica created the snapshot signing key at the same moment, and that key is no longer active, " +
				"so this replica has no key to sign policy snapshots with. Start this replica again, and it loads the current key when it starts"},
	}
	for _, tc := range cases {
		t.Run(tc.purpose, func(t *testing.T) {
			_, err := tc.boot(context.Background(), vanishedPeerRepo{})
			if err == nil || err.Error() != tc.want {
				t.Fatalf("err = %v\nwant %s", err, tc.want)
			}
		})
	}
}
