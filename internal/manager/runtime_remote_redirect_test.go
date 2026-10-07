package manager

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestFollowRedirect pins the upstream client's redirect policy hop by hop:
// a hop may stay on the host and port of the first request, and may never
// go from https to http.
func TestFollowRedirect(t *testing.T) {
	cases := []struct {
		name string
		via  []string // the requests so far, the manifest's address first
		to   string
		want string // a phrase of the refusal, empty when the hop is followed
	}{
		{name: "a path on the same host", via: []string{"https://mcp.example.com/mcp"}, to: "https://mcp.example.com/mcp/"},
		{name: "the same host in other letter case", via: []string{"https://mcp.example.com/mcp"}, to: "https://MCP.Example.com/mcp/"},
		{name: "an upgrade to https on the same host", via: []string{"http://mcp.example.com/mcp"}, to: "https://mcp.example.com/mcp"},
		{name: "another host", via: []string{"https://mcp.example.com/mcp"}, to: "https://collect.example.net/mcp",
			want: "redirected to collect.example.net, and Straza follows a redirect only on the host and port"},
		{name: "a subdomain", via: []string{"https://mcp.example.com/mcp"}, to: "https://eu.mcp.example.com/mcp",
			want: "redirected to eu.mcp.example.com,"},
		{name: "another port on the same host", via: []string{"https://mcp.example.com/mcp"}, to: "https://mcp.example.com:8443/mcp",
			want: "redirected to mcp.example.com:8443,"},
		{name: "a later hop that leaves the host", via: []string{"https://mcp.example.com/mcp", "https://mcp.example.com/mcp/"},
			to: "https://collect.example.net/mcp", want: "redirected to collect.example.net,"},
		{name: "https to http on the same host", via: []string{"https://mcp.example.com/mcp"}, to: "http://mcp.example.com/mcp",
			want: "redirected from https to http"},
		{name: "a later hop from https to http", via: []string{"http://mcp.example.com/mcp", "https://mcp.example.com/mcp"},
			to: "http://mcp.example.com/mcp/", want: "redirected from https to http"},
		{name: "the tenth redirect", via: []string{"https://mcp.example.com/1", "https://mcp.example.com/2", "https://mcp.example.com/3",
			"https://mcp.example.com/4", "https://mcp.example.com/5", "https://mcp.example.com/6", "https://mcp.example.com/7",
			"https://mcp.example.com/8", "https://mcp.example.com/9", "https://mcp.example.com/10"},
			to: "https://mcp.example.com/11", want: "redirected 10 times"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var via []*http.Request
			for _, u := range tc.via {
				via = append(via, httptest.NewRequest(http.MethodPost, u, nil))
			}
			err := followRedirect(httptest.NewRequest(http.MethodPost, tc.to, nil), via)
			switch {
			case tc.want == "" && err != nil:
				t.Fatalf("the hop to %s was refused: %v", tc.to, err)
			case tc.want != "" && err == nil:
				t.Fatalf("the hop to %s was followed, want a refusal naming %q", tc.to, tc.want)
			case tc.want != "" && !strings.Contains(err.Error(), tc.want):
				t.Fatalf("refusal = %q, want it to name %q", err, tc.want)
			}
		})
	}
}

// TestRemoteRuntimeRedirects drives a credentialed call against upstreams
// that redirect. A redirect to another host is refused before the hop is
// sent, so the injected credential never reaches that host, and a redirect
// on the same host, such as /mcp to /mcp/, keeps working.
func TestRemoteRuntimeRedirects(t *testing.T) {
	cases := []struct {
		name string
		// front builds the address the manifest names, in front of the
		// recording upstream target.
		front   func(t *testing.T, target *upstreamEcho) string
		refused bool
	}{
		{name: "to another host", refused: true, front: func(t *testing.T, target *upstreamEcho) string {
			return redirectingFront(t, strings.Replace(target.URL, "127.0.0.1", "localhost", 1)+"/mcp")
		}},
		{name: "to another port on the same host", refused: true, front: func(t *testing.T, target *upstreamEcho) string {
			return redirectingFront(t, target.URL+"/mcp")
		}},
		{name: "to a path on the same host", front: func(t *testing.T, target *upstreamEcho) string {
			mux := http.NewServeMux()
			mux.HandleFunc("/mcp", func(w http.ResponseWriter, r *http.Request) {
				http.Redirect(w, r, "/mcp/", http.StatusTemporaryRedirect)
			})
			mux.Handle("/mcp/", target.Config.Handler)
			same := httptest.NewServer(mux)
			t.Cleanup(same.Close)
			return same.URL + "/mcp"
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			target := newUpstreamEcho(t)
			inject := &InjectSpec{As: InjectHeader, Name: "Authorization", Template: "Bearer {{secret}}"}
			r := NewRemoteRuntime("echo", RemoteSpec{URL: tc.front(t, target), Auth: AuthInject}, inject, nil)
			r.AllowLoopback = true
			t.Cleanup(r.Stop)

			res, err := r.Call(context.Background(), CallInput{Tool: "echo", Args: mustJSON(t, map[string]string{"text": "hi"}),
				Secret: &Secret{ID: "cred-1", Value: "sk-never-elsewhere"}})
			if tc.refused {
				if err == nil || !strings.Contains(err.Error(), "an administrator puts it in the manifest") {
					t.Fatalf("call err = %v, want the redirect refused with its fix", err)
				}
				target.mu.Lock()
				reached := len(target.headers)
				target.mu.Unlock()
				if reached != 0 {
					t.Fatalf("the redirect target received %d requests, want none, so the credential never left the manifest's host", reached)
				}
				return
			}
			if err != nil {
				t.Fatalf("a redirect on the same host broke the call: %v", err)
			}
			if got := textOf(res); got != "echo: hi" {
				t.Errorf("echo = %q", got)
			}
			if !target.sawAuth("Bearer sk-never-elsewhere") {
				t.Error("the credential did not reach the same-host path the server redirected to")
			}
		})
	}
}

// redirectingFront answers every request with a 307 to target, which keeps
// the method, the body and, for a client that follows it, the headers.
func redirectingFront(t *testing.T, target string) string {
	t.Helper()
	front := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target, http.StatusTemporaryRedirect)
	}))
	t.Cleanup(front.Close)
	return front.URL
}
