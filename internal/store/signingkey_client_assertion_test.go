package store

import (
	"context"
	"errors"
	"os"
	"sync"
	"testing"

	"github.com/strazahq/straza/internal/config"
)

func clientAssertionKey(kid, status string) SigningKey {
	return SigningKey{KID: kid, Purpose: KeyPurposeClientAssertion, Status: status, PrivateKey: []byte{1}, PublicKey: []byte{2}}
}

func keyStatus(t *testing.T, s Store, kid string) string {
	t.Helper()
	k, err := s.SigningKeys().Get(context.Background(), kid)
	if err != nil {
		t.Fatalf("get %s: %v", kid, err)
	}
	return k.Status
}

// TestClientAssertionKeyGuards pins what the database itself refuses for the
// client assertion purpose, on both engines: a second staged key, a second
// active key and a kid another purpose holds. It also pins that the session
// purpose still takes two staged keys, because only its active key is guarded.
func TestClientAssertionKeyGuards(t *testing.T) {
	forEachStore(t, func(t *testing.T, s Store) {
		ctx := context.Background()
		repo := s.SigningKeys()
		if _, err := repo.Create(ctx, clientAssertionKey("ca-1", KeyStaged)); err != nil {
			t.Fatalf("first staged key: %v", err)
		}
		session, err := repo.Create(ctx, SigningKey{Purpose: KeyPurposeSession, PrivateKey: []byte{1}, PublicKey: []byte{2}})
		if err != nil {
			t.Fatalf("session key: %v", err)
		}
		refused := []struct {
			name string
			key  SigningKey
		}{
			{"a second staged key", clientAssertionKey("ca-2", KeyStaged)},
			{"a kid the session purpose holds", clientAssertionKey(session.KID, KeyRetired)},
		}
		for _, tc := range refused {
			if _, err := repo.Create(ctx, tc.key); !errors.Is(err, ErrConflict) {
				t.Errorf("%s: err = %v, want ErrConflict", tc.name, err)
			}
		}
		if _, err := repo.Create(ctx, clientAssertionKey("ca-active", KeyActive)); err != nil {
			t.Fatalf("first active key: %v", err)
		}
		if _, err := repo.Create(ctx, clientAssertionKey("ca-active-2", KeyActive)); !errors.Is(err, ErrConflict) {
			t.Errorf("a second active key: err = %v, want ErrConflict", err)
		}
		if _, err := repo.Create(ctx, SigningKey{KID: "bad", Purpose: "banana", PrivateKey: []byte{1}, PublicKey: []byte{2}}); err == nil {
			t.Error("an unknown purpose was accepted: the purpose CHECK is missing")
		}
		for _, kid := range []string{"ses-staged-1", "ses-staged-2"} {
			if _, err := repo.Create(ctx, SigningKey{KID: kid, Purpose: KeyPurposeSession, Status: KeyStaged, PrivateKey: []byte{1}, PublicKey: []byte{2}}); err != nil {
				t.Errorf("session staged key %s: %v, want the session purpose unguarded as before", kid, err)
			}
		}
	})
}

