package store

import (
	"bytes"
	"context"
	"testing"
)

// TestReplaceSigningKeyPrivate pins the compare-and-set write of a signing
// key's private column on both engines: it changes the row only while the
// column still holds the value the caller read, so of two replicas sealing
// the same key exactly one writes.
func TestReplaceSigningKeyPrivate(t *testing.T) {
	forEachStore(t, func(t *testing.T, s Store) {
		ctx := context.Background()
		repo := s.SigningKeys()
		raw, sealed, other := []byte("raw seed"), []byte("sealed value"), []byte("a peer's sealed value")
		if _, err := repo.Create(ctx, SigningKey{KID: "ses-1", Purpose: KeyPurposeSession, Status: KeyActive, PrivateKey: raw, PublicKey: []byte{2}}); err != nil {
			t.Fatal(err)
		}
		cases := []struct {
			name     string
			kid      string
			from, to []byte
			changed  bool
			stored   []byte
		}{
			{"a caller that read another value", "ses-1", other, sealed, false, raw},
			{"the first caller", "ses-1", raw, sealed, true, sealed},
			{"a second caller with the same read", "ses-1", raw, other, false, sealed},
			{"the way back", "ses-1", sealed, raw, true, raw},
			{"a key that does not exist", "ses-9", raw, sealed, false, raw},
		}
		for _, tc := range cases {
			changed, err := repo.ReplacePrivateKey(ctx, tc.kid, tc.from, tc.to)
			if err != nil {
				t.Fatalf("%s: %v", tc.name, err)
			}
			if changed != tc.changed {
				t.Errorf("%s: changed = %v, want %v", tc.name, changed, tc.changed)
			}
			got, err := repo.Get(ctx, "ses-1")
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(got.PrivateKey, tc.stored) {
				t.Errorf("%s: the row holds %q, want %q", tc.name, got.PrivateKey, tc.stored)
			}
			if got.Status != KeyActive || !bytes.Equal(got.PublicKey, []byte{2}) {
				t.Errorf("%s: the write changed more than the private column: %+v", tc.name, got)
			}
		}
	})
}
