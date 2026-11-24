package entragraph

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestRedirectRefused(t *testing.T) {
	tests := []struct {
		name string
		hc   *http.Client
	}{
		{"the client New builds", nil},
		{"a given client that follows redirects", &http.Client{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFixture(t)
			f.script(step{method: http.MethodGet, path: "/v1.0/groups/" + roleGroup + "/members", status: http.StatusFound,
				header: map[string]string{"Location": f.away.URL + "/collect"}})
			f.client = f.newClient(f.graph.URL, tt.hc)
			members, err := f.client.Members(context.Background(), roleGroup)
			f.wantError(err, "with a redirect, which Straza never follows")
			if members != nil {
				t.Errorf("members = %v, want none", members)
			}
			f.done()
		})
	}

	f := newFixture(t)
	f.answerTokens(func(int) (int, string) { return http.StatusTemporaryRedirect, "" })
	f.client = f.newClient(f.graph.URL, &http.Client{})
	f.wantError(f.client.DeleteGroup(context.Background(), roleGroup), "redirect", "never follows with a client assertion")
	f.done()
}

func TestNextLinkOffGraph(t *testing.T) {
	path := "/v1.0/groups/" + roleGroup + "/members"
	tests := []struct {
		name string
		link func(f *fixture) string
	}{
		{"another host", func(f *fixture) string { return f.away.URL + path + "?$skiptoken=2" }},
		{"the Graph host over https", func(f *fixture) string {
			return "https" + strings.TrimPrefix(f.graph.URL, "http") + path + "?$skiptoken=2"
		}},
		{"a relative link", func(*fixture) string { return path + "?$skiptoken=2" }},
		{"a host that starts with the Graph host", func(f *fixture) string {
			u, _ := url.Parse(f.graph.URL)
			return "http://" + u.Hostname() + ".evil.example:" + u.Port() + path
		}},
		{"not a URL", func(*fixture) string { return "http://[::1" }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFixture(t)
			body := `{"value":[{"@odata.type":"#microsoft.graph.user","id":"u1"}],"@odata.nextLink":"` + tt.link(f) + `"}`
			f.script(step{method: http.MethodGet, path: path, status: http.StatusOK, body: body})
			members, err := f.client.Members(context.Background(), roleGroup)
			f.wantError(err, "a link that is not on "+strings.TrimPrefix(f.graph.URL, "http://"))
			if members != nil {
				t.Errorf("members = %v, want none", members)
			}
			f.done()
		})
	}
}

func TestHTTPSEndpointRefusesHTTPLink(t *testing.T) {
	f := newFixture(t)
	path := "/v1.0/groups/" + roleGroup + "/members"
	secure := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host := r.Host
		_, _ = w.Write([]byte(`{"value":[],"@odata.nextLink":"http://` + host + path + `?$skiptoken=2"}`))
	}))
	defer secure.Close()
	f.client = f.newClient(secure.URL, secure.Client())
	members, err := f.client.Members(context.Background(), roleGroup)
	f.wantError(err, "a link that is not on")
	if members != nil {
		t.Errorf("members = %v, want none", members)
	}
	f.done()
}

func TestBodyCap(t *testing.T) {
	path := "/v1.0/groups/" + roleGroup + "/members"
	pad := func(total int) string {
		head, tail := `{"value":[],"pad":"`, `"}`
		return head + strings.Repeat("x", total-len(head)-len(tail)) + tail
	}
	tests := []struct {
		name string
		body string
		fail bool
	}{
		{"exactly the cap", pad(maxBody), false},
		{"one byte over the cap", pad(maxBody + 1), true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFixture(t, step{method: http.MethodGet, path: path, status: http.StatusOK, body: tt.body})
			members, err := f.client.Members(context.Background(), roleGroup)
			f.done()
			if !tt.fail {
				if err != nil || members == nil {
					t.Errorf("Members = %v, %s, want an empty list", members, f.show(err))
				}
				return
			}
			f.wantError(err, "more than 8 MiB")
		})
	}

	f := newFixture(t)
	f.answerTokens(func(int) (int, string) { return http.StatusOK, pad(maxBody + 1) })
	f.wantError(f.client.DeleteGroup(context.Background(), roleGroup), "more than 8 MiB", "the token request")
	f.done()
}

