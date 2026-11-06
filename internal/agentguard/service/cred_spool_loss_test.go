package service

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/strazahq/straza/internal/agentguard/spool"
)

// TestCredSpoolLossReachesStrazad pins the service's half of the loss
// record: a loss ledger with no spooled record behind it still starts a
// drain, the drain sends the loss record to strazad, and status counts every
// lost record until strazad took the record, a refused upload included.
func TestCredSpoolLossReachesStrazad(t *testing.T) {
	ledger := "2026-11-04T10:00:00Z dropped 2 parked file(s) holding 7 record(s) over the 33554432-byte spool cap\n"
	for _, refused := range []bool{false, true} {
		t.Run(map[bool]string{false: "accepted at once", true: "refused, then accepted"}[refused], func(t *testing.T) {
			r := newCredRig(t)
			r.enroll(alice)
			if _, err := r.start(alice, "w1"); err != nil {
				t.Fatal(err)
			}
			a := r.s.account(alice.UID)
			if refused {
				r.srv.with(func() { r.srv.auditFail = http.StatusInternalServerError })
			}
			if err := os.WriteFile(filepath.Join(a.dir, spool.DroppedMarkerName), []byte(ledger), 0o600); err != nil {
				t.Fatal(err)
			}
			if refused {
				a.kickDrain()
				waitFor(t, "the refused report", func() bool { return r.s.Status(alice).Spool.LastError != "" })
				if got := r.s.Status(alice).Spool.Dropped; got != 7 {
					t.Fatalf("status dropped = %d after a refused report, want all 7 records still counted", got)
				}
				r.srv.with(func() { r.srv.auditFail = 0 })
			}
			a.kickDrain()
			waitFor(t, "the report taken and the ledger cleared", func() bool {
				st := r.s.Status(alice).Spool
				return st.Dropped == 0 && st.LastError == "" && !st.LastDrain.IsZero()
			})
			r.srv.with(func() {
				if r.srv.batches != 1 {
					t.Errorf("strazad took %d records, want the one loss record", r.srv.batches)
				}
			})
			sent := 0
			for _, line := range r.sent.carrying("/v1/audit/batch") {
				if strings.Contains(line, `"event":"`+spool.LossEvent+`"`) && strings.Contains(line, `"evictedRecords":7`) {
					sent++
				}
			}
			if sent == 0 || (refused && sent < 2) {
				t.Fatalf("loss record uploads = %d, want one per try", sent)
			}
		})
	}
}

// TestCredRefusedCallReachesStrazad pins the credential half of a refused
// call's note: it lands in the caller's loss ledger, status counts it, and
// the next drain's loss record carries it.
func TestCredRefusedCallReachesStrazad(t *testing.T) {
	r := newCredRig(t)
	r.enroll(alice)
	if _, err := r.start(alice, "w1"); err != nil {
		t.Fatal(err)
	}
	r.srv.with(func() { r.srv.auditFail = http.StatusInternalServerError })
	r.s.NoteRefused(alice, "capture")
	r.s.NoteRefused(alice, "decide")
	waitFor(t, "the refused report", func() bool { return r.s.Status(alice).Spool.LastError != "" })
	if got := r.s.Status(alice).Spool.Dropped; got != 2 {
		t.Fatalf("status dropped = %d, want the 2 refused calls", got)
	}
	r.srv.with(func() { r.srv.auditFail = 0 })
	a := r.s.account(alice.UID)
	waitFor(t, "every note reported", func() bool {
		a.kickDrain()
		st := r.s.Status(alice).Spool
		return st.Dropped == 0 && st.LastError == "" && !st.LastDrain.IsZero()
	})
	// A retry resends a report under the same id, so the sum runs over ids.
	refused := map[string]float64{}
	for _, line := range r.sent.carrying("/v1/audit/batch") {
		var body struct {
			Events []struct {
				ID   string         `json:"id"`
				Data map[string]any `json:"data"`
			} `json:"events"`
		}
		if i := strings.Index(line, `{"events"`); i < 0 || json.Unmarshal([]byte(line[i:]), &body) != nil {
			continue
		}
		for _, ev := range body.Events {
			if ev.Data["event"] == spool.LossEvent {
				refused[ev.ID], _ = ev.Data["refusedCalls"].(float64)
			}
		}
	}
	total := 0.0
	for _, n := range refused {
		total += n
	}
	if total != 2 {
		t.Fatalf("refused calls reported = %v, want the 2 notes", refused)
	}
}
