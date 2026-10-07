package server

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/strazahq/straza/internal/bodystore"
	"github.com/strazahq/straza/internal/store"
)

// TestTranscriptBodyResolution pins the body-store read contract: the
// transcripts API
// serves identical JSON wherever bodies live, and a missing external body is
// a loud body_missing flag, never silent empty content.
func TestTranscriptBodyResolution(t *testing.T) {
	t.Parallel()
	app, base := testApp(t)
	user := seedIdentity(t, app)
	grantAdmin(t, app, user.ID)
	adminTok, _ := checkinToken(t, app, base)

	mem := bodystore.NewMemory()
	app.bodyStore = mem
	ctx := t.Context()

	if err := mem.Put(ctx, "sha256:ext-1", []byte("externalized prompt body")); err != nil {
		t.Fatal(err)
	}
	turns := []store.ConversationTurn{
		{CEID: "bt-1", SessionID: "bs-1", UserID: "u1", Kind: "prompt",
			ContentHash: "sha256:ext-1", BodyExternal: true, At: time.Now().UTC()},
		{CEID: "bt-2", SessionID: "bs-1", UserID: "u1", Kind: "reply",
			Content: "inline reply", ContentHash: "sha256:inl", At: time.Now().UTC().Add(time.Second)},
		{CEID: "bt-3", SessionID: "bs-1", UserID: "u1", Kind: "prompt",
			ContentHash: "sha256:gone", BodyExternal: true, At: time.Now().UTC().Add(2 * time.Second)},
	}
	if _, err := app.store.Conversations().InsertBatch(ctx, turns); err != nil {
		t.Fatal(err)
	}

	req, _ := http.NewRequest(http.MethodGet, base+"/v1/admin/sessions/bs-1/transcript", nil)
	req.Header.Set("Authorization", "Bearer "+adminTok)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("transcript = %d", resp.StatusCode)
	}
	var out struct {
		Turns []struct {
			Content     string `json:"content"`
			BodyMissing bool   `json:"body_missing"`
		} `json:"turns"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	if len(out.Turns) != 3 {
		t.Fatalf("turns = %d, want 3", len(out.Turns))
	}
	if out.Turns[0].Content != "externalized prompt body" || out.Turns[0].BodyMissing {
		t.Fatalf("external turn = %+v, want resolved body", out.Turns[0])
	}
	if out.Turns[1].Content != "inline reply" || out.Turns[1].BodyMissing {
		t.Fatalf("inline turn = %+v", out.Turns[1])
	}
	if !out.Turns[2].BodyMissing || out.Turns[2].Content != "" {
		t.Fatalf("gone-body turn = %+v, want loud body_missing", out.Turns[2])
	}
}
