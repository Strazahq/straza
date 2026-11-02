//go:build linux

package service

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/strazahq/straza/internal/agentguard/localapi"
)

// TestIdentifyNamesTheCallerPerRequest pins that the caller comes from the
// connection on each request and never at accept: withPeer reads nothing,
// each of two requests on one connection is named through SO_PEERCRED, and
// a connection the system cannot name is refused with the sentence and log
// line it always had.
func TestIdentifyNamesTheCallerPerRequest(t *testing.T) {
	ln, err := net.Listen("unix", filepath.Join(t.TempDir(), "s.sock"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()
	client, err := net.Dial("unix", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = client.Close() }()
	server, err := ln.Accept()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = server.Close() }()

	ctx := withPeer(context.Background(), server)
	if p, _ := ctx.Value(peerKey{}).(peer); p.conn != server || p.caller != (Caller{}) || p.err != nil {
		t.Fatalf("withPeer stored %+v, want the connection and nothing read from it", p)
	}
	f := &apiDouble{}
	a, _ := testAPI(f, false)
	for range 2 {
		w := httptest.NewRecorder()
		a.routes().ServeHTTP(w, httptest.NewRequest(http.MethodGet, localapi.BaseURL+localapi.RouteStatus, nil).WithContext(ctx))
		if w.Code != http.StatusOK {
			t.Fatalf("answer = %d %s, want 200", w.Code, w.Body)
		}
	}
	want := Caller{UID: strconv.Itoa(os.Getuid()), PID: int32(os.Getpid())}
	if len(f.callers) != 2 || f.callers[0] != want || f.callers[1] != want {
		t.Errorf("callers = %+v, want %+v twice", f.callers, want)
	}

	other, peerEnd := net.Pipe()
	defer func() { _ = other.Close(); _ = peerEnd.Close() }()
	a, logs := testAPI(&apiDouble{}, false)
	w := httptest.NewRecorder()
	a.routes().ServeHTTP(w, httptest.NewRequest(http.MethodGet, localapi.BaseURL+localapi.RouteStatus, nil).
		WithContext(withPeer(context.Background(), other)))
	if w.Code != http.StatusForbidden || refusalOf(t, w) != callerUnknown {
		t.Fatalf("answer = %d %s, want 403 with %q", w.Code, w.Body, callerUnknown)
	}
	lines := logLines(t, logs)
	if len(lines) != 1 || lines[0]["uid"] != "unknown" ||
		!strings.HasPrefix(lines[0]["reason"].(string), "the kernel did not name the caller: the connection is ") {
		t.Errorf("log = %v, want one line with an unknown uid and the reason", lines)
	}
}
