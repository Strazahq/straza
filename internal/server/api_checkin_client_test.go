package server

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/strazahq/straza/internal/store"
)

// TestCheckinRecordsClientVersion pins the additive client stamp: a check-in
// that names its build lands the version and the commit on the session, the
// sessions list shows them as client_version and client_commit, and one that
// sends nothing (an older client) leaves both blank and out of the list.
func TestCheckinRecordsClientVersion(t *testing.T) {
	t.Parallel()
	app, base := testApp(t)
	user := seedIdentity(t, app)
	grantAdmin(t, app, user.ID)
	adminTok, _ := checkinToken(t, app, base)
	idToken := loginDeviceFlow(t, base, "kim", "hunter2!")

	cases := []struct {
		name       string
		client     map[string]string
		want       string
		wantCommit string
	}{
		{"stamped client", map[string]string{"version": "v1.2.3-4-gabcdef0", "commit": "abcdef0"}, "v1.2.3-4-gabcdef0", "abcdef0"},
		{"a release build", map[string]string{"version": "1.1.2", "commit": "87aad807"}, "1.1.2", "87aad807"},
		{"older client sends nothing", nil, "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body := map[string]any{
				"id_token":    idToken,
				"harness":     map[string]string{"name": "claude-code", "version": "2.1.0"},
				"attestation": map[string]any{"managed": false, "hashes": map[string]string{}},
			}
			if tc.client != nil {
				body["client"] = tc.client
			}
			code, resp := postJSON(t, base+"/v1/checkin", body)
			if code != http.StatusOK {
				t.Fatalf("checkin = %d: %v", code, resp)
			}
			ses, err := app.store.Sessions().GetByID(context.Background(), resp["session_id"].(string))
			if err != nil || ses.ClientVersion != tc.want || ses.ClientCommit != tc.wantCommit {
				t.Fatalf("stored client = %q at %q, %v; want %q at %q", ses.ClientVersion, ses.ClientCommit, err, tc.want, tc.wantCommit)
			}
			var rows []map[string]any
			if code := adminReq(t, http.MethodGet, base+"/v1/admin/sessions", adminTok, nil, &rows); code != http.StatusOK {
				t.Fatalf("sessions list = %d", code)
			}
			var row map[string]any
			for _, r := range rows {
				if r["id"] == ses.ID {
					row = r
				}
			}
			if row == nil {
				t.Fatalf("session %s is not in the sessions list", ses.ID)
			}
			for field, want := range map[string]string{"client_version": tc.want, "client_commit": tc.wantCommit} {
				got, present := row[field]
				if want == "" && present {
					t.Errorf("listed %s = %v, want the field left out", field, got)
				}
				if want != "" && got != want {
					t.Errorf("listed %s = %v, want %q", field, got, want)
				}
			}
		})
	}
}

// TestCheckinRefusesOverlongHarnessAndClient pins the check-in's text
// bounds: a harness name, a harness version or a client version over 256
// bytes is refused with a sentence that names the field, its size, the
// bound and what to do, before the check-in writes a session or a record,
// and a check-in at the bound is unchanged. The raw case sends 900 KiB of
// unescaped < the way a hand-made client would, inside the body cap.
func TestCheckinRefusesOverlongHarnessAndClient(t *testing.T) {
	t.Parallel()
	app, base := testApp(t)
	seedIdentity(t, app)
	idToken := loginDeviceFlow(t, base, "kim", "hunter2!")
	ctx := context.Background()
	at := strings.Repeat("v", checkinTextMax)
	over := at + "v"
	cases := []struct {
		name                     string
		harness, version, client string
		want                     string
	}{
		{"harness name over the bound", over, "2.1.0", "1.1.2", fmt.Sprintf(harnessNameTooLongMsg, len(over), checkinTextMax)},
		{"harness name of raw < signs", strings.Repeat("<", 900<<10), "2.1.0", "1.1.2", fmt.Sprintf(harnessNameTooLongMsg, 900<<10, checkinTextMax)},
		{"harness version over the bound", "claude-code", over, "1.1.2", fmt.Sprintf(harnessVersionTooLongMsg, len(over), checkinTextMax)},
		{"client version over the bound", "claude-code", "2.1.0", over, fmt.Sprintf(clientVersionTooLongMsg, len(over), checkinTextMax)},
		{"every field at the bound", at, at, at, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			before, err := app.store.Sessions().List(ctx, "")
			if err != nil {
				t.Fatal(err)
			}
			head, err := app.store.Outbox().Head(ctx)
			if err != nil {
				t.Fatal(err)
			}
			body := `{"id_token":"` + idToken + `","harness":{"name":"` + tc.harness + `","version":"` + tc.version +
				`"},"client":{"version":"` + tc.client + `","commit":"abcdef0"},"attestation":{"managed":false,"hashes":{}}}`
			resp, err := http.Post(base+"/v1/checkin", "application/json", strings.NewReader(body))
			if err != nil {
				t.Fatal(err)
			}
			raw, _ := io.ReadAll(resp.Body)
			_ = resp.Body.Close()
			if tc.want == "" {
				if resp.StatusCode != http.StatusOK {
					t.Fatalf("checkin at the bound = %d: %.300s", resp.StatusCode, raw)
				}
				return
			}
			var answer map[string]string
			if err := jsonUnmarshal(raw, &answer); err != nil || resp.StatusCode != http.StatusBadRequest || answer["error"] != tc.want {
				t.Fatalf("checkin = %d %.300s, want 400 with %q", resp.StatusCode, raw, tc.want)
			}
			after, err := app.store.Sessions().List(ctx, "")
			if err != nil || len(after) != len(before) {
				t.Fatalf("sessions %d before and %d after the refusal (%v), want no new session", len(before), len(after), err)
			}
			if now, _ := app.store.Outbox().Head(ctx); now != head {
				t.Fatalf("the refused check-in wrote an outbox row: head %q, was %q", now, head)
			}
		})
	}
}

