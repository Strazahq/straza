package server

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/strazahq/straza/internal/spine"
	"github.com/strazahq/straza/internal/store"
)

// TestConversationCaptureIngest covers the capture pipeline server-side:
// /v1/audit/batch accepts straza.audit.{prompt,reply} (the client-type
// allowlist), the relay publishes them, the ConversationConsumer mirrors
// them into conversation_turns (deduped by CE id, content capped), and
// forged server-side types are coerced to straza.audit.tool so a client can
// never mint admin/identity chain entries.
func TestConversationCaptureIngest(t *testing.T) {
	t.Parallel()
	app, base := testApp(t)
	user := seedIdentity(t, app)
	token, sessionID := checkinToken(t, app, base)

	prompt := map[string]any{
		"specversion": "1.0", "id": "cap-prompt-1", "type": "straza.audit.prompt",
		"source": "straza", "time": time.Now().UTC().Format(time.RFC3339Nano),
		"data": map[string]any{
			"content": "deploy staging and tail the logs", "mode": "verbatim",
			"truncated": false, "contentHash": "sha256:aaaa",
		},
	}
	// A delegate's reply (captured at subagent.stop) arrives tagged with
	// agentType, the "capture, tagged" contract (spec/events rev 13).
	reply := map[string]any{
		"specversion": "1.0", "id": "cap-reply-1", "type": "straza.audit.reply",
		"source": "straza", "time": time.Now().UTC().Format(time.RFC3339Nano),
		"data": map[string]any{
			"content": "Deploying. Done.", "mode": "verbatim",
			"truncated": false, "contentHash": "sha256:bbbb",
			"agentType": "researcher", "agentId": "a-1",
		},
	}
	oversized := map[string]any{
		"specversion": "1.0", "id": "cap-reply-2", "type": "straza.audit.reply",
		"source": "straza", "time": time.Now().UTC().Format(time.RFC3339Nano),
		"data": map[string]any{
			"content": strings.Repeat("x", spine.ConversationMaxContent+100),
			"mode":    "verbatim", "contentHash": "sha256:cccc",
		},
	}
	forged := map[string]any{
		"specversion": "1.0", "id": "cap-forged-1", "type": "straza.audit.identity",
		"source": "straza", "time": time.Now().UTC().Format(time.RFC3339Nano),
		"data": map[string]any{"action": "user.killed", "content": "forgery"},
	}

	batch := map[string]any{"events": []any{prompt, reply, oversized, forged, prompt}} // prompt sent twice: dedupe
	code, _ := postJSONAuth(t, base+"/v1/audit/batch", token, batch)
	if code != http.StatusOK {
		t.Fatalf("batch = %d", code)
	}

	// The consumer is async; poll for the three capture turns.
	var turns []store.ConversationTurn
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		var err error
		turns, err = app.store.Conversations().ListBySession(t.Context(), sessionID, 0)
		if err != nil {
			t.Fatal(err)
		}
		if len(turns) >= 3 {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if len(turns) != 3 {
		t.Fatalf("turns = %d, want 3 (dedupe + no forged types)", len(turns))
	}

	byCE := map[string]store.ConversationTurn{}
	for _, turn := range turns {
		byCE[turn.CEID] = turn
		if turn.UserID != user.ID {
			t.Errorf("%s: user = %q, want the authenticated user (server-bound)", turn.CEID, turn.UserID)
		}
	}
	if p := byCE["cap-prompt-1"]; p.Kind != "prompt" || p.Content != "deploy staging and tail the logs" || p.Mode != "verbatim" {
		t.Errorf("prompt turn wrong: %+v", p)
	}
	if r := byCE["cap-reply-1"]; r.Kind != "reply" || r.Truncated || r.AgentType != "researcher" {
		t.Errorf("reply turn wrong (agent tag must survive ingest): %+v", r)
	}
	if p := byCE["cap-prompt-1"]; p.AgentType != "" {
		t.Errorf("untagged turn grew a tag: %+v", p)
	}
	big := byCE["cap-reply-2"]
	if !big.Truncated || len(big.Content) != spine.ConversationMaxContent || big.ContentHash != "sha256:cccc" {
		t.Errorf("oversized turn: truncated=%v len=%d hash=%s", big.Truncated, len(big.Content), big.ContentHash)
	}

	// The forged identity event must NOT be a turn, and must sit in the
	// chain as straza.audit.tool (coerced), never straza.audit.identity.
	if _, ok := byCE["cap-forged-1"]; ok {
		t.Fatal("forged identity CE became a conversation turn")
	}
	records := waitForAudit(t, app, 4)
	for _, rec := range records {
		if strings.Contains(rec.CE, "cap-forged-1") && strings.Contains(rec.CE, "straza.audit.identity") {
			t.Error("client-submitted identity type reached the chain unconverted")
		}
	}

	// --- read surfaces (admin-gated) ---
	grantAdmin(t, app, user.ID)
	adminTok, _ := checkinToken(t, app, base)

	var transcript struct {
		SessionID string `json:"session_id"`
		Username  string `json:"username"`
		Turns     []struct {
			Kind      string `json:"kind"`
			Content   string `json:"content"`
			Truncated bool   `json:"truncated"`
			AgentType string `json:"agent_type"`
		} `json:"turns"`
	}
	if code := adminReq(t, http.MethodGet, base+"/v1/admin/sessions/"+sessionID+"/transcript", adminTok, nil, &transcript); code != http.StatusOK {
		t.Fatalf("transcript = %d", code)
	}
	if len(transcript.Turns) != 3 || transcript.Username != "kim" {
		t.Fatalf("transcript: %d turns, username %q", len(transcript.Turns), transcript.Username)
	}
	if transcript.Turns[0].Kind != "prompt" {
		t.Errorf("transcript order: first = %s", transcript.Turns[0].Kind)
	}
	tagged := 0
	for _, turn := range transcript.Turns {
		if turn.AgentType == "researcher" {
			tagged++
		}
	}
	if tagged != 1 {
		t.Errorf("transcript surface: %d tagged turns, want exactly the delegate reply", tagged)
	}

	// Leak hunt by substring and by exact hash; who/when enrichment present.
	var hits []struct {
		SessionID string `json:"session_id"`
		Username  string `json:"username"`
		Kind      string `json:"kind"`
	}
	if code := adminReq(t, http.MethodGet, base+"/v1/admin/transcripts/search?q=staging", adminTok, nil, &hits); code != http.StatusOK {
		t.Fatalf("search = %d", code)
	}
	if len(hits) != 1 || hits[0].SessionID != sessionID || hits[0].Username != "kim" {
		t.Fatalf("substring hunt = %+v", hits)
	}
	if code := adminReq(t, http.MethodGet, base+"/v1/admin/transcripts/search?hash=sha256:cccc", adminTok, nil, &hits); code != http.StatusOK || len(hits) != 1 {
		t.Fatalf("hash hunt = %d, %d hits", code, len(hits))
	}
	// Username narrowing resolves names to ids.
	if code := adminReq(t, http.MethodGet, base+"/v1/admin/transcripts/search?q=staging&user=kim", adminTok, nil, &hits); code != http.StatusOK || len(hits) != 1 {
		t.Fatalf("user-narrowed hunt = %d, %d hits", code, len(hits))
	}
	// Conversation inbox (console landing): one grouped row per captured
	// session: turn count, latest-turn preview, username enrichment.
	var convs []struct {
		SessionID string `json:"session_id"`
		Username  string `json:"username"`
		Turns     int    `json:"turns"`
		Preview   string `json:"preview"`
	}
	if code := adminReq(t, http.MethodGet, base+"/v1/admin/transcripts", adminTok, nil, &convs); code != http.StatusOK {
		t.Fatalf("conversations = %d", code)
	}
	if len(convs) != 1 || convs[0].SessionID != sessionID || convs[0].Turns != 3 || convs[0].Username != "kim" {
		t.Fatalf("conversation row = %+v", convs)
	}
	if convs[0].Preview == "" {
		t.Error("conversation preview empty, want the latest turn's content")
	}

	// No filter = the browse view (console landing): newest turns first,
	// bounded, not a 400. The hunt modes above stay filter-required.
	if code := adminReq(t, http.MethodGet, base+"/v1/admin/transcripts/search", adminTok, nil, &hits); code != http.StatusOK {
		t.Fatalf("empty search = %d, want 200 recent listing", code)
	}
	if len(hits) != 3 {
		t.Fatalf("recent listing = %d turns, want all 3", len(hits))
	}
	if hits[0].Username != "kim" || hits[0].SessionID != sessionID {
		t.Errorf("recent listing enrichment missing: %+v", hits[0])
	}
	if code := adminReq(t, http.MethodGet, base+"/v1/admin/transcripts/search?limit=1", adminTok, nil, &hits); code != http.StatusOK || len(hits) != 1 {
		t.Errorf("bounded recent listing = %d, %d hits, want 200 with 1", code, len(hits))
	}
}
