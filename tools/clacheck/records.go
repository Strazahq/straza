package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// recordsRepo is the private repository that holds the acceptance records.
const recordsRepo = "strazahq/cla-records"

// accountFile is v<version>/accounts/<id>.json, written once when an account's
// first acceptance of the version is recorded. The check reads only whether it
// exists. Nothing in it is keyed by email.
type accountFile struct {
	ID              int64           `json:"id"`
	Login           string          `json:"login"`
	Version         string          `json:"version"`
	TextSHA256      string          `json:"text_sha256"`
	TextURL         string          `json:"text_url"`
	FirstAcceptance firstAcceptance `json:"first_acceptance"`
	RecordedAt      string          `json:"recorded_at"`
	Basis           string          `json:"basis"`
}

// firstAcceptance names the comment of an account's first acceptance.
// AcceptedAt is when its body became the sentence, which is later than
// CreatedAt when an edit made it the sentence.
type firstAcceptance struct {
	Repo       string `json:"repo"`
	PR         int    `json:"pr"`
	Kind       string `json:"kind"`
	CommentID  int64  `json:"comment_id"`
	CreatedAt  string `json:"created_at"`
	AcceptedAt string `json:"accepted_at"`
}

// eventRecord is one file under v<version>/events/. Comment, Event and History
// keep what GitHub returned, unchanged. An accepted record also names when
// the comment's body became the sentence and that body.
type eventRecord struct {
	RecordedAt   string          `json:"recorded_at"`
	FoundBy      string          `json:"found_by,omitempty"`
	CommentID    int64           `json:"comment_id,omitempty"`
	BodyBefore   *string         `json:"body_before,omitempty"`
	AcceptedAt   string          `json:"accepted_at,omitempty"`
	AcceptedBody string          `json:"accepted_body,omitempty"`
	Comment      json.RawMessage `json:"comment,omitempty"`
	Event        json.RawMessage `json:"event,omitempty"`
	History      json.RawMessage `json:"history,omitempty"`
}

func (r *runner) versionDir() string { return "v" + r.set.version }

func (r *runner) accountPath(id int64) string {
	return r.versionDir() + "/accounts/" + itoa(id) + ".json"
}

func (r *runner) eventsDir(pr int) string {
	return r.versionDir() + "/events/" + strings.Replace(r.cfg.Repo, "/", "__", 1) + "/" + strconv.Itoa(pr)
}

func (r *runner) stamp() string { return r.now().UTC().Format(time.RFC3339) }

func contentsPath(path string) string { return "/repos/" + recordsRepo + "/contents/" + path }

// readRecord returns the content of a records file and whether it exists.
func (r *runner) readRecord(path string) ([]byte, bool, error) {
	data, _, err := r.rec.do("GET", contentsPath(path), rawAccept, nil)
	switch {
	case statusOf(err) == 404:
		return nil, false, nil
	case err != nil:
		return nil, false, r.recordsErr("read "+path, err)
	}
	return data, true, nil
}

// listRecords returns the names of the files in a records folder, which is
// empty when the folder does not exist yet.
func (r *runner) listRecords(dir string) (map[string]bool, error) {
	data, _, err := r.rec.do("GET", contentsPath(dir), jsonAccept, nil)
	names := map[string]bool{}
	switch {
	case statusOf(err) == 404:
		return names, nil
	case err != nil:
		return nil, r.recordsErr("list "+dir, err)
	}
	var entries []struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(data, &entries); err != nil {
		return nil, fmt.Errorf("the records folder %s is not a folder listing, so the check cannot tell what it recorded before. %v. Check %s by hand, then run the cla workflow again for this pull request", dir, err, recordsRepo)
	}
	for _, e := range entries {
		names[e.Name] = true
	}
	return names, nil
}

// writeRecord creates a records file with one commit. Every path the check
// writes is fixed by the data it records, so the callers write only a file
// that does not exist yet, and a second run finds it and skips it.
func (r *runner) writeRecord(path string, v any, message string) error {
	// The records keep GitHub's text as it was sent, so "<" in a comment stays
	// "<" instead of the escape the encoder writes by default.
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		return err
	}
	in := map[string]string{"message": message, "content": base64.StdEncoding.EncodeToString(buf.Bytes())}
	if _, _, err := r.rec.do("PUT", contentsPath(path), jsonAccept, in); err != nil {
		return r.recordsErr("write "+path, err)
	}
	fmt.Fprintf(r.out, "Recorded %s.\n", path)
	return nil
}

