package agentguard

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/strazahq/straza/internal/agentguard/spool"
)

func markerStr(s string) *string { return &s }

// TestDroppedAuditCheck drives the doctor surface for the audit-dropped
// marker through its states. enforceSpoolCap and dropOversize discard audit
// records and note it only in the marker file, so doctor is what makes the
// loss visible. No marker = no line (loss is the exception, not a routine
// row), a populated marker = a loud FAIL naming the totals, an
// EXISTING-but-empty marker = warn (its only creator is noteDropped, so an
// empty one means a drop happened and even the note about it failed, and
// unknown loss is loss, never silence), a stale compaction claim is read and
// summed (a crash between rename and fold must not hide the ledger), and the
// documented acknowledgement, deleting the files, clears it.
func TestDroppedAuditCheck(t *testing.T) {
	ts := "2026-07-30T04:05:06Z"
	tsOld := "2026-07-29T10:00:00Z"
	cases := []struct {
		name       string
		marker     *string // nil = no marker file
		claim      *string // nil = no stale .compacting claim
		markerDir  bool    // marker path is a directory: unreadable
		wantNil    bool
		wantStatus string
		wantDetail []string
		wantHint   []string
	}{
		{name: "no marker means no check line", wantNil: true},
		{name: "empty marker warns as unknown loss",
			marker:     markerStr(""),
			wantStatus: "warn",
			wantDetail: []string{"no readable drop record", "audit-dropped"},
			wantHint:   []string{"unknown amount"}},
		{name: "cap drops fail loudly with totals",
			marker: markerStr(tsOld + " dropped 2 parked file(s) over the 33554432-byte spool cap\n" +
				ts + " dropped 1 oversize record(s)\n"),
			wantStatus: "fail",
			wantDetail: []string{"DROPPED", "2 parked spool file(s)", "1 oversize record(s)", "2 drop event(s)", ts},
			wantHint:   []string{"audit-dropped", "delete", "never reached the server"}},
		{name: "foreign content still fails with the line shown",
			marker:     markerStr("something ate the spool\n"),
			wantStatus: "fail",
			wantDetail: []string{"DROPPED", "1 drop event(s)", "something ate the spool"}},
		{name: "stale compaction claim alone is still loss",
			claim:      markerStr(tsOld + " dropped 2 parked file(s) over the 33554432-byte spool cap\n"),
			wantStatus: "fail",
			wantDetail: []string{"2 parked spool file(s)", "1 drop event(s)", tsOld}},
		{name: "claim and marker sum",
			claim:      markerStr(tsOld + " dropped 2 parked file(s) over the 33554432-byte spool cap\n"),
			marker:     markerStr(ts + " dropped 1 oversize record(s)\n"),
			wantStatus: "fail",
			wantDetail: []string{"2 parked spool file(s)", "1 oversize record(s)", "2 drop event(s)", ts},
			wantHint:   []string{"compacting"}},
		{name: "unreadable marker warns with the path",
			markerDir:  true,
			wantStatus: "warn",
			wantHint:   []string{"audit-dropped"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store := &Store{root: t.TempDir()}
			dir := filepath.Dir(store.SpoolPath())
			if err := os.MkdirAll(dir, 0o700); err != nil {
				t.Fatal(err)
			}
			marker := filepath.Join(dir, spool.DroppedMarkerName)
			if tc.markerDir {
				if err := os.Mkdir(marker, 0o700); err != nil {
					t.Fatal(err)
				}
			}
			if tc.marker != nil {
				if err := os.WriteFile(marker, []byte(*tc.marker), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			if tc.claim != nil {
				if err := os.WriteFile(marker+spool.DroppedCompactingSuffix+"crashed",
					[]byte(*tc.claim), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			c := droppedAuditCheck(store)
			if tc.wantNil {
				if c != nil {
					t.Fatalf("want no check, got %+v", c)
				}
				return
			}
			if c == nil {
				t.Fatal("want a check, got none")
			}
			if c.Status != tc.wantStatus {
				t.Fatalf("status = %s (%s / %s), want %s", c.Status, c.Detail, c.Hint, tc.wantStatus)
			}
			if c.Hint == "" {
				t.Fatal("non-ok check without a hint: a diagnosis without a next step is noise")
			}
			for _, want := range tc.wantDetail {
				if !strings.Contains(c.Detail, want) {
					t.Errorf("detail %q missing %q", c.Detail, want)
				}
			}
			for _, want := range tc.wantHint {
				if !strings.Contains(c.Hint, want) {
					t.Errorf("hint %q missing %q", c.Hint, want)
				}
			}
		})
	}
}