func TestGraphError(t *testing.T) {
	path := "/v1.0/groups/" + roleGroup
	long := strings.Repeat("é", 400)
	tests := []struct {
		name     string
		status   int
		body     string
		header   map[string]string
		want     Error
		notFound bool
		says     []string
	}{
		{name: "429 with Retry-After", status: 429, body: "@error-throttled.json", header: map[string]string{"Retry-After": "7"},
			want: Error{Status: 429, Code: "TooManyRequests", Message: "Please retry again later.", RetryAfter: 7 * time.Second},
			says: []string{"the delete of group " + roleGroup + " with HTTP 429 TooManyRequests: Please retry again later.", "wait 7 seconds"}},
		{name: "503 with Retry-After", status: 503, body: `{"error":{"code":"serviceNotAvailable","message":"Service unavailable."}}`, header: map[string]string{"Retry-After": "120"},
			want: Error{Status: 503, Code: "serviceNotAvailable", Message: "Service unavailable.", RetryAfter: 2 * time.Minute}},
		{name: "429 without Retry-After", status: 429, body: "@error-throttled.json",
			want: Error{Status: 429, Code: "TooManyRequests", Message: "Please retry again later."}},
		{name: "Retry-After as a date", status: 429, body: "{}", header: map[string]string{"Retry-After": "Wed, 18 Nov 2026 07:28:00 GMT"},
			want: Error{Status: 429}, says: []string{"HTTP 429 and no Graph error."}},
		{name: "Retry-After of years", status: 503, body: "{}", header: map[string]string{"Retry-After": "999999999999"},
			want: Error{Status: 503, RetryAfter: time.Hour}},
		{name: "Retry-After on a 500 is ignored", status: 500, body: "{}", header: map[string]string{"Retry-After": "7"},
			want: Error{Status: 500}},
		{name: "404", status: 404, body: "@error-not-found.json", notFound: true,
			want: Error{Status: 404, Code: "Request_ResourceNotFound", Message: "Resource '" + roleGroup + "' does not exist or one of its queried reference-property objects are not present."}},
		{name: "a body that is not JSON", status: 502, body: "<html>bad gateway</html>", want: Error{Status: 502}},
		{name: "a code that is not plain", status: 400, body: `{"error":{"code":"Bad Code<script>","message":"No."}}`,
			want: Error{Status: 400, Message: "No."}, says: []string{"HTTP 400: No."}},
		{name: "control characters", status: 400, body: `{"error":{"code":"x","message":"line\none\u0007"}}`,
			want: Error{Status: 400, Code: "x", Message: "line one"}},
		{name: "a long message", status: 400, body: `{"error":{"code":"x","message":"` + long + `"}}`,
			want: Error{Status: 400, Code: "x", Message: long[:300]}},
		{name: "a message that repeats the token", status: 401, body: `{"error":{"code":"InvalidAuthenticationToken","message":"Token ` + tokenMark + ` is bad."}}`,
			want: Error{Status: 401, Code: "InvalidAuthenticationToken", Message: "Graph's message is withheld, because it repeats Straza's token"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFixture(t, step{method: http.MethodDelete, path: path, status: tt.status, body: tt.body, header: tt.header})
			err := f.client.DeleteGroup(context.Background(), roleGroup)
			f.done()
			f.wantError(err, tt.says...)
			var ge *Error
			if !errors.As(err, &ge) {
				t.Fatalf("err = %s, want an *Error", f.show(err))
			}
			got := *ge
			got.what = ""
			if got != tt.want {
				t.Errorf("Error = %+v, want %+v", got, tt.want)
			}
			if errors.Is(err, ErrNotFound) != tt.notFound {
				t.Errorf("errors.Is(err, ErrNotFound) = %v, want %v", !tt.notFound, tt.notFound)
			}
		})
	}
}
