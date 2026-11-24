package e2ematrix

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestBodyAfter pins the bodyAfter assertion of an admin or scim step: the
// value at each path must be an RFC 3339 time later than the one given,
// compared as times and not as text, and a missing or unreadable value
// fails the step.
func TestBodyAfter(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`[{"last_seen":"2026-11-21T10:03:10.25Z","started_at":"2026-11-21T10:00:00Z","status":"active"}]`))
	}))
	defer srv.Close()
	s := &scenarioRun{stack: &stack{server: &strazad{URL: srv.URL}}}
	cases := []struct {
		name   string
		after  map[string]any
		pass   bool
		detail string
	}{
		{"a later time", map[string]any{"0.last_seen": "2026-11-21T10:00:10.5Z"}, true, ""},
		{"the same time", map[string]any{"0.last_seen": "2026-11-21T10:03:10.25Z"}, false, "want a time later than 2026-11-21T10:03:10.25Z"},
		{"the same time in another zone", map[string]any{"0.last_seen": "2026-11-21T11:03:10.25+01:00"}, false, "want a time later than"},
		{"a later reference", map[string]any{"0.last_seen": "2026-11-21T10:04:00Z"}, false, "body.0.last_seen = 2026-11-21T10:03:10.25Z"},
		{"a whole second is earlier than a fraction past it", map[string]any{"0.started_at": "2026-11-21T10:00:00.5Z"}, false, "want a time later than"},
		{"a path the answer lacks", map[string]any{"1.last_seen": "2026-11-21T10:00:00Z"}, false, "body.1.last_seen = , want a time later than"},
		{"a value that is not a time", map[string]any{"0.status": "2026-11-21T10:00:00Z"}, false, "body.0.status = active"},
		{"a reference that is not a time", map[string]any{"0.last_seen": "soon"}, false, "want a time later than soon"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a, err := s.httpStep(context.Background(), map[string]any{"method": "GET", "path": "/v1/admin/sessions"},
				map[string]any{"status": 200, "bodyAfter": tc.after}, "")
			if err != nil {
				t.Fatal(err)
			}
			if a.ok != tc.pass || !strings.Contains(a.detail, tc.detail) {
				t.Fatalf("httpStep with bodyAfter %v = %v %q, want %v and a detail with %q", tc.after, a.ok, a.detail, tc.pass, tc.detail)
			}
		})
	}
}
