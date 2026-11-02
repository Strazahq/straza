package server

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/strazahq/straza/internal/authn"
	"github.com/strazahq/straza/internal/clientassertion"
	"github.com/strazahq/straza/internal/config"
	"github.com/strazahq/straza/internal/store"
)

// keyRigStore fails every read of an agent's assertion key row with a
// Postgres outage while outage is set, and holds the next session create of
// the user a createHold names until the hold is released.
type keyRigStore struct {
	store.Store
	outage *atomic.Bool
	hold   *atomic.Pointer[createHold]
}

func (s keyRigStore) Settings() store.SettingsRepo {
	return keyRigSettings{s.Store.Settings(), s.outage}
}

func (s keyRigStore) Sessions() store.SessionRepo {
	return keyRigSessions{s.Store.Sessions(), s.hold}
}

type keyRigSettings struct {
	store.SettingsRepo
	outage *atomic.Bool
}

func (r keyRigSettings) Get(ctx context.Context, key string) (string, error) {
	if r.outage.Load() && strings.HasPrefix(key, nhiKeyPrefix) {
		return "", pgDown
	}
	return r.SettingsRepo.Get(ctx, key)
}

// createHold pauses one session create of userID: reached closes when the
// create arrives, and the create goes on once release closes.
type createHold struct {
	userID           string
	reached, release chan struct{}
}

type keyRigSessions struct {
	store.SessionRepo
	hold *atomic.Pointer[createHold]
}

func (r keyRigSessions) Create(ctx context.Context, s store.Session) (store.Session, error) {
	if h := r.hold.Load(); h != nil && h.userID == s.UserID && r.hold.CompareAndSwap(h, nil) {
		close(h.reached)
		<-h.release
	}
	return r.SessionRepo.Create(ctx, s)
}

// keyRig is a strazad with kim as its admin over a keyRigStore.
type keyRig struct {
	app    *App
	base   string
	admin  string
	outage *atomic.Bool
	hold   *atomic.Pointer[createHold]
}

func newKeyRig(t *testing.T) *keyRig {
	t.Helper()
	outage, hold := &atomic.Bool{}, &atomic.Pointer[createHold]{}
	app, base := testAppPreRun(t, []func(*App){func(a *App) {
		a.store = keyRigStore{Store: a.store, outage: outage, hold: hold}
	}})
	kim := seedIdentity(t, app)
	grantAdmin(t, app, kim.ID)
	admin, _ := checkinToken(t, app, base)
	return &keyRig{app: app, base: base, admin: admin, outage: outage, hold: hold}
}

// agent creates an AI agent through the admin API.
func (k *keyRig) agent(t *testing.T, username string) store.User {
	t.Helper()
	var created struct {
		ID string `json:"id"`
	}
	if code := adminReq(t, http.MethodPost, k.base+"/v1/admin/users", k.admin,
		map[string]any{"username": username, "kind": "nhi"}, &created); code != http.StatusCreated {
		t.Fatalf("create %s = %d", username, code)
	}
	u, err := k.app.store.Users().GetByID(context.Background(), created.ID)
	if err != nil {
		t.Fatal(err)
	}
	return u
}

// setKey registers pub for u and returns the status and body.
func (k *keyRig) setKey(t *testing.T, u store.User, pub ed25519.PublicKey) (int, map[string]any) {
	t.Helper()
	var out map[string]any
	code := adminReq(t, http.MethodPut, k.base+"/v1/admin/users/"+u.ID+"/nhi-key", k.admin,
		map[string]any{"public_key": base64.StdEncoding.EncodeToString(pub)}, &out)
	return code, out
}

// mustSetKey registers pub for u and fails the test unless it answers 200.
func (k *keyRig) mustSetKey(t *testing.T, u store.User, pub ed25519.PublicKey) {
	t.Helper()
	if code, out := k.setKey(t, u, pub); code != http.StatusOK {
		t.Fatalf("register a key for %s = %d %v", u.Username, code, out)
	}
}

