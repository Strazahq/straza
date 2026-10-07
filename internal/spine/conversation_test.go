package spine

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/strazahq/straza/internal/store"
)

func captureCE(id, content string) []byte {
	ce := map[string]any{
		"id": id, "type": "straza.audit.prompt", "time": time.Now().UTC().Format(time.RFC3339Nano),
		"data": map[string]any{"session": "sess-1", "user": "u1", "content": content},
	}
	b, _ := json.Marshal(ce)
	return b
}

// TestConversationParseCutKeepsValidUTF8 pins the server-side content cap:
// an ASCII cut is byte-exact, and a cut that splits a multibyte character
// drops the dangling lead bytes so the row stays valid UTF-8.
func TestConversationParseCutKeepsValidUTF8(t *testing.T) {
	c := &ConversationConsumer{log: slog.New(slog.NewTextHandler(bytes.NewBuffer(nil), nil))}
	cases := []struct {
		name    string
		content string
		wantLen int
	}{
		{"ascii cut", strings.Repeat("a", ConversationMaxContent+10), ConversationMaxContent},
		{"cut splits a three-byte character", strings.Repeat("a", ConversationMaxContent-1) + "€tail", ConversationMaxContent - 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			turn, ok := c.parse(captureCE("ce-"+tc.name, tc.content))
			if !ok || !turn.Truncated {
				t.Fatalf("parse ok=%v truncated=%v, want a truncated turn", ok, turn.Truncated)
			}
			if len(turn.Content) != tc.wantLen || !utf8.ValidString(turn.Content) || !strings.HasPrefix(tc.content, turn.Content) {
				t.Fatalf("content len=%d valid=%v prefix=%v, want len %d of valid prefix",
					len(turn.Content), utf8.ValidString(turn.Content), strings.HasPrefix(tc.content, turn.Content), tc.wantLen)
			}
		})
	}
}

// refusingStore stands in for a database that refuses one turn on its values
// (Postgres SQLSTATE class 22) while taking every other row, independent of
// the dialect the test runs on.
type refusingStore struct {
	store.Store
	bad string
}

func (r refusingStore) Conversations() store.ConversationRepo {
	return refusingRepo{ConversationRepo: r.Store.Conversations(), bad: r.bad}
}

type refusingRepo struct {
	store.ConversationRepo
	bad string
}

func (r refusingRepo) InsertBatch(ctx context.Context, turns []store.ConversationTurn) (int, error) {
	for _, t := range turns {
		if t.CEID == r.bad {
			return 0, fmt.Errorf("%w: SQLSTATE 22021", store.ErrInvalidData)
		}
	}
	return r.ConversationRepo.InsertBatch(ctx, turns)
}

// TestConversationConsumerDropsRefusedTurn is the poison-batch contract: one
// turn the database will never take is dropped from the read model and
// terminated (one warning, no redelivery), and every turn around it lands.
func TestConversationConsumerDropsRefusedTurn(t *testing.T) {
	bus, ctx := startSpineBus(t)
	st := refusingStore{Store: newRelayStore(t), bad: "cap-bad"}
	for _, id := range []string{"cap-1", "cap-bad", "cap-2"} {
		if err := bus.Publish(ctx, "straza.audit.prompt", captureCE(id, "turn "+id)); err != nil {
			t.Fatalf("publish %s: %v", id, err)
		}
	}

	var logs bytes.Buffer
	cons := NewConversationConsumer(st, bus, slog.New(slog.NewTextHandler(&logs, nil)), nil)
	runCtx, stop := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() { done <- cons.Run(runCtx) }()

	jsCons, err := bus.ConversationConsumer(ctx)
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(20 * time.Second)
	for {
		turns, err := st.Store.Conversations().ListBySession(ctx, "sess-1", 0)
		if err != nil {
			t.Fatal(err)
		}
		info, err := jsCons.Info(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if len(turns) == 2 && info.NumAckPending == 0 && info.NumPending == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("turns=%d ackPending=%d pending=%d, want the two good turns landed and the refused one terminated",
				len(turns), info.NumAckPending, info.NumPending)
		}
		time.Sleep(100 * time.Millisecond)
	}
	stop()
	if err := <-done; err != nil {
		t.Fatalf("Run: %v", err)
	}
	if n := strings.Count(logs.String(), "dropped from transcripts"); n != 1 {
		t.Fatalf("refused-turn warning logged %d times, want exactly once:\n%s", n, logs.String())
	}
	if !strings.Contains(logs.String(), "ce=cap-bad") {
		t.Fatalf("warning does not name the refused CE:\n%s", logs.String())
	}
}
