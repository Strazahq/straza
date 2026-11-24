package server

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"path/filepath"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/strazahq/straza/internal/authn"
	"github.com/strazahq/straza/internal/config"
	"github.com/strazahq/straza/internal/store"
)

// TestSigningKeyRotate pins POST /v1/admin/signing-keys/rotate: a 200 with
// the staged kid, status staged and the two timings derived from the reload
// interval and the retirement horizon, exactly one straza.audit.admin record
// naming the actor and the kid, the staged public key in the JWKS at once,
// and a second call answering the same kid because a staged key exists.
func TestSigningKeyRotate(t *testing.T) {
	t.Parallel()
	app, base := testApp(t)
	user := seedIdentity(t, app)
	idToken := loginDeviceFlow(t, base, "kim", "hunter2!")
	grantAdmin(t, app, user.ID)

	type rotation struct {
		KID         string `json:"kid"`
		Status      string `json:"status"`
		ActiveIn    int    `json:"active_in_seconds"`
		PreviousFor int    `json:"previous_key_verifies_for_seconds"`
	}
	var first rotation
	if code := adminReq(t, "POST", base+"/v1/admin/signing-keys/rotate", idToken, nil, &first); code != http.StatusOK {
		t.Fatalf("rotate = %d", code)
	}
	if first.KID == "" || first.Status != store.KeyStaged {
		t.Errorf("body = %+v, want a kid with status staged", first)
	}
	if want := int((2 * authn.KeyReloadInterval).Seconds()); first.ActiveIn != want {
		t.Errorf("active_in_seconds = %d, want %d", first.ActiveIn, want)
	}
	if want := int((2*authn.KeyReloadInterval + app.keyRetireAfter()).Seconds()); first.PreviousFor != want {
		t.Errorf("previous_key_verifies_for_seconds = %d, want %d", first.PreviousFor, want)
	}

	var rotates []map[string]any
	for _, ev := range adminAuditEvents(t, app) {
		if ev["action"] == "signing-keys.rotate" {
			rotates = append(rotates, ev)
		}
	}
	if len(rotates) != 1 {
		t.Fatalf("straza.audit.admin signing-keys.rotate records = %d, want exactly 1", len(rotates))
	}
	if ev := rotates[0]; ev["kid"] != first.KID || ev["purpose"] != store.KeyPurposeSession || ev["actor"] != "kim" || ev["actorId"] != user.ID || ev["actorVia"] != "login" {
		t.Errorf("audit record = %v, want the session key %s by kim via login", ev, first.KID)
	}
	for k := range rotates[0] {
		if k == "private_key" || k == "privateKey" {
			t.Errorf("audit record carries %s", k)
		}
	}

	keys, err := app.store.SigningKeys().ListByPurpose(context.Background(), store.KeyPurposeSession)
	if err != nil {
		t.Fatal(err)
	}
	var staged int
	for _, k := range keys {
		if k.Status == store.KeyStaged {
			staged++
			if k.KID != first.KID {
				t.Errorf("staged key in store = %s, want %s", k.KID, first.KID)
			}
		}
	}
	if staged != 1 {
		t.Errorf("staged keys in store = %d, want 1", staged)
	}

	resp, err := http.Get(base + "/.well-known/straza/jwks.json")
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	var jwks struct {
		Keys []map[string]any `json:"keys"`
	}
	if err := json.Unmarshal(raw, &jwks); err != nil {
		t.Fatalf("jwks: %v", err)
	}
	var published bool
	for _, k := range jwks.Keys {
		if k["kid"] == first.KID {
			published = true
		}
	}
	if !published || len(jwks.Keys) != 2 {
		t.Errorf("JWKS = %d keys, staged key published %v; want 2 keys with the staged one", len(jwks.Keys), published)
	}

	var second rotation
	if code := adminReq(t, "POST", base+"/v1/admin/signing-keys/rotate", idToken, nil, &second); code != http.StatusOK {
		t.Fatalf("second rotate = %d", code)
	}
	if second.KID != first.KID {
		t.Errorf("second call staged %s, want the existing staged key %s", second.KID, first.KID)
	}
}

