package server

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/strazahq/straza/internal/store"
)

// Transcript read surfaces:
// the READABLE view over captured turns. Admin-gated like every /v1/admin
// route; in verbatim deployments this is a secrets-bearing surface, so it
// belongs to a dedicated auditor role or to straza-admin only.

type turnPayload struct {
	At          time.Time `json:"at"`
	Kind        string    `json:"kind"`
	Mode        string    `json:"mode"`
	Content     string    `json:"content"`
	Truncated   bool      `json:"truncated,omitempty"`
	ContentHash string    `json:"content_hash"`
	AgentType   string    `json:"agent_type,omitempty"` // delegate class; "" = the main agent's own lane
	// BodyMissing flags an INTEGRITY finding: the turn's body should
	// be in the body store but was not found (or no store is configured for
	// an external turn). Loud, never silent.
	BodyMissing bool   `json:"body_missing,omitempty"`
	SessionID   string `json:"session_id,omitempty"`
	UserID      string `json:"user_id,omitempty"`
	Username    string `json:"username,omitempty"`
}

// resolveBodies fills externally-stored turn bodies with bounded
// concurrency, mutating turns in place; the returned slice flags turns whose
// body could not be resolved. Inline turns cost nothing.
func (a *App) resolveBodies(ctx context.Context, turns []store.ConversationTurn) []bool {
	missing := make([]bool, len(turns))
	var wg sync.WaitGroup
	sem := make(chan struct{}, 8)
	for i := range turns {
		if !turns[i].BodyExternal {
			continue
		}
		if a.bodyStore == nil {
			missing[i] = true
			a.log.Warn("external transcript body with no body store configured (integrity)",
				"hash", turns[i].ContentHash)
			continue
		}
		wg.Add(1)
		sem <- struct{}{}
		go func(i int) {
			defer wg.Done()
			defer func() { <-sem }()
			body, err := a.bodyStore.Get(ctx, turns[i].ContentHash)
			if err != nil {
				missing[i] = true
				a.log.Warn("transcript body missing from body store (integrity)",
					"hash", turns[i].ContentHash, "err", err)
				return
			}
			turns[i].Content = string(body)
		}(i)
	}
	wg.Wait()
	return missing
}

// handleTranscriptsList serves GET /v1/admin/transcripts: the conversation
// inbox: one row per captured session, newest activity first, with turn
// count and a preview of the latest turn (operators browse
// conversations, not raw turns; the search endpoint stays turn-level).
func (a *App) handleTranscriptsList(w http.ResponseWriter, r *http.Request) {
	limit := 0
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 1000 {
			limit = n
		}
	}
	convs, err := a.store.Conversations().ListConversations(r.Context(), limit)
	if err != nil {
		a.fail(w, r, http.StatusInternalServerError, "conversation listing failed", err)
		return
	}
	usernames := map[string]string{}
	type convPayload struct {
		SessionID string    `json:"session_id"`
		UserID    string    `json:"user_id"`
		Username  string    `json:"username,omitempty"`
		Turns     int       `json:"turns"`
		FirstAt   time.Time `json:"first_at"`
		LastAt    time.Time `json:"last_at"`
		Preview   string    `json:"preview"`
	}
	out := make([]convPayload, len(convs))
	for i, c := range convs {
		name, ok := usernames[c.UserID]
		if !ok {
			if u, err := a.store.Users().GetByID(r.Context(), c.UserID); err == nil {
				name = u.Username
			}
			usernames[c.UserID] = name
		}
		out[i] = convPayload{SessionID: c.SessionID, UserID: c.UserID, Username: name,
			Turns: c.Turns, FirstAt: c.FirstAt, LastAt: c.LastAt, Preview: c.Preview}
	}
	writeJSON(w, http.StatusOK, out)
}

// handleSessionTranscript returns one session's captured conversation in
// order, for the console Transcript tab and `strazactl sessions transcript`.
func (a *App) handleSessionTranscript(w http.ResponseWriter, r *http.Request) {
	sessionID := r.PathValue("id")
	turns, err := a.store.Conversations().ListBySession(r.Context(), sessionID, 0)
	if err != nil {
		a.fail(w, r, http.StatusInternalServerError, "transcript read failed", err)
		return
	}
	missing := a.resolveBodies(r.Context(), turns)
	username := ""
	if len(turns) > 0 {
		if u, err := a.store.Users().GetByID(r.Context(), turns[0].UserID); err == nil {
			username = u.Username
		}
	}
	out := struct {
		SessionID string        `json:"session_id"`
		Username  string        `json:"username,omitempty"`
		Turns     []turnPayload `json:"turns"`
	}{SessionID: sessionID, Username: username, Turns: make([]turnPayload, len(turns))}
	for i, t := range turns {
		out.Turns[i] = turnPayload{At: t.At, Kind: t.Kind, Mode: t.Mode,
			Content: t.Content, Truncated: t.Truncated, ContentHash: t.ContentHash,
			AgentType: t.AgentType, BodyMissing: missing[i]}
	}
	writeJSON(w, http.StatusOK, out)
}

// handleTranscriptSearch serves both transcript read modes: with ?q=
// (substring scan) or ?hash=<sha256:...> (the client hashes the secret
// value locally; the plaintext never travels) it is the leak hunt, ?user=
// narrowing by id or username; with NO filter it is the browse view, the
// newest captured turns org-wide (the console Transcripts landing).
// Output answers "who said/leaked what, when".
func (a *App) handleTranscriptSearch(w http.ResponseWriter, r *http.Request) {
	q := store.ConversationSearch{
		Substring:   r.URL.Query().Get("q"),
		ContentHash: r.URL.Query().Get("hash"),
	}
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 1000 {
			q.Limit = n
		}
	}
	if user := r.URL.Query().Get("user"); user != "" {
		q.UserID = user
		if u, err := a.store.Users().GetByUsername(r.Context(), user); err == nil {
			q.UserID = u.ID
		} else if !errors.Is(err, store.ErrNotFound) {
			a.fail(w, r, http.StatusInternalServerError, "user lookup failed", err)
			return
		}
	}
	var (
		hits []store.ConversationTurn
		err  error
	)
	if q.Substring == "" && q.ContentHash == "" {
		hits, err = a.store.Conversations().ListRecent(r.Context(), q.UserID, q.Limit)
	} else {
		hits, err = a.store.Conversations().Search(r.Context(), q)
	}
	if err != nil {
		a.fail(w, r, http.StatusInternalServerError, "search failed", err)
		return
	}
	missing := a.resolveBodies(r.Context(), hits)
	usernames := map[string]string{}
	out := make([]turnPayload, len(hits))
	for i, t := range hits {
		name, ok := usernames[t.UserID]
		if !ok {
			if u, err := a.store.Users().GetByID(r.Context(), t.UserID); err == nil {
				name = u.Username
			}
			usernames[t.UserID] = name
		}
		out[i] = turnPayload{At: t.At, Kind: t.Kind, Mode: t.Mode, Content: t.Content,
			Truncated: t.Truncated, ContentHash: t.ContentHash, AgentType: t.AgentType,
			BodyMissing: missing[i], SessionID: t.SessionID, UserID: t.UserID, Username: name}
	}
	writeJSON(w, http.StatusOK, out)
}
