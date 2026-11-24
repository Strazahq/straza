package server

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/strazahq/straza/internal/store"
)

// TestPublishedDraftAnswersPassed pins passed on a published draft's
// stored check: the publish stores how many checks passed, and the read of
// the published draft answers the count its open check showed.
func TestPublishedDraftAnswersPassed(t *testing.T) {
	t.Parallel()
	f := newDraftsFixture(t, nil)
	a := f.create(t, f.root, draftRole("zz-pass", "Passes."))
	_, open := f.get(t, f.root, a.Draft.ID)
	want := len(open.Verdict.Passed)
	if want == 0 {
		t.Fatal("the open check passed nothing, so the count cannot tell a stored 0 from a stored count")
	}
	if code, ans := f.publishAll(t, f.root, a.Draft.ID); code != http.StatusOK {
		t.Fatalf("publish = %d %q", code, ans.Error)
	}
	code, raw, _ := adminBytes(t, http.MethodGet, f.base+"/v1/admin/drafts/"+a.Draft.ID, f.root, "", nil)
	if code != http.StatusOK {
		t.Fatalf("read the published draft = %d %s", code, raw)
	}
	var d struct {
		Checks map[string]any `json:"checks"`
	}
	if err := json.Unmarshal(raw, &d); err != nil {
		t.Fatal(err)
	}
	if got, ok := d.Checks["passed"].(float64); !ok || int(got) != want {
		t.Errorf("published draft checks = %v, want passed %d as its open check showed", d.Checks, want)
	}
}

// TestStoredCheckReadsWithAndWithoutPassed pins the read of a decided
// draft's stored counts: a check stored with passed answers it, 0 included,
// and a check stored before passed was counted still reads its other
// counts and leaves passed out of the answer rather than send 0.
func TestStoredCheckReadsWithAndWithoutPassed(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 11, 2, 9, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name, counts, wantJSON string
	}{
		{"stored before passed was counted", `{"refused":0,"risks":1,"warnings":2,"unchecked":0}`,
			`{"refused":0,"risks":1,"warnings":2,"unchecked":0,"revision":3,"checked_at":"2026-11-02T09:00:00Z"}`},
		{"stored with six passed", `{"passed":6,"refused":0,"risks":1,"unchecked":0,"warnings":2}`,
			`{"refused":0,"risks":1,"warnings":2,"unchecked":0,"passed":6,"revision":3,"checked_at":"2026-11-02T09:00:00Z"}`},
		{"stored with none passed", `{"passed":0,"refused":1,"risks":0,"unchecked":0,"warnings":0}`,
			`{"refused":1,"risks":0,"warnings":0,"unchecked":0,"passed":0,"revision":3,"checked_at":"2026-11-02T09:00:00Z"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			row := store.DraftRow{ID: 7, Revision: 3, State: "published", CheckedRevision: 3, CheckedAt: &at, CheckCounts: tc.counts}
			_, checks := storedCheck(row)
			if checks == nil {
				t.Fatal("storedCheck answered no counts")
			}
			got, err := json.Marshal(checks)
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != tc.wantJSON {
				t.Errorf("checks = %s, want %s", got, tc.wantJSON)
			}
			if strings.Contains(tc.counts, "passed") != (checks.Passed != nil) {
				t.Errorf("passed = %v for stored counts %s", checks.Passed, tc.counts)
			}
		})
	}
}
