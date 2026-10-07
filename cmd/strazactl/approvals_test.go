package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/strazahq/straza/internal/ctl"
	"github.com/strazahq/straza/internal/qrterm"
)

// TestHonestyLine pins the two CLI honesty strings (kept in step with
// approval.HonestyLine).
func TestHonestyLine(t *testing.T) {
	if got := honestyLine("call", "abc123abc123"); got != "preview only; this approval covers the exact call (sha256:abc123abc123)" {
		t.Errorf("call: %q", got)
	}
	if got := honestyLine("tool_identity", "abc123abc123"); got != "preview only; this approval covers tool identity (sha256:abc123abc123)" {
		t.Errorf("tool_identity: %q", got)
	}
}

// TestPrintApprovalsWide: the detail view shows the preview and honesty line when
// present, and omits both when there is no preview (the pairing invariant).
func TestPrintApprovalsWide(t *testing.T) {
	var buf bytes.Buffer
	recs := []ctl.ApprovalInfo{
		{
			ID: "a1", State: "pending", Username: "nova", Summary: "mcp.call midpoint:disable_user",
			ArgsPreview: "{\n  \"user\": \"nova\"\n}", ArgsTruncated: true, ArgsBytes: 4096,
			ArgvHashPrefix: "0123456789ab", BindingScope: "tool_identity", Justification: "offboard",
		},
		{ID: "a2", State: "approved", Username: "kim", Summary: "shell.exec: ls"},
	}
	if err := printApprovalsWide(&buf, recs); err != nil {
		t.Fatalf("printApprovalsWide: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "parameters (preview)") || !strings.Contains(out, "nova") {
		t.Errorf("preview not rendered:\n%s", out)
	}
	if !strings.Contains(out, "preview truncated; 4096 bytes") {
		t.Errorf("truncation footnote missing:\n%s", out)
	}
	if !strings.Contains(out, "preview only; this approval covers tool identity (sha256:0123456789ab)") {
		t.Errorf("honesty line missing:\n%s", out)
	}
	// The no-preview record must not emit a params block or honesty line.
	a2 := out[strings.Index(out, "a2"):]
	if strings.Contains(a2, "parameters (preview)") || strings.Contains(a2, "preview only;") {
		t.Errorf("no-preview record leaked preview affordances:\n%s", a2)
	}
}

// The minted enrolment the tests below render: real shapes from
// POST /v1/admin/approvers/enroll-token.
const (
	testEnrollToken = "wXTvB-hm3ltEub_uFdvqwl0yvyV_icGg7vzRvK2yy7M"
	testProjectID   = "prj_0198f4c1-6a20-7c3e-b5d9-4f2a1c7e93b6"
	testSPKIPin     = "sha256/V13tbotp+v8SUsHrXjWzV2jrpKRC5TZeBI/IShtoYSA="
	testQRPayload   = `{"v":1,"servers":["https://192.168.1.42:8443"],"token":"` + testEnrollToken +
		`","pin":"` + testSPKIPin + `","project":{"id":"` + testProjectID + `","name":"straza-93b6"}}`
)

// mintedEnrollToken builds the response with every field populated; the
// per-case tests blank out what they are about.
func mintedEnrollToken() enrollToken {
	var e enrollToken
	e.EnrollToken = testEnrollToken
	e.ExpiresIn = 600
	e.Servers = []string{"https://192.168.1.42:8443"}
	e.TLSSPKIPin = testSPKIPin
	e.QRPayload = testQRPayload
	e.Project.ID = testProjectID
	e.Project.Name = "straza-93b6"
	return e
}

// TestEnrollTokenStdoutIsUnchanged is the compatibility pin for adding the QR:
// stdout is what scripts read, so these bytes are the ones the command shipped
// with, written out in full here rather than derived, so a reformat cannot
// quietly agree with itself.
func TestEnrollTokenStdoutIsUnchanged(t *testing.T) {
	tests := []struct {
		name string
		mut  func(*enrollToken)
		want string
	}{
		{
			name: "pinned listener, project and payload",
			want: "Enroll token (one-time, expires in 600s):\n" +
				"  " + testEnrollToken + "\n" +
				"\n" +
				"Project: straza-93b6 (" + testProjectID + ")\n" +
				"Servers:\n" +
				"  https://192.168.1.42:8443\n" +
				"TLS SPKI pin: " + testSPKIPin + "\n" +
				"\n" +
				"QR payload (encode as a QR for the app):\n" +
				"  " + testQRPayload + "\n",
		},
		{
			// No pin means public-CA TLS through the system trust store, the
			// EXPECTED ingress shape, stated as what happens, not as an assumption.
			// It matches the console's pin card.
			name: "no pin names the system trust store, not an assumption",
			mut:  func(e *enrollToken) { e.TLSSPKIPin = "" },
			want: "Enroll token (one-time, expires in 600s):\n" +
				"  " + testEnrollToken + "\n" +
				"\n" +
				"Project: straza-93b6 (" + testProjectID + ")\n" +
				"Servers:\n" +
				"  https://192.168.1.42:8443\n" +
				"TLS SPKI pin: none (public-CA TLS via the system trust store, the expected ingress shape)\n" +
				"\n" +
				"QR payload (encode as a QR for the app):\n" +
				"  " + testQRPayload + "\n",
		},
		{
			name: "no project id drops the project line entirely",
			mut:  func(e *enrollToken) { e.Project.ID = "" },
			want: "Enroll token (one-time, expires in 600s):\n" +
				"  " + testEnrollToken + "\n" +
				"\n" +
				"Servers:\n" +
				"  https://192.168.1.42:8443\n" +
				"TLS SPKI pin: " + testSPKIPin + "\n" +
				"\n" +
				"QR payload (encode as a QR for the app):\n" +
				"  " + testQRPayload + "\n",
		},
		{
			name: "every server the app should try, one per line",
			mut: func(e *enrollToken) {
				e.Servers = []string{"https://approve.straza.dev", "https://192.168.1.42:8443"}
			},
			want: "Enroll token (one-time, expires in 600s):\n" +
				"  " + testEnrollToken + "\n" +
				"\n" +
				"Project: straza-93b6 (" + testProjectID + ")\n" +
				"Servers:\n" +
				"  https://approve.straza.dev\n" +
				"  https://192.168.1.42:8443\n" +
				"TLS SPKI pin: " + testSPKIPin + "\n" +
				"\n" +
				"QR payload (encode as a QR for the app):\n" +
				"  " + testQRPayload + "\n",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			out := mintedEnrollToken()
			if tc.mut != nil {
				tc.mut(&out)
			}
			var stdout, stderr bytes.Buffer
			printEnrollToken(&stdout, &stderr, out)
			if stdout.String() != tc.want {
				t.Errorf("stdout moved:\ngot:\n%q\nwant:\n%q", stdout.String(), tc.want)
			}
		})
	}
}

