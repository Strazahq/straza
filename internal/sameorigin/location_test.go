package sameorigin

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
)

// TestBadLocation pins the text BadLocation depends on against Go's own
// client: a redirect to an unparseable Location is recognised with the origin
// of the request that received it, and other errors are not.
func TestBadLocation(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Location", "http://[::1")
		w.WriteHeader(http.StatusTemporaryRedirect)
	}))
	defer srv.Close()
	resp, err := srv.Client().Post(srv.URL+"/in?token=x", "text/plain", http.NoBody)
	if resp != nil {
		_ = resp.Body.Close()
	}
	u, _ := url.Parse(srv.URL)
	if origin, ok := BadLocation(err); !ok || origin != "http://"+u.Host {
		t.Fatalf("BadLocation(%v) = %q, %v, want %q, true", err, origin, ok, "http://"+u.Host)
	}
	for _, other := range []error{nil, errors.New("failed to parse Location header"),
		&url.Error{Op: "Post", URL: srv.URL, Err: ErrTooManyRedirects}} {
		if _, ok := BadLocation(other); ok {
			t.Errorf("BadLocation(%v) = true, want false", other)
		}
	}
}
