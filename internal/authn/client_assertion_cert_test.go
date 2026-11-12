package authn

import (
	"bytes"
	"context"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"math/big"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/lestrrat-go/jwx/v3/jwa"
	"github.com/lestrrat-go/jwx/v3/jws"
	"github.com/lestrrat-go/jwx/v3/jwt"

	"github.com/strazahq/straza/internal/store"
)

// promotable is the earliest time Promote accepts a key staged just before
// the call: two reload intervals after the store stamped the row.
func promotable() time.Time { return time.Now().Add(2 * KeyReloadInterval) }

// stagedAndPromoted stages a key and promotes it, and returns its kid.
func stagedAndPromoted(t *testing.T, keys *ClientAssertionKeys) string {
	t.Helper()
	ctx := context.Background()
	staged, err := keys.Stage(ctx, time.Now())
	if err != nil {
		t.Fatalf("Stage: %v", err)
	}
	if _, err := keys.Promote(ctx, promotable(), staged.KID); err != nil {
		t.Fatalf("Promote: %v", err)
	}
	return staged.KID
}

// TestAdvanceNeverPromotes pins that a staged client assertion key waits for
// an administrator however long it has been staged, so an Entra blueprint
// that does not hold its certificate yet never meets it. Signing refuses with
// the staged key's id, which the refusal sentence names.
func TestAdvanceNeverPromotes(t *testing.T) {
	repo := testRepo(t)
	ctx := context.Background()
	keys := testAssertionKeys(t, repo, testSealer(t))
	staged, err := keys.Stage(ctx, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	for _, after := range []time.Duration{2 * KeyReloadInterval, time.Hour, 30 * 24 * time.Hour} {
		now := time.Now().Add(after)
		step, err := keys.Advance(ctx, now, caRetireAfter)
		if err != nil {
			t.Fatalf("Advance after %s: %v", after, err)
		}
		if step.Promoted != "" {
			t.Fatalf("Advance promoted %s after %s, want no promotion without an administrator", step.Promoted, after)
		}
		if err := keys.Reload(ctx, now); err != nil {
			t.Fatal(err)
		}
		_, err = keys.SignAssertion(now, "sam-sre-agent", "https://idp.example/realms/x")
		var stagedErr *StagedKeyError
		if !errors.As(err, &stagedErr) || stagedErr.KID != staged.KID {
			t.Fatalf("SignAssertion after %s = %v, want a StagedKeyError naming %s", after, err, staged.KID)
		}
		if !errors.Is(err, ErrAssertionKeyStaged) || !errors.Is(err, ErrNoAssertionKey) {
			t.Errorf("the staged error %v does not match ErrAssertionKeyStaged and ErrNoAssertionKey", err)
		}
	}
	rec, err := repo.Get(ctx, staged.KID)
	if err != nil || rec.Status != store.KeyStaged {
		t.Errorf("the key is %q (%v) after a month of janitor steps, want staged", rec.Status, err)
	}
}

// TestPromote pins the administrator's promotion: the minimum age that lets
// every replica publish the key first, the answer for each state, the signing
// switch on this replica at once and on a peer at its next reload, and the
// demotion of the key that signed before.
func TestPromote(t *testing.T) {
	repo := testRepo(t)
	ctx := context.Background()
	sealer := testSealer(t)
	keys := testAssertionKeys(t, repo, sealer)
	peer := testAssertionKeys(t, repo, sealer)
	const client, audience = "sam-sre-agent", "https://idp.example/realms/x"
	signer := func(replica *ClientAssertionKeys, now time.Time) string {
		t.Helper()
		signed, err := replica.SignAssertion(now, client, audience)
		if err != nil {
			return ""
		}
		return kidOf(t, signed)
	}

	first, err := keys.Stage(ctx, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	_, err = keys.Promote(ctx, time.Now().Add(KeyReloadInterval), first.KID)
	var early *PromoteTooEarlyError
	if !errors.As(err, &early) || early.KID != first.KID || early.Wait <= 0 || early.Wait > KeyReloadInterval {
		t.Fatalf("Promote one interval after staging = %v, want a PromoteTooEarlyError with a wait of at most one interval", err)
	}

	now := promotable()
	got, err := keys.Promote(ctx, now, first.KID)
	if err != nil {
		t.Fatalf("Promote: %v", err)
	}
	if got != (Promotion{KID: first.KID, Was: store.KeyStaged, Changed: true}) {
		t.Errorf("Promote = %+v, want the first key promoted from staged with no previous key", got)
	}
	if kid := signer(keys, now); kid != first.KID {
		t.Errorf("this replica signs with %q right after the promotion, want %s", kid, first.KID)
	}
	if kid := signer(peer, now); kid != "" {
		t.Errorf("the peer signs with %q before its next reload, want nothing", kid)
	}
	if err := peer.Reload(ctx, now); err != nil {
		t.Fatal(err)
	}
	if kid := signer(peer, now); kid != first.KID {
		t.Errorf("the peer signs with %q after its reload, want %s", kid, first.KID)
	}
	if again, err := peer.Promote(ctx, now, first.KID); err != nil || again != (Promotion{KID: first.KID, Was: store.KeyActive}) {
		t.Errorf("a second Promote = %+v, %v, want active and unchanged", again, err)
	}

	second, err := keys.Stage(ctx, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	now = promotable()
	got, err = keys.Promote(ctx, now, second.KID)
	if err != nil {
		t.Fatalf("Promote the second key: %v", err)
	}
	if got != (Promotion{KID: second.KID, Was: store.KeyStaged, Changed: true, PreviousKID: first.KID}) {
		t.Errorf("Promote = %+v, want the second key promoted with the first as the previous key", got)
	}
	if kid := signer(keys, now); kid != second.KID {
		t.Errorf("this replica signs with %q, want %s", kid, second.KID)
	}
	if kid := signer(peer, now); kid != first.KID {
		t.Errorf("the peer signs with %q before its reload, want the first key, which still verifies", kid)
	}
	if doc := sortedKIDs(assertionDoc(t, keys, now)); doc != sortedKIDs(map[string]map[string]any{first.KID: nil, second.KID: nil}) {
		t.Errorf("the document after the promotion is %q, want both keys", doc)
	}
	if rec, _ := repo.Get(ctx, first.KID); rec.Status != store.KeyRetiring {
		t.Errorf("the first key is %q after the promotion, want retiring", rec.Status)
	}

	sessions, err := NewTokenService(ctx, repo, "https://straza.local", 0)
	if err != nil {
		t.Fatal(err)
	}
	minted, _, err := sessions.Mint(sampleClaims())
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := keys.Retire(ctx, now, second.KID); err != nil {
		t.Fatal(err)
	}
	refused := []struct {
		name  string
		kid   string
		check func(error) bool
	}{
		{"a retiring key", first.KID, func(err error) bool {
			var e *KeyStatusError
			return errors.As(err, &e) && e.KID == first.KID && e.Status == store.KeyRetiring
		}},
		{"a retired key", second.KID, func(err error) bool {
			var e *KeyStatusError
			return errors.As(err, &e) && e.KID == second.KID && e.Status == store.KeyRetired
		}},
		{"an unknown key", "no-such-kid", func(err error) bool { return errors.Is(err, store.ErrNotFound) }},
		{"a session key", kidOf(t, minted), func(err error) bool {
			var e *WrongPurposeError
			return errors.As(err, &e) && e.Purpose == store.KeyPurposeSession
		}},
	}
	for _, tc := range refused {
		if _, err := keys.Promote(ctx, now, tc.kid); !tc.check(err) {
			t.Errorf("Promote of %s = %v, want its own refusal", tc.name, err)
		}
	}
}

// TestPromoteRace pins Promote between replicas: of eight concurrent calls
// for one staged key exactly one reports the change, so the deployment writes
// one audit record.
func TestPromoteRace(t *testing.T) {
	repo := testRepo(t)
	ctx := context.Background()
	sealer := testSealer(t)
	staged, err := testAssertionKeys(t, repo, sealer).Stage(ctx, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	now := promotable()
	const replicas = 8
	results := make([]Promotion, replicas)
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := range replicas {
		keys := testAssertionKeys(t, repo, sealer)
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			got, err := keys.Promote(ctx, now, staged.KID)
			if err != nil {
				t.Errorf("replica %d: %v", i, err)
			}
			results[i] = got
		}()
	}
	close(start)
	wg.Wait()
	changed := 0
	for _, r := range results {
		if r.Changed {
			changed++
		}
	}
	if changed != 1 {
		t.Errorf("%d replicas report the promotion, want exactly 1", changed)
	}
}

// TestClientAssertionCertificate pins the certificate of a client assertion
// key: every field the builder sets, the public key equal to the published
// one, a served certificate for each published key and a refusal for every
// other kid, and the same bytes from two loads on two replicas, which is what
// lets every replica present one thumbprint with nothing stored.
func TestClientAssertionCertificate(t *testing.T) {
	repo := testRepo(t)
	ctx := context.Background()
	sealer := testSealer(t)
	keys := testAssertionKeys(t, repo, sealer)
	kid := stagedAndPromoted(t, keys)
	now := time.Now()

	got, err := keys.Certificate(ctx, now, kid)
	if err != nil {
		t.Fatalf("Certificate: %v", err)
	}
	cert, err := x509.ParseCertificate(got.DER)
	if err != nil {
		t.Fatalf("the certificate does not parse: %v", err)
	}
	rec, err := repo.Get(ctx, kid)
	if err != nil {
		t.Fatal(err)
	}
	created := rec.CreatedAt.UTC().Truncate(time.Second)
	sum := sha256.Sum256([]byte(kid))
	serial := sum[:16]
	serial[0] &= 0x7f
	checks := []struct {
		name string
		ok   bool
	}{
		{"the subject names the kid", cert.Subject.String() == "CN=Straza client assertion "+kid},
		{"it is self-issued", bytes.Equal(cert.RawIssuer, cert.RawSubject)},
		{"it is valid from the key's creation", cert.NotBefore.Equal(created)},
		{"it is valid for 730 days", cert.NotAfter.Equal(created.Add(730 * 24 * time.Hour))},
		{"NotAfter is the certificate's own", got.NotAfter.Equal(cert.NotAfter) && got.NotAfter.Equal(CertificateNotAfter(rec.CreatedAt))},
		{"the serial comes from the kid", cert.SerialNumber.Cmp(new(big.Int).SetBytes(serial)) == 0},
		{"it is signed with SHA-256 and RSA PKCS#1 v1.5", cert.SignatureAlgorithm == x509.SHA256WithRSA},
		{"it signs nothing but signatures", cert.KeyUsage == x509.KeyUsageDigitalSignature && len(cert.ExtKeyUsage) == 0},
		{"it is not a CA", cert.BasicConstraintsValid && !cert.IsCA},
		{"it carries no key identifiers a toolchain could fill", len(cert.SubjectKeyId) == 0 && len(cert.AuthorityKeyId) == 0},
		{"its signature verifies under its own key", cert.CheckSignature(cert.SignatureAlgorithm, cert.RawTBSCertificate, cert.Signature) == nil},
		{"the KID is the key's", got.KID == kid},
	}
	for _, c := range checks {
		if !c.ok {
			t.Errorf("%s: false", c.name)
		}
	}
	pub, ok := cert.PublicKey.(*rsa.PublicKey)
	if !ok {
		t.Fatalf("the certificate holds a %T, want an RSA key", cert.PublicKey)
	}
	doc := assertionDoc(t, keys, now)[kid]
	if n := base64.RawURLEncoding.EncodeToString(pub.N.Bytes()); n != doc["n"] {
		t.Error("the certificate's modulus is not the published key's")
	}
	if e := base64.RawURLEncoding.EncodeToString(big.NewInt(int64(pub.E)).Bytes()); e != doc["e"] {
		t.Error("the certificate's exponent is not the published key's")
	}
	block, rest := pem.Decode(got.PEM())
	if block == nil || block.Type != "CERTIFICATE" || !bytes.Equal(block.Bytes, got.DER) || len(rest) != 0 {
		t.Error("PEM is not one CERTIFICATE block of the DER")
	}

	other := testAssertionKeys(t, repo, sealer)
	again, err := other.Certificate(ctx, now, kid)
	if err != nil {
		t.Fatal(err)
	}
	if err := keys.Reload(ctx, now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	reloaded, err := keys.Certificate(ctx, now.Add(time.Second), kid)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(again.DER, got.DER) || !bytes.Equal(reloaded.DER, got.DER) {
		t.Fatal("two loads built different certificate bytes, so two replicas would present two thumbprints")
	}

	// A key a peer staged after this replica's last load is served too.
	staged, err := other.Stage(ctx, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if c, err := keys.Certificate(ctx, time.Now(), staged.KID); err != nil || c.KID != staged.KID {
		t.Errorf("Certificate of a key a peer just staged = %+v, %v, want it served", c.KID, err)
	}

	sessions, err := NewTokenService(ctx, repo, "https://straza.local", 0)
	if err != nil {
		t.Fatal(err)
	}
	minted, _, err := sessions.Mint(sampleClaims())
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := keys.Retire(ctx, time.Now(), kid); err != nil {
		t.Fatal(err)
	}
	refused := []struct {
		name  string
		kid   string
		check func(error) bool
	}{
		{"a retired key", kid, func(err error) bool {
			var e *KeyStatusError
			return errors.As(err, &e) && e.KID == kid && e.Status == store.KeyRetired
		}},
		{"an unknown key", "no-such-kid", func(err error) bool { return errors.Is(err, store.ErrNotFound) }},
		{"a session key", kidOf(t, minted), func(err error) bool {
			var e *WrongPurposeError
			return errors.As(err, &e) && e.Purpose == store.KeyPurposeSession
		}},
	}
	for _, tc := range refused {
		if _, err := keys.Certificate(ctx, time.Now(), tc.kid); !tc.check(err) {
			t.Errorf("Certificate of %s = %v, want its own refusal", tc.name, err)
		}
	}
	stale := time.Now().Add(assertionKeysMaxStale + KeyReloadInterval)
	if _, err := keys.Certificate(ctx, stale, staged.KID); !errors.Is(err, ErrAssertionKeysStale) {
		t.Errorf("Certificate on a replica past its stale bound = %v, want ErrAssertionKeysStale", err)
	}
}

// goldenCertSHA256 is the SHA-256 of the certificate the builder makes from
// the RFC 9500 key testRSA2048 for goldenKID created at goldenCreated. It was
// taken from the first build and read back with openssl x509 -text.
const (
	goldenKID        = "6f1c2f0e-3b7a-4d19-9a55-2f4c8e1b7d10"
	goldenCertSHA256 = "9469f8e3aa801eeb469d7aba6cc71c1dc1a2e733a85315521f50ff0444fe37ef"
)

var goldenCreated = time.Date(2026, 11, 7, 12, 0, 0, 123456000, time.UTC)

// TestClientAssertionCertificateGolden pins the certificate bytes of a fixed
// key, so a Go release that changes what x509.CreateCertificate writes fails
// here and not as a thumbprint that no Entra blueprint holds.
func TestClientAssertionCertificateGolden(t *testing.T) {
	raw, err := os.ReadFile("testdata/rfc9500-testRSA2048.pem")
	if err != nil {
		t.Fatal(err)
	}
	block, _ := pem.Decode(raw)
	if block == nil {
		t.Fatal("testdata/rfc9500-testRSA2048.pem holds no PEM block")
	}
	priv, err := x509.ParsePKCS1PrivateKey(block.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	got, err := buildCertificate(priv, goldenKID, goldenCreated)
	if err != nil {
		t.Fatalf("buildCertificate: %v", err)
	}
	sum := sha256.Sum256(got.DER)
	if hex.EncodeToString(sum[:]) != goldenCertSHA256 {
		t.Errorf("the certificate of the RFC 9500 key hashes to %x, want %s. A Go upgrade changed the bytes, "+
			"which changes the thumbprint Entra matches, so set the changed field explicitly in buildCertificate", sum, goldenCertSHA256)
	}
}

// TestSignCertificateAssertion pins the assertion of a Microsoft identity
// platform certificate credential in the shape the Entra lab tenant accepted:
// the header is RS256, typ JWT and the certificate's x5t#S256 with no
// kid, iss and sub are the client id, aud the token endpoint, nbf equals iat,
// and the signature verifies under the certificate's key. It names the key
// that signed, so a refusal can name the certificate to upload. A key whose
// certificate has expired signs nothing.
func TestSignCertificateAssertion(t *testing.T) {
	repo := testRepo(t)
	ctx := context.Background()
	keys := testAssertionKeys(t, repo, testSealer(t))
	const client, audience = "76568ffa-0000-4000-8000-000000000001", "https://login.microsoftonline.com/3c6bff24-ce9b-405c-8251-fe86232353b6/oauth2/v2.0/token"

	if _, _, err := keys.SignCertificateAssertion(time.Now(), client, audience); !errors.Is(err, ErrNoAssertionKey) {
		t.Errorf("SignCertificateAssertion with no key = %v, want ErrNoAssertionKey", err)
	}
	staged, err := keys.Stage(ctx, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	var stagedErr *StagedKeyError
	if _, _, err := keys.SignCertificateAssertion(time.Now(), client, audience); !errors.As(err, &stagedErr) || stagedErr.KID != staged.KID {
		t.Errorf("SignCertificateAssertion with a staged key = %v, want a StagedKeyError naming %s", err, staged.KID)
	}
	now := promotable()
	if _, err := keys.Promote(ctx, now, staged.KID); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ name, client, audience string }{{"no client id", "", audience}, {"no audience", client, ""}} {
		if _, _, err := keys.SignCertificateAssertion(now, tc.client, tc.audience); err == nil {
			t.Errorf("%s: signed an assertion", tc.name)
		}
	}

	signed, kid, err := keys.SignCertificateAssertion(now, client, audience)
	if err != nil {
		t.Fatalf("SignCertificateAssertion: %v", err)
	}
	if kid != staged.KID {
		t.Errorf("the assertion names the key %q, want the active key %s", kid, staged.KID)
	}
	cert, err := keys.Certificate(ctx, now, staged.KID)
	if err != nil {
		t.Fatal(err)
	}
	thumb := sha256.Sum256(cert.DER)
	msg, err := jws.Parse([]byte(signed))
	if err != nil {
		t.Fatal(err)
	}
	hdr, _ := json.Marshal(msg.Signatures()[0].ProtectedHeaders())
	if want := `{"alg":"RS256","typ":"JWT","x5t#S256":"` + base64.RawURLEncoding.EncodeToString(thumb[:]) + `"}`; string(hdr) != want {
		t.Errorf("header = %s, want %s", hdr, want)
	}
	parsed, err := x509.ParseCertificate(cert.DER)
	if err != nil {
		t.Fatal(err)
	}
	tok, err := jwt.Parse([]byte(signed), jwt.WithKey(jwa.RS256(), parsed.PublicKey), jwt.WithClock(jwt.ClockFunc(func() time.Time { return now })))
	if err != nil {
		t.Fatalf("the assertion does not verify under the certificate's key: %v", err)
	}
	iss, _ := tok.Issuer()
	sub, _ := tok.Subject()
	aud, _ := tok.Audience()
	iat, _ := tok.IssuedAt()
	nbf, _ := tok.NotBefore()
	exp, _ := tok.Expiration()
	jti, _ := tok.JwtID()
	if iss != client || sub != client || len(aud) != 1 || aud[0] != audience {
		t.Errorf("iss %q sub %q aud %v, want the client id twice and the token endpoint", iss, sub, aud)
	}
	if !nbf.Equal(iat) || exp.Sub(iat) != ClientAssertionTTL || jti == "" {
		t.Errorf("iat %s nbf %s exp %s jti %q, want nbf equal to iat, a lifetime of %s and a jti", iat, nbf, exp, jti, ClientAssertionTTL)
	}

	for _, tc := range []struct {
		name    string
		at      time.Time
		expired bool
	}{
		{"at NotAfter", cert.NotAfter, false},
		{"one second past NotAfter", cert.NotAfter.Add(time.Second), true},
	} {
		if err := keys.Reload(ctx, tc.at); err != nil {
			t.Fatal(err)
		}
		_, _, err := keys.SignCertificateAssertion(tc.at, client, audience)
		var expired *CertificateExpiredError
		switch {
		case tc.expired && (!errors.As(err, &expired) || expired.KID != staged.KID || !expired.NotAfter.Equal(cert.NotAfter)):
			t.Errorf("%s: err = %v, want a CertificateExpiredError naming the key and its NotAfter", tc.name, err)
		case !tc.expired && err != nil:
			t.Errorf("%s: %v, want a signed assertion", tc.name, err)
		}
		if _, err := keys.SignAssertion(tc.at, client, audience); err != nil {
			t.Errorf("%s: the RFC 7523 assertion, which no certificate backs, refused: %v", tc.name, err)
		}
	}
}