// TestEnrollTokenDrawsQROnStderr: the QR is the whole point of the command, so
// it is drawn every run, on stderr, where it cannot reach a pipeline, and
// nowhere else. Its bytes are the renderer's, so a scanner sees the encoder's
// modules and not something this file reformatted.
func TestEnrollTokenDrawsQROnStderr(t *testing.T) {
	code, err := qrterm.Render(testQRPayload)
	if err != nil {
		t.Fatalf("qrterm.Render: %v", err)
	}

	var stdout, stderr bytes.Buffer
	printEnrollToken(&stdout, &stderr, mintedEnrollToken())

	if !strings.Contains(stderr.String(), code.Text) {
		t.Errorf("stderr does not carry the rendered QR:\n%s", stderr.String())
	}
	if !strings.Contains(stderr.String(), "Scan with the Straza approver app") {
		t.Errorf("stderr has no caption telling the operator what to do:\n%s", stderr.String())
	}
	// The width is load-bearing: it is how an operator knows a wrapped QR is a
	// too-narrow window and not a bad token.
	if !strings.Contains(stderr.String(), "65 columns wide") {
		t.Errorf("stderr does not state the QR's width (%d columns):\n%s", code.Columns, stderr.String())
	}
	for _, glyph := range []string{"█", "▀", "▄", "\x1b["} {
		if strings.Contains(stdout.String(), glyph) {
			t.Errorf("stdout leaked QR drawing (%q) into the machine-readable stream:\n%s", glyph, stdout.String())
		}
	}
}

