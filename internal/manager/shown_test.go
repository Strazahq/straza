package manager

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/strazahq/straza/internal/store"
)

// addressSecrets are the parts of a server's address a client, a log
// reader and an audit record never read: a user name, a password with a
// quote in it, a capability in the path, a token in the query and a token
// in the fragment.
var addressSecrets = []string{"svcuser7", "Pw0rdS3cr3t", "Cap9T0AB1CD2E3F4G5H6I7J8K9L0M1N2P3", "Qt0kS3cr3t", "Fr4gS3cr3t"}

// secretAddress is the address of a server on host with every part of
// addressSecrets in it.
func secretAddress(host string) string {
	return "http://svcuser7:It'sPw0rdS3cr3t@" + host + "/mcp/Cap9T0AB1CD2E3F4G5H6I7J8K9L0M1N2P3/v1?session=Qt0kS3cr3t#frag=Fr4gS3cr3t"
}

// lockedBuffer is a buffer the manager's logger may write from any
// goroutine while a test reads it.
type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// TestRingMasksLines pins that the log ring keeps no user information, no
// query, fragment or capability of an address and no credential shape
// redact knows, from Append and from a process's output alike, and that a
// plain line reads as it was written.
func TestRingMasksLines(t *testing.T) {
	t.Parallel()
	cases := []struct{ name, in, want string }{
		{"an address with every secret part", "connected " + secretAddress("mcp.example"), "connected http://mcp.example/mcp/%5BREDACTED%5D/v1?…#…"},
		{"a GitHub token", "using ghp_abcdefghijklmnopqrstuvwxyz0123", "using [REDACTED]"},
		{"a plain line", "helper started on port 8080", "helper started on port 8080"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ring := NewRing(4)
			ring.Append(tc.in)
			_, _ = ring.Write([]byte(tc.in + "\n"))
			for i, line := range ring.Last(0) {
				if line != tc.want {
					t.Errorf("line %d = %q, want %q", i, line, tc.want)
				}
			}
		})
	}
}

// TestStatusReasonMasked pins that no text the manager hands out carries a
// secret part of a server's address: the health reason, the log ring, the
// error of a call and of a token probe, strazad's own log and the manifest
// parser's sentence. Each row of an error also reads the error the
// surface wraps, which quotes the address, as the positive control, and
// finds the manager's wrapper on it, so a row fails when the wrapper is
// gone. A refused address still matches ErrDialRefused, and a command
// server that is not running ErrNotReady, through the wrapper.
func TestStatusReasonMasked(t *testing.T) {
	logs := &lockedBuffer{}
	mgr := New(Options{
		AllowLoopbackUpstreams: true,
		Store:                  testStore(t),
		HealthInterval:         time.Hour,
		Log:                    slog.New(slog.NewTextHandler(logs, nil)),
	})
	t.Cleanup(mgr.stopAll)
	ctx := context.Background()
	up := newUpstreamEcho(t)
	for name, host := range map[string]string{"live": strings.TrimPrefix(up.URL, "http://"), "nohost": ""} {
		if _, err := mgr.Install(ctx, remoteManifest(t, name, `"`+secretAddress(host)+`"`), store.AppSourceAPI); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := mgr.Install(ctx, helperManifest(t, "crashy", nil, EnvVar{Name: "STRAZA_HELPER_CRASH", Value: "1"}), store.AppSourceAPI); err != nil {
		t.Fatal(err)
	}
	waitStatus(t, mgr, "live", StatusRunning)
	waitStatus(t, mgr, "nohost", StatusDegraded)
	up.Close()
	if v, ok := mgr.HealthCheckOne(ctx, "live"); !ok || v.Status != StatusDegraded {
		t.Fatalf("recheck after the upstream closed = %+v, want degraded", v)
	}
	_, liveCall := mgr.Call(ctx, "live", "echo", nil, nil)
	_, noHostCall := mgr.Call(ctx, "nohost", "echo", nil, nil)
	_, crashCall := mgr.Call(ctx, "crashy", "echo", nil, nil)
	liveProbe := mgr.ProbeWith(ctx, "live", nil)
	noHostProbe := mgr.ProbeWith(ctx, "nohost", nil)
	_, parse := Parse([]byte("apiVersion: straza.dev/v1beta1\nkind: App\nmetadata: {name: ftp}\nserver: {name: straza.test/ftp, version: \"1.0.0\"}\n" +
		"straza:\n  runtime:\n    kind: remote\n    remote: {url: \"" + strings.Replace(secretAddress("ftp.example"), "http", "ftp", 1) + "\"}\n"))
	lines := func(name string) string {
		entries, _ := mgr.LogEntries(name, 0)
		var b strings.Builder
		for _, e := range entries {
			b.WriteString(e.Line + "\n")
		}
		return b.String()
	}
	view := func(name string) string { v, _ := mgr.View(name); return v.Detail }
	cases := []struct {
		name string
		text string
		// err is the error the surface answers, which must carry the
		// manager's wrapper; quotes says the error it wraps quotes the
		// address, the positive control; is is a sentinel it must match.
		err    error
		quotes bool
		is     error
	}{
		{name: "the health reason of a server that stopped answering", text: view("live")},
		{name: "the health reason of an address with no host", text: view("nohost")},
		{name: "the log ring of a server that connected and then failed", text: lines("live")},
		{name: "the log ring of an address with no host", text: lines("nohost")},
		{name: "a call to a server that stopped answering", text: errText(liveCall), err: liveCall, quotes: true},
		{name: "a call to an address with no host", text: errText(noHostCall), err: noHostCall, is: ErrDialRefused},
		{name: "a call to a command server that is not running", text: errText(crashCall), err: crashCall, is: ErrNotReady},
		{name: "a token probe of a server that stopped answering", text: errText(liveProbe), err: liveProbe, quotes: true},
		{name: "a token probe of an address with no host", text: errText(noHostProbe), err: noHostProbe, is: ErrDialRefused},
		{name: "strazad's log", text: logs.String()},
		{name: "the parser's sentence for an address that is not http", text: errText(parse)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.text == "" {
				t.Fatal("the surface said nothing, so the test reads nothing")
			}
			for _, secret := range addressSecrets {
				if strings.Contains(tc.text, secret) {
					t.Errorf("%q holds %s", tc.text, secret)
				}
			}
			if tc.err == nil {
				return
			}
			var shown *shownError
			if !errors.As(tc.err, &shown) {
				t.Errorf("%v does not carry the manager's text mask", tc.err)
			}
			if inner := errors.Unwrap(tc.err); tc.quotes && (inner == nil || !strings.Contains(inner.Error(), "svcuser7") ||
				!strings.Contains(inner.Error(), "Qt0kS3cr3t") || !strings.Contains(inner.Error(), "Cap9T0AB1CD2E3F4G5H6I7J8K9L0M1N2P3")) {
				t.Errorf("the wrapped error %v does not quote the address, so the control proves nothing", inner)
			}
			if tc.is != nil && !errors.Is(tc.err, tc.is) {
				t.Errorf("errors.Is(%v, %v) = false", tc.err, tc.is)
			}
		})
	}
	if !strings.Contains(lines("live"), "connected http://127.0.0.1") {
		t.Errorf("the ring lost the connected line's host:\n%s", lines("live"))
	}
}

// errText is the text of err, "" for nil.
func errText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
