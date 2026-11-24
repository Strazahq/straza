package authn

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/rsa"
	"strings"
	"testing"
	"time"

	"github.com/lestrrat-go/jwx/v3/jwa"
	"github.com/lestrrat-go/jwx/v3/jwt"

	"github.com/strazahq/straza/internal/store"
)

// seedKey is one ed25519 key a test stores by hand.
type seedKey struct {
	pub  ed25519.PublicKey
	priv ed25519.PrivateKey
}

func newSeedKey(t *testing.T) seedKey {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return seedKey{pub: pub, priv: priv}
}

// loadSeedKeys runs the loader of purpose over repo, the way a strazad start
// does.
func loadSeedKeys(repo store.SigningKeyRepo, sealer KeySealer, purpose string) error {
	var err error
	if purpose == store.KeyPurposeSession {
		_, err = NewTokenService(context.Background(), repo, sealer, "https://straza.local", 0)
	} else {
		_, err = NewSnapshotKeys(context.Background(), repo, sealer)
	}
	return err
}

func storedKey(t *testing.T, repo store.SigningKeyRepo, kid string) store.SigningKey {
	t.Helper()
	rec, err := repo.Get(context.Background(), kid)
	if err != nil {
		t.Fatal(err)
	}
	return rec
}

// wantSealed fails unless the row kid holds seed sealed under sealer with the
// header of its purpose and key id.
func wantSealed(t *testing.T, repo store.SigningKeyRepo, sealer KeySealer, kid string, seed []byte) {
	t.Helper()
	rec := storedKey(t, repo, kid)
	if len(rec.PrivateKey) == ed25519.SeedSize || bytes.Contains(rec.PrivateKey, seed) {
		t.Fatalf("key %s: the row still holds the raw seed", kid)
	}
	plain, err := sealer.Open(rec.PrivateKey)
	if err != nil {
		t.Fatalf("key %s: the stored value does not open: %v", kid, err)
	}
	if want := append([]byte(rec.Purpose+" "+kid+"\n"), seed...); !bytes.Equal(plain, want) {
		t.Fatalf("key %s: the sealed plaintext is not the header and the seed", kid)
	}
}

