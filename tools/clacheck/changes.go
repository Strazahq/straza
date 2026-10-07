package main

import (
	"sort"
	"strings"
	"time"
)

// change is a recorded acceptance on this pull request that was edited or
// deleted after it was recorded.
type change struct {
	Login   string
	URL     string
	Deleted bool
}

// recordChanges is step 2. It stores an edit or a deletion event of an
// acceptance comment, judged by the records as they were before this run and
// by the body before the change, so the edit that made a comment the
// sentence is not filed as a change. Then it compares every recorded
// acceptance with the current comment and stores a difference or an absence
// that no event recorded. None of this undoes an acceptance.
func (r *runner) recordChanges(ev event, pr pullRequest, all []issueComment, before, names map[string]bool, recs map[string]acceptedRecord) ([]change, error) {
	dir := r.eventsDir(pr.Number)
	if ev.Comment != nil && (ev.Action == "edited" || ev.Action == "deleted") {
		key := ev.Comment.key()
		if before[key+".accepted.json"] || strings.TrimSpace(ev.BodyBefore) == acceptSentence(r.set.version) {
			name := key + ".deleted.json"
			if ev.Action == "edited" {
				name = key + ".edited-" + compactTime(ev.Comment.UpdatedAt) + ".json"
			}
			if !names[name] {
				rec := eventRecord{RecordedAt: r.stamp(), FoundBy: "event", Event: ev.Raw}
				if err := r.writeRecord(dir+"/"+name, rec, "Record the "+ev.Action+" event of acceptance comment "+key); err != nil {
					return nil, err
				}
				names[name] = true
			}
		}
	}
	current := map[string]issueComment{}
	for _, c := range all {
		current[c.key()] = c
	}
	keys := make([]string, 0, len(recs))
	for key := range recs {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	var changes []change
	for _, key := range keys {
		rec := recs[key]
		cur, present := current[key]
		switch {
		case present && strings.TrimSpace(cur.Body) != strings.TrimSpace(rec.Body):
			name := key + ".edited-" + editStamp(cur) + ".json"
			if !names[name] {
				body := rec.Body
				out := eventRecord{RecordedAt: r.stamp(), FoundBy: "comparison", BodyBefore: &body, Comment: cur.Raw}
				if err := r.writeRecord(dir+"/"+name, out, "Record an edit of acceptance comment "+key+" found by comparison"); err != nil {
					return nil, err
				}
				names[name] = true
			}
		case !present && !names[key+".deleted.json"] && !hasName(names, key+".missing-"):
			name := key + ".missing-" + r.now().UTC().Format("2006-01-02") + ".json"
			out := eventRecord{RecordedAt: r.stamp(), FoundBy: "comparison", CommentID: rec.Comment.ID}
			if err := r.writeRecord(dir+"/"+name, out, "Record that acceptance comment "+key+" is missing"); err != nil {
				return nil, err
			}
			names[name] = true
		}
		switch {
		case names[key+".deleted.json"] || hasName(names, key+".missing-"):
			changes = append(changes, change{Login: rec.Comment.User.Login, URL: rec.Comment.HTMLURL, Deleted: true})
		case hasName(names, key+".edited-"):
			changes = append(changes, change{Login: rec.Comment.User.Login, URL: rec.Comment.HTMLURL})
		}
	}
	return changes, nil
}

// editStamp names an edit found by comparison by the time of the edit. The
// REST API gives a review no edit time, so a review's edit is named by the
// start of the SHA-256 of its new body instead.
func editStamp(c issueComment) string {
	if c.Kind == kindReview {
		return "body-" + sha256Hex([]byte(c.Body))[:12]
	}
	return compactTime(c.UpdatedAt)
}

func hasName(names map[string]bool, prefix string) bool {
	for n := range names {
		if strings.HasPrefix(n, prefix) {
			return true
		}
	}
	return false
}

// compactTime writes a time for a file name, without the colons that some
// file systems refuse.
func compactTime(t time.Time) string { return t.UTC().Format("20060102T150405Z") }
