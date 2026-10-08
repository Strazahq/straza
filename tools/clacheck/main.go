// Command clacheck is the contributor agreement check of Straza. A workflow
// runs it on every pull request event, every comment event and a manual
// dispatch. It records each acceptance of the agreement in the company's
// records repository, confirms each one in writing on the pull request,
// refuses material marked "Not a Contribution", and reports the commit status
// straza/cla. It reads the pull request only through GitHub's API and never
// runs code from it.
//
// The workflow passes GITHUB_TOKEN, CLA_RECORDS_TOKEN, CLA_VERSION,
// CLA_TEXT_URL, CLA_ALLOWLIST_IDS and, for a manual dispatch, CLA_PR. GitHub
// sets GITHUB_REPOSITORY, GITHUB_EVENT_NAME, GITHUB_EVENT_PATH, GITHUB_API_URL,
// GITHUB_GRAPHQL_URL, GITHUB_SERVER_URL and GITHUB_RUN_ID.
package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"maps"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// maxCommits is the most commits the check reads. A larger pull request fails
// with a request to split it.
const maxCommits = 250

var (
	repoRE    = regexp.MustCompile(`^[A-Za-z0-9._-]+/[A-Za-z0-9._-]+$`)
	shaRE     = regexp.MustCompile(`^[0-9a-f]{40}([0-9a-f]{24})?$`)
	versionRE = regexp.MustCompile(`^[0-9]+\.[0-9]+$`)
	textURLRE = regexp.MustCompile(`^https://github\.com/([A-Za-z0-9._-]+/[A-Za-z0-9._-]+)/blob/([0-9a-f]{40})/CLA\.md$`)
)

// config is the run's environment as strings, before any check.
type config struct {
	Repo         string
	EventName    string
	EventPath    string
	PRInput      string
	Version      string
	TextURL      string
	AllowIDs     string
	Token        string
	RecordsToken string
	APIURL       string
	GraphQLURL   string
	RunURL       string
}

func configFromEnv(getenv func(string) string) config {
	c := config{
		Repo: getenv("GITHUB_REPOSITORY"), EventName: getenv("GITHUB_EVENT_NAME"), EventPath: getenv("GITHUB_EVENT_PATH"),
		PRInput: getenv("CLA_PR"), Version: getenv("CLA_VERSION"), TextURL: getenv("CLA_TEXT_URL"),
		AllowIDs: getenv("CLA_ALLOWLIST_IDS"), Token: getenv("GITHUB_TOKEN"), RecordsToken: getenv("CLA_RECORDS_TOKEN"),
		APIURL: getenv("GITHUB_API_URL"), GraphQLURL: getenv("GITHUB_GRAPHQL_URL"),
	}
	if c.APIURL == "" {
		c.APIURL = "https://api.github.com"
	}
	if c.GraphQLURL == "" {
		c.GraphQLURL = c.APIURL + "/graphql"
	}
	if server, id := getenv("GITHUB_SERVER_URL"), getenv("GITHUB_RUN_ID"); server != "" && id != "" {
		c.RunURL = server + "/" + c.Repo + "/actions/runs/" + id
	}
	return c
}

// settings are the checked values of the workflow's CLA_ variables.
type settings struct {
	version string
	textURL string
	allow   map[int64]bool
}

func (c config) settings() (settings, error) {
	s := settings{version: c.Version, textURL: c.TextURL, allow: map[int64]bool{}}
	if !versionRE.MatchString(c.Version) {
		return s, fmt.Errorf("CLA_VERSION is %q, which is not a version such as 1.2, so the check cannot tell which agreement is in force. Set it in .github/workflows/cla.yml", c.Version)
	}
	if !textURLRE.MatchString(c.TextURL) {
		return s, fmt.Errorf("CLA_TEXT_URL is not a link to CLA.md at a full commit id, so the confirmation cannot cite the exact text and the check stops. It is %q. Set it in .github/workflows/cla.yml to https://github.com/<owner>/<repo>/blob/<40-character commit id>/CLA.md", c.TextURL)
	}
	for _, f := range strings.Split(c.AllowIDs, ",") {
		if f = strings.TrimSpace(f); f == "" {
			continue
		}
		id, err := strconv.ParseInt(f, 10, 64)
		if err != nil || id <= 0 {
			return s, fmt.Errorf("CLA_ALLOWLIST_IDS holds %q, which is not a numeric GitHub account id. The allowlist takes ids only, never names. Correct it in .github/workflows/cla.yml", f)
		}
		s.allow[id] = true
	}
	return s, nil
}

// event is what the check reads from the event that started the run.
type event struct {
	Name       string
	Action     string
	PR         int
	HeadSHA    string
	Closed     bool
	Raw        json.RawMessage
	Comment    *issueComment
	BodyBefore string
}