// TestCheckinRefusesNULCharacter pins the refusal of a check-in whose
// harness name or version, client version or commit, or an attestation hash
// holds a NUL character. On every lane it answers 400 with a sentence that
// names the field, before the check-in reads a credential, so it starts no
// session and writes no record, as the check-in's other 400 refusals do. The
// test store is SQLite, which would keep the character, so the refusal does
// not depend on the store. Each lane answers a check-in without the
// character as before.
func TestCheckinRefusesNULCharacter(t *testing.T) {
	t.Parallel()
	app, base := testApp(t)
	kim := seedIdentity(t, app)
	ctx := context.Background()
	idToken := loginDeviceFlow(t, base, "kim", "hunter2!")
	_, deviceToken := mintDevice(t, app, kim.ID, store.DeviceClientHuman, "host:kim-laptop")
	sessionToken, _ := checkinTokenAs(t, base, "claude-code")
	h := newKeyHolder(t, base)
	h.enroll(idToken, "sha256:fp-nul")

	// body is a check-in under harness with every text field set and the
	// lane's credential fields.
	body := func(harness string, credential map[string]any) map[string]any {
		b := map[string]any{
			"harness":     map[string]string{"name": harness, "version": "2.1.0"},
			"client":      map[string]string{"version": "1.1.2", "commit": "abcdef0"},
			"attestation": map[string]any{"managed": false, "hashes": map[string]string{"self": "sha256:x"}},
		}
		for k, v := range credential {
			b[k] = v
		}
		return b
	}
	post := func(t *testing.T, b map[string]any) (int, map[string]any) { return postJSON(t, base+"/v1/checkin", b) }
	lanes := []struct {
		name string
		want int // the answer to the lane's check-in without a NUL character
		body func() map[string]any
		post func(*testing.T, map[string]any) (int, map[string]any)
	}{
		{"id token", http.StatusOK, func() map[string]any {
			return body("claude-code", map[string]any{"id_token": idToken})
		}, post},
		{"console", http.StatusOK, func() map[string]any {
			return body("console", map[string]any{"id_token": idToken})
		}, post},
		{"self-service page", http.StatusOK, func() map[string]any {
			return body("self-service", map[string]any{"id_token": idToken})
		}, post},
		{"device credential", http.StatusForbidden, func() map[string]any {
			return body("console", map[string]any{"device_token": deviceToken})
		}, post},
		{"key", http.StatusOK, func() map[string]any {
			return body("claude-code", map[string]any{"device_id": h.device, "counter": h.counter + 1})
		}, func(_ *testing.T, b map[string]any) (int, map[string]any) {
			code, out := h.call("/v1/checkin", "", b)
			if code == http.StatusOK {
				h.counter++
			}
			return code, out
		}},
		{"session refresh", http.StatusOK, func() map[string]any {
			return body("claude-code", map[string]any{"session_token": sessionToken})
		}, post},
	}
	text := func(b map[string]any, field string) map[string]string { return b[field].(map[string]string) }
	hashes := func(b map[string]any) map[string]string {
		return b["attestation"].(map[string]any)["hashes"].(map[string]string)
	}
	fields := []struct {
		name  string
		field string // the field the refusal names
		step  string // the next step the refusal gives for the field
		spoil func(map[string]any)
	}{
		{"harness name", "harness.name", nulStepStraza, func(b map[string]any) { text(b, "harness")["name"] += "\x00" }},
		{"harness version", "harness.version", nulStepHarness, func(b map[string]any) { text(b, "harness")["version"] = "2.1\x00.0" }},
		{"client version", "client.version", nulStepStraza, func(b map[string]any) { text(b, "client")["version"] = "\x001.1.2" }},
		{"client commit", "client.commit", nulStepStraza, func(b map[string]any) { text(b, "client")["commit"] = "abc\x00def0" }},
		{"attestation hash", "attestation.hashes", nulStepStraza, func(b map[string]any) { hashes(b)["self"] = "sha256:\x00" }},
		{"attestation artifact", "attestation.hashes", nulStepStraza, func(b map[string]any) { hashes(b)["self\x00"] = "sha256:x" }},
	}
	for _, lane := range lanes {
		t.Run(lane.name, func(t *testing.T) {
			if code, out := lane.post(t, lane.body()); code != lane.want {
				t.Fatalf("check-in without a NUL character = %d %v, want %d", code, out, lane.want)
			}
			for _, f := range fields {
				t.Run(f.name, func(t *testing.T) {
					before, err := app.store.Sessions().List(ctx, "")
					if err != nil {
						t.Fatal(err)
					}
					head, err := app.store.Outbox().Head(ctx)
					if err != nil {
						t.Fatal(err)
					}
					b := lane.body()
					f.spoil(b)
					want := fmt.Sprintf(checkinNULMsg, f.field, f.step)
					if code, out := lane.post(t, b); code != http.StatusBadRequest || out["error"] != want {
						t.Fatalf("checkin = %d %v, want 400 with %q", code, out, want)
					}
					after, err := app.store.Sessions().List(ctx, "")
					if err != nil || len(after) != len(before) {
						t.Fatalf("sessions %d before and %d after the refusal (%v), want no new session", len(before), len(after), err)
					}
					if now, _ := app.store.Outbox().Head(ctx); now != head {
						t.Fatalf("the refused check-in wrote an outbox row: head %q, was %q", now, head)
					}
				})
			}
		})
	}
}
