package spine

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
)

func TestEffectiveFilters(t *testing.T) {
	auditStream := []string{"straza.audit.>"}
	eventsStream := []string{"straza.policy.>", "straza.revocation.>", "straza.apps.>", "straza.identity.>"}

	cases := []struct {
		name   string
		stream []string
		want   []string
		expect []string
	}{
		{"broad filter narrows to the audit stream", auditStream, []string{"straza.>"}, []string{"straza.audit.>"}},
		{"broad filter expands to every events subject", eventsStream, []string{"straza.>"},
			[]string{"straza.policy.>", "straza.revocation.>", "straza.apps.>", "straza.identity.>"}},
		{"narrow filter passes through", eventsStream, []string{"straza.revocation.>"}, []string{"straza.revocation.>"}},
		{"exact subject passes through", auditStream, []string{"straza.audit.tool"}, []string{"straza.audit.tool"}},
		{"disjoint filter drops", auditStream, []string{"straza.identity.>"}, nil},
		{"mixed filters dedupe", eventsStream, []string{"straza.>", "straza.apps.>"},
			[]string{"straza.policy.>", "straza.revocation.>", "straza.apps.>", "straza.identity.>"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := EffectiveFilters(tc.stream, tc.want); !reflect.DeepEqual(got, tc.expect) {
				t.Errorf("EffectiveFilters = %v, want %v", got, tc.expect)
			}
		})
	}
}

// TestWebhookSinkCustomHeaders pins the headers knob: operator
// headers ride every delivery and may OVERRIDE the Content-Type default,
// which lets a webhook sink speak directly to receivers with their own
// contracts (Elasticsearch: Basic auth + application/json; Splunk HEC:
// `Authorization: Splunk <token>`). The Straza headers (subject, sink,
// signature) are not overridable; receivers depend on them for dedupe and
// verification.
func TestWebhookSinkCustomHeaders(t *testing.T) {
	var gotAuth, gotCT, gotSink string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotCT = r.Header.Get("Content-Type")
		gotSink = r.Header.Get("X-Straza-Sink")
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	sink := NewWebhookSink("es", srv.URL, nil, map[string]string{
		"Authorization": "Basic ZWxhc3RpYzpjaGFuZ2VtZQ==",
		"Content-Type":  "application/json",
		"X-Straza-Sink": "spoofed", // must lose to the built-in header
	})
	if err := sink.Deliver(context.Background(), "straza.audit.tool", []byte(`{"id":"e1"}`)); err != nil {
		t.Fatalf("Deliver: %v", err)
	}
	if gotAuth != "Basic ZWxhc3RpYzpjaGFuZ2VtZQ==" {
		t.Errorf("Authorization = %q", gotAuth)
	}
	if gotCT != "application/json" {
		t.Errorf("Content-Type override lost: %q", gotCT)
	}
	if gotSink != "es" {
		t.Errorf("built-in X-Straza-Sink overridden: %q", gotSink)
	}
}

func TestWebhookSinkSignsAndRetriesSemantics(t *testing.T) {
	secret := []byte("hmac-key")
	var fail atomic.Bool
	var gotBody []byte
	var gotSig, gotSubject string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if fail.Load() {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		gotBody, _ = io.ReadAll(r.Body)
		gotSig = r.Header.Get("X-Straza-Signature")
		gotSubject = r.Header.Get("X-Straza-Subject")
		if r.Header.Get("Content-Type") != "application/cloudevents+json" {
			t.Errorf("content type = %q", r.Header.Get("Content-Type"))
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	sink := NewWebhookSink("siem", srv.URL, secret, nil)
	ce := []byte(`{"specversion":"1.0","id":"e1","type":"straza.audit.tool"}`)

	// Outage → error (the runner would NAK; nothing is lost).
	fail.Store(true)
	if err := sink.Deliver(context.Background(), "straza.audit.tool", ce); err == nil {
		t.Fatal("5xx must be a delivery error")
	}
	// Recovery → delivered with a valid HMAC.
	fail.Store(false)
	if err := sink.Deliver(context.Background(), "straza.audit.tool", ce); err != nil {
		t.Fatalf("Deliver: %v", err)
	}
	if string(gotBody) != string(ce) || gotSubject != "straza.audit.tool" {
		t.Errorf("delivered body/subject = %q / %q", gotBody, gotSubject)
	}
	mac := hmac.New(sha256.New, secret)
	mac.Write(ce)
	if want := "sha256=" + hex.EncodeToString(mac.Sum(nil)); gotSig != want {
		t.Errorf("signature = %q, want %q", gotSig, want)
	}
}

func TestFileSinkAppendsJSONL(t *testing.T) {
	path := filepath.Join(t.TempDir(), "siem", "events.jsonl")
	sink := NewFileSink("archive", path)
	for _, id := range []string{"e1", "e2"} {
		ce := []byte(`{"id":"` + id + `"}`)
		if err := sink.Deliver(context.Background(), "straza.audit.tool", ce); err != nil {
			t.Fatalf("Deliver %s: %v", id, err)
		}
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
	if len(lines) != 2 || lines[0] != `{"id":"e1"}` || lines[1] != `{"id":"e2"}` {
		t.Errorf("file content = %q", raw)
	}

	// Close (shutdown) then deliver again: the sink reopens and appends.
	if err := sink.Close(); err != nil {
		t.Fatal(err)
	}
	if err := sink.Deliver(context.Background(), "x", []byte(`{"id":"e3"}`)); err != nil {
		t.Fatalf("Deliver after Close: %v", err)
	}
	raw, _ = os.ReadFile(path)
	if !strings.Contains(string(raw), `"e3"`) {
		t.Errorf("post-reopen content = %q", raw)
	}
	// Release the handle so TempDir cleanup works on Windows.
	if err := sink.Close(); err != nil {
		t.Fatal(err)
	}
}