// TestEnrollTokenUndrawableQRIsANote: the token is minted and already burning
// by the time we draw, so nothing about the QR may fail the command; the
// operator gets the payload plus a line saying why there is no picture.
func TestEnrollTokenUndrawableQRIsANote(t *testing.T) {
	tests := []struct {
		name    string
		payload string
		want    string
	}{
		{
			name:    "server returned no payload",
			payload: "",
			want:    "no qr_payload in the server response",
		},
		{
			name:    "payload beyond QR capacity",
			payload: `{"v":1,"token":"` + strings.Repeat("x", 3000) + `"}`,
			want:    "QR not drawn",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			out := mintedEnrollToken()
			out.QRPayload = tc.payload
			var stdout, stderr bytes.Buffer
			printEnrollToken(&stdout, &stderr, out)

			if !strings.Contains(stderr.String(), tc.want) {
				t.Errorf("stderr = %q, want it to mention %q", stderr.String(), tc.want)
			}
			if strings.Contains(stderr.String(), "█") {
				t.Errorf("something was drawn anyway:\n%s", stderr.String())
			}
			if !strings.Contains(stdout.String(), "Enroll token (one-time, expires in 600s):") {
				t.Errorf("the minted token did not survive the failed draw:\n%s", stdout.String())
			}
		})
	}
}

// TestEnrollTokenEndToEnd walks the real cobra tree against a fake strazad: the
// username reaches the admin route as an operand, the fields land on stdout and
// the QR on stderr. The unit tests above pin the bytes; this one pins the wiring.
func TestEnrollTokenEndToEnd(t *testing.T) {
	var gotBody map[string]string
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/checkin", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"session_id":"ses-1","session_token":"stok-2"}`))
	})
	mux.HandleFunc("POST /v1/admin/approvers/enroll-token", func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &gotBody)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"enroll_token":"` + testEnrollToken + `","expires_in":600,` +
			`"servers":["https://192.168.1.42:8443"],"tls_spki_pin":"` + testSPKIPin + `",` +
			`"project":{"id":"` + testProjectID + `","name":"straza-93b6"},` +
			`"qr_payload":` + jsonQuote(testQRPayload) + `}`))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	t.Setenv("STRAZA_SERVER", "")
	stdout, stderr, err := runCLI(t, writeCreds(t, srv.URL), "approvals", "enroll-token", "alice")
	if err != nil {
		t.Fatalf("enroll-token: %v", err)
	}
	if gotBody["username"] != "alice" {
		t.Errorf("server received %v, want the username operand", gotBody)
	}
	for _, want := range []string{testEnrollToken, testSPKIPin, "https://192.168.1.42:8443", testQRPayload} {
		if !strings.Contains(stdout, want) {
			t.Errorf("stdout missing %q:\n%s", want, stdout)
		}
	}
	if !strings.Contains(stderr, "█") || !strings.Contains(stderr, "Scan with the Straza approver app") {
		t.Errorf("no QR on stderr:\n%s", stderr)
	}
}

// TestEnrollTokenHelpNamesBothStreams: the command writes to two streams, so
// its help says which is which; an operator must not have to run it to find
// out where the QR went, or which half `2>/dev/null` throws away.
func TestEnrollTokenHelpNamesBothStreams(t *testing.T) {
	t.Setenv("STRAZA_SERVER", "")
	stdout, _, err := runCLI(t, noCreds(t), "approvals", "enroll-token", "--help")
	if err != nil {
		t.Fatalf("help: %v", err)
	}
	for _, want := range []string{"STDOUT", "STDERR", "2>/dev/null"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("help does not mention %q:\n%s", want, stdout)
		}
	}
}

// jsonQuote JSON-quotes a payload for the fixture body above.
func jsonQuote(s string) string {
	raw, err := json.Marshal(s)
	if err != nil {
		panic(err)
	}
	return string(raw)
}
