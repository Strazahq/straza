package server

import (
	"bytes"
	"context"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"io"
	"net/http"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/strazahq/straza/internal/authn"
	"github.com/strazahq/straza/internal/config"
	"github.com/strazahq/straza/internal/store"
)

const assertionJWKS = "/.well-known/straza/client-assertion-jwks.json"

type assertionRotation struct {
	KID         string `json:"kid"`
	Status      string `json:"status"`
	PreviousKID string `json:"previous_kid"`
	ActiveIn    int    `json:"active_in_seconds"`
	PreviousFor int    `json:"previous_key_verifies_for_seconds"`
	JWKSURI     string `json:"jwks_uri"`
	Error       string `json:"error"`
}

// keyAuditRecords returns the straza.audit.admin records of the signing key
// life cycle for one purpose, oldest first.
func keyAuditRecords(t *testing.T, app *App, purpose string) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, ev := range adminAuditEvents(t, app) {
		if action, _ := ev["action"].(string); strings.HasPrefix(action, "signing-keys.") && ev["purpose"] == purpose {
			out = append(out, ev)
		}
	}
	return out
}

func rawGet(t *testing.T, method, url string) (int, http.Header, []byte) {
	t.Helper()
	req, err := http.NewRequest(method, url, strings.NewReader(`{"kid":"x"}`))
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, resp.Header, body
}

