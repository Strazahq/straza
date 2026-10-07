package approval

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/strazahq/straza/internal/config"
)

// A push resource PATH is a per-subscription capability token (RFC 8030), so
// no delivery-failure error may echo it: the error reaches logs, the
// lastDelivery state, and the channels admin API. This is the
// secrets-in-logs rule, which endpointNotAllowed already follows for refusals.
func TestPushDeliveryErrorsRedactEndpoint(t *testing.T) {
	p := &pushDelivery{httpc: &http.Client{Timeout: 50 * time.Millisecond}}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	// 192.0.2.0/24 is TEST-NET: fails fast, no network touched.
	_, err := p.postJSON(ctx, "https://192.0.2.9/push/CAPABILITY-SECRET", []byte(`{}`), 60)
	if err == nil {
		t.Fatalf("expected a transport error")
	}
	if s := err.Error(); strings.Contains(s, "CAPABILITY-SECRET") {
		t.Fatalf("postJSON error leaked the capability path: %q", s)
	}
	if s := err.Error(); !strings.Contains(s, "192.0.2.9") {
		t.Fatalf("postJSON error lost the host: %q", s)
	}
}

func TestPushResourceOriginErrorsRedactEndpoint(t *testing.T) {
	// Non-https rejection: the message names scheme://host, never the path.
	_, err := pushResourceOrigin("http://push.example/wp/CAPABILITY-SECRET")
	if err == nil {
		t.Fatalf("expected a scheme rejection")
	}
	if s := err.Error(); strings.Contains(s, "CAPABILITY-SECRET") {
		t.Fatalf("origin rejection leaked the capability path: %q", s)
	}
	if s := err.Error(); !strings.Contains(s, "push.example") {
		t.Fatalf("origin rejection lost the host: %q", s)
	}

	// Unparseable endpoint: url.Parse's echo is redacted too.
	_, err = pushResourceOrigin("https://bad\x7fhost/wp/CAPABILITY-SECRET")
	if err == nil {
		t.Fatalf("expected a parse error")
	}
	if s := err.Error(); strings.Contains(s, "CAPABILITY-SECRET") {
		t.Fatalf("parse error leaked the capability path: %q", s)
	}
}

// The FCM token-exchange error parses the OAuth error fields instead of
// echoing the raw third-party body (up to 64 KiB, verbatim, into logs and
// the channels admin API).
func TestFCMTokenExchangeErrorParsesNotEchoes(t *testing.T) {
	cases := []struct {
		name, body    string
		wantIn, notIn string
	}{
		{"oauth fields parsed", `{"error":"invalid_grant","error_description":"expired assertion"}`,
			"invalid_grant: expired assertion", `{"error"`},
		{"garbage body suppressed", `<html>enormous proxy page SECRET-ECHO</html>`,
			"status 403", "SECRET-ECHO"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusForbidden)
				_, _ = w.Write([]byte(c.body))
			}))
			defer srv.Close()
			saPath, _ := writeServiceAccount(t, srv.URL+"/token")
			sender, err := newFCMSender(
				config.FCMPush{Enabled: true, ServiceAccountFile: saPath, ProjectID: "p"},
				&http.Client{Timeout: 5 * time.Second},
			)
			if err != nil {
				t.Fatalf("newFCMSender: %v", err)
			}
			_, err = sender.bearer(context.Background())
			if err == nil {
				t.Fatalf("expected a token-exchange error")
			}
			if !strings.Contains(err.Error(), c.wantIn) {
				t.Fatalf("error %q missing %q", err.Error(), c.wantIn)
			}
			if strings.Contains(err.Error(), c.notIn) {
				t.Fatalf("error %q must not carry %q", err.Error(), c.notIn)
			}
		})
	}
}
