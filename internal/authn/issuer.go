package authn

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"html/template"
	"net/http"
	"sync"
	"time"

	"golang.org/x/crypto/bcrypt"

	"github.com/strazahq/straza/internal/store"
)

// Issuer is the built-in OIDC provider: discovery, RFC 8628 device
// authorization, token endpoint, and a minimal local-user login page. The
// standalone profile signs every human in here. The enterprise profile
// mounts the same issuer for two callers only: NHIs on the
// client_credentials grant, and the break-glass admin on the login page
// (LoginOnly), so a lockout by the identity provider stays recoverable. It
// is deliberately NOT a general-purpose IdP, just enough for
// `straza enroll` / `strazactl login`.
//
// Grants live in memory: standalone runs a single process, and a lost grant
// only means re-running the login (fail closed).
type Issuer struct {
	users   store.UserRepo
	tokens  *TokenService
	baseURL string

	// NHIKeys enables the client_credentials grant for headless NHI
	// principals (issuer_nhi.go). nil ⇒ the grant is refused.
	NHIKeys NHIKeyLookup

	// LoginOnly, when set, restricts the login page to that one username
	// and presents the page as the emergency sign-in. The enterprise
	// profile sets it to the break-glass admin: humans there have no local
	// passwords, so a hash on any other row must never open this
	// page. Every other username is refused before its credential is
	// judged, and the refusal fires OnLoginFailed with an empty username.
	LoginOnly string

	// OnLogin, when set, observes every successful interactive login
	// (userID, username). The server uses it to alarm break-glass
	// authentications on the audit chain.
	OnLogin func(userID, username string)

	// OnLoginFailed, when set, observes every refused password submit
	// (spec/events rev 21 authn producer). username is the post-zeroing
	// value: "" for an unknown, passwordless, or inactive account, so the
	// observer can never fabricate an identity from attacker input. r is the
	// submit request (the sourceIp/userAgent source). Unknown or expired
	// device codes do NOT fire it: no credential was judged.
	OnLoginFailed func(username string, r *http.Request)

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
	seenJTI    map[string]time.Time    // assertion jti → exp (replay cache)
}

type deviceGrant struct {
	deviceCode string
	userCode   string
	clientID   string
	status     string // pending|approved|denied
	userID     string
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
		seenJTI:    map[string]time.Time{},
	}
}

// Routes registers the issuer endpoints on mux.
func (i *Issuer) Routes(mux *http.ServeMux) {
	mux.HandleFunc("GET /.well-known/openid-configuration", i.handleDiscovery)
	mux.HandleFunc("POST /oidc/device_authorization", i.handleDeviceAuthorization)
	mux.HandleFunc("GET /oidc/device", i.handleLoginPage)
	mux.HandleFunc("POST /oidc/device", i.handleLoginSubmit)
	mux.HandleFunc("POST /oidc/token", i.handleToken)
}

func (i *Issuer) handleDiscovery(w http.ResponseWriter, _ *http.Request) {
	writeJSONIssuer(w, http.StatusOK, map[string]any{
		"issuer":                                i.baseURL,
		"device_authorization_endpoint":         i.baseURL + "/oidc/device_authorization",
		"token_endpoint":                        i.baseURL + "/oidc/token",
		"jwks_uri":                              i.baseURL + "/.well-known/straza/jwks.json",
		"grant_types_supported":                 []string{"urn:ietf:params:oauth:grant-type:device_code", "client_credentials"},
		"token_endpoint_auth_methods_supported": []string{"private_key_jwt"},
		"response_types_supported":              []string{"id_token"},
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

	g := &deviceGrant{
		deviceCode: randomToken(32),
		userCode:   randomUserCode(),
		clientID:   clientID,
		status:     "pending",
		expiresAt:  time.Now().Add(i.grantTTL),
	}
	i.mu.Lock()
	i.gcLocked()
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

var loginTmpl = template.Must(template.New("login").Parse(`<!doctype html>
<title>Sign in to Straza</title>
<style>body{font-family:system-ui;max-width:22rem;margin:4rem auto}label{display:block;margin:.6rem 0 .2rem}input{width:100%;padding:.4rem}button{margin-top:1rem;padding:.5rem 1.2rem}.err{color:#b00}.ok{color:#070}</style>
<h1>{{if .Only}}Emergency sign-in to Straza{{else}}Sign in to Straza{{end}}</h1>
{{if .Done}}<p class="ok">Signed in. You can close this tab. The console or terminal that showed the code carries on by itself.</p>{{else}}
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

	i.mu.Lock()
	deviceCode, ok := i.byUserCode[userCode]
	var g *deviceGrant
	if ok {
		g = i.grants[deviceCode]
	}
	i.mu.Unlock()
	if g == nil || g.status != "pending" || time.Now().After(g.expiresAt) {
		view.Error = "That code is not valid or has expired. Check it against the console or terminal and try again."
		renderLogin(w, view)
		return
	}
	if i.LoginOnly != "" && username != i.LoginOnly {
		// Refused before any lookup: on this mount no other row's hash is
		// a credential, whatever set-password stored on it.
		if i.OnLoginFailed != nil {
			i.OnLoginFailed("", r)
		}
		view.Error = "This page signs in the account " + i.LoginOnly + " only. Everyone else signs in at your identity provider."
		renderLogin(w, view)
		return
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
			i.OnLoginFailed(username, r)
		}
		view.Error = "Invalid username or password."
		renderLogin(w, view)
		return
	}

	i.mu.Lock()
	g.status = "approved"
	g.userID = u.ID
	i.mu.Unlock()
	if i.OnLogin != nil {
		i.OnLogin(u.ID, u.Username)
	}
	renderLogin(w, loginView{Done: true})
}

func (i *Issuer) handleToken(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		oauthError(w, "invalid_request", "malformed form body")
		return
	}
	switch gt := r.PostForm.Get("grant_type"); gt {
	case "urn:ietf:params:oauth:grant-type:device_code":
		// interactive lane, below
	case "client_credentials":
		i.handleClientCredentials(w, r) // headless NHI lane (issuer_nhi.go)
		return
	default:
		oauthError(w, "unsupported_grant_type", "supported grants: device_code (interactive) and client_credentials (NHI headless)")
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
	status, userID := g.status, g.userID
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
		idTok, err := i.tokens.MintIDToken(u.ID, clientID, i.idTTL, u.Username, u.Email)
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
}

// gcLocked drops expired grants. Called with i.mu held.
func (i *Issuer) gcLocked() {
	nowT := time.Now()
	for code, g := range i.grants {
		if nowT.After(g.expiresAt) {
			delete(i.grants, code)
			delete(i.byUserCode, g.userCode)
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
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = loginTmpl.Execute(w, v)
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
