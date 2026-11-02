package oidcflow

import (
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"runtime"
)

// CanOpenBrowser reports whether a browser can be opened for the person on
// this machine: $BROWSER is set, the system is macOS or Windows, or the
// session has DISPLAY or WAYLAND_DISPLAY. goos is runtime.GOOS and getenv
// is os.Getenv.
func CanOpenBrowser(goos string, getenv func(string) string) bool {
	switch {
	case getenv("BROWSER") != "", goos == "darwin", goos == "windows":
		return true
	}
	return getenv("DISPLAY") != "" || getenv("WAYLAND_DISPLAY") != ""
}

// OpenBrowser starts the person's browser at link without waiting for it,
// and tells the person on w, with the link to open by hand if no browser
// shows. A link that is not http or https is never opened.
func OpenBrowser(w io.Writer, link string) {
	u, err := url.Parse(link)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
		fmt.Fprintf(w, "Straza did not open the sign-in link %q, because it is not an http or https address. Press Ctrl-C and tell your Straza admin.\n", link)
		return
	}
	name, args := browserCommand(runtime.GOOS, os.Getenv("BROWSER"), link)
	cmd := exec.Command(name, args...) // #nosec G204 G702 -- the person's own $BROWSER or the system opener, with the link as one argument
	if err := cmd.Start(); err != nil {
		fmt.Fprintf(w, "Straza could not open a browser here: %v. Open this link in a browser on this computer to sign in:\n  %s\n", err, link)
		return
	}
	go func() { _ = cmd.Wait() }()
	fmt.Fprintf(w, "Straza opened your browser to sign in at %s://%s. If no browser opened, open this link on this computer:\n  %s\n", u.Scheme, u.Host, link)
}

// browserCommand is the program that opens link on goos: browser, the
// value of $BROWSER, with the link as its only argument when it is set.
func browserCommand(goos, browser, link string) (string, []string) {
	switch {
	case browser != "":
		return browser, []string{link}
	case goos == "darwin":
		return "open", []string{link}
	case goos == "windows":
		return "rundll32", []string{"url.dll,FileProtocolHandler", link}
	}
	return "xdg-open", []string{link}
}
