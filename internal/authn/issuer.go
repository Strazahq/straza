package authn

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"html/template"
	"net/http"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/bcrypt"

	"github.com/strazahq/straza/internal/store"
)

// Issuer is the built-in OIDC provider: discovery, RFC 8628 device
// authorization, the authorization code flow with PKCE for programs on the
// person's own machine (issuer_code.go), the token endpoint, and minimal
// local-user login pages. The standalone profile signs every human in here.
// The enterprise profile mounts the same issuer for two callers only: NHIs
// on the client_credentials grant, and the break-glass admin on the login
// pages (LoginOnly), so a lockout by the identity provider stays
// recoverable. It is deliberately NOT a general-purpose IdP, just enough
// for `straza enroll` and the console's sign-in.
//
// Grants and codes live in memory: standalone runs a single process, and a
// lost grant or code only means re-running the login (fail closed).
type Issuer struct {
	users   store.UserRepo
	tokens  *TokenService
	baseURL string

	// NHIKeys enables the client_credentials grant for headless NHI
	// principals (issuer_nhi.go). nil ⇒ the grant is refused.
	NHIKeys NHIKeyLookup

	// LoginOnly, when set, restricts both login pages to that one username
	// and presents them as the emergency sign-in. The enterprise
	// profile sets it to the break-glass admin: humans there have no local
	// passwords, so a hash on any other row must never open these
	// pages. Every other username is refused before its credential is
	// judged, and the refusal fires OnLoginFailed with an empty username.
	LoginOnly string

	// OnLogin, when set, observes every successful interactive login
	// (userID, username). The server uses it to alarm break-glass
	// authentications on the audit chain.
	OnLogin func(userID, username string)

	// OnLoginFailed, when set, observes every refused password submit
	// (spec/events rev 21 authn producer). username is the post-zeroing
	// value: "" for an unknown, passwordless, or inactive account, so the
	// observer can never fabricate an identity from attacker input. reason
	// is ReasonBadPassword or ReasonNonPersonPassword. r is the submit
	// request (the sourceIp/userAgent source). Unknown or expired device
	// codes do NOT fire it: no credential was judged.
	OnLoginFailed func(username, reason string, r *http.Request)

	// OnNHIGrant and OnNHIGrantFailed observe every judged
	// client_credentials outcome (spec/events rev 22 authn producer). The
	// same honesty rule as OnLoginFailed applies: userID and username are ""
	// unless the client lookup succeeded, so an attacker-chosen client_id
	// never becomes chained identity. A malformed request (missing
	// assertion) judges no credential and fires neither; a mint outage is a
	// server fault, not an authn outcome, and fires neither.
	OnNHIGrant       func(userID, username string, r *http.Request)
	OnNHIGrantFailed func(userID, username, reason string, r *http.Request)

	grantTTL time.Duration
	interval time.Duration
	idTTL    time.Duration

	mu         sync.Mutex
	grants     map[string]*deviceGrant // device_code → grant
	byUserCode map[string]string       // user_code → device_code
	codes      map[string]*authCode    // authorization code → its binding
	seenJTI    map[string]time.Time    // assertion jti → exp (replay cache)
	lastSweep  time.Time               // when gcLocked last swept grants and codes
}

// sweepEvery is the least time between two sweeps of expired grants and
// authorization codes. Every reader checks expiry itself, so the sweep only
// frees memory and may lag behind it.
const sweepEvery = time.Second

// maxPendingGrants is the most device grants the issuer holds at once. A
// grant stays in memory until it is redeemed or expires, so past this bound
// a new start is refused and no grant is evicted: a flood of starts can
// delay new sign-ins but never cancels one a person has begun.
const maxPendingGrants = 4096

// The answers of the device authorization endpoint when it starts no grant.
const (
	deviceClientRefused = "client_id must be straza, strazactl or console, the Straza programs this server signs in. Start the sign-in again with straza enroll."
	deviceGrantsFull    = "This server already holds 4096 sign-ins waiting for a code, the most it keeps. Wait a few minutes and sign in again, or sign in through the browser. If it repeats, look in the access log for a flood of POST /oidc/device_authorization."
)