// TestClientAssertionKeyLifecycleOverTheAPI walks the client assertion key
// through the admin routes and the janitor step on one node. It pins the
// answers, the key document at every stage, that only the promote route makes
// a key sign, one audit record per transition with actor, purpose and kid,
// the certificate route, and that no answer, record or log line carries
// private key material.
func TestClientAssertionKeyLifecycleOverTheAPI(t *testing.T) {
	t.Parallel()
	log, logs := captureLogger()
	clk := &edgeClock{t: time.Now()}
	app, base := testAppPreRun(t, []func(*App){func(a *App) { a.log = log; a.assertionKeys.clock = clk.now }})
	ctx := context.Background()
	kim := seedIdentity(t, app)
	grantAdmin(t, app, kim.ID)
	tok := loginDeviceFlow(t, base, "kim", "hunter2!")
	var answers bytes.Buffer

	docKIDs := func() string {
		t.Helper()
		code, hdr, body := rawGet(t, "GET", base+assertionJWKS)
		if code != http.StatusOK || hdr.Get("Cache-Control") != "max-age=30" || hdr.Get("Content-Type") != "application/json" {
			t.Fatalf("document = %d, Cache-Control %q, Content-Type %q", code, hdr.Get("Cache-Control"), hdr.Get("Content-Type"))
		}
		answers.Write(body)
		var doc struct {
			Keys []map[string]any `json:"keys"`
		}
		if err := json.Unmarshal(body, &doc); err != nil || doc.Keys == nil {
			t.Fatalf("document %s: %v", body, err)
		}
		var kids []string
		for _, k := range doc.Keys {
			members := make([]string, 0, len(k))
			for name := range k {
				members = append(members, name)
			}
			sort.Strings(members)
			if strings.Join(members, ",") != "alg,e,kid,kty,n,use" || k["kty"] != "RSA" || k["alg"] != "RS256" || k["use"] != "sig" {
				t.Errorf("document key = %v, want exactly the RSA public members with alg RS256 and use sig", k)
			}
			kids = append(kids, k["kid"].(string))
		}
		sort.Strings(kids)
		return strings.Join(kids, ",")
	}
	rotate := func() assertionRotation {
		t.Helper()
		var out assertionRotation
		if code := adminReq(t, "POST", base+"/v1/admin/signing-keys/client-assertion/rotate", tok, nil, &out); code != http.StatusOK {
			t.Fatalf("rotate = %d (%s)", code, out.Error)
		}
		raw, _ := json.Marshal(out)
		answers.Write(raw)
		return out
	}
	wantRecords := func(when string, want ...string) {
		t.Helper()
		var got []string
		for _, ev := range keyAuditRecords(t, app, store.KeyPurposeClientAssertion) {
			actor, _ := ev["actor"].(string)
			got = append(got, ev["action"].(string)+" "+ev["kid"].(string)+" "+actor)
		}
		if strings.Join(got, "\n") != strings.Join(want, "\n") {
			t.Errorf("%s: audit records =\n%s\nwant\n%s", when, strings.Join(got, "\n"), strings.Join(want, "\n"))
		}
	}

	if kids := docKIDs(); kids != "" {
		t.Fatalf("a fresh server publishes %q, want an empty key set", kids)
	}

	first := rotate()
	if first.KID == "" || first.Status != store.KeyStaged || first.PreviousKID != "" || first.JWKSURI != base+assertionJWKS {
		t.Errorf("first rotate = %+v, want a staged key, no previous key and the document URL", first)
	}
	if first.ActiveIn != 60 || first.PreviousFor != 60+int(app.assertionKeyRetireAfter().Seconds()) {
		t.Errorf("timings = %d and %d, want 60 and 120", first.ActiveIn, first.PreviousFor)
	}
	if again := rotate(); again.KID != first.KID {
		t.Errorf("a second rotate staged %s, want the staged key %s", again.KID, first.KID)
	}
	wantRecords("after two rotate calls", "signing-keys.create "+first.KID+" kim")
	if kids := docKIDs(); kids != first.KID {
		t.Errorf("document = %q, want the staged key at once", kids)
	}

	type promotion struct {
		KID, Status, Was string
		PreviousKID      string `json:"previous_kid"`
		EverySigns       int    `json:"every_replica_signs_in_seconds"`
		PreviousLeaves   int    `json:"previous_key_leaves_document_in_seconds"`
		Error            string `json:"error"`
	}
	promote := func(kid string, want int) promotion {
		t.Helper()
		var out promotion
		if code := adminReq(t, "POST", base+"/v1/admin/signing-keys/client-assertion/"+kid+"/promote", tok, nil, &out); code != want {
			t.Fatalf("promote %s = %d (%s), want %d", kid, code, out.Error, want)
		}
		raw, _ := json.Marshal(out)
		answers.Write(raw)
		return out
	}

	staged1 := time.Now()
	app.clientAssertionKeyStep(ctx, staged1.Add(authn.KeyReloadInterval))
	app.clientAssertionKeyStep(ctx, staged1.Add(time.Hour))
	wantRecords("an hour of janitor steps after staging", "signing-keys.create "+first.KID+" kim")
	if keys, _ := app.store.SigningKeys().ListByPurpose(ctx, store.KeyPurposeClientAssertion); len(keys) != 1 || keys[0].Status != store.KeyStaged {
		t.Fatalf("stored keys = %+v, want the one key still staged", keys)
	}

	clk.set(time.Now())
	early := promote(first.KID, http.StatusConflict)
	for _, want := range []string{first.KID, "every replica publishes it", "60 seconds after staging", "Run the command again in"} {
		if !strings.Contains(early.Error, want) {
			t.Errorf("early promote refusal %q does not say %q", early.Error, want)
		}
	}
	clk.set(time.Now().Add(2 * authn.KeyReloadInterval))
	if got := promote(first.KID, http.StatusOK); got != (promotion{KID: first.KID, Status: store.KeyActive, Was: store.KeyStaged, EverySigns: 30}) {
		t.Errorf("promote = %+v, want the first key active from staged and no previous key", got)
	}
	if got := promote(first.KID, http.StatusOK); got.Was != store.KeyActive {
		t.Errorf("a second promote = %+v, want was active", got)
	}
	wantRecords("after the promotion ran twice",
		"signing-keys.create "+first.KID+" kim", "signing-keys.promote "+first.KID+" kim")
	if ev := lastAdminAction(t, app, "signing-keys.promote"); ev["actorId"] != kim.ID || ev["actorVia"] == nil {
		t.Errorf("promote record = %v, want kim's actor fields", ev)
	}

	second := rotate()
	if second.PreviousKID != first.KID || second.KID == first.KID {
		t.Errorf("second rotate = %+v, want a new key beside %s", second, first.KID)
	}
	if kids := docKIDs(); len(strings.Split(kids, ",")) != 2 {
		t.Errorf("document = %q, want the active and the staged key", kids)
	}
	clk.set(time.Now().Add(2 * authn.KeyReloadInterval))
	got := promote(second.KID, http.StatusOK)
	if got.PreviousKID != first.KID || got.PreviousLeaves != int((app.assertionKeyRetireAfter()+authn.KeyReloadInterval).Seconds()) {
		t.Errorf("promote = %+v, want the first key as the previous key and its departure time", got)
	}
	demoted := time.Now()
	app.clientAssertionKeyStep(ctx, demoted.Add(app.assertionKeyRetireAfter()))
	if kids := docKIDs(); kids != second.KID {
		t.Errorf("document = %q, want only %s once the old key retired", kids, second.KID)
	}
	lifecycle := []string{
		"signing-keys.create " + first.KID + " kim", "signing-keys.promote " + first.KID + " kim",
		"signing-keys.rotate " + second.KID + " kim", "signing-keys.promote " + second.KID + " kim",
		"signing-keys.retire " + first.KID + " ",
	}
	wantRecords("after the second key took over", lifecycle...)

	// The certificate of the signing key holds the published public key.
	req, _ := http.NewRequest("GET", base+"/v1/admin/signing-keys/client-assertion/"+second.KID+"/certificate", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	certPEM, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	answers.Write(certPEM)
	block, _ := pem.Decode(certPEM)
	if resp.StatusCode != http.StatusOK || resp.Header.Get("Content-Type") != "application/x-pem-file" || block == nil || block.Type != "CERTIFICATE" {
		t.Fatalf("certificate = %d, %q, %s, want one PEM CERTIFICATE", resp.StatusCode, resp.Header.Get("Content-Type"), certPEM)
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	_, _, docBody := rawGet(t, "GET", base+assertionJWKS)
	if n := base64.RawURLEncoding.EncodeToString(cert.PublicKey.(*rsa.PublicKey).N.Bytes()); !bytes.Contains(docBody, []byte(`"n":"`+n+`"`)) {
		t.Error("the certificate's modulus is not in the key document")
	}
	if cert.Subject.CommonName != "Straza client assertion "+second.KID {
		t.Errorf("certificate subject = %q, want it to name %s", cert.Subject.CommonName, second.KID)
	}

	sessionKeys, err := app.store.SigningKeys().ListByPurpose(ctx, store.KeyPurposeSession)
	if err != nil || len(sessionKeys) == 0 {
		t.Fatal(err)
	}
	refusals := []struct {
		name, route string
		want        int
		says        []string
	}{
		{"retire an unknown kid", "POST no-such-kid/retire", http.StatusNotFound, []string{"no-such-kid", "strazactl signing-keys list"}},
		{"retire a session key", "POST " + sessionKeys[0].KID + "/retire", http.StatusConflict, []string{"session", "only a client assertion key", "strazactl signing-keys rotate session"}},
		{"promote an unknown kid", "POST no-such-kid/promote", http.StatusNotFound, []string{"no-such-kid", "strazactl signing-keys list"}},
		{"promote a session key", "POST " + sessionKeys[0].KID + "/promote", http.StatusConflict, []string{"session", "only a client assertion key is promoted by hand", "strazactl signing-keys rotate session"}},
		{"promote a retired key", "POST " + first.KID + "/promote", http.StatusConflict, []string{first.KID, "is retired and cannot sign again", "strazactl signing-keys rotate client_assertion", "promote that one"}},
		{"the certificate of an unknown kid", "GET no-such-kid/certificate", http.StatusNotFound, []string{"no-such-kid", "strazactl signing-keys list"}},
		{"the certificate of a session key", "GET " + sessionKeys[0].KID + "/certificate", http.StatusConflict, []string{"session", "only a client assertion key has a certificate"}},
		{"the certificate of a retired key", "GET " + first.KID + "/certificate", http.StatusConflict, []string{first.KID, "no longer serves its certificate", "Entra blueprint"}},
	}
	for _, tc := range refusals {
		var out struct {
			Error string `json:"error"`
		}
		method, path, _ := strings.Cut(tc.route, " ")
		if code := adminReq(t, method, base+"/v1/admin/signing-keys/client-assertion/"+path, tok, nil, &out); code != tc.want {
			t.Errorf("%s = %d, want %d", tc.name, code, tc.want)
		}
		for _, want := range tc.says {
			if !strings.Contains(out.Error, want) {
				t.Errorf("%s: refusal %q does not say %q", tc.name, out.Error, want)
			}
		}
	}
	for i, wantWas := range []string{store.KeyActive, store.KeyRetired} {
		var out struct {
			KID, Status, Was string
			Leaves           int `json:"leaves_document_in_seconds"`
			Next             int `json:"next_key_active_in_seconds"`
		}
		if code := adminReq(t, "POST", base+"/v1/admin/signing-keys/client-assertion/"+second.KID+"/retire", tok, nil, &out); code != http.StatusOK {
			t.Fatalf("retire call %d = %d", i+1, code)
		}
		if out.KID != second.KID || out.Status != store.KeyRetired || out.Was != wantWas || out.Leaves != 30 || out.Next != 60 {
			t.Errorf("retire call %d = %+v, want retired, was %s, 30 and 60", i+1, out, wantWas)
		}
	}
	wantRecords("after the forced retire ran twice", append(lifecycle, "signing-keys.retire "+second.KID+" kim")...)
	if kids := docKIDs(); kids != "" {
		t.Errorf("document = %q after the forced retire, want it empty at once", kids)
	}
	if ev := lastAdminAction(t, app, "signing-keys.retire"); ev["actorId"] != kim.ID || ev["purpose"] != store.KeyPurposeClientAssertion {
		t.Errorf("forced retire record = %v, want kim and the purpose", ev)
	}

	var listed []struct {
		KID, Purpose, Status string
		CreatedAt            string `json:"created_at"`
		RotatedAt            string `json:"rotated_at"`
		CertificateExpires   string `json:"certificate_expires_at"`
	}
	if code := adminReq(t, "GET", base+"/v1/admin/signing-keys", tok, nil, &listed); code != http.StatusOK {
		t.Fatalf("list = %d", code)
	}
	byKID := map[string]string{}
	for _, k := range listed {
		byKID[k.KID] = k.Purpose + " " + k.Status
		if k.CreatedAt == "" {
			t.Errorf("listed key %s has no created_at", k.KID)
		}
		if k.CertificateExpires != "" {
			t.Errorf("listed key %s, %s and %s, names a certificate expiry %s", k.KID, k.Purpose, k.Status, k.CertificateExpires)
		}
	}
	if byKID[first.KID] != "client_assertion retired" || byKID[second.KID] != "client_assertion retired" || byKID[sessionKeys[0].KID] != "session active" {
		t.Errorf("list = %v, want both client assertion keys retired and the session key active", byKID)
	}
	raw, _ := json.Marshal(listed)
	answers.Write(raw)

	// Nothing that left the server, was recorded or was logged carries private
	// key material: no private JWK member, no PEM, and no encoding of a stored
	// private column.
	events, _ := json.Marshal(adminAuditEvents(t, app))
	surfaces := map[string]string{"the answers": answers.String(), "the audit records": string(events), "the logs": logs.String()}
	keys, err := app.store.SigningKeys().ListByPurpose(ctx, store.KeyPurposeClientAssertion)
	if err != nil || len(keys) != 2 {
		t.Fatalf("stored keys = %d (%v), want 2", len(keys), err)
	}
	for name, text := range surfaces {
		for _, member := range []string{`"d"`, `"p"`, `"q"`, `"dp"`, `"dq"`, `"qi"`, "PRIVATE KEY", "private_key", "privateKey"} {
			if strings.Contains(text, member) {
				t.Errorf("%s carry %s", name, member)
			}
		}
		for _, k := range keys {
			sealed := k.PrivateKey[:48]
			for _, enc := range []string{
				base64.StdEncoding.EncodeToString(sealed), base64.RawURLEncoding.EncodeToString(sealed), hex.EncodeToString(sealed),
			} {
				if strings.Contains(text, enc[:40]) {
					t.Errorf("%s carry the sealed private column of %s", name, k.KID)
				}
			}
		}
	}
}

// TestClientAssertionDocumentRoute pins the unauthenticated route: GET only,
// nothing a caller sends changes the answer, no request reads the store, and
// the session key document keeps carrying session keys only.
func TestClientAssertionDocumentRoute(t *testing.T) {
	t.Parallel()
	app, base, cs := testAppCounting(t)
	if _, err := app.assertionKeys.Stage(context.Background(), time.Now()); err != nil {
		t.Fatal(err)
	}
	_, _, want := rawGet(t, "GET", base+assertionJWKS)
	if !bytes.Contains(want, []byte(`"kty":"RSA"`)) {
		t.Fatalf("document = %s, want the staged RSA key", want)
	}

	for _, method := range []string{"POST", "PUT", "PATCH", "DELETE"} {
		code, hdr, body := rawGet(t, method, base+assertionJWKS)
		if code != http.StatusMethodNotAllowed || hdr.Get("Allow") == "" || bytes.Contains(body, []byte("kty")) {
			t.Errorf("%s = %d with Allow %q and body %s, want 405 and no key", method, code, hdr.Get("Allow"), body)
		}
	}

	before := cs.snapshot()
	for _, suffix := range []string{"", "?kid=other", "?purpose=session&status=retired", "?callback=x"} {
		code, _, body := rawGet(t, "GET", base+assertionJWKS+suffix)
		if code != http.StatusOK || !bytes.Equal(body, want) {
			t.Errorf("GET %s = %d with another body, want the same document", suffix, code)
		}
	}
	assertNoRepoAccess(t, cs, "client assertion key document", before)

	_, _, sessionDoc := rawGet(t, "GET", base+"/.well-known/straza/jwks.json")
	var doc struct {
		Keys []map[string]any `json:"keys"`
	}
	if err := json.Unmarshal(sessionDoc, &doc); err != nil || len(doc.Keys) == 0 {
		t.Fatalf("session document %s: %v", sessionDoc, err)
	}
	for _, k := range doc.Keys {
		if k["kty"] != "OKP" {
			t.Errorf("the session document carries a %v key, want OKP only", k["kty"])
		}
	}
}

// TestClientAssertionDocumentFailsClosed pins a replica that lost the key
// store: once its last good load is older than the bound it answers 503 with
// no-store and no key, never a document that looks valid.
func TestClientAssertionDocumentFailsClosed(t *testing.T) {
	t.Parallel()
	app, base := testApp(t)
	ctx := context.Background()
	if _, err := app.assertionKeys.Stage(ctx, time.Now()); err != nil {
		t.Fatal(err)
	}
	// The handler reads the real clock, so the test makes the last good load
	// old by loading with a clock one hour back.
	if err := app.assertionKeys.Reload(ctx, time.Now().Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	code, hdr, body := rawGet(t, "GET", base+assertionJWKS)
	if code != http.StatusServiceUnavailable || hdr.Get("Cache-Control") != "no-store" || bytes.Contains(body, []byte("kty")) || bytes.Contains(body, []byte(`"keys"`)) {
		t.Errorf("stale document = %d, Cache-Control %q, body %s, want 503, no-store and no key set", code, hdr.Get("Cache-Control"), body)
	}
	var out struct {
		Error string `json:"error"`
	}
	_ = json.Unmarshal(body, &out)
	for _, want := range []string{"client assertion key document", "store"} {
		if !strings.Contains(out.Error, want) {
			t.Errorf("answer %q does not say %q", out.Error, want)
		}
	}
}

// TestClientAssertionKeyTwoReplicas is the two-pods proof on Postgres: two
// servers share one database and one KEK, both take the rotate call at the
// same moment, both run the janitor step, which promotes nothing, and both
// take the promote call at the same moment. The deployment ends with one
// active key, one create record and one promote record with the
// administrator as its actor. Each replica publishes the key and serves the
// same certificate, and a replica with another KEK refuses to boot.
func TestClientAssertionKeyTwoReplicas(t *testing.T) {
	t.Parallel()
	dsn := freshPostgresDSN(t)
	kek := filepath.Join(t.TempDir(), "secret.key")
	shared := func(cfg *config.Config) {
		cfg.Store = config.Store{Driver: config.DriverPostgres, DSN: dsn}
		cfg.Secrets.KEKFile = kek
	}
	clk := &edgeClock{t: time.Now()}
	withClock := []func(*App){func(a *App) { a.assertionKeys.clock = clk.now }}
	podA, baseA := testAppPreRun(t, withClock, shared)
	podB, baseB := testAppPreRun(t, withClock, shared)
	ctx := context.Background()
	kim := seedIdentity(t, podA)
	grantAdmin(t, podA, kim.ID)
	podB.resolver.Bump()
	tokA := loginDeviceFlow(t, baseA, "kim", "hunter2!")
	tokB := loginDeviceFlow(t, baseB, "kim", "hunter2!")

	var wg sync.WaitGroup
	results := make([]assertionRotation, 6)
	for i := range results {
		wg.Add(1)
		go func() {
			defer wg.Done()
			base, tok := baseA, tokA
			if i%2 == 1 {
				base, tok = baseB, tokB
			}
			if code := adminReq(t, "POST", base+"/v1/admin/signing-keys/client-assertion/rotate", tok, nil, &results[i]); code != http.StatusOK {
				t.Errorf("rotate %d = %d (%s)", i, code, results[i].Error)
			}
		}()
	}
	wg.Wait()
	for _, r := range results {
		if r.KID == "" || r.KID != results[0].KID {
			t.Fatalf("the replicas staged %q and %q, want one key", r.KID, results[0].KID)
		}
	}

	now := time.Now().Add(2 * authn.KeyReloadInterval)
	for range 2 {
		wg.Add(2)
		go func() { defer wg.Done(); podA.clientAssertionKeyStep(ctx, now) }()
		go func() { defer wg.Done(); podB.clientAssertionKeyStep(ctx, now) }()
		wg.Wait()
	}
	clk.set(now)
	for i := range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			base, tok := baseA, tokA
			if i%2 == 1 {
				base, tok = baseB, tokB
			}
			if code := adminReq(t, "POST", base+"/v1/admin/signing-keys/client-assertion/"+results[0].KID+"/promote", tok, nil, nil); code != http.StatusOK {
				t.Errorf("promote %d = %d", i, code)
			}
		}()
	}
	wg.Wait()
	if err := podB.assertionKeys.Reload(ctx, now); err != nil {
		t.Fatal(err)
	}
	keys, err := podA.store.SigningKeys().ListByPurpose(ctx, store.KeyPurposeClientAssertion)
	if err != nil || len(keys) != 1 || keys[0].Status != store.KeyActive {
		t.Fatalf("stored keys = %+v (%v), want exactly one active key", len(keys), err)
	}
	var got []string
	for _, ev := range keyAuditRecords(t, podA, store.KeyPurposeClientAssertion) {
		actor, _ := ev["actor"].(string)
		got = append(got, ev["action"].(string)+" "+actor)
	}
	if strings.Join(got, ",") != "signing-keys.create kim,signing-keys.promote kim" {
		t.Errorf("audit records = %v, want one create and one promote by kim for the whole deployment", got)
	}
	var certs [][]byte
	for name, pod := range map[string]struct {
		base, tok string
	}{"pod A": {baseA, tokA}, "pod B": {baseB, tokB}} {
		if _, _, body := rawGet(t, "GET", pod.base+assertionJWKS); !bytes.Contains(body, []byte(keys[0].KID)) {
			t.Errorf("%s does not publish the key: %s", name, body)
		}
		req, _ := http.NewRequest("GET", pod.base+"/v1/admin/signing-keys/client-assertion/"+keys[0].KID+"/certificate", nil)
		req.Header.Set("Authorization", "Bearer "+pod.tok)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("%s certificate = %d (%s)", name, resp.StatusCode, body)
		}
		certs = append(certs, body)
	}
	if !bytes.Equal(certs[0], certs[1]) {
		t.Error("the two replicas serve different certificate bytes, so Entra would see two thumbprints")
	}

	dir := t.TempDir()
	cfg := podA.cfg
	cfg.DataDir, cfg.Server.Listen = dir, "127.0.0.1:0"
	cfg.Secrets.KEKFile = filepath.Join(dir, "other.key")
	_, err = New(ctx, cfg, podA.log)
	if err == nil || !strings.Contains(err.Error(), "secrets.kekFile") {
		t.Errorf("a replica with another KEK booted: err = %v, want a refusal that names secrets.kekFile", err)
	}
}
