package main

import (
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"
)

// acceptedRecord is what the check knows of one recorded acceptance: the
// comment as GitHub returned it at record time, when its body became the
// sentence, and that body.
type acceptedRecord struct {
	Comment issueComment
	At      time.Time
	Body    string
}

// keyRE matches the key of a comment in the records.
var keyRE = regexp.MustCompile(`^(review-comment-|review-)?[0-9]+$`)

// acceptedKeys returns the keys of the accepted records in a folder listing,
// sorted.
func acceptedKeys(names map[string]bool) []string {
	var out []string
	for n := range names {
		if key, ok := strings.CutSuffix(n, ".accepted.json"); ok && keyRE.MatchString(key) {
			out = append(out, key)
		}
	}
	sort.Strings(out)
	return out
}

// readAccepted reads every accepted record of the pull request's folder.
func (r *runner) readAccepted(dir string, names map[string]bool) (map[string]acceptedRecord, error) {
	out := map[string]acceptedRecord{}
	for _, key := range acceptedKeys(names) {
		data, ok, err := r.readRecord(dir + "/" + key + ".accepted.json")
		if err != nil {
			return nil, err
		}
		var rec eventRecord
		if !ok || json.Unmarshal(data, &rec) != nil {
			return nil, fmt.Errorf("the record %s/%s.accepted.json is listed but cannot be read as an acceptance. Check it by hand in %s, then run the cla workflow again for this pull request", dir, key, recordsRepo)
		}
		c, err := parseComment(rec.Comment, kindOfKey(key))
		if err != nil {
			return nil, err
		}
		at, err := time.Parse(time.RFC3339, rec.AcceptedAt)
		if err != nil {
			at = c.CreatedAt
		}
		body := rec.AcceptedBody
		if body == "" {
			body = c.Body
		}
		out[key] = acceptedRecord{Comment: c, At: at, Body: body}
	}
	return out, nil
}

// acceptanceOf decides whether a comment the check has not recorded is an
// acceptance, and when its body became the sentence. A comment never edited
// is judged by its body and dated by its creation. An edited one is judged
// by GitHub's edit history: the oldest revision that is the sentence and that
// the comment's author wrote decides and dates it, so an acceptance edited
// away before any run saw it still counts, and one that an edit made is
// dated by that edit. A revision another account wrote, such as a maintainer
// editing the comment, is never an acceptance.
func acceptanceOf(c issueComment, h history, sentence string) (time.Time, string, bool) {
	if !h.Edited {
		if strings.TrimSpace(c.Body) == sentence {
			return c.CreatedAt, c.Body, true
		}
		return time.Time{}, "", false
	}
	for _, v := range h.Revisions {
		if v.Diff != nil && strings.TrimSpace(*v.Diff) == sentence && v.Editor != nil && v.Editor.DatabaseID == c.User.ID {
			return v.EditedAt, *v.Diff, true
		}
	}
	return time.Time{}, "", false
}

// payloadAcceptance judges the comment of the event in hand when GitHub no
// longer lists it, because it was deleted before any run recorded it. The
// payload is the last copy GitHub offers: the body when the event fired,
// dated by the last edit, and, for an edit, the body before it, dated by the
// comment's creation, because the payload shows no earlier edit.
func payloadAcceptance(ev event, sentence string) (time.Time, string, bool) {
	c := ev.Comment
	since := c.CreatedAt
	if c.UpdatedAt.After(c.CreatedAt) {
		since = c.UpdatedAt
	}
	switch {
	case strings.TrimSpace(c.Body) == sentence:
		return since, c.Body, true
	case ev.Action == "edited" && strings.TrimSpace(ev.BodyBefore) == sentence:
		return c.CreatedAt, ev.BodyBefore, true
	}
	return time.Time{}, "", false
}

