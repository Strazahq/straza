package approval

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/strazahq/straza/internal/config"
	"github.com/strazahq/straza/internal/policy"
	"github.com/strazahq/straza/internal/store"
)

// redirectLanding records every request that reaches it and answers with one
// body every lane reads as a success: Slack's ok, an OAuth token and the
// relay's verdict. Each lane ignores the other lanes' fields.
type redirectLanding struct {
	mu    sync.Mutex
	heads []http.Header
	reqs  []string // method and body
}

func (l *redirectLanding) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	b, _ := io.ReadAll(r.Body)
	l.mu.Lock()
	l.heads = append(l.heads, r.Header.Clone())
	l.reqs = append(l.reqs, r.Method+" "+string(b))
	l.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	_, _ = io.WriteString(w, `{"ok":true,"access_token":"ya29.landing","expires_in":3600,"token_type":"Bearer",`+
		`"delivered":true,"downstream_status":200}`)
}

// got returns copies of what reached the handler.
func (l *redirectLanding) got() ([]http.Header, []string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]http.Header(nil), l.heads...), append([]string(nil), l.reqs...)
}

// pushTarget is one registration of device d1.
func pushTarget(kind, route string) store.ApproverPushTarget {
	return store.ApproverPushTarget{ApproverPush: store.ApproverPush{DeviceID: "d1", Kind: kind, TokenOrEndpoint: route}}
}

func (l *redirectLanding) String() string {
	heads, reqs := l.got()
	parts := []string{fmt.Sprintf("%d request(s)", len(reqs))}
	for i, h := range heads {
		parts = append(parts, fmt.Sprintf("[%s | Authorization=%q]", reqs[i], h.Get("Authorization")))
	}
	return strings.Join(parts, " ")
}