// TestNewSeedsAreSealed pins that no session or snapshot key reaches the
// store as a raw seed: not the first key of a new store and not a staged one.
func TestNewSeedsAreSealed(t *testing.T) {
	ctx := context.Background()
	repo, sealer := testRepo(t), testSealer(t)
	svc, err := NewTokenService(ctx, repo, sealer, "https://straza.local", 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Stage(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := NewSnapshotKeys(ctx, repo, sealer); err != nil {
		t.Fatal(err)
	}
	for purpose, want := range map[string]int{store.KeyPurposeSession: 2, store.KeyPurposeSnapshot: 1} {
		recs, err := repo.ListByPurpose(ctx, purpose)
		if err != nil {
			t.Fatal(err)
		}
		if len(recs) != want {
			t.Fatalf("%s: %d keys, want %d", purpose, len(recs), want)
		}
		for _, rec := range recs {
			plain, err := sealer.Open(rec.PrivateKey)
			if err != nil {
				t.Fatalf("%s key %s is not sealed under the KEK: %v", purpose, rec.KID, err)
			}
			seed, ok := bytes.CutPrefix(plain, []byte(purpose+" "+rec.KID+"\n"))
			if !ok || len(seed) != ed25519.SeedSize {
				t.Fatalf("%s key %s: the sealed plaintext is not the header and a seed", purpose, rec.KID)
			}
			if !bytes.Equal(ed25519.NewKeyFromSeed(seed).Public().(ed25519.PublicKey), rec.PublicKey) {
				t.Fatalf("%s key %s: the sealed seed is not the key of the public column", purpose, rec.KID)
			}
		}
	}
}

// TestRawSeedsAreSealedInPlace pins the upgrade of a store that holds raw
// seeds: the first load seals every row where it sits, the retired one too,
// and the keys stay the same keys, so a token signed before the upgrade still
// verifies and the snapshot key a machine pinned still signs.
func TestRawSeedsAreSealedInPlace(t *testing.T) {
	ctx := context.Background()
	repo, sealer := testRepo(t), testSealer(t)
	ses, old, snap := newSeedKey(t), newSeedKey(t), newSeedKey(t)
	for _, rec := range []store.SigningKey{
		{KID: "ses", Purpose: store.KeyPurposeSession, Status: store.KeyActive, PrivateKey: ses.priv.Seed(), PublicKey: ses.pub},
		{KID: "old", Purpose: store.KeyPurposeSession, Status: store.KeyRetired, PrivateKey: old.priv.Seed(), PublicKey: old.pub},
		{KID: "snap", Purpose: store.KeyPurposeSnapshot, Status: store.KeyActive, PrivateKey: snap.priv.Seed(), PublicKey: snap.pub},
	} {
		if _, err := repo.Create(ctx, rec); err != nil {
			t.Fatal(err)
		}
	}
	// A token the server signed with the raw seed before it was sealed.
	rawKey, err := importPrivate("ses", ses.priv)
	if err != nil {
		t.Fatal(err)
	}
	tok, err := jwt.NewBuilder().Issuer("https://straza.local").Subject("user-1").JwtID("j").
		IssuedAt(time.Now()).Expiration(time.Now().Add(time.Minute)).
		Claim("ses", "ses-1").Claim("hrn", "claude-code/2.1.0").Claim("att", "managed").Claim("rol", "r").Claim("snp", "s").Build()
	if err != nil {
		t.Fatal(err)
	}
	before, err := jwt.Sign(tok, jwt.WithKey(jwa.EdDSA(), rawKey))
	if err != nil {
		t.Fatal(err)
	}

	svc, err := NewTokenService(ctx, repo, sealer, "https://straza.local", 0)
	if err != nil {
		t.Fatalf("NewTokenService over raw seeds: %v", err)
	}
	keys, err := NewSnapshotKeys(ctx, repo, sealer)
	if err != nil {
		t.Fatalf("NewSnapshotKeys over a raw seed: %v", err)
	}
	wantSealed(t, repo, sealer, "ses", ses.priv.Seed())
	wantSealed(t, repo, sealer, "old", old.priv.Seed())
	wantSealed(t, repo, sealer, "snap", snap.priv.Seed())

	if _, err := svc.Verify(string(before)); err != nil {
		t.Errorf("a token signed before the seed was sealed no longer verifies: %v", err)
	}
	kid, priv, err := keys.Active(ctx)
	if err != nil || kid != "snap" || !priv.Equal(snap.priv) {
		t.Errorf("the snapshot key changed: kid %q, err %v", kid, err)
	}
	if pub, ok := keys.Public(ctx, "snap"); !ok || !pub.Equal(snap.pub) {
		t.Error("the snapshot public key changed")
	}

	// A second start finds sealed rows and leaves them as they are.
	sealed := storedKey(t, repo, "ses").PrivateKey
	if err := loadSeedKeys(repo, sealer, store.KeyPurposeSession); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(storedKey(t, repo, "ses").PrivateKey, sealed) {
		t.Error("a second load wrote the sealed row again")
	}
}

// TestSealedSeedRefusals pins what a load of the session and the snapshot
// keys refuses, each as a whole: a seed it cannot open, a seed that belongs
// to another row or purpose, a seed that is not the key of its public column,
// and a load without the KEK.
func TestSealedSeedRefusals(t *testing.T) {
	sealer := testSealer(t)
	key, other := newSeedKey(t), newSeedKey(t)
	seal := func(s KeySealer, header string, seed []byte) []byte {
		out, err := s.Seal(append([]byte(header), seed...))
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	for _, purpose := range []string{store.KeyPurposeSession, store.KeyPurposeSnapshot} {
		otherPurpose := store.KeyPurposeSnapshot
		if purpose == store.KeyPurposeSnapshot {
			otherPurpose = store.KeyPurposeSession
		}
		cases := []struct {
			name    string
			private []byte
			public  ed25519.PublicKey
			sealer  KeySealer
			want    string
		}{
			{"sealed under another KEK", seal(testSealer(t), purpose+" k1\n", key.priv.Seed()), key.pub, sealer,
				"does not open: it was sealed under another secret.key or changed in the database. Put back the secret.key that belongs to this database"},
			{"the sealed seed of another row", seal(sealer, purpose+" k2\n", key.priv.Seed()), key.pub, sealer, "belongs to another key"},
			{"the sealed seed of another purpose", seal(sealer, otherPurpose+" k1\n", key.priv.Seed()), key.pub, sealer, "belongs to another key"},
			{"a sealed value that is no seed", seal(sealer, purpose+" k1\n", []byte("short")), key.pub, sealer, "is not an ed25519 seed"},
			{"a public column of another key", seal(sealer, purpose+" k1\n", key.priv.Seed()), other.pub, sealer, "is not the key's own public half"},
			{"a changed sealed value", append(seal(sealer, purpose+" k1\n", key.priv.Seed()), 0), key.pub, sealer, "does not open"},
			{"no KEK", seal(sealer, purpose+" k1\n", key.priv.Seed()), key.pub, nil, "need the secrets KEK"},
		}
		for _, tc := range cases {
			t.Run(purpose+"/"+tc.name, func(t *testing.T) {
				repo := testRepo(t)
				if _, err := repo.Create(context.Background(), store.SigningKey{
					KID: "k1", Purpose: purpose, Status: store.KeyActive, PrivateKey: tc.private, PublicKey: tc.public,
				}); err != nil {
					t.Fatal(err)
				}
				err := loadSeedKeys(repo, tc.sealer, purpose)
				if err == nil || !strings.Contains(err.Error(), tc.want) {
					t.Fatalf("err = %v, want one that says %q", err, tc.want)
				}
				if strings.Contains(err.Error(), string(key.priv.Seed())) {
					t.Fatal("the refusal carries key bytes")
				}
				if !bytes.Equal(storedKey(t, repo, "k1").PrivateKey, tc.private) {
					t.Fatal("a refused load changed the row")
				}
			})
		}
	}
}

// TestReloadKeepsKeysWhenASeedStopsOpening pins the running server: a reload
// that meets a session key it cannot open fails as a whole and leaves the
// keys of the last good load signing and verifying.
func TestReloadKeepsKeysWhenASeedStopsOpening(t *testing.T) {
	ctx := context.Background()
	repo, sealer := testRepo(t), testSealer(t)
	svc, err := NewTokenService(ctx, repo, sealer, "https://straza.local", 0)
	if err != nil {
		t.Fatal(err)
	}
	tok, _, err := svc.Mint(sampleClaims())
	if err != nil {
		t.Fatal(err)
	}
	recs, err := repo.ListByPurpose(ctx, store.KeyPurposeSession)
	if err != nil || len(recs) != 1 {
		t.Fatalf("session keys: %d, %v", len(recs), err)
	}
	foreign, err := testSealer(t).Seal([]byte("session " + recs[0].KID + "\nxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx"))
	if err != nil {
		t.Fatal(err)
	}
	if ok, err := repo.ReplacePrivateKey(ctx, recs[0].KID, recs[0].PrivateKey, foreign); err != nil || !ok {
		t.Fatalf("replace: %v, %v", ok, err)
	}
	if err := svc.Reload(ctx); err == nil || !strings.Contains(err.Error(), "does not open") {
		t.Fatalf("Reload = %v, want the refusal of a key that does not open", err)
	}
	if _, err := svc.Verify(tok); err != nil {
		t.Errorf("the failed reload dropped the verify set: %v", err)
	}
	if _, _, err := svc.Mint(sampleClaims()); err != nil {
		t.Errorf("the failed reload dropped the signing key: %v", err)
	}
}

// peerSealsFirst is a repository whose next compare-and-set write of a
// private column loses to a peer that seals the same row a moment earlier.
type peerSealsFirst struct {
	store.SigningKeyRepo
	peer KeySealer
	done bool
}

func (r *peerSealsFirst) ReplacePrivateKey(ctx context.Context, kid string, from, to []byte) (bool, error) {
	if !r.done {
		r.done = true
		rec, err := r.Get(ctx, kid)
		if err != nil {
			return false, err
		}
		sealed, err := r.peer.Seal(append([]byte(rec.Purpose+" "+kid+"\n"), from...))
		if err != nil {
			return false, err
		}
		if _, err := r.SigningKeyRepo.ReplacePrivateKey(ctx, kid, from, sealed); err != nil {
			return false, err
		}
	}
	return r.SigningKeyRepo.ReplacePrivateKey(ctx, kid, from, to)
}

// TestTwoReplicasSealOneRawSeed pins the first start of two replicas over one
// raw seed: the replica whose write loses loads the value its peer stored.
func TestTwoReplicasSealOneRawSeed(t *testing.T) {
	for _, purpose := range []string{store.KeyPurposeSession, store.KeyPurposeSnapshot} {
		t.Run(purpose, func(t *testing.T) {
			ctx := context.Background()
			sealer, key := testSealer(t), newSeedKey(t)
			repo := &peerSealsFirst{SigningKeyRepo: testRepo(t), peer: sealer}
			if _, err := repo.Create(ctx, store.SigningKey{
				KID: "k1", Purpose: purpose, Status: store.KeyActive, PrivateKey: key.priv.Seed(), PublicKey: key.pub,
			}); err != nil {
				t.Fatal(err)
			}
			if err := loadSeedKeys(repo, sealer, purpose); err != nil {
				t.Fatalf("the replica that lost the write did not load: %v", err)
			}
			if !repo.done {
				t.Fatal("the load never wrote the sealed seed")
			}
			wantSealed(t, repo, sealer, "k1", key.priv.Seed())
		})
	}
}

// TestUnsealSeeds pins the step a rollback runs: it stores every session and
// snapshot seed raw again, the form a strazad without sealing reads, changes
// nothing when one key does not open, and leaves client assertion keys sealed.
func TestUnsealSeeds(t *testing.T) {
	ctx := context.Background()
	repo, sealer := testRepo(t), testSealer(t)
	svc, err := NewTokenService(ctx, repo, sealer, "https://straza.local", 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Stage(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := NewSnapshotKeys(ctx, repo, sealer); err != nil {
		t.Fatal(err)
	}
	rsaKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	ca := sealedKey(t, sealer, "ca", store.KeyActive, rsaKey)
	if _, err := repo.Create(ctx, ca); err != nil {
		t.Fatal(err)
	}

	if n, err := UnsealSeeds(ctx, repo, testSealer(t)); err == nil || n != 0 {
		t.Fatalf("UnsealSeeds under another KEK = %d, %v, want a refusal", n, err)
	}
	for _, purpose := range []string{store.KeyPurposeSession, store.KeyPurposeSnapshot} {
		recs, _ := repo.ListByPurpose(ctx, purpose)
		for _, rec := range recs {
			if len(rec.PrivateKey) == ed25519.SeedSize {
				t.Fatalf("a refused unseal left %s key %s raw", purpose, rec.KID)
			}
		}
	}

	n, err := UnsealSeeds(ctx, repo, sealer)
	if err != nil || n != 3 {
		t.Fatalf("UnsealSeeds = %d, %v, want 3 keys", n, err)
	}
	for _, purpose := range []string{store.KeyPurposeSession, store.KeyPurposeSnapshot} {
		recs, _ := repo.ListByPurpose(ctx, purpose)
		for _, rec := range recs {
			if len(rec.PrivateKey) != ed25519.SeedSize ||
				!bytes.Equal(ed25519.NewKeyFromSeed(rec.PrivateKey).Public().(ed25519.PublicKey), rec.PublicKey) {
				t.Fatalf("%s key %s is not its raw seed after the unseal", purpose, rec.KID)
			}
		}
	}
	if !bytes.Equal(storedKey(t, repo, "ca").PrivateKey, ca.PrivateKey) {
		t.Error("the unseal changed the client assertion key")
	}
	if n, err := UnsealSeeds(ctx, repo, sealer); err != nil || n != 0 {
		t.Errorf("a second UnsealSeeds = %d, %v, want 0 keys", n, err)
	}
}

// TestResealKeys pins the replacement of the KEK: every signing key a start
// opens moves from the old KEK to the new one in one write, so the keys stay
// the same keys and no row ever holds one unsealed. A retired session or
// client assertion key, which no start opens, stays as it is. A run that
// stopped part way finishes on the next run, and a key neither KEK opens
// changes nothing.
func TestResealKeys(t *testing.T) {
	ctx := context.Background()
	repo, oldKEK, newKEK := testRepo(t), testSealer(t), testSealer(t)
	svc, err := NewTokenService(ctx, repo, oldKEK, "https://straza.local", 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Stage(ctx); err != nil {
		t.Fatal(err)
	}
	tok, _, err := svc.Mint(sampleClaims())
	if err != nil {
		t.Fatal(err)
	}
	snaps, err := NewSnapshotKeys(ctx, repo, oldKEK)
	if err != nil {
		t.Fatal(err)
	}
	snapKID, snapKey, err := snaps.Active(ctx)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	assertions := testAssertionKeys(t, repo, oldKEK)
	caKID := stagedAndPromoted(t, assertions)
	jwksBefore, err := assertions.JWKS(now)
	if err != nil {
		t.Fatal(err)
	}
	// A retired key sealed under a KEK that is long gone.
	gone := newSeedKey(t)
	goneSealed, err := sealSeed(testSealer(t), store.KeyPurposeSession, "gone", gone.priv.Seed())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repo.Create(ctx, store.SigningKey{
		KID: "gone", Purpose: store.KeyPurposeSession, Status: store.KeyRetired, PrivateKey: goneSealed, PublicKey: gone.pub,
	}); err != nil {
		t.Fatal(err)
	}

	if n, err := ResealKeys(ctx, repo, testSealer(t), newKEK); err == nil || n != 0 {
		t.Fatalf("ResealKeys from a KEK that opens nothing = %d, %v, want a refusal", n, err)
	}
	if err := loadSeedKeys(repo, oldKEK, store.KeyPurposeSession); err != nil {
		t.Fatalf("a refused reseal changed a row: %v", err)
	}

	// One row moved already, as after a run that stopped part way.
	caRec := storedKey(t, repo, caKID)
	plain, err := oldKEK.Open(caRec.PrivateKey)
	if err != nil {
		t.Fatal(err)
	}
	moved, err := newKEK.Seal(plain)
	if err != nil {
		t.Fatal(err)
	}
	if ok, err := repo.ReplacePrivateKey(ctx, caKID, caRec.PrivateKey, moved); err != nil || !ok {
		t.Fatalf("replace: %v, %v", ok, err)
	}

	n, err := ResealKeys(ctx, repo, oldKEK, newKEK)
	if err != nil || n != 3 {
		t.Fatalf("ResealKeys = %d, %v, want the 3 keys still under the old KEK", n, err)
	}
	if !bytes.Equal(storedKey(t, repo, "gone").PrivateKey, goneSealed) {
		t.Error("the reseal changed a retired key")
	}
	if !bytes.Equal(storedKey(t, repo, caKID).PrivateKey, moved) {
		t.Error("the reseal wrote a key that was under the new KEK already")
	}
	for _, purpose := range []string{store.KeyPurposeSession, store.KeyPurposeSnapshot} {
		if err := loadSeedKeys(repo, oldKEK, purpose); err == nil {
			t.Errorf("the %s keys still open under the old KEK", purpose)
		}
	}
	if _, err := NewClientAssertionKeys(ctx, repo, oldKEK, now); err == nil {
		t.Error("the client assertion key still opens under the old KEK")
	}

	after, err := NewTokenService(ctx, repo, newKEK, "https://straza.local", 0)
	if err != nil {
		t.Fatalf("the session keys under the new KEK: %v", err)
	}
	if _, err := after.Verify(tok); err != nil {
		t.Errorf("a token from before the reseal no longer verifies: %v", err)
	}
	snapsAfter, err := NewSnapshotKeys(ctx, repo, newKEK)
	if err != nil {
		t.Fatalf("the snapshot keys under the new KEK: %v", err)
	}
	if kid, key, err := snapsAfter.Active(ctx); err != nil || kid != snapKID || !key.Equal(snapKey) {
		t.Errorf("the snapshot key changed: kid %q, err %v", kid, err)
	}
	assertionsAfter, err := NewClientAssertionKeys(ctx, repo, newKEK, now)
	if err != nil {
		t.Fatalf("the client assertion keys under the new KEK: %v", err)
	}
	if jwks, err := assertionsAfter.JWKS(now); err != nil || !bytes.Equal(jwks, jwksBefore) {
		t.Errorf("the client assertion key document changed: %v", err)
	}
	if n, err := ResealKeys(ctx, repo, oldKEK, newKEK); err != nil || n != 0 {
		t.Errorf("a second ResealKeys = %d, %v, want 0 keys", n, err)
	}
}

// TestUnsealSeedsLeavesRetiredSessionKeys pins that the rollback step skips a
// retired session key, which no release reads, so one sealed under a KEK that
// is gone does not stop the rollback.
func TestUnsealSeedsLeavesRetiredSessionKeys(t *testing.T) {
	ctx := context.Background()
	repo, sealer := testRepo(t), testSealer(t)
	if _, err := NewTokenService(ctx, repo, sealer, "https://straza.local", 0); err != nil {
		t.Fatal(err)
	}
	gone := newSeedKey(t)
	goneSealed, err := sealSeed(testSealer(t), store.KeyPurposeSession, "gone", gone.priv.Seed())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repo.Create(ctx, store.SigningKey{
		KID: "gone", Purpose: store.KeyPurposeSession, Status: store.KeyRetired, PrivateKey: goneSealed, PublicKey: gone.pub,
	}); err != nil {
		t.Fatal(err)
	}
	if n, err := UnsealSeeds(ctx, repo, sealer); err != nil || n != 1 {
		t.Fatalf("UnsealSeeds = %d, %v, want the one active key", n, err)
	}
	if !bytes.Equal(storedKey(t, repo, "gone").PrivateKey, goneSealed) {
		t.Error("the unseal changed a retired key")
	}
}
