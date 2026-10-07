package server

import (
	"context"
	"encoding/base64"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/strazahq/straza/internal/store"
)

// historyEnvelope is the paginated shape of GET /v1/approver/history:
// an {items, next_cursor} object.
type historyEnvelope struct {
	Items      []approverRow `json:"items"`
	NextCursor string        `json:"next_cursor"`
}

// seedHistoryRows inserts n resolved approvals owned by ownerID (so the bound
// user sees them as "own"), oldest-first, and returns their ids in insertion
// order (ascending id ⇒ newest last).
func seedHistoryRows(t *testing.T, app *App, ownerID string, n int) []string {
	t.Helper()
	ctx := context.Background()
	base := time.Now().UTC().Add(-time.Hour).Truncate(time.Microsecond)
	ids := make([]string, 0, n)
	for i := 0; i < n; i++ {
		a, err := app.store.Approvals().Insert(ctx, store.Approval{
			SessionID: "s", UserID: ownerID, Username: "kim", RuleID: "r-1", SetName: "g",
			ArgvHash: "h" + strconv.Itoa(i), Lane: "hook", Summary: "shell.exec: deploy",
			State: "approved", CreatedAt: base.Add(time.Duration(i) * time.Second),
			ExpiresAt: base.Add(time.Hour),
		})
		if err != nil {
			t.Fatalf("seed history row %d: %v", i, err)
		}
		ids = append(ids, a.ID)
	}
	return ids
}

// TestApproverHistoryEnvelopeAndPaging drives the paginated feed over HTTP: the
// response is an {items, next_cursor} envelope; the default limit is 50; an
// over-max limit clamps to 200; and paging with the opaque cursor walks the
// whole feed newest-first with no overlap or gap, ending on an empty next_cursor.
func TestApproverHistoryEnvelopeAndPaging(t *testing.T) {
	t.Parallel()
	app, base := testApp(t)
	kim := seedIdentity(t, app)
	grantAdmin(t, app, kim.ID)
	adminTok, _ := checkinToken(t, app, base)
	_, deviceTok, _ := enrollApproverDevice(t, base, adminTok)

	const n = 205
	ids := seedHistoryRows(t, app, kim.ID, n) // ascending id; newest last

	// Default limit = 50, and the envelope carries an opaque continuation token.
	var page historyEnvelope
	if code := adminReq(t, "GET", base+"/v1/approver/history", deviceTok, nil, &page); code != http.StatusOK {
		t.Fatalf("history default = %d", code)
	}
	if len(page.Items) != 50 {
		t.Errorf("default page len = %d, want the default 50", len(page.Items))
	}
	if page.NextCursor == "" {
		t.Fatal("next_cursor must be non-empty with 205 rows and a 50-row page")
	}
	// The cursor is opaque base64url of "1\x00<id>": a token, not a raw id.
	raw, err := base64.RawURLEncoding.DecodeString(page.NextCursor)
	if err != nil || !strings.HasPrefix(string(raw), "1\x00") {
		t.Errorf("next_cursor is not the opaque 1\\x00<id> token: %q (%v)", page.NextCursor, err)
	}
	if page.Items[0].ID != ids[n-1] {
		t.Errorf("newest item = %s, want %s", page.Items[0].ID, ids[n-1])
	}

	// Over-max limit clamps to 200 (not rejected).
	var clamped historyEnvelope
	if code := adminReq(t, "GET", base+"/v1/approver/history?limit=9999", deviceTok, nil, &clamped); code != http.StatusOK {
		t.Fatalf("history limit=9999 = %d", code)
	}
	if len(clamped.Items) != 200 {
		t.Errorf("over-max page len = %d, want clamp to 200", len(clamped.Items))
	}

	// Page through with limit=50; collect ids; assert full newest-first coverage.
	var got []string
	cursor := ""
	for i := 0; i < 20 && (i == 0 || cursor != ""); i++ {
		url := base + "/v1/approver/history?limit=50"
		if cursor != "" {
			url += "&cursor=" + cursor
		}
		var p historyEnvelope
		if code := adminReq(t, "GET", url, deviceTok, nil, &p); code != http.StatusOK {
			t.Fatalf("history page %d = %d", i, code)
		}
		if len(p.Items) > 50 {
			t.Fatalf("page %d over limit: %d", i, len(p.Items))
		}
		for _, it := range p.Items {
			got = append(got, it.ID)
		}
		cursor = p.NextCursor
		if cursor == "" {
			break
		}
	}
	if len(got) != n {
		t.Fatalf("paged %d rows, want %d (no gap, no overlap)", len(got), n)
	}
	for i := 0; i < n; i++ {
		if want := ids[n-1-i]; got[i] != want {
			t.Errorf("row %d = %s, want %s (newest-first)", i, got[i], want)
			break
		}
	}
}

// TestApproverHistoryInvalidCursor pins the fail-closed cursor handling: garbage,
// a wrong version tag, and an empty id all answer 400 with code invalid_cursor
// (the app drops the cursor and re-fetches from the top, NOT a 401/revocation).
// A structurally-valid cursor for an empty window is a normal 200, not a 400.
func TestApproverHistoryInvalidCursor(t *testing.T) {
	t.Parallel()
	app, base := testApp(t)
	kim := seedIdentity(t, app)
	grantAdmin(t, app, kim.ID)
	adminTok, _ := checkinToken(t, app, base)
	_, deviceTok, _ := enrollApproverDevice(t, base, adminTok)

	enc := func(s string) string { return base64.RawURLEncoding.EncodeToString([]byte(s)) }

	for _, tc := range []struct {
		name, cursor string
	}{
		{"garbage", "not..valid..base64!!"},
		{"wrong version", enc("2\x00" + "0192abcd")},
		{"empty id", enc("1\x00")},
	} {
		var body struct {
			Error string `json:"error"`
			Code  string `json:"code"`
		}
		code := adminReq(t, "GET", base+"/v1/approver/history?cursor="+tc.cursor, deviceTok, nil, &body)
		if code != http.StatusBadRequest || body.Code != "invalid_cursor" {
			t.Errorf("%s cursor: got %d/%q, want 400/invalid_cursor", tc.name, code, body.Code)
		}
		if body.Error == "" {
			t.Errorf("%s cursor: error message must be populated alongside the code", tc.name)
		}
	}

	// A well-formed cursor addressing an empty window is a 200 with no items:
	// structural validity is enough; the id need not exist.
	var ok historyEnvelope
	if code := adminReq(t, "GET", base+"/v1/approver/history?cursor="+enc("1\x0000000000-0000-0000-0000-000000000000"),
		deviceTok, nil, &ok); code != http.StatusOK {
		t.Errorf("valid cursor for an empty window = %d, want 200", code)
	}
	if len(ok.Items) != 0 || ok.NextCursor != "" {
		t.Errorf("empty window = %+v, want no items and empty next_cursor", ok)
	}
}
