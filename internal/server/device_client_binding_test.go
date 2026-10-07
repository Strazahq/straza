package server

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/strazahq/straza/internal/config"
	"github.com/strazahq/straza/internal/store"
)

// checkinDeviceAs checks a device credential in under the given harness name
// and version and answers the status code and body.
func checkinDeviceAs(t *testing.T, base, deviceToken, harness, version string) (int, map[string]any) {
	t.Helper()
	return postJSON(t, base+"/v1/checkin", map[string]any{
		"device_token": deviceToken,
		"harness":      map[string]string{"name": harness, "version": version},
		"attestation":  map[string]any{"managed": false, "hashes": map[string]string{}},
	})
}

// mintDevice creates a device row of the given client kind for the user and
// mints its credential the way enroll does.
func mintDevice(t *testing.T, app *App, userID, kind, fingerprint string) (store.Device, string) {
	t.Helper()
	d, err := app.store.Devices().Create(context.Background(), store.Device{
		UserID: userID, Name: "box-" + fingerprint, Fingerprint: fingerprint, Platform: "linux", ClientKind: kind,
	})
	if err != nil {
		t.Fatal(err)
	}
	tok, err := app.tokens.MintDeviceToken(userID, d.ID, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	return d, tok
}

// TestCheckinDeviceClientBinding pins the rule that a device credential opens
// sessions only for the class of client that enrolled it: a kit row under a
// human client's harness name is refused, a human row under a coding harness
// is refused, a row from before the kind was recorded counts as kit, and
// every refusal writes exactly one chained authn login failure.
func TestCheckinDeviceClientBinding(t *testing.T) {
	t.Parallel()
	app, base := testApp(t)
	user := seedIdentity(t, app)
	cases := []struct {
		name, kind, harness string
		wantCode            int
		wantReason          string
		wantHint            string
	}{
		{"kit row under claude-code", store.DeviceClientKit, "claude-code", http.StatusOK, "", ""},
		{"kit row under console", store.DeviceClientKit, "console", http.StatusForbidden,
			"device credential is bound to the enforcement kit", "Sign in to the console in your browser"},
		{"kit row under strazactl", store.DeviceClientKit, "strazactl", http.StatusForbidden,
			"device credential is bound to the enforcement kit", "Run strazactl login again from your own terminal"},
		{"kit row under self-service", store.DeviceClientKit, "self-service", http.StatusForbidden,
			"device credential is bound to the enforcement kit", "Sign in to the self-service page in your browser"},
		{"human row under strazactl", store.DeviceClientHuman, "strazactl", http.StatusOK, "", ""},
		{"human row under console", store.DeviceClientHuman, "console", http.StatusOK, "", ""},
		{"human row under claude-code", store.DeviceClientHuman, "claude-code", http.StatusForbidden,
			"device credential is bound to the human clients", "Run straza enroll on this machine"},
		{"unbound row under codex", "", "codex", http.StatusOK, "", ""},
		{"unbound row under strazactl", "", "strazactl", http.StatusForbidden,
			"device credential carries no client kind", "Run strazactl login again from your own terminal"},
	}
	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, tok := mintDevice(t, app, user.ID, tc.kind, fmt.Sprintf("fp-%d", i))
			version := fmt.Sprintf("b045-%d", i)
			code, body := checkinDeviceAs(t, base, tok, tc.harness, version)
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
			msg, _ := body["error"].(string)
			if !strings.Contains(msg, tc.harness+" session") || !strings.Contains(msg, tc.wantHint) {
				t.Fatalf("refusal = %q, want the harness name and %q", msg, tc.wantHint)
			}
			rec := waitAuthn(t, app, tc.name, failures)
			if rec["reason"] != tc.wantReason || rec["user"] != "kim" || rec["userId"] != user.ID || rec["via"] != "device-token" {
				t.Fatalf("login failure = %v, want reason %q for kim on the device-token lane", rec, tc.wantReason)
			}
			if n := countAuthn(t, app, failures); n != 1 {
				t.Fatalf("login failures for %s = %d, want exactly 1", label, n)
			}
		})
	}
}