// removeKey deletes u's key and returns the status and body.
func (k *keyRig) removeKey(t *testing.T, u store.User) (int, map[string]any) {
	t.Helper()
	var out map[string]any
	code := adminReq(t, http.MethodDelete, k.base+"/v1/admin/users/"+u.ID+"/nhi-key", k.admin, nil, &out)
	return code, out
}

// grant runs the client_credentials grant as u with priv and returns the
// ID token.
func (k *keyRig) grant(t *testing.T, u store.User, priv ed25519.PrivateKey) string {
	t.Helper()
	assertion, err := clientassertion.Mint(priv, u.Username, k.base, 0)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.PostForm(k.base+"/oidc/token", url.Values{
		"grant_type": {"client_credentials"}, "client_id": {u.Username},
		"client_assertion_type": {"urn:ietf:params:oauth:client-assertion-type:jwt-bearer"},
		"client_assertion":      {assertion},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	var body map[string]any
	if err := jsonNewDecoder(resp.Body).Decode(&body); err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("grant for %s = %d %v %v", u.Username, resp.StatusCode, body, err)
	}
	return body["id_token"].(string)
}

// newEd25519 returns a fresh key pair.
func newEd25519(t *testing.T) (ed25519.PublicKey, ed25519.PrivateKey) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return pub, priv
}

// loginFailuresOf returns the login failure records that name the user id.
func loginFailuresOf(t *testing.T, app *App, userID string) []map[string]any {
	t.Helper()
	return matching(t, app, "straza.audit.authn", func(d map[string]any) bool {
		return d["action"] == "login" && d["outcome"] == "failure" && d["userId"] == userID
	})
}

// stampDoor is one route that takes an agent's built-in ID token, and what
// it answers once the stamp passes.
type stampDoor struct {
	name, method, path string
	bearer             func(idt string) string
	body               func(idt string) any
	passCode           int
}

func asBearer(idt string) string { return idt }
func noBearer(string) string     { return "" }
func noBody(string) any          { return nil }

// enrollBody is an enrollment of client kind kind with the ID token.
func enrollBody(kind string) func(string) any {
	return func(idt string) any {
		return map[string]any{"id_token": idt, "client_kind": kind,
			"device": map[string]any{"name": "bot-host", "platform": "linux"}}
	}
}

// stampDoors are every reader of verifyLoginFacts. Past the stamp, the
// admin plane, the approvals lane and both enrollments refuse an agent with
// 403 on their own.
var stampDoors = []stampDoor{
	{"check-in", http.MethodPost, "/v1/checkin", noBearer, func(idt string) any { return nhiCheckinBody(idt) }, http.StatusOK},
	{"/v1/self", http.MethodGet, "/v1/self", asBearer, noBody, http.StatusOK},
	{"admin bearer", http.MethodGet, "/v1/admin/users", asBearer, noBody, http.StatusForbidden},
	{"approvals", http.MethodPost, "/v1/approvals/self/enroll-token", asBearer, func(string) any { return map[string]any{} }, http.StatusForbidden},
	{"enroll human", http.MethodPost, "/v1/enroll", noBearer, enrollBody("human"), http.StatusForbidden},
	{"enroll kit", http.MethodPost, "/v1/enroll", noBearer, enrollBody("kit"), http.StatusForbidden},
}

