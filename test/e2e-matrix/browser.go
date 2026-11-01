package e2ematrix

import (
	"context"
	"errors"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// enrollBrowser runs `straza enroll` on the browser lane. BROWSER names a
// helper that writes the link it is started with to a file, which is all a
// browser does for the client. The runner then plays the person's browser:
// it opens the link at the built-in issuer, signs in with the declared
// password, and follows the redirect to the service's loopback port.
func (c *client) enrollBrowser(ctx context.Context) error {
	helper, linkFile, err := writeBrowserHelper(filepath.Dir(c.home))
	if err != nil {
		return err
	}
	p, err := startProc("straza enroll", c.bin, []string{"enroll", "--server", c.server.URL}, c.env("BROWSER="+helper), nil)
	if err != nil {
		return err
	}
	defer p.stop()
	deadline := time.Now().Add(20 * time.Second)
	var link string
	for {
		var ok bool
		if link, ok, err = readLink(linkFile); err != nil || ok {
			break
		}
		if p.exited() {
			return fmt.Errorf("straza enroll as %s exited before it opened a browser: %s", c.name, trim(redact(p.out.String())))
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("straza enroll as %s did not start BROWSER within 20s: %s", c.name, trim(redact(p.out.String())))
		}
		time.Sleep(25 * time.Millisecond)
	}
	if err != nil {
		return err
	}
	if err := browserSignIn(ctx, link, c.name, c.password); err != nil {
		return fmt.Errorf("browser sign-in as %s: %w", c.name, err)
	}
	if err := p.wait(60 * time.Second); err != nil {
		return fmt.Errorf("straza enroll as %s: %v: %s", c.name, err, trim(redact(p.out.String())))
	}
	if !strings.Contains(p.out.String(), link) {
		return fmt.Errorf("straza enroll as %s did not print the link it opened: %s", c.name, trim(redact(p.out.String())))
	}
	return nil
}

// writeBrowserHelper writes the BROWSER helper under dir and returns its
// path and the file it writes. The helper writes every argument it gets on a
// line of its own and renames the file into place, so the runner never reads
// half a link.
func writeBrowserHelper(dir string) (helper, linkFile string, err error) {
	helper, linkFile = filepath.Join(dir, "browser.sh"), filepath.Join(dir, "browser.link")
	if strings.ContainsRune(dir, '\'') {
		return "", "", fmt.Errorf("the run directory %s holds a quote, which the BROWSER helper cannot name", dir)
	}
	script := fmt.Sprintf("#!/bin/sh\nprintf '%%s\\n' \"$@\" > '%s.tmp' && mv '%s.tmp' '%s'\n", linkFile, linkFile, linkFile)
	return helper, linkFile, os.WriteFile(helper, []byte(script), 0o700) // #nosec G306 -- the helper must be executable, under the run's own temp dir
}

// readLink reports the link the helper wrote, false while it has not run,
// and an error when the helper got anything but the link alone.
func readLink(linkFile string) (string, bool, error) {
	raw, err := os.ReadFile(linkFile) // #nosec G304 -- the helper's file under the run's temp dir
	if errors.Is(err, os.ErrNotExist) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	args := strings.Split(strings.TrimSuffix(string(raw), "\n"), "\n")
	if len(args) != 1 || args[0] == "" {
		return "", false, fmt.Errorf("the client started BROWSER with %d arguments, want the link alone: %q", len(args), args)
	}
	return args[0], true, nil
}

var (
	formRE  = regexp.MustCompile(`(?is)<form\b([^>]*)>(.*?)(?:</form>|$)`)
	inputRE = regexp.MustCompile(`(?is)<input\b([^>]*)>`)
	attrRE  = regexp.MustCompile(`(?is)(?:^|\s)(action|name|value)\s*=\s*"([^"]*)"`)
)

// browserSignIn plays a person's browser at the issuer: it opens the
// authorization link, posts the sign-in form it finds there with the
// person's password, and follows the issuer's redirect to the service. The
// redirect must be the loopback callback the link named, with a code and
// the link's own state, so a code never travels anywhere else. The password
// and the code are never part of an error.
func browserSignIn(ctx context.Context, link, username, password string) error {
	start, err := url.Parse(link)
	if err != nil {
		return fmt.Errorf("the link %q does not parse: %w", link, err)
	}
	jar, _ := cookiejar.New(nil)
	hc := &http.Client{Timeout: 30 * time.Second, Jar: jar,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	_, page, err := browse(ctx, hc, http.MethodGet, link, nil)
	if err != nil {
		return err
	}
	form := formRE.FindStringSubmatch(string(page))
	if form == nil {
		return fmt.Errorf("the sign-in page at %s carries no form: %s", start.Path, trim(string(page)))
	}
	action, err := start.Parse(attrs(form[1])["action"])
	if err != nil {
		return fmt.Errorf("the sign-in form's action does not parse: %w", err)
	}
	fields := url.Values{}
	for _, in := range inputRE.FindAllStringSubmatch(form[2], -1) {
		if a := attrs(in[1]); a["name"] != "" {
			fields.Set(a["name"], a["value"])
		}
	}
	fields.Set("username", username)
	fields.Set("password", password)
	var resp *http.Response
	// The issuer throttles password submits per client IP and answers 429,
	// which a run that signs people in step after step meets, so the runner
	// waits it out a few times like a person would.
	for attempt := 0; ; attempt++ {
		resp, page, err = browse(ctx, hc, http.MethodPost, action.String(), fields)
		if err != nil || resp.StatusCode != http.StatusTooManyRequests || attempt >= 6 {
			break
		}
		time.Sleep(2 * time.Second)
	}
	if err != nil {
		return err
	}
	if resp.StatusCode/100 != 3 {
		return fmt.Errorf("the sign-in form at %s answered status %d instead of a redirect: %s", action.Path, resp.StatusCode, trim(string(page)))
	}
	to, err := resp.Location()
	if err != nil {
		return fmt.Errorf("the sign-in form at %s redirected without a usable Location: %w", action.Path, err)
	}
	want := start.Query().Get("redirect_uri")
	if got := to.Scheme + "://" + to.Host + to.Path; got != want || !strings.HasPrefix(want, "http://127.0.0.1:") || !strings.HasSuffix(want, "/callback") ||
		to.Query().Get("code") == "" || to.Query().Get("state") != start.Query().Get("state") {
		return fmt.Errorf("the issuer redirected to %s, want the link's redirect_uri %s on 127.0.0.1 with a code and the state of the link", got, want)
	}
	resp, page, err = browse(ctx, hc, http.MethodGet, to.String(), nil)
	if err != nil {
		return fmt.Errorf("the service's callback at %s: %w", want, err)
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("the service's callback answered status %d: %s", resp.StatusCode, trim(string(page)))
	}
	return nil
}

// browse sends one browser request without following a redirect and
// returns the answer and its body. The error names the method and path,
// never the query, which carries the code on the callback.
func browse(ctx context.Context, hc *http.Client, method, target string, form url.Values) (*http.Response, []byte, error) {
	var body io.Reader
	if form != nil {
		body = strings.NewReader(form.Encode())
	}
	req, err := http.NewRequestWithContext(ctx, method, target, body)
	if err != nil {
		return nil, nil, err
	}
	if form != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	resp, err := hc.Do(req)
	if err != nil {
		return nil, nil, fmt.Errorf("%s %s: %w", method, req.URL.Path, errors.Unwrap(err))
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	return resp, raw, err
}

// attrs reads the action, name and value attributes of one tag, unescaped
// the way a browser reads them.
func attrs(tag string) map[string]string {
	out := map[string]string{}
	for _, m := range attrRE.FindAllStringSubmatch(tag, -1) {
		out[strings.ToLower(m[1])] = html.UnescapeString(m[2])
	}
	return out
}
