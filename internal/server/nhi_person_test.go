package server

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/strazahq/straza/internal/store"
)

// TestPersonUserReadsBothSignals pins the person predicate behind the
// people-only gates: a user is a person only when neither the typology
// (user_type agent or service) nor the create-time mark (attrs.kind nhi)
// says otherwise, so a non-human identity stored without a type stays out.
func TestPersonUserReadsBothSignals(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		u    store.User
		want bool
	}{
		{"user_type human", store.User{UserType: store.UserTypeHuman}, true},
		{"user_type agent", store.User{UserType: store.UserTypeAgent}, false},
		{"user_type service", store.User{UserType: store.UserTypeService}, false},
		{"no user_type, attrs kind nhi", store.User{Attrs: `{"kind":"nhi"}`}, false},
		{"no user_type, no attrs", store.User{}, true},
		{"user_type human beside attrs kind nhi", store.User{UserType: store.UserTypeHuman, Attrs: `{"kind":"nhi"}`}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := personUser(tc.u); got != tc.want {
				t.Fatalf("personUser(%+v) = %v, want %v", tc.u, got, tc.want)
			}
		})
	}
}

// untypedNHI stores a non-human identity the way the admin create did before
// it recorded a type: attrs.kind nhi and no user_type. It answers the row and
// an ID token for it.
func untypedNHI(t *testing.T, app *App, username string) (store.User, string) {
	t.Helper()
	u, err := app.store.Users().Create(context.Background(), store.User{Username: username, Attrs: `{"kind":"nhi"}`})
	if err != nil {
		t.Fatal(err)
	}
	idt, err := app.tokens.MintIDToken(u.ID, "straza", time.Minute, username, "")
	if err != nil {
		t.Fatal(err)
	}
	return u, idt
}

// TestCheckinUntypedNHIUnderHumanClient pins that a non-human identity with
// no user_type opens no session under a human client's harness name, and
// that each refusal writes exactly one authn login failure naming the user
// and the reason, as a typed agent's refusal does.
func TestCheckinUntypedNHIUnderHumanClient(t *testing.T) {
	t.Parallel()
	app, base := testApp(t)
	_ = seedIdentity(t, app)
	cases := []struct {
		harness  string
		wantCode int
	}{
		{"strazactl", http.StatusForbidden},
		{"console", http.StatusForbidden},
		{"claude-code", http.StatusOK},
	}
	for i, tc := range cases {
		t.Run(tc.harness, func(t *testing.T) {
			username := fmt.Sprintf("untyped-%d", i)
			u, idt := untypedNHI(t, app, username)
			version := fmt.Sprintf("untyped-%d", i)
			code, body := postJSON(t, base+"/v1/checkin", map[string]any{
				"id_token":    idt,
				"harness":     map[string]string{"name": tc.harness, "version": version},
				"attestation": map[string]any{"managed": false, "hashes": map[string]string{}},
			})
			if code != tc.wantCode {
				t.Fatalf("checkin = %d %v, want %d", code, body, tc.wantCode)
			}
			label := tc.harness + "/" + version
			failures := func(d map[string]any) bool {
				return d["action"] == "login" && d["outcome"] == "failure" && d["harness"] == label
			}
			if tc.wantCode == http.StatusOK {
				if n := countAuthn(t, app, failures); n != 0 {
					t.Fatalf("login failures for %s = %d, want 0", label, n)
				}
				return
			}
			want := fmt.Sprintf("the user %s is an agent, and only a person can open a %s session", username, tc.harness)
			if msg, _ := body["error"].(string); !strings.HasPrefix(msg, want) {
				t.Fatalf("refusal = %q, want a sentence starting %q", msg, want)
			}
			rec := waitAuthn(t, app, username+" refused", failures)
			if rec["reason"] != "only a person can open a human client session" || rec["user"] != username ||
				rec["userId"] != u.ID || rec["via"] != "id-token" {
				t.Fatalf("login failure = %v, want the person-only reason for %s", rec, username)
			}
			if n := countAuthn(t, app, failures); n != 1 {
				t.Fatalf("login failures for %s = %d, want exactly 1", label, n)
			}
		})
	}
}

