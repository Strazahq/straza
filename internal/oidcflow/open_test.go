package oidcflow

import (
	"bytes"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"
)

// TestCanOpenBrowser pins the can-open rule: $BROWSER anywhere, macOS and
// Windows always, and elsewhere only a session with DISPLAY or
// WAYLAND_DISPLAY.
func TestCanOpenBrowser(t *testing.T) {
	cases := []struct {
		name string
		goos string
		env  map[string]string
		want bool
	}{
		{"linux with nothing", "linux", nil, false},
		{"linux over X", "linux", map[string]string{"DISPLAY": ":0"}, true},
		{"linux on Wayland", "linux", map[string]string{"WAYLAND_DISPLAY": "wayland-0"}, true},
		{"linux with BROWSER", "linux", map[string]string{"BROWSER": "/usr/local/bin/follow"}, true},
		{"freebsd with nothing", "freebsd", nil, false},
		{"macOS", "darwin", nil, true},
		{"Windows", "windows", nil, true},
		{"empty values count as unset", "linux", map[string]string{"DISPLAY": "", "BROWSER": ""}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := CanOpenBrowser(tc.goos, func(k string) string { return tc.env[k] })
			if got != tc.want {
				t.Fatalf("CanOpenBrowser = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestBrowserCommand pins the program each system opens a link with:
// $BROWSER with the link as its only argument wherever it is set.
func TestBrowserCommand(t *testing.T) {
	const link = "https://idp.example/authorize?a=1&b=2"
	cases := []struct {
		goos, browser string
		want          []string
	}{
		{"linux", "", []string{"xdg-open", link}},
		{"freebsd", "", []string{"xdg-open", link}},
		{"darwin", "", []string{"open", link}},
		{"windows", "", []string{"rundll32", "url.dll,FileProtocolHandler", link}},
		{"linux", "/opt/rig/follow --flag", []string{"/opt/rig/follow --flag", link}},
		{"windows", `C:\rig\follow.exe`, []string{`C:\rig\follow.exe`, link}},
	}
	for _, tc := range cases {
		name, args := browserCommand(tc.goos, tc.browser, link)
		if got := append([]string{name}, args...); !slices.Equal(got, tc.want) {
			t.Errorf("%s with BROWSER=%q: %q, want %q", tc.goos, tc.browser, got, tc.want)
		}
	}
}

// TestOpenBrowserRunsBROWSER runs the opener with a $BROWSER that records
// its arguments: it gets the link as its only argument, the opener does
// not wait for it, and the person reads the opened sentence with the link.
func TestOpenBrowserRunsBROWSER(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the recorder is a shell script")
	}
	dir := t.TempDir()
	record := filepath.Join(dir, "args")
	script := filepath.Join(dir, "browser")
	body := "#!/bin/sh\nfor a in \"$@\"; do printf '%s\\n' \"$a\"; done > " + record + ".tmp\nmv " + record + ".tmp " + record + "\nsleep 5\n"
	if err := os.WriteFile(script, []byte(body), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("BROWSER", script)
	const link = "https://idp.example/authorize?client_id=straza&state=s1"
	var out bytes.Buffer
	start := time.Now()
	OpenBrowser(&out, link)
	if took := time.Since(start); took > 2*time.Second {
		t.Fatalf("the opener waited %v for the browser to exit", took)
	}
	want := "Straza opened your browser to sign in at https://idp.example. If no browser opened, open this link on this computer:\n  " + link + "\n"
	if out.String() != want {
		t.Errorf("printed %q\nwant %q", out.String(), want)
	}
	// The browser runs in the background, and macOS may check a script it
	// has not seen before it first runs, so the wait allows ten seconds.
	var got []byte
	for range 500 {
		if b, err := os.ReadFile(record); err == nil {
			got = b
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if string(got) != link+"\n" {
		t.Fatalf("BROWSER got %q, want the link as its only argument", got)
	}
}

// TestOpenBrowserFailures pins what the person reads when no browser
// starts, and that a link which is not http or https is never handed to
// one nor offered to open by hand.
func TestOpenBrowserFailures(t *testing.T) {
	t.Setenv("BROWSER", filepath.Join(t.TempDir(), "no-such-browser"))
	var out bytes.Buffer
	OpenBrowser(&out, "https://idp.example/authorize?x=1")
	if got := out.String(); !strings.HasPrefix(got, "Straza could not open a browser here: ") ||
		!strings.HasSuffix(got, ". Open this link in a browser on this computer to sign in:\n  https://idp.example/authorize?x=1\n") {
		t.Errorf("printed %q", got)
	}
	out.Reset()
	OpenBrowser(&out, "file:///etc/passwd")
	if got := out.String(); got != "Straza did not open the sign-in link \"file:///etc/passwd\", because it is not an http or https address. Press Ctrl-C and tell your Straza admin.\n" {
		t.Errorf("printed %q", got)
	}
}