type deviceGrant struct {
	deviceCode string
	userCode   string
	clientID   string
	status     string // pending|approved|denied
	userID     string
	authTime   time.Time // when the password was checked
	expiresAt  time.Time
	lastPoll   time.Time
}

// NewIssuer builds the built-in issuer. baseURL must equal the token
// service's issuer string (one identity for discovery and signing).
func NewIssuer(users store.UserRepo, tokens *TokenService, baseURL string) *Issuer {
	return &Issuer{
		users:      users,
		tokens:     tokens,
		baseURL:    baseURL,
		grantTTL:   10 * time.Minute,
		interval:   2 * time.Second,
		idTTL:      10 * time.Minute,
		grants:     map[string]*deviceGrant{},
		byUserCode: map[string]string{},
		codes:      map[string]*authCode{},
		seenJTI:    map[string]time.Time{},
	}
}

// Routes registers the issuer endpoints on mux.
func (i *Issuer) Routes(mux *http.ServeMux) {
	mux.HandleFunc("GET /.well-known/openid-configuration", i.handleDiscovery)
	mux.HandleFunc("POST /oidc/device_authorization", i.handleDeviceAuthorization)
	mux.HandleFunc("GET /oidc/device", i.handleLoginPage)
	mux.HandleFunc("POST /oidc/device", i.handleLoginSubmit)
	mux.HandleFunc("GET /oidc/authorize", i.handleAuthorizePage)
	mux.HandleFunc("POST /oidc/authorize", i.handleAuthorizeSubmit)
	mux.HandleFunc("POST /oidc/token", i.handleToken)
}

func (i *Issuer) handleDiscovery(w http.ResponseWriter, _ *http.Request) {
	writeJSONIssuer(w, http.StatusOK, map[string]any{
		"issuer":                                i.baseURL,
		"authorization_endpoint":                i.baseURL + "/oidc/authorize",
		"device_authorization_endpoint":         i.baseURL + "/oidc/device_authorization",
		"token_endpoint":                        i.baseURL + "/oidc/token",
		"jwks_uri":                              i.baseURL + "/.well-known/straza/jwks.json",
		"grant_types_supported":                 []string{"authorization_code", "urn:ietf:params:oauth:grant-type:device_code", "client_credentials"},
		"token_endpoint_auth_methods_supported": []string{"private_key_jwt", "none"},
		"response_types_supported":              []string{"code"},
		"code_challenge_methods_supported":      []string{"S256"},
		"subject_types_supported":               []string{"public"},
		"id_token_signing_alg_values_supported": []string{"EdDSA"},
		"scopes_supported":                      []string{"openid", "profile", "email"},
	})
}

func (i *Issuer) handleDeviceAuthorization(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		oauthError(w, "invalid_request", "malformed form body")
		return
	}
	clientID := r.PostForm.Get("client_id")
	if clientID == "" {
		oauthError(w, "invalid_request", "client_id is required")
		return
	}
	if len(r.PostForm["client_id"]) != 1 || !authorizeClients[clientID] {
		oauthError(w, "invalid_client", deviceClientRefused)
		return
	}

	// The form parser hands out values that share memory with the whole
	// request body, so the grant keeps a copy of the client id of its own.
	g := &deviceGrant{
		deviceCode: randomToken(32),
		userCode:   randomUserCode(),
		clientID:   strings.Clone(clientID),
		status:     "pending",
		expiresAt:  time.Now().Add(i.grantTTL),
	}
	i.mu.Lock()
	i.gcLocked()
	// Only this endpoint adds to grants, and every grant in it, approved
	// ones not yet redeemed included, holds memory. A grant that expired
	// since the last sweep still counts, which can only refuse a start
	// early, for under a second.
	if len(i.grants) >= maxPendingGrants {
		i.mu.Unlock()
		w.Header().Set("Retry-After", "60")
		writeJSONIssuer(w, http.StatusServiceUnavailable, map[string]string{
			"error":             "temporarily_unavailable",
			"error_description": deviceGrantsFull,
		})
		return
	}
	i.grants[g.deviceCode] = g
	i.byUserCode[g.userCode] = g.deviceCode
	i.mu.Unlock()

	writeJSONIssuer(w, http.StatusOK, map[string]any{
		"device_code":               g.deviceCode,
		"user_code":                 g.userCode,
		"verification_uri":          i.baseURL + "/oidc/device",
		"verification_uri_complete": i.baseURL + "/oidc/device?user_code=" + g.userCode,
		"expires_in":                int(i.grantTTL.Seconds()),
		"interval":                  int(i.interval.Seconds()),
	})
}

