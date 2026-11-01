//go:build darwin

package service

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"golang.org/x/sys/unix"

	"github.com/strazahq/straza/internal/agentguard/localapi"
)

// TestIdentifyNamesTheCallerPerRequest pins that on macOS the caller comes
// from the connection on each request through LOCAL_PEERCRED: each of two
// requests on one connection is named as the test's own uid, with its pid
// from LOCAL_PEERPID.
func TestIdentifyNamesTheCallerPerRequest(t *testing.T) {
	ln, err := net.Listen("unix", filepath.Join(shortTempDir(t), "s.sock"))
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
}

// TestCallerFromXucred pins how the kernel's peer credentials name a
// caller: version 0 gives the uid in decimal with the pid beside it, a pid
// that could not be read stays 0, and any other version is refused,
// because its layout may differ.
func TestCallerFromXucred(t *testing.T) {
	for _, row := range []struct {
		name    string
		cred    unix.Xucred
		pid     int
		want    Caller
		refused bool
	}{
		{"a person", unix.Xucred{Version: 0, Uid: 501}, 4242, Caller{UID: "501", PID: 4242}, false},
		{"root", unix.Xucred{Version: 0, Uid: 0}, 1, Caller{UID: "0", PID: 1}, false},
		{"no pid", unix.Xucred{Version: 0, Uid: 502}, 0, Caller{UID: "502"}, false},
		{"another version", unix.Xucred{Version: 1, Uid: 501}, 4242, Caller{}, true},
	} {
		t.Run(row.name, func(t *testing.T) {
			got, err := callerFromXucred(&row.cred, row.pid)
			if row.refused != (err != nil) || got != row.want {
				t.Errorf("callerFromXucred = %+v, %v; want %+v, refused %v", got, err, row.want, row.refused)
			}
		})
	}
}