// TestEnrollUntypedNHIAsHuman pins that a non-human identity with no
// user_type cannot enrol a credential for the human clients, writes exactly
// one authn login failure for it, and still enrols a kit credential.
func TestEnrollUntypedNHIAsHuman(t *testing.T) {
	t.Parallel()
	app, base := testApp(t)
	u, idt := untypedNHI(t, app, "build-bot")
	enroll := func(kind, fingerprint string) (int, map[string]any) {
		return postJSON(t, base+"/v1/enroll", map[string]any{
			"id_token":    idt,
			"client_kind": kind,
			"device":      map[string]string{"name": "box-" + fingerprint, "platform": "linux", "fingerprint": fingerprint},
		})
	}
	code, body := enroll(store.DeviceClientHuman, "fp-human")
	want := "the user build-bot is an agent, and only a person can enrol a credential for strazactl or the console"
	if code != http.StatusForbidden || body["error"] != want {
		t.Fatalf("human enrol = %d %v, want 403 with %q", code, body, want)
	}
	refused := func(d map[string]any) bool {
		return d["action"] == "login" && d["outcome"] == "failure" && d["user"] == "build-bot"
	}
	rec := waitAuthn(t, app, "build-bot's refused enrol", refused)
	if rec["reason"] != "only a person can enrol a credential for the human clients" || rec["userId"] != u.ID || rec["via"] != "id-token" {
		t.Fatalf("login failure = %v, want the person-only enrol reason for build-bot", rec)
	}
	if n := countAuthn(t, app, refused); n != 1 {
		t.Fatalf("login failures for build-bot = %d, want exactly 1", n)
	}
	if code, body := enroll(store.DeviceClientKit, "fp-kit"); code != http.StatusOK {
		t.Fatalf("kit enrol = %d %v, want 200", code, body)
	}
}

// TestUsersCreateNHIUserType pins the create side: an nhi without user_type
// is stored as an agent, service is kept, any other value is refused with
// the sentence naming the two legal ones, a person's create refuses the
// field, and a person's create without it stays unclassified.
func TestUsersCreateNHIUserType(t *testing.T) {
	t.Parallel()
	app, base := testApp(t)
	admin := seedIdentity(t, app)
	grantAdmin(t, app, admin.ID)
	tok, _ := checkinToken(t, app, base)
	const badNHI = "user_type must be agent or service when kind is nhi. Send agent for an AI agent that acts for a person, or service for a technical account with no agency"
	const badHuman = "user_type is accepted only when kind is nhi. Leave it out to create a person, or send kind nhi with user_type agent or service"
	cases := []struct {
		name     string
		body     map[string]any
		wantCode int
		wantType string
		wantErr  string
	}{
		{"nhi without user_type", map[string]any{"username": "bot-a", "kind": "nhi"}, http.StatusCreated, store.UserTypeAgent, ""},
		{"nhi as service", map[string]any{"username": "bot-s", "kind": "nhi", "user_type": "service"}, http.StatusCreated, store.UserTypeService, ""},
		{"nhi as agent, loose case", map[string]any{"username": "bot-g", "kind": "nhi", "user_type": " Agent "}, http.StatusCreated, store.UserTypeAgent, ""},
		{"nhi as human", map[string]any{"username": "bot-h", "kind": "nhi", "user_type": "human"}, http.StatusBadRequest, "", badNHI},
		{"nhi with an unknown type", map[string]any{"username": "bot-r", "kind": "nhi", "user_type": "robot"}, http.StatusBadRequest, "", badNHI},
		{"person with a type", map[string]any{"username": "pat", "user_type": "agent"}, http.StatusBadRequest, "", badHuman},
		{"person without a type", map[string]any{"username": "lee"}, http.StatusCreated, "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			code, body := postJSONAuth(t, base+"/v1/admin/users", tok, tc.body)
			if code != tc.wantCode {
				t.Fatalf("create = %d %v, want %d", code, body, tc.wantCode)
			}
			if tc.wantErr != "" {
				if body["error"] != tc.wantErr {
					t.Fatalf("refusal = %q, want %q", body["error"], tc.wantErr)
				}
				if _, err := app.store.Users().GetByUsername(context.Background(), tc.body["username"].(string)); err == nil {
					t.Fatal("a refused create stored a row")
				}
				return
			}
			if got, _ := body["user_type"].(string); got != tc.wantType {
				t.Fatalf("answer user_type = %q, want %q", got, tc.wantType)
			}
			stored, err := app.store.Users().GetByID(context.Background(), body["id"].(string))
			if err != nil {
				t.Fatal(err)
			}
			if stored.UserType != tc.wantType {
				t.Fatalf("stored user_type = %q, want %q", stored.UserType, tc.wantType)
			}
		})
	}
}
