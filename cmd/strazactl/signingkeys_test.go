package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// signingKeysServer is the admin surface `signing-keys` walks: the list, the
// two rotate routes, and the retire, promote and certificate routes of a
// client assertion key. It counts the calls and can refuse them the way a
// grant check does.
type signingKeysServer struct {
	mu     sync.Mutex
	calls  int
	refuse bool
	// rotateBody, retireBody, promoteBody and certBody are the client
	// assertion answers under test.
	rotateBody  string
	retireBody  string
	promoteBody string
	certBody    string
}

func (s *signingKeysServer) start(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	write := func(body func() string) http.HandlerFunc {
		return func(w http.ResponseWriter, _ *http.Request) {
			s.mu.Lock()
			s.calls++
			refuse := s.refuse
			s.mu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			if refuse {
				w.WriteHeader(http.StatusForbidden)
				_, _ = w.Write([]byte(`{"error":"token lacks scope sessions:write"}`))
				return
			}
			_, _ = w.Write([]byte(body()))
		}
	}
	mux.HandleFunc("POST /v1/admin/signing-keys/rotate", write(func() string {
		return `{"kid":"k-new","status":"staged","active_in_seconds":60,"previous_key_verifies_for_seconds":2592090}`
	}))
	mux.HandleFunc("POST /v1/admin/signing-keys/client-assertion/rotate", write(func() string { return s.rotateBody }))
	mux.HandleFunc("POST /v1/admin/signing-keys/client-assertion/{kid}/retire", write(func() string { return s.retireBody }))
	mux.HandleFunc("POST /v1/admin/signing-keys/client-assertion/{kid}/promote", write(func() string { return s.promoteBody }))
	mux.HandleFunc("GET /v1/admin/signing-keys/client-assertion/{kid}/certificate", write(func() string { return s.certBody }))
	mux.HandleFunc("GET /v1/admin/signing-keys", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[
			{"kid":"k-ca","purpose":"client_assertion","status":"active","created_at":"2026-09-21T10:00:00Z","rotated_at":"2026-09-21T10:01:00Z","certificate_expires_at":"2028-09-20T10:00:00Z"},
			{"kid":"k-ses","purpose":"session","status":"active","created_at":"2026-09-01T08:00:00Z"}]`))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

// TestSigningKeysRotate pins `strazactl signing-keys rotate <purpose>`: one
// call to the purpose's route and plain sentences that name the staged kid
// and what happens next, computed from the response. A session key promotes
// itself. A client assertion key waits for its promotion, so the sentences
// name the certificate for an Entra blueprint and the promote command, and a
// first key also says where its key document is and what to do with it.
func TestSigningKeysRotate(t *testing.T) {
	tests := []struct {
		name, purpose, body, want string
	}{
		{"the session key", "session", "",
			"Staged a new session signing key k-new.\n" +
				"It becomes the signing key in about 1 minute, and the previous key stops verifying about 30 days after that.\n"},
		{"a client assertion key while none signs", "client_assertion",
			`{"kid":"k-ca","status":"staged","active_in_seconds":60,"previous_key_verifies_for_seconds":120,"jwks_uri":"https://straza.example/.well-known/straza/client-assertion-jwks.json"}`,
			"Created a client assertion key k-ca. No other key signs client assertions.\n" +
				"It is in the key document now and signs nothing until you promote it, which you can do in about 1 minute.\n" +
				"The key document is https://straza.example/.well-known/straza/client-assertion-jwks.json. Register it as the JWKS URL of each agent's client at your identity provider.\n" +
				"If an Entra agent blueprint uses this key, upload its certificate there first, from `strazactl signing-keys certificate client_assertion k-ca`.\n" +
				"Then run `strazactl signing-keys promote client_assertion k-ca`.\n"},
		{"the next client assertion key", "client_assertion",
			`{"kid":"k-ca2","status":"staged","previous_kid":"k-ca","active_in_seconds":60,"previous_key_verifies_for_seconds":120,"jwks_uri":"https://straza.example/.well-known/straza/client-assertion-jwks.json"}`,
			"Staged a new client assertion key k-ca2. The key k-ca keeps signing until you promote the new one.\n" +
				"It is in the key document now, and you can promote it in about 1 minute.\n" +
				"If an Entra agent blueprint uses these keys, upload the new key's certificate there first, from `strazactl signing-keys certificate client_assertion k-ca2`.\n" +
				"Then run `strazactl signing-keys promote client_assertion k-ca2`.\n"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := &signingKeysServer{rotateBody: tc.body}
			srv := s.start(t)
			t.Setenv("STRAZA_SERVER", "")
			credsPath := onToken(t, srv.URL)

			stdout, _, err := runCLI(t, credsPath, "signing-keys", "rotate", tc.purpose)
			if err != nil {
				t.Fatalf("signing-keys rotate %s: %v", tc.purpose, err)
			}
			if stdout != tc.want {
				t.Errorf("stdout:\n%s\nwant:\n%s", stdout, tc.want)
			}
			s.mu.Lock()
			calls := s.calls
			s.refuse = true
			s.mu.Unlock()
			if calls != 1 {
				t.Errorf("server saw %d rotate calls, want 1", calls)
			}
			// A refused rotation surfaces the server's reason and exits non-zero.
			if _, _, err := runCLI(t, credsPath, "signing-keys", "rotate", tc.purpose); err == nil ||
				!strings.Contains(err.Error(), "token lacks scope sessions:write") {
				t.Errorf("refused rotate = %v, want the server's 403 reason", err)
			}
		})
	}
}

// TestSigningKeysRetire pins `strazactl signing-keys retire <kid>`: what the
// operator reads depends on the state the key was in, only the retirement of
// the signing key tells them that nothing signs until they rotate and
// promote, and every retirement says that Entra accepts a key until its
// certificate leaves the blueprint.
func TestSigningKeysRetire(t *testing.T) {
	const cache = "Your identity provider may keep the retired key in its cache. If the key was copied, clear that cache or remove the JWKS URL from the agents' clients there.\n" +
		"If an Entra agent blueprint holds this key's certificate, remove it there now. Entra accepts the key until you do.\n"
	tests := []struct {
		name, body, want string
	}{
		{"the signing key",
			`{"kid":"k-ca","status":"retired","was":"active","leaves_document_in_seconds":30,"next_key_active_in_seconds":60}`,
			"Retired the client assertion key k-ca. It leaves the key document on every replica within 30 seconds.\n" +
				"No key signs client assertions now. Run `strazactl signing-keys rotate client_assertion`, then about 1 minute later `strazactl signing-keys promote client_assertion` with the new key's id.\n" + cache},
		{"a key that no longer signs",
			`{"kid":"k-ca","status":"retired","was":"retiring","leaves_document_in_seconds":30,"next_key_active_in_seconds":60}`,
			"Retired the client assertion key k-ca. It leaves the key document on every replica within 30 seconds.\n" + cache},
		{"a key that is retired already",
			`{"kid":"k-ca","status":"retired","was":"retired","leaves_document_in_seconds":30,"next_key_active_in_seconds":60}`,
			"The client assertion key k-ca was already retired. Nothing changed.\n"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := &signingKeysServer{retireBody: tc.body}
			srv := s.start(t)
			t.Setenv("STRAZA_SERVER", "")
			stdout, _, err := runCLI(t, onToken(t, srv.URL), "signing-keys", "retire", "k-ca")
			if err != nil {
				t.Fatalf("signing-keys retire: %v", err)
			}
			if stdout != tc.want {
				t.Errorf("stdout:\n%s\nwant:\n%s", stdout, tc.want)
			}
		})
	}
}

// TestSigningKeysList pins the table: every key with its purpose and status,
// an empty CHANGED cell for a key whose status never changed, and the end of
// the certificate's validity for a client assertion key.
func TestSigningKeysList(t *testing.T) {
	s := &signingKeysServer{}
	srv := s.start(t)
	t.Setenv("STRAZA_SERVER", "")
	stdout, _, err := runCLI(t, onToken(t, srv.URL), "signing-keys", "list")
	if err != nil {
		t.Fatalf("signing-keys list: %v", err)
	}
	want := "KID    PURPOSE           STATUS  CREATED               CHANGED               CERTIFICATE EXPIRES\n" +
		"k-ca   client_assertion  active  2026-09-21T10:00:00Z  2026-09-21T10:01:00Z  2028-09-20T10:00:00Z\n" +
		"k-ses  session           active  2026-09-01T08:00:00Z  -                     -\n"
	if stdout != want {
		t.Errorf("stdout:\n%s\nwant:\n%s", stdout, want)
	}
}

// TestSigningKeysOperands: the purpose and the kid are required operands with
// no default, and a wrong invocation says what to type and never reaches the
// server.
func TestSigningKeysOperands(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want string
	}{
		{"rotate without a purpose", []string{"rotate"},
			"signing-keys rotate needs the purpose of the key to rotate: session or client_assertion. Example: strazactl signing-keys rotate session"},
		{"rotate with an unknown purpose", []string{"rotate", "snapshot"},
			`"snapshot" is not a purpose this command rotates. Use session or client_assertion. Example: strazactl signing-keys rotate session`},
		{"rotate with a stray operand", []string{"rotate", "session", "extra"},
			"signing-keys rotate takes one operand, the purpose: session or client_assertion. Example: strazactl signing-keys rotate session"},
		{"retire without a kid", []string{"retire"},
			"signing-keys retire needs the id of one client assertion key. Run `strazactl signing-keys list` to see the ids"},
		{"promote without a kid", []string{"promote", "client_assertion"},
			"signing-keys promote needs the purpose and the id of one key. Example: strazactl signing-keys promote client_assertion <kid>. Run `strazactl signing-keys list` to see the ids"},
		{"promote a session key", []string{"promote", "session", "k-ses"},
			`"session" is not a purpose this command promotes. A session key promotes itself after strazactl signing-keys rotate session, and only a client_assertion key is promoted by hand. Example: strazactl signing-keys promote client_assertion <kid>`},
		{"certificate without a kid", []string{"certificate", "client_assertion"},
			"signing-keys certificate needs the purpose and the id of one key. Example: strazactl signing-keys certificate client_assertion <kid>. Run `strazactl signing-keys list` to see the ids"},
		{"certificate of a session key", []string{"certificate", "session", "k-ses"},
			`"session" is not a purpose this command serves a certificate for. Only a client_assertion key has one. Example: strazactl signing-keys certificate client_assertion <kid>`},
		{"list with a stray operand", []string{"list", "extra"}, "unknown command"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := &signingKeysServer{}
			srv := s.start(t)
			t.Setenv("STRAZA_SERVER", "")
			_, _, err := runCLI(t, onToken(t, srv.URL), append([]string{"signing-keys"}, tc.args...)...)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("err = %v, want %q", err, tc.want)
			}
			s.mu.Lock()
			defer s.mu.Unlock()
			if s.calls != 0 {
				t.Errorf("a rejected invocation reached the server %d times", s.calls)
			}
		})
	}
}

// TestSigningKeysPromote pins `strazactl signing-keys promote
// client_assertion <kid>`: one call, and sentences that say when every
// replica signs with the key and when the previous key goes, or that nothing
// changed for a key that signs already.
func TestSigningKeysPromote(t *testing.T) {
	tests := []struct {
		name, body, want string
	}{
		{"the first key",
			`{"kid":"k-ca","status":"active","was":"staged","every_replica_signs_in_seconds":30}`,
			"Promoted the client assertion key k-ca. This replica signs with it now, and every replica does within 30 seconds.\n"},
		{"a key over the one that signed",
			`{"kid":"k-ca2","status":"active","was":"staged","every_replica_signs_in_seconds":30,"previous_kid":"k-ca","previous_key_leaves_document_in_seconds":90}`,
			"Promoted the client assertion key k-ca2. This replica signs with it now, and every replica does within 30 seconds.\n" +
				"The previous key k-ca leaves the key document in about 2 minutes. After that you can remove its certificate wherever you uploaded it.\n"},
		{"a key that signs already",
			`{"kid":"k-ca","status":"active","was":"active","every_replica_signs_in_seconds":30}`,
			"The client assertion key k-ca signs already. Nothing changed.\n"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := &signingKeysServer{promoteBody: tc.body}
			srv := s.start(t)
			t.Setenv("STRAZA_SERVER", "")
			credsPath := onToken(t, srv.URL)
			stdout, _, err := runCLI(t, credsPath, "signing-keys", "promote", "client_assertion", "k-ca")
			if err != nil {
				t.Fatalf("signing-keys promote: %v", err)
			}
			if stdout != tc.want {
				t.Errorf("stdout:\n%s\nwant:\n%s", stdout, tc.want)
			}
			s.mu.Lock()
			s.refuse = true
			s.mu.Unlock()
			if _, _, err := runCLI(t, credsPath, "signing-keys", "promote", "client_assertion", "k-ca"); err == nil ||
				!strings.Contains(err.Error(), "token lacks scope sessions:write") {
				t.Errorf("refused promote = %v, want the server's 403 reason", err)
			}
		})
	}
}

// TestSigningKeysCertificate pins `strazactl signing-keys certificate
// client_assertion <kid>`: the PEM alone on stdout, so a redirect saves a
// file Entra takes, and one sentence on stderr with the end of validity and
// where the certificate goes.
func TestSigningKeysCertificate(t *testing.T) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "Straza client assertion k-ca"},
		NotBefore: time.Date(2026, 9, 21, 10, 0, 0, 0, time.UTC), NotAfter: time.Date(2028, 9, 20, 10, 0, 0, 0, time.UTC),
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	certPEM := string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
	s := &signingKeysServer{certBody: certPEM}
	srv := s.start(t)
	t.Setenv("STRAZA_SERVER", "")
	stdout, stderr, err := runCLI(t, onToken(t, srv.URL), "signing-keys", "certificate", "client_assertion", "k-ca")
	if err != nil {
		t.Fatalf("signing-keys certificate: %v", err)
	}
	if stdout != certPEM {
		t.Errorf("stdout:\n%s\nwant the PEM alone:\n%s", stdout, certPEM)
	}
	if want := "The certificate of client assertion key k-ca is valid until 2028-09-20. Upload it to the Entra agent blueprint under Credentials, Certificates.\n"; stderr != want {
		t.Errorf("stderr = %q, want %q", stderr, want)
	}
	s.mu.Lock()
	s.certBody = "not a certificate"
	s.mu.Unlock()
	if _, _, err := runCLI(t, onToken(t, srv.URL), "signing-keys", "certificate", "client_assertion", "k-ca"); err == nil ||
		!strings.Contains(err.Error(), "is not a PEM certificate") {
		t.Errorf("certificate of a garbled answer = %v, want a refusal that says so", err)
	}
}

// TestRoughDuration pins the unit and rounding the rotate sentences use.
func TestRoughDuration(t *testing.T) {
	for _, tc := range []struct {
		seconds int
		want    string
	}{
		{45, "45 seconds"},
		{60, "1 minute"},
		{330, "6 minutes"},
		{3600, "1 hour"},
		{7500, "2 hours"},
		{86400, "1 day"},
		{2592030, "30 days"},
	} {
		if got := roughDuration(tc.seconds); got != tc.want {
			t.Errorf("roughDuration(%d) = %q, want %q", tc.seconds, got, tc.want)
		}
	}
}