// sessionKeyRecords renders the straza.audit.admin records of the session
// key life cycle, oldest first, as "action kid previous_kid actor". It fails
// the test when a record carries a field outside the life cycle's set, or an
// actor's id without its name.
func sessionKeyRecords(t *testing.T, app *App) []string {
	t.Helper()
	known := []string{"action", "purpose", "kid", "previous_kid", "actor", "actorId", "actorVia"}
	var out []string
	for _, ev := range keyAuditRecords(t, app, store.KeyPurposeSession) {
		for k := range ev {
			if !slices.Contains(known, k) {
				t.Errorf("record %v carries the field %s", ev, k)
			}
		}
		actor, _ := ev["actor"].(string)
		if _, id := ev["actorId"]; id != (actor != "") {
			t.Errorf("record %v names half an actor", ev)
		}
		if _, via := ev["actorVia"]; via != (actor != "") {
			t.Errorf("record %v names half an actor", ev)
		}
		previous, _ := ev["previous_kid"].(string)
		out = append(out, ev["action"].(string)+" "+ev["kid"].(string)+" "+previous+" "+actor)
	}
	return out
}

// TestSessionKeyLifecycleRecords walks two rotations of the session key
// through the janitor's step with a clock the test moves, and pins the
// straza.audit.admin records: the administrator's rotate record, one promote
// record that names the key it replaced, one retire record per key, the
// janitor's three without an actor, and none for a pass that changed nothing.
func TestSessionKeyLifecycleRecords(t *testing.T) {
	t.Parallel()
	app, base := testApp(t)
	ctx := context.Background()
	kim := seedIdentity(t, app)
	idToken := loginDeviceFlow(t, base, "kim", "hunter2!")
	grantAdmin(t, app, kim.ID)
	keys, err := app.store.SigningKeys().ListByPurpose(ctx, store.KeyPurposeSession)
	if err != nil || len(keys) != 1 || keys[0].Status != store.KeyActive {
		t.Fatalf("session keys at boot = %+v (%v), want one active key", keys, err)
	}
	old := keys[0].KID
	var want []string
	check := func(when string) {
		t.Helper()
		if got := sessionKeyRecords(t, app); !slices.Equal(got, want) {
			t.Fatalf("%s: records = %q, want %q", when, got, want)
		}
	}
	rotate := func() string {
		t.Helper()
		var out struct {
			KID string `json:"kid"`
		}
		if code := adminReq(t, "POST", base+"/v1/admin/signing-keys/rotate", idToken, nil, &out); code != http.StatusOK || out.KID == "" {
			t.Fatalf("rotate = %d with kid %q", code, out.KID)
		}
		return out.KID
	}

	app.sessionKeyStep(ctx, time.Now().Add(time.Hour))
	check("a pass with no staged key")

	first := rotate()
	want = append(want, "signing-keys.rotate "+first+"  kim")
	check("after the rotate call")
	app.sessionKeyStep(ctx, time.Now().Add(authn.KeyReloadInterval))
	check("one interval after staging")
	// The store stamps a demotion with the real clock, so the horizon of
	// both old keys lies between these two readings plus keyRetireAfter.
	beforeDemotions := time.Now()
	promoted := time.Now().Add(2*authn.KeyReloadInterval + time.Second)
	app.sessionKeyStep(ctx, promoted)
	want = append(want, "signing-keys.promote "+first+" "+old+" ")
	check("two intervals after staging")
	app.sessionKeyStep(ctx, promoted)
	check("a second pass in the same tick")

	second := rotate()
	want = append(want, "signing-keys.rotate "+second+"  kim")
	promoted = time.Now().Add(2*authn.KeyReloadInterval + time.Second)
	app.sessionKeyStep(ctx, promoted)
	want = append(want, "signing-keys.promote "+second+" "+first+" ")
	check("the second rotation")
	afterDemotions := time.Now()

	app.sessionKeyStep(ctx, beforeDemotions.Add(app.keyRetireAfter()-time.Second))
	check("one second before the horizon")
	app.sessionKeyStep(ctx, afterDemotions.Add(app.keyRetireAfter()))
	got := sessionKeyRecords(t, app)
	if len(got) != len(want)+2 {
		t.Fatalf("at the horizon: records = %q, want two retire records after %q", got, want)
	}
	retired := got[len(want):]
	slices.Sort(retired)
	wantRetired := []string{"signing-keys.retire " + first + "  ", "signing-keys.retire " + old + "  "}
	slices.Sort(wantRetired)
	if !slices.Equal(retired, wantRetired) {
		t.Fatalf("at the horizon: retire records = %q, want %q", retired, wantRetired)
	}
	want = sessionKeyRecords(t, app)
	app.sessionKeyStep(ctx, afterDemotions.Add(app.keyRetireAfter()+time.Hour))
	check("a pass after the retirement")
}

