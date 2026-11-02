package service

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/strazahq/straza/internal/agentguard"
)

// fetchFailure is a failed fetch of a session's missing snapshot: when it
// failed, what came back and the advice that follows.
type fetchFailure struct {
	at     time.Time
	Answer string
	Advice fetchAdvice
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

// fetchOutcome words what came back from a failed snapshot fetch and names
// the advice that follows it: the refusal's own sentences when the server
// refused it, the status of a 5xx, what this machine could not do with a
// snapshot the server sent, or the error of a fetch that got no answer.
func fetchOutcome(err error) fetchFailure {
	var refuse *agentguard.StatusError
	status := errors.As(err, &refuse)
	switch why := refusalReason(err); {
	case why != "" && status && (refuse.Status == http.StatusRequestTimeout || refuse.Status == http.StatusTooManyRequests):
		// Something in front of strazad timed the request out or rate-limited
		// it, and a later fetch with the same session can land.
		return fetchFailure{Answer: why, Advice: adviceRetry}
	case why != "" && status && strings.Contains(strings.ToLower(why), "new session") && strings.Contains(why, "straza doctor"):
		// strazad's snapshot 401 already tells the person to start a new
		// session and run straza doctor, so neither is said again.
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
