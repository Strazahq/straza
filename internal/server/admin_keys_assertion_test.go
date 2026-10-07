package server

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
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
// answers, the key document at every stage, one audit record per transition
// with actor, purpose and kid, and that no answer, record or log line carries
// private key material.
func TestClientAssertionKeyLifecycleOverTheAPI(t *testing.T) {
	t.Parallel()
	log, logs := captureLogger()
	app, base := testAppPreRun(t, []func(*App){func(a *App) { a.log = log }})
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

	staged1 := time.Now()
	app.clientAssertionKeyStep(ctx, staged1.Add(authn.KeyReloadInterval))
	wantRecords("one interval after staging", "signing-keys.create "+first.KID+" kim")
	app.clientAssertionKeyStep(ctx, staged1.Add(2*authn.KeyReloadInterval))
	app.clientAssertionKeyStep(ctx, staged1.Add(2*authn.KeyReloadInterval))
	wantRecords("after the promotion ran twice",
		"signing-keys.create "+first.KID+" kim", "signing-keys.promote "+first.KID+" ")

	second := rotate()
	if second.PreviousKID != first.KID || second.KID == first.KID {
		t.Errorf("second rotate = %+v, want a new key beside %s", second, first.KID)
	}
	if kids := docKIDs(); len(strings.Split(kids, ",")) != 2 {
		t.Errorf("document = %q, want the active and the staged key", kids)
	}
	staged2 := time.Now()
	app.clientAssertionKeyStep(ctx, staged2.Add(2*authn.KeyReloadInterval))
	demoted := time.Now()
	app.clientAssertionKeyStep(ctx, demoted.Add(app.assertionKeyRetireAfter()))
	if kids := docKIDs(); kids != second.KID {
		t.Errorf("document = %q, want only %s once the old key retired", kids, second.KID)
	}
	lifecycle := []string{
		"signing-keys.create " + first.KID + " kim", "signing-keys.promote " + first.KID + " ",
		"signing-keys.rotate " + second.KID + " kim", "signing-keys.promote " + second.KID + " ",
		"signing-keys.retire " + first.KID + " ",
	}
	wantRecords("after the second key took over", lifecycle...)

	sessionKeys, err := app.store.SigningKeys().ListByPurpose(ctx, store.KeyPurposeSession)
	if err != nil || len(sessionKeys) == 0 {
		t.Fatal(err)
	}
	retires := []struct {
		name, kid string
		want      int
		says      []string
	}{
		{"an unknown kid", "no-such-kid", http.StatusNotFound, []string{"no-such-kid", "strazactl signing-keys list"}},
		{"a session key", sessionKeys[0].KID, http.StatusConflict, []string{"session", "only a client assertion key", "strazactl signing-keys rotate session"}},
	}
	for _, tc := range retires {
		var out struct {
			Error string `json:"error"`
		}
		if code := adminReq(t, "POST", base+"/v1/admin/signing-keys/client-assertion/"+tc.kid+"/retire", tok, nil, &out); code != tc.want {
			t.Errorf("retire %s = %d, want %d", tc.name, code, tc.want)
		}
		for _, want := range tc.says {
			if !strings.Contains(out.Error, want) {
				t.Errorf("retire %s: refusal %q does not say %q", tc.name, out.Error, want)
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
// same moment and both run the janitor step, and the deployment ends with one
// key, one create record and one promote record. Each replica publishes the
// key and a replica with another KEK refuses to boot.
func TestClientAssertionKeyTwoReplicas(t *testing.T) {
	t.Parallel()
	dsn := freshPostgresDSN(t)
	kek := filepath.Join(t.TempDir(), "secret.key")
	shared := func(cfg *config.Config) {
		cfg.Store = config.Store{Driver: config.DriverPostgres, DSN: dsn}
		cfg.Secrets.KEKFile = kek
	}
	podA, baseA := testApp(t, shared)
	podB, baseB := testApp(t, shared)
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
	keys, err := podA.store.SigningKeys().ListByPurpose(ctx, store.KeyPurposeClientAssertion)
	if err != nil || len(keys) != 1 || keys[0].Status != store.KeyActive {
		t.Fatalf("stored keys = %+v (%v), want exactly one active key", len(keys), err)
	}
	var got []string
	for _, ev := range keyAuditRecords(t, podA, store.KeyPurposeClientAssertion) {
		got = append(got, ev["action"].(string))
	}
	if strings.Join(got, ",") != "signing-keys.create,signing-keys.promote" {
		t.Errorf("audit records = %v, want one create and one promote for the whole deployment", got)
	}
	for name, base := range map[string]string{"pod A": baseA, "pod B": baseB} {
		if _, _, body := rawGet(t, "GET", base+assertionJWKS); !bytes.Contains(body, []byte(keys[0].KID)) {
			t.Errorf("%s does not publish the key: %s", name, body)
		}
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
