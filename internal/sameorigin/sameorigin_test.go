package sameorigin

import (
	"errors"
	"net/http"
	"strings"
	"testing"
)

// hop builds a request to target, with a bearer and a body when asked.
func hop(t *testing.T, method, target, bearer, body string) *http.Request {
	t.Helper()
	req, err := http.NewRequest(method, target, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if body == "" {
		req, _ = http.NewRequest(method, target, nil)
	}
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	return req
}

// TestCheck pins the origin comparison: scheme, host and port must all stay
// those of the first request, with a default port read as written.
func TestCheck(t *testing.T) {
	cases := []struct {
		from, to string
		follow   bool
	}{
		{"http://127.0.0.1:8420/v1/snapshot", "http://127.0.0.1:8420/elsewhere", true},
		{"https://straza.example/v1/snapshot", "https://straza.example:443/v1/snapshot", true},
		{"http://straza.example/v1/snapshot", "http://STRAZA.example:80/v1/snapshot", true},
		{"http://[::1]:8420/a", "http://[::1]:8420/b", true},
		{"http://127.0.0.1:8420/a", "http://127.0.0.1:9999/a", false},
		{"https://straza.example/a", "http://straza.example:443/a", false},
		{"https://straza.example/a", "http://straza.example/a", false},
		{"http://127.0.0.1:8420/a", "http://localhost:8420/a", false},
		{"https://straza.example/a", "https://evil.straza.example/a", false},
	}
	for _, tc := range cases {
		t.Run(tc.from+" to "+tc.to, func(t *testing.T) {
			err := Check(hop(t, http.MethodGet, tc.to, "", ""), []*http.Request{hop(t, http.MethodGet, tc.from, "tok", "")})
			if tc.follow != (err == nil) {
				t.Fatalf("Check = %v, want follow %v", err, tc.follow)
			}
			var refused *RedirectError
			if !tc.follow && (!errors.As(err, &refused) || strings.Contains(err.Error(), "/a") || strings.Contains(err.Error(), "tok")) {
				t.Errorf("refusal %v, want a RedirectError naming origins only", err)
			}
		})
	}
}

// TestCheckCredentialed pins which requests the rule covers: one with an
// Authorization header or a body follows Check, one with neither keeps Go's
// default, and every request stops after 10 redirects with
// ErrTooManyRedirects, which tells a loop from a network failure.
func TestCheckCredentialed(t *testing.T) {
	away := "http://127.0.0.1:9999/landing"
	cases := []struct {
		name         string
		bearer, body string
		follow       bool
	}{
		{"bearer header", "tok", "", false},
		{"session token in the body", "", `{"session_token":"tok"}`, false},
		{"no credential", "", "", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			first := hop(t, http.MethodPost, "http://127.0.0.1:8420/v1/checkin", tc.bearer, tc.body)
			err := CheckCredentialed(hop(t, http.MethodPost, away, "", ""), []*http.Request{first})
			if tc.follow != (err == nil) {
				t.Fatalf("CheckCredentialed = %v, want follow %v", err, tc.follow)
			}
		})
	}
	var ten []*http.Request
	for range 10 {
		ten = append(ten, hop(t, http.MethodGet, "http://127.0.0.1:8420/a", "", ""))
	}
	if err := CheckCredentialed(hop(t, http.MethodGet, "http://127.0.0.1:8420/b", "", ""), ten); !errors.Is(err, ErrTooManyRedirects) {
		t.Errorf("an eleventh hop without a credential = %v, want ErrTooManyRedirects", err)
	}
	ten[0] = hop(t, http.MethodGet, "http://127.0.0.1:8420/a", "tok", "")
	if err := Check(hop(t, http.MethodGet, "http://127.0.0.1:8420/b", "", ""), ten); !errors.Is(err, ErrTooManyRedirects) {
		t.Errorf("an eleventh hop with a credential = %v, want ErrTooManyRedirects", err)
	}
}