// TestNHIKeyStampRefusals pins the check of an agent ID token's key stamp on
// every route that takes one: a token stamped for the key registered now
// passes, a token stamped for a replaced or removed key is refused with 401,
// the stamp sentence and exactly one login failure naming the agent, a token
// without a stamp passes as before, and a failed key read answers the
// outage 503 with no record.
func TestNHIKeyStampRefusals(t *testing.T) {
	t.Parallel()
	k := newKeyRig(t)
	cases := []struct {
		name   string
		change func(t *testing.T, u store.User, pubA ed25519.PublicKey) // after the token is minted
		token  func(t *testing.T, u store.User, privA ed25519.PrivateKey) string
		outage bool
		want   int
	}{
		{name: "the key is unchanged", want: http.StatusOK},
		{name: "the key was rotated", want: http.StatusUnauthorized,
			change: func(t *testing.T, u store.User, _ ed25519.PublicKey) {
				pubB, _ := newEd25519(t)
				if code, out := k.setKey(t, u, pubB); code != http.StatusOK {
					t.Fatalf("rotate = %d %v", code, out)
				}
			}},
		{name: "the key was removed", want: http.StatusUnauthorized,
			change: func(t *testing.T, u store.User, _ ed25519.PublicKey) {
				if code, out := k.removeKey(t, u); code != http.StatusOK {
					t.Fatalf("remove = %d %v", code, out)
				}
			}},
		{name: "the same key was registered again", want: http.StatusOK,
			change: func(t *testing.T, u store.User, pubA ed25519.PublicKey) {
				if code, out := k.setKey(t, u, pubA); code != http.StatusOK {
					t.Fatalf("register again = %d %v", code, out)
				}
			}},
		{name: "a token minted without a stamp", want: http.StatusOK,
			change: func(t *testing.T, u store.User, _ ed25519.PublicKey) {
				pubB, _ := newEd25519(t)
				if code, out := k.setKey(t, u, pubB); code != http.StatusOK {
					t.Fatalf("rotate = %d %v", code, out)
				}
			},
			token: func(t *testing.T, u store.User, _ ed25519.PrivateKey) string {
				idt, err := k.app.tokens.MintIDToken(u.ID, "straza", 5*time.Minute, u.Username, "")
				if err != nil {
					t.Fatal(err)
				}
				return idt
			}},
		{name: "the key read fails", outage: true, want: http.StatusServiceUnavailable},
	}
	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			u := k.agent(t, "stamp-bot-"+string(rune('a'+i)))
			pubA, privA := newEd25519(t)
			if code, out := k.setKey(t, u, pubA); code != http.StatusOK {
				t.Fatalf("register = %d %v", code, out)
			}
			idt := k.grant(t, u, privA)
			if tc.token != nil {
				idt = tc.token(t, u, privA)
			}
			if tc.change != nil {
				tc.change(t, u, pubA)
			}
			k.outage.Store(tc.outage)
			defer k.outage.Store(false)

			for _, d := range stampDoors {
				before := len(loginFailuresOf(t, k.app, u.ID))
				code, hdr, out := callJSON(t, d.method, k.base+d.path, d.bearer(idt), d.body(idt))
				failures := loginFailuresOf(t, k.app, u.ID)
				switch tc.want {
				case http.StatusOK:
					// The stamp passes, and the door then answers as it does for
					// any agent: 200, or its own refusal of an agent.
					if code != d.passCode || out["error"] == staleKeyStampMsg || (d.passCode == http.StatusOK && len(failures) != before) {
						t.Errorf("%s = %d %v with %d new failures, want %d past the stamp", d.name, code, out, len(failures)-before, d.passCode)
					}
				case http.StatusServiceUnavailable:
					requireOutage(t, d.name, code, hdr, out)
					if len(failures) != before {
						t.Errorf("%s: %d login failures after an outage, want none", d.name, len(failures)-before)
					}
				default:
					if code != tc.want || out["error"] != staleKeyStampMsg {
						t.Fatalf("%s = %d %v, want %d %q", d.name, code, out, tc.want, staleKeyStampMsg)
					}
					if len(failures) != before+1 {
						t.Fatalf("%s: %d new login failures, want exactly one", d.name, len(failures)-before)
					}
					f := failures[len(failures)-1]
					if f["via"] != "id-token" || f["user"] != u.Username || f["reason"] != staleKeyStampReason {
						t.Errorf("%s: login failure = %v, want via id-token naming %s with reason %q", d.name, f, u.Username, staleKeyStampReason)
					}
				}
			}
		})
	}
}

