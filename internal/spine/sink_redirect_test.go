package spine

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
)

// landing records every request that reaches a /final handler.
type landing struct {
	mu    sync.Mutex
	heads []http.Header
	reqs  []string // method and body
}

func (l *landing) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	b, _ := io.ReadAll(r.Body)
	l.mu.Lock()
	l.heads = append(l.heads, r.Header.Clone())
	l.reqs = append(l.reqs, r.Method+" "+string(b))
	l.mu.Unlock()
	w.WriteHeader(http.StatusOK)
}

// got returns copies of what reached the handler.
func (l *landing) got() ([]http.Header, []string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]http.Header(nil), l.heads...), append([]string(nil), l.reqs...)
}

func (l *landing) String() string {
	heads, reqs := l.got()
	parts := []string{fmt.Sprintf("%d request(s)", len(reqs))}
	for i, h := range heads {
		parts = append(parts, fmt.Sprintf("[%s | Authorization=%q DD-API-KEY=%q X-Straza-Signature set=%t]",
			reqs[i], h.Get("Authorization"), h.Get("DD-API-KEY"), h.Get("X-Straza-Signature") != ""))
	}
	return strings.Join(parts, " ")
}

// TestWebhookSinkRedirectStaysOnOrigin pins the webhook client's redirect
// rule on both delivery paths. A delivery follows a redirect only while the
// scheme, host and port stay those of the configured url and the POST stays
// a POST, as a 307 or 308 keeps it. Any other redirect is refused before a
// byte reaches its target, so the event, the signature and the operator's
// headers never leave the configured origin. A redirect loop and a Location
// that does not parse stop with a sentence of their own. Each refusal is a
// retryable failure, the class the runner gives any error that is not a status.
func TestWebhookSinkRedirectStaysOnOrigin(t *testing.T) {
	const (
		auth  = "Splunk test-hec-value"
		ddKey = "test-dd-value"
		query = "token=test-target-value"
	)
	secret := []byte("test-hmac-value")
	originRefusal := "sink siem: %s answered with a redirect to %s, which is another scheme, host or port, " +
		"so the sink did not follow it and sent neither the event nor its headers there. " +
		"The event and the sink's credentials must not leave the configured origin. " +
		"Set the sink's url to the final address of the receiver"
	methodRefusal := "sink siem: %s answered with a %d redirect, which turns the POST of the event into a GET without it, " +
		"so the sink did not follow it. A 2xx answer to that GET would count an event the receiver never got. " +
		"Set the sink's url to the final address of the receiver"
	loopRefusal := "sink siem: %s redirected the delivery 10 times in a row, " +
		"so the sink stopped following it as a redirect loop and the event was not delivered. " +
		"Set the sink's url to the final address of the receiver"
	badLocationRefusal := "sink siem: %s answered with a redirect whose Location header is not a valid address, " +
		"so the sink could not follow it and the event was not delivered. " +
		"Set the sink's url to the final address of the receiver"

	type servers struct{ a, b *url.URL }
	origin := func(u *url.URL) string { return u.Scheme + "://" + u.Host }
	cases := []struct {
		name string
		tls  bool // the configured origin serves https
		code int  // the configured origin answers this redirect; 0 means the url is the landing itself
		loc  func(s servers) string
		// atOrigin is true when the redirect target is the configured
		// origin's own /final, false when it is the second server's.
		atOrigin bool
		// wantErr is empty when the delivery must land once; otherwise a
		// format taking the configured origin and the target origin or code.
		wantErr func(s servers) string
	}{
		{name: "direct 2xx delivers (positive control)"},
		{name: "same origin 307 follows", code: 307, atOrigin: true,
			loc: func(servers) string { return "/final?" + query }},
		{name: "same origin 308 follows", code: 308, atOrigin: true,
			loc: func(servers) string { return "/final?" + query }},
		{name: "same origin 302 is refused", code: 302, atOrigin: true,
			loc:     func(servers) string { return "/final?" + query },
			wantErr: func(s servers) string { return fmt.Sprintf(methodRefusal, origin(s.a), 302) }},
		{name: "another port 307 is refused", code: 307,
			loc:     func(s servers) string { return origin(s.b) + "/final?" + query },
			wantErr: func(s servers) string { return fmt.Sprintf(originRefusal, origin(s.a), origin(s.b)) }},
		{name: "another port 308 is refused", code: 308,
			loc:     func(s servers) string { return origin(s.b) + "/final?" + query },
			wantErr: func(s servers) string { return fmt.Sprintf(originRefusal, origin(s.a), origin(s.b)) }},
		{name: "another port 302 is refused", code: 302,
			loc:     func(s servers) string { return origin(s.b) + "/final?" + query },
			wantErr: func(s servers) string { return fmt.Sprintf(originRefusal, origin(s.a), origin(s.b)) }},
		{name: "another host 307 is refused", code: 307,
			loc: func(s servers) string { return "http://localhost:" + s.b.Port() + "/final?" + query },
			wantErr: func(s servers) string {
				return fmt.Sprintf(originRefusal, origin(s.a), "http://localhost:"+s.b.Port())
			}},
		{name: "https to http on the same port 307 is refused", code: 307, tls: true, atOrigin: true,
			loc: func(s servers) string { return "http://" + s.a.Host + "/final?" + query },
			wantErr: func(s servers) string {
				return fmt.Sprintf(originRefusal, origin(s.a), "http://"+s.a.Host)
			}},
		{name: "same origin 307 loop stops", code: 307, atOrigin: true,
			loc:     func(servers) string { return "/in" },
			wantErr: func(s servers) string { return fmt.Sprintf(loopRefusal, origin(s.a)) }},
		{name: "unparseable Location 307 stops", code: 307, atOrigin: true,
			loc:     func(servers) string { return "http://[::1" },
			wantErr: func(s servers) string { return fmt.Sprintf(badLocationRefusal, origin(s.a)) }},
	}
	lanes := []struct {
		name    string
		body    string
		deliver func(s *WebhookSink) error
	}{
		{"event", `{"id":"e1"}`, func(s *WebhookSink) error {
			return s.Deliver(context.Background(), "straza.audit.tool", []byte(`{"id":"e1"}`))
		}},
		{"batch", "{\"id\":\"a\"}\n{\"id\":\"b\"}\n", func(s *WebhookSink) error {
			return s.DeliverBatch(context.Background(), []SinkEvent{
				{Subject: "straza.audit.tool", CE: []byte(`{"id":"a"}`)},
				{Subject: "straza.audit.tool", CE: []byte(`{"id":"b"}`)},
			})
		}},
	}
	for _, lane := range lanes {
		for _, tc := range cases {
			t.Run(lane.name+"/"+tc.name, func(t *testing.T) {
				atA, atB := &landing{}, &landing{}
				var s servers
				muxA := http.NewServeMux()
				muxA.Handle("/final", atA)
				muxA.HandleFunc("/in", func(w http.ResponseWriter, r *http.Request) {
					http.Redirect(w, r, tc.loc(s), tc.code)
				})
				srvA := httptest.NewUnstartedServer(muxA)
				defer srvA.Close()
				muxB := http.NewServeMux()
				muxB.Handle("/final", atB)
				srvB := httptest.NewUnstartedServer(muxB)
				defer srvB.Close()
				// The addresses are known before the servers start, so the
				// redirect handler reads them without a race.
				s.a = &url.URL{Scheme: "http", Host: srvA.Listener.Addr().String()}
				s.b = &url.URL{Scheme: "http", Host: srvB.Listener.Addr().String()}
				if tc.tls {
					s.a.Scheme = "https"
					srvA.StartTLS()
				} else {
					srvA.Start()
				}
				srvB.Start()

				target, landed, spared := srvA.URL+"/in", atA, atB
				switch {
				case tc.code == 0:
					target, landed, spared = srvB.URL+"/final", atB, atA
				case !tc.atOrigin:
					landed, spared = atB, atA
				}
				sink := NewWebhookSink("siem", target, secret, map[string]string{
					"Authorization": auth,
					"DD-API-KEY":    ddKey,
				})
				if tc.tls {
					sink.client.Transport = srvA.Client().Transport
				}
				err := lane.deliver(sink)

				if tc.wantErr == nil {
					if err != nil {
						t.Fatalf("delivery error = %v, want delivered", err)
					}
					heads, reqs := landed.got()
					if len(reqs) != 1 {
						t.Fatalf("landing got %s, want 1 request", landed)
					}
					mac := hmac.New(sha256.New, secret)
					mac.Write([]byte(lane.body))
					h := heads[0]
					if reqs[0] != "POST "+lane.body || h.Get("Authorization") != auth || h.Get("DD-API-KEY") != ddKey ||
						h.Get("X-Straza-Signature") != "sha256="+hex.EncodeToString(mac.Sum(nil)) {
						t.Errorf("landing got %s, want the POST with its body, both headers and the signature", landed)
					}
					return
				}
				if err == nil {
					t.Fatalf("delivery succeeded, want the refusal; the target got %s", landed)
				}
				if got, want := err.Error(), tc.wantErr(s); got != want {
					t.Errorf("error =\n  %s\nwant\n  %s", got, want)
				}
				for _, v := range []string{auth, ddKey, query, "/final"} {
					if strings.Contains(err.Error(), v) {
						t.Errorf("error text carries %q: %v", v, err)
					}
				}
				for who, l := range map[string]*landing{"redirect target": landed, "other server": spared} {
					if _, reqs := l.got(); len(reqs) != 0 {
						t.Errorf("%s got %s, want nothing", who, l)
					}
				}
				if c := classify(err); c != classRetryable {
					t.Errorf("class = %v, want retryable", c)
				}
			})
		}
	}
}
