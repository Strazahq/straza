package ctl

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/strazahq/straza/internal/wire"
)

// TestTransportErrorTellsATimeoutFromNoConnection pins how strazactl words a
// request that got no answer. When the request went out in full and no
// answer came, or the answer did not finish, within the client's timeout,
// the sentence says so. For a change it says that the change may still be
// running on strazad and how to check before the command runs again, and
// for a read that running it again is safe. strazactl status reads through
// the same words. When nothing listens at the address, or the connection is
// not made before the timeout, strazad stays unreachable.
func TestTransportErrorTellsATimeoutFromNoConnection(t *testing.T) {
	t.Parallel()
	hold := func(t *testing.T, headersFirst bool) string {
		release := make(chan struct{})
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if headersFirst {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusOK)
				w.(http.Flusher).Flush()
			}
			select {
			case <-r.Context().Done():
			case <-release:
			}
		}))
		t.Cleanup(srv.Close)
		t.Cleanup(func() { close(release) })
		return srv.URL
	}
	held := func(t *testing.T) string { return hold(t, false) }
	headersOnly := func(t *testing.T) string { return hold(t, true) }
	closed := func(t *testing.T) string {
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		base := "http://" + ln.Addr().String()
		if err := ln.Close(); err != nil {
			t.Fatal(err)
		}
		return base
	}
	sent := func(request, base, answer string) string {
		return "strazactl sent the request " + request + " to strazad at " + base + " in full and got " + answer + " within 300ms. " +
			"The change may still be running on strazad. " +
			"Check whether it took effect with the matching strazactl show or list command before you run this command again"
	}
	read := func(request, base string) string {
		return "strazactl sent the request " + request + " to strazad at " + base + " in full and got no answer within 300ms. " +
			"A read changes nothing on strazad, so it is safe to run the command again"
	}
	ctx := context.Background()
	cases := []struct {
		name string
		base func(t *testing.T) string
		// dialHangs makes every dial wait until the request gives up.
		dialHangs bool
		call      func(c *Client) error
		// want answers the whole sentence, or its start when prefix is set.
		want   func(base string) string
		prefix bool
	}{
		{"strazad took a request and did not answer", held, false,
			func(c *Client) error { return c.Do(ctx, http.MethodPost, "/v1/admin/apps/lf-echo/health", nil, nil) },
			func(base string) string { return sent("POST /v1/admin/apps/lf-echo/health", base, "no answer") }, false},
		{"strazad took an upload and did not answer", held, false,
			func(c *Client) error {
				return c.DoRawBody(ctx, http.MethodPost, "/v1/admin/apps", "application/yaml", []byte("kind: App\n"), nil)
			},
			func(base string) string { return sent("POST /v1/admin/apps", base, "no answer") }, false},
		{"strazad sent the headers of an answer and not its body", headersOnly, false,
			func(c *Client) error { return c.Do(ctx, http.MethodPost, "/v1/admin/apps/lf-echo/health", nil, nil) },
			func(base string) string {
				return sent("POST /v1/admin/apps/lf-echo/health", base, "no complete answer")
			}, false},
		{"strazad took a read and did not answer", held, false,
			func(c *Client) error { return c.Do(ctx, http.MethodGet, "/v1/admin/apps", nil, nil) },
			func(base string) string { return read("GET /v1/admin/apps", base) }, false},
		{"strazactl status took no answer", held, false,
			func(c *Client) error {
				var ver wire.VersionStatus
				return getJSON(ctx, c.HTTP, c.Base, "/version", &ver)
			},
			func(base string) string { return read("GET /version", base) }, false},
		{"nothing listens at the address", closed, false,
			func(c *Client) error { return c.Do(ctx, http.MethodGet, "/v1/admin/apps", nil, nil) },
			func(base string) string { return "strazad unreachable at " + base + ": " }, true},
		{"the connection is not made before the timeout", func(*testing.T) string { return "http://127.0.0.1:1" }, true,
			func(c *Client) error { return c.Do(ctx, http.MethodGet, "/v1/admin/apps", nil, nil) },
			func(base string) string { return "strazad unreachable at " + base + ": " }, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			base := tc.base(t)
			c := loggedInClient(t, base)
			c.HTTP.Timeout = 300 * time.Millisecond
			if tc.dialHangs {
				c.HTTP.Transport = &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
					<-ctx.Done()
					return nil, ctx.Err()
				}}
			}
			err := tc.call(c)
			if err == nil {
				t.Fatal("the call answered nil, want an error")
			}
			want := tc.want(base)
			if tc.prefix && !strings.HasPrefix(err.Error(), want) {
				t.Errorf("error = %q, want it to start %q", err, want)
			}
			if !tc.prefix && err.Error() != want {
				t.Errorf("error = %q, want %q", err, want)
			}
		})
	}
}
