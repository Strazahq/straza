package store

import (
	"context"
	"errors"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestConversationTurns(t *testing.T) {
	forEachStore(t, func(t *testing.T, s Store) {
		ctx := context.Background()
		base := time.Now().UTC().Add(-time.Hour)

		mk := func(ceID, session, kind, content string, at time.Time) ConversationTurn {
			turn, err := s.Conversations().Insert(ctx, ConversationTurn{
				CEID: ceID, SessionID: session, UserID: "u-bob", Kind: kind,
				Mode: "verbatim", Content: content, ContentHash: "sha256:" + ceID, At: at,
			})
			if err != nil {
				t.Fatalf("Insert %s: %v", ceID, err)
			}
			return turn
		}
		mk("ce-1", "s-1", "prompt", "deploy staging and tail the logs", base)
		mk("ce-2", "s-1", "reply", "Deploying now. The AWS key is AKIAIOSFODNN7EXAMPLE", base.Add(time.Minute))
		mk("ce-3", "s-2", "prompt", "unrelated session", base.Add(2*time.Minute))

		// Dedupe by CE id: at-least-once redelivery must not double-store.
		if _, err := s.Conversations().Insert(ctx, ConversationTurn{
			CEID: "ce-1", SessionID: "s-1", Kind: "prompt", Content: "dup",
		}); !errors.Is(err, ErrConflict) {
			t.Errorf("duplicate ce_id: want ErrConflict, got %v", err)
		}

		// Per-session transcript, ordered.
		turns, err := s.Conversations().ListBySession(ctx, "s-1", 0)
		if err != nil || len(turns) != 2 {
			t.Fatalf("ListBySession = %d turns, %v", len(turns), err)
		}
		if turns[0].Kind != "prompt" || turns[1].Kind != "reply" {
			t.Errorf("order wrong: %s then %s", turns[0].Kind, turns[1].Kind)
		}
		if turns[0].Truncated {
			t.Error("truncated should round-trip false")
		}

		// Default browse (console Transcripts landing view): newest first,
		// bounded, no filter required; optional user scoping.
		recent, err := s.Conversations().ListRecent(ctx, "", 2)
		if err != nil || len(recent) != 2 {
			t.Fatalf("ListRecent = %d turns, %v", len(recent), err)
		}
		if recent[0].CEID != "ce-3" || recent[1].CEID != "ce-2" {
			t.Errorf("ListRecent order = %s, %s; want newest first (ce-3, ce-2)", recent[0].CEID, recent[1].CEID)
		}
		if all, err := s.Conversations().ListRecent(ctx, "", 0); err != nil || len(all) != 3 {
			t.Errorf("ListRecent default limit = %d turns, %v; want all 3", len(all), err)
		}
		if mine, err := s.Conversations().ListRecent(ctx, "u-bob", 0); err != nil || len(mine) != 3 {
			t.Errorf("ListRecent user-scoped = %d turns, %v", len(mine), err)
		}
		if none, err := s.Conversations().ListRecent(ctx, "u-nobody", 0); err != nil || len(none) != 0 {
			t.Errorf("ListRecent unknown user = %d turns, %v; want none", len(none), err)
		}

		// Conversation inbox (console Transcripts landing): one row per
		// captured session, newest activity first, with turn count and a
		// preview of the latest turn.
		convs, err := s.Conversations().ListConversations(ctx, 0)
		if err != nil || len(convs) != 2 {
			t.Fatalf("ListConversations = %d rows, %v", len(convs), err)
		}
		if convs[0].SessionID != "s-2" || convs[1].SessionID != "s-1" {
			t.Errorf("conversation order = %s, %s; want newest session first (s-2, s-1)", convs[0].SessionID, convs[1].SessionID)
		}
		if convs[1].Turns != 2 || convs[0].Turns != 1 {
			t.Errorf("turn counts = %d/%d, want s-1=2 s-2=1", convs[1].Turns, convs[0].Turns)
		}
		if convs[1].Preview == "" || !strings.Contains(convs[1].Preview, "AKIAIOSFODNN7") {
			t.Errorf("s-1 preview = %q, want the NEWEST turn's content", convs[1].Preview)
		}
		if convs[0].UserID != "u-bob" || convs[1].LastAt.Before(convs[1].FirstAt) {
			t.Errorf("conversation row fields wrong: %+v", convs[0])
		}
		if one, err := s.Conversations().ListConversations(ctx, 1); err != nil || len(one) != 1 || one[0].SessionID != "s-2" {
			t.Errorf("bounded ListConversations = %+v, %v", one, err)
		}

		// Leak hunt: substring scan finds the credential-bearing reply.
		hits, err := s.Conversations().Search(ctx, ConversationSearch{Substring: "AKIAIOSFODNN7"})
		if err != nil || len(hits) != 1 || hits[0].CEID != "ce-2" {
			t.Fatalf("substring search = %+v, %v", hits, err)
		}
		// LIKE wildcards in the query are literals, not wildcards.
		if hits, _ := s.Conversations().Search(ctx, ConversationSearch{Substring: "%"}); len(hits) != 0 {
			t.Errorf("wildcard %% must match literally, got %d hits", len(hits))
		}
		// Exact content-hash match (client-hashed value search).
		hits, err = s.Conversations().Search(ctx, ConversationSearch{ContentHash: "sha256:ce-3"})
		if err != nil || len(hits) != 1 || hits[0].SessionID != "s-2" {
			t.Fatalf("hash search = %+v, %v", hits, err)
		}
		// User narrowing composes.
		hits, _ = s.Conversations().Search(ctx, ConversationSearch{Substring: "deploy", UserID: "nobody"})
		if len(hits) != 0 {
			t.Errorf("user filter must exclude, got %d", len(hits))
		}
		// Empty query returns nothing rather than the whole table.
		if hits, _ := s.Conversations().Search(ctx, ConversationSearch{}); len(hits) != 0 {
			t.Errorf("empty search must return nothing, got %d", len(hits))
		}

		// Delegate attribution round-trips (subagent capture: "capture,
		// tagged"): a tagged reply keeps its agent_type through insert,
		// transcript read, and the leak hunt; root-lane turns stay "".
		if _, err := s.Conversations().Insert(ctx, ConversationTurn{
			CEID: "ce-4", SessionID: "s-2", UserID: "u-bob", Kind: "reply",
			Mode: "verbatim", Content: "delegate findings", ContentHash: "sha256:ce-4",
			AgentType: "researcher", At: base.Add(3 * time.Minute),
		}); err != nil {
			t.Fatalf("Insert tagged turn: %v", err)
		}
		tagged, err := s.Conversations().ListBySession(ctx, "s-2", 0)
		if err != nil || len(tagged) != 2 {
			t.Fatalf("ListBySession s-2 = %d turns, %v", len(tagged), err)
		}
		if tagged[0].AgentType != "" || tagged[1].AgentType != "researcher" {
			t.Errorf("agent_type round-trip = %q/%q, want \"\"/researcher", tagged[0].AgentType, tagged[1].AgentType)
		}
		if hits, _ := s.Conversations().Search(ctx, ConversationSearch{Substring: "delegate findings"}); len(hits) != 1 || hits[0].AgentType != "researcher" {
			t.Errorf("search lost the delegate tag: %+v", hits)
		}

		// Retention purge: drop everything before ce-3's timestamp.
		n, err := s.Conversations().PurgeBefore(ctx, base.Add(2*time.Minute))
		if err != nil || n != 2 {
			t.Fatalf("PurgeBefore = %d, %v (want 2)", n, err)
		}
		if left, _ := s.Conversations().ListBySession(ctx, "s-1", 0); len(left) != 0 {
			t.Errorf("purged session still has %d turns", len(left))
		}
		if left, _ := s.Conversations().ListBySession(ctx, "s-2", 0); len(left) != 2 {
			t.Errorf("s-2 should survive the purge, has %d", len(left))
		}
	})
}

// TestConversationListBySessionOrder pins the transcript order: the order in
// time, then the id for turns of one instant, with a limit cutting the
// latest. Every case inserts first a turn that must read later, and gives it
// the lower id where the times differ, so neither the insert order nor the
// id can put the turns right. sqlite keeps a time as text without trailing
// zeros, so in the two fraction cases the text of the earlier time is the
// start of the text of the later one.
func TestConversationListBySessionOrder(t *testing.T) {
	second := time.Date(2026, 3, 1, 10, 0, 0, 0, time.UTC)
	type turn struct {
		id, content string
		at          time.Duration // offset from second
	}
	cases := []struct {
		name   string
		insert []turn
		limit  int
		want   []string
	}{
		{
			name:   "a limit keeps the earliest turns",
			insert: []turn{{"1", "late", 2 * time.Second}, {"2", "early", 0}, {"3", "middle", time.Second}},
			limit:  2,
			want:   []string{"early", "middle"},
		},
		{
			name:   "a fraction that ends in zero is earlier than the next microsecond",
			insert: []turn{{"1", "late", 123451 * time.Microsecond}, {"2", "early", 123450 * time.Microsecond}},
			want:   []string{"early", "late"},
		},
		{
			name:   "a whole second is earlier than a fraction of it",
			insert: []turn{{"1", "late", 500 * time.Millisecond}, {"2", "early", 0}},
			want:   []string{"early", "late"},
		},
		{
			name:   "turns of one instant read in id order",
			insert: []turn{{"2", "second", 123450 * time.Microsecond}, {"1", "first", 123450 * time.Microsecond}},
			want:   []string{"first", "second"},
		},
	}
	forEachStore(t, func(t *testing.T, s Store) {
		ctx := context.Background()
		sq := s.(*sqlStore)
		for i, tc := range cases {
			session := "s-order-" + strconv.Itoa(i)
			for _, in := range tc.insert {
				// The store assigns ids in insert order, so a row with a
				// chosen id goes in with its columns spelled out.
				id := session + "-" + in.id
				if _, err := sq.exec(ctx, `INSERT INTO conversation_turns (id, ce_id, session_id, kind, content, at)
					VALUES ($1, $2, $3, $4, $5, $6)`, id, id, session, "prompt", in.content, sq.tArg(second.Add(in.at))); err != nil {
					t.Fatalf("%s: insert %s: %v", tc.name, in.content, err)
				}
			}
			turns, err := s.Conversations().ListBySession(ctx, session, tc.limit)
			if err != nil {
				t.Fatalf("%s: ListBySession: %v", tc.name, err)
			}
			var got []string
			for _, row := range turns {
				got = append(got, row.Content)
			}
			if !slices.Equal(got, tc.want) {
				t.Errorf("%s: order = %v, want %v", tc.name, got, tc.want)
			}
		}
	})
}