// TestNHIKeyStampExternalToken pins that the stamp check reads only the
// built-in issuer's tokens: a stamped agent token from another Straza that
// is this server's external identity provider checks in, although this
// server holds no key for the user.
func TestNHIKeyStampExternalToken(t *testing.T) {
	t.Parallel()
	idp := newKeyRig(t)
	bot := idp.agent(t, "ext-bot")
	pub, priv := newEd25519(t)
	if code, out := idp.setKey(t, bot, pub); code != http.StatusOK {
		t.Fatalf("register at the IdP = %d %v", code, out)
	}
	idt := idp.grant(t, bot, priv)
	claims, err := idp.app.tokens.VerifyIDToken(idt, "straza")
	if err != nil || claims.KeyThumbprint == "" {
		t.Fatalf("the IdP's token carries no stamp: %+v %v", claims, err)
	}

	platform, platformBase := testApp(t, func(cfg *config.Config) {
		cfg.OIDC.Issuer = idp.base
		cfg.OIDC.ClientID = "straza"
		cfg.OIDC.JITProvision = true
	})
	code, out := postJSON(t, platformBase+"/v1/checkin", nhiCheckinBody(idt))
	if code != http.StatusOK {
		t.Fatalf("external check-in = %d %v, want 200", code, out)
	}
	u, err := platform.store.Users().GetByUsername(context.Background(), "ext-bot")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := platform.registeredNHIKey(context.Background(), u.ID); err == nil {
		t.Fatal("the platform holds a key for the user, so the row proves nothing")
	}
}

// TestNHIProofMustMatchStamp pins the proof key of a stamped agent check-in:
// a proof from the key the token names starts a session bound to it, and a
// proof from another key is refused with 401, invalid_dpop_proof, the stamp
// sentence and exactly one login failure, with no session. A token minted
// without a stamp still takes a proof from any key.
func TestNHIProofMustMatchStamp(t *testing.T) {
	t.Parallel()
	k := newKeyRig(t)
	cases := []struct {
		name     string
		stamped  bool
		sameKey  bool
		wantCode int
	}{
		{"the proof comes from the stamped key", true, true, http.StatusOK},
		{"the proof comes from another key", true, false, http.StatusUnauthorized},
		{"an unstamped token with a proof from any key", false, false, http.StatusOK},
	}
	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			u := k.agent(t, "proof-bot-"+string(rune('a'+i)))
			pub, priv := newEd25519(t)
			if code, out := k.setKey(t, u, pub); code != http.StatusOK {
				t.Fatalf("register = %d %v", code, out)
			}
			idt := k.grant(t, u, priv)
			if !tc.stamped {
				var err error
				if idt, err = k.app.tokens.MintIDToken(u.ID, "straza", 5*time.Minute, u.Username, ""); err != nil {
					t.Fatal(err)
				}
			}
			h := newKeyHolder(t, k.base)
			if tc.sameKey {
				h.priv = priv
			}
			before := len(loginFailuresOf(t, k.app, u.ID))
			code, hdr, out := h.send("/v1/checkin", "", nhiCheckinBody(idt))
			if code == http.StatusUnauthorized && strings.Contains(hdr.Get("WWW-Authenticate"), "use_dpop_nonce") {
				code, hdr, out = h.send("/v1/checkin", "", nhiCheckinBody(idt))
			}
			failures := loginFailuresOf(t, k.app, u.ID)
			sessions, err := k.app.store.Sessions().ListByUser(context.Background(), u.ID)
			if err != nil {
				t.Fatal(err)
			}
			if tc.wantCode == http.StatusOK {
				if code != http.StatusOK || len(failures) != before {
					t.Fatalf("check-in = %d %v with %d new failures, want 200 and none", code, out, len(failures)-before)
				}
				if c, err := k.app.tokens.Verify(out["session_token"].(string)); err != nil || c.CNF != h.thumbprint() {
					t.Errorf("session claims = %+v %v, want cnf %s", c, err, h.thumbprint())
				}
				return
			}
			if code != tc.wantCode || out["error"] != proofNotStampMsg ||
				!strings.Contains(hdr.Get("WWW-Authenticate"), `error="invalid_dpop_proof"`) {
				t.Fatalf("check-in = %d %v %v, want %d %q with invalid_dpop_proof", code, hdr, out, tc.wantCode, proofNotStampMsg)
			}
			if len(failures) != before+1 {
				t.Fatalf("%d new login failures, want exactly one", len(failures)-before)
			}
			f := failures[len(failures)-1]
			if f["via"] != "id-token" || f["user"] != u.Username || f["harness"] != "claude-code/2.1.0" || f["reason"] != proofNotStampReason {
				t.Errorf("login failure = %v, want via id-token naming %s with reason %q", f, u.Username, proofNotStampReason)
			}
			if len(sessions) != 0 {
				t.Errorf("sessions = %d after the refusal, want none", len(sessions))
			}
		})
	}
}