// TestSessionKeyStepTwoReplicas is the two-pods proof on Postgres: two
// servers share one database and one KEK and run the janitor's step in the
// same tick, at the promotion and at the horizon of the old key. The
// deployment writes one promote record and one retire record.
func TestSessionKeyStepTwoReplicas(t *testing.T) {
	t.Parallel()
	dsn := freshPostgresDSN(t)
	kek := filepath.Join(t.TempDir(), "secret.key")
	shared := func(cfg *config.Config) {
		cfg.Store = config.Store{Driver: config.DriverPostgres, DSN: dsn}
		cfg.Secrets.KEKFile = kek
	}
	podA, baseA := testAppPreRun(t, nil, shared)
	podB, _ := testAppPreRun(t, nil, shared)
	ctx := context.Background()
	kim := seedIdentity(t, podA)
	grantAdmin(t, podA, kim.ID)
	keys, err := podA.store.SigningKeys().ListByPurpose(ctx, store.KeyPurposeSession)
	if err != nil || len(keys) != 1 {
		t.Fatalf("session keys at boot = %+v (%v), want one key for both replicas", keys, err)
	}
	old := keys[0].KID
	var staged struct {
		KID string `json:"kid"`
	}
	if code := adminReq(t, "POST", baseA+"/v1/admin/signing-keys/rotate", loginDeviceFlow(t, baseA, "kim", "hunter2!"), nil, &staged); code != http.StatusOK {
		t.Fatalf("rotate = %d", code)
	}
	both := func(now time.Time) {
		var wg sync.WaitGroup
		for _, pod := range []*App{podA, podB} {
			wg.Add(1)
			go func() { defer wg.Done(); pod.sessionKeyStep(ctx, now) }()
		}
		wg.Wait()
	}
	for range 2 {
		both(time.Now().Add(2*authn.KeyReloadInterval + time.Second))
	}
	for range 2 {
		both(time.Now().Add(podA.keyRetireAfter()))
	}
	want := []string{
		"signing-keys.rotate " + staged.KID + "  kim",
		"signing-keys.promote " + staged.KID + " " + old + " ",
		"signing-keys.retire " + old + "  ",
	}
	if got := sessionKeyRecords(t, podA); !slices.Equal(got, want) {
		t.Errorf("records = %q, want %q for the whole deployment", got, want)
	}
}

// failRetireKeys is a signing key store whose retirement write fails once
// armed.
type failRetireKeys struct {
	store.SigningKeyRepo
	fail atomic.Bool
}

func (r *failRetireKeys) Retire(ctx context.Context, kid, from string) (bool, error) {
	if r.fail.Load() {
		return false, errors.New("the store is down")
	}
	return r.SigningKeyRepo.Retire(ctx, kid, from)
}

// TestSessionKeyStepRecordsTheChangeOfAFailedPass pins a janitor pass that
// promotes a key and then fails on the retirement of an older one: the
// promotion is in the store, so its record is written all the same.
func TestSessionKeyStepRecordsTheChangeOfAFailedPass(t *testing.T) {
	t.Parallel()
	repo := &failRetireKeys{}
	app, base := testAppPreRun(t, []func(*App){func(a *App) {
		repo.SigningKeyRepo = a.store.SigningKeys()
		tokens, err := authn.NewTokenService(context.Background(), repo, a.kek, a.cfg.Server.PublicURL, 0)
		if err != nil {
			t.Fatal(err)
		}
		a.tokens = tokens
		a.http.Handler = a.routes()
	}})
	ctx := context.Background()
	kim := seedIdentity(t, app)
	idToken := loginDeviceFlow(t, base, "kim", "hunter2!")
	grantAdmin(t, app, kim.ID)
	rotate := func() string {
		t.Helper()
		var out struct {
			KID string `json:"kid"`
		}
		if code := adminReq(t, "POST", base+"/v1/admin/signing-keys/rotate", idToken, nil, &out); code != http.StatusOK || out.KID == "" {
			t.Fatalf("rotate = %d with kid %q", code, out.KID)
		}
		return out.KID
	}
	first := rotate()
	app.sessionKeyStep(ctx, time.Now().Add(2*authn.KeyReloadInterval+time.Second))
	second := rotate()

	repo.fail.Store(true)
	app.sessionKeyStep(ctx, time.Now().Add(app.keyRetireAfter()+time.Minute))
	got := sessionKeyRecords(t, app)
	if want := "signing-keys.promote " + second + " " + first + " "; len(got) != 4 || got[3] != want {
		t.Fatalf("records after the failed pass = %q, want four that end with %q", got, want)
	}
	keys, err := app.store.SigningKeys().ListByPurpose(ctx, store.KeyPurposeSession)
	if err != nil {
		t.Fatal(err)
	}
	for _, k := range keys {
		if k.Status == store.KeyRetired {
			t.Errorf("the key %s is retired although the retirement write failed", k.KID)
		}
	}
}
