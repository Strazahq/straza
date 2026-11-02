package server

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
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