// TestApprovalClientsRedirectStayOnOrigin pins the redirect rule of every
// client strazad's approval channels send with: the Slack Web API client, the
// push client that web push, UnifiedPush, FCM and the relay share, and the
// APNs client. A request follows a redirect only while the scheme, host and
// port stay those it was sent to and the POST stays a POST, as a 307 or 308
// keeps it. Any other redirect is refused before a byte reaches its target, so
// neither the message nor its credential leaves the address strazad was
// given, and a lane that records deliveries records the refusal as a failure.
// A redirect loop and a Location that does not parse stop with a sentence of
// their own.
func TestApprovalClientsRedirectStayOnOrigin(t *testing.T) {
	const query = "token=test-target-value"
	originRefusal := "%s answered with a redirect to %s, which is another scheme, host or port, " +
		"so strazad did not follow it and sent neither the message nor its credentials there. " +
		"An approval message and its credentials must not leave the address strazad was given. " +
		"Check that no proxy between strazad and that service redirects, and that the address strazad has for it is the final one"
	methodRefusal := "%s answered with a %d redirect, which turns the POST into a GET without its body, " +
		"so strazad did not follow it and the message was not delivered. " +
		"Check that no proxy between strazad and that service redirects, and that the address strazad has for it is the final one"
	loopRefusal := "%s redirected the request 10 times in a row, " +
		"so strazad stopped following it as a redirect loop and the message was not delivered. " +
		"Check that no proxy between strazad and that service redirects, and that the address strazad has for it is the final one"
	badLocationRefusal := "%s answered with a redirect whose Location header is not a valid address, " +
		"so strazad could not follow it and the message was not delivered. " +
		"Check that no proxy between strazad and that service redirects, and that the address strazad has for it is the final one"

	h := newHarness(t)
	ctx := context.Background()
	saPath, _ := writeServiceAccount(t, "http://127.0.0.1:1/token")
	apnsCfg := testAPNSConfig()
	apnsCfg.KeyFile = writeAPNSKeyPEM(t)
	relayToken := filepath.Join(t.TempDir(), "relay-token")
	if err := os.WriteFile(relayToken, []byte("wpt_test-relay-value"), 0o600); err != nil {
		t.Fatal(err)
	}
	newPush := func(t *testing.T, cfg config.ApprovalPush) *pushDelivery {
		t.Helper()
		p, err := newPushDelivery(h.svc, cfg, discardLog())
		if err != nil {
			t.Fatalf("newPushDelivery: %v", err)
		}
		return p
	}
	trust := func(c *http.Client, rt http.RoundTripper) {
		if rt != nil {
			c.Transport = rt
		}
	}

	// Each lane sends one request to base, a URL without a trailing slash,
	// through the client its constructor builds. It returns the channel whose
	// delivery record it writes, or "" for an inner call, and the error.
	lanes := []struct {
		name    string
		bodyHas string // the landing's body must contain this
		cred    string // the Authorization value the landing must see, "" for none
		secret  string // must never appear in the error text
		run     func(t *testing.T, base string, rt http.RoundTripper) (string, error)
	}{
		{name: "slack test message", bodyHas: `"channel":"C1"`, cred: "Bearer test-bot-value", secret: "test-bot-value",
			run: func(t *testing.T, base string, rt http.RoundTripper) (string, error) {
				sc, err := newSlackChannel(config.SlackChannel{Enabled: true, BotToken: "test-bot-value",
					SigningSecret: testSigningSecret, Channel: "C1"}, h.st, h.svc, discardLog())
				if err != nil {
					t.Fatalf("newSlackChannel: %v", err)
				}
				sc.apiBase = base
				trust(sc.http, rt)
				return policy.NotifySlack, sc.testMessage(ctx)
			}},
		{name: "unifiedpush post", bodyHas: `"ref":"r1"`,
			run: func(t *testing.T, base string, rt http.RoundTripper) (string, error) {
				p := newPush(t, config.ApprovalPush{})
				trust(p.httpc, rt)
				status, err := p.postJSON(ctx, base+"/up", []byte(`{"v":1,"ref":"r1","kind":"decide"}`), 60)
				if err == nil && status >= 400 {
					err = fmt.Errorf("status %d", status)
				}
				return "", err
			}},
		{name: "fcm send", bodyHas: `"token":"fcm-reg"`, cred: "Bearer ya29.test-preset-value", secret: "test-preset-value",
			run: func(t *testing.T, base string, rt http.RoundTripper) (string, error) {
				p := newPush(t, config.ApprovalPush{FCM: config.FCMPush{Enabled: true, ServiceAccountFile: saPath, ProjectID: "straza-proj"}})
				p.fcm.sendEndpoint = base + "/send"
				p.fcm.token, p.fcm.expiry = "ya29.test-preset-value", time.Now().Add(time.Hour)
				trust(p.httpc, rt)
				return policy.NotifyPush, p.sendFCM(pushTarget("fcm", "fcm-reg"), pushKindDecide, "r1")
			}},
		{name: "fcm token exchange", bodyHas: "assertion=test-assertion-value", secret: "test-assertion-value",
			run: func(t *testing.T, base string, rt http.RoundTripper) (string, error) {
				p := newPush(t, config.ApprovalPush{FCM: config.FCMPush{Enabled: true, ServiceAccountFile: saPath, ProjectID: "straza-proj"}})
				p.fcm.tokenURI = base + "/token"
				trust(p.httpc, rt)
				_, _, err := p.fcm.exchange(ctx, "test-assertion-value")
				return "", err
			}},
		{name: "relay send", bodyHas: `"route":"route-1"`, cred: "Bearer wpt_test-relay-value", secret: "test-relay-value",
			run: func(t *testing.T, base string, rt http.RoundTripper) (string, error) {
				p := newPush(t, config.ApprovalPush{Relay: config.RelayPush{Enabled: true, URL: base, TokenFile: relayToken}})
				trust(p.httpc, rt)
				return policy.NotifyPush, p.sendRelay(pushTarget("apns", "route-1"),
					"apns", pushKindDecide, "r1", time.Time{})
			}},
		{name: "apns send", bodyHas: `"ref":"r1"`,
			run: func(t *testing.T, base string, rt http.RoundTripper) (string, error) {
				p := newPush(t, config.ApprovalPush{APNS: apnsCfg})
				p.apns.host = base
				trust(p.apns.httpc, rt)
				return policy.NotifyPush, p.sendAPNS(pushTarget("apns", "devtok"),
					pushKindDecide, "r1", time.Time{})
			}},
	}

	type servers struct{ a, b *url.URL }
	origin := func(u *url.URL) string { return u.Scheme + "://" + u.Host }
	cases := []struct {
		name     string
		tls      bool // the first server serves https
		code     int  // the first server answers this redirect; 0 sends straight to the second server
		loc      func(s servers) string
		atOrigin bool // the redirect targets the first server's own /final
		loop     bool // the first server redirects every request to its own path
		wantErr  func(s servers) string
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
		{name: "same origin 307 loop stops", code: 307, atOrigin: true, loop: true,
			wantErr: func(s servers) string { return fmt.Sprintf(loopRefusal, origin(s.a)) }},
		{name: "unparseable Location 307 stops", code: 307, atOrigin: true,
			loc:     func(servers) string { return "http://[::1" },
			wantErr: func(s servers) string { return fmt.Sprintf(badLocationRefusal, origin(s.a)) }},
	}
	for _, lane := range lanes {
		for _, tc := range cases {
			t.Run(lane.name+"/"+tc.name, func(t *testing.T) {
				atA, atB := &redirectLanding{}, &redirectLanding{}
				var s servers
				muxA := http.NewServeMux()
				muxA.Handle("/final", atA)
				muxA.HandleFunc("/in/", func(w http.ResponseWriter, r *http.Request) {
					if tc.loop {
						http.Redirect(w, r, r.URL.Path, tc.code)
						return
					}
					http.Redirect(w, r, tc.loc(s), tc.code)
				})
				srvA := httptest.NewUnstartedServer(muxA)
				defer srvA.Close()
				srvB := httptest.NewUnstartedServer(atB)
				defer srvB.Close()
				// The addresses are known before the servers start, so the
				// redirect handler reads them without a race.
				s.a = &url.URL{Scheme: "http", Host: srvA.Listener.Addr().String()}
				s.b = &url.URL{Scheme: "http", Host: srvB.Listener.Addr().String()}
				var rt http.RoundTripper
				if tc.tls {
					s.a.Scheme = "https"
					srvA.StartTLS()
					rt = srvA.Client().Transport
				} else {
					srvA.Start()
				}
				srvB.Start()

				base, landed, spared := srvA.URL+"/in", atA, atB
				switch {
				case tc.code == 0:
					base, landed, spared = srvB.URL+"/direct", atB, atA
				case !tc.atOrigin:
					landed, spared = atB, atA
				}
				h.svc.dmu.Lock()
				h.svc.lastDelivery = nil
				h.svc.dmu.Unlock()
				channel, err := lane.run(t, base, rt)
				rec, recorded := h.svc.lastDeliveryFor(channel)
				if channel != "" && !recorded {
					t.Errorf("no delivery record on channel %q", channel)
				}

				if tc.wantErr == nil {
					if err != nil {
						t.Fatalf("error = %v, want delivered", err)
					}
					heads, reqs := landed.got()
					if len(reqs) != 1 {
						t.Fatalf("landing got %s, want 1 request", landed)
					}
					if !strings.HasPrefix(reqs[0], "POST ") || !strings.Contains(reqs[0], lane.bodyHas) ||
						(lane.cred != "" && heads[0].Get("Authorization") != lane.cred) {
						t.Errorf("landing got %s, want the POST with %s and Authorization %q", landed, lane.bodyHas, lane.cred)
					}
					if channel != "" && !rec.OK {
						t.Errorf("delivery record = %+v, want OK", rec)
					}
					return
				}
				if err == nil {
					t.Fatalf("delivered, want the refusal; the target got %s", landed)
				}
				want := tc.wantErr(s)
				if !strings.Contains(err.Error(), want) {
					t.Errorf("error =\n  %v\nwant it to hold\n  %s", err, want)
				}
				if lane.secret != "" && strings.Contains(err.Error(), lane.secret) {
					t.Errorf("error text carries the credential: %v", err)
				}
				for who, l := range map[string]*redirectLanding{"redirect target": landed, "other server": spared} {
					if _, reqs := l.got(); len(reqs) != 0 {
						t.Errorf("%s got %s, want nothing", who, l)
					}
				}
				if channel != "" && (rec.OK || !strings.Contains(rec.Note, want)) {
					t.Errorf("delivery record = %+v, want a failure holding the refusal", rec)
				}
			})
		}
	}
}
