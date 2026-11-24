package entragraph

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// maxBody caps what is read of any answer. A page of members or of a delta
// and a $batch of agent reads are far smaller, and the body is untrusted.
const maxBody = 8 << 20

// errOverCap says an answer was longer than maxBody.
var errOverCap = errors.New("entragraph: the answer is over the size cap")

// answer is one HTTP answer with a status below 300, its body read through
// the cap. token is the token the request carried, so that an error built
// from a part of the body, as from a read inside a $batch, withholds it.
type answer struct {
	status int
	body   []byte
	token  string
}

// call sends one request to Graph as the service app and reads the answer.
// what names the request in every sentence. body, when not nil, goes as
// JSON, and prefer, when not empty, as the Prefer header. A target off the
// Graph host is refused before the token is fetched. A redirect or an answer
// over the cap is an error, and a status of 400 or more an *Error.
func (c *Client) call(ctx context.Context, what, method, target string, body any, prefer string) (answer, error) {
	u, err := url.Parse(target)
	if err != nil || !c.onGraph(u) {
		return answer{}, errorf("Straza did not send %s, because its address is not on %s, the only host that gets its Graph token.", what, c.graph.Host)
	}
	token, _, err := c.bearer(ctx)
	if err != nil {
		return answer{}, err
	}
	var payload io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return answer{}, errorf("Straza could not write the body of %s: %w.", what, err)
		}
		payload = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(ctx, method, u.String(), payload)
	if err != nil {
		return answer{}, errorf("Straza could not build %s: %w.", what, err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if prefer != "" {
		req.Header.Set("Prefer", prefer)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return answer{}, unreached(what, c.graph.Host, err)
	}
	defer func() { _ = resp.Body.Close() }()
	data, err := readCapped(resp.Body)
	switch {
	case err != nil:
		return answer{}, readFailed(what, c.graph.Host, err)
	case resp.StatusCode >= 300 && resp.StatusCode < 400:
		return answer{}, errorf("Microsoft Graph answered %s with a redirect, which Straza never follows with its token, so it counts the request as failed.", what)
	case resp.StatusCode >= 400:
		return answer{}, graphError(what, resp, data, token)
	}
	return answer{status: resp.StatusCode, body: data, token: token}, nil
}

// onGraph says whether u is on the Graph endpoint: the same scheme and host,
// so https unless the endpoint itself is http, as a test server is.
func (c *Client) onGraph(u *url.URL) bool {
	return u.Scheme == c.graph.Scheme && strings.EqualFold(u.Host, c.graph.Host)
}

// checkLink refuses a paging or delta link that is not on the Graph
// endpoint, before it is followed or handed to the caller.
func (c *Client) checkLink(what, link string) error {
	u, err := url.Parse(link)
	if err != nil || !c.onGraph(u) {
		return errorf("Microsoft Graph answered %s with a link that is not on %s, so Straza did not follow it and counts the read as failed.", what, c.graph.Host)
	}
	return nil
}

// page is one page of a Graph collection.
type page struct {
	Value     json.RawMessage `json:"value"`
	NextLink  string          `json:"@odata.nextLink"`
	DeltaLink string          `json:"@odata.deltaLink"`
}

// walk reads a paged collection from link to its last page and hands the
// value list of each page to each, in order. A plain read ends at the first
// page without a next link. A delta read ends at the page that names a
// delta link, which it returns, and a page with neither link is an error.
// Any failure on any page ends the walk with that error.
func (c *Client) walk(ctx context.Context, what, link string, delta bool, each func(value json.RawMessage) error) (string, error) {
	for {
		ans, err := c.call(ctx, what, http.MethodGet, link, nil, "")
		if err != nil {
			return "", err
		}
		if ans.status != http.StatusOK {
			return "", unexpected(what, ans.status, "200")
		}
		var p page
		if json.Unmarshal(ans.body, &p) != nil || len(p.Value) == 0 || string(p.Value) == "null" {
			return "", errorf("Microsoft Graph answered %s with a page that holds no list, so Straza counts the read as incomplete.", what)
		}
		if err := each(p.Value); err != nil {
			return "", err
		}
		switch {
		case p.NextLink != "":
			if err := c.checkLink(what, p.NextLink); err != nil {
				return "", err
			}
			link = p.NextLink
		case !delta:
			return "", nil
		case p.DeltaLink != "":
			if err := c.checkLink(what, p.DeltaLink); err != nil {
				return "", err
			}
			return p.DeltaLink, nil
		default:
			return "", errorf("Microsoft Graph ended %s with a page that names neither a next link nor a delta link, so Straza counts the read as incomplete.", what)
		}
	}
}

// readCapped reads a body up to maxBody and fails with errOverCap past it.
func readCapped(r io.Reader) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(r, maxBody+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxBody {
		return nil, errOverCap
	}
	return data, nil
}

// unreached is the error for a request that got no HTTP answer. The cause is
// the transport's error under the URL, which a delta link makes long.
func unreached(what, host string, err error) error {
	var ue *url.Error
	if errors.As(err, &ue) {
		err = ue.Err
	}
	return errorf("Straza got no answer from %s to %s: %w. Check that strazad can reach %s, then Straza tries again.", host, what, err, host)
}

// readFailed is the error for an answer whose body could not be read whole.
func readFailed(what, host string, err error) error {
	if errors.Is(err, errOverCap) {
		return errorf("%s answered %s with more than 8 MiB, which no answer Straza asks for needs, so Straza counts the request as failed.", host, what)
	}
	return errorf("Straza could not read the answer of %s to %s: %w. Straza tries again.", host, what, err)
}

// unexpected is the error for a status below 300 that the request does not
// expect, so the call does not count as done.
func unexpected(what string, status int, want string) error {
	return errorf("Microsoft Graph answered %s with HTTP %d, where Straza expects %s, so Straza does not count it as done.", what, status, want)
}

// errorf builds this package's errors. They are whole sentences for an
// operator, so they start with a capital letter, and %w keeps the cause.
func errorf(format string, args ...any) error {
	return fmt.Errorf(format, args...)
}