// recordsErr turns a failed call to the records repository into a sentence
// that says what to do. A used-up rate limit and a refused token are named
// as such, because they need different steps.
func (r *runner) recordsErr(what string, err error) error {
	switch s := statusOf(err); {
	case rateLimited(err):
		return fmt.Errorf("the records token reached GitHub's rate limit, so no acceptance can be recorded until it resets. The check tried to %s in %s, and %v. Run the cla workflow by hand for this pull request after that time", what, recordsRepo, err)
	case s == 401 || s == 403:
		return fmt.Errorf("the records token was refused, so no acceptance can be recorded and the check cannot pass. The check tried to %s in %s, and %v. The token has expired or lost access. Renew CLA_RECORDS_TOKEN in the cla environment, then run the cla workflow by hand for this pull request", what, recordsRepo, err)
	}
	return fmt.Errorf("the check could not %s in %s. %v. Nothing is lost, because the next run repairs what this one missed. Run the cla workflow by hand for this pull request once GitHub answers again", what, recordsRepo, err)
}

// loadText returns the SHA-256 of the agreement text of the version. It reads
// v<version>/text/CLA.md and v<version>/text/SHA256SUMS and the published
// CLA.md at the commit that CLA_TEXT_URL names, and fails closed when a file
// is missing or they disagree, because an acceptance must be bound to the
// exact text the confirmation links to.
func (r *runner) loadText() (string, error) {
	textPath, sumsPath := r.versionDir()+"/text/CLA.md", r.versionDir()+"/text/SHA256SUMS"
	text, okText, err := r.readRecord(textPath)
	if err != nil {
		return "", err
	}
	sums, okSums, err := r.readRecord(sumsPath)
	if err != nil {
		return "", err
	}
	if !okText || !okSums {
		return "", fmt.Errorf("the records repository shows no text for agreement version %s, so no acceptance can be bound to its text. The check looked for %s and %s in %s. Either the text is not there yet or the records token cannot see the repository, which GitHub reports the same way. Add the exact published text and its SHA256SUMS, or give CLA_RECORDS_TOKEN access to %s, then run the cla workflow by hand for this pull request", r.set.version, textPath, sumsPath, recordsRepo, recordsRepo)
	}
	got := sha256Hex(text)
	want := ""
	for _, line := range strings.Split(string(sums), "\n") {
		if f := strings.Fields(line); len(f) == 2 && strings.TrimPrefix(f[1], "*") == "CLA.md" {
			want = strings.ToLower(f[0])
		}
	}
	if want != got {
		return "", fmt.Errorf("the recorded agreement text does not match its SHA256SUMS, so the check cannot tell which text contributors accept. %s has SHA-256 %s, and %s names %q for CLA.md. SHA256SUMS needs the line %q, which sha256sum CLA.md writes when it runs inside %s. Restore the published text or correct SHA256SUMS in %s, then run the cla workflow by hand for this pull request", textPath, got, sumsPath, want, got+"  CLA.md", r.versionDir()+"/text", recordsRepo)
	}
	m := textURLRE.FindStringSubmatch(r.set.textURL)
	published, _, err := r.gh.do("GET", "/repos/"+m[1]+"/contents/CLA.md?ref="+m[2], rawAccept, nil)
	if err != nil {
		return "", fmt.Errorf("the check could not read the published agreement text that CLA_TEXT_URL names, so it cannot compare it with the records copy. %v. Check that the commit is in %s, then run the cla workflow by hand for this pull request", err, m[1])
	}
	if !bytes.Equal(published, text) {
		return "", fmt.Errorf("the records copy of the agreement text differs from the published text that CLA_TEXT_URL names, so the check cannot tell which text contributors accept. %s holds %d bytes with SHA-256 %s, and CLA.md at commit %s holds %d bytes with SHA-256 %s. Copy the published file byte for byte, line ends included, to %s and make SHA256SUMS from that copy, then run the cla workflow by hand for this pull request", textPath, len(text), got, shortOID(m[2]), len(published), sha256Hex(published), textPath)
	}
	return got, nil
}

func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// hasAccount reports whether the account has an account file for the version.
// The deleted account never has one.
func (r *runner) hasAccount(id int64) (bool, error) {
	if id == ghostID {
		return false, nil
	}
	if has, ok := r.accounts[id]; ok {
		return has, nil
	}
	_, has, err := r.readRecord(r.accountPath(id))
	if err != nil {
		return false, err
	}
	r.accounts[id] = has
	return has, nil
}
