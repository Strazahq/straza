package store

import (
	"context"
	"errors"
	"os"
	"sync"
	"testing"

	"github.com/strazahq/straza/internal/config"
)

func purposeKey(purpose, kid, status string) SigningKey {
	return SigningKey{KID: kid, Purpose: purpose, Status: status, PrivateKey: []byte{1}, PublicKey: []byte{2}}
}

// TestOneActiveSigningKeyPerPurpose pins what the database refuses for every
// signing key purpose, on both engines: a second active key of the purpose.
// Keys of the other statuses stay free, so a staged session key waits beside
// the active one and any number of retiring and retired keys keep verifying.
func TestOneActiveSigningKeyPerPurpose(t *testing.T) {
	forEachStore(t, func(t *testing.T, s Store) {
		ctx := context.Background()
		repo := s.SigningKeys()
		cases := []struct {
			name     string
			key      SigningKey
			conflict bool
		}{
			{"the first active session key", purposeKey(KeyPurposeSession, "ses-1", KeyActive), false},
			{"a second active session key", purposeKey(KeyPurposeSession, "ses-2", KeyActive), true},
			{"a staged session key beside the active one", purposeKey(KeyPurposeSession, "ses-3", KeyStaged), false},
			{"a second staged session key", purposeKey(KeyPurposeSession, "ses-4", KeyStaged), false},
			{"a retiring session key", purposeKey(KeyPurposeSession, "ses-5", KeyRetiring), false},
			{"a second retiring session key", purposeKey(KeyPurposeSession, "ses-6", KeyRetiring), false},
			{"a retired session key", purposeKey(KeyPurposeSession, "ses-7", KeyRetired), false},
			{"a second retired session key", purposeKey(KeyPurposeSession, "ses-8", KeyRetired), false},
			{"the first active snapshot key", purposeKey(KeyPurposeSnapshot, "snap-1", KeyActive), false},
			{"a second active snapshot key", purposeKey(KeyPurposeSnapshot, "snap-2", KeyActive), true},
			{"a retiring snapshot key", purposeKey(KeyPurposeSnapshot, "snap-3", KeyRetiring), false},
			{"a second retiring snapshot key", purposeKey(KeyPurposeSnapshot, "snap-4", KeyRetiring), false},
			{"a retired snapshot key", purposeKey(KeyPurposeSnapshot, "snap-5", KeyRetired), false},
			{"the first active client assertion key", purposeKey(KeyPurposeClientAssertion, "ca-1", KeyActive), false},
			{"a second active client assertion key", purposeKey(KeyPurposeClientAssertion, "ca-2", KeyActive), true},
		}
		for _, tc := range cases {
			_, err := repo.Create(ctx, tc.key)
			switch {
			case tc.conflict && !errors.Is(err, ErrConflict):
				t.Errorf("%s: err = %v, want ErrConflict", tc.name, err)
			case !tc.conflict && err != nil:
				t.Errorf("%s: %v, want it accepted", tc.name, err)
			}
		}
		for _, purpose := range []string{KeyPurposeSession, KeyPurposeSnapshot, KeyPurposeClientAssertion} {
			if active := activeKeys(t, s, purpose); len(active) != 1 {
				t.Errorf("%s: %d active keys, want 1", purpose, len(active))
			}
		}
	})
}

// activeKeys answers the kids of the active keys of purpose, in the order
// ListByPurpose reads them.
func activeKeys(t *testing.T, s Store, purpose string) []string {
	t.Helper()
	keys, err := s.SigningKeys().ListByPurpose(context.Background(), purpose)
	if err != nil {
		t.Fatalf("list %s keys: %v", purpose, err)
	}
	var out []string
	for _, k := range keys {
		if k.Status == KeyActive {
			out = append(out, k.KID)
		}
	}
	return out
}

// TestOneActiveSigningKeyConcurrentReplicas is the two-pods proof at the
// store for the session and the snapshot key: eight writers race to create
// the active key of an empty purpose, and the database lets exactly one
// through. On Postgres the writers use two handles, which is two replicas on
// one database.
func TestOneActiveSigningKeyConcurrentReplicas(t *testing.T) {
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
		for _, purpose := range []string{KeyPurposeSession, KeyPurposeSnapshot} {
			const writers = 8
			var wg sync.WaitGroup
			var mu sync.Mutex
			created := 0
			start := make(chan struct{})
			for i := range writers {
				wg.Add(1)
				go func() {
					defer wg.Done()
					<-start
					_, err := handles[i%2].SigningKeys().Create(ctx, purposeKey(purpose, "", KeyActive))
					switch {
					case errors.Is(err, ErrConflict):
					case err != nil:
						t.Errorf("%s writer %d: %v", purpose, i, err)
					default:
						mu.Lock()
						created++
						mu.Unlock()
					}
				}()
			}
			close(start)
			wg.Wait()
			if created != 1 {
				t.Errorf("%d writers created an active %s key, want exactly 1", created, purpose)
			}
			if active := activeKeys(t, s, purpose); len(active) != 1 {
				t.Errorf("%s: %d active keys after the race, want 1", purpose, len(active))
			}
		}
	})
}