// pageHead opens both sign-in pages, the device page and the authorize
// page: the title, the style and the heading, which reads .Only.
const pageHead = `<!doctype html>
<title>Sign in to Straza</title>
<style>body{font-family:system-ui;max-width:22rem;margin:4rem auto}label{display:block;margin:.6rem 0 .2rem}input{width:100%;padding:.4rem}button{margin-top:1rem;padding:.5rem 1.2rem}.err{color:#b00}.ok{color:#070}</style>
<h1>{{if .Only}}Emergency sign-in to Straza{{else}}Sign in to Straza{{end}}</h1>
`

var loginTmpl = template.Must(template.New("login").Parse(pageHead + `{{if .Done}}<p class="ok">Signed in. You can close this tab. The terminal that showed the code carries on by itself.</p>{{else}}
{{if .Only}}<p>This page signs in the emergency admin {{.Only}} and nobody else. Everyone else signs in at your identity provider.</p>{{end}}
{{if .Error}}<p class="err">{{.Error}}</p>{{end}}
<form method="post" action="/oidc/device">
<p>Enter the code shown in the console or the terminal.</p>
<label>Code</label><input name="user_code" value="{{.UserCode}}" autofocus>
<label>Username</label><input name="username" autocomplete="username">
<label>Password</label><input name="password" type="password" autocomplete="current-password">
<button type="submit">Sign in</button>
</form>{{end}}
`))

type loginView struct {
	UserCode string
	Error    string
	Done     bool
	Only     string // the one account the page signs in, "" for every local user
}

func (i *Issuer) handleLoginPage(w http.ResponseWriter, r *http.Request) {
	renderLogin(w, loginView{UserCode: r.URL.Query().Get("user_code"), Only: i.LoginOnly})
}

func (i *Issuer) handleLoginSubmit(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		renderLogin(w, loginView{Error: "Malformed form."})
		return
	}
	userCode := r.PostForm.Get("user_code")
	username := r.PostForm.Get("username")
	password := r.PostForm.Get("password")
	view := loginView{UserCode: userCode, Only: i.LoginOnly}

	const codeRefused = "That code is not valid or has expired. Check it against the console or terminal and try again."

	i.mu.Lock()
	g := i.grants[i.byUserCode[userCode]]
	pending := g.pendingLocked(time.Now())
	i.mu.Unlock()
	if !pending {
		view.Error = codeRefused
		renderLogin(w, view)
		return
	}
	u, refusal := i.judgePassword(r, username, password)
	if refusal != "" {
		view.Error = refusal
		renderLogin(w, view)
		return
	}
	authTime := time.Now()
	// The person authenticated, whatever happens to the grant next, so the
	// break-glass alarm never depends on the grant's timing.
	if i.OnLogin != nil {
		i.OnLogin(u.ID, u.Username)
	}

	// The grant may have changed during the password check: an overlapping
	// submit approved it, or it expired. Only a grant still pending now is
	// approved, so one sign-in decides it.
	i.mu.Lock()
	pending = g.pendingLocked(authTime)
	if pending {
		g.status = "approved"
		g.userID = u.ID
		g.authTime = authTime
	}
	i.mu.Unlock()
	if !pending {
		view.Error = codeRefused
		renderLogin(w, view)
		return
	}
	renderLogin(w, loginView{Done: true})
}

// pendingLocked reports whether g still waits for a sign-in at now. A nil
// grant does not. Called with i.mu held.
func (g *deviceGrant) pendingLocked(now time.Time) bool {
	return g != nil && g.status == "pending" && !now.After(g.expiresAt)
}