// TestCheckinDeviceClientBindingLanes pins the binding on the id-token lane
// that names a device and on the refresh lane, where the session row's
// harness name governs rather than the body's.
func TestCheckinDeviceClientBindingLanes(t *testing.T) {
	t.Parallel()
	app, base := testApp(t)
	user := seedIdentity(t, app)
	kit, kitTok := mintDevice(t, app, user.ID, store.DeviceClientKit, "fp-lanes-kit")
	_, humanTok := mintDevice(t, app, user.ID, store.DeviceClientHuman, "fp-lanes-human")
	idToken := loginDeviceFlow(t, base, "kim", "hunter2!")

	code, body := postJSON(t, base+"/v1/checkin", map[string]any{
		"id_token": idToken, "device_id": kit.ID,
		"harness":     map[string]string{"name": "console", "version": "1"},
		"attestation": map[string]any{"managed": false, "hashes": map[string]string{}},
	})
	if msg, _ := body["error"].(string); code != http.StatusForbidden || !strings.Contains(msg, "enforcement kit") {
		t.Fatalf("id-token checkin naming a kit device under console = %d %v, want 403 with the kit sentence", code, body)
	}

	code, body = checkinDeviceAs(t, base, kitTok, "claude-code", "1")
	if code != http.StatusOK {
		t.Fatalf("kit session start = %d %v", code, body)
	}
	refresh := func(sessionToken, harness string) (int, map[string]any) {
		return postJSON(t, base+"/v1/checkin", map[string]any{
			"session_token": sessionToken,
			"harness":       map[string]string{"name": harness, "version": "1"},
			"attestation":   map[string]any{"managed": false, "hashes": map[string]string{}},
		})
	}
	// The body names console, the session row says claude-code, and the row
	// is what the kit device is checked against.
	if code, body = refresh(body["session_token"].(string), "console"); code != http.StatusOK {
		t.Fatalf("refresh of the kit session = %d %v, want 200", code, body)
	}
	code, body = checkinDeviceAs(t, base, humanTok, "strazactl", "1")
	if code != http.StatusOK {
		t.Fatalf("strazactl session start = %d %v", code, body)
	}
	if code, body = refresh(body["session_token"].(string), "strazactl"); code != http.StatusOK {
		t.Fatalf("refresh of the strazactl session = %d %v, want 200", code, body)
	}
}

// TestCheckinDeviceClientBindingBeforeExemption pins that the binding is
// judged ahead of the human clients' exemption from the attestation minimum:
// a kit credential under console is refused by the binding, not rescued by
// the exemption, while a human credential under strazactl still passes.
func TestCheckinDeviceClientBindingBeforeExemption(t *testing.T) {
	t.Parallel()
	app, base := testApp(t, func(c *config.Config) {
		c.Governance.MinAttestation = config.AttestationManaged
	})
	user := seedIdentity(t, app)
	_, kitTok := mintDevice(t, app, user.ID, store.DeviceClientKit, "fp-exempt-kit")
	_, humanTok := mintDevice(t, app, user.ID, store.DeviceClientHuman, "fp-exempt-human")

	code, body := checkinDeviceAs(t, base, kitTok, "console", "1")
	if msg, _ := body["error"].(string); code != http.StatusForbidden || !strings.Contains(msg, "enforcement kit") {
		t.Fatalf("kit credential under console = %d %v, want 403 with the kit sentence", code, body)
	}
	if code, body = checkinDeviceAs(t, base, humanTok, "strazactl", "1"); code != http.StatusOK {
		t.Fatalf("human credential under strazactl = %d %v, want 200 under the exemption", code, body)
	}
}

