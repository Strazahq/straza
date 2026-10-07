package store

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

// TestConversationPreviewCutKeepsValidUTF8 pins the preview cut: the
// 200-byte cut may land inside a multibyte character, and the dangling lead
// bytes must go, because Postgres refuses invalid UTF-8 in a TEXT column and
// the whole batch would roll back forever.
func TestConversationPreviewCutKeepsValidUTF8(t *testing.T) {
	const euro = "€" // the euro sign is three bytes, lead byte 0xE2
	cases := []struct {
		name, content, want string
	}{
		{"short", "hi", "hi"},
		{"ascii cut", strings.Repeat("a", 250), strings.Repeat("a", 200)},
		{"cut splits a three-byte character", strings.Repeat("a", 199) + euro + strings.Repeat("b", 50), strings.Repeat("a", 199)},
		{"cut lands after a whole character", strings.Repeat("a", 197) + euro + strings.Repeat("b", 50), strings.Repeat("a", 197) + euro},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := previewOf(tc.content)
			if got != tc.want || !utf8.ValidString(got) {
				t.Fatalf("previewOf = %q (valid utf8 %v), want %q", got, utf8.ValidString(got), tc.want)
			}
		})
	}

	// The same turn lands on both dialects. Postgres would refuse a cut
	// that keeps the lead bytes with SQLSTATE 22021 on the summary upsert.
	forEachStore(t, func(t *testing.T, s Store) {
		ctx := context.Background()
		turn := ConversationTurn{
			CEID: "utf8-1", SessionID: "s1", UserID: "u1", Kind: "reply",
			Content: strings.Repeat("a", 199) + euro + strings.Repeat("b", 50),
			At:      time.Now().UTC(),
		}
		if _, err := s.Conversations().InsertBatch(ctx, []ConversationTurn{turn}); err != nil {
			t.Fatalf("InsertBatch: %v", err)
		}
		sums, err := s.Conversations().ListConversations(ctx, 10)
		if err != nil || len(sums) != 1 {
			t.Fatalf("ListConversations = %d, %v", len(sums), err)
		}
		if !utf8.ValidString(sums[0].Preview) || len(sums[0].Preview) > previewLen {
			t.Fatalf("preview %q is not a valid cut", sums[0].Preview)
		}
	})
}

// TestMapErrInvalidData pins the driver error mapping the spine relies on to
// tell a row the database will never take from a transient failure.
func TestMapErrInvalidData(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want error
	}{
		{"pgx data exception", errors.New(`ERROR: invalid byte sequence for encoding "UTF8": 0xe2 (SQLSTATE 22021)`), ErrInvalidData},
		{"pgx unique violation", errors.New("ERROR: duplicate key value (SQLSTATE 23505)"), ErrConflict},
		{"sqlite unique violation", errors.New("constraint failed: UNIQUE constraint failed: users.id"), ErrConflict},
		{"pgx connection failure stays transient", errors.New("connection reset (SQLSTATE 08006)"), nil},
		{"nil", nil, nil},
	}
	s := &sqlStore{}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := s.mapErr(tc.err)
			if tc.want == nil {
				if got != tc.err {
					t.Fatalf("mapErr(%v) = %v, want it passed through unmapped", tc.err, got)
				}
				return
			}
			if !errors.Is(got, tc.want) {
				t.Fatalf("mapErr(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}
