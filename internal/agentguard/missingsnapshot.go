package agentguard

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"
)

// pdpAfterFetch builds the hook's PDP again once the snapshot that ses
// lacked is on disk. cause is why the first build failed. A session whose
// time has run out renews first (renewBeforeFetch), and a live one fetches
// the snapshot (fetchMissingSnapshot). For a live session cause stands when
// a verifiable blob was on disk already, because then the snapshot was not
// what failed.
func (d liveDecider) pdpAfterFetch(ses Session, cause error) (*LocalPDP, error) {
	cfg, err := d.store.LoadConfig()
	if err != nil {
		return nil, cause
	}
	client := NewClient(cfg.ServerURL)
	if !time.Now().Before(ses.ExpiresAt) {
		if ses, err = renewBeforeFetch(client, d.store, cfg, ses, cause); err != nil {
			return nil, err
		}
		// The renewal adopts the snapshot it names, so the PDP may build now.
		if pdp, err := NewLocalPDP(d.store, ses.Subject()); err == nil {
			return pdp, nil
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	fetched, err := fetchMissingSnapshot(ctx, client, d.store, cfg, ses)
	switch {
	case err != nil:
		return nil, err
	case !fetched:
		return nil, cause
	}
	return NewLocalPDP(d.store, ses.Subject())
}

// renewBeforeFetch renews ses, whose time has run out, through the steps
// RefreshIfStale takes (renewSession), and returns the renewed session. It
// runs when the blob is missing and when it verifies under another id than
// the pin, as a crash between a renewal's two writes leaves it, because the
// renewal reconciles the pin to the verified blob. A renewal that fails
// returns the deny sentence for what came back, so no snapshot is ever
// fetched with a token that is past its time. cause comes back only for a
// renewal that landed on a session time that has already passed.
func renewBeforeFetch(client *Client, store *Store, cfg Config, ses Session, cause error) (Session, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	fail, detail := renewSession(ctx, client, store, cfg, &ses)
	age := time.Since(ses.ExpiresAt).Round(time.Second)
	switch fail {
	case renewTerminal:
		return ses, errors.New(renewRefusedReason(detail))
	case renewUnjudged:
		return ses, errors.New(renewUnjudgedReason(age, detail))
	case renewTransport, renewRejected:
		return ses, errors.New(renewFailedReason(age, detail))
	}
	if !time.Now().Before(ses.ExpiresAt) {
		return ses, cause
	}
	return ses, nil
}

// fetchMissingSnapshot is the step a decision on the live session ses takes
// when no verifiable policy snapshot is on disk: it fetches the snapshot with
// the session's own token, saves the blob, pins its id on the session and
// saves the session. It reports whether it fetched, and it does nothing when a
// verifiable blob is on disk. The error is the deny sentence: the fetch failed,
// what came back, and what to do next. A failed fetch is written to the
// snapshot-checked marker, so a decision on the same session inside the
// snapshot lag repeats that answer and asks the server nothing, while the
// first fetch for a session is never held back. Like the drain pulse, the
// step renews nothing, so a failed fetch leaves the session as it was and a
// fetch that lands ends any refusal (settlePolicy).
func fetchMissingSnapshot(ctx context.Context, client *Client, store *Store, cfg Config, ses Session) (bool, error) {
	if _, ok := diskSnapshotID(store, cfg); ok {
		return false, nil
	}
	lag, marker := snapshotLag(cfg), store.statePath("snapshot-checked")
	if f, age, ok := recentFetchFailure(marker, ses.SessionID, lag); ok {
		return false, errors.New(missingSnapshotReason(cfg.ServerURL, age, f, lag-age))
	}
	id, err := fetchAndAdopt(ctx, client, ses.SessionToken, store, cfg, ses.SnapshotID)
	if err != nil {
		f := fetchOutcome(err)
		f.Session = ses.SessionID
		raw, _ := json.Marshal(f)
		_ = os.WriteFile(marker, raw, 0o600)
		return false, errors.New(missingSnapshotReason(cfg.ServerURL, 0, f, lag))
	}
	// Re-load right before writing, as the drain pulse does: a hook may have
	// renewed the token meanwhile, and a session that another process
	// replaced or dropped is not written back.
	if cur, err := store.LoadSession(); err == nil && cur.SessionID == ses.SessionID {
		cur.SnapshotID = id
		cur.settlePolicy(id, nil, cur.ExpiresAt)
		_ = store.SaveSession(cur)
	}
	_ = os.WriteFile(marker, []byte(time.Now().UTC().Format(time.RFC3339)), 0o600)
	return true, nil
}

// snapshotLag is the least time between two requests for the policy
// snapshot that nothing forces: cfg.SnapshotLagSeconds, or 30 s when unset.
func snapshotLag(cfg Config) time.Duration {
	if cfg.SnapshotLagSeconds > 0 {
		return time.Duration(cfg.SnapshotLagSeconds) * time.Second
	}
	return 30 * time.Second
}

// fetchFailure is what the snapshot-checked marker holds after a fetch by
// fetchMissingSnapshot failed: the session, what came back and the advice
// that follows it. The drain pulse writes a time there instead, and both
// read the marker's modification time as the throttle.
type fetchFailure struct {
	Session string      `json:"session"`
	Answer  string      `json:"answer"`
	Advice  fetchAdvice `json:"advice"`
}

// fetchAdvice names what a person can do after a failed fetch, which depends
// on whether a later fetch with the same session can land.
type fetchAdvice string

// The advice kinds: retry after no answer, a 5xx, a 408 or a 429, fix-local
// when this machine could not keep what the server sent, new-session when the
// server refused this session, own when the refusal's sentence says what to
// do, and exec when the refusal's sentence already names a new session and
// straza doctor, so only what that means for straza exec is missing.
const (
	adviceRetry      fetchAdvice = "retry"
	adviceFixLocal   fetchAdvice = "fix-local"
	adviceNewSession fetchAdvice = "new-session"
	adviceOwn        fetchAdvice = "own"
	adviceExec       fetchAdvice = "exec"
)

// execChecksInItself tells the person what a new session means for straza
// exec, which has no session start of its own.
const execChecksInItself = "For straza exec, restarting the agent starts no new session, " +
	"because straza exec checks in again by itself only when this session is about to end."

// recentFetchFailure returns the failed fetch the marker records for session
// and its age, when that age is under lag. A marker from the future, as a
// clock set back leaves, counts as none, so it never holds a fetch back.
func recentFetchFailure(marker, session string, lag time.Duration) (fetchFailure, time.Duration, bool) {
	fi, err := os.Stat(marker)
	if err != nil {
		return fetchFailure{}, 0, false
	}
	age := time.Since(fi.ModTime())
	if age < 0 || age >= lag {
		return fetchFailure{}, 0, false
	}
	var f fetchFailure
	raw, err := os.ReadFile(marker) // #nosec G304 -- our own state dir
	if err != nil || json.Unmarshal(raw, &f) != nil || f.Session != session {
		return fetchFailure{}, 0, false
	}
	return f, age, true
}

// fetchOutcome words what came back from a failed snapshot fetch and names
// the advice that follows it: the refusal's own sentences when the server
// refused it, the status of a 5xx, what this machine could not do with a
// snapshot the server sent, or the error of a fetch that got no answer.
func fetchOutcome(err error) fetchFailure {
	var refuse *StatusError
	status := errors.As(err, &refuse)
	switch why := refusalReason(err); {
	case why != "" && status && (refuse.Status == http.StatusRequestTimeout || refuse.Status == http.StatusTooManyRequests):
		// Something in front of strazad timed the request out or rate-limited
		// it, and a later fetch with the same session can land.
		return fetchFailure{Answer: why, Advice: adviceRetry}
	case why != "" && status && strings.Contains(strings.ToLower(why), "new session") && strings.Contains(why, "straza doctor"):
		// strazad's snapshot 401 already tells the person to start a new
		// session and run straza doctor, so the client says neither again.
		// No "retrying will not help" either: once the session ends, the
		// next decision renews it and fetches again.
		return fetchFailure{Answer: why, Advice: adviceExec}
	case why != "" && status:
		return fetchFailure{Answer: why, Advice: adviceNewSession}
	case why != "":
		return fetchFailure{Answer: why, Advice: adviceOwn}
	case status && refuse.Msg == fmt.Sprintf("HTTP %d", refuse.Status):
		return fetchFailure{Answer: fmt.Sprintf("The server answered HTTP %d and gave no reason.", refuse.Status), Advice: adviceRetry}
	case status:
		return fetchFailure{Answer: fmt.Sprintf("The server answered HTTP %d: %s", refuse.Status,
			endSentence(strings.TrimPrefix(refuse.Msg, "Straza: "))), Advice: adviceRetry}
	case errors.Is(err, errSnapshotNotKept):
		return fetchFailure{Answer: "The server answered HTTP 200 with the policy, but " + endSentence(err.Error()), Advice: adviceFixLocal}
	}
	return fetchFailure{Answer: "No answer came back: " + endSentence(err.Error()), Advice: adviceRetry}
}

// text words the advice for a fetch from server. wait is the time left until
// the next fetch, which only a retry and a local fix wait for.
func (a fetchAdvice) text(server string, wait time.Duration) string {
	after := (wait + time.Second - 1).Truncate(time.Second).String()
	switch a {
	case adviceNewSession:
		return "Retrying will not change this answer while this session lasts. Start a new session of the AI agent so straza checks in again. " +
			execChecksInItself + " " +
			"If the fetch is refused again, check that " + server + " is the address strazad serves on and run `straza doctor`."
	case adviceExec:
		return execChecksInItself
	case adviceOwn:
		return "Retrying will not change this answer."
	case adviceFixLocal:
		return "Fix that error on this machine, then wait " + after + " and run the tool call again, which fetches it again. " +
			"Run `straza doctor` if it keeps failing."
	}
	return "Wait " + after + " and run the tool call again, which fetches it again. " +
		"If the fetch keeps failing, check that " + server + " is the address strazad serves on, and run `straza doctor`."
}

// missingSnapshotReason words the deny of a live session whose policy
// snapshot is missing: what failed, what came back from the fetch age ago,
// and what to do next. wait is the time left until the next fetch.
func missingSnapshotReason(server string, age time.Duration, f fetchFailure, wait time.Duration) string {
	when := "just now"
	if age >= time.Second {
		when = age.Round(time.Second).String() + " ago"
	}
	return "this session is live, but the policy snapshot is missing on this machine, so tool calls are denied until straza has it. " +
		"straza tried to fetch it from " + server + " " + when + ", and the fetch failed. " + f.Answer + " " + f.Advice.text(server, wait)
}
