package authn

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"errors"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lestrrat-go/jwx/v3/jwk"
	"github.com/lestrrat-go/jwx/v3/jws"
	"github.com/lestrrat-go/jwx/v3/jwt"

	"github.com/strazahq/straza/internal/secrets"
	"github.com/strazahq/straza/internal/store"
)

const caRetireAfter = ClientAssertionTTL + KeyReloadInterval

func testSealer(t testing.TB) *secrets.Builtin {
	t.Helper()
	kek, err := secrets.LoadOrCreateKEK(filepath.Join(t.TempDir(), "kek"))
	if err != nil {
		t.Fatal(err)
	}
	return kek
}

func testAssertionKeys(t *testing.T, repo store.SigningKeyRepo, sealer KeySealer) *ClientAssertionKeys {
	t.Helper()
	keys, err := NewClientAssertionKeys(context.Background(), repo, sealer, time.Now())
	if err != nil {
		t.Fatalf("NewClientAssertionKeys: %v", err)
	}
	return keys
}

// assertionDoc parses the key document and returns its keys by kid.
func assertionDoc(t *testing.T, keys *ClientAssertionKeys, now time.Time) map[string]map[string]any {
	t.Helper()
	raw, err := keys.JWKS(now)
	if err != nil {
		t.Fatalf("JWKS: %v", err)
	}
	var doc struct {
		Keys []map[string]any `json:"keys"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("the key document is not JSON: %v", err)
	}
	out := map[string]map[string]any{}
	for _, k := range doc.Keys {
		out[k["kid"].(string)] = k
	}
	return out
}

func sortedKIDs(doc map[string]map[string]any) string {
	kids := make([]string, 0, len(doc))
	for kid := range doc {
		kids = append(kids, kid)
	}
	sort.Strings(kids)
	return strings.Join(kids, ",")
}

// TestClientAssertionKeyLifecycle walks two keys through staged, active,
// retiring and retired with a caller-supplied clock. At every step it pins
// which key signs, what the key document carries and what this replica
// reports, and that a peer replica running the same step reports nothing, so
// each transition is recorded exactly once. The store stamps its rows with
// the real clock, so every step's time is an offset from a reading taken just
// before or just after the write it depends on.
func TestClientAssertionKeyLifecycle(t *testing.T) {
	repo := testRepo(t)
	ctx := context.Background()
	sealer := testSealer(t)
	keys := testAssertionKeys(t, repo, sealer)
	peer := testAssertionKeys(t, repo, sealer)
	const client, audience = "sam-sre-agent", "https://idp.example/realms/x"

	step := func(name string, now time.Time, wantPromoted string, wantRetired []string, wantSigner string, wantDoc ...string) {
		t.Helper()
		got, err := keys.Advance(ctx, now, caRetireAfter)
		if err != nil {
			t.Fatalf("%s: Advance: %v", name, err)
		}
		if got.Promoted != wantPromoted || strings.Join(got.Retired, ",") != strings.Join(wantRetired, ",") {
			t.Errorf("%s: Advance = %+v, want promoted %q and retired %v", name, got, wantPromoted, wantRetired)
		}
		echo, err := peer.Advance(ctx, now, caRetireAfter)
		if err != nil {
			t.Fatalf("%s: peer Advance: %v", name, err)
		}
		if echo.Promoted != "" || len(echo.Retired) != 0 {
			t.Errorf("%s: the peer reports %+v for a step this replica already made", name, echo)
		}
		sort.Strings(wantDoc)
		for who, replica := range map[string]*ClientAssertionKeys{"this replica": keys, "the peer": peer} {
			if err := replica.Reload(ctx, now); err != nil {
				t.Fatalf("%s: %s Reload: %v", name, who, err)
			}
			if doc := sortedKIDs(assertionDoc(t, replica, now)); doc != strings.Join(wantDoc, ",") {
				t.Errorf("%s: %s publishes %q, want %q", name, who, doc, strings.Join(wantDoc, ","))
			}
			signed, err := replica.SignAssertion(now, client, audience)
			if wantSigner == "" {
				if !errors.Is(err, ErrNoAssertionKey) {
					t.Errorf("%s: %s signed before any key was active: %v", name, who, err)
				}
				continue
			}
			if err != nil {
				t.Fatalf("%s: %s SignAssertion: %v", name, who, err)
			}
			if kid := kidOf(t, signed); kid != wantSigner {
				t.Errorf("%s: %s signed with %s, want %s", name, who, kid, wantSigner)
			}
		}
	}

	step("an empty store publishes an empty document and signs nothing", time.Now(), "", nil, "")

	first, err := keys.Stage(ctx, time.Now())
	if err != nil {
		t.Fatalf("Stage: %v", err)
	}
	if !first.Created || first.ActiveKID != "" || first.KID == "" {
		t.Fatalf("first Stage = %+v, want a created key with no active key beside it", first)
	}
	again, err := peer.Stage(ctx, time.Now())
	if err != nil {
		t.Fatalf("second Stage: %v", err)
	}
	if again.KID != first.KID || again.Created {
		t.Fatalf("second Stage = %+v, want the staged key %s and created false", again, first.KID)
	}
	staged1 := time.Now()
	step("one interval after staging the key is published and does not sign", staged1.Add(KeyReloadInterval), "", nil, "", first.KID)
	step("two intervals after staging it signs", staged1.Add(2*KeyReloadInterval), first.KID, nil, first.KID, first.KID)
	step("a second tick at the same time changes nothing", staged1.Add(2*KeyReloadInterval), "", nil, first.KID, first.KID)

	second, err := keys.Stage(ctx, time.Now())
	if err != nil {
		t.Fatalf("Stage the second key: %v", err)
	}
	if !second.Created || second.ActiveKID != first.KID {
		t.Fatalf("second key = %+v, want created beside the active key %s", second, first.KID)
	}
	staged2 := time.Now()
	step("the second key is published while the first still signs", staged2.Add(KeyReloadInterval), "", nil, first.KID, first.KID, second.KID)
	beforeDemotion := time.Now()
	step("two intervals later the second key signs and the first stays published", staged2.Add(2*KeyReloadInterval), second.KID, nil, second.KID, first.KID, second.KID)
	afterDemotion := time.Now()
	step("the retiring key stays until every assertion it signed has expired", beforeDemotion.Add(caRetireAfter-time.Second), "", nil, second.KID, first.KID, second.KID)
	step("then it leaves the document", afterDemotion.Add(caRetireAfter), "", []string{first.KID}, second.KID, second.KID)
	step("retirement is not repeated", afterDemotion.Add(caRetireAfter+KeyReloadInterval), "", nil, second.KID, second.KID)

	// A forced retirement of the signing key stops signing on this replica at
	// once and on the peer at its next reload.
	was, changed, err := keys.Retire(ctx, time.Now(), second.KID)
	if err != nil || was != store.KeyActive || !changed {
		t.Fatalf("Retire = %q, %v, %v, want active, true", was, changed, err)
	}
	if was, changed, err := peer.Retire(ctx, time.Now(), second.KID); err != nil || was != store.KeyRetired || changed {
		t.Errorf("second Retire = %q, %v, %v, want retired, false", was, changed, err)
	}
	step("a retired signing key leaves nothing to sign with", time.Now(), "", nil, "")
	if _, _, err := keys.Retire(ctx, time.Now(), "no-such-kid"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("Retire of an unknown kid = %v, want ErrNotFound", err)
	}
	sessions, err := NewTokenService(ctx, repo, "https://straza.local", 0)
	if err != nil {
		t.Fatal(err)
	}
	minted, _, err := sessions.Mint(sampleClaims())
	if err != nil {
		t.Fatal(err)
	}
	var wrong *WrongPurposeError
	if _, _, err := keys.Retire(ctx, time.Now(), kidOf(t, minted)); !errors.As(err, &wrong) || wrong.Purpose != store.KeyPurposeSession {
		t.Errorf("Retire of a session key = %v, want a WrongPurposeError naming session", err)
	}
	if _, err := sessions.Verify(minted); err != nil {
		t.Errorf("the refused retire broke the session key: %v", err)
	}
}

// TestClientAssertionDocumentIsPublicOnly pins the key document as plain RFC
// 7517 with RSA public parameters and nothing else: every key carries exactly
// kty, n, e, kid, alg and use, so no private member can ride along.
func TestClientAssertionDocumentIsPublicOnly(t *testing.T) {
	repo := testRepo(t)
	ctx := context.Background()
	keys := testAssertionKeys(t, repo, testSealer(t))
	if _, err := keys.Stage(ctx, time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := keys.Advance(ctx, time.Now().Add(2*KeyReloadInterval+time.Second), caRetireAfter); err != nil {
		t.Fatal(err)
	}
	if _, err := keys.Stage(ctx, time.Now()); err != nil {
		t.Fatal(err)
	}
	doc := assertionDoc(t, keys, time.Now())
	if len(doc) != 2 {
		t.Fatalf("document carries %d keys, want the active and the staged key", len(doc))
	}
	for kid, k := range doc {
		members := make([]string, 0, len(k))
		for name := range k {
			members = append(members, name)
		}
		sort.Strings(members)
		if got := strings.Join(members, ","); got != "alg,e,kid,kty,n,use" {
			t.Errorf("key %s carries the members %s, want exactly alg,e,kid,kty,n,use", kid, got)
		}
		if k["kty"] != "RSA" || k["alg"] != "RS256" || k["use"] != "sig" {
			t.Errorf("key %s = kty %v alg %v use %v, want RSA, RS256, sig", kid, k["kty"], k["alg"], k["use"])
		}
		n, err := base64.RawURLEncoding.DecodeString(k["n"].(string))
		if err != nil || len(n)*8 != clientAssertionKeyBits {
			t.Errorf("key %s modulus = %d bits (%v), want %d", kid, len(n)*8, err, clientAssertionKeyBits)
		}
	}
}

// TestClientAssertionKeyAtRest pins what a copy of the database yields: the
// private column is ciphertext that opens only under the KEK, and a replica
// with another KEK refuses to load with a sentence that says what to fix.
func TestClientAssertionKeyAtRest(t *testing.T) {
	repo := testRepo(t)
	ctx := context.Background()
	sealer := testSealer(t)
	keys := testAssertionKeys(t, repo, sealer)
	staged, err := keys.Stage(ctx, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	rec, err := repo.Get(ctx, staged.KID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := x509.ParsePKCS8PrivateKey(rec.PrivateKey); err == nil {
		t.Fatal("the private column parses as a PKCS#8 key: it is stored in the clear")
	}
	if _, err := x509.ParsePKCS1PrivateKey(rec.PrivateKey); err == nil {
		t.Fatal("the private column parses as a PKCS#1 key: it is stored in the clear")
	}
	if bytes.Contains(rec.PrivateKey, rec.PublicKey[:64]) {
		t.Error("the private column carries the public key in the clear, so it is not sealed as a whole")
	}
	plain, err := sealer.Open(rec.PrivateKey)
	if err != nil {
		t.Fatalf("the KEK does not open the private column: %v", err)
	}
	if !bytes.HasPrefix(plain, []byte(store.KeyPurposeClientAssertion+" "+staged.KID+"\n")) {
		t.Error("the sealed key does not name its purpose and kid, so it could be moved to another row")
	}

	_, err = NewClientAssertionKeys(ctx, repo, testSealer(t), time.Now())
	if err == nil {
		t.Fatal("a replica with another KEK loaded the key")
	}
	for _, want := range []string{staged.KID, "secrets.kekFile"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("load error %q does not name %q", err, want)
		}
	}
}

// fakeKeyRepo hands a loader whatever records the test planted, whatever
// purpose it asks for, which is what a wrong query or a tampered database
// would do.
type fakeKeyRepo struct {
	store.SigningKeyRepo
	keys []store.SigningKey
	err  error
}

func (f *fakeKeyRepo) ListByPurpose(context.Context, string) ([]store.SigningKey, error) {
	return f.keys, f.err
}

// sealedKey builds a record the way Stage does, with each part open to
// tampering by the caller.
func sealedKey(t *testing.T, sealer KeySealer, kid, status string, priv any) store.SigningKey {
	t.Helper()
	der, err := x509.MarshalPKCS8PrivateKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	sealed, err := sealer.Seal(append([]byte(store.KeyPurposeClientAssertion+" "+kid+"\n"), der...))
	if err != nil {
		t.Fatal(err)
	}
	rec := store.SigningKey{KID: kid, Purpose: store.KeyPurposeClientAssertion, Status: status, PrivateKey: sealed}
	if rsaKey, ok := priv.(*rsa.PrivateKey); ok {
		if rec.PublicKey, err = x509.MarshalPKIXPublicKey(&rsaKey.PublicKey); err != nil {
			t.Fatal(err)
		}
	}
	return rec
}

// TestClientAssertionKeysRefuseTamperedRecords pins the load as fail closed:
// one record that is not what Stage wrote fails the whole load, a first load
// yields no service and a later load keeps the state it had. A retired row is
// never opened, so its content cannot break the load.
func TestClientAssertionKeysRefuseTamperedRecords(t *testing.T) {
	sealer := testSealer(t)
	good, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	other, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	weak, err := rsa.GenerateKey(rand.Reader, 1024)
	if err != nil {
		t.Fatal(err)
	}
	_, edKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	with := func(rec store.SigningKey, change func(*store.SigningKey)) store.SigningKey {
		change(&rec)
		return rec
	}
	otherPub, _ := x509.MarshalPKIXPublicKey(&other.PublicKey)

	tests := []struct {
		name string
		keys []store.SigningKey
		want string // substring of the error, empty when the load must pass
	}{
		{"a key as Stage wrote it", []store.SigningKey{sealedKey(t, sealer, "k1", store.KeyActive, good)}, ""},
		{"a retired row with a garbage key", []store.SigningKey{
			sealedKey(t, sealer, "k1", store.KeyActive, good),
			{KID: "k0", Purpose: store.KeyPurposeClientAssertion, Status: store.KeyRetired, PrivateKey: []byte("junk")},
		}, ""},
		{"a session key among the records", []store.SigningKey{
			{KID: "s1", Purpose: store.KeyPurposeSession, Status: store.KeyActive, PrivateKey: make([]byte, 32), PublicKey: make([]byte, 32)},
		}, "purpose"},
		{"a private column that does not open", []store.SigningKey{
			with(sealedKey(t, sealer, "k1", store.KeyActive, good), func(r *store.SigningKey) { r.PrivateKey[30] ^= 1 }),
		}, "secrets.kekFile"},
		{"a sealed key moved to another row", []store.SigningKey{
			with(sealedKey(t, sealer, "k1", store.KeyActive, good), func(r *store.SigningKey) { r.KID = "k2" }),
		}, "another key"},
		{"a public column that is not the key's own", []store.SigningKey{
			with(sealedKey(t, sealer, "k1", store.KeyActive, good), func(r *store.SigningKey) { r.PublicKey = otherPub }),
		}, "public"},
		{"a key that is not RSA", []store.SigningKey{sealedKey(t, sealer, "k1", store.KeyActive, edKey)}, "RSA"},
		{"an RSA key below 2048 bits", []store.SigningKey{sealedKey(t, sealer, "k1", store.KeyActive, weak)}, "this server accepts"},
		{"two active keys", []store.SigningKey{
			sealedKey(t, sealer, "k1", store.KeyActive, good), sealedKey(t, sealer, "k2", store.KeyActive, other),
		}, "two active"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			now := time.Now()
			_, err := NewClientAssertionKeys(ctx, &fakeKeyRepo{keys: tc.keys}, sealer, now)
			if tc.want == "" {
				if err != nil {
					t.Fatalf("load: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("load error = %v, want one that says %q", err, tc.want)
			}

			// A replica that was loaded keeps its document when a later load fails.
			repo := &fakeKeyRepo{keys: []store.SigningKey{sealedKey(t, sealer, "k-good", store.KeyActive, good)}}
			loaded, err := NewClientAssertionKeys(ctx, repo, sealer, now)
			if err != nil {
				t.Fatal(err)
			}
			repo.keys = tc.keys
			if err := loaded.Reload(ctx, now); err == nil {
				t.Fatal("a reload over the tampered records passed")
			}
			if doc := sortedKIDs(assertionDoc(t, loaded, now)); doc != "k-good" {
				t.Errorf("after the failed reload the replica publishes %q, want its last good document", doc)
			}
		})
	}
}

// TestClientAssertionKeysGoStale pins the bound on a replica that lost the
// store: it serves and signs from its last good load for three reload
// intervals and then refuses both, so a key an administrator retired cannot
// stay published or keep signing there.
func TestClientAssertionKeysGoStale(t *testing.T) {
	sealer := testSealer(t)
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	repo := &fakeKeyRepo{keys: []store.SigningKey{sealedKey(t, sealer, "k1", store.KeyActive, key)}}
	ctx := context.Background()
	t0 := time.Now()
	keys, err := NewClientAssertionKeys(ctx, repo, sealer, t0)
	if err != nil {
		t.Fatal(err)
	}
	repo.err = errors.New("store is down")

	tests := []struct {
		name   string
		at     time.Duration
		reload bool
		stale  bool
	}{
		{"right after the load", 0, false, false},
		{"after two failed reloads", 2 * KeyReloadInterval, true, false},
		{"at the bound", 3 * KeyReloadInterval, true, false},
		{"past the bound", 3*KeyReloadInterval + time.Second, true, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			now := t0.Add(tc.at)
			if tc.reload {
				if err := keys.Reload(ctx, now); err == nil {
					t.Fatal("Reload passed while the store is down")
				}
			}
			doc, docErr := keys.JWKS(now)
			_, signErr := keys.SignAssertion(now, "sam-sre-agent", "https://idp.example/realms/x")
			if errors.Is(docErr, ErrAssertionKeysStale) != tc.stale || errors.Is(signErr, ErrAssertionKeysStale) != tc.stale {
				t.Errorf("document error %v, sign error %v, want stale = %v", docErr, signErr, tc.stale)
			}
			if tc.stale && doc != nil {
				t.Error("a stale replica still handed out a document")
			}
			if !tc.stale && !bytes.Contains(doc, []byte(`"kid":"k1"`)) {
				t.Errorf("while the store is down the replica publishes %s, want its last good document", doc)
			}
		})
	}

	repo.err = nil
	now := t0.Add(10 * KeyReloadInterval)
	if err := keys.Reload(ctx, now); err != nil {
		t.Fatalf("Reload after the store came back: %v", err)
	}
	if _, err := keys.JWKS(now); err != nil {
		t.Errorf("document after the store came back: %v", err)
	}
}

// TestSigningKeyLoadersRefuseOtherPurposes pins purpose separation in code,
// beside the CHECK constraint and the query: each loader refuses a record of
// another purpose before it touches the key bytes, so a client assertion key
// never becomes a session or snapshot key and neither of those ever signs an
// assertion.
func TestSigningKeyLoadersRefuseOtherPurposes(t *testing.T) {
	sealer := testSealer(t)
	rsaKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	records := map[string]store.SigningKey{
		store.KeyPurposeSession:         {KID: "ses", Purpose: store.KeyPurposeSession, Status: store.KeyActive, PrivateKey: priv.Seed(), PublicKey: pub},
		store.KeyPurposeSnapshot:        {KID: "snap", Purpose: store.KeyPurposeSnapshot, Status: store.KeyActive, PrivateKey: priv.Seed(), PublicKey: pub},
		store.KeyPurposeClientAssertion: sealedKey(t, sealer, "ca", store.KeyActive, rsaKey),
	}
	loaders := map[string]func(store.SigningKeyRepo) error{
		store.KeyPurposeSession: func(r store.SigningKeyRepo) error {
			_, err := NewTokenService(context.Background(), r, "https://straza.local", 0)
			return err
		},
		store.KeyPurposeSnapshot: func(r store.SigningKeyRepo) error {
			_, err := NewSnapshotKeys(context.Background(), r)
			return err
		},
		store.KeyPurposeClientAssertion: func(r store.SigningKeyRepo) error {
			_, err := NewClientAssertionKeys(context.Background(), r, sealer, time.Now())
			return err
		},
	}
	for loaderPurpose, load := range loaders {
		for recordPurpose, rec := range records {
			t.Run(loaderPurpose+" loader, "+recordPurpose+" record", func(t *testing.T) {
				err := load(&fakeKeyRepo{keys: []store.SigningKey{rec}})
				if loaderPurpose == recordPurpose {
					if err != nil {
						t.Fatalf("the loader refused its own purpose: %v", err)
					}
					return
				}
				if err == nil || !strings.Contains(err.Error(), "purpose") {
					t.Fatalf("err = %v, want a refusal that names the purpose", err)
				}
			})
		}
	}
}

// TestSignAssertion pins the assertion as RFC 7523 section 2.2 in the
// self-issued profile: iss and sub are the client id, aud is the configured
// audience, the lifetime is ClientAssertionTTL, jti is fresh per call, the
// header says RS256 with the kid, and the signature verifies against the
// published document and not against the session key set.
func TestSignAssertion(t *testing.T) {
	repo := testRepo(t)
	ctx := context.Background()
	keys := testAssertionKeys(t, repo, testSealer(t))
	staged, err := keys.Stage(ctx, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().Add(2*KeyReloadInterval + time.Second)
	if _, err := keys.Advance(ctx, now, caRetireAfter); err != nil {
		t.Fatal(err)
	}
	const client, audience = "sam-sre-agent", "https://idp.example/realms/straza"

	refused := []struct{ name, client, audience string }{
		{"no client id", "", audience},
		{"no audience", client, ""},
	}
	for _, tc := range refused {
		if _, err := keys.SignAssertion(now, tc.client, tc.audience); err == nil {
			t.Errorf("%s: signed an assertion", tc.name)
		}
	}

	signed, err := keys.SignAssertion(now, client, audience)
	if err != nil {
		t.Fatalf("SignAssertion: %v", err)
	}
	msg, err := jws.Parse([]byte(signed))
	if err != nil {
		t.Fatal(err)
	}
	hdr, _ := json.Marshal(msg.Signatures()[0].ProtectedHeaders())
	if string(hdr) != `{"alg":"RS256","kid":"`+staged.KID+`","typ":"JWT"}` {
		t.Errorf("header = %s, want RS256 with the kid and typ JWT", hdr)
	}
	raw, err := keys.JWKS(now)
	if err != nil {
		t.Fatal(err)
	}
	set, err := jwk.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	tok, err := jwt.Parse([]byte(signed), jwt.WithKeySet(set), jwt.WithClock(jwt.ClockFunc(func() time.Time { return now })))
	if err != nil {
		t.Fatalf("the assertion does not verify against the published document: %v", err)
	}
	iss, _ := tok.Issuer()
	sub, _ := tok.Subject()
	aud, _ := tok.Audience()
	iat, _ := tok.IssuedAt()
	exp, _ := tok.Expiration()
	jti, _ := tok.JwtID()
	if iss != client || sub != client || len(aud) != 1 || aud[0] != audience {
		t.Errorf("iss %q sub %q aud %v, want the client id twice and the audience", iss, sub, aud)
	}
	if exp.Sub(iat) != ClientAssertionTTL || jti == "" {
		t.Errorf("lifetime %s jti %q, want %s and a jti", exp.Sub(iat), jti, ClientAssertionTTL)
	}
	again, err := keys.SignAssertion(now, client, audience)
	if err != nil {
		t.Fatal(err)
	}
	tok2, err := jwt.Parse([]byte(again), jwt.WithKeySet(set), jwt.WithClock(jwt.ClockFunc(func() time.Time { return now })))
	if err != nil {
		t.Fatal(err)
	}
	if jti2, _ := tok2.JwtID(); jti2 == jti {
		t.Error("two assertions share a jti, so the provider would refuse the second as a replay")
	}

	sessions, err := NewTokenService(ctx, repo, "https://straza.local", 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := sessions.Verify(signed); err == nil {
		t.Error("the session verifier accepted a client assertion")
	}
	sessionToken, _, err := sessions.Mint(sampleClaims())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := jwt.Parse([]byte(sessionToken), jwt.WithKeySet(set)); err == nil {
		t.Error("a session token verifies against the client assertion document")
	}
}

// TestClientAssertionStageRace pins Stage between replicas: of eight
// concurrent calls over one store exactly one creates the key, and every
// other call answers with that key.
func TestClientAssertionStageRace(t *testing.T) {
	repo := testRepo(t)
	ctx := context.Background()
	sealer := testSealer(t)
	const replicas = 8
	results := make([]StagedKey, replicas)
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := range replicas {
		keys := testAssertionKeys(t, repo, sealer)
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			got, err := keys.Stage(ctx, time.Now())
			if err != nil {
				t.Errorf("replica %d: %v", i, err)
			}
			results[i] = got
		}()
	}
	close(start)
	wg.Wait()
	created := 0
	for _, r := range results {
		if r.Created {
			created++
		}
		if r.KID != results[0].KID {
			t.Errorf("replicas staged %s and %s, want one key", r.KID, results[0].KID)
		}
	}
	if created != 1 {
		t.Errorf("%d replicas report that they created the key, want exactly 1", created)
	}
}
