package server

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"net/http"
	"testing"

	"github.com/strazahq/straza/internal/store"
)

// TestNHIKeyDeleteWithoutKey pins the key delete of a user who holds no
// assertion key: a 404 that says there is nothing to remove and where to
// look, with no admin record, no identity event and no Error line, because
// nothing changed. A person holds no key either and gets the same answer.
// An AI agent with a key still answers 200 with one nhi-key.removed record.
func TestNHIKeyDeleteWithoutKey(t *testing.T) {
	t.Parallel()
	log, logs := captureLogger()
	app, base := testAppPreRun(t, []func(*App){func(a *App) { a.log = log }})
	ctx := context.Background()
	admin := seedIdentity(t, app)
	grantAdmin(t, app, admin.ID)
	tok, _ := checkinToken(t, app, base)

	pub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	keyed := seedNHIWithKey(t, app, "keyed-bot", pub)
	bare, err := app.store.Users().Create(ctx, store.User{Username: "bare-bot", Attrs: `{"kind":"nhi"}`})
	if err != nil {
		t.Fatal(err)
	}
	person := mkHuman(t, app, "pat")

	cases := []struct {
		name    string
		user    store.User
		code    int
		says    string
		records int
	}{
		{"an AI agent with no key", bare, http.StatusNotFound,
			"the user bare-bot has no assertion key registered, so there is nothing to remove. " +
				"To see what is registered, read GET /v1/admin/users/" + bare.ID + "/nhi-key. " +
				"For an AI agent the console also shows the key status on the user's panel.", 0},
		{"a person, who never holds a key, answers the same 404", person, http.StatusNotFound,
			"the user pat has no assertion key registered, so there is nothing to remove. " +
				"To see what is registered, read GET /v1/admin/users/" + person.ID + "/nhi-key. " +
				"For an AI agent the console also shows the key status on the user's panel.", 0},
		{"an AI agent with a key, the positive control", keyed, http.StatusOK, "", 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			logs.Reset()
			code, _, out := callJSON(t, http.MethodDelete, base+"/v1/admin/users/"+tc.user.ID+"/nhi-key", tok, nil)
			if code != tc.code {
				t.Fatalf("delete = %d %v, want %d", code, out, tc.code)
			}
			if tc.says != "" && out["error"] != tc.says {
				t.Errorf("error = %q, want %q", out["error"], tc.says)
			}
			if tc.code == http.StatusOK && (out["status"] != "removed" || out["user_id"] != tc.user.ID) {
				t.Errorf("answer = %v, want status removed for %s", out, tc.user.ID)
			}
			if recs := errorRecords(logs); len(recs) != 0 {
				t.Errorf("Error lines = %v, want none", recs)
			}
			records := keyRemoveEvents(t, app, tc.user.ID)
			if len(records) != tc.records {
				t.Fatalf("nhi-key.removed records = %d %v, want %d", len(records), records, tc.records)
			}
			for _, ev := range records {
				want := map[string]any{
					"action": "nhi-key.removed", "target": tc.user.ID, "user": tc.user.ID,
					"actor": "kim", "actorId": admin.ID, "actorVia": "session",
				}
				for k, v := range want {
					if ev[k] != v {
						t.Errorf("record %s = %v, want %v (record %v)", k, ev[k], v, ev)
					}
				}
			}
			identity := 0
			for _, d := range outboxDataFor(t, app, "straza.identity.updated") {
				if d["action"] == "nhi-key.removed" && d["user"] == tc.user.ID {
					identity++
				}
			}
			if identity != tc.records {
				t.Errorf("nhi-key.removed identity events = %d, want %d", identity, tc.records)
			}
		})
	}
}