// TestNHIKeyChangeEndsSessions pins the session end of a key change: a key
// that replaces a different one, a removed key and a row that held no key
// end every active session of the agent, with the revocation row, the
// denylist entry, one straza.revocation.sessions event and exactly one
// session.end record per ended session. A first key and the same key again
// end nothing, and another user's session is never touched.
func TestNHIKeyChangeEndsSessions(t *testing.T) {
	t.Parallel()
	k := newKeyRig(t)
	ctx := context.Background()
	bystander := k.agent(t, "bystander-bot")
	other, err := k.app.store.Sessions().Create(ctx, store.Session{UserID: bystander.ID, HarnessName: "claude-code", Status: store.SessionActive})
	if err != nil {
		t.Fatal(err)
	}

	pubA, _ := newEd25519(t)
	pubB, _ := newEd25519(t)
	cases := []struct {
		name       string
		before     func(t *testing.T, u store.User) // the key state before the change
		change     func(t *testing.T, u store.User) (int, map[string]any)
		wantReason string // "" when no session ends
	}{
		{name: "a new key replaces the old one",
			before:     func(t *testing.T, u store.User) { k.mustSetKey(t, u, pubA) },
			change:     func(t *testing.T, u store.User) (int, map[string]any) { return k.setKey(t, u, pubB) },
			wantReason: nhiKeyReplacedReason},
		{name: "the key is removed",
			before:     func(t *testing.T, u store.User) { k.mustSetKey(t, u, pubA) },
			change:     func(t *testing.T, u store.User) (int, map[string]any) { return k.removeKey(t, u) },
			wantReason: nhiKeyRemovedReason},
		{name: "a row that held no key is replaced",
			before: func(t *testing.T, u store.User) {
				if err := k.app.store.Settings().Set(ctx, nhiKeyPrefix+u.ID, `{"publicKey":"not-a-key"}`); err != nil {
					t.Fatal(err)
				}
			},
			change:     func(t *testing.T, u store.User) (int, map[string]any) { return k.setKey(t, u, pubA) },
			wantReason: nhiKeyReplacedReason},
		{name: "a first key",
			before: func(*testing.T, store.User) {},
			change: func(t *testing.T, u store.User) (int, map[string]any) { return k.setKey(t, u, pubA) }},
		{name: "the same key again",
			before: func(t *testing.T, u store.User) { k.mustSetKey(t, u, pubA) },
			change: func(t *testing.T, u store.User) (int, map[string]any) { return k.setKey(t, u, pubA) }},
	}
	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			u := k.agent(t, "end-bot-"+string(rune('a'+i)))
			tc.before(t, u)
			var live []string
			for range 2 {
				s, err := k.app.store.Sessions().Create(ctx, store.Session{UserID: u.ID, HarnessName: "claude-code", Status: store.SessionActive})
				if err != nil {
					t.Fatal(err)
				}
				live = append(live, s.ID)
			}
			gone, err := k.app.store.Sessions().Create(ctx, store.Session{UserID: u.ID, HarnessName: "claude-code", Status: store.SessionRevoked})
			if err != nil {
				t.Fatal(err)
			}

			if code, out := tc.change(t, u); code != http.StatusOK {
				t.Fatalf("change = %d %v", code, out)
			}

			wantEnded := tc.wantReason != ""
			for _, id := range live {
				s, err := k.app.store.Sessions().GetByID(ctx, id)
				if err != nil {
					t.Fatal(err)
				}
				revs, err := k.app.store.Revocations().ListByTarget(ctx, store.RevokeSession, id)
				if err != nil {
					t.Fatal(err)
				}
				blocked := k.app.denylist.blocked(authn.Claims{Session: id})
				if wantEnded {
					if s.Status != store.SessionRevoked || len(revs) != 1 || revs[0].Reason != tc.wantReason || !blocked {
						t.Errorf("session %s: status %s, revocations %+v, denylisted %v, want revoked with %q", id, s.Status, revs, blocked, tc.wantReason)
					}
				} else if s.Status != store.SessionActive || len(revs) != 0 || blocked {
					t.Errorf("session %s: status %s, revocations %+v, denylisted %v, want untouched", id, s.Status, revs, blocked)
				}
			}

			ends := matching(t, k.app, "straza.audit.authn", func(d map[string]any) bool {
				return d["action"] == "session.end" && d["userId"] == u.ID
			})
			var events [][]any
			for _, d := range outboxDataFor(t, k.app, "straza.revocation.sessions") {
				ids, _ := d["sessions"].([]any)
				if slices.Contains(ids, any(live[0])) || slices.Contains(ids, any(gone.ID)) {
					events = append(events, ids)
					if d["reason"] != tc.wantReason {
						t.Errorf("event reason = %v, want %q", d["reason"], tc.wantReason)
					}
				}
			}
			if !wantEnded {
				if len(ends) != 0 || len(events) != 0 {
					t.Errorf("session.end records %v and events %v, want none", ends, events)
				}
				return
			}
			if len(events) != 1 || len(events[0]) != 2 || !slices.Contains(events[0], any(live[1])) {
				t.Errorf("revocation events = %v, want one naming %v", events, live)
			}
			if len(ends) != len(live) {
				t.Fatalf("session.end records = %d, want exactly one per ended session (%d)", len(ends), len(live))
			}
			for _, e := range ends {
				if e["outcome"] != "revoked-admin" || e["reason"] != tc.wantReason || e["user"] != u.Username ||
					!slices.Contains(live, e["session"].(string)) {
					t.Errorf("session.end = %v, want revoked-admin for one of %v with %q", e, live, tc.wantReason)
				}
			}
		})
	}

	s, err := k.app.store.Sessions().GetByID(ctx, other.ID)
	if err != nil || s.Status != store.SessionActive || k.app.denylist.blocked(authn.Claims{Session: other.ID}) {
		t.Errorf("another user's session = %+v %v, want active and not denylisted", s, err)
	}
}

