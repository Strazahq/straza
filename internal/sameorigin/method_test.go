package sameorigin

import (
	"errors"
	"net/http"
	"testing"
)

// TestCheckMethod pins the method half of the rule: within the origin a
// redirect that keeps the method follows, one that turns a POST into a GET is
// refused with a MethodError, and a redirect to another origin is still the
// RedirectError of Check.
func TestCheckMethod(t *testing.T) {
	cases := []struct {
		name         string
		first, next  string // methods
		status       int
		to           string
		follow       bool
		wantOrigin   bool // the refusal is a RedirectError
		wantMethodOf int  // the refusal is a MethodError with this status
	}{
		{"POST 307 same origin", http.MethodPost, http.MethodPost, 307, "http://127.0.0.1:8420/b", true, false, 0},
		{"POST 308 same origin", http.MethodPost, http.MethodPost, 308, "http://127.0.0.1:8420/b", true, false, 0},
		{"GET 302 same origin", http.MethodGet, http.MethodGet, 302, "http://127.0.0.1:8420/b", true, false, 0},
		{"POST 302 same origin", http.MethodPost, http.MethodGet, 302, "http://127.0.0.1:8420/b", false, false, 302},
		{"POST 303 same origin", http.MethodPost, http.MethodGet, 303, "http://127.0.0.1:8420/b", false, false, 303},
		{"POST 307 another port", http.MethodPost, http.MethodPost, 307, "http://127.0.0.1:9999/b", false, true, 0},
		{"POST 302 another port", http.MethodPost, http.MethodGet, 302, "http://127.0.0.1:9999/b", false, true, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			first, err := http.NewRequest(tc.first, "http://127.0.0.1:8420/a", http.NoBody)
			if err != nil {
				t.Fatal(err)
			}
			next, err := http.NewRequest(tc.next, tc.to, http.NoBody)
			if err != nil {
				t.Fatal(err)
			}
			next.Response = &http.Response{StatusCode: tc.status}
			err = CheckMethod(next, []*http.Request{first})
			if tc.follow != (err == nil) {
				t.Fatalf("CheckMethod = %v, want follow %v", err, tc.follow)
			}
			var cross *RedirectError
			if tc.wantOrigin != errors.As(err, &cross) {
				t.Errorf("CheckMethod = %v, want a RedirectError %v", err, tc.wantOrigin)
			}
			var method *MethodError
			if got := errors.As(err, &method); got != (tc.wantMethodOf != 0) ||
				got && *method != (MethodError{Origin: "http://127.0.0.1:8420", Status: tc.wantMethodOf, From: http.MethodPost, To: http.MethodGet}) {
				t.Errorf("CheckMethod = %#v, want a MethodError with status %d", err, tc.wantMethodOf)
			}
		})
	}
}
