package server

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"
)

// TestCheckinRecordsWhere pins the machine and the folder of a session: a
// check-in that starts a session lands host and folder on it, the sessions
// list and the read of one session answer them, and a check-in that names
// neither, as every older client does, leaves both empty and out of the
// answers.
func TestCheckinRecordsWhere(t *testing.T) {
	t.Parallel()
	app, base := testApp(t)
	user := seedIdentity(t, app)
	grantAdmin(t, app, user.ID)
	adminTok, _ := checkinToken(t, app, base)
	idToken := loginDeviceFlow(t, base, "kim", "hunter2!")

	cases := []struct {
		name         string
		host, folder string
	}{
		{"machine and folder", "build-box-7", "~/src/billing"},
		{"machine alone", "build-box-7", ""},
		{"folder alone", "", `~\src\billing`},
		{"an older client sends neither", "", ""},
		{"both at the bound", strings.Repeat("h", checkinTextMax), strings.Repeat("f", checkinFolderMax)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body := map[string]any{
				"id_token":    idToken,
				"harness":     map[string]string{"name": "claude-code", "version": "2.1.0"},
				"attestation": map[string]any{"managed": false, "hashes": map[string]string{}},
			}
			if tc.host != "" {
				body["host"] = tc.host
			}
			if tc.folder != "" {
				body["folder"] = tc.folder
			}
			code, resp := postJSON(t, base+"/v1/checkin", body)
			if code != http.StatusOK {
				t.Fatalf("checkin = %d: %v", code, resp)
			}
			id := resp["session_id"].(string)
			ses, err := app.store.Sessions().GetByID(context.Background(), id)
			if err != nil || ses.Host != tc.host || ses.Folder != tc.folder {
				t.Fatalf("stored session is on %q in %q, %v; want %q in %q", ses.Host, ses.Folder, err, tc.host, tc.folder)
			}
			var rows []map[string]any
			if code := adminReq(t, http.MethodGet, base+"/v1/admin/sessions", adminTok, nil, &rows); code != http.StatusOK {
				t.Fatalf("sessions list = %d", code)
			}
			var listed map[string]any
			for _, r := range rows {
				if r["id"] == id {
					listed = r
				}
			}
			if listed == nil {
				t.Fatalf("session %s is not in the sessions list", id)
			}
			var one map[string]any
			if code := adminReq(t, http.MethodGet, base+"/v1/admin/sessions/"+id, adminTok, nil, &one); code != http.StatusOK {
				t.Fatalf("read of session %s = %d", id, code)
			}
			for read, row := range map[string]map[string]any{"the list": listed, "the read of one session": one} {
				for field, want := range map[string]string{"host": tc.host, "folder": tc.folder} {
					got, present := row[field]
					if want == "" && present {
						t.Errorf("%s answers %s = %v, want the field left out", read, field, got)
					}
					if want != "" && got != want {
						t.Errorf("%s answers %s = %v, want %q", read, field, got, want)
					}
				}
			}
		})
	}
}

// TestCheckinRefreshKeepsWhere pins that the machine and the folder are
// written when a check-in starts a session and never by a refresh: a
// refresh that names another machine and folder answers 200 and leaves the
// session's own in place.
func TestCheckinRefreshKeepsWhere(t *testing.T) {
	t.Parallel()
	app, base := testApp(t)
	seedIdentity(t, app)
	idToken := loginDeviceFlow(t, base, "kim", "hunter2!")
	start := map[string]any{
		"id_token":    idToken,
		"harness":     map[string]string{"name": "claude-code", "version": "2.1.0"},
		"attestation": map[string]any{"managed": false, "hashes": map[string]string{}},
		"host":        "build-box-7",
		"folder":      "~/src/billing",
	}
	code, resp := postJSON(t, base+"/v1/checkin", start)
	if code != http.StatusOK {
		t.Fatalf("checkin = %d: %v", code, resp)
	}
	id, token := resp["session_id"].(string), resp["session_token"].(string)
	refresh := map[string]any{
		"session_token": token,
		"harness":       map[string]string{"name": "claude-code", "version": "2.1.0"},
		"attestation":   map[string]any{"managed": false, "hashes": map[string]string{}},
		"host":          "another-box",
		"folder":        "~/elsewhere",
	}
	code, resp = postJSON(t, base+"/v1/checkin", refresh)
	if code != http.StatusOK || resp["session_id"] != id {
		t.Fatalf("refresh = %d %v, want 200 for session %s", code, resp, id)
	}
	ses, err := app.store.Sessions().GetByID(context.Background(), id)
	if err != nil || ses.Host != "build-box-7" || ses.Folder != "~/src/billing" {
		t.Fatalf("after the refresh the session is on %q in %q, %v; want build-box-7 in ~/src/billing", ses.Host, ses.Folder, err)
	}
}

// TestCheckinRefusesOverlongWhere pins the bounds of host and folder: a
// host over 256 bytes or a folder over 1024 is refused with a sentence that
// names the field, its size, the bound and what to do, on a session start
// and on a refresh, before the check-in writes a session or a record.
func TestCheckinRefusesOverlongWhere(t *testing.T) {
	t.Parallel()
	app, base := testApp(t)
	seedIdentity(t, app)
	idToken := loginDeviceFlow(t, base, "kim", "hunter2!")
	sessionToken, _ := checkinTokenAs(t, base, "claude-code")
	ctx := context.Background()
	longHost := strings.Repeat("h", checkinTextMax+1)
	longFolder := strings.Repeat("f", checkinFolderMax+1)
	credentials := map[string]map[string]any{
		"a session start": {"id_token": idToken},
		"a refresh":       {"session_token": sessionToken},
	}
	cases := []struct {
		name         string
		host, folder string
		want         string
	}{
		{"host over the bound", longHost, "~/src", fmt.Sprintf(hostTooLongMsg, len(longHost), checkinTextMax)},
		{"folder over the bound", "build-box-7", longFolder, fmt.Sprintf(folderTooLongMsg, len(longFolder), checkinFolderMax)},
		{"a folder of megabytes", "build-box-7", strings.Repeat("f", 900<<10), fmt.Sprintf(folderTooLongMsg, 900<<10, checkinFolderMax)},
	}
	for lane, credential := range credentials {
		for _, tc := range cases {
			t.Run(lane+", "+tc.name, func(t *testing.T) {
				before, err := app.store.Sessions().List(ctx, "")
				if err != nil {
					t.Fatal(err)
				}
				head, err := app.store.Outbox().Head(ctx)
				if err != nil {
					t.Fatal(err)
				}
				body := map[string]any{
					"harness":     map[string]string{"name": "claude-code", "version": "2.1.0"},
					"attestation": map[string]any{"managed": false, "hashes": map[string]string{}},
					"host":        tc.host,
					"folder":      tc.folder,
				}
				for k, v := range credential {
					body[k] = v
				}
				code, out := postJSON(t, base+"/v1/checkin", body)
				if code != http.StatusBadRequest || out["error"] != tc.want {
					t.Fatalf("checkin = %d %.300v, want 400 with %q", code, out, tc.want)
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
}
