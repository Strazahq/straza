package ctl

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/strazahq/straza/internal/wire"
)

func fakeStrazad(t *testing.T, readyCode int, readyBody string) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/version", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"version":"v0.1.0","commit":"abc1234","go":"go1.24","os":"linux","arch":"amd64","profile":"standalone"}`))
	})
	mux.HandleFunc("/readyz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(readyCode)
		_, _ = w.Write([]byte(readyBody))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func TestStatusHealthy(t *testing.T) {
	srv := fakeStrazad(t, http.StatusOK, `{"status":"ok","components":{"store":"ok","bus":"ok"}}`)
	var out strings.Builder
	if err := Status(context.Background(), srv.URL, &out); err != nil {
		t.Fatalf("Status: %v", err)
	}
	got := out.String()
	for _, want := range []string{"v0.1.0", "standalone", "store", "bus", "status     ok"} {
		if !strings.Contains(got, want) {
			t.Errorf("output missing %q:\n%s", want, got)
		}
	}
}

func TestStatusDegraded(t *testing.T) {
	srv := fakeStrazad(t, http.StatusServiceUnavailable, `{"status":"degraded","components":{"store":"ok","bus":"nats gone"}}`)
	var out strings.Builder
	err := Status(context.Background(), srv.URL, &out)
	if err == nil || !strings.Contains(err.Error(), "degraded") {
		t.Fatalf("want degraded error, got %v", err)
	}
	if !strings.Contains(out.String(), "nats gone") {
		t.Errorf("output should show the failing component:\n%s", out.String())
	}
}

func TestStatusUnreachable(t *testing.T) {
	var out strings.Builder
	err := Status(context.Background(), "http://127.0.0.1:1", &out)
	if err == nil || !strings.Contains(err.Error(), "unreachable") {
		t.Fatalf("want unreachable error, got %v", err)
	}
}

// TestStatusAnswerThatIsNotStrazad pins what status says when the address
// answers with something other than strazad's JSON. Go's TLS listener asked
// over plain http gets the sentence that names the https login, any other
// answer gets its status and what to check, and neither calls strazad
// unreachable. An https front that relays Go's refusal, and a page that
// only quotes it, get the proxy sentence. A strazad that answers JSON is the
// positive control.
func TestStatusAnswerThatIsNotStrazad(t *testing.T) {
	tlsSrv := httptest.NewTLSServer(http.NotFoundHandler())
	t.Cleanup(tlsSrv.Close)
	plain := "http://" + tlsSrv.Listener.Addr().String()
	answering := func(code int, body string) *httptest.Server {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "text/html")
			w.WriteHeader(code)
			_, _ = w.Write([]byte(body))
		}))
		t.Cleanup(srv.Close)
		return srv
	}
	htmlSrv := answering(http.StatusOK, "<!doctype html><html><body>Welcome to nginx!</body></html>")
	quoting := answering(http.StatusBadRequest, "<html><body>Client sent an HTTP request to an HTTPS server.</body></html>")
	relay := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte("Client sent an HTTP request to an HTTPS server.\n"))
	}))
	t.Cleanup(relay.Close)
	healthy := fakeStrazad(t, http.StatusOK, `{"status":"ok","components":{"store":"ok"}}`)
	proxy := func(base string, code int) string {
		return base + " answered HTTP " + strconv.Itoa(code) + " with something that is not strazad's JSON, so a proxy or another program answers at that address. " +
			"Check that the address names the host, port and scheme strazad serves on, and that no proxy in front of strazad answers in its place"
	}

	cases := []struct {
		name   string
		base   string
		client *http.Client // set: getJSON with this client, which trusts the test certificate
		want   string
	}{
		{"a TLS listener asked over plain http", plain, nil,
			"the server at " + plain + " speaks HTTPS now, so it refused this plain http request. " +
				"Log in again with `strazactl login --server https://" + tlsSrv.Listener.Addr().String() +
				"`, which stores the https address for the commands that follow"},
		{"an HTML page", htmlSrv.URL, nil, proxy(htmlSrv.URL, http.StatusOK)},
		{"an HTML page that quotes Go's refusal", quoting.URL, nil, proxy(quoting.URL, http.StatusBadRequest)},
		{"an https front that relays Go's refusal", relay.URL, relay.Client(), proxy(relay.URL, http.StatusBadRequest)},
		{"strazad", healthy.URL, nil, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var err error
			if tc.client != nil {
				var ver wire.VersionStatus
				err = getJSON(context.Background(), tc.client, tc.base, "/version", &ver)
			} else {
				var out strings.Builder
				err = Status(context.Background(), tc.base, &out)
			}
			if tc.want == "" {
				if err != nil {
					t.Fatalf("Status: %v", err)
				}
				return
			}
			if err == nil || err.Error() != tc.want {
				t.Fatalf("Status error\n got %v\nwant %s", err, tc.want)
			}
		})
	}
}