// TestSigningKeyPromoteAndRetire walks the two compare-and-set writes: Promote
// makes a staged key the only active key of its purpose in one transaction,
// and Retire moves one key from a named state to retired. Each reports
// whether this call made the change, and a call whose precondition no longer
// holds changes nothing.
func TestSigningKeyPromoteAndRetire(t *testing.T) {
	forEachStore(t, func(t *testing.T, s Store) {
		ctx := context.Background()
		repo := s.SigningKeys()
		create := func(kid, status string) {
			t.Helper()
			if _, err := repo.Create(ctx, clientAssertionKey(kid, status)); err != nil {
				t.Fatalf("create %s: %v", kid, err)
			}
		}
		steps := []struct {
			name string
			do   func() (bool, error)
			want bool
			// statuses after the step
			after map[string]string
		}{
			{"the first key is promoted with no active key before it",
				func() (bool, error) { create("k1", KeyStaged); return repo.Promote(ctx, "k1") },
				true, map[string]string{"k1": KeyActive}},
			{"a second promote of the same key changes nothing",
				func() (bool, error) { return repo.Promote(ctx, "k1") },
				false, map[string]string{"k1": KeyActive}},
			{"promoting the next key demotes the active one",
				func() (bool, error) { create("k2", KeyStaged); return repo.Promote(ctx, "k2") },
				true, map[string]string{"k1": KeyRetiring, "k2": KeyActive}},
			{"a stale promote of a retiring key leaves the active key alone",
				func() (bool, error) { return repo.Promote(ctx, "k1") },
				false, map[string]string{"k1": KeyRetiring, "k2": KeyActive}},
			{"a retire from the wrong state changes nothing",
				func() (bool, error) { return repo.Retire(ctx, "k1", KeyActive) },
				false, map[string]string{"k1": KeyRetiring, "k2": KeyActive}},
			{"a retire from the right state retires the key",
				func() (bool, error) { return repo.Retire(ctx, "k1", KeyRetiring) },
				true, map[string]string{"k1": KeyRetired, "k2": KeyActive}},
			{"the same retire is not repeated",
				func() (bool, error) { return repo.Retire(ctx, "k1", KeyRetiring) },
				false, map[string]string{"k1": KeyRetired, "k2": KeyActive}},
			{"a staged key retired before its promotion keeps the active key signing",
				func() (bool, error) {
					create("k3", KeyStaged)
					if ok, err := repo.Retire(ctx, "k3", KeyStaged); err != nil || !ok {
						t.Fatalf("retire staged k3 = %v, %v", ok, err)
					}
					return repo.Promote(ctx, "k3")
				},
				false, map[string]string{"k2": KeyActive, "k3": KeyRetired}},
			{"an unknown kid is no transition",
				func() (bool, error) { return repo.Promote(ctx, "nope") },
				false, map[string]string{"k2": KeyActive}},
		}
		for _, st := range steps {
			got, err := st.do()
			if err != nil {
				t.Fatalf("%s: %v", st.name, err)
			}
			if got != st.want {
				t.Errorf("%s: changed = %v, want %v", st.name, got, st.want)
			}
			for kid, want := range st.after {
				if status := keyStatus(t, s, kid); status != want {
					t.Errorf("%s: %s is %s, want %s", st.name, kid, status, want)
				}
			}
		}
		k1, err := repo.Get(ctx, "k1")
		if err != nil || k1.RotatedAt == nil {
			t.Errorf("k1 after its demotion = %+v (%v), want rotated_at stamped", k1, err)
		}
	})
}

// TestClientAssertionKeyConcurrentReplicas is the two-pods proof at the
// store: eight writers race to stage a key and then race to promote it, and
// the database lets exactly one of each through. On Postgres the writers use
// two handles, which is two replicas on one database.
func TestClientAssertionKeyConcurrentReplicas(t *testing.T) {
	forEachStore(t, func(t *testing.T, s Store) {
		ctx := context.Background()
		handles := []Store{s, s}
		if s.(*sqlStore).d == dialectPostgres {
			peer, err := Open(config.Config{Store: config.Store{
				Driver: config.DriverPostgres, DSN: os.Getenv("STRAZA_TEST_POSTGRES_DSN"),
			}})
			if err != nil {
				t.Fatalf("open the second replica's handle: %v", err)
			}
			t.Cleanup(func() { _ = peer.Close() })
			handles[1] = peer
		}
		const writers = 8
		race := func(fn func(i int, repo SigningKeyRepo) (bool, error)) int {
			t.Helper()
			var wg sync.WaitGroup
			var mu sync.Mutex
			won := 0
			start := make(chan struct{})
			for i := range writers {
				wg.Add(1)
				go func() {
					defer wg.Done()
					<-start
					ok, err := fn(i, handles[i%2].SigningKeys())
					if err != nil {
						t.Errorf("writer %d: %v", i, err)
					}
					if ok {
						mu.Lock()
						won++
						mu.Unlock()
					}
				}()
			}
			close(start)
			wg.Wait()
			return won
		}

		staged := race(func(i int, repo SigningKeyRepo) (bool, error) {
			_, err := repo.Create(ctx, clientAssertionKey("", KeyStaged))
			if errors.Is(err, ErrConflict) {
				return false, nil
			}
			return err == nil, err
		})
		if staged != 1 {
			t.Fatalf("%d writers staged a key, want exactly 1", staged)
		}
		keys, err := s.SigningKeys().ListByPurpose(ctx, KeyPurposeClientAssertion)
		if err != nil || len(keys) != 1 {
			t.Fatalf("keys after the staging race = %d (%v), want 1", len(keys), err)
		}
		promoted := race(func(_ int, repo SigningKeyRepo) (bool, error) {
			return repo.Promote(ctx, keys[0].KID)
		})
		if promoted != 1 {
			t.Errorf("%d writers report the promotion, want exactly 1", promoted)
		}
		if status := keyStatus(t, s, keys[0].KID); status != KeyActive {
			t.Errorf("the key is %s after the promotion race, want active", status)
		}
	})
}
