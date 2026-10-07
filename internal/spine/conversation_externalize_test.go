package spine

import (
	"context"
	"testing"

	"github.com/strazahq/straza/internal/bodystore"
	"github.com/strazahq/straza/internal/store"
)

// TestConversationExternalize pins the body store write path: body BEFORE row
// (the flag is only set after a successful Put), one object per unique hash.
func TestConversationExternalize(t *testing.T) {
	mem := bodystore.NewMemory()
	c := &ConversationConsumer{bodies: mem}
	turns := []store.ConversationTurn{
		{CEID: "e1", SessionID: "s", Kind: "prompt", Content: "same prompt", ContentHash: "sha256:dup"},
		{CEID: "e2", SessionID: "s", Kind: "prompt", Content: "same prompt", ContentHash: "sha256:dup"},
		{CEID: "e3", SessionID: "s", Kind: "reply", Content: "a reply", ContentHash: "sha256:uniq"},
	}
	if err := c.externalize(context.Background(), turns); err != nil {
		t.Fatal(err)
	}
	if mem.Len() != 2 {
		t.Fatalf("stored objects = %d, want 2 (in-batch dedupe by hash)", mem.Len())
	}
	for i, turn := range turns {
		if !turn.BodyExternal {
			t.Fatalf("turn %d not flagged external", i)
		}
	}
	body, err := mem.Get(context.Background(), "sha256:dup")
	if err != nil || string(body) != "same prompt" {
		t.Fatalf("body = %q, %v", body, err)
	}

	// nil store: no-op, nothing flagged.
	inline := &ConversationConsumer{}
	plain := []store.ConversationTurn{{CEID: "e4", Content: "x", ContentHash: "sha256:x"}}
	if err := inline.externalize(context.Background(), plain); err != nil {
		t.Fatal(err)
	}
	if plain[0].BodyExternal {
		t.Fatal("inline consumer flagged a turn external")
	}
}