func readEvent(c config) (event, error) {
	ev := event{Name: c.EventName}
	if c.EventName == "workflow_dispatch" {
		n, err := strconv.Atoi(strings.TrimPrefix(strings.TrimSpace(c.PRInput), "#"))
		if err != nil || n <= 0 {
			return ev, fmt.Errorf("the pull request number %q is not a number, so the check does not know what to check. Start the workflow again and enter the number of the pull request, for example 123", c.PRInput)
		}
		ev.PR = n
		return ev, nil
	}
	if c.EventName != "pull_request_target" && c.EventName != "issue_comment" {
		return ev, fmt.Errorf("the event %q does not start this check. It runs on pull_request_target, issue_comment and workflow_dispatch only, so check the on: list in .github/workflows/cla.yml", c.EventName)
	}
	raw, err := os.ReadFile(c.EventPath) //nolint:gosec // G304: GitHub names the event file in GITHUB_EVENT_PATH
	if err != nil {
		return ev, fmt.Errorf("the check could not read the event file that GITHUB_EVENT_PATH names. %v. Run it from GitHub Actions, which writes that file", err)
	}
	var p struct {
		Action      string `json:"action"`
		PullRequest *struct {
			Number int    `json:"number"`
			State  string `json:"state"`
			Head   struct {
				SHA string `json:"sha"`
			} `json:"head"`
		} `json:"pull_request"`
		Issue *struct {
			Number      int             `json:"number"`
			State       string          `json:"state"`
			PullRequest json.RawMessage `json:"pull_request"`
		} `json:"issue"`
		Comment json.RawMessage `json:"comment"`
		Changes struct {
			Body *struct {
				From string `json:"from"`
			} `json:"body"`
		} `json:"changes"`
	}
	if err := json.Unmarshal(raw, &p); err != nil {
		return ev, fmt.Errorf("the event file is not JSON, so the check cannot tell which pull request to check. %v. Run the cla workflow by hand for the pull request", err)
	}
	ev.Action, ev.Raw = p.Action, raw
	switch {
	case c.EventName == "pull_request_target" && p.PullRequest != nil && shaRE.MatchString(p.PullRequest.Head.SHA):
		ev.PR, ev.HeadSHA, ev.Closed = p.PullRequest.Number, p.PullRequest.Head.SHA, p.PullRequest.State == "closed"
	case c.EventName == "issue_comment" && p.Issue != nil && len(p.Issue.PullRequest) > 0 && string(p.Issue.PullRequest) != "null" && len(p.Comment) > 0:
		cm, err := parseComment(p.Comment, kindComment)
		if err != nil {
			return ev, err
		}
		ev.PR, ev.Comment, ev.BodyBefore, ev.Closed = p.Issue.Number, &cm, cm.Body, p.Issue.State == "closed"
		if p.Changes.Body != nil {
			ev.BodyBefore = p.Changes.Body.From
		}
	default:
		return ev, fmt.Errorf("the %s event does not name a pull request with its comment or head commit, so there is nothing to check. The workflow's if: condition should have skipped it", c.EventName)
	}
	return ev, nil
}

// runner holds one run of the check.
type runner struct {
	cfg      config
	set      settings
	gh       *client
	rec      *client
	now      func() time.Time
	out      io.Writer
	accounts map[int64]bool
	// closed is set once the run knows the pull request is closed, and then
	// setStatus writes nothing.
	closed bool
}

func newRunner(c config, out io.Writer) *runner {
	return &runner{cfg: c, gh: newClient(c.APIURL, c.Token), rec: newClient(c.APIURL, c.RecordsToken),
		now: time.Now, out: out, accounts: map[int64]bool{}}
}

func main() {
	pendingOnly := flag.Bool("pending", false, "only set straza/cla to pending on the head of the event's pull request, before the recording run queues")
	flag.Parse()
	r := newRunner(configFromEnv(os.Getenv), os.Stdout)
	run := r.run
	if *pendingOnly {
		run = r.markPending
	}
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "clacheck:", err)
		os.Exit(1)
	}
}

// preflight checks what every run needs and reads the event.
func (r *runner) preflight() (event, error) {
	if r.cfg.Token == "" {
		return event{}, errors.New("GITHUB_TOKEN is missing, so the check can neither read the pull request nor set its status. Pass secrets.GITHUB_TOKEN to the step in .github/workflows/cla.yml")
	}
	if !repoRE.MatchString(r.cfg.Repo) {
		return event{}, fmt.Errorf("GITHUB_REPOSITORY is %q, not owner/name. Run the check from GitHub Actions, which sets it", r.cfg.Repo)
	}
	return readEvent(r.cfg)
}

// markPending sets straza/cla to pending on the head of the event's pull
// request and does nothing else. The workflow runs it for every event before
// the recording run waits in the queue, so no earlier success stands while a
// run is pending, and it needs only the workflow's token.
func (r *runner) markPending() error {
	ev, err := r.preflight()
	if err != nil {
		return err
	}
	head := ev.HeadSHA
	r.closed = ev.Closed
	if head == "" {
		pr, err := r.pull(ev.PR)
		if err != nil {
			return err
		}
		head, r.closed = pr.Head.SHA, pr.State == "closed"
	}
	return r.setStatus(head, "pending", pendingText, r.cfg.RunURL)
}

