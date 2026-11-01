package spool

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"
)

// hangingUploader holds every upload until release closes, and says when
// the first one arrived.
type hangingUploader struct {
	entered chan struct{}
	release chan struct{}
}

func (u *hangingUploader) AuditBatch(_ context.Context, _ string, events []json.RawMessage) (int, error) {
	select {
	case u.entered <- struct{}{}:
	default:
	}
	<-u.release
	return len(events), nil
}

// TestSpoolAppendAndPendingDuringUpload pins that an append and a count of
// the same spool answer at once while a drain's upload hangs, as it does
// when strazad is blackholed, and that the record appended meanwhile is
// kept for the next drain.
func TestSpoolAppendAndPendingDuringUpload(t *testing.T) {
	s := NewSpool(filepath.Join(t.TempDir(), "audit-spool.jsonl"))
	for range 2 {
		if err := s.AppendCE("straza.audit.tool", map[string]any{"effect": "allow"}); err != nil {
			t.Fatal(err)
		}
	}
	u := &hangingUploader{entered: make(chan struct{}, 1), release: make(chan struct{})}
	drained := make(chan int, 1)
	go func() {
		n, _ := s.Drain(context.Background(), u, "tok")
		drained <- n
	}()
	<-u.entered
	steps := []struct {
		name string
		run  func() error
	}{
		{"append", func() error { return s.AppendCE("straza.audit.tool", map[string]any{"effect": "deny"}) }},
		{"pending", func() error {
			n, err := s.Pending()
			if err == nil && n != 3 {
				t.Errorf("pending %d during the upload, want 3", n)
			}
			return err
		}},
	}
	for _, st := range steps {
		done := make(chan error, 1)
		go func() { done <- st.run() }()
		select {
		case err := <-done:
			if err != nil {
				t.Fatalf("%s: %v", st.name, err)
			}
		case <-time.After(2 * time.Second):
			close(u.release)
			t.Fatalf("%s waited for the hung upload", st.name)
		}
	}
	close(u.release)
	if n := <-drained; n != 2 {
		t.Fatalf("the drain uploaded %d, want the 2 rotated records", n)
	}
	if n, err := s.Pending(); err != nil || n != 1 {
		t.Fatalf("after the drain: %d pending, %v, want the record appended during it", n, err)
	}
}
