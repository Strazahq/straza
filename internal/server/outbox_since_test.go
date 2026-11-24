package server

import (
	"context"
	"encoding/json"
	"testing"
)

// outboxSince marks the outbox rows app holds now and answers a reader of
// the rows written after the mark. The reader returns the payloads of the
// rows of subject that satisfy pred and were not in the outbox at the mark,
// in no set order. A test takes the mark before a call and reads the call's
// own records after its answer. The rows are told apart by id, never by
// position, because sqlite orders created_at as text without trailing
// zeros, so a newer row can list before an older one.
func outboxSince(t *testing.T, app *App) func(subject string, pred func(map[string]any) bool) []map[string]any {
	t.Helper()
	marked := map[string]bool{}
	rows, err := app.store.Outbox().ListRecent(context.Background(), 1000)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range rows {
		marked[e.ID] = true
	}
	return func(subject string, pred func(map[string]any) bool) []map[string]any {
		t.Helper()
		rows, err := app.store.Outbox().ListRecent(context.Background(), 1000)
		if err != nil {
			t.Fatal(err)
		}
		var out []map[string]any
		for _, e := range rows {
			if marked[e.ID] || e.Subject != subject {
				continue
			}
			var ce struct {
				Data map[string]any `json:"data"`
			}
			if err := json.Unmarshal([]byte(e.CE), &ce); err != nil {
				t.Fatal(err)
			}
			if pred(ce.Data) {
				out = append(out, ce.Data)
			}
		}
		return out
	}
}
