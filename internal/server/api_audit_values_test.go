package server

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/strazahq/straza/internal/authn"
	"github.com/strazahq/straza/internal/policy"
	"github.com/strazahq/straza/internal/store"
)

// TestAuditBatchBoundsTheValuesItDecodes pins the value budget of one
// upload: its records are decoded in full while their values total at most
// store.MaxRecordValues, and a record past that is stored with its envelope,
// its kept fields and dataOmitted, bound to the uploader's session and user
// like every other record. The upload still answers 200 for all of them,
// and one Warn line names the uploader and the count.
func TestAuditBatchBoundsTheValuesItDecodes(t *testing.T) {
	t.Parallel()
	log, buf := captureLogger()
	app, base := testAppPreRun(t, []func(*App){func(a *App) { a.log = log }})
	user := seedIdentity(t, app)
	token, session := checkinToken(t, app, base)

	// Each of the first two records holds three fifths of the budget, so the
	// first fits and the second does not. Neither is long enough for the
	// store's own byte bound.
	part := make([]int, store.MaxRecordValues*3/5)
	record := func(command string, paths any) map[string]any {
		data := map[string]any{"event": "tool.pre", "tool": "shell.exec", "command": command, "user": "SPOOFED"}
		if paths != nil {
			data["paths"] = paths
		}
		return map[string]any{"type": "straza.audit.tool", "data": data}
	}
	batch := map[string]any{"events": []map[string]any{
		record("values-first", part), record("values-second", part), record("values-third", nil),
	}}
	if code, resp := postJSONAuth(t, base+"/v1/audit/batch", token, batch); code != http.StatusOK || resp["accepted"] != float64(3) {
		t.Fatalf("batch = %d %v, want 200 with all three accepted", code, resp)
	}

	first := waitChainData(t, app, "values-first")
	if paths, _ := first["paths"].([]any); len(paths) != len(part) || first["dataOmitted"] != nil {
		t.Fatalf("the first record kept %d of %d paths and dataOmitted %v, want it whole", len(paths), len(part), first["dataOmitted"])
	}
	second := waitChainData(t, app, "values-second")
	if omitted, _ := second["dataOmitted"].(string); second["paths"] != nil || !strings.Contains(omitted, " bytes, sha256 ") {
		t.Fatalf("the second record is %v, want its data omitted with its size and hash", second)
	}
	if second["session"] != session || second["user"] != user.ID {
		t.Fatalf("the reduced record names session %v and user %v, want the uploader's %s and %s", second["session"], second["user"], session, user.ID)
	}
	if third := waitChainData(t, app, "values-third"); third["dataOmitted"] != nil {
		t.Fatalf("the third record is %v, want it whole", third)
	}
	if n := warnLines(buf, "their upload holds more than", "count=1", "session="+session); n != 1 {
		t.Fatalf("Warn lines about the reduced record = %d, want 1:\n%s", n, buf.String())
	}
}

// TestDecideRecordIsBoundedBeforeTheQueue pins that a decision's record is
// cut in the request that made it, so the one audit writer that every
// decision shares never pays for it: the record in the queue is already
// within store.MaxRecordBytes, for a long command and for many paths.
func TestDecideRecordIsBoundedBeforeTheQueue(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		ev   policy.Event
	}{
		{"a command that escapes to 1.8 MB", policy.Event{Kind: "tool.pre", Tool: policy.ToolShellExec, Command: strings.Repeat("<", 300<<10)}},
		{"many paths", policy.Event{Kind: "tool.pre", Tool: "file.write", Paths: strings.Split(strings.Repeat("a,", 200000), ",")}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			a := &App{audit: &auditSpool{ch: make(chan store.OutboxEvent, 1)}}
			claims := authn.Claims{Subject: "u1", Session: "s1"}
			if !a.auditDecision(context.Background(), claims, DecideRequest{Event: c.ev}, policy.Decision{Effect: policy.EffectAllow}, "snap") {
				t.Fatal("the record was not queued")
			}
			if ev := <-a.audit.ch; len(ev.CE) > store.MaxRecordBytes {
				t.Fatalf("the queued record is %d bytes, want it within %d", len(ev.CE), store.MaxRecordBytes)
			}
		})
	}
}

// TestAuditBatchWarnCountsOnlyStoredOmittedRecords pins that the Warn line
// about omitted data counts only the records that were stored: a record past
// the budget that names a session the uploader may not use is refused, and
// counted with the refusals, not with the stored ones.
func TestAuditBatchWarnCountsOnlyStoredOmittedRecords(t *testing.T) {
	t.Parallel()
	log, buf := captureLogger()
	app, base := testAppPreRun(t, []func(*App){func(a *App) { a.log = log }})
	seedIdentity(t, app)
	token, session := checkinToken(t, app, base)
	part := make([]int, store.MaxRecordValues*3/5)
	record := func(command, named string) map[string]any {
		data := map[string]any{"event": "tool.pre", "tool": "shell.exec", "command": command, "paths": part}
		if named != "" {
			data["session"] = named
		}
		return map[string]any{"data": data}
	}
	batch := map[string]any{"events": []map[string]any{
		record("counted-first", ""), record("counted-refused", "not-a-session-id"), record("counted-omitted", ""),
	}}
	if code, resp := postJSONAuth(t, base+"/v1/audit/batch", token, batch); code != http.StatusOK || resp["accepted"] != float64(2) || resp["refused"] != float64(1) {
		t.Fatalf("batch = %d %v, want 200 with two accepted and one refused", code, resp)
	}
	waitChainData(t, app, "counted-omitted")
	if n := warnLines(buf, "their upload holds more than", "count=1", "session="+session); n != 1 {
		t.Fatalf("Warn lines counting one stored omitted record = %d, want 1:\n%s", n, buf.String())
	}
}