// The reasons OnLoginFailed reports. ReasonNonPersonPassword is given only
// after the password matched, so a wrong password reads the same for every
// account.
const (
	ReasonBadPassword       = "invalid username or password"
	ReasonNonPersonPassword = "an AI agent or a service account cannot sign in with a password" // #nosec G101 -- a refusal reason, not a credential
)

// nonPersonPasswordRefused is the sign-in pages' answer to the right
// password of an AI agent or a service account, which signs in with its key
// only.
const nonPersonPasswordRefused = "This account is an AI agent or a service account, so it cannot sign in here: only a person signs in with a password. " +
	"An agent signs in with its key, through straza enroll --headless on its machine."

// judgePassword checks a password submit, the same way on the device page
// and the authorize page. It returns the active user who is a person, or
// the sentence the page shows instead. Each refusal fires OnLoginFailed
// once with the post-zeroing username and its reason.
func (i *Issuer) judgePassword(r *http.Request, username, password string) (store.User, string) {
	if i.LoginOnly != "" && username != i.LoginOnly {
		// Refused before any lookup: on this mount no other row's hash is
		// a credential, whatever set-password stored on it.
		if i.OnLoginFailed != nil {
			i.OnLoginFailed("", ReasonBadPassword, r)
		}
		return store.User{}, "This page signs in the account " + i.LoginOnly + " only. Everyone else signs in at your identity provider."
	}

	u, err := i.users.GetByUsername(r.Context(), username)
	// A user without a password hash (SCIM-origin) can never use the local
	// login page. bcrypt on a dummy hash keeps timing roughly uniform.
	hash := u.PasswordHash
	if err != nil || hash == "" || u.Status != store.UserActive {
		hash = "$2a$10$7EqJtq98hPqEX7fNZaFWoOhi5B0iYq7Zw1zW0f0eS9dJ7yQO5r/9y"
		username = ""
	}
	if bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)) != nil || username == "" {
		if i.OnLoginFailed != nil {
			i.OnLoginFailed(username, ReasonBadPassword, r)
		}
		return store.User{}, "Invalid username or password."
	}
	// A password an older version let an admin set on an AI agent or a
	// service account opens nothing here, because such a user signs in with
	// its key. The password is judged first, so only its holder learns that
	// the account is not a person.
	if u.IsNHI() {
		if i.OnLoginFailed != nil {
			i.OnLoginFailed(username, ReasonNonPersonPassword, r)
		}
		return store.User{}, nonPersonPasswordRefused
	}
	return u, ""
}

func (i *Issuer) handleToken(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		oauthError(w, "invalid_request", "malformed form body")
		return
	}
	switch gt := r.PostForm.Get("grant_type"); gt {
	case "urn:ietf:params:oauth:grant-type:device_code":
		// interactive lane, below
	case "authorization_code":
		i.handleCodeGrant(w, r) // browser lane (issuer_code.go)
		return
	case "client_credentials":
		i.handleClientCredentials(w, r) // headless NHI lane (issuer_nhi.go)
		return
	default:
		oauthError(w, "unsupported_grant_type", "the supported grants are authorization_code and device_code for people, and client_credentials for NHIs")
		return
	}
	deviceCode := r.PostForm.Get("device_code")
	clientID := r.PostForm.Get("client_id")

	i.mu.Lock()
	g := i.grants[deviceCode]
	if g == nil || g.clientID != clientID {
		i.mu.Unlock()
		oauthError(w, "invalid_grant", "unknown device_code")
		return
	}
	nowT := time.Now()
	if nowT.After(g.expiresAt) {
		delete(i.grants, g.deviceCode)
		delete(i.byUserCode, g.userCode)
		i.mu.Unlock()
		oauthError(w, "expired_token", "device_code expired; restart login")
		return
	}
	if !g.lastPoll.IsZero() && nowT.Sub(g.lastPoll) < i.interval {
		g.lastPoll = nowT
		i.mu.Unlock()
		oauthError(w, "slow_down", "poll slower")
		return
	}
	g.lastPoll = nowT
	status, userID, authTime := g.status, g.userID, g.authTime
	if status == "approved" {
		delete(i.grants, g.deviceCode)
		delete(i.byUserCode, g.userCode)
	}
	i.mu.Unlock()

	switch status {
	case "pending":
		oauthError(w, "authorization_pending", "user has not approved yet")
	case "denied":
		oauthError(w, "access_denied", "user denied the request")
	case "approved":
		u, err := i.users.GetByID(r.Context(), userID)
		if err != nil || u.Status != store.UserActive {
			oauthError(w, "access_denied", "user no longer active")
			return
		}
		i.issueIDToken(w, u, clientID, LoginFacts{AuthTime: authTime})
	}
}