// TestNHIKeyChangeRacesCheckin pins the second stamp check of an agent's
// check-in, made once its session row exists. The check-in passes the first
// check and is held at the session create while the key changes or the key
// read starts to fail, so the key change's session end cannot see the row.
// A key change answers the stamp 401 with exactly one login failure, and a
// failed read answers the outage 503 with none. Either way the new session
// is revoked and denylisted, and no session.start, login success or
// session.end names it. With no change during the hold, the session starts.
func TestNHIKeyChangeRacesCheckin(t *testing.T) {
	t.Parallel()
	k := newKeyRig(t)
	ctx := context.Background()
	cases := []struct {
		name   string
		during func(t *testing.T, u store.User)
		want   int
	}{
		{"the key is replaced while the session row is written", func(t *testing.T, u store.User) {
			pubB, _ := newEd25519(t)
			k.mustSetKey(t, u, pubB)
		}, http.StatusUnauthorized},
		{"the key is removed while the session row is written", func(t *testing.T, u store.User) {
			if code, out := k.removeKey(t, u); code != http.StatusOK {
				t.Fatalf("remove = %d %v", code, out)
			}
		}, http.StatusUnauthorized},
		{"the key read fails once the session row is written", func(*testing.T, store.User) { k.outage.Store(true) },
			http.StatusServiceUnavailable},
		{"nothing changes while the session row is written", func(*testing.T, store.User) {}, http.StatusOK},
	}
	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			u := k.agent(t, "race-bot-"+string(rune('a'+i)))
			pubA, privA := newEd25519(t)
			k.mustSetKey(t, u, pubA)
			idt := k.grant(t, u, privA)

			hold := &createHold{userID: u.ID, reached: make(chan struct{}), release: make(chan struct{})}
			k.hold.Store(hold)
			type answer struct {
				code int
				hdr  http.Header
				out  map[string]any
				err  error
			}
			done := make(chan answer, 1)
			go func() {
				resp, err := http.Post(k.base+"/v1/checkin", "application/json", strings.NewReader(string(mustJSON(nhiCheckinBody(idt)))))
				if err != nil {
					done <- answer{err: err}
					return
				}
				defer func() { _ = resp.Body.Close() }()
				var out map[string]any
				err = jsonNewDecoder(resp.Body).Decode(&out)
				done <- answer{resp.StatusCode, resp.Header, out, err}
			}()
			select {
			case <-hold.reached:
			case <-time.After(30 * time.Second):
				t.Fatal("the check-in never reached the session create")
			}
			before := len(loginFailuresOf(t, k.app, u.ID))
			tc.during(t, u)
			close(hold.release)
			got := <-done
			k.outage.Store(false)
			if got.err != nil {
				t.Fatal(got.err)
			}

			sessions, err := k.app.store.Sessions().ListByUser(ctx, u.ID)
			if err != nil || len(sessions) != 1 {
				t.Fatalf("sessions = %+v %v, want the one the check-in wrote", sessions, err)
			}
			ses := sessions[0]
			blocked := k.app.denylist.blocked(authn.Claims{Session: ses.ID})
			starts := matching(t, k.app, "straza.identity.updated", func(d map[string]any) bool {
				return d["action"] == "session.start" && d["session"] == ses.ID
			})
			named := matching(t, k.app, "straza.audit.authn", func(d map[string]any) bool { return d["session"] == ses.ID })
			failures := loginFailuresOf(t, k.app, u.ID)

			if tc.want == http.StatusOK {
				if got.code != http.StatusOK || ses.Status != store.SessionActive || blocked || len(starts) != 1 || len(failures) != before {
					t.Errorf("check-in = %d %v, session %s denylisted %v, %d session.start, %d failures, want a started session",
						got.code, got.out, ses.Status, blocked, len(starts), len(failures)-before)
				}
				return
			}
			wantReason := staleKeyStampReason
			if tc.want == http.StatusServiceUnavailable {
				wantReason = "the agent's key could not be read when the session started"
				requireOutage(t, tc.name, got.code, got.hdr, got.out)
				if len(failures) != before {
					t.Errorf("%d login failures after an outage, want none", len(failures)-before)
				}
			} else {
				if got.code != tc.want || got.out["error"] != staleKeyStampMsg {
					t.Fatalf("check-in = %d %v, want %d %q", got.code, got.out, tc.want, staleKeyStampMsg)
				}
				if len(failures) != before+1 || failures[len(failures)-1]["reason"] != staleKeyStampReason {
					t.Errorf("login failures = %v, want exactly one more with %q", failures, staleKeyStampReason)
				}
			}
			if _, ok := got.out["session_token"]; ok {
				t.Error("the refused check-in handed out a session token")
			}
			revs, err := k.app.store.Revocations().ListByTarget(ctx, store.RevokeSession, ses.ID)
			if err != nil || ses.Status != store.SessionRevoked || !blocked || len(revs) != 1 || revs[0].Reason != wantReason {
				t.Errorf("session %s status %s, denylisted %v, revocations %+v %v, want revoked and denylisted with %q",
					ses.ID, ses.Status, blocked, revs, err, wantReason)
			}
			if len(starts) != 0 || len(named) != 0 {
				t.Errorf("session.start %v and authn records %v name the refused session, want none", starts, named)
			}
		})
	}
}