// recordAcceptances is step 1. It records every comment, review comment and
// review that is an acceptance and has no record yet, then makes sure that
// every recorded acceptance has its account file and its written
// confirmation. It returns the accounts that accepted on this pull request.
// The deleted account's comments are never an acceptance.
func (r *runner) recordAcceptances(ev event, pr pullRequest, conv, all []issueComment, names map[string]bool, recs map[string]acceptedRecord, sha string) (map[int64]bool, error) {
	sentence := acceptSentence(r.set.version)
	var ask []issueComment
	for _, c := range all {
		if _, done := recs[c.key()]; !done && c.NodeID != "" && (c.UpdatedAt.After(c.CreatedAt) || c.Kind == kindReview) {
			ask = append(ask, c)
		}
	}
	hist, err := r.histories(ask, pr.Number)
	if err != nil {
		return nil, err
	}
	present := map[string]bool{}
	for _, c := range all {
		present[c.key()] = true
		if _, done := recs[c.key()]; done || c.User.ID == 0 || c.User.ID == ghostID {
			continue
		}
		h := history{}
		if got := hist[c.NodeID]; got != nil {
			h = *got
		}
		if at, body, ok := acceptanceOf(c, h, sentence); ok {
			var raw json.RawMessage
			if h.Edited {
				raw = h.Raw
			}
			if err := r.recordAccepted(pr, names, recs, c, acceptedRecord{Comment: c, At: at, Body: body}, raw, ""); err != nil {
				return nil, err
			}
		}
	}
	if c := ev.Comment; c != nil && !present[c.key()] && c.User.ID != 0 && c.User.ID != ghostID {
		if _, done := recs[c.key()]; !done {
			if at, body, ok := payloadAcceptance(ev, sentence); ok {
				if err := r.recordAccepted(pr, names, recs, *c, acceptedRecord{Comment: *c, At: at, Body: body}, nil, "event"); err != nil {
					return nil, err
				}
			}
		}
	}
	here := map[int64]bool{}
	keys := make([]string, 0, len(recs))
	for key := range recs {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		if rec := recs[key]; rec.Comment.User.ID != ghostID {
			if err := r.settle(pr, conv, names, key, rec, sha); err != nil {
				return nil, err
			}
			here[rec.Comment.User.ID] = true
		}
	}
	return here, nil
}

// recordAccepted writes the accepted record of one acceptance.
func (r *runner) recordAccepted(pr pullRequest, names map[string]bool, recs map[string]acceptedRecord, c issueComment, a acceptedRecord, hist json.RawMessage, foundBy string) error {
	key := c.key()
	rec := eventRecord{RecordedAt: r.stamp(), FoundBy: foundBy, AcceptedAt: a.At.UTC().Format(time.RFC3339),
		AcceptedBody: a.Body, Comment: c.Raw, History: hist}
	who := fmt.Sprintf("@%s (id %d) in %s#%d", c.User.Login, c.User.ID, r.cfg.Repo, pr.Number)
	if err := r.writeRecord(r.eventsDir(pr.Number)+"/"+key+".accepted.json", rec, "Record the acceptance of version "+r.set.version+" by "+who); err != nil {
		return err
	}
	names[key+".accepted.json"] = true
	recs[key] = a
	return nil
}

// settle writes the account file of a recorded acceptance when the account
// has none, and posts its written confirmation when none exists, then
// records that confirmation.
func (r *runner) settle(pr pullRequest, conv []issueComment, names map[string]bool, key string, rec acceptedRecord, sha string) error {
	c := rec.Comment
	who := fmt.Sprintf("@%s (id %d) in %s#%d", c.User.Login, c.User.ID, r.cfg.Repo, pr.Number)
	has, err := r.hasAccount(c.User.ID)
	if err != nil {
		return err
	}
	if !has {
		acct := accountFile{ID: c.User.ID, Login: c.User.Login, Version: r.set.version, TextSHA256: sha,
			TextURL: r.set.textURL, RecordedAt: r.stamp(), Basis: "comment",
			FirstAcceptance: firstAcceptance{Repo: r.cfg.Repo, PR: pr.Number, Kind: c.Kind, CommentID: c.ID,
				CreatedAt: c.CreatedAt.UTC().Format(time.RFC3339), AcceptedAt: rec.At.UTC().Format(time.RFC3339)}}
		if err := r.writeRecord(r.accountPath(c.User.ID), acct, "Record the account of "+who); err != nil {
			return err
		}
		r.accounts[c.User.ID] = true
	}
	confirmed := key + ".confirmed.json"
	if names[confirmed] {
		return nil
	}
	conf, found := findConfirmation(conv, key)
	if !found {
		if conf, err = r.postComment(pr.Number, confirmationText(c, rec.At, r.set.version, r.set.textURL, sha)); err != nil {
			return err
		}
	}
	if err := r.writeRecord(r.eventsDir(pr.Number)+"/"+confirmed, eventRecord{RecordedAt: r.stamp(), Comment: conf.Raw}, "Record the confirmation to "+who); err != nil {
		return err
	}
	names[confirmed] = true
	return nil
}

// findConfirmation returns the check's own confirmation of the acceptance
// key. Only a comment by the check's account that ends with the hidden
// marker counts, and never the status comment, which quotes text from the
// pull request.
func findConfirmation(conv []issueComment, key string) (issueComment, bool) {
	for _, c := range conv {
		if c.User.ID == botID && !strings.HasPrefix(c.Body, statusMarker) &&
			strings.HasSuffix(strings.TrimSpace(c.Body), confirmMarker(key)) {
			return c, true
		}
	}
	return issueComment{}, false
}