// run is one whole run. It sets the status to pending before anything else,
// so that a run that dies leaves the merge refused, and on any error it sets
// the status to failure with the reason. It sets success only at the end of
// a complete run.
func (r *runner) run() error {
	ev, err := r.preflight()
	if err != nil {
		return err
	}
	head := ev.HeadSHA
	r.closed = ev.Closed
	if head != "" {
		if err := r.setStatus(head, "pending", pendingText, r.cfg.RunURL); err != nil {
			return err
		}
	}
	err = r.check(ev, &head)
	var limit *limitError
	if err != nil && head != "" && !errors.As(err, &limit) {
		if serr := r.setStatus(head, "failure", statusLine(err), r.cfg.RunURL); serr != nil {
			fmt.Fprintf(r.out, "The failure status could not be set either: %v\n", serr)
		}
	}
	return err
}

// check does steps 1 to 6 for the pull request of ev. It moves head to the
// pull request's current head commit as soon as it knows it. When the pull
// request changed while it read it, it leaves the status pending for the run
// that the change started.
func (r *runner) check(ev event, head *string) error {
	start := r.now()
	pr, err := r.pull(ev.PR)
	if err != nil {
		return err
	}
	r.closed = r.closed || pr.State == "closed"
	if pr.Head.SHA != *head {
		*head = pr.Head.SHA
		if err := r.setStatus(*head, "pending", pendingText, r.cfg.RunURL); err != nil {
			return err
		}
	}
	if r.set, err = r.cfg.settings(); err != nil {
		return err
	}
	if r.cfg.RecordsToken == "" {
		return errors.New("the records token is missing, so no acceptance can be recorded and the check cannot pass. Add CLA_RECORDS_TOKEN to the cla environment of this repository, then run the cla workflow by hand for this pull request")
	}
	sha, err := r.loadText()
	if err != nil {
		return err
	}
	conv, all, err := r.comments(pr.Number)
	if err != nil {
		return err
	}
	dir := r.eventsDir(pr.Number)
	names, err := r.listRecords(dir)
	if err != nil {
		return err
	}
	before := maps.Clone(names)
	recs, err := r.readAccepted(dir, names)
	if err != nil {
		return err
	}
	here, err := r.recordAcceptances(ev, pr, conv, all, names, recs, sha)
	if err != nil {
		return err
	}
	changes, err := r.recordChanges(ev, pr, all, before, names, recs)
	if err != nil {
		return err
	}
	if pr.Commits > maxCommits {
		return fmt.Errorf("pull request #%d has %d commits, more than the %d the check reads, so it cannot tell who wrote them all. Split it into smaller pull requests", pr.Number, pr.Commits, maxCommits)
	}
	commits, err := r.commits(pr.Number)
	if err != nil {
		return err
	}
	opener := account{ID: pr.User.ID, Login: pr.User.Login}
	speakers := map[int64]bool{opener.ID: true}
	has := map[int64]bool{}
	if has[opener.ID], err = r.hasAccount(opener.ID); err != nil {
		return err
	}
	for _, c := range commits {
		for _, a := range c.Authors {
			if a.ID == 0 || speakers[a.ID] {
				continue
			}
			speakers[a.ID] = true
			if has[a.ID], err = r.hasAccount(a.ID); err != nil {
				return err
			}
		}
	}
	v := decide(verdictInput{Opener: opener, Commits: commits, HasAccount: has, AcceptedHere: here, Tools: aiTools, Allow: r.set.allow})
	diff, err := r.diff(pr.Number)
	if err != nil {
		return err
	}
	markers := scanMarkers(markerInput{Title: pr.Title, Body: pr.Body, Commits: commits, Diff: diff, Comments: all, Speakers: speakers})
	url, err := r.upsertStatus(pr.Number, conv, statusBody(statusView{Version: r.set.version, TextURL: r.set.textURL, Verdict: v, Markers: markers, Changes: changes}))
	if err != nil {
		return err
	}
	if pr.State == "closed" && !pr.Locked && len(here) > 0 {
		if err := r.lock(pr.Number); err != nil {
			return err
		}
	}
	state, desc := verdictStatus(v, markers, r.set.version)
	if state == "success" {
		if err := r.sharedHead(pr); err != nil {
			return err
		}
	}
	changed, err := r.changedDuringRun(pr, conv, start)
	if err != nil {
		return err
	}
	if changed {
		fmt.Fprintf(r.out, "Pull request #%d changed while the check ran. The run that the change started decides.\n", pr.Number)
		return r.setStatus(*head, "pending", changedText, r.cfg.RunURL)
	}
	fmt.Fprintf(r.out, "Pull request #%d: %d accepted, %d still to accept, %d AI tool emails, %d places marked Not a Contribution.\n",
		pr.Number, len(v.Passed), len(v.Gaps), len(v.Tools), len(markers))
	return r.setStatus(*head, state, desc, url)
}
