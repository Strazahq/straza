package server

import (
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/strazahq/straza/internal/drafts"
	"github.com/strazahq/straza/internal/store"
)

// readDecided reads, for the draft d that is not open, live state with the
// fingerprints of d's objects, for the read standing cuts and the live
// block, and the check storedCheck finds for row. It runs no check, since d
// cannot change: a check now would name fixes d cannot take, and read what
// a published draft's own publish changed as stale. It answers 503 itself
// and reports false when live state cannot be read.
func (a *App) readDecided(w http.ResponseWriter, r *http.Request, row store.DraftRow, d drafts.Draft, rows []store.DraftItemRow) (checkedDraft, *checksPayload, bool) {
	world, err := a.liveWorld(r.Context())
	if err == nil {
		world.Fingerprints, err = a.liveFingerprints(r.Context(), world, d)
	}
	if err != nil {
		a.fail(w, r, http.StatusServiceUnavailable, worldReadRefusal(err), err)
		return checkedDraft{}, nil, false
	}
	v, checks := storedCheck(row)
	return checkedDraft{w: world, d: d, rows: rows, v: v}, checks, true
}

// storedCheck is the check the server stored for the draft row's current
// revision, as a draft that is not open answers it: a verdict with no line,
// with that check's snapshot and time, and its counts. For a published
// draft it is the check its publish ran, which the publish stores. A
// revision the server stored no check of, such as a saved edit that a
// direct route published before the checker reached it, answers neither
// snapshot, time nor counts.
func storedCheck(row store.DraftRow) (drafts.Verdict, *checksPayload) {
	none := []drafts.Finding{}
	v := drafts.Verdict{Draft: strconv.FormatInt(row.ID, 10), Revision: row.Revision, Refused: none, Risks: none, Warnings: none,
		Unchecked: none, Passed: none, Info: none, Gains: []drafts.Gain{}, Needs: []drafts.Need{}}
	checks := lastChecks(row)
	if checks == nil || checks.Revision != row.Revision {
		return v, nil
	}
	v.Snapshot, v.CheckedAt = row.CheckedSnapshot, checks.CheckedAt
	return v, checks
}

// lastChecks is the counts the draft row's last stamp stored, the revision
// they belong to and when the check ran, or nil before the first stamp.
func lastChecks(row store.DraftRow) *checksPayload {
	var counts checksPayload
	if row.CheckedRevision == 0 || row.CheckedAt == nil || json.Unmarshal([]byte(row.CheckCounts), &counts) != nil {
		return nil
	}
	counts.Revision, counts.CheckedAt = row.CheckedRevision, rfc3339(*row.CheckedAt)
	return &counts
}
