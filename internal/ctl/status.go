// Package ctl implements strazactl subcommand logic, kept separate from the
// cobra wiring so it is unit-testable against httptest servers.
package ctl

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/strazahq/straza/internal/wire"
)

// Status queries a strazad instance and renders a human-readable report to w.
// It returns an error when the server is unreachable or any component is
// degraded, so callers can exit non-zero.
func Status(ctx context.Context, baseURL string, w io.Writer) error {
	client := &http.Client{Timeout: 5 * time.Second}

	// Where am I pointed? First line, before any probe: the answer matters
	// most exactly when the probe below is about to fail.
	fmt.Fprintf(w, "%-10s %s\n", "server", baseURL)

	var ver wire.VersionStatus
	if err := getJSON(ctx, client, baseURL, "/version", &ver); err != nil {
		return err
	}
	fmt.Fprintf(w, "strazad    %s (commit %s, profile %s)\n", ver.Version, ver.Commit, ver.Profile)

	var ready wire.ReadyStatus
	// /readyz answers 503 with a body when degraded; only transport errors
	// and an answer that is not JSON are fatal here.
	if err := getJSON(ctx, client, baseURL, "/readyz", &ready); err != nil {
		return fmt.Errorf("query /readyz: %w", err)
	}

	names := make([]string, 0, len(ready.Components))
	for name := range ready.Components {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		fmt.Fprintf(w, "%-10s %s\n", name, ready.Components[name])
	}
	fmt.Fprintf(w, "status     %s\n", ready.Status)

	if ready.Status != "ok" {
		return fmt.Errorf("strazad is %s", ready.Status)
	}
	return nil
}

// tlsRefusal is the whole body Go's TLS listener answers a plain http
// request with, under HTTP 400.
const tlsRefusal = "Client sent an HTTP request to an HTTPS server."

// getJSON fetches base+path and decodes its JSON body whatever the status.
// A request that went out in full and got no complete answer in time is
// worded as every other strazactl call words it, and any other request that
// got no answer is strazad unreachable at base. An answer that is not JSON
// came from something other than strazad, and its error says what answered
// and what to do. Only an http base that gets Go's TLS
// refusal as the whole body is told to log in again over https; an https
// base that relays it has a proxy in front, which the other sentence names.
func getJSON(ctx context.Context, client *http.Client, base, path string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+path, nil)
	if err != nil {
		return err
	}
	c := &Client{Base: base, HTTP: client}
	resp, err := c.exchange(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := c.readAnswer(req, io.LimitReader(resp.Body, 1<<20))
	var unanswered *unansweredError
	switch {
	case errors.As(err, &unanswered):
		return err
	case err != nil:
		return fmt.Errorf("strazad unreachable at %s: %w", base, err)
	}
	if json.Unmarshal(body, out) == nil {
		return nil
	}
	if strings.HasPrefix(base, "http://") && resp.StatusCode == http.StatusBadRequest &&
		string(bytes.TrimSpace(body)) == tlsRefusal {
		return fmt.Errorf("the server at %s speaks HTTPS now, so it refused this plain http request. "+
			"Log in again with `strazactl login --server https://%s`, which stores the https address for the commands that follow",
			base, strings.TrimPrefix(base, "http://"))
	}
	return fmt.Errorf("%s answered HTTP %d with something that is not strazad's JSON, so a proxy or another program answers at that address. "+
		"Check that the address names the host, port and scheme strazad serves on, and that no proxy in front of strazad answers in its place",
		base, resp.StatusCode)
}