// issueIDToken answers a token request with an ID token for u, minted for
// clientID with the login facts f.
func (i *Issuer) issueIDToken(w http.ResponseWriter, u store.User, clientID string, f LoginFacts) {
	idTok, err := i.tokens.MintIDTokenFor(u.ID, clientID, i.idTTL, u.Username, u.Email, f)
	if err != nil {
		oauthError(w, "server_error", "could not sign id token")
		return
	}
	writeJSONIssuer(w, http.StatusOK, map[string]any{
		"access_token": idTok, // the ID token doubles as access token for Straza APIs
		"id_token":     idTok,
		"token_type":   "Bearer",
		"expires_in":   int(i.idTTL.Seconds()),
	})
}

// gcLocked drops expired grants and authorization codes, at most once per
// sweepEvery, so a flood of starts does not walk both tables under i.mu on
// each one. Called with i.mu held.
func (i *Issuer) gcLocked() {
	nowT := time.Now()
	if nowT.Sub(i.lastSweep) < sweepEvery {
		return
	}
	i.lastSweep = nowT
	for code, g := range i.grants {
		if nowT.After(g.expiresAt) {
			delete(i.grants, code)
			delete(i.byUserCode, g.userCode)
		}
	}
	for code, c := range i.codes {
		if nowT.After(c.expiresAt) {
			delete(i.codes, code)
		}
	}
}

// HashPassword hashes a local-user password for storage.
func HashPassword(plain string) (string, error) {
	b, err := bcrypt.GenerateFromPassword([]byte(plain), bcrypt.DefaultCost)
	if err != nil {
		return "", fmt.Errorf("authn: hash password: %w", err)
	}
	return string(b), nil
}

// RandomPassword returns a generated credential for bootstrap flows.
func RandomPassword() string {
	return randomToken(18)
}

// Checksum returns the canonical content checksum used for knowledge packs.
func Checksum(content string) string {
	sum := sha256.Sum256([]byte(content))
	return "sha256:" + hex.EncodeToString(sum[:])
}

func renderLogin(w http.ResponseWriter, v loginView) {
	pageHeaders(w)
	_ = loginTmpl.Execute(w, v)
}

// pageHeaders marks a sign-in page as HTML that no other site may frame. The
// server sets the same frame headers on every response, and the issuer sets
// them too, so a password form never depends on how it is mounted.
func pageHeaders(w http.ResponseWriter) {
	h := w.Header()
	h.Set("Content-Type", "text/html; charset=utf-8")
	h.Set("X-Frame-Options", "DENY")
	h.Set("Content-Security-Policy", "frame-ancestors 'none'")
}

// oauthError writes an RFC 6749 error response.
func oauthError(w http.ResponseWriter, code, description string) {
	writeJSONIssuer(w, http.StatusBadRequest, map[string]string{
		"error":             code,
		"error_description": description,
	})
}

func writeJSONIssuer(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

// randomToken returns n bytes of hex-encoded entropy.
func randomToken(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(fmt.Sprintf("authn: entropy unavailable: %v", err))
	}
	return hex.EncodeToString(b)
}

// randomUserCode returns an XXXX-XXXX code from an unambiguous alphabet.
func randomUserCode() string {
	const alphabet = "BCDFGHJKLMNPQRSTVWXZ23456789"
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		panic(fmt.Sprintf("authn: entropy unavailable: %v", err))
	}
	out := make([]byte, 9)
	for i := 0; i < 8; i++ {
		pos := i
		if i >= 4 {
			pos = i + 1
		}
		out[pos] = alphabet[int(b[i])%len(alphabet)]
	}
	out[4] = '-'
	return string(out)
}
