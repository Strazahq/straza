package agentguard

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/strazahq/straza/internal/agentguard/localapi"
)

// TestDoctorSpoolChecks drives the doctor's audit rows through the spool
// states the Straza background service reports. No drop means no
// audit-dropped line (loss is the exception, not a routine row), and a drop
// is a loud FAIL naming the count and the marker an administrator removes. A backlog warns with its size, the last
// successful upload and the last upload error.
func TestDoctorSpoolChecks(t *testing.T) {
	now := time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name        string
		spool       localapi.SpoolStatus
		wantSpool   string
		spoolDetail []string
		wantDropped string // "" = no audit-dropped line
	}{
		{name: "empty and never uploaded",
			wantSpool: checkOK, spoolDetail: []string{"no pending audit events", "no successful upload recorded yet"}},
		{name: "empty after an upload",
			spool:     localapi.SpoolStatus{LastDrain: now.Add(-90 * time.Second)},
			wantSpool: checkOK, spoolDetail: []string{"last successful upload 1m30s ago"}},
		{name: "backlog with a failing upload",
			spool:     localapi.SpoolStatus{Pending: 3, LastDrain: now.Add(-time.Hour), LastError: "the server answered HTTP 503"},
			wantSpool: checkWarn, spoolDetail: []string{"3 audit event(s) awaiting upload", "last successful upload 1h0m0s ago", "The last upload failed: the server answered HTTP 503"}},
		{name: "dropped records fail loudly",
			spool:     localapi.SpoolStatus{Dropped: 2},
			wantSpool: checkOK, wantDropped: checkFail},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := spoolCheck(tt.spool, now)
			if c.Status != tt.wantSpool {
				t.Fatalf("audit-spool = %+v, want %s", c, tt.wantSpool)
			}
			if c.Status != checkOK && c.Hint == "" {
				t.Error("non-ok check without a hint: a diagnosis without a next step is noise")
			}
			for _, want := range tt.spoolDetail {
				if !strings.Contains(c.Detail, want) {
					t.Errorf("audit-spool detail %q missing %q", c.Detail, want)
				}
			}
			d := droppedAuditCheck(tt.spool, "/var/lib/straza-service/u/1000/audit-dropped")
			switch {
			case tt.wantDropped == "" && d != nil:
				t.Fatalf("audit-dropped line with nothing dropped: %+v", d)
			case tt.wantDropped != "" && (d == nil || d.Status != tt.wantDropped || d.Hint == "" ||
				!strings.Contains(d.Detail, "2 record(s) never reached the server") ||
				!strings.Contains(d.Hint, "an administrator removes /var/lib/straza-service/u/1000/audit-dropped")):
				t.Fatalf("audit-dropped = %+v, want a %s naming the 2 records and the marker to remove", d, tt.wantDropped)
			}
		})
	}
}

// TestDroppedMarkerPath pins where the hint sends an administrator: the
// caller's directory in the state directory of the service this client
// talks to, which STRAZA_HOME picks.
func TestDroppedMarkerPath(t *testing.T) {
	t.Setenv("STRAZA_HOME", "/home/kim/.straza-ci")
	want := filepath.Join("/home/kim/.straza-ci", "service", "u", strconv.Itoa(os.Getuid()), "audit-dropped")
	if got := droppedMarkerPath(); got != want {
		t.Errorf("droppedMarkerPath() = %q, want %q", got, want)
	}
}
