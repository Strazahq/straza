package server

import (
	"net/http"
	"slices"

	"github.com/strazahq/straza/internal/drafts"
	"github.com/strazahq/straza/internal/store"
)

// checkOpen checks the open draft d, whose row is row, whose items are rows
// and whose revisions are revs, against live state read now, and answers
// the check as the reader c reads it, waived in c's view by waiveFor. An
// item not stamped yet is stamped by this read first, with the check the
// checker stores: waived in the view of the author of the last revision,
// with intake again for a draft of the straza-app door only, so the stored
// check does not depend on who read the draft first. The stamp that lands
// writes one draft.check. It answers the request itself and reports
// false when live state cannot be read.
func (a *App) checkOpen(w http.ResponseWriter, r *http.Request, c draftCaller, row store.DraftRow, d drafts.Draft, rows []store.DraftItemRow,
	revs []store.DraftRevisionRow) (checkedDraft, bool) {
	world, in, ok := a.readLive(w, r, d)
	if !ok {
		return checkedDraft{}, false
	}
	unstamped := slices.ContainsFunc(rows, func(it store.DraftItemRow) bool { return it.BaseOp == "" })
	base, ok := a.stampAndCheck(w, r, world, in, d, slices.Clone(rows))
	if !ok {
		return base, false
	}
	cd := waiveFor(c, base, lastIntake(base.d, revs))
	if !unstamped || len(revs) == 0 {
		return cd, true
	}
	ctx := r.Context()
	author := principalOf(revs[len(revs)-1].Author)
	ac, err := a.proposerCaller(ctx, author)
	if err != nil {
		a.log.Warn("draft read: the stamp of the read was not stored, and the next read stamps again", "draft", d.ID, "err", err)
		return cd, true
	}
	var fs []drafts.Finding
	if d.Door == drafts.DoorAgent {
		fs = intakeOf(base.d, author)
	}
	stamp := waiveFor(ac, base, fs)
	landed, err := a.store.Drafts().Stamp(ctx, row.ID, row.Revision, stamp.rows, stampOf(stamp.d, stamp.v, stamp.in.Now))
	switch {
	case err != nil:
		a.log.Warn("draft read: the stamp of the read was not stored, and the next read stamps again", "draft", d.ID, "err", err)
	case landed:
		a.recordCheck(ctx, d.ID, stamp.v)
	}
	return cd, true
}

// lastIntake answers the intake findings of the stored draft d, whose
// revisions are revs, for the author of the last revision, as that
// revision's door met them. A file the apps directory door could not read
// holds no item, and its refusal stands for the intake that door never
// ran, so it answers none, and so does a draft with no revision.
func lastIntake(d drafts.Draft, revs []store.DraftRevisionRow) []drafts.Finding {
	if len(revs) == 0 || d.Refusal != "" {
		return nil
	}
	return intakeOf(d, principalOf(revs[len(revs)-1].Author))
}

// waiveFor is cd, a check of a stored draft, waived in the read view of c
// as every door and the checker waive it: the intake findings fs that live
// state already holds in c's view join the warnings, the rest join the
// refusals, and a live object c may not read is waived nothing, so the
// answer tells c nothing its read routes would not show. A
// refusal equal to one already answered is left out, because the check's
// agent rules make some of intake's findings again. Equal means the whole
// finding: two refusals of one code on one object, such as two missing
// roles a Role implies, share a key and are both answered. cd's own lists
// are left as they are, so one check can be waived for two callers.
func waiveFor(c draftCaller, cd checkedDraft, fs []drafts.Finding) checkedDraft {
	cd.v.Refused, cd.v.Warnings = slices.Clone(cd.v.Refused), slices.Clone(cd.v.Warnings)
	refused, waived := c.readerWaived(cd.w, cd.d, fs)
	cd.v = c.readerVerdict(cd.w, cd.d, cd.v, waived)
	out := []drafts.Finding{}
	for _, f := range append(cd.v.Refused, refused...) {
		if !slices.Contains(out, f) {
			out = append(out, f)
		}
	}
	cd.v.Refused = sortFindings(out)
	return cd
}
