package gitremote

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/strazahq/straza/internal/agentguard/localapi"
	"github.com/strazahq/straza/internal/version"
)

// resolveTimeout bounds the resolve call, which may start a session for a
// caller that named no window and may wait for the Straza server.
const resolveTimeout = 30 * time.Second

// noRoutePrefix opens the answer of a service that predates the git door
// to any route it does not know.
const noRoutePrefix = "The Straza background service has no route"

// Sentences about the service and the relay.
const (
	sentUnreachable = "Straza: git cannot reach `straza service` on this machine, so this git command did not run. " +
		"Run `straza doctor` to see why."
	sentTimeout = "Straza: `straza service` did not answer in time, so this git command did not run. " +
		"Run `straza doctor` to see why."
	sentBroken = "Straza: the connection to `straza service` broke during this git command (%v), so it did not finish. " +
		"Run git again, and run `straza doctor` if it keeps failing."
	sentNoRoute = "Straza: the `straza service` on this machine has no git route, because it is older than this " +
		"straza or has not restarted since an upgrade, so this git command did not run. Restart `straza service` or " +
		"upgrade Straza on this machine, then run git again."
	sentUnreadable = "Straza: `straza service` gave an answer to the %s step that git-remote-straza cannot read, so " +
		"this git command did not run. Update Straza on this machine so the helper and the service match."
	sentGrantEnded = "Straza: `straza service` stopped answering before it gave this git command a git grant, so the " +
		"command did not run. Run git again, and run `straza doctor` if it keeps failing."
	sentNoReason = "Straza: the git request for %s answered HTTP %d and gave no reason. Run git again, and run " +
		"`straza doctor` if it keeps failing."
)

// git services and the media types of their smart HTTP requests.
const (
	uploadPack  = "git-upload-pack"
	receivePack = "git-receive-pack"
)

// send makes one request to the service's git door and returns an answer
// with status 200. Any other answer or a transport failure comes back as a
// stop with its sentence. service names the git service the request
// relays, or is empty for the resolve and grant steps.
func (h *helper) send(ctx context.Context, method, route string, body io.Reader, service string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, method, h.base+route, body)
	if err != nil {
		return nil, stopf(sentBroken, err)
	}
	req.Header.Set(localapi.HeaderHarness, h.window.Name)
	req.Header.Set(localapi.HeaderHarnessVersion, h.window.Version)
	if h.window.Session != "" {
		req.Header.Set(localapi.HeaderHarnessSession, h.window.Session)
	}
	req.Header.Set(localapi.HeaderGitURL, h.url)
	req.Header.Set(localapi.HeaderGitOp, h.op)
	req.Header.Set("User-Agent", "git-remote-straza/"+version.Version)
	switch {
	case service == uploadPack:
		req.Header.Set("Git-Protocol", "version=2")
		fallthrough
	case service != "":
		req.Header.Set("Accept", "application/x-"+service+"-result")
		if body != nil {
			req.Header.Set("Content-Type", "application/x-"+service+"-request")
		}
	case body != nil:
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := h.http.Do(req)
	if err != nil {
		return nil, transportStop(ctx, err)
	}
	if resp.StatusCode != http.StatusOK {
		defer func() { _ = resp.Body.Close() }()
		return nil, h.refusal(resp)
	}
	return resp, nil
}

// transportStop words a request that got no answer.
func transportStop(ctx context.Context, err error) error {
	switch {
	case errors.Is(err, localapi.ErrUnreachable):
		return &stop{sentUnreachable}
	case errors.Is(ctx.Err(), context.DeadlineExceeded):
		return &stop{sentTimeout}
	}
	return stopf(sentBroken, err)
}

// refusal words an answer that is not 200. The service's own refusals are
// JSON with a sentence, and the resource server's come through as text.
// An older service answers a git route with its no-route sentence.
func (h *helper) refusal(resp *http.Response) error {
	body, _ := io.ReadAll(io.LimitReader(resp.Body, textMax))
	var r localapi.Refusal
	msg := strings.TrimSpace(string(body))
	if json.Unmarshal(body, &r) == nil {
		msg = r.Error
	}
	msg = oneLine(msg)
	switch {
	case resp.StatusCode == http.StatusNotFound && strings.HasPrefix(msg, noRoutePrefix):
		return &stop{sentNoRoute}
	case msg == "":
		return stopf(sentNoReason, h.url, resp.StatusCode)
	}
	return &stop{clipTo(msg, 2000)}
}

// resolve asks the service whether a Straza server lists the repo.
func (h *helper) resolve(ctx context.Context) (bool, error) {
	ctx, cancel := context.WithTimeout(ctx, resolveTimeout)
	defer cancel()
	resp, err := h.send(ctx, http.MethodPost, localapi.RouteGitResolve, nil, "")
	if err != nil {
		return false, err
	}
	defer func() { _ = resp.Body.Close() }()
	var a localapi.GitResolveAnswer
	if err := json.NewDecoder(io.LimitReader(resp.Body, textMax)).Decode(&a); err != nil {
		return false, stopf(sentUnreadable, "resolve")
	}
	return a.Listed, nil
}

// grant asks the service for a git grant for this run and prints each
// wait line on stderr while an approval is open. The service keeps the
// grant and never returns it. A refusal comes back as a stop with the
// service's sentence.
func (h *helper) grant(ctx context.Context, req localapi.GitGrantRequest) error {
	raw, err := json.Marshal(req)
	if err != nil {
		return stopf(sentUnreadable, "grant")
	}
	resp, err := h.send(ctx, http.MethodPost, localapi.RouteGitGrant, bytes.NewReader(raw), "")
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 0, 4096), textMax)
	for sc.Scan() {
		var line localapi.GitGrantLine
		if err := json.Unmarshal(sc.Bytes(), &line); err != nil {
			return stopf(sentUnreadable, "grant")
		}
		switch {
		case line.Error != "":
			return &stop{clipTo(oneLine(line.Error), 2000)}
		case line.OK:
			return nil
		case line.Wait != "":
			_, _ = fmt.Fprintln(h.errw, oneLine(line.Wait))
		}
	}
	if errors.Is(sc.Err(), bufio.ErrTooLong) {
		return stopf(sentUnreadable, "grant")
	}
	return &stop{sentGrantEnded}
}
