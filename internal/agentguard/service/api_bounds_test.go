package service

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/strazahq/straza/internal/agentguard/localapi"
)

// TestAPIHarnessFieldBounds pins the bound on the harness name, version and
// session id every route reads: 256 bytes reach the credential half, and
// 257 are refused with the sentence that names the field, on the body
// routes and on the proxy's headers alike.
func TestAPIHarnessFieldBounds(t *testing.T) {
	long := func(n int) string { return strings.Repeat("x", n) }
	cases := []struct {
		name  string
		h     localapi.Harness
		field string // "" means the request passes the bound
	}{
		{"every field at the bound", localapi.Harness{Name: long(256), Version: long(256), Session: long(256)}, ""},
		{"a name over the bound", localapi.Harness{Name: long(257)}, "harness name"},
		{"a version over the bound", localapi.Harness{Name: "claude-code", Version: long(257)}, "harness version"},
		{"a session id over the bound", localapi.Harness{Name: "claude-code", Session: long(257)}, "harness session id"},
	}
	routes := []struct {
		name string
		req  func(h localapi.Harness) *http.Request
	}{
		{"sessions", func(h localapi.Harness) *http.Request {
			return jsonReq(localapi.RouteSessions, localapi.SessionRequest{Harness: h})
		}},
		{"decide", func(h localapi.Harness) *http.Request {
			return jsonReq(localapi.RouteDecide, localapi.DecideRequest{Harness: h})
		}},
		{"capture", func(h localapi.Harness) *http.Request {
			return jsonReq(localapi.RouteCapture, localapi.CaptureRequest{Harness: h, Kind: localapi.CapturePrompt})
		}},
		{"mcp", func(h localapi.Harness) *http.Request {
			r := httptest.NewRequest(http.MethodPost, localapi.BaseURL+localapi.RouteMCP, strings.NewReader("{}"))
			r.Header.Set(localapi.HeaderHarness, h.Name)
			r.Header.Set(localapi.HeaderHarnessVersion, h.Version)
			r.Header.Set(localapi.HeaderHarnessSession, h.Session)
			return r
		}},
	}
	for _, rt := range routes {
		for _, tc := range cases {
			t.Run(rt.name+"/"+tc.name, func(t *testing.T) {
				f := &apiDouble{sessionErr: &Refused{Status: http.StatusForbidden, Sentence: notEnrolledSentence}}
				a, _ := testAPI(f, false)
				w := httptest.NewRecorder()
				a.routes().ServeHTTP(w, asCaller(rt.req(tc.h), "1000"))
				if tc.field == "" {
					if w.Code != http.StatusForbidden || refusalOf(t, w) != notEnrolledSentence {
						t.Fatalf("answer = %d %q, want the credential half's 403", w.Code, w.Body.String())
					}
					return
				}
				want := fmt.Sprintf(harnessFieldTooLong, tc.field, 257, maxHarnessField)
				if w.Code != http.StatusBadRequest || refusalOf(t, w) != want || len(f.sessionCalls) != 0 {
					t.Fatalf("answer = %d %q after %d calls, want 400 %q before the credential half", w.Code, w.Body.String(), len(f.sessionCalls), want)
				}
			})
		}
	}
}

// jsonReq is a POST of v as JSON to route.
func jsonReq(route string, v any) *http.Request {
	raw, _ := json.Marshal(v)
	return httptest.NewRequest(http.MethodPost, localapi.BaseURL+route, bytes.NewReader(raw))
}