// TestCheckinAgentUnderHumanClient pins that a user typed agent or service
// never opens a session under a human client's harness name, whatever
// credential it presents.
func TestCheckinAgentUnderHumanClient(t *testing.T) {
	t.Parallel()
	app, base := testApp(t)
	_ = seedIdentity(t, app)
	ctx := context.Background()
	cases := []struct {
		name, userType, harness string
		wantCode                int
		wantWord                string
	}{
		{"agent under console", store.UserTypeAgent, "console", http.StatusForbidden, "an agent"},
		{"agent under strazactl", store.UserTypeAgent, "strazactl", http.StatusForbidden, "an agent"},
		{"service under self-service", store.UserTypeService, "self-service", http.StatusForbidden, "a service account"},
		{"agent under claude-code", store.UserTypeAgent, "claude-code", http.StatusOK, ""},
		{"agent under exec-wrapper", store.UserTypeAgent, "exec-wrapper", http.StatusOK, ""},
	}
	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			username := fmt.Sprintf("nhi-%d", i)
			u, err := app.store.Users().Create(ctx, store.User{Username: username, UserType: tc.userType})
			if err != nil {
				t.Fatal(err)
			}
			idt, err := app.tokens.MintIDToken(u.ID, "straza", time.Minute, username, "")
			if err != nil {
				t.Fatal(err)
			}
			version := fmt.Sprintf("b045-%d", i)
			code, body := postJSON(t, base+"/v1/checkin", map[string]any{
				"id_token":    idt,
				"harness":     map[string]string{"name": tc.harness, "version": version},
				"attestation": map[string]any{"managed": false, "hashes": map[string]string{}},
			})
			if code != tc.wantCode {
				t.Fatalf("checkin = %d %v, want %d", code, body, tc.wantCode)
			}
			if tc.wantCode == http.StatusOK {
				return
			}
			want := fmt.Sprintf("the user %s is %s, and only a person can open a %s session", username, tc.wantWord, tc.harness)
			if msg, _ := body["error"].(string); !strings.HasPrefix(msg, want) {
				t.Fatalf("refusal = %q, want a sentence starting %q", msg, want)
			}
			label := tc.harness + "/" + version
			failures := func(d map[string]any) bool {
				return d["action"] == "login" && d["outcome"] == "failure" && d["harness"] == label
			}
			rec := waitAuthn(t, app, tc.name, failures)
			if rec["reason"] != "only a person can open a human client session" || rec["user"] != username || rec["via"] != "id-token" {
				t.Fatalf("login failure = %v, want the person-only reason for %s", rec, username)
			}
			if n := countAuthn(t, app, failures); n != 1 {
				t.Fatalf("login failures for %s = %d, want exactly 1", label, n)
			}
		})
	}
}

