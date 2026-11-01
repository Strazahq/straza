package gitremote

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/strazahq/straza/internal/agentguard/localapi"
)

// copyBuf is the chunk size of every relayed stream.
const copyBuf = 32 << 10

// Sentences about the git conversation itself.
const (
	sentNotV2 = "Straza: the git server behind Straza did not answer %s in git protocol version 2, which Straza needs " +
		"for a fetch, so this git command did not run. Ask an administrator to check the MCP server that serves git " +
		"for this repo."
	sentAdvert = "Straza: the git server behind Straza sent an advertisement for %s that git-remote-straza cannot read " +
		"(%v), so this git command did not run. Ask an administrator to check the MCP server that serves git for this repo."
	sentPushHead = "Straza: git-remote-straza could not read the push commands git sent (%v), so this push did not run. " +
		"Update git, then run git push again."
	sentHeadTooLarge = "Straza: this push names more ref updates than git-remote-straza reads, which is 1 MiB of push " +
		"commands, so it did not run. Push fewer branches at a time."
	sentRequest = "Straza: git sent a fetch request that git-remote-straza cannot read (%v), so this git command did " +
		"not finish. Update git, then run git again."
)

// fetch relays git-upload-pack as protocol v2: one fetch grant for the
// run, the capability advertisement, then each request git writes, up to
// its flush, as one POST whose answer goes back to git as it streams.
func (h *helper) fetch(ctx context.Context) error {
	if err := h.grant(ctx, localapi.GitGrantRequest{Op: "fetch"}); err != nil {
		return err
	}
	first, rest, err := h.advertise(ctx, uploadPack)
	if err != nil {
		return err
	}
	defer func() { _ = rest.Close() }()
	if !bytes.Equal(bytes.TrimSuffix(first.payload(), []byte("\n")), []byte("version 2")) {
		return stopf(sentNotV2, h.url)
	}
	if err := h.ready(first); err != nil {
		return err
	}
	if err := h.copyOut(rest); err != nil {
		return err
	}
	for {
		p, err := readPkt(h.in)
		switch {
		case errors.Is(err, io.EOF):
			return nil
		case err != nil:
			return stopf(sentRequest, err)
		case p.kind == pktFlush:
			// A flush where a request would start is how a protocol v2
			// client ends the conversation.
			return nil
		case p.kind != pktData:
			return stopf(sentRequest, "a request opens with a special pkt-line")
		}
		body := &untilFlush{r: h.in}
		resp, err := h.send(ctx, http.MethodPost, localapi.RouteGit+uploadPack, io.MultiReader(bytes.NewReader(p.raw), body), uploadPack)
		if err != nil {
			if body.err != nil {
				return stopf(sentRequest, body.err)
			}
			return err
		}
		err = h.copyOut(resp.Body)
		_ = resp.Body.Close()
		if err != nil {
			return err
		}
	}
}

// push relays git-receive-pack. A grant without refs opens the
// advertisement, then the helper reads the commands, takes the grant that
// names exactly those updates, and posts the commands it read followed by
// the rest of stdin, the pack. When every command is a delete git sends no
// pack and keeps stdin open, so the helper posts the commands alone. A
// refused grant answers git a report that refuses each ref with the
// service's sentence.
func (h *helper) push(ctx context.Context) error {
	if err := h.grant(ctx, localapi.GitGrantRequest{Op: "push"}); err != nil {
		return err
	}
	first, rest, err := h.advertise(ctx, receivePack)
	if err != nil {
		return err
	}
	err = h.ready(first)
	if err == nil {
		err = h.copyOut(rest)
	}
	_ = rest.Close()
	if err != nil {
		return err
	}
	head, err := readPushHead(h.in)
	switch {
	case errors.Is(err, errHeadTooLarge):
		return &stop{sentHeadTooLarge}
	case err != nil:
		return stopf(sentPushHead, err)
	case len(head.refs) == 0:
		// Nothing to push. git prints "Everything up-to-date".
		return nil
	}
	if err := h.grant(ctx, localapi.GitGrantRequest{Op: "push", Refs: head.refs}); err != nil {
		return h.refuse(head, err)
	}
	var body io.Reader = bytes.NewReader(head.raw)
	if !head.deleteOnly() {
		body = io.MultiReader(body, h.in)
	}
	resp, err := h.send(ctx, http.MethodPost, localapi.RouteGit+receivePack, body, receivePack)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	return h.copyOut(resp.Body)
}

// refuse answers a push whose grant was refused. It reads the pack to its
// end, so git is not left blocked on a full pipe, and then writes the
// receive-pack report that refuses each ref. Without a report to write, the
// sentence goes to stderr.
func (h *helper) refuse(head pushHead, err error) error {
	var s *stop
	if !errors.As(err, &s) {
		return err
	}
	if !head.deleteOnly() {
		if _, err := io.Copy(io.Discard, h.in); err != nil {
			return stopf(sentGitRead, err)
		}
	}
	report := refusalReport(head, s.sentence)
	if report == nil {
		return s
	}
	return h.write(report)
}

// ready answers git's connect with the empty line, then sends the first
// pkt-line of the advertisement in a write of its own. git reads the empty
// line through an unbuffered stream that it discards afterwards, and on
// Windows such a stream reads two bytes at a time, so a pkt-line byte in the
// same write would be lost. git is already waiting when the empty line
// arrives, because the advertisement took a round trip, so its read ends
// with that line alone.
func (h *helper) ready(first pkt) error {
	if err := h.write([]byte("\n")); err != nil {
		return err
	}
	return h.write(first.raw)
}

// advertise gets the service's advertisement for a git service and drops
// the `# service=` header that smart HTTP puts before it. It returns the
// first pkt-line after the header and the rest of the body.
func (h *helper) advertise(ctx context.Context, service string) (pkt, io.ReadCloser, error) {
	resp, err := h.send(ctx, http.MethodGet, localapi.RouteGit+"info/refs?service="+service, nil, service)
	if err != nil {
		return pkt{}, nil, err
	}
	first, err := readPkt(resp.Body)
	if err == nil && strings.HasPrefix(string(first.payload()), "# service=") {
		var flush pkt
		if flush, err = readPkt(resp.Body); err == nil && flush.kind != pktFlush {
			err = errors.New("no flush after the service header")
		}
		if err == nil {
			first, err = readPkt(resp.Body)
		}
	}
	if err != nil {
		_ = resp.Body.Close()
		if errors.Is(err, io.EOF) {
			err = errors.New("the advertisement is empty")
		}
		return pkt{}, nil, stopf(sentAdvert, h.url, err)
	}
	return first, resp.Body, nil
}

// copyOut streams r to git in copyBuf chunks.
func (h *helper) copyOut(r io.Reader) error {
	_, err := io.CopyBuffer(h.out, r, make([]byte, copyBuf))
	if err != nil {
		return stopf(sentBroken, fmt.Errorf("relay to git: %w", err))
	}
	return nil
}