// TestEnrollClientKind pins the enrol side: the kind is recorded on the row,
// a missing kind means kit, a bad value is refused, an unbound row is bound
// on reuse, a row of the other kind is never reused, an agent cannot enrol a
// credential for the human clients, and the admin device list shows the kind.
func TestEnrollClientKind(t *testing.T) {
	t.Parallel()
	app, base := testApp(t)
	user := seedIdentity(t, app)
	ctx := context.Background()
	idToken := loginDeviceFlow(t, base, "kim", "hunter2!")
	enroll := func(t *testing.T, idt string, kind any, fingerprint string) (int, map[string]any) {
		t.Helper()
		body := map[string]any{
			"id_token": idt,
			"device":   map[string]string{"name": "box-" + fingerprint, "platform": "linux", "fingerprint": fingerprint},
		}
		if kind != nil {
			body["client_kind"] = kind
		}
		return postJSON(t, base+"/v1/enroll", body)
	}
	// enrolled enrols and answers the row's id and stored client kind.
	enrolled := func(t *testing.T, idt string, kind any, fingerprint string) (string, string) {
		t.Helper()
		code, body := enroll(t, idt, kind, fingerprint)
		if code != http.StatusOK {
			t.Fatalf("enroll = %d %v, want 200", code, body)
		}
		id, _ := body["device_id"].(string)
		d, err := app.store.Devices().GetByID(ctx, id)
		if err != nil {
			t.Fatalf("device %q: %v", id, err)
		}
		return id, d.ClientKind
	}

	if _, kind := enrolled(t, idToken, nil, "fp-missing"); kind != store.DeviceClientKit {
		t.Fatalf("kind of an enrol without client_kind = %q, want kit", kind)
	}
	kitID, kind := enrolled(t, idToken, "kit", "fp-kit")
	if kind != store.DeviceClientKit {
		t.Fatalf("kind of a kit enrol = %q", kind)
	}
	if again, _ := enrolled(t, idToken, "kit", "fp-kit"); again != kitID {
		t.Fatalf("a kit enrol of the same fingerprint made a new row %s, want %s", again, kitID)
	}
	if _, kind := enrolled(t, idToken, "human", "fp-human"); kind != store.DeviceClientHuman {
		t.Fatalf("kind of a human enrol = %q", kind)
	}
	if code, body := enroll(t, idToken, "phone", "fp-bad"); code != http.StatusBadRequest || body["error"] != "client_kind must be kit or human" {
		t.Fatalf("bad client_kind = %d %v, want 400 with the sentence", code, body)
	}

	old, err := app.store.Devices().Create(ctx, store.Device{UserID: user.ID, Name: "old-box", Fingerprint: "fp-old", Platform: "linux"})
	if err != nil {
		t.Fatal(err)
	}
	if id, kind := enrolled(t, idToken, "human", "fp-old"); id != old.ID || kind != store.DeviceClientHuman {
		t.Fatalf("a human enrol over an unbound row gave %s kind %q, want %s bound to human", id, kind, old.ID)
	}
	if id, kind := enrolled(t, idToken, "human", "fp-kit"); id == kitID || kind != store.DeviceClientHuman {
		t.Fatalf("a human enrol over the kit row's fingerprint gave %s kind %q, want a new human row", id, kind)
	}
	if d, _ := app.store.Devices().GetByID(ctx, kitID); d.ClientKind != store.DeviceClientKit {
		t.Fatalf("the kit row changed kind to %q", d.ClientKind)
	}

	joe, err := app.store.Users().Create(ctx, store.User{Username: "joe", UserType: store.UserTypeAgent})
	if err != nil {
		t.Fatal(err)
	}
	joeToken, err := app.tokens.MintIDToken(joe.ID, "straza", time.Minute, "joe", "")
	if err != nil {
		t.Fatal(err)
	}
	code, body := enroll(t, joeToken, "human", "fp-joe")
	want := "the user joe is an agent, and only a person can enrol a credential for strazactl or the console"
	if code != http.StatusForbidden || body["error"] != want {
		t.Fatalf("agent enrolling a human credential = %d %v, want 403 with %q", code, body, want)
	}
	refused := func(d map[string]any) bool {
		return d["action"] == "login" && d["outcome"] == "failure" && d["user"] == "joe"
	}
	rec := waitAuthn(t, app, "joe's refused enrol", refused)
	if rec["reason"] != "only a person can enrol a credential for the human clients" || rec["userId"] != joe.ID || rec["via"] != "id-token" {
		t.Fatalf("login failure = %v, want the person-only enrol reason for joe", rec)
	}
	if n := countAuthn(t, app, refused); n != 1 {
		t.Fatalf("login failures for joe = %d, want exactly 1", n)
	}
	if _, kind := enrolled(t, joeToken, "kit", "fp-joe"); kind != store.DeviceClientKit {
		t.Fatalf("kind of joe's kit enrol = %q", kind)
	}

	grantAdmin(t, app, user.ID)
	var devs []struct {
		ID         string `json:"id"`
		ClientKind string `json:"client_kind"`
	}
	if code := adminReq(t, http.MethodGet, base+"/v1/admin/users/"+user.ID+"/devices", idToken, nil, &devs); code != http.StatusOK {
		t.Fatalf("list devices = %d", code)
	}
	shown := map[string]string{}
	for _, d := range devs {
		shown[d.ID] = d.ClientKind
	}
	if shown[kitID] != store.DeviceClientKit || shown[old.ID] != store.DeviceClientHuman {
		t.Fatalf("device list kinds = %v, want %s kit and %s human", shown, kitID, old.ID)
	}
}
